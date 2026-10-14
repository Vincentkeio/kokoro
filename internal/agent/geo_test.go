package agent

// 位置探测：国家码要认得出，城市要按 colo 落点推。

import (
	"strings"
	"testing"
)

// TestColoCityKnown 常见的 Cloudflare 数据中心码要能翻译成城市。
//
// 这几个是本站在用的（HKG/NRT/LAX/SIN），其余是防手滑。
func TestColoCityKnown(t *testing.T) {
	for colo, want := range map[string]string{
		"HKG": "Hong Kong", "NRT": "Tokyo", "LAX": "Los Angeles",
		"SIN": "Singapore", "ICN": "Seoul", "FRA": "Frankfurt",
	} {
		if got := coloCity(colo); got != want {
			t.Errorf("coloCity(%s) = %q，应为 %q", colo, got, want)
		}
	}
	// 没收录的宁可返回空，也不要瞎猜一个城市出来
	if got := coloCity("ZZZ"); got != "" {
		t.Errorf("没收录的 colo 应返回空，实际 %q", got)
	}
}

// TestCountryNameCN 常见国家要有中文名。
func TestCountryNameCN(t *testing.T) {
	for code, want := range map[string]string{
		"JP": "日本", "HK": "中国香港", "SG": "新加坡", "US": "美国",
	} {
		if got := countryNameCN(code); got != want {
			t.Errorf("countryNameCN(%s) = %q，应为 %q", code, got, want)
		}
	}
	if got := countryNameCN("ZZ"); got != "" {
		t.Errorf("没收录的国家应返回空，实际 %q", got)
	}
}

// TestGeoCountryCodesAreTwoUpper 国家码表里的键必须都是两位大写 ——
// 探测出来的 loc 是大写的，键写错了就永远匹配不上（静默失效）。
func TestGeoCountryCodesAreTwoUpper(t *testing.T) {
	for code := range countryNamesCN {
		if len(code) != 2 || code != strings.ToUpper(code) {
			t.Errorf("国家码 %q 不是两位大写，跟 CF 返回的 loc 对不上", code)
		}
	}
	for colo := range coloCities {
		if len(colo) != 3 || colo != strings.ToUpper(colo) {
			t.Errorf("colo %q 不是三位大写，跟 CF 返回的对不上", colo)
		}
	}
}

// TestRegionNoRedundantCity 城邦型地区不要拼成「中国香港 · Hong Kong」。
func TestRegionNoRedundantCity(t *testing.T) {
	// 复刻 detectGeo 的拼接逻辑。
	// ⚠️ 判据是**国家码**，不是"国家名里含不含城市名" ——
	// 国家名是中文、城市名是英文，字符串包含判断永远不成立
	// （我第一版就是这么写的，测试立刻抓到 HK/SG 变成了"xx · xx"）。
	join := func(loc, city string) string {
		region := countryNameCN(loc)
		if city != "" && !cityStates[loc] {
			if region != "" {
				region += " · " + city
			} else {
				region = city
			}
		}
		return region
	}
	cases := []struct {
		loc, city, want string
	}{
		{"HK", "Hong Kong", "中国香港"}, // 城邦，不重复
		{"SG", "Singapore", "新加坡"},
		{"MO", "Macao", "中国澳门"},
		{"JP", "Tokyo", "日本 · Tokyo"}, // 正常拼
		{"US", "Los Angeles", "美国 · Los Angeles"},
		{"JP", "", "日本"}, // 只有国家
	}
	for _, c := range cases {
		if got := join(c.loc, c.city); got != c.want {
			t.Errorf("join(%q, %q) = %q，应为 %q", c.loc, c.city, got, c.want)
		}
	}
	// 没收录的国家：至少把城市带出来，别是空的
	if got := join("ZZ", "Tokyo"); got != "Tokyo" {
		t.Errorf("没收录的国家应退回城市，实际 %q", got)
	}
}
