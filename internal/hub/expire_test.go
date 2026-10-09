package hub

// 到期倒计时标签。

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestExpireTagLevels(t *testing.T) {
	day := func(n int) string {
		return time.Now().AddDate(0, 0, n).Format("2006-01-02")
	}
	cases := []struct {
		days  int
		level string
	}{
		{365, "exp-ok"},   // 还早
		{31, "exp-ok"},    // 刚过一个月
		{30, "exp-warn"},  // 进入提醒区
		{8, "exp-warn"},   // 提醒区尾巴
		{7, "exp-bad"},    // 告警区
		{1, "exp-bad"},    // 明天
		{0, "exp-bad"},    // 今天到期
		{-1, "exp-bad"},   // 已过期
		{-100, "exp-bad"}, // 过期很久
	}
	for _, c := range cases {
		text, level := expireTag(day(c.days))
		if level != c.level {
			t.Errorf("剩 %d 天：档位 = %q，应为 %q", c.days, level, c.level)
		}
		if text == "" {
			t.Errorf("剩 %d 天：文案是空的", c.days)
		}
	}
}

// TestExpireTagBoundaryByDayNotHour 判据要按**天**，不能按小时。
//
// 用 time.Until/24h 的话，「明天到期」在下午会算出 0.6 天 → 显示"今天到期"，
// 上午算出 1.4 天 → 显示"剩 1 天"。同一个日期在一天里来回变，像在胡报。
func TestExpireTagBoundaryByDayNotHour(t *testing.T) {
	tm := time.Now().AddDate(0, 0, 1).Format("2006-01-02")
	text, _ := expireTag(tm)
	if !strings.Contains(text, "剩 1 天") {
		t.Errorf("明天到期应显示「剩 1 天」，实际 %q —— 判据可能按小时算了", text)
	}
	today := time.Now().Format("2006-01-02")
	if text, _ := expireTag(today); text != "今天到期" {
		t.Errorf("今天到期应显示「今天到期」，实际 %q", text)
	}
}

// TestExpireTagEmptyAndUnknown 空 / 认不出的格式都不显示。
//
// 认不出就**不显示**，绝不瞎猜一个天数 —— 猜错比不显示糟糕得多。
// 尤其"没填"和"永久"是两回事，不能把空的显示成"永久"。
func TestExpireTagEmptyAndUnknown(t *testing.T) {
	for _, in := range []string{"", "   ", "长期", "2027年3月", "待定"} {
		text, level := expireTag(in)
		if text != "" || level != "" {
			t.Errorf("%q 不该产生标签，实际 (%q, %q)", in, text, level)
		}
	}
}

// TestExpireTagAcceptsDateTime 带时间的 ISO 串也要认（只取日期部分）。
func TestExpireTagAcceptsDateTime(t *testing.T) {
	future := time.Now().AddDate(0, 0, 100).Format("2006-01-02")
	for _, in := range []string{future, future + "T00:00:00Z", future + " 12:30"} {
		if text, _ := expireTag(in); text == "" {
			t.Errorf("%q 应能解析出标签", in)
		}
	}
}

// TestExpireForeverIsAState 「永久」要是正式状态，不是"没填"。
//
// 这个区分很重要：空串 = 还没填（该显示成空白，提醒站长去填），
// 「永久」= 明确不会到期（该显式标出来）。混在一起的话，
// 站长看到空白分不清是"不限时间"还是"我忘了填"。
func TestExpireForeverIsAState(t *testing.T) {
	text, level := expireTag(expireForever)
	if text != "永久" {
		t.Errorf("「永久」应显示成「永久」，实际 %q", text)
	}
	if level == "" {
		t.Error("「永久」要有自己的档位，不能和普通标签一样")
	}
	// 空串还是"没填"，什么都不显示
	if text, level := expireTag(""); text != "" || level != "" {
		t.Errorf("空串不该显示标签，实际 (%q, %q)", text, level)
	}
}

// TestExpireTagTextShape 文案形状：过期带天数，提醒带天数。
func TestExpireTagTextShape(t *testing.T) {
	past := time.Now().AddDate(0, 0, -3).Format("2006-01-02")
	text, _ := expireTag(past)
	if want := "已过期 3 天"; text != want {
		t.Errorf("过期文案 = %q，应为 %q", text, want)
	}
	future := time.Now().AddDate(0, 0, 45).Format("2006-01-02")
	if text, _ := expireTag(future); text != fmt.Sprintf("剩 %d 天", 45) {
		t.Errorf("未来文案 = %q", text)
	}
}
