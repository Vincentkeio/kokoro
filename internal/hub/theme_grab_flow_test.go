package hub

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Vincentkeio/kokoro/internal/theme"
)

func TestGrabThemePeerFormFlow(t *testing.T) {
	peer, _ := newThemeTestHub(t)
	m, err := theme.Parse([]byte(`{"id":"peer.flow","name":"分享测试","version":"1.0.0","tokens":{"--kokoro-bg-image":"url(/_theme-assets/bg.webp)"}}`))
	if err != nil {
		t.Fatal(err)
	}
	asset := []byte("WEBP fixture")
	bundle, err := theme.BuildPackage(m, map[string][]byte{"bg.webp": asset}, nil, "MIT", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = peer.installBundle(bundle, ""); err != nil {
		t.Fatal(err)
	}
	peer.setActiveTheme(m.ID)
	server := httptest.NewServer(peer)
	defer server.Close()

	for _, suffix := range []string{"", "/theme.json", "/theme.json?share=1", "/theme-bundle/peer.flow", "/theme-bundle/peer.flow?share=1"} {
		t.Run(suffix, func(t *testing.T) {
			h, st := newThemeTestHub(t)
			cookies := loginAsAdmin(t, h)
			form := url.Values{"url": {server.URL + suffix}}
			req := withCookies(httptest.NewRequest(http.MethodPost, "/admin/themes/grab", strings.NewReader(form.Encode())), cookies)
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			location, _ := url.Parse(w.Header().Get("Location"))
			if w.Code != http.StatusSeeOther || strings.Contains(location.Query().Get("msg"), "失败") || h.themes.Get(m.ID) == nil {
				t.Fatalf("form grab failed: status=%d feedback=%s", w.Code, location.Query().Get("msg"))
			}
			if got, ok := h.storedBundle(m.ID); !ok || !bytes.Equal(got, bundle) {
				t.Fatal("complete bundle not persisted")
			}
			if h.ActiveThemeID() != theme.DefaultID {
				t.Fatal("grabbing must not enable the theme")
			}
			restarted := newHubOn(t, st, h.cfg.DataDir)
			if restarted.themes.Get(m.ID) == nil {
				t.Fatal("grabbed theme lost after restart")
			}
			w = httptest.NewRecorder()
			restarted.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/_theme-assets/peer.flow/bg.webp", nil))
			if w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), asset) {
				t.Fatal("wallpaper asset unavailable after restart")
			}
			for _, source := range []string{server.URL, server.URL + "/theme-bundle/peer.flow"} {
				if _, kind, err := restarted.grabTheme(source); err != nil || kind != "existing" {
					t.Fatalf("duplicate grab: kind=%s err=%v", kind, err)
				}
			}
		})
	}
}

func TestGrabThemeGitHubKeepsExisting(t *testing.T) {
	for _, tc := range []struct {
		name         string
		file, bundle bool
	}{
		{"manifest-directory", false, false}, {"manifest-file", true, false},
		{"bundle-directory", false, true}, {"bundle-file", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := []byte(testThemeJSON("peer.existing", "远程版本"))
			filename := "/theme.json"
			if tc.bundle {
				raw = makeBundle(t, "peer.existing", nil)
				filename = "/theme.kokoro-theme"
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, filename) {
					_, _ = w.Write(raw)
					return
				}
				http.NotFound(w, r)
			}))
			defer server.Close()
			withRawHost(t, server.URL+"/")
			h, _ := newThemeTestHub(t)
			if _, err := h.importManifest([]byte(testThemeJSON("peer.existing", "本地修改")), ""); err != nil {
				t.Fatal(err)
			}
			source := "https://github.com/u/r"
			if tc.file {
				source += "/blob/main" + filename
			}
			m, kind, err := h.grabTheme(source)
			if err != nil || kind != "existing" || m.Name != "本地修改" {
				t.Fatalf("existing theme overwritten: kind=%s err=%v theme=%+v", kind, err, m)
			}
		})
	}
}
