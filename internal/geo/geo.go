// Package geo 把「国家 / 地区 / 城市」文本换算成经纬度，
// 供首页的点阵地球把小鸡摆到正确位置。
//
// 为什么需要它：`model.Node` 只存了 country / region / city 三个文本字段，
// 没有坐标。与其改协议让 agent 上报坐标（要动 agent、要重新发版），
// 不如在服务端按文本回退到一个近似位置——探针面板只需要「大概在哪儿」。
//
// 优先级：city 精确命中 > region 文本里出现的城市名 > 国家质心。
// 都认不出来时返回 ok=false，前端就不画这个点（画错位置比不画更糟）。
package geo

import (
	"strings"

	"github.com/kokoro-probe/kokoro/internal/flags"
)

// Coord 是 WGS-84 经纬度。
type Coord struct {
	Lat float64
	Lon float64
}

// manualOverrides 补 world.json 里没有独立要素的地区。
//
// 合规说明（不可变更）：TW / HK / MO 分别是中国的台湾地区、香港特别行政区、
// 澳门特别行政区。world.json 把三者并入中国国土（因此 countries.go 里没有
// 独立的 HK/TW/MO 条目），但坐标要单独给，否则这三地的节点会落到大陆质心上。
// **不要**把它们当成独立国家的坐标来理解。
var manualOverrides = map[string]Coord{
	"HK": {22.3193, 114.1694}, // 中国香港
	"MO": {22.1987, 113.5439}, // 中国澳门
	"TW": {23.6978, 120.9605}, // 中国台湾
}

// cityCoords 常见探针落点的城市坐标。
//
// 键是小写、去空格的写法，中英文都收——region 字段里两种都可能出现
// （「日本 · 东京」「Japan · Tokyo」）。
var cityCoords = map[string]Coord{
	// 中国内地
	"北京": {39.9042, 116.4074}, "beijing": {39.9042, 116.4074},
	"上海": {31.2304, 121.4737}, "shanghai": {31.2304, 121.4737},
	"广州": {23.1291, 113.2644}, "guangzhou": {23.1291, 113.2644},
	"深圳": {22.5431, 114.0579}, "shenzhen": {22.5431, 114.0579},
	"杭州": {30.2741, 120.1551}, "hangzhou": {30.2741, 120.1551},
	"成都": {30.5728, 104.0668}, "chengdu": {30.5728, 104.0668},
	"重庆": {29.5630, 106.5516}, "chongqing": {29.5630, 106.5516},
	"武汉": {30.5928, 114.3055}, "wuhan": {30.5928, 114.3055},
	"西安": {34.3416, 108.9398}, "xi'an": {34.3416, 108.9398},
	"南京": {32.0603, 118.7969}, "nanjing": {32.0603, 118.7969},
	"青岛": {36.0671, 120.3826}, "qingdao": {36.0671, 120.3826},
	"天津": {39.3434, 117.3616},
	// 港澳台
	"香港": {22.3193, 114.1694}, "hongkong": {22.3193, 114.1694},
	"澳门": {22.1987, 113.5439}, "macao": {22.1987, 113.5439}, "macau": {22.1987, 113.5439},
	"台北": {25.0330, 121.5654}, "taipei": {25.0330, 121.5654},
	"高雄": {22.6273, 120.3014}, "kaohsiung": {22.6273, 120.3014},
	"新竹": {24.8138, 120.9675},
	// 亚洲其他
	"东京": {35.6762, 139.6503}, "tokyo": {35.6762, 139.6503},
	"大阪": {34.6937, 135.5023}, "osaka": {34.6937, 135.5023},
	"神奈川": {35.4478, 139.6425},
	"首尔":  {37.5665, 126.9780}, "seoul": {37.5665, 126.9780},
	"新加坡": {1.3521, 103.8198}, "singapore": {1.3521, 103.8198},
	"曼谷": {13.7563, 100.5018}, "bangkok": {13.7563, 100.5018},
	"吉隆坡": {3.1390, 101.6869}, "kualalumpur": {3.1390, 101.6869},
	"雅加达": {-6.2088, 106.8456}, "jakarta": {-6.2088, 106.8456},
	"孟买": {19.0760, 72.8777}, "mumbai": {19.0760, 72.8777},
	"迪拜": {25.2048, 55.2708}, "dubai": {25.2048, 55.2708},
	// 北美
	"洛杉矶": {34.0522, -118.2437}, "losangeles": {34.0522, -118.2437},
	"圣何塞": {37.3382, -121.8863}, "sanjose": {37.3382, -121.8863},
	"西雅图": {47.6062, -122.3321}, "seattle": {47.6062, -122.3321},
	"达拉斯": {32.7767, -96.7970}, "dallas": {32.7767, -96.7970},
	"芝加哥": {41.8781, -87.6298}, "chicago": {41.8781, -87.6298},
	"纽约": {40.7128, -74.0060}, "newyork": {40.7128, -74.0060},
	"阿什本": {39.0438, -77.4874}, "ashburn": {39.0438, -77.4874},
	"凤凰城": {33.4484, -112.0740}, "phoenix": {33.4484, -112.0740},
	"多伦多": {43.6532, -79.3832}, "toronto": {43.6532, -79.3832},
	// 欧洲
	"伦敦": {51.5074, -0.1278}, "london": {51.5074, -0.1278},
	"法兰克福": {50.1109, 8.6821}, "frankfurt": {50.1109, 8.6821},
	"阿姆斯特丹": {52.3676, 4.9041}, "amsterdam": {52.3676, 4.9041},
	"巴黎": {48.8566, 2.3522}, "paris": {48.8566, 2.3522},
	"柏林": {52.5200, 13.4050}, "berlin": {52.5200, 13.4050},
	"莫斯科": {55.7558, 37.6173}, "moscow": {55.7558, 37.6173},
	"苏黎世": {47.3769, 8.5417}, "zurich": {47.3769, 8.5417},
	"斯德哥尔摩": {59.3293, 18.0686}, "stockholm": {59.3293, 18.0686},
	// 大洋洲 / 南美
	"悉尼": {-33.8688, 151.2093}, "sydney": {-33.8688, 151.2093},
	"墨尔本": {-37.8136, 144.9631}, "melbourne": {-37.8136, 144.9631},
	"圣保罗": {-23.5505, -46.6333}, "saopaulo": {-23.5505, -46.6333},
}

// foldKey 与 cityCoords 的键保持一致：小写、去掉分隔与标点。
func foldKey(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r > 127: // 中文等非 ASCII 原样保留
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Lookup 尽最大努力确定坐标。第二个返回值为 false 表示认不出来。
func Lookup(country, region, city string) (Coord, bool) {
	// 1) city 字段直接命中
	if c, ok := cityCoords[foldKey(city)]; ok {
		return c, true
	}

	// 2) region 里可能写着城市（"日本 · 东京" / "Los Angeles, US"）。
	//
	//    ⚠️ 光按分隔符切词是不够的：多词城市名会被空格切开，
	//    "Los Angeles" 变成 "Los" + "Angeles" 两个都不认识。
	//    所以切完还要试**相邻词拼接**（最多 3 个词）。
	toks := tokenize(region)
	for i := range toks {
		for j := i + 1; j <= len(toks) && j <= i+3; j++ {
			if c, ok := cityCoords[foldKey(strings.Join(toks[i:j], ""))]; ok {
				return c, true
			}
		}
	}

	// 3) 回退到国家质心
	code := flags.Normalize(country)
	if code == "" {
		// country 为空时，从 region 里猜国家（"日本 · 东京" 能猜出 JP）
		code = flags.Normalize(region)
	}
	if code == "" {
		return Coord{}, false
	}
	if c, ok := manualOverrides[code]; ok {
		return c, true
	}
	if v, ok := countryCentroids[code]; ok {
		return Coord{Lat: v[0], Lon: v[1]}, true
	}
	return Coord{}, false
}

// tokenize 把地区文本切成候选词：按常见分隔符断开。
func tokenize(s string) []string {
	fields := strings.FieldsFunc(s, func(r rune) bool {
		switch r {
		case ' ', '·', ',', '，', '/', '|', '-', '_', '(', ')', '[', ']':
			return true
		}
		return false
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}
