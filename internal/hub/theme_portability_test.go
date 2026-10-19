package hub

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Vincentkeio/kokoro/internal/theme"
)

func TestIncompleteManifestAndFailedReplacementKeepExistingTheme(t *testing.T) {
	h, st := newThemeTestHub(t)
	m, err := theme.Parse([]byte(`{"id":"peer.complete","name":"Complete","version":"1.0.0","tokens":{"--kokoro-bg-image":"url(/_theme-assets/bg.webp)"}}`))
	if err != nil {
		t.Fatal(err)
	}
	old, err := theme.BuildPackage(m, map[string][]byte{"bg.webp": []byte("old image")}, nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.installBundle(old, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := h.importManifest([]byte(`{"id":"peer.complete","name":"Incomplete","version":"2.0.0","tokens":{"--kokoro-bg-image":"url(/_theme-assets/missing.webp)"}}`), ""); err == nil {
		t.Fatal("resourceful bare manifest replaced a complete theme")
	}
	m.Version = "2.0.0"
	updated, err := theme.BuildPackage(m, map[string][]byte{"bg.webp": []byte("new image")}, nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	h.store = nil // Force a persistence failure after successful parsing.
	_, err = h.installBundle(updated, "")
	h.store = st
	if err == nil {
		t.Fatal("replacement succeeded without storage")
	}
	if got := h.themes.Get(m.ID); got == nil || got.Version != "1.0.0" {
		t.Fatalf("old manifest lost: %+v", got)
	}
	if got, ok := h.themeAssets.get(m.ID, "assets/bg.webp"); !ok || string(got) != "old image" {
		t.Fatal("old resources lost")
	}
}

func TestGrabRejectsMismatchedPackageIdentity(t *testing.T) {
	h, _ := newThemeTestHub(t)
	bundle := makeBundle(t, "peer.other", nil)
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/theme.json" {
			w.Write([]byte(`{"id":"peer.requested","name":"Requested","version":"1","tokens":{"--kokoro-color-primary":"#fff"}}`))
			return
		}
		w.Write(bundle)
	}))
	defer peer.Close()
	if _, _, err := h.grabTheme(peer.URL); err == nil {
		t.Fatal("mismatched identity accepted")
	}
	if h.themes.Get("peer.other") != nil || h.themes.Get("peer.requested") != nil {
		t.Fatal("mismatched theme registered")
	}
}

func TestGitHubDirectoryPrefersCompletePackage(t *testing.T) {
	h, _ := newThemeTestHub(t)
	bundle := makeBundle(t, "peer.full", map[string][]byte{"bg.webp": []byte("image")})
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/author/repo/HEAD/theme.kokoro-theme":
			w.Write(bundle)
		case "/author/repo/HEAD/theme.json":
			w.Write([]byte(`{"id":"peer.plain","name":"Plain","version":"1","tokens":{"--kokoro-color-primary":"#fff"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer peer.Close()
	withRawHost(t, peer.URL+"/")
	m, kind, err := h.grabTheme("https://github.com/author/repo")
	if err != nil {
		t.Fatal(err)
	}
	if kind != "bundle" || m.ID != "peer.full" {
		t.Fatalf("got %s/%s", kind, m.ID)
	}
}
