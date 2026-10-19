package theme

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Authors choose components, never selectors. Each rule remains inside the
// public theme surface, away from administration controls and document markup.
var visualTargets = map[string]string{
	"header": ".top", "hero": ".hero", "overview": ".console-summary",
	"card": ".card", "card-hover": ".card:hover", "card-offline": ".card.off",
	"card-result": ".card :is(.task-summary,.netcard,.netqrows,.specs > div.full)",
	"rate-panel":  ".home-band > #speed", "rate-chart": "#speed .rate-chart-wrap",
	"globe-panel": ".map-card", "globe-background": ".map-card .globe-body",
	"clock": ".overview-clock", "usage-track": ".card .gauges .bar",
	"usage-fill":  ".card .gauges .bar i:is(.cpu,.mem,.disk)",
	"chart-line":  ":is(.dash-line-up,.dash-line-down,.res-line-cpu,.res-line-mem,.sp-line)",
	"chart-area":  ":is(.dash-area-up,.dash-area-down,.res-area-cpu,.res-area-mem,.sp-area)",
	"uptime-cell": ".uptime-bar > *", "button": ".btn", "tag": ".tag",
	"button-hover": ".btn:hover", "button-focus": ".btn:focus-visible",
	"card-focus": ".card:focus-within", "tag-hover": ".tag:hover",
}

var visualProperties = map[string]tokenKind{
	"background": kindLoose, "background-image": kindLoose, "background-color": kindColor,
	"background-size": kindLoose, "background-position": kindLoose, "background-repeat": kindLoose,
	"background-blend-mode": kindLoose, "color": kindColor,
	"border": kindLoose, "border-color": kindColor, "border-style": kindLoose,
	"border-width": kindLength, "border-radius": kindLoose,
	"box-shadow": kindShadow, "text-shadow": kindShadow,
	"filter": kindLoose, "backdrop-filter": kindLoose, "opacity": kindNumber,
	"mix-blend-mode": kindLoose, "mask-image": kindLoose, "mask-size": kindLoose,
	"mask-position": kindLoose, "mask-repeat": kindLoose, "clip-path": kindLoose,
	"transform": kindLoose, "transform-origin": kindLoose,
	"outline-color": kindColor, "stroke": kindColor, "stroke-width": kindNumber,
	"stroke-dasharray": kindLoose, "fill": kindColor,
	"font-family": kindFontStack, "font-size": kindLength, "letter-spacing": kindLength,
	"transition-duration": kindTime,
	"outline":             kindLoose, "outline-width": kindLength, "outline-style": kindKeyword, "outline-offset": kindLength,
	"text-decoration": kindLoose, "text-decoration-color": kindColor, "text-decoration-thickness": kindLength, "text-underline-offset": kindLength,
	"font-weight": kindLoose, "line-height": kindLoose, "text-transform": kindKeyword,
	"background-clip": kindKeyword, "background-origin": kindKeyword,
	"border-image": kindLoose, "border-image-source": kindLoose, "border-image-slice": kindLoose, "border-image-width": kindLoose,
	"stroke-dashoffset": kindLoose,
}

func ValidateVisuals(visuals map[string]map[string]string, definitions ...map[string]ThemeMotion) error {
	if len(visuals) > len(visualTargets) {
		return fmt.Errorf("layout.visuals: 组件规则过多")
	}
	for target, properties := range visuals {
		if _, ok := visualTargets[target]; !ok {
			return fmt.Errorf("layout.visuals: 未知组件 %q", target)
		}
		if len(properties) > 56 {
			return fmt.Errorf("layout.visuals.%s: 属性过多", target)
		}
		for property, value := range properties {
			field := "layout.visuals." + target + "." + property
			switch property {
			case "motion":
				if value != "none" && value != "breathe" && value != "float" && value != "pan" {
					name := strings.TrimPrefix(value, "custom:")
					if !strings.HasPrefix(value, "custom:") || len(definitions) == 0 {
						return fmt.Errorf("%s: 未声明的动效", field)
					}
					if _, ok := definitions[0][name]; !ok {
						return fmt.Errorf("%s: 未声明的动效 %q", field, name)
					}
				}
				continue
			case "motion-duration":
				d, err := time.ParseDuration(value)
				if err != nil || !isTimeValue(value) || d < 2*time.Second || d > time.Minute {
					return fmt.Errorf("%s: 动效周期必须为 2s–60s", field)
				}
				continue
			case "motion-distance":
				if err := validateTokenValue(field, value, kindLength); err != nil {
					return fmt.Errorf("%s: %w", field, err)
				}
				continue
			}
			kind, ok := visualProperties[property]
			if !ok {
				return fmt.Errorf("%s: 属性不在白名单内", field)
			}
			if strings.Contains(value, "!") {
				return fmt.Errorf("%s: 不允许 !important", field)
			}
			if err := validateTokenValue(field, value, kind); err != nil {
				return fmt.Errorf("%s: %w", field, err)
			}
		}
	}
	return nil
}

func renderVisualCSS(m *Manifest) string {
	var out strings.Builder
	out.WriteString(renderVisualRules(m, m.Layout.Visuals))
	for _, breakpoint := range []string{"tablet", "mobile"} {
		if rules := m.Layout.Responsive[breakpoint]; len(rules) > 0 {
			out.WriteString("@container k-page " + visualBreakpoint(breakpoint) + "{" + renderVisualRules(m, rules) + "}\n")
		}
	}
	out.WriteString(renderMotionCSS(m))
	out.WriteString(renderLayerCSS(m))
	return out.String()
}

func renderVisualRules(m *Manifest, visuals map[string]map[string]string) string {
	var out, reduced, paused strings.Builder
	targets := make([]string, 0, len(visuals))
	for target := range visuals {
		targets = append(targets, target)
	}
	sort.Strings(targets)
	for _, target := range targets {
		properties := visuals[target]
		selector := `body[data-k-theme]:not([data-admin]) ` + visualTargets[target]
		keys := make([]string, 0, len(properties))
		for property := range properties {
			if _, ok := visualProperties[property]; ok {
				keys = append(keys, property)
			}
		}
		sort.Strings(keys)
		out.WriteString(selector + "{")
		for _, property := range keys {
			fmt.Fprintf(&out, "%s:%s;", property, rewriteAssetURL(properties[property], m.ID))
		}
		out.WriteString(renderMotionOverrides(properties))
		if motion := properties["motion"]; motion != "" {
			if motion == "none" {
				out.WriteString("animation:none;")
			} else {
				out.WriteString(renderMotionDeclaration(m, properties))
				reduced.WriteString(selector + "{animation:none!important}")
				paused.WriteString("html.k-effects-paused " + selector + "{animation-play-state:paused}")
			}
		}
		out.WriteString("}\n")
	}
	if reduced.Len() > 0 {
		out.WriteString("@media(prefers-reduced-motion:reduce){" + reduced.String() + "}\n")
		out.WriteString(paused.String() + "\n")
	}
	return out.String()
}
