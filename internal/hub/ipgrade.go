package hub

// IP 质量评分：把「这 IP 干不干净」压成一个从绿到红的等级。
//
// 参考社区的做法（Scamalytics 风险分 + DNSBL 黑名单 + IP 类型 + 解锁率），
// 但**自己定权重**：不同脚本口径不一样，与其抄一个不如写清楚我们怎么算的。
//
// 分档刻意偏严 —— IP 质量是买小鸡时最看重的，把"机房 IP 但有风险标记"
// 说成"优秀"是不负责任的。

import (
	"fmt"
	"strings"
)

// ipGrade 是 IP 质量的结论。
type ipGrade struct {
	Level   string // best | good | fair | poor | bad
	Label   string // 优秀 / 良好 / 一般 / 较差 / 差
	Score   int    // 0~100
	Reasons []string
}

// gradeIP 按 IP 检测结果打分。
//
// 数据来自 kokoro-bench 的 ip 项：
//
//	risk_scamalytics  0~100，越低越干净
//	blacklist_listed  DNSBL 里被拉黑的条数
//	blacklist_marked  被"标记"（灰名单）的条数
//	usage / ip_type   机房 / 家宽 / 广播IP
//	unlocked/locked   流媒体解锁情况
func gradeIP(d map[string]any) *ipGrade {
	if d == nil {
		return nil
	}
	score := 100
	var reasons []string

	// 1) 风险分：Scamalytics 0~100。这是最硬的指标，权重给足。
	risk, hasRisk := numOf(d, "risk_scamalytics")
	if !hasRisk {
		risk, hasRisk = numOf(d, "risk_abuseipdb")
	}
	if hasRisk {
		// 风险分每 1 分扣 0.5，满分 100 直接扣 50
		pen := int(risk / 2)
		if pen > 50 {
			pen = 50
		}
		score -= pen
		switch {
		case risk <= 0:
			reasons = append(reasons, "风险分 0（各库都判为干净）")
		case risk < 25:
			reasons = append(reasons, fmt.Sprintf("风险分 %.0f（低）", risk))
		case risk < 60:
			reasons = append(reasons, fmt.Sprintf("风险分 %.0f（中）", risk))
		default:
			reasons = append(reasons, fmt.Sprintf("风险分 %.0f（高）", risk))
		}
	}

	// 2) 黑名单：被拉黑是最严重的问题 —— 直接影响邮件、注册、风控
	if listed, ok := numOf(d, "blacklist_listed"); ok && listed > 0 {
		score -= 40
		reasons = append(reasons, fmt.Sprintf("已被 %d 个黑名单收录", int(listed)))
	} else if marked, ok := numOf(d, "blacklist_marked"); ok && marked > 0 {
		pen := int(marked) * 3
		if pen > 15 {
			pen = 15
		}
		score -= pen
		reasons = append(reasons, fmt.Sprintf("%d 个黑名单有标记记录", int(marked)))
	} else if _, ok := numOf(d, "blacklist_total"); ok {
		reasons = append(reasons, "未进任何黑名单")
	}

	// 3) IP 类型：家宽/住宅最值钱，机房最容易被风控
	typ := strings.TrimSpace(strOf(d, "usage"))
	if typ == "" {
		typ = strings.TrimSpace(strOf(d, "ip_type"))
	}
	switch {
	case strings.Contains(typ, "家宽"), strings.Contains(typ, "住宅"),
		strings.Contains(typ, "Residential"), strings.Contains(typ, "ISP"):
		reasons = append(reasons, "住宅/家宽 IP（最干净）")
	case strings.Contains(typ, "机房"), strings.Contains(typ, "IDC"),
		strings.Contains(typ, "Hosting"), strings.Contains(typ, "DataCenter"):
		// 扣 20 不是 10：机房 IP 就算各库都判干净，它在注册/风控场景里
		// 依然是最容易被拦的一类。给 90 分判"优秀"是在误导买家。
		score -= 20
		reasons = append(reasons, "机房 IP（易被风控）")
	case strings.Contains(typ, "广播"):
		score -= 5
		reasons = append(reasons, "广播 IP")
	case typ != "":
		reasons = append(reasons, typ)
	}

	// 4) 解锁率：一个都解不开通常意味着 IP 段被流媒体整体拉黑了
	total, hasTotal := numOf(d, "unlock_total")
	if hasTotal && total > 0 {
		unlocked := 0
		if arr, ok := d["unlocked"].([]any); ok {
			unlocked = len(arr)
		}
		ratio := float64(unlocked) / total
		switch {
		case ratio >= 0.8:
			reasons = append(reasons, fmt.Sprintf("流媒体解锁 %d/%d", unlocked, int(total)))
		case ratio >= 0.5:
			score -= 8
			reasons = append(reasons, fmt.Sprintf("流媒体只解锁 %d/%d", unlocked, int(total)))
		default:
			score -= 20
			reasons = append(reasons, fmt.Sprintf("流媒体几乎全不解锁（%d/%d）", unlocked, int(total)))
		}
	}

	if score < 0 {
		score = 0
	}
	if score > 100 {
		score = 100
	}
	g := &ipGrade{Score: score, Reasons: reasons}
	switch {
	case score >= 85:
		g.Level, g.Label = "best", "优秀"
	case score >= 70:
		g.Level, g.Label = "good", "良好"
	case score >= 50:
		g.Level, g.Label = "fair", "一般"
	case score >= 30:
		g.Level, g.Label = "poor", "较差"
	default:
		g.Level, g.Label = "bad", "差"
	}
	return g
}

// strOf 取一个字符串字段。
func strOf(m map[string]any, k string) string {
	if v, ok := m[k].(string); ok {
		return v
	}
	return ""
}

// ipGradeEmoji 给卡片摘要用的小图标 —— 一眼看出好坏。
func ipGradeEmoji(level string) string {
	switch level {
	case "best":
		return "🟢"
	case "good":
		return "🟢"
	case "fair":
		return "🟡"
	case "poor":
		return "🟠"
	case "bad":
		return "🔴"
	}
	return "⚪"
}

// routeLine 把 route 项的一项渲染成「线路 · 延迟」。
//
// 刻意**不显示跳数**：站长和买家关心的是"走的哪条骨干、延迟多少"，
// 而不是经过几个路由器。也**不显示任何 IP** —— 那是服务器隐私。
func routeLine(d map[string]any) string {
	if d == nil {
		return ""
	}
	line := strings.TrimSpace(strOf(d, "line"))
	ms, hasMS := numOf(d, "latency_ms")
	switch {
	case line != "" && hasMS && ms > 0:
		return fmt.Sprintf("%s · %.0fms", line, ms)
	case line != "":
		return line
	case hasMS && ms > 0:
		return fmt.Sprintf("%.0fms", ms)
	}
	return ""
}
