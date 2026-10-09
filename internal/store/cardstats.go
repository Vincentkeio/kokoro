package store

// 首页卡片要用的两类统计：三网延迟、本日/本月流量。
//
// ⚠️ 都做成**批量**接口。首页一屏可能有几十张卡，
// 每张卡各查一次就是几百次往返 —— 这是首页变慢最典型的写法。

// ISP 代码。跟 netprobe 里那三个保持一致，但这里不引那个包：
// store 只认字符串，避免为了三个常量多一层依赖。
const (
	ispTelecom = "telecom"
	ispUnicom  = "unicom"
	ispMobile  = "mobile"
)

// ISPLatency 是某个 ISP 的聚合延迟。
type ISPLatency struct {
	ISP     string
	Latency float64 // 毫秒，已按成功条数平均
	OKN     int
}

// NetQByNode 取每台机器**最近一轮**探测里三网的延迟。
//
// 为什么用"最近一轮"而不是"最近 N 小时平均"：
// 详情页用的就是最近一轮，两处口径必须一样 ——
// 不然卡片写 45ms、点进去写 82ms，站长会以为其中一个是错的。
//
// 只算 ok=1 的条目：不通的目标延迟记 0，
// 混进平均值里会把结果拉低，看起来比实际更好。
func (s *Store) NetQByNode() (map[string][]ISPLatency, error) {
	rows, err := s.db.Query(`
SELECT n.node_id, n.isp, SUM(n.latency), COUNT(*)
FROM net_quality AS n
JOIN (
    SELECT node_id, MAX(ts) AS ts FROM net_quality GROUP BY node_id
) AS last ON last.node_id = n.node_id AND last.ts = n.ts
WHERE n.ok = 1 AND n.isp IN (?, ?, ?)
GROUP BY n.node_id, n.isp`, ispTelecom, ispUnicom, ispMobile)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string][]ISPLatency{}
	for rows.Next() {
		var nodeID, isp string
		var sum float64
		var n int
		if err := rows.Scan(&nodeID, &isp, &sum, &n); err != nil {
			return nil, err
		}
		if n == 0 {
			continue
		}
		out[nodeID] = append(out[nodeID], ISPLatency{ISP: isp, Latency: sum / float64(n), OKN: n})
	}
	return out, rows.Err()
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
