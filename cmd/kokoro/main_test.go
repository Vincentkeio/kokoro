package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRefreshInstallerUpdatesVerifiedScript(t *testing.T) {
	script := []byte("#!/bin/sh\necho updated\n")
	digest := sha256.Sum256(script)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/dl/install.sh":
			_, _ = w.Write(script)
		case "/api/v1/dl/install.sh.sha256":
			_, _ = fmt.Fprintf(w, "%s  install.sh\n", hex.EncodeToString(digest[:]))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	dir := t.TempDir()
	installer := filepath.Join(dir, "install.sh")
	if err := os.WriteFile(installer, []byte("#!/bin/sh\necho old\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := refreshInstaller(server.URL, installer, server.Client()); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(installer)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(script) {
		t.Fatalf("installer = %q, want %q", got, script)
	}
}

func TestRefreshInstallerRejectsBadChecksumWithoutReplacing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".sha256") {
			_, _ = fmt.Fprintln(w, strings.Repeat("0", 64)+"  install.sh")
			return
		}
		_, _ = w.Write([]byte("#!/bin/sh\necho untrusted\n"))
	}))
	defer server.Close()

	dir := t.TempDir()
	installer := filepath.Join(dir, "install.sh")
	original := []byte("#!/bin/sh\necho old\n")
	if err := os.WriteFile(installer, original, 0700); err != nil {
		t.Fatal(err)
	}
	if err := refreshInstaller(server.URL, installer, server.Client()); err == nil {
		t.Fatal("refreshInstaller succeeded with an invalid checksum")
	}
	got, err := os.ReadFile(installer)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(original) {
		t.Fatalf("failed refresh changed installer: %q", got)
	}
}
