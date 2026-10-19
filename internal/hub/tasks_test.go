package hub

// 下发测试任务的回归测试。
//
// 这条链路是：后台点一下 → 入队 → agent 上报时取走 → 执行 → 回传结果
// → 落库 + 解析摘要 → 卡片上显示。任何一段断了，表现都是"点了没反应"。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Vincentkeio/kokoro/internal/model"
	"github.com/Vincentkeio/kokoro/internal/store"
)

// TestDispatchAndDeliver 入队的任务会在下一次上报时被下发。
func TestDispatchAndDeliver(t *testing.T) {
	h, st := newTestHub(t)
	n := mkNode(t, st, "测试节点 JP", "JP", "日本 · 东京", "")

	task, err := h.DispatchTask(n.ID, "ipquality")
	if err != nil {
		t.Fatalf("下发失败: %v", err)
	}
	if task.Status != model.TaskQueued {
		t.Errorf("新任务状态 = %s，应为 queued", task.Status)
	}

	cmds := h.pendingCommands(n.ID, h.netqProvinces(), "", h.netqInterval())
	if len(cmds) != 1 {
		t.Fatalf("应下发 1 条命令，实际 %d", len(cmds))
	}
	c := cmds[0]
	if c.Type != "test" || len(c.Payload) != 1 || c.Payload["kind"] != "ipquality" {
		t.Fatalf("测试指令必须只包含固定类型：%+v", c)
	}

	// 取走之后不能重复下发
	if again := h.pendingCommands(n.ID, h.netqProvinces(), "", h.netqInterval()); len(again) != 0 {
		t.Errorf("同一任务被下发了两次: %+v", again)
	}

	// 状态要变成 running，否则卡片上看不出"在跑"
	got, err := st.GetTask(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != model.TaskRunning {
		t.Errorf("下发后状态 = %s，应为 running", got.Status)
	}
}

// TestTaskResultBecomesSummary 结果回传后要落库、生成摘要、写进动态流。
func TestTaskResultBecomesSummary(t *testing.T) {
	h, st := newTestHub(t)
	n := mkNode(t, st, "测试节点 JP", "JP", "日本 · 东京", "")
	task, _ := h.DispatchTask(n.ID, "bench")
	h.pendingCommands(n.ID, h.netqProvinces(), "", h.netqInterval()) // 取走，置 running

	// 模拟 agent 回传：JSON 前后夹着进度输出（真实脚本就是这样）
	stdout := `# ## ## ##  YABS ## ## ## ##
starting fio...
` + `{"geekbench":{"single":1234,"multi":4567},"fio":{"disk_read":812,"disk_write":503}}`

	h.finishTaskFromResult(model.CommandResult{
		ID: task.ID, OK: true, ExitCode: 0, Stdout: stdout,
	})

	got, err := st.GetTask(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != model.TaskDone {
		t.Fatalf("状态 = %s，应为 done（err=%s）", got.Status, got.Error)
	}
	for _, want := range []string{"1234", "4567", "812"} {
		if !strings.Contains(got.Summary, want) {
			t.Errorf("摘要 %q 里应包含 %s", got.Summary, want)
		}
	}
	if !strings.Contains(got.Detail, "YABS") {
		t.Error("原始输出应该完整存下来，供后台展开看")
	}

	// 动态流里要出现"跑分完成"
	evs, _ := st.ListEvents(10)
	found := false
	for _, e := range evs {
		if e.Kind == "task" && strings.Contains(e.Text, "硬件跑分") {
			found = true
		}
	}
	if !found {
		t.Errorf("动态流里没有任务完成事件: %+v", evs)
	}

	// 卡片上要显示摘要。
	//
	// ⚠️ 但**硬件跑分（bench）是例外** —— boss 要求卡片那一行只留
	// 线路和 IP：磁盘 IOPS、CPU 事件数对"要不要买"几乎没影响。
	// 所以这里再补一条线路测试，验证"该显示的那类会显示"。
	if err := st.CreateArticle(&model.Article{
		NodeID: n.ID, Title: "t", Summary: "s", ContentMD: "c",
	}); err != nil {
		t.Fatal(err)
	}
	mkRouteTask(t, h, st, n.ID)

	body := renderBody(t, h, httptest.NewRequest(http.MethodGet, "/", nil))
	if !strings.Contains(body, "task-line") {
		t.Error("首页卡片没有显示测试摘要")
	}
	if !strings.Contains(body, "回程") {
		t.Error("线路结果应该显示在卡片摘要上")
	}
	start := strings.Index(body, `<div class="task-line task-summary"`)
	if start < 0 {
		t.Fatal("卡片摘要容器缺失")
	}
	end := strings.Index(body[start:], `<div class="task-pop"`)
	if end < 0 {
		t.Fatal("测试结果浮窗缺失")
	}
	if strings.Contains(body[start:start+end], "4567") {
		t.Error("硬件跑分不该出现在卡片摘要里（详情页和浮窗里仍然有）")
	}
}

// mkRouteTask 塞一条三网线路测试的完成结果。
func mkRouteTask(t *testing.T, h *Hub, st *store.Store, nodeID string) {
	t.Helper()
	task := &model.NodeTask{NodeID: nodeID, Kind: "netquality", Title: "线路与三网质量",
		Cmd: "bash /tmp/kb.sh", Status: model.TaskQueued}
	if err := st.CreateTask(task); err != nil {
		t.Fatal(err)
	}
	out := `{"event":"result","test":"route","ok":true,"data":{"电信":{"line":"CN2 GIA","latency_ms":45.0}}}
{"event":"done","ok":true,"elapsed_ms":1000,"failed":[]}`
	h.finishTaskFromResult(model.CommandResult{ID: task.ID, OK: true, Stdout: out})
}

// TestTaskFailureIsRecorded 失败的测试要记成 failed 并带上原因，不能静默吞掉。
func TestTaskFailureIsRecorded(t *testing.T) {
	h, st := newTestHub(t)
	n := mkNode(t, st, "测试节点 JP", "JP", "日本 · 东京", "")
	task, _ := h.DispatchTask(n.ID, "netquality")
	h.pendingCommands(n.ID, h.netqProvinces(), "", h.netqInterval())

	h.finishTaskFromResult(model.CommandResult{
		ID: task.ID, OK: false, ExitCode: 1, Stderr: "curl: command not found",
	})

	got, err := st.GetTask(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != model.TaskFailed {
		t.Errorf("状态 = %s，应为 failed", got.Status)
	}
	if !strings.Contains(got.Error, "curl") {
		t.Errorf("失败原因没记下来: %q", got.Error)
	}

	// 失败也要进动态流，否则站长根本不知道跑挂了
	evs, _ := st.ListEvents(10)
	found := false
	for _, e := range evs {
		if e.Kind == "alert" && strings.Contains(e.Text, "失败") {
			found = true
		}
	}
	if !found {
		t.Error("失败的测试没有进动态流")
	}
}

// TestDispatchRejectsUnknownKind 未知类型要被拒，而不是下发一条空命令。
func TestDispatchRejectsUnknownKind(t *testing.T) {
	h, st := newTestHub(t)
	n := mkNode(t, st, "测试节点 JP", "JP", "日本 · 东京", "")
	if _, err := h.DispatchTask(n.ID, "rm-rf-everything"); err == nil {
		t.Error("未知测试类型应该报错")
	}
}

// TestNodeOnlineTransitionWritesEvent 上下线跃迁要写事件，且**只在跃迁时写**。
//
// 上报很频繁，每次都写会把事件表刷爆、动态流也没法看。
func TestNodeOnlineTransitionWritesEvent(t *testing.T) {
	_, st := newTestHub(t)
	n := mkNode(t, st, "测试节点 JP", "JP", "日本 · 东京", "")

	// 注册进来的节点默认就是**在线**的（agent 刚注册完），
	// 所以要先把它打成掉线，才看得到跃迁。
	_ = st.SetNodeOnline(n.ID, false) // 跃迁：写（掉线）
	_ = st.SetNodeOnline(n.ID, false) // 没变：不写
	_ = st.SetNodeOnline(n.ID, false) // 没变：不写
	_ = st.SetNodeOnline(n.ID, true)  // 跃迁：写（上线）

	evs, err := st.ListEvents(20)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 2 {
		t.Fatalf("应写 2 条事件（上线 + 掉线），实际 %d 条: %+v", len(evs), evs)
	}
	// 最新的一条是上线
	if evs[0].Kind != "online" || !strings.Contains(evs[0].Text, "上线") {
		t.Errorf("最新事件 = %+v，应为上线", evs[0])
	}
}

// TestHomeFeedMergesSources 动态流要把多路来源合成一条按时间倒序的时间线。
func TestHomeFeedMergesSources(t *testing.T) {
	h, st := newTestHub(t)
	n := mkNode(t, st, "测试节点 JP", "JP", "日本 · 东京", "")

	// 先掉线再上线，制造一次看得见的跃迁事件
	_ = st.SetNodeOnline(n.ID, false)
	_ = st.SetNodeOnline(n.ID, true)
	if err := st.AddComment(&model.Comment{
		NodeID: n.ID, Author: "阿宝", Content: "这台延迟真稳", Status: model.CommentApproved,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveProfile(&model.NodeProfile{
		NodeID: n.ID, Summary: "东京小鸡", ContentMD: "# 介绍",
	}); err != nil {
		t.Fatal(err)
	}

	// ⚠️ 动态流**不再渲染在首页上**（boss 要求撤掉那张卡片，
	// 位置换成了"全网速率"）。所以这里直接测函数本身，
	// 而不是去首页的 HTML 里找 —— 这样万一以后又把流接回某个页面，
	// 合流逻辑仍然是受测的。
	feed := h.homeFeed([]model.Node{*n}, 10)
	joined := ""
	for _, it := range feed {
		joined += it.Text + "\n"
	}
	for _, want := range []string{"上线了", "阿宝", "东京小鸡"} {
		if !strings.Contains(joined, want) {
			t.Errorf("动态流里缺少 %s，实际:\n%s", want, joined)
		}
	}
	// 留言必须**原文**出现（不是被截断）
	if !strings.Contains(joined, "这台延迟真稳") {
		t.Error("留言内容没进动态流")
	}
	// 未审核的留言不该出现
	if err := st.AddComment(&model.Comment{
		NodeID: n.ID, Author: "刷屏的", Content: "买茶叶加微信", Status: model.CommentPending,
	}); err != nil {
		t.Fatal(err)
	}
	body2 := renderBody(t, h, httptest.NewRequest(http.MethodGet, "/", nil))
	if strings.Contains(body2, "买茶叶加微信") {
		t.Error("未审核的留言不该出现在公开首页")
	}
}

// TestExtractJSON 脚本输出是"进度 + JSON"混合，得能切出 JSON 主体。
func TestExtractJSON(t *testing.T) {
	got := extractJSON("some progress\n{\"a\":1}\ntrailing")
	if got != `{"a":1}` {
		t.Errorf("extractJSON = %q", got)
	}
	if extractJSON("no json here") != "" {
		t.Error("没有 JSON 时应返回空串")
	}
	// 确保真的是合法 JSON
	var v map[string]any
	if err := json.Unmarshal([]byte(got), &v); err != nil {
		t.Errorf("切出来的不是合法 JSON: %v", err)
	}
}

// TestTestCommandOnlyTransmitsKind 固定测试类型不得携带执行参数。
func TestTestCommandOnlyTransmitsKind(t *testing.T) {
	h, st := newTestHub(t)
	for _, sc := range testScripts {
		n := mkNode(t, st, sc.Kind, "JP", "日本", "")
		if _, err := h.DispatchTask(n.ID, sc.Kind); err != nil {
			t.Fatal(err)
		}
		cmds := h.pendingCommands(n.ID, h.netqProvinces(), "", h.netqInterval())
		if len(cmds) != 1 || cmds[0].Type != "test" || len(cmds[0].Payload) != 1 || cmds[0].Payload["kind"] != sc.Kind {
			t.Fatalf("不能下发脚本、地址、参数或超时：%+v", cmds)
		}
	}
}

func TestLegacyTaskDoesNotDeliverShell(t *testing.T) {
	h, st := newTestHub(t)
	n := mkNode(t, st, "旧任务", "JP", "日本", "")
	task := &model.NodeTask{NodeID: n.ID, Kind: "bench", Title: "旧任务", Cmd: "touch /tmp/unauthorized-command", Status: model.TaskQueued}
	if err := st.CreateTask(task); err != nil {
		t.Fatal(err)
	}
	cmds := h.pendingCommands(n.ID, h.netqProvinces(), "", h.netqInterval())
	if len(cmds) != 1 || cmds[0].Type != "test" || len(cmds[0].Payload) != 1 || cmds[0].Payload["kind"] != "bench" {
		t.Fatalf("旧任务中的命令文本不能下发：%+v", cmds)
	}
}

func TestTestCommandDoesNotDependOnDomain(t *testing.T) {
	h, st := newTestHub(t)
	h.cfg.Domain = ""
	n := mkNode(t, st, "无域名", "JP", "日本", "")
	task, err := h.DispatchTask(n.ID, "ipquality")
	if err != nil || task.Cmd != "test:ipquality" {
		t.Fatalf("任务不应依赖域名或 shell：%+v %v", task, err)
	}
}
