package hub

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestInstallTokenActionsStayOnSystemTab(t *testing.T) {
	h, _ := newTestHub(t)
	cookies := loginAsAdmin(t, h)
	token, err := h.CreateInstallToken("temporary", 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"create", "revoke"} {
		form := url.Values{"action": {action}, "label": {"temporary"}, "token": {token.Token}}
		req := httptest.NewRequest(http.MethodPost, "/admin/tokens", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		for _, c := range cookies {
			req.AddCookie(c)
		}
		w := httptest.NewRecorder()
		h.handleAdminTokens(w, req)
		if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/admin#sys" {
			t.Fatalf("%s: %d %s", action, w.Code, w.Header().Get("Location"))
		}
	}
	if _, err := h.peekInstallToken(token.Token); err == nil {
		t.Fatal("revoked token still valid")
	}
}
