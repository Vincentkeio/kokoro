// Package agent 是跑在子机上的 Kokoro 探针运行时：
// 负责注册、定时上报指标、断线重连与补报、执行 Hub 下发的命令。
//
// 只依赖标准库，采集能力通过 Collector 接口由 cmd 层注入。
package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kokoro-probe/kokoro/internal/model"
)

// Version 是 agent 版本，注册时上报给 Hub。
var Version = "0.1.0"

const logPrefix = "[kokoro-agent] "

const (
	defaultIntervalMS = 2000
	minIntervalMS     = 500
	maxIntervalMS     = 600000
	// replayBufferSize 断线期间最多缓冲的指标条数（恢复后按序补报）。
	replayBufferSize = 30

	initialBackoff = 1 * time.Second
	maxBackoff     = 60 * time.Second

	reportTimeout   = 15 * time.Second
	registerTimeout = 30 * time.Second
	resultTimeout   = 10 * time.Second
	shutdownWait    = 3 * time.Second

	cmdQueueSize = 64
	// maxResponseBytes 限制 Hub 响应体大小，避免异常响应打爆内存。
	maxResponseBytes = 1 << 20
)

var logger = log.New(os.Stderr, logPrefix, log.LstdFlags|log.Lmsgprefix)

// Collector 采集器接口。真实实现在 internal/collector 包，由 cmd 层注入，
// 本包只依赖接口，不依赖实现。
type Collector interface {
	Collect() (*model.Metrics, error)
}

// fallbackCollector 是未注入采集器时的兜底：只填协议必需字段，
// 保证 agent 自身可独立编译运行（不依赖 collector 包的实现进度）。
type fallbackCollector struct {
	start time.Time
}

func (f fallbackCollector) Collect() (*model.Metrics, error) {
	return &model.Metrics{
		V:    model.ProtocolVersion,
		Ts:   time.Now().UnixMilli(),
		Host: model.HostStat{Uptime: int64(time.Since(f.start).Seconds())},
	}, nil
}

// fatalError 表示不可恢复的错误（401/403）：agent 必须停止并提示重新注册。
type fatalError struct{ err error }

func (e *fatalError) Error() string { return e.err.Error() }
func (e *fatalError) Unwrap() error { return e.err }

// retryAfterError 携带 429 响应里 Retry-After 指定的等待时长。
type retryAfterError struct {
	err error
	d   time.Duration
}

func (e *retryAfterError) Error() string { return e.err.Error() }
func (e *retryAfterError) Unwrap() error { return e.err }

// Agent 是一台子机上的运行时实例。
type Agent struct {
	cfg       *model.AgentConfig
	collector Collector

	hub        string // 规范化后的 Hub 地址（含 scheme、无结尾斜杠）
	hubHost    string // Hub 主机名，用于证书校验
	client     *http.Client
	configPath string

	mu         sync.Mutex
	interval   time.Duration
	seq        int64
	buf        []*model.Metrics // 断线缓冲，按时间顺序，最多 replayBufferSize 条
	lastRTTms  float64
	cmdCh      chan model.Command
	cmdDone    chan struct{}
	closed     bool
	cmdWG      sync.WaitGroup
	warnNoColl bool
}

// New 构造 agent。c 为采集器，传 nil 时使用内置兜底采集器。
func New(cfg *model.AgentConfig, c Collector) (*Agent, error) {
	if cfg == nil {
		return nil, errors.New("配置不能为空")
	}
	hub := strings.TrimSpace(cfg.Hub)
	if hub == "" {
		return nil, errors.New("配置缺少 hub 地址")
	}
	if !strings.Contains(hub, "://") {
		hub = "https://" + hub
	}
	hub = strings.TrimRight(hub, "/")
	u, err := url.Parse(hub)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("hub 地址非法：%s", cfg.Hub)
	}

	interval := clampInterval(cfg.IntervalMS)
	if c == nil {
		c = fallbackCollector{start: time.Now()}
	}
	a := &Agent{
		cfg:        cfg,
		collector:  c,
		hub:        hub,
		hubHost:    u.Hostname(),
		interval:   time.Duration(interval) * time.Millisecond,
		configPath: ConfigPath(),
		buf:        make([]*model.Metrics, 0, replayBufferSize),
	}
	if err := a.buildClient(); err != nil {
		return nil, err
	}
	return a, nil
}

// clampInterval 把上报间隔限制在合理区间内。
func clampInterval(ms int) int {
	switch {
	case ms <= 0:
		return defaultIntervalMS
	case ms < minIntervalMS:
		return minIntervalMS
	case ms > maxIntervalMS:
		return maxIntervalMS
	default:
		return ms
	}
}

// buildClient 构造 HTTP 客户端：自签 CA 模式下按 SPKI 指纹固定校验服务端证书。
func (a *Agent) buildClient() error {
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}

	if fp := strings.TrimSpace(a.cfg.CAFingerprint); fp != "" {
		pin, err := normalizeFingerprint(fp)
		if err != nil {
			return err
		}
		// 自签 CA 无法走系统根证书链校验，改为自行比对 SPKI 指纹 + 主机名。
		tlsCfg.InsecureSkipVerify = true
		want, host := pin, a.hubHost
		tlsCfg.VerifyPeerCertificate = func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			return verifyPinned(rawCerts, want, host)
		}
		logger.Printf("已启用 CA 指纹固定：%s", maskFingerprint(pin))
	} else if a.cfg.Insecure {
		tlsCfg.InsecureSkipVerify = true
		logger.Printf("警告：insecure=true，已跳过服务端证书校验")
	}

	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = tlsCfg
	a.client = &http.Client{Timeout: reportTimeout, Transport: tr}
	return nil
}

// normalizeFingerprint 把 `sha256/xxxx` 形式的指纹统一成标准 base64。
// 同时兼容裸 base64、URL-safe base64 与十六进制写法。
func normalizeFingerprint(fp string) (string, error) {
	raw := strings.TrimSpace(fp)
	if i := strings.Index(raw, "/"); i >= 0 {
		alg := strings.ToLower(strings.TrimSpace(raw[:i]))
		if alg != "sha256" {
			return "", fmt.Errorf("不支持的指纹算法 %q，只支持 sha256", alg)
		}
		raw = strings.TrimSpace(raw[i+1:])
	}
	cands := [](func(string) ([]byte, error)){
		base64.StdEncoding.DecodeString,
		base64.RawURLEncoding.DecodeString,
		func(s string) ([]byte, error) { return hex.DecodeString(s) },
	}
	for _, dec := range cands {
		if b, err := dec(raw); err == nil && len(b) == sha256.Size {
			return base64.StdEncoding.EncodeToString(b), nil
		}
	}
	return "", fmt.Errorf("ca_fingerprint 格式非法：%s", fp)
}

// verifyPinned 校验服务端证书链里任意一张证书的 SPKI sha256 与指纹一致，
// 并对叶子证书做主机名校验（因为 InsecureSkipVerify 会跳过默认校验）。
func verifyPinned(rawCerts [][]byte, wantFp, host string) error {
	if len(rawCerts) == 0 {
		return errors.New("服务端未提供证书")
	}
	matched := false
	for _, raw := range rawCerts {
		c, err := x509.ParseCertificate(raw)
		if err != nil {
			return fmt.Errorf("解析服务端证书失败：%w", err)
		}
		sum := sha256.Sum256(c.RawSubjectPublicKeyInfo)
		got := base64.StdEncoding.EncodeToString(sum[:])
		if subtle.ConstantTimeCompare([]byte(got), []byte(wantFp)) == 1 {
			matched = true
		}
	}
	if !matched {
		return fmt.Errorf("服务端证书链的 SPKI 指纹与 ca_fingerprint 不匹配，拒绝连接")
	}
	leaf, err := x509.ParseCertificate(rawCerts[0])
	if err != nil {
		return fmt.Errorf("解析叶子证书失败：%w", err)
	}
	if host != "" {
		if err := leaf.VerifyHostname(host); err != nil {
			return fmt.Errorf("服务端证书主机名校验失败：%w", err)
		}
	}
	return nil
}

// Run 阻塞运行：必要时先注册，然后循环采集上报、执行 Hub 下发的命令，
// 直到 ctx 被取消或遇到不可恢复错误（401/403）。
func (a *Agent) Run(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := a.ensureToken(ctx); err != nil {
		return err
	}

	a.cmdCh = make(chan model.Command, cmdQueueSize)
	a.cmdDone = make(chan struct{})
	a.cmdWG.Add(1)
	go a.commandWorker(ctx)
	// 三网探测是独立循环：低频、重活，失败不影响指标上报
	go a.netqLoop(ctx)
	defer a.shutdown()

	logger.Printf("已启动：hub=%s interval=%s token=%s…", a.hub, a.interval, maskToken(a.token()))

	bo := newBackoff()
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		a.collectOnce()
		if err := a.flush(ctx); err != nil {
			var fe *fatalError
			if errors.As(err, &fe) {
				return err
			}
			var ra *retryAfterError
			var d time.Duration
			if errors.As(err, &ra) {
				d, bo = ra.d, newBackoff()
			} else {
				d = bo.next()
			}
			logger.Printf("上报失败：%v；%.1fs 后重试", err, d.Seconds())
			if !sleepCtx(ctx, d) {
				return nil
			}
			continue
		}
		bo.reset()
		if !sleepCtx(ctx, a.getInterval()) {
			return nil
		}
	}
}

// RegisterOnce 只执行一次注册并把 node_token/ca_fingerprint 落盘（安装脚本用）。
func (a *Agent) RegisterOnce(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	installToken := a.installToken()
	if installToken == "" {
		return &fatalError{err: errors.New("缺少 install_token：请在配置文件写 install_token 或设置环境变量 " + InstallTokenEnv)}
	}

	reqBody, err := json.Marshal(a.buildRegisterRequest(installToken))
	if err != nil {
		return fmt.Errorf("构造注册请求失败：%w", err)
	}
	rctx, cancel := context.WithTimeout(ctx, registerTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodPost, a.hub+"/api/v1/register", bytes.NewReader(reqBody))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("请求注册接口失败：%w", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized:
		return &fatalError{err: errors.New("注册失败 401：install_token 无效或已用尽")}
	case http.StatusForbidden:
		return &fatalError{err: errors.New("注册失败 403：该节点已被吊销")}
	default:
		return fmt.Errorf("注册失败 HTTP %d：%s", resp.StatusCode, truncate(string(b), 200))
	}

	var out model.RegisterResponse
	if err := json.Unmarshal(b, &out); err != nil {
		return fmt.Errorf("解析注册响应失败：%w", err)
	}
	if out.NodeToken == "" {
		return errors.New("注册响应缺少 node_token")
	}

	// 更新内存配置
	a.mu.Lock()
	a.cfg.Token = out.NodeToken
	a.cfg.NodeID = out.NodeID
	if out.CAFingerprint != "" {
		a.cfg.CAFingerprint = out.CAFingerprint
	}
	a.mu.Unlock()

	// 指纹可能变化，重建客户端
	if err := a.buildClient(); err != nil {
		return err
	}
	if out.IntervalMS > 0 {
		a.setInterval(out.IntervalMS)
	}

	token, fp, interval, nodeID := out.NodeToken, out.CAFingerprint, out.IntervalMS, out.NodeID
	if err := saveConfig(a.configPath, func(raw map[string]string) {
		raw["token"] = token
		if nodeID != "" {
			raw["node_id"] = nodeID
		}
		if fp != "" {
			raw["ca_fingerprint"] = fp
		}
		if interval > 0 {
			raw["interval_ms"] = strconv.Itoa(clampInterval(interval))
		}
	}); err != nil {
		// token 已在内存里，进程本次运行仍然可用，只告警不中断。
		logger.Printf("警告：token 写入配置文件 %s 失败（仅存在于内存）：%v", a.configPath, err)
	}

	logger.Printf("注册成功：node_id=%s token=%s… hub_version=%s", out.NodeID, maskToken(out.NodeToken), out.HubVersion)
	return nil
}

// ensureToken 没有 token 时先注册，失败按退避重试直到成功或遇到致命错误。
func (a *Agent) ensureToken(ctx context.Context) error {
	tok := strings.TrimSpace(a.token())
	// 安装脚本会把一次性安装令牌写在 token 键里，它不能用来上报，必须先换成正经的节点令牌。
	if tok != "" && !strings.HasPrefix(tok, "it_") {
		return nil
	}
	if tok == "" {
		logger.Printf("未配置 node_token，开始注册")
	} else {
		logger.Printf("检测到安装令牌，先注册换取节点令牌")
	}
	bo := newBackoff()
	for {
		err := a.RegisterOnce(ctx)
		if err == nil {
			return nil
		}
		var fe *fatalError
		if errors.As(err, &fe) {
			return err
		}
		d := bo.next()
		logger.Printf("注册失败：%v；%.1fs 后重试", err, d.Seconds())
		if !sleepCtx(ctx, d) {
			return ctx.Err()
		}
	}
}

// collectOnce 采集一次指标，分配 seq 并入缓冲（缓冲区只保留最近 30 条）。
func (a *Agent) collectOnce() {
	m, err := a.collector.Collect()
	if err != nil {
		logger.Printf("采集失败：%v", err)
		return
	}
	if m == nil {
		return
	}
	m.V = model.ProtocolVersion
	if m.Ts <= 0 {
		m.Ts = time.Now().UnixMilli()
	}

	a.mu.Lock()
	a.seq++
	m.Seq = a.seq
	if m.NetQ == nil && a.lastRTTms > 0 {
		m.NetQ = &model.NetQStat{HubLatencyMS: a.lastRTTms}
	}
	a.buf = append(a.buf, m)
	if n := len(a.buf); n > replayBufferSize {
		a.buf = append([]*model.Metrics(nil), a.buf[n-replayBufferSize:]...)
	}
	a.mu.Unlock()
}

// flush 按序补报缓冲里的全部指标；任一条失败即返回错误（保留缓冲下次再试）。
func (a *Agent) flush(ctx context.Context) error {
	for {
		a.mu.Lock()
		if len(a.buf) == 0 {
			a.mu.Unlock()
			return nil
		}
		m := a.buf[0]
		a.mu.Unlock()

		resp, err := a.sendReport(ctx, m)
		if err != nil {
			return err
		}
		a.mu.Lock()
		if len(a.buf) > 0 && a.buf[0] == m {
			a.buf = a.buf[1:]
		}
		a.mu.Unlock()
		if resp != nil {
			a.applyReport(resp)
		}
	}
}

// sendReport 上报一条指标并解析 Hub 响应。
func (a *Agent) sendReport(ctx context.Context, m *model.Metrics) (*model.ReportResponse, error) {
	body, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("序列化指标失败：%w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.hub+"/api/v1/report", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.token())

	start := time.Now()
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求上报接口失败：%w", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))

	// 记录到 Hub 的往返延迟，供下一轮 netq 使用
	a.mu.Lock()
	a.lastRTTms = float64(time.Since(start).Microseconds()) / 1000.0
	a.mu.Unlock()

	switch {
	case resp.StatusCode == http.StatusOK:
		var out model.ReportResponse
		if err := json.Unmarshal(b, &out); err != nil {
			return nil, fmt.Errorf("解析上报响应失败：%w", err)
		}
		if !out.OK {
			return nil, fmt.Errorf("hub 返回 ok=false：%s", truncate(string(b), 200))
		}
		return &out, nil
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, &fatalError{err: fmt.Errorf("HTTP %d：节点令牌无效或节点被禁用，请重新注册", resp.StatusCode)}
	case resp.StatusCode == http.StatusTooManyRequests:
		return nil, &retryAfterError{
			err: fmt.Errorf("HTTP 429：被限流"),
			d:   parseRetryAfter(resp.Header.Get("Retry-After")),
		}
	default:
		return nil, fmt.Errorf("HTTP %d：%s", resp.StatusCode, truncate(string(b), 200))
	}
}

// applyReport 处理上报响应：更新间隔、分发命令。
func (a *Agent) applyReport(resp *model.ReportResponse) {
	if ms := clampInterval(resp.IntervalMS); resp.IntervalMS > 0 && ms != int(a.getInterval()/time.Millisecond) {
		logger.Printf("上报间隔调整为 %dms", ms)
		a.setInterval(ms)
	}
	for _, cmd := range resp.Commands {
		a.enqueue(cmd)
	}
}

// ---- 本机信息（注册用，跨平台 best-effort）----

// buildRegisterRequest 组装注册请求，本机信息 best-effort 采集，取不到就留空。
func (a *Agent) buildRegisterRequest(installToken string) *model.RegisterRequest {
	info := hostInfo()
	return &model.RegisterRequest{
		InstallToken: installToken,
		Hostname:     info.hostname,
		OS:           info.os,
		Kernel:       info.kernel,
		Arch:         info.arch,
		Virt:         info.virt,
		AgentVersion: Version,
		CPUModel:     info.cpuModel,
		CPUCores:     info.cpuCores,
		MemTotal:     info.memTotal,
		DiskTotal:    info.diskTotal,
	}
}

type hostFacts struct {
	hostname, os, kernel, arch, virt, cpuModel string
	cpuCores                                   int
	memTotal, diskTotal                        int64
}

// hostInfo 采集注册所需本机信息。平台相关的细节交给 exec/文件读取，
// 取不到时返回空值而不是报错，避免在精简容器里注册失败。
func hostInfo() hostFacts {
	f := hostFacts{
		// OS 不再用 runtime.GOOS（只能给出 "linux" / "darwin" 这种平台名），
		// 而是读 /etc/os-release 的 PRETTY_NAME，拿到 "Debian GNU/Linux 12"、
		// "Ubuntu 22.04.4 LTS" 这种具体的。读不到时降级到 lsb_release，再降级 "linux"。
		os:       detectDistro(),
		arch:     runtime.GOARCH,
		cpuCores: runtime.NumCPU(),
	}
	if h, err := os.Hostname(); err == nil {
		f.hostname = h
	}
	f.kernel = readCommand("uname", "-r")
	f.virt = detectVirt()
	f.cpuModel = detectCPUModel()
	f.memTotal = detectMemTotal()
	f.diskTotal = detectDiskTotal()
	return f
}

// detectDistro 读 /etc/os-release 取发行版名。
//
// 原本 agent 注册时 OS 字段是 runtime.GOOS="linux"，看不出是 Debian / Ubuntu / CentOS。
// /etc/os-release 是 systemd 起的标准文件，几乎所有主流发行版都有：
//
//	PRETTY_NAME="Debian GNU/Linux 12 (bookworm)"
//	NAME="Debian GNU/Linux"
//
// 读不到时降级到 lsb_release，最后降级到 GOOS。
func detectDistro() string {
	if b, err := os.ReadFile("/etc/os-release"); err == nil {
		kv := map[string]string{}
		for _, line := range strings.Split(string(b), "\n") {
			if i := strings.IndexByte(line, '='); i > 0 {
				kv[line[:i]] = strings.Trim(strings.TrimSpace(line[i+1:]), "\"")
			}
		}
		// 优先拼「Debian 13」这种紧凑写法：PRETTY_NAME 常常是
		// "Debian GNU/Linux 13 (trixie)"，卡片上放不下。
		if id, ver := kv["ID"], kv["VERSION_ID"]; id != "" && ver != "" {
			return titleASCII(id) + " " + ver
		}
		for _, k := range []string{"PRETTY_NAME", "NAME"} {
			if v := kv[k]; v != "" {
				return v
			}
		}
	}
	if v := readCommand("lsb_release", "-d", "-s"); v != "" {
		return v
	}
	return runtime.GOOS
}

// titleASCII 把 "debian" 这种全小写的发行版 ID 首字母大写，其余不动。
// 不用 strings.Title（已废弃），也不用 cases 包（引入依赖不值当）。
func titleASCII(s string) string {
	if s == "" || s[0] < 'a' || s[0] > 'z' {
		return s
	}
	return string(s[0]-32) + s[1:]
}

// readCommand 执行命令并取首行输出，失败返回空串。
func readCommand(name string, args ...string) string {
	out, err := exec.Command(name, args...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// detectVirt 猜测虚拟化类型。
func detectVirt() string {
	if v := readCommand("systemd-detect-virt"); v != "" && v != "none" {
		return v
	}
	for _, path := range []string{
		"/sys/class/dmi/id/product_name",
		"/sys/class/dmi/id/sys_vendor",
		"/sys/class/dmi/id/product_version",
	} {
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		s := strings.ToLower(string(b))
		for _, k := range []string{"kvm", "qemu", "vmware", "virtualbox", "xen", "hyper-v", "microsoft", "amazon", "openstack", "docker", "lxc", "parallels", "bhyve"} {
			if strings.Contains(s, k) {
				return k
			}
		}
	}
	return ""
}

// detectCPUModel 读取 CPU 型号。
func detectCPUModel() string {
	if b, err := os.ReadFile("/proc/cpuinfo"); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			for _, key := range []string{"model name", "Hardware", "Processor"} {
				if i := strings.Index(line, key+":"); i >= 0 {
					if v := strings.TrimSpace(line[i+len(key)+1:]); v != "" {
						return v
					}
				}
			}
		}
	}
	if v := readCommand("sysctl", "-n", "machdep.cpu.brand_string"); v != "" {
		return v
	}
	return os.Getenv("PROCESSOR_IDENTIFIER")
}

// detectMemTotal 读取内存总字节数。
func detectMemTotal() int64 {
	if b, err := os.ReadFile("/proc/meminfo"); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(line, "MemTotal:") {
				fields := strings.Fields(line)
				if len(fields) >= 2 {
					if kb, err := strconv.ParseInt(fields[1], 10, 64); err == nil {
						return kb * 1024
					}
				}
			}
		}
	}
	if v := readCommand("sysctl", "-n", "hw.memsize"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n
		}
	}
	return 0
}

// detectDiskTotal 用 df 估算根分区总字节数（取不到返回 0）。
func detectDiskTotal() int64 {
	root := "/"
	if runtime.GOOS == "windows" {
		return 0
	}
	out := readCommand("df", "-P", "-k", root)
	lines := strings.Split(out, "\n")
	if len(lines) < 2 {
		return 0
	}
	fields := strings.Fields(lines[1])
	if len(fields) < 2 {
		return 0
	}
	if kb, err := strconv.ParseInt(fields[1], 10, 64); err == nil {
		return kb * 1024
	}
	return 0
}

// ---- 小工具 ----

func (a *Agent) token() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cfg.Token
}

// installToken 取注册用的一次性安装令牌。
//
// 取值顺序：环境变量 → 命令行/配置里的 token（仅当它是 it_ 前缀）→ 配置文件 install_token → 配置文件 token。
// 最后一项是为了兼容安装脚本：脚本把安装令牌写在 token 键里，注册成功后该键会被替换成节点令牌。
func (a *Agent) installToken() string {
	if v := strings.TrimSpace(os.Getenv(InstallTokenEnv)); v != "" {
		return v
	}
	a.mu.Lock()
	cfgTok := strings.TrimSpace(a.cfg.Token)
	a.mu.Unlock()
	if strings.HasPrefix(cfgTok, "it_") {
		return cfgTok
	}
	if b, err := os.ReadFile(a.configPath); err == nil {
		m := parseFlatTOML(string(b))
		if v := strings.TrimSpace(m["install_token"]); v != "" {
			return v
		}
		return strings.TrimSpace(m["token"])
	}
	return ""
}

func (a *Agent) getInterval() time.Duration {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.interval
}

func (a *Agent) setInterval(ms int) {
	a.mu.Lock()
	a.interval = time.Duration(clampInterval(ms)) * time.Millisecond
	a.cfg.IntervalMS = clampInterval(ms)
	a.mu.Unlock()
}

// shutdown 停止命令 worker（最多等 3 秒）并关闭空闲连接。
func (a *Agent) shutdown() {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return
	}
	a.closed = true
	close(a.cmdDone)
	a.mu.Unlock()

	done := make(chan struct{})
	go func() {
		a.cmdWG.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(shutdownWait):
		logger.Printf("等待在途命令结束超时（%s），强制退出", shutdownWait)
	}
	if a.client != nil {
		a.client.CloseIdleConnections()
	}
	logger.Printf("已停止")
}

// backoff 指数退避：1s 起步、60s 封顶、±20% 抖动。
type backoff struct {
	cur time.Duration
}

func newBackoff() *backoff {
	return &backoff{cur: initialBackoff}
}

func (b *backoff) next() time.Duration {
	d := b.cur
	// ±20% 抖动
	jitter := 0.8 + 0.4*rand.Float64()
	d = time.Duration(float64(d) * jitter)
	if b.cur < maxBackoff {
		b.cur *= 2
		if b.cur > maxBackoff {
			b.cur = maxBackoff
		}
	}
	if d > maxBackoff*6/5 {
		d = maxBackoff * 6 / 5
	}
	return d
}

func (b *backoff) reset() { b.cur = initialBackoff }

// parseRetryAfter 解析 Retry-After：支持秒数与 HTTP 日期两种形式。
func parseRetryAfter(v string) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return initialBackoff
	}
	if sec, err := strconv.Atoi(v); err == nil && sec >= 0 {
		return clampDur(time.Duration(sec)*time.Second, initialBackoff, maxBackoff)
	}
	if t, err := http.ParseTime(v); err == nil {
		return clampDur(time.Until(t), initialBackoff, maxBackoff)
	}
	return initialBackoff
}

func clampDur(d, lo, hi time.Duration) time.Duration {
	if d < lo {
		return lo
	}
	if d > hi {
		return hi
	}
	return d
}

// sleepCtx 可被 ctx 打断的 sleep，返回 false 表示 ctx 已取消。
func sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		select {
		case <-ctx.Done():
			return false
		default:
			return true
		}
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// maskToken 只暴露 token 前 6 位，避免日志泄露凭据。
func maskToken(t string) string {
	if len(t) <= 6 {
		return "******"
	}
	return t[:6]
}

// maskFingerprint 指纹只打前 12 位。
func maskFingerprint(fp string) string {
	if len(fp) > 12 {
		return fp[:12] + "…"
	}
	return fp
}

// truncate 截断字符串，避免把大段响应体打进日志。
func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
