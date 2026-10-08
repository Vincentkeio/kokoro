// Package alert 是 Kokoro Hub 的告警规则引擎。
//
// 引擎每 15 秒扫一轮：把规则与节点的最新指标做比较，用内存状态机记录「首次满足时间」，
// 连续满足 Duration 秒后才触发（避免抖动误报），恢复时再发一条恢复通知并回填 resolved。
//
// 后台 HTTP handler（HandleRules / HandleTest / HandleNotifyConfig）**不做鉴权**，
// 必须由调用方挂在已鉴权的路径下（例如 hub 的 /admin/* 之后）。
package alert

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kokoro-probe/kokoro/internal/model"
	"github.com/kokoro-probe/kokoro/internal/notify"
	"github.com/kokoro-probe/kokoro/internal/store"
)

const (
	// checkInterval 每轮检查间隔。
	checkInterval = 15 * time.Second
	// notifyCooldown 同一规则同一节点两次触发的最小间隔（抑制告警风暴）。
	notifyCooldown = 10 * time.Minute
	// sendTimeout 单轮内投递通知的超时上限。
	sendTimeout = 30 * time.Second
	// maxBody 后台接口请求体上限。
	maxBody = 1 << 20
)

// extraSchema 本包自建的表（幂等）。
// alert_traffic_base 记录每月流量基线的累计计数器读数，用于算「本月已用流量」；
// 计数器会因重启/重装而回绕，所以同时存 last 值用于校正。
const extraSchema = `
CREATE TABLE IF NOT EXISTS alert_traffic_base (
    node_id   TEXT    NOT NULL,
    month     TEXT    NOT NULL,          -- YYYY-MM，本地时区
    base_up   INTEGER NOT NULL DEFAULT 0,
    base_down INTEGER NOT NULL DEFAULT 0,
    last_up   INTEGER NOT NULL DEFAULT 0,
    last_down INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (node_id, month)
);
`

// MetricDef 描述一个可告警的指标，供后台页面下拉框使用。
type MetricDef struct {
	Key  string `json:"key"`
	Name string `json:"name"`
	Unit string `json:"unit"` // 阈值单位说明
}

// MetricDefs 支持的告警指标清单。
var MetricDefs = []MetricDef{
	{Key: "cpu", Name: "CPU 使用率", Unit: "%"},
	{Key: "mem", Name: "内存使用率", Unit: "%"},
	{Key: "disk", Name: "磁盘使用率", Unit: "%"},
	{Key: "swap", Name: "SWAP 使用率", Unit: "%"},
	{Key: "load1", Name: "1 分钟负载", Unit: ""},
	{Key: "net_up", Name: "上行速率", Unit: "MB/s"},
	{Key: "net_down", Name: "下行速率", Unit: "MB/s"},
	{Key: "offline", Name: "离线时长", Unit: "秒（阈值即秒数）"},
	{Key: "traffic", Name: "本月总流量", Unit: "GB"},
}

// metricAliases 兼容历史/文档里出现过的别名。
var metricAliases = map[string]string{
	"load":  "load1",
	"load1": "load1",
}

// metricLabels 指标中文名，用于通知文案。
var metricLabels = map[string]string{
	"cpu":      "CPU",
	"mem":      "内存",
	"disk":     "磁盘",
	"swap":     "SWAP",
	"load1":    "负载",
	"net_up":   "上行",
	"net_down": "下行",
	"offline":  "离线",
	"traffic":  "月流量",
}

// ruleState 一条规则在一个节点上的内存状态。
type ruleState struct {
	firing    bool      // 当前是否处于告警中
	eventID   string    // 最近一次写入的 alert_events.id（用于回填 resolved）
	lastFired time.Time // 上次触发时间（10 分钟抑制用）
	since     time.Time // 首次满足条件的时间（持续时间用）；零值表示当前不满足
}

// Manager 告警引擎。
type Manager struct {
	st     *store.Store
	hubURL string

	mu     sync.Mutex
	states map[string]*ruleState // key: ruleID + "|" + nodeID
}

// New 创建告警引擎，并幂等建好本包需要的表。
// hubURL 用于拼通知里的节点链接（如 https://kokoro.example.com），可为空。
func New(st *store.Store, hubURL string) *Manager {
	m := &Manager{
		st:     st,
		hubURL: strings.TrimRight(hubURL, "/"),
		states: make(map[string]*ruleState),
	}
	if st != nil {
		if _, err := st.DB().Exec(extraSchema); err != nil {
			log.Printf("[alert] 建表失败: %v", err)
		}
	}
	return m
}

// Start 启动后台检查循环，每 15 秒一轮；ctx 取消后退出。非阻塞。
func (m *Manager) Start(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(checkInterval)
		defer ticker.Stop()
		m.check(ctx)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				m.check(ctx)
			}
		}
	}()
}

// ==================== 核心循环 ====================

// check 跑一轮检查。
func (m *Manager) check(ctx context.Context) {
	if m.st == nil {
		return
	}
	nodes, err := m.st.ListNodes(true)
	if err != nil {
		log.Printf("[alert] 读取节点失败: %v", err)
		return
	}
	snap, err := m.st.LatestSnapshot()
	if err != nil {
		log.Printf("[alert] 读取最新指标失败: %v", err)
		return
	}
	rules, err := m.st.ListAlertRules()
	if err != nil {
		log.Printf("[alert] 读取告警规则失败: %v", err)
		return
	}
	cfg, err := notify.LoadConfig(m.st)
	if err != nil {
		log.Printf("[alert] 读取通知配置失败: %v", err)
	}
	sender := notify.New(cfg)

	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	nowMS := now.UnixMilli()
	active := make(map[string]bool, len(m.states))

	for i := range rules {
		rule := rules[i]
		if rule.Silenced || rule.Metric == "" {
			continue
		}
		for j := range nodes {
			node := nodes[j]
			if rule.NodeID != "" && rule.NodeID != node.ID {
				continue
			}
			key := rule.ID + "|" + node.ID
			active[key] = true
			st := m.state(key, rule.ID, node.ID)

			latest := snap[node.ID]
			value, ok := metricValue(rule.Metric, &node, latest)
			if normalizeMetric(rule.Metric) == "traffic" {
				// 月流量要读/写基线表，只在真的用得上时才算，避免每轮多余写入。
				if latest == nil {
					st.since = time.Time{}
					continue
				}
				value, ok = m.trafficGB(node.ID, latest), true
			}
			if !ok {
				// 取不到值（没数据 / 没 SWAP 等）：清掉持续时间，但不算恢复。
				st.since = time.Time{}
				continue
			}
			if !compare(rule.Op, value, rule.Threshold) {
				st.since = time.Time{}
				if st.firing {
					m.resolve(ctx, sender, rule, &node, st, value, now)
				}
				continue
			}

			if st.since.IsZero() {
				st.since = now
			}
			sustained := now.Sub(st.since)
			if sustained < time.Duration(rule.Duration)*time.Second {
				continue
			}
			if st.firing {
				// 已在告警中：持续满足也不重复打扰（10 分钟冷却到了才补发）。
				if now.Sub(st.lastFired) < notifyCooldown {
					continue
				}
			}
			m.fire(ctx, sender, rule, &node, st, value, sustained, nowMS)
		}
	}

	// 清掉已被删除的规则/节点留下的状态。
	for k := range m.states {
		if !active[k] {
			delete(m.states, k)
		}
	}
}

// state 取（必要时创建）规则+节点的状态。
// 首次创建时会回看 alert_events：若最近一条事件尚未恢复，则视为「已在告警中」，
// 这样 Hub 重启后不会把同一条告警再发一遍。
func (m *Manager) state(key, ruleID, nodeID string) *ruleState {
	if st, ok := m.states[key]; ok {
		return st
	}
	st := &ruleState{}
	var id string
	var firedAt, resolved int64
	err := m.st.DB().QueryRow(
		`SELECT id, fired_at, resolved FROM alert_events
WHERE rule_id = ? AND node_id = ? ORDER BY fired_at DESC LIMIT 1`, ruleID, nodeID).
		Scan(&id, &firedAt, &resolved)
	if err == nil && id != "" && resolved == 0 {
		st.firing = true
		st.eventID = id
		if firedAt > 0 {
			st.lastFired = time.UnixMilli(firedAt)
		}
	} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
		log.Printf("[alert] 读取告警历史失败: %v", err)
	}
	m.states[key] = st
	return st
}

// fire 触发告警：写事件 + 发通知。
func (m *Manager) fire(ctx context.Context, sender notify.Notifier, rule model.AlertRule,
	node *model.Node, st *ruleState, value float64, sustained time.Duration, nowMS int64) {

	title := triggerText(rule, node.Name, value, sustained)
	ev := &model.AlertEvent{
		NodeID:  node.ID,
		RuleID:  rule.ID,
		Message: title,
		FiredAt: nowMS,
	}
	if err := m.st.AddAlertEvent(ev); err != nil {
		log.Printf("[alert] 写入告警事件失败: %v", err)
	}

	st.firing = true
	st.eventID = ev.ID
	st.lastFired = time.UnixMilli(nowMS)

	m.deliver(ctx, sender, rule, notify.Message{
		Title:    title,
		Body:     detailBody(rule, node, value, sustained, false),
		Level:    levelOf(rule),
		NodeID:   node.ID,
		NodeURL:  m.nodeURL(node),
		Channels: rule.Channels,
	})
	log.Printf("[alert] 触发告警: node=%s rule=%s %s", node.ID, rule.ID, title)
}

// resolve 恢复：回填 resolved + 发恢复通知（不受冷却限制）。
func (m *Manager) resolve(ctx context.Context, sender notify.Notifier, rule model.AlertRule,
	node *model.Node, st *ruleState, value float64, now time.Time) {

	st.firing = false
	st.lastFired = now

	// store 未提供解析告警的方法，这里直接用 DB() 回填本包自己写入的事件，
	// 不改动 store 包。
	if id := st.eventID; id != "" {
		if _, err := m.st.DB().Exec("UPDATE alert_events SET resolved = ? WHERE id = ?",
			now.UnixMilli(), id); err != nil {
			log.Printf("[alert] 回填 resolved 失败: %v", err)
		}
		st.eventID = "" // 已闭环，避免重复回填
	}

	title := recoverText(rule, node.Name, value)
	m.deliver(ctx, sender, rule, notify.Message{
		Title:    title,
		Body:     detailBody(rule, node, value, 0, true),
		Level:    notify.LevelInfo,
		NodeID:   node.ID,
		NodeURL:  m.nodeURL(node),
		Channels: rule.Channels,
	})
	log.Printf("[alert] 告警恢复: node=%s rule=%s %s", node.ID, rule.ID, title)
}

// deliver 真正投递；通知失败只记日志，不影响状态机。
func (m *Manager) deliver(ctx context.Context, sender notify.Notifier, rule model.AlertRule, msg notify.Message) {
	ctx2, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	if err := sender.Send(ctx2, msg); err != nil {
		log.Printf("[alert] 通知发送失败: rule=%s err=%v", rule.ID, err)
	}
}

// ==================== 指标取值与文案 ====================

// compare 按 op 比较：gt 为 >，lt 为 <；未知 op 默认 gt。
func compare(op string, value, threshold float64) bool {
	if strings.EqualFold(strings.TrimSpace(op), "lt") {
		return value < threshold
	}
	return value > threshold
}

// metricValue 取规则指标在当前节点上的值。
// ok=false 表示取不到（无指标数据、无 SWAP、指标名不认识），该规则本轮跳过。
// traffic 不在这里算（需要读写流量基线表），由 check 单独处理。
func metricValue(metric string, node *model.Node, m *model.Metrics) (float64, bool) {
	switch normalizeMetric(metric) {
	case "cpu":
		if m == nil {
			return 0, false
		}
		return m.CPU.Usage, true
	case "mem":
		if m == nil || m.Mem.Total <= 0 {
			return 0, false
		}
		return pct(m.Mem.Used, m.Mem.Total), true
	case "disk":
		if m == nil || m.Disk.Total <= 0 {
			return 0, false
		}
		return pct(m.Disk.Used, m.Disk.Total), true
	case "swap":
		if m == nil || m.Mem.SwapTotal <= 0 {
			return 0, false // 没开 SWAP 就不告警
		}
		return pct(m.Mem.SwapUsed, m.Mem.SwapTotal), true
	case "load1":
		if m == nil {
			return 0, false
		}
		return m.CPU.Load1, true
	case "net_up":
		if m == nil {
			return 0, false
		}
		return mbPerSec(m.Net.Up), true
	case "net_down":
		if m == nil {
			return 0, false
		}
		return mbPerSec(m.Net.Down), true
	case "offline":
		if node.LastSeen <= 0 {
			return 0, false // 从未上报过，不告警
		}
		return float64(time.Now().UnixMilli()-node.LastSeen) / 1000, true
	}
	return 0, false
}

// normalizeMetric 归一化指标名并校验合法（含别名）。
func normalizeMetric(metric string) string {
	k := strings.ToLower(strings.TrimSpace(metric))
	if alias, ok := metricAliases[k]; ok {
		return alias
	}
	if k == "" {
		return ""
	}
	if _, ok := metricLabels[k]; !ok {
		return ""
	}
	return k
}

// ValidMetric 判断指标名是否被支持（后台校验用，会顺带归一化别名）。
func ValidMetric(metric string) (string, bool) {
	k := normalizeMetric(metric)
	return k, k != ""
}

// pct 算百分比，分母为 0 时返回 0。
func pct(used, total int64) float64 {
	if total <= 0 {
		return 0
	}
	return float64(used) / float64(total) * 100
}

// mbPerSec 把 B/s 转成 MB/s（MiB/s）。
func mbPerSec(bps int64) float64 {
	if bps < 0 {
		bps = 0
	}
	return float64(bps) / (1024 * 1024)
}

// trafficGB 算该节点本月已用流量（GB）。
// 用累计计数器差值：本月首次见到时记基线，之后用「当前 - 基线」；
// 计数器回绕（重启/重装导致清零）时把已累计的量保留下来。
func (m *Manager) trafficGB(nodeID string, latest *model.Metrics) float64 {
	if latest == nil {
		return 0
	}
	month := time.Now().Format("2006-01")
	curUp, curDown := latest.Net.TotalUp, latest.Net.TotalDown
	if curUp < 0 {
		curUp = 0
	}
	if curDown < 0 {
		curDown = 0
	}

	var baseUp, baseDown, lastUp, lastDown int64
	err := m.st.DB().QueryRow(
		`SELECT base_up, base_down, last_up, last_down FROM alert_traffic_base
WHERE node_id = ? AND month = ?`, nodeID, month).Scan(&baseUp, &baseDown, &lastUp, &lastDown)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// 本月第一次见到该节点：以当前读数作为基线，本月用量从 0 开始计。
		_, exErr := m.st.DB().Exec(
			`INSERT INTO alert_traffic_base (node_id, month, base_up, base_down, last_up, last_down)
VALUES (?,?,?,?,?,?)`, nodeID, month, curUp, curDown, curUp, curDown)
		if exErr != nil {
			log.Printf("[alert] 写入流量基线失败: %v", exErr)
		}
		return 0
	case err != nil:
		log.Printf("[alert] 读取流量基线失败: %v", err)
		return 0
	}

	if curUp < lastUp { // 上行计数器回绕
		baseUp += curUp - lastUp
	}
	if curDown < lastDown {
		baseDown += curDown - lastDown
	}
	if _, exErr := m.st.DB().Exec(
		`UPDATE alert_traffic_base SET base_up=?, base_down=?, last_up=?, last_down=?
WHERE node_id=? AND month=?`, baseUp, baseDown, curUp, curDown, nodeID, month); exErr != nil {
		log.Printf("[alert] 更新流量基线失败: %v", exErr)
	}

	used := (curUp - baseUp) + (curDown - baseDown)
	if used < 0 {
		used = 0
	}
	return float64(used) / (1024 * 1024 * 1024)
}

// levelOf 规则触发时的告警级别：离线算 critical，其余 warning。
func levelOf(rule model.AlertRule) string {
	if normalizeMetric(rule.Metric) == "offline" {
		return notify.LevelCritical
	}
	return notify.LevelWarning
}

// triggerText 触发文案。
// 例：⚠️ [Kokoro] 节点 hk-01 CPU 超过 90%（当前 95.2%，持续 5 分钟）
func triggerText(rule model.AlertRule, nodeName string, value float64, sustained time.Duration) string {
	metric := normalizeMetric(rule.Metric)
	label := metricLabels[metric]
	opText := "超过"
	if strings.EqualFold(rule.Op, "lt") {
		opText = "低于"
	}
	if metric == "offline" {
		return fmt.Sprintf("⚠️ [Kokoro] 节点 %s 离线%s %s（已离线 %s）",
			nodeName, opText, fmtSecs(int(rule.Threshold)), fmtElapsed(time.Duration(value)*time.Second))
	}
	if rule.Duration <= 0 {
		// 不要求持续时间，就不提「持续」二字。
		return fmt.Sprintf("⚠️ [Kokoro] 节点 %s %s %s %s（当前 %s）",
			nodeName, label, opText, fmtThreshold(metric, rule.Threshold), fmtValue(metric, value))
	}
	return fmt.Sprintf("⚠️ [Kokoro] 节点 %s %s %s %s（当前 %s，持续 %s）",
		nodeName, label, opText, fmtThreshold(metric, rule.Threshold),
		fmtValue(metric, value), fmtElapsed(sustained))
}

// recoverText 恢复文案。
// 例：✅ [Kokoro] 节点 hk-01 CPU 已恢复（当前 42.1%）
func recoverText(rule model.AlertRule, nodeName string, value float64) string {
	metric := normalizeMetric(rule.Metric)
	if metric == "offline" {
		return fmt.Sprintf("✅ [Kokoro] 节点 %s 已恢复在线", nodeName)
	}
	return fmt.Sprintf("✅ [Kokoro] 节点 %s %s 已恢复（当前 %s）",
		nodeName, metricLabels[metric], fmtValue(metric, value))
}

// detailBody 通知正文（多行，Telegram 会用等宽排版）。
func detailBody(rule model.AlertRule, node *model.Node, value float64, sustained time.Duration, recovered bool) string {
	metric := normalizeMetric(rule.Metric)
	label := metricLabels[metric]
	var sb strings.Builder
	fmt.Fprintf(&sb, "节点:   %s\n", node.Name)
	fmt.Fprintf(&sb, "指标:   %s\n", label)
	fmt.Fprintf(&sb, "规则:   %s %s 持续 %s\n", opSymbol(rule.Op), fmtThreshold(metric, rule.Threshold), fmtSecs(rule.Duration))
	fmt.Fprintf(&sb, "当前:   %s\n", fmtValue(metric, value))
	if recovered {
		fmt.Fprintf(&sb, "恢复:   %s\n", time.Now().Format("2006-01-02 15:04:05"))
	} else {
		fmt.Fprintf(&sb, "持续:   %s\n", fmtElapsed(sustained))
		fmt.Fprintf(&sb, "时间:   %s\n", time.Now().Format("2006-01-02 15:04:05"))
	}
	return strings.TrimRight(sb.String(), "\n")
}

// fmtValue 格式化当前值（带单位）。
func fmtValue(metric string, v float64) string {
	switch metric {
	case "cpu", "mem", "disk", "swap":
		return fmt.Sprintf("%.1f%%", v)
	case "load1":
		return fmt.Sprintf("%.2f", v)
	case "net_up", "net_down":
		return fmt.Sprintf("%.2f MB/s", v)
	case "traffic":
		return fmt.Sprintf("%.2f GB", v)
	case "offline":
		return fmtElapsed(time.Duration(v) * time.Second)
	}
	return fmt.Sprintf("%.2f", v)
}

// fmtThreshold 格式化阈值（带单位，去掉多余的 0）。
func fmtThreshold(metric string, v float64) string {
	num := strconv.FormatFloat(v, 'f', -1, 64)
	switch metric {
	case "cpu", "mem", "disk", "swap":
		return num + "%"
	case "net_up", "net_down":
		return num + " MB/s"
	case "traffic":
		return num + " GB"
	case "offline":
		return fmtSecs(int(v))
	}
	return num
}

// fmtSecs 把秒数写成「30 秒 / 5 分钟 / 2 小时」。
func fmtSecs(sec int) string {
	if sec <= 0 {
		return "立即"
	}
	return fmtElapsed(time.Duration(sec) * time.Second)
}

// fmtElapsed 把时长写成中文短句。
func fmtElapsed(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d < time.Minute {
		return fmt.Sprintf("%d 秒", int(d.Seconds()))
	}
	if d < time.Hour {
		if d%time.Minute == 0 {
			return fmt.Sprintf("%d 分钟", int(d.Minutes()))
		}
		return fmt.Sprintf("%.1f 分钟", d.Minutes())
	}
	if d%time.Hour == 0 {
		return fmt.Sprintf("%d 小时", int(d.Hours()))
	}
	return fmt.Sprintf("%.1f 小时", d.Hours())
}

// opSymbol 返回比较符文本。
func opSymbol(op string) string {
	if strings.EqualFold(strings.TrimSpace(op), "lt") {
		return "<"
	}
	return ">"
}

// nodeURL 拼节点详情页链接。
func (m *Manager) nodeURL(n *model.Node) string {
	if m.hubURL == "" {
		return ""
	}
	slug := n.Slug
	if slug == "" {
		slug = n.ID
	}
	return m.hubURL + "/n/" + slug
}

// ==================== 后台 HTTP 接口 ====================

// HandleRules 告警规则的列出 / 新增 / 删除。
//
//	GET    → {"rules":[...],"metrics":[...]}
//	POST   → 新增或更新规则（JSON 或表单）；{"action":"delete","id":"ar_xxx"} 表示删除
//	DELETE → ?id=ar_xxx 删除规则
//
// 注意：本 handler 不做鉴权，调用方必须保证只有管理员能访问。
func (m *Manager) HandleRules(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		rules, err := m.st.ListAlertRules()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "读取规则失败")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"rules": rules, "metrics": MetricDefs})

	case http.MethodPost:
		p, err := readPayload(r)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if strings.EqualFold(p.str("action"), "delete") {
			m.deleteRule(w, p.str("id"))
			return
		}
		rule, err := ruleFromPayload(p)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if rule.NodeID != "" {
			if n, err := m.st.GetNode(rule.NodeID); err != nil || n == nil {
				writeErr(w, http.StatusBadRequest, "节点不存在: "+rule.NodeID)
				return
			}
		}
		if err := m.st.SaveAlertRule(&rule); err != nil {
			log.Printf("[alert] 保存规则失败: %v", err)
			writeErr(w, http.StatusInternalServerError, "保存规则失败")
			return
		}
		_ = m.st.AddAudit("admin", "alert.rule.save", rule.ID, describeRule(rule))
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "rule": rule})

	case http.MethodDelete:
		p, _ := readPayload(r)
		id := p.str("id")
		if id == "" {
			id = r.URL.Query().Get("id")
		}
		m.deleteRule(w, id)

	default:
		writeErr(w, http.StatusMethodNotAllowed, "只支持 GET / POST / DELETE")
	}
}

// deleteRule 删除规则并清掉内存状态。
func (m *Manager) deleteRule(w http.ResponseWriter, id string) {
	id = strings.TrimSpace(id)
	if id == "" {
		writeErr(w, http.StatusBadRequest, "缺少规则 id")
		return
	}
	res, err := m.st.DB().Exec("DELETE FROM alert_rules WHERE id = ?", id)
	if err != nil {
		log.Printf("[alert] 删除规则失败: %v", err)
		writeErr(w, http.StatusInternalServerError, "删除规则失败")
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		writeErr(w, http.StatusNotFound, "规则不存在")
		return
	}
	m.mu.Lock()
	for k := range m.states {
		if strings.HasPrefix(k, id+"|") {
			delete(m.states, k)
		}
	}
	m.mu.Unlock()
	_ = m.st.AddAudit("admin", "alert.rule.delete", id, "")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// HandleTest 立即发一条测试通知（使用当前通知配置，不受免打扰限制）。
//
// 注意：本 handler 不做鉴权，调用方必须保证只有管理员能访问。
func (m *Manager) HandleTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "只支持 POST")
		return
	}
	cfg, err := notify.LoadConfig(m.st)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "读取通知配置失败")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), sendTimeout)
	defer cancel()

	if err := notify.Test(ctx, cfg); err != nil {
		log.Printf("[alert] 测试通知失败: %v", err)
		_ = m.st.AddAudit("admin", "notify.test", "", "失败: "+err.Error())
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	_ = m.st.AddAudit("admin", "notify.test", "", "已发送测试通知")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// HandleNotifyConfig 通知配置的读写与连通性测试。
//
//	GET  → {"config":{...}}（bot token 打码后返回）
//	POST → 保存配置（JSON 或表单，字段可只传部分）；{"action":"test"} 表示发测试消息
//
// token 字段若被打码（含 *）或留空，则保持原值不变。
//
// 注意：本 handler 不做鉴权，调用方必须保证只有管理员能访问。
func (m *Manager) HandleNotifyConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		cfg, err := notify.LoadConfig(m.st)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "读取通知配置失败")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"config": masked(cfg)})

	case http.MethodPost:
		p, err := readPayload(r)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		cfg, err := notify.LoadConfig(m.st)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "读取通知配置失败")
			return
		}
		changed := applyPayload(&cfg, p)
		if changed {
			if err := notify.SaveConfig(m.st, cfg); err != nil {
				log.Printf("[alert] 保存通知配置失败: %v", err)
				writeErr(w, http.StatusInternalServerError, "保存通知配置失败")
				return
			}
			_ = m.st.AddAudit("admin", "notify.config.save", "notify.config", "")
		}
		if strings.EqualFold(p.str("action"), "test") {
			ctx, cancel := context.WithTimeout(r.Context(), sendTimeout)
			defer cancel()
			if err := notify.Test(ctx, cfg); err != nil {
				log.Printf("[alert] 测试通知失败: %v", err)
				_ = m.st.AddAudit("admin", "notify.test", "", "失败: "+err.Error())
				writeJSON(w, http.StatusBadGateway, map[string]any{
					"ok": true, "saved": changed, "test_ok": false, "error": err.Error(),
				})
				return
			}
			_ = m.st.AddAudit("admin", "notify.test", "", "已发送测试通知")
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "saved": changed, "test_ok": true})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "saved": changed, "config": masked(cfg)})

	default:
		writeErr(w, http.StatusMethodNotAllowed, "只支持 GET / POST")
	}
}

// ==================== 参数解析 ====================

// payload 同时容纳 JSON 与表单提交的字段值。
type payload map[string]any

// readPayload 先按 JSON 解析，Content-Type 不是 JSON 时退回表单，
// 这样后台页面用 fetch 或原生表单提交都能工作。
func readPayload(r *http.Request) (payload, error) {
	p := payload{}
	if r.Body == nil {
		return p, nil
	}
	if strings.Contains(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		b, err := io.ReadAll(io.LimitReader(r.Body, maxBody))
		if err != nil {
			return p, err
		}
		if len(bytes.TrimSpace(b)) == 0 {
			return p, nil
		}
		if err := json.Unmarshal(b, &p); err != nil {
			return p, fmt.Errorf("请求体不是合法 JSON")
		}
		return p, nil
	}
	if err := r.ParseForm(); err != nil {
		return p, err
	}
	for k, v := range r.Form {
		if len(v) > 0 {
			p[k] = v[0]
		}
	}
	return p, nil
}

func (p payload) has(k string) bool { _, ok := p[k]; return ok }

func (p payload) str(k string) string {
	switch v := p[k].(type) {
	case nil:
		return ""
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(v)
	default:
		return fmt.Sprint(v)
	}
}

func (p payload) num(k string) float64 {
	switch v := p[k].(type) {
	case float64:
		return v
	case bool:
		if v {
			return 1
		}
	case string:
		if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
			return f
		}
	}
	return 0
}

func (p payload) int(k string) int { return int(p.num(k)) }

func (p payload) bool(k string) bool {
	switch v := p[k].(type) {
	case bool:
		return v
	case float64:
		return v != 0
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "1", "true", "on", "yes":
			return true
		}
	}
	return false
}

// ruleFromPayload 从请求参数构造并校验一条规则。
func ruleFromPayload(p payload) (model.AlertRule, error) {
	var r model.AlertRule
	r.ID = strings.TrimSpace(p.str("id"))
	r.NodeID = strings.TrimSpace(p.str("node_id"))

	metric, ok := ValidMetric(p.str("metric"))
	if !ok {
		return r, fmt.Errorf("不支持的指标: %s（可用: %s）", p.str("metric"), strings.Join(metricKeys(), " "))
	}
	r.Metric = metric

	r.Op = strings.ToLower(strings.TrimSpace(p.str("op")))
	if r.Op == "" {
		r.Op = "gt"
	}
	if r.Op != "gt" && r.Op != "lt" {
		return r, fmt.Errorf("op 只能是 gt 或 lt")
	}
	if !p.has("threshold") {
		return r, fmt.Errorf("缺少 threshold")
	}
	r.Threshold = p.num("threshold")
	if r.Threshold <= 0 {
		return r, fmt.Errorf("threshold 必须大于 0")
	}
	if p.has("duration") {
		r.Duration = p.int("duration")
	}
	if r.Duration < 0 {
		return r, fmt.Errorf("duration 不能为负")
	}
	r.Silenced = p.bool("silenced")
	if raw := strings.TrimSpace(p.str("channels")); raw != "" {
		for _, c := range strings.Split(raw, ",") {
			c = strings.ToLower(strings.TrimSpace(c))
			if c == "" {
				continue
			}
			if c != notify.ChannelTelegram && c != notify.ChannelWebhook {
				return r, fmt.Errorf("不支持的渠道: %s", c)
			}
			r.Channels = append(r.Channels, c)
		}
	}
	return r, nil
}

// applyPayload 把请求里出现的字段覆盖到配置上，返回是否有改动。
// bot token 被打码（含 *）或留空时保持原值，避免前端回写把真 token 冲掉。
func applyPayload(cfg *notify.Config, p payload) bool {
	changed := false
	if p.has("telegram_enabled") {
		cfg.TelegramEnabled = p.bool("telegram_enabled")
		changed = true
	}
	if p.has("telegram_bot_token") {
		tok := strings.TrimSpace(p.str("telegram_bot_token"))
		if tok != "" && !strings.Contains(tok, "*") {
			cfg.TelegramBotToken = tok
			changed = true
		}
	}
	if p.has("telegram_chat_id") {
		cfg.TelegramChatID = strings.TrimSpace(p.str("telegram_chat_id"))
		changed = true
	}
	if p.has("telegram_silent") {
		cfg.TelegramSilent = p.bool("telegram_silent")
		changed = true
	}
	if p.has("webhook_enabled") {
		cfg.WebhookEnabled = p.bool("webhook_enabled")
		changed = true
	}
	if p.has("webhook_url") {
		cfg.WebhookURL = strings.TrimSpace(p.str("webhook_url"))
		changed = true
	}
	if p.has("quiet_hours_start") {
		cfg.QuietHoursStart = clampHour(p.int("quiet_hours_start"))
		changed = true
	}
	if p.has("quiet_hours_end") {
		cfg.QuietHoursEnd = clampHour(p.int("quiet_hours_end"))
		changed = true
	}
	return changed
}

// clampHour 把小时数收敛到 -1..23。
func clampHour(h int) int {
	if h < -1 {
		return -1
	}
	if h > 23 {
		return 23
	}
	return h
}

// masked 返回打码后的配置副本（不泄露 bot token）。
func masked(cfg notify.Config) map[string]any {
	tok := notify.MaskToken(cfg.TelegramBotToken)
	return map[string]any{
		"telegram_enabled":   cfg.TelegramEnabled,
		"telegram_bot_token": tok,
		"telegram_chat_id":   cfg.TelegramChatID,
		"telegram_silent":    cfg.TelegramSilent,
		"webhook_enabled":    cfg.WebhookEnabled,
		"webhook_url":        cfg.WebhookURL,
		"quiet_hours_start":  cfg.QuietHoursStart,
		"quiet_hours_end":    cfg.QuietHoursEnd,
		"has_token":          cfg.TelegramBotToken != "",
	}
}

// describeRule 生成审计用的规则摘要（不含节点名等隐私，只列条件）。
func describeRule(r model.AlertRule) string {
	scope := "全局"
	if r.NodeID != "" {
		scope = r.NodeID
	}
	return fmt.Sprintf("%s %s %s %s 持续 %s", scope, r.Metric, opSymbol(r.Op),
		fmtThreshold(normalizeMetric(r.Metric), r.Threshold), fmtSecs(r.Duration))
}

// metricKeys 返回全部指标 key，用于报错信息。
func metricKeys() []string {
	out := make([]string, 0, len(MetricDefs))
	for _, d := range MetricDefs {
		out = append(out, d.Key)
	}
	return out
}

// ==================== HTTP 工具 ====================

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("[alert] 写响应失败: %v", err)
	}
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}
