package hub

import (
	"fmt"
	"math"
	"net/url"
	"strings"
	"time"

	"github.com/kokoro-probe/kokoro/internal/alert"
)

// FmtBytes 把字节数格式化为人类可读形式（1024 进制）。
func FmtBytes(b int64) string {
	if b < 0 {
		return "-"
	}
	return fmt.Sprintf("%sB", fmtSize(float64(b)))
}

// FmtRate 把字节每秒格式化为速率。
func FmtRate(bps int64) string {
	if bps < 0 {
		return "-"
	}
	return fmt.Sprintf("%sB/s", fmtSize(float64(bps)))
}

func fmtSize(f float64) string {
	units := []string{"", "K", "M", "G", "T", "P"}
	i := 0
	for f >= 1024 && i < len(units)-1 {
		f /= 1024
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%d ", int64(f))
	}
	if f >= 100 {
		return fmt.Sprintf("%.0f %s", f, units[i])
	}
	return fmt.Sprintf("%.1f %s", f, units[i])
}

// FmtPercent 保留一位小数。
func FmtPercent(v float64) string {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return "-"
	}
	return fmt.Sprintf("%.1f%%", v)
}

// FmtDuration 把秒数格式化为「x天 y小时 z分」。
func FmtDuration(sec int64) string {
	if sec <= 0 {
		return "-"
	}
	d := time.Duration(sec) * time.Second
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	minutes := int(d.Minutes()) % 60
	switch {
	case days > 0:
		return fmt.Sprintf("%d天%d小时", days, hours)
	case hours > 0:
		return fmt.Sprintf("%d小时%d分", hours, minutes)
	default:
		return fmt.Sprintf("%d分", minutes)
	}
}

// MetricName 把告警指标的 key 翻译成中文名，模板里用来显示规则。
// 未知 key 原样返回。
func MetricName(key string) string {
	for _, d := range alert.MetricDefs {
		if d.Key == key {
			return d.Name
		}
	}
	return key
}

// LatClass 按延迟给一个颜色级别，模板里用来上色。
// <80ms 好、80–200ms 一般、>200ms 差；没测出数据返回空串。
func LatClass(ms float64) string {
	switch {
	case ms <= 0:
		return ""
	case ms < 80:
		return "ok"
	case ms <= 200:
		return "warn"
	default:
		return "bad"
	}
}

// FmtMS 把毫秒数格式化得短一点，页面表格里用。
func FmtMS(ms float64) string {
	if ms <= 0 {
		return "—"
	}
	if ms >= 100 {
		return fmt.Sprintf("%.0f", ms)
	}
	return fmt.Sprintf("%.1f", ms)
}

// FmtTime 把毫秒时间戳格式化为「2006-01-02 15:04」。ms<=0 时返回空串。
func FmtTime(ms int64) string {
	if ms <= 0 {
		return ""
	}
	return time.UnixMilli(ms).Local().Format("2006-01-02 15:04")
}

// FmtTimeShort 只留月日时分，用在评论这类密集列表里。
func FmtTimeShort(ms int64) string {
	if ms <= 0 {
		return ""
	}
	t := time.UnixMilli(ms).Local()
	if t.Year() == time.Now().Year() {
		return t.Format("01-02 15:04")
	}
	return t.Format("2006-01-02")
}

// Pct 计算百分比，分母为 0 时返回 0。
func Pct(used, total float64) float64 {
	if total <= 0 {
		return 0
	}
	p := used / total * 100
	if p < 0 {
		return 0
	}
	if p > 100 {
		return 100
	}
	return p
}

// SafeURL 只允许 http/https，其余返回空串，防止主题或评论里塞 javascript: 之类的链接。
func SafeURL(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	u, err := url.Parse(s)
	if err != nil {
		return ""
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return ""
	}
	return u.String()
}

// relativeTime 把毫秒时间戳说成「3 分钟前」。
//
// 首页的"最新留言"要的是"新不新"，精确到秒反而不好读。
// 超过 30 天就退回日期——"47 天前"没人算得出来是哪天。
func relativeTime(ms int64) string {
	if ms <= 0 {
		return ""
	}
	d := time.Since(time.UnixMilli(ms))
	switch {
	case d < 0:
		return "刚刚"
	case d < time.Minute:
		return "刚刚"
	case d < time.Hour:
		return fmt.Sprintf("%d 分钟前", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d 小时前", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%d 天前", int(d.Hours()/24))
	default:
		return time.UnixMilli(ms).Format("2006-01-02")
	}
}
