package hub

import (
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/kokoro-probe/kokoro/internal/collector"
	"github.com/kokoro-probe/kokoro/internal/model"
)

// 本文件是「主机仪表盘」页：一个总览看板，只读渲染，不做鉴权（访客可见）。
// 页面里不出现任何节点令牌或 IP，只统计公开节点。
//
// 设计原则与 web.go 一致：任何一块数据取不到就记日志并跳过，
// 缺一块好过整页 500。

// 仪表盘用到的时间窗口与分桶长度（毫秒）。
const (
	dashBucket1m  = int64(60 * 1000)
	dashBucket5m  = int64(5 * 60 * 1000)
	dashWindow1h  = int64(60 * 60 * 1000)
	dashWindow24h = int64(24 * 60 * 60 * 1000)
)

// ---- 页面数据 ----

type dashboardData struct {
	Theme    themeView
	SiteName string
	Year     int

	Total   int // 节点总数
	Online  int // 在线
	Offline int // 离线

	AlertRules  int // 生效中的告警规则数（未静默）
	AlertFiring int // 未恢复的告警事件数

	CPUCores  int   // CPU 核数合计
	MemTotal  int64 // 内存总量合计（字节）
	DiskTotal int64 // 磁盘总量合计（字节）

	NetUp   int64 // 在线节点实时上行合计（B/s）
	NetDown int64 // 在线节点实时下行合计（B/s）

	TrafficUp   int64 // 最近 24 小时上行增量（字节）
	TrafficDown int64 // 最近 24 小时下行增量（字节）
	TrafficOK   bool  // false 表示这段时间内没有可算的数据

	Points   []dashPoint // 全网最近一小时的上下行曲线
	PeakUp   int64       // 曲线上的上行峰值（B/s）
	PeakDown int64       // 曲线上的下行峰值（B/s）

	TopLoad []dashTopNode // 负载最高的几台小鸡
	Events  []dashEvent   // 最近告警事件

	Host        *model.Metrics // 主机自身（Hub 所在机器）现采一次
	HostMemPct  float64
	HostDiskPct float64
	HostNote    string // 采不到时的说明（平台不支持 / 采集失败）
}

// dashPoint 是全网曲线上的一点：某个分钟桶内各节点速率之和。
type dashPoint struct {
	Ts   int64
	Up   int64
	Down int64
}

type dashTopNode struct {
	Name   string
	Slug   string
	CPUPct float64
	MemPct float64
	Load1  float64
}

type dashEvent struct {
	ID       string
	NodeID   string
	NodeName string
	Message  string
	FiredAt  int64
	Resolved bool
}

// ---- 页面 ----

// handleDashboard 渲染主机仪表盘。路由由外部注册（/dashboard）。
func (h *Hub) handleDashboard(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "方法不允许", http.StatusMethodNotAllowed)
		return
	}

	data := dashboardData{SiteName: h.cfg.SiteName, Year: time.Now().Year()}

	// 仪表盘对访客可见，所以只统计公开节点，隐藏机器不出现在任何一块里。
	nodes, err := h.store.ListNodes(false)
	if err != nil {
		log.Printf("[hub] 仪表盘读取节点失败: %v", err)
	}
	snap, err := h.store.LatestSnapshot()
	if err != nil {
		log.Printf("[hub] 仪表盘读取最新指标失败: %v", err)
	}

	ids := make([]string, 0, len(nodes))
	data.Total = len(nodes)
	for _, n := range nodes {
		ids = append(ids, n.ID)
		if n.Online {
			data.Online++
		}
		data.CPUCores += n.CPUCores

		// 容量取上报值，取不到才退回注册时记的静态值。
		mem, disk := n.MemTotal, n.DiskTotal
		if m := snap[n.ID]; m != nil {
			if m.Mem.Total > 0 {
				mem = m.Mem.Total
			}
			if m.Disk.Total > 0 {
				disk = m.Disk.Total
			}
			if n.Online {
				data.NetUp += m.Net.Up
				data.NetDown += m.Net.Down
			}
		}
		data.MemTotal += mem
		data.DiskTotal += disk
	}
	data.Offline = data.Total - data.Online

	if rules, err := h.store.ListAlertRules(); err != nil {
		log.Printf("[hub] 仪表盘读取告警规则失败: %v", err)
	} else {
		for _, r := range rules {
			if !r.Silenced {
				data.AlertRules++
			}
		}
	}
	if err := h.store.DB().QueryRow("SELECT COUNT(*) FROM alert_events WHERE resolved = 0").
		Scan(&data.AlertFiring); err != nil {
		log.Printf("[hub] 仪表盘统计未恢复告警失败: %v", err)
	}

	data.TrafficUp, data.TrafficDown, data.TrafficOK = h.dashTraffic24h(ids)

	data.Points = h.dashNetPoints(ids)
	for _, p := range data.Points {
		if p.Up > data.PeakUp {
			data.PeakUp = p.Up
		}
		if p.Down > data.PeakDown {
			data.PeakDown = p.Down
		}
	}

	data.TopLoad = h.dashTopLoad(nodes, snap)
	data.Events = h.dashAlertEvents(ids)

	data.Host, data.HostNote = h.collectHostSelf()
	if data.Host != nil {
		data.HostMemPct = Pct(float64(data.Host.Mem.Used), float64(data.Host.Mem.Total))
		data.HostDiskPct = Pct(float64(data.Host.Disk.Used), float64(data.Host.Disk.Total))
	}

	h.render(w, "dashboard.html", &data, r)
}

// ---- 各块数据 ----

// dashTopLoad 按 1 分钟负载挑出最忙的几台小鸡。没有上报数据的节点不参与排序。
func (h *Hub) dashTopLoad(nodes []model.Node, snap map[string]*model.Metrics) []dashTopNode {
	type row struct {
		id   string
		load float64
		cpu  float64
		mem  float64
	}
	rows := make([]row, 0, len(nodes))
	for _, n := range nodes {
		m := snap[n.ID]
		if m == nil {
			continue
		}
		rows = append(rows, row{
			id:   n.ID,
			load: m.CPU.Load1,
			cpu:  m.CPU.Usage,
			mem:  Pct(float64(m.Mem.Used), float64(m.Mem.Total)),
		})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].load != rows[j].load {
			return rows[i].load > rows[j].load
		}
		return rows[i].cpu > rows[j].cpu
	})

	nameOf := make(map[string]string, len(nodes))
	for _, n := range nodes {
		nameOf[n.ID] = n.Name
	}
	out := make([]dashTopNode, 0, 5)
	for i, r := range rows {
		if i >= 5 {
			break
		}
		out = append(out, dashTopNode{
			Name:   nameOf[r.id],
			Slug:   slugOfNode(nodes, r.id),
			CPUPct: r.cpu,
			MemPct: r.mem,
			Load1:  r.load,
		})
	}
	return out
}

// slugOfNode 按 ID 找节点的 slug，找不到返回空串（模板里就是个不跳转的名字）。
func slugOfNode(nodes []model.Node, id string) string {
	for _, n := range nodes {
		if n.ID == id {
			return n.Slug
		}
	}
	return ""
}

// dashTraffic24h 统计最近 24 小时全网的上下行流量增量（字节）。
//
// total_up / total_down 是网卡累计值，这里按相邻分桶的增量求和：
// 节点重启后计数器归零，该段增量为负，按 0 处理（宁可少算，也不算成负数）。
func (h *Hub) dashTraffic24h(ids []string) (up, down int64, ok bool) {
	if len(ids) == 0 {
		return 0, 0, false
	}
	from := time.Now().UnixMilli() - dashWindow24h
	ph := dashPlaceholders(len(ids))
	args := make([]any, 0, len(ids)+1)
	args = append(args, from)
	for _, id := range ids {
		args = append(args, id)
	}

	// 优先读 5 分钟聚合表：一天只有 288 个桶/节点，扫起来几乎不花钱。
	q := `SELECT node_id, bucket, total_up_max, total_down_max FROM metrics_5m
WHERE bucket >= ? AND node_id IN (` + ph + `) ORDER BY node_id, bucket`
	rows, err := h.store.DB().Query(q, args...)
	if err != nil {
		log.Printf("[hub] 仪表盘读取 5 分钟聚合失败: %v", err)
	} else {
		up, down, ok = sumCounterDelta(rows)
		rows.Close()
		if ok {
			return up, down, true
		}
	}

	// 聚合表还没数据时退回原始表，现按 5 分钟分桶（同样最多 288 个桶/节点）。
	q = fmt.Sprintf(`SELECT node_id, (ts / %d) * %d, MAX(total_up), MAX(total_down)
FROM metrics_raw WHERE ts >= ? AND node_id IN (%s)
GROUP BY node_id, (ts / %d) * %d ORDER BY node_id, 2`,
		dashBucket5m, dashBucket5m, ph, dashBucket5m, dashBucket5m)
	rows, err = h.store.DB().Query(q, args...)
	if err != nil {
		log.Printf("[hub] 仪表盘统计 24 小时流量失败: %v", err)
		return 0, 0, false
	}
	defer rows.Close()
	return sumCounterDelta(rows)
}

// sumCounterDelta 把「node_id, 分桶, 累计上行, 累计下行」的行按节点累加正增量。
func sumCounterDelta(rows *sql.Rows) (up, down int64, ok bool) {
	type acc struct{ up, down int64 }
	last := make(map[string]acc)
	for rows.Next() {
		var (
			node  string
			bk    int64
			upV   int64
			downV int64
		)
		if err := rows.Scan(&node, &bk, &upV, &downV); err != nil {
			log.Printf("[hub] 仪表盘扫描流量分桶失败: %v", err)
			return up, down, len(last) > 0
		}
		_ = bk // 只用来排序，值本身不参与计算
		if p, seen := last[node]; seen {
			if upV > p.up {
				up += upV - p.up
			}
			if downV > p.down {
				down += downV - p.down
			}
		}
		last[node] = acc{up: upV, down: downV}
	}
	if err := rows.Err(); err != nil {
		log.Printf("[hub] 仪表盘遍历流量分桶失败: %v", err)
	}
	return up, down, len(last) > 0
}

// dashNetPoints 取全网最近一小时的上下行曲线：按 1 分钟分桶，
// 桶内先对每个节点求平均速率，再把各节点相加，得到「全网这一分钟的合计速率」。
// 结果最多 60 个点。
func (h *Hub) dashNetPoints(ids []string) []dashPoint {
	if len(ids) == 0 {
		return nil
	}
	from := time.Now().UnixMilli() - dashWindow1h
	ph := dashPlaceholders(len(ids))
	args := make([]any, 0, len(ids)+1)
	args = append(args, from)
	for _, id := range ids {
		args = append(args, id)
	}

	q := fmt.Sprintf(`SELECT (ts / %d) * %d AS b, AVG(net_up), AVG(net_down)
FROM metrics_raw WHERE ts >= ? AND node_id IN (%s)
GROUP BY b, node_id ORDER BY b`, dashBucket1m, dashBucket1m, ph)
	rows, err := h.store.DB().Query(q, args...)
	if err != nil {
		log.Printf("[hub] 仪表盘读取最近一小时曲线失败: %v", err)
		return nil
	}
	defer rows.Close()

	sums := make(map[int64]*[2]int64)
	var buckets []int64
	for rows.Next() {
		var (
			bk       int64
			up, down float64
		)
		if err := rows.Scan(&bk, &up, &down); err != nil {
			log.Printf("[hub] 仪表盘扫描曲线分桶失败: %v", err)
			return nil
		}
		s, seen := sums[bk]
		if !seen {
			s = &[2]int64{}
			sums[bk] = s
			buckets = append(buckets, bk)
		}
		s[0] += int64(up + 0.5)
		s[1] += int64(down + 0.5)
	}
	if err := rows.Err(); err != nil {
		log.Printf("[hub] 仪表盘遍历曲线分桶失败: %v", err)
	}

	// ORDER BY b 保证 buckets 是升序的，直接按顺序输出即可。
	out := make([]dashPoint, 0, len(buckets))
	for _, bk := range buckets {
		s := sums[bk]
		out = append(out, dashPoint{Ts: bk, Up: s[0], Down: s[1]})
	}
	return out
}

// dashAlertEvents 读最近 10 条告警事件；非公开节点的事件跳过，避免泄露隐藏机器。
func (h *Hub) dashAlertEvents(ids []string) []dashEvent {
	const limit = 10
	q := `SELECT e.id, e.node_id, e.message, e.fired_at, e.resolved, COALESCE(n.name, '')
FROM alert_events e LEFT JOIN nodes n ON n.id = e.node_id
ORDER BY e.fired_at DESC LIMIT ?`
	rows, err := h.store.DB().Query(q, limit*2)
	if err != nil {
		log.Printf("[hub] 仪表盘读取告警事件失败: %v", err)
		return nil
	}
	defer rows.Close()

	visible := make(map[string]bool, len(ids))
	for _, id := range ids {
		visible[id] = true
	}
	var out []dashEvent
	for rows.Next() && len(out) < limit {
		var (
			e        dashEvent
			resolved int
		)
		if err := rows.Scan(&e.ID, &e.NodeID, &e.Message, &e.FiredAt, &resolved, &e.NodeName); err != nil {
			log.Printf("[hub] 仪表盘扫描告警事件失败: %v", err)
			break
		}
		e.Resolved = resolved != 0
		if e.NodeID != "" && !visible[e.NodeID] {
			continue
		}
		out = append(out, e)
	}
	return out
}

// collectHostSelf 现采一次主机（Hub 进程所在机器）的资源占用。
// 只有 Linux 支持；平台不支持或采集失败时返回说明文案，页面显示占位而不是报错。
func (h *Hub) collectHostSelf() (*model.Metrics, string) {
	if runtime.GOOS != "linux" {
		return nil, "当前平台不支持（主机资源采集仅支持 Linux）"
	}
	c, err := collector.New()
	if err != nil {
		log.Printf("[hub] 仪表盘创建采集器失败: %v", err)
		return nil, "当前平台不支持"
	}
	defer c.Close()

	// CPU 使用率靠两次采样的差分得出，先采一次当基线，间隔片刻再采一次才是真实占用。
	if _, err := c.Collect(); err != nil {
		log.Printf("[hub] 仪表盘主机基线采集失败: %v", err)
	}
	time.Sleep(300 * time.Millisecond)
	m, err := c.Collect()
	if err != nil {
		log.Printf("[hub] 仪表盘主机采集失败: %v", err)
		return nil, "采集失败（Hub 可能不是 root，部分指标读不到）"
	}
	return m, ""
}

// dashPlaceholders 生成 n 个 ? 占位符，用于 node_id IN (...) 这类查询。
func dashPlaceholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}
