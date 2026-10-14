package netprobe

import "testing"

// 上游原始数据的解析（后台「更新探测点列表」按钮走这条路径）。
func TestParseUpstreamNodesNormalizes(t *testing.T) {
	raw := []byte(`[
	  {"province":"河北省","isp_code":"cu","isp":"联通","ip_version":"v4",
	   "ips":"110.249.198.54,110.249.198.3,110.249.198.13,9.9.9.9","latency_ms":251.81},
	  {"province":"河北省","isp_code":"cu","isp":"联通","ip_version":"v6",
	   "ips":"2408:871a::1"},
	  {"province":"北京市","isp_code":"ct","isp":"电信","ip_version":"v4",
	   "ips":"106.37.68.13"},
	  {"province":"不存在的地区","isp_code":"ct","isp":"电信","ip_version":"v4","ips":"1.2.3.4"}
	]`)
	ts, err := ParseUpstreamNodes(raw)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(ts) != 2 {
		t.Fatalf("应得 2 个点（河北联通 + 北京电信），实际 %d: %+v", len(ts), ts)
	}
	var hebei *Target
	for i := range ts {
		if ts[i].Province == "河北" && ts[i].ISP == ISPUnicom {
			hebei = &ts[i]
		}
	}
	if hebei == nil {
		t.Fatalf("没有河北联通：%+v", ts)
	}
	// 省份转简称、运营商转代码、名称拼好
	if hebei.Name != "河北联通" {
		t.Errorf("Name = %q", hebei.Name)
	}
	// 只留 3 个候选（上游给了 4 个）
	if len(hebei.Hosts) != 3 {
		t.Errorf("候选应截到 3 个，实际 %v", hebei.Hosts)
	}
	if hebei.Host != hebei.Hosts[0] {
		t.Errorf("Host 应等于 Hosts[0]：%q vs %v", hebei.Host, hebei.Hosts)
	}
}

func TestParseUpstreamNodesRejectsGarbage(t *testing.T) {
	if _, err := ParseUpstreamNodes([]byte(`{"not":"a list"}`)); err == nil {
		t.Error("结构不对时应当报错")
	}
	if _, err := ParseUpstreamNodes([]byte(`[{"province":"火星","isp_code":"ct","ip_version":"v4","ips":"1.2.3.4"}]`)); err == nil {
		t.Error("一个可用点都解析不出来时应当报错，别把空表当成功")
	}
}

// 覆盖目录要影响 Catalog/CatalogVersion，且能退回内置表。
func TestSetCatalogOverridesAndResets(t *testing.T) {
	base := CatalogVersion()
	baseLen := len(Catalog())

	SetCatalog([]Target{{Province: "测试省", ISP: ISPTelecom, Name: "测试省电信", Host: "1.2.3.4"}})
	if got := len(Catalog()); got != 1 {
		t.Fatalf("覆盖后应只剩 1 个点，实际 %d", got)
	}
	if CatalogVersion() == base {
		t.Error("覆盖后版本号应改变")
	}
	// Hosts 缺省时自动补成 [Host]
	if c := Catalog(); len(c) != 1 || len(c[0].Hosts) != 1 || c[0].Hosts[0] != "1.2.3.4" {
		t.Errorf("Hosts 应自动补成 [Host]：%+v", c)
	}

	SetCatalog(nil)
	if len(Catalog()) != baseLen || CatalogVersion() != base {
		t.Error("清空覆盖后应退回内置表")
	}
}
