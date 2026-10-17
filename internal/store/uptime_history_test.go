package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/Vincentkeio/kokoro/internal/model"
)

func TestDailyUptimeSurvivesMetricRollupPruning(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "uptime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	n := &model.Node{ID: "nd_uptime", Name: "uptime", CreatedAt: time.Now().AddDate(0, 0, -12).UnixMilli()}
	if err := s.CreateNode(n); err != nil {
		t.Fatal(err)
	}
	_, offset := time.Now().Zone()
	loc := time.FixedZone("test", offset)
	now := time.Now().In(loc)
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, -8)
	for i := 0; i < 288; i++ {
		if _, err := s.db.Exec(`INSERT INTO metrics_5m(node_id,bucket,samples) VALUES(?,?,100)`, n.ID, day.Add(time.Duration(i)*5*time.Minute).UnixMilli()); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.ArchiveUptimeBuckets(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`DELETE FROM metrics_5m WHERE node_id=?`, n.ID); err != nil {
		t.Fatal(err)
	}
	days, err := s.DailyUptime(n.ID, 30, offset/60)
	if err != nil {
		t.Fatal(err)
	}
	want := day.Format("2006-01-02")
	for _, d := range days {
		if d.Date == want {
			if !d.HasData || d.Pct < 99 || d.ExpectedSamples <= 0 {
				t.Fatalf("归档后的在线率数据丢失或分母错误：%+v", d)
			}
			return
		}
	}
	t.Fatalf("未返回历史日期 %s", want)
}

func TestMigrationBackfillsUptimeFromHourlyRollups(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "migration.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	n := &model.Node{ID: "nd_hourly", Name: "hourly", CreatedAt: time.Now().AddDate(0, 0, -12).UnixMilli()}
	if err := s.CreateNode(n); err != nil {
		t.Fatal(err)
	}
	_, offset := time.Now().Zone()
	loc := time.FixedZone("test", offset)
	now := time.Now().In(loc)
	hour := time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, loc).AddDate(0, 0, -8)
	if _, err := s.db.Exec(`INSERT INTO metrics_1h(node_id,bucket,samples) VALUES(?,?,1800)`, n.ID, hour.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateTask(&model.NodeTask{ID: "tk_ghost", NodeID: "nd_missing", Kind: "bench", Cmd: "test:bench"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`PRAGMA user_version=18`); err != nil {
		t.Fatal(err)
	}
	if err := s.migrate(); err != nil {
		t.Fatal(err)
	}
	var count, samples int
	if err := s.db.QueryRow(`SELECT COUNT(*),SUM(samples) FROM uptime_buckets WHERE node_id=?`, n.ID).Scan(&count, &samples); err != nil {
		t.Fatal(err)
	}
	if count != 12 || samples != 1800 {
		t.Fatalf("小时汇总回填应保留 12 个五分钟桶和原样本数，得到 count=%d samples=%d", count, samples)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM node_tasks WHERE node_id='nd_missing'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("迁移后仍有 %d 条幽灵任务", count)
	}
}
