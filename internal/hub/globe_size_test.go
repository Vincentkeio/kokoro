package hub

// 地球画布的尺寸契约。
//
// 2026-10-10 boss 报过："网页往下滚时，地球的下半部分会被截断。"
// 成因：globe.js 往 canvas 上写了 style.width / style.height。容器一旦变矮
// （窗口改小、--band-h 里的 vh 变化、字体加载把卡片撑高…）而 window 没发
// resize，画布就停在旧高度上，多出来的部分被 .globe 的 overflow:hidden
// 裁掉。**不报错**，只能靠肉眼发现，所以钉进测试。
//
// 修法是两条，缺一不可：
//  1. 显示尺寸交给 CSS（.globe canvas 撑满容器）——保证**永不裁切**；
//  2. JS 只写 canvas.width/height 当绘制分辨率，并用 ResizeObserver
//     盯住容器 —— 保证分辨率跟得上（否则画面会被拉伸发糊）。
//
// 谁把 style 写回去，或者把 ResizeObserver 删掉，这里立刻红。

import (
	"io/fs"
	"strings"
	"testing"
)

// stripJSComments 去掉 JS 的块注释与「整行」行注释。
//
// 只剥整行注释（行首是 //），不碰行尾注释和字符串里的 "//" —— 后者
// 会误伤 URL（"https://…"），而这里的目的是排除"注释里提到的反模式"，
// 不是做真正的语法解析。
func stripJSComments(src string) string {
	for {
		a := strings.Index(src, "/*")
		if a < 0 {
			break
		}
		b := strings.Index(src[a:], "*/")
		if b < 0 {
			break
		}
		src = src[:a] + src[a+b+2:]
	}
	var out []string
	for _, line := range strings.Split(src, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "//") {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

func TestGlobeCanvasSizeHandedToCSS(t *testing.T) {
	jsRaw, err := fs.ReadFile(staticFS, "static/globe.js")
	if err != nil {
		t.Fatalf("读 globe.js 失败: %v", err)
	}
	js := string(jsRaw)
	cssRaw, err := fs.ReadFile(staticFS, "static/style.css")
	if err != nil {
		t.Fatalf("读样式表失败: %v", err)
	}
	css := string(cssRaw)

	// 1) JS 不许再给画布写 style 尺寸。
	//    先剥掉注释再看：_resize 里那段注释**故意**提到了这两个字符串
	//    （说明为什么不能这么写），不该被当成"又写回去了"。
	code := stripJSComments(js)
	for _, bad := range []string{"cv.style.width", "cv.style.height"} {
		if strings.Contains(code, bad) {
			t.Errorf("globe.js 里出现 %s —— 画布的显示尺寸必须交给 CSS。"+
				"写死 style 之后，容器变矮时画布不会跟着缩，下半部分会被 "+
				"overflow:hidden 裁掉", bad)
		}
	}

	// 2) 必须用 ResizeObserver 盯容器：只监听 window resize 漏得太多，
	//    「容器变了但窗口没变」正是这个 bug 的触发条件。
	if !strings.Contains(code, "ResizeObserver") {
		t.Error("globe.js 缺少 ResizeObserver —— 容器尺寸变化时绘制分辨率不会跟上")
	}
	if !strings.Contains(code, "_ro.disconnect()") {
		t.Error("globe.js 的 destroy 没有断开 ResizeObserver，会泄漏")
	}

	// 3) CSS 必须让画布撑满容器。
	i := strings.Index(css, ".globe canvas")
	if i < 0 {
		t.Fatal("style.css 里找不到 .globe canvas 规则")
	}
	rule := css[i:min(i+240, len(css))]
	if !strings.Contains(rule, "width: 100%") || !strings.Contains(rule, "height: 100%") {
		t.Errorf(".globe canvas 必须 width/height: 100%%（实际规则片段：%q）", rule)
	}
}
