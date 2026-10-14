package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/Vincentkeio/kokoro/internal/model"
)

func TestTaskDeliverySerialAndRecovery(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	first := &model.NodeTask{NodeID: "nd_test", Kind: "bench", Cmd: "test:bench", CreatedAt: 1}
	second := &model.NodeTask{NodeID: "nd_test", Kind: "ipquality", Cmd: "test:ipquality", CreatedAt: 2}
	for _, task := range []*model.NodeTask{first, second} {
		if err := s.CreateTask(task); err != nil {
			t.Fatal(err)
		}
	}
	claimed, err := s.ClaimQueuedTask(first.NodeID)
	if err != nil || claimed.ID != first.ID {
		t.Fatalf("claim: %+v %v", claimed, err)
	}
	if next, err := s.ClaimQueuedTask(first.NodeID); err != nil || next != nil {
		t.Fatal("more than one task delivered before first finishes")
	}
	s.db.Exec(`UPDATE node_tasks SET delivered_at=0 WHERE id=?`, first.ID)
	retry, err := s.RetryTaskDelivery(first.NodeID)
	if err != nil || retry == nil || retry.ID != first.ID {
		t.Fatal("lost delivery must reuse original task ID")
	}
	if _, err := s.ReconcileTaskHeartbeat(first.NodeID, &model.TaskHeartbeat{IDs: []string{first.ID}}); err != nil {
		t.Fatal(err)
	}
	s.db.Exec(`UPDATE node_tasks SET delivered_at=0 WHERE id=?`, first.ID)
	if retry, err := s.RetryTaskDelivery(first.NodeID); err != nil || retry != nil {
		t.Fatal("acknowledged work must not be redelivered")
	}
	ended, err := s.ReconcileTaskHeartbeat(first.NodeID, &model.TaskHeartbeat{})
	if err != nil || len(ended) != 1 || ended[0] != first.ID {
		t.Fatal("lost acknowledged task must finish as interrupted")
	}
	task, _ := s.GetTask(first.ID)
	if task.Status != model.TaskFailed || task.Error == "" {
		t.Fatal("interruption was not visible")
	}
	if next, err := s.ClaimQueuedTask(first.NodeID); err != nil || next == nil || next.ID != second.ID {
		t.Fatal("interrupted task blocks next queued test")
	}
	changed, err := s.FinishTask(second.ID, "CPU 300", "result", "", true)
	if err != nil || !changed {
		t.Fatal("could not save result")
	}
	changed, err = s.FinishTask(second.ID, "wrong", "wrong", "wrong", false)
	if err != nil || changed {
		t.Fatal("duplicate upload overwrote saved result")
	}
}

func TestLegacyTaskHasDeadline(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	task := &model.NodeTask{NodeID: "nd_test", Kind: "netquality", Cmd: "test:netquality"}
	s.CreateTask(task)
	s.ClaimQueuedTask(task.NodeID)
	s.db.Exec(`UPDATE node_tasks SET started_at=? WHERE id=?`, nowMS()-(23*time.Minute).Milliseconds(), task.ID)
	ended, err := s.ReconcileTaskHeartbeat(task.NodeID, nil)
	if err != nil || len(ended) != 1 {
		t.Fatal("legacy running record never expires")
	}
}

func TestPersistedResultCanReturnAfterLongOutage(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	task := &model.NodeTask{NodeID: "nd_test", Kind: "bench", Cmd: "test:bench"}
	s.CreateTask(task)
	s.ClaimQueuedTask(task.NodeID)
	s.db.Exec(`UPDATE node_tasks SET started_at=? WHERE id=?`, nowMS()-(2*time.Hour).Milliseconds(), task.ID)
	ended, err := s.ReconcileTaskHeartbeat(task.NodeID, &model.TaskHeartbeat{IDs: []string{task.ID}, ResultIDs: []string{task.ID}})
	if err != nil || len(ended) != 0 {
		t.Fatal("persisted completed result was discarded after outage")
	}
	changed, err := s.FinishTask(task.ID, "CPU 321", "result", "", true)
	if err != nil || !changed {
		t.Fatal("late completed result was not saved")
	}
}
