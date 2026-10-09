package agent

// 探测本机所在的位置（国家 + 城市）。
//
// 为什么不由 Hub 按 IP 反查：**IP 归属库对 VPS 不准**。
// 实测这台站上的 5 台里就有 2 台会被判错：
//   - zouter：IP 是美国段（216.23.83.149），机器实际在东京
//   - ByteVirt：IP 是美国段（188.253.125.101），机器实际在新加坡
// 因为 VPS 商经常拿别处注册的 IP 段开出机器。
//
// 改成让 agent **从自己所在的网络**去问 Cloudflare：
// `https://www.cloudflare.com/cdn-cgi/trace` 返回的 `loc` 是 CF 按
// 真实网络路径（他们自己的 GeoIP + anycast 落点）判的，准得多；
// 还会返回 `colo`（请求落到了哪个 CF 数据中心），能进一步推出城市。
//
// 拿不到就返回空 —— Hub 侧看到空值不动原值，不会把已有的地区抹掉。

import (
	"bufio"
	"net/http"
	"strings"
	"sync"
	"time"
)

// geoProbeURL 是 Cloudflare 的 trace 端点：纯文本、几十字节、不要 key。
const geoProbeURL = "https://www.cloudflare.com/cdn-cgi/trace"

// geoProbeTimeout 短一点 —— 这只是锦上添花的信息，
// 不值得为它拖慢启动或上报。
const geoProbeTimeout = 8 * time.Second

// detectGeo 返回 (国家码, 地区文案)。两个都可能为空。
func detectGeo() (string, string) {
	cli := &http.Client{Timeout: geoProbeTimeout}
	resp, err := cli.Get(geoProbeURL)
	if err != nil {
		return "", ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", ""
	}

	var loc, colo string
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 8*1024), 64*1024)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "loc="):
			loc = strings.ToUpper(strings.TrimSpace(line[4:]))
		case strings.HasPrefix(line, "colo="):
			colo = strings.ToUpper(strings.TrimSpace(line[5:]))
		}
	}
	if len(loc) != 2 {
		loc = "" // 不是两位国家码就当没拿到
	}
	if loc == "" {
		return "", ""
	}

	// 城市用 colo（最近的数据中心）推。
	city := coloCity(colo)

	// 地区文案拼成"国家 · 城市"。
	//
	// ⚠️ 但**城邦**不要拼 —— "中国香港 · Hong Kong"、"新加坡 · Singapore"
	// 是同一个地方写两遍，纯属噪音。
	//
	// 判据用显式的城邦清单，**不能用"国家名里含不含城市名"**
	// —— 国家名是中文、城市名是英文，字符串包含判断永远不成立。
	region := countryNameCN(loc)
	if city != "" && !cityStates[loc] {
		if region != "" {
			region += " · " + city
		} else {
			region = city
		}
	}
	return loc, region
}

// cityStates 是"国家就是城市"的地方：地区文案里不用再拼城市名。
//
// 只放确实是城邦/城市型经济体的 —— 放多了会漏掉真实城市，
// 放少了会出现"中国香港 · Hong Kong"这种重复。
var cityStates = map[string]bool{
	"HK": true, // 中国香港
	"MO": true, // 中国澳门
	"SG": true, // 新加坡
	"MC": true, // 摩纳哥
	"LU": true, // 卢森堡
	"MT": true, // 马耳他
	"BH": true, // 巴林
	"GI": true, // 直布罗陀
}

// coloCity 把 Cloudflare 的三字机场码翻成城市名。
//
// 只列常用的那些 —— 没列到的就只显示国家，不瞎猜。
func coloCity(colo string) string {
	return coloCities[colo]
}

var coloCities = map[string]string{
	// 大中华区
	"HKG": "Hong Kong", "TPE": "Taipei", "MFM": "Macao",
	"PEK": "Beijing", "PVG": "Shanghai", "CAN": "Guangzhou",
	"SZX": "Shenzhen", "CTU": "Chengdu", "XIY": "Xi'an",
	"WUH": "Wuhan", "CKG": "Chongqing", "HGH": "Hangzhou",
	"TSN": "Tianjin", "NKG": "Nanjing", "CSX": "Changsha",
	"FOC": "Fuzhou", "XMN": "Xiamen", "TAO": "Qingdao",
	"KMG": "Kunming", "DLC": "Dalian", "SJW": "Shijiazhuang",
	// 日韩
	"NRT": "Tokyo", "HND": "Tokyo", "KIX": "Osaka", "ITM": "Osaka",
	"FUK": "Fukuoka", "CTS": "Sapporo", "NGO": "Nagoya", "OKA": "Okinawa",
	"ICN": "Seoul", "GMP": "Seoul", "PUS": "Busan",
	// 东南亚
	"SIN": "Singapore", "KUL": "Kuala Lumpur", "BKK": "Bangkok",
	"CGK": "Jakarta", "MNL": "Manila", "HAN": "Hanoi", "SGN": "Ho Chi Minh",
	// 南亚 / 中东
	"BOM": "Mumbai", "DEL": "Delhi", "MAA": "Chennai", "BLR": "Bangalore",
	"DXB": "Dubai", "AUH": "Abu Dhabi", "TLV": "Tel Aviv",
	// 欧洲
	"LHR": "London", "LGW": "London", "MAN": "Manchester",
	"CDG": "Paris", "MRS": "Marseille", "AMS": "Amsterdam",
	"FRA": "Frankfurt", "MUC": "Munich", "BER": "Berlin", "DUS": "Dusseldorf",
	"MAD": "Madrid", "BCN": "Barcelona", "LIS": "Lisbon",
	"MXP": "Milan", "FCO": "Rome", "ZRH": "Zurich", "VIE": "Vienna",
	"ARN": "Stockholm", "CPH": "Copenhagen", "OSL": "Oslo", "HEL": "Helsinki",
	"WAW": "Warsaw", "PRG": "Prague", "BUD": "Budapest", "OTP": "Bucharest",
	"DUB": "Dublin", "BRU": "Brussels", "KBP": "Kyiv", "IST": "Istanbul",
	"LED": "St Petersburg", "DME": "Moscow", "SVO": "Moscow",
	// 北美
	"LAX": "Los Angeles", "SJC": "San Jose", "SFO": "San Francisco",
	"SEA": "Seattle", "PDX": "Portland", "LAS": "Las Vegas",
	"PHX": "Phoenix", "DEN": "Denver", "DFW": "Dallas", "IAH": "Houston",
	"ORD": "Chicago", "MSP": "Minneapolis", "DTW": "Detroit",
	"ATL": "Atlanta", "MIA": "Miami", "IAD": "Washington",
	"EWR": "New York", "JFK": "New York", "BOS": "Boston",
	"YYZ": "Toronto", "YVR": "Vancouver", "YUL": "Montreal",
	"MEX": "Mexico City",
	// 南美
	"GRU": "Sao Paulo", "EZE": "Buenos Aires", "SCL": "Santiago",
	"BOG": "Bogota", "LIM": "Lima",
	// 大洋洲 / 非洲
	"SYD": "Sydney", "MEL": "Melbourne", "BNE": "Brisbane",
	"PER": "Perth", "AKL": "Auckland",
	"JNB": "Johannesburg", "CPT": "Cape Town", "LOS": "Lagos",
	"NBO": "Nairobi", "CAI": "Cairo",
}

// countryNameCN 把两位国家码翻成中文名，用于地区文案。
//
// 只覆盖常见国家 —— 没列到就用国家码本身（至少国旗还能画出来）。
func countryNameCN(code string) string {
	if n, ok := countryNamesCN[code]; ok {
		return n
	}
	return ""
}

var countryNamesCN = map[string]string{
	"CN": "中国大陆", "HK": "中国香港", "MO": "中国澳门", "TW": "中国台湾",
	"JP": "日本", "KR": "韩国", "SG": "新加坡", "MY": "马来西亚",
	"TH": "泰国", "VN": "越南", "PH": "菲律宾", "ID": "印度尼西亚",
	"IN": "印度", "PK": "巴基斯坦", "BD": "孟加拉", "LK": "斯里兰卡",
	"AE": "阿联酋", "SA": "沙特", "IL": "以色列", "TR": "土耳其",
	"GB": "英国", "DE": "德国", "FR": "法国", "NL": "荷兰",
	"IT": "意大利", "ES": "西班牙", "PT": "葡萄牙", "CH": "瑞士",
	"AT": "奥地利", "SE": "瑞典", "NO": "挪威", "DK": "丹麦",
	"FI": "芬兰", "PL": "波兰", "CZ": "捷克", "HU": "匈牙利",
	"RO": "罗马尼亚", "IE": "爱尔兰", "BE": "比利时", "UA": "乌克兰",
	"RU": "俄罗斯", "GR": "希腊", "BG": "保加利亚", "LT": "立陶宛",
	"LV": "拉脱维亚", "EE": "爱沙尼亚", "RS": "塞尔维亚", "HR": "克罗地亚",
	"US": "美国", "CA": "加拿大", "MX": "墨西哥", "BR": "巴西",
	"AR": "阿根廷", "CL": "智利", "CO": "哥伦比亚", "PE": "秘鲁",
	"AU": "澳大利亚", "NZ": "新西兰", "ZA": "南非", "EG": "埃及",
	"NG": "尼日利亚", "KE": "肯尼亚", "MA": "摩洛哥",
}

// geoOnce 缓存位置探测结果：探测要走网络，不该每 5 分钟重来一次。
var (
	geoOnceMu sync.Mutex
	geoDone   bool
	geoCount  string
	geoRegion string
)

func geoOnce() (string, string) {
	geoOnceMu.Lock()
	defer geoOnceMu.Unlock()
	if !geoDone {
		geoCount, geoRegion = detectGeo()
		// 探失败也标记 done —— 否则每条上报都会去试一次，
		// 没网的时候会一直卡在超时上。想重试就重启 agent。
		geoDone = true
	}
	return geoCount, geoRegion
}
