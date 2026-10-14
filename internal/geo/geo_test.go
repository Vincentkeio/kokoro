package geo

import (
	"math"
	"testing"
)

// TestLookupCity 城市命中优先于国家质心。
func TestLookupCity(t *testing.T) {
	cases := []struct {
		name    string
		country string
		region  string
		city    string
		wantLat float64
		wantLon float64
	}{
		{"city 字段中文", "JP", "日本", "东京", 35.6762, 139.6503},
		{"city 字段英文", "US", "United States", "Los Angeles", 34.0522, -118.2437},
		{"region 里带城市", "JP", "日本 · 东京", "", 35.6762, 139.6503},
		{"region 英文逗号", "US", "Los Angeles, US", "", 34.0522, -118.2437},
		{"港澳台有独立坐标", "HK", "中国香港", "", 22.3193, 114.1694},
		{"中国台湾", "TW", "中国台湾 · 台北", "", 25.0330, 121.5654},
	}
	for _, c := range cases {
		got, ok := Lookup(c.country, c.region, c.city)
		if !ok {
			t.Errorf("%s: 应能识别，实际 ok=false", c.name)
			continue
		}
		if math.Abs(got.Lat-c.wantLat) > 0.01 || math.Abs(got.Lon-c.wantLon) > 0.01 {
			t.Errorf("%s: 得到 (%.4f, %.4f)，应为 (%.4f, %.4f)",
				c.name, got.Lat, got.Lon, c.wantLat, c.wantLon)
		}
	}
}

// TestLookupFallsBackToCountry 只有国家信息时回退到质心。
func TestLookupFallsBackToCountry(t *testing.T) {
	got, ok := Lookup("DE", "德国", "")
	if !ok {
		t.Fatal("德国应能回退到质心")
	}
	// 德国质心大约在 (51, 10)
	if got.Lat < 47 || got.Lat > 55 || got.Lon < 5 || got.Lon > 16 {
		t.Errorf("德国质心 (%.2f, %.2f) 不在合理范围", got.Lat, got.Lon)
	}
	// 中文国名也要能认
	got2, ok2 := Lookup("", "日本", "")
	if !ok2 || math.Abs(got2.Lat-36.6) > 2 {
		t.Errorf("中文国名回退失败: %+v ok=%v", got2, ok2)
	}
}

// TestLookupUnknown 认不出来必须返回 false。
//
// 这条很重要：画错位置比不画更糟。宁可让地球少一个点，
// 也不要把一台小鸡摆在错误的大洲上。
func TestLookupUnknown(t *testing.T) {
	for _, c := range []struct{ country, region, city string }{
		{"", "", ""},
		{"", "某个不存在的地方", ""},
		{"ZZ", "", ""},
	} {
		if _, ok := Lookup(c.country, c.region, c.city); ok {
			t.Errorf("(%q,%q,%q) 不该识别成功", c.country, c.region, c.city)
		}
	}
}

// TestCountryCentroidsSane 生成的坐标表不能有明显离谱的条目。
func TestCountryCentroidsSane(t *testing.T) {
	if len(countryCentroids) < 150 {
		t.Fatalf("国家表只有 %d 条，生成器可能出问题了", len(countryCentroids))
	}
	for code, v := range countryCentroids {
		lat, lon := v[0], v[1]
		if lat < -90 || lat > 90 || lon < -180 || lon > 180 {
			t.Errorf("%s 坐标越界: (%f, %f)", code, lat, lon)
		}
		if lat == 0 && lon == 0 {
			t.Errorf("%s 落在几内亚湾，多半是没算出质心", code)
		}
	}
	// 抽查几个不可能错的
	for code, want := range map[string][2]float64{
		"US": {39.5, -99.1}, "JP": {36.6, 138.0}, "SG": {1.37, 103.8},
	} {
		got, ok := countryCentroids[code]
		if !ok {
			t.Errorf("缺少 %s", code)
			continue
		}
		if math.Abs(got[0]-want[0]) > 3 || math.Abs(got[1]-want[1]) > 3 {
			t.Errorf("%s = (%.2f, %.2f)，期望接近 (%.2f, %.2f)", code, got[0], got[1], want[0], want[1])
		}
	}
}
