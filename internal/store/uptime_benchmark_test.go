package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/Vincentkeio/kokoro/internal/model"
)

func BenchmarkDailyUptimeRetainedHistory(b *testing.B) {
	benchmarkDailyUptimeRetainedHistory(b, false)
}

func BenchmarkDailyUptimeRetainedHistoryWithLiveRollups(b *testing.B) {
	benchmarkDailyUptimeRetainedHistory(b, true)
}

func benchmarkDailyUptimeRetainedHistory(b *testing.B, live bool) {
	s, err := Open(filepath.Join(b.TempDir(), "bench.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer s.Close()
	n := &model.Node{ID: "uptime-bench", Name: "uptime-bench", CreatedAt: time.Now().Add(-100 * 24 * time.Hour).UnixMilli()}
	if err := s.CreateNode(n); err != nil {
		b.Fatal(err)
	}
	start := nowMS()/bucket5m*bucket5m - 90*bucket1d
	if _, err := s.DB().Exec(`WITH RECURSIVE ticks(n) AS (SELECT 0 UNION ALL SELECT n+1 FROM ticks WHERE n<25919)
INSERT INTO uptime_buckets(node_id,bucket,samples) SELECT ?,?+n*300000,150 FROM ticks`, n.ID, start); err != nil {
		b.Fatal(err)
	}
	if live {
		if _, err := s.DB().Exec(`INSERT INTO metrics_5m(node_id,bucket,samples) SELECT node_id,bucket,samples FROM uptime_buckets WHERE node_id=? AND bucket>=?`, n.ID, start+83*bucket1d); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.DailyUptime(n.ID, 30, 480); err != nil {
			b.Fatal(err)
		}
	}
}
