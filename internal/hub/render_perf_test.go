package hub

// 首页卡顿的回归网。
//
// 2026-10-19 线上实测：kokoro.funnyai.wiki（beta.19）在 1440×900 的桌面上
// 空闲时只有 2~4 帧/秒，而同一个浏览器打开普通网页是满帧 60。用页面内探针
// 逐层排除后定位到两件事叠在一起：
//
//  1. style.css 把桌面最小宽度规则写成了 `html:has(body[data-k-…])`。
//     :has() 一旦挂在根元素上，**文档里任何一次 DOM 变更**都会让这条规则
//     失效，从而触发一次全文档样式重算 —— 地球每帧要插拔十几个取色探针，
//     SSE 又不停改数值，于是每帧都在重算整篇文档。
//     同一页面只把这三条换成属性选择器，地球每帧的重算时间从 249ms 掉到 51ms。
//
//  2. globe.js 的 `_colors()` 每调用一次就要插一个探针元素 + getComputedStyle，
//     而它在按节点、按航线的循环里被反复调用（实测最多 92 次/帧）→ 1394ms/帧。
//     一帧只算一次后回到 67 帧。
//
// 两条都"看不出错"：页面照样能开，只是慢得没法用。所以在这里钉住口径，
// 让后来者改这两处时必须显式推倒理由。

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

var cssCommentRe = regexp.MustCompile(`(?s)/\*.*?\*/`)

// TestStyleAvoidsRootHasSelector 钉住"桌面最小宽度"不能用根元素上的 :has()。
//
// 允许的是 `html[data-k-card-view=…]` 这类**自己身上**的属性选择器：
// 属性由模板渲染在 <html> 上，改属性只影响根元素自身，不会因为子树里
// 冒出一个 <span> 就全文档重算。
func TestStyleAvoidsRootHasSelector(t *testing.T) {
	raw, err := fs.ReadFile(staticFS, "static/style.css")
	if err != nil {
		t.Fatal(err)
	}
	// 注释里当然可以提这件事（本改动就是靠注释解释理由的），
	// 所以先剥掉 /* … */ 再找。
	css := cssCommentRe.ReplaceAllString(string(raw), "")

	if strings.Contains(css, "html:has(") {
		t.Error("style.css 又在 <html> 上用了 :has()：文档里任何一次 DOM 变更都会触发全文档样式重算，" +
			"实测把首页拖到 3 帧/秒。请改成 html[自己的属性]（见本文件顶部说明）。")
	}

	// 规则本身也要在：漏掉这几条，桌面端 4/5 列排版会被容器查询挤回窄布局。
	for _, want := range []string{
		`html[data-k-card-view="card"] { min-width: 1800px; }`,
		`html[data-k-card-view="card"][data-k-card-columns="4"] { min-width: 2400px; }`,
		`html[data-k-card-view="card"][data-k-card-columns="5"] { min-width: 3000px; }`,
	} {
		if !strings.Contains(css, want) {
			t.Errorf("style.css 缺少桌面最小宽度规则：%s", want)
		}
	}
}

// TestHomePageCarriesRootLayoutMarkers 保证上面那几条 CSS 真的能命中：
// 标记必须由模板渲染在 <html> 上（不能只放 <body>），否则桌面端宽度规则失效。
func TestHomePageCarriesRootLayoutMarkers(t *testing.T) {
	h, _ := newThemeTestHub(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("首页 = %d", rec.Code)
	}
	page := rec.Body.String()
	start := strings.Index(page, "<html")
	if start < 0 {
		t.Fatal("首页没有 <html> 标签")
	}
	rest := page[start:]
	root := rest[:strings.Index(rest, ">")+1]
	if !strings.Contains(root, `data-k-card-view="card"`) {
		t.Fatalf("首页 <html> 上缺少 data-k-card-view：%s", root)
	}
	if !strings.Contains(root, `data-k-card-columns=`) {
		t.Fatalf("首页 <html> 上缺少 data-k-card-columns：%s", root)
	}
	// 访客改排列时 app.js 必须同步写根元素；只写 <body> 的话，
	// 容器查询看到的仍是旧宽度，卡片会被挤成一列。
	js, err := fs.ReadFile(staticFS, "static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(js), "document.documentElement.setAttribute('data-k-card-columns'") {
		t.Error("app.js 的 applyCardColumns 没有同步写 <html> 上的 data-k-card-columns")
	}
}

// TestGlobeCachesThemeColorsPerFrame 钉住地球的取色缓存。
//
// 地球一帧要画上百个节点/航线，取色函数在循环里被反复调用；每次调用都会
// 插一个探针元素再读 getComputedStyle，也就是一次强制样式重算。
// 缓存按帧失效：主题色只在切主题（整页重载）时才变，一帧的粒度足够，
// 但必须在 _render / _step 开头清掉，否则就真的变成"永远用旧颜色"。
func TestGlobeCachesThemeColorsPerFrame(t *testing.T) {
	raw, err := fs.ReadFile(staticFS, "static/globe.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(raw)

	if !strings.Contains(js, "if (this._frameColors) return this._frameColors;") {
		t.Error("globe.js 的 _colors 没有按帧复用结果：每次取色都是一次全文档样式重算，" +
			"实测上百次/帧、单帧 1.4 秒")
	}
	render := js[strings.Index(js, "KokoroGlobe.prototype._render = function"):]
	render = render[:strings.Index(render, "\n  };")]
	if !strings.Contains(render, "this._frameColors = null;") {
		t.Error("_render 没有清掉取色缓存：换主题后地球会一直用旧颜色")
	}
	step := js[strings.Index(js, "KokoroGlobe.prototype._step = function"):]
	step = step[:strings.Index(step, "\n  };")]
	if !strings.Contains(step, "this._frameColors = null;") {
		t.Error("_step 没有清掉取色缓存：动画帧会一直复用同一份颜色")
	}
}
