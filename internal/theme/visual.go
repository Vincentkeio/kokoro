package theme

import (
	"fmt"
	"net/url"
	"path"
	"strings"
)

func isVisualV3Token(name string) bool {
	return strings.HasPrefix(name, "--kokoro-usage-bar-") ||
		strings.HasPrefix(name, "--kokoro-globe-matrix-") ||
		name == "--kokoro-globe-background" || name == "--kokoro-globe-background-size" ||
		name == "--kokoro-chart-filter"
}

// ValidateAssetReferences ensures every local resource used by a theme is part
// of the exact package that will be stored and served. Resource paths are
// relative to assets/ and may optionally include the current theme ID.
func ValidateAssetReferences(m *Manifest, assets []Asset) error {
	available := make(map[string]bool, len(assets))
	for _, asset := range assets {
		available[asset.Name] = true
	}
	visualValues := make(map[string]string)
	for target, properties := range m.Layout.Visuals {
		for property, value := range properties {
			visualValues["layout.visuals."+target+"."+property] = value
		}
	}
	for _, tokens := range []map[string]string{m.Tokens, m.TokensDark, visualValues} {
		for token, value := range tokens {
			for _, match := range cssFunctionPattern.FindAllStringSubmatchIndex(value, -1) {
				if !strings.EqualFold(value[match[2]:match[3]], "url") {
					continue
				}
				start := match[1]
				end := strings.IndexByte(value[start:], ')')
				if end < 0 {
					return fmt.Errorf("%s: url() 未闭合", token)
				}
				resource := strings.TrimSpace(value[start : start+end])
				resource = strings.Trim(resource, "\"'")
				if strings.HasPrefix(strings.ToLower(resource), "data:image/") {
					continue
				}
				u, err := url.Parse(resource)
				if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil || u.Path == "" {
					return fmt.Errorf("%s: 资源地址无效", token)
				}
				if !strings.HasPrefix(u.Path, "/_theme-assets/") {
					return fmt.Errorf("%s: 只允许站内主题资源", token)
				}
				name := strings.TrimPrefix(u.Path, "/_theme-assets/")
				name = strings.TrimPrefix(name, m.ID+"/")
				full, err := SafeAssetPath(name)
				if err != nil {
					return fmt.Errorf("%s: 资源路径无效：%w", token, err)
				}
				if _, allowed := AssetMIME(full); !allowed {
					return fmt.Errorf("%s: %s 不是面板可提供的栅格图片资源", token, path.Base(full))
				}
				if !available[full] {
					return fmt.Errorf("%s 引用的资源 %q 不在主题包内，请导入完整 .kokoro-theme 包", token, name)
				}
			}
		}
	}
	return nil
}

// AssetMIME is shared by package validation, resource references and serving.
// Fonts, vector documents and executable content are outside this boundary.
func AssetMIME(name string) (string, bool) {
	switch strings.ToLower(path.Ext(name)) {
	case ".png":
		return "image/png", true
	case ".jpg", ".jpeg":
		return "image/jpeg", true
	case ".webp":
		return "image/webp", true
	case ".gif":
		return "image/gif", true
	case ".avif":
		return "image/avif", true
	case ".bmp":
		return "image/bmp", true
	case ".ico":
		return "image/x-icon", true
	default:
		return "", false
	}
}
