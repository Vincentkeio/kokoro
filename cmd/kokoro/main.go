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
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
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
var Version = "0.1.2"

const usage = `kokoro - 每台小鸡都有一颗心

用法:
  kokoro                 打开管理菜单（查看信息 / 重置唯一管理员）
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

func runMenu() int {
	reader := bufio.NewReader(os.Stdin)
	for {
		fmt.Println("\nKokoro 管理菜单")
		fmt.Println("1) 服务状态")
		fmt.Println("2) 安装信息")
		fmt.Println("3) 重启服务")
		fmt.Println("4) 更新探针程序（Hub 主程序请从部署端更新）")
		if unitInstalled("kokoro-hub") {
			fmt.Println("5) 重置唯一管理员账户")
		}
		fmt.Println("6) 查看最近日志")
		fmt.Println("7) 卸载面板（可选连数据库一起删除）")
		fmt.Println("8) 卸载 Agent")
		fmt.Println("9) 卸载快捷命令与管理脚本")
		fmt.Println("0) 退出")
		fmt.Print("请选择：")
		line, err := reader.ReadString('\n')
		if err != nil && line == "" {
			return 0
		}
		choice := strings.TrimSpace(line)
		switch choice {
		case "1":
			showServiceStatus()
		case "2":
			showInstallInfo()
		case "3":
			restartServices()
		case "4":
			updateAgent()
		case "5":
			if unitInstalled("kokoro-hub") && resetAdminFromMenu(reader) != 0 {
				return 1
			}
		case "6":
			showLogs()
		case "7":
			if uninstallHub(reader) != 0 {
				return 1
			}
		case "8":
			if uninstallAgent(reader) != 0 {
				return 1
			}
		case "9":
			if uninstallMenuCommand() != 0 {
				return 1
			}
		case "0", "q", "quit", "exit":
			return 0
		default:
			if choice == "" {
				fmt.Println("请输入菜单编号。")
			} else {
				fmt.Println("无效选项，请选择菜单中的编号。")
			}
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

func updateAgent() {
	installer := "/usr/local/lib/kokoro/install.sh"
	if _, err := os.Stat(installer); err != nil {
		fmt.Println("未找到探针安装脚本。请在节点上重新运行 Hub 提供的安装命令。")
		if unitInstalled("kokoro-hub") {
			fmt.Println("Hub 主程序的版本更新请在部署端运行 scripts/deploy.ps1。")
		}
		return
	}
	if cfg, err := agent.LoadConfig(""); err != nil {
		fmt.Printf("无法读取 Hub 地址，保留现有安装脚本：%v\n", err)
	} else if err := refreshInstaller(cfg.Hub, installer, &http.Client{Timeout: 30 * time.Second}); err != nil {
		fmt.Printf("无法从 Hub 刷新安装脚本，继续使用本地版本：%v\n", err)
	} else {
		fmt.Println("已从 Hub 更新本地 install.sh")
	}
	cmd := exec.Command("sh", installer, "--upgrade")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Printf("更新探针失败：%v\n", err)
	}
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

func uninstallHub(reader *bufio.Reader) int {
	if !unitInstalled("kokoro-hub") {
		fmt.Println("未检测到 Kokoro 面板服务。")
		return 0
	}
	hubBinary, hubData := installedHubPaths()
	if promptLine(reader, "确定卸载面板？（输入 yes 确认）：") != "yes" {
		fmt.Println("已取消。")
		return 0
	}
	// 第二步问"要不要连库一起删"，且默认保留：
	// 第一步的 yes 只表示"卸面板"，不该顺手把数据库清了。
	purge := false
	fmt.Printf("\n面板数据（数据库、上传文件、SWAP）在：%s\n", hubData)
	if promptLine(reader, "是否连数据一起彻底删除？（不可恢复）输入 purge 彻底删除，直接回车只卸载面板：") == "purge" {
		purge = true
	}

	runSystemctl("disable", "--now", "kokoro-hub")
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

	if purge {
		if err := purgeHubData(hubData, hubBinary); err != nil {
			fmt.Printf("面板已卸载，但删除数据失败：%v\n", err)
			return 1
		}
		fmt.Printf("Kokoro 面板与数据目录 %s 已彻底删除。\n", hubData)
	} else {
		fmt.Printf("Kokoro 面板已卸载；%s 数据目录已保留（重装可复用）。\n", hubData)
	}

	if unitInstalled("kokoro-agent") {
		if err := replaceMenuCommand("/usr/local/bin/kokoro-agent"); err != nil {
			fmt.Printf("面板已卸载，但恢复 Agent 菜单命令失败：%v\n", err)
			return 1
		}
	} else if err := removeMenuCommand(); err != nil {
		fmt.Printf("移除菜单命令失败：%v\n", err)
		return 1
	}
	return 0
}

// ---- 面板数据清理（连库一起删） ----

// managedSwapDir 是 Agent 新版 SWAP 用的 root 独占目录（见 internal/agent/swap.go）。
const managedSwapDir = "/var/lib/kokoro-swap"

// hubSwapFiles 列出 Kokoro 可能托管的 swap 文件：
// 数据目录下的旧版 swapfile 系列，以及 root 独占目录里的新版 swapfile。
func hubSwapFiles(dataDir string) []string {
	return []string{
		filepath.Join(dataDir, "swapfile"),
		filepath.Join(dataDir, "swapfile.new"),
		filepath.Join(dataDir, "swapfile.old"),
		filepath.Join(managedSwapDir, "swapfile"),
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

// fstabWithoutSwapPaths 去掉 fstab 里指向这些文件的 swap 挂载行。
//
// fstab 格式：<设备> <挂载点> <类型> <选项> ... ，swap 行的类型列是第 3 列。
// 只删「第 1 列命中给定路径、第 3 列是 swap」的行；注释、别的挂载一律原样保留。
func fstabWithoutSwapPaths(src string, paths map[string]bool) string {
	lines := strings.Split(src, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) >= 3 && paths[fields[0]] && fields[2] == "swap" {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// stripFstabSwapEntries 从 /etc/fstab 移除指向这些 swap 文件的挂载行（写回前先备份）。
func stripFstabSwapEntries(paths []string) error {
	const fstab = "/etc/fstab"
	raw, err := os.ReadFile(fstab)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // 容器/精简系统里没有 fstab，跳过
		}
		return fmt.Errorf("读取 fstab 失败: %w", err)
	}
	want := make(map[string]bool, len(paths))
	for _, p := range paths {
		want[p] = true
	}
	updated := fstabWithoutSwapPaths(string(raw), want)
	if updated == string(raw) {
		return nil
	}
	backup := fstab + ".kokoro-bak"
	if err := os.WriteFile(backup, raw, 0o644); err != nil {
		return fmt.Errorf("备份 fstab 失败: %w", err)
	}
	if err := os.WriteFile(fstab, []byte(updated), 0o644); err != nil {
		return fmt.Errorf("写回 fstab 失败: %w", err)
	}
	return nil
}

// cancelSwap 尽力关掉这些 swap 文件；没启用或已不存在就跳过。
//
// 必须先 swapoff 再删文件：启用中的 swapfile 被删掉会留下"已删除但仍在使用"的
// 交换区，白占内存且 fstab 里还挂着一条指向空气的记录。
func cancelSwap(files []string) {
	for _, f := range files {
		if _, err := os.Stat(f); err != nil {
			continue
		}
		if out, err := exec.Command("swapoff", f).CombinedOutput(); err != nil {
			fmt.Printf("提示：swapoff %s 未成功（可能未启用）：%s\n", f, strings.TrimSpace(string(out)))
		}
	}
}

// purgeHubData 彻底删除面板数据：先关掉 Kokoro 托管的 SWAP 并清掉 fstab 记录，
// 再删数据目录与 root 独占的 SWAP 目录。删除路径前一律过 safePurgeDir。
func purgeHubData(dataDir, hubBinary string) error {
	if !safePurgeDir(dataDir) {
		return fmt.Errorf("数据目录 %q 未通过安全检查，已中止删除", dataDir)
	}
	swapFiles := hubSwapFiles(dataDir)
	cancelSwap(swapFiles)
	if err := stripFstabSwapEntries(swapFiles); err != nil {
		return err
	}
	if err := os.RemoveAll(dataDir); err != nil {
		return fmt.Errorf("删除数据目录失败: %w", err)
	}
	if safePurgeDir(managedSwapDir) {
		if err := os.RemoveAll(managedSwapDir); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("删除 SWAP 目录失败: %w", err)
		}
	}
	// 安装器会在程序目录里放一个 data 软链；数据没了把它一起清掉，别留悬空链接。
	if hubBinary != "" {
		link := filepath.Join(filepath.Dir(hubBinary), "data")
		if info, err := os.Lstat(link); err == nil && info.Mode()&os.ModeSymlink != 0 {
			_ = os.Remove(link)
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

	st, err := store.Open(*data + "/kokoro.db")
	if err != nil {
		log.Printf("[kokoro] 打开数据库失败: %v", err)
		return 1
	}
	defer st.Close()

	cfg := &model.HubConfig{
		Listen:      *listen,
		DataDir:     *data,
		Domain:      *domain,
		TLSMode:     *tls,
		SiteName:    *siteName,
		BehindProxy: *tls == "proxy",
	}

	h, err := hub.New(cfg, st)
	if err != nil {
		log.Printf("[kokoro] 初始化 Hub 失败: %v", err)
		return 1
	}

	// 后台任务：分层 rollup 聚合（每 5 分钟）与保留期清理（每小时）。
	//
	// 参照 Komari 的节奏：「关桶聚合」跑得勤（分钟级），「按保留期清理」放低频
	// （小时级）—— 清理会做大范围 DELETE、必要时 VACUUM，不该挤在写路径上。
	// 各层保留期见 store 里的 retention* 常量（也可用 settings 覆盖）。
	go func() {
		compact := time.NewTicker(5 * time.Minute)
		retention := time.NewTicker(time.Hour)
		defer compact.Stop()
		defer retention.Stop()

		if err := st.AggregateRollups(); err != nil {
			log.Printf("[kokoro] 聚合失败: %v", err)
		}
		for {
			select {
			case <-compact.C:
				if err := st.AggregateRollups(); err != nil {
					log.Printf("[kokoro] 聚合失败: %v", err)
				}
			case <-retention.C:
				if err := st.Prune(); err != nil {
					log.Printf("[kokoro] 清理失败: %v", err)
				}
			}
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Printf("[kokoro] Hub 启动 %s（数据目录 %s，版本 %s）", *listen, *data, Version)
	if err := h.Run(ctx); err != nil {
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
		fmt.Println("管理员账户已存在，未做修改。")
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
	if len(p) < 8 {
		fmt.Fprintln(os.Stderr, "密码至少 8 位")
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

func runAgent(args []string) int {
	agent.Version = Version
	fs := flag.NewFlagSet("agent", flag.ExitOnError)
	cfgPath := fs.String("config", "/etc/kokoro/config.toml", "配置文件")
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

	a, err := agent.New(cfg, newCollector())
	if err != nil {
		log.Printf("[kokoro-agent] 初始化失败: %v", err)
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Printf("[kokoro-agent] 启动，Hub=%s，版本 %s", cfg.Hub, Version)
	if err := a.Run(ctx); err != nil {
		log.Printf("[kokoro-agent] 退出: %v", err)
		return 1
	}
	return 0
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
	cfgPath := fs.String("config", "/etc/kokoro/config.toml", "配置文件")
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
	if err := a.RegisterOnce(context.Background()); err != nil {
		log.Printf("[kokoro-agent] 注册失败: %v", err)
		return 1
	}
	log.Printf("[kokoro-agent] 注册成功，token 已写入 %s", *cfgPath)
	return 0
}
