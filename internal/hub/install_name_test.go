package hub

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Vincentkeio/kokoro/internal/model"
)

func TestRegistrationUsesInstallDisplayName(t *testing.T) {
	for _, tc := range []struct{ label, preset, requested, want string }{
		{"系统备注", "预设节点名", "命令指定节点名", "命令指定节点名"},
		{"系统备注", "预设节点名", "", "预设节点名"},
		{"香港 'A' $(id)", "", "", "香港 'A' $(id)"},
		{"", "", "", "real-hostname"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			h, st := newTestHub(t)
			token, err := h.CreateInstallToken(tc.label, 0, 1, tc.preset)
			if err != nil {
				t.Fatal(err)
			}
			body, err := json.Marshal(model.RegisterRequest{InstallToken: token.Token, Hostname: "real-hostname", NodeName: tc.requested})
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodPost, "/api/v1/register", strings.NewReader(string(body)))
			w := httptest.NewRecorder()
			h.handleRegister(w, req)
			if w.Code != http.StatusOK {
				t.Fatalf("HTTP %d: %s", w.Code, w.Body.String())
			}
			var response model.RegisterResponse
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			node, err := st.GetNode(response.NodeID)
			if err != nil || node == nil {
				t.Fatalf("node missing: %v", err)
			}
			if node.Name != tc.want || node.Hostname != "real-hostname" {
				t.Fatalf("name=%q hostname=%q, want display name %q", node.Name, node.Hostname, tc.want)
			}
		})
	}
}

func TestRegistrationRejectsInvalidDisplayNameBeforeUsingToken(t *testing.T) {
	h, _ := newTestHub(t)
	token, err := h.CreateInstallToken("节点备注", 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"bad\nname", "bad\tname", strings.Repeat("名", 129)} {
		body, err := json.Marshal(model.RegisterRequest{InstallToken: token.Token, Hostname: "real-hostname", NodeName: name})
		if err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		h.handleRegister(w, httptest.NewRequest(http.MethodPost, "/api/v1/register", strings.NewReader(string(body))))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("invalid name accepted: HTTP %d", w.Code)
		}
		remaining, err := h.peekInstallToken(token.Token)
		if err != nil || remaining.Uses != 0 {
			t.Fatalf("invalid name consumed token: %v", err)
		}
	}
}

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
