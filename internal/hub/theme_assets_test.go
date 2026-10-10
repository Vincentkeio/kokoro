package hub

// /_theme-assets/ 路由的回归测试。
//
// 这条路由是「主题里的背景图 / 字体到底能不能显示」的唯一通道，
// 同时它也是主题包资源第一次暴露给未登录访客的出口——所以两组断言都要有：
// 一组保证「能取到」，一组保证「取不到不该取的」。

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Vincentkeio/kokoro/internal/theme"
)

func TestThemeAssetsServesWhitelistedTypes(t *testing.T) {
	dir := t.TempDir()
	st := newStoreOn(t, dir)
	h := newHubOn(t, st, dir)
	cookies := loginAsAdmin(t, h)
	uploadBundle(t, h, cookies, makeBundle(t, "acme.assets", map[string][]byte{
		"bg.png":        []byte("PNGDATA"),
		"fonts/x.woff2": []byte("WOFFDATA"),
	}))

	cases := []struct {
		path string
		ct   string
		body string
	}{
		{"/_theme-assets/acme.assets/bg.png", "image/png", "PNGDATA"},
		{"/_theme-assets/acme.assets/fonts/x.woff2", "font/woff2", "WOFFDATA"},
	}
	for _, c := range cases {
		req := httptest.NewRequest(http.MethodGet, c.path, nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Errorf("%s 应 200，实际 %d", c.path, w.Code)
			continue
		}
		if got := w.Header().Get("Content-Type"); got != c.ct {
			t.Errorf("%s Content-Type = %q，应为 %q", c.path, got, c.ct)
		}
		if got := w.Body.String(); got != c.body {
			t.Errorf("%s 内容 = %q", c.path, got)
		}
		// nosniff 是这条路由的硬要求：类型靠扩展名判断，浏览器不许再猜。
		if got := w.Header().Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("%s 缺 nosniff，实际 %q", c.path, got)
		}
	}
}

// SVG 必须被拒：它是唯一能在"图片"位置执行脚本的格式，
// 而主题包来自第三方。这条一旦放松，等于给主题作者一个 XSS 后门。
func TestThemeAssetsRejectsSVG(t *testing.T) {
	dir := t.TempDir()
	st := newStoreOn(t, dir)
	h := newHubOn(t, st, dir)
	cookies := loginAsAdmin(t, h)
	uploadBundle(t, h, cookies, makeBundle(t, "acme.svg", map[string][]byte{
		"logo.svg": []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`),
	}))

	for _, p := range []string{
		"/_theme-assets/acme.svg/logo.svg",
		"/_theme-assets/acme.svg/logo.SVG",
	} {
		req := httptest.NewRequest(http.MethodGet, p, nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != http.StatusNotFound {
			t.Errorf("%s 应 404（SVG 不允许下发），实际 %d", p, w.Code)
		}
	}
}

func TestThemeAssetsUnknownThemeOrFile(t *testing.T) {
	dir := t.TempDir()
	st := newStoreOn(t, dir)
	h := newHubOn(t, st, dir)
	cookies := loginAsAdmin(t, h)
	uploadBundle(t, h, cookies, makeBundle(t, "acme.present", map[string][]byte{
		"bg.png": []byte("X"),
	}))

	for _, p := range []string{
		"/_theme-assets/no.such.theme/bg.png", // 主题不存在
		"/_theme-assets/acme.present/none.png", // 资源不存在
		"/_theme-assets/acme.present/",         // 缺资源名
		"/_theme-assets/bg.png",                // 缺主题 ID
		"/_theme-assets/kokoro.daylight/x.png", // 内置主题没有包
	} {
		req := httptest.NewRequest(http.MethodGet, p, nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != http.StatusNotFound {
			t.Errorf("%s 应 404，实际 %d", p, w.Code)
		}
	}
}

// 目录回退（zip-slip）在 HTTP 层也必须挡掉。
func TestThemeAssetsPathTraversal(t *testing.T) {
	dir := t.TempDir()
	st := newStoreOn(t, dir)
	h := newHubOn(t, st, dir)
	cookies := loginAsAdmin(t, h)
	uploadBundle(t, h, cookies, makeBundle(t, "acme.trav", map[string][]byte{
		"bg.png": []byte("X"),
	}))

	for _, p := range []string{
		"/_theme-assets/acme.trav/../theme.json",
		"/_theme-assets/acme.trav/%2e%2e/theme.json",
		"/_theme-assets/acme.trav/a/../../theme.json",
	} {
		req := httptest.NewRequest(http.MethodGet, p, nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		// ServeMux 可能先 301 清理路径，也可能直接落到 handler 判 404；
		// 无论哪条，都不能 200 地把清单吐出来。
		if w.Code == http.StatusOK {
			t.Errorf("%s 不应成功（实际吐了 %d 字节）", p, w.Body.Len())
		}
	}
}

// SafeAssetPath 是这条路由的第一道闸，单独钉住它的判定。
func TestSafeAssetPath(t *testing.T) {
	ok := map[string]string{
		"bg.png":        "assets/bg.png",
		"fonts/x.woff2": "assets/fonts/x.woff2",
	}
	for in, want := range ok {
		got, err := theme.SafeAssetPath(in)
		if err != nil {
			t.Errorf("%q 应通过，实际 %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("%q → %q，应为 %q", in, got, want)
		}
	}
	bad := []string{"", "..", "../x.png", "a/../../x.png", "/etc/passwd",
		"a\\b.png", "a:b.png", "./a.png", "a//b.png"}
	for _, in := range bad {
		if got, err := theme.SafeAssetPath(in); err == nil {
			t.Errorf("%q 应被拒绝，实际得到 %q", in, got)
		}
	}
}

// 重启后资源缓存必须重建——否则线上表现为"重启一次，所有背景图消失"。
func TestThemeAssetsSurviveRestart(t *testing.T) {
	dir := t.TempDir()
	st := newStoreOn(t, dir)
	h := newHubOn(t, st, dir)
	cookies := loginAsAdmin(t, h)
	uploadBundle(t, h, cookies, makeBundle(t, "acme.reload", map[string][]byte{
		"bg.png": []byte("RELOAD"),
	}))

	h2 := newHubOn(t, st, dir)
	req := httptest.NewRequest(http.MethodGet, "/_theme-assets/acme.reload/bg.png", nil)
	w := httptest.NewRecorder()
	h2.ServeHTTP(w, req)
	if w.Code != http.StatusOK || w.Body.String() != "RELOAD" {
		t.Fatalf("重启后取资源失败：%d %q", w.Code, w.Body.String())
	}
}

// 删除主题后资源不能再被取到（内存缓存也要清）。
func TestThemeAssetsDroppedOnDelete(t *testing.T) {
	dir := t.TempDir()
	st := newStoreOn(t, dir)
	h := newHubOn(t, st, dir)
	cookies := loginAsAdmin(t, h)
	uploadBundle(t, h, cookies, makeBundle(t, "acme.gone", map[string][]byte{
		"bg.png": []byte("X"),
	}))

	form := strings.NewReader("id=acme.gone")
	req := withCookies(httptest.NewRequest(http.MethodPost, "/admin/themes/delete", form), cookies)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	h.ServeHTTP(httptest.NewRecorder(), req)

	get := httptest.NewRequest(http.MethodGet, "/_theme-assets/acme.gone/bg.png", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, get)
	if w.Code != http.StatusNotFound {
		t.Errorf("删除后资源应 404，实际 %d", w.Code)
	}
}

// 渲染 tokens 时要把包内相对路径补上主题 ID，
// 否则作者写 url(/_theme-assets/bg.png) 会请求到不存在的地址。
func TestThemeAssetURLRewrittenWithID(t *testing.T) {
	h, _ := newThemeTestHub(t)
	importTestTheme(t, h, "peer.bg",
		`{"schemaVersion":1,"id":"peer.bg","name":"带背景","version":"1.0.0",
		"author":"t","tokensSchemaVersion":2,"layoutSchemaVersion":1,
		"tokens":{"--kokoro-color-primary":"#123456",
		"--kokoro-bg-image":"url(/_theme-assets/bg.png)"},
		"layout":{}}`)
	h.setActiveTheme("peer.bg")

	body := renderBody(t, h, httptest.NewRequest(http.MethodGet, "/", nil))
	if !strings.Contains(body, "/_theme-assets/peer.bg/bg.png") {
		t.Error("tokens 里的包内路径没有被补上主题 ID")
	}
	// 已经写全 ID 的不该被拼成 <id>/<id>/。
	importTestTheme(t, h, "peer.bg2",
		`{"schemaVersion":1,"id":"peer.bg2","name":"带背景2","version":"1.0.0",
		"author":"t","tokensSchemaVersion":2,"layoutSchemaVersion":1,
		"tokens":{"--kokoro-color-primary":"#123456",
		"--kokoro-bg-image":"url(/_theme-assets/peer.bg2/bg.png)"},
		"layout":{}}`)
	h.setActiveTheme("peer.bg2")
	body = renderBody(t, h, httptest.NewRequest(http.MethodGet, "/", nil))
	if strings.Contains(body, "/_theme-assets/peer.bg2/peer.bg2/") {
		t.Error("已带 ID 的路径被重复拼接")
	}
}
