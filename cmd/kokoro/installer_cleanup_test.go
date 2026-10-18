package main

import (
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
