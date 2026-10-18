package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Vincentkeio/kokoro/internal/model"
	"github.com/Vincentkeio/kokoro/internal/netprobe"
)

const (
	// maxOutputBytes stdout/stderr 各保留的上限，防止回传超大结果。
	maxOutputBytes = 64 * 1024
)

// enqueue 把 Hub 下发的命令放入队列；队列满或已停止时丢弃并记日志。
func (a *Agent) enqueue(cmd model.Command) {
	if cmd.ID == "" || len(cmd.ID) > 128 || len(cmd.Type) > 32 || len(cmd.Payload) > 16 {
		logger.Printf("拒绝格式异常的命令")
		return
	}
	a.mu.Lock()
	ch := a.cmdCh
	closed := a.closed
	a.mu.Unlock()
	if closed || ch == nil {
		logger.Printf("agent 已停止，丢弃命令 %s(%s)", cmd.ID, cmd.Type)
		return
	}
	if (cmd.Type == "test" || cmd.Type == "swap") && !a.acceptTask(cmd) {
		return
	}
	select {
	case ch <- cmd:
	default:
		logger.Printf("命令队列已满，丢弃命令 %s(%s)", cmd.ID, cmd.Type)
		if cmd.Type == "test" || cmd.Type == "swap" {
			a.saveTaskResult(model.CommandResult{ID: cmd.ID, Stderr: "Agent 命令队列已满，本次任务未执行"})
		}
	}
}

// commandWorker 串行消费命令队列：同一时刻最多执行 1 个命令，其余排队。
func (a *Agent) commandWorker(ctx context.Context) {
	defer a.cmdWG.Done()
	for {
		select {
		case <-a.cmdDone:
			return
		case <-ctx.Done():
			return
		case cmd := <-a.cmdCh:
			if cmd.Type == "test" {
				logger.Printf("执行命令 %s type=test", cmd.ID)
				a.runTaskCommand(ctx, cmd)
			} else if cmd.Type == "swap" {
				logger.Printf("执行命令 %s type=swap", cmd.ID)
				a.runTaskCommand(ctx, cmd)
			} else {
				a.runCommand(cmd)
				select {
				case <-a.migrationRestart:
					return
				default:
				}
			}
		}
	}
}

func (a *Agent) runTaskCommand(parent context.Context, cmd model.Command) {
	ctx, cancel := context.WithCancel(parent)
	a.mu.Lock()
	if a.activeTaskCancels == nil {
		a.activeTaskCancels = make(map[string]context.CancelFunc)
	}
	if a.cancelledTasks == nil {
		a.cancelledTasks = make(map[string]bool)
	}
	wasCancelled := a.cancelledTasks[cmd.ID]
	delete(a.cancelledTasks, cmd.ID)
	a.activeTaskCancels[cmd.ID] = cancel
	a.mu.Unlock()
	if wasCancelled {
		cancel()
	}
	var res model.CommandResult
	if wasCancelled {
		res = model.CommandResult{ID: cmd.ID, ExitCode: -1, Stderr: "任务已由后台取消，未执行"}
	} else if cmd.Type == "test" {
		res = a.runTestContext(ctx, cmd)
	} else {
		res = a.runSwapContext(ctx, cmd)
	}
	cancel()
	a.mu.Lock()
	delete(a.activeTaskCancels, cmd.ID)
	delete(a.cancelledTasks, cmd.ID)
	a.mu.Unlock()
	a.saveTaskResult(res)
}

func (a *Agent) cancelTask(id string) {
	if id == "" || len(id) > 128 {
		return
	}
	a.mu.Lock()
	if a.cancelledTasks == nil {
		a.cancelledTasks = make(map[string]bool)
	}
	if a.activeTaskCancels[id] == nil && len(a.cancelledTasks) >= cmdQueueSize {
		a.mu.Unlock()
		return
	}
	a.cancelledTasks[id] = true
	cancel := a.activeTaskCancels[id]
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// runCommand 执行单条命令并回传结果。
func (a *Agent) runCommand(cmd model.Command) {
	logger.Printf("执行命令 %s type=%s", cmd.ID, cmd.Type)
	res := a.execCommand(cmd)
	// 结果上报用独立超时，保证即使主循环正在退出也能回传。
	if err := a.postResult(res); err != nil {
		logger.Printf("回传命令 %s 结果失败：%v", cmd.ID, err)
	}
	if cmd.Type == "migrate" && res.OK {
		a.migrationRestartOnce.Do(func() { close(a.migrationRestart) })
		return
	}
	if cmd.Type == "restart" && res.OK {
		logger.Printf("收到 restart 指令，退出进程等待 systemd 拉起")
		os.Exit(0)
	}
}

// execCommand 按类型分发命令，未知类型返回 ok=false 而不 panic。
func (a *Agent) execCommand(cmd model.Command) model.CommandResult {
	res := model.CommandResult{ID: cmd.ID}
	fields := map[string]map[string]bool{
		"test":         {"kind": true},
		"swap":         {"size_mb": true},
		"reconfig":     {"interval_ms": true, "node_name": true, "netq_interval_min": true},
		"migrate":      {"hub": true, "backup_id": true},
		"netq_targets": {"provinces": true, "interval_min": true, "ip_version": true},
		"netq_catalog": {"catalog": true},
		"restart":      {},
		"upgrade":      {"version": true},
		"uninstall":    {},
	}
	if allowed, exists := fields[cmd.Type]; exists {
		for key := range cmd.Payload {
			if !allowed[key] {
				res.Stderr = "安全限制：指令包含不允许的参数：" + key
				return res
			}
		}
	}
	switch cmd.Type {
	case "test":
		return a.runTest(cmd)
	case "swap":
		return a.runSwapContext(context.Background(), cmd)
	case "shell":
		res.Stderr = "安全限制：不接受远程 shell 命令，请下发白名单测试指令"
		return res
	case "reconfig":
		return a.reconfig(cmd)
	case "migrate":
		return a.migrate(cmd)
	case "netq_targets":
		return a.netqTargets(cmd)
	case "netq_catalog":
		return a.netqCatalog(cmd)
	case "restart":
		res.OK = true
		res.Stdout = "准备退出，由 systemd 拉起"
		return res
	case "upgrade":
		return a.upgrade(cmd)
	case "uninstall":
		res.OK = false
		res.Stderr = "uninstall 本版本未实现：不做任何删除操作，请人工处理"
		logger.Printf("收到 uninstall 指令，已拒绝执行（本版本不实现）")
		return res
	default:
		res.OK = false
		res.Stderr = fmt.Sprintf("未知命令类型：%s", cmd.Type)
		logger.Printf("未知命令类型：%s", cmd.Type)
		return res
	}
}

// netqTargets 更新三网分省探测范围（payload.provinces：省份名数组）。
//
// 这是 Hub 对「探测范围」设置的统一下发入口：站长在后台点保存，
// Hub 在下一条上报响应里把命令带回来，agent 在这里改范围并**立刻**
// 唤醒探测循环 —— 不用等下一个 30 分钟的周期。空数组 = 关闭分省探测。
func (a *Agent) netqTargets(cmd model.Command) model.CommandResult {
	res := model.CommandResult{ID: cmd.ID}
	raw, ok := cmd.Payload["provinces"]
	if !ok {
		res.OK = false
		res.Stderr = "payload.provinces 缺失"
		return res
	}
	list, ok := raw.([]any)
	if !ok {
		res.OK = false
		res.Stderr = "payload.provinces 不是数组"
		return res
	}
	provinces := make([]string, 0, len(list))
	if len(list) > 64 {
		res.Stderr = "探测省份数量超出限制"
		return res
	}
	allowedProvinces := map[string]bool{}
	for _, p := range netprobe.Provinces() {
		allowedProvinces[p] = true
	}
	for _, v := range list {
		s, ok := v.(string)
		if !ok {
			res.OK = false
			res.Stderr = "payload.provinces 里有非字符串元素"
			return res
		}
		if !allowedProvinces[s] {
			res.Stderr = "不支持的探测省份：" + s
			return res
		}
		provinces = append(provinces, s)
	}
	if raw, ok := cmd.Payload["interval_min"]; ok {
		if !validCommandInteger(raw, -1, 1440) {
			res.Stderr = "探测间隔必须为 -1 到 1440 分钟"
			return res
		}
	}
	if raw, ok := cmd.Payload["ip_version"]; ok {
		version, valid := raw.(string)
		if !valid || (version != "v4" && version != "v6") {
			res.Stderr = "探测协议必须是 v4 或 v6"
			return res
		}
		a.setNetQIPFamily(version)
	}
	a.setNetQSelection(provinces)
	// 间隔也随这条命令带下来（后台可调）：连续探测是默认，但勾了几十个
	// 省时一轮要几分钟，需要间隔防止把机器和目标都打爆。
	if raw, ok := cmd.Payload["interval_min"]; ok {
		if n := payloadIntValue(raw); n >= -1 {
			a.setNetQInterval(n)
		}
	}
	res.OK = true
	res.Stdout = fmt.Sprintf("探测范围已更新：%v（%d 个点，%s）", provinces, len(provinces)*3, a.netqIPFamily())
	logger.Printf("三网探测范围命令：%v，协议 %s", provinces, a.netqIPFamily())
	return res
}

// netqCatalog 用 Hub 下发的新探测点目录覆盖本机内置表
// （payload.catalog：netprobe.CatalogFile 的 JSON 字符串）。
//
// 后台点「更新探测点列表」→ Hub 落库 → 发现本机上报的版本号不一致 →
// 下发这条命令 → 本机换目录并立刻重跑一轮。**不用重新构建/部署**。
func (a *Agent) netqCatalog(cmd model.Command) model.CommandResult {
	res := model.CommandResult{ID: cmd.ID}
	raw, ok := cmd.Payload["catalog"]
	if !ok {
		res.OK = false
		res.Stderr = "payload.catalog 缺失"
		return res
	}
	s, ok := raw.(string)
	if !ok || strings.TrimSpace(s) == "" {
		res.OK = false
		res.Stderr = "payload.catalog 不是非空字符串"
		return res
	}
	var f netprobe.CatalogFile
	if len(s) > 512*1024 {
		res.Stderr = "探测点目录超出大小限制"
		return res
	}
	if err := json.Unmarshal([]byte(s), &f); err != nil {
		res.OK = false
		res.Stderr = "目录 JSON 解析失败: " + err.Error()
		return res
	}
	if len(f.Targets) > 512 {
		res.Stderr = "探测点目录超出数量限制"
		return res
	}
	f.Targets = netprobe.PreserveBuiltinIPv6(f.Targets)
	if len(f.Targets) > 512 {
		res.Stderr = "合并内置 IPv6 探测点后目录超出数量限制"
		return res
	}
	for _, target := range f.Targets {
		if target.Province == "" || len(target.Province) > 64 || len(target.Hosts) > 8 ||
			(target.IPVersion != "" && target.IPVersion != "v4" && target.IPVersion != "v6") {
			res.Stderr = "探测点字段不合法"
			return res
		}
		switch target.ISP {
		case netprobe.ISPTelecom, netprobe.ISPUnicom, netprobe.ISPMobile, "hk", "tw", "jp", "us", "eu":
		default:
			res.Stderr = "不支持的探测点运营商"
			return res
		}
		for _, host := range append([]string{target.Host}, target.Hosts...) {
			ip := net.ParseIP(host)
			valid := isPublicProbeIPv4(ip)
			if target.IPVersion == "v6" {
				valid = isPublicProbeIPv6(ip)
			}
			if !valid {
				res.Stderr = "探测点必须是与协议族相符的公网 IP 地址"
				return res
			}
		}
	}
	netprobe.SetCatalog(f.Targets)
	a.setNetQSelection(a.netqSelection()) // 借它的唤醒通道立刻重跑一轮
	res.OK = true
	res.Stdout = fmt.Sprintf("探测点目录已更新：%d 个点（抓取于 %s）", len(f.Targets), f.FetchedAt)
	logger.Printf("三网探测点目录已更新：%d 个点", len(f.Targets))
	return res
}

func isPublicProbeIPv4(ip net.IP) bool {
	v4 := ip.To4()
	if v4 == nil || !ip.IsGlobalUnicast() || ip.IsPrivate() || v4[0] == 0 {
		return false
	}
	if v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127 {
		return false
	}
	if v4[0] == 198 && (v4[1] == 18 || v4[1] == 19) {
		return false
	}
	return true
}

func isPublicProbeIPv6(ip net.IP) bool {
	if ip == nil || ip.To4() != nil || ip.To16() == nil || !ip.IsGlobalUnicast() || ip.IsPrivate() {
		return false
	}
	var raw [16]byte
	copy(raw[:], ip.To16())
	addr := netip.AddrFrom16(raw)
	return netip.MustParsePrefix("2000::/3").Contains(addr) &&
		!netip.MustParsePrefix("2001:db8::/32").Contains(addr)
}

// reconfig 更新内存与磁盘配置（目前支持 interval_ms / node_name）。
func (a *Agent) reconfig(cmd model.Command) model.CommandResult {
	res := model.CommandResult{ID: cmd.ID, OK: true}
	var notes []string
	// Validate the complete payload before changing memory or disk state.
	for key, value := range cmd.Payload {
		switch key {
		case "interval_ms":
			if !validCommandInteger(value, 1, 600000) {
				res.OK = false
				res.Stderr = "interval_ms 非法"
				return res
			}
		case "netq_interval_min":
			if !validCommandInteger(value, -1, 1440) {
				res.OK = false
				res.Stderr = "netq_interval_min 非法"
				return res
			}
		case "node_name":
			name, ok := value.(string)
			if !ok || len(name) > 256 || strings.ContainsAny(name, "\r\n\x00") {
				res.OK = false
				res.Stderr = "node_name 非法"
				return res
			}
		}
	}

	if v, ok := cmd.Payload["interval_ms"]; ok {
		ms := payloadIntValue(v)
		if ms > 0 {
			applied := clampInterval(ms)
			a.setInterval(applied)
			notes = append(notes, fmt.Sprintf("interval_ms=%d", applied))
		} else {
			res.OK = false
			notes = append(notes, "interval_ms 非法")
		}
	}
	if v, ok := cmd.Payload["node_name"]; ok {
		name := strings.TrimSpace(fmt.Sprint(v))
		a.mu.Lock()
		a.cfg.NodeName = name
		a.mu.Unlock()
		notes = append(notes, "node_name="+name)
	}

	// 三网探测间隔：0=连续（默认）、-1=关、正数=分钟。
	if v, ok := cmd.Payload["netq_interval_min"]; ok {
		n := payloadIntValue(v)
		if n >= -1 {
			a.setNetQInterval(n)
			notes = append(notes, fmt.Sprintf("netq_interval_min=%d", n))
		} else {
			res.OK = false
			notes = append(notes, "netq_interval_min 非法（-1 关 / 0 连续 / 正数分钟）")
		}
	}
	if len(notes) == 0 {
		res.OK = false
		res.Stderr = "payload 中没有可识别的配置项"
		return res
	}

	interval := int(a.getInterval() / time.Millisecond)
	a.mu.Lock()
	nodeName := a.cfg.NodeName
	netqMin := a.cfg.NetQIntervalMin
	a.mu.Unlock()
	if err := saveConfig(a.configPath, func(raw map[string]string) {
		if _, ok := cmd.Payload["interval_ms"]; ok {
			raw["interval_ms"] = strconv.Itoa(interval)
		}
		if _, ok := cmd.Payload["node_name"]; ok {
			raw["node_name"] = nodeName
		}
		if _, ok := cmd.Payload["netq_interval_min"]; ok {
			raw["netq_interval_min"] = strconv.Itoa(netqMin)
		}
	}); err != nil {
		res.OK = false
		res.Stderr = "配置已更新到内存，但落盘失败：" + err.Error()
		logger.Printf("reconfig 落盘失败：%v", err)
		return res
	}

	res.Stdout = strings.Join(notes, "; ")
	logger.Printf("reconfig 完成：%s", res.Stdout)
	return res
}

// postResult 把命令结果回传给 Hub。
func (a *Agent) postResult(res model.CommandResult) error {
	body, err := json.Marshal(res)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), resultTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.hub+"/api/v1/command/result", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.token())

	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if resp.StatusCode/100 != 2 {
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return fmt.Errorf("HTTP %d：节点令牌无效，请重新注册", resp.StatusCode)
		}
		return fmt.Errorf("HTTP %d：%s", resp.StatusCode, truncate(string(b), 200))
	}
	var ack struct {
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal(b, &ack); err != nil || !ack.OK {
		return fmt.Errorf("Hub 未确认任务结果已保存")
	}
	return nil
}

// payloadString 取 payload 里的字符串值。
func payloadString(p map[string]any, key string) string {
	if p == nil {
		return ""
	}
	v, ok := p[key]
	if !ok || v == nil {
		return ""
	}
	return fmt.Sprint(v)
}

func validCommandInteger(value any, minimum, maximum int) bool {
	n := payloadIntValue(value)
	if n < minimum || n > maximum {
		return false
	}
	switch v := value.(type) {
	case float64:
		return v == float64(n)
	case float32:
		return v == float32(n)
	case int, int64:
		return true
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(v))
		return err == nil && parsed == n
	case json.Number:
		parsed, err := v.Int64()
		return err == nil && parsed == int64(n)
	default:
		return false
	}
}

// payloadIntValue 兼容 JSON number（float64）与字符串数字。
func payloadIntValue(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case float32:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return int(i)
		}
	case string:
		if i, err := strconv.Atoi(strings.TrimSpace(n)); err == nil {
			return i
		}
	}
	return 0
}

// limitedBuffer 是限长的输出缓冲，超出部分丢弃并标记截断。
type limitedBuffer struct {
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.limit <= 0 {
		b.limit = maxOutputBytes
	}
	room := b.limit - b.buf.Len()
	if room <= 0 {
		b.truncated = true
		return len(p), nil // 假装写成功，避免命令因输出被截断而失败
	}
	if len(p) > room {
		b.truncated = true
		b.buf.Write(p[:room])
		return len(p), nil
	}
	return b.buf.Write(p)
}

func (b *limitedBuffer) String() string {
	s := b.buf.String()
	if b.truncated {
		s += "\n...[输出已截断]"
	}
	return s
}
