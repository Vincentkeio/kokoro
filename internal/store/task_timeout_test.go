package store

import (
	"testing"
	"time"

	"github.com/Vincentkeio/kokoro/internal/model"
)

func TestTaskDeadlinesAllowFullAgentExecutionAndResultUpload(t *testing.T) {
	for _, kind := range []string{"bench", "ipquality", "ipquality4", "ipquality6", "netquality", "netquality4", "netquality6"} {
		t.Run(kind, func(t *testing.T) {
			s := openTmp(t)
			task := &model.NodeTask{ID: "deadline", NodeID: "node", Kind: kind, Cmd: "test:" + kind, Status: model.TaskRunning}
			if err := s.CreateTask(task); err != nil {
				t.Fatal(err)
			}
			deadline := model.BuiltinTestTimeout(kind)
			if deadline <= 0 {
				t.Fatal("missing shared execution timeout")
			}
			if _, err := s.db.Exec(`UPDATE node_tasks SET started_at=? WHERE id=?`, nowMS()-(deadline+time.Minute).Milliseconds(), task.ID); err != nil {
				t.Fatal(err)
			}
			heartbeat := &model.TaskHeartbeat{IDs: []string{task.ID}}
			if ended, err := s.ReconcileTaskHeartbeat(task.NodeID, heartbeat); err != nil || len(ended) != 0 {
				t.Fatalf("execution/upload period cut short: %v %v", ended, err)
			}
			if _, err := s.db.Exec(`UPDATE node_tasks SET started_at=? WHERE id=?`, nowMS()-(deadline+3*time.Minute).Milliseconds(), task.ID); err != nil {
				t.Fatal(err)
			}
			if ended, err := s.ReconcileTaskHeartbeat(task.NodeID, heartbeat); err != nil || len(ended) != 1 {
				t.Fatalf("stuck task never expired: %v %v", ended, err)
			}
		})
	}
}
