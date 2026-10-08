package hub

// kokoro-bench 的 NDJSON 解析测试。
//
// 用的是 2026-10-08 在 zouter 上**真跑**出来的输出原文 ——
// 用真实数据而不是自己编的，才能发现"字段名猜错了"这类问题。
// 之前用第三方脚本时就是靠猜，结果摘要一直是空的。

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Vincentkeio/kokoro/internal/model"
)

const realBenchNDJSON = `{"event":"start","schema":1,"tests":["sysinfo","disk","cpu"]}
{"event":"result","test":"sysinfo","ok":true,"data":{"cpu":"Intel(R) Xeon(R) Platinum 8272CL CPU @ 2.60GHz","cores":1,"mem_total":1014341632,"disk_total":15764295680,"os":"Debian GNU/Linux 13 (trixie)","virt":"kvm"}}
{"event":"progress","test":"disk","pct":10,"msg":"准备磁盘测试"}
{"event":"progress","test":"disk","pct":30,"msg":"fio 顺序读写"}
{"event":"result","test":"disk","ok":true,"data":{"seq_read_mbs":1387,"seq_write_mbs":1388,"rand4k_read_iops":208799.210039,"rand4k_write_iops":208712.664367,"tool":"fio"}}
{"event":"progress","test":"cpu","pct":50,"msg":"sysbench 单核"}
{"event":"result","test":"cpu","ok":true,"data":{"single_eps":1029.53,"multi_eps":1027.95,"threads":1,"tool":"sysbench"}}
{"event":"done","ok":true,"elapsed_ms":61319,"failed":[]}
`

// TestParseBenchNDJSON 逐行解析要能挑出 result 事件、跳过 progress。
func TestParseBenchNDJSON(t *testing.T) {
	rep := parseBenchNDJSON(realBenchNDJSON)
	if len(rep.Items) != 3 {
		t.Fatalf("应解析出 3 项，实际 %d: %+v", len(rep.Items), rep.Items)
	}
	if rep.ElapsedM != 61319 {
		t.Errorf("耗时 = %d，应为 61319", rep.ElapsedM)
	}
	d := rep.get("disk")
	if d == nil {
		t.Fatal("没解析出 disk 项")
	}
	if v, ok := numOf(d, "seq_read_mbs"); !ok || v != 1387 {
		t.Errorf("磁盘读 = %v，应为 1387", v)
	}
	// progress 行不该被当成结果
	for _, it := range rep.Items {
		if it.Test == "progress" || it.Test == "" {
			t.Errorf("混进了非结果事件: %+v", it)
		}
	}
}

// TestSummarizeBenchRealOutput 摘要要能压成一行、且带上关键数字。
func TestSummarizeBenchRealOutput(t *testing.T) {
	got := summarizeTask("bench", realBenchNDJSON)
	t.Logf("摘要 = %s", got)
	if got == "" {
		t.Fatal("摘要为空 —— 卡片上就什么都看不到了")
	}
	// 磁盘读原样保留；CPU 是 1029.53，显示时四舍五入成 1030
	for _, want := range []string{"1387", "1030"} {
		if !strings.Contains(got, want) {
			t.Errorf("摘要 %q 里应包含 %s", got, want)
		}
	}
}

// TestSummarizeBenchUnlock 解锁项的摘要要能数出解锁了几个。
func TestSummarizeBenchUnlock(t *testing.T) {
	raw := `{"event":"result","test":"ip","ok":true,"data":{"ip_type":"机房","unlock_total":7,"unlocked":["Netflix","Disney+","ChatGPT"],"locked":["Prime"]}}
{"event":"done","ok":true,"elapsed_ms":90000,"failed":[]}`
	got := summarizeTask("ipquality", raw)
	t.Logf("摘要 = %s", got)
	if !strings.Contains(got, "3/7") {
		t.Errorf("应显示解锁 3/7：%q", got)
	}
	if !strings.Contains(got, "机房") {
		t.Errorf("应显示 IP 类型：%q", got)
	}
}

// TestParseBenchToleratesGarbage 认不出来的行要跳过，不能整份丢掉。
func TestParseBenchToleratesGarbage(t *testing.T) {
	raw := "随便打的一行\n" + realBenchNDJSON + "又一行杂质\n"
	rep := parseBenchNDJSON(raw)
	if len(rep.Items) != 3 {
		t.Errorf("杂质行不该影响解析，实际解析出 %d 项", len(rep.Items))
	}
	if parseBenchNDJSON("").Items != nil {
		t.Error("空输入应返回空报告，不该 panic")
	}
}

// TestNodePageShowsBenchResult 详情页要真的把测试结果渲染出来。
//
// boss 提的问题就是：卡片上有了，点进详情页什么都没有。
func TestNodePageShowsBenchResult(t *testing.T) {
	h, st := newTestHub(t)
	n := mkNode(t, st, "东京 zouter", "JP", "日本 · 东京", "")
	task := &model.NodeTask{
		NodeID: n.ID, Kind: "bench", Title: "硬件跑分",
		Cmd: "bash /tmp/kb.sh --only sysinfo,disk,cpu", Status: model.TaskQueued,
	}
	if err := st.CreateTask(task); err != nil {
		t.Fatal(err)
	}
	h.pendingCommands(n.ID) // 取走 → running
	h.finishTaskFromResult(model.CommandResult{
		ID: task.ID, OK: true, ExitCode: 0, Stdout: realBenchNDJSON,
	})

	// 摘要要落库（卡片靠它）
	saved, _ := st.GetTask(task.ID)
	if saved.Summary == "" {
		t.Fatal("摘要是空的 —— 卡片上显示不出来")
	}
	if !strings.Contains(saved.Summary, "1387") {
		t.Errorf("摘要里应有磁盘速度：%q", saved.Summary)
	}

	// 详情页要能看到每一项
	body := renderBody(t, h, httptest.NewRequest(http.MethodGet, "/n/"+n.Slug, nil))
	for _, want := range []string{"测试结果", "磁盘 IO", "顺序读", "1387", "CPU", "1029", "系统信息"} {
		if !strings.Contains(body, want) {
			t.Errorf("详情页缺少 %q", want)
		}
	}
}

// TestBenchFieldsStableOrder 字段顺序要稳定。
//
// Go 模板里 range map 的顺序是随机的，每次刷新字段位置都在跳 ——
// 所以是在 Go 里排好序再交给模板的。这条钉住它。
func TestBenchFieldsStableOrder(t *testing.T) {
	d := map[string]any{
		"seq_read_mbs":  float64(1387),
		"seq_write_mbs": float64(1388),
		"tool":          "fio",
	}
	var first string
	for i := 0; i < 20; i++ {
		f := benchFields("disk", d)
		var labels []string
		for _, kv := range f {
			labels = append(labels, kv[0])
		}
		got := strings.Join(labels, ",")
		if i == 0 {
			first = got
		} else if got != first {
			t.Fatalf("字段顺序不稳定：第 %d 次 %q ≠ 首次 %q", i, got, first)
		}
	}
	if !strings.HasPrefix(first, "顺序读,顺序写") {
		t.Errorf("字段顺序应按关注度排：%q", first)
	}
}

// TestBenchFieldValueNoMapDump 嵌套对象不能直接 fmt.Sprint 出来。
//
// 踩过：详情页上出现了
//
//	「城市  map[Name:null PostalCode:null SubCode:N/A Subdivisions:N/A]」
//
// 和「risk_dbip  <nil>」—— 一个把 Go 的 map 内部结构暴露给了用户，
// 一个是 nil 没被跳过。
func TestBenchFieldValueNoMapDump(t *testing.T) {
	d := map[string]any{
		"city":      map[string]any{"Name": nil, "PostalCode": nil, "SubCode": "N/A"},
		"risk_dbip": nil,
		"country":   "JP",
	}
	f := benchFields("ip", d)
	for _, kv := range f {
		if strings.Contains(kv[1], "map[") {
			t.Errorf("把 Go map 打印给用户看了: %s = %s", kv[0], kv[1])
		}
		if strings.Contains(kv[1], "<nil>") || strings.Contains(kv[1], "null") {
			t.Errorf("空值没被处理: %s = %s", kv[0], kv[1])
		}
		if kv[0] == "risk_dbip" {
			t.Errorf("值为 nil 的字段不该展示")
		}
	}
	// 有 Name 的嵌套对象优先取 Name
	got := benchFieldValue("", map[string]any{"Name": "东京", "Other": 1})
	if got != "东京" {
		t.Errorf("嵌套对象应优先取 Name，实际 %q", got)
	}
}

// TestBenchFieldsSkipBlankPlaceholders 上游返回的字面 "null" 不该显示给用户。
//
// 实测 IPQuality 的 city 是 {"Name":"null","PostalCode":"null",...} ——
// 用的是**字符串 "null"** 而不是 JSON null，直接显示就是「城市 null」。
func TestBenchFieldsSkipBlankPlaceholders(t *testing.T) {
	d := map[string]any{
		"city":    map[string]any{"Name": "null", "PostalCode": "null", "SubCode": "N/A"},
		"country": "",
		"org":     "Zouter Limited",
	}
	for _, kv := range benchFields("ip", d) {
		if strings.Contains(strings.ToLower(kv[1]), "null") {
			t.Errorf("显示了占位值 null: %s = %s", kv[0], kv[1])
		}
		if kv[0] == "城市" {
			t.Errorf("全空的嵌套对象不该展示: %s = %s", kv[0], kv[1])
		}
	}
	for _, v := range []string{"", "null", "N/A", "  ", "-", "None"} {
		if !isBlankValue(v) {
			t.Errorf("%q 应被判为空值", v)
		}
	}
	if isBlankValue("Tokyo") {
		t.Error("正常城市名不该被判为空值")
	}
}

// TestCardShowsTaskPopup 卡片上的摘要要带悬浮浮窗，里面是完整结果。
//
// boss 的原话：「这个悬浮弹窗不够详细…比如流媒体解锁情况，
// 比如是否是机房 IP 等」。所以浮窗里必须有这些，不能只有一行摘要。
func TestCardShowsTaskPopup(t *testing.T) {
	h, st := newTestHub(t)
	n := mkNode(t, st, "东京 zouter", "JP", "日本 · 东京", "")
	task := &model.NodeTask{
		NodeID: n.ID, Kind: "ipquality", Title: "IP 质量与解锁",
		Cmd: "bash /tmp/kb.sh --only ip", Status: model.TaskQueued,
	}
	if err := st.CreateTask(task); err != nil {
		t.Fatal(err)
	}
	h.pendingCommands(n.ID)
	h.finishTaskFromResult(model.CommandResult{
		ID: task.ID, OK: true, ExitCode: 0,
		Stdout: `{"event":"result","test":"ip","ok":true,"data":{"ip_type":"机房","org":"Zouter Limited","asn":"205548","risk_scamalytics":0,"unlock_total":7,"unlocked":["Netflix","Disney+","ChatGPT"],"locked":["Prime"]}}
{"event":"done","ok":true,"elapsed_ms":90000,"failed":[]}`,
	})

	body := renderBody(t, h, httptest.NewRequest(http.MethodGet, "/", nil))
	for _, want := range []string{
		"task-hover", "task-pop", // 浮窗容器
		"IP 质量与解锁",                       // 标题
		"机房", "Zouter Limited", "205548", // 是不是机房 IP、谁的 IP
		// ⚠️ 这里断言 "Disney" 而不是 "Disney+"：
		// Go 的 html/template 会把 `+` 转义成 `&#43;`（浏览器显示仍是 Disney+），
		// 直接找 "Disney+" 会假失败。
		"✅", "Netflix", "Disney", "ChatGPT", // 解锁了哪些
		"❌", "Prime", // 没解锁哪些
	} {
		if !strings.Contains(body, want) {
			t.Errorf("卡片浮窗里缺少 %q", want)
		}
	}
}

// TestCardPopupMergesAllTasks 浮窗要把最近几项测试**合并**，不是只显示最新一条。
//
// 踩过：循环里写成 `if c.TaskSummary != "" { continue }`，
// 摘要确实只该取最新一条，但这一 continue 把后续任务的
// **项收集也一起跳过了** —— 浮窗里只剩最新那一次的项。
func TestCardPopupMergesAllTasks(t *testing.T) {
	h, st := newTestHub(t)
	n := mkNode(t, st, "东京 zouter", "JP", "日本 · 东京", "")

	for _, tc := range []struct{ kind, title, out string }{
		{"bench", "硬件跑分", `{"event":"result","test":"disk","ok":true,"data":{"seq_read_mbs":1387}}
{"event":"done","ok":true,"elapsed_ms":1000,"failed":[]}`},
		{"ipquality", "IP 质量与解锁", `{"event":"result","test":"ip","ok":true,"data":{"ip_type":"机房","unlock_total":7,"unlocked":["Netflix"]}}
{"event":"done","ok":true,"elapsed_ms":1000,"failed":[]}`},
	} {
		task := &model.NodeTask{NodeID: n.ID, Kind: tc.kind, Title: tc.title,
			Cmd: "x", Status: model.TaskQueued}
		if err := st.CreateTask(task); err != nil {
			t.Fatal(err)
		}
		h.pendingCommands(n.ID)
		h.finishTaskFromResult(model.CommandResult{ID: task.ID, OK: true, Stdout: tc.out})
	}

	body := renderBody(t, h, httptest.NewRequest(http.MethodGet, "/", nil))
	// 两次测试的项都要在浮窗里出现
	for _, want := range []string{"磁盘 IO", "1387", "IP 质量与解锁", "机房", "Netflix"} {
		if !strings.Contains(body, want) {
			t.Errorf("浮窗里缺少 %q —— 合并逻辑又只留了最新一条", want)
		}
	}
}
