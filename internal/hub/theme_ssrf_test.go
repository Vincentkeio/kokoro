package hub

// SSRF 防护的回归测试。
//
// 这里的每一条断言背后都是一个真实的滥用路径，值得单独钉住：
// 抓主题是「让 Hub 按别人给的地址发请求」，不设防就是一台内网跳板。
//
// ⚠️ 策略已改为「回环默认拒绝 + 按主机名精确放行」。
// 早期版本放行整个 127.0.0.0/8，理由是「抓自己站是正常用法」——
// 但回环段是**这台机器上的所有服务**，不是「自己的站」。一个 SSRF 就能
// 把本机数据库、Docker 映射端口、任何本地管理面板读一遍。
// 现在只有 --domain 或后台白名单里的主机名才允许走回环。

import (
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// noLoopback 是「什么都不放行」的策略，用于测默认拒绝。
var noLoopback = themeLoopbackPolicy{}

func TestSafeThemeURLRejectsNonHTTP(t *testing.T) {
	for _, bad := range []string{
		"file:///etc/passwd",
		"ftp://x/y",
		"gopher://127.0.0.1:11211/_stats",
		"javascript:alert(1)",
		"data:text/plain,hello",
		"example.com/theme.json",
		"https://",
	} {
		if _, err := safeThemeURL(bad); err == nil {
			t.Errorf("%q 应被拒绝", bad)
		}
	}
	// URL 层只挡「一眼就不该放行」的写法。回环**不在这里拒**——
	// 它由拨号层按主机名放行，所以这里回环仍是放行的。
	for _, ok := range []string{
		"https://example.com/theme.json",
		"http://example.com/",
		"https://vps.mjfuns.lat/theme.json",
		"http://127.0.0.1:8799/theme.json",
		"http://localhost:8799/theme.json",
	} {
		if _, err := safeThemeURL(ok); err != nil {
			t.Errorf("%q 应放行，实际 %v", ok, err)
		}
	}
	// 字面量内网地址在这一层就该挡住，不该发出去。
	for _, bad := range []string{
		"http://169.254.169.254/latest/meta-data/",
		"http://10.0.0.1/theme.json",
		"http://192.168.0.1:8799/admin",
		"http://[fc00::1]/theme.json",
		"http://100.64.0.1/x",
	} {
		if _, err := safeThemeURL(bad); err == nil {
			t.Errorf("%q 字面量内网地址应在 URL 层就被拒绝", bad)
		}
	}
}

func TestForbiddenThemeIPs(t *testing.T) {
	// 这些必须拒：能直接打到内网服务或云元数据
	forbidden := []string{
		"169.254.169.254", // 云厂商元数据
		"10.0.0.5",
		"172.16.0.1",
		"192.168.1.1",
		"0.0.0.0",
		"224.0.0.1",  // 组播
		"100.64.0.1", // CGNAT
		"fc00::1",    // IPv6 ULA
		"fe80::1",    // IPv6 链路本地
		"::",
		// 回环默认也禁——它指的是「本机所有服务」，不是「本站」。
		// 想放行必须按主机名在拨号层显式声明。
		"127.0.0.1",
		"127.0.0.53",
		"::1",
	}
	for _, s := range forbidden {
		if !forbiddenThemeIP(net.ParseIP(s)) {
			t.Errorf("%s 应被拒绝", s)
		}
	}
	// 这些必须放行：公网地址
	allowed := []string{"1.1.1.1", "8.8.8.8", "203.0.113.9", "2001:4860:4860::8888"}
	for _, s := range allowed {
		if forbiddenThemeIP(net.ParseIP(s)) {
			t.Errorf("%s 应放行", s)
		}
	}
	if !forbiddenThemeIP(nil) {
		t.Error("nil IP 应视为不可用")
	}
}

func TestNormalizeThemeFetchHostsInput(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"  ", ""},
		{"a.com", "a.com"},
		{" a.com , b.com ", "a.com,b.com"},
		{"a.com,,b.com,", "a.com,b.com"}, // 空项与尾逗号
		{"a.com,a.com", "a.com"},
		// 去重是大小写不敏感的，跟 policy.allow 的比较方式一致——
		// A.COM 和 a.com 放行效果完全相同，留两条只会让界面看起来像有两条配置。
		{"a.com,A.COM,a.com", "a.com"},
	}
	for _, c := range cases {
		if got := normalizeThemeFetchHostsInput(c.in); got != c.want {
			t.Errorf("normalize(%q) = %q，应为 %q", c.in, got, c.want)
		}
	}
}

// TestThemeFetchPolicyReadsDomainAndSetting 确认白名单的两个来源都生效。
func TestThemeFetchPolicyReadsDomainAndSetting(t *testing.T) {
	// 来源一：--domain
	h, st := newThemeTestHubWithDomain(t, "vps.mjfuns.lat")
	p := h.themeFetchPolicy()
	if !p.allow("vps.mjfuns.lat") {
		t.Error("--domain 里的主机名应放行")
	}
	if p.allow("other.com") {
		t.Error("未声明的主机名不该放行")
	}

	// 来源二：settings 白名单
	if err := st.SetSetting(settingThemeFetchHosts, "mirror.example.com, 10.1.2.3"); err != nil {
		t.Fatalf("写入设置失败: %v", err)
	}
	p = h.themeFetchPolicy()
	if !p.allow("mirror.example.com") {
		t.Error("settings 里的主机名应放行")
	}
	if !p.allow("10.1.2.3") {
		t.Error("settings 里的 IP 字面量应放行")
	}
	// --domain 那条不能因为加了设置而丢掉
	if !p.allow("vps.mjfuns.lat") {
		t.Error("--domain 的放行不应被覆盖")
	}
}

// TestAdminSettingsSavesThemeFetchHosts 端到端走一遍后台表单。
func TestAdminSettingsSavesThemeFetchHosts(t *testing.T) {
	h, st := newThemeTestHub(t)
	cookies := loginAsAdmin(t, h)

	form := url.Values{
		"comment_enabled":   {"1"},
		"theme_fetch_hosts": {"  mirror.example.com ,, 127.0.0.1  "},
	}
	req := httptest.NewRequest(http.MethodPost, "/admin/settings", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("保存设置状态码 = %d，应为 303", rec.Code)
	}

	got, err := st.GetSetting(settingThemeFetchHosts)
	if err != nil {
		t.Fatalf("读取设置失败: %v", err)
	}
	if got != "mirror.example.com,127.0.0.1" {
		t.Errorf("存下的白名单 = %q，应已去空项去空格", got)
	}
	// 存进去的每一项都必须真的能被 allow 认出来。
	p := h.themeFetchPolicy()
	for _, want := range []string{"mirror.example.com", "127.0.0.1"} {
		if !p.allow(want) {
			t.Errorf("刚保存的 %q 应能通过白名单校验", want)
		}
	}
}

func TestParseThemeFetchHosts(t *testing.T) {
	got := parseThemeFetchHosts("vps.mjfuns.lat, https://other.example.com/x , 127.0.0.1:8799 , ,http")
	for _, want := range []string{"vps.mjfuns.lat", "other.example.com", "127.0.0.1"} {
		if !got[want] {
			t.Errorf("应解析出 %q，实际 got=%v", want, got)
		}
	}
	if got["http"] {
		t.Error("明显不是域名的碎片不该被收进来")
	}
	if len(got) != 3 {
		t.Errorf("解析结果应恰好 3 项，实际 %d: %v", len(got), got)
	}
}

// TestValidFetchHostname 把校验规则本身钉住。
//
// 错收一条白名单 = 把回环放开给一个意料之外的主机名，
// 所以宁可漏收也不能错收——这些断言里的 "no" 比 "yes" 更要紧。
func TestValidFetchHostname(t *testing.T) {
	yes := []string{
		"mjfuns.lat",
		"vps.mjfuns.lat",
		"a.b.c.example.com",
		"xn--fiqs8s.example",
		"localhost",
		"127.0.0.1",
		"10.0.0.5",
		"::1",
		"fc00::1",
	}
	for _, s := range yes {
		if !validFetchHostname(s) {
			t.Errorf("%q 应被接受", s)
		}
	}
	no := []string{
		"",                       // 空
		"http",                   // 手滑掉出来的碎片（早期版本的 bug）
		"abc",                    // 没有点
		"a b.com",                // 空格
		"a/b.com",                // 斜杠
		"a\\b.com",               // 反斜杠
		".mjfuns.lat",            // 前导点
		"mjfuns.lat.",            // 尾点（虽然前面 Trim 过，但规则本身要挡）
		"-mjfuns.lat",            // 前导连字符
		"mjfuns.lat-",            // 尾连字符
		"a@b.com",                // 含@
		"a b",                    //
		strings.Repeat("a", 300), // 超长
	}
	for _, s := range no {
		if validFetchHostname(s) {
			t.Errorf("%q 应被拒绝（错收 = 放开回环）", s)
		}
	}
}

func TestThemeLoopbackPolicyAllowIsExact(t *testing.T) {
	p := themeLoopbackPolicy{hosts: parseThemeFetchHosts("mjfuns.lat")}
	if !p.allow("mjfuns.lat") {
		t.Error("白名单里的主机名应放行")
	}
	if !p.allow("MJfuns.Lat") {
		t.Error("应大小写不敏感")
	}
	// 子域名不该跟着放行——否则 evil-mjfuns.lat 就能打本机回环。
	if p.allow("evil.mjfuns.lat") {
		t.Error("子域名不该因为父域在白名单里而放行（必须是精确匹配）")
	}
	if p.allow("mjfuns.lat.evil.com") {
		t.Error("后缀拼接必须拒绝")
	}
	if (themeLoopbackPolicy{}).allow("mjfuns.lat") {
		t.Error("空策略不该放行任何主机")
	}
}

// TestGrabThemeRejectsPrivateTarget 说明拨号层真的会拦下内网。
func TestGrabThemeRejectsPrivateTarget(t *testing.T) {
	h, _ := newThemeTestHub(t)
	for _, bad := range []string{
		"http://169.254.169.254/latest/meta-data/",
		"http://10.0.0.1/theme.json",
		"http://192.168.0.1:8799/admin",
		"http://[fc00::1]/theme.json",
	} {
		if _, _, err := h.grabTheme(bad); err == nil {
			t.Errorf("抓取内网地址 %q 应被拒绝", bad)
		}
	}
}

// TestGrabThemeRejectsLoopbackByDefault 是这次策略收紧的核心断言：
// 没有白名单时，连本机回环都必须拒绝——因为那是「本机的所有服务」。
func TestGrabThemeRejectsLoopbackByDefault(t *testing.T) {
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"schemaVersion":1,"id":"peer.loop","name":"回环",
			"version":"1.0.0","author":"me","tokensSchemaVersion":2,"layoutSchemaVersion":1,
			"tokens":{},"layout":{}}`))
	}))
	defer peer.Close()

	// 关键：Domain 留空 = 没有任何回环白名单。
	h, _ := newThemeTestHubWithDomain(t, "")
	if _, _, err := h.grabTheme(peer.URL); err == nil {
		t.Fatal("默认策略下抓本机回环应被拒绝（回环指向本机所有服务）")
	}
}

// TestGrabThemeAllowsLoopbackViaDomain 确认「抓自己站」这个正当用途
// 没被一起弄死：--domain 里声明了的主机名，走回环是放行的。
// 这就是反代架构下的真实情形（Nginx 在前、Go 在后）。
func TestGrabThemeAllowsLoopbackViaDomain(t *testing.T) {
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/theme.json" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"schemaVersion":1,"id":"peer.local","name":"自抓",
			"version":"1.0.0","author":"me","tokensSchemaVersion":2,"layoutSchemaVersion":1,
			"tokens":{"--kokoro-color-primary":"#00d0a0"},"layout":{}}`))
	}))
	defer peer.Close()

	// --domain 就是 httptest 的主机名。
	h, _ := newThemeTestHubWithDomain(t, "127.0.0.1")
	m, _, err := h.grabTheme(peer.URL)
	if err != nil {
		t.Fatalf("--domain 内的回环应放行，实际 %v", err)
	}
	if m.ID != "peer.local" {
		t.Errorf("导入的主题 = %q", m.ID)
	}
}

// TestGrabThemeAllowsLoopbackWhenAllowlisted 与上面是两条不同的路：
// 后台显式填白名单，而不是靠 --domain。
func TestGrabThemeAllowsLoopbackWhenAllowlisted(t *testing.T) {
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/theme.json" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"schemaVersion":1,"id":"peer.local","name":"自抓",
			"version":"1.0.0","author":"me","tokensSchemaVersion":2,"layoutSchemaVersion":1,
			"tokens":{"--kokoro-color-primary":"#00d0a0"},"layout":{}}`))
	}))
	defer peer.Close()

	// --domain 留空（不放行），改由 settings 白名单显式声明。
	h, st := newThemeTestHubWithDomain(t, "")
	if err := st.SetSetting(settingThemeFetchHosts, "127.0.0.1"); err != nil {
		t.Fatalf("写入白名单失败: %v", err)
	}
	m, _, err := h.grabTheme(peer.URL)
	if err != nil {
		t.Fatalf("白名单内的回环应放行，实际 %v", err)
	}
	if m.ID != "peer.local" {
		t.Errorf("导入的主题 = %q", m.ID)
	}
}

// TestGrabThemeRejectsRedirectToPrivate 守 redirect 那一跳：
// 对方先给一个正常响应，再用 302 把我们送去内网。每一跳都必须重新校验。
//
// 用 169.254.169.254 当样本：它是字面量内网，与回环策略无关，
// 无论白名单怎么配都必须被拒。
func TestGrabThemeRejectsRedirectToPrivate(t *testing.T) {
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://169.254.169.254/latest/meta-data/", http.StatusFound)
	}))
	defer peer.Close()

	h, _ := newThemeTestHub(t)
	if _, _, err := h.grabTheme(peer.URL); err == nil {
		t.Fatal("重定向到内网应报错")
	}
}

// TestRedirectGuardIsPerHop 直接断言 CheckRedirect 这个闭包的行为，
// 而不是靠「最终有没有抓到东西」间接推断——后者会因为中间任何一环
// 提前失败而假绿。
func TestRedirectGuardIsPerHop(t *testing.T) {
	client := newThemeGrabClient(0, noLoopback)
	mk := func(raw string) *http.Request {
		req, err := http.NewRequest(http.MethodGet, raw, nil)
		if err != nil {
			t.Fatalf("构造请求失败: %v", err)
		}
		return req
	}

	// 内网目标必须被拦
	for _, bad := range []string{
		"http://169.254.169.254/latest/meta-data/",
		"http://10.1.2.3/theme.json",
		"http://192.168.1.1/admin",
		"http://[fc00::1]/theme.json",
		"http://100.64.0.1/x",
		"file:///etc/passwd",
	} {
		if err := client.CheckRedirect(mk(bad), nil); err == nil {
			t.Errorf("重定向到 %q 应被拦", bad)
		}
	}
	// 公网目标必须放行
	for _, ok := range []string{"http://8.8.8.8/x", "https://example.com/theme.json"} {
		if err := client.CheckRedirect(mk(ok), nil); err != nil {
			t.Errorf("重定向到 %q 被误拦: %v", ok, err)
		}
	}
	// 重定向次数上限
	via := make([]*http.Request, 3)
	if err := client.CheckRedirect(mk("https://example.com/x"), via); err == nil {
		t.Error("超过 3 跳应被拦")
	}
}
