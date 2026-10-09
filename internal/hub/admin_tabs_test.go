package hub

// 后台选项卡：每个区块都要归到某个页签，且页签要真的存在。

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/Vincentkeio/kokoro/internal/store"
)

// adminBody 登录后台并返回渲染出来的 HTML。
//
// ⚠️ 必须带会话 —— 不带的话 handleAdmin 返回的是**登录页**，
// 里面一个 panel 都没有，测试会以"面板数只有 0"这种很误导的形式红。
func adminBody(t *testing.T, h *Hub, st *store.Store) string {
	t.Helper()
	if err := SetAdminCredentials(st, "boss", "s3cret-pass"); err != nil {
		t.Fatalf("设置账号失败: %v", err)
	}
	form := url.Values{"username": {"boss"}, "password": {"s3cret-pass"}}
	req := httptest.NewRequest(http.MethodPost, "/admin", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.handleAdmin(w, req)
	cs := w.Result().Cookies()
	if len(cs) == 0 {
		t.Fatal("登录没拿到会话 cookie")
	}
	req = httptest.NewRequest(http.MethodGet, "/admin", nil)
	req.AddCookie(cs[0])
	w = httptest.NewRecorder()
	h.handleAdmin(w, req)
	return w.Body.String()
}

// TestAdminEveryPanelHasTab 后台每块面板都要挂 data-atab。
//
// 漏挂的表现很隐蔽：那块面板**永远不显示**（CSS 默认 display:none），
// 而且不报错 —— 站长只会觉得"某个设置项找不到了"。
func TestAdminEveryPanelHasTab(t *testing.T) {
	h, st := newTestHub(t)
	n := mkNode(t, st, "东京 zouter", "JP", "日本 · 东京", "")
	if err := st.SetSetting(settingHubLat, "34.05"); err != nil {
		t.Fatal(err)
	}
	_ = n
	body := adminBody(t, h, st)

	panels := regexp.MustCompile(`<section class="panel"([^>]*)>`).FindAllStringSubmatch(body, -1)
	if len(panels) < 8 {
		t.Fatalf("后台面板数只有 %d，是不是渲染出问题了", len(panels))
	}
	for _, p := range panels {
		if !strings.Contains(p[1], "data-atab=") {
			t.Errorf("有面板没挂 data-atab，它会永远不显示: %s", p[0])
		}
	}

	// 页签栏要在
	if !strings.Contains(body, `class="atabs"`) {
		t.Error("后台缺少选项卡栏")
	}
}

// TestAdminTabBarCoversEveryTab 页签栏要把所有用到的页签都列出来。
//
// 少列一个的后果：那组面板**没有任何入口**能切过去。
func TestAdminTabBarCoversEveryTab(t *testing.T) {
	h, st := newTestHub(t)
	mkNode(t, st, "东京 zouter", "JP", "日本 · 东京", "")
	body := adminBody(t, h, st)

	// 页签栏里声明了哪些
	bar := regexp.MustCompile(`data-goto="([a-z]+)"`).FindAllStringSubmatch(body, -1)
	has := map[string]bool{}
	for _, m := range bar {
		has[m[1]] = true
	}
	if len(has) < 5 {
		t.Fatalf("页签只有 %d 个，太少", len(has))
	}

	// 面板实际用了哪些
	used := regexp.MustCompile(`data-atab="([a-z]+)"`).FindAllStringSubmatch(body, -1)
	for _, m := range used {
		if !has[m[1]] {
			t.Errorf("面板用了页签 %q，但页签栏里没有它 —— 那组面板没有入口能切过去", m[1])
		}
	}
}

// TestAdminOldAnchorsStillWork 老锚点（#profile 等）必须还能用。
//
// 「去填名片」按钮指向 /admin#profile，页签化之后不能让它失效。
func TestAdminOldAnchorsStillWork(t *testing.T) {
	h, st := newTestHub(t)
	mkNode(t, st, "东京 zouter", "JP", "日本 · 东京", "")
	body := adminBody(t, h, st)

	// 前端靠 id + data-atab 反查页签，所以这两样都得在
	for _, id := range []string{"profile", "comments", "alerts", "hubgeo", "account"} {
		if !strings.Contains(body, `id="`+id+`"`) {
			t.Errorf("老锚点 #%s 的 id 丢了，老链接会失效", id)
		}
	}
	// 前端那个"hash 也能是区块 id"的分支得留着
	if !strings.Contains(body, "app.js") {
		t.Error("后台没引 app.js，页签切换不会工作")
	}
}
