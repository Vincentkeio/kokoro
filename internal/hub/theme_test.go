package hub

// 主题在 HTTP 层的回归测试。
//
// internal/theme 那边测的是"引擎对不对"，这里测的是"接上线之后对不对"：
// 变量有没有真的注入到页面、切换是否持久化、越权是否被挡住、
// 一键获取是否守住安全边界。

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kokoro-probe/kokoro/internal/model"
	"github.com/kokoro-probe/kokoro/internal/store"
	"github.com/kokoro-probe/kokoro/internal/theme"
)

// newThemeTestHub 起一个不监听端口的 Hub，数据落在临时目录。
func newThemeTestHub(t *testing.T) (*Hub, *store.Store) {
	t.Helper()
	return newThemeTestHubWithDomain(t, "127.0.0.1")
}

// newThemeTestHubWithDomain 造一个测试 Hub，可指定 --domain。
//
// 默认把 Domain 设成 127.0.0.1：测试里的「对方站点」全都是 httptest 起在
// 回环上的，而抓取出口的策略是「回环默认拒绝，只有 --domain 里的主机名放行」。
// 这正好对应真实的自建站场景——Hub 监听在 127.0.0.1，反代在外面，
// 抓自己站就是走回环。
func newThemeTestHubWithDomain(t *testing.T, domain string) (*Hub, *store.Store) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "kokoro.db"))
	if err != nil {
		t.Fatalf("打开 store 失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	h, err := New(&model.HubConfig{
		Listen:   "127.0.0.1:0",
		Domain:   domain,
		SiteName: "测试站",
		DataDir:  dir,
		TLSMode:  "none",
	}, st)
	if err != nil {
		t.Fatalf("构造 Hub 失败: %v", err)
	}
	return h, st
}

// loginAsAdmin 走一遍真实的登录流程，返回带着会话 cookie 的请求头。
// 用真流程而不是直接塞 token，是为了让测试顺带覆盖会话这一环。
func loginAsAdmin(t *testing.T, h *Hub) []*http.Cookie {
	t.Helper()
	user, pass, _ := h.ensureAdmin()
	form := "username=" + user + "&password=" + pass
	req := httptest.NewRequest(http.MethodPost, "/admin", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("登录应 303，实际 %d", w.Code)
	}
	return w.Result().Cookies()
}

func withCookies(req *http.Request, cookies []*http.Cookie) *http.Request {
	for _, c := range cookies {
		req.AddCookie(c)
	}
	return req
}

// importTestTheme 导入一套社区主题，返回它的 ID。
//
// 切换类测试都需要一个"另一套主题"当目标，而内置主题目前只有默认那一份。
// 所以这里现场导入社区主题（走真实的 Import 校验链路），而不是引用某套
// 内置主题——内置清单会随界面定稿增减，测试不该被它拖着走。
func importTestTheme(t *testing.T, h *Hub, id, json string) string {
	t.Helper()
	if _, err := h.themes.Import([]byte(json)); err != nil {
		t.Fatalf("导入测试主题 %s 失败: %v", id, err)
	}
	return id
}

// testThemeJSON 拼一份带指定 id 的合法社区主题。
//
// layout 段落按调用方的目的覆盖：这里给的是「深色 + 表格 + 渐变 Hero」，
// 与默认主题的「浅色 + 卡片 + 无 Hero」正好相反，切换后差异一眼可辨。
func testThemeJSON(id, name string) string {
	return `{"schemaVersion":1,"id":"` + id + `","name":"` + name + `","version":"1.0.0",
		"author":"tester","tokensSchemaVersion":2,"layoutSchemaVersion":1,
		"tokens":{"--kokoro-color-primary":"#5b8cff"},
		"mode":{"default":"dark","supportsDark":true,"allowUserSwitch":true},
		"layout":{
			"shell":{"header":{"variant":"blur"}},
			"home":{"list":{"mode":"table"},"hero":{"enabled":true,"variant":"gradient"}},
			"misc":{"statusDotStyle":"glow","usageBarStyle":"gradient"},
			"charts":{"type":"line","grid":false}
		}}`
}

func TestThemeTokensInjectedIntoEveryPage(t *testing.T) {
	h, _ := newThemeTestHub(t)
	cookies := loginAsAdmin(t, h)

	for _, path := range []string{"/", "/dashboard", "/admin", "/admin/themes"} {
		req := withCookies(httptest.NewRequest(http.MethodGet, path, nil), cookies)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		body := w.Body.String()
		if w.Code != http.StatusOK {
			t.Errorf("%s 返回 %d", path, w.Code)
			continue
		}
		if !strings.Contains(body, `id="kokoro-tokens"`) {
			t.Errorf("%s 没有注入主题变量块", path)
		}
		if !strings.Contains(body, "--kokoro-color-primary:") {
			t.Errorf("%s 的变量块里没有主色定义", path)
		}
		if !strings.Contains(body, "data-k-mode=") {
			t.Errorf("%s 没有输出深浅色标记", path)
		}
		// html/template 拒绝输出时会填这个占位符。
		// 一旦某个字段的类型用错（比如 HTML 放进属性位置）就会撞上它。
		if strings.Contains(body, "ZgotmplZ") {
			t.Errorf("%s 出现 ZgotmplZ，说明有模板字段类型不对", path)
		}
	}
}

func TestLoginPageAlsoThemed(t *testing.T) {
	h, _ := newThemeTestHub(t)
	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("登录页返回 %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "--kokoro-color-primary:") {
		t.Error("登录页也应带主题变量，否则会出现没主题的裸页面")
	}
}

func TestSwitchThemePersistsAndChangesLayout(t *testing.T) {
	h, st := newThemeTestHub(t)
	cookies := loginAsAdmin(t, h)
	importTestTheme(t, h, "peer.alt", testThemeJSON("peer.alt", "对照"))

	before := getAttr(t, h, cookies, "/", "data-k-list-mode")
	if before != "card" {
		t.Fatalf("默认主题列表形态 = %q，应为 card", before)
	}

	// 管理员把站点默认主题切成社区主题：列表应变表格、顶栏变毛玻璃
	req := withCookies(httptest.NewRequest(http.MethodGet,
		"/theme/peer.alt?back=%2F", nil), cookies)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("切换主题应重定向，实际 %d", w.Code)
	}

	if got := h.ActiveThemeID(); got != "peer.alt" {
		t.Fatalf("当前主题 = %q", got)
	}
	if saved, err := st.GetSetting(settingTheme); err != nil || saved != "peer.alt" {
		t.Errorf("主题没有落库（重启会丢）: %q %v", saved, err)
	}
	if got := getAttr(t, h, cookies, "/", "data-k-list-mode"); got != "table" {
		t.Errorf("切换后列表形态 = %q，应为 table", got)
	}
	if got := getAttr(t, h, cookies, "/", "data-k-dot"); got != "glow" {
		t.Errorf("切换后状态点样式 = %q，应为 glow", got)
	}
	if got := getAttr(t, h, cookies, "/", "data-k-header"); got != "blur" {
		t.Errorf("切换后顶栏形态 = %q，应为 blur", got)
	}
	// Hero 的"启用"与"变体"是两个独立属性，别只测一个。
	if got := getAttr(t, h, cookies, "/", "data-k-hero"); got != "gradient" {
		t.Errorf("Hero 变体 = %q，应为 gradient", got)
	}
	if got := getAttr(t, h, cookies, "/", "data-k-hero-on"); got != "1" {
		t.Errorf("Hero 启用标记 = %q，应为 1", got)
	}
	// grid:false 必须真的输出 data-k-chart-grid="0"，否则那条 CSS 规则匹配不到。
	if got := getAttr(t, h, cookies, "/", "data-k-chart-grid"); got != "0" {
		t.Errorf("关闭图表网格后 data-k-chart-grid = %q，应为 0", got)
	}
}

func TestSwitchThemeRequiresAdmin(t *testing.T) {
	h, _ := newThemeTestHub(t)
	importTestTheme(t, h, "peer.admin", testThemeJSON("peer.admin", "管理员用"))
	req := httptest.NewRequest(http.MethodGet, "/theme/peer.admin", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("未登录切换主题应 401，实际 %d", w.Code)
	}
	// 而且不应真的切换
	if got := h.ActiveThemeID(); got != theme.DefaultID {
		t.Errorf("未登录却把主题改成了 %q", got)
	}
}

func TestSwitchThemeRejectsUnknownID(t *testing.T) {
	h, _ := newThemeTestHub(t)
	cookies := loginAsAdmin(t, h)
	req := withCookies(httptest.NewRequest(http.MethodGet, "/theme/no.such.theme?back=%2F", nil), cookies)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("未知主题应重定向回后台，实际 %d", w.Code)
	}
	if !strings.Contains(w.Header().Get("Location"), "msg=") {
		t.Errorf("未知主题应带错误提示跳转，实际 Location = %q", w.Header().Get("Location"))
	}
}

func TestSwitchThemeRejectsOpenRedirect(t *testing.T) {
	h, _ := newThemeTestHub(t)
	cookies := loginAsAdmin(t, h)
	req := withCookies(httptest.NewRequest(http.MethodGet,
		"/theme/"+theme.DefaultID+"?back=https%3A%2F%2Fevil.example%2Fx", nil), cookies)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	loc := w.Header().Get("Location")
	if strings.Contains(loc, "evil.example") {
		t.Errorf("切换主题时存在开放重定向: %q", loc)
	}
}

// ---- 访客级主题切换（/pick/）----
//
// 语义上和 /theme/ 是两件事：/theme/ 改站点设置、影响所有人、要管理员；
// /pick/ 只写访客自己的 cookie、不动站点设置、人人可用。
// 这组测试守的就是这条边界不被日后改坏。

// pickCookie 从响应里取出 k_theme cookie 的值（空串表示"没有 cookie"）。
func pickCookie(w *httptest.ResponseRecorder) string {
	for _, c := range w.Result().Cookies() {
		if c.Name == theme.ThemeCookie {
			if c.MaxAge < 0 {
				return ""
			}
			return c.Value
		}
	}
	return ""
}

func TestVisitorPickNeedsNoLoginButKeepsSiteDefault(t *testing.T) {
	h, st := newThemeTestHub(t)
	importTestTheme(t, h, "peer.v", testThemeJSON("peer.v", "访客选的"))

	// 未登录访问 /pick/peer.v：必须成功（不是 401）
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/pick/peer.v?back=%2F", nil))
	if w.Code != http.StatusSeeOther {
		t.Fatalf("访客切换主题不应要求登录，实际 %d", w.Code)
	}
	if got := pickCookie(w); got != "peer.v" {
		t.Errorf("k_theme cookie = %q，应为 peer.v", got)
	}
	if got := w.Header().Get("Location"); got != "/" {
		t.Errorf("back=/ 时应跳回 /，实际 %q", got)
	}
	// 关键：访客自选绝不能改站点设置
	if got := h.ActiveThemeID(); got != theme.DefaultID {
		t.Errorf("访客切换把站点主题改成了 %q", got)
	}
	if saved, _ := st.GetSetting(settingTheme); saved == "peer.v" {
		t.Error("访客切换不该写库")
	}

	// 该访客再看首页：自己的选择生效
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: theme.ThemeCookie, Value: "peer.v"})
	if got := h.themeFor(req).ID; got != "peer.v" {
		t.Errorf("访客 cookie 未生效，themeFor.ID = %q", got)
	}
	if got := getAttr(t, h, nil, "/", "data-k-list-mode"); got != "card" {
		// 注意：getAttr 不带 cookie，所以看到的应是站点默认的 card
		t.Errorf("无 cookie 的访客应看到站点默认布局，实际 %q", got)
	}
	if got := getAttr(t, h, []*http.Cookie{{Name: theme.ThemeCookie, Value: "peer.v"}},
		"/", "data-k-list-mode"); got != "table" {
		t.Errorf("带 cookie 的访客应看到 table 布局，实际 %q", got)
	}
}

func TestVisitorPickDefaultRestoresSiteTheme(t *testing.T) {
	h, _ := newThemeTestHub(t)
	cookies := loginAsAdmin(t, h)
	importTestTheme(t, h, "peer.site", testThemeJSON("peer.site", "站点用的"))

	// 管理员先把站点主题设成社区主题
	w := httptest.NewRecorder()
	h.ServeHTTP(w, withCookies(httptest.NewRequest(http.MethodGet,
		"/theme/peer.site?back=%2Fadmin%2Fthemes", nil), cookies))
	if h.ActiveThemeID() != "peer.site" {
		t.Fatalf("站点主题未切换，实际 %q", h.ActiveThemeID())
	}

	// 访客通过 /pick/ 换到别处，再通过 /pick/default 回来
	for _, target := range []string{"peer.site", "default"} {
		w = httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/pick/"+target+"?back=%2F", nil))
		if w.Code != http.StatusSeeOther {
			t.Fatalf("/pick/%s 返回 %d", target, w.Code)
		}
	}
	if got := pickCookie(w); got != "" {
		t.Errorf("恢复默认应删掉 k_theme cookie，实际 %q", got)
	}
	// 站点主题自己没被动过
	if got := h.ActiveThemeID(); got != "peer.site" {
		t.Errorf("访客恢复默认却改了站点主题: %q", got)
	}
}

// TestVisitorPickAllowsInstalledThemesAndRejectsGhosts 访客可以切到
// 站点已安装的**任何**主题——因为这只影响他自己，不影响别人。
// 但 id 必须真实存在：手改 cookie 指向已删除或不存在的 id 时，
// 要安静地回落站点默认，而不是崩页面或加载出半截主题。
func TestVisitorPickAllowsInstalledThemesAndRejectsGhosts(t *testing.T) {
	h, _ := newThemeTestHub(t)
	// 站点装了但没启用两套社区主题，访客仍应能切过去预览。
	importTestTheme(t, h, "peer.hidden1", testThemeJSON("peer.hidden1", "隐藏一"))
	importTestTheme(t, h, "peer.hidden2", testThemeJSON("peer.hidden2", "隐藏二"))

	for _, id := range []string{"peer.hidden1", "peer.hidden2"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/pick/"+id+"?back=%2F", nil))
		if w.Code != http.StatusSeeOther {
			t.Errorf("切到已安装的主题 %q 应成功，实际 %d", id, w.Code)
		}
		if got := pickCookie(w); got != id {
			t.Errorf("k_theme cookie = %q，应为 %q", got, id)
		}
		// 站点主题始终不受影响
		if h.ActiveThemeID() != theme.DefaultID {
			t.Fatalf("访客切换把站点主题改成了 %q", h.ActiveThemeID())
		}
	}

	// 不存在的 id：404，不要 200 静默回首页
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/pick/no.such.theme", nil))
	if w.Code != http.StatusNotFound {
		t.Errorf("切到不存在的主题应 404，实际 %d", w.Code)
	}
	if got := pickCookie(w); got != "" {
		t.Errorf("404 时不该写 cookie，实际 %q", got)
	}

	// 主题被删掉后，指向它的旧 cookie 必须被忽略（不能加载已注销的主题）
	if err := h.themes.Unregister("peer.hidden1"); err != nil {
		t.Fatalf("注销主题失败: %v", err)
	}
	if err := h.removeTheme("peer.hidden1"); err != nil {
		t.Fatalf("删除主题存档失败: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: theme.ThemeCookie, Value: "peer.hidden1"})
	v := h.themeFor(req)
	if v.ID != theme.DefaultID {
		t.Errorf("指向已删除主题的 cookie 竟仍生效，实际 %q", v.ID)
	}
	if v.VisitorOverride {
		t.Error("失效的 cookie 不该被标记成访客覆盖")
	}

	// 超长 / 带空格的垃圾 cookie 也不能出事
	for _, junk := range []string{"../../../etc/passwd", "no such theme", "a", "%00"} {
		rj := httptest.NewRequest(http.MethodGet, "/", nil)
		rj.AddCookie(&http.Cookie{Name: theme.ThemeCookie, Value: junk})
		if got := h.themeFor(rj).ID; got != theme.DefaultID {
			t.Errorf("垃圾 cookie %q 竟生效为 %q", junk, got)
		}
	}
}

// TestVisitorSwitchShownWithSingleTheme 钉住「只有一套主题也要显示切换条」。
//
// 这条曾经是反的：早期 VisitorSwitch = len(pickable) > 1，
// 于是站点刚部署、只有内置默认主题时整条皮肤栏消失，
// 访客看不到"本站支持换外观"，也看不到"我正用着哪套"。
//
// 可发现性优先于"整洁"——单主题时它只有一个选项，但它仍然必须存在。
func TestVisitorSwitchShownWithSingleTheme(t *testing.T) {
	h, _ := newThemeTestHub(t)

	// 站点只有内置默认主题。
	if got := len(h.Themes().List()); got != 1 {
		t.Fatalf("前置条件不成立：站点应只有 1 套主题，实际 %d", got)
	}

	v := h.themeFor(httptest.NewRequest(http.MethodGet, "/", nil))
	if !v.VisitorSwitch {
		t.Fatal("只有一套主题时 VisitorSwitch 也应为真（否则切换器消失）")
	}
	if len(v.Pickable) != 1 {
		t.Fatalf("可选主题应恰有 1 套，实际 %d", len(v.Pickable))
	}

	// 真的渲染到页面上：访客（未登录）看首页必须能看到切换器，
	// 且里面有一个指向 /pick/ 的访客入口。
	body := renderBody(t, h, httptest.NewRequest(http.MethodGet, "/", nil))
	if !strings.Contains(body, "data-skin-picker") {
		t.Error("单主题时首页应渲染出皮肤切换器")
	}
	if !strings.Contains(body, "/pick/"+theme.DefaultID) {
		t.Error("单主题时切换器应给出访客切换路径 /pick/<id>")
	}
	// 位置要求在页头右上角：必须在 </header> 之前。
	if end := strings.Index(body, "</header>"); end < 0 {
		t.Error("首页没有 </header>，结构不对")
	} else if at := strings.Index(body, "data-skin-picker"); at > end {
		t.Error("皮肤切换器应位于页头右上角，不应出现在页尾")
	}
	// 混进管理员专用路径就是权限串台了。
	if strings.Contains(body, `href="/theme/`) {
		t.Error("切换器不该出现管理员路径 /theme/")
	}
}

// TestVisitorSwitchHiddenOnlyWhenNoThemes 唯一的例外：注册表为空。
func TestVisitorSwitchHiddenOnlyWhenNoThemes(t *testing.T) {
	h, _ := newThemeTestHub(t)
	h.themes = nil // 模拟内置主题加载失败
	v := h.themeFor(httptest.NewRequest(http.MethodGet, "/", nil))
	if v.VisitorSwitch {
		t.Error("没有任何主题时不该显示皮肤栏")
	}
}

// TestVisitorPickSwitchesListModeAndChips 验证访客切换后：
// 列表形态回到新主题的默认（旧的访客偏好被清掉）、主题条出现"恢复默认"。
func TestVisitorPickSwitchesListModeAndChips(t *testing.T) {
	h, _ := newThemeTestHub(t)
	cookies := loginAsAdmin(t, h)
	// 第二套内置主题不存在，所以这里给站点设成社区主题、
	// 再让访客切回默认主题——这样至少有两套可切。
	importTestTheme(t, h, "peer.cur", testThemeJSON("peer.cur", "站点当前"))
	h.ServeHTTP(httptest.NewRecorder(), withCookies(httptest.NewRequest(http.MethodGet,
		"/theme/peer.cur?back=%2F", nil), cookies))

	// 访客之前记住了 compact（站点主题下合法的形态）
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: listModeCookie, Value: "compact"})
	v := h.themeFor(req)
	if v.ListMode != "compact" {
		t.Fatalf("前置条件不成立：访客列表形态 = %q", v.ListMode)
	}
	if !v.VisitorSwitch {
		t.Fatal("存在可切换的第二套主题时，VisitorSwitch 应为真")
	}

	// 切到默认主题：应当清掉旧的 compact，回到默认主题自己的 card
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/pick/"+theme.DefaultID+"?back=%2F", nil))
	for _, c := range w.Result().Cookies() {
		if c.Name == listModeCookie && c.MaxAge >= 0 {
			t.Error("切换主题后不该保留旧的列表形态 cookie")
		}
	}

	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	req2.AddCookie(&http.Cookie{Name: theme.ThemeCookie, Value: theme.DefaultID})
	v2 := h.themeFor(req2)
	if v2.ID != theme.DefaultID || !v2.VisitorOverride {
		t.Errorf("访客主题未生效: id=%q override=%v", v2.ID, v2.VisitorOverride)
	}
	if v2.ListMode != "card" {
		t.Errorf("换主题后列表形态应回到新默认 card，实际 %q", v2.ListMode)
	}
	// 主题条应给出"恢复默认"的出路
	if !strings.Contains(renderBody(t, h, req2), "/pick/default") {
		t.Error("访客覆盖站点主题时，主题条应有「恢复默认」入口")
	}
}

// renderBody 渲染一次页面并返回完整 HTML。
func renderBody(t *testing.T, h *Hub, req *http.Request) string {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("页面返回 %d", w.Code)
	}
	return w.Body.String()
}

func TestThemeManifestIsPublicAndClean(t *testing.T) {
	h, _ := newThemeTestHub(t)
	req := httptest.NewRequest(http.MethodGet, "/theme.json", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("theme.json 应公开可取，实际 %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("Content-Type = %q", ct)
	}
	body := w.Body.String()
	if !strings.Contains(body, `"id"`) {
		t.Error("返回内容不是 JSON 清单")
	}
	// 运行时字段不该泄漏到分享出去的文件里
	for _, leaked := range []string{"Swatches", "\"Builtin\"", "\"Source\""} {
		if strings.Contains(body, leaked) {
			t.Errorf("导出内容混进了运行时字段 %s", leaked)
		}
	}
}

func TestThemeImportRejectsHostileContent(t *testing.T) {
	h, _ := newThemeTestHub(t)
	cookies := loginAsAdmin(t, h)

	post := func(body string) string {
		form := url.Values{"manifest": {body}}.Encode()
		req := withCookies(httptest.NewRequest(http.MethodPost, "/admin/themes/import",
			strings.NewReader(form)), cookies)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w.Header().Get("Location")
	}

	hostile := `{"schemaVersion":1,"id":"evil.x","name":"恶意","version":"1.0.0","author":"e",
	"tokens":{"--kokoro-color-bg":"red; background:url(https://evil.example/x)"},
	"layoutSchemaVersion":1,"layout":{}}`
	if loc := post(hostile); !strings.Contains(loc, "msg=") {
		t.Error("含注入的主题应被拒绝并给出提示")
	}
	if h.themes.Get("evil.x") != nil {
		t.Error("被拒绝的主题不应进入注册表")
	}

	fake := `{"schemaVersion":1,"id":"kokoro.official-fake","name":"假","version":"1.0.0","author":"e",
	"tokens":{"--kokoro-color-bg":"#fff"},"layoutSchemaVersion":1,"layout":{}}`
	post(fake)
	if h.themes.Get("kokoro.official-fake") != nil {
		t.Error("冒用官方前缀的主题不应被接受")
	}
}

func TestThemeGrabRequiresAdminAndHTTPURL(t *testing.T) {
	h, _ := newThemeTestHub(t)

	// 未登录
	req := httptest.NewRequest(http.MethodPost, "/admin/themes/grab",
		strings.NewReader("url=https://example.com"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("未登录抓取应 401，实际 %d", w.Code)
	}

	cookies := loginAsAdmin(t, h)
	for _, bad := range []string{"file:///etc/passwd", "ftp://x/y", "javascript:alert(1)"} {
		form := "url=" + url.QueryEscape(bad)
		req = withCookies(httptest.NewRequest(http.MethodPost, "/admin/themes/grab",
			strings.NewReader(form)), cookies)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w = httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code == http.StatusSeeOther &&
			!strings.Contains(w.Header().Get("Location"), "msg=") {
			t.Errorf("地址 %q 应被拒绝", bad)
		}
	}
}

func TestNormalizeThemeURL(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://example.com/", "https://example.com/theme.json"},
		{"https://example.com", "https://example.com/theme.json"},
		{"http://a.b/sub/", "http://a.b/sub/theme.json"},
		{"https://example.com/theme.json", "https://example.com/theme.json"},
	}
	for _, c := range cases {
		got, err := normalizeThemeURL(c.in)
		if err != nil {
			t.Errorf("%s: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s → %q，应为 %q", c.in, got, c.want)
		}
	}
	for _, bad := range []string{"", "example.com", "file:///x", "javascript:alert(1)"} {
		if _, err := normalizeThemeURL(bad); err == nil {
			t.Errorf("%q 应被拒绝", bad)
		}
	}
}

// TestGrabThemeFromPeerSite 模拟"一键获取"：本地起一个只提供 theme.json 的假站。
func TestGrabThemeFromPeerSite(t *testing.T) {
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/theme.json" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"schemaVersion":1,"id":"peer.aurora","name":"极光",
			"version":"2.1.0","author":"someone",
			"tokens":{"--kokoro-color-primary":"#00d0a0","--kokoro-color-bg":"#04121a"},
			"layoutSchemaVersion":1,"layout":{"home":{"list":{"mode":"table"}}}}`))
	}))
	defer peer.Close()

	h, _ := newThemeTestHub(t)
	m, _, err := h.grabTheme(peer.URL)
	if err != nil {
		t.Fatalf("一键获取失败: %v", err)
	}
	if m.Name != "极光" || m.Version != "2.1.0" {
		t.Errorf("元数据不对: %s %s", m.Name, m.Version)
	}
	if h.themes.Get("peer.aurora") == nil {
		t.Error("获取到的主题应进入注册表")
	}
	h.setActiveTheme("peer.aurora")
	if got := h.ActiveThemeID(); got != "peer.aurora" {
		t.Errorf("启用后当前主题 = %q", got)
	}
}

// TestGrabThemeRejectsMaliciousSite 对方返回带注入的主题时必须拒绝。
func TestGrabThemeRejectsMaliciousSite(t *testing.T) {
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"schemaVersion":1,"id":"evil.theme","name":"坏","version":"1.0.0",
			"author":"e","tokens":{"--kokoro-color-bg":"#fff}body{display:none"},
			"layoutSchemaVersion":1,"layout":{}}`))
	}))
	defer peer.Close()

	h, _ := newThemeTestHub(t)
	if _, _, err := h.grabTheme(peer.URL); err == nil {
		t.Fatal("恶意主题应被拒绝")
	}
	if h.themes.Get("evil.theme") != nil {
		t.Error("被拒的主题不应进入注册表")
	}
}

func TestThemeDeleteRoutes(t *testing.T) {
	h, _ := newThemeTestHub(t)
	cookies := loginAsAdmin(t, h)

	// 内置主题删不掉
	form := "id=" + "kokoro.daylight"
	req := withCookies(httptest.NewRequest(http.MethodPost, "/admin/themes/delete",
		strings.NewReader(form)), cookies)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if h.themes.Get("kokoro.daylight") == nil {
		t.Error("内置主题被删掉了")
	}
	if !strings.Contains(w.Header().Get("Location"), "msg=") {
		t.Error("删除内置主题应给出提示")
	}

	// 自定义主题能删，且删的是当前主题时自动回落默认
	if _, err := h.themes.Import([]byte(`{"schemaVersion":1,"id":"mine.t","name":"我的","version":"1.0.0",
		"author":"me","tokens":{"--kokoro-color-primary":"#123456"},
		"layoutSchemaVersion":1,"layout":{}}`)); err != nil {
		t.Fatal(err)
	}
	h.setActiveTheme("mine.t")
	form = "id=mine.t"
	req = withCookies(httptest.NewRequest(http.MethodPost, "/admin/themes/delete",
		strings.NewReader(form)), cookies)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	h.ServeHTTP(httptest.NewRecorder(), req)
	if h.themes.Get("mine.t") != nil {
		t.Error("自定义主题没删掉")
	}
	if h.ActiveThemeID() != theme.DefaultID {
		t.Errorf("删除当前主题后应回落默认，实际 %q", h.ActiveThemeID())
	}
}

func TestModeAndListModeCookies(t *testing.T) {
	h, _ := newThemeTestHub(t)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: theme.ModeCookie, Value: "dark"})
	if got := h.themeFor(req).Mode; got != "dark" {
		t.Errorf("k_mode=dark cookie 未生效，mode = %q", got)
	}

	// 不支持深色的主题（supportsDark=false）应忽略深色 cookie，
	// 否则页面会出现"按钮显示深色、实际渲染浅色"的错位。
	importTestTheme(t, h, "peer.lightonly",
		`{"schemaVersion":1,"id":"peer.lightonly","name":"仅浅色","version":"1.0.0",
		"author":"t","tokensSchemaVersion":2,"layoutSchemaVersion":1,
		"tokens":{"--kokoro-color-primary":"#123456"},
		"mode":{"default":"dark","supportsDark":false},
		"layout":{}}`)
	h.setActiveTheme("peer.lightonly")
	if got := h.themeFor(req).Mode; got != "light" {
		t.Errorf("不支持深色的主题应强制浅色，mode = %q", got)
	}

	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	req2.AddCookie(&http.Cookie{Name: listModeCookie, Value: "compact"})
	if got := h.themeFor(req2).ListMode; got != "compact" {
		t.Errorf("列表形态 cookie 未生效，%q", got)
	}
}

// getAttr 抓一次首页并取 body 上的某个 data-k-* 属性值。
func getAttr(t *testing.T, h *Hub, cookies []*http.Cookie, path, attr string) string {
	t.Helper()
	req := withCookies(httptest.NewRequest(http.MethodGet, path, nil), cookies)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	body := w.Body.String()
	const marker = "<body "
	i := strings.Index(body, marker)
	if i < 0 {
		t.Fatalf("页面里找不到 <body>")
	}
	seg := body[i:min(i+1200, len(body))]
	needle := attr + "=\""
	j := strings.Index(seg, needle)
	if j < 0 {
		return ""
	}
	rest := seg[j+len(needle):]
	k := strings.Index(rest, "\"")
	if k < 0 {
		return ""
	}
	return rest[:k]
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
