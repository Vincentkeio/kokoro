package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/Vincentkeio/kokoro/internal/model"
)

const (
	defaultShellTimeoutMS = 10000
	// 上限放到 30 分钟：跑分脚本（YABS 含 fio + iperf3 + Geekbench）实测
	// 要十几分钟，5 分钟根本跑不完。命令 worker 是独立 goroutine，
	// 长命令不会阻塞上报主循环。
	maxShellTimeoutMS = 1800000
	// maxOutputBytes stdout/stderr 各保留的上限，防止回传超大结果。
	maxOutputBytes = 64 * 1024
)

// enqueue 把 Hub 下发的命令放入队列；队列满或已停止时丢弃并记日志。
func (a *Agent) enqueue(cmd model.Command) {
	a.mu.Lock()
	ch := a.cmdCh
	closed := a.closed
	a.mu.Unlock()
	if closed || ch == nil {
		logger.Printf("agent 已停止，丢弃命令 %s(%s)", cmd.ID, cmd.Type)
		return
	}
	select {
	case ch <- cmd:
	default:
		logger.Printf("命令队列已满，丢弃命令 %s(%s)", cmd.ID, cmd.Type)
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
			a.runCommand(cmd)
		}
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
	if cmd.Type == "restart" && res.OK {
		logger.Printf("收到 restart 指令，退出进程等待 systemd 拉起")
		os.Exit(0)
	}
}

// execCommand 按类型分发命令，未知类型返回 ok=false 而不 panic。
func (a *Agent) execCommand(cmd model.Command) model.CommandResult {
	res := model.CommandResult{ID: cmd.ID}
	switch cmd.Type {
	case "shell":
		return a.runShell(cmd)
	case "reconfig":
		return a.reconfig(cmd)
	case "restart":
		res.OK = true
		res.Stdout = "准备退出，由 systemd 拉起"
		return res
	case "upgrade":
		// 本阶段只做占位：实际升级交给安装脚本，避免自更新出事故。
		target := payloadString(cmd.Payload, "version")
		res.OK = true
		res.Stdout = fmt.Sprintf("upgrade 占位：当前版本 %s，目标版本 %s；本版本不支持自更新，请由安装脚本升级", Version, target)
		logger.Printf("收到 upgrade 指令（目标版本 %s），仅占位不执行", target)
		return res
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

// runShell 通过 shell 执行 payload.cmd，带超时并回收 stdout/stderr。
func (a *Agent) runShell(cmd model.Command) model.CommandResult {
	res := model.CommandResult{ID: cmd.ID}
	script := payloadString(cmd.Payload, "cmd")
	if strings.TrimSpace(script) == "" {
		res.OK = false
		res.Stderr = "payload.cmd 为空"
		return res
	}
	timeout := payloadInt(cmd.Payload, "timeout_ms", defaultShellTimeoutMS)
	if timeout <= 0 {
		timeout = defaultShellTimeoutMS
	}
	if timeout > maxShellTimeoutMS {
		timeout = maxShellTimeoutMS
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Millisecond)
	defer cancel()

	name, flag := shellAndFlag()
	c := exec.CommandContext(ctx, name, flag, script)
	c.Env = os.Environ()
	if dir := payloadString(cmd.Payload, "cwd"); dir != "" {
		c.Dir = dir
	}
	var stdout, stderr limitedBuffer
	c.Stdout = &stdout
	c.Stderr = &stderr

	start := time.Now()
	err := c.Run()
	res.DurationMS = time.Since(start).Milliseconds()
	res.Stdout = stdout.String()
	res.Stderr = stderr.String()

	switch {
	case err == nil:
		res.OK = true
		return res
	case ctx.Err() == context.DeadlineExceeded:
		res.OK = false
		res.ExitCode = -1
		res.Stderr += fmt.Sprintf("\n[kokoro] 命令超时（%dms）被终止", timeout)
		return res
	default:
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			res.ExitCode = ee.ExitCode()
		} else {
			res.ExitCode = -1
			res.Stderr += "\n[kokoro] " + err.Error()
		}
		res.OK = false
		return res
	}
}

// reconfig 更新内存与磁盘配置（目前支持 interval_ms / node_name）。
func (a *Agent) reconfig(cmd model.Command) model.CommandResult {
	res := model.CommandResult{ID: cmd.ID, OK: true}
	var notes []string

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
	if _, ok := cmd.Payload["hub"]; ok {
		// 换 hub 需要重建客户端与重新注册，本阶段不支持热切换。
		notes = append(notes, "hub 暂不支持热切换（需重启 agent）")
		logger.Printf("reconfig 请求修改 hub，本版本不支持热切换")
	}
	if len(notes) == 0 {
		res.OK = false
		res.Stderr = "payload 中没有可识别的配置项"
		return res
	}

	interval := int(a.getInterval() / time.Millisecond)
	a.mu.Lock()
	nodeName := a.cfg.NodeName
	a.mu.Unlock()
	if err := saveConfig(a.configPath, func(raw map[string]string) {
		if _, ok := cmd.Payload["interval_ms"]; ok {
			raw["interval_ms"] = strconv.Itoa(interval)
		}
		if _, ok := cmd.Payload["node_name"]; ok {
			raw["node_name"] = nodeName
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
	return nil
}

// shellAndFlag 返回平台对应的 shell 与参数。
func shellAndFlag() (string, string) {
	if runtime.GOOS == "windows" {
		return "cmd", "/C"
	}
	// 优先 bash：社区的一键脚本（YABS / IPQuality / 融合怪…）几乎都按 bash 写，
	// 里面常有数组、`[[ ]]`、`<( )` 这类 POSIX sh 不支持的东西。
	// Debian 上 /bin/sh 是 dash，用 sh 跑会直接语法报错。
	// 机器上没有 bash 时才退回 sh。
	if p, err := exec.LookPath("bash"); err == nil {
		return p, "-c"
	}
	return "/bin/sh", "-c"
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

// payloadInt 取 payload 里的整数值，缺失或非法时用默认值。
func payloadInt(p map[string]any, key string, def int) int {
	if p == nil {
		return def
	}
	v, ok := p[key]
	if !ok {
		return def
	}
	if n := payloadIntValue(v); n > 0 {
		return n
	}
	return def
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
