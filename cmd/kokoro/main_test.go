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

// TestHubSwapFilesArePreserved 卸载删数据时，旧版落在数据目录里的交换文件必须留下：
// 交换空间是这台机器的资源，不是面板数据。fstab 也一概不动。
func TestHubSwapFilesArePreserved(t *testing.T) {
	// filepath 在 Windows 上用 "\" 拼接，这里只比文件名——这些路径运行时才在 Linux 上解析。
	want := []string{"swapfile", "swapfile.new", "swapfile.old"}
	got := hubSwapFiles("/var/lib/kokoro")
	if len(got) != len(want) {
		t.Fatalf("应当列出 %d 个旧版交换文件，得到 %v", len(want), got)
	}
	for i, p := range got {
		if filepath.Base(p) != want[i] {
			t.Errorf("第 %d 项应为 %s，得到 %q", i, want[i], p)
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
