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
	"net/http"
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
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)) // 最多 1MB
	if err != nil {
		return err
	}
	return json.Unmarshal(body, dst)
}

// newToken 生成带前缀的随机令牌。
func newToken(prefix string) string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return prefix + fmt.Sprint(time.Now().UnixNano())
	}
	return prefix + hex.EncodeToString(buf)
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
	if req.InstallToken == "" {
		writeErr(w, http.StatusUnauthorized, "缺少 install_token")
		return
	}
	if err := h.consumeInstallToken(req.InstallToken); err != nil {
		writeErr(w, http.StatusUnauthorized, err.Error())
		return
	}

	now := time.Now().UnixMilli()
	nodeToken := newToken("nt_")
	node := &model.Node{
		ID:         newID("nd_"),
		Name:       req.Hostname,
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
		HubVersion: "0.1.0",
	})
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
	if m.Ts == 0 {
		m.Ts = time.Now().UnixMilli()
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

	wasOffline := !node.Online
	if err := h.store.TouchNode(node.ID, m.Ts, ip); err != nil {
		log.Printf("[hub] 更新节点状态失败 %s: %v", node.ID, err)
	}
	if wasOffline {
		h.publishStatus(node.ID, true)
	}

	writeJSON(w, http.StatusOK, model.ReportResponse{
		OK:         true,
		IntervalMS: 2000,
		ServerTime: time.Now().UnixMilli(),
		// 把后台下发的测试任务带回去。agent 每次上报都会拉一次，
		// 所以不需要推送通道 —— 最长等一个上报周期就能拿到命令。
		Commands: h.pendingCommands(node.ID),
		// 告诉 agent「静态信息收到了」—— 它收到才会停发。
		// 写库失败时不回 true，让它下一条继续带。
		FactsOK: factsOK,
	})
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
	if seenIP == "" || len(localIPs) == 0 {
		return false // 判不了就当没有，别瞎猜
	}
	for _, ip := range localIPs {
		if ip == seenIP {
			return false // 公网 IP 就在自己网卡上 → 直连
		}
	}
	return true
}

func (h *Hub) clientIP(r *http.Request) string {
	if h.cfg.BehindProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			return strings.TrimSpace(strings.Split(xff, ",")[0])
		}
		if xr := r.Header.Get("X-Real-IP"); xr != "" {
			return strings.TrimSpace(xr)
		}
	}
	host, _, err := splitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func splitHostPort(s string) (string, string, error) {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == ':' {
			return s[:i], s[i+1:], nil
		}
	}
	return "", "", errors.New("没有端口")
}

// ---- 命令结果 ----

func (h *Hub) handleCommandResult(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "只接受 POST")
		return
	}
	var res model.CommandResult
	if err := readJSON(r, &res); err != nil {
		writeErr(w, http.StatusBadRequest, "解析失败")
		return
	}
	// 落库 + 解析摘要 + 写动态。失败不返回错误给 agent：
	// agent 已经跑完了，重试没有意义，重复回传只会刷日志。
	h.finishTaskFromResult(res)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ---- 制品分发 ----

// handleDownload 分发 agent 二进制与安装脚本。
// 文件放在数据目录的 dist/ 下，由构建流程放进去，避免 GitHub 抽风时装不上小鸡。
func (h *Hub) handleDownload(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/api/v1/dl/")
	// 只允许简单文件名，杜绝路径穿越
	if name == "" || strings.Contains(name, "/") || strings.Contains(name, "..") {
		writeErr(w, http.StatusBadRequest, "非法文件名")
		return
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
	token := strings.TrimPrefix(r.URL.Path, "/i/")
	token = strings.Trim(token, "/")
	if token == "" {
		writeErr(w, http.StatusBadRequest, "缺少安装令牌")
		return
	}
	if _, err := h.peekInstallToken(token); err != nil {
		writeErr(w, http.StatusUnauthorized, "安装令牌无效或已用完")
		return
	}

	scheme := "https"
	if r.TLS == nil && h.cfg.TLSMode != "proxy" {
		scheme = "http"
	}
	if h.cfg.BehindProxy {
		scheme = "https"
	}
	hubURL := fmt.Sprintf("%s://%s", scheme, r.Host)
	if h.cfg.Domain != "" {
		hubURL = fmt.Sprintf("https://%s", h.cfg.Domain)
	}

	script := fmt.Sprintf(`#!/bin/sh
# Kokoro 一键安装（由 Hub 生成，token 已内置）
set -e
KOKORO_HUB="%s"
KOKORO_TOKEN="%s"
echo "==> Kokoro 安装: $KOKORO_HUB"
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT
if command -v curl >/dev/null 2>&1; then
  curl -fsSL "$KOKORO_HUB/api/v1/dl/install.sh" -o "$TMP/install.sh"
else
  wget -qO "$TMP/install.sh" "$KOKORO_HUB/api/v1/dl/install.sh"
fi
sh "$TMP/install.sh" --hub "$KOKORO_HUB" --token "$KOKORO_TOKEN" "$@"
`, hubURL, token)

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", "inline; filename=kokoro-install.sh")
	fmt.Fprint(w, script)
}

// ---- 安装令牌管理 ----

type installToken struct {
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
	t, err := h.peekInstallToken(tok)
	if err != nil {
		return err
	}
	t.Uses++
	return h.saveInstallToken(t)
}

// CreateInstallToken 新建一个安装令牌（后台与首次启动都会用到）。
func (h *Hub) CreateInstallToken(label string, ttl time.Duration, maxUses int) (*installToken, error) {
	t := &installToken{
		Token:     newToken("it_"),
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
