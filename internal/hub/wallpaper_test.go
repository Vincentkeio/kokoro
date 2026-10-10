package hub

// 站点壁纸：上传、填 URL、清除，以及注入的那段 CSS。

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Vincentkeio/kokoro/internal/store"
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
		"//example.com/bg.jpg",              // 协议相对，看不出走 http 还是别的
		`https://x.com/a.jpg");}`+"\n"+`body{display:none}`, // 经典跳出
		"https://x.com/a b.jpg",             // 空格
		`https://x.com/a"b.jpg`,             // 引号
		"https://x.com/a(b).jpg",            // 圆括号
		"https://x.com/a;b.jpg",             // 分号
		"https://x.com/" + strings.Repeat("a", 600), // 超长
	}
	for _, u := range bad {
		if safeWallpaperURL(u) {
			t.Errorf("%q 应该拒绝", u)
		}
	}
}

// wallpaperCookies 拿到一个**可重复使用**的后台会话。
//
// ⚠️ 不能用 loginAsAdmin：它内部走 ensureAdmin()，而那个函数在"已经有密码"
// 时返回的 pass 是**空串**（见 auth.go）—— 同一个 Hub 上登录第二次必然失败，
// 报"登录应 303，实际 200"。loginCookie 用固定的账号密码，
// 调多少次都能过。
func wallpaperCookies(t *testing.T, h *Hub, st *store.Store) []*http.Cookie {
	t.Helper()
	return []*http.Cookie{loginCookie(t, h, st)}
}

// wallpaperPost 构造一个带会话的后台壁纸请求。
func wallpaperPost(t *testing.T, h *Hub, cookies []*http.Cookie, kind string, fields map[string]string, fileName string, fileBody []byte) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("action", kind)
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

	rec := wallpaperPost(t, h, ck, "url", map[string]string{
		"wallpaper_url": "https://cdn.example.com/bg.jpg",
	}, "", nil)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("状态码 = %d，应为 303", rec.Code)
	}

	css := h.wallpaperCSS()
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
	rec = wallpaperPost(t, h, ck, "url", map[string]string{
		"wallpaper_url": `https://x.com/a.jpg");}body{display:none}`,
	}, "", nil)
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "wperr=url") {
		t.Errorf("非法地址应带着 wperr=url 退回，实际 Location=%q", loc)
	}
	if css := h.wallpaperCSS(); !strings.Contains(css, "cdn.example.com") {
		t.Errorf("被拒的地址不该覆盖掉原来的设置：%q", css)
	}
}

// TestWallpaperClearClearsCSS 清除之后不该再注入任何东西。
func TestWallpaperClearClearsCSS(t *testing.T) {
	h, st := newThemeTestHub(t)
	ck := wallpaperCookies(t, h, st)
	wallpaperPost(t, h, ck, "url", map[string]string{
		"wallpaper_url": "https://cdn.example.com/bg.jpg",
	}, "", nil)
	if h.wallpaperCSS() == "" {
		t.Fatal("前置条件不成立：设置没生效")
	}
	wallpaperPost(t, h, ck, "clear", nil, "", nil)
	if got := h.wallpaperCSS(); got != "" {
		t.Errorf("清除后仍注入了 %q", got)
	}
}

// TestWallpaperRejectsSVG svg 是唯一能在"图片"位置执行脚本的格式，必须拒。
func TestWallpaperRejectsSVG(t *testing.T) {
	h, st := newThemeTestHub(t)
	rec := wallpaperPost(t, h, wallpaperCookies(t, h, st), "upload",
		nil, "evil.svg", []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`))
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "wperr=type") {
		t.Errorf("svg 应被拒（wperr=type），实际 Location=%q", loc)
	}
	if h.wallpaperKind() != "" {
		t.Error("上传失败不该把站点标成已设壁纸")
	}
}

// TestWallpaperRouteNeedsAuth /wallpaper 是公开的（访客要加载它），
// 但没设壁纸时必须 404 —— 不能让人靠它探到"上传过东西"。
func TestWallpaperRouteNeedsAuth(t *testing.T) {
	h, _ := newThemeTestHub(t)
	req := httptest.NewRequest(http.MethodGet, "/wallpaper", nil)
	rec := httptest.NewRecorder()
	h.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("没设壁纸时 /wallpaper 应为 404，实际 %d", rec.Code)
	}
}

// TestWallpaperPanelOnThemePage 主题页要有壁纸面板（含本地上传与填 URL 两条路）。
func TestWallpaperPanelOnThemePage(t *testing.T) {
	h, st := newThemeTestHub(t)
	req := httptest.NewRequest(http.MethodGet, "/admin/themes", nil)
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
}
