package store

import (
	"sync"
	"testing"

	"github.com/Vincentkeio/kokoro/internal/model"
)

func TestProfileViewsAndEditsPreserveEachOther(t *testing.T) {
	s := openTmp(t)
	if err := s.CreateNode(&model.Node{ID: "visits", Name: "Visits", TokenHash: "visits"}); err != nil {
		t.Fatal(err)
	}
	initial := &model.NodeProfile{NodeID: "visits", Price: "old", ExpireAt: "2026-11-01", PV: 5, UpdatedAt: 1234}
	if err := s.SaveProfile(initial); err != nil {
		t.Fatal(err)
	}
	staleEditor, err := s.GetProfile(initial.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	const views = 24
	var workers sync.WaitGroup
	for i := 0; i < views; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			if err := s.IncrementProfilePV(initial.NodeID); err != nil {
				t.Error(err)
			}
		}()
	}
	workers.Wait()
	staleEditor.Price, staleEditor.ExpireAt, staleEditor.UpdatedAt = "new", "2026-12-01", 5678
	if err := s.SaveProfile(staleEditor); err != nil {
		t.Fatal(err)
	}
	if err := s.IncrementProfilePV(initial.NodeID); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetProfile(initial.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Price != "new" || got.ExpireAt != "2026-12-01" || got.PV != initial.PV+views+1 || got.UpdatedAt != 5678 {
		t.Fatalf("visits and edits overwrote each other: %+v", got)
	}
}
