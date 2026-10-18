package main

import (
	"strings"
	"os"
	"path/filepath"
	"testing"
)

// 卸载只能收回安装器自己写下的配置：认标记，不认路径。
// 反例（别人的站点）必须原样保留 —— 这是"卸载了再重装"能安全兼容的前提。
func TestRemoveIfKokoroManagedKeepsForeignConfig(t *testing.T) {
	dir := t.TempDir()

	ours := filepath.Join(dir, "Caddyfile")
	if err := os.WriteFile(ours, []byte("# Managed-By=Kokoro-Installer\n\nexample.com {\n  reverse_proxy 127.0.0.1:8799\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	removed, err := removeIfKokoroManaged(ours)
	if err != nil {
		t.Fatalf("删除自己的配置失败: %v", err)
	}
	if !removed {
		t.Error("带 Kokoro 标记的配置应被删除")
	}
	if _, err := os.Stat(ours); !os.IsNotExist(err) {
		t.Error("带 Kokoro 标记的配置删除后不该还在")
	}

	foreign := filepath.Join(dir, "foreign.conf")
	body := []byte("server {\n  server_name other.example.com;\n}\n")
	if err := os.WriteFile(foreign, body, 0o644); err != nil {
		t.Fatal(err)
	}
	removed, err = removeIfKokoroManaged(foreign)
	if err != nil {
		t.Fatalf("检查别人的配置时出错: %v", err)
	}
	if removed {
		t.Error("没有 Kokoro 标记的配置不该被删除")
	}
	kept, err := os.ReadFile(foreign)
	if err != nil {
		t.Fatalf("别人的配置被删掉了: %v", err)
	}
	if string(kept) != string(body) {
		t.Error("别人的配置内容被改动了")
	}

	missing := filepath.Join(dir, "absent.conf")
	removed, err = removeIfKokoroManaged(missing)
	if err != nil {
		t.Fatalf("文件不存在不该报错: %v", err)
	}
	if removed {
		t.Error("文件不存在时不该报告已删除")
	}
}

// isKokoroManaged 是卸载路径的前置判断：文件不存在要当成"不是我们的"，
// 而不是报错退出，否则没装过的机器上卸载会中断。
func TestIsKokoroManagedMissingFile(t *testing.T) {
	managed, err := isKokoroManaged(filepath.Join(t.TempDir(), "nope"))
	if err == nil {
		t.Error("文件不存在时应返回错误供调用方判断")
	}
	if managed {
		t.Error("文件不存在时不该报告为 Kokoro 托管")
	}
}

// 安装器允许把 Kokoro 站点合并进别人的 Caddyfile。卸载时只能摘掉我们那一段：
// 别人的站点必须一个字节都不变。
func TestStripKokoroCaddyBlockKeepsForeignSites(t *testing.T) {
	foreign := "other.example.com {\n  root * /srv/other\n  file_server\n}\n"
	block := "\n" + caddyBlockBegin + "\n# " + installerMarker + "\npanel.example.com {\n  reverse_proxy 127.0.0.1:8799\n}\n\n" + caddyBlockEnd + "\n"

	remaining, found := stripKokoroCaddyBlock(foreign + block)
	if !found {
		t.Fatal("应当识别出 Kokoro 分段")
	}
	if remaining != foreign {
		t.Errorf("别人的站点被改动了:\n%q\n期望:\n%q", remaining, foreign)
	}
	if strings.Contains(remaining, "8799") {
		t.Error("Kokoro 的反代站点没有被摘掉")
	}

	if _, found := stripKokoroCaddyBlock(foreign); found {
		t.Error("没有分段的文件不该报告命中")
	}
}

// 分段摘完只剩空白，说明这份 Caddyfile 原本只属于 Kokoro，可以整份删掉。
func TestStripKokoroCaddyBlockDetectsSoleOwner(t *testing.T) {
	only := caddyBlockBegin + "\n# " + installerMarker + "\npanel.example.com {\n  reverse_proxy 127.0.0.1:8799\n}\n" + caddyBlockEnd + "\n\n"
	remaining, found := stripKokoroCaddyBlock(only)
	if !found {
		t.Fatal("应当识别出 Kokoro 分段")
	}
	if strings.TrimSpace(remaining) != "" {
		t.Errorf("摘完应当只剩空白，得到 %q", remaining)
	}
}

// removeKokoroCaddySite 对"别人的站点 + 我们的分段"只能改写文件，不能删文件。
func TestRemoveKokoroCaddySiteRewritesForeignFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "Caddyfile")
	foreign := "other.example.com {\n  file_server\n}\n"
	content := foreign + "\n" + caddyBlockBegin + "\n# " + installerMarker + "\npanel.example.com {\n  reverse_proxy 127.0.0.1:8799\n}\n" + caddyBlockEnd + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	removed, changed, err := removeKokoroCaddySite(path)
	if err != nil {
		t.Fatalf("摘除 Kokoro 站点失败: %v", err)
	}
	if removed {
		t.Fatal("文件里还有别人的站点，不该整个删掉")
	}
	if !changed {
		t.Fatal("应当报告文件被改写")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("文件不该消失: %v", err)
	}
	if !strings.Contains(string(after), "other.example.com") || !strings.Contains(string(after), "file_server") {
		t.Errorf("别人的站点被弄丢了:\n%s", after)
	}
	if strings.Contains(string(after), "8799") {
		t.Error("Kokoro 的站点没有被摘掉")
	}
}

// 整份文件都归 Kokoro 时，卸载把文件删掉，安装器重装才会走"从零"这条路。
func TestRemoveKokoroCaddySiteRemovesSoleOwnerFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "Caddyfile")
	content := caddyBlockBegin + "\n# " + installerMarker + "\npanel.example.com {\n  reverse_proxy 127.0.0.1:8799\n}\n" + caddyBlockEnd + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	removed, changed, err := removeKokoroCaddySite(path)
	if err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	if !removed || !changed {
		t.Errorf("整份归 Kokoro 时应删除文件，得到 removed=%v changed=%v", removed, changed)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("文件应当已被删除")
	}
}

// 早期版本把整份 Caddyfile 都写成 Kokoro 的（没有分段标记），卸载仍要能删掉。
func TestRemoveKokoroCaddySiteRemovesLegacyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "Caddyfile")
	legacy := "# " + installerMarker + "\n{\n  storage file_system /var/lib/caddy/kokoro-caddy\n}\n\npanel.example.com {\n  reverse_proxy 127.0.0.1:8799\n}\n"
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	removed, _, err := removeKokoroCaddySite(path)
	if err != nil {
		t.Fatalf("删除旧格式失败: %v", err)
	}
	if !removed {
		t.Error("旧格式（整份归 Kokoro）应当被删除")
	}
}

// 完全不属于 Kokoro 的配置必须原样保留。
func TestRemoveKokoroCaddySiteLeavesForeignFileAlone(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "Caddyfile")
	foreign := "other.example.com {\n  root * /srv/other\n  file_server\n}\n"
	if err := os.WriteFile(path, []byte(foreign), 0o644); err != nil {
		t.Fatal(err)
	}
	removed, changed, err := removeKokoroCaddySite(path)
	if err != nil {
		t.Fatalf("检查失败: %v", err)
	}
	if removed || changed {
		t.Errorf("不是 Kokoro 的配置不该被动过，removed=%v changed=%v", removed, changed)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("文件不该消失: %v", err)
	}
	if string(after) != foreign {
		t.Error("别人的配置内容被改动了")
	}
}

