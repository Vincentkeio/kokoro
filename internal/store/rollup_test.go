package store

import (
	"testing"

	"github.com/Vincentkeio/kokoro/internal/model"
)

// TestRollupLadderCascade 钉住分层降采样：
//   - raw → 1m → 5m 会级联；
//   - 只聚合"已闭合"的窗口（一小时内不可能有闭合的 1 小时桶，粗层必须为空）；
//   - 粗层的平均值等于"按 samples 加权的细层平均"，与原始表直接平均一致。
func TestRollupLadderCascade(t *testing.T) {
	s := openTmp(t)
	if err := s.CreateNode(&model.Node{ID: "n1", Name: "n1", TokenHash: "h"}); err != nil {
		t.Fatal(err)
	}

	now := nowMS()
	// 从 12 分钟前铺到 2 分钟前，每 2 秒一条：跨多个 1 分钟桶与 5 分钟桶。
	var ms []*model.Metrics
	i := 0
	for ts := now - 12*60*1000; ts <= now-2*60*1000; ts += 2000 {
		ms = append(ms, &model.Metrics{
			V: 1, Ts: ts, Seq: int64(i),
			CPU:  model.CPUStat{Usage: 10 + float64(i%3), Load1: 0.5},
			Mem:  model.MemStat{Total: 8 << 30, Used: int64(1000 + i)},
			Disk: model.DiskStat{Total: 40 << 30, Used: 8 << 30},
			Net: model.NetStat{
				Up: int64(100 + i), Down: int64(200 + i),
				TotalUp: int64(5000 + i), TotalDown: int64(9000 + i),
			},
		})
		i++
	}
	if err := s.InsertMetricsBatch("n1", ms); err != nil {
		t.Fatal(err)
	}
	if err := s.AggregateRollups(); err != nil {
		t.Fatal(err)
	}

	count := func(tbl string) int {
		var n int
		if err := s.DB().QueryRow("SELECT COUNT(*) FROM " + tbl).Scan(&n); err != nil {
			t.Fatalf("统计 %s 失败: %v", tbl, err)
		}
		return n
	}
	if n := count("metrics_1m"); n < 8 {
		t.Errorf("1 分钟层应有 ≥8 个桶，实际 %d", n)
	}
	if n := count("metrics_5m"); n < 1 {
		t.Errorf("5 分钟层应有已闭合桶，实际 %d", n)
	}
	// 数据都在最近 12 分钟内：不可能有闭合的 1 小时/1 天窗口。
	if n := count("metrics_1h"); n != 0 {
		t.Errorf("1 小时层窗口未闭合，不该有数据，实际 %d", n)
	}
	if n := count("metrics_1d"); n != 0 {
		t.Errorf("1 天层窗口未闭合，不该有数据，实际 %d", n)
	}

	// 级联正确性：任取一个 5 分钟桶，其 cpu_avg 应等于原始表在该桶内的直接平均。
	var bucket int64
	if err := s.DB().QueryRow("SELECT MIN(bucket) FROM metrics_5m").Scan(&bucket); err != nil {
		t.Fatal(err)
	}
	var cascaded, direct float64
	if err := s.DB().QueryRow("SELECT cpu_avg FROM metrics_5m WHERE bucket = ?", bucket).Scan(&cascaded); err != nil {
		t.Fatal(err)
	}
	if err := s.DB().QueryRow(
		"SELECT AVG(cpu) FROM metrics_raw WHERE ts >= ? AND ts < ?", bucket, bucket+bucket5m).Scan(&direct); err != nil {
		t.Fatal(err)
	}
	if diff := cascaded - direct; diff > 0.01 || diff < -0.01 {
		t.Errorf("5 分钟层 cpu_avg = %v，与原始直接平均 %v 不一致（级联加权出错）", cascaded, direct)
	}

	// 读路径：1 小时窗口应走 raw 并拿到点。
	pts, err := s.History("n1", now-3600*1000, now, 180)
	if err != nil {
		t.Fatal(err)
	}
	if len(pts) == 0 {
		t.Error("History(1h) 应返回数据")
	}
}

// TestPruneRespectsPerLayerRetention 钉住"按层保留期"：各层用各自的时长期限清理，
// 而不是写死的 24h/30d。
func TestPruneRespectsPerLayerRetention(t *testing.T) {
	s := openTmp(t)
	if err := s.CreateNode(&model.Node{ID: "n2", Name: "n2", TokenHash: "h2"}); err != nil {
		t.Fatal(err)
	}
	now := nowMS()
	// 手工塞入三层数据：新老各一条。
	oldTs := now - 10*24*60*60*1000 // 10 天前
	newTs := now - 60*1000
	for _, ts := range []int64{oldTs, newTs} {
		if _, err := s.DB().Exec(
			`INSERT INTO metrics_1m (node_id,bucket,cpu_avg,cpu_max,mem_used_avg,net_up_avg,net_down_avg,load1_avg,
 net_up_max,net_down_max,total_up_max,total_down_max,samples) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			"n2", ts, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.DB().Exec(
		`INSERT INTO metrics_1h (node_id,bucket,cpu_avg,cpu_max,mem_used_avg,net_up_avg,net_down_avg,load1_avg,
 net_up_max,net_down_max,total_up_max,total_down_max,samples) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		"n2", oldTs, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1); err != nil {
		t.Fatal(err)
	}

	if err := s.Prune(); err != nil {
		t.Fatal(err)
	}

	var n1m, n1h int
	_ = s.DB().QueryRow("SELECT COUNT(*) FROM metrics_1m WHERE node_id='n2'").Scan(&n1m)
	_ = s.DB().QueryRow("SELECT COUNT(*) FROM metrics_1h WHERE node_id='n2'").Scan(&n1h)
	// 1m 层默认只留 24 小时 → 10 天前那条应被清掉，剩新的一条。
	if n1m != 1 {
		t.Errorf("metrics_1m 保留后剩 %d 条，应为 1（10 天前那条应被清）", n1m)
	}
	// 1h 层默认留 45 天 → 10 天前那条应保留。
	if n1h != 1 {
		t.Errorf("metrics_1h 保留后剩 %d 条，应为 1（45 天内不该清）", n1h)
	}
}
