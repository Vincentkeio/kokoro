package store

// 首页卡片要用的两类统计：三网延迟、本日/本月流量。
//
// ⚠️ 都做成**批量**接口。首页一屏可能有几十张卡，
// 每张卡各查一次就是几百次往返 —— 这是首页变慢最典型的写法。

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"
)

// NetSeriesByNode returns a bounded, batched network-rate series for the
// selected nodes and interval. It selects the same rollup layers as History
// so longer ranges do not read raw metrics or exceed their retention window.
func (s *Store) NetSeriesByNode(ids []string, from, to int64, maxPoints int) (map[string][]NetPoint, error) {
	out := make(map[string][]NetPoint, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	if to <= 0 {
		to = nowMS()
	}
	if from <= 0 || from >= to {
		from = to - int64(time.Hour/time.Millisecond)
	}
	if maxPoints <= 0 {
		maxPoints = 144
	}
	span := to - from
	step := (span + int64(maxPoints) - 1) / int64(maxPoints)
	if step < 1 {
		step = 1
	}
	var table, tsCol, upCol, downCol string
	switch {
	case span <= int64(2*time.Hour/time.Millisecond):
		table, tsCol, upCol, downCol = "metrics_raw", "ts", "net_up", "net_down"
	case span <= int64(12*time.Hour/time.Millisecond):
		table, tsCol, upCol, downCol = "metrics_1m", "bucket", "net_up_avg", "net_down_avg"
	case span <= int64(3*24*time.Hour/time.Millisecond):
		table, tsCol, upCol, downCol = "metrics_5m", "bucket", "net_up_avg", "net_down_avg"
	case span <= int64(45*24*time.Hour/time.Millisecond):
		table, tsCol, upCol, downCol = "metrics_1h", "bucket", "net_up_avg", "net_down_avg"
	default:
		table, tsCol, upCol, downCol = "metrics_1d", "bucket", "net_up_avg", "net_down_avg"
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	q := fmt.Sprintf(`SELECT node_id, b, AVG(up), AVG(down) FROM (
  SELECT node_id, (%[1]s / ?) * ? AS b, %[2]s AS up, %[3]s AS down
  FROM %[4]s WHERE %[1]s >= ? AND %[1]s <= ? AND node_id IN (%[5]s)
) GROUP BY node_id, b ORDER BY node_id, b`, tsCol, upCol, downCol, table, placeholders)
	args := make([]any, 0, len(ids)+4)
	args = append(args, step, step, from, to)
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var p NetPoint
		if err := rows.Scan(&id, &p.Bucket, &p.Up, &p.Down); err != nil {
			return nil, err
		}
		out[id] = append(out[id], p)
	}
	return out, rows.Err()
}

// ---- 聚合窗口（与 hub 侧 LoadNetQ 同一口径）----
//
// 卡片三网的「当前值」用多轮聚合，不取裸的最新一轮：连续探测下 ICMP
// 单轮抖动（偶尔整轮漏包到对端）会让数字「闪一下不通、又闪回延迟」——
// boss 2026-10-12 在北京电信上撞见的就是这个。15 分钟内最近 5 轮一起
// 算，单轮漏包就被抹掉；慢速探测（30 分钟一轮）时 15 分钟窗内通常只有
// 1 轮，自动退化成"最近一轮"，不破坏原口径。
const (
	netqAggWindowMS = 15 * 60 * 1000
	netqAggRounds   = 5
)

// ISP 代码。跟 netprobe 里那三个保持一致，但这里不引那个包：
// store 只认字符串，避免为了三个常量多一层依赖。
const (
	ispTelecom = "telecom"
	ispUnicom  = "unicom"
	ispMobile  = "mobile"
)

// ISPLatency 是某个 ISP 的聚合延迟。
type ISPLatency struct {
	ISP        string
	Latency    float64   // 毫秒，最近一轮已按成功条数平均
	Loss       float64   // 丢包率 %，最近一轮按全部探测点平均（与详情页口径一致）
	OKN        int       // 最近一轮里通的探测点数
	HasCurrent bool      // 15 分钟窗口里有探测结果；OKN=0 时表示本轮全无响应
	Hist       []float64 // 最近几轮的平均延迟（旧→新），画卡片上的波动图用

	// histPts 是 Hist 的排序暂存：查询返回的 UNION 行序不定，按轮次
	// rank 收进来、排好序再吐给 Hist（不可导出，只活在这个包里）。
	histPts []histPt
}

// histPt 是一轮历史点：rn 是该轮的新旧 rank（1=最新），lat 是该轮平均延迟。
type histPt struct {
	rn  int
	lat float64
}

// netqWaveRounds 详情页分省格子里波动图取最近几轮。
// 连续探测（默认）下一轮几十秒，24 轮 ≈ 半小时到几小时的波形；
// 全量场景（间隔拉大到几分钟）也能覆盖半天。够看出趋势又不糊。
const netqWaveRounds = 24

// NetQWaveByNode 按 (node, province, isp) 取最近 N 轮延迟序列，
// 供详情页每个格子画波动小图。
//
// 和 NetQByNode 的窗口函数同款：按轮取而不是按时间窗 —— 探测间隔
// 可配（连续模式几十秒一轮 / 手动几分钟一轮），按轮数取波形密度才稳。
// 不通的轮次（ok=0 的轮里没有可用延迟）在 SQL 层就滤掉，序列里
// 只留有数据的点；某个格子历史不足 2 轮时调用方画不出线，自然留空。
// 时间下限沿用 netqHistorySince（48h）：全覆盖 24 轮，又不会把
// 保留期（7 天）里的陈年老数据翻出来排序。
func (s *Store) NetQWaveByNode() (map[string]map[string]map[string][]float64, error) {
	out := map[string]map[string]map[string][]float64{}
	rows, err := s.db.Query(`
WITH rounds AS (
    SELECT node_id, province, isp, ts,
           AVG(CASE WHEN ok = 1 THEN latency END) AS lat
    FROM net_quality
    WHERE ts >= ? AND ip_version =
          (SELECT netq_ip_version FROM nodes WHERE id = net_quality.node_id)
    GROUP BY node_id, province, isp, ts
),
ranked AS (
    SELECT *, ROW_NUMBER() OVER (
        PARTITION BY node_id, province, isp ORDER BY ts DESC) AS rn
    FROM rounds
    WHERE lat IS NOT NULL
)
SELECT node_id, province, isp, lat
FROM ranked
WHERE rn <= ?
ORDER BY node_id, province, isp, ts`,
		nowMS()-netqHistorySince.Milliseconds(), netqWaveRounds)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var nodeID, prov, isp string
		var lat float64
		if err := rows.Scan(&nodeID, &prov, &isp, &lat); err != nil {
			return nil, err
		}
		byProv, ok := out[nodeID]
		if !ok {
			byProv = map[string]map[string][]float64{}
			out[nodeID] = byProv
		}
		byISP, ok := byProv[prov]
		if !ok {
			byISP = map[string][]float64{}
			byProv[prov] = byISP
		}
		byISP[isp] = append(byISP[isp], lat)
	}
	return out, rows.Err()
}

// NetQWaveForNode returns a fixed-width, time-bucketed history for the selected
// node. Empty buckets remain zero so the renderer can show gaps instead of
// implying that a missing report was a failed probe or a continuous line.
func (s *Store) NetQWaveForNode(nodeID string, sinceMS, untilMS int64, buckets int) (map[string]map[string][]float64, error) {
	if buckets < 2 {
		buckets = 2
	}
	if buckets > 240 {
		buckets = 240
	}
	if untilMS <= sinceMS {
		return map[string]map[string][]float64{}, nil
	}
	span := untilMS - sinceMS
	step := span / int64(buckets)
	if step < 1 {
		step = 1
	}
	out := map[string]map[string][]float64{}
	rows, err := s.db.Query(`SELECT province, isp, CAST((ts - ?)/? AS INTEGER) AS bucket,
       AVG(latency) AS lat
FROM (
  SELECT ts, province, isp, MAX(ok) AS ok,
         AVG(CASE WHEN ok=1 THEN latency END) AS latency
  FROM net_quality
  WHERE node_id=? AND ts>=? AND ts<=? AND ip_version =
        (SELECT netq_ip_version FROM nodes WHERE id = net_quality.node_id)
  GROUP BY ts, province, isp
)
WHERE ok=1 AND latency IS NOT NULL
GROUP BY province, isp, bucket
ORDER BY province, isp, bucket`, sinceMS, step, nodeID, sinceMS, untilMS)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var province, isp string
		var bucket int
		var latency float64
		if err := rows.Scan(&province, &isp, &bucket, &latency); err != nil {
			return nil, err
		}
		if bucket < 0 || bucket >= buckets {
			continue
		}
		byISP := out[province]
		if byISP == nil {
			byISP = map[string][]float64{}
			out[province] = byISP
		}
		series := byISP[isp]
		if series == nil {
			series = make([]float64, buckets)
			byISP[isp] = series
		}
		series[bucket] = latency
	}
	return out, rows.Err()
}

// netqHistoryRounds 波动图取最近几轮。30 分钟一轮（默认）时约等于半天，
// 5 分钟一轮（下限）时约两小时 —— 卡片上一条小波，这个密度刚好。
const netqHistoryRounds = 24

// netqHistorySince 历史查询的时间下限：只给窗口函数一个边界，
// 防止某些配置（间隔被调很小）时要排序的行数失控。保留期是 7 天，
// 48 小时足够覆盖 24 轮（最慢 30 分钟一轮也只要 12 小时）。
const netqHistorySince = 48 * time.Hour

// NetQByNode 取每台机器上海三网基准的当前值与延迟历史。
// 缺少上海数据时不借用其他省份；延迟、丢包和历史曲线使用同一地域口径。
//
// 当前值 = **窗口聚合**（15 分钟内最近 5 轮一起算），不取裸的最新
// 一轮：连续探测下 ICMP 单轮抖动（偶尔整轮漏包到对端）会让卡片上的
// 数字「闪一下不通、又闪回延迟」—— boss 2026-10-12 在北京电信上撞见
// 的就是这个。多轮一起算，单轮漏包就被抹掉；慢速探测（30 分钟一轮）
// 时 15 分钟窗内通常只有 1 轮，自动退化成"最近一轮"，不破坏原口径。
//
// 为什么不是 SUM（速率那条就是这么合的）：延迟**不可加** —— 按全部
// 通点取平均，机器多少都不会把结果冲高。
//
// 延迟只算 ok=1 的点：不通的目标延迟记 0，混进平均值里会把结果拉低，
// 看起来比实际更好（丢包率相反，按全部点算 —— 不通本身就是丢包的
// 一部分，见 netq.go 里 NetQIspStat 的算法）。
//
// 历史用 ROW_NUMBER() 窗口函数按 (node, isp) 分组取最近 N **轮**：按轮
// 取而不是按时间窗（探测间隔可配），图上密度才稳定。不通的轮（无可用
// 延迟）在 SQL 层滤掉，序列里只留有数据的点。
func (s *Store) NetQByNode() (map[string][]ISPLatency, error) {
	// cur = 窗口聚合（当前值）；hist = 最近 N 轮延迟（画波）。两个查询
	// 拼成 UNION，一次往返拿完。
	// 从现存节点出发，沿 (node_id, ts) 索引读取时间窗口。CROSS JOIN
	// 固定这一驱动顺序，避免规划器反过来扫描全部历史探测记录。
	q := `WITH params AS (SELECT ? AS agg_since, ? AS hist_since),
isps(isp) AS (VALUES (?), (?), (?)),
current_windows AS MATERIALIZED (
    SELECT n.id AS node_id, COALESCE((
        SELECT ts FROM net_quality INDEXED BY idx_netq_node_time
        WHERE node_id = n.id AND ts >= params.agg_since
          AND ip_version = n.netq_ip_version
          AND isp IN (SELECT isp FROM isps) AND province = '上海'
        GROUP BY ts ORDER BY ts DESC LIMIT 1 OFFSET ?
    ), params.agg_since) AS cutoff
    FROM nodes n CROSS JOIN params
),
cur AS (
    SELECT q.node_id, q.isp,
           AVG(CASE WHEN q.ok = 1 THEN q.latency END) AS lat,
           AVG(q.loss) AS loss,
           SUM(q.ok) AS okn
    FROM current_windows win
    CROSS JOIN net_quality q INDEXED BY idx_netq_node_time
    WHERE q.node_id = win.node_id AND q.ts >= win.cutoff
      AND q.ip_version = (SELECT netq_ip_version FROM nodes WHERE id = win.node_id)
      AND q.isp IN (SELECT isp FROM isps) AND q.province = '上海'
    GROUP BY q.node_id, q.isp
),
history_windows AS MATERIALIZED (
    SELECT n.id AS node_id, isps.isp, COALESCE((
        SELECT ts FROM net_quality INDEXED BY idx_netq_node_time
        WHERE node_id = n.id AND isp = isps.isp AND ts >= params.hist_since
          AND ip_version = n.netq_ip_version AND province = '上海'
        GROUP BY ts ORDER BY ts DESC LIMIT 1 OFFSET ?
    ), params.hist_since) AS cutoff
    FROM nodes n CROSS JOIN isps CROSS JOIN params
),
hist AS (
    SELECT q.node_id, q.isp, q.ts,
           AVG(CASE WHEN q.ok = 1 THEN q.latency END) AS lat,
           ROW_NUMBER() OVER (
               PARTITION BY q.node_id, q.isp ORDER BY q.ts DESC) AS rn
    FROM history_windows win
    CROSS JOIN net_quality q INDEXED BY idx_netq_node_time
    WHERE q.node_id = win.node_id AND q.isp = win.isp AND q.ts >= win.cutoff
      AND q.ip_version = (SELECT netq_ip_version FROM nodes WHERE id = win.node_id)
      AND q.province = '上海'
    GROUP BY q.node_id, q.isp, q.ts
)
SELECT node_id, isp, lat, loss, okn, 0 FROM cur
UNION ALL
SELECT node_id, isp, lat, 0, 0, rn FROM hist WHERE lat IS NOT NULL AND rn <= ?`

	rows, err := s.db.Query(q,
		nowMS()-netqAggWindowMS, nowMS()-netqHistorySince.Milliseconds(),
		ispTelecom, ispUnicom, ispMobile, netqAggRounds-1, netqHistoryRounds-1,
		netqHistoryRounds)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type key struct{ node, isp string }
	idx := map[key]int{}
	out := map[string][]ISPLatency{}
	for rows.Next() {
		var (
			nodeID, isp string
			lat         sql.NullFloat64
			loss        float64
			okn, rn     int
		)
		if err := rows.Scan(&nodeID, &isp, &lat, &loss, &okn, &rn); err != nil {
			return nil, err
		}
		k := key{nodeID, isp}
		i, seen := idx[k]
		if !seen {
			out[nodeID] = append(out[nodeID], ISPLatency{ISP: isp})
			i = len(out[nodeID]) - 1
			idx[k] = i
		}
		e := &out[nodeID][i]
		if rn > 0 {
			// hist 行：带轮次 rank 暂存，收齐后按 rank 恢复时间序
			if lat.Valid {
				e.histPts = append(e.histPts, histPt{rn: rn, lat: lat.Float64})
			}
			continue
		}
		e.HasCurrent = true
		e.Latency = 0
		if lat.Valid {
			e.Latency = lat.Float64
		}
		e.Loss = loss
		e.OKN = okn
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Hist 恢复旧→新：UNION 行序不定，收的时候按轮次 rank（rn）暂存，
	// rn 越大轮次越旧，按 rn 降序排出来就是时间升序（画波从左到右）。
	for _, list := range out {
		for i := range list {
			e := &list[i]
			pts := e.histPts
			sort.Slice(pts, func(a, b int) bool { return pts[a].rn > pts[b].rn })
			e.Hist = e.Hist[:0]
			for _, p := range pts {
				e.Hist = append(e.Hist, p.lat)
			}
		}
	}
	return out, nil
}

// TrafficSince 按节点汇总从 since 起的上下行字节数。
//
// 算法跟站级那个 TrafficSince 一致：每个 5 分钟窗口的**平均速率 × 300 秒**累加。
// 是估算不是精确计数（假设窗口内速率均匀），但对卡片上的"本日/本月"
// 足够了 —— 要精确计量得另存累计差值，那是另一个量级的事。
//
// 返回 map[node_id]{up, down}。没有任何窗口的机器不会出现在 map 里。
func (s *Store) TrafficByNodeSince(since int64) (map[string][2]int64, error) {
	out := map[string][2]int64{}
	if since <= 0 {
		return out, nil
	}
	rows, err := s.db.Query(
		`SELECT node_id, COALESCE(SUM(net_up_avg), 0), COALESCE(SUM(net_down_avg), 0)
FROM metrics_5m WHERE bucket >= ? GROUP BY node_id`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	const bucketSec = 300 // 5 分钟窗口
	for rows.Next() {
		var id string
		var upAvg, downAvg float64
		if err := rows.Scan(&id, &upAvg, &downAvg); err != nil {
			return nil, err
		}
		out[id] = [2]int64{int64(upAvg * bucketSec), int64(downAvg * bucketSec)}
	}
	return out, rows.Err()
}
