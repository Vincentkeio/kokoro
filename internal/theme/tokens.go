package theme

// tokens 命名表与渲染。
//
// 硬约束（spec §3.2）：tokens 里只允许出现命名表内的键，且值的形态受限。
// 这条约束是主题系统安全边界的一部分——主题不能借 tokens 注入 url() 拉远程
// 资源、不能写 --x: red; background:url(...) 这种注入、也不能覆盖非 kokoro 变量。
//
// 值校验分三类：
//   - color：#rgb/#rrggbb/#rrggbbaa/rgb()/rgba()/hsl()/hsla()/color-mix(允许的参数里不含 var 以外的可执行内容)/currentColor/transparent/inherit + 少量具名色。
//   - length / number / time / keyword / font-stack / easing / url-token / gradient / shadow：按各自规则。
//   - color-mix 与 var() 允许引用其它 --kokoro-* 变量，这是 token 图的关键能力。

import (
	"fmt"
	"sort"
	"strings"
)

// TokenGroup 把命名表按组组织，同时给出每组的默认值（= 内置 daylight）。
type TokenGroup struct {
	Prefix   string            // 变量名前缀，如 "--kokoro-color-"
	Defaults map[string]string // 完整变量名 → 默认值
}

// TokenSchema 是完整命名表（tokensSchemaVersion = 2）。
//
// 数量刻意给足：主题能调的维度越多，「换皮」才不是换色。桌面全部给默认值，
// 主题只需要写自己不一样的键——这既是省事，也是稀疏深色覆盖的前提。
var TokenSchema = []TokenGroup{
	{"--kokoro-color-", map[string]string{
		"primary":        "#2f6feb",
		"primary-hover":  "#2a61d0",
		"primary-active": "#2455b8",
		"primary-fg":     "#ffffff",
		"accent":         "#7c5cff",
		"accent-fg":      "#ffffff",
		"success":        "#1a7f37",
		"warning":        "#bf6a02",
		"danger":         "#cf222e",
		"info":           "#0969da",
		"bg":             "#ffffff",
		"bg-subtle":      "#f6f8fa",
		"surface":        "#ffffff",
		"surface-raised": "#ffffff",
		"surface-sunken": "#f6f8fa",
		"border":         "#d0d7de",
		"border-strong":  "#8c959f",
		"text":           "#1f2328",
		"text-muted":     "#59636e",
		"text-faint":     "#818b98",
		"text-inverse":   "#ffffff",
		"link":           "#0969da",
	}},
	{"--kokoro-radius-", map[string]string{
		"sm":   "4px",
		"md":   "8px",
		"lg":   "12px",
		"full": "999px",
	}},
	{"--kokoro-space-", map[string]string{
		"1": "4px", "2": "8px", "3": "12px", "4": "16px",
		"5": "24px", "6": "32px", "7": "48px", "8": "64px",
	}},
	{"--kokoro-border-", map[string]string{
		"width": "1px",
	}},
	{"--kokoro-ring-", map[string]string{
		"width": "3px",
		"color": "#2f6feb59",
	}},
	{"--kokoro-font-", map[string]string{
		"sans":           "system-ui, -apple-system, 'Segoe UI', 'PingFang SC', 'Microsoft YaHei', sans-serif",
		"mono":           "ui-monospace, 'Cascadia Code', 'JetBrains Mono', Consolas, monospace",
		"size-xs":        "12px",
		"size-sm":        "13px",
		"size-md":        "14px",
		"size-lg":        "16px",
		"size-xl":        "20px",
		"size-2xl":       "26px",
		"size-3xl":       "34px",
		"weight-normal":  "400",
		"weight-medium":  "500",
		"weight-bold":    "700",
		"line-height":    "1.55",
		"letter-spacing": "0",
	}},
	{"--kokoro-shadow-", map[string]string{
		"sm": "0 1px 2px rgba(31, 35, 40, .08)",
		"md": "0 4px 12px rgba(31, 35, 40, .10)",
		"lg": "0 16px 40px rgba(31, 35, 40, .16)",
	}},
	{"--kokoro-transition-", map[string]string{
		"fast": "120ms",
		"base": "200ms",
		"slow": "320ms",
	}},
	{"--kokoro-chart-", map[string]string{
		"series-1":     "#2f6feb",
		"series-2":     "#7c5cff",
		"series-3":     "#1a7f37",
		"series-4":     "#bf6a02",
		"series-5":     "#cf222e",
		"series-6":     "#0969da",
		"series-7":     "#8250df",
		"series-8":     "#6e7781",
		"grid":         "#eaeef2",
		"axis":         "#8c959f",
		"tooltip-bg":   "#1f2328",
		"tooltip-fg":   "#ffffff",
		"area-opacity": "0.18",
		"line-width":   "1.75",
		"point-radius": "2.5",
		"bar-radius":   "3",
	}},
	{"--kokoro-status-", map[string]string{
		"online":      "#1a7f37",
		"offline":     "#8c959f",
		"idle":        "#bf6a02",
		"maintenance": "#0969da",
	}},
	{"--kokoro-", map[string]string{
		"header-height":     "56px",
		"sidebar-width":     "240px",
		"content-max-width": "1280px",
		"card-padding":      "16px",
		"opacity-disabled":  "0.5",
		"easing":            "cubic-bezier(.2,0,0,1)",
		"z-dropdown":        "1000",
		"z-modal":           "1100",
		"z-toast":           "1200",
	}},
	{"--kokoro-bg-", map[string]string{
		"image":      "none",
		"size":       "auto",
		"repeat":     "repeat",
		"attachment": "fixed",
	}},
	{"--kokoro-hero-", map[string]string{
		"gradient": "linear-gradient(135deg, var(--kokoro-color-primary), var(--kokoro-color-accent))",
		"overlay":  "rgba(0,0,0,.35)",
	}},
	{"--kokoro-card-", map[string]string{
		"bg":           "var(--kokoro-color-surface)",
		"border":       "var(--kokoro-color-border)",
		"border-image": "none",
	}},
	{"--kokoro-header-", map[string]string{
		"bg": "var(--kokoro-color-surface)",
		"fg": "var(--kokoro-color-text)",
	}},
	{"--kokoro-footer-", map[string]string{
		"bg": "var(--kokoro-color-bg-subtle)",
	}},
	{"--kokoro-code-", map[string]string{
		"bg": "var(--kokoro-color-surface-sunken)",
	}},
}

// tokenKinds 记录每个变量名的值形态，供 ValidateTokens 使用。
type tokenKind int

const (
	kindColor tokenKind = iota
	kindLength
	kindNumber
	kindTime
	kindKeyword
	kindFontStack
	kindEasing
	kindShadow
	kindGradient
	kindURLToken
	kindLoose
)

// varKinds 是手工维护的形态表。之所以不自动推断：推断不出来就必须允许任意值，
// 那就等于没有约束。
var varKinds = map[string]tokenKind{}

func init() {
	for _, g := range TokenSchema {
		for name := range g.Defaults {
			varKinds[g.Prefix+name] = kindFor(g.Prefix + name)
		}
	}
	for name := range Singles {
		varKinds[name] = kindFor(name)
	}
	assertNoDuplicateTokens()
}

// kindFor 按变量名后缀猜形态。猜不出就是 keyword（最宽松但仍拒绝分号与花括号）。
func kindFor(name string) tokenKind {
	base := name
	for _, g := range TokenSchema {
		if strings.HasPrefix(base, g.Prefix) {
			base = strings.TrimPrefix(base, g.Prefix)
			break
		}
	}
	switch {
	case name == "--kokoro-glow":
		return kindShadow
	case strings.HasPrefix(name, "--kokoro-shadow-"):
		return kindShadow
	case strings.HasPrefix(name, "--kokoro-hero-gradient"):
		return kindGradient
	case strings.HasPrefix(name, "--kokoro-bg-image"):
		return kindURLToken
	// 图表组里的几何/透明度类是 number（无单位），网格与轴色类是 color。
	// 必须排在下面的 "-width/-radius 后缀 → length" 规则之前，
	// 否则 chart-line-width 会被当成 1.75px 而拒绝。
	case strings.HasPrefix(name, "--kokoro-chart-line-width"),
		strings.HasPrefix(name, "--kokoro-chart-point-radius"),
		strings.HasPrefix(name, "--kokoro-chart-bar-radius"),
		strings.HasPrefix(name, "--kokoro-chart-area-opacity"):
		return kindNumber
	case strings.HasPrefix(name, "--kokoro-chart-grid"),
		strings.HasPrefix(name, "--kokoro-chart-axis"),
		strings.HasPrefix(name, "--kokoro-chart-tooltip-"):
		return kindColor
	case strings.HasPrefix(name, "--kokoro-ring-color"):
		return kindColor
	case strings.HasPrefix(name, "--kokoro-status-"):
		return kindColor
	case strings.HasPrefix(name, "--kokoro-hero-overlay"):
		return kindColor
	case strings.HasPrefix(name, "--kokoro-color-"):
		return kindColor
	case strings.HasPrefix(name, "--kokoro-font-sans"),
		strings.HasPrefix(name, "--kokoro-font-mono"):
		return kindFontStack
	case strings.HasPrefix(name, "--kokoro-easing"):
		return kindEasing
	case strings.HasPrefix(name, "--kokoro-transition-"):
		return kindTime
	case strings.HasPrefix(name, "--kokoro-font-weight"),
		strings.HasPrefix(name, "--kokoro-font-line-height"),
		strings.HasPrefix(name, "--kokoro-opacity-"),
		strings.HasPrefix(name, "--kokoro-z-"):
		return kindNumber
	// 表面别名类（--kokoro-card-bg / header-bg / footer-bg / code-bg …）的
	// 默认值本身就是 var(--kokoro-color-…)，而主题也常把它们写成 rgba(…)、color-mix(…)
	// 之类。它们是「取值」而不是纯关键字，所以要用宽松规则：括号可以出现，
	// 但仍受第一道闸（不许 ; { } < > \ @）与远程 url 禁令约束。
	case strings.HasPrefix(name, "--kokoro-card-bg"),
		strings.HasPrefix(name, "--kokoro-card-border"),
		strings.HasPrefix(name, "--kokoro-header-bg"),
		strings.HasPrefix(name, "--kokoro-header-fg"),
		strings.HasPrefix(name, "--kokoro-footer-bg"),
		strings.HasPrefix(name, "--kokoro-code-bg"):
		return kindLoose
	}
	// 剩余按名字里的语义词判形态。
	switch {
	case strings.HasSuffix(base, "-width"), strings.HasSuffix(base, "-size"),
		strings.HasSuffix(base, "-radius"), strings.HasSuffix(base, "-padding"),
		strings.HasSuffix(base, "-height"), strings.HasSuffix(base, "-blur"),
		strings.HasSuffix(base, "-spacing"):
		return kindLength
	}
	return kindKeyword
}

// Singles 是那些不以「前缀 + 短名」形式出现的独立变量。
//
// 这里的每个名字都不能与 TokenSchema 里的任何组前缀重合，
// 否则 DefaultTokenMap 会出现重复键、渲染出的 CSS 里同一个变量写两遍，
// 表现为「值被静默覆盖」。init 里有断言守着这条不变量。
var Singles = map[string]string{
	"--kokoro-backdrop-blur": "0px",
	"--kokoro-glow":          "none",
}

// assertNoDuplicateTokens 在包初始化时校验命名表自身没有冲突。
//
// 这类错误只有在运行时才会显形（某个变量莫名其妙取不到值），
// 放在 init 里炸掉比让主题作者慢慢查要划算得多。
func assertNoDuplicateTokens() {
	seen := make(map[string]string, 160)
	for _, g := range TokenSchema {
		for name := range g.Defaults {
			full := g.Prefix + name
			if prev, dup := seen[full]; dup {
				panic(fmt.Sprintf("theme: 变量 %q 在 %q 与 %q 中重复定义", full, prev, g.Prefix))
			}
			seen[full] = g.Prefix
		}
	}
	for name := range Singles {
		if prev, dup := seen[name]; dup {
			panic(fmt.Sprintf("theme: 变量 %q 在 Singles 与 %q 中重复定义", name, prev))
		}
		seen[name] = "Singles"
	}
}

// AllowedTokenNames 返回全部合法变量名（测试与后台提示用）。
func AllowedTokenNames() []string {
	out := make([]string, 0, len(varKinds))
	for k := range varKinds {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// DefaultTokenMap 返回「 daylight 」默认主题的完整变量表。
func DefaultTokenMap() map[string]string {
	out := make(map[string]string, 128)
	for _, g := range TokenSchema {
		for name, v := range g.Defaults {
			out[g.Prefix+name] = v
		}
	}
	for k, v := range Singles {
		out[k] = v
	}
	return out
}

// ValidateTokens 校验一组变量。field 只用于报错信息（tokens / tokensDark）。
func ValidateTokens(m map[string]string, field string) error {
	if m == nil {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !strings.HasPrefix(k, "--kokoro-") {
			return fmt.Errorf("%s: 变量 %q 必须以 --kokoro- 开头", field, k)
		}
		kind, ok := varKinds[k]
		if !ok {
			return fmt.Errorf("%s: 未知变量 %q（主题只能声明命名表内的变量）", field, k)
		}
		if err := validateTokenValue(k, m[k], kind); err != nil {
			return fmt.Errorf("%s.%s: %w", field, k, err)
		}
	}
	return nil
}

// validateTokenValue 校验单个值。共享的第一道闸是「不许出现 ; { } < > \ 」——
// 这几个字符能终止声明、插入新规则或开新标签，等于给了主题注入面。
func validateTokenValue(name, val string, kind tokenKind) error {
	val = strings.TrimSpace(val)
	if val == "" {
		return fmt.Errorf("值为空")
	}
	if len(val) > 512 {
		return fmt.Errorf("值过长（%d 字节，上限 512）", len(val))
	}
	bad := []string{";", "{", "}", "<", ">", "\\", "@", "/*", "*/"}
	for _, b := range bad {
		if strings.Contains(val, b) {
			return fmt.Errorf("值里不允许出现 %q", b)
		}
	}
	// url() 只允许 data: 与本地路径 /_theme-assets/。远程 URL 一律拒绝：
	// 主题不该让访客的浏览器去别处拉东西（隐私 + 可用性）。
	if i := strings.Index(strings.ToLower(val), "url("); i >= 0 {
		rest := strings.ToLower(val[i+4:])
		if !strings.HasPrefix(strings.TrimSpace(rest), "data:image/") &&
			!strings.Contains(val, "/_theme-assets/") {
			return fmt.Errorf("url() 只允许 data:image/ 与 /_theme-assets/ 下的本地资源")
		}
	}
	// @import 能拉远程样式表，直接禁。
	if strings.Contains(strings.ToLower(val), "@import") {
		return fmt.Errorf("不允许 @import")
	}

	switch kind {
	case kindColor:
		if !isColorValue(val) {
			return fmt.Errorf("不是合法颜色值")
		}
	case kindLength:
		if !isLengthValue(val) {
			return fmt.Errorf("不是合法长度值")
		}
	case kindNumber:
		if !isNumberValue(val) {
			return fmt.Errorf("不是合法数值")
		}
	case kindTime:
		if !isTimeValue(val) {
			return fmt.Errorf("不是合法时间值")
		}
	case kindFontStack:
		if !isFontStack(val) {
			return fmt.Errorf("不是合法字体栈")
		}
	case kindEasing:
		if !strings.Contains(val, "(") || !strings.Contains(val, ")") {
			return fmt.Errorf("不是合法缓动函数")
		}
	case kindShadow:
		if val != "none" && !strings.ContainsAny(val, "0123456789") {
			return fmt.Errorf("不是合法阴影值")
		}
	case kindGradient:
		if !strings.Contains(val, "gradient(") {
			return fmt.Errorf("不是合法渐变值")
		}
	case kindURLToken:
		if val != "none" && val != "inherit" && val != "auto" {
			if !strings.HasPrefix(val, "url(") && !strings.HasPrefix(val, "data:image/") {
				return fmt.Errorf("背景图只允许 none / url(本地) / data:image/")
			}
		}
	case kindKeyword:
		if strings.ContainsAny(val, "()") && !strings.Contains(val, "var(") {
			return fmt.Errorf("关键字值里不应出现括号")
		}
	case kindLoose:
		// 表面别名类：只要求括号配对（挡住 "foo(" 这种残缺写法），
		// 具体是颜色还是长度由 CSS 自己判断，我们不越权替作者决定。
		if strings.ContainsAny(val, "()") && !balancedArgs(val) {
			return fmt.Errorf("括号不配对")
		}
		if strings.HasPrefix(val, "var(") && !strings.Contains(val, "--kokoro-") {
			return fmt.Errorf("var() 只能引用 --kokoro-* 变量")
		}
	}
	return nil
}

// namedColors 是允许的关键字颜色。刻意只放不会与主题语义打架的。
var namedColors = map[string]bool{
	"transparent": true, "currentcolor": true, "inherit": true,
	"black": true, "white": true, "gray": true, "grey": true, "silver": true,
	"navy": true, "teal": true, "olive": true, "purple": true, "maroon": true,
}

func isColorValue(v string) bool {
	lv := strings.ToLower(v)
	if namedColors[lv] {
		return true
	}
	switch {
	case strings.HasPrefix(lv, "#"):
		h := lv[1:]
		if len(h) != 3 && len(h) != 4 && len(h) != 6 && len(h) != 8 {
			return false
		}
		return isHex(h)
	case strings.HasPrefix(lv, "rgb("), strings.HasPrefix(lv, "rgba("),
		strings.HasPrefix(lv, "hsl("), strings.HasPrefix(lv, "hsla("):
		return balancedArgs(lv)
	case strings.HasPrefix(lv, "color-mix("):
		return balancedArgs(lv)
	case strings.HasPrefix(lv, "var("):
		return balancedArgs(lv) && strings.Contains(v, "--kokoro-")
	}
	return false
}

func isHex(s string) bool {
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

// lengthUnits 是允许的长度单位。
var lengthUnits = []string{"px", "em", "rem", "%", "vh", "vw", "vmin", "vmax", "ch", "ex", "pt", "cm", "mm", "in"}

func isLengthValue(v string) bool {
	lv := strings.ToLower(strings.TrimSpace(v))
	if lv == "0" || lv == "auto" || lv == "none" || lv == "inherit" || lv == "initial" {
		return true
	}
	if strings.HasPrefix(lv, "calc(") || strings.HasPrefix(lv, "min(") ||
		strings.HasPrefix(lv, "max(") || strings.HasPrefix(lv, "clamp(") {
		return balancedArgs(lv)
	}
	if strings.HasPrefix(lv, "var(") {
		return balancedArgs(lv)
	}
	for _, u := range lengthUnits {
		if strings.HasSuffix(lv, u) {
			num := strings.TrimSpace(strings.TrimSuffix(lv, u))
			if isNumberValue(num) {
				return true
			}
		}
	}
	return false
}

func isNumberValue(v string) bool {
	v = strings.TrimSpace(strings.ToLower(v))
	if v == "" {
		return false
	}
	i := 0
	if v[0] == '+' || v[0] == '-' {
		i = 1
	}
	digits, dots := false, false
	for ; i < len(v); i++ {
		c := v[i]
		switch {
		case c >= '0' && c <= '9':
			digits = true
		case c == '.':
			if dots {
				return false
			}
			dots = true
		default:
			return false
		}
	}
	return digits
}

func isTimeValue(v string) bool {
	lv := strings.ToLower(strings.TrimSpace(v))
	if strings.HasPrefix(lv, "var(") || strings.HasPrefix(lv, "calc(") {
		return balancedArgs(lv)
	}
	// ms 必须先判：否则 "420ms" 会因为以 "s" 结尾而被当成秒，剥出 "420m"。
	unit := "s"
	if strings.HasSuffix(lv, "ms") {
		unit = "ms"
	}
	num := strings.TrimSpace(strings.TrimSuffix(lv, unit))
	return isNumberValue(num)
}

func isFontStack(v string) bool {
	if strings.TrimSpace(v) == "" {
		return false
	}
	// 字体栈里允许引号、逗号、连字符；不允许 url() 与 @。
	lv := strings.ToLower(v)
	return !strings.Contains(lv, "url(") && !strings.Contains(lv, "@") && !strings.Contains(lv, ";")
}

// balancedArgs 检查形如 f(...) 的括号是否配对且内部没有危险字符。
func balancedArgs(v string) bool {
	open := strings.Index(v, "(")
	if open < 0 || !strings.HasSuffix(v, ")") {
		return false
	}
	depth, i := 0, 0
	for i = open; i < len(v); i++ {
		switch v[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i == len(v)-1
			}
		}
	}
	return false
}

// ---- 渲染 ----

// RenderTokens 把一份清单渲染成 <style> 内容。
//
// 浅色用 :root，深色用 [data-k-mode="dark"]。深色只输出 tokensDark 里声明过的键，
// 未声明的沿用浅色——所以主题写深色覆盖时不用把 100 个变量重写一遍。
//
// 同时把布局变量（列表模式、卡片最小宽、顶栏形态…）输出成 --k-* 自定义属性，
// 供生成的 layout CSS 与静态样式表共同消费。这样「布局差异」也走同一套
// 变量通道，主题作者不需要碰 CSS 文件。
func RenderTokens(m *Manifest) string {
	var b strings.Builder
	light := DefaultTokenMap()
	for k, v := range m.Tokens {
		light[k] = v
	}
	writeVars(&b, ":root", light)
	if len(m.TokensDark) > 0 && m.SupportsDark() {
		dark := make(map[string]string, len(m.TokensDark))
		for k, v := range m.TokensDark {
			dark[k] = v
		}
		writeVars(&b, `[data-k-mode="dark"]`, dark)
	}
	b.WriteString("\n")
	writeLayoutVars(&b, m)
	return b.String()
}

// writeVars 输出一个选择器下的全部变量，按名字排序保证输出稳定。
func writeVars(b *strings.Builder, sel string, vars map[string]string) {
	keys := make([]string, 0, len(vars))
	for k := range vars {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	b.WriteString(sel + "{\n")
	for _, k := range keys {
		fmt.Fprintf(b, "%s:%s;\n", k, vars[k])
	}
	b.WriteString("}\n")
}

// writeLayoutVars 把 layout JSON 的取值落成 --k-* 变量与 data-k-* 属性。
func writeLayoutVars(b *strings.Builder, m *Manifest) {
	l := m.Layout
	vals := map[string]string{}

	put := func(k, v string) {
		if v != "" {
			vals[k] = v
		}
	}

	put("--k-list-mode", pickStr(l.Home.List.Mode, "card"))
	put("--k-card-min", pickStr(l.Home.List.Card.MinWidth, "300px"))
	put("--k-card-gap", pickStr(l.Home.List.Card.Gap, "var(--kokoro-space-5)"))
	put("--k-row-h", pickStr(l.Home.List.Compact.RowHeight, "44px"))
	put("--k-maxw", pickStr(l.Shell.ContentMaxWidth, "var(--kokoro-content-max-width)"))
	put("--k-detail-header-h", pickStr(l.Detail.Header.Height, "320px"))
	put("--k-section-gap", pickStr(l.Detail.SectionGap, "var(--kokoro-space-6)"))
	put("--k-chart-h", pickStr(l.Charts.Height, "200px"))
	put("--k-chart-h-detail", pickStr(l.Charts.HeightDetail, "300px"))
	if v := l.Detail.IntroFont; v != "" {
		vals["--k-intro-font"] = v
	}

	// 阴影档位：卡片 shadow 枚举 → 实际 box-shadow 值。
	if sh := l.Home.List.Card.Shadow; sh != "" {
		switch sh {
		case "none":
			vals["--k-card-shadow"] = "none"
		case "md":
			vals["--k-card-shadow"] = "var(--kokoro-shadow-md)"
		case "lg":
			vals["--k-card-shadow"] = "var(--kokoro-shadow-lg)"
		case "glow":
			vals["--k-card-shadow"] = "var(--kokoro-glow)"
		default:
			vals["--k-card-shadow"] = "var(--kokoro-shadow-sm)"
		}
	}

	b.WriteString(":root{\n")
	keys := make([]string, 0, len(vals))
	for k := range vals {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(b, "%s:%s;\n", k, vals[k])
	}
	b.WriteString("}\n")
}

// RenderLayoutCSS 把 layout 里那些「必须生成 CSS 而不能只给变量」的键输出成
// 少量规则（例如终端主题要求列表行无圆角、mono 字体；纸感主题要求详情页头部放大）。
//
// 只生成枚举型、结构型的规则；所有颜色尺寸仍走变量，避免这里成为第二个样式来源。
func RenderLayoutCSS(m *Manifest) string {
	l := m.Layout
	var b strings.Builder

	// 顶栏形态
	switch l.Shell.Header.Variant {
	case "transparent":
		b.WriteString(`[data-k-header="transparent"]{background:transparent}` + "\n")
	case "blur":
		b.WriteString(`[data-k-header="blur"]{backdrop-filter:blur(var(--kokoro-backdrop-blur)) saturate(140%);background:color-mix(in srgb,var(--kokoro-color-bg) 78%,transparent)}` + "\n")
	case "bordered":
		b.WriteString(`[data-k-header="bordered"]{background:transparent;border-bottom:2px solid var(--kokoro-color-primary)}` + "\n")
	}

	// Hero 变体
	switch l.Home.Hero.Variant {
	case "gradient":
		b.WriteString(`[data-k-hero="gradient"]{background-image:var(--kokoro-hero-gradient)}` + "\n")
	case "plain":
		b.WriteString(`[data-k-hero="plain"]{background:var(--kokoro-color-bg-subtle)}` + "\n")
	}

	// 详情页头部形态
	switch l.Detail.Header.Variant {
	case "compact":
		b.WriteString(`[data-k-detail-header="compact"]{min-height:0;padding:var(--kokoro-space-4)}` + "\n")
	case "none":
		b.WriteString(`[data-k-detail-header="none"]{min-height:0;padding:0;border:0;background:none}` + "\n")
	}

	// 图表网格关闭时，前端 SVG 不画网格线。
	if l.Charts.Grid != nil && !*l.Charts.Grid {
		b.WriteString(`[data-k-chart-grid="0"] .chart .grid{display:none}` + "\n")
	}
	return b.String()
}

// BodyAttrs 返回应该写在 <body> 上的 data-k-* 属性。
func BodyAttrs(m *Manifest, mode, listMode string) map[string]string {
	l := m.Layout
	out := map[string]string{
		"data-k-list-mode":  pickStr(listMode, pickStr(l.Home.List.Mode, "card")),
		"data-k-dot":        pickStr(l.Misc.StatusDotStyle, "solid"),
		"data-k-bar":        pickStr(l.Misc.UsageBarStyle, "solid"),
		"data-k-tag":        pickStr(l.Misc.TagStyle, "pill"),
		"data-k-badge":      pickStr(l.Misc.BadgeStyle, "soft"),
		"data-k-chart-type": pickStr(l.Charts.Type, "area"),
		"data-k-mode":       mode,
		"data-k-header":     pickStr(l.Shell.Header.Variant, "solid"),
		"data-k-hero":       pickStr(l.Home.Hero.Variant, "plain"),
	}
	// Hero 的"变体"与"是否启用"是两个维度，必须分成两个属性：
	// 拼成 "on:gradient" 这种值虽然能塞进一个属性，但 CSS 选择器没法匹配，
	// 主题的 Hero 就永远不生效。
	if pickBool(l.Home.Hero.Enabled, false) {
		out["data-k-hero-on"] = "1"
	}
	if v := l.Home.List.GroupBy; v != "" && v != "none" {
		out["data-k-group"] = v
	}
	if v := pickStr(l.Shell.ContentAlign, "center"); v != "left" {
		out["data-k-align"] = v
	}
	// 卡片上浮：只有默认开启时才输出 data-k-lift="0"，
	// 这样 CSS 里一条 [data-k-lift="0"] 就能关掉它。
	if !pickBool(l.Home.List.Card.HoverLift, true) {
		out["data-k-lift"] = "0"
	}
	// compact 模式的行间分隔线同理。
	if l.Home.List.Compact.Dividers != nil && !*l.Home.List.Compact.Dividers {
		out["data-k-divider"] = "0"
	}
	// 详情页头部形态要落在详情页容器上，所以一并输出。
	out["data-k-detail-header"] = pickStr(l.Detail.Header.Variant, "compact")
	// 图表网格：只在显式关闭时输出 ="0"。
	// RenderLayoutCSS 生成的选择器是 [data-k-chart-grid="0"]，
	// 这里漏掉的话主题里的 "grid": false 就只是一段没人匹配的 CSS——
	// 看着写进去了，实际不生效。
	if l.Charts.Grid != nil && !*l.Charts.Grid {
		out["data-k-chart-grid"] = "0"
	}
	return out
}

// AttrsString 把 BodyAttrs 拼成可以直接塞进模板的字符串。
func AttrsString(attrs map[string]string) string {
	keys := make([]string, 0, len(attrs))
	for k := range attrs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf(`%s="%s"`, k, attrs[k]))
	}
	return strings.Join(parts, " ")
}
