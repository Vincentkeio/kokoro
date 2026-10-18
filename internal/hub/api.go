package hub

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Vincentkeio/kokoro/internal/model"
)

// ---- 工具 ----

func newID(prefix string) string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%s%d", prefix, time.Now().UnixNano())
	}
	return prefix + hex.EncodeToString(buf)
}

func hashToken(tok string) string {
	sum := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(sum[:])
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("[hub] 写响应失败: %v", err)
	}
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func readJSON(r *http.Request, dst any) error {
	defer r.Body.Close()
	const maxJSONBody = 1 << 20
	body, err := io.ReadAll(io.LimitReader(r.Body, maxJSONBody+1))
	if err != nil {
		return err
	}
	if len(body) > maxJSONBody {
		return errors.New("请求体超过 1MB")
	}
	return json.Unmarshal(body, dst)
}

// newToken 生成带前缀的随机令牌。
func newToken(prefix string) (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("生成安全令牌失败: %w", err)
	}
	return prefix + hex.EncodeToString(buf), nil
}

// ---- 注册 ----

func (h *Hub) handleRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "只接受 POST")
		return
	}

	var req model.RegisterRequest
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体解析失败")
		return
	}
	if req.InstallToken == "" || len(req.InstallToken) > 64 {
		writeErr(w, http.StatusUnauthorized, "缺少 install_token")
		return
	}
	if !validRegisterRequest(req) {
		writeErr(w, http.StatusBadRequest, "注册信息格式无效")
		return
	}
	installation, err := h.peekInstallToken(req.InstallToken)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	name := strings.TrimSpace(req.NodeName)
	if name == "" {
		name = installation.NodeName
	}
	if name == "" {
		name = installation.Label
	}
	name, err = normalizedInstallName(name)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if name == "" {
		name = req.Hostname
	}
	if err := h.consumeInstallToken(req.InstallToken); err != nil {
		writeErr(w, http.StatusUnauthorized, err.Error())
		return
	}

	now := time.Now().UnixMilli()
	nodeToken, err := newToken("nt_")
	if err != nil {
		log.Printf("[hub] %v", err)
		writeErr(w, http.StatusInternalServerError, "注册失败")
		return
	}
	node := &model.Node{
		ID:         newID("nd_"),
		Name:       name,
		Slug:       defaultSlug(req.Hostname),
		TokenHash:  hashToken(nodeToken),
		Visibility: model.VisibilityPublic,
		Hostname:   req.Hostname,
		OS:         req.OS,
		Kernel:     req.Kernel,
		Arch:       req.Arch,
		Virt:       req.Virt,
		CPUModel:   req.CPUModel,
		CPUCores:   req.CPUCores,
		MemTotal:   req.MemTotal,
		DiskTotal:  req.DiskTotal,
		TCPCC:      req.TCPCC,
		TCPQdisc:   req.TCPQdisc,
		// NAT 判定：Hub 看到的来源 IP 不在机器自己的网卡上 → 在 NAT 后面。
		// 只留布尔值，地址本身不落库。
		NAT:       isBehindNAT(req.LocalIPs, h.clientIP(r)),
		AgentVer:  req.AgentVersion,
		Online:    true,
		LastSeen:  now,
		CreatedAt: now,
	}
	if node.Name == "" {
		node.Name = node.ID
	}
	if err := h.store.CreateNode(node); err != nil {
		writeErr(w, http.StatusInternalServerError, "创建节点失败")
		return
	}

	// 顺带建一份空的档案，详情页不用判空
	_ = h.store.SaveProfile(&model.NodeProfile{NodeID: node.ID, UpdatedAt: now})

	log.Printf("[hub] 新节点注册: %s (%s) %s", node.ID, node.Name, node.Arch)
	_ = h.store.AddAudit("system", "node.register", node.ID, req.Hostname)

	writeJSON(w, http.StatusOK, model.RegisterResponse{
		NodeID:     node.ID,
		NodeToken:  nodeToken,
		IntervalMS: 2000,
		HubVersion: Version,
	})
}

func validRegisterRequest(req model.RegisterRequest) bool {
	if _, err := normalizedInstallName(req.NodeName); err != nil {
		return false
	}
	within := func(s string, max int) bool {
		return len([]rune(s)) <= max && !strings.ContainsAny(s, "\x00\r\n")
	}
	if !within(req.Hostname, 255) || !within(req.OS, 128) || !within(req.Kernel, 256) ||
		!within(req.Arch, 64) || !within(req.Virt, 64) || !within(req.AgentVersion, 64) ||
		!within(req.CPUModel, 256) || !within(req.TCPCC, 64) || !within(req.TCPQdisc, 64) ||
		len(req.LocalIPs) > 64 || req.CPUCores < 0 || req.CPUCores > 4096 ||
		req.MemTotal < 0 || req.DiskTotal < 0 {
		return false
	}
	for _, raw := range req.LocalIPs {
		ip := net.ParseIP(raw)
		if ip == nil {
			return false
		}
	}
	return true
}

func defaultSlug(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_':
			b.WriteRune('-')
		}
	}
	out := b.String()
	if out == "" {
		out = "node"
	}
	return out + "-" + newID("")[:6]
}

// ---- 上报 ----

func (h *Hub) handleReport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "只接受 POST")
		return
	}

	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if tok == "" {
		writeErr(w, http.StatusUnauthorized, "缺少令牌")
		return
	}
	node, err := h.store.GetNodeByTokenHash(hashToken(tok))
	if err != nil || node == nil {
		writeErr(w, http.StatusUnauthorized, "令牌无效，请重新注册")
		return
	}

	var m model.Metrics
	if err := readJSON(r, &m); err != nil {
		writeErr(w, http.StatusBadRequest, "指标解析失败")
		return
	}
	if m.Facts != nil && !validHostFacts(m.Facts) {
		writeErr(w, http.StatusBadRequest, "硬件或网络信息超出允许范围")
		return
	}
	if len(m.AgentVersion) > 100 || (m.UpdateState != nil && (len(m.UpdateState.Message) > 2048 || len(m.UpdateState.ID) > 64 || len(m.UpdateState.Target) > 100 || len(m.UpdateState.Status) > 32)) {
		writeErr(w, 400, "更新状态无效")
		return
	}
	if m.Ts == 0 {
		m.Ts = time.Now().UnixMilli()
	}
	if m.Tasks != nil {
		if len(m.Tasks.IDs) > 64 || len(m.Tasks.ResultIDs) > 64 {
			writeErr(w, http.StatusBadRequest, "任务状态数量超出限制")
			return
		}
		for _, id := range append(append([]string{}, m.Tasks.IDs...), m.Tasks.ResultIDs...) {
			if len(id) == 0 || len(id) > 128 {
				writeErr(w, http.StatusBadRequest, "任务状态编号无效")
				return
			}
		}
	}

	if err := h.store.InsertMetrics(node.ID, &m); err != nil {
		log.Printf("[hub] 写入指标失败 %s: %v", node.ID, err)
		writeErr(w, http.StatusInternalServerError, "写入失败")
		return
	}

	ip := h.clientIP(r)

	// 静态信息（虚拟化/CPU/加速/NAT）在**启动后的第一条上报**里带一次。
	// 收到就更新 —— 老 agent 升级后靠这条补齐，不用重新注册。
	factsOK := false
	if m.Facts != nil {
		if err := h.store.UpdateNodeFacts(node.ID, m.Facts, isBehindNAT(m.Facts.LocalIPs, ip)); err != nil {
			log.Printf("[hub] 更新节点静态信息失败 %s: %v", node.ID, err)
		} else {
			factsOK = true
		}
	}

	age := time.Now().UnixMilli() - node.LastSeen
	wasOffline := !node.Online || age > (20*time.Second).Milliseconds() || age < -(20*time.Second).Milliseconds()
	// 在线状态按 Hub 真正收到上报的时刻计算，不能使用 VPS 本地时钟；
	// 否则节点时间偏差会让 LastSeen 落在未来或过去，误判在线/离线。
	receivedAt := time.Now().UnixMilli()
	if err := h.store.TouchNode(node.ID, receivedAt, ip); err != nil {
		log.Printf("[hub] 更新节点状态失败 %s: %v", node.ID, err)
	}
	if wasOffline {
		h.publishStatus(node.ID, true)
		h.broadcastNodeTaskConnection(node.ID, true)
	}

	ended, err := h.store.ReconcileTaskHeartbeat(node.ID, m.Tasks)
	if err != nil {
		log.Printf("[hub] 核对任务状态失败 %s: %v", node.ID, err)
		writeErr(w, http.StatusInternalServerError, "核对任务状态失败")
		return
	}
	if len(ended) > 0 {
		for _, id := range ended {
			h.broadcastTaskProgress(id)
		}
		h.broadcastTaskCard(node.ID)
	} else if wasOffline {
		h.broadcastTaskCard(node.ID)
	}
	commands := h.pendingCommandsWithNetQ(node.ID, m.NetQProvinces, m.NetQCatalogVer, m.NetQIntervalMin, m.NetQIPVersion, m.Tasks)
	if c := h.updateCommand(node, &m); c != nil {
		commands = append(commands, *c)
	}
	if c := h.migrationCommand(node.ID, m.MigrationVersion); c != nil {
		commands = append(commands, *c)
	}
	writeJSON(w, http.StatusOK, model.ReportResponse{
		OK:         true,
		IntervalMS: 2000,
		ServerTime: time.Now().UnixMilli(),
		// 把后台下发的测试任务带回去。agent 每次上报都会拉一次，
		// 所以不需要推送通道 —— 最长等一个上报周期就能拿到命令。
		//
		// netq 的探测范围命令也走这条路：agent 上报里带自己当前用的
		// 省份列表 + 探测点目录版本 + 探测间隔，Hub 一发现和设置不一致
		// 就随这条响应下发（netq_targets / netq_catalog）。
		// 保存后 agent 数秒内切到新范围（即点即生效），agent 重启后
		// 也会在第一次上报时被自愈纠正。
		Commands:       commands,
		CancelledTasks: h.cancelledTaskIDs(node.ID, m.Tasks),
		// 告诉 agent「静态信息收到了」—— 它收到才会停发。
		// 写库失败时不回 true，让它下一条继续带。
		FactsOK: factsOK,
	})
}

func validHostFacts(f *model.HostFacts) bool {
	if !validRegisterRequest(model.RegisterRequest{CPUModel: f.CPUModel, CPUCores: f.CPUCores, Virt: f.Virt, TCPCC: f.TCPCC, TCPQdisc: f.TCPQdisc, LocalIPs: f.LocalIPs}) {
		return false
	}
	for _, value := range []string{f.Country, f.Region} {
		if len([]rune(value)) > 128 || strings.ContainsAny(value, "\x00\r\n") {
			return false
		}
	}
	return true
}

// clientIP 取真实客户端 IP，兼容 Nginx 反代。
// isBehindNAT 判断这台机器是不是在 NAT 后面。
//
// 判据：**Hub 看到的来源 IP 不在机器自己的网卡上**。
// NAT 小鸡（共享公网 IP 那种）网卡上是 10.x/172.x 私网地址，
// 出口公网 IP 属于宿主机，所以对不上。
//
// 只返回布尔值 —— 两边比对完就把地址丢掉，不落库、不展示。
func isBehindNAT(localIPs []string, seenIP string) bool {
	seen := net.ParseIP(seenIP)
	if seen == nil || len(localIPs) == 0 {
		return false // 判不了就当没有，别瞎猜
	}
	comparable := false
	for _, raw := range localIPs {
		ip := net.ParseIP(raw)
		if ip == nil || (ip.To4() == nil) != (seen.To4() == nil) {
			continue
		}
		comparable = true
		if ip.Equal(seen) {
			return false // 公网 IP 就在自己网卡上 → 直连
		}
	}
	// Older agents sent only IPv4. An IPv6 report cannot prove IPv4 NAT.
	return comparable
}

func remoteIP(remoteAddr string) net.IP {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	if i := strings.LastIndexByte(host, '%'); i >= 0 {
		host = host[:i]
	}
	return net.ParseIP(host)
}

// ---- 命令结果 ----

func (h *Hub) handleCommandResult(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeErr(w, http.StatusMethodNotAllowed, "只接受 POST")
		return
	}
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if tok == "" {
		writeErr(w, http.StatusUnauthorized, "缺少令牌")
		return
	}
	node, err := h.store.GetNodeByTokenHash(hashToken(tok))
	if err != nil || node == nil {
		writeErr(w, http.StatusUnauthorized, "令牌无效")
		return
	}
	var res model.CommandResult
	if err := readJSON(r, &res); err != nil {
		writeErr(w, http.StatusBadRequest, "解析失败")
		return
	}
	if len(res.ID) == 0 || len(res.ID) > 128 {
		writeErr(w, http.StatusBadRequest, "任务编号无效")
		return
	}
	if handled, e := h.migrationResult(node.ID, res); handled {
		if e != nil {
			writeErr(w, 500, "保存迁移状态失败，请重试")
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
		return
	}
	if h.updateResult(node.ID, res) {
		writeJSON(w, 200, map[string]bool{"ok": true})
		return
	}
	task, err := h.store.GetTask(res.ID)
	if err != nil {
		log.Printf("[hub] 读取任务结果目标失败 %s: %v", res.ID, err)
		writeErr(w, http.StatusInternalServerError, "任务状态暂时不可读，请重试")
		return
	}
	if task == nil {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	if task.NodeID != node.ID {
		writeErr(w, http.StatusForbidden, "任务不属于此节点")
		return
	}
	// Acknowledge only after durable storage. Retried results are idempotent.
	if err := h.finishTaskFromResult(res); err != nil {
		log.Printf("[hub] 保存任务结果失败 %s: %v", res.ID, err)
		writeErr(w, http.StatusInternalServerError, "任务结果尚未保存，请重试")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ---- 制品分发 ----

// handleDownload 分发 agent 二进制与安装脚本。
// 文件放在数据目录的 dist/ 下，由构建流程放进去，避免 GitHub 抽风时装不上小鸡。
func (h *Hub) handleDownload(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/api/v1/dl/")
	// 只允许简单文件名，杜绝路径穿越
	if name == "" || strings.Contains(name, "/") || strings.Contains(name, "\\") || strings.Contains(name, "..") {
		writeErr(w, http.StatusBadRequest, "非法文件名")
		return
	}
	for _, c := range name {
		if !(c >= 'a' && c <= 'z') && !(c >= 'A' && c <= 'Z') &&
			!(c >= '0' && c <= '9') && c != '.' && c != '_' && c != '-' {
			writeErr(w, http.StatusBadRequest, "非法文件名")
			return
		}
	}

	path := filepath.Join(h.cfg.DataDir, "dist", filepath.Clean(name))
	f, err := os.Open(path)
	if err != nil {
		writeErr(w, http.StatusNotFound, "制品不存在: "+name)
		return
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil || st.IsDir() {
		writeErr(w, http.StatusNotFound, "制品不存在")
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", fmt.Sprint(st.Size()))
	http.ServeContent(w, r, name, st.ModTime(), f)
}

// ---- 一键安装脚本 ----

// handleInstallScript 返回 /i/<token> 对应的引导脚本。
// 脚本本体从 Hub 自己下载，保证版本永远和 Hub 一致。
func (h *Hub) handleInstallScript(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		writeErr(w, http.StatusMethodNotAllowed, "只接受 GET")
		return
	}
	token := strings.TrimPrefix(r.URL.Path, "/i/")
	token = strings.Trim(token, "/")
	if token == "" || len(token) > 64 {
		writeErr(w, http.StatusBadRequest, "缺少安装令牌")
		return
	}
	installation, err := h.peekInstallToken(token)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "安装令牌无效或已用完")
		return
	}
	name := installation.NodeName
	if name == "" {
		name = installation.Label
	}
	if values, present := r.URL.Query()["name"]; present && len(values) > 0 {
		name = values[0]
	}
	name, err = normalizedInstallName(name)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	scheme := "https"
	if r.TLS == nil && h.cfg.TLSMode != "proxy" {
		scheme = "http"
	}
	if h.cfg.BehindProxy {
		scheme = "https"
	}
	host := r.Host
	if h.cfg.Domain != "" {
		host = h.cfg.Domain
		scheme = "https"
	}
	if !safeAuthority(host) {
		writeErr(w, http.StatusBadRequest, "站点域名配置无效")
		return
	}
	for _, c := range token {
		if !(c >= 'a' && c <= 'z') && !(c >= 'A' && c <= 'Z') &&
			!(c >= '0' && c <= '9') && c != '_' && c != '-' {
			writeErr(w, http.StatusUnauthorized, "安装令牌无效")
			return
		}
	}
	hubURL := (&url.URL{Scheme: scheme, Host: host}).String()
	releaseTag := Version
	if releaseTag == "" {
		releaseTag = "latest"
	}
	for _, c := range releaseTag {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '.' || c == '-') {
			releaseTag = "latest"
			break
		}
	}

	script := fmt.Sprintf(`#!/bin/sh
# Kokoro 一键安装（由 Hub 生成，token 已内置）
set -e
KOKORO_HUB="%s"
KOKORO_TOKEN="%s"
KOKORO_NODE_NAME=%s
KOKORO_RELEASE_TAG=%s
echo "==> Kokoro 安装: $KOKORO_HUB"
TMP=$(mktemp -d 2>/dev/null) || { echo "无法创建临时目录，请先安装 mktemp。" >&2; exit 1; }
trap 'rm -rf "$TMP"' EXIT
GH_ASSET_BASE="https://github.com/Vincentkeio/kokoro/releases/download/$KOKORO_RELEASE_TAG"
if command -v curl >/dev/null 2>&1; then
  curl -fsSL "$GH_ASSET_BASE/install.sh" -o "$TMP/install.sh" || { echo "从 GitHub 下载 Kokoro 安装脚本失败。" >&2; exit 1; }
  curl -fsSL "$GH_ASSET_BASE/install.sh.sha256" -o "$TMP/install.sh.sha256" || { echo "从 GitHub 下载安装脚本校验文件失败。" >&2; exit 1; }
elif command -v wget >/dev/null 2>&1; then
  wget -qO "$TMP/install.sh" "$GH_ASSET_BASE/install.sh" || { echo "从 GitHub 下载 Kokoro 安装脚本失败。" >&2; exit 1; }
  wget -qO "$TMP/install.sh.sha256" "$GH_ASSET_BASE/install.sh.sha256" || { echo "从 GitHub 下载安装脚本校验文件失败。" >&2; exit 1; }
else
  echo "需要 curl 或 wget 才能从 GitHub 下载 Kokoro 安装脚本。" >&2
  exit 1
fi
EXPECTED_SHA=$(awk '{print $1}' "$TMP/install.sh.sha256")
if command -v sha256sum >/dev/null 2>&1; then
  ACTUAL_SHA=$(sha256sum "$TMP/install.sh" | awk '{print $1}')
elif command -v shasum >/dev/null 2>&1; then
  ACTUAL_SHA=$(shasum -a 256 "$TMP/install.sh" | awk '{print $1}')
elif command -v openssl >/dev/null 2>&1; then
  ACTUAL_SHA=$(openssl dgst -sha256 "$TMP/install.sh" | awk '{print $NF}')
else
  echo "找不到 SHA256 校验工具。" >&2
  exit 1
fi
if [ -z "$EXPECTED_SHA" ] || [ "$EXPECTED_SHA" != "$ACTUAL_SHA" ]; then
  echo "GitHub 安装脚本 SHA256 校验失败。" >&2
  exit 1
fi
if [ -n "$KOKORO_NODE_NAME" ]; then set -- --name "$KOKORO_NODE_NAME" "$@"; fi
GH_TAG="$KOKORO_RELEASE_TAG" sh "$TMP/install.sh" --github --hub "$KOKORO_HUB" --token "$KOKORO_TOKEN" "$@"
`, hubURL, token, shellLiteral(name), shellLiteral(releaseTag))

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", "inline; filename=kokoro-install.sh")
	fmt.Fprint(w, script)
}

// ---- 安装令牌管理 ----

type installToken struct {
	NodeName  string `json:"node_name,omitempty"`
	Token     string `json:"token"`
	Label     string `json:"label"`
	CreatedAt int64  `json:"created_at"`
	ExpiresAt int64  `json:"expires_at"`
	MaxUses   int    `json:"max_uses"`
	Uses      int    `json:"uses"`
}

const tokenKeyPrefix = "it:"

func (h *Hub) saveInstallToken(t *installToken) error {
	b, err := json.Marshal(t)
	if err != nil {
		return err
	}
	return h.store.SetSetting(tokenKeyPrefix+t.Token, string(b))
}

func (h *Hub) peekInstallToken(tok string) (*installToken, error) {
	if tok == "" || len(tok) > 64 {
		return nil, errors.New("安装令牌无效")
	}
	v, err := h.store.GetSetting(tokenKeyPrefix + tok)
	if err != nil || v == "" {
		return nil, errors.New("安装令牌无效")
	}
	var t installToken
	if err := json.Unmarshal([]byte(v), &t); err != nil {
		return nil, errors.New("安装令牌损坏")
	}
	if t.ExpiresAt > 0 && time.Now().UnixMilli() > t.ExpiresAt {
		return nil, errors.New("安装令牌已过期")
	}
	if t.MaxUses > 0 && t.Uses >= t.MaxUses {
		return nil, errors.New("安装令牌已用尽")
	}
	return &t, nil
}

func (h *Hub) consumeInstallToken(tok string) error {
	if tok == "" || len(tok) > 64 {
		return errors.New("安装令牌无效")
	}
	h.tokenMu.Lock()
	defer h.tokenMu.Unlock()
	t, err := h.peekInstallToken(tok)
	if err != nil {
		return err
	}
	t.Uses++
	return h.saveInstallToken(t)
}

// CreateInstallToken 新建一个安装令牌（后台与首次启动都会用到）。
func (h *Hub) CreateInstallToken(label string, ttl time.Duration, maxUses int, presetName ...string) (*installToken, error) {
	name := ""
	if len(presetName) > 0 {
		var err error
		name, err = normalizedInstallName(presetName[0])
		if err != nil {
			return nil, err
		}
	}
	token, err := newToken("it_")
	if err != nil {
		return nil, err
	}
	t := &installToken{
		NodeName:  name,
		Token:     token,
		Label:     label,
		CreatedAt: time.Now().UnixMilli(),
		MaxUses:   maxUses,
	}
	if ttl > 0 {
		t.ExpiresAt = time.Now().Add(ttl).UnixMilli()
	}
	if err := h.saveInstallToken(t); err != nil {
		return nil, err
	}
	return t, nil
}

// ListInstallTokens 列出全部安装令牌。
func (h *Hub) ListInstallTokens() ([]installToken, error) {
	rows, err := h.store.ListSettings(tokenKeyPrefix)
	if err != nil {
		return nil, err
	}
	out := make([]installToken, 0, len(rows))
	for _, v := range rows {
		var t installToken
		if err := json.Unmarshal([]byte(v), &t); err == nil {
			out = append(out, t)
		}
	}
	return out, nil
}
