package theme

// layout 的枚举取值校验。
//
// 与 tokens 的区别：layout 的值会进 <body> 的 data 属性和生成的 CSS，
// 所以除了白名单枚举，还必须额外守住「长度」与「形态」两道闸——
// 枚举值是封闭集合，逐个校验即可，不需要正则去猜。

import "fmt"

// enum 集合。布局里的每个枚举键都在这里登记，未登记的键一律拒绝，
// 这样拼错的键会立刻报错，而不是悄悄回落到默认值。
var enums = map[string]map[string]bool{
	"shell.header.variant":        {"solid": true, "transparent": true, "blur": true, "bordered": true},
	"shell.contentAlign":          {"center": true, "left": true},
	"shell.footer.variant":        {"simple": true, "columns": true, "minimal": true},
	"home.hero.variant":           {"plain": true, "gradient": true, "image": true, "split": true},
	"home.hero.align":             {"left": true, "center": true},
	"home.list.mode":              {"card": true, "table": true, "compact": true, "map": true},
	"home.list.groupBy":           {"none": true, "status": true, "region": true, "tag": true, "provider": true},
	"home.list.card.shadow":       {"none": true, "sm": true, "md": true, "lg": true, "glow": true},
	"home.list.table.density":     {"compact": true, "comfortable": true},
	"home.list.compact.showFlag":  {"true": true, "false": true},
	"detail.header.variant":       {"hero": true, "compact": true, "none": true},
	"detail.header.secondaryLine": {"location": true, "specs": true, "uptime": true, "none": true},
	"charts.type":                 {"line": true, "area": true, "bar": true},
	"charts.tooltip":              {"axis": true, "item": true, "none": true},
	"misc.statusDotStyle":         {"solid": true, "ring": true, "glow": true, "none": true},
	"misc.usageBarStyle":          {"solid": true, "gradient": true, "segmented": true, "none": true},
	"misc.tagStyle":               {"pill": true, "square": true, "text": true},
	"misc.badgeStyle":             {"soft": true, "outline": true, "solid": true},
	"misc.numberFormat":           {"si": true, "plain": true, "percent": true},
	"misc.dateFormat":             {"relative": true, "absolute": true},
}

// maxLayoutLen 是 layout 里字符串值的通用上限。
const maxLayoutLen = 200

// ValidateLayout 校验第二层布局。
//
// 注意 booleans 不在这里校验：Go 的 *bool 能区分「显式 false」与「没写」，
// 而 DisallowUnknownFields 已经挡住了拼错的键，不需要额外处理 false 的语义。
func ValidateLayout(l *Layout) error {
	check := func(key, val string) error {
		set, ok := enums[key]
		if !ok {
			return fmt.Errorf("layout: %s 不在可校验范围", key)
		}
		if val == "" {
			return nil
		}
		if !set[val] {
			return fmt.Errorf("layout: %s 的取值 %q 非法", key, val)
		}
		return nil
	}
	lengthy := func(key, val string) error {
		if len(val) > maxLayoutLen {
			return fmt.Errorf("layout: %s 的值过长", key)
		}
		return nil
	}

	if err := check("shell.header.variant", l.Shell.Header.Variant); err != nil {
		return err
	}
	if err := check("shell.contentAlign", l.Shell.ContentAlign); err != nil {
		return err
	}
	if err := check("shell.footer.variant", l.Shell.Footer.Variant); err != nil {
		return err
	}
	if err := check("home.hero.variant", l.Home.Hero.Variant); err != nil {
		return err
	}
	if err := check("home.hero.align", l.Home.Hero.Align); err != nil {
		return err
	}
	if err := lengthy("home.hero.subtitle", l.Home.Hero.Subtitle); err != nil {
		return err
	}
	if err := check("home.list.mode", l.Home.List.Mode); err != nil {
		return err
	}
	if err := check("home.list.groupBy", l.Home.List.GroupBy); err != nil {
		return err
	}
	if err := check("home.list.card.shadow", l.Home.List.Card.Shadow); err != nil {
		return err
	}
	if err := check("home.list.table.density", l.Home.List.Table.Density); err != nil {
		return err
	}
	if err := check("detail.header.variant", l.Detail.Header.Variant); err != nil {
		return err
	}
	if err := check("detail.header.secondaryLine", l.Detail.Header.SecondaryLine); err != nil {
		return err
	}
	if err := check("charts.type", l.Charts.Type); err != nil {
		return err
	}
	if err := check("charts.tooltip", l.Charts.Tooltip); err != nil {
		return err
	}
	for key, val := range map[string]string{
		"misc.statusDotStyle": l.Misc.StatusDotStyle,
		"misc.usageBarStyle":  l.Misc.UsageBarStyle,
		"misc.tagStyle":       l.Misc.TagStyle,
		"misc.badgeStyle":     l.Misc.BadgeStyle,
		"misc.numberFormat":   l.Misc.NumberFormat,
		"misc.dateFormat":     l.Misc.DateFormat,
	} {
		if err := check(key, val); err != nil {
			return err
		}
	}

	// 尺寸类字段要过一遍长度值校验，防止 "100px; evil{}" 这种注入。
	for key, val := range map[string]string{
		"shell.contentMaxWidth":       l.Shell.ContentMaxWidth,
		"home.list.card.minWidth":     l.Home.List.Card.MinWidth,
		"home.list.card.gap":          l.Home.List.Card.Gap,
		"home.list.compact.rowHeight": l.Home.List.Compact.RowHeight,
		"detail.header.height":        l.Detail.Header.Height,
		"detail.sectionGap":           l.Detail.SectionGap,
		"detail.introFontSize":        l.Detail.IntroFont,
		"charts.height":               l.Charts.Height,
		"charts.heightDetail":         l.Charts.HeightDetail,
	} {
		if val == "" {
			continue
		}
		if !isLengthValue(val) {
			return fmt.Errorf("layout: %s 的值 %q 不是合法长度（如 320px / 40%% / var(--x)）", key, val)
		}
	}
	return nil
}
