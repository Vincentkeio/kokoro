package hub

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/Vincentkeio/kokoro/internal/model"
)

func TestSwapGiBToMiB(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  int
		ok    bool
	}{{"0", 0, false}, {"0.5", 512, true}, {"2", 2048, true}, {"512", 512 * 1024, true}, {"0.25", 0, false}, {"513", 0, false}, {"NaN", 0, false}, {"abc", 0, false}} {
		got, err := swapGiBToMiB(tc.input)
		if (err == nil) != tc.ok || got != tc.want {
			t.Errorf("swapGiBToMiB(%q) = (%d, %v), want (%d, ok=%v)", tc.input, got, err, tc.want, tc.ok)
		}
	}
}

func TestDispatchSwapUsesStructuredCommand(t *testing.T) {
	h, st := newTestHub(t)
	n := mkNode(t, st, "测试节点 JP", "JP", "日本 · 东京", "")
	task, err := h.DispatchSwap(n.ID, 2048)
	if err != nil {
		t.Fatal(err)
	}
	if task.Kind != "swap" || task.Cmd != "swap:2048" {
		t.Fatalf("unexpected queued swap task: %+v", task)
	}
	commands := h.pendingCommands(n.ID, h.netqProvinces(), "", h.netqInterval())
	if len(commands) != 1 || commands[0].Type != "swap" || commands[0].Payload["size_mb"] != 2048 {
		t.Fatalf("swap task did not become a structured command: %+v", commands)
	}
	if got, err := st.GetTask(task.ID); err != nil || got.Status != model.TaskRunning {
		t.Fatalf("swap task should be marked running after delivery: got=%+v err=%v", got, err)
	}
}

func TestDispatchSwapRejectsTargetBelowReportedCurrentCapacity(t *testing.T) {
	h, st := newTestHub(t)
	n := mkNode(t, st, "zgo · 洛杉矶", "US", "美国 · 洛杉矶", "")
	if err := st.InsertMetrics(n.ID, &model.Metrics{
		V: 1, Ts: 1, Mem: model.MemStat{SwapTotal: 4 << 30},
	}); err != nil {
		t.Fatal(err)
	}
	_, err := h.DispatchSwap(n.ID, 3*1024)
	if err == nil || !strings.Contains(err.Error(), "不能执行") || !strings.Contains(err.Error(), "4.0 GiB") {
		t.Fatalf("target below current capacity should be rejected with a clear message, got %v", err)
	}
	tasks, listErr := st.ListTasks(n.ID, 10)
	if listErr != nil || len(tasks) != 0 {
		t.Fatalf("rejected swap request must not create a task: tasks=%+v err=%v", tasks, listErr)
	}
}

func TestAdminCanQueueSwapForSelectedNode(t *testing.T) {
	h, st := newTestHub(t)
	n := mkNode(t, st, "测试节点 JP", "JP", "日本 · 东京", "")
	rec := postToAdmin(t, h, url.Values{
		"action":   {"swap"},
		"id":       {n.ID},
		"swap_gib": {"1.5"},
	})
	if rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "taskmsg=") {
		t.Fatalf("SWAP 表单应反馈并返回节点页，status=%d location=%q", rec.Code, rec.Header().Get("Location"))
	}
	tasks, err := st.ListTasks(n.ID, 10)
	if err != nil || len(tasks) != 1 || tasks[0].Kind != "swap" || tasks[0].Cmd != "swap:1536" || tasks[0].Status != model.TaskQueued {
		t.Fatalf("SWAP 应按 MiB 排入该节点队列：tasks=%+v err=%v", tasks, err)
	}
}
