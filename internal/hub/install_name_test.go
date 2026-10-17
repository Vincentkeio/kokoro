package hub

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallPresetNamePersistsAndIsSafelyQuoted(t *testing.T) {
	h, _ := newTestHub(t)
	name := "东京 'Node' $(printf injected) `printf injected`"
	token, err := h.CreateInstallToken("preset", 0, 1, name)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := h.peekInstallToken(token.Token)
	if err != nil || saved.NodeName != name {
		t.Fatal(saved, err)
	}
	req := httptest.NewRequest(http.MethodGet, "/i/"+token.Token, nil)
	w := httptest.NewRecorder()
	h.handleInstallScript(w, req)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	assignment := ""
	for _, line := range strings.Split(w.Body.String(), "\n") {
		if strings.HasPrefix(line, "KOKORO_NODE_NAME=") {
			assignment = line
		}
	}
	if assignment == "" || !strings.Contains(w.Body.String(), `set -- --name "$KOKORO_NODE_NAME" "$@"`) {
		t.Fatal("preset not passed to installer")
	}
	path := filepath.Join(t.TempDir(), "check-name.sh")
	if err = os.WriteFile(path, []byte(assignment+"\nprintf '%s' \"$KOKORO_NODE_NAME\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("bash", path).Output()
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != name {
		t.Fatalf("name was evaluated by shell: %q", out)
	}
}

func TestInstallCommandNameOverrideAndValidation(t *testing.T) {
	h, _ := newTestHub(t)
	token, _ := h.CreateInstallToken("preset", 0, 1, "旧名字")
	name := "洛杉矶 VPS A"
	req := httptest.NewRequest(http.MethodGet, "/i/"+token.Token+"?name="+url.QueryEscape(name), nil)
	w := httptest.NewRecorder()
	h.handleInstallScript(w, req)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "KOKORO_NODE_NAME="+shellLiteral(name)) {
		t.Fatal(w.Code, w.Body.String())
	}
	req = httptest.NewRequest(http.MethodGet, "/i/"+token.Token+"?name=bad%0Aname", nil)
	w = httptest.NewRecorder()
	h.handleInstallScript(w, req)
	if w.Code != 400 {
		t.Fatal(w.Code)
	}
	if _, err := h.CreateInstallToken("too long", 0, 1, strings.Repeat("名", 129)); err == nil {
		t.Fatal("oversized preset accepted")
	}
}

func TestInstallCommandNameUsesRemark(t *testing.T) {
	h, _ := newTestHub(t)
	name := "香港 'A' $(id)"
	form := url.Values{"action": {"create"}, "label": {name}}
	req := httptest.NewRequest(http.MethodPost, "/admin/tokens", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for _, c := range loginAsAdmin(t, h) {
		req.AddCookie(c)
	}
	w := httptest.NewRecorder()
	h.handleAdminTokens(w, req)
	if w.Code != http.StatusSeeOther {
		t.Fatal(w.Code)
	}
	tokens, err := h.ListInstallTokens()
	if err != nil {
		t.Fatal(err)
	}
	var created *installToken
	for i := range tokens {
		if tokens[i].Label == name {
			created = &tokens[i]
			break
		}
	}
	if created == nil || created.NodeName != name {
		t.Fatalf("remark not used as name: %+v", created)
	}
	w = httptest.NewRecorder()
	h.handleInstallScript(w, httptest.NewRequest(http.MethodGet, "/i/"+created.Token, nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "KOKORO_NODE_NAME="+shellLiteral(name)) {
		t.Fatal("installer did not receive remark")
	}
	legacy, _ := h.CreateInstallToken("旧备注", 0, 1)
	w = httptest.NewRecorder()
	h.handleInstallScript(w, httptest.NewRequest(http.MethodGet, "/i/"+legacy.Token, nil))
	if !strings.Contains(w.Body.String(), "KOKORO_NODE_NAME="+shellLiteral("旧备注")) {
		t.Fatal("legacy remark missing")
	}
}
