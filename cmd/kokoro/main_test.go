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

// TestFstabWithoutSwapPathsRemovesOnlyKokoroSwap 卸载面板时清 fstab：
// 只删 Kokoro 自己的 swap 行，别的 swap 与普通挂载一行都不能动。
func TestFstabWithoutSwapPathsRemovesOnlyKokoroSwap(t *testing.T) {
	paths := map[string]bool{
		"/var/lib/kokoro/swapfile":      true,
		"/var/lib/kokoro-swap/swapfile": true,
	}
	src := strings.Join([]string{
		"# /etc/fstab",
		"/var/lib/kokoro/swapfile none swap sw 0 0 # kokoro-managed-swap",
		"/swap.img none swap sw 0 0",
		"/dev/vda1 / ext4 defaults 0 1",
		"/var/lib/kokoro-swap/swapfile none swap sw 0 0 # kokoro-managed-swap",
		"",
	}, "\n")

	got := fstabWithoutSwapPaths(src, paths)
	if strings.Contains(got, "/var/lib/kokoro/swapfile") || strings.Contains(got, "/var/lib/kokoro-swap/swapfile") {
		t.Fatalf("Kokoro 的 swap 行应被删除：\n%s", got)
	}
	for _, keep := range []string{"# /etc/fstab", "/swap.img none swap sw 0 0", "/dev/vda1 / ext4 defaults 0 1"} {
		if !strings.Contains(got, keep) {
			t.Errorf("不该动这一行：%q\n%s", keep, got)
		}
	}
}

// TestSafePurgeDir 卸载删数据前的路径闸门：只有 Kokoro 自己的目录能删。
func TestSafePurgeDir(t *testing.T) {
	for _, ok := range []string{"/var/lib/kokoro", "/www/wwwroot/kokoro/data", "/var/lib/kokoro-swap"} {
		if !safePurgeDir(ok) {
			t.Errorf("%s 应允许删除", ok)
		}
	}
	for _, bad := range []string{"", "/", "/var/lib", "/etc", "relative/kokoro", "/var/lib/other", "/var/lib/kokoro/../.."} {
		if safePurgeDir(bad) {
			t.Errorf("%s 不该允许删除", bad)
		}
	}
}
