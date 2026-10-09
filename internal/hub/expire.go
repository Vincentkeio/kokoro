package hub

// 到期倒计时。
//
// 站长最关心的不是"哪年哪月到期"，而是"**还有几天**"。
// 所以把到期日算成天数，挂在卡片标签里。

import (
	"fmt"
	"strings"
	"time"
)

// expireForever 是"永久有效"在库里的存法。
//
// 用一个固定的中文词而不是空串：空串是"还没填"，
// 两者含义完全不同 —— 混起来之后没人分得清
// "这台不限时间"和"这台我忘了填"。
const expireForever = "永久"

// expireTag 返回到期标签的文案和档位。
//
// 返回空串表示「没填到期日」—— 那就什么都不显示，
// 而不是显示"永久"（没填 ≠ 永久，站长可能只是还没填）。
//
// 支持两种输入：
//   - YYYY-MM-DD（<input type="date"> 给的）
//   - 其他格式（老数据手填的）—— 认不出就不显示，不瞎猜
func expireTag(raw string) (text, level string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ""
	}
	// 「永久」是一个**正式状态**，不是"没填"。
	// 它的语义是"不会到期" —— 所以给一个明确的标签，
	// 而不是像没填那样什么都不显示。
	if raw == expireForever {
		return "永久", "exp-perm"
	}
	// 只取日期部分：万一带了时间（2027-03-15T00:00）也认
	if i := strings.IndexAny(raw, "T "); i > 0 {
		raw = raw[:i]
	}
	day, err := time.ParseInLocation("2006-01-02", raw, time.Local)
	if err != nil {
		return "", "" // 认不出的格式不显示，别瞎猜一个天数出来
	}

	// 按**天**比，不比小时 —— "今天到期"和"明天到期"差了整整一天，
	// 用 time.Until 会因为当前是几点而给出 0 或 1，看起来像随机数。
	today := time.Now()
	today = time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.Local)
	days := int(day.Sub(today).Hours() / 24)

	switch {
	case days < 0:
		return fmt.Sprintf("已过期 %d 天", -days), "exp-bad"
	case days == 0:
		return "今天到期", "exp-bad"
	case days <= 7:
		return fmt.Sprintf("剩 %d 天", days), "exp-bad"
	case days <= 30:
		return fmt.Sprintf("剩 %d 天", days), "exp-warn"
	default:
		return fmt.Sprintf("剩 %d 天", days), "exp-ok"
	}
}
