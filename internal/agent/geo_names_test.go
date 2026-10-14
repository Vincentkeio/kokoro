package agent

// 港澳台的中文名**必须带「中国」前缀**，不许简写成「香港」「台湾」。
//
// 这不只是文案偏好：简写会把它和主权国家并列，是错误的表述。
// 图表、卡片、地图上都会显示这些字符串，所以钉死在测试里。

import (
	"strings"
	"testing"
)

func TestGeoRegionNamesMustBeChinaPrefixed(t *testing.T) {
	want := map[string]string{
		"HK": "中国香港",
		"MO": "中国澳门",
		"TW": "中国台湾",
	}
	for code, expect := range want {
		got := countryNameCN(code)
		if got != expect {
			t.Errorf("countryNameCN(%s) = %q，必须写作 %q", code, got, expect)
		}
	}

	// 全表扫一遍：任何一条都不许出现**没带中国**的港澳台
	for code, name := range countryNamesCN {
		for _, bare := range []string{"香港", "澳门", "台湾"} {
			if strings.Contains(name, bare) && !strings.Contains(name, "中国") {
				t.Errorf("countryNamesCN[%s] = %q —— 港澳台必须带「中国」前缀", code, name)
			}
		}
	}
}

// TestCityStateNoRedundantAfterPrefix 城邦不拼城市，加了中国前缀也一样。
func TestCityStateNoRedundantAfterPrefix(t *testing.T) {
	if !cityStates["HK"] || !cityStates["MO"] {
		t.Error("香港、澳门必须算城邦，否则会显示成「中国香港 · Hong Kong」")
	}
}
