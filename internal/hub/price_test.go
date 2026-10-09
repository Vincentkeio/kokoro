package hub

// 续费费用：结构化存、格式化显示、弹窗预填。

import (
	"strings"
	"testing"
)

// TestPriceRoundTrip 存进去再读出来要还是那句话。
func TestPriceRoundTrip(t *testing.T) {
	raw := priceFromForm("100", "CNY", "year")
	if raw == "" {
		t.Fatal("拼出来是空的")
	}
	if got := formatPrice(raw); got != "¥100 /年" {
		t.Errorf("格式化 = %q，应为 ¥100 /年", got)
	}
	// 弹窗预填要能拆回去
	if got := priceAmount(raw); got != "100" {
		t.Errorf("金额 = %q", got)
	}
	if got := priceCur(raw); got != "CNY" {
		t.Errorf("货币 = %q", got)
	}
	if got := pricePer(raw); got != "year" {
		t.Errorf("周期 = %q", got)
	}
}

// TestPriceOnlyDigits 金额里的非数字要滤掉。
//
// 站长很可能直接输「100元」—— 存进去会变成 "¥100元 /年"，
// 难看而且下次预填时拆不出来。
func TestPriceOnlyDigits(t *testing.T) {
	raw := priceFromForm("100元", "CNY", "year")
	if got := priceAmount(raw); got != "100" {
		t.Errorf("「100元」应滤成 100，实际 %q", got)
	}
	if got := formatPrice(raw); strings.Contains(got, "元") == false && got != "¥100 /年" {
		t.Errorf("格式化 = %q", got)
	}
}

// TestPriceEmptyMeansUnset 金额空了就是"不填"，不是 0 元。
func TestPriceEmptyMeansUnset(t *testing.T) {
	for _, in := range []string{"", "  ", "元", ".", "abc"} {
		if got := priceFromForm(in, "CNY", "year"); got != "" {
			t.Errorf("金额 %q 应该是空（不填），实际存成 %q", in, got)
		}
	}
}

// TestPriceLegacyTextKept 老数据（自由文本）不能被吃掉。
//
// 这个字段以前是纯文本手填的。解析失败时必须**原样显示**，
// 不能因为格式变了就显示空白 —— 那等于把站长填的东西弄丢了。
func TestPriceLegacyTextKept(t *testing.T) {
	for _, legacy := range []string{"100 元/年", "￥100/年", "面议"} {
		if got := formatPrice(legacy); got != legacy {
			t.Errorf("老数据 %q 应原样显示，实际 %q", legacy, got)
		}
		// 拆不出结构就都返回空（弹窗留空让站长重选），不能崩
		if priceAmount(legacy) != "" || priceCur(legacy) != "" {
			t.Errorf("老数据 %q 不该被拆出结构", legacy)
		}
	}
	// 空串还是空串
	if got := formatPrice(""); got != "" {
		t.Errorf("空值 = %q", got)
	}
}

// TestPriceCurrencyAndPeriodTables 货币和周期表要有内容，且 code 不重复。
func TestPriceCurrencyAndPeriodTables(t *testing.T) {
	if len(priceCursList()) < 3 {
		t.Error("货币选项太少")
	}
	if len(pricePersList()) < 3 {
		t.Error("周期选项太少")
	}
	seen := map[string]bool{}
	for _, c := range priceCursList() {
		if seen[c.Code] {
			t.Errorf("货币 %s 重复", c.Code)
		}
		seen[c.Code] = true
	}
	// 每个货币都要有符号，否则会显示成 "CNY100" 这种
	for _, c := range priceCursList() {
		if c.Symbol == "" {
			t.Errorf("货币 %s 没有符号", c.Code)
		}
	}
	// "一次性"不该有后缀（"¥100 /次"读起来怪）
	for _, p := range pricePersList() {
		if p.Code == "once" && p.Suffix != "" {
			t.Errorf("一次性不该有周期后缀，实际 %q", p.Suffix)
		}
	}
}
