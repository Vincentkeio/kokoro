package hub

// 从面板下发测试任务：Hub 入队 → Agent 拉走执行 → 结果回传 → 卡片显示摘要。
//
// 命令通道本身早就铺好了（model.Command + agent 的 shell 执行器 + 结果回传路由），
// 这里补的是 Hub 侧：脚本清单、入队、下发、结果落库与摘要解析。
//
// 选脚本的原则（调研结论）：**优先原生支持 -j / --json 输出的**。
// 彩色表格看着漂亮但没法稳定解析，一个空格变化就解析崩了。
//
// ⚠️ **必须带 `-y`**：脚本第一次跑要装依赖（jq/mtr/iperf3/bc/stun/nexttrace…），
// 它会先问一句"要不要装"，而 agent 跑命令时**没有 TTY**，读不到确认就直接退出。
// 实测：不带 -y 的表现就是"Lacking necessary dependencies"然后失败。
//
// ⚠️ 命令一律写成 **POSIX 管道形式**（`curl ... | bash -s -- -j`），
// 不要用 `bash <(curl ...) -j` 这种进程替换：agent 默认用 /bin/sh 执行，
// 而 Debian 上 /bin/sh 是 dash，**不支持 <(...)**，会直接报
// "Syntax error: "(" unexpected"。

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/kokoro-probe/kokoro/internal/model"
)

// testScript 是一个可下发的测试脚本。
type testScript struct {
	Kind      string
	Title     string
	Cmd       string
	TimeoutMS int
	Note      string
}

// testScripts 是内置脚本清单。
//
// 三个都是社区最流行的，且**都支持 -j 输出 JSON**：
//
//	bench      YABS（fio 磁盘 + iperf3 测速 + Geekbench CPU）
//	ipquality  xykt/IPQuality（IP 类型/风险分/流媒体解锁）
//	netquality xykt/NetQuality（BGP 线路 + 三网延迟 + 回程路由 + 测速）
//
// ⚠️ NodeQuality 只是这几个脚本的**外壳**，输出是 Markdown 表格，
// 反而不如直接调 xykt 的原脚本加 -j 好解析。所以这里不用它。
//
// 超时按实测放宽：YABS 含 Geekbench 要十几分钟。
var testScripts = []testScript{
	{
		Kind:      "bench",
		Title:     "硬件跑分",
		Cmd:       `curl -sL yabs.sh | bash -s -- -j -y`,
		TimeoutMS: 1800000,
		Note:      "fio 磁盘 IO + iperf3 测速 + Geekbench CPU，约 10~20 分钟",
	},
	{
		Kind:      "ipquality",
		Title:     "IP 质量与解锁",
		Cmd:       `curl -Ls IP.Check.Place | bash -s -- -j -y`,
		TimeoutMS: 900000,
		Note:      "IP 类型/原生 IP/风险分 + Netflix、Disney+、ChatGPT 等解锁检测，约 3~8 分钟",
	},
	{
		Kind:      "netquality",
		Title:     "线路与三网质量",
		Cmd:       `curl -Ls Net.Check.Place | bash -s -- -j -y`,
		TimeoutMS: 900000,
		Note:      "三网延迟/回程线路（CN2 GIA、9929、4837）+ 路由，约 5~10 分钟",
	},
}

// testScriptByKind 按 kind 找脚本。
func testScriptByKind(kind string) (testScript, bool) {
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
		Cmd:    sc.Cmd,
		Status: model.TaskQueued,
	}
	if err := h.store.CreateTask(t); err != nil {
		return nil, err
	}
	_ = h.store.AddEvent(nodeID, "task", "下发了测试："+sc.Title, t.ID)
	return t, nil
}

// DispatchAll 一键下发全部测试。
//
// 返回实际入队的条数。已经排队/在跑的同类任务会被跳过 ——
// 连点两下不该把同一台机器上的跑分排两遍（那是二十分钟的 CPU）。
func (h *Hub) DispatchAll(nodeID string) (int, error) {
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

// pendingCommands 取该节点待下发的命令，转成 agent 能执行的 Command。
//
// 一次只取一条：跑分脚本动辄十几分钟，同机并发跑多个会互相抢 CPU，
// 结果全是错的。取到就置 running，避免下次上报重复下发。
func (h *Hub) pendingCommands(nodeID string) []model.Command {
	t, err := h.store.ClaimQueuedTask(nodeID)
	if err != nil || t == nil {
		return nil
	}
	sc, ok := testScriptByKind(t.Kind)
	timeout := 600000
	if ok {
		timeout = sc.TimeoutMS
	}
	return []model.Command{{
		ID:   t.ID,
		Type: "shell",
		Payload: map[string]any{
			"cmd":        t.Cmd,
			"timeout_ms": timeout,
		},
	}}
}

// finishTaskFromResult 处理 agent 回传的结果：落库 + 生成摘要 + 写动态。
func (h *Hub) finishTaskFromResult(res model.CommandResult) {
	t, err := h.store.GetTask(res.ID)
	if err != nil || t == nil {
		return // 不是我们发的任务（或已被清理），忽略
	}
	ok := res.OK && res.ExitCode == 0
	summary := summarizeTask(t.Kind, res.Stdout)
	errMsg := ""
	if !ok {
		errMsg = strings.TrimSpace(res.Stderr)
		if errMsg == "" {
			errMsg = fmt.Sprintf("退出码 %d", res.ExitCode)
		}
		errMsg = clampRunes(errMsg, 300)
	}
	detail := clampRunes(res.Stdout, 200000) // 跑分原始输出可能很大，留够但不无限
	if err := h.store.FinishTask(res.ID, summary, detail, errMsg, ok); err != nil {
		return
	}
	text := t.Title + "完成"
	if summary != "" {
		text = t.Title + "完成：" + summary
	}
	kind := "task"
	if !ok {
		kind, text = "alert", t.Title+"失败："+errMsg
	}
	_ = h.store.AddEvent(t.NodeID, kind, text, res.ID)
}

// summarizeTask 从脚本输出里抽一句话，显示在卡片上。
//
// 三个脚本都输出 JSON，所以先按 JSON 抽字段；抽不到再退回纯文本启发式。
// **不追求完美**：抽不出来就留空，卡片上不显示这一行，
// 比硬凑一句错误的话要好。
func summarizeTask(kind, stdout string) string {
	out := strings.TrimSpace(stdout)
	if out == "" {
		return ""
	}
	// 脚本会在 JSON 前后打印进度，先把 JSON 主体切出来
	if js := extractJSON(out); js != "" {
		var doc map[string]any
		if err := json.Unmarshal([]byte(js), &doc); err == nil {
			if s := summarizeJSON(kind, doc); s != "" {
				return s
			}
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
		// 字段名来自**实测**（2026-10-08 在 zouter 上真跑了一遍 IPQuality -j）：
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
