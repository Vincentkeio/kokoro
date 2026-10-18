package agent

import (
	"context"
	"encoding/json"
	"github.com/Vincentkeio/kokoro/internal/migration"
	"github.com/Vincentkeio/kokoro/internal/model"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMigrationVerifiesExistingTokenNodeAndBackup(t *testing.T) {
	id := strings.Repeat("b", 64)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/migration/node" || r.Header.Get("Authorization") != "Bearer nt_original" {
			w.WriteHeader(401)
			return
		}
		json.NewEncoder(w).Encode(migration.NodeResponse{NodeID: "nd_original", BackupID: id})
	}))
	defer server.Close()
	for _, tc := range []struct {
		node, backup string
		ok           bool
	}{{"nd_original", id, true}, {"wrong", id, false}, {"nd_original", strings.Repeat("a", 64), false}} {
		e := verifyMigrationTarget(context.Background(), server.Client(), server.URL, "nt_original", tc.node, tc.backup)
		if (e == nil) != tc.ok {
			t.Fatalf("identity check: %v", e)
		}
	}
}
func TestFailedMigrationDoesNotReplaceConfig(t *testing.T) {
	cfg := &model.AgentConfig{Hub: "https://old.example.com", Token: "nt_original", NodeID: "nd_original"}
	a, e := New(cfg, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer a.client.CloseIdleConnections()
	a.configPath = filepath.Join(t.TempDir(), "config.toml")
	saveConfig(a.configPath, func(m map[string]string) { m["hub"] = cfg.Hub; m["token"] = cfg.Token })
	seen := false
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { seen = true; w.WriteHeader(200) }))
	defer server.Close()
	before, _ := os.ReadFile(a.configPath)
	res := a.migrate(model.Command{ID: "migration", Payload: map[string]any{"hub": server.URL, "backup_id": strings.Repeat("b", 64)}})
	after, _ := os.ReadFile(a.configPath)
	if res.OK || seen || string(before) != string(after) || a.hub != cfg.Hub {
		t.Fatal("failed migration changed identity or leaked token")
	}
}
func TestMigrationFirstConnectionFailureRestoresOldConfig(t *testing.T) {
	a, e := New(&model.AgentConfig{Hub: "https://127.0.0.1:1", Token: "nt_original", NodeID: "nd_original"}, nil)
	if e != nil {
		t.Fatal(e)
	}
	a.configPath = filepath.Join(t.TempDir(), "config.toml")
	saveConfig(a.configPath, func(m map[string]string) {
		m["hub"] = a.hub
		m["token"] = "nt_original"
		m["migration_previous_hub"] = "https://old.example.com"
		m["migration_backup_id"] = strings.Repeat("b", 64)
	})
	if e = a.resumeMigration(context.Background()); e != nil {
		t.Fatal(e)
	}
	defer a.client.CloseIdleConnections()
	raw, _ := loadRaw(a.configPath)
	if a.hub != "https://old.example.com" || raw["hub"] != a.hub || raw["token"] != "nt_original" || raw["migration_backup_id"] != "" {
		t.Fatal("failed handoff did not restore original config")
	}
}
