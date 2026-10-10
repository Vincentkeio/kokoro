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
// loginCookie 登录并返回会话 cookie。
//
// ⚠️ 需要鉴权的接口在测试里**必须带上它**。不带的话拿到的是 401，
// 而很多断言（"拒绝非法输入"）会因为 401 而**碰巧成立** ——
// 测试全绿，但被测的那段逻辑一行都没跑到。
func loginCookie(t *testing.T, h *Hub, st *store.Store) *http.Cookie {
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
	return cs[0]
}

// adminBody 登录后台并返回渲染出来的 HTML。
func adminBody(t *testing.T, h *Hub, st *store.Store) string {
	t.Helper()
	sess := loginCookie(t, h, st)
	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	req.AddCookie(sess)
	w := httptest.NewRecorder()
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

	// 面板数是个哨兵：数量掉下来通常意味着某块被删了、或渲染提前中断。
	// 曾经是 8，2026-10-11 减到 7 —— 原来的「主题」页签里只有一块
	// 「抓主题的本机地址白名单」高级配置，跟主题管理毫不相干，
	// 已按 boss 要求撤掉；主题管理本身在独立页面 /admin/themes 上。
	panels := regexp.MustCompile(`<section class="panel"([^>]*)>`).FindAllStringSubmatch(body, -1)
	if len(panels) < 7 {
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
	// ⚠️ 下面这些是**故意删掉**的，别再加回来：
	//   #hubgeo      面板位置设置
	//   #theme-fetch 抓主题的本机地址白名单（2026-10-11 撤掉，
	//                主题管理改由页签链接直达 /admin/themes）
	// 这个断言就是用来发现"某个锚点悄悄消失"的，删是有意为之。
	for _, id := range []string{"profile", "comments", "alerts", "account"} {
		if !strings.Contains(body, `id="`+id+`"`) {
			t.Errorf("老锚点 #%s 的 id 丢了，老链接会失效", id)
		}
	}
	// 前端那个"hash 也能是区块 id"的分支得留着
	if !strings.Contains(body, "app.js") {
		t.Error("后台没引 app.js，页签切换不会工作")
	}
}

// TestAdminThemesTabLinksToPage 「主题」页签必须是通向 /admin/themes 的链接。
//
// 它曾经是个普通页签（data-goto="theme"），而那里面**只有**一块
// 「抓主题的本机地址白名单」高级配置 —— 点进去看到的就是这么个跟主题管理
// 毫不相干的东西，任谁都会以为主题功能没了。（boss 2026-10-11 就是这么反馈的。）
// 真正完整的管理页在 /admin/themes：卡片列表 + 启用/导出/删除 + 一键获取
// （站点地址 / theme.json / GitHub）+ 导入。
func TestAdminThemesTabLinksToPage(t *testing.T) {
	h, st := newTestHub(t)
	body := adminBody(t, h, st)

	if !strings.Contains(body, `<a class="atab" href="/admin/themes">主题</a>`) {
		t.Error("「主题」页签应该是指向 /admin/themes 的链接")
	}
	// 不能同时又是个页签 —— 那会多出一块永远空着的面板。
	if strings.Contains(body, `data-goto="theme"`) {
		t.Error("「主题」不该再有 data-goto：它是链接，不是页签")
	}
	// 那个高级配置整块都该没了。
	for _, gone := range []string{"theme_fetch_hosts", "本机地址白名单"} {
		if strings.Contains(body, gone) {
			t.Errorf("后台不该再出现 %q", gone)
		}
	}
}

// TestAdminNodeTableColumnsMatch 节点表格的表头列数要和单元格数一致。
//
// 删列时最容易出的错：删了 <th> 忘了删 <td>（或反过来），
// 表格就会整行错位 —— 而且看起来像"数据串了"，很难往删列上想。
func TestAdminNodeTableColumnsMatch(t *testing.T) {
	h, st := newTestHub(t)
	mkNode(t, st, "东京 zouter", "JP", "日本 · 东京", "")
	body := adminBody(t, h, st)

	// 找到节点表格
	i := strings.Index(body, "节点（")
	if i < 0 {
		t.Fatal("没找到节点区块")
	}
	seg := body[i:]

	head := regexp.MustCompile(`<thead><tr>(.*?)</tr></thead>`).FindStringSubmatch(seg)
	if head == nil {
		t.Fatal("节点表格没有表头")
	}
	ths := regexp.MustCompile(`<th>`).FindAllString(head[1], -1)
	t.Logf("表头 %d 列", len(ths))

	// 数全部单元格 —— 它必须是列数的整数倍。
	// （不逐行匹配：tbody 里的内容跨行，单行正则会漏掉。）
	rows := seg[strings.Index(seg, "<tbody>"):]
	if j := strings.Index(rows, "</tbody>"); j > 0 {
		rows = rows[:j]
	}
	tds := regexp.MustCompile(`<td>`).FindAllString(rows, -1)
	if len(tds) == 0 {
		t.Fatal("节点表格没有数据行")
	}
	if len(tds)%len(ths) != 0 {
		t.Errorf("表头 %d 列，但单元格总数 %d 不是它的整数倍 —— 表格会错位",
			len(ths), len(tds))
	}
	t.Logf("单元格 %d 个 = %d 行", len(tds), len(tds)/len(ths))

	// 「可见性」下拉已删（展示开关本身就管公开/不公开）
	if strings.Contains(seg, `name="visibility"`) {
		t.Error("节点表里不该再有可见性下拉 —— 展示开关已经管了这件事")
	}
}
