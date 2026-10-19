package theme

import (
	"strings"
	"testing"
)

func TestVisualPackageIsSelfContained(t *testing.T) {
	m := sampleManifest()
	m.Tokens["--kokoro-globe-background"] = "url(/_theme-assets/ambient.webp)"
	m.Layout.Globe.Backdrop = "matrix"
	m.Layout.Globe.MatrixGlyphs = "hex"
	m.Layout.Visuals = map[string]map[string]string{
		"usage-fill": {"box-shadow": "0 0 12px currentColor", "motion": "breathe", "motion-duration": "5s"},
		"card":       {"background": "linear-gradient(120deg, transparent, #123456), url(/_theme-assets/texture.webp)"},
	}
	assets := map[string][]byte{"ambient.webp": []byte("ambient"), "texture.webp": []byte("texture")}
	raw, err := BuildPackage(m, assets, nil, "MIT", "")
	if err != nil {
		t.Fatal(err)
	}
	packaged, err := ExtractPackage(raw)
	if err != nil {
		t.Fatal(err)
	}
	received, err := Parse(packaged.ManifestRaw)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateAssetReferences(received, packaged.Assets); err != nil {
		t.Fatal(err)
	}
	css := RenderLayoutCSS(received)
	for _, want := range []string{"kokoro-theme-breathe 5s", "prefers-reduced-motion", "k-effects-paused", "/_theme-assets/" + m.ID + "/texture.webp"} {
		if !strings.Contains(css, want) {
			t.Errorf("received theme lost %q", want)
		}
	}
	if err := ValidateAssetReferences(received, packaged.Assets[:1]); err == nil {
		t.Fatal("missing assets accepted")
	}
	if err := ValidateAssetReferences(received, nil); err == nil {
		t.Fatal("resourceful standalone JSON accepted")
	}
	delete(assets, "texture.webp")
	if _, err := BuildPackage(m, assets, nil, "MIT", ""); err == nil {
		t.Fatal("builder created a package missing a referenced visual resource")
	}
}

func TestVisualRulesRejectCodeAndUnboundedMotion(t *testing.T) {
	for _, bad := range []map[string]map[string]string{
		{".card,body": {"color": "#fff"}},
		{"card": {"position": "fixed"}},
		{"card": {"background": "url(https://example.com/image.webp)"}},
		{"card": {"background": "image-set(url(/_theme-assets/a.webp) 1x)"}},
		{"card": {"box-shadow": "0 0 2px #fff;display:none"}},
		{"card": {"color": "#fff!important"}},
		{"card": {"motion": "custom-script"}},
		{"card": {"motion": "float", "motion-duration": "1ms"}},
		{"card": {"motion": "float", "motion-duration": "2000000us"}},
	} {
		if err := ValidateVisuals(bad); err == nil {
			t.Errorf("unsafe visual accepted: %v", bad)
		}
	}
}

func TestFontDescriptorsAreNotThemeCapabilities(t *testing.T) {
	_, err := Parse([]byte(`{"id":"example.font","name":"Font","version":"1","tokens":{"--kokoro-color-primary":"#fff"},"layoutSchemaVersion":2,"layout":{"fonts":[{"family":"Font","asset":"font.woff2"}]}}`))
	if err == nil {
		t.Fatal("third-party font descriptor accepted")
	}
}

func TestVisualCapabilitiesNeedDeclaredVersion(t *testing.T) {
	m := sampleManifest()
	m.LayoutSchemaVersion = 1
	m.Layout.Visuals = map[string]map[string]string{"card": {"color": "#fff"}}
	if err := m.Validate(); err == nil {
		t.Fatal("v1 manifest accepted v2 visual rules")
	}
	m.LayoutSchemaVersion = 2
	m.TokensSchemaVersion = 2
	m.Tokens["--kokoro-chart-filter"] = "drop-shadow(0 0 3px #fff)"
	if err := m.Validate(); err == nil {
		t.Fatal("v2 tokens accepted v3 visual token")
	}
}
