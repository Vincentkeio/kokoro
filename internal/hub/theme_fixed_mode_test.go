package hub

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Vincentkeio/kokoro/internal/theme"
)

// A previous light-mode cookie must not change a fixed dark appearance.
func TestFixedDarkThemeIgnoresVisitorLightMode(t *testing.T) {
	r, err := theme.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: theme.ModeCookie, Value: "light"})
	h := &Hub{}
	for _, id := range []string{"kokoro.midnight"} {
		mode, defaultMode := h.resolveMode(req, r.Get(id))
		if mode != "dark" || defaultMode != "dark" {
			t.Errorf("%s: mode=%q default=%q, want fixed dark", id, mode, defaultMode)
		}
	}
}

func TestThemeAppearanceHasNoNightSwitch(t *testing.T) {
	h, _ := newTestHub(t)
	registry, err := theme.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"kokoro.simple", "kokoro.paper", "kokoro.mist", "kokoro.midnight"} {
		m := registry.Get(id)
		for _, cookie := range []string{"dark", "light"} {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.AddCookie(&http.Cookie{Name: theme.ModeCookie, Value: cookie})
			mode, _ := h.resolveMode(r, m)
			if mode != m.Mode.Default {
				t.Fatalf("%s changed by old cookie: %s", id, mode)
			}
		}
	}
	body := renderBody(t, h, httptest.NewRequest(http.MethodGet, "/", nil))
	if strings.Contains(body, "data-toggle-mode") || strings.Contains(body, `matchMedia("(prefers-color-scheme: dark)")`) {
		t.Fatal("night switch still rendered")
	}
}
