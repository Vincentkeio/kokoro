package netprobe

// 本文件是 Kokoro 三网探测点数据表。
//
// 数据来源（2026-10-05 联网调研，两份独立资料交叉核对）：
//   1. ip.cn《全国 DNS 服务器 IP 地址》 https://ip.cn/dns.html —— 主表，覆盖最全；
//   2. i5p.com《各省 DNS 服务器地址大全》 https://www.i5p.com/zh/dns —— 交叉核对；
//   3. 青海、宁夏两省用 51dns（https://www.51dns.com/dns/public/qinghai.html）
//      与运维兔（https://www.yunweitu.cn/jishu/373.html）的省级 DNS 表补齐。
//
// 地址类型说明（每行末尾注释）：
//   - 「省级 DNS」：该省该运营商面向宽带用户下发的递归 DNS，是三网测速最常引用的地址类型；
//   - 「公共 DNS」：面向全网开放的公共递归 DNS（任播），不代表单一机房位置；
//   - 「境外 DNS」：港澳台/海外运营商或机构提供的 DNS。
//
// 覆盖情况：
//   - 已覆盖 31 个省级行政区（22 省 + 5 自治区 + 4 直辖市）；
//   - 缺失：澳门（三网均查不到可信地址，留空待补）；
//   - 缺失：西藏电信（多份资料均无明确依据，不编造，留空待补）；
//   - 境外/其他 11 个点：中国香港、中国台湾、日本、美国、欧洲。
//
// 重要局限（必须知道）：
//   - 运营商 DNS 普遍只对本网用户开放递归，且相当一部分 ACL 会丢弃跨网/境外的 ICMP，
//     因此「探测超时」不等于「线路不通」，只说明该点不响应本机的 ICMP。
//   - TCP 降级探测（443/80/53）对 DNS 服务器基本无效，因为 DNS 主机通常不开放这些端口。
//     也就是说：**本表最适合的用法是 ICMP 模式**，TCP 模式下绝大多数点会失败。
//   - 地址会随运营商网络调整而变更，建议定期复核。

import "strings"

// ISP 取值常量。三网用 telecom / unicom / mobile，境外与其他用地区代码。
const (
	ISPTelecom = "telecom" // 中国电信
	ISPUnicom  = "unicom"  // 中国联通
	ISPMobile  = "mobile"  // 中国移动
	ISPHK      = "hk"      // 中国香港
	ISPTW      = "tw"      // 中国台湾
	ISPJP      = "jp"      // 日本
	ISPUS      = "us"      // 美国
	ISPEU      = "eu"      // 欧洲
)

// MainlandISPs 是三网 ISP 代码，便于前端按「三网」筛选。
var MainlandISPs = []string{ISPTelecom, ISPUnicom, ISPMobile}

// Target 一个探测点。
type Target struct {
	Province string `json:"province"` // 省份，如 "广东"
	ISP      string `json:"isp"`      // telecom | unicom | mobile | hk | tw | jp | us | eu
	Name     string `json:"name"`     // 展示名，如 "广东电信"
	Host     string `json:"host"`     // 域名或 IP
}

// Targets 内置探测点全集。
//
// 按省级行政区分组，组内顺序为 电信 / 联通 / 移动。
var Targets = []Target{
	// ---- 直辖市 ----
	{"北京", ISPTelecom, "北京电信", "219.141.136.10"}, // 省级 DNS
	{"北京", ISPUnicom, "北京联通", "123.123.123.123"}, // 省级 DNS
	{"北京", ISPMobile, "北京移动", "221.130.33.52"},   // 省级 DNS
	{"天津", ISPTelecom, "天津电信", "219.150.32.132"}, // 省级 DNS
	{"天津", ISPUnicom, "天津联通", "202.99.104.68"},   // 省级 DNS
	{"天津", ISPMobile, "天津移动", "211.137.160.50"},  // 省级 DNS
	{"上海", ISPTelecom, "上海电信", "202.96.209.133"}, // 省级 DNS
	{"上海", ISPUnicom, "上海联通", "210.22.70.3"},     // 省级 DNS
	{"上海", ISPMobile, "上海移动", "211.136.112.50"},  // 省级 DNS
	{"重庆", ISPTelecom, "重庆电信", "61.128.192.68"},  // 省级 DNS
	{"重庆", ISPUnicom, "重庆联通", "221.5.203.98"},    // 省级 DNS
	{"重庆", ISPMobile, "重庆移动", "218.201.4.3"},     // 省级 DNS

	// ---- 华北 ----
	{"河北", ISPTelecom, "河北电信", "222.222.202.202"},  // 省级 DNS
	{"河北", ISPUnicom, "河北联通", "202.99.160.68"},     // 省级 DNS
	{"河北", ISPMobile, "河北移动", "211.138.13.66"},     // 省级 DNS
	{"山西", ISPTelecom, "山西电信", "59.49.49.49"},      // 省级 DNS
	{"山西", ISPUnicom, "山西联通", "202.99.192.66"},     // 省级 DNS
	{"山西", ISPMobile, "山西移动", "211.138.106.2"},     // 省级 DNS
	{"内蒙古", ISPTelecom, "内蒙古电信", "219.148.162.31"}, // 省级 DNS
	{"内蒙古", ISPUnicom, "内蒙古联通", "202.99.224.68"},   // 省级 DNS
	{"内蒙古", ISPMobile, "内蒙古移动", "211.138.91.1"},    // 省级 DNS

	// ---- 东北 ----
	{"辽宁", ISPTelecom, "辽宁电信", "219.148.204.66"},    // 省级 DNS
	{"辽宁", ISPUnicom, "辽宁联通", "202.96.69.38"},       // 省级 DNS
	{"辽宁", ISPMobile, "辽宁移动", "211.137.32.178"},     // 省级 DNS
	{"吉林", ISPTelecom, "吉林电信", "219.149.194.55"},    // 省级 DNS
	{"吉林", ISPUnicom, "吉林联通", "202.98.0.68"},        // 省级 DNS
	{"吉林", ISPMobile, "吉林移动", "211.141.16.99"},      // 省级 DNS
	{"黑龙江", ISPTelecom, "黑龙江电信", "219.147.198.230"}, // 省级 DNS
	{"黑龙江", ISPUnicom, "黑龙江联通", "202.97.224.69"},    // 省级 DNS
	{"黑龙江", ISPMobile, "黑龙江移动", "211.137.241.34"},   // 省级 DNS

	// ---- 华东 ----
	{"江苏", ISPTelecom, "江苏电信", "218.2.2.2"},      // 省级 DNS
	{"江苏", ISPUnicom, "江苏联通", "221.6.4.66"},      // 省级 DNS
	{"江苏", ISPMobile, "江苏移动", "221.131.143.69"},  // 省级 DNS
	{"浙江", ISPTelecom, "浙江电信", "202.101.172.35"}, // 省级 DNS
	{"浙江", ISPUnicom, "浙江联通", "221.12.1.227"},    // 省级 DNS
	{"浙江", ISPMobile, "浙江移动", "211.140.13.188"},  // 省级 DNS
	{"安徽", ISPTelecom, "安徽电信", "61.132.163.68"},  // 省级 DNS
	{"安徽", ISPUnicom, "安徽联通", "218.104.78.2"},    // 省级 DNS
	{"安徽", ISPMobile, "安徽移动", "211.138.180.2"},   // 省级 DNS
	{"福建", ISPTelecom, "福建电信", "218.85.152.99"},  // 省级 DNS
	{"福建", ISPUnicom, "福建联通", "218.104.128.106"}, // 省级 DNS
	{"福建", ISPMobile, "福建移动", "211.138.151.161"}, // 省级 DNS
	{"江西", ISPTelecom, "江西电信", "202.101.224.69"}, // 省级 DNS
	{"江西", ISPUnicom, "江西联通", "220.248.192.12"},  // 省级 DNS
	{"江西", ISPMobile, "江西移动", "211.141.90.68"},   // 省级 DNS
	{"山东", ISPTelecom, "山东电信", "219.146.1.66"},   // 省级 DNS
	{"山东", ISPUnicom, "山东联通", "202.102.128.68"},  // 省级 DNS
	{"山东", ISPMobile, "山东移动", "218.201.96.130"},  // 省级 DNS

	// ---- 华中 ----
	{"河南", ISPTelecom, "河南电信", "222.88.88.88"},   // 省级 DNS
	{"河南", ISPUnicom, "河南联通", "202.102.224.68"},  // 省级 DNS
	{"河南", ISPMobile, "河南移动", "211.138.24.66"},   // 省级 DNS
	{"湖北", ISPTelecom, "湖北电信", "202.103.24.68"},  // 省级 DNS
	{"湖北", ISPUnicom, "湖北联通", "218.104.111.122"}, // 省级 DNS
	{"湖北", ISPMobile, "湖北移动", "211.137.58.20"},   // 省级 DNS
	{"湖南", ISPTelecom, "湖南电信", "222.246.129.80"}, // 省级 DNS
	{"湖南", ISPUnicom, "湖南联通", "58.20.127.238"},   // 省级 DNS
	{"湖南", ISPMobile, "湖南移动", "211.142.210.98"},  // 省级 DNS

	// ---- 华南 ----
	{"广东", ISPTelecom, "广东电信", "202.96.128.86"},  // 省级 DNS
	{"广东", ISPUnicom, "广东联通", "210.21.196.6"},    // 省级 DNS
	{"广东", ISPMobile, "广东移动", "211.136.192.6"},   // 省级 DNS
	{"广西", ISPTelecom, "广西电信", "202.103.225.68"}, // 省级 DNS
	{"广西", ISPUnicom, "广西联通", "221.7.128.68"},    // 省级 DNS
	{"广西", ISPMobile, "广西移动", "211.138.245.180"}, // 省级 DNS
	{"海南", ISPTelecom, "海南电信", "202.100.192.68"}, // 省级 DNS
	{"海南", ISPUnicom, "海南联通", "221.11.132.2"},    // 省级 DNS
	{"海南", ISPMobile, "海南移动", "221.176.88.95"},   // 省级 DNS

	// ---- 西南 ----
	{"四川", ISPTelecom, "四川电信", "61.139.2.69"},    // 省级 DNS
	{"四川", ISPUnicom, "四川联通", "119.6.6.6"},       // 省级 DNS
	{"四川", ISPMobile, "四川移动", "211.137.82.4"},    // 省级 DNS
	{"贵州", ISPTelecom, "贵州电信", "202.98.192.67"},  // 省级 DNS
	{"贵州", ISPUnicom, "贵州联通", "221.13.30.242"},   // 省级 DNS
	{"贵州", ISPMobile, "贵州移动", "211.139.5.29"},    // 省级 DNS
	{"云南", ISPTelecom, "云南电信", "222.172.200.68"}, // 省级 DNS
	{"云南", ISPUnicom, "云南联通", "221.3.131.11"},    // 省级 DNS
	{"云南", ISPMobile, "云南移动", "211.139.29.68"},   // 省级 DNS
	// 西藏电信：多份资料均无明确依据，留空待补（不编造）。
	{"西藏", ISPUnicom, "西藏联通", "221.13.65.34"},  // 省级 DNS
	{"西藏", ISPMobile, "西藏移动", "211.139.73.34"}, // 省级 DNS

	// ---- 西北 ----
	{"陕西", ISPTelecom, "陕西电信", "218.30.19.40"},   // 省级 DNS
	{"陕西", ISPUnicom, "陕西联通", "221.11.1.67"},     // 省级 DNS
	{"陕西", ISPMobile, "陕西移动", "211.137.130.3"},   // 省级 DNS
	{"甘肃", ISPTelecom, "甘肃电信", "202.100.64.68"},  // 省级 DNS
	{"甘肃", ISPUnicom, "甘肃联通", "221.7.34.11"},     // 省级 DNS
	{"甘肃", ISPMobile, "甘肃移动", "218.203.160.194"}, // 省级 DNS
	{"青海", ISPTelecom, "青海电信", "202.100.128.68"}, // 省级 DNS（51dns）
	{"青海", ISPUnicom, "青海联通", "221.207.58.58"},   // 省级 DNS（51dns）
	{"青海", ISPMobile, "青海移动", "211.138.75.123"},  // 省级 DNS（ip.cn + 51dns 交叉核对）
	{"宁夏", ISPTelecom, "宁夏电信", "202.100.96.68"},  // 省级 DNS（运维兔）
	{"宁夏", ISPUnicom, "宁夏联通", "221.199.12.157"},  // 省级 DNS（ip.cn）
	{"宁夏", ISPMobile, "宁夏移动", "218.203.123.116"}, // 省级 DNS（ip.cn + 运维兔 交叉核对）
	{"新疆", ISPTelecom, "新疆电信", "61.128.114.167"}, // 省级 DNS
	{"新疆", ISPUnicom, "新疆联通", "221.7.1.20"},      // 省级 DNS
	{"新疆", ISPMobile, "新疆移动", "218.202.152.130"}, // 省级 DNS

	// ---- 境外 / 其他 ----
	//
	// ⚠️ 境外点**必须用非全球任播的主机**，否则测出来的数字毫无地理意义。
	//
	// 踩过的坑：早先用 8.8.8.8 / 1.1.1.1 / 9.9.9.9 当「美国」「欧洲」，
	// 从日本小鸡上测出**美国 0.8ms、欧洲 0.6ms**——因为它连到的是这两家
	// 在东京的任播节点，跟美国、欧洲都无关。同一批里 4.2.2.1、64.6.64.6、
	// 195.46.39.39、185.228.168.9 也全是任播（实测均 <1ms），一并换掉。
	//
	// 选点与验证方法（2026-10-07，从日本东京 AS205548 zouter 实测 ICMP 平均 RTT）：
	// 判据是**延迟是否落在该地理位置的物理合理区间**。
	// 从东京出发，真·美国应为 120~200ms、真·欧洲应为 200~240ms；
	// 凡 <5ms 的一律判为全球任播，剔除（无论它标称在哪）。
	//
	// 香港/台湾/日本这几组经同样方法复核**不是任播**，予以保留：
	// 香港 HKBN 46.9ms、香港通用 81.4ms、台湾 HiNet 32.1ms、
	// 台湾 TWNIC 30.9ms、日本东京 2.9ms、日本神奈川 4.0ms。
	{"中国香港", ISPHK, "中国香港 HKBN", "203.80.96.10"},     // 境外 DNS（香港宽频）；实测 46.9ms
	{"中国香港", ISPHK, "中国香港通用", "103.147.12.3"},        // 境外 DNS（ip.cn 香港）；实测 81.4ms
	{"中国台湾", ISPTW, "中国台湾 HiNet", "168.95.192.1"},    // 境外 DNS（中华电信）；实测 32.1ms
	{"中国台湾", ISPTW, "中国台湾 TWNIC", "101.101.101.101"}, // 境外 DNS（TWNIC Quad 101）；实测 30.9ms
	{"日本", ISPJP, "日本 东京", "211.10.168.1"},           // 境外 DNS（ip.cn 东京）；实测 2.9ms
	{"日本", ISPJP, "日本 神奈川", "122.210.62.171"},        // 境外 DNS（ip.cn 神奈川）；实测 4.0ms
	// 美国：换成单点落地的运营商/机构主机，实测均为真·跨太平洋延迟。
	{"美国", ISPUS, "美国 Comcast", "75.75.75.75"}, // Comcast 递归 DNS（美国本土）；实测 158ms
	{"美国", ISPUS, "美国 Cox", "68.105.28.11"},    // Cox 递归 DNS（美国本土）；实测 140ms
	{"美国", ISPUS, "美国 NIST", "132.163.96.5"},   // NIST 时间服务器（马里兰州，单点）；实测 192ms
	// 欧洲：同上，换成落地在德国/瑞士的主机。
	{"欧洲", ISPEU, "欧洲 DNS.WATCH", "84.200.69.80"}, // DNS.WATCH（德国）；实测 217ms
	{"欧洲", ISPEU, "欧洲 SWITCH", "130.59.31.248"},   // SWITCH 基金会（瑞士苏黎世）；实测 224ms
}

// Filter 按省份与 ISP 筛选探测点。
// 两个条件取交集；任一为 nil 或空表示不限制该项。省份精确匹配，ISP 忽略大小写。
func Filter(provinces []string, isps []string) []Target {
	pSet := make(map[string]struct{}, len(provinces))
	for _, p := range provinces {
		if p != "" {
			pSet[p] = struct{}{}
		}
	}
	iSet := make(map[string]struct{}, len(isps))
	for _, i := range isps {
		if i != "" {
			iSet[strings.ToLower(i)] = struct{}{}
		}
	}
	out := make([]Target, 0, len(Targets))
	for _, t := range Targets {
		if len(pSet) > 0 {
			if _, ok := pSet[t.Province]; !ok {
				continue
			}
		}
		if len(iSet) > 0 {
			if _, ok := iSet[strings.ToLower(t.ISP)]; !ok {
				continue
			}
		}
		out = append(out, t)
	}
	return out
}

// CoverageStat 描述内置探测点的覆盖情况，供后台展示。
type CoverageStat struct {
	Total             int            `json:"total"`              // 探测点总数
	Provinces         int            `json:"provinces"`          // 省级行政区（含境外地区）数
	MainlandProvinces int            `json:"mainland_provinces"` // 国内省级行政区数
	ByISP             map[string]int `json:"by_isp"`             // 按 ISP 分组的数量
	ByProvince        map[string]int `json:"by_province"`        // 按省级行政区分组的数量
}

// Coverage 返回内置探测点的覆盖统计。
func Coverage() CoverageStat {
	byISP := make(map[string]int)
	byProv := make(map[string]int)
	mainland := make(map[string]struct{})
	for _, t := range Targets {
		byISP[t.ISP]++
		byProv[t.Province]++
		switch t.ISP {
		case ISPTelecom, ISPUnicom, ISPMobile:
			mainland[t.Province] = struct{}{}
		}
	}
	return CoverageStat{
		Total:             len(Targets),
		Provinces:         len(byProv),
		MainlandProvinces: len(mainland),
		ByISP:             byISP,
		ByProvince:        byProv,
	}
}

// Provinces 返回所有出现过的省级行政区名（去重，顺序与 Targets 首次出现顺序一致）。
func Provinces() []string {
	seen := make(map[string]struct{}, 32)
	out := make([]string, 0, 32)
	for _, t := range Targets {
		if _, ok := seen[t.Province]; ok {
			continue
		}
		seen[t.Province] = struct{}{}
		out = append(out, t.Province)
	}
	return out
}
