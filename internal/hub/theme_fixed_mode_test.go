package hub

import (
	"net/http"
	"net/http/httptest"
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
	for _, id := range []string{"kokoro.midnight", "kokoro.tech"} {
		mode, defaultMode := h.resolveMode(req, r.Get(id))
		if mode != "dark" || defaultMode != "dark" {
			t.Errorf("%s: mode=%q default=%q, want fixed dark", id, mode, defaultMode)
		}
	}
}
