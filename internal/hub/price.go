package hub

// 续费费用的结构化表示。
//
// 为什么不直接存"100 元/年"这种自由文本：编辑时要把它拆回
// 金额 / 货币 / 周期三个部分才能填进弹窗，靠正则拆自由文本
// 是在给自己挖坑（"100元/年""￥100 / 年""100 CNY 每年"都要照顾）。
//
// 存成 JSON，显示时再拼。`price` 列是 TEXT，装得下。

import (
	"encoding/json"
	"strings"
)

// priceSpec 是一笔续费报价。
type priceSpec struct {
	Amount string `json:"amount"` // 只存数字，如 "100"
	Cur    string `json:"cur"`    // CNY | USD | HKD | JPY | EUR
	Per    string `json:"per"`    // year | month | quarter | once
}

// priceCurs 是支持的货币。**顺序就是下拉框的顺序**。
var priceCurs = []struct{ Code, Symbol, Label string }{
	{"CNY", "¥", "人民币"},
	{"USD", "$", "美元"},
	{"HKD", "HK$", "港币"},
	{"JPY", "¥", "日元"},
	{"EUR", "€", "欧元"},
}

// pricePers 是计费周期。
var pricePers = []struct{ Code, Label, Suffix string }{
	{"year", "每年", "/年"},
	{"quarter", "每季", "/季"},
	{"month", "每月", "/月"},
	{"once", "一次性", ""},
}

// parsePrice 把库里存的字符串解成结构。
//
// 解析失败（老数据、手填的自由文本）时返回 nil ——
// 调用方会把原文照原样显示，不走格式化。**不能静默丢内容。**
func parsePrice(raw string) *priceSpec {
	raw = strings.TrimSpace(raw)
	if raw == "" || !strings.HasPrefix(raw, "{") {
		return nil
	}
	var p priceSpec
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return nil
	}
	if strings.TrimSpace(p.Amount) == "" {
		return nil
	}
	return &p
}

// formatPrice 把结构拼成给人看的一句话，如「¥100 /年」。
//
// 解析不出结构就把原文返回 —— 老数据/手填的内容不能因为格式变了就消失。
func formatPrice(raw string) string {
	p := parsePrice(raw)
	if p == nil {
		return strings.TrimSpace(raw)
	}
	sym := p.Cur
	for _, c := range priceCurs {
		if c.Code == p.Cur {
			sym = c.Symbol
			break
		}
	}
	suffix := ""
	for _, q := range pricePers {
		if q.Code == p.Per {
			suffix = q.Suffix
			break
		}
	}
	out := sym + p.Amount
	if suffix != "" {
		out += " " + suffix
	}
	return out
}

// priceFromForm 从表单里读三件套并拼成 JSON。
//
// 金额为空就返回空串 —— 表示"没填"，而不是"0 元"。
func priceFromForm(amount, cur, per string) string {
	amount = strings.TrimSpace(amount)
	if amount == "" {
		return ""
	}
	// 只留数字和小数点，挡住 "100元" 这类输入
	var b strings.Builder
	for _, r := range amount {
		if (r >= '0' && r <= '9') || r == '.' {
			b.WriteRune(r)
		}
	}
	clean := b.String()
	if clean == "" || clean == "." {
		return ""
	}
	p := priceSpec{Amount: clean, Cur: cur, Per: per}
	if p.Cur == "" {
		p.Cur = "CNY"
	}
	if p.Per == "" {
		p.Per = "year"
	}
	out, err := json.Marshal(p)
	if err != nil {
		return ""
	}
	return string(out)
}

// 模板用的小函数。模板里不能写逻辑，只能调函数。

// priceText 给按钮当文案用，如「¥100 /年」。
func priceText(raw string) string { return formatPrice(raw) }

// priceAmount / priceCur / pricePer 给弹窗预填用。
// 解析不出结构就都返回空 —— 弹窗留空，让站长重新选。
func priceAmount(raw string) string {
	if p := parsePrice(raw); p != nil {
		return p.Amount
	}
	return ""
}

func priceCur(raw string) string {
	if p := parsePrice(raw); p != nil {
		return p.Cur
	}
	return ""
}

func pricePer(raw string) string {
	if p := parsePrice(raw); p != nil {
		return p.Per
	}
	return ""
}

// 模板里 range 用的列表（模板不能直接 range 匿名结构体切片）
type priceCurOption struct{ Code, Symbol, Label string }
type pricePerOption struct{ Code, Label, Suffix string }

func priceCursList() []priceCurOption {
	out := make([]priceCurOption, 0, len(priceCurs))
	for _, c := range priceCurs {
		out = append(out, priceCurOption{c.Code, c.Symbol, c.Label})
	}
	return out
}

func pricePersList() []pricePerOption {
	out := make([]pricePerOption, 0, len(pricePers))
	for _, p := range pricePers {
		out = append(out, pricePerOption{p.Code, p.Label, p.Suffix})
	}
	return out
}
