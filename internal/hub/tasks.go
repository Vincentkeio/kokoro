package hub

// 从面板下发测试任务：Hub 入队 → Agent 领取固定测试指令 → 结果回传。
// 测试的执行流程、工具版本和参数白名单由 Agent 维护，Hub 不下发 shell 文本。

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Vincentkeio/kokoro/internal/model"
)

// testScript 是面板展示的测试项；执行参数只由 Agent 定义。
type testScript struct {
	Kind  string
	Title string
	Note  string
}

// testScripts 是内置的测试项。
var testScripts = []testScript{
	{
		Kind:  "bench",
		Title: "硬件跑分",
		Note:  "系统信息 + CPU、内存与 fio 磁盘读写，约 3~6 分钟",
	},
	{
		Kind:  "ipquality",
		Title: "IP 质量与解锁",
		Note:  "分别检测 IPv4 / IPv6 的 IP 类型、风险与流媒体解锁，约 6~16 分钟",
	},
	{
		Kind:  "netquality",
		Title: "线路与三网质量",
		Note:  "上下行测速 + IPv4 / IPv6 京沪深三网 TCP 线路，约 6~16 分钟",
	},
}

// testScriptByKind 按 kind 找脚本。
func testScriptByKind(kind string) (testScript, bool) {
	if kind == "netquality4" || kind == "netquality6" {
		family := "IPv4"
		if kind == "netquality6" {
			family = "IPv6"
		}
		return testScript{Kind: kind, Title: family + " 线路与三网质量"}, true
	}
	if kind == "ipquality4" || kind == "ipquality6" {
		family := "IPv4"
		if kind == "ipquality6" {
			family = "IPv6"
		}
		return testScript{Kind: kind, Title: family + " 质量与解锁"}, true
	}
	for _, s := range testScripts {
		if s.Kind == kind {
			return s, true
		}
	}
	return testScript{}, false
}

// DispatchTask 从后台入队一条测试任务。
func (h *Hub) DispatchTask(nodeID, kind string) (*model.NodeTask, error) {
	sc, ok := testScriptByKind(kind)
	if !ok {
		return nil, fmt.Errorf("未知的测试类型: %s", kind)
	}
	t := &model.NodeTask{
		NodeID: nodeID,
		Kind:   kind,
		Title:  sc.Title,
		Cmd:    "test:" + kind,
		Status: model.TaskQueued,
	}
	if err := h.store.CreateTask(t); err != nil {
		return nil, err
	}
	_ = h.store.AddEvent(nodeID, "task", "下发了测试："+sc.Title, t.ID)
	h.broadcastTaskProgress(t.ID)
	h.broadcastTaskCard(nodeID)
	return t, nil
}

// DispatchAll 一键下发全部测试。
//
// 返回实际入队的条数。已经排队/在跑的同类任务会被跳过 ——
// 连点两下不该把同一台机器上的跑分排两遍（那是二十分钟的 CPU）。
func (h *Hub) DispatchAll(nodeID string) (int, error) { return h.dispatchAllIP(nodeID, "ipquality") }
func (h *Hub) dispatchAllIP(nodeID, ipKind string) (int, error) {
	busy := map[string]bool{}
	if ts, err := h.store.ListTasks(nodeID, 30); err == nil {
		for _, t := range ts {
			if t.Status == model.TaskQueued || t.Status == model.TaskRunning {
				busy[t.Kind] = true
			}
		}
	}
	n := 0
	var firstErr error
	for _, sc := range testScripts {
		if sc.Kind == "ipquality" {
			sc.Kind = ipKind
			if busy["ipquality"] || (busy["ipquality4"] && busy["ipquality6"]) {
				continue
			}
			if sc.Kind == "ipquality" && busy["ipquality4"] {
				sc.Kind = "ipquality6"
			}
			if sc.Kind == "ipquality" && busy["ipquality6"] {
				sc.Kind = "ipquality4"
			}
		}
		if sc.Kind == "netquality" && ipKind != "ipquality" {
			sc.Kind = "netquality" + strings.TrimPrefix(ipKind, "ipquality")
		}
		if busy[sc.Kind] {
			continue
		}
		if _, err := h.DispatchTask(nodeID, sc.Kind); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		n++
	}
	return n, firstErr
}

// DispatchAllNodes 为所有服务器分别排入三项测试。每台机器内部仍由
// DispatchAll 与 agent 的单条领取机制保持串行；已有排队/运行中的同类任务会跳过。
func (h *Hub) DispatchAllNodes() (int, int, error) {
	nodes, err := h.store.ListNodes(true)
	if err != nil {
		return 0, 0, err
	}
	queued := 0
	var firstErr error
	for _, node := range nodes {
		n, err := h.DispatchAll(node.ID)
		queued += n
		if err != nil && firstErr == nil {
			firstErr = fmt.Errorf("%s: %w", node.Name, err)
		}
	}
	return len(nodes), queued, firstErr
}

// Validate the full selection before creating any tasks. A missing or stale
// selection never expands to all nodes.
func (h *Hub) DispatchSelectedNodes(ids []string) (int, int, error) {
	return h.dispatchSelectedIP(ids, "ipquality")
}
func (h *Hub) dispatchSelectedIP(ids []string, ipKind string) (int, int, error) {
	if len(ids) == 0 {
		return 0, 0, fmt.Errorf("请先选择要测试的服务器")
	}
	if len(ids) > 10000 {
		return 0, 0, fmt.Errorf("选中的服务器数量过多")
	}
	nodes, err := h.store.ListNodes(true)
	if err != nil {
		return 0, 0, err
	}
	available := make(map[string]model.Node, len(nodes))
	for _, node := range nodes {
		available[node.ID] = node
	}
	var selected []model.Node
	seen := map[string]bool{}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		node, ok := available[id]
		if !ok {
			return 0, 0, fmt.Errorf("选中的服务器已不存在，请刷新列表后重新选择")
		}
		if !seen[id] {
			selected = append(selected, node)
			seen[id] = true
		}
	}
	queued := 0
	var firstErr error
	for _, node := range selected {
		n, err := h.dispatchAllIP(node.ID, ipKind)
		queued += n
		if err != nil && firstErr == nil {
			firstErr = fmt.Errorf("%s: %w", node.Name, err)
		}
	}
	return len(selected), queued, firstErr
}

// pendingCommands 取该节点待下发的命令，转成 agent 能执行的 Command。
//
// 一次只取一条：跑分脚本动辄十几分钟，同机并发跑多个会互相抢 CPU，
// 结果全是错的。取到就置 running，避免下次上报重复下发。
func (h *Hub) pendingCommands(nodeID string, netqReported []string, catalogVer string, netqInterval int, heartbeat ...*model.TaskHeartbeat) []model.Command {
	cmds := []model.Command{}
	// 测试任务：一次一条，claim 到才算下发
	var task *model.NodeTask
	if len(heartbeat) > 0 && heartbeat[0] != nil {
		task, _ = h.store.RetryTaskDelivery(nodeID)
	}
	if task == nil {
		task, _ = h.store.ClaimQueuedTask(nodeID)
	}
	if t := task; t != nil {
		h.broadcastTaskProgress(t.ID)
		// Agent owns execution recipes and limits. Never transmit stored shell text,
		// including when an older queued task still has Cmd populated.
		if t.Kind == "swap" {
			if size, err := strconv.Atoi(strings.TrimPrefix(t.Cmd, "swap:")); strings.HasPrefix(t.Cmd, "swap:") && err == nil && (size == 0 || (size >= 128 && size <= maxManagedSwapSizeMB)) {
				cmds = append(cmds, model.Command{ID: t.ID, Type: "swap", Payload: map[string]any{"size_mb": size}})
			}
		} else {
			cmds = append(cmds, model.Command{
				ID: t.ID, Type: "test",
				Payload: map[string]any{"kind": t.Kind},
			})
		}
	}
	// 三网探测范围与间隔：agent 报的和设置不一致才发
	if c := h.netqTargetsCommand(netqReported, netqInterval); c != nil {
		cmds = append(cmds, *c)
	}
	// 探测点目录覆盖：agent 报的目录版本和 Hub 的不一致才发
	// （只在后台设过覆盖件时才可能触发）
	if c := h.netqCatalogCommand(catalogVer); c != nil {
		cmds = append(cmds, *c)
	}
	if len(cmds) == 0 {
		return nil
	}
	return cmds
}

// finishTaskFromResult 处理 agent 回传的结果：落库 + 生成摘要 + 写动态。
func (h *Hub) finishTaskFromResult(res model.CommandResult) error {
	t, err := h.store.GetTask(res.ID)
	if err != nil {
		return err
	}
	if t == nil {
		return nil
	}
	ok := res.OK && res.ExitCode == 0
	summary := summarizeTask(t.Kind, res.Stdout)
	errMsg := ""
	report := parseBenchNDJSON(res.Stdout)
	seen := map[string]bool{}
	for _, item := range report.Items {
		seen[item.Test] = true
		if !item.OK {
			ok = false
			errMsg += benchLabel(item.Test) + "：" + firstNonEmpty(item.Err, "测试失败") + "；"
		}
	}
	if report.Structured {
		expected := map[string][]string{"bench": {"disk", "cpu", "memory"}, "ipquality": {"ip"}, "netquality": {"net", "route"}}
		if t.Kind == "netquality4" {
			expected[t.Kind] = []string{"net", "route4"}
		}
		if t.Kind == "netquality6" {
			expected[t.Kind] = []string{"net", "route6"}
		}
		if t.Kind == "netquality" && !seen["route"] {
			expected[t.Kind] = []string{"net", "route4", "route6"}
		}
		if t.Kind == "ipquality4" {
			expected[t.Kind] = []string{"ip4"}
		}
		if t.Kind == "ipquality6" {
			expected[t.Kind] = []string{"ip6"}
		}
		if t.Kind == "ipquality" && !seen["ip"] {
			expected["ipquality"] = []string{"ip4", "ip6"}
		}
		for _, test := range expected[t.Kind] {
			if !seen[test] {
				ok = false
				errMsg += benchLabel(test) + "：未返回有效测试结果；"
			}
		}
	}
	if !ok {
		if errMsg == "" {
			errMsg = strings.TrimSpace(res.Stderr)
		}
		if errMsg == "" {
			errMsg = fmt.Sprintf("退出码 %d", res.ExitCode)
		}
		errMsg = clampRunes(errMsg, 300)
	}
	detail := clampRunes(res.Stdout, 200000) // 跑分原始输出可能很大，留够但不无限
	changed, err := h.store.FinishTask(res.ID, summary, detail, errMsg, ok)
	if err != nil {
		return err
	}
	if !changed {
		return nil
	}
	h.broadcastTaskProgress(res.ID)
	text := t.Title + "完成"
	if summary != "" {
		text = t.Title + "完成：" + summary
	}
	kind := "task"
	if !ok {
		kind, text = "alert", t.Title+"失败："+errMsg
	}
	_ = h.store.AddEvent(t.NodeID, kind, text, res.ID)
	h.broadcastTaskCard(t.NodeID)
	return nil
}

// broadcastTaskProgress 将排队、开始、完成或失败状态推送到管理后台消息条。
func (h *Hub) broadcastTaskProgress(taskID string) {
	if h.broker == nil || taskID == "" {
		return
	}
	t, err := h.store.GetTask(taskID)
	if err != nil || t == nil {
		return
	}
	node, err := h.store.GetNode(t.NodeID)
	if err != nil || node == nil {
		return
	}
	message := node.Name + "：" + t.Title
	switch t.Status {
	case model.TaskQueued:
		message += "已排队，等待节点领取"
	case model.TaskRunning:
		message += "已开始执行"
	case model.TaskDone:
		message += "执行成功"
		if t.Summary != "" {
			message += "：" + t.Summary
		}
	case model.TaskFailed:
		message += "执行失败"
		if t.Error != "" {
			message += "：" + t.Error
		}
	case model.TaskCancelled:
		message += "已取消"
	default:
		return
	}
	h.broker.broadcast("task_status", map[string]any{
		"node": t.NodeID, "task_id": t.ID, "title": t.Title,
		"status": t.Status, "message": message,
	})
	h.broadcastTaskHistory(t.NodeID)
	h.broadcastTaskList()
}

func (h *Hub) broadcastTaskList() {
	if h.broker == nil {
		return
	}
	if html := h.renderAdminTaskRows(); html != "" {
		h.broker.broadcastAdmin("*", "task_list", map[string]string{"html": html})
	}
}

// broadcastTaskHistory refreshes the private task history shown on the admin
// test page. It uses a separate authenticated stream because task details can
// include host output and must not be sent over the public metrics SSE.
func (h *Hub) broadcastTaskHistory(nodeID string) {
	if h.broker == nil || nodeID == "" {
		return
	}
	html, ok := h.renderTaskHistory(nodeID)
	if !ok {
		return
	}
	h.broker.broadcastAdmin(nodeID, "task_history", map[string]string{
		"node": nodeID,
		"html": html,
	})
}

func (h *Hub) cancelledTaskIDs(nodeID string, heartbeat *model.TaskHeartbeat) []string {
	if heartbeat == nil || len(heartbeat.IDs) == 0 {
		return nil
	}
	var ids []string
	for _, id := range heartbeat.IDs {
		task, err := h.store.GetTask(id)
		if err == nil && task != nil && task.NodeID == nodeID && task.Status == model.TaskCancelled {
			ids = append(ids, id)
		}
	}
	return ids
}

func (h *Hub) broadcastNodeTaskConnection(nodeID string, online bool) {
	if h.broker == nil {
		return
	}
	tasks, err := h.store.ListTasks(nodeID, 20)
	if err != nil {
		return
	}
	node, err := h.store.GetNode(nodeID)
	if err != nil || node == nil {
		return
	}
	for _, t := range tasks {
		if t.Status != model.TaskQueued && t.Status != model.TaskRunning {
			continue
		}
		message := node.Name + "：" + t.Title
		if online {
			message += "：节点已恢复上报，正在确认任务进度"
		} else {
			message += "：连续 20 秒未收到上报，节点离线，等待恢复"
		}
		h.broker.broadcast("task_status", map[string]any{
			"node": t.NodeID, "task_id": t.ID, "title": t.Title,
			"status": "waiting", "message": message,
		})
	}
}

// broadcastTaskCard 将新结果推到首页对应节点卡片，避免访客必须手动刷新。
func (h *Hub) broadcastTaskCard(nodeID string) {
	if h.broker == nil || nodeID == "" {
		return
	}
	// The task partial also renders online/offline copy. Load the current node
	// state instead of constructing a zero-valued Node, which would make every
	// live task update claim that the node is offline.
	node, err := h.store.GetNode(nodeID)
	if err != nil || node == nil {
		return
	}
	card := nodeCard{Node: *node}
	h.populateTaskCard(&card)
	h.broker.broadcast("task", map[string]any{
		"node":   nodeID,
		"card":   h.renderPartial("task_card.html", &card),
		"uptime": h.renderPartial("uptime.html", h.loadUptime(nodeID)),
	})
}

// summarizeTask 从脚本输出里抽一句话，显示在卡片上。
//
// 现在脚本输出的是 **NDJSON**（每行一个事件），直接逐行解析即可。
// 老任务存的是第三方脚本那种「彩色输出里夹一个 JSON」，留着兼容分支 ——
// 历史任务的摘要是算好存库的，不用重算，但手动重跑时还能用。
func summarizeTask(kind, stdout string) string {
	out := strings.TrimSpace(stdout)
	if out == "" {
		return ""
	}
	if kind == "swap" {
		return out
	}
	// 新格式是 NDJSON，但脚本/系统可能在事件前写入提示或进度行。
	// 解析器会逐行忽略非 JSON 内容，因此不要要求首字节就是 '{'。
	if rep := parseBenchNDJSON(out); len(rep.Items) > 0 {
		if summary := summarizeBench(rep); summary != "" {
			return summary
		}
	}
	// 老格式：在混合输出里找最外层 JSON
	if js := extractJSON(out); js != "" {
		var doc map[string]any
		if err := json.Unmarshal([]byte(js), &doc); err == nil {
			return summarizeJSON(kind, doc)
		}
	}
	return ""
}

// extractJSON 从混合输出里切出最外层的 JSON 对象。
//
// 脚本会先打印一堆进度（彩色），JSON 夹在中间或末尾，不能直接 Unmarshal。
// 做法是找第一个 '{' 和最后一个 '}'。
func extractJSON(s string) string {
	i := strings.IndexByte(s, '{')
	j := strings.LastIndexByte(s, '}')
	if i < 0 || j <= i {
		return ""
	}
	return s[i : j+1]
}

// summarizeJSON 按类型从 JSON 里挑关键字段拼一句摘要。
func summarizeJSON(kind string, doc map[string]any) string {
	switch kind {
	case "bench":
		// YABS 的 JSON 结构随版本变，这里按"扁平找键"的方式容错：
		// 不假设层级，全树搜名字里带关键词的数值。
		var gbSingle, gbMulti, diskW, diskR float64
		walkNumbers(doc, func(key string, v float64) {
			k := strings.ToLower(key)
			switch {
			case strings.Contains(k, "single") && gbSingle == 0:
				gbSingle = v
			case strings.Contains(k, "multi") && gbMulti == 0:
				gbMulti = v
			case strings.Contains(k, "disk") && strings.Contains(k, "write") && diskW == 0:
				diskW = v
			case strings.Contains(k, "disk") && strings.Contains(k, "read") && diskR == 0:
				diskR = v
			}
		})
		var parts []string
		if gbSingle > 0 {
			parts = append(parts, fmt.Sprintf("Geekbench 单核 %.0f", gbSingle))
		}
		if gbMulti > 0 {
			parts = append(parts, fmt.Sprintf("多核 %.0f", gbMulti))
		}
		if diskR > 0 || diskW > 0 {
			parts = append(parts, fmt.Sprintf("磁盘 读 %.0f / 写 %.0f MB/s", diskR, diskW))
		}
		return strings.Join(parts, " · ")

	case "ipquality":
		// 字段名来自测试 VPS 实际运行 IPQuality -j 的输出：
		//   Info.Type      IP 类型（机房 / 住宅 / 商业…）
		//   Score.SCAMALYTICS / AbuseIPDB / DBIP   风险分
		//   Media.<服务>.Status = "解锁" / 其它
		// 之前是按关键词猜的，容易抽错；现在按真实结构取。
		var parts []string
		if media, ok := doc["Media"].(map[string]any); ok && len(media) > 0 {
			keys := make([]string, 0, len(media))
			for k := range media {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			var unlocked []string
			for _, k := range keys {
				svc, _ := media[k].(map[string]any)
				st, _ := svc["Status"].(string)
				if st == "解锁" {
					unlocked = append(unlocked, unlockShortName(k))
				}
			}
			parts = append(parts, fmt.Sprintf("解锁 %d/%d", len(unlocked), len(keys)))
			if len(unlocked) > 0 {
				parts = append(parts, strings.Join(unlocked, " "))
			}
		}
		if sc, ok := doc["Score"].(map[string]any); ok {
			if v, ok := sc["SCAMALYTICS"].(string); ok && v != "" && v != "null" {
				parts = append(parts, "风险分 "+v)
			}
		}
		if info, ok := doc["Info"].(map[string]any); ok {
			if t, ok := info["Type"].(string); ok && t != "" {
				parts = append(parts, t)
			}
		}
		return strings.Join(parts, " · ")

	case "netquality":
		var parts []string
		walkStrings(doc, func(key, val string) {
			k := strings.ToLower(key)
			if (strings.Contains(k, "route") || strings.Contains(k, "line") || strings.Contains(k, "asn")) &&
				val != "" && len(parts) < 3 {
				parts = append(parts, val)
			}
		})
		return strings.Join(parts, " · ")
	}
	return ""
}

// walkNumbers 递归遍历 JSON，把数值字段喂给 fn。
//
// 之所以"扁平找键"而不是按固定路径取值：这些脚本的 JSON 结构
// 版本间会变，写死路径一升级就失效；按关键词模糊匹配更耐操。
func walkNumbers(v any, fn func(key string, val float64)) {
	switch x := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if f, ok := toFloat(x[k]); ok {
				fn(k, f)
			} else {
				walkNumbers(x[k], fn)
			}
		}
	case []any:
		for _, it := range x {
			walkNumbers(it, fn)
		}
	}
}

// walkStrings 同上，但只喂字符串字段。
func walkStrings(v any, fn func(key, val string)) {
	switch x := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if s, ok := x[k].(string); ok {
				fn(k, s)
			} else {
				walkStrings(x[k], fn)
			}
		}
	case []any:
		for _, it := range x {
			walkStrings(it, fn)
		}
	}
}

// walkBools 同上，但只喂布尔字段。
func walkBools(v any, fn func(key string, val bool)) {
	switch x := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if b, ok := x[k].(bool); ok {
				fn(k, b)
			} else {
				walkBools(x[k], fn)
			}
		}
	case []any:
		for _, it := range x {
			walkBools(it, fn)
		}
	}
}

// unlockShortName 把脚本里的服务名缩成卡片上好读的短名。
func unlockShortName(k string) string {
	switch k {
	case "DisneyPlus":
		return "Disney+"
	case "AmazonPrimeVideo":
		return "Prime"
	case "Youtube":
		return "YouTube"
	}
	return k
}

// isUnlockKey 判断这个布尔键是不是"某个服务解锁了"。
func isUnlockKey(key string) bool {
	k := strings.ToLower(key)
	for _, name := range []string{"netflix", "disney", "youtube", "chatgpt", "openai",
		"tiktok", "prime", "hbo", "spotify", "unlock", "available"} {
		if strings.Contains(k, name) {
			return true
		}
	}
	return false
}

func toFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int:
		return float64(x), true
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	}
	return 0, false
}

// 供模板显示：脚本清单 + 最近任务
var _ = time.Now
