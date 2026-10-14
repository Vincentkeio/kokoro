package hub

import (
	"encoding/json"
	"fmt"
	"github.com/Vincentkeio/kokoro/internal/model"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMalformedPasswordHashesFailClosed(t *testing.T) {
	salt := strings.Repeat("ab", pbkdf2SaltLen)
	for _, hash := range []string{"pbkdf2$sha256$100000$" + salt + "$", "pbkdf2$sha256$100000$" + salt + "$00", "pbkdf2$sha256$100000$$" + strings.Repeat("ab", pbkdf2KeyLen)} {
		if ok, _ := verifyPassword(hash, "anything"); ok {
			t.Errorf("malformed hash authenticated: %q", hash)
		}
	}
}

func TestHardwareReportsUseRegistrationFieldBounds(t *testing.T) {
	for _, facts := range []*model.HostFacts{
		{CPUModel: strings.Repeat("X", 257)},
		{CPUCores: 4097},
		{Region: "region\nother"},
		{LocalIPs: []string{"not-an-address"}},
	} {
		if validHostFacts(facts) {
			t.Errorf("malformed host report accepted: %+v", facts)
		}
	}
	if !validHostFacts(&model.HostFacts{CPUModel: "AMD EPYC", CPUCores: 1, Country: "新加坡", Region: "Singapore", LocalIPs: []string{"10.0.0.1"}}) {
		t.Fatal("normal facts rejected")
	}
}

func TestThemeSourceCannotPublishURLCredentials(t *testing.T) {
	if _, err := safeThemeURL("https://user:secret@example.com/theme.json"); err == nil {
		t.Fatal("credential-bearing theme source accepted")
	}
}

func TestBrowserLoginOriginCheckedBeforeSessionExists(t *testing.T) {
	h, _ := newTestHub(t)
	for _, origin := range []string{"https://evil.example", "null", "http://example.com"} {
		req := httptest.NewRequest(http.MethodPost, "http://example.com/admin", strings.NewReader("username=x&password=x"))
		req.Header.Set("Origin", origin)
		called := false
		handler := h.securityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if called != (origin == "http://example.com") {
			t.Errorf("origin %q: handler=%v, status=%d", origin, called, w.Code)
		}
	}
}

func TestPublicStreamHonorsVisibilityAndKeepsUnlistedDetailUpdates(t *testing.T) {
	h, st := newTestHub(t)
	for _, visibility := range []model.Visibility{model.VisibilityPublic, model.VisibilityUnlisted, model.VisibilityPrivate} {
		id := string(visibility)
		n := &model.Node{ID: id, Name: id, Slug: id, TokenHash: "token-" + id, Visibility: visibility}
		if err := st.CreateNode(n); err != nil {
			t.Fatal(err)
		}
	}
	for _, query := range []string{"", "?node=unlisted", "?node=private"} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/stream"+query, nil)
		for _, event := range []string{"metrics", "node_status"} {
			msg := []byte(fmt.Sprintf("event: %s\ndata: {\"public\":{},\"unlisted\":{},\"private\":{}}\n\n", event))
			out := h.publicStreamMessage(req, msg)
			parts := strings.SplitN(strings.TrimSpace(string(out)), "\ndata: ", 2)
			if len(parts) != 2 {
				t.Fatalf("missing public event: %q", out)
			}
			var values map[string]json.RawMessage
			if err := json.Unmarshal([]byte(parts[1]), &values); err != nil {
				t.Fatal(err)
			}
			if values["public"] == nil || values["private"] != nil || (values["unlisted"] != nil) != (query == "?node=unlisted") {
				t.Errorf("visibility leaked or detail update lost: query=%s, data=%s", query, parts[1])
			}
		}
		if out := h.publicStreamMessage(req, []byte("event: task_status\ndata: {\"message\":\"private task error\"}\n\n")); len(out) != 0 {
			t.Fatal("administrative notification exposed")
		}
		if out := h.publicStreamMessage(req, []byte("event: task\ndata: {\"node\":\"private\",\"card\":\"secret\"}\n\n")); len(out) != 0 {
			t.Fatal("private task card exposed")
		}
		for _, event := range []string{"task", "netq", "node_online", "node_offline"} {
			key := "id"
			if event == "task" || event == "netq" {
				key = "node"
			}
			msg := []byte(fmt.Sprintf("event: %s\ndata: {\"%s\":\"public\"}\n\n", event, key))
			if len(h.publicStreamMessage(req, msg)) == 0 {
				t.Errorf("public %s realtime update lost", event)
			}
		}
	}
}

func TestDisconnectCannotClosePublisherSnapshot(t *testing.T) {
	b := newBroker()
	ch := b.subscribe()
	publisherSnapshot := ch
	b.unsubscribe(ch)
	if len(b.subs) != 0 {
		t.Fatal("disconnected subscriber retained")
	}
	// A broadcast that took its subscriber snapshot before unsubscribe remains
	// safe. The old implementation closed this channel and panicked here.
	publisherSnapshot <- []byte("metrics")
}

func TestAutomaticOwnDomainGrantDoesNotExposeOtherLocalPorts(t *testing.T) {
	h, _ := newTestHub(t)
	h.cfg.Domain = "panel.example.com"
	p := h.themeFetchPolicy()
	if !p.allowPort("panel.example.com", "443") || !p.allowPort("panel.example.com", "80") || p.allowPort("panel.example.com", "8799") || p.allowPort("panel.example.com", "6379") {
		t.Fatal("own domain grant leaked access to unrelated local services")
	}
	h.cfg.Domain = "panel.example.com:8799"
	p = h.themeFetchPolicy()
	if !p.allowPort("panel.example.com", "8799") || p.allowPort("panel.example.com", "6379") {
		t.Fatal("explicit site port ignored")
	}
}

func TestRestartRevalidatesThemesWithoutDeletingImports(t *testing.T) {
	h, st := newTestHub(t)
	if _, err := h.importManifest([]byte(`{"id":"acme.saved","name":"Saved","version":"1.0.0","tokens":{"--kokoro-color-primary":"#123456"}}`), ""); err != nil {
		t.Fatal(err)
	}
	cookies := loginAsAdmin(t, h)
	req := withCookies(httptest.NewRequest(http.MethodPost, "/theme/acme.saved", nil), cookies)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("legitimate theme selection failed: %d", w.Code)
	}
	unsafe := []byte(`{"id":"acme.unsafe","name":"Unsafe","version":"1.0.0","tokens":{"--kokoro-bg-image":"url(https://evil.example/_theme-assets/bg.png)"}}`)
	if err := st.SaveTheme(&model.Theme{ID: "acme.unsafe", Name: "Unsafe", Version: "1.0.0", Manifest: unsafe}); err != nil {
		t.Fatal(err)
	}
	restarted, err := New(h.cfg, st)
	if err != nil {
		t.Fatal(err)
	}
	if restarted.themes.Get("acme.saved") == nil {
		t.Fatal("valid imported theme lost on restart")
	}
	if restarted.ActiveThemeID() != "acme.saved" {
		t.Fatal("selected valid theme lost after restart")
	}
	if restarted.themes.Get("acme.unsafe") != nil {
		t.Fatal("stored unsafe theme activated")
	}
	rows, err := st.ListThemes()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatal("startup deleted archived themes instead of revalidating")
	}
}
