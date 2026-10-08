package hub

// 网络质量（三网分省探测）在 Hub 侧的两件事：
//   1. 收 agent 上报的探测结果（/api/v1/netq）；
//   2. 把最近一轮结果聚合成页面要用的 NetQSummary。
//
// 数据是"一轮一轮"的：每个 ts 是一次完整扫描，页面只关心最近一轮。
// 历史留 7 天，超期自动清掉，避免 SQLite 被这种低频但条数多的表撑爆。

import (
	"fmt"
	"log"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/kokoro-probe/kokoro/internal/netprobe"
)

// netqKeep 网络质量数据的保留时长。
const netqKeep = 7 * 24 * time.Hour

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
	OK      bool
	Latency float64
	Jitter  float64
	Loss    float64
	Name    string
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
	Ts        int64
	Mode      string
	Total     int
	OKN       int
	Provinces int
	Stale     bool // 超过 6 小时没更新，页面上要提示
	ISPs      []NetQIspStat
	Rows      []NetQRow
	Overseas  []NetQRow // 港澳台与境外，单独列，不混进三网统计
	Best      []NetQRow
	Worst     []NetQRow
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
		province, isp, name, host string
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
		if lat < 0 || lat > 60000 {
			lat = 0
		}
		if jit < 0 || jit > 60000 {
			jit = 0
		}
		if los < 0 {
			los = 0
		}
		if los > 100 {
			los = 100
		}
		rows = append(rows, row{
			province: province, isp: isp, name: res.Target.Name, host: host,
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
(node_id, ts, mode, province, isp, city, host, ok, latency, jitter, loss)
VALUES (?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		_ = tx.Rollback()
		log.Printf("[hub] 预编译网络质量写入失败: %v", err)
		writeErr(w, http.StatusInternalServerError, "写入失败")
		return
	}
	for _, x := range rows {
		okVal := 0
		if x.ok {
			okVal = 1
		}
		// city 列目前留空：探测目标表里没有城市字段，保留列位以备后续细化。
		if _, err := stmt.Exec(node.ID, ts, mode, x.province, x.isp, "", x.host,
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

	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "stored": len(rows), "ts": ts, "mode": mode})
}

func finite(f float64) bool {
	return !math.IsNaN(f) && !math.IsInf(f, 0)
}

// ---- 聚合 ----

// LoadNetQ 读某台小鸡最近一轮的三网探测结果并聚合成页面数据。
// 没有数据返回 nil（模板会跳过整个区块）。
func (h *Hub) LoadNetQ(nodeID string) *NetQSummary {
	if nodeID == "" {
		return nil
	}
	db := h.store.DB()

	var ts int64
	var mode string
	err := db.QueryRow(
		`SELECT ts, mode FROM net_quality WHERE node_id = ? ORDER BY ts DESC LIMIT 1`,
		nodeID).Scan(&ts, &mode)
	if err != nil {
		return nil
	}

	rows, err := db.Query(`SELECT province, isp, host, ok, latency, jitter, loss
FROM net_quality WHERE node_id = ? AND ts = ? ORDER BY province, isp`, nodeID, ts)
	if err != nil {
		log.Printf("[hub] 读取网络质量失败 %s: %v", nodeID, err)
		return nil
	}
	defer rows.Close()

	sum := &NetQSummary{Ts: ts, Mode: mode}
	byProvince := make(map[string]*NetQRow, 40)
	byISP := make(map[string]*NetQIspStat, 8)
	overseas := make(map[string]*NetQRow, 16)

	for rows.Next() {
		var (
			province, isp, host string
			okInt               int
			lat, jit, los       float64
		)
		if err := rows.Scan(&province, &isp, &host, &okInt, &lat, &jit, &los); err != nil {
			log.Printf("[hub] 解析网络质量行失败: %v", err)
			return nil
		}
		cell := &NetQCell{OK: okInt != 0, Latency: lat, Jitter: jit, Loss: los, Name: host}

		sum.Total++
		if cell.OK {
			sum.OKN++
		}

		st := byISP[isp]
		if st == nil {
			st = &NetQIspStat{ISP: isp, Label: ispLabel(isp)}
			byISP[isp] = st
		}
		st.Total++
		if cell.OK {
			st.OKN++
			st.Latency += lat
			st.Jitter += jit
		}
		st.Loss += los

		if isISP3(isp) {
			r := byProvince[province]
			if r == nil {
				r = &NetQRow{Province: province}
				byProvince[province] = r
			}
			switch isp {
			case netprobe.ISPTelecom:
				r.Telecom = cell
			case netprobe.ISPUnicom:
				r.Unicom = cell
			case netprobe.ISPMobile:
				r.Mobile = cell
			}
		} else {
			// 港澳台与境外不参与三网统计，单独一张表
			r := overseas[ispLabel(isp)]
			if r == nil {
				r = &NetQRow{Province: ispLabel(isp)}
				overseas[ispLabel(isp)] = r
			}
			r.Telecom = cell
		}
	}
	if err := rows.Err(); err != nil {
		return nil
	}
	if sum.Total == 0 {
		return nil
	}

	// 三网卡片：延迟/抖动按"通的点"取平均，丢包率按全部点平均
	for _, st := range byISP {
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
			if c == nil {
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
	sort.Slice(sum.Rows, func(i, j int) bool {
		// 有数据的排前面，其次按平均延迟
		if (sum.Rows[i].Avg > 0) != (sum.Rows[j].Avg > 0) {
			return sum.Rows[i].Avg > 0
		}
		return sum.Rows[i].Avg < sum.Rows[j].Avg
	})
	sum.Provinces = len(sum.Rows)
	for _, r := range overseas {
		if r.Telecom != nil && r.Telecom.OK {
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

// isISP3 判断是不是三网（排除境外点）。
func isISP3(isp string) bool {
	return isp == netprobe.ISPTelecom || isp == netprobe.ISPUnicom || isp == netprobe.ISPMobile
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
