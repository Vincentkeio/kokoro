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

	"github.com/kokoro-probe/kokoro/internal/model"
)

// TestDispatchAndDeliver 入队的任务会在下一次上报时被下发。
func TestDispatchAndDeliver(t *testing.T) {
	h, st := newTestHub(t)
	n := mkNode(t, st, "东京 zouter", "JP", "日本 · 东京", "")

	task, err := h.DispatchTask(n.ID, "ipquality")
	if err != nil {
		t.Fatalf("下发失败: %v", err)
	}
	if task.Status != model.TaskQueued {
		t.Errorf("新任务状态 = %s，应为 queued", task.Status)
	}

	cmds := h.pendingCommands(n.ID)
	if len(cmds) != 1 {
		t.Fatalf("应下发 1 条命令，实际 %d", len(cmds))
	}
	c := cmds[0]
	if c.Type != "shell" {
		t.Errorf("命令类型 = %s，应为 shell", c.Type)
	}
	if got := c.Payload["cmd"]; got == nil || !strings.Contains(got.(string), "IP.Check.Place") {
		t.Errorf("命令内容不对: %v", got)
	}
	// 超时要够跑脚本，默认 10 秒肯定不够
	if ms, _ := c.Payload["timeout_ms"].(int); ms < 300000 {
		t.Errorf("超时 %d ms 太短，跑不完测试脚本", ms)
	}

	// 取走之后不能重复下发
	if again := h.pendingCommands(n.ID); len(again) != 0 {
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
	n := mkNode(t, st, "东京 zouter", "JP", "日本 · 东京", "")
	task, _ := h.DispatchTask(n.ID, "bench")
	h.pendingCommands(n.ID) // 取走，置 running

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

	// 卡片上要显示摘要
	body := renderBody(t, h, httptest.NewRequest(http.MethodGet, "/", nil))
	if !strings.Contains(body, "task-line") || !strings.Contains(body, "4567") {
		t.Error("首页卡片没有显示测试摘要")
	}
}

// TestTaskFailureIsRecorded 失败的测试要记成 failed 并带上原因，不能静默吞掉。
func TestTaskFailureIsRecorded(t *testing.T) {
	h, st := newTestHub(t)
	n := mkNode(t, st, "东京 zouter", "JP", "日本 · 东京", "")
	task, _ := h.DispatchTask(n.ID, "netquality")
	h.pendingCommands(n.ID)

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
	n := mkNode(t, st, "东京 zouter", "JP", "日本 · 东京", "")
	if _, err := h.DispatchTask(n.ID, "rm-rf-everything"); err == nil {
		t.Error("未知测试类型应该报错")
	}
}

// TestNodeOnlineTransitionWritesEvent 上下线跃迁要写事件，且**只在跃迁时写**。
//
// 上报很频繁，每次都写会把事件表刷爆、动态流也没法看。
func TestNodeOnlineTransitionWritesEvent(t *testing.T) {
	_, st := newTestHub(t)
	n := mkNode(t, st, "东京 zouter", "JP", "日本 · 东京", "")

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
	n := mkNode(t, st, "东京 zouter", "JP", "日本 · 东京", "")

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

	body := renderBody(t, h, httptest.NewRequest(http.MethodGet, "/", nil))
	for _, want := range []string{"feed-item", "上线了", "阿宝", "东京小鸡"} {
		if !strings.Contains(body, want) {
			t.Errorf("动态流里缺少 %s", want)
		}
	}
	// 留言必须**原文**出现（不是被截断或转义坏掉）
	if !strings.Contains(body, "这台延迟真稳") {
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

// TestScriptsArePosixShellSafe 内置脚本必须能被 /bin/sh 执行。
//
// 踩过的坑：NetQuality 官方给的写法是 `bash <(curl -Ls Net.Check.Place) -j`，
// 进程替换 `<( )` 是 bash 扩展，Debian 的 /bin/sh 是 dash，
// 直接报 `Syntax error: "(" unexpected` —— 任务下发出去必然失败。
// 所以命令一律写成 `curl ... | bash -s -- -j` 这种 POSIX 管道形式。
func TestScriptsArePosixShellSafe(t *testing.T) {
	for _, sc := range testScripts {
		if strings.Contains(sc.Cmd, "<(") || strings.Contains(sc.Cmd, ">(") {
			t.Errorf("%s 的命令用了进程替换 <( )，/bin/sh 跑不了: %s", sc.Kind, sc.Cmd)
		}
		if strings.Contains(sc.Cmd, "[[") {
			t.Errorf("%s 的命令用了 bash 专有的 [[ ]]: %s", sc.Kind, sc.Cmd)
		}
		// 脚本第一次跑要装依赖，没 TTY 时读不到确认就会直接退出，
		// 所以 -y 是必须的（实测不带就是 "Lacking necessary dependencies" 然后失败）
		if !strings.Contains(sc.Cmd, "-y") {
			t.Errorf("%s 的命令没带 -y，非交互环境下装不了依赖: %s", sc.Kind, sc.Cmd)
		}
		if !strings.Contains(sc.Cmd, "bash") {
			t.Errorf("%s 的命令没有显式调 bash —— 这些脚本都是按 bash 写的", sc.Kind)
		}
		if sc.TimeoutMS < 300000 {
			t.Errorf("%s 的超时 %d ms 太短，跑不完", sc.Kind, sc.TimeoutMS)
		}
	}
}

// TestSummarizeIPQualityRealJSON 用**从真实机器上抓下来的** IPQuality 输出测解析。
//
// 这份结构是 2026-10-08 在 zouter 上跑 `curl -Ls IP.Check.Place | bash -s -- -j -y`
// 拿到的原文（只删了无关的 Mail 段）。用真实数据而不是自己编的，
// 才能发现"字段名猜错了"这类问题 —— 之前就是猜的。
func TestSummarizeIPQualityRealJSON(t *testing.T) {
	// 脚本会把 JSON 夹在彩色进度输出里，所以故意加前后噪声
	raw := "Lacking necessary dependencies...\n" +
		"[216.23.83.149]# 正在检测IP数据库 Maxmind ... 03%\r" +
		`{"Info":{"ASN":"AS3258","Organization":"xTom Japan","City":"Tokyo","Type":"机房"},
"Type":{"Usage":{"IPinfo":"机房"}},
"Score":{"IP2LOCATION":"3","SCAMALYTICS":"0","AbuseIPDB":"0","DBIP":"0","IPQS":"null"},
"Media":{"TikTok":{"Status":"解锁","Region":"JP","Type":"原生"},
"DisneyPlus":{"Status":"解锁","Region":"JP","Type":"原生"},
"Netflix":{"Status":"解锁","Region":"JP","Type":"原生"},
"Youtube":{"Status":"解锁","Region":"JP","Type":"原生"},
"AmazonPrimeVideo":{"Status":"失败","Region":"","Type":""},
"Reddit":{"Status":"解锁","Region":"JP","Type":"原生"},
"ChatGPT":{"Status":"解锁","Region":"JP","Type":"原生"}},
"Mail":{"Sohu":null,"DNSBlacklist":{"Total":423,"Clean":420,"Marked":3}}}` +
		"\n\n  评测结果已保存\n"

	got := summarizeTask("ipquality", raw)
	if got == "" {
		t.Fatal("解析结果为空 —— 摘要没生成")
	}
	t.Logf("摘要 = %s", got)

	// 7 个服务里 6 个解锁（AmazonPrimeVideo 是失败的）
	if !strings.Contains(got, "6/7") {
		t.Errorf("解锁计数不对，应为 6/7：%q", got)
	}
	if !strings.Contains(got, "Netflix") || !strings.Contains(got, "ChatGPT") {
		t.Errorf("应列出解锁的服务：%q", got)
	}
	if strings.Contains(got, "Prime") {
		t.Errorf("失败的服务不该出现在解锁列表里：%q", got)
	}
	if !strings.Contains(got, "风险分 0") {
		t.Errorf("应显示风险分：%q", got)
	}
	if !strings.Contains(got, "机房") {
		t.Errorf("应显示 IP 类型：%q", got)
	}
}
