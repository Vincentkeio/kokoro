package hub

// 网络质量（三网分省探测）在 Hub 侧的两件事：
//   1. 收 agent 上报的探测结果（/api/v1/netq）；
//   2. 把最近一轮结果聚合成页面要用的 NetQSummary。
//
// 数据是"一轮一轮"的：每个 ts 是一次完整扫描，页面只关心最近一轮。
// 历史最多留 30 天，供详情页切换最长 30 天的分省波形。

import (
	"bytes"
	"fmt"
	"html/template"
	"log"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/Vincentkeio/kokoro/internal/netprobe"
	"github.com/Vincentkeio/kokoro/internal/store"
)

// netqKeep 网络质量数据的保留时长。

const netqKeep = 30 * 24 * time.Hour

// netqMaxResults 单次上报的条数上限，防止畸形请求把库写爆。
const netqMaxResults = 2000

// ispLabels 把 netprobe 的 ISP 代码翻成中文。
var ispLabels = map[string]string{
	netprobe.ISPTelecom: "电信",
	netprobe.ISPUnicom:  "联通",
	netprobe.ISPMobile:  "移动",
	netprobe.ISPHK:      "中国香港",
	netprobe.ISPTW:      "中国台湾",
	netprobe.ISPJP:      "日本",
	netprobe.ISPUS:      "美国",
	netprobe.ISPEU:      "欧洲",
}

// ---- 页面数据结构 ----

// NetQCell 是某个省某个运营商的一格数据。
type NetQCell struct {
	HasData  bool
	OK       bool
	Latency  float64
	Jitter   float64
	Loss     float64
	Name     string
	WaveLine string // 最近 N 轮延迟的波动路径（空 = 历史不够画）
	WaveArea string
}

// NetQRow 是明细表的一行：一个省 × 三个运营商。
type NetQRow struct {
	Province string
	Telecom  *NetQCell
	Unicom   *NetQCell
	Mobile   *NetQCell
	Avg      float64
	OKN      int
}

// NetQIspStat 是按运营商聚合出来的卡片。
type NetQIspStat struct {
	Label   string
	ISP     string
	Total   int
	OKN     int
	Latency float64
	Jitter  float64
	Loss    float64
}

// NetQSummary 是详情页「网络质量」区块的全部数据。
type NetQSummary struct {
	Ts         int64
	Mode       string
	Total      int
	OKN        int
	Provinces  int
	Stale      bool // 超过 6 小时没更新，页面上要提示
	ISPs       []NetQIspStat
	Rows       []NetQRow
	DetailRows []NetQRow // 省份明细表不重复列出上海固定基准
	Overseas   []NetQRow // 港澳台与境外，单独列，不混进三网统计
	Best       []NetQRow
	Worst      []NetQRow
	WaveRange  string
	WaveRanges []NetQWaveRange
}

type NetQWaveRange struct{ Key, Label string }

var netQWaveRanges = []NetQWaveRange{
	{Key: "1h", Label: "1 小时"}, {Key: "6h", Label: "6 小时"},
	{Key: "1d", Label: "1 天"}, {Key: "7d", Label: "7 天"}, {Key: "30d", Label: "30 天"},
}

// ModeLabel 返回探测模式的中文说明。
func (s *NetQSummary) ModeLabel() string {
	switch s.Mode {
	case netprobe.ModeICMP:
		return "ICMP"
	case netprobe.ModeTCP:
		return "TCP 降级（无 raw socket 权限）"
	case "":
		return "未知"
	}
	return s.Mode
}

// Lifespan 返回本轮数据的"年龄"，用于页面提示。
func (s *NetQSummary) Age() string {
	if s.Ts <= 0 {
		return "未知"
	}
	d := time.Since(time.UnixMilli(s.Ts))
	switch {
	case d < time.Minute:
		return "刚刚"
	case d < time.Hour:
		return fmt.Sprintf("%d 分钟前", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d 小时前", int(d.Hours()))
	}
	return fmt.Sprintf("%d 天前", int(d.Hours()/24))
}

// ---- 上报 ----

// handleNetQ 接收 agent 的三网探测结果。
// 鉴权与 /api/v1/report 一致：Bearer 节点令牌；节点身份以令牌为准，不信 body。
func (h *Hub) handleNetQ(w http.ResponseWriter, r *http.Request) {
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

	var p netprobe.ResultsPayload
	if err := readJSON(r, &p); err != nil {
		writeErr(w, http.StatusBadRequest, "探测结果解析失败")
		return
	}
	if len(p.Results) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "stored": 0})
		return
	}
	if len(p.Results) > netqMaxResults {
		writeErr(w, http.StatusRequestEntityTooLarge,
			fmt.Sprintf("一次最多 %d 条，收到 %d 条", netqMaxResults, len(p.Results)))
		return
	}

	now := time.Now().UnixMilli()
	ts := p.Ts
	// 时间戳必须落在最近一小时里，否则用服务器时间兜底——
	// 小主机时钟漂移很常见，不能因此丢掉整轮数据。
	if ts <= 0 || ts > now || now-ts > 3600_000 {
		ts = now
	}
	mode := p.Mode
	if mode != netprobe.ModeICMP && mode != netprobe.ModeTCP {
		mode = "unknown"
	}

	type row struct {
		province, isp, name, host, ipVersion string
		ok                        bool
		latency, jitter, loss     float64
	}
	rows := make([]row, 0, len(p.Results))
	for _, res := range p.Results {
		host := strings.TrimSpace(res.Target.Host)
		province := strings.TrimSpace(res.Target.Province)
		isp := strings.TrimSpace(res.Target.ISP)
		if host == "" || province == "" || isp == "" {
			continue // 缺关键字段的脏数据直接丢
		}
		lat, jit, los := res.LatencyMS, res.JitterMS, res.LossPct
		if !finite(lat) || !finite(jit) || !finite(los) {
			continue
		}
		// Probe 有整轮时限。队列里还没开始的目标会以 context deadline
		// 返回；它们不是“目标不通”，而是本轮没有实际探测，不能写成失败。
		probeErr := strings.ToLower(strings.TrimSpace(res.Error))
		if !res.OK && (probeErr == "context deadline exceeded" || probeErr == "context canceled") {
			continue
		}
		if lat < 0 || lat > 60000 {
			lat = 0
		}
		if jit < 0 || jit > 60000 {
			jit = 0
		}
		if los < 0 {
			los = 0
		}
		// 探测失败的早退分支（例如 DNS 解析失败或 TCP 备用端口全部
		// 拒绝）可能没有填 LossPct；失败本身代表该目标本轮 100% 丢包，
		// 不能存成 0% 再显示成“无响应但零丢包”。
		if !res.OK && los == 0 {
			los = 100
		}
		if los > 100 {
			los = 100
		}
		rows = append(rows, row{
			province: province, isp: isp, name: res.Target.Name, host: host,
			ipVersion: strings.ToLower(strings.TrimSpace(res.Target.IPVersion)),
			ok: res.OK, latency: lat, jitter: jit, loss: los,
		})
	}
	if len(rows) == 0 {
		writeErr(w, http.StatusBadRequest, "没有可入库的有效结果")
		return
	}

	db := h.store.DB()
	tx, err := db.Begin()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "写入失败")
		return
	}
	stmt, err := tx.Prepare(`INSERT INTO net_quality
(node_id, ts, mode, ip_version, province, isp, city, host, ok, latency, jitter, loss)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		_ = tx.Rollback()
		log.Printf("[hub] 预编译网络质量写入失败: %v", err)
		writeErr(w, http.StatusInternalServerError, "写入失败")
		return
	}
	for _, x := range rows {
		if x.ipVersion != "v6" {
			x.ipVersion = "v4"
		}
		okVal := 0
		if x.ok {
			okVal = 1
		}
		// city 列目前留空：探测目标表里没有城市字段，保留列位以备后续细化。
		if _, err := stmt.Exec(node.ID, ts, mode, x.ipVersion, x.province, x.isp, "", x.host,
			okVal, x.latency, x.jitter, x.loss); err != nil {
			stmt.Close()
			_ = tx.Rollback()
			log.Printf("[hub] 写入网络质量失败 %s: %v", node.ID, err)
			writeErr(w, http.StatusInternalServerError, "写入失败")
			return
		}
	}
	stmt.Close()
	// 顺手清理过期数据（同一事务，省一次往返）
	if _, err := tx.Exec("DELETE FROM net_quality WHERE node_id = ? AND ts < ?",
		node.ID, now-netqKeep.Milliseconds()); err != nil {
		log.Printf("[hub] 清理网络质量旧数据失败 %s: %v", node.ID, err)
	}
	if err := tx.Commit(); err != nil {
		log.Printf("[hub] 提交网络质量失败 %s: %v", node.ID, err)
		writeErr(w, http.StatusInternalServerError, "写入失败")
		return
	}

	h.broadcastNetQ(node.ID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "stored": len(rows), "ts": ts, "mode": mode})
}

// broadcastNetQ 探测结果入库后立刻广播一条 netq 事件。
//
// 为什么不做成等 2 秒的指标上报：探测是几十秒~几分钟一轮的独立数据，
// 混在 metrics 里推会把它误当成"2 秒前的快照"；单独一条事件，前端
// 只换三网相关的那两块 DOM（首页卡片行 + 详情页区块），别的元素
// 完全不动（boss 2026-10-12："主面板卡片每秒都要刷新"——数据一到
// 就推，开着页面也看得到，不用手刷）。
//
// payload 带**服务端渲染好的 HTML 片段**而不是结构化数据：前端拿到
// 直接 outerHTML 替换，和 SSR 首屏共用同一套模板，不会出现
// "首屏一种画法、刷新后另一种画法"。
func (h *Hub) broadcastNetQ(nodeID string) {
	if h.broker == nil {
		return
	}
	payload := map[string]any{"node": nodeID}
	if netq, err := h.store.NetQByNode(); err == nil {
		node, _ := h.store.GetNode(nodeID)
		family := "v4"
		if node != nil {
			family = node.NetQIPVersion
		}
		cells := net3Cells(netq[nodeID], family)
		if len(cells) > 0 {
			payload["rows"] = h.renderPartial("netq_rows.html", map[string]any{
				"ID": nodeID, "Net3": cells,
			})
		}
	}
	if sum := h.LoadNetQ(nodeID); sum != nil {
		payload["detail"] = h.renderPartial("netq.html", sum)
	}
	if len(payload) < 3 {
		return // 既没卡片数据又没详情数据（全不选的空轮），没什么可推
	}
	h.broker.broadcast("netq", payload)
}

// renderPartial 渲染一个 <script> 之外的模板片段（SSE 局部更新用）。
func (h *Hub) renderPartial(name string, data any) template.HTML {
	var buf bytes.Buffer
	if err := h.tmpl.ExecuteTemplate(&buf, name, data); err != nil {
		log.Printf("[hub] 渲染片段 %s 失败: %v", name, err)
		return ""
	}
	return template.HTML(buf.String())
}

func finite(f float64) bool {
	return !math.IsNaN(f) && !math.IsInf(f, 0)
}

// ---- 聚合 ----

// LoadNetQ 读某台小鸡每个省份/运营商格子最近一次实际上报的探测序列，
// 再聚合成页面数据。序列内可能有多个候选地址；只在同一轮内合并候选，
// 不跨轮做“最近几次有一次成功就算通”的平滑。
// 没有数据返回 nil（模板会跳过整个区块）。
func (h *Hub) LoadNetQ(nodeID string) *NetQSummary { return h.LoadNetQRange(nodeID, "1h") }

func (h *Hub) LoadNetQRange(nodeID, waveRange string) *NetQSummary {
	if nodeID == "" {
		return nil
	}
	win := netQWaveWindowFor(waveRange)
	db := h.store.DB()

	var ts int64
	var mode string
	err := db.QueryRow(
		`SELECT ts, mode FROM net_quality WHERE node_id = ? AND ip_version =
         (SELECT netq_ip_version FROM nodes WHERE id = ?) ORDER BY ts DESC LIMIT 1`,
		nodeID, nodeID).Scan(&ts, &mode)
	if err != nil {
		return nil
	}
	now := time.Now().UnixMilli()
	_ = ts // ts 只用于"有没有数据"的判断；Age 用聚合窗口里最新轮的 ts

	// 行的查询在下面的「窗口聚合」里（多轮一起读），这里不预查。

	sum := &NetQSummary{Ts: ts, Mode: mode, WaveRange: win.key, WaveRanges: netQWaveRanges}
	byProvince := make(map[string]*NetQRow, 40)
	byISP := make(map[string]*NetQIspStat, 8)
	overseas := make(map[string]*NetQRow, 16)

	// 只展示后台选中的范围（省内 + 境外地区）。
	//
	// 为什么在展示层过滤而不是只靠 agent 收敛：agent 收到范围命令才切，
	// 但旧版 agent 不认 netq_targets 命令（2026-10-12 boss 撞见：
	// 后台存了 4 个点，详情还是 31 省）。Hub 侧过滤是兜底 —— 后台选了
	// 几个，页面就只呈现几个，口径（ISP 卡片、覆盖数、最好/最差）
	// 也全都按选中的算。agent 更新后上报收缩，两边自然对齐。
	selected := make(map[string]bool, 8)
	domesticSelected := make(map[string]bool, 8)
	for _, p := range h.netqProvinces() {
		selected[p] = true
		for _, target := range netprobe.Filter([]string{p}, nil) {
			if isISP3(target.ISP) {
				domesticSelected[p] = true
				break
			}
		}
	}
	// selected 为空 = 站长显式「全不选」（分省探测整体关闭）：
	// 一行都不展示。解析失败时 netqProvinces 会退回默认值，到不了这里。

	// 分省波动图的历史：每个 (province, isp) 最近 N 轮延迟。
	// 和卡片三网同一张脸（netqWavePaths），一格一条小波，趋势一眼见。
	nodeWave, _ := h.store.NetQWaveForNode(nodeID, now-win.duration.Milliseconds(), now, win.buckets)

	// 先按探测轮次聚合候选地址，再为每个省份 × 运营商取最近一轮。
	// 不能直接给原始行 ROW_NUMBER：同一轮内多个候选共用 ts，排名会
	// 任意挑中某一个地址，造成成功/失败随查询计划变化。某格本轮没有
	// 实际结果（例如整轮超时被上报端跳过）时，rn=1 自然落到它自己的
	// 上一轮；其他省份/运营商的轮次不会覆盖它。
	const cellHistoryAge = netqKeep
	rows, err := db.Query(`WITH ranked AS (
		SELECT ts, province, isp, host, ok, latency, jitter, loss,
			ROW_NUMBER() OVER (PARTITION BY province, isp ORDER BY ts DESC) AS rn
		FROM (
			SELECT ts, province, isp,
				COALESCE(MAX(CASE WHEN ok = 1 THEN host END), MAX(host), '') AS host,
				MAX(ok) AS ok,
				COALESCE(AVG(CASE WHEN ok = 1 THEN latency END), 0) AS latency,
				COALESCE(AVG(CASE WHEN ok = 1 THEN jitter END), 0) AS jitter,
				AVG(loss) AS loss
			FROM net_quality
			WHERE node_id = ? AND ts >= ? AND ip_version =
              (SELECT netq_ip_version FROM nodes WHERE id = ?)
			GROUP BY ts, province, isp
		) AS rounds
	)
	SELECT ts, province, isp, host, ok, latency, jitter, loss
	FROM ranked WHERE rn = 1 ORDER BY ts DESC`, nodeID,
		now-cellHistoryAge.Milliseconds(), nodeID)
	if err != nil {
		log.Printf("[hub] 读取网络质量失败 %s: %v", nodeID, err)
		return nil
	}
	defer rows.Close()

	// key = 省/地区 + ISP。查询已折叠为该格最近一个实际序列。
	type cellKey struct{ prov, isp string }
	type aggCell struct {
		n, okn        int
		lat, jit, los float64
		host          string
	}
	aggs := make(map[cellKey]*aggCell, 60)

	for rows.Next() {
		var (
			rts                 int64
			province, isp, host string
			okInt               int
			lat, jit, los       float64
		)
		if err := rows.Scan(&rts, &province, &isp, &host, &okInt, &lat, &jit, &los); err != nil {
			log.Printf("[hub] 解析网络质量行失败: %v", err)
			return nil
		}
		// 范围外的一行整行跳过（国内按省名、境外按地区名）。跳在统计
		// 之前，后面的覆盖数、ISP 卡片、最好/最差就全都只按选中的算
		// —— 和 agent 收敛后的口径一致。
		// 境外行 province 入库时就是地区名（见 overseasTargets）；老数据
		// province 为空时用 ispLabel 兜底，两条都匹配不上才跳过。
		if isISP3(isp) {
			if !selected[province] {
				continue
			}
		} else if !selected[province] && !selected[ispLabel(isp)] {
			continue
		}
		k := cellKey{province, isp}
		c := aggs[k]
		if c == nil {
			c = &aggCell{}
			aggs[k] = c
		}
		c.host = host
		c.n++
		c.los += los
		if okInt != 0 {
			if c.okn == 0 {
				c.lat, c.jit = lat, jit // 查询按 ts 倒序，保留最近一次成功结果
			}
			c.okn++
		}
	}
	if err := rows.Err(); err != nil {
		log.Printf("[hub] 遍历网络质量失败: %v", err)
		return nil
	}

	// 聚合结果落成页面数据：一 (省, ISP) 一格。
	for k, c := range aggs {
		cell := &NetQCell{
			HasData: true,
			OK:      c.okn > 0,
			Latency: c.lat,
			Jitter:  c.jit,
			Loss:    c.los / float64(c.n), // 丢包按全部点（不通也是丢包）
			Name:    c.host,
		}
		if c.okn == 0 {
			cell.Latency, cell.Jitter = 0, 0
		}

		// 波动路径：历史按 (province, isp) 取。境外目标入库时
		// province 就是地区名（"中国台湾"/"日本"…，见 overseasTargets），
		// 所以一把钥匙就行；空 province 的老数据用 ispLabel 兜底。
		waveKey := k.prov
		if waveKey == "" {
			waveKey = ispLabel(k.isp)
		}
		if hist := nodeWave[waveKey]; hist != nil {
			cell.WaveLine, cell.WaveArea = netqWavePaths(hist[k.isp])
		}

		sum.Total++
		if cell.OK {
			sum.OKN++
		}

		// 三网大卡片固定显示上海基准；分省表仍各自保留最近实际上报值。
		if isISP3(k.isp) && k.prov == "上海" {
			st := byISP[k.isp]
			if st == nil {
				st = &NetQIspStat{ISP: k.isp, Label: ispLabel(k.isp)}
				byISP[k.isp] = st
			}
			st.Total++
			if cell.OK {
				st.OKN++
				st.Latency += cell.Latency
				st.Jitter += cell.Jitter
			}
			st.Loss += cell.Loss
		}

		if isISP3(k.isp) {
			r := byProvince[k.prov]
			if r == nil {
				r = &NetQRow{Province: k.prov}
				byProvince[k.prov] = r
			}
			switch k.isp {
			case netprobe.ISPTelecom:
				r.Telecom = cell
			case netprobe.ISPUnicom:
				r.Unicom = cell
			case netprobe.ISPMobile:
				r.Mobile = cell
			}
		} else {
			// 港澳台与境外不参与三网统计，单独一张表
			label := ispLabel(k.isp)
			r := overseas[label]
			if r == nil {
				r = &NetQRow{Province: label}
				overseas[label] = r
			}
			r.Telecom = cell
		}
	}
	if err := rows.Err(); err != nil {
		return nil
	}
	// 为已选中的国内省份保留固定行。某格最近 30 天没有实际
	// 观测时留横线，不会因为当前轮没轮到它而误报“不通”。
	for province := range domesticSelected {
		r := byProvince[province]
		if r == nil {
			r = &NetQRow{Province: province}
			byProvince[province] = r
		}
		if r.Telecom == nil {
			r.Telecom = &NetQCell{}
		}
		if r.Unicom == nil {
			r.Unicom = &NetQCell{}
		}
		if r.Mobile == nil {
			r.Mobile = &NetQCell{}
		}
	}
	if sum.Total == 0 && len(overseas) == 0 && len(byProvince) == 0 {
		return nil
	}

	// 三张基准卡固定存在；上海未被选中或该轮缺测时清楚显示横线/0，而不借用别省数据。
	for _, isp := range []string{netprobe.ISPTelecom, netprobe.ISPUnicom, netprobe.ISPMobile} {
		if byISP[isp] == nil {
			byISP[isp] = &NetQIspStat{ISP: isp, Label: ispLabel(isp)}
		}
	}
	// 三网卡片：延迟/抖动按"通的点"取平均，丢包率按全部点平均
	for _, st := range byISP {
		if !isISP3(st.ISP) {
			continue
		}
		if st.OKN > 0 {
			st.Latency /= float64(st.OKN)
			st.Jitter /= float64(st.OKN)
		}
		if st.Total > 0 {
			st.Loss /= float64(st.Total)
		}
		sum.ISPs = append(sum.ISPs, *st)
	}
	sort.Slice(sum.ISPs, func(i, j int) bool { return ispOrder(sum.ISPs[i].ISP) < ispOrder(sum.ISPs[j].ISP) })

	// 省份明细：算每行平均延迟（只算通的格子）
	for _, r := range byProvince {
		var sumLat float64
		var n int
		for _, c := range []*NetQCell{r.Telecom, r.Unicom, r.Mobile} {
			if c == nil || !c.HasData {
				continue
			}
			if c.OK {
				sumLat += c.Latency
				n++
				r.OKN++
			}
		}
		if n > 0 {
			r.Avg = sumLat / float64(n)
		}
		sum.Rows = append(sum.Rows, *r)
	}
	provinceOrder := make(map[string]int, len(netprobe.Provinces()))
	for i, province := range netprobe.Provinces() {
		provinceOrder[province] = i
	}
	sort.Slice(sum.Rows, func(i, j int) bool {
		left, lok := provinceOrder[sum.Rows[i].Province]
		right, rok := provinceOrder[sum.Rows[j].Province]
		if lok != rok {
			return lok
		}
		if lok {
			return left < right
		}
		return sum.Rows[i].Province < sum.Rows[j].Province
	})
	sum.Provinces = len(sum.Rows)
	for _, row := range sum.Rows {
		if row.Province != "上海" {
			sum.DetailRows = append(sum.DetailRows, row)
		}
	}
	for _, r := range overseas {
		if r.Telecom != nil && r.Telecom.HasData && r.Telecom.OK {
			r.Avg = r.Telecom.Latency
			r.OKN = 1
		}
		sum.Overseas = append(sum.Overseas, *r)
	}
	sort.Slice(sum.Overseas, func(i, j int) bool { return sum.Overseas[i].Province < sum.Overseas[j].Province })

	// 最好/最差各三：只取真正测出延迟的省
	var ranked []NetQRow
	for _, r := range sum.Rows {
		if r.Avg > 0 {
			ranked = append(ranked, r)
		}
	}
	sort.Slice(ranked, func(i, j int) bool { return ranked[i].Avg < ranked[j].Avg })
	if len(ranked) > 3 {
		sum.Best = ranked[:3]
		sum.Worst = ranked[len(ranked)-3:]
		// 最差的三省按由差到好排列，读起来更顺
		for i, j := 0, len(sum.Worst)-1; i < j; i, j = i+1, j-1 {
			sum.Worst[i], sum.Worst[j] = sum.Worst[j], sum.Worst[i]
		}
	}

	sum.Stale = time.Since(time.UnixMilli(ts)) > 6*time.Hour
	return sum
}

type netQWaveWindow struct {
	key      string
	duration time.Duration
	buckets  int
}

func netQWaveWindowFor(key string) netQWaveWindow {
	switch key {
	case "6h":
		return netQWaveWindow{"6h", 6 * time.Hour, 72}
	case "1d":
		return netQWaveWindow{"1d", 24 * time.Hour, 96}
	case "7d":
		return netQWaveWindow{"7d", 7 * 24 * time.Hour, 168}
	case "30d":
		return netQWaveWindow{"30d", 30 * 24 * time.Hour, 180}
	default:
		return netQWaveWindow{"1h", time.Hour, 60}
	}
}

// isISP3 判断是不是三网（排除境外点）。
func isISP3(isp string) bool {
	return isp == netprobe.ISPTelecom || isp == netprobe.ISPUnicom || isp == netprobe.ISPMobile
}

// net3Cells 把一次 store.NetQByNode 的结果转成卡片三网行的数据。
//
// 抽成函数是因为**两条路都要用**：首页 SSR 渲染（web.go renderHome）
// 和 SSE 的 netq 事件（探测结果一到就广播，见 broadcastNetQ）——
// 两处必须同一套排序与口径，否则"刷新后数字变了"说不清。
func net3Cells(lats []store.ISPLatency, family string) []net3Cell {
	if len(lats) == 0 {
		return nil
	}
	sort.SliceStable(lats, func(i, j int) bool {
		return ispOrder(lats[i].ISP) < ispOrder(lats[j].ISP)
	})
	family = strings.ToLower(strings.TrimSpace(family))
	if family != "v6" {
		family = "v4"
	}
	out := make([]net3Cell, 0, len(lats))
	for _, l := range lats {
		line, area := netqWavePaths(l.Hist)
		cell := net3Cell{
			Label:     ispLabel(l.ISP) + " " + family,
			Level:     "ok",
			SparkLine: line,
			SparkArea: area,
		}
		switch {
		case l.HasCurrent && l.OKN == 0:
			// 全部探测都失败时要把失败显示出来，不能因为 SQL 的平均值为
			// NULL 而把这一轮抹掉，让卡片一直留着旧数字或横线。
			cell.Unavailable = true
			cell.HasLoss = true
			cell.Loss = l.Loss
			cell.Level = "bad"
		case l.HasCurrent:
			cell.MS = fmt.Sprintf("%.1f", math.Round(l.Latency*10)/10)
			cell.HasLoss = true
			cell.Loss = l.Loss
			cell.Level = netLevel(l.Latency)
		case len(l.Hist) > 0:
			// 超出当前聚合窗口时沿用最近一次成功延迟；历史缺测不会
			// 被伪装成当前丢包。
			last := l.Hist[len(l.Hist)-1]
			cell.MS = fmt.Sprintf("%.1f", math.Round(last*10)/10)
			cell.Level = netLevel(last)
		default:
			continue
		}
		out = append(out, cell)
	}
	return out
}

func ispLabel(isp string) string {
	if s, ok := ispLabels[isp]; ok {
		return s
	}
	return isp
}

// ispOrder 让卡片固定按 电信 → 联通 → 移动 排列，其余跟在后面。
func ispOrder(isp string) int {
	switch isp {
	case netprobe.ISPTelecom:
		return 0
	case netprobe.ISPUnicom:
		return 1
	case netprobe.ISPMobile:
		return 2
	}
	return 10
}
