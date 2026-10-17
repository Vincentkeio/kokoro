package hub

// 站点壁纸：按主题分开的上传、填 URL、停用，以及注入的那段 CSS。

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Vincentkeio/kokoro/internal/store"
)

// 两个真实存在的内置主题，用来验证"壁纸按主题存、互不串台"。
const (
	wpThemeA = "kokoro.simple"
	wpThemeB = "kokoro.paper"
)

// TestSafeWallpaperURL 钉住 URL 校验的两个方向：
// 好的要放行，能**跳出 url()** 的一律拒绝。
//
// 那段 CSS 是直接拼进 <style> 的，一个引号就能把声明拆开、
// 往页面里塞任意规则 —— 而它只该是张图。
func TestSafeWallpaperURL(t *testing.T) {
	ok := []string{
		"https://example.com/bg.jpg",
		"http://example.com/a/b.png?x=1&y=2",
		"https://cdn.example.com/%E5%9B%BE.webp",
	}
	for _, u := range ok {
		if !safeWallpaperURL(u) {
			t.Errorf("%q 应该放行", u)
		}
	}

	bad := []string{
		"", "   ",
		"javascript:alert(1)",
		"data:image/svg+xml;base64,AAAA",
		"file:///etc/passwd",
		"//example.com/bg.jpg",                                  // 协议相对，看不出走 http 还是别的
		`https://x.com/a.jpg");}` + "\n" + `body{display:none}`, // 经典跳出
		"https://x.com/a b.jpg",                                 // 空格
		`https://x.com/a"b.jpg`,                                 // 引号
		"https://x.com/a(b).jpg",                                // 圆括号
		"https://x.com/a;b.jpg",                                 // 分号
		"https://x.com/" + strings.Repeat("a", 600),             // 超长
	}
	for _, u := range bad {
		if safeWallpaperURL(u) {
			t.Errorf("%q 应该拒绝", u)
		}
	}
}

// TestSafeThemeFileID 主题 ID 会拼进文件名，所以必须洗成安全形状。
func TestSafeThemeFileID(t *testing.T) {
	for _, ok := range []string{"kokoro.simple", "acme.foo-bar_1", "abcd"} {
		if safeThemeFileID(ok) == "" {
			t.Errorf("%q 应该可用作文件名", ok)
		}
	}
	for _, bad := range []string{"", ".", "..", "../../etc/passwd", strings.Repeat("a", 70)} {
		if got := safeThemeFileID(bad); got != "" && strings.ContainsAny(got, "/\\") {
			t.Errorf("%q 洗出来的 %q 仍然危险", bad, got)
		}
	}
	if safeThemeFileID("../../etc/passwd") == "../../etc/passwd" {
		t.Error("路径分隔符必须被换掉")
	}
}

// wallpaperCookies 拿到一个**可重复使用**的后台会话。
//
// ⚠️ 不能用 loginAsAdmin：它内部走 ensureAdmin()，而那个函数在"已经有密码"
// 时返回的 pass 是**空串**（见 auth.go）—— 同一个 Hub 上登录第二次必然失败。
// loginCookie 用固定的账号密码，调多少次都能过。
func wallpaperCookies(t *testing.T, h *Hub, st *store.Store) []*http.Cookie {
	t.Helper()
	return []*http.Cookie{loginCookie(t, h, st)}
}

// wallpaperPost 构造一个带会话的后台壁纸请求。
//
// fields 里请带上 action 与 theme —— 表单就是这么提交的。
func wallpaperPost(t *testing.T, h *Hub, cookies []*http.Cookie, fields map[string]string, fileName string, fileBody []byte) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for k, v := range fields {
		_ = mw.WriteField(k, v)
	}
	if fileName != "" {
		fw, err := mw.CreateFormFile("wallpaper", fileName)
		if err != nil {
			t.Fatalf("构造上传失败: %v", err)
		}
		_, _ = fw.Write(fileBody)
	}
	_ = mw.Close()

	req := httptest.NewRequest(http.MethodPost, "/admin/wallpaper", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.mux.ServeHTTP(rec, req)
	return rec
}

// TestWallpaperURLRoundTrip 填 URL → 存下来 → 注入 CSS 里能看到它。
func TestWallpaperURLRoundTrip(t *testing.T) {
	h, st := newThemeTestHub(t)
	ck := wallpaperCookies(t, h, st)

	rec := wallpaperPost(t, h, ck, map[string]string{
		"action": "url", "theme": wpThemeA,
		"wallpaper_url": "https://cdn.example.com/bg.jpg",
	}, "", nil)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("状态码 = %d，应为 303", rec.Code)
	}

	css := h.wallpaperCSS(wpThemeA)
	if !strings.Contains(css, `url("https://cdn.example.com/bg.jpg")`) {
		t.Fatalf("注入的 CSS 里没有那个地址：%q", css)
	}
	// 顺手把"整屏铺满"也带上：这三个变量在主题 tokens 里写不出有效值
	// （校验按长度类型走，不认 cover / no-repeat），只能由这里补。
	for _, want := range []string{"--kokoro-bg-size:cover", "--kokoro-bg-repeat:no-repeat", "--kokoro-bg-attachment:fixed"} {
		if !strings.Contains(css, want) {
			t.Errorf("注入的 CSS 缺少 %q：%q", want, css)
		}
	}

	// 危险地址一律拒绝，且**不能脏了设置**。
	rec = wallpaperPost(t, h, ck, map[string]string{
		"action": "url", "theme": wpThemeA,
		"wallpaper_url": `https://x.com/a.jpg");}body{display:none}`,
	}, "", nil)
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "wperr=url") {
		t.Errorf("非法地址应带着 wperr=url 退回，实际 Location=%q", loc)
	}
	if css := h.wallpaperCSS(wpThemeA); !strings.Contains(css, "cdn.example.com") {
		t.Errorf("被拒的地址不该覆盖掉原来的设置：%q", css)
	}
}

// TestWallpaperIsPerTheme 壁纸按主题分开：给 A 设的不影响 B，各取各的。
func TestWallpaperIsPerTheme(t *testing.T) {
	h, st := newThemeTestHub(t)
	ck := wallpaperCookies(t, h, st)

	wallpaperPost(t, h, ck, map[string]string{
		"action": "url", "theme": wpThemeA,
		"wallpaper_url": "https://cdn.example.com/a.jpg",
	}, "", nil)
	wallpaperPost(t, h, ck, map[string]string{
		"action": "url", "theme": wpThemeB,
		"wallpaper_url": "https://cdn.example.com/b.jpg",
	}, "", nil)

	if css := h.wallpaperCSS(wpThemeA); !strings.Contains(css, "a.jpg") || strings.Contains(css, "b.jpg") {
		t.Errorf("主题 A 的壁纸串台了：%q", css)
	}
	if css := h.wallpaperCSS(wpThemeB); !strings.Contains(css, "b.jpg") || strings.Contains(css, "a.jpg") {
		t.Errorf("主题 B 的壁纸串台了：%q", css)
	}

	// 停用 A 之后：A 回到"没设壁纸"（页面就会用 A 主题自带的图），B 纹丝不动。
	wallpaperPost(t, h, ck, map[string]string{"action": "clear", "theme": wpThemeA}, "", nil)
	if css := h.wallpaperCSS(wpThemeA); css != "" {
		t.Errorf("停用后主题 A 不该再注入壁纸：%q", css)
	}
	if css := h.wallpaperCSS(wpThemeB); !strings.Contains(css, "b.jpg") {
		t.Errorf("停用 A 不该动到 B：%q", css)
	}
}

// TestWallpaperUploadServesFile 上传本地图 → 存成 wallpaper-<主题>.ext → 由 /wallpaper/<主题> 取回。
func TestWallpaperUploadServesFile(t *testing.T) {
	h, st := newThemeTestHub(t)
	ck := wallpaperCookies(t, h, st)

	png := []byte("\x89PNG\r\n\x1a\nfake-image-bytes")
	rec := wallpaperPost(t, h, ck, map[string]string{
		"action": "upload", "theme": wpThemeA,
	}, "bg.png", png)
	if loc := rec.Header().Get("Location"); strings.Contains(loc, "wperr=") {
		t.Fatalf("上传不该报错，实际 Location=%q", loc)
	}
	if kind := h.wallpaperKind(wpThemeA); kind != "local" {
		t.Fatalf("上传后 kind = %q，应为 local", kind)
	}
	css := h.wallpaperCSS(wpThemeA)
	if !strings.Contains(css, "/wallpaper/"+wpThemeA) {
		t.Fatalf("本地壁纸的地址不对：%q", css)
	}
	// 版本参数要在，否则换图后访客 24 小时内还看旧图。
	if !strings.Contains(css, "?v=") {
		t.Errorf("本地壁纸地址应带版本参数绕缓存：%q", css)
	}

	get := httptest.NewRequest(http.MethodGet, "/wallpaper/"+wpThemeA, nil)
	w := httptest.NewRecorder()
	h.mux.ServeHTTP(w, get)
	if w.Code != http.StatusOK {
		t.Fatalf("取回本地壁纸 = %d，应为 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "image/png" {
		t.Errorf("Content-Type = %q", ct)
	}
	if !bytes.Equal(w.Body.Bytes(), png) {
		t.Error("取回的字节和上传的不一致")
	}

	// 另一个主题没设壁纸：它的地址必须 404，不能把 A 的图吐出去。
	other := httptest.NewRequest(http.MethodGet, "/wallpaper/"+wpThemeB, nil)
	wOther := httptest.NewRecorder()
	h.mux.ServeHTTP(wOther, other)
	if wOther.Code != http.StatusNotFound {
		t.Errorf("没设壁纸的主题应 404，实际 %d", wOther.Code)
	}
}

// TestWallpaperRejectsUnknownTheme 主题字段会被拼进设置名和文件名，
// 所以只认已注册的主题 —— 否则等于让人写任意设置键。
func TestWallpaperRejectsUnknownTheme(t *testing.T) {
	h, st := newThemeTestHub(t)
	ck := wallpaperCookies(t, h, st)

	rec := wallpaperPost(t, h, ck, map[string]string{
		"action": "url", "theme": "../../evil",
		"wallpaper_url": "https://cdn.example.com/bg.jpg",
	}, "", nil)
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "wperr=theme") {
		t.Fatalf("未知主题应带 wperr=theme 退回，实际 Location=%q", loc)
	}
	if v, _ := st.GetSetting(wallpaperSettingKey(settingWallpaperKind, "../../evil")); v != "" {
		t.Errorf("不该为未知主题写入设置，实际 %q", v)
	}
}

// TestWallpaperClearClearsCSS 停用之后不该再注入任何东西。
func TestWallpaperClearClearsCSS(t *testing.T) {
	h, st := newThemeTestHub(t)
	ck := wallpaperCookies(t, h, st)
	wallpaperPost(t, h, ck, map[string]string{
		"action": "url", "theme": wpThemeA,
		"wallpaper_url": "https://cdn.example.com/bg.jpg",
	}, "", nil)
	if h.wallpaperCSS(wpThemeA) == "" {
		t.Fatal("前置条件不成立：设置没生效")
	}
	wallpaperPost(t, h, ck, map[string]string{"action": "clear", "theme": wpThemeA}, "", nil)
	if got := h.wallpaperCSS(wpThemeA); got != "" {
		t.Errorf("停用后仍注入了 %q", got)
	}
}

// TestWallpaperRejectsSVG svg 是唯一能在"图片"位置执行脚本的格式，必须拒。
func TestWallpaperRejectsSVG(t *testing.T) {
	h, st := newThemeTestHub(t)
	rec := wallpaperPost(t, h, wallpaperCookies(t, h, st), map[string]string{
		"action": "upload", "theme": wpThemeA,
	}, "evil.svg", []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`))
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "wperr=type") {
		t.Errorf("svg 应被拒（wperr=type），实际 Location=%q", loc)
	}
	if h.wallpaperKind(wpThemeA) != "" {
		t.Error("上传失败不该把站点标成已设壁纸")
	}
}

// TestWallpaperRouteNeedsAuth /wallpaper/<主题> 是公开的（访客要加载它），
// 但那个主题没设壁纸时必须 404 —— 不能让人靠它探到"上传过东西"。
func TestWallpaperRouteNeedsAuth(t *testing.T) {
	h, _ := newThemeTestHub(t)
	for _, path := range []string{"/wallpaper", "/wallpaper/" + wpThemeA} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		h.mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s 没设壁纸时应 404，实际 %d", path, rec.Code)
		}
	}
}

// TestWallpaperPanelOnThemePage 主题页要有壁纸面板（含本地上传与填 URL 两条路），
// 且两个表单都要带上"当前主题"——否则服务端不知道该改哪一套。
func TestWallpaperPanelOnThemePage(t *testing.T) {
	h, st := newThemeTestHub(t)
	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	req.AddCookie(loginCookie(t, h, st))
	rec := httptest.NewRecorder()
	h.mux.ServeHTTP(rec, req)
	body := rec.Body.String()

	if !strings.Contains(body, `action="/admin/wallpaper"`) {
		t.Fatal("主题页没有壁纸表单")
	}
	if !strings.Contains(body, `enctype="multipart/form-data"`) {
		t.Error("壁纸面板缺少本地上传（multipart 表单）")
	}
	if !strings.Contains(body, `name="wallpaper_url"`) {
		t.Error("壁纸面板缺少填 URL 的输入框")
	}
	// 上传与填地址两个表单都要带上"当前主题"，否则服务端不知道该改哪一套。
	// （"停用"那个表单只在已设壁纸时才渲染，所以这里不数它。）
	if n := strings.Count(body, `name="theme"`); n < 2 {
		t.Errorf("壁纸表单都应带上主题字段，实际只有 %d 处", n)
	}
	if !strings.Contains(body, `value="`+wpThemeA+`"`) {
		t.Errorf("主题字段应填当前启用主题 %s", wpThemeA)
	}
}

// TestWallpaperURLFormIsUrlencoded 盯的是一个只有真实浏览器才暴露的坑。
//
// 填地址那个表单没有 enctype，浏览器按 application/x-www-form-urlencoded 发；
// 端点以前无条件 ParseMultipartForm，ErrNotMultipart 被当成"图太大"，
// 于是"填地址"永远存不进去、还弹一条牛头不对马嘴的提示。
//
// 注意：本文件里的 wallpaperPost 桩**总是**发 multipart，所以它测不出这个 bug ——
// 这条测试必须按真实表单的编码发请求。
func TestWallpaperURLFormIsUrlencoded(t *testing.T) {
	h, st := newThemeTestHub(t)
	ck := wallpaperCookies(t, h, st)

	// 手写编码，和 <form> 实际发出的 body 一致。
	body := "action=url&theme=" + wpThemeA + "&wallpaper_url=https%3A%2F%2Fcdn.example.com%2Fbg.jpg"
	req := httptest.NewRequest(http.MethodPost, "/admin/wallpaper", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for _, c := range ck {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.mux.ServeHTTP(rec, req)

	if loc := rec.Header().Get("Location"); strings.Contains(loc, "wperr=") {
		t.Fatalf("urlencoded 填地址不该报错，实际 Location=%q", loc)
	}
	if kind := h.wallpaperKind(wpThemeA); kind != "url" {
		t.Fatalf("URL 壁纸没存下来，kind=%q", kind)
	}
	if css := h.wallpaperCSS(wpThemeA); !strings.Contains(css, "cdn.example.com") {
		t.Fatalf("注入的 CSS 里没有那个地址：%q", css)
	}
}
