package hub

import (
	"strings"
	"testing"

	"github.com/Vincentkeio/kokoro/internal/model"
)

func TestPartialTestResultsRemainVisible(t *testing.T) {
	h, st := newTestHub(t)
	n := mkNode(t, st, "partial", "JP", "日本", "")
	task, err := h.DispatchTask(n.ID, "bench")
	if err != nil {
		t.Fatal(err)
	}
	out := `{"event":"result","test":"cpu","ok":true,"data":{"single_eps":321,"multi_eps":654,"tool":"sysbench"}}` + "\n" +
		`{"event":"result","test":"memory","ok":false,"error":"sysbench 安装失败"}`
	if err := h.finishTaskFromResult(model.CommandResult{ID: task.ID, OK: true, Stdout: out}); err != nil {
		t.Fatal(err)
	}
	saved, _ := st.GetTask(task.ID)
	if saved.Status != model.TaskFailed || !strings.Contains(saved.Error, "安装失败") {
		t.Fatal("item failure incorrectly reported successful")
	}
	card := nodeCard{Node: *n}
	h.populateTaskCard(&card)
	var cpuFound, memoryFailure bool
	for _, item := range card.TaskItems {
		if item.Label == benchLabel("cpu") && item.OK && len(item.Fields) > 0 {
			cpuFound = true
		}
		if item.Label == benchLabel("memory") && !item.OK && strings.Contains(item.Err, "安装失败") {
			memoryFailure = true
		}
	}
	if !cpuFound || !memoryFailure {
		t.Fatalf("popover lost successful CPU or memory failure: %+v", card.TaskItems)
	}
	events, _ := st.ListEvents(100)
	if err := h.finishTaskFromResult(model.CommandResult{ID: task.ID, OK: true, Stdout: out}); err != nil {
		t.Fatal(err)
	}
	after, _ := st.ListEvents(100)
	if len(after) != len(events) {
		t.Fatal("retried result emitted duplicate events")
	}
}

func TestMissingRouteResultIsNotReportedSuccessful(t *testing.T) {
	h, st := newTestHub(t)
	n := mkNode(t, st, "missing route", "HK", "香港", "")
	task, _ := h.DispatchTask(n.ID, "netquality")
	out := `{"event":"result","test":"net","ok":true,"data":{"down_mbps":80}}` + "\n" +
		`{"event":"result","test":"route","ok":true,"data":}` + "\n" +
		`{"event":"done","ok":true,"elapsed_ms":1}`
	if err := h.finishTaskFromResult(model.CommandResult{ID: task.ID, OK: true, Stdout: out}); err != nil {
		t.Fatal(err)
	}
	saved, _ := st.GetTask(task.ID)
	if saved.Status != model.TaskFailed || !strings.Contains(saved.Error, "回程") {
		t.Fatal("missing route report was silently marked done")
	}
}

func TestRunningTaskKeepsCompletedNumbersInCard(t *testing.T) {
	h, st := newTestHub(t)
	n := mkNode(t, st, "refresh results", "HK", "香港", "")
	task, _ := h.DispatchTask(n.ID, "bench")
	out := `{"event":"result","test":"cpu","ok":true,"data":{"single_eps":321,"multi_eps":654}}` + "\n" +
		`{"event":"result","test":"disk","ok":true,"data":{"seq_read_mbs":100,"seq_write_mbs":90}}` + "\n" +
		`{"event":"result","test":"memory","ok":true,"data":{"read_mib_s":200,"write_mib_s":180}}`
	if err := h.finishTaskFromResult(model.CommandResult{ID: task.ID, OK: true, Stdout: out}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.DispatchTask(n.ID, "bench"); err != nil {
		t.Fatal(err)
	}
	h.pendingCommands(n.ID, h.netqProvinces(), "", h.netqInterval()) // 领取任务后才进入 running
	card := nodeCard{Node: *n}
	h.populateTaskCard(&card)
	body := h.renderPartial("task_card.html", &card)
	if !card.TaskRunning || !strings.Contains(string(body), "321") || !strings.Contains(string(body), "测试进行中") {
		t.Fatalf("refreshing task hides last completed results: running=%v offline=%v summary=%q body=%q", card.TaskRunning, card.TaskOffline, card.TaskSummary, body)
	}
}

func TestCompactCardRoutePrefersKnownObservations(t *testing.T) {
	line := "北京 路径 ASN 不足，未识别 / 上海 163 骨干（AS4134） / 深圳 163 骨干（AS4134）"
	got := compactCardRoute(line)
	if got != "163 骨干" {
		t.Fatalf("compact route should keep known backbones once: %q", got)
	}
	if got := compactCardRoute("CN2 GIA（AS4809）"); got != "CN2 GIA（AS4809）" {
		t.Fatal("single route was rewritten")
	}
}

func TestCarrierSummaryKeepsBackboneSlashes(t *testing.T) {
	got := routeSummaryByCarrier([]string{
		"回程 电信 未识别 / 163 骨干",
		"回程 联通 4837/169",
		"回程 移动 CMIN2",
	})
	if got["电信"] != "未识别 / 163 骨干" || got["联通"] != "4837/169" || got["移动"] != "CMIN2" {
		t.Fatalf("carrier summary truncated: %#v", got)
	}
	legacy := routeSummaryByCarrier([]string{"回程 电信 163 / 联通 4837/169 / 移动 CMI"})
	if legacy["电信"] != "163" || legacy["联通"] != "4837/169" || legacy["移动"] != "CMI" {
		t.Fatalf("legacy carrier summary changed: %#v", legacy)
	}
}
