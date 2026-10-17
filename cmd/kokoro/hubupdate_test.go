package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// testELF 造一个只够格式校验用的最小 64 位小端 ELF 头。
func testELF(machine uint16) []byte {
	b := make([]byte, 64)
	copy(b, []byte{0x7f, 'E', 'L', 'F'})
	b[4] = 2                                   // EI_CLASS = ELFCLASS64
	b[5] = 1                                   // EI_DATA = ELFDATA2LSB
	binary.LittleEndian.PutUint16(b[16:18], 2) // e_type = ET_EXEC
	binary.LittleEndian.PutUint16(b[18:20], machine)
	return b
}

func checksumFor(data []byte, name string) []byte {
	sum := sha256.Sum256(data)
	return []byte(fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), name))
}

func hubTestRelease(tag string, prerelease bool, binary []byte, assetName string) hubRelease {
	return hubRelease{
		Tag:        tag,
		Prerelease: prerelease,
		Assets: []hubAsset{
			{Name: assetName, URL: "https://example.test/" + assetName, Size: int64(len(binary))},
			{Name: assetName + ".sha256", URL: "https://example.test/" + assetName + ".sha256", Size: 80},
		},
	}
}

type stubHubSource struct {
	releases []hubRelease
	files    map[string][]byte
}

func (s *stubHubSource) Releases(context.Context) ([]hubRelease, error) { return s.releases, nil }

func (s *stubHubSource) Download(_ context.Context, rawURL string, limit int64) ([]byte, error) {
	data, ok := s.files[rawURL]
	if !ok {
		return nil, fmt.Errorf("测试桩没有 %s", rawURL)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("超过大小限制")
	}
	return data, nil
}

type stubHubService struct {
	restartErr error
	active     bool
	healthErr  error
	restarts   int
	onRestart  func(int) error
}

func (s *stubHubService) Stop(context.Context) error { return nil }

func (s *stubHubService) Restart(context.Context) error {
	s.restarts++
	if s.onRestart != nil {
		return s.onRestart(s.restarts)
	}
	return s.restartErr
}
func (s *stubHubService) Active(context.Context) (bool, error) {
	return s.active, nil
}
func (s *stubHubService) Healthy(context.Context) error { return s.healthErr }

func TestCompareKokoroVersion(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"v0.1.2", "v0.1.1", 1},
		{"0.1.3-preview.7", "v0.1.2", 1}, // 预览版可以比旧的正式版新
		{"v0.1.3-preview.7", "v0.1.3", -1},
		{"v0.1.3-preview.10", "v0.1.3-preview.7", 1},
		{"v0.1.3-preview.7", "v0.1.3-preview.7", 0},
		{"v0.2.0", "v0.1.9", 1},
	}
	for _, c := range cases {
		got := compareKokoroVersion(c.a, c.b)
		if (got < 0) != (c.want < 0) || (got > 0) != (c.want > 0) {
			t.Errorf("compareKokoroVersion(%q, %q) = %d，期望符号 %d", c.a, c.b, got, c.want)
		}
	}
}

func TestVerifyHubBinaryFormat(t *testing.T) {
	if err := verifyHubBinaryFormat(testELF(0x3e), "amd64"); err != nil {
		t.Errorf("合法 amd64 ELF 被拒：%v", err)
	}
	if err := verifyHubBinaryFormat(testELF(0xb7), "arm64"); err != nil {
		t.Errorf("合法 arm64 ELF 被拒：%v", err)
	}
	if err := verifyHubBinaryFormat(testELF(0x3e), "arm64"); err == nil {
		t.Error("架构不符的 ELF 应被拒")
	}
	if err := verifyHubBinaryFormat([]byte("#!/bin/sh\n"), "amd64"); err == nil {
		t.Error("非 ELF 内容应被拒")
	}
	bad := testELF(0x3e)
	bad[4] = 1 // 32 位
	if err := verifyHubBinaryFormat(bad, "amd64"); err == nil {
		t.Error("32 位 ELF 应被拒")
	}
	bad = testELF(0x3e)
	bad[5] = 2 // 大端
	if err := verifyHubBinaryFormat(bad, "amd64"); err == nil {
		t.Error("大端 ELF 应被拒")
	}
}

func TestHubAssetURLsReportsMissingAssets(t *testing.T) {
	rel := hubRelease{Tag: "v1.0.0", Assets: []hubAsset{{Name: "kokoro-linux-amd64", URL: "u"}}}
	if _, _, err := hubAssetURLs(rel, "kokoro-linux-amd64"); err == nil || !strings.Contains(err.Error(), "sha256") {
		t.Errorf("缺校验文件应报明确错误，得到 %v", err)
	}
	if _, _, err := hubAssetURLs(rel, "kokoro-linux-arm64"); err == nil || !strings.Contains(err.Error(), "缺少资产") {
		t.Errorf("缺二进制应报明确错误，得到 %v", err)
	}
}

func TestChooseReleaseDefaultsToHighestVersion(t *testing.T) {
	releases := []hubRelease{
		{Tag: "v0.1.3-preview.9", Prerelease: true},
		{Tag: "v0.1.3-preview.31", Prerelease: true},
		{Tag: "v0.1.2"},
	}
	u := &hubUpdater{currentVersion: "v0.1.2", out: io.Discard, assumeYes: true}
	got, err := u.chooseRelease(releases)
	if err != nil {
		t.Fatal(err)
	}
	if got.Tag != "v0.1.3-preview.31" {
		t.Fatalf("应选择编号最高的兼容版本，得到 %s", got.Tag)
	}
	if _, err := u.chooseRelease(nil); err == nil {
		t.Error("没有可用版本时应报错")
	}
}

// TestGitHubHubSourceKeepsPrereleasesSkipsDrafts 发布列表必须保留 Preview：
// 只查 /releases/latest 会把预览版挡掉，导致 Preview 环境被迫降到正式版。
func TestGitHubHubSourceKeepsPrereleasesSkipsDrafts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/repos/Vincentkeio/kokoro/releases") {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`[
			{"tag_name":"v0.1.3-preview.7","draft":false,"prerelease":true,"assets":[]},
			{"tag_name":"v0.1.3-preview.8","draft":true,"prerelease":true,"assets":[]},
			{"tag_name":"v0.1.2","draft":false,"prerelease":false,"assets":[]}
		]`))
	}))
	defer server.Close()

	src := newGitHubHubSource(server.Client())
	src.apiBase = server.URL
	releases, err := src.Releases(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(releases) != 2 {
		t.Fatalf("应过滤掉 draft，剩 2 条，得到 %d 条：%+v", len(releases), releases)
	}
	if releases[0].Tag != "v0.1.3-preview.7" || !releases[0].Prerelease {
		t.Fatalf("第一条应是最新预览版，得到 %+v", releases[0])
	}
}

func TestHubUpdateReplacesBinaryOnSuccess(t *testing.T) {
	dir := t.TempDir()
	binaryPath := filepath.Join(dir, "kokoro")
	old := []byte("old-hub-binary")
	if err := os.WriteFile(binaryPath, old, 0o755); err != nil {
		t.Fatal(err)
	}
	newBin := testELF(0x3e)
	asset := "kokoro-linux-amd64"
	rel := hubTestRelease("v9.9.9-preview.1", true, newBin, asset)
	src := &stubHubSource{
		releases: []hubRelease{rel},
		files: map[string][]byte{
			"https://example.test/" + asset:             newBin,
			"https://example.test/" + asset + ".sha256": checksumFor(newBin, asset),
		},
	}
	svc := &stubHubService{active: true}
	u := &hubUpdater{
		binaryPath: binaryPath, currentVersion: "v0.1.2", arch: "amd64",
		source: src, service: svc, assumeYes: true, out: io.Discard,
		precheck: func() error { return nil }, healthAttempts: 2, healthInterval: time.Millisecond,
	}
	if err := u.Run(context.Background()); err != nil {
		t.Fatalf("Run 失败：%v", err)
	}
	got, err := os.ReadFile(binaryPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, newBin) {
		t.Fatalf("二进制未被替换：%q", got)
	}
	backups, _ := filepath.Glob(binaryPath + ".bak-*")
	if len(backups) != 1 {
		t.Fatalf("应保留 1 份回滚备份，得到 %d", len(backups))
	}
	if bak, _ := os.ReadFile(backups[0]); !bytes.Equal(bak, old) {
		t.Fatalf("备份内容应为旧程序，得到 %q", bak)
	}
	if svc.restarts != 1 {
		t.Fatalf("成功路径应只重启 1 次，实际 %d", svc.restarts)
	}
}

func TestHubUpdateRollsBackWhenHealthFails(t *testing.T) {
	dir := t.TempDir()
	binaryPath := filepath.Join(dir, "kokoro")
	old := []byte("old-hub-binary")
	if err := os.WriteFile(binaryPath, old, 0o755); err != nil {
		t.Fatal(err)
	}
	newBin := testELF(0x3e)
	asset := "kokoro-linux-amd64"
	rel := hubTestRelease("v9.9.9", false, newBin, asset)
	src := &stubHubSource{
		releases: []hubRelease{rel},
		files: map[string][]byte{
			"https://example.test/" + asset:             newBin,
			"https://example.test/" + asset + ".sha256": checksumFor(newBin, asset),
		},
	}
	svc := &stubHubService{active: true, healthErr: errors.New("端点 500")}
	u := &hubUpdater{
		binaryPath: binaryPath, currentVersion: "v0.1.2", arch: "amd64",
		source: src, service: svc, assumeYes: true, out: io.Discard,
		precheck: func() error { return nil }, healthAttempts: 2, healthInterval: time.Millisecond,
	}
	err := u.Run(context.Background())
	if err == nil {
		t.Fatal("健康检查失败时 Run 应返回错误")
	}
	if !strings.Contains(err.Error(), "回滚") {
		t.Errorf("错误信息应说明已回滚，得到 %v", err)
	}
	got, _ := os.ReadFile(binaryPath)
	if !bytes.Equal(got, old) {
		t.Fatalf("回滚后应恢复旧程序，得到 %q", got)
	}
	if svc.restarts != 2 {
		t.Fatalf("应重启 2 次（更新 + 回滚），实际 %d", svc.restarts)
	}
}

func TestHubUpdateRejectsBadChecksumWithoutTouchingBinary(t *testing.T) {
	dir := t.TempDir()
	binaryPath := filepath.Join(dir, "kokoro")
	old := []byte("old-hub-binary")
	if err := os.WriteFile(binaryPath, old, 0o755); err != nil {
		t.Fatal(err)
	}
	newBin := testELF(0x3e)
	asset := "kokoro-linux-amd64"
	rel := hubTestRelease("v9.9.9", false, newBin, asset)
	src := &stubHubSource{
		releases: []hubRelease{rel},
		files: map[string][]byte{
			"https://example.test/" + asset: newBin,
			// 摘要对不上：必须拒绝，且不能碰现有程序
			"https://example.test/" + asset + ".sha256": checksumFor([]byte("tampered"), asset),
		},
	}
	svc := &stubHubService{active: true}
	u := &hubUpdater{
		binaryPath: binaryPath, currentVersion: "v0.1.2", arch: "amd64",
		source: src, service: svc, assumeYes: true, out: io.Discard,
		precheck: func() error { return nil }, healthAttempts: 2, healthInterval: time.Millisecond,
	}
	err := u.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("应因校验失败报错，得到 %v", err)
	}
	got, _ := os.ReadFile(binaryPath)
	if !bytes.Equal(got, old) {
		t.Fatalf("校验失败不该改动现有程序，得到 %q", got)
	}
	if svc.restarts != 0 {
		t.Fatalf("校验失败不该重启服务，实际 %d 次", svc.restarts)
	}
	if backups, _ := filepath.Glob(binaryPath + ".bak-*"); len(backups) != 0 {
		t.Fatalf("校验失败不该留下备份，得到 %v", backups)
	}
}

func TestHubUpdateReportsNoUpdateWhenLatestIsOlder(t *testing.T) {
	dir := t.TempDir()
	binaryPath := filepath.Join(dir, "kokoro")
	if err := os.WriteFile(binaryPath, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	rel := hubRelease{Tag: "v0.1.2"}
	src := &stubHubSource{releases: []hubRelease{rel}}
	var output bytes.Buffer
	u := &hubUpdater{
		binaryPath: binaryPath, currentVersion: "v0.1.3-preview.7", arch: "amd64",
		source: src, service: &stubHubService{active: true}, assumeYes: true, out: &output,
		precheck: func() error { return nil },
	}
	if err := u.Run(context.Background()); err != nil {
		t.Fatalf("较旧的最新发布版应作为无更新处理，得到 %v", err)
	}
	if !strings.Contains(output.String(), "没有可用更新") {
		t.Fatalf("应提示没有可用更新，得到 %q", output.String())
	}
	got, err := os.ReadFile(binaryPath)
	if err != nil || string(got) != "old" {
		t.Fatalf("没有更新时程序应保持不变，内容=%q err=%v", got, err)
	}
}

func TestPruneHubBackupsKeepsNewest(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "kokoro")
	for _, ts := range []string{"20260101-000001", "20260102-000002", "20260103-000003", "20260104-000004", "20260105-000005"} {
		if err := os.WriteFile(target+".bak-"+ts, []byte(ts), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	pruneHubBackups(target, 3)
	left, _ := filepath.Glob(target + ".bak-*")
	if len(left) != 3 {
		t.Fatalf("应保留 3 份，得到 %d：%v", len(left), left)
	}
	for _, keep := range []string{"20260105-000005", "20260104-000004", "20260103-000003"} {
		if _, err := os.Stat(target + ".bak-" + keep); err != nil {
			t.Errorf("最新的 %s 应被保留：%v", keep, err)
		}
	}
}

// TestHubUpdatePrecheckRejectsUnsupportedHost 在开发机（非 Linux）上，
// 预检必须直接拒绝，不能去动本机文件。
func TestHubUpdatePrecheckRejectsUnsupportedHost(t *testing.T) {
	err := hubUpdatePrecheck(filepath.Join(t.TempDir(), "kokoro"), "amd64")
	if hubPlatformSupported() {
		t.Skip("Linux 主机上跳过：预检会走真实权限与文件检查")
	}
	if err == nil {
		t.Fatal("非 Linux 平台应拒绝更新面板")
	}
}
