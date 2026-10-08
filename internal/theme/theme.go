// Package theme 是 Kokoro 的主题（皮肤）引擎。
//
// 设计对应 docs/THEME-SPEC.md 的「双层定制模型」，但只落地第一层 tokens 与
// 第二层 layout——第三层模板覆盖默认关闭，且在可预见的将来也不打算开启
// （它会引入任意 HTML/JS 注入面，收益远小于风险）。
//
// 两层的分工：
//   - tokens：一组 CSS 自定义属性（--kokoro-*），管颜色、圆角、阴影、字体、间距、图表色。
//     主题只能声明这些变量，不能写业务样式。
//   - layout：一份 JSON，描述页面结构偏好（列表模式、顶栏形态、图表类型、
//     详情页头部形态、状态点样式…）。Hub 把它展开成 <body> 上的 data-k-* 属性
//     与一小段生成的 CSS，内置样式表按这些属性预置全部规则。
//
// 因此「换主题」不只是换配色：terminal 会把列表压成等宽紧凑行，paper 会把
// 详情页头部放大成留白 hero，midnight 默认深色并且图表变折线——都是 layout 层的差异。
//
// 内置主题走与第三方完全相同的解析/校验代码路径（只有 source 标记不同），
// 所以「复制内置主题再改」是天然工作流。
package theme

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// 版本号。tokensSchemaVersion 变化时，Hub 需要按 §8 的别名表迁移旧变量名。
const (
	TokensSchemaVersion = 2
	LayoutSchemaVersion = 1
	ManifestVersion     = 1
)

// Cookie 名。theme 是站点级设置（一个探针站一个主题），mode 是访客偏好。
const (
	ThemeCookie = "k_theme"
	ModeCookie  = "k_mode"
)

// ThemeIDPrefix 是官方内置主题保留的 ID 前缀。
const ThemeIDPrefix = "kokoro."

// Manifest 是一份主题清单（对应 theme.json）。
//
// 字段严格对齐 spec §2.3；未落地的字段（overrides / files / checksum / signature /
// 预览图）暂时不解析——内置主题不需要，第三方导入在后续版本补。
type Manifest struct {
	SchemaVersion int      `json:"schemaVersion"`
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Version       string   `json:"version"`
	Description   string   `json:"description,omitempty"`
	Author        string   `json:"author"`
	Homepage      string   `json:"homepage,omitempty"`
	License       string   `json:"license,omitempty"`
	Tags          []string `json:"tags,omitempty"`

	TokensSchemaVersion int               `json:"tokensSchemaVersion"`
	Tokens              map[string]string `json:"tokens"`
	TokensDark          map[string]string `json:"tokensDark,omitempty"`
	Mode                Mode              `json:"mode,omitempty"`
	LayoutSchemaVersion int               `json:"layoutSchemaVersion"`
	Layout              Layout            `json:"layout"`

	// Package 描述 .kokoro-theme 包的完整性信息（spec §2.4）。
	// 裸 theme.json 导入时为 nil，不影响任何渲染逻辑。
	Package *Package `json:"package,omitempty"`

	// Builtin 表示这份主题随二进制 go:embed，不落在数据库里。
	Builtin bool `json:"-"`
	// Source 说明主题来源：builtin / builtin-modified / custom。
	Source string `json:"-"`
	// Preview 是数据 URI 或 Hub 路径，用于后台主题选择器的色板预览。
	Swatches []string `json:"-"`
}

// Mode 是深浅色策略（spec §2.3.2）。零值按 Default 处理。
type Mode struct {
	Default         string `json:"default,omitempty"`         // light | dark | auto
	AllowUserSwitch *bool  `json:"allowUserSwitch,omitempty"` // 默认 true
	SupportsDark    *bool  `json:"supportsDark,omitempty"`    // 默认 true
}

// ---- layout 结构（spec §3.5） ----

// Layout 是第二层布局描述。
type Layout struct {
	Shell  ShellLayout  `json:"shell,omitempty"`
	Home   HomeLayout   `json:"home,omitempty"`
	Detail DetailLayout `json:"detail,omitempty"`
	Charts ChartsLayout `json:"charts,omitempty"`
	Misc   MiscLayout   `json:"misc,omitempty"`
}

// ShellLayout 是整站骨架。
type ShellLayout struct {
	ContentMaxWidth string `json:"contentMaxWidth,omitempty"`
	ContentAlign    string `json:"contentAlign,omitempty"`
	Header          struct {
		Variant        string `json:"variant,omitempty"` // solid|transparent|blur|bordered
		Sticky         *bool  `json:"sticky,omitempty"`
		ShowSearch     *bool  `json:"showSearch,omitempty"`
		ShowModeSwitch *bool  `json:"showModeSwitch,omitempty"`
	} `json:"header,omitempty"`
	Footer struct {
		Variant string `json:"variant,omitempty"` // simple|columns|minimal
	} `json:"footer,omitempty"`
}

// HomeLayout 是首页。
type HomeLayout struct {
	Hero struct {
		Enabled   *bool  `json:"enabled,omitempty"`
		Variant   string `json:"variant,omitempty"` // plain|gradient|image|split
		Align     string `json:"align,omitempty"`   // left|center
		Subtitle  string `json:"subtitle,omitempty"`
		ShowStats *bool  `json:"showStats,omitempty"`
	} `json:"hero,omitempty"`
	List ListLayout `json:"list,omitempty"`
}

// ListLayout 是首页节点列表。
type ListLayout struct {
	Mode            string `json:"mode,omitempty"` // card|table|compact
	AllowUserSwitch *bool  `json:"allowUserSwitch,omitempty"`
	GroupBy         string `json:"groupBy,omitempty"`
	Card            struct {
		MinWidth  string `json:"minWidth,omitempty"`
		Gap       string `json:"gap,omitempty"`
		Border    *bool  `json:"border,omitempty"`
		Shadow    string `json:"shadow,omitempty"`
		HoverLift *bool  `json:"hoverLift,omitempty"`
	} `json:"card,omitempty"`
	Table struct {
		Density  string `json:"density,omitempty"` // compact|comfortable
		Striped  *bool  `json:"striped,omitempty"`
		RowHover *bool  `json:"rowHover,omitempty"`
		ShowUnit *bool  `json:"showUnit,omitempty"`
	} `json:"table,omitempty"`
	Compact struct {
		RowHeight  string `json:"rowHeight,omitempty"`
		Dividers   *bool  `json:"dividers,omitempty"`
		ShowFlag   *bool  `json:"showFlag,omitempty"`
		InlineBars *bool  `json:"inlineBars,omitempty"`
	} `json:"compact,omitempty"`
}

// DetailLayout 是节点详情页。
type DetailLayout struct {
	Header struct {
		Variant       string `json:"variant,omitempty"` // hero|compact|none
		Height        string `json:"height,omitempty"`
		ShowTags      *bool  `json:"showTags,omitempty"`
		SecondaryLine string `json:"secondaryLine,omitempty"`
	} `json:"header,omitempty"`
	SectionGap  string `json:"sectionGap,omitempty"`
	IntroFont   string `json:"introFontSize,omitempty"`
	CommentFold *bool  `json:"commentsCollapsed,omitempty"`
}

// ChartsLayout 是图表通用参数。
type ChartsLayout struct {
	Type         string `json:"type,omitempty"` // line|area|bar
	Grid         *bool  `json:"grid,omitempty"`
	Legend       *bool  `json:"legend,omitempty"`
	AxisLabel    *bool  `json:"axisLabel,omitempty"`
	Smooth       *bool  `json:"smooth,omitempty"`
	Tooltip      string `json:"tooltip,omitempty"`
	Height       string `json:"height,omitempty"`
	HeightDetail string `json:"heightDetail,omitempty"`
}

// MiscLayout 是零碎风格开关。
type MiscLayout struct {
	StatusDotStyle string `json:"statusDotStyle,omitempty"` // solid|ring|glow|none
	UsageBarStyle  string `json:"usageBarStyle,omitempty"`  // solid|gradient|segmented|none
	TagStyle       string `json:"tagStyle,omitempty"`       // pill|square|text
	BadgeStyle     string `json:"badgeStyle,omitempty"`     // soft|outline|solid
	ShowUptime     *bool  `json:"showUptime,omitempty"`
	NumberFormat   string `json:"numberFormat,omitempty"`
	DateFormat     string `json:"dateFormat,omitempty"`
}

// ---- 解析与校验 ----

// Parse 从 JSON 解析并校验一份清单。校验顺序刻意与 spec §2.5 一致：
// 结构版本 → 标识字段 → tokens → layout。
func Parse(data []byte) (*Manifest, error) {
	var m Manifest
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("主题 JSON 解析失败: %w", err)
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	m.normalize()
	return &m, nil
}

// Validate 只做检查不改内容。
func (m *Manifest) Validate() error {
	if m.SchemaVersion == 0 {
		m.SchemaVersion = ManifestVersion
	}
	if m.SchemaVersion > ManifestVersion {
		return fmt.Errorf("主题结构版本 %d 高于当前 Hub 支持的 %d，请升级 Hub", m.SchemaVersion, ManifestVersion)
	}
	if m.ID == "" {
		return fmt.Errorf("主题缺少 id")
	}
	if len(m.ID) < 3 || len(m.ID) > 64 {
		return fmt.Errorf("主题 id 长度非法")
	}
	for i, r := range m.ID {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-'
		if !ok {
			return fmt.Errorf("主题 id 只能含小写字母、数字、. _ -（第 %d 个字符非法）", i)
		}
	}
	if m.Name == "" {
		return fmt.Errorf("主题缺少 name")
	}
	if m.Version == "" {
		return fmt.Errorf("主题缺少 version")
	}
	if m.Author == "" {
		m.Author = "未知作者"
	}
	if m.Homepage != "" && !strings.HasPrefix(m.Homepage, "http://") && !strings.HasPrefix(m.Homepage, "https://") {
		return fmt.Errorf("主题 homepage 必须是 http(s) 地址")
	}
	if m.TokensSchemaVersion == 0 {
		m.TokensSchemaVersion = TokensSchemaVersion
	}
	if m.TokensSchemaVersion > TokensSchemaVersion {
		return fmt.Errorf("主题使用了 tokens v%d，当前 Hub 只认 v%d", m.TokensSchemaVersion, TokensSchemaVersion)
	}
	if m.LayoutSchemaVersion == 0 {
		m.LayoutSchemaVersion = LayoutSchemaVersion
	}
	if m.LayoutSchemaVersion > LayoutSchemaVersion {
		return fmt.Errorf("主题使用了 layout v%d，当前 Hub 只认 v%d", m.LayoutSchemaVersion, LayoutSchemaVersion)
	}
	if err := ValidateTokens(m.Tokens, "tokens"); err != nil {
		return err
	}
	if err := ValidateTokens(m.TokensDark, "tokensDark"); err != nil {
		return err
	}
	if len(m.Tokens) == 0 {
		return fmt.Errorf("主题没有任何 tokens")
	}
	return ValidateLayout(&m.Layout)
}

// normalize 补齐零值，让后面的渲染代码不用到处判断指针。
func (m *Manifest) normalize() {
	if m.License == "" {
		m.License = "MIT"
	}
	if m.Source == "" {
		if m.Builtin {
			m.Source = "builtin"
		} else {
			m.Source = "custom"
		}
	}
	m.Mode.Default = pickEnum(m.Mode.Default, "auto", "light", "dark", "auto")
	if m.Mode.AllowUserSwitch == nil {
		m.Mode.AllowUserSwitch = boolPtr(true)
	}
	if m.Mode.SupportsDark == nil {
		m.Mode.SupportsDark = boolPtr(true)
	}
	m.Swatches = swatchesOf(m.Tokens)
}

// SupportsDark 报告这份主题是否支持深色模式。
func (m *Manifest) SupportsDark() bool { return *m.Mode.SupportsDark }

// AllowModeSwitch 报告是否给访客看深浅色切换按钮。
func (m *Manifest) AllowModeSwitch() bool { return *m.Mode.AllowUserSwitch }

// BuiltinID 报告 ID 是否属于官方保留前缀。
func (m *Manifest) BuiltinID() bool { return strings.HasPrefix(m.ID, ThemeIDPrefix) }

// DarkTokens 返回深色模式变量；主题没声明时返回 nil（表示沿用浅色值）。
func (m *Manifest) DarkTokens() map[string]string { return m.TokensDark }

// ---- 工具 ----

func boolPtr(b bool) *bool { return &b }

func pickEnum(v string, def string, allowed ...string) string {
	if v == "" {
		return def
	}
	for _, a := range allowed {
		if v == a {
			return v
		}
	}
	return def
}

func pickStr(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

func pickBool(p *bool, def bool) bool {
	if p == nil {
		return def
	}
	return *p
}

// swatchesOf 从 tokens 里挑出用于后台主题选择器预览的主色。
func swatchesOf(t map[string]string) []string {
	keys := []string{
		"--kokoro-color-primary",
		"--kokoro-color-accent",
		"--kokoro-color-bg",
		"--kokoro-color-surface",
		"--kokoro-color-text",
	}
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		if v := strings.TrimSpace(t[k]); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// SortByID 让主题列表顺序稳定（便于测试与缓存）。
func SortByID(list []*Manifest) {
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
}
