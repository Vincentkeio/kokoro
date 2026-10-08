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
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/kokoro-probe/kokoro/internal/agent"
	"github.com/kokoro-probe/kokoro/internal/collector"
	"github.com/kokoro-probe/kokoro/internal/hub"
	"github.com/kokoro-probe/kokoro/internal/model"
	"github.com/kokoro-probe/kokoro/internal/store"
)

// Version 会被构建脚本用 -ldflags "-X main.Version=..." 覆盖。
var Version = "0.1.0"

const usage = `kokoro - 每台小鸡都有一颗心

用法:
  kokoro serve    [选项]    启动主控 Hub
  kokoro agent    [选项]    启动子机探针（别名: kokoro run）
  kokoro register [选项]    仅执行一次注册（把 token 写回配置文件）
  kokoro passwd   [选项]    重设后台管理员账号与密码
  kokoro version            打印版本

serve 选项:
  --listen   监听地址（默认 127.0.0.1:8799）
  --data     数据目录（默认 ./data，SQLite 与上传文件都放这里）
  --domain   对外域名（用于自动申请证书与生成安装命令）
  --tls      证书模式: proxy | selfsigned | none（默认 proxy，即由 Nginx 前置反代）

passwd 选项:
  --data     数据目录（默认 ./data）
  --user     管理员用户名（留空保持原值）
  --password 新密码（留空则从标准输入读一行，避免进 shell 历史）

agent 选项:
  --config   配置文件（默认 /etc/kokoro/config.toml）
  --hub      Hub 地址，例如 https://vps.mjfuns.lat
  --token    安装令牌 it_xxx（首次注册用）
  --interval 上报间隔毫秒（默认 2000）

示例:
  kokoro serve --listen 127.0.0.1:8799 --data /www/wwwroot/kokoro/data --domain vps.mjfuns.lat
  kokoro agent --hub https://vps.mjfuns.lat --token it_xxxxxxxx
`

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	if len(os.Args) < 2 {
		fmt.Print(usage)
		os.Exit(2)
	}

	switch os.Args[1] {
	case "serve":
		os.Exit(runServe(os.Args[2:]))
	case "agent", "run":
		// run 是 agent 的别名：systemd 里的写法 kokoro-agent run 更自然
		os.Exit(runAgent(os.Args[2:]))
	case "register":
		os.Exit(runRegister(os.Args[2:]))
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

func runServe(args []string) int {
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

	// 后台任务：聚合与清理
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			if err := st.Aggregate5m(); err != nil {
				log.Printf("[kokoro] 聚合失败: %v", err)
			}
			if err := st.Prune(24*time.Hour, 90*24*time.Hour); err != nil {
				log.Printf("[kokoro] 清理失败: %v", err)
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
	data := fs.String("data", "./data", "数据目录")
	user := fs.String("user", "", "管理员用户名（留空则保持原值）")
	pass := fs.String("password", "", "新密码（留空则从标准输入读一行）")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	p := *pass
	if p == "" {
		// 从 stdin 读一行：管道输入不会回显，也不会进 shell 历史
		fmt.Fprint(os.Stderr, "请输入新密码（从标准输入读一行）: ")
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && line == "" {
			fmt.Fprintln(os.Stderr, "读取密码失败:", err)
			return 1
		}
		p = strings.TrimRight(line, "\r\n")
	}
	if p == "" {
		fmt.Fprintln(os.Stderr, "密码不能为空")
		return 2
	}
	if len(p) < 8 {
		fmt.Fprintln(os.Stderr, "密码至少 8 位")
		return 2
	}

	st, err := store.Open(*data + "/kokoro.db")
	if err != nil {
		fmt.Fprintf(os.Stderr, "打开数据库失败: %v\n", err)
		return 1
	}
	defer st.Close()

	if err := hub.SetAdminCredentials(st, *user, p); err != nil {
		fmt.Fprintf(os.Stderr, "设置管理员账号失败: %v\n", err)
		return 1
	}

	name := *user
	if name == "" {
		name = "（保持原用户名）"
	}
	fmt.Printf("已设置管理员账号：%s\n", name)
	fmt.Println("如果 Hub 正在运行，直接去后台登录即可；改完密码会把已有会话全部注销。")
	return 0
}

func runAgent(args []string) int {
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
