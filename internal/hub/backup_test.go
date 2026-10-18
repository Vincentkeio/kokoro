package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/Vincentkeio/kokoro/internal/migration"
	"github.com/Vincentkeio/kokoro/internal/model"
	"github.com/Vincentkeio/kokoro/internal/store"
	"mime/multipart"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBackupExportImportPreservesIdentityAndRejectsBadPassword(t *testing.T) {
	h, st := newThemeTestHub(t)
	n := &model.Node{Name: "original node", TokenHash: hashToken("nt_original")}
	if e := st.CreateNode(n); e != nil {
		t.Fatal(e)
	}
	st.SetSetting("site_name", "Original")
	os.WriteFile(filepath.Join(h.cfg.DataDir, "avatar.png"), []byte("avatar fixture"), 0600)
	form := url.Values{
		"password": {"long-backup-password"}, "confirm_password": {"long-backup-password"}, "download_id": {"test"},
		"include_nodes": {"1"}, "include_config": {"1"}, "include_themes": {"1"},
		"include_history": {"1"}, "include_content": {"1"}, "include_files": {"1"},
	}
	req := withCookies(httptest.NewRequest("POST", "/admin/backup/export", strings.NewReader(form.Encode())), loginAsAdmin(t, h))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "http://example.com")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("export: %d %s", w.Code, w.Body.String())
	}
	sealed := append([]byte(nil), w.Body.Bytes()...)
	target, targetStore := newThemeTestHub(t)
	targetStore.SetSetting("site_name", "Untouched")
	targetCookies := loginAsAdmin(t, target)
	upload := func(pass string) *httptest.ResponseRecorder {
		var body bytes.Buffer
		mw := multipart.NewWriter(&body)
		f, _ := mw.CreateFormFile("backup", "backup.kokoro")
		f.Write(sealed)
		mw.WriteField("password", pass)
		mw.WriteField("confirm", "覆盖并恢复")
		mw.Close()
		r := withCookies(httptest.NewRequest("POST", "/admin/backup/import", &body), targetCookies)
		r.Header.Set("Content-Type", mw.FormDataContentType())
		r.Header.Set("Origin", "http://example.com")
		rec := httptest.NewRecorder()
		target.ServeHTTP(rec, r)
		return rec
	}
	if rec := upload("wrong-password-long"); rec.Code != 400 {
		t.Fatalf("bad password: %d %s", rec.Code, rec.Body.String())
	}
	v, _ := targetStore.GetSetting("site_name")
	if v != "Untouched" {
		t.Fatal("failed import changed target")
	}
	if rec := upload("long-backup-password"); rec.Code != 200 {
		t.Fatalf("import: %d %s", rec.Code, rec.Body.String())
	}
	stage := <-target.restoreCh
	defer os.RemoveAll(stage)
	restored, e := store.Open(filepath.Join(stage, "kokoro.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer restored.Close()
	if e = restored.ValidateBackup(context.Background()); e != nil {
		t.Fatal(e)
	}
	got, e := restored.GetNodeByTokenHash(hashToken("nt_original"))
	if e != nil || got.ID != n.ID {
		t.Fatalf("node identity lost: %v", e)
	}
	sessions, e := restored.ListSettings(sessionKeyPrefix)
	if e != nil || len(sessions) != 0 {
		t.Fatal("exported browser sessions remain active")
	}
	if b, _ := os.ReadFile(filepath.Join(stage, "avatar.png")); string(b) != "avatar fixture" {
		t.Fatal("avatar lost")
	}
}
func TestMigrationIdentityAndCapabilityGate(t *testing.T) {
	h, st := newThemeTestHub(t)
	n := &model.Node{Name: "probe", TokenHash: hashToken("nt_probe")}
	st.CreateNode(n)
	secret := strings.Repeat("s", 64)
	id := strings.Repeat("b", 64)
	st.SetSetting("panel_migration_key", secret)
	st.SetSetting("panel_restored_backup_id", id)
	nonce := strings.Repeat("n", 64)
	body, _ := json.Marshal(migration.Proof{Nonce: nonce, MAC: migration.MAC(secret, "request", nonce)})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/api/v1/migration/check", bytes.NewReader(body)))
	if w.Code != 200 {
		t.Fatalf("proof rejected: %s", w.Body.String())
	}
	var response migration.Response
	json.Unmarshal(w.Body.Bytes(), &response)
	if !migration.Verify(secret, "response", nonce, response.MAC) {
		t.Fatal("response identity missing")
	}
	req := httptest.NewRequest("POST", "/api/v1/migration/node", nil)
	req.Header.Set("Authorization", "Bearer nt_probe")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatal("existing token not accepted")
	}
	h.migrationLoaded = true
	h.migrationPlan = &panelMigrationPlan{ID: id, Target: "https://new.example.com", Active: true, BackupID: id, Nodes: []migrationNode{{ID: n.ID, State: "waiting"}}}
	if c := h.migrationCommand(n.ID, 0); c != nil || h.migrationPlan.Nodes[0].State != "upgrade_required" {
		t.Fatal("old Agent received unsupported migration command")
	}
	if c := h.migrationCommand(n.ID, 1); c == nil || c.Type != "migrate" {
		t.Fatal("capable Agent received no migration command")
	}
	if c := h.migrationCommand(n.ID, 1); c != nil {
		t.Fatal("command redelivered too frequently")
	}
	if handled, e := h.migrationResult("other", model.CommandResult{ID: "mig_" + id, OK: true}); !handled || e == nil {
		t.Fatal("another node could acknowledge migration")
	}
}
