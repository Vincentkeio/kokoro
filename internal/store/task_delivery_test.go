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
	s.db.Exec(`UPDATE node_tasks SET started_at=? WHERE id=?`, nowMS()-(38*time.Minute).Milliseconds(), task.ID)
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

func TestDeleteNodeRemovesQueuedTasks(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	n := &model.Node{ID: "nd_delete", Name: "Delete me", Online: true}
	if err := s.CreateNode(n); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateTask(&model.NodeTask{ID: "tk_orphan", NodeID: n.ID, Kind: "bench", Cmd: "test:bench"}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteNode(n.ID); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM node_tasks WHERE node_id=?`, n.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("节点删除后仍有 %d 条幽灵任务", count)
	}
}

func TestOfflineWatchDoesNotOverwriteFreshHeartbeatOrVisibility(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	n := &model.Node{ID: "nd_race", Name: "Race", Online: true, LastSeen: nowMS() - time.Hour.Milliseconds(), Visibility: model.VisibilityPrivate}
	if err := s.CreateNode(n); err != nil {
		t.Fatal(err)
	}
	if err := s.TouchNode(n.ID, nowMS(), ""); err != nil {
		t.Fatal(err)
	}
	changed, err := s.MarkNodeOfflineIfStale(n.ID, nowMS()-time.Minute.Milliseconds(), nowMS()+time.Minute.Milliseconds())
	if err != nil || changed {
		t.Fatalf("新心跳不应被过期扫描覆盖：changed=%v err=%v", changed, err)
	}
	got, err := s.GetNode(n.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Online || got.Visibility != model.VisibilityPrivate {
		t.Fatalf("在线或可见性被错误覆盖：online=%v visibility=%s", got.Online, got.Visibility)
	}
}
