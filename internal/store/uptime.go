package store

// 每日在线率统计 —— 「状态时间轴」（一天一格）的数据来源。
//
// 历史从 metrics_5m / uptime_buckets 读取，尚未汇总的部分用原始上报补齐，
// 新接入节点无需等待首个五分钟窗口闭合。
//
// 为什么不是简单数格子：装机当天和今天都是**半天**，按整天算会显示成
// 50% 在线，是假的。所以每天按**实际覆盖到的时间跨度**折算期望值。

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/Vincentkeio/kokoro/internal/model"
)

// Prefer the current rollup when an archived bucket has since been corrected.
// UNION on (bucket,samples) would count both versions of a changed bucket.
const uptimeSourceSQL = `
SELECT bucket,samples FROM metrics_5m WHERE node_id=? AND bucket>=? AND bucket<=?
UNION ALL
SELECT u.bucket,u.samples FROM uptime_buckets u WHERE u.node_id=? AND u.bucket>=? AND u.bucket<=?
AND NOT EXISTS (SELECT 1 FROM metrics_5m m WHERE m.node_id=u.node_id AND m.bucket=u.bucket)`

// Calibrate from the most recent 2016 observed buckets (a full week), rather
// than old reporting intervals. Limit each indexed branch before the merge;
// limiting only the outer UNION sorts the entire retained archive.
const recentUptimeSourceSQL = `
SELECT bucket,samples FROM (
 SELECT bucket,samples FROM metrics_5m WHERE node_id=? AND bucket>=? AND bucket<=?
 ORDER BY bucket DESC LIMIT 2016
)
UNION ALL
SELECT bucket,samples FROM (
 SELECT u.bucket,u.samples FROM uptime_buckets u WHERE u.node_id=? AND u.bucket>=? AND u.bucket<=?
 AND NOT EXISTS (SELECT 1 FROM metrics_5m m WHERE m.node_id=u.node_id AND m.bucket=u.bucket)
 ORDER BY u.bucket DESC LIMIT 2016
)`

// DayUptime 是某一天的在线率。
type DayUptime struct {
	Date    string  // 2026-10-06
	Label   string  // 10-06，给格子上显示
	Pct     float64 // 0~100
	Buckets int     // 有数据的 5 分钟格数
	Samples int     // 累计上报次数
	Partial bool    // 这一天只覆盖了一部分（装机当天 / 今天）
	HasData bool
	// BeforeStart 表示这一天**整段都在这台机器接入之前**。
	//
	// 跟"采集中断"要分开：一片灰格子可能是"机器那时候还不存在"，
	// 也可能是"机器在那儿但没上报"。前者不是故障，别吓着站长。
	BeforeStart     bool
	ExpectedBuckets int
	ExpectedSamples int64
}

// DailyUptime 取最近 days 天的在线率。
//
// tzOffsetMin 是相对 UTC 的分钟数（东八区 = 480）—— 用面板所在时区切天，
// 跟首页那个主机时钟保持一致。
func (s *Store) DailyUptime(nodeID string, days int, tzOffsetMin int) ([]DayUptime, error) {
	// Keep rollup boundaries and raw samples in one read snapshot while the
	// background compactor moves data between layers.
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// 这台机器是从什么时候开始存在的。用来决定"这一天从几点开始
	// 就应该有数据"—— 装机当天之前的时间不该算进分母。
	var since int64
	if err := tx.QueryRow(`SELECT COALESCE(created_at, 0) FROM nodes WHERE id = ?`, nodeID).Scan(&since); err != nil {
		return nil, err
	}
	if days <= 0 {
		days = 30
	}
	if days > 90 {
		days = 90 // 格子上放不下更多了
	}

	// perBucket 是"满格"应该有多少次上报 —— 整个算法的分母基准。
	//
	// ⚠️ 用**80 分位**而不是 MAX：实测满格是 142 次，最大值偶有 143，
	// 拿 MAX 当基准的话，一个完全正常的日子会被算成 99.4%，
	// 然后被分档判成"抖动"—— 那是在制造假警报。
	// 用分位数当基准，正常的日子就是 100%，真掉线才掉下来。
	//
	// 不写死数字（比如 150）：上报间隔是可以配的，写死会算出一堆假的百分比。
	now := time.Now()
	nowMS := now.UnixMilli()
	baselineFrom := nowMS - int64(90*24*time.Hour/time.Millisecond)
	var perBucket int
	err = tx.QueryRow(`WITH source AS (`+recentUptimeSourceSQL+`)
SELECT samples FROM (
  SELECT samples, ROW_NUMBER() OVER (ORDER BY samples) AS rn, COUNT(*) OVER () AS n
  FROM (SELECT samples FROM source WHERE samples>0 ORDER BY bucket DESC LIMIT 2016)
) WHERE rn=CAST(n*4/5 AS INTEGER)+1`,
		nodeID, baselineFrom, nowMS, nodeID, baselineFrom, nowMS).Scan(&perBucket)
	if err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("取上报基准失败: %w", err)
	}

	// Read only the unaggregated tail. Once a five-minute bucket is written,
	// its raw samples are excluded, so switching to rollups never counts twice.
	var rawFrom int64
	err = tx.QueryRow(`SELECT COALESCE(MAX(bucket)+300000,0) FROM (
SELECT MAX(bucket) AS bucket FROM metrics_5m WHERE node_id=?
UNION ALL SELECT MAX(bucket) FROM uptime_buckets WHERE node_id=?)`, nodeID, nodeID).Scan(&rawFrom)
	if err != nil {
		return nil, err
	}
	if perBucket <= 0 {
		var first, last sql.NullInt64
		var count int64
		err = tx.QueryRow(`SELECT MIN(ts),MAX(ts),COUNT(*) FROM (
SELECT ts FROM metrics_raw WHERE node_id=? AND ts<=? ORDER BY ts DESC LIMIT 32)`, nodeID, nowMS+20000).Scan(&first, &last, &count)
		if err != nil {
			return nil, err
		}
		if count > 0 {
			interval := int64(2000) // Hub's initial reporting interval.
			if count > 1 && last.Int64 > first.Int64 {
				interval = (last.Int64 - first.Int64) / (count - 1)
			}
			if interval < 1000 {
				interval = 1000
			}
			perBucket = int((300000 + interval/2) / interval)
			if perBucket < 1 {
				perBucket = 1
			}
		}
	}

	loc := time.FixedZone("hub", tzOffsetMin*60)
	localNow := now.In(loc)
	cutoff := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, -(days - 1)).UnixMilli()

	rows, err := tx.Query(`WITH source AS (`+uptimeSourceSQL+`)
SELECT CAST((bucket+?)/86400000 AS INTEGER) AS d,
       COUNT(*)              AS buckets,
       COALESCE(SUM(samples), 0) AS samples
FROM source WHERE samples>0
GROUP BY d
ORDER BY d`, nodeID, cutoff, nowMS, nodeID, cutoff, nowMS, int64(tzOffsetMin)*60*1000)
	if err != nil {
		return nil, fmt.Errorf("查每日在线率失败: %w", err)
	}

	got := map[string]DayUptime{}
	for rows.Next() {
		var day int64
		var buckets, samples int
		if err := rows.Scan(&day, &buckets, &samples); err != nil {
			rows.Close()
			return nil, err
		}
		d := time.UnixMilli(day * bucket1d).UTC().Format("2006-01-02")
		got[d] = DayUptime{
			Date: d, Buckets: buckets, Samples: samples,
			Pct: dayPct(d, int64(samples), perBucket, tzOffsetMin, since), HasData: samples > 0,
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	if rawFrom < cutoff {
		rawFrom = cutoff
	}
	rawRows, err := tx.Query(`SELECT CAST((ts+?)/86400000 AS INTEGER),COUNT(*)
FROM metrics_raw WHERE node_id=? AND ts>=? AND ts<=? GROUP BY 1`, int64(tzOffsetMin)*60000, nodeID, rawFrom, nowMS+20000)
	if err != nil {
		return nil, err
	}
	for rawRows.Next() {
		var day int64
		var samples int
		if err := rawRows.Scan(&day, &samples); err != nil {
			rawRows.Close()
			return nil, err
		}
		key := time.UnixMilli(day * bucket1d).UTC().Format("2006-01-02")
		du := got[key]
		du.Date, du.HasData = key, true
		du.Samples += samples
		got[key] = du
	}
	err = rawRows.Err()
	rawRows.Close()
	if err != nil {
		return nil, err
	}

	// 补齐没有数据的天 —— 空格子本身也是信息（那天机器是挂的）
	out := make([]DayUptime, 0, days)
	for i := days - 1; i >= 0; i-- {
		t := localNow.AddDate(0, 0, -i)
		key := t.Format("2006-01-02")
		du, ok := got[key]
		if !ok || !du.HasData {
			du = DayUptime{Date: key, Pct: 0, HasData: false}
		}
		du.Label = key[5:] // 10-06
		// 整天都在接入之前 —— 那天这台机器还不存在。
		//
		// ⚠️ 日末尾要取**下一天零点**，不能用 t.AddDate(0,0,1) ——
		// 那个保留的是"当前时刻"（比如 11:21），于是"昨天"的日末尾
		// 正好等于今天 11:21，跟创建时间撞在边界上，判断结果随机。
		// 用零点算，"昨天 24:00" 严格早于"今天 11:21"，稳定成立。
		if since > 0 {
			dayStart := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
			dayEnd := dayStart.AddDate(0, 0, 1).UnixMilli()
			du.BeforeStart = dayEnd <= since
		}
		// 装机当天和今天都只覆盖半天，标出来免得被误读成"那天挂了一半"
		du.ExpectedBuckets = dayExpectedBuckets(key, tzOffsetMin, since)
		du.ExpectedSamples = dayExpectedSamples(key, tzOffsetMin, since, perBucket)
		if du.ExpectedSamples > 0 {
			du.Pct = float64(du.Samples) / float64(du.ExpectedSamples) * 100
			if du.Pct > 100 {
				du.Pct = 100
			}
		}
		du.Partial = i == 0 || du.Buckets > 0 && du.Buckets < 280
		out = append(out, du)
	}
	return out, nil
}

// Include only elapsed time, rather than charging a newly joined node for an
// entire five-minute window before that time has elapsed.
func dayExpectedSamples(key string, offset int, since int64, perBucket int) int64 {
	loc := time.FixedZone("hub", offset*60)
	start, err := time.ParseInLocation("2006-01-02", key, loc)
	if err != nil || perBucket <= 0 {
		return 0
	}
	end := start.AddDate(0, 0, 1).UnixMilli()
	from := start.UnixMilli()
	if since > from {
		from = since
	}
	if now := time.Now().UnixMilli(); now < end {
		end = now
	}
	if end <= from {
		return 0
	}
	return (int64(perBucket)*(end-from) + 299999) / 300000
}

// dayPct 算某一天的在线率。
//
// 分母是「这一天**从几点开始就应该有数据**」，不是「数据覆盖到哪」——
// 这两个差得很远，而且差的正是最关键的那种情况：
//
//	机器中午挂了再没起来 → 数据只覆盖 00:00~12:00
//	按"覆盖跨度"折算 = 12 小时里满了 = 100%（把掉线藏起来了，绝对不行）
//	按"应该有的跨度"折算 = 该有 24 小时，实际 12 小时 = 50% ✓
//
// 所以窗口取 [max(当天零点, 机器创建时间), min(当天结束, 现在)]：
// 装机当天之前的时间不算分母，今天之后的时间也不算。
func dayPct(dateKey string, samples int64, perBucket, tzOffsetMin int, sinceMS int64) float64 {
	if perBucket <= 0 {
		return 0
	}
	loc := time.FixedZone("hub", tzOffsetMin*60)
	if _, err := time.ParseInLocation("2006-01-02", dateKey, loc); err != nil {
		return 0
	}
	expectedBuckets := int64(dayExpectedBuckets(dateKey, tzOffsetMin, sinceMS))
	if expectedBuckets <= 0 {
		return 0
	}
	pct := float64(samples) / float64(expectedBuckets*int64(perBucket)) * 100
	if pct > 100 {
		pct = 100 // 补报会让 samples 超出，别显示 102%
	}
	if pct < 0 {
		pct = 0
	}
	return pct
}

func dayExpectedBuckets(dateKey string, tzOffsetMin int, sinceMS int64) int {
	loc := time.FixedZone("hub", tzOffsetMin*60)
	dayStart, err := time.ParseInLocation("2006-01-02", dateKey, loc)
	if err != nil {
		return 0
	}
	start, end := dayStart, dayStart.AddDate(0, 0, 1)
	if sinceMS > 0 {
		if created := time.UnixMilli(sinceMS).In(loc); created.After(start) {
			start = created
		}
	}
	if now := time.Now().In(loc); now.Before(end) {
		end = now
	}
	if !end.After(start) {
		return 0
	}
	const bucketMS = int64(5 * 60 * 1000)
	return int((end.UnixMilli() - start.UnixMilli() + bucketMS - 1) / bucketMS)
}

// UpdateNodeFacts 更新节点的静态信息（虚拟化 / CPU / 加速 / NAT / 国家地区）。
//
// 为什么单独一个方法而不是走 UpdateNode：UpdateNode 会覆盖一大堆字段
// （名字、坐标、标签…），而上报里只有静态信息 —— 用整条更新会把
// 站长在后台改过的东西冲掉。
//
// 只在**值真的变了**的时候才写库：上报是每 2 秒一次，无脑写会把
// SQLite 写爆（这些字段开机后基本不变）。
func (s *Store) UpdateNodeFacts(nodeID string, f *model.HostFacts, nat bool) error {
	if f == nil {
		return nil
	}
	// 先读当前值，一样就不写
	var cur struct {
		virt, cc, qdisc, cpu string
		country, region      string
		cores                int
		nat                  int
	}
	err := s.db.QueryRow(
		`SELECT virt, IFNULL(tcp_cc,''), IFNULL(tcp_qdisc,''), IFNULL(cpu_model,''),
		        IFNULL(cpu_cores,0), IFNULL(nat,0),
		        IFNULL(country,''), IFNULL(region,'') FROM nodes WHERE id = ?`, nodeID).
		Scan(&cur.virt, &cur.cc, &cur.qdisc, &cur.cpu, &cur.cores, &cur.nat,
			&cur.country, &cur.region)
	if err != nil {
		return err // 节点不存在就算了
	}
	natInt := 0
	if nat {
		natInt = 1
	}

	// ⚠️ 位置**只在探到值时才覆盖**。
	//
	// agent 那边探测可能失败（没网、Cloudflare 被墙），那时 country/region
	// 是空串。无脑覆盖会把已经有的位置抹成空 —— 表现成"地区突然变未知"，
	// 而且下次探成功之前一直是空的。
	country, region := cur.country, cur.region
	if f.Country != "" {
		country = f.Country
	}
	if f.Region != "" {
		region = f.Region
	}

	if cur.virt == f.Virt && cur.cc == f.TCPCC && cur.qdisc == f.TCPQdisc &&
		cur.cpu == f.CPUModel && cur.cores == f.CPUCores && cur.nat == natInt &&
		cur.country == country && cur.region == region {
		return nil // 一模一样，不写
	}
	_, err = s.db.Exec(`UPDATE nodes SET
virt = ?, tcp_cc = ?, tcp_qdisc = ?, cpu_model = ?, cpu_cores = ?, nat = ?,
country = ?, region = ?
WHERE id = ?`,
		f.Virt, f.TCPCC, f.TCPQdisc, f.CPUModel, f.CPUCores, natInt,
		country, region, nodeID)
	return err
}
