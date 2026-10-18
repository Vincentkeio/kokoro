package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Vincentkeio/kokoro/internal/panelupdate"
)

// 面板（Hub）自更新：从 GitHub Release 取已发布版本，按架构下载并校验，
// 原子替换 Hub 二进制后重启服务，健康检查失败则回滚。
//
// 语义对齐部署端 scripts/deploy-zgo-remote.sh：备份二进制 → 替换 → 重启 →
// 轮询 is-active 与 127.0.0.1:8799 健康端点 → 失败还原旧二进制和数据库。
// 数据库在停服后备份，迁移由新 Hub 启动时执行。

const (
	hubRepoName    = "Vincentkeio/kokoro"
	hubGitHubAPI   = "https://api.github.com"
	hubAssetPrefix = "kokoro-linux-"
	hubUnitName    = "kokoro-hub"
	hubHealthURL   = "http://127.0.0.1:8799/"

	maxHubBinarySize   = 128 << 20
	maxHubChecksumSize = 4096
	maxHubReleaseBytes = 4 << 20

	hubReleasePageSize = 100
	hubBackupsToKeep   = 3

	hubHealthAttempts = 10
	hubHealthInterval = 2 * time.Second
)

// hubAsset 是 GitHub Release 里的一个下载项。
type hubAsset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
	Size int64  `json:"size"`
}

// hubRelease 是 GitHub Releases API 返回的一条已发布版本。
type hubRelease struct {
	Tag         string     `json:"tag_name"`
	Draft       bool       `json:"draft"`
	Prerelease  bool       `json:"prerelease"`
	PublishedAt time.Time  `json:"published_at"`
	Assets      []hubAsset `json:"assets"`
}

// hubReleaseSource 抽象「列出发布版本 / 下载资产」，测试可用桩替换。
type hubReleaseSource interface {
	Releases(ctx context.Context) ([]hubRelease, error)
	Download(ctx context.Context, rawURL string, limit int64) ([]byte, error)
}

// hubService 抽象 systemd 服务控制与健康探测，测试可用桩替换。
type hubService interface {
	Stop(ctx context.Context) error
	Restart(ctx context.Context) error
	Active(ctx context.Context) (bool, error)
	Healthy(ctx context.Context) error
}

// githubHubSource 直接走 api.github.com / github.com，公开仓库无需凭据。
type githubHubSource struct {
	client  *http.Client
	apiBase string
	repo    string
	perPage int
}

func newGitHubHubSource(client *http.Client) *githubHubSource {
	if client == nil {
		client = http.DefaultClient
	}
	return &githubHubSource{
		client:  client,
		apiBase: hubGitHubAPI,
		repo:    hubRepoName,
		perPage: hubReleasePageSize,
	}
}

func (s *githubHubSource) Releases(ctx context.Context) ([]hubRelease, error) {
	endpoint := fmt.Sprintf("%s/repos/%s/releases?per_page=%d", s.apiBase, s.repo, s.perPage)
	body, err := s.get(ctx, endpoint, maxHubReleaseBytes)
	if err != nil {
		return nil, err
	}
	var releases []hubRelease
	if err := json.Unmarshal(body, &releases); err != nil {
		return nil, fmt.Errorf("解析 GitHub 发布列表失败：%w", err)
	}
	published := make([]hubRelease, 0, len(releases))
	for _, rel := range releases {
		if rel.Draft || strings.TrimSpace(rel.Tag) == "" {
			continue
		}
		published = append(published, rel)
	}
	return published, nil
}

func (s *githubHubSource) Download(ctx context.Context, rawURL string, limit int64) ([]byte, error) {
	return s.get(ctx, rawURL, limit)
}

func (s *githubHubSource) get(ctx context.Context, rawURL string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "kokoro-updater")
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s 返回 HTTP %d", rawURL, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("下载内容超过 %d 字节上限", limit)
	}
	return body, nil
}

// systemdHubService 是真实实现：systemctl 控制 kokoro-hub，curl 等价物查健康端点。
type systemdHubService struct {
	unit      string
	healthURL string
	client    *http.Client
}

func (s systemdHubService) Stop(ctx context.Context) error {
	out, err := exec.CommandContext(ctx, "systemctl", "stop", s.unit).CombinedOutput()
	if err != nil {
		return fmt.Errorf("停止 %s 失败: %w (%s)", s.unit, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (s systemdHubService) Restart(ctx context.Context) error {
	out, err := exec.CommandContext(ctx, "systemctl", "restart", s.unit).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg != "" {
			return fmt.Errorf("%v：%s", err, msg)
		}
		return err
	}
	return nil
}

func (s systemdHubService) Active(ctx context.Context) (bool, error) {
	err := exec.CommandContext(ctx, "systemctl", "is-active", "--quiet", s.unit).Run()
	if err == nil {
		return true, nil
	}
	// is-active 用退出码表达"未激活"，这不是执行失败；只有连 systemctl
	// 都跑不起来才算错误。
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return false, nil
	}
	return false, err
}

func (s systemdHubService) Healthy(ctx context.Context) error {
	client := s.client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.healthURL, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("健康端点 %s 返回 HTTP %d", s.healthURL, resp.StatusCode)
	}
	return nil
}

// hubUpdater 串起一次面板更新。字段可注入，便于测试。
type hubUpdater struct {
	binaryPath     string
	databasePath   string
	currentVersion string
	arch           string
	source         hubReleaseSource
	service        hubService
	in             *bufio.Reader
	out            io.Writer

	// assumeYes 为非交互模式，发现更新后不再询问。
	assumeYes bool

	precheck       func() error
	healthAttempts int
	healthInterval time.Duration
	now            func() time.Time
}

func (u *hubUpdater) Run(ctx context.Context) error {
	if u.out == nil {
		u.out = os.Stdout
	}
	if u.now == nil {
		u.now = time.Now
	}
	if u.healthAttempts <= 0 {
		u.healthAttempts = hubHealthAttempts
	}
	if u.healthInterval <= 0 {
		u.healthInterval = hubHealthInterval
	}
	check := u.precheck
	if check == nil {
		check = func() error { return hubUpdatePrecheck(u.binaryPath, u.arch) }
	}
	if err := check(); err != nil {
		return err
	}

	releases, err := u.source.Releases(ctx)
	if err != nil {
		return fmt.Errorf("获取 GitHub 发布列表失败：%w", err)
	}
	rel, err := u.chooseRelease(releases)
	if err != nil {
		return err
	}

	if !hubReleaseNewerThanVersion(rel, u.currentVersion, releases) {
		fmt.Fprintln(u.out, "当前面板已是最新版本，没有可用更新。")
		return nil
	}
	fmt.Fprintf(u.out, "发现更新：%s\n", describeHubRelease(rel))
	if !u.assumeYes && !confirmUpdate(u.in, u.out) {
		fmt.Fprintln(u.out, "已取消。")
		return nil
	}
	fmt.Fprintln(u.out, "正在更新面板；更新失败时会自动还原旧程序。")
	fmt.Fprintln(u.out, "更新只替换 Hub 程序并重启服务；数据库迁移由 Hub 启动时自行执行，")
	fmt.Fprintln(u.out, "更新前会备份数据库；迁移或启动失败会一并还原数据库和旧程序。")

	assetName := hubAssetPrefix + u.arch
	binURL, sumURL, err := hubAssetURLs(rel, assetName)
	if err != nil {
		return err
	}
	binary, err := u.source.Download(ctx, binURL, maxHubBinarySize)
	if err != nil {
		return fmt.Errorf("下载 %s 失败：%w", assetName, err)
	}
	checksum, err := u.source.Download(ctx, sumURL, maxHubChecksumSize)
	if err != nil {
		return fmt.Errorf("下载 %s.sha256 失败：%w", assetName, err)
	}
	if err := verifyHubArtifact(rel, assetName, binary, checksum, u.arch); err != nil {
		return err
	}

	var dbBackup string
	if u.databasePath != "" {
		if err := u.service.Stop(ctx); err != nil {
			return err
		}
		dbBackup, err = backupHubDatabase(u.databasePath, u.now())
		if err != nil {
			recoveryCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			restartErr := u.service.Restart(recoveryCtx)
			return errors.Join(fmt.Errorf("备份数据库失败，未替换程序: %w", err), restartErr)
		}
	}
	backup, err := replaceHubBinary(u.binaryPath, binary, u.now())
	if err != nil {
		if dbBackup != "" {
			recoveryCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			err = errors.Join(err, u.service.Restart(recoveryCtx))
		}
		return fmt.Errorf("替换 Hub 程序失败：%w", err)
	}
	fmt.Fprintf(u.out, "已替换 %s（回滚备份 %s）\n", u.binaryPath, backup)
	fmt.Fprintf(u.out, "正在重启 %s 并检查健康……\n", hubUnitName)

	if err := u.restartAndCheck(ctx); err != nil {
		fmt.Fprintf(u.out, "健康检查未通过（%v），正在回滚……\n", err)
		recoveryCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		if dbBackup != "" {
			if rerr := u.service.Stop(recoveryCtx); rerr != nil {
				return fmt.Errorf("回滚前停止新程序失败，数据库备份保留在 %s: %w", dbBackup, rerr)
			}
		}
		if rerr := restoreHubBinary(u.binaryPath, backup); rerr != nil {
			return fmt.Errorf("回滚失败：%v；原错误：%w；旧程序备份保留在 %s", rerr, err, backup)
		}
		if dbBackup != "" {
			if rerr := restoreHubDatabase(u.databasePath, dbBackup); rerr != nil {
				return fmt.Errorf("数据库回滚失败，备份保留在 %s: %w", dbBackup, rerr)
			}
		}
		if rerr := u.service.Restart(recoveryCtx); rerr != nil {
			return fmt.Errorf("已还原旧程序但重启失败：%v；旧程序备份保留在 %s", rerr, backup)
		}
		return fmt.Errorf("更新失败，已回滚到 %s：%w", u.currentVersion, err)
	}

	pruneHubBackups(u.binaryPath, hubBackupsToKeep)
	if dbBackup != "" {
		pruneHubDatabaseBackups(u.databasePath, hubBackupsToKeep)
	}
	fmt.Fprintln(u.out, "面板更新完成。")
	return nil
}

// chooseRelease selects the most recently published non-draft release,
// including prereleases. Older releases without timestamps fall back to tags.
func (u *hubUpdater) chooseRelease(releases []hubRelease) (hubRelease, error) {
	if len(releases) == 0 {
		return hubRelease{}, errors.New("GitHub 上没有可用的已发布版本")
	}
	chosen := releases[0]
	for _, rel := range releases[1:] {
		if hubReleaseNewer(rel, chosen) {
			chosen = rel
		}
	}
	return chosen, nil
}

func hubReleaseNewer(a, b hubRelease) bool {
	if !a.PublishedAt.IsZero() && !b.PublishedAt.IsZero() && !a.PublishedAt.Equal(b.PublishedAt) {
		return a.PublishedAt.After(b.PublishedAt)
	}
	return compareKokoroVersion(a.Tag, b.Tag) > 0
}

func hubReleaseNewerThanVersion(target hubRelease, current string, releases []hubRelease) bool {
	if target.Tag == current {
		return false
	}
	for _, rel := range releases {
		if rel.Tag == current {
			return hubReleaseNewer(target, rel)
		}
	}
	return compareKokoroVersion(target.Tag, current) > 0
}

func describeHubRelease(rel hubRelease) string {
	label := rel.Tag
	if rel.Prerelease {
		label += "（预览版）"
	}
	if !rel.PublishedAt.IsZero() {
		label += "，发布于 " + rel.PublishedAt.Local().Format("2006-01-02 15:04:05")
	}
	return label
}

func (u *hubUpdater) restartAndCheck(ctx context.Context) error {
	if err := u.service.Restart(ctx); err != nil {
		return fmt.Errorf("重启 %s 失败：%w", hubUnitName, err)
	}
	var lastErr error
	for attempt := 0; attempt < u.healthAttempts; attempt++ {
		active, err := u.service.Active(ctx)
		switch {
		case err != nil:
			lastErr = fmt.Errorf("查询服务状态失败：%w", err)
		case !active:
			lastErr = fmt.Errorf("%s 未处于 active", hubUnitName)
		default:
			herr := u.service.Healthy(ctx)
			if herr == nil {
				return nil
			}
			lastErr = herr
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(u.healthInterval):
		}
	}
	return fmt.Errorf("%s 在预期时间内未通过健康检查：%w", hubUnitName, lastErr)
}

// hubAssetURLs 找出二进制与校验文件两个资产；缺任何一个都报明确错误。
func hubAssetURLs(rel hubRelease, assetName string) (binURL, sumURL string, err error) {
	for _, asset := range rel.Assets {
		switch asset.Name {
		case assetName:
			binURL = asset.URL
		case assetName + ".sha256":
			sumURL = asset.URL
		}
	}
	if binURL == "" {
		return "", "", fmt.Errorf("Release %s 缺少资产 %s", rel.Tag, assetName)
	}
	if sumURL == "" {
		return "", "", fmt.Errorf("Release %s 缺少校验文件 %s.sha256", rel.Tag, assetName)
	}
	return binURL, sumURL, nil
}

// verifyHubArtifact 校验大小、ELF 格式与 SHA-256，任一不过都拒绝替换。
func verifyHubArtifact(rel hubRelease, assetName string, binary, checksum []byte, arch string) error {
	var want int64
	for _, asset := range rel.Assets {
		if asset.Name == assetName {
			want = asset.Size
		}
	}
	if want > 0 && int64(len(binary)) != want {
		return fmt.Errorf("%s 大小不符：发布清单 %d 字节，实际下载 %d 字节", assetName, want, len(binary))
	}
	if err := verifyHubBinaryFormat(binary, arch); err != nil {
		return err
	}
	fields := strings.Fields(string(checksum))
	if len(fields) == 0 || len(fields[0]) != sha256.Size*2 {
		return fmt.Errorf("%s.sha256 格式无效", assetName)
	}
	if _, err := hex.DecodeString(fields[0]); err != nil {
		return fmt.Errorf("%s.sha256 格式无效：%w", assetName, err)
	}
	actual := sha256.Sum256(binary)
	if !strings.EqualFold(fields[0], hex.EncodeToString(actual[:])) {
		return fmt.Errorf("%s SHA-256 校验失败（期望 %s，实际 %s）", assetName, strings.ToLower(fields[0]), hex.EncodeToString(actual[:]))
	}
	return nil
}

// verifyHubBinaryFormat 确认下载物是架构匹配的 64 位小端 ELF。
func verifyHubBinaryFormat(data []byte, arch string) error {
	machine, ok := map[string]uint16{"amd64": 0x3e, "arm64": 0xb7}[arch]
	if !ok {
		return fmt.Errorf("不支持的架构 %q", arch)
	}
	if len(data) < 64 {
		return errors.New("下载的 Hub 程序不是有效的 ELF（长度不足）")
	}
	if data[0] != 0x7f || data[1] != 'E' || data[2] != 'L' || data[3] != 'F' {
		return errors.New("下载的 Hub 程序不是有效的 ELF")
	}
	if data[4] != 2 {
		return errors.New("下载的 Hub 程序不是 64 位 ELF")
	}
	if data[5] != 1 {
		return errors.New("下载的 Hub 程序不是小端 ELF")
	}
	if got := binary.LittleEndian.Uint16(data[18:20]); got != machine {
		return fmt.Errorf("下载的 Hub 程序架构不符：期望 %s（0x%x），实际 0x%x", arch, machine, got)
	}
	switch binary.LittleEndian.Uint16(data[16:18]) {
	case 2, 3: // ET_EXEC / ET_DYN
	default:
		return fmt.Errorf("下载的 Hub 程序 ELF 类型异常：%d", binary.LittleEndian.Uint16(data[16:18]))
	}
	return nil
}

// hubUpdatePrecheck 在动任何东西之前确认环境与权限。
func hubUpdatePrecheck(binaryPath, arch string) error {
	if !hubPlatformSupported() {
		return errors.New("更新面板仅支持 Linux + systemd 的 Hub 主机")
	}
	if !runningAsRoot() {
		return errors.New("更新面板需要 root 权限，请用 sudo 运行 kokoro")
	}
	if arch != "amd64" && arch != "arm64" {
		return fmt.Errorf("不支持的架构 %q（仅支持 amd64 / arm64）", arch)
	}
	info, err := os.Stat(binaryPath)
	if err != nil {
		return fmt.Errorf("未找到 Hub 程序 %s：%w", binaryPath, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s 不是普通文件", binaryPath)
	}
	dir := filepath.Dir(binaryPath)
	probe, err := os.CreateTemp(dir, ".kokoro-hub-write-*")
	if err != nil {
		return fmt.Errorf("%s 不可写，无法更新面板：%w", dir, err)
	}
	name := probe.Name()
	probe.Close()
	_ = os.Remove(name)
	return nil
}

// replaceHubBinary 备份现有程序后原子替换，返回备份路径供回滚。
func replaceHubBinary(target string, data []byte, now time.Time) (string, error) {
	info, err := os.Stat(target)
	if err != nil {
		return "", fmt.Errorf("读取现有 Hub 程序失败：%w", err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s 不是普通文件", target)
	}
	mode := info.Mode().Perm()
	uid, gid, hasOwner := fileOwnership(info)
	backup := fmt.Sprintf("%s.bak-%s", target, now.Format("20060102-150405"))
	if err := copyFile(target, backup, mode, uid, gid, hasOwner); err != nil {
		return "", fmt.Errorf("备份现有程序失败：%w", err)
	}
	if err := atomicWriteFile(target, data, mode, uid, gid, hasOwner); err != nil {
		return "", fmt.Errorf("写入新程序失败（原程序未改动）：%w", err)
	}
	return backup, nil
}

// restoreHubBinary 用备份覆盖回目标程序，保留备份自身的权限与属主。
func restoreHubBinary(target, backup string) error {
	info, err := os.Stat(backup)
	if err != nil {
		return fmt.Errorf("找不到回滚备份 %s：%w", backup, err)
	}
	data, err := os.ReadFile(backup)
	if err != nil {
		return err
	}
	uid, gid, hasOwner := fileOwnership(info)
	return atomicWriteFile(target, data, info.Mode().Perm(), uid, gid, hasOwner)
}

// atomicWriteFile 同目录写临时文件 → 同步 → 原子改名，避免半截文件替换目标。
func atomicWriteFile(target string, data []byte, mode os.FileMode, uid, gid int, hasOwner bool) error {
	dir := filepath.Dir(target)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(target)+".*.new")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	removeTmp := true
	defer func() {
		if removeTmp {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if hasOwner {
		if err := chownPath(tmpPath, uid, gid); err != nil {
			tmp.Close()
			return fmt.Errorf("设置属主失败：%w", err)
		}
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, target); err != nil {
		return err
	}
	removeTmp = false
	return nil
}

func copyFile(src, dst string, mode os.FileMode, uid, gid int, hasOwner bool) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.CreateTemp(filepath.Dir(dst), ".kokoro-copy-*.new")
	if err != nil {
		return err
	}
	tmp := out.Name()
	defer os.Remove(tmp)
	defer out.Close()
	if err := out.Chmod(mode); err != nil {
		return err
	}
	if hasOwner {
		if err := chownPath(tmp, uid, gid); err != nil {
			return err
		}
	}
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	if err := out.Sync(); err != nil {
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

// Called only while the service is stopped. Include SQLite sidecars when an
// unclean earlier shutdown left a WAL; never load a potentially large DB in RAM.
func backupHubDatabase(path string, now time.Time) (string, error) {
	if _, err := os.Stat(path); err != nil {
		return "", err
	}
	dir := filepath.Join(filepath.Dir(path), "backups")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	backup := filepath.Join(dir, "hub-update-"+now.Format("20060102-150405.000000000")+".db")
	for _, suffix := range []string{"", "-wal", "-shm"} {
		info, err := os.Stat(path + suffix)
		if os.IsNotExist(err) && suffix != "" {
			continue
		}
		if err != nil {
			return "", err
		}
		uid, gid, owned := fileOwnership(info)
		if err := copyFile(path+suffix, backup+suffix, 0600, uid, gid, owned); err != nil {
			return "", err
		}
	}
	return backup, nil
}

func restoreHubDatabase(path, backup string) error {
	for _, suffix := range []string{"", "-wal", "-shm"} {
		info, err := os.Stat(backup + suffix)
		if os.IsNotExist(err) && suffix != "" {
			if err := os.Remove(path + suffix); err != nil && !os.IsNotExist(err) {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		uid, gid, owned := fileOwnership(info)
		if err := copyFile(backup+suffix, path+suffix, info.Mode().Perm(), uid, gid, owned); err != nil {
			return err
		}
	}
	return nil
}

func pruneHubDatabaseBackups(path string, keep int) {
	paths, _ := filepath.Glob(filepath.Join(filepath.Dir(path), "backups", "hub-update-*.db"))
	sort.Sort(sort.Reverse(sort.StringSlice(paths)))
	if keep < 0 || len(paths) <= keep {
		return
	}
	for _, backup := range paths[keep:] {
		for _, suffix := range []string{"", "-wal", "-shm"} {
			_ = os.Remove(backup + suffix)
		}
	}
}

// pruneHubBackups 只保留最近的若干份回滚备份，别让程序目录越堆越乱。
func pruneHubBackups(target string, keep int) {
	matches, err := filepath.Glob(target + ".bak-*")
	if err != nil || len(matches) <= keep {
		return
	}
	sort.Sort(sort.Reverse(sort.StringSlice(matches)))
	for _, path := range matches[keep:] {
		_ = os.Remove(path)
	}
}

// compareKokoroVersion 比较两个版本号：正式版大于同号预览版，预览段按点分段比较。
func compareKokoroVersion(a, b string) int { return panelupdate.Compare(a, b) }

func parseKokoroVersion(v string) ([3]int, string) {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(strings.TrimPrefix(v, "v"), "V")
	core, pre, _ := strings.Cut(v, "-")
	var nums [3]int
	parts := strings.Split(core, ".")
	for i := 0; i < 3 && i < len(parts); i++ {
		nums[i], _ = strconv.Atoi(strings.TrimSpace(parts[i]))
	}
	return nums, pre
}

func comparePreRelease(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		ai, aerr := strconv.Atoi(as[i])
		bi, berr := strconv.Atoi(bs[i])
		if aerr == nil && berr == nil {
			if ai != bi {
				if ai < bi {
					return -1
				}
				return 1
			}
			continue
		}
		if as[i] != bs[i] {
			if as[i] < bs[i] {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(as) < len(bs):
		return -1
	case len(as) > len(bs):
		return 1
	}
	return 0
}

// newHubUpdater 组装一次真实更新所需的依赖。
func newHubUpdater(binaryPath, currentVersion string, in *bufio.Reader, out io.Writer) *hubUpdater {
	_, dataDir := installedHubPaths()
	return &hubUpdater{
		binaryPath:     binaryPath,
		databasePath:   filepath.Join(dataDir, "kokoro.db"),
		currentVersion: currentVersion,
		arch:           runtime.GOARCH,
		source:         newGitHubHubSource(&http.Client{Timeout: 60 * time.Second}),
		service:        systemdHubService{unit: hubUnitName, healthURL: hubHealthURL, client: &http.Client{Timeout: 10 * time.Second}},
		in:             in,
		out:            out,
	}
}
