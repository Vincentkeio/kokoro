package main

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestHubUpdateDoesNotKeepDatabaseCopyAfterFailedHealth(t *testing.T) {
	dir := t.TempDir()
	binPath := filepath.Join(dir, "kokoro")
	dbPath := filepath.Join(dir, "kokoro.db")
	if err := os.WriteFile(binPath, []byte("original"), 0755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`CREATE TABLE important_data (value TEXT); INSERT INTO important_data VALUES ('original'); PRAGMA user_version=19`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	newBin := testELF(0x3e)
	asset := "kokoro-linux-amd64"
	rel := hubTestRelease("v9.9.9", false, newBin, asset)
	svc := &stubHubService{active: true, healthErr: errors.New("failed after migration")}
	svc.onRestart = func(attempt int) error {
		if attempt != 1 {
			return nil
		}
		db, err := sql.Open("sqlite", dbPath)
		if err != nil {
			return err
		}
		defer db.Close()
		_, err = db.Exec(`UPDATE important_data SET value='migrated'; PRAGMA user_version=99`)
		return err
	}
	u := &hubUpdater{binaryPath: binPath, databasePath: dbPath, currentVersion: "v0.1.2", arch: "amd64",
		source:  &stubHubSource{releases: []hubRelease{rel}, files: map[string][]byte{"https://example.test/" + asset: newBin, "https://example.test/" + asset + ".sha256": checksumFor(newBin, asset)}},
		service: svc, assumeYes: true, out: io.Discard, precheck: func() error { return nil }, healthAttempts: 1, healthInterval: time.Millisecond}
	if err := u.Run(context.Background()); err == nil {
		t.Fatal("expected failed update")
	}
	db, err = sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version int
	var value string
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT value FROM important_data`).Scan(&value); err != nil {
		t.Fatal(err)
	}
	if version != 99 || value != "migrated" {
		t.Fatalf("database should remain as migrated: schema=%d value=%q", version, value)
	}
	if data, _ := os.ReadFile(binPath); !bytes.Equal(data, newBin) {
		t.Fatal("new binary should remain installed")
	}
	if _, err := os.Stat(filepath.Join(dir, "backups")); !os.IsNotExist(err) {
		t.Fatalf("backup directory should not exist: %v", err)
	}
}
