package store

import (
	"testing"
	"time"

	"github.com/Vincentkeio/kokoro/internal/model"
)

func TestNodeLabelsPreserveConcurrentAgentAndAdminChanges(t *testing.T) {
	s := openTmp(t)
	n := &model.Node{ID: "labels", Name: "old", Tags: []string{"old"}}
	if err := s.CreateNode(n); err != nil {
		t.Fatal(err)
	}
	stale, err := s.GetNode(n.ID)
	if err != nil {
		t.Fatal(err)
	}
	ts := time.Now().UnixMilli()
	if err := s.TouchNode(n.ID, ts, "1.2.3.4"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetNodeVisibility(n.ID, model.VisibilityPrivate); err != nil {
		t.Fatal(err)
	}
	if err := s.SetNodeSortOrder(n.ID, 12); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateNodeFacts(n.ID, &model.HostFacts{CPUModel: "new CPU", CPUCores: 8}, false); err != nil {
		t.Fatal(err)
	}
	stale.Name = "renamed"
	if err := s.UpdateNodeLabels(n.ID, &stale.Name, nil); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetNode(n.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "renamed" || !got.Online || got.LastSeen != ts || got.IP != "1.2.3.4" || got.Visibility != model.VisibilityPrivate || got.SortOrder != 12 || got.CPUModel != "new CPU" {
		t.Fatalf("编辑备注名覆盖了并发更新：%+v", got)
	}
}
