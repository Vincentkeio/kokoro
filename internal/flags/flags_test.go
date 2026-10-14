package flags

import (
	"encoding/xml"
	"strings"
	"testing"
)

// TestAllSVGWellFormed 保证 embed 进来的每个 SVG 都能被 XML 解析器读通，
// 防止下载过程中混进 HTML 错误页或截断文件。
func TestAllSVGWellFormed(t *testing.T) {
	codes := Codes()
	if len(codes) < 100 {
		t.Fatalf("可用国旗太少：%d 个，检查 svg/ 目录", len(codes))
	}
	for _, code := range codes {
		src := SVG(code)
		if src == "" {
			t.Errorf("%s: 读不到 SVG", code)
			continue
		}
		if err := xml.Unmarshal([]byte(src), new(struct {
			XMLName xml.Name
		})); err != nil {
			t.Errorf("%s: SVG 不是合法 XML: %v", code, err)
		}
		if strings.Contains(src, "<script") {
			t.Errorf("%s: SVG 里出现 script，不能内联", code)
		}
		if strings.Contains(src, "\n") {
			t.Errorf("%s: 未压缩，仍含换行", code)
		}
	}
}

func TestNormalize(t *testing.T) {
	cases := map[string]string{
		"us":              "US",
		"US":              "US",
		"Us":              "US",
		"United States":   "US",
		"USA":             "US",
		"美国":              "US",
		"美国 · 洛杉矶":        "US",
		"Los Angeles, US": "US",
		"日本":              "JP",
		"Japan":           "JP",
		"jp":              "JP",
		"香港":              "HK",
		"中国香港":            "HK",
		"Hong Kong":       "HK",
		"台湾":              "TW",
		"中国台湾":            "TW",
		"Taiwan":          "TW",
		"澳门":              "MO",
		"中国澳门":            "MO",
		"中国":              "CN",
		"China":           "CN",
		"":                "",
		"火星":              "",
		"zz":              "",
	}
	for in, want := range cases {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q，期望 %q", in, got, want)
		}
	}
}

// TestComplianceNames 守住红线：港澳台的中文名必须是「中国…」的写法。
func TestComplianceNames(t *testing.T) {
	want := map[string]string{
		"TW": "中国台湾",
		"HK": "中国香港",
		"MO": "中国澳门",
	}
	for code, name := range want {
		if got := Name(code); got != name {
			t.Errorf("Name(%s) = %q，期望 %q", code, got, name)
		}
	}
	// 反向归一化也要落到正确的代码上
	for _, in := range []string{"中国台湾", "台湾", "中国香港", "香港", "中国澳门", "澳门"} {
		if Normalize(in) == "" {
			t.Errorf("Normalize(%q) 应该能识别", in)
		}
	}
}

// TestTaiwanUsesChineseTaipeiFlag 是合规红线测试：
// 台湾必须显示「中华台北奥委会旗」（梅花 + 青天白日 + 奥运五环），
// 不能用上游数据集里的中华民国旗。判据是五环的五种颜色必须齐全 ——
// 这是奥林匹克旗的独有特征，普通国旗不会有。
func TestTaiwanUsesChineseTaipeiFlag(t *testing.T) {
	src := SVG("TW")
	if src == "" {
		t.Fatal("TW 没有国旗数据")
	}
	rings := map[string]string{
		"#0083ce": "蓝环",
		"#e61446": "红环",
		"#009c45": "绿环",
		"#f7b000": "黄环",
		"#000006": "黑环",
	}
	for color, name := range rings {
		if !strings.Contains(src, color) {
			t.Errorf("tw.svg 缺少奥运%s（%s）—— 台湾必须用中华台北奥委会旗", name, color)
		}
	}
	if !strings.Contains(src, "中华台北") {
		t.Error("tw.svg 里应保留「中华台北」出处的注释")
	}
	// 上游数据集里那面旗是红底 + 蓝矩形旗角，这里不该出现
	if strings.Contains(src, "#fe0000") || strings.Contains(src, "#000095") {
		t.Error("tw.svg 疑似仍在使用中华民国旗的配色")
	}
}

func TestUnknownInputIsSafe(t *testing.T) {
	for _, in := range []string{"", "没有这个国家", "ZZ", "!!!", "12345"} {
		if got := SVG(in); got != "" {
			t.Errorf("SVG(%q) 应返回空串，实际 %d 字节", in, len(got))
		}
		if !strings.Contains(Name("ZZ"), "ZZ") && Normalize("ZZ") != "" {
			t.Errorf("未知代码不该被改名")
		}
	}
}

// TestSizeBudget 守住体积预算：全套 SVG 不能太胖，否则二进制会明显变大。
func TestSizeBudget(t *testing.T) {
	var total int
	for _, code := range Codes() {
		total += len(SVG(code))
	}
	t.Logf("全部 %d 面国旗原始体积：%.0f KB", len(Codes()), float64(total)/1024)
	if total > 900*1024 {
		t.Errorf("国旗数据 %.0f KB 超出 900KB 预算，请删减冷门国家或压缩", float64(total)/1024)
	}
}
