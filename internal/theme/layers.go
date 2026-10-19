package theme

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

type ThemeLayer struct {
	Style               map[string]string `json:"style"`
	Mobile              map[string]string `json:"mobile,omitempty"`
	Tablet              map[string]string `json:"tablet,omitempty"`
	HideOnMobile        bool              `json:"hideOnMobile,omitempty"`
	HideOnReducedMotion bool              `json:"hideOnReducedMotion,omitempty"`
}

var layerTargets = map[string]bool{
	"header": true, "hero": true, "card": true, "rate-panel": true,
	"globe-panel": true, "globe-background": true, "usage-track": true, "usage-fill": true,
}

func ValidateLayers(layers map[string][]ThemeLayer, motions map[string]ThemeMotion) error {
	count := 0
	for target, items := range layers {
		if !layerTargets[target] {
			return fmt.Errorf("layout.layers: %q 不支持装饰层", target)
		}
		if len(items) > 2 {
			return fmt.Errorf("layout.layers.%s: 每组件最多 2 层", target)
		}
		count += len(items)
		for _, layer := range items {
			if len(layer.Style) == 0 {
				return fmt.Errorf("layout.layers.%s: style 不能为空", target)
			}
			for _, styles := range []map[string]string{layer.Style, layer.Mobile, layer.Tablet} {
				if err := ValidateVisuals(map[string]map[string]string{target: styles}, motions); err != nil {
					return fmt.Errorf("layout.layers.%s: %w", target, err)
				}
			}
		}
	}
	if count > 16 {
		return fmt.Errorf("layout.layers: 总共最多 16 层声明")
	}
	return nil
}

// Only engine-owned selectors and bounded counts reach the client. Style values
// remain in validated CSS, never in executable or interpolated HTML.
func LayerBindingsJSON(m *Manifest) string {
	type binding struct {
		Target   string `json:"target"`
		Selector string `json:"selector"`
		Count    int    `json:"count"`
	}
	var items []binding
	for _, target := range sortedLayerTargets(m) {
		if len(m.Layout.Layers[target]) > 0 {
			items = append(items, binding{target, visualTargets[target], len(m.Layout.Layers[target])})
		}
	}
	if len(items) == 0 {
		return ""
	}
	raw, _ := json.Marshal(items)
	return string(raw)
}

func sortedLayerTargets(m *Manifest) []string {
	keys := make([]string, 0, len(m.Layout.Layers))
	for key := range m.Layout.Layers {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func renderLayerRule(m *Manifest, selector string, properties map[string]string) string {
	var out strings.Builder
	keys := make([]string, 0, len(properties))
	for key := range properties {
		if _, ok := visualProperties[key]; ok {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	out.WriteString(selector + "{")
	for _, key := range keys {
		fmt.Fprintf(&out, "%s:%s;", key, rewriteAssetURL(properties[key], m.ID))
	}
	out.WriteString(renderMotionOverrides(properties))
	if motion := properties["motion"]; motion != "" {
		if motion == "none" {
			out.WriteString("animation:none;")
		} else {
			out.WriteString(renderMotionDeclaration(m, properties))
		}
	}
	out.WriteString("}\n")
	return out.String()
}

func renderLayerCSS(m *Manifest) string {
	var out strings.Builder
	for _, target := range sortedLayerTargets(m) {
		host := `body[data-k-theme]:not([data-admin]) ` + visualTargets[target]
		out.WriteString(host + "{isolation:isolate}\n")
		for i, layer := range m.Layout.Layers[target] {
			wrapper := fmt.Sprintf(`%s > .k-theme-layer[data-k-layer-index="%d"]`, host, i)
			paint := wrapper + ` > .k-theme-layer-paint`
			out.WriteString(renderLayerRule(m, paint, layer.Style))
			if len(layer.Tablet) > 0 {
				out.WriteString("@container k-page " + visualBreakpoint("tablet") + "{" + renderLayerRule(m, paint, layer.Tablet) + "}\n")
			}
			if len(layer.Mobile) > 0 {
				out.WriteString("@container k-page " + visualBreakpoint("mobile") + "{" + renderLayerRule(m, paint, layer.Mobile) + "}\n")
			}
			out.WriteString("@media(prefers-reduced-motion:reduce){" + paint + "{animation:none!important}}\n")
			out.WriteString("html.k-effects-paused " + paint + "{animation-play-state:paused}\n")
			if layer.HideOnMobile {
				out.WriteString("@container k-page (max-width:640px){" + wrapper + "{display:none}}\n")
			}
			if layer.HideOnReducedMotion {
				out.WriteString("@media(prefers-reduced-motion:reduce){" + wrapper + "{display:none}}\n")
			}
		}
	}
	return out.String()
}
