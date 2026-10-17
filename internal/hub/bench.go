package hub

// 解析 kokoro-bench.sh 的 NDJSON 输出。
//
// 脚本的设计原则是「输出即接口」：stdout 每行一个 JSON 事件，
// 所以这里读起来是**逐行解析**，不用在彩色终端输出里捞 JSON。
//
//   {"event":"start",   "tests":["disk","cpu"]}
//   {"event":"progress","test":"disk","pct":30,"msg":"fio 顺序读写"}
//   {"event":"result",  "test":"disk","ok":true,"data":{...}}
//   {"event":"result",  "test":"net", "ok":false,"error":"测速失败"}
//   {"event":"done",    "ok":true,"elapsed_ms":41200,"failed":[]}

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// benchItem 是脚本里一个测试项的结果。
type benchItem struct {
	Test string
	OK   bool
	Data map[string]any
	Err  string
}

// benchReport 是整份测试报告。
type benchReport struct {
	Items      []benchItem
	Failed     []string
	ElapsedM   int64
	Structured bool
}

// taskKindRank 是"测试结果"的展示顺序：先系统硬件，再三网质量，最后 IP 质量。
// 谁先跑完不该决定谁排前面 —— 先看清机器什么配置，再看线路，最后看 IP 质量，
// 跟人的阅读顺序一致（boss 2026-10-17）。认不出的类型排在最后。
func taskKindRank(kind string) int {
	switch {
	case kind == "bench" || kind == "swap":
		return 0
	case strings.HasPrefix(kind, "netquality"):
		return 1
	case strings.HasPrefix(kind, "ipquality"):
		return 2
	default:
		return 3
	}
}

// benchErrorDuplicated 判断任务级错误是否已被某个条目的错误覆盖。
//
// Hub 会把失败条目的原因同时写进任务级 Error（finishTaskFromResult），
// 两处都渲染就是同一句话说两遍 —— 悬浮面板里曾出现"IP 质量与解锁 失败 /
// IPv6 质量与解锁：…" 连着两遍（boss 2026-10-17 报过）。
func benchErrorDuplicated(items []benchItem, msg string) bool {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return false
	}
	for _, it := range items {
		e := strings.TrimSpace(it.Err)
		if e == "" {
			continue
		}
		if e == msg || strings.Contains(e, msg) || strings.Contains(msg, e) {
			return true
		}
	}
	return false
}

// parseBenchNDJSON 逐行解析脚本输出。
//
// 认不出来的行**直接跳过**：脚本可能中途打印别的东西，
// 为了几行杂质把整份结果丢掉不值得。
func parseBenchNDJSON(s string) *benchReport {
	rep := &benchReport{}
	if strings.TrimSpace(s) == "" {
		return rep
	}
	seen := map[string]bool{}
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var ev struct {
			Event     string          `json:"event"`
			Test      string          `json:"test"`
			OK        bool            `json:"ok"`
			Data      json.RawMessage `json:"data"`
			Error     string          `json:"error"`
			ElapsedMS int64           `json:"elapsed_ms"`
			Failed    []string        `json:"failed"`
		}
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}
		switch ev.Event {
		case "start", "progress":
			rep.Structured = true
		case "result":
			rep.Structured = true
			if ev.Test == "" || seen[ev.Test] {
				continue // 同一个测试项重复上报时保留第一条
			}
			seen[ev.Test] = true
			it := benchItem{Test: ev.Test, OK: ev.OK, Err: ev.Error}
			if len(ev.Data) > 0 {
				_ = json.Unmarshal(ev.Data, &it.Data)
			}
			rep.Items = append(rep.Items, it)
		case "done":
			rep.Structured = true
			rep.ElapsedM = ev.ElapsedMS
			rep.Failed = ev.Failed
		}
	}
	return rep
}

// has 判断报告里有没有某一项（且成功）。
func (r *benchReport) get(test string) map[string]any {
	for _, it := range r.Items {
		if it.Test == test && it.OK {
			return it.Data
		}
	}
	return nil
}

// summarizeBench 把测试结果压成卡片上的一行字。
//
// 分项拼：能抽出来的就写，抽不出来就不写 —— 宁可短，不要凑一句错的。
func summarizeBench(rep *benchReport) string {
	var parts []string

	// ⚠️ 硬件跑分**保留在这里**，由卡片那一层去过滤。
	// 摘要函数是共用的：详情页和浮窗要看完整结果，
	// 只有卡片那一行按 boss 要求只留线路和 IP。
	// 在这里删掉的话，详情页也跟着少一块。
	if d := rep.get("disk"); d != nil {
		if v, ok := numOf(d, "seq_read_mbs"); ok && v > 0 {
			w, _ := numOf(d, "seq_write_mbs")
			parts = append(parts, fmt.Sprintf("磁盘 读 %.0f / 写 %.0f MB/s", v, w))
		}
	}
	if d := rep.get("cpu"); d != nil {
		if v, ok := numOf(d, "single_eps"); ok && v > 0 {
			parts = append(parts, fmt.Sprintf("CPU %.0f 事件/秒", v))
		}
	}
	// 带宽数字**不放摘要里**（boss 要求换成 IP 质量和用途）。
	//
	// 理由：下行多少 Mbps 是测速那一刻的瞬时值，受测速节点和当时网络影响很大，
	// 放卡片上参考价值低；而"这 IP 干不干净、是机房还是家宽"才是买家
	// 扫一眼就要判断的事。带宽数字在详情页和浮窗里仍然看得到。
	for _, test := range []string{"ip", "ip4", "ip6"} {
		if d := rep.get(test); d != nil {
			firstPart := len(parts)
			// IP 质量等级放最前面 —— 这是买家扫一眼卡片最想知道的事，
			// 从最安全的绿到最坏的屏蔽。
			if g := gradeIP(d); g != nil {
				parts = append(parts, ipGradeEmoji(g.Level)+" IP质量:"+g.Label)
			}
			if u, ok := d["unlocked"].([]any); ok {
				total := 0
				if t, ok := numOf(d, "unlock_total"); ok {
					total = int(t)
				}
				parts = append(parts, fmt.Sprintf("流媒体解锁:%d/%d", len(u), total))
			}
			if t := firstNonEmpty(strOf(d, "usage"), strOf(d, "ip_type")); t != "" {
				parts = append(parts, "性质: "+t)
			}
			if test != "ip" && len(parts) > firstPart {
				family := "IPv4"
				if test == "ip6" {
					family = "IPv6"
				}
				parts = append(parts[:firstPart], family+" "+strings.Join(parts[firstPart:], " "))
			}
		}
	}
	if d := rep.get("route"); d != nil {
		// 回程线路只报"走了哪条骨干"，三网一样时合并成一条，
		// 免得摘要里挤三遍同一个词
		var lines, labeledLines []string
		for _, isp := range []string{"电信", "联通", "移动"} {
			sub, _ := d[isp].(map[string]any)
			if l := strOf(sub, "line"); l != "" {
				lines = append(lines, l)
				labeledLines = append(labeledLines, isp+" "+l)
			}
		}
		if u := uniqStrings(lines); len(u) == 1 {
			parts = append(parts, "回程 "+u[0])
		} else if len(labeledLines) > 0 {
			parts = append(parts, "回程 "+strings.Join(labeledLines, " / "))
		}
	}
	if len(parts) == 0 {
		// 一项都没抽出来时退回系统信息 —— 至少让卡片上有点东西
		if d := rep.get("sysinfo"); d != nil {
			if cpu, ok := d["cpu"].(string); ok && cpu != "" {
				return "CPU " + clampRunes(cpu, 40)
			}
		}
		return ""
	}
	// 段内用空格连，不用 " · "：卡片上这两组是**两行**（线路一行、IP 一行），
	// 行内再用分隔符堆就显得很吵。
	return strings.Join(parts, " ")
}

// summarizeBenchCard 为首页卡片提取线路、IP 与硬件跑分；完整明细仍由
// summarizeBench 和 TaskItems 提供给详情页与悬浮面板。
func summarizeBenchCard(rep *benchReport) string {
	if rep == nil {
		return ""
	}
	var routeLines []string
	var cpuLine, diskLine, memoryLine string
	var ipLines []string
	hasHardwareTest := false
	for _, item := range rep.Items {
		switch item.Test {
		case "sysinfo", "disk", "cpu", "memory":
			hasHardwareTest = true
		}
	}
	if hasHardwareTest {
		cpuLine, memoryLine, diskLine = "CPU 跑分 —", "内存读写 —", "磁盘读写 —"
	}
	if d := rep.get("route"); d != nil {
		for _, isp := range []string{"电信", "联通", "移动"} {
			sub, _ := d[isp].(map[string]any)
			if line := cardBackboneName(strings.TrimSpace(strOf(sub, "line"))); line != "" {
				line = strings.TrimSpace(strings.TrimPrefix(line, isp))
				routeLines = append(routeLines, "回程 "+isp+" "+line)
			}
		}
	}
	for _, test := range []string{"ip", "ip4", "ip6"} {
		if d := rep.get(test); d != nil {
			var parts []string
			if grade := gradeIP(d); grade != nil {
				parts = append(parts, "IP质量 "+grade.Label)
			}
			if unlocked, ok := d["unlocked"].([]any); ok {
				total := 0
				if value, ok := numOf(d, "unlock_total"); ok {
					total = int(value)
				}
				parts = append(parts, fmt.Sprintf("解锁 %d/%d", len(unlocked), total))
			}
			if usage := firstNonEmpty(strOf(d, "usage"), strOf(d, "ip_type")); usage != "" {
				parts = append(parts, usage)
			}
			prefix := ""
			if test == "ip4" {
				prefix = "IPv4 "
			}
			if test == "ip6" {
				prefix = "IPv6 "
			}
			ipLines = append(ipLines, prefix+strings.Join(parts, " · "))
		} else if test != "ip" {
			for _, item := range rep.Items {
				if item.Test == test && !item.OK {
					family := "IPv4"
					if test == "ip6" {
						family = "IPv6"
					}
					ipLines = append(ipLines, family+" IP质量 检测失败 / 不可用")
				}
			}
		}
	}
	if d := rep.get("cpu"); d != nil {
		var scores []string
		if value, ok := numOf(d, "single_eps"); ok && value > 0 {
			scores = append(scores, fmt.Sprintf("%.0f", value))
		}
		if value, ok := numOf(d, "multi_eps"); ok && value > 0 {
			scores = append(scores, fmt.Sprintf("%.0f", value))
		}
		if len(scores) > 0 {
			cpuLine = "CPU 跑分 " + strings.Join(scores, "/")
		} else if value, ok := numOf(d, "sha256_mbs"); ok && value > 0 {
			cpuLine = fmt.Sprintf("CPU SHA-256 %.1f MB/s", value)
		}
	}
	if d := rep.get("disk"); d != nil {
		if read, readOK := numOf(d, "seq_read_mbs"); readOK {
			if write, writeOK := numOf(d, "seq_write_mbs"); writeOK {
				diskLine = fmt.Sprintf("磁盘读写 %.0f / %.0f MB/s", read, write)
			} else {
				diskLine = fmt.Sprintf("磁盘读取 %.0f MB/s", read)
			}
		} else if write, writeOK := numOf(d, "seq_write_mbs"); writeOK {
			diskLine = fmt.Sprintf("磁盘写入 %.0f MB/s", write)
		}
	}
	// Keep legacy field aliases, but display only measured bandwidth values.
	for _, item := range rep.Items {
		if !item.OK || item.Data == nil {
			continue
		}
		isMemoryTest := strings.Contains(strings.ToLower(item.Test), "mem")
		read, hasRead := firstMemoryRate(item.Data, isMemoryTest, "read")
		write, hasWrite := firstMemoryRate(item.Data, isMemoryTest, "write")
		if hasRead || hasWrite {
			switch {
			case hasRead && hasWrite:
				memoryLine = fmt.Sprintf("内存读写 %.0f / %.0f MB/s", read, write)
			case hasRead:
				memoryLine = fmt.Sprintf("内存读取 %.0f MB/s", read)
			default:
				memoryLine = fmt.Sprintf("内存写入 %.0f MB/s", write)
			}
			break
		}
	}
	var rows []string
	if cpuLine != "" {
		rows = append(rows, cpuLine)
	}
	if memoryLine != "" {
		rows = append(rows, memoryLine)
	}
	if diskLine != "" {
		rows = append(rows, diskLine)
	}
	rows = append(rows, routeLines...)
	rows = append(rows, ipLines...)
	return strings.Join(rows, "\n")
}

// City-by-city trace details belong in the popover. Each carrier's card shows
// the first optimized backbone, falling back to its first recognized value.
func compactCardRoute(line string) string {
	parts := strings.Split(line, " / ")
	if len(parts) < 2 {
		if unknownCardRoute(line) {
			return "未知"
		}
		return line
	}
	var compact []string
	for _, part := range parts {
		for _, city := range []string{"北京 ", "上海 ", "深圳 ", "广东 "} {
			if strings.HasPrefix(part, city) {
				part = strings.TrimPrefix(part, city)
				break
			}
		}
		if unknownCardRoute(part) {
			compact = append(compact, "未知")
			continue
		}
		switch {
		case strings.Contains(part, "GT/GIA未确认"):
			part = "CN2（GT/GIA未确认）"
		case strings.HasPrefix(part, "CN2 GT"):
			part = "CN2 GT"
		case strings.HasPrefix(part, "CN2 GIA"):
			part = "CN2 GIA"
		case strings.HasPrefix(part, "163 骨干"):
			part = "163 骨干"
		case strings.HasPrefix(part, "联通 4837/169"):
			part = "4837/169"
		case strings.HasPrefix(part, "联通 9929"):
			part = "9929"
		case strings.HasPrefix(part, "移动 CMIN2"):
			part = "CMIN2"
		case strings.HasPrefix(part, "移动 CMI"):
			part = "CMI"
		case strings.Contains(part, "路径") && strings.Contains(part, "未识别"):
			part = "未识别"
		}
		compact = append(compact, part)
	}
	known := []string{}
	for _, value := range uniqStrings(compact) {
		if !strings.Contains(value, "未识别") && !strings.Contains(value, "未知") && value != "—" {
			known = append(known, value)
		}
	}
	if len(known) > 0 {
		for _, value := range known {
			upper := strings.ToUpper(value)
			if strings.Contains(upper, "CN2") || strings.Contains(upper, "9929") || strings.Contains(upper, "CMIN2") {
				return value
			}
		}
		return known[0]
	}
	return "未知"
}

// Keep diagnostic sentences in the detailed report, not the compact card.
func unknownCardRoute(line string) bool {
	for _, phrase := range []string{"未能定位", "未定位到", "未识别", "无法识别", "未知", "ASN 不足", "ASN 不可见"} {
		if strings.Contains(line, phrase) {
			return true
		}
	}
	return false
}

func firstMemoryRate(data map[string]any, memoryTest bool, direction string) (float64, bool) {
	keys := []string{"mem_" + direction + "_mbs", "memory_" + direction + "_mbs", "mem_" + direction + "_mib_s", "memory_" + direction + "_mib_s"}
	if memoryTest {
		keys = append(keys, direction+"_mbs", direction+"_mib_s", direction+"_mb_s", direction+"_bandwidth_mbs")
	}
	for _, key := range keys {
		if value, ok := numOf(data, key); ok && value > 0 {
			if strings.HasSuffix(key, "_mib_s") {
				value *= 1.048576 // convert MiB/s to decimal MB/s
			}
			return value, true
		}
	}
	return 0, false
}

// ---- 模板里用的小工具 ----

// benchLabel 把测试项的英文 id 翻成中文标题。
func benchLabel(test string) string {
	switch test {
	case "env":
		return "环境预检"
	case "sysinfo":
		return "系统信息"
	case "disk":
		return "磁盘 IO"
	case "memory":
		return "内存读写"
	case "cpu":
		return "CPU 跑分"
	case "net":
		return "网络测速"
	case "ip4":
		return "IPv4 质量与解锁"
	case "ip6":
		return "IPv6 质量与解锁"
	case "ip":
		return "IP 质量与解锁"
	case "route4":
		return "IPv4 回程路由"
	case "route6":
		return "IPv6 回程路由"
	case "route":
		return "回程路由"
	}
	return test
}

// benchFields 把一项结果的字段整理成「标签 / 值」列表，给详情页直接渲染。
//
// 为什么不在模板里直接 range map：Go 模板里 range map 的**顺序是随机的**，
// 每次刷新字段位置都在跳，很难看。这里排好序再交出去。
func benchFields(test string, d map[string]any) [][2]string {
	if d == nil {
		return nil
	}
	if test == "ip4" || test == "ip6" {
		test = "ip"
	}
	if test == "route" || test == "route4" || test == "route6" {
		if fields := routeBenchFields(d); len(fields) > 0 {
			return fields
		}
	}
	// 每项的字段顺序按"人关心什么"来定，不是字母序
	order := map[string][]string{
		"env":     {"virt", "root", "issues"},
		"sysinfo": {"cpu", "cores", "mem_total", "disk_total", "os", "virt"},
		"disk":    {"seq_read_mbs", "seq_write_mbs", "seq_read_mb_s", "seq_write_mb_s", "rand4k_read_iops", "rand4k_write_iops", "tool", "skipped", "reason"},
		"memory":  {"read_mib_s", "write_mib_s", "read_mbs", "write_mbs", "read_mb_s", "write_mb_s", "mem_read_mib_s", "mem_write_mib_s", "mem_read_mbs", "mem_write_mbs", "mem_read_mb_s", "mem_write_mb_s", "memory_read_mib_s", "memory_write_mib_s", "memory_read_mbs", "memory_write_mbs", "memory_read_mb_s", "memory_write_mb_s", "tool", "note"},
		"cpu":     {"single_eps", "multi_eps", "threads", "sha256_mbs", "elapsed_ms", "tool"},
		"net":     {"down_mbps", "up_mbps", "ping_ms", "server", "tool", "note"},
		// 解锁列表放在最前：访客最关心的就是"能不能看 Netflix"
		"ip":    {"unlocked", "locked", "ip_type", "org", "asn", "city", "country", "risk_scamalytics", "risk_abuseipdb", "unlock_total"},
		"route": {},
	}
	var out [][2]string
	used := map[string]bool{}
	for _, k := range order[test] {
		v, ok := d[k]
		if !ok || v == nil {
			continue
		}
		// 没测出来的项不展示：curl 降级只测下行，up_mbps 会是 0，
		// 摆出来会让人以为上传坏了
		if k == "up_mbps" {
			if f, ok := numOf(d, k); ok && f <= 0 {
				continue
			}
		}
		val := benchFieldValue(k, v)
		if val == "" {
			continue // 整块都是占位值（"null"/"N/A"）时不必占一行
		}
		used[k] = true
		out = append(out, [2]string{benchFieldLabel(k), val})
	}
	// 剩下的字段兜底展示（脚本加了新字段时不至于看不见）
	rest := make([]string, 0, len(d))
	for k := range d {
		if !used[k] && k != "unlock_detail" {
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)
	for _, k := range rest {
		if d[k] == nil {
			continue // nil 渲染出来是 "<nil>"，很难看
		}
		if v := benchFieldValue(k, d[k]); v != "" {
			out = append(out, [2]string{benchFieldLabel(k), v})
		}
	}
	return out
}

// routeBenchFields keeps the three carrier paths explicit and in the same
// order used on node cards. The values may contain per-city paths such as
// Beijing / Shanghai / Shenzhen, so they stay together under their carrier.
func routeBenchFields(data map[string]any) [][2]string {
	var out [][2]string
	for _, carrier := range []string{"电信", "联通", "移动"} {
		value := ""
		switch row := data[carrier].(type) {
		case map[string]any:
			value = routeLine(row)
			if note := routeQualityNote(row); note != "" && !strings.Contains(value, "（精品）") {
				value += "（" + note + "）"
			}
		case string:
			value = strings.TrimSpace(row)
		}
		if !isBlankValue(value) {
			out = append(out, [2]string{carrier, value})
		}
	}
	return out
}

// benchFieldLabel 字段名 → 中文标签。
func benchFieldLabel(k string) string {
	switch k {
	case "virt":
		return "虚拟化"
	case "root":
		return "root 运行"
	case "issues":
		return "环境问题"
	case "cpu":
		return "CPU"
	case "cores":
		return "核心数"
	case "mem_total":
		return "内存"
	case "disk_total":
		return "磁盘"
	case "os":
		return "系统"
	case "seq_read_mbs", "seq_read_mb_s":
		return "顺序读"
	case "seq_write_mbs", "seq_write_mb_s":
		return "顺序写"
	case "read_mib_s", "read_mbs", "read_mb_s", "mem_read_mib_s", "mem_read_mbs", "mem_read_mb_s", "memory_read_mib_s", "memory_read_mbs", "memory_read_mb_s":
		return "内存读取"
	case "write_mib_s", "write_mbs", "write_mb_s", "mem_write_mib_s", "mem_write_mbs", "mem_write_mb_s", "memory_write_mib_s", "memory_write_mbs", "memory_write_mb_s":
		return "内存写入"
	case "rand4k_read_iops":
		return "4K 随机读"
	case "rand4k_write_iops":
		return "4K 随机写"
	case "single_eps":
		return "单核"
	case "multi_eps":
		return "多核"
	case "threads":
		return "线程数"
	case "sha256_mbs":
		return "sha256 吞吐"
	case "down_mbps":
		return "下行"
	case "up_mbps":
		return "上行"
	case "ping_ms":
		return "延迟"
	case "server":
		return "测速节点"
	case "tool":
		return "测试工具"
	case "note":
		return "备注"
	case "ip_type":
		return "IP 类型"
	case "org":
		return "运营商"
	case "asn":
		return "ASN"
	case "city":
		return "城市"
	case "country":
		return "国家"
	case "risk_scamalytics":
		return "Scamalytics 风险分"
	case "risk_abuseipdb":
		return "AbuseIPDB 风险分"
	case "risk_dbip":
		return "DB-IP 风险分"
	case "hops":
		return "跳数"
	case "电信", "联通", "移动":
		return k
	case "target":
		return "目标"
	case "blacklist_listed":
		return "黑名单收录"
	case "blacklist_marked":
		return "黑名单标记"
	case "blacklist_clean":
		return "黑名单干净"
	case "blacklist_total":
		return "黑名单库数"
	case "usage":
		return "IP 用途"
	case "usage_detail":
		return "用途明细"
	case "unlocked":
		return "已解锁"
	case "locked":
		return "未解锁"
	case "unlock_total":
		return "检测服务数"
	case "skipped":
		return "已跳过"
	case "reason":
		return "跳过原因"
	case "elapsed_ms":
		return "耗时"
	}
	return k
}

// benchFieldValue 把值渲染成人类可读的字符串。
func benchFieldValue(k string, v any) string {
	// CPU 型号统一洗一遍 —— 卡片和详情页显示的应该是同一个东西，
	// 详情页却还是 "Intel(R) Xeon(R) Platinum 8272CL CPU @ 2.60GHz" 那种长格式
	// （卡片用了 shortCPUModel、详情页没用，两处不一致）。
	if k == "cpu" {
		if s, ok := v.(string); ok {
			return shortCPUModel(s)
		}
	}
	switch x := v.(type) {
	case bool:
		if x {
			return "是"
		}
		return "否"
	case []any:
		ss := make([]string, 0, len(x))
		for _, it := range x {
			ss = append(ss, benchFieldValue("", it))
		}
		if len(ss) == 0 {
			return "—"
		}
		// 解锁/未解锁用 ✅❌ 打头，一眼能扫出哪些能用
		switch k {
		case "unlocked":
			return "✅ " + strings.Join(ss, "、")
		case "locked":
			return "❌ " + strings.Join(ss, "、")
		}
		return strings.Join(ss, "、")
	case map[string]any:
		// 嵌套对象不能直接 fmt.Sprint —— 会打出
		// "map[Name:null PostalCode:null]" 这种东西。
		// 优先取 Name，没有就按 key=value 排好序拼。
		if n, ok := x["Name"].(string); ok && !isBlankValue(n) {
			return n
		}
		// 回程线路至少包含 line；可选字段可能还包括 latency_ms / quality。
		// 只显示线路与有效端到端延迟，不显示跳数或任何 IP（那是服务器隐私）。
		if line := strOf(x, "line"); line != "" || x["latency_ms"] != nil {
			out := routeLine(x)
			// 普通线路明确写出来 —— "163 骨干 · 64ms" 看着还行，
			// 但它是普通线路，高峰期会堵，不说明会误导买家。
			if q := routeQualityNote(x); q != "" && !strings.Contains(out, "（精品）") {
				out += "（" + q + "）"
			}
			return out
		}
		keys := sortedKeysOf(x)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			v := x[k]
			if v == nil {
				continue
			}
			// "null" / "N/A" 这类占位值也当没有 —— IPQuality 就爱返回
			// 字面字符串 "null"（不是 JSON null），直接显示会很怪
			if sv, ok := v.(string); ok && isBlankValue(sv) {
				continue
			}
			parts = append(parts, k+"="+benchFieldValue("", v))
		}
		if len(parts) == 0 {
			return "" // 全空就整个字段不显示，别摆一个 "—" 占地方
		}
		separator := " "
		if k == "usage_detail" {
			separator = "  ·  "
		}
		return strings.Join(parts, separator)
	case float64:
		switch k {
		case "mem_total", "disk_total":
			return humanBytes(x)
		case "seq_read_mbs", "seq_write_mbs", "seq_read_mb_s", "seq_write_mb_s":
			return fmt.Sprintf("%s MB/s", benchNumber(x))
		case "rand4k_read_iops", "rand4k_write_iops":
			return fmt.Sprintf("%s IOPS", benchNumber(x))
		case "read_mib_s", "write_mib_s", "mem_read_mib_s", "mem_write_mib_s", "memory_read_mib_s", "memory_write_mib_s":
			return fmt.Sprintf("%.2f MB/s", x*1.048576)
		case "read_mbs", "write_mbs", "read_mb_s", "write_mb_s", "mem_read_mbs", "mem_write_mbs", "mem_read_mb_s", "mem_write_mb_s", "memory_read_mbs", "memory_write_mbs", "memory_read_mb_s", "memory_write_mb_s":
			return fmt.Sprintf("%.2f MB/s", x)
		case "sha256_mbs":
			return fmt.Sprintf("%.2f MB/s", x)
		case "down_mbps", "up_mbps":
			return fmt.Sprintf("%.2f Mbps", x)
		case "elapsed_ms":
			return fmt.Sprintf("%.1f 秒", x/1000)
		}
		if x == float64(int64(x)) {
			return fmt.Sprintf("%d", int64(x))
		}
		return fmt.Sprintf("%.2f", x)
	}
	s := fmt.Sprint(v)
	if s == "" {
		return "—"
	}
	return s
}

func benchNumber(x float64) string {
	if x == float64(int64(x)) {
		return fmt.Sprintf("%.0f", x)
	}
	return fmt.Sprintf("%.2f", x)
}

// isBlankValue 判断一个字符串是不是"其实没有值"。
//
// 有些上游接口不用 JSON null，而是返回**字面字符串** "null" / "N/A" / "-"。
// 直接显示出来就是「城市 null」，很怪。
func isBlankValue(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "null", "nil", "n/a", "na", "-", "unknown", "none":
		return true
	}
	return false
}

// humanBytes 把字节数说成 1.0 GB 这种。
func humanBytes(v float64) string {
	const unit = 1024.0
	if v < unit {
		return fmt.Sprintf("%.0f B", v)
	}
	units := []string{"KB", "MB", "GB", "TB", "PB"}
	i := -1
	for v >= unit && i < len(units)-1 {
		v /= unit
		i++
	}
	return fmt.Sprintf("%.1f %s", v, units[i])
}

// firstNonEmpty 取第一个非空字符串。
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// uniqStrings 去重但保持原顺序。
func uniqStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range in {
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

// numOf 从 map 里取一个数值字段。
func numOf(m map[string]any, k string) (float64, bool) {
	v, ok := m[k]
	if !ok {
		return 0, false
	}
	switch x := v.(type) {
	case float64:
		return x, true
	case int:
		return float64(x), true
	}
	return 0, false
}

// sortedKeysOf 取 map 的键并排序。
func sortedKeysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
