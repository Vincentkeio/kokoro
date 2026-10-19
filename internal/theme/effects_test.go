package theme

import (
	"strings"
	"testing"
)

func sampleMotion() ThemeMotion {
	return ThemeMotion{Duration: "8s", Easing: "linear", Frames: []MotionFrame{
		{At: 0, Style: map[string]string{"background-position": "0% 50%", "opacity": "0.2"}},
		{At: 100, Style: map[string]string{"background-position": "100% 50%", "opacity": "0.6"}},
	}}
}

func TestMotionLayersAndResponsiveStylesRemainPortable(t *testing.T) {
	m := sampleManifest()
	m.Layout.Motions = map[string]ThemeMotion{"scan": sampleMotion()}
	m.Layout.Layers = map[string][]ThemeLayer{"card": {{Style: map[string]string{"background": "url(/_theme-assets/scan.webp)", "motion": "custom:scan"}, HideOnMobile: true, HideOnReducedMotion: true}}}
	m.Layout.Responsive = map[string]map[string]map[string]string{"mobile": {"button-focus": {"outline": "2px solid #fff", "outline-offset": "3px"}, "card": {"motion-duration": "9s"}}}
	raw, err := BuildPackage(m, map[string][]byte{"scan.webp": []byte("texture")}, nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	b, err := ExtractPackage(raw)
	if err != nil {
		t.Fatal(err)
	}
	received, err := Parse(b.ManifestRaw)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateAssetReferences(received, b.Assets); err != nil {
		t.Fatal(err)
	}
	css := RenderLayoutCSS(received)
	for _, wanted := range []string{"@keyframes kokoro-custom-acme-midnight-rain-scan", "outline:2px solid #fff", "animation-duration:9s", "@container k-page (max-width:640px)", "animation:none!important", "data-k-layer-index=\"0\"", "scan.webp", "display:none"} {
		if !strings.Contains(css, wanted) {
			t.Errorf("received theme lost %q", wanted)
		}
	}
	bindings := LayerBindingsJSON(received)
	if !strings.Contains(bindings, `"selector":".card"`) || strings.Contains(bindings, "scan.webp") {
		t.Fatalf("unexpected client layer metadata: %s", bindings)
	}
	if err := ValidateAssetReferences(received, nil); err == nil {
		t.Fatal("missing layer artwork accepted")
	}
	// A different author ID must receive a separate generated animation name.
	received.ID = "another.copy"
	if !strings.Contains(RenderLayoutCSS(received), "kokoro-custom-another-copy-scan") {
		t.Fatal("motion tied to the source ID")
	}
}

func TestMotionAndLayerBoundaries(t *testing.T) {
	for _, change := range []func(*ThemeMotion){
		func(m *ThemeMotion) { m.Duration = "1ms" },
		func(m *ThemeMotion) { m.Easing = "ease;display:none" },
		func(m *ThemeMotion) { m.Frames[1].At = 0 },
		func(m *ThemeMotion) { m.Frames[1].At = 99 },
		func(m *ThemeMotion) { m.Frames[0].Style = map[string]string{"content": "secret"} },
		func(m *ThemeMotion) { m.Frames[0].Style = map[string]string{"transform": "scale(1);display:none"} },
	} {
		motion := sampleMotion()
		change(&motion)
		if err := ValidateMotions(map[string]ThemeMotion{"scan": motion}); err == nil {
			t.Fatalf("invalid motion accepted: %+v", motion)
		}
	}
	if err := ValidateMotions(map[string]ThemeMotion{"bad}body": sampleMotion()}); err == nil {
		t.Fatal("injected motion name accepted")
	}
	if err := ValidateLayers(map[string][]ThemeLayer{"card": {{Style: map[string]string{"motion": "custom:missing"}}}}, nil); err == nil {
		t.Fatal("undefined layer motion accepted")
	}
	if err := ValidateLayers(map[string][]ThemeLayer{"body": {{Style: map[string]string{"background": "#fff"}}}}, nil); err == nil {
		t.Fatal("arbitrary layer host accepted")
	}
	layer := ThemeLayer{Style: map[string]string{"opacity": "0.5"}}
	if err := ValidateLayers(map[string][]ThemeLayer{"card": {layer, layer, layer}}, nil); err == nil {
		t.Fatal("too many layers accepted")
	}
	m := sampleManifest()
	m.Layout.Responsive = map[string]map[string]map[string]string{"unbounded": {"card": {"color": "#fff"}}}
	if err := m.Validate(); err == nil {
		t.Fatal("custom breakpoint accepted")
	}
	for _, properties := range []map[string]string{{"position": "fixed"}, {"pointer-events": "auto"}, {"content": "click"}, {"z-index": "9999"}} {
		if err := ValidateLayers(map[string][]ThemeLayer{"card": {{Style: properties}}}, nil); err == nil {
			t.Fatalf("interactive layer capability accepted: %+v", properties)
		}
	}
}

func TestAssetReferencesIncludeResponsiveAndLayerVariants(t *testing.T) {
	for _, apply := range []func(*Manifest){
		func(m *Manifest) {
			m.Layout.Responsive = map[string]map[string]map[string]string{"mobile": {"card": {"background-image": "url(/_theme-assets/mobile.webp)"}}}
		},
		func(m *Manifest) {
			m.Layout.Layers = map[string][]ThemeLayer{"card": {{Style: map[string]string{"opacity": "0.5"}, Mobile: map[string]string{"background": "url(/_theme-assets/mobile.webp)"}}}}
		},
	} {
		m := sampleManifest()
		apply(m)
		if err := m.Validate(); err != nil {
			t.Fatal(err)
		}
		if err := ValidateAssetReferences(m, nil); err == nil {
			t.Fatal("variant-only missing resource accepted")
		}
		if err := ValidateAssetReferences(m, []Asset{{Name: "assets/mobile.webp"}}); err != nil {
			t.Fatal(err)
		}
	}
}
