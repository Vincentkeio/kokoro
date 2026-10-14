package hub

// IP 质量评分与回程线路渲染的测试。
//
// 两条硬要求（boss 明确提的）：
//   1. 线路只显示「走什么骨干 + 延迟」，**不显示几跳、更不显示 IP**
//   2. IP 质量要有从绿到红的等级，且要出现在卡片摘要里

import (
	"strings"
	"testing"
)

// TestGradeIPCleanResidential 干净的住宅 IP 应该是最高档。
func TestGradeIPCleanResidential(t *testing.T) {
	g := gradeIP(map[string]any{
		"usage":            "住宅",
		"risk_scamalytics": float64(0),
		"blacklist_total":  float64(423),
		"blacklist_listed": float64(0),
		"blacklist_marked": float64(0),
		"unlock_total":     float64(7),
		"unlocked":         []any{"Netflix", "Disney+", "ChatGPT", "Prime", "Reddit", "TikTok", "YouTube"},
	})
	if g == nil {
		t.Fatal("没算出等级")
	}
	if g.Level != "best" {
		t.Errorf("干净住宅 IP 应为 best，实际 %s（%d 分）: %v", g.Level, g.Score, g.Reasons)
	}
	if ipGradeEmoji(g.Level) != "🟢" {
		t.Errorf("最高档应该是绿点，实际 %s", ipGradeEmoji(g.Level))
	}
}

// TestGradeIPBlacklisted 进了黑名单必须是差评 —— 这直接影响邮件和注册。
func TestGradeIPBlacklisted(t *testing.T) {
	g := gradeIP(map[string]any{
		"usage":            "机房",
		"risk_scamalytics": float64(80),
		"blacklist_listed": float64(3),
		"unlock_total":     float64(7),
		"unlocked":         []any{},
	})
	if g.Level != "bad" {
		t.Errorf("被拉黑 + 高风险应为 bad，实际 %s（%d 分）: %v", g.Level, g.Score, g.Reasons)
	}
	if ipGradeEmoji(g.Level) != "🔴" {
		t.Errorf("最差档应该是红点，实际 %s", ipGradeEmoji(g.Level))
	}
	// 理由里要说清楚为什么差
	joined := strings.Join(g.Reasons, " ")
	for _, want := range []string{"黑名单", "风险分"} {
		if !strings.Contains(joined, want) {
			t.Errorf("理由里应提到 %s：%v", want, g.Reasons)
		}
	}
}

// TestGradeIPDatacenterMidrange 机房 IP 但干净，应该是中档而不是"优秀"。
//
// 分档偏严：机房 IP 容易被风控，说成优秀是不负责任的。
func TestGradeIPDatacenterMidrange(t *testing.T) {
	g := gradeIP(map[string]any{
		"usage":            "机房",
		"risk_scamalytics": float64(0),
		"blacklist_listed": float64(0),
		"unlock_total":     float64(7),
		"unlocked":         []any{"a", "b", "c", "d", "e", "f", "g"},
	})
	if g.Level == "best" {
		t.Errorf("机房 IP 不该是最高档（%d 分）: %v", g.Score, g.Reasons)
	}
	if g.Level != "good" && g.Level != "fair" {
		t.Errorf("干净机房 IP 应在 good/fair，实际 %s（%d 分）", g.Level, g.Score)
	}
}

// TestRouteLineNoIPNoHops 线路渲染不能出现 IP，也不该显示跳数。
func TestRouteLineNoIPNoHops(t *testing.T) {
	d := map[string]any{
		"line":       "CN2 GIA",
		"asn":        "4809",
		"hops":       float64(14),
		"latency_ms": float64(66.1),
		// 故意塞一个 IP 进来，验证渲染时不会被带出去
		"target": "202.96.209.133",
	}
	got := routeLine(d)
	if got != "CN2 GIA · 66ms" {
		t.Errorf("线路渲染 = %q，应为 \"CN2 GIA · 66ms\"", got)
	}
	if strings.Contains(got, "202.96") {
		t.Error("渲染结果里泄露了 IP")
	}
	if strings.Contains(got, "跳") {
		t.Error("不该显示跳数 —— 买家关心的是走哪条线、延迟多少")
	}
}

// 注意：这里的 raw 必须是**真正的 NDJSON**（一行一个事件）。
// 解析器是逐行读的 —— 我第一版写成多行缩进的 JSON，摘要一直是空，
// 还以为是解析器坏了。真实脚本输出的就是一行一个事件。

// TestSummarizeRouteMergesSameLine 三网走同一条线时只报一次，别挤三遍。
func TestSummarizeRouteMergesSameLine(t *testing.T) {
	raw := `{"event":"result","test":"route","ok":true,"data":{"电信":{"line":"CN2 GIA","latency_ms":45.0},"联通":{"line":"CN2 GIA","latency_ms":46.0},"移动":{"line":"CN2 GIA","latency_ms":47.0}}}
{"event":"done","ok":true,"elapsed_ms":1000,"failed":[]}`
	got := summarizeTask("netquality", raw)
	t.Logf("摘要 = %s", got)
	if got == "" {
		t.Fatal("摘要为空")
	}
	if strings.Count(got, "CN2 GIA") != 1 {
		t.Errorf("三网同线时应只报一次：%q", got)
	}
}

// TestSummarizeRouteListsDifferentLines 三网不同线时要都列出来。
func TestSummarizeRouteListsDifferentLines(t *testing.T) {
	raw := `{"event":"result","test":"route","ok":true,"data":{"电信":{"line":"CN2 GIA","latency_ms":45.0},"联通":{"line":"联通 9929","latency_ms":46.0},"移动":{"line":"移动 CMI","latency_ms":47.0}}}
{"event":"done","ok":true,"elapsed_ms":1000,"failed":[]}`
	got := summarizeTask("netquality", raw)
	t.Logf("摘要 = %s", got)
	for _, want := range []string{"CN2 GIA", "9929", "CMI"} {
		if !strings.Contains(got, want) {
			t.Errorf("摘要应列出 %s：%q", want, got)
		}
	}
}

// TestSummarizeIPShowsGrade 摘要里要有 IP 质量等级（boss 明确要求的）。
func TestSummarizeIPShowsGrade(t *testing.T) {
	raw := `{"event":"result","test":"ip","ok":true,"data":{"usage":"住宅","ip_type":"住宅","risk_scamalytics":0,"blacklist_total":423,"blacklist_listed":0,"unlock_total":7,"unlocked":["Netflix","Disney+","ChatGPT","Prime","Reddit","TikTok","YouTube"]}}
{"event":"done","ok":true,"elapsed_ms":1000,"failed":[]}`
	got := summarizeTask("ipquality", raw)
	t.Logf("摘要 = %s", got)
	// 卡片摘要的格式：`🟢 IP质量:优秀 流媒体解锁:7/7 性质: 住宅`
	// （boss 定的写法，比原来 `IP 优秀 · 解锁 7/7 · 住宅` 更明确）
	if !strings.Contains(got, "🟢") || !strings.Contains(got, "IP质量:优秀") {
		t.Errorf("摘要应显示 IP 质量等级：%q", got)
	}
	if !strings.Contains(got, "流媒体解锁:7/7") {
		t.Errorf("摘要应显示解锁数：%q", got)
	}
	if !strings.Contains(got, "性质:") {
		t.Errorf("摘要应显示 IP 性质（机房/家宽）：%q", got)
	}
}

// TestSummarizeIPBlacklistedShowsRed 被拉黑的 IP 摘要里要是红点。
func TestSummarizeIPBlacklistedShowsRed(t *testing.T) {
	raw := `{"event":"result","test":"ip","ok":true,"data":{"usage":"机房","risk_scamalytics":90,"blacklist_listed":5,"unlock_total":7,"unlocked":[]}}
{"event":"done","ok":true,"elapsed_ms":1000,"failed":[]}`
	got := summarizeTask("ipquality", raw)
	t.Logf("摘要 = %s", got)
	if !strings.Contains(got, "🔴") {
		t.Errorf("被拉黑的 IP 摘要里应是红点：%q", got)
	}
}

// TestIPFieldsIncludeBlacklist 详情页的字段里要有黑名单和 IP 类型。
func TestIPFieldsIncludeBlacklist(t *testing.T) {
	d := map[string]any{
		"usage":            "机房",
		"risk_scamalytics": float64(0),
		"blacklist_total":  float64(423),
		"blacklist_clean":  float64(420),
		"blacklist_listed": float64(0),
		"blacklist_marked": float64(3),
		"unlock_total":     float64(7),
		"unlocked":         []any{"Netflix"},
	}
	labels := map[string]string{}
	for _, kv := range benchFields("ip", d) {
		labels[kv[0]] = kv[1]
	}
	// 标签是"黑名单收录/标记/干净/库数"四个，不叫光秃秃的"黑名单"
	var hasBlacklist bool
	for k := range labels {
		if strings.Contains(k, "黑名单") {
			hasBlacklist = true
		}
	}
	if !hasBlacklist {
		t.Errorf("详情页缺少黑名单相关字段，现有: %v", labels)
	}
	for _, want := range []string{"IP 用途", "Scamalytics 风险分"} {
		if _, ok := labels[want]; !ok {
			t.Errorf("详情页缺少字段 %q，现有: %v", want, labels)
		}
	}
}

// TestRouteLineMarksPremium 精品线路要打标，普通线路也要说清楚。
//
// 起因：boss 看到"电信 CN2 GT"问"这真是 CN2 吗，用着很卡"。
// 一查发现 AS4812 根本不是 CN2 —— 是"中国电信"的普通 ASN。
// 而且 CN2 GIA 和 GT **共用 AS4809**，靠单个 ASN 分不出来。
// 结论：必须把"精品/普通"明确标出来，否则会被误读成好线路。
func TestRouteLineMarksPremium(t *testing.T) {
	premium := map[string]any{"line": "CN2 GIA", "quality": "精品", "latency_ms": float64(45.0)}
	if got := routeLine(premium); !strings.Contains(got, "精品") {
		t.Errorf("精品线路要标出来：%q", got)
	}
	normal := map[string]any{"line": "163 骨干", "quality": "普通", "latency_ms": float64(64.6)}
	got := routeLine(normal)
	if strings.Contains(got, "精品") {
		t.Errorf("普通线路不该标成精品：%q", got)
	}
	if !strings.Contains(got, "163 骨干") {
		t.Errorf("应保留线路名：%q", got)
	}
	// 详情页要能看出这条是普通线路
	if note := routeQualityNote(normal); note != "普通线路" {
		t.Errorf("普通线路的说明 = %q", note)
	}
}
