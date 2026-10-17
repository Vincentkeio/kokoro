package hub

// .kokoro-theme 包在 HTTP 层的端到端测试。
//
// 覆盖的是 theme_test.go 没覆盖的那条链：
//   导入包 → 落库 → 重启恢复 → 导出成包 → 对方站一键获取完整包。

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Vincentkeio/kokoro/internal/model"
	"github.com/Vincentkeio/kokoro/internal/store"
	"github.com/Vincentkeio/kokoro/internal/theme"
)

// newStoreOn 在临时目录里开一个 store，并在测试结束时关闭。
//
// 关闭必须注册成 t.Cleanup 且发生在 TempDir 的清理之前：
// Windows 上 SQLite 文件被持有时无法删除，t.TempDir 的清理会直接失败，
// 于是测试报出一个和被测逻辑毫无关系的错。
func newStoreOn(t *testing.T, dir string) *store.Store {
	t.Helper()
	st, err := store.Open(dir + "/kokoro.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// newHubOn 用已有的 store 构造 Hub，模拟「进程重启」。
// 关键点：多个 Hub 共用一个 DB 文件，但各自有独立的注册表，
// 所以只有真的走了 loadStoredThemes，重启后的 Hub 才会认得自定义主题。
func newHubOn(t *testing.T, st *store.Store, dir string) *Hub {
	t.Helper()
	h, err := New(&model.HubConfig{
		Listen:   "127.0.0.1:0",
		SiteName: "测试站",
		DataDir:  dir,
		TLSMode:  "none",
	}, st)
	if err != nil {
		t.Fatalf("构造 Hub 失败: %v", err)
	}
	return h
}

// makeBundle 打一个测试用的主题包。
func makeBundle(t *testing.T, id string, assets map[string][]byte) []byte {
	t.Helper()
	m := &theme.Manifest{
		SchemaVersion:       theme.ManifestVersion,
		ID:                  id,
		Name:                "包主题",
		Version:             "3.0.0",
		Author:              "tester",
		License:             "MIT",
		TokensSchemaVersion: theme.TokensSchemaVersion,
		Tokens: map[string]string{
			"--kokoro-color-primary": "#ff5722",
		},
		LayoutSchemaVersion: theme.LayoutSchemaVersion,
		Layout:              theme.Layout{},
	}
	raw, err := theme.BuildPackage(m, assets, []byte("preview-bytes"), "MIT", "# 包主题")
	if err != nil {
		t.Fatalf("打包失败: %v", err)
	}
	return raw
}

// uploadBundle 模拟浏览器上传一个 .kokoro-theme 包。
func uploadBundle(t *testing.T, h *Hub, cookies []*http.Cookie, raw []byte) *httptest.ResponseRecorder {
	return uploadThemeFile(t, h, cookies, "theme.kokoro-theme", raw)
}

func uploadThemeFile(t *testing.T, h *Hub, cookies []*http.Cookie, filename string, raw []byte) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("bundle", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	req := withCookies(httptest.NewRequest(http.MethodPost, "/admin/themes/import", &buf), cookies)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func TestImportJSONFilePersistsAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	st := newStoreOn(t, dir)
	h := newHubOn(t, st, dir)
	cookies := []*http.Cookie{loginCookie(t, h, st)}
	raw := []byte(`{"schemaVersion":1,"id":"acme.json-file","name":"JSON 文件","version":"1.0.0","tokens":{"--kokoro-color-primary":"#d8b56d"}}`)
	w := uploadThemeFile(t, h, cookies, "theme.json", raw)
	if w.Code != http.StatusSeeOther || h.themes.Get("acme.json-file") == nil {
		t.Fatalf("JSON 文件未导入: status=%d location=%s", w.Code, w.Header().Get("Location"))
	}
	restarted := newHubOn(t, st, dir)
	if restarted.themes.Get("acme.json-file") == nil {
		t.Fatal("重启后丢失 JSON 文件导入的主题")
	}
}

func TestImportJSONFileRejectsInvalidManifest(t *testing.T) {
	h, st := newThemeTestHub(t)
	cookies := []*http.Cookie{loginCookie(t, h, st)}
	w := uploadThemeFile(t, h, cookies, "theme.json", []byte(`{"id":"acme.bad-json",`))
	if h.themes.Get("acme.bad-json") != nil || !strings.Contains(w.Header().Get("Location"), "msg=") {
		t.Fatal("非法 JSON 应被拒绝并显示错误")
	}
}

// TestPackagedWallpaperUsesThemeAsset 主题包自带的图与站点壁纸是两回事：
// 前者由主题 token 提供（包里的 /_theme-assets/），后者由站长**按主题**设。
// 这条盯的是「主题包自己的图没被弄丢」，而不是谁盖谁 ——
// 站长给这个主题设了壁纸就会盖上去，那是预期行为。
func TestPackagedWallpaperUsesThemeAsset(t *testing.T) {
	h, st := newThemeTestHub(t)
	m, err := theme.Parse([]byte(`{"id":"acme.wallpaper","name":"壁纸包","version":"1.0.0","tokens":{"--kokoro-bg-image":"url(/_theme-assets/bg.png)"}}`))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := theme.BuildPackage(m, map[string][]byte{"bg.png": []byte("PNG")}, nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	m, err = h.installBundle(raw, "")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)

	// 没给这个主题设站点壁纸 → 不注入，页面用包里那张图。
	if v := h.buildThemeView(req, m); v.WallpaperCSS != "" {
		t.Fatalf("没设站点壁纸时不该注入: %s", v.WallpaperCSS)
	}
	if v := h.buildThemeView(req, m); !strings.Contains(string(v.Tokens), "url(/_theme-assets/acme.wallpaper/bg.png)") {
		t.Fatalf("主题包自己的 bg-image 丢了: %s", v.Tokens)
	}

	// 给这个主题设了站点壁纸 → 注入并盖上去，这是预期行为。
	if err := st.SetSetting(wallpaperSettingKey(settingWallpaperKind, "acme.wallpaper"), "url"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting(wallpaperSettingKey(settingWallpaperURL, "acme.wallpaper"), "https://cdn.example.com/site.jpg"); err != nil {
		t.Fatal(err)
	}
	if v := h.buildThemeView(req, m); !strings.Contains(string(v.WallpaperCSS), "cdn.example.com") {
		t.Fatalf("给该主题设了壁纸就该注入: %s", v.WallpaperCSS)
	}

	// 另一个没设壁纸的自定义主题：不该被影响（壁纸按主题存）。
	bare, err := theme.Parse([]byte(`{"id":"acme.bare","name":"无壁纸","version":"1.0.0","tokens":{"--kokoro-color-bg":"#111"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if v := h.buildThemeView(req, bare); v.WallpaperCSS != "" {
		t.Fatalf("没给 acme.bare 设壁纸，不该注入: %s", v.WallpaperCSS)
	}
}

func TestImportBundlePersistsAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	st := newStoreOn(t, dir)
	h := newHubOn(t, st, dir)
	cookies := loginAsAdmin(t, h)

	raw := makeBundle(t, "acme.persisted", map[string][]byte{"bg.png": []byte("PNGDATA")})
	w := uploadBundle(t, h, cookies, raw)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("上传应 303，实际 %d: %s", w.Code, w.Body.String())
	}
	if h.themes.Get("acme.persisted") == nil {
		t.Fatal("上传后主题应进入注册表")
	}
	// 原始包字节也要留下来，否则「导出成包」会退化成只有清单。
	if got, ok := h.storedBundle("acme.persisted"); !ok || len(got) == 0 {
		t.Fatal("原始包字节没有被保存")
	}

	// 模拟重启：新 Hub，同一个 DB。
	h2 := newHubOn(t, st, dir)
	if h2.themes.Get("acme.persisted") == nil {
		t.Fatal("重启后自定义主题消失了 —— 这是必须修的持久化 bug")
	}
	m := h2.themes.Get("acme.persisted")
	if m.Tokens["--kokoro-color-primary"] != "#ff5722" {
		t.Errorf("重启后 tokens 不对: %+v", m.Tokens)
	}
	// 重启后仍能原样导出包（资源与哈希不变）。
	if got, ok := h2.storedBundle("acme.persisted"); !ok || len(got) == 0 {
		t.Error("重启后原始包丢失")
	}
}

// TestPackagedWallpaperSurvivesRestart 盯的是「重启后主题包壁纸被站点壁纸顶掉」。
//
// 这条链路上 Package 只在 installBundle 的内存对象里被填上（exportManifest
// 刻意不序列化它），所以必须由 loadStoredThemes 从 themes.meta 快照还原；
// 漏了这一步 hasPackagedWallpaper() 在重启后恒为 false，于是站点后备壁纸
// 被注入到 #kokoro-tokens **之后**——后定义的赢——自带壁纸的主题包
// 静默退回站点壁纸。界面不报错，只是「换了个背景」，极难和"主题坏了"区分。
func TestPackagedWallpaperSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	st := newStoreOn(t, dir)
	h := newHubOn(t, st, dir)
	// 壁纸按主题存，所以给这个导入主题单独设一张。站点壁纸**必须真的存在**，
	// 否则"主题包自己那张图还在不在"这件事根本测不出来。
	if err := st.SetSetting(wallpaperSettingKey(settingWallpaperKind, "acme.wall-restart"), "url"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting(wallpaperSettingKey(settingWallpaperURL, "acme.wall-restart"), "https://cdn.example.com/site.jpg"); err != nil {
		t.Fatal(err)
	}
	pkg := theme.Manifest{
		SchemaVersion:       theme.ManifestVersion,
		ID:                  "acme.wall-restart",
		Name:                "重启后仍自带壁纸",
		Version:             "1.0.0",
		TokensSchemaVersion: theme.TokensSchemaVersion,
		Tokens:              map[string]string{"--kokoro-bg-image": "url(/_theme-assets/wg.png)"},
		LayoutSchemaVersion: theme.LayoutSchemaVersion,
		Layout:              theme.Layout{},
	}
	raw, err := theme.BuildPackage(&pkg, map[string][]byte{"wg.png": []byte("PNGDATA")}, nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.installBundle(raw, ""); err != nil {
		t.Fatalf("导入主题包失败: %v", err)
	}
	if err := st.SetSetting(settingTheme, "acme.wall-restart"); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)

	// 站点壁纸是通用设置，会盖在主题自带背景图之上；所以这里要盯的不是"谁盖谁"，
	// 而是**主题包自己那张图还在不在** —— 包元数据/资源一丢，主题就只剩站点壁纸，
	// 表现成"换了个背景"，极难和"主题坏了"区分。
	view := h.buildThemeView(req, h.themes.Get("acme.wall-restart"))
	if !strings.Contains(string(view.Tokens), "url(/_theme-assets/acme.wall-restart/wg.png)") {
		t.Fatalf("导入后主题包自己的 bg-image 丢了: %s", view.Tokens)
	}

	h2 := newHubOn(t, st, dir)
	m := h2.themes.Get("acme.wall-restart")
	if m == nil {
		t.Fatal("重启后自定义主题消失")
	}
	if m.Package == nil {
		t.Fatal("重启后 m.Package 未还原 —— 主题包自己的图会跟着丢")
	}
	if v := h2.buildThemeView(req, m); !strings.Contains(string(v.Tokens), "url(/_theme-assets/acme.wall-restart/wg.png)") {
		t.Fatalf("重启后主题包自己的 bg-image 丢了: %s", v.Tokens)
	}
	// 资源也要跟着回来，否则 url() 指向一个 404，背景一样是空的。
	asset, ok := h2.themeAssets.get("acme.wall-restart", "assets/wg.png")
	if !ok || len(asset) == 0 {
		t.Fatal("重启后主题包资源没有进缓存，/_theme-assets/ 会 404")
	}
}

// 复现 2026-10-16 线上事故：站点当前生效的主题是一套「裸 JSON 导入」的
// 旧版本，管理员补导了带资源包的新版本后，页面必须立刻换上新背景。
//
// 修好前：buildThemeView 对站点主题走 h.theme.tokens 这份启动时渲染的快照，
// installBundle/importManifest 不重建它——导入后 tokens 仍是旧的
// bg-image:none，而站点后备壁纸又被 hasPackagedWallpaper 抑制，背景整个
// 消失。表现成「导入了但什么都没变」，且要等到下次重启才生效。
func TestImportActiveThemeRebuildsRenderCache(t *testing.T) {
	dir := t.TempDir()
	st := newStoreOn(t, dir)
	h := newHubOn(t, st, dir)
	cookies := loginAsAdmin(t, h)
	// 站点壁纸先开着（给这个主题），才能看出「主题包自带图 + 站点壁纸」两者都在。
	if err := st.SetSetting(wallpaperSettingKey(settingWallpaperKind, "acme.reimport"), "url"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting(wallpaperSettingKey(settingWallpaperURL, "acme.reimport"), "https://cdn.example.com/site.jpg"); err != nil {
		t.Fatal(err)
	}

	// ① 先导入一份裸 JSON 旧版（无包，bg-image:none）。
	bare := []byte(`{"schemaVersion":1,"id":"acme.reimport","name":"重导入","version":"1.0.0","tokens":{"--kokoro-bg-image":"none","--kokoro-color-primary":"#111111"}}`)
	w := uploadThemeFile(t, h, cookies, "theme.json", bare)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("旧版导入失败: %d %s", w.Code, w.Header().Get("Location"))
	}
	// ② 切为站点主题（POST /theme/<id> 才会重建快照并落库）。
	req := withCookies(httptest.NewRequest(http.MethodPost, "/theme/acme.reimport", nil), cookies)
	h.ServeHTTP(httptest.NewRecorder(), req)
	if got := h.ActiveThemeID(); got != "acme.reimport" {
		t.Fatalf("切换后当前主题 = %q", got)
	}
	if v := string(h.buildThemeView(httptest.NewRequest(http.MethodGet, "/", nil), h.themes.Get("acme.reimport")).Tokens); !strings.Contains(v, "none") {
		t.Fatalf("导入前 tokens 应含 none: %s", v)
	}

	// ③ 导入带资源包的新版：bg-image 指向主题自己的壁纸。
	pkg := theme.Manifest{
		SchemaVersion:       theme.ManifestVersion,
		ID:                  "acme.reimport",
		Name:                "重导入",
		Version:             "2.0.0",
		Author:              "tester",
		License:             "MIT",
		TokensSchemaVersion: theme.TokensSchemaVersion,
		Tokens:              map[string]string{"--kokoro-bg-image": "url(/_theme-assets/wg.png)", "--kokoro-color-primary": "#ffd700"},
		LayoutSchemaVersion: theme.LayoutSchemaVersion,
		Layout:              theme.Layout{},
	}
	raw, err := theme.BuildPackage(&pkg, map[string][]byte{"wg.png": []byte("PNGDATA")}, nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	w = uploadBundle(t, h, cookies, raw)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("新版包导入失败: %d %s", w.Code, w.Header().Get("Location"))
	}
	if loc := w.Header().Get("Location"); strings.Contains(loc, "msg=") && strings.Contains(loc, "导入失败") {
		t.Fatalf("新版包导入报错: %s", loc)
	}

	// ④ 不重启：tokens 必须已经是新版，且主题包壁纸取代站点后备。
	// rewriteAssetURL 会把 url() 里的资源路径补上主题 ID 前缀。
	view := h.buildThemeView(httptest.NewRequest(http.MethodGet, "/", nil), h.themes.Get("acme.reimport"))
	if !strings.Contains(string(view.Tokens), "url(/_theme-assets/acme.reimport/wg.png)") {
		t.Fatalf("导入当前生效主题后 tokens 未刷新: %s", view.Tokens)
	}
	// 站点壁纸是通用设置：设了就会注入（盖在主题自带背景之上）。
	// "主题包自己的图还在不在"由上面 tokens 那条断言守着。
	if view.WallpaperCSS == "" {
		t.Error("站点壁纸应当注入，这里却缺席了")
	}
	// 资源也要立刻可访问，否则 url() 指向 404。
	reqAsset := httptest.NewRequest(http.MethodGet, "/_theme-assets/acme.reimport/wg.png", nil)
	wAsset := httptest.NewRecorder()
	h.ServeHTTP(wAsset, reqAsset)
	if wAsset.Code != http.StatusOK || wAsset.Body.String() != "PNGDATA" {
		t.Fatalf("主题资源应立刻可用: %d", wAsset.Code)
	}
}

func TestImportedThemeBecomesActiveAfterRestart(t *testing.T) {
	dir := t.TempDir()
	st := newStoreOn(t, dir)
	h := newHubOn(t, st, dir)
	cookies := loginAsAdmin(t, h)
	raw := makeBundle(t, "acme.activated", nil)
	uploadBundle(t, h, cookies, raw)

	// 切换到它并确认落库（入口是 POST，见 theme.go 的 handleThemeSwitch）。
	req := withCookies(httptest.NewRequest(http.MethodPost, "/theme/acme.activated", nil), cookies)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if got := h.ActiveThemeID(); got != "acme.activated" {
		t.Fatalf("切换后当前主题 = %q", got)
	}

	// 重启后必须仍然是它——否则「选了主题重启就丢」比「导入就丢」更隐蔽。
	h2 := newHubOn(t, st, dir)
	if got := h2.ActiveThemeID(); got != "acme.activated" {
		t.Fatalf("重启后当前主题 = %q，应恢复为 acme.activated", got)
	}
	// 内置主题不受影响，仍在注册表里。
	if h2.themes.Get(theme.DefaultID) == nil {
		t.Error("重启后内置主题丢失")
	}
}

func TestThemeBundleEndpointServesPackage(t *testing.T) {
	dir := t.TempDir()
	st := newStoreOn(t, dir)
	h := newHubOn(t, st, dir)
	cookies := loginAsAdmin(t, h)
	uploadBundle(t, h, cookies, makeBundle(t, "acme.served", nil))

	// 公开端点：不需要登录。
	req := httptest.NewRequest(http.MethodGet, "/theme-bundle/acme.served", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("取包应 200，实际 %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/zip" {
		t.Errorf("Content-Type = %q", ct)
	}
	b, err := theme.ExtractPackage(w.Body.Bytes())
	if err != nil {
		t.Fatalf("端点发出的包解不开: %v", err)
	}
	m, err := theme.Parse(b.ManifestRaw)
	if err != nil {
		t.Fatal(err)
	}
	if m.ID != "acme.served" {
		t.Errorf("包内 ID = %q", m.ID)
	}
}

// 一键获取应当优先拿到完整包，而不是退回裸清单。
func TestGrabThemePrefersBundle(t *testing.T) {
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/theme.json":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"schemaVersion": 1, "id": "peer.packaged", "name": "对方包主题",
				"version": "9.9.9", "author": "peer",
				"tokensSchemaVersion": 2, "layoutSchemaVersion": 1,
				"tokens": map[string]string{"--kokoro-color-primary": "#00bcd4"},
				"layout": map[string]any{},
			})
		case strings.HasPrefix(r.URL.Path, "/theme-bundle/"):
			w.Header().Set("Content-Type", "application/zip")
			_, _ = w.Write(makeBundle(t, "peer.packaged", map[string][]byte{"a.txt": []byte("A")}))
		default:
			http.NotFound(w, r)
		}
	}))
	defer peer.Close()

	h, _ := newThemeTestHub(t)
	m, kind, err := h.grabTheme(peer.URL)
	if err != nil {
		t.Fatalf("一键获取失败: %v", err)
	}
	if kind != "bundle" {
		t.Errorf("应拿到完整包，实际 kind = %q", kind)
	}
	if m.ID != "peer.packaged" {
		t.Errorf("主题 ID = %q", m.ID)
	}
	// 资源字节应该在存档里。
	if raw, ok := h.storedBundle("peer.packaged"); !ok || len(raw) == 0 {
		t.Error("抓来的包字节没有保存")
	}
}

// 对方只提供清单（没有包端点）时，应优雅退回而不是报错。
func TestGrabThemeFallsBackToManifest(t *testing.T) {
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/theme.json" {
			_, _ = w.Write([]byte(`{"schemaVersion":1,"id":"peer.plain","name":"只有清单",
				"version":"1.0.0","author":"peer","tokensSchemaVersion":2,
				"layoutSchemaVersion":1,"tokens":{"--kokoro-color-primary":"#4caf50"},
				"layout":{}}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer peer.Close()

	h, _ := newThemeTestHub(t)
	m, kind, err := h.grabTheme(peer.URL)
	if err != nil {
		t.Fatalf("应能退回清单模式: %v", err)
	}
	if kind != "manifest" {
		t.Errorf("kind = %q，应为 manifest", kind)
	}
	if m.ID != "peer.plain" {
		t.Errorf("ID = %q", m.ID)
	}
}

// 对方提供恶意包时，必须拒绝且不落库。
func TestGrabThemeRejectsMaliciousBundle(t *testing.T) {
	raw := zipEntries(t, map[string]string{
		"theme.json": `{"schemaVersion":1,"id":"peer.evil","name":"坏包",
		"version":"1.0.0","author":"peer","tokensSchemaVersion":2,"layoutSchemaVersion":1,
		"tokens":{"--kokoro-color-bg":"#fff}body{display:none"},"layout":{}}`,
		"../../etc/passwd": "root:x:0:0",
	})

	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/theme.json":
			_, _ = w.Write([]byte(`{"schemaVersion":1,"id":"peer.evil","name":"坏包","version":"1.0.0",
				"author":"peer","tokensSchemaVersion":2,"layoutSchemaVersion":1,
				"tokens":{"--kokoro-color-primary":"#000"},"layout":{}}`))
		case strings.HasPrefix(r.URL.Path, "/theme-bundle/"):
			_, _ = w.Write(raw)
		default:
			http.NotFound(w, r)
		}
	}))
	defer peer.Close()

	h, _ := newThemeTestHub(t)
	_, kind, err := h.grabTheme(peer.URL)
	if err == nil {
		t.Fatal("恶意包（含 zip-slip 路径）必须被拒绝")
	}
	// 拒绝的是包，之后应退回清单——清单本身也是恶意 tokens，所以最终仍要失败。
	if kind == "bundle" {
		t.Error("恶意包不应被安装")
	}
	if h.themes.Get("peer.evil") != nil {
		t.Error("被拒的主题不应进入注册表")
	}
}

func TestImportBundleRejectsZipSlip(t *testing.T) {
	h, _ := newThemeTestHub(t)
	cookies := loginAsAdmin(t, h)

	var buf []byte
	buf = zipEntries(t, map[string]string{
		"theme.json": `{"schemaVersion":1,"id":"acme.slipped","name":"x","version":"1.0.0",
		"author":"a","tokensSchemaVersion":2,"layoutSchemaVersion":1,
		"tokens":{"--kokoro-color-primary":"#000"},"layout":{}}`,
		"../../../../tmp/kokoro-evil.txt": "pwn",
	})

	w := uploadBundle(t, h, cookies, buf)
	loc := w.Header().Get("Location")
	if !strings.Contains(loc, "msg=") {
		t.Fatalf("应报错并回后台，实际 Location = %q", loc)
	}
	if h.themes.Get("acme.slipped") != nil {
		t.Error("含 zip-slip 的包不应被安装")
	}
}

func TestThemeBundleRejectsOfficialPrefix(t *testing.T) {
	h, _ := newThemeTestHub(t)
	cookies := loginAsAdmin(t, h)

	raw := makeBundle(t, "kokoro.imposter", nil)
	w := uploadBundle(t, h, cookies, raw)
	loc := w.Header().Get("Location")
	if !strings.Contains(loc, "msg=") {
		t.Fatalf("应给反馈，实际 %q", loc)
	}
	if h.themes.Get("kokoro.imposter") != nil {
		t.Error("冒用官方前缀的包必须被拒绝")
	}
}

func TestDeleteThemeRemovesFromStore(t *testing.T) {
	dir := t.TempDir()
	st := newStoreOn(t, dir)
	h := newHubOn(t, st, dir)
	cookies := loginAsAdmin(t, h)
	uploadBundle(t, h, cookies, makeBundle(t, "acme.temporary", nil))

	form := strings.NewReader("id=acme.temporary")
	req := withCookies(httptest.NewRequest(http.MethodPost, "/admin/themes/delete", form), cookies)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("删除应 303，实际 %d", w.Code)
	}
	if h.themes.Get("acme.temporary") != nil {
		t.Error("删除后不应留在注册表")
	}
	// 重启后也不能复活。
	h2 := newHubOn(t, st, dir)
	if h2.themes.Get("acme.temporary") != nil {
		t.Error("删除后重启仍复活，说明数据库里没删掉")
	}
}

// TestWallpaperIsKeyedByTheme 盯的是「壁纸按主题存」这条规则：
// 只有**给这个主题设过**壁纸时才注入，没设时返回空串 —— 页面就去用主题自带的背景图。
// 内置主题一视同仁，不再有"按主题 ID 白名单判断谁自带壁纸"那套：
// 那套和主题 token 脱钩过一次，把简洁主题的背景整个搞成空白。
func TestWallpaperIsKeyedByTheme(t *testing.T) {
	dir := t.TempDir()
	st := newStoreOn(t, dir)
	h := newHubOn(t, st, dir)
	req := httptest.NewRequest(http.MethodGet, "/", nil)

	// 谁都没设：一个都不该注入。
	for _, id := range []string{"kokoro.paper", "kokoro.mist", "kokoro.midnight", "kokoro.simple"} {
		m := h.themes.Get(id)
		if m == nil {
			t.Fatalf("内置主题 %s 未注册", id)
		}
		if v := h.buildThemeView(req, m); v.WallpaperCSS != "" {
			t.Errorf("%s 没设壁纸，不该注入: %s", id, v.WallpaperCSS)
		}
	}

	// 只给简洁主题设一张：只有它注入，别的主题照旧。
	if err := st.SetSetting(wallpaperSettingKey(settingWallpaperKind, "kokoro.simple"), "url"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting(wallpaperSettingKey(settingWallpaperURL, "kokoro.simple"), "https://cdn.example.com/site.jpg"); err != nil {
		t.Fatal(err)
	}
	if v := h.buildThemeView(req, h.themes.Get("kokoro.simple")); !strings.Contains(string(v.WallpaperCSS), "cdn.example.com") {
		t.Errorf("简洁主题应注入它自己的站点壁纸: %s", v.WallpaperCSS)
	}
	if v := h.buildThemeView(req, h.themes.Get("kokoro.mist")); v.WallpaperCSS != "" {
		t.Errorf("别的主题不该被影响: %s", v.WallpaperCSS)
	}
}

// TestThemeSwitchClearsVisitorCookie 后台启用主题必须把**自己浏览器**上的
// 访客主题偏好清掉：站长之前用前端切换器试过别的主题，那个 cookie 会继续
// 盖住站点默认，表现成"后台点了启用，前台还是原来的主题"。
func TestThemeSwitchClearsVisitorCookie(t *testing.T) {
	h, st := newThemeTestHub(t)
	req := httptest.NewRequest(http.MethodPost, "/theme/"+theme.DefaultID, nil)
	req.AddCookie(loginCookie(t, h, st))
	req.AddCookie(&http.Cookie{Name: theme.ThemeCookie, Value: "kokoro.mist"})
	rec := httptest.NewRecorder()
	h.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("切换站点主题应 303，实际 %d", rec.Code)
	}
	cleared := false
	for _, c := range rec.Result().Cookies() {
		if c.Name == theme.ThemeCookie && (c.MaxAge < 0 || c.Value == "") {
			cleared = true
		}
	}
	if !cleared {
		t.Error("启用主题时应清掉访客主题 cookie，否则站长自己看不到变化")
	}
}

// ---- 极简 zip 写入辅助 ----

// zipEntries 按给定条目造一个 zip 字节流。键是条目名，值是内容。
func zipEntries(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
