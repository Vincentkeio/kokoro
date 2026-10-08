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
	Items    []benchItem
	Failed   []string
	ElapsedM int64
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
		case "result":
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
	if d := rep.get("net"); d != nil {
		if v, ok := numOf(d, "down_mbps"); ok && v > 0 {
			// 只有**真测了**上行才写上行。脚本在 speedtest 不可用时会降级成
			// curl 下载测速，那只测下行 —— 写成「上行 0 Mbps」会让人
			// 以为这机器上传是坏的，其实是没测。
			if up, ok := numOf(d, "up_mbps"); ok && up > 0 {
				parts = append(parts, fmt.Sprintf("下行 %.0f / 上行 %.0f Mbps", v, up))
			} else {
				parts = append(parts, fmt.Sprintf("下行 %.0f Mbps", v))
			}
		}
	}
	if d := rep.get("ip"); d != nil {
		// IP 质量等级放最前面 —— 这是买家扫一眼卡片最想知道的事，
		// 从最安全的绿到最坏的屏蔽。
		if g := gradeIP(d); g != nil {
			parts = append(parts, ipGradeEmoji(g.Level)+" IP "+g.Label)
		}
		if u, ok := d["unlocked"].([]any); ok {
			total := 0
			if t, ok := numOf(d, "unlock_total"); ok {
				total = int(t)
			}
			parts = append(parts, fmt.Sprintf("解锁 %d/%d", len(u), total))
		}
		if t := firstNonEmpty(strOf(d, "usage"), strOf(d, "ip_type")); t != "" {
			parts = append(parts, t)
		}
	}
	if d := rep.get("route"); d != nil {
		// 回程线路只报"走了哪条骨干"，三网一样时合并成一条，
		// 免得摘要里挤三遍同一个词
		var lines []string
		for _, isp := range []string{"电信", "联通", "移动"} {
			sub, _ := d[isp].(map[string]any)
			if l := strOf(sub, "line"); l != "" {
				lines = append(lines, l)
			}
		}
		if u := uniqStrings(lines); len(u) == 1 {
			parts = append(parts, "回程 "+u[0])
		} else if len(u) > 0 {
			parts = append(parts, "回程 "+strings.Join(u, " / "))
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
	return strings.Join(parts, " · ")
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
	case "cpu":
		return "CPU"
	case "net":
		return "网络测速"
	case "ip":
		return "IP 质量与解锁"
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
	// 每项的字段顺序按"人关心什么"来定，不是字母序
	order := map[string][]string{
		"env":     {"virt", "root", "issues"},
		"sysinfo": {"cpu", "cores", "mem_total", "disk_total", "os", "virt"},
		"disk":    {"seq_read_mbs", "seq_write_mbs", "rand4k_read_iops", "rand4k_write_iops", "tool", "skipped", "reason"},
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
	case "seq_read_mbs":
		return "顺序读"
	case "seq_write_mbs":
		return "顺序写"
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
		// 回程线路是 {line, asn, hops, latency_ms}。
		// 只显示「线路 · 延迟」——**不显示跳数**（没人关心过了几个路由器），
		// 更不显示任何 IP（那是服务器隐私）。
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
		return strings.Join(parts, " ")
	case float64:
		switch k {
		case "mem_total", "disk_total":
			return humanBytes(x)
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
