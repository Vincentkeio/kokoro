package store

import (
	"testing"

	"github.com/Vincentkeio/kokoro/internal/model"
)

func TestLatestSnapshotKeepsLatestPerNode(t *testing.T) {
	s := openTmp(t)
	for _, id := range []string{"snapshot-a", "snapshot-b", "snapshot-empty"} {
		if err := s.CreateNode(&model.Node{ID: id, Name: id, TokenHash: id}); err != nil {
			t.Fatal(err)
		}
	}
	for _, row := range []struct {
		id  string
		ts  int64
		cpu float64
	}{
		{"snapshot-a", 3000, 30}, {"snapshot-a", 1000, 10},
		{"snapshot-b", 2000, 20}, {"snapshot-b", 4000, 40},
	} {
		if err := s.InsertMetrics(row.id, &model.Metrics{V: 1, Ts: row.ts, CPU: model.CPUStat{Usage: row.cpu}}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.LatestSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got["snapshot-a"] == nil || got["snapshot-b"] == nil {
		t.Fatalf("快照缺失或包含无指标节点: %v", got)
	}
	if got["snapshot-a"].Ts != 3000 || got["snapshot-a"].CPU.Usage != 30 || got["snapshot-b"].Ts != 4000 || got["snapshot-b"].CPU.Usage != 40 {
		t.Fatal("应分别返回每个节点时间最新的指标")
	}
}
