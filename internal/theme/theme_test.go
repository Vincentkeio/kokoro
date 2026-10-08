package theme

// 主题引擎的回归测试。
//
// 重点保护三件事：
//  1. 内置默认主题永远能解析（它是编译期常量，坏了要立刻发现）；
//  2. 主题**不能**借 tokens 或 layout 注入 CSS / 逃逸沙箱；
//  3. layout 差异是真的落到渲染产物里，而不只是数据存在。

import (
	"strings"
	"testing"
)

// want 是当前维护的内置主题清单。
//
// 目前**只有一套**内置默认主题 kokoro.daylight：界面方向还没定稿，
// 现在多写几套只是让「等定了再写新的」变成「等定了再删三套」。
//
// 需要多主题时往这个列表里加；下面所有「换主题是否真的换脸」的断言都
// 用现场构造的主题，不依赖具体内置主题，所以加主题不会牵动这些测试。
var want = []string{
	"kokoro.daylight",
}

func TestBuiltinThemesParse(t *testing.T) {
	r, err := NewRegistry()
	if err != nil {
		t.Fatalf("构造注册表失败: %v", err)
	}
	if got := r.ListIDs(); len(got) != len(want) {
		t.Fatalf("内置主题数量 = %d，期望 %d（%v）", len(got), len(want), got)
	}
	for _, id := range want {
		m := r.Get(id)
		if m == nil {
			t.Fatalf("缺少内置主题 %s", id)
		}
		if !m.Builtin || !m.BuiltinID() {
			t.Errorf("%s 应标记为内置", id)
		}
		if m.Name == "" || m.Author == "" {
			t.Errorf("%s 缺少 name/author", id)
		}
		if len(m.Tokens) < 20 {
			t.Errorf("%s 的 tokens 太少：%d 个", id, len(m.Tokens))
		}
	}
	// DefaultID 必须真的在注册表里，否则 Resolve("") 会回落到一个不存在的 id。
	if DefaultID != want[0] {
		t.Errorf("DefaultID = %q，与内置清单第一项 %q 不一致", DefaultID, want[0])
	}
	if r.Resolve("") == nil || r.Resolve("").ID != DefaultID {
		t.Error("Resolve(\"\") 应回落到默认主题")
	}
}

// mkTheme 按 JSON 造一份主题，走 Parse 这条真实链路。
//
// 测试里需要"另一套主题"时用它，而不是引用某套内置主题：内置清单会随
// 界面定稿增减，而「换主题必须连布局一起变」这条规则本身不该跟着变。
func mkTheme(t *testing.T, id, json string) *Manifest {
	t.Helper()
	m, err := Parse([]byte(json))
	if err != nil {
		t.Fatalf("构造主题 %s 失败: %v", id, err)
	}
	return m
}

// altTheme 是与默认主题形态刻意相反的那套：深色 + 表格 + line 图表 + 实心顶栏。
const altTheme = `{
	"schemaVersion":1,"id":"test.alt","name":"对照","version":"1.0.0",
	"author":"t","tokensSchemaVersion":2,"layoutSchemaVersion":1,
	"tokens":{"--kokoro-color-primary":"#5b8cff"},
	"tokensDark":{"--kokoro-color-bg":"#0d1117"},
	"mode":{"default":"dark","supportsDark":true,"allowUserSwitch":true},
	"layout":{
		"shell":{"header":{"variant":"solid"}},
		"home":{"list":{"mode":"table"}},
		"misc":{"statusDotStyle":"glow","usageBarStyle":"gradient"},
		"charts":{"type":"line","grid":true}
	}}`

// TestThemeSwitchChangesLayout 是核心断言：换主题必须连布局一起变，
// 否则「主题可玩性丰富」这个需求就退化成了换配色。
func TestThemeSwitchChangesLayout(t *testing.T) {
	r, err := NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	base := r.Get(DefaultID)
	alt := mkTheme(t, "test.alt", altTheme)

	if base.Layout.Home.List.Mode != "card" {
		t.Errorf("默认主题列表形态 = %q，应为 card", base.Layout.Home.List.Mode)
	}
	if alt.Layout.Home.List.Mode != "table" {
		t.Errorf("对照主题列表形态 = %q，应为 table", alt.Layout.Home.List.Mode)
	}
	if alt.Layout.Charts.Type != "line" {
		t.Errorf("对照主题图表类型 = %q，应为 line", alt.Layout.Charts.Type)
	}
	// 默认主题是浅色系（auto 或 light 都算），不该默认深色。
	if base.Mode.Default == "dark" {
		t.Errorf("默认主题默认模式 = %q，不该是 dark", base.Mode.Default)
	}
	if alt.Mode.Default != "dark" {
		t.Errorf("对照主题默认模式 = %q，应为 dark", alt.Mode.Default)
	}
	// 主色必须不同，否则「换主题」看起来只是换了个底色。
	if base.Tokens["--kokoro-color-primary"] == alt.Tokens["--kokoro-color-primary"] {
		t.Error("两套主题的主色相同，切换后看不出区别")
	}
	if !base.SupportsDark() {
		t.Error("默认主题应支持深色（tokensDark 非空）")
	}
	// 渲染产物层面再确认一次：tokens 和 layout 都不能只停留在 JSON 里。
	if RenderTokens(base) == RenderTokens(alt) {
		t.Error("两套主题渲染出的 tokens CSS 完全相同")
	}
	if RenderLayoutCSS(base) == RenderLayoutCSS(alt) {
		t.Error("两套主题渲染出的 layout CSS 完全相同")
	}
	// 列表形态要真的进了 CSS 变量，否则切换后布局不变。
	if !strings.Contains(RenderTokens(base), "--k-list-mode:card") {
		t.Error("默认主题的列表形态没进 CSS 变量")
	}
	if !strings.Contains(RenderTokens(alt), "--k-list-mode:table") {
		t.Error("对照主题的列表形态没进 CSS 变量")
	}
}

// TestRenderTokensSparesDark 验证深色是稀疏覆盖：只输出声明过的键。
func TestRenderTokensSparesDark(t *testing.T) {
	r, _ := NewRegistry()
	css := RenderTokens(r.Get("kokoro.daylight"))
	darkStart := strings.Index(css, `[data-k-mode="dark"]{`)
	if darkStart < 0 {
		t.Fatal("没输出深色变量块")
	}
	rootStart := strings.Index(css, ":root{")
	lightBlock := css[rootStart:darkStart]
	darkBlock := css[darkStart:]
	// 深色块里应该只出现少数几个键（daylight 只声明了几个）。
	if n := strings.Count(darkBlock, "--kokoro-"); n > 40 {
		t.Errorf("深色覆盖块有 %d 个变量，稀疏覆盖失效", n)
	}
	if !strings.Contains(lightBlock, "--kokoro-color-primary:#2f6feb") {
		t.Error("浅色块应包含 daylight 的主色")
	}
	if !strings.Contains(darkBlock, "--kokoro-color-bg:#0d1117") {
		t.Error("深色块应覆盖背景色")
	}
	// 声明 supportsDark=false 的主题不该渲染深色块。
	// 这里现造一份而不是引用某个内置主题：内置清单会随界面定稿增减，
	// 而「仅浅色主题不渲染深色块」这条规则本身不该跟着变。
	lightOnly := &Manifest{
		SchemaVersion:       ManifestVersion,
		ID:                  "test.light-only",
		Name:                "仅浅色",
		Version:             "1.0.0",
		TokensSchemaVersion: TokensSchemaVersion,
		Tokens:              map[string]string{"--kokoro-color-primary": "#123456"},
		Mode:                Mode{SupportsDark: boolPtr(false)},
		LayoutSchemaVersion: LayoutSchemaVersion,
	}
	lightOnlyCSS := RenderTokens(lightOnly)
	if strings.Contains(lightOnlyCSS, `[data-k-mode="dark"]`) {
		t.Error("supportsDark=false 时不应渲染深色块")
	}
}

// ---- 安全 ----

func TestTokensRejectsInjection(t *testing.T) {
	cases := []struct {
		name  string
		token string
		val   string
	}{
		{"声明终止符", "--kokoro-color-primary", "#fff; background:url(https://evil.example/x)"},
		{"花括号开规则", "--kokoro-color-bg", "red} body{display:none"},
		{"远程图片", "--kokoro-bg-image", "url(https://evil.example/bg.png)"},
		{"远程字体", "--kokoro-font-sans", "url(https://evil.example/f.woff)"},
		{"at-import", "--kokoro-font-sans", "@import url(https://evil.example/x.css)"},
		{"未知变量", "--kokoro-evil-key", "red"},
		{"非 kokoro 命名空间", "--evil", "red"},
		{"非法颜色", "--kokoro-color-primary", "javascript:alert(1)"},
		{"非法长度", "--kokoro-radius-md", "8px; red"},
		{"注释注入", "--kokoro-color-bg", "red /* x */"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := ValidateTokens(map[string]string{c.token: c.val}, "tokens")
			if err == nil {
				t.Fatalf("应该拒绝 %s = %q", c.token, c.val)
			}
		})
	}
}

func TestTokensAcceptsLegitValues(t *testing.T) {
	ok := map[string]string{
		"--kokoro-color-primary":    "#2f6feb",
		"--kokoro-color-bg":         "rgba(0,0,0,.35)",
		"--kokoro-color-text":       "var(--kokoro-color-bg-subtle)",
		"--kokoro-color-border":     "color-mix(in srgb, #fff 60%, #000)",
		"--kokoro-radius-md":        "14px",
		"--kokoro-font-sans":        "Georgia, 'Songti SC', serif",
		"--kokoro-chart-line-width": "1.25",
		"--kokoro-transition-fast":  "0ms",
		"--kokoro-chart-series-1":   "#7c5cff",
		"--kokoro-shadow-sm":        "0 1px 2px rgba(31,35,40,.08)",
		"--kokoro-hero-gradient":    "linear-gradient(135deg, #7c5cff, #22d3ee)",
		"--kokoro-bg-image":         "none",
		"--kokoro-easing":           "cubic-bezier(.2,0,0,1)",
	}
	if err := ValidateTokens(ok, "tokens"); err != nil {
		t.Fatalf("合法值被误拒: %v", err)
	}
}

// TestLocalAssetURLAllowed 确认本地主题资源路径仍被放行——
// 一键获取主题时作者的 css 可能引用自己的图片，不能一刀切禁掉 url()。
func TestLocalAssetURLAllowed(t *testing.T) {
	err := ValidateTokens(map[string]string{
		"--kokoro-bg-image": "url(/_theme-assets/kokoro.mine/2/css/bg.png)",
	}, "tokens")
	if err != nil {
		t.Fatalf("本地主题资源应放行: %v", err)
	}
	// 但 data: 之外的协议不行，即使是相对路径里带 javascript:
	err = ValidateTokens(map[string]string{
		"--kokoro-bg-image": "url(javascript:alert(1))",
	}, "tokens")
	if err == nil {
		t.Fatal("javascript: 应被拒绝")
	}
}

func TestLayoutRejectsUnknownEnumAndUnknownKey(t *testing.T) {
	base := func() *Layout {
		return &Layout{}
	}
	l := base()
	l.Home.List.Mode = "grid" // 不在枚举里
	if err := ValidateLayout(l); err == nil {
		t.Error("非法列表模式应被拒绝")
	}
	l = base()
	l.Detail.Header.Variant = "hero"
	l.Detail.SectionGap = "48px; color:red"
	if err := ValidateLayout(l); err == nil {
		t.Error("含分号的长度值应被拒绝")
	}
}

func TestParseRejectsUnknownField(t *testing.T) {
	raw := `{"schemaVersion":1,"id":"x.bad","name":"x","version":"1.0.0","author":"a",
	"tokens":{"--kokoro-color-primary":"#fff"},"layoutSchemaVersion":1,"layout":{},
	"totallyUnknownKey":123}`
	if _, err := Parse([]byte(raw)); err == nil {
		t.Fatal("未知顶层字段应被拒绝（避免拼写错误静默失效）")
	}
}

func TestRegistryRegisterAndUnregister(t *testing.T) {
	r, _ := NewRegistry()
	custom := `{"schemaVersion":1,"id":"mine.paper2","name":"我改的纸感","version":"1.0.0",
	"author":"boss","tokens":{"--kokoro-color-primary":"#123456"},
	"layoutSchemaVersion":1,"layout":{"home":{"list":{"mode":"compact"}}}}`
	m, err := r.Import([]byte(custom))
	if err != nil {
		t.Fatalf("导入自定义主题失败: %v", err)
	}
	if m.Source != "custom" || m.Builtin {
		t.Error("自定义主题的 source 应为 custom")
	}
	if r.Resolve("mine.paper2").ID != "mine.paper2" {
		t.Error("Resolve 应能取到自定义主题")
	}
	// 内置主题不可删。
	if err := r.Unregister("kokoro.daylight"); err == nil {
		t.Error("内置主题不应允许删除")
	}
	if err := r.Unregister("mine.paper2"); err != nil {
		t.Errorf("删除自定义主题失败: %v", err)
	}
	// 未知 id 回落到默认主题，不返回 nil。
	if r.Resolve("does.not.exist").ID != DefaultID {
		t.Error("未知主题应回落到默认主题")
	}
}

func TestImportRejectsOfficialPrefixHijack(t *testing.T) {
	r, _ := NewRegistry()
	// 社区包不许冒用官方前缀。
	bad := `{"schemaVersion":1,"id":"kokoro.fake","name":"假官方","version":"1.0.0",
	"author":"evil","tokens":{"--kokoro-color-primary":"#f00"},
	"layoutSchemaVersion":1,"layout":{}}`
	if _, err := r.Import([]byte(bad)); err == nil {
		t.Fatal("冒用 kokoro. 前缀应被拒绝")
	}
}

// TestOneClickImportMirrorsSource 是「看到别人探针的主题一键获取」的核心逻辑单测：
// 从远程站点拉 theme.json，校验后登记。
func TestOneClickImportMirrorsSource(t *testing.T) {
	r, _ := NewRegistry()
	raw := `{"schemaVersion":1,"id":"mirror.aurora","name":"极光","version":"2.1.0",
	"author":"someone","description":"从别的站抄的",
	"tokens":{"--kokoro-color-primary":"#00d0a0","--kokoro-color-bg":"#04121a"},
	"layoutSchemaVersion":1,"layout":{"home":{"list":{"mode":"table"}}}}`
	m, err := r.Import([]byte(raw))
	if err != nil {
		t.Fatalf("导入失败: %v", err)
	}
	if m.Name != "极光" || m.Version != "2.1.0" {
		t.Errorf("元数据没解析对: %s %s", m.Name, m.Version)
	}
	if m.Layout.Home.List.Mode != "table" {
		t.Error("layout 没解析")
	}
	if len(m.Swatches) == 0 {
		t.Error("应提取出用于后台预览的主色")
	}
	// 导入的主题与内置主题走同一条校验路径：非法 token 一律拒绝。
	badRaw := strings.Replace(raw, `"#00d0a0"`, `"red; background:url(https://evil.example/x)"`, 1)
	if _, err := r.Import([]byte(badRaw)); err == nil {
		t.Fatal("一键获取的主题也必须过 token 校验")
	}
}

func TestBodyAttrsAndLayoutCSS(t *testing.T) {
	// 用与默认主题形态相反的那套来验证「layout 落到 body 属性」。
	// 不用内置主题：它会被界面定稿反复改，而这里的断言要稳定。
	m := mkTheme(t, "test.alt", altTheme)
	attrs := BodyAttrs(m, "dark", "table")
	if attrs["data-k-list-mode"] != "table" {
		t.Errorf("列表模式属性 = %q", attrs["data-k-list-mode"])
	}
	if attrs["data-k-dot"] != "glow" || attrs["data-k-bar"] != "gradient" {
		t.Errorf("misc 没落到属性: dot=%q bar=%q", attrs["data-k-dot"], attrs["data-k-bar"])
	}
	if attrs["data-k-header"] != "solid" {
		t.Errorf("顶栏形态 = %q", attrs["data-k-header"])
	}
	if attrs["data-k-chart-type"] != "line" {
		t.Errorf("图表类型属性 = %q", attrs["data-k-chart-type"])
	}
	if attrs["data-k-chart-grid"] != "" {
		t.Errorf("grid:true 是默认值，不该输出 data-k-chart-grid，实际 %q",
			attrs["data-k-chart-grid"])
	}
	// 曾经踩过的坑：RenderLayoutCSS 写了 [data-k-chart-grid="0"] 的规则，
	// BodyAttrs 却从不输出这个属性，于是 "grid": false 看着生效了其实没有。
	noGrid := mkTheme(t, "test.nogrid", `{
		"schemaVersion":1,"id":"test.nogrid","name":"无网格","version":"1.0.0",
		"author":"t","tokensSchemaVersion":2,"layoutSchemaVersion":1,
		"tokens":{"--kokoro-color-primary":"#123456"},
		"layout":{"charts":{"grid":false}}}`)
	if BodyAttrs(noGrid, "light", "card")["data-k-chart-grid"] != "0" {
		t.Error(`grid:false 应输出 data-k-chart-grid="0"`)
	}
	if !strings.Contains(RenderLayoutCSS(noGrid), `[data-k-chart-grid="0"]`) {
		t.Error("grid:false 应生成隐藏网格线的 CSS 规则")
	}
	// alt 主题没开 Hero，所以不该出现 data-k-hero-on。
	if _, ok := attrs["data-k-hero-on"]; ok {
		t.Error("未启用 Hero 的主题不应有 data-k-hero-on")
	}
	s := AttrsString(attrs)
	if !strings.Contains(s, `data-k-mode="dark"`) {
		t.Error("属性串应含 mode")
	}

	// 现造一份「开 Hero + blur 顶栏 + hero 详情页头」的主题，
	// 验证这些只有枚举值、没有内置主题覆盖的分支也走得通。
	// 用 JSON 解析而不是手搓结构体：嵌套匿名结构体写起来啰嗦且容易漏字段，
	// 而这里要验的正是「JSON → 结构体 → 属性/CSS」这条真实链路。
	exotic := mkTheme(t, "test.exotic", `{
		"schemaVersion":1,"id":"test.exotic","name":"花活","version":"1.0.0",
		"author":"t","tokensSchemaVersion":2,"layoutSchemaVersion":1,
		"tokens":{"--kokoro-color-primary":"#7c5cff"},
		"mode":{"default":"dark","supportsDark":true,"allowUserSwitch":true},
		"layout":{
			"shell":{"header":{"variant":"blur"}},
			"home":{"hero":{"enabled":true,"variant":"gradient"}},
			"detail":{"header":{"variant":"hero"}}
		}}`)

	exAttrs := BodyAttrs(exotic, "dark", "card")
	if exAttrs["data-k-hero-on"] != "1" {
		t.Error("开了 Hero 却没有 data-k-hero-on")
	}
	if exAttrs["data-k-hero"] != "gradient" {
		t.Errorf("Hero 形态 = %q", exAttrs["data-k-hero"])
	}
	if exAttrs["data-k-detail-header"] != "hero" {
		t.Errorf("详情页头形态 = %q", exAttrs["data-k-detail-header"])
	}
	css := RenderLayoutCSS(exotic)
	if !strings.Contains(css, `[data-k-header="blur"]`) {
		t.Error("blur 顶栏应生成规则")
	}
	if !strings.Contains(css, `[data-k-hero="gradient"]`) {
		t.Error("gradient Hero 应生成规则")
	}
}

// TestLayoutVarsCarryCardAndChartSizing 验证 layout 里的尺寸与档位
// 真的落成了 --k-* 变量，而不是只停留在 JSON 里没人读。
func TestLayoutVarsCarryCardAndChartSizing(t *testing.T) {
	m := mkTheme(t, "test.sizing", `{
		"schemaVersion":1,"id":"test.sizing","name":"尺寸","version":"1.0.0",
		"author":"t","tokensSchemaVersion":2,"layoutSchemaVersion":1,
		"tokens":{"--kokoro-color-primary":"#123456","--kokoro-glow":"0 0 24px rgba(124,92,255,.45)"},
		"layout":{
			"shell":{"contentMaxWidth":"1100px"},
			"home":{"list":{"mode":"card","card":{"minWidth":"340px","shadow":"glow"}}},
			"detail":{"sectionGap":"48px"},
			"charts":{"height":"240px","heightDetail":"360px"}
		}}`)
	css := RenderTokens(m)
	for _, want := range []string{
		"--k-card-min:340px",
		"--k-maxw:1100px",
		"--k-chart-h:240px",
		"--k-chart-h-detail:360px",
		"--k-section-gap:48px",
		"--k-list-mode:card",
		// shadow:"glow" 必须映射到 glow token，而不是回落到默认阴影。
		"--k-card-shadow:var(--kokoro-glow)",
	} {
		if !strings.Contains(css, want) {
			t.Errorf("layout 变量缺少 %s", want)
		}
	}
	// glow 档位要真的用上主题声明的那条阴影值。
	if !strings.Contains(css, "--kokoro-glow:0 0 24px rgba(124, 92, 255, .45)") &&
		!strings.Contains(css, "--kokoro-glow:0 0 24px rgba(124,92,255,.45)") {
		t.Error("glow token 的值没有原样输出")
	}
}

// TestHeroVariantIsSeparateAttribute 守住一个已经踩过的坑：
// Hero 的"启用"与"变体"曾经被拼成一个属性值（on:gradient），
// 结果 CSS 选择器匹配不上，主题里的 Hero 形同虚设。
//
// 内置主题默认不开 Hero，所以这里现造三份来覆盖三个分支。
func TestHeroVariantIsSeparateAttribute(t *testing.T) {
	mk := func(id, variant string, enabled bool) *Manifest {
		t.Helper()
		return mkTheme(t, id, `{"schemaVersion":1,"id":"`+id+`","name":"H","version":"1.0.0",
			"author":"t","tokensSchemaVersion":2,"layoutSchemaVersion":1,
			"tokens":{"--kokoro-color-primary":"#7c5cff"},
			"layout":{"home":{"hero":{"enabled":`+boolStr(enabled)+`,"variant":"`+variant+`"}}}}`)
	}

	a := BodyAttrs(mk("test.hero-gradient", "gradient", true), "dark", "card")
	if a["data-k-hero"] != "gradient" {
		t.Errorf("Hero 变体属性 = %q，应为 gradient", a["data-k-hero"])
	}
	if a["data-k-hero-on"] != "1" {
		t.Error("启用了 Hero，应输出 data-k-hero-on=1")
	}
	p := BodyAttrs(mk("test.hero-plain", "plain", true), "light", "card")
	if p["data-k-hero"] != "plain" || p["data-k-hero-on"] != "1" {
		t.Errorf("plain Hero 的属性 = %v", p)
	}
	d := BodyAttrs(mk("test.hero-off", "plain", false), "light", "card")
	if _, ok := d["data-k-hero-on"]; ok {
		t.Error("未启用 Hero 时不应有 data-k-hero-on")
	}
	// 内置主题同样默认不开 Hero。
	r, _ := NewRegistry()
	for _, id := range r.ListIDs() {
		if _, ok := BodyAttrs(r.Get(id), "light", "card")["data-k-hero-on"]; ok {
			t.Errorf("%s 默认不开 Hero，不应有 data-k-hero-on", id)
		}
	}
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
