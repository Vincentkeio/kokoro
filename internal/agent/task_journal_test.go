package agent

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/Vincentkeio/kokoro/internal/model"
)

func TestTaskJournalRetriesAcrossRestart(t *testing.T) {
	available := false
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if !available {
			w.WriteHeader(503)
			return
		}
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	configPath := filepath.Join(t.TempDir(), "config.toml")
	newAgent := func() *Agent {
		a, err := New(&model.AgentConfig{Hub: srv.URL, Token: "nt_test"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		a.configPath = configPath
		if err := a.initTaskJournal(); err != nil {
			t.Fatal(err)
		}
		return a
	}
	a := newAgent()
	cmd := model.Command{ID: "tk_retry", Type: "test", Payload: map[string]any{"kind": "bench"}}
	if !a.acceptTask(cmd) || a.acceptTask(cmd) {
		t.Fatal("duplicate delivery must not run twice")
	}
	a.saveTaskResult(model.CommandResult{ID: cmd.ID, OK: true, Stdout: `{"event":"result","test":"cpu","ok":true,"data":{"single_eps":321}}`})
	a.flushTaskResults()
	if len(a.taskHeartbeat().IDs) != 1 {
		t.Fatal("failed upload must remain pending")
	}
	b := newAgent()
	if b.taskRecords[cmd.ID].Result.Stdout != a.taskRecords[cmd.ID].Result.Stdout {
		t.Fatal("restart lost saved result")
	}
	available = true
	b.flushTaskResults()
	if len(b.taskHeartbeat().IDs) != 0 || attempts != 2 {
		t.Fatal("result was not acknowledged and removed")
	}
	if _, err := os.Stat(b.taskRecordPath(cmd.ID)); !os.IsNotExist(err) {
		t.Fatal("acknowledged result remains on disk")
	}
	if b.acceptTask(cmd) {
		t.Fatal("late duplicate must not repeat benchmark")
	}
}

func TestTaskJournalReportsInterruptedTest(t *testing.T) {
	a, err := New(&model.AgentConfig{Hub: "https://example.invalid", Token: "nt_test"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	a.configPath = filepath.Join(t.TempDir(), "config.toml")
	if err := a.initTaskJournal(); err != nil {
		t.Fatal(err)
	}
	cmd := model.Command{ID: "tk_interrupted", Type: "test", Payload: map[string]any{"kind": "netquality"}}
	if !a.acceptTask(cmd) {
		t.Fatal("could not persist accepted test")
	}
	if err := a.initTaskJournal(); err != nil {
		t.Fatal(err)
	}
	record := a.taskRecords[cmd.ID]
	if record.Result == nil || record.Result.OK || record.Result.ExitCode != -1 || record.Result.Stderr == "" {
		t.Fatal("interrupted test must become an explicit failure result")
	}
	if a.acceptTask(cmd) {
		t.Fatal("interrupted test was silently rerun")
	}
}
