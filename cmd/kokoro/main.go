// Command kokoro 是 Kokoro 探针的唯一入口。
//
// 一个二进制，两种角色：
//
//	kokoro serve   主控 Hub（面板 + API，跑在你的主力 VPS 上）
//	kokoro agent   子机探针（跑在各台小鸡上，主动上报）
//
// 体积是硬指标，所以只用标准库 + 纯 Go SQLite，不引 CLI 框架。
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Vincentkeio/kokoro/internal/agent"
	"github.com/Vincentkeio/kokoro/internal/collector"
	"github.com/Vincentkeio/kokoro/internal/hub"
	"github.com/Vincentkeio/kokoro/internal/model"
	"github.com/Vincentkeio/kokoro/internal/store"
)

// Version 会被构建脚本用 -ldflags "-X main.Version=..." 覆盖。
var Version = "v0.2.0-beta.23"

const usage = `kokoro - 每台小鸡都有一颗心

用法:
  kokoro                 打开管理菜单（服务状态 / 更新面板 / 重置唯一管理员）
  kokoro serve    [选项]    启动主控 Hub
  kokoro agent    [选项]    启动子机探针（别名: kokoro run）
  kokoro register [选项]    仅执行一次注册（把 token 写回配置文件）
  kokoro passwd   [选项]    覆盖唯一管理员账户的用户名与密码
  kokoro version            打印版本

serve 选项:
  --listen   监听地址（默认 127.0.0.1:8799）
  --data     数据目录（默认 ./data，SQLite 与上传文件都放这里）
  --domain   对外域名（用于自动申请证书与生成安装命令）
  --tls      证书模式: proxy | selfsigned | none（默认 proxy，即由 Nginx 前置反代）

passwd 选项:
  --user     新用户名
  --password 新密码（至少 8 位）
  --data     数据目录（默认自动从程序安装目录识别）
  --check    只检查是否已设置管理员账户
  --if-unset 仅首次设置；已存在账户时不覆盖

重设密码示例:
  kokoro passwd --user boss --password '你的新密码'

agent 选项:
  --config   配置文件（默认 /etc/kokoro/config.toml）
  --hub      Hub 地址，例如 https://panel.example.com
  --token    安装令牌 it_xxx（首次注册用）
  --interval 上报间隔毫秒（默认 2000）

示例:
  kokoro serve --listen 127.0.0.1:8799 --data /var/lib/kokoro --domain panel.example.com
  kokoro agent --hub https://panel.example.com --token it_xxxxxxxx
`

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	if len(os.Args) < 2 {
		os.Exit(runMenu())
	}

	switch os.Args[1] {
	case "serve":
		os.Exit(runServe(os.Args[2:]))
	case "apply-update":
		os.Exit(runWebUpdate(os.Args[2:]))
	case "apply-update-request":
		os.Exit(runWebUpdateRequest(os.Args[2:]))
	case "agent", "run":
		// run 是 agent 的别名：systemd 里的写法 kokoro-agent run 更自然
		os.Exit(runAgent(os.Args[2:]))
	case "register":
		os.Exit(runRegister(os.Args[2:]))
	case "apply-swap":
		fs := flag.NewFlagSet("apply-swap", flag.ContinueOnError)
		sizeMB := fs.Int("size-mb", -1, "Target swap capacity in MiB; increase only")
		if err := fs.Parse(os.Args[2:]); err != nil || fs.NArg() != 0 || *sizeMB < 0 {
			os.Exit(2)
		}
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		if err := agent.ApplySwapContext(ctx, *sizeMB); err != nil {
			log.Printf("调整 SWAP 失败：%v", err)
			os.Exit(1)
		}
		fmt.Println("SWAP 调整完成")
	case "prepare-tests":
		// Local systemd helper only; this is never a remotely accepted command.
		fs := flag.NewFlagSet("prepare-tests", flag.ContinueOnError)
		configPath := fs.String("config", agent.ConfigPath(), "Agent configuration")
		if err := fs.Parse(os.Args[2:]); err != nil || fs.NArg() != 0 {
			os.Exit(2)
		}
		cfg, err := agent.LoadConfig(*configPath)
		if err == nil {
			err = agent.PrepareTestDependencies(context.Background(), cfg)
		}
		if err != nil {
			log.Printf("安装固定测试组件失败：%v", err)
			os.Exit(1)
		}
	case "passwd":
		os.Exit(runPasswd(os.Args[2:]))
	case "version":
		fmt.Printf("kokoro %s\n", Version)
	case "-h", "--help", "help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "未知子命令: %s\n\n", os.Args[1])
		fmt.Print(usage)
		os.Exit(2)
	}
}

// menuItem 是管理菜单的一项。用表驱动而不是写死编号：菜单项会随本机装了什么
// （Hub / Agent）增减，硬编码编号很容易错位。
type menuItem struct {
	label      string
	run        func(reader *bufio.Reader) int
	pauseAfter bool
}

func menuItems() []menuItem {
	hub := unitInstalled("kokoro-hub")
	// 面板可能只卸了一半：服务没了，Caddy 配置还在。这时也把卸载项列出来，
	// 否则主机既回不到干净状态，重装又会被残留的反代配置挡住。
	leftovers := !hub && hubLeftoversPresent()
	agentInstalled := unitInstalled("kokoro-agent")

	items := []menuItem{
		{label: "服务状态", run: func(*bufio.Reader) int { showServiceStatus(); return 0 }},
		{label: "安装信息", run: func(*bufio.Reader) int { showInstallInfo(); return 0 }},
		{label: "重启服务", run: func(*bufio.Reader) int { restartServices(); return 0 }},
	}
	// 面板与探针的更新、卸载分开，只显示本机实际装了的角色：
	// 面板主机不会看到"更新/卸载探针"，纯子机也不会看到面板相关项。
	if hub {
		items = append(items, menuItem{label: "更新面板（Hub 主程序）", run: updateHub, pauseAfter: true})
	}
	if agentInstalled {
		items = append(items, menuItem{label: "更新探针程序（Agent）", run: updateAgent, pauseAfter: true})
	}
	if hub {
		items = append(items, menuItem{label: "更新安装脚本", run: updateHubInstaller, pauseAfter: true})
	}
	if hub {
		items = append(items, menuItem{label: "重置唯一管理员账户", run: resetAdminFromMenu})
	}
	items = append(items, menuItem{label: "查看最近日志", run: func(*bufio.Reader) int { showLogs(); return 0 }})
	if hub {
		items = append(items, menuItem{label: "卸载面板（可选保留配置与数据）", run: uninstallHub})
	} else if leftovers {
		items = append(items, menuItem{label: "清理面板残留（服务已不在）", run: uninstallHub})
	}
	if agentInstalled {
		items = append(items, menuItem{label: "卸载 Agent", run: uninstallAgent})
	}
	items = append(items, menuItem{label: "卸载快捷命令与管理脚本", run: func(*bufio.Reader) int { return uninstallMenuCommand() }})
	return items
}

func runMenu() int {
	reader := bufio.NewReader(os.Stdin)
	for {
		items := menuItems()
		fmt.Println("\nKokoro 管理菜单")
		for i, item := range items {
			fmt.Printf("%d) %s\n", i+1, item.label)
		}
		fmt.Println("0) 退出")
		fmt.Print("请选择：")
		line, err := reader.ReadString('\n')
		if err != nil && line == "" {
			return 0
		}
		choice := strings.TrimSpace(line)
		switch choice {
		case "", "q", "quit", "exit":
			if choice == "" {
				fmt.Println("请输入菜单编号。")
			}
			continue
		case "0":
			return 0
		}
		index, convErr := strconv.Atoi(choice)
		if convErr != nil || index < 1 || index > len(items) {
			fmt.Println("无效选项，请选择菜单中的编号。")
			continue
		}
		item := items[index-1]
		rc := item.run(reader)
		if item.pauseAfter {
			fmt.Print("\n按回车返回菜单...")
			_, _ = reader.ReadString('\n')
		}
		if rc != 0 {
			return 1
		}
	}
}

func unitInstalled(unit string) bool {
	for _, path := range []string{
		filepath.Join("/etc/systemd/system", unit+".service"),
		filepath.Join("/lib/systemd/system", unit+".service"),
		filepath.Join("/usr/lib/systemd/system", unit+".service"),
	} {
		if _, err := os.Stat(path); err == nil {
			return true
		}
	}
	return false
}

func runSystemctl(args ...string) {
	cmd := exec.Command("systemctl", args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Printf("执行 systemctl %s 失败：%v\n", strings.Join(args, " "), err)
	}
}

func serviceUnits() []string {
	units := make([]string, 0, 2)
	if unitInstalled("kokoro-hub") {
		units = append(units, "kokoro-hub")
	}
	if unitInstalled("kokoro-agent") {
		units = append(units, "kokoro-agent")
	}
	return units
}

func showServiceStatus() {
	units := serviceUnits()
	if len(units) == 0 {
		fmt.Println("没有检测到 Kokoro systemd 服务。")
		return
	}
	for _, unit := range units {
		fmt.Printf("\n--- %s ---\n", unit)
		runSystemctl("status", unit, "--no-pager")
	}
}

func restartServices() {
	units := serviceUnits()
	if len(units) == 0 {
		fmt.Println("没有检测到 Kokoro systemd 服务。")
		return
	}
	args := append([]string{"restart"}, units...)
	runSystemctl(args...)
	for _, unit := range units {
		runSystemctl("is-active", unit)
	}
}

func updateAgent(reader *bufio.Reader) int {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	source := newGitHubHubSource(&http.Client{Timeout: 60 * time.Second})
	releases, err := source.Releases(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "检查 Agent 更新失败：%v\n", err)
		return 1
	}
	latest, err := (&hubUpdater{}).chooseRelease(releases)
	if err != nil {
		fmt.Fprintf(os.Stderr, "检查 Agent 更新失败：%v\n", err)
		return 1
	}
	if !hubReleaseNewerThanVersion(latest, Version, releases) {
		fmt.Println("当前探针程序已是最新版本，没有可用更新。")
		return 0
	}
	fmt.Printf("发现 Agent 更新：%s\n", describeHubRelease(latest))
	if !confirmUpdate(reader, os.Stdout) {
		fmt.Println("已取消。")
		return 0
	}

	installer := "/usr/local/lib/kokoro/install.sh"
	if _, err := os.Stat(installer); err != nil {
		fmt.Println("未找到探针安装脚本。请在节点上重新运行 Hub 提供的安装命令。")
		if unitInstalled("kokoro-hub") {
			fmt.Println("本机是面板主机；更新 Hub 主程序请用菜单里的「更新面板」。")
		}
		return 1
	}
	if cfg, err := agent.LoadConfig(""); err != nil {
		fmt.Printf("无法读取 Hub 地址，保留现有安装脚本：%v\n", err)
	} else if err := refreshInstaller(cfg.Hub, installer, &http.Client{Timeout: 30 * time.Second}); err != nil {
		fmt.Printf("无法从 Hub 刷新安装脚本，继续使用本地版本：%v\n", err)
	} else {
		fmt.Println("已从 Hub 更新本地 install.sh")
	}
	fmt.Println("正在更新探针程序。")
	cmd := exec.Command("sh", installer, "--upgrade", "--github")
	cmd.Env = envWithOverride(os.Environ(), "GH_TAG", latest.Tag)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Printf("更新探针失败：%v\n", err)
		return 1
	}
	return 0
}

// updateHub 更新 Hub 主程序本体。与 updateAgent 完全分开：这里只动
// Hub 二进制和 kokoro-hub 服务，不碰 Agent、也不碰 /var/lib/kokoro 数据。
func updateHub(reader *bufio.Reader) int {
	if !unitInstalled("kokoro-hub") {
		fmt.Println("未检测到 Kokoro 面板服务。")
		return 0
	}
	binaryPath, _ := installedHubPaths()
	updater := newHubUpdater(binaryPath, Version, reader, os.Stdout)
	if err := updater.Run(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "更新面板失败：%v\n", err)
		return 1
	}
	return 0
}

// updateHubInstaller refreshes the installer distributed by Hub for future
// Agent installs and upgrades. It does not update the Hub or Agent binaries.
func updateHubInstaller(reader *bufio.Reader) int {
	if !unitInstalled("kokoro-hub") {
		fmt.Println("未检测到 Kokoro 面板服务。")
		return 0
	}
	if !runningAsRoot() {
		fmt.Println("更新安装脚本需要 root 权限，请用 sudo 运行 kokoro。")
		return 1
	}
	_, dataDir := installedHubPaths()
	distDir := filepath.Join(dataDir, "dist")
	if info, err := os.Stat(distDir); err != nil || !info.IsDir() {
		fmt.Printf("未找到 Hub 脚本分发目录 %s。\n", distDir)
		return 1
	}

	updater := newHubUpdater("", Version, reader, os.Stdout)
	ctx := context.Background()
	releases, err := updater.source.Releases(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "获取 GitHub 发布列表失败：%v\n", err)
		return 1
	}
	rel, err := updater.chooseRelease(releases)
	if err != nil {
		fmt.Fprintf(os.Stderr, "检查安装脚本更新失败：%v\n", err)
		return 1
	}

	assetName := "install.sh"
	scriptPath := filepath.Join(distDir, assetName)
	currentScript, err := os.ReadFile(scriptPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "读取当前安装脚本失败：%v\n", err)
		return 1
	}
	currentTag := installerReleaseTag(currentScript)
	if currentTag != "" && currentTag != "latest" && !hubReleaseNewerThanVersion(rel, currentTag, releases) {
		fmt.Println("Hub 分发的安装脚本已是最新版本，没有可用更新。")
		return 0
	}
	fmt.Printf("发现安装脚本更新：%s\n", describeHubRelease(rel))
	if !confirmUpdate(reader, os.Stdout) {
		fmt.Println("已取消。")
		return 0
	}
	assetURL, checksumURL, err := hubAssetURLs(rel, assetName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "获取安装脚本地址失败：%v\n", err)
		return 1
	}
	script, err := updater.source.Download(ctx, assetURL, maxInstallerSize)
	if err != nil {
		fmt.Fprintf(os.Stderr, "下载安装脚本失败：%v\n", err)
		return 1
	}
	checksum, err := updater.source.Download(ctx, checksumURL, 4096)
	if err != nil {
		fmt.Fprintf(os.Stderr, "下载安装脚本校验文件失败：%v\n", err)
		return 1
	}
	if err := verifyHubScriptArtifact(rel, assetName, script, checksum); err != nil {
		fmt.Fprintf(os.Stderr, "安装脚本校验失败：%v\n", err)
		return 1
	}

	checksumPath := scriptPath + ".sha256"
	if err := writeAtomicFile(scriptPath, script, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "保存安装脚本失败：%v\n", err)
		return 1
	}
	if err := writeAtomicFile(checksumPath, checksum, 0644); err != nil {
		_ = writeAtomicFile(scriptPath, currentScript, 0644)
		fmt.Fprintf(os.Stderr, "保存校验文件失败，已还原原安装脚本：%v\n", err)
		return 1
	}
	fmt.Fprintln(os.Stdout, "Hub 分发的安装脚本更新完成。")
	return 0
}

func installerReleaseTag(script []byte) string {
	for _, prefix := range []string{`GH_INSTALLER_VERSION="${GH_INSTALLER_VERSION:-`, `GH_TAG="${GH_TAG:-`} {
		for _, line := range strings.Split(string(script), "\n") {
			line = strings.TrimSpace(line)
			if !strings.HasPrefix(line, prefix) {
				continue
			}
			tag := strings.TrimSuffix(strings.TrimPrefix(line, prefix), `}"`)
			tag = strings.TrimSpace(tag)
			if tag != "" && tag != "dev" {
				return tag
			}
		}
	}
	return ""
}

func envWithOverride(env []string, key, value string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env)+1)
	for _, entry := range env {
		if !strings.HasPrefix(entry, prefix) {
			out = append(out, entry)
		}
	}
	return append(out, prefix+value)
}

func confirmUpdate(reader *bufio.Reader, out io.Writer) bool {
	if out == nil {
		out = os.Stdout
	}
	_, _ = fmt.Fprint(out, "检测到可用更新，是否立即更新？输入 y 确认，其他输入取消：")
	if reader == nil {
		reader = bufio.NewReader(os.Stdin)
	}
	answer := strings.TrimSpace(promptLine(reader, ""))
	return strings.EqualFold(answer, "y") || strings.EqualFold(answer, "yes")
}

func verifyHubScriptArtifact(rel hubRelease, assetName string, script, checksum []byte) error {
	if len(script) == 0 || !strings.HasPrefix(string(script), "#!/bin/sh\n") {
		return fmt.Errorf("%s 格式无效", assetName)
	}
	for _, asset := range rel.Assets {
		if asset.Name == assetName && asset.Size > 0 && int64(len(script)) != asset.Size {
			return fmt.Errorf("%s 大小与发布清单不符", assetName)
		}
	}
	fields := strings.Fields(string(checksum))
	if len(fields) == 0 || len(fields[0]) != sha256.Size*2 {
		return fmt.Errorf("%s.sha256 格式无效", assetName)
	}
	if _, err := hex.DecodeString(fields[0]); err != nil {
		return fmt.Errorf("%s.sha256 格式无效：%w", assetName, err)
	}
	actual := sha256.Sum256(script)
	if !strings.EqualFold(fields[0], hex.EncodeToString(actual[:])) {
		return fmt.Errorf("%s SHA-256 校验失败", assetName)
	}
	return nil
}

func writeAtomicFile(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".kokoro-script-*.new")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
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
	return os.Rename(tmpPath, path)
}

const maxInstallerSize = 2 << 20

// refreshInstaller downloads the Hub's current installer, verifies its SHA-256,
// then atomically replaces the saved installer used by future menu updates.
func refreshInstaller(hubURL, installerPath string, client *http.Client) error {
	base, err := url.Parse(strings.TrimRight(strings.TrimSpace(hubURL), "/"))
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return fmt.Errorf("Hub 地址无效")
	}
	if base.Path != "" && base.Path != "/" {
		return fmt.Errorf("Hub 地址不能包含路径")
	}
	if client == nil {
		client = http.DefaultClient
	}
	base.Path = "/api/v1/dl/install.sh"
	base.RawPath = ""
	assetURL := base.String()
	base.Path += ".sha256"
	checksumURL := base.String()

	fetch := func(source string, limit int64) ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
		if err != nil {
			return nil, err
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("%s 返回 HTTP %d", source, resp.StatusCode)
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
		if err != nil {
			return nil, err
		}
		if int64(len(body)) > limit {
			return nil, fmt.Errorf("下载内容超过大小限制")
		}
		return body, nil
	}

	script, err := fetch(assetURL, maxInstallerSize)
	if err != nil {
		return fmt.Errorf("下载 install.sh 失败：%w", err)
	}
	if len(script) == 0 || !strings.HasPrefix(string(script), "#!/bin/sh\n") {
		return fmt.Errorf("Hub 返回的 install.sh 格式无效")
	}
	checksum, err := fetch(checksumURL, 4096)
	if err != nil {
		return fmt.Errorf("下载 install.sh.sha256 失败：%w", err)
	}
	fields := strings.Fields(string(checksum))
	if len(fields) == 0 || len(fields[0]) != sha256.Size*2 {
		return fmt.Errorf("install.sh.sha256 格式无效")
	}
	if _, err := hex.DecodeString(fields[0]); err != nil {
		return fmt.Errorf("install.sh.sha256 格式无效：%w", err)
	}
	actual := sha256.Sum256(script)
	if !strings.EqualFold(fields[0], hex.EncodeToString(actual[:])) {
		return fmt.Errorf("install.sh SHA-256 校验失败")
	}

	tmp, err := os.CreateTemp(filepath.Dir(installerPath), ".install.sh-*.new")
	if err != nil {
		return fmt.Errorf("创建安装脚本临时文件失败：%w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0755); err != nil {
		tmp.Close()
		return fmt.Errorf("设置安装脚本权限失败：%w", err)
	}
	if _, err := tmp.Write(script); err != nil {
		tmp.Close()
		return fmt.Errorf("写入安装脚本失败：%w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("同步安装脚本失败：%w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("关闭安装脚本失败：%w", err)
	}
	if err := os.Rename(tmpPath, installerPath); err != nil {
		return fmt.Errorf("替换本地安装脚本失败：%w", err)
	}
	return nil
}

func showLogs() {
	units := serviceUnits()
	if len(units) == 0 {
		fmt.Println("没有检测到 Kokoro systemd 服务。")
		return
	}
	for _, unit := range units {
		fmt.Printf("\n--- %s（最近 50 行）---\n", unit)
		cmd := exec.Command("journalctl", "-u", unit, "-n", "50", "--no-pager")
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			fmt.Printf("读取日志失败：%v\n", err)
		}
	}
}

// ---- 安装器产物的识别与回收 ----

// installerMarker 是 install-hub.sh 写进它自己生成的配置里的标记行。
// 只代表"这段是 Kokoro 写的"，不代表整份文件都归 Kokoro：安装器允许把 Kokoro
// 的站点段合并进别人的 Caddyfile，别人的站点就在我们那段标记外面。
const installerMarker = "Managed-By=Kokoro-Installer"

// kokoroCaddyFile / kokoroCaddyStorage 与 install-hub.sh 的 CADDY_FILE、
// CADDY_STORAGE 保持一致。
const (
	kokoroCaddyFile    = "/etc/caddy/Caddyfile"
	kokoroCaddyStorage = "/var/lib/caddy/kokoro-caddy"
)

// caddyBlockBegin / caddyBlockEnd 与 install-hub.sh 的 CADDY_BLOCK_BEGIN、
// CADDY_BLOCK_END 一致。安装器只占这两个标记之间的一段，别人的站点在标记外面。
const (
	caddyBlockBegin = "# >>> Kokoro-Installer begin >>>"
	caddyBlockEnd   = "# <<< Kokoro-Installer end <<<"
)

// stripKokoroCaddyBlock 摘掉标记之间的 Kokoro 站点段，返回剩余内容与是否命中。
// 别人的站点都在标记外面，所以摘完只剩空白，就说明这份文件原本只属于 Kokoro。
func stripKokoroCaddyBlock(content string) (string, bool) {
	lines := strings.Split(content, "\n")
	out := make([]string, 0, len(lines))
	found := false
	skip := false
	for _, line := range lines {
		switch strings.TrimRight(line, " \t\r") {
		case caddyBlockBegin:
			// 我们自己那段前面可能留了一个分隔空行，一并收走：
			// 这样"合并再摘除"之后，别人的配置和装之前逐字节一致。
			if n := len(out); n > 0 && strings.TrimSpace(out[n-1]) == "" {
				out = out[:n-1]
			}
			skip = true
			found = true
			continue
		case caddyBlockEnd:
			skip = false
			continue
		}
		if !skip {
			out = append(out, line)
		}
	}
	if !found {
		return content, false
	}
	return strings.Join(out, "\n"), true
}

// isLegacyKokoroCaddyFile 判断这份 Caddyfile 是不是早期版本整体写给 Kokoro 的：
// 那时还没有分段标记，整份文件第一行就是 Kokoro 的标记。
func isLegacyKokoroCaddyFile(content string) bool {
	for i, line := range strings.Split(content, "\n") {
		if i >= 3 {
			break
		}
		if strings.Contains(line, installerMarker) {
			return true
		}
	}
	return false
}

// removeKokoroCaddySite 收回 Kokoro 写进 Caddyfile 的站点段。
// fileRemoved 表示整份文件都归 Kokoro 且已删除；changed 表示文件被改写过。
// 别人的站点只会被摘掉我们那一段，其余原样保留 —— 这是兼容安装必须守住的一条。
func removeKokoroCaddySite(path string) (fileRemoved, changed bool, err error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, false, nil
		}
		return false, false, err
	}
	remaining, found := stripKokoroCaddyBlock(string(raw))
	switch {
	case found && strings.TrimSpace(remaining) == "":
		if err := os.Remove(path); err != nil {
			return false, false, err
		}
		return true, true, nil
	case found:
		mode := os.FileMode(0o644)
		if info, statErr := os.Stat(path); statErr == nil {
			mode = info.Mode().Perm()
		}
		if err := os.WriteFile(path, []byte(remaining), mode); err != nil {
			return false, false, err
		}
		return false, true, nil
	case isLegacyKokoroCaddyFile(string(raw)):
		if err := os.Remove(path); err != nil {
			return false, false, err
		}
		return true, true, nil
	}
	return false, false, nil
}

// kokoroInstallerMarker 是安装器留在程序目录里的"这台机是我装的"标记。
const kokoroInstallerMarker = ".managed-by-kokoro-installer"

// kokoroNginxVhostDirs 与 install-hub.sh 选择虚拟主机目录的顺序一致。
var kokoroNginxVhostDirs = []string{
	"/www/server/panel/vhost/nginx",
	"/etc/nginx/sites-enabled",
	"/etc/nginx/conf.d",
}

// isKokoroManaged 报告文件存在且带 Kokoro 安装器标记。
func isKokoroManaged(path string) (bool, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	return bytes.Contains(raw, []byte(installerMarker)), nil
}

// removeIfKokoroManaged 删除带 Kokoro 标记的文件；不是 Kokoro 写的就原样保留。
func removeIfKokoroManaged(path string) (bool, error) {
	managed, err := isKokoroManaged(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if !managed {
		return false, nil
	}
	if err := os.Remove(path); err != nil {
		return false, err
	}
	return true, nil
}

// kokoroNginxBinary 找到本机 nginx，找不到返回空串。
func kokoroNginxBinary() string {
	if path, err := exec.LookPath("nginx"); err == nil {
		return path
	}
	const panelNginx = "/www/server/nginx/sbin/nginx"
	if info, err := os.Stat(panelNginx); err == nil && info.Mode().Perm()&0o111 != 0 {
		return panelNginx
	}
	return ""
}

// systemctlQuiet 与 runSystemctl 一样，但吞掉输出：调用点就是拿它做尽力而为的
// 清理，单元不存在时报一堆错只会盖住真正的结果。
func systemctlQuiet(args ...string) {
	_ = exec.Command("systemctl", args...).Run()
}

// removeKokoroWebServerConfig 收回安装器写下的反代配置和更新单元。
//
// 卸载后重装之所以会被"检测到已有 Caddy 配置"挡住，根因就是这里以前什么都不清：
// Caddyfile、Nginx 站点和 kokoro-update / kokoro-nginx-reload 单元都留在了主机上，
// 而安装器把它们当成"别人的站点"。
func removeKokoroWebServerConfig(purge bool) {
	for _, unit := range []string{"kokoro-update.path", "kokoro-nginx-reload.path"} {
		systemctlQuiet("disable", "--now", unit)
	}

	caddyRemoved, caddyChanged, err := removeKokoroCaddySite(kokoroCaddyFile)
	if err != nil {
		fmt.Printf("清理 Caddy 配置失败：%v\n", err)
	} else if caddyRemoved {
		fmt.Printf("已移除 Kokoro 写入的 Caddy 配置：%s\n", kokoroCaddyFile)
		if backups, _ := filepath.Glob(kokoroCaddyFile + ".kokoro-backup.*"); len(backups) > 0 {
			fmt.Printf("安装时备份的原配置仍在：%s\n", backups[len(backups)-1])
		}
	} else if caddyChanged {
		fmt.Printf("已从 %s 摘除 Kokoro 站点，其余站点保持不动\n", kokoroCaddyFile)
	}

	nginxRemoved := false
	for _, dir := range kokoroNginxVhostDirs {
		matches, _ := filepath.Glob(filepath.Join(dir, "kokoro-hub-*.conf"))
		for _, match := range matches {
			removed, err := removeIfKokoroManaged(match)
			if err != nil {
				fmt.Printf("移除 Nginx 站点配置失败 %s：%v\n", match, err)
				continue
			}
			if removed {
				nginxRemoved = true
				fmt.Printf("已移除 Kokoro 的 Nginx 站点配置：%s\n", match)
			}
		}
	}
	if nginxRemoved {
		if nginx := kokoroNginxBinary(); nginx != "" {
			if err := exec.Command(nginx, "-t").Run(); err == nil {
				_ = exec.Command(nginx, "-s", "reload").Run()
			}
		}
	}

	removedUnit := false
	for _, unit := range []string{
		"/etc/systemd/system/kokoro-update.service",
		"/etc/systemd/system/kokoro-update.path",
		"/etc/systemd/system/kokoro-nginx-reload.service",
		"/etc/systemd/system/kokoro-nginx-reload.path",
	} {
		if err := os.Remove(unit); err == nil {
			removedUnit = true
			fmt.Printf("已移除 %s\n", unit)
		} else if !os.IsNotExist(err) {
			fmt.Printf("移除 %s 失败：%v\n", unit, err)
		}
	}
	if removedUnit {
		runSystemctl("daemon-reload")
	}

	// Caddy 是安装器装来给 Kokoro 用的：整份配置都归 Kokoro 时，配置没了就没必要
	// 再让它跑下去，否则它会拿内存里的旧配置继续占着 80/443。
	// 但配置里还有别人的站点时只重新加载，别把人家的服务停掉。
	if caddyRemoved {
		runSystemctl("disable", "--now", "caddy")
	} else if caddyChanged {
		systemctlQuiet("reload", "caddy")
	}
	if purge && safePurgeDir(kokoroCaddyStorage) {
		if err := os.RemoveAll(kokoroCaddyStorage); err != nil && !os.IsNotExist(err) {
			fmt.Printf("删除 Caddy 证书目录失败：%v\n", err)
		} else {
			fmt.Printf("已删除 Caddy 证书目录 %s\n", kokoroCaddyStorage)
		}
	}
}

// removeKokoroProgramMarkers 清掉程序目录里的安装器标记，并在目录已经空了时收走它。
func removeKokoroProgramMarkers(hubBinary string, purge bool) {
	// 用 path 而不是 filepath：这些是 Linux 路径，filepath 在测试机上按 "\" 切分，
	// 会让同一份判断得出不同结果（同 safePurgeDir 的理由）。
	for _, dir := range []string{path.Dir(hubBinary), "/opt/kokoro", "/www/wwwroot/kokoro"} {
		if dir == "" || dir == "." || dir == "/" || !safePurgeDir(dir) {
			continue
		}
		_ = os.Remove(filepath.Join(dir, kokoroInstallerMarker))
		if purge {
			// os.Remove 只在目录为空时成功，所以这里不会误删还有内容的目录。
			_ = os.Remove(dir)
		}
	}
}

// removeKokoroMenuLib 收走 /usr/local/lib/kokoro 里的安装脚本。
//
// 只在本机没有 Agent 时做：那份 install.sh 也是 Agent 的卸载入口
// （uninstallAgent 就靠它），Agent 还在时不能碰。
func removeKokoroMenuLib(agentInstalled bool) {
	if agentInstalled {
		return
	}
	removed := false
	for _, name := range []string{"install.sh", "uninstall-menu.sh"} {
		if err := os.Remove(filepath.Join("/usr/local/lib/kokoro", name)); err == nil {
			removed = true
		}
	}
	if removed {
		fmt.Println("已移除 /usr/local/lib/kokoro 下的安装脚本")
	}
	_ = os.Remove("/usr/local/lib/kokoro")
}

// removeKokoroSystemUser 彻底删除时收走安装器建的 kokoro 系统账户。
//
// 只认安装器建的那种（system 账户 + nologin shell）：shell 不是 nologin 就说明
// 是别人手建的账户，一律保留。
func removeKokoroSystemUser(purge bool) {
	if !purge {
		return
	}
	if err := exec.Command("id", "-u", "kokoro").Run(); err != nil {
		return
	}
	if raw, err := os.ReadFile("/etc/passwd"); err == nil {
		for _, line := range strings.Split(string(raw), "\n") {
			fields := strings.Split(line, ":")
			if len(fields) >= 7 && fields[0] == "kokoro" && fields[6] != "/usr/sbin/nologin" {
				fmt.Printf("系统账户 kokoro 的登录 shell 是 %s，不是安装器建的，已保留\n", fields[6])
				return
			}
		}
	}
	if err := exec.Command("userdel", "kokoro").Run(); err != nil {
		fmt.Printf("删除系统账户 kokoro 失败：%v\n", err)
		return
	}
	fmt.Println("已删除系统账户 kokoro")
}

func uninstallHub(reader *bufio.Reader) int {
	hubBinary, hubData := installedHubPaths()
	hubService := unitInstalled("kokoro-hub")
	binaryPresent := false
	if info, err := os.Stat(hubBinary); err == nil && info.Mode().IsRegular() {
		binaryPresent = true
	}
	caddyPresent, _ := isKokoroManaged(kokoroCaddyFile)
	if !hubService && !binaryPresent && !caddyPresent {
		fmt.Println("未检测到 Kokoro 面板服务。")
		return 0
	}
	if !hubService || !binaryPresent {
		fmt.Println("检测到 Kokoro 面板的残留（服务或程序已不在），本次会一并清理。")
	}
	if promptLine(reader, "确定卸载面板？（输入 yes 确认）：") != "yes" {
		fmt.Println("已取消。")
		return 0
	}
	// 配置和数据分开问，因为这两件事的用途完全不同：
	// 数据要留着迁移或重装，配置有时想跟着机器一起清掉。
	// 默认值保持原来的安全取向——数据默认保留，配置默认删除。
	fmt.Printf("\n面板数据（数据库、上传文件、备份）在：%s\n", hubData)
	fmt.Println("SWAP 不算面板数据：交换空间是这台机器的资源，卸载不会动它。")
	purge := promptLine(reader, "是否彻底删除数据？（不可恢复）输入 delete 删除，直接回车保留：") == "delete"
	fmt.Println("\n面板配置包括：服务单元（kokoro-hub / kokoro-update / kokoro-nginx-reload）、")
	fmt.Printf("程序目录（%s）、反代配置、快捷命令和 kokoro 系统账户。\n", path.Dir(hubBinary))
	keepConfig := promptLine(reader, "是否保留配置？（重装可直接复用）输入 keep 保留，直接回车删除：") == "keep"

	if hubService {
		runSystemctl("disable", "--now", "kokoro-hub")
	}
	if keepConfig {
		fmt.Println("已停止并禁用面板服务；配置和程序按你的选择保留，重装时可直接复用。")
	} else {
		if err := os.Remove("/etc/systemd/system/kokoro-hub.service"); err != nil && !os.IsNotExist(err) {
			fmt.Printf("移除面板服务文件失败：%v\n", err)
			return 1
		}
		runSystemctl("daemon-reload")
		for _, path := range []string{
			hubBinary,
			hubBinary + ".new",
		} {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				fmt.Printf("移除 %s 失败：%v\n", path, err)
				return 1
			}
		}
	}

	if purge {
		if err := purgeHubData(hubData, hubBinary); err != nil {
			fmt.Printf("面板已卸载，但删除数据失败：%v\n", err)
			return 1
		}
		if _, statErr := os.Stat(hubData); statErr == nil {
			// 旧版把交换文件放在数据目录里，那是这台机器的 SWAP，不能被一起删掉，
			// 所以目录留了下来——如实说明，不谎报"彻底删除"。
			fmt.Printf("面板数据已删除；%s 仍然保留，因为里面还留着这台机器的交换文件（SWAP）。\n", hubData)
			fmt.Println("  Kokoro 不会再动它；确认不再需要时再手动处理：swapoff <文件> → 删除文件 → 去掉 /etc/fstab 里对应的行。")
		} else {
			fmt.Printf("数据目录 %s 已彻底删除。\n", hubData)
		}
	} else {
		fmt.Printf("数据目录 %s 已保留（重装可复用）。\n", hubData)
	}

	if !keepConfig {
		removeKokoroWebServerConfig(purge)
		removeKokoroProgramMarkers(hubBinary, purge)
		removeKokoroSystemUser(purge)

		agentInstalled := unitInstalled("kokoro-agent")
		if agentInstalled {
			if err := replaceMenuCommand("/usr/local/bin/kokoro-agent"); err != nil {
				fmt.Printf("面板已卸载，但恢复 Agent 菜单命令失败：%v\n", err)
				return 1
			}
		} else if err := removeMenuCommand(); err != nil {
			fmt.Printf("移除菜单命令失败：%v\n", err)
			return 1
		}
		removeKokoroMenuLib(agentInstalled)
	}
	// 有意保留的东西要讲明白，否则"卸载不干净"会变成下一次排查的起点。
	fmt.Println("以下内容有意保留：")
	fmt.Println("  - Caddy 软件包与其 apt 源（可能还有别的站点在用）")
	if keepConfig {
		fmt.Println("  - 面板配置（服务单元、程序、反代配置、快捷命令）：重装时可直接复用")
	}
	if !purge {
		fmt.Printf("  - 数据目录 %s（重装可复用；要彻底删除请在卸载时输入 delete）\n", hubData)
	}
	return 0
}

// ---- 面板数据清理（连库一起删） ----

// hubSwapFiles 列出旧版可能落在数据目录里的交换文件。
//
// 卸载只把它们**留下来**：交换空间是这台机器的资源，不是面板数据。新版已经把
// 交换文件放到根目录（/swapfile、/swapfile2…），这里只为兼容旧布局。
func hubSwapFiles(dataDir string) []string {
	return []string{
		filepath.Join(dataDir, "swapfile"),
		filepath.Join(dataDir, "swapfile.new"),
		filepath.Join(dataDir, "swapfile.old"),
	}
}

// safePurgeDir 只允许删「绝对路径、不是根、且路径里含 kokoro 段」的目录。
//
// 卸载是破坏性操作：宁可删不掉，也不能因为一个变量为空/被改就 rm -rf 掉系统目录。
// 用 path（而不是 filepath）是因为这些都是 Linux 路径，卸载只在 Linux 上跑；
// filepath 在 Windows 上按 "\\" 切分，会让同一份判断在测试机上得出不同结论。
func safePurgeDir(dir string) bool {
	if dir == "" {
		return false
	}
	clean := path.Clean(dir)
	if !strings.HasPrefix(clean, "/") || clean == "/" {
		return false
	}
	for _, part := range strings.Split(clean, "/") {
		if part == "kokoro" || strings.HasPrefix(part, "kokoro-") {
			return true
		}
	}
	return false
}

// purgeHubData 删除面板自己的数据：数据库、上传文件、备份、更新请求。
//
// **不动 SWAP。** 交换空间是这台机器的资源，不是面板数据：面板（或节点）只是
// 一次性替它配过一次。所以这里不 swapoff、不改 /etc/fstab、也不删交换文件；
// 旧版把交换文件放在数据目录里（/var/lib/kokoro/swapfile），那就把它留下，
// 目录也跟着留下，由调用方如实告知操作者。删除路径前一律过 safePurgeDir。
func purgeHubData(dataDir, hubBinary string) error {
	if !safePurgeDir(dataDir) {
		return fmt.Errorf("数据目录 %q 未通过安全检查，已中止删除", dataDir)
	}
	keep := make(map[string]bool)
	for _, swap := range hubSwapFiles(dataDir) {
		keep[swap] = true
	}
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		return fmt.Errorf("读取数据目录失败: %w", err)
	}
	for _, entry := range entries {
		target := filepath.Join(dataDir, entry.Name())
		if keep[target] {
			continue
		}
		if err := os.RemoveAll(target); err != nil {
			return fmt.Errorf("删除 %s 失败: %w", target, err)
		}
	}
	// 安装器会在程序目录里放一个 data 软链。数据目录整个没了就别留悬空链接；
	// 里面还有交换文件时链接仍然有效，保留。
	if hubBinary != "" {
		if _, statErr := os.Stat(dataDir); statErr != nil {
			link := filepath.Join(filepath.Dir(hubBinary), "data")
			if info, err := os.Lstat(link); err == nil && info.Mode()&os.ModeSymlink != 0 {
				_ = os.Remove(link)
			}
		}
	}
	return nil
}

func uninstallAgent(reader *bufio.Reader) int {
	if !unitInstalled("kokoro-agent") {
		fmt.Println("未检测到 Kokoro Agent 服务。")
		return 0
	}
	if promptLine(reader, "确定卸载 Agent？配置会备份；面板及面板数据不会删除。（输入 yes 确认）：") != "yes" {
		fmt.Println("已取消。")
		return 0
	}
	installer := "/usr/local/lib/kokoro/install.sh"
	if _, err := os.Stat(installer); err != nil {
		fmt.Println("未找到 Agent 卸载脚本：" + installer)
		return 1
	}
	cmd := exec.Command("sh", installer, "--uninstall")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Printf("卸载 Agent 失败：%v\n", err)
		return 1
	}
	return 0
}

func uninstallMenuCommand() int {
	script := "/usr/local/lib/kokoro/uninstall-menu.sh"
	if _, err := os.Stat(script); err != nil {
		fmt.Println("未找到快捷命令清理脚本：" + script)
		return 1
	}
	cmd := exec.Command("sh", script)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Printf("清理快捷命令失败：%v\n", err)
		return 1
	}
	return 0
}

func replaceMenuCommand(target string) error {
	const menuPath = "/usr/local/bin/kokoro"
	targetInfo, err := os.Stat(target)
	if err != nil {
		return fmt.Errorf("菜单目标不可用 %s: %w", target, err)
	}
	if targetInfo.IsDir() || targetInfo.Mode().Perm()&0111 == 0 {
		return fmt.Errorf("菜单目标不可执行: %s", target)
	}
	if info, err := os.Lstat(menuPath); err == nil {
		if info.Mode()&os.ModeSymlink == 0 {
			return fmt.Errorf("%s 已存在且不是软链接", menuPath)
		}
		if err := os.Remove(menuPath); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.Symlink(target, menuPath); err != nil {
		return err
	}
	if _, err := os.Stat(menuPath); err != nil {
		return fmt.Errorf("链接目标不可用: %w", err)
	}
	return nil
}

func removeMenuCommand() error {
	const menuPath = "/usr/local/bin/kokoro"
	info, err := os.Lstat(menuPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return fmt.Errorf("%s 已存在且不是软链接", menuPath)
	}
	return os.Remove(menuPath)
}

func promptLine(reader *bufio.Reader, label string) string {
	fmt.Print(label)
	line, err := reader.ReadString('\n')
	if err != nil && line == "" {
		return ""
	}
	return strings.TrimRight(line, "\r\n")
}

func showInstallInfo() {
	exe, _ := os.Executable()
	fmt.Printf("\n版本：Kokoro %s\n程序：%s\n", Version, exe)
	if unitInstalled("kokoro-agent") {
		if raw, err := os.ReadFile("/etc/kokoro/config.toml"); err == nil {
			for _, line := range strings.Split(string(raw), "\n") {
				key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
				if !ok {
					continue
				}
				key, value = strings.TrimSpace(key), strings.Trim(strings.TrimSpace(value), "\"'")
				if key == "hub" || key == "node_name" {
					fmt.Printf("%s：%s\n", map[string]string{"hub": "Hub 地址", "node_name": "节点名称"}[key], value)
				}
			}
		}
	}
	if !unitInstalled("kokoro-hub") {
		fmt.Printf("探针服务：%s\n", serviceState("kokoro-agent"))
		return
	}
	dataDir, err := resolvePasswdDataDir("")
	if err != nil {
		fmt.Printf("数据目录：%v\n", err)
		return
	}
	st, err := store.Open(filepath.Join(dataDir, "kokoro.db"))
	if err != nil {
		fmt.Printf("读取安装信息失败：%v\n", err)
		return
	}
	defer st.Close()
	fmt.Printf("数据目录：%s\n", dataDir)
	if hub.AdminCredentialsConfigured(st) {
		fmt.Printf("管理员账户：%s（已设置）\n", hub.AdminUsername(st))
	} else {
		fmt.Println("管理员账户：尚未设置")
	}
}

func serviceState(unit string) string {
	if !unitInstalled(unit) {
		return "未安装"
	}
	output, err := exec.Command("systemctl", "is-active", unit).Output()
	if err != nil {
		return "未运行"
	}
	return strings.TrimSpace(string(output))
}

func resetAdminFromMenu(reader *bufio.Reader) int {
	user := strings.TrimSpace(promptLine(reader, "新用户名（回车随机生成）："))
	if user == "" {
		random, err := randomText(6, "abcdefghjkmnpqrstuvwxyz23456789")
		if err != nil {
			fmt.Fprintln(os.Stderr, "生成随机用户名失败：", err)
			return 1
		}
		user = "kokoro-" + random
	}
	pass := promptLine(reader, "新密码（回车随机生成）：")
	if pass == "" {
		var err error
		pass, err = randomText(24, "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789!@#%+=")
		if err != nil {
			fmt.Fprintln(os.Stderr, "生成随机密码失败：", err)
			return 1
		}
	}
	if len(pass) < 8 {
		fmt.Fprintln(os.Stderr, "密码至少 8 位。")
		return 2
	}
	dataDir, err := resolvePasswdDataDir("")
	if err != nil {
		fmt.Fprintf(os.Stderr, "定位数据目录失败：%v\n", err)
		return 1
	}
	st, err := store.Open(filepath.Join(dataDir, "kokoro.db"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "打开数据库失败：%v\n", err)
		return 1
	}
	defer st.Close()
	if err := hub.SetAdminCredentials(st, user, pass); err != nil {
		fmt.Fprintf(os.Stderr, "重置管理员账户失败：%v\n", err)
		return 1
	}
	fmt.Println("\n管理员账户已重置，请保存新凭据：")
	fmt.Printf("用户名：%s\n密码：%s\n", user, pass)
	return 0
}

func randomText(length int, alphabet string) (string, error) {
	bytes := make([]byte, length)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	result := make([]byte, length)
	for i, b := range bytes {
		result[i] = alphabet[int(b)%len(alphabet)]
	}
	return string(result), nil
}

func runServe(args []string) int {
	hub.Version = Version
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	listen := fs.String("listen", "127.0.0.1:8799", "监听地址")
	data := fs.String("data", "./data", "数据目录")
	domain := fs.String("domain", "", "对外域名")
	tls := fs.String("tls", "proxy", "证书模式")
	siteName := fs.String("site", "Kokoro", "站点名")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	if err := os.MkdirAll(*data, 0o755); err != nil {
		log.Printf("[kokoro] 无法创建数据目录 %s: %v", *data, err)
		return 1
	}

	cfg := &model.HubConfig{
		Listen:      *listen,
		DataDir:     *data,
		Domain:      *domain,
		TLSMode:     *tls,
		SiteName:    *siteName,
		BehindProxy: *tls == "proxy",
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := serveHub(ctx, cfg); err != nil {
		log.Printf("[kokoro] Hub 退出: %v", err)
		return 1
	}
	return 0
}

// runPasswd 重设后台管理员账号。
// 口令明文不落盘，忘了只能重设——所以这个子命令是唯一的找回途径。
func runPasswd(args []string) int {
	fs := flag.NewFlagSet("passwd", flag.ExitOnError)
	data := fs.String("data", "", "数据目录（默认自动识别）")
	user := fs.String("user", "", "新管理员用户名")
	pass := fs.String("password", "", "新管理员密码")
	check := fs.Bool("check", false, "只检查管理员账户是否已设置")
	ifUnset := fs.Bool("if-unset", false, "仅当管理员账户尚未设置时写入")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	dataDir, err := resolvePasswdDataDir(*data)
	if err != nil {
		fmt.Fprintf(os.Stderr, "定位 Kokoro 数据目录失败：%v\n", err)
		return 1
	}
	st, err := store.Open(filepath.Join(dataDir, "kokoro.db"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "打开数据库失败: %v\n", err)
		return 1
	}
	defer st.Close()
	configured := hub.AdminCredentialsConfigured(st)
	if *check {
		if configured {
			fmt.Println("initialized")
		} else {
			fmt.Println("uninitialized")
		}
		return 0
	}
	if *ifUnset && configured {
		// 这里必须说清楚"密码没生效"：安装器复用旧数据目录时就走这条路，
		// 操作者以为刚设置的密码已经生效，登录时才发现进不去。
		fmt.Printf("管理员账户已存在（%s），未做修改；本次提供的密码没有生效。\n", hub.AdminUsername(st))
		fmt.Println("如需更换凭据：kokoro passwd --user 新用户名 --password '新密码'")
		return 0
	}

	p := *pass
	if p == "" {
		fmt.Fprintln(os.Stderr, "请用 --password 提供新密码")
		return 2
	}
	if *user == "" {
		fmt.Fprintln(os.Stderr, "请用 --user 提供新用户名")
		return 2
	}
	if err := hub.ValidateAdminPassword(p); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 2
	}

	if err := hub.SetAdminCredentials(st, *user, p); err != nil {
		fmt.Fprintf(os.Stderr, "设置管理员账号失败: %v\n", err)
		return 1
	}

	fmt.Printf("唯一管理员账户已覆盖：%s\n", *user)
	fmt.Println("如果 Hub 正在运行，请使用新账户重新登录。")
	return 0
}

// resolvePasswdDataDir follows the normal install layout where the Hub binary
// and its data/ directory are siblings. This keeps password recovery usable
// without asking the operator to know the internal data path.
func resolvePasswdDataDir(explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	candidates := make([]string, 0, 4)
	candidates = append(candidates, "/var/lib/kokoro", "/www/wwwroot/kokoro/data")
	if exe, err := os.Executable(); err == nil {
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			exe = resolved
		}
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), "data"))
	}
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(cwd, "data"))
	}
	for _, dir := range candidates {
		if info, err := os.Stat(filepath.Join(dir, "kokoro.db")); err == nil && !info.IsDir() {
			return dir, nil
		}
	}
	return "", fmt.Errorf("未找到 kokoro.db；请在 Kokoro 安装目录运行，或添加 --data <数据目录>")
}

func installedHubPaths() (binary, data string) {
	for _, candidate := range []string{"/opt/kokoro/kokoro", "/www/wwwroot/kokoro/kokoro"} {
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
			binary = candidate
			if candidate == "/opt/kokoro/kokoro" {
				data = "/var/lib/kokoro"
			} else {
				data = "/www/wwwroot/kokoro/data"
			}
			return
		}
	}
	return "/www/wwwroot/kokoro/kokoro", "/www/wwwroot/kokoro/data"
}

// hubLeftoversPresent 报告主机上是否还留着 Kokoro 面板的产物（程序或反代配置）。
// 卸载中断、手工删过二进制都会留下这种状态，需要一条路径能把它清干净。
func hubLeftoversPresent() bool {
	binary, _ := installedHubPaths()
	if info, err := os.Stat(binary); err == nil && info.Mode().IsRegular() {
		return true
	}
	managed, err := isKokoroManaged(kokoroCaddyFile)
	return err == nil && managed
}

func runAgent(args []string) int {
	agent.Version = Version
	fs := flag.NewFlagSet("agent", flag.ExitOnError)
	cfgPath := fs.String("config", agent.ConfigPath(), "配置文件")
	hubURL := fs.String("hub", "", "Hub 地址")
	token := fs.String("token", "", "安装令牌")
	interval := fs.Int("interval", 0, "上报间隔毫秒")
	name := fs.String("name", "", "节点名")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	cfg, err := agent.LoadConfig(*cfgPath)
	if err != nil {
		log.Printf("[kokoro-agent] 读取配置失败: %v", err)
		return 1
	}
	if *hubURL != "" {
		cfg.Hub = *hubURL
	}
	if *token != "" {
		cfg.Token = *token
	}
	if *interval > 0 {
		cfg.IntervalMS = *interval
	}
	if *name != "" {
		cfg.NodeName = *name
	}
	if cfg.Hub == "" {
		log.Printf("[kokoro-agent] 缺少 hub 地址：用 --hub https://<你的域名> 指定")
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	for {
		a, err := agent.New(cfg, newCollector())
		if err != nil {
			log.Printf("[kokoro-agent] 初始化失败: %v", err)
			return 1
		}
		a.SetConfigPath(*cfgPath)
		log.Printf("[kokoro-agent] 启动，Hub=%s，版本 %s", cfg.Hub, Version)
		err = a.Run(ctx)
		if errors.Is(err, agent.ErrMigrationRestart) && ctx.Err() == nil {
			cfg, err = agent.LoadConfig(*cfgPath)
			if err != nil {
				log.Printf("[kokoro-agent] 重新读取配置失败: %v", err)
				return 1
			}
			continue
		}
		if err != nil {
			log.Printf("[kokoro-agent] 退出: %v", err)
			return 1
		}
		return 0
	}
}

// newCollector 构造真实采集器；不支持的平台降级为 nil（agent 内部会退回兜底采集）。
func newCollector() agent.Collector {
	c, err := collector.New()
	if err != nil {
		log.Printf("[kokoro-agent] 采集器初始化失败，将只上报基础信息: %v", err)
		return nil
	}
	return c
}

func runRegister(args []string) int {
	fs := flag.NewFlagSet("register", flag.ExitOnError)
	cfgPath := fs.String("config", agent.ConfigPath(), "配置文件")
	hubURL := fs.String("hub", "", "Hub 地址")
	token := fs.String("token", "", "安装令牌")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	cfg, err := agent.LoadConfig(*cfgPath)
	if err != nil {
		cfg = &model.AgentConfig{}
	}
	if *hubURL != "" {
		cfg.Hub = *hubURL
	}
	if *token != "" {
		cfg.Token = *token
	}

	a, err := agent.New(cfg, newCollector())
	if err != nil {
		log.Printf("[kokoro-agent] 初始化失败: %v", err)
		return 1
	}
	a.SetConfigPath(*cfgPath)
	if err := a.RegisterOnce(context.Background()); err != nil {
		log.Printf("[kokoro-agent] 注册失败: %v", err)
		return 1
	}
	log.Printf("[kokoro-agent] 注册成功，token 已写入 %s", *cfgPath)
	return 0
}
