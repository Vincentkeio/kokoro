package netprobe

// 本文件是 Kokoro 三网探测点数据表。
//
// 数据来源分两块：
//
//  1. **中国大陆**：mainland.go 从嵌入的 icmp_targets.json 生成，来源
//     https://github.com/spiritLHLS/icmp_targets —— 「闭源爬虫解析 CN 全国
//     三大运营商**可用 ICMP** 的 Ping 地址，每日更新」。每省每网最多 3 个
//     候选，探测时依次回退。
//
//  2. **境外**：下面 overseasTargets 手工维护（上游不覆盖港澳台与海外）。
//
// 为什么换掉早期手搓的表（2026-10-11）：原表按公开 DNS 资料拼的，大量地址
// 已经不通，boss 反馈"很多探测点不通"。新的上游只收「确实 ping 通」的地址。
//
// 重要局限（仍然成立，必须知道）：
//   - 运营商网络普遍对跨网/境外 ICMP 有 ACL，**探测超时 ≠ 线路不通**，
//     只说明该点不响应本机的 ICMP；
//   - TCP 降级探测（443/80/53）对这些地址大多无效（它们通常不开这些端口），
//     所以**本表最适合 ICMP 模式**；
//   - 地址会随运营商调整而变 —— 所以同步脚本要定期跑（表是每日更新的）。

import (
	"sort"
	"strings"
)

// provinceDisplayOrder follows the province sequence used by official
// government/statistics portals (GB/T 2260 region-code order).
var provinceDisplayOrder = []string{
	"北京", "天津", "河北", "山西", "内蒙古",
	"辽宁", "吉林", "黑龙江",
	"上海", "江苏", "浙江", "安徽", "福建", "江西", "山东",
	"河南", "湖北", "湖南", "广东", "广西", "海南",
	"重庆", "四川", "贵州", "云南", "西藏",
	"陕西", "甘肃", "青海", "宁夏", "新疆",
}

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
	Host     string `json:"host"`     // 首选地址（== Hosts[0]）
	// Hosts 是候选地址：大陆点来自每日更新的「可用 ICMP」表，每省每网最多 3 个。
	// 探测时依次回退，第一个通的就是结果 —— 单点失效不再整格红。
	// 境外点只有一个地址，Hosts 与 Host 相同。
	Hosts []string `json:"hosts,omitempty"`
}

// Targets 内置探测点全集。
//
// 大陆部分由 mainland.go 从嵌入的 icmp_targets.json 生成；境外部分在下面
// 手维护。组内顺序固定 电信 / 联通 / 移动。
var Targets = buildTargets()

func buildTargets() []Target {
	out := mainlandTargets()
	return append(out, overseasTargets...)
}

// overseasTargets 是境外探测点（数据源不覆盖港澳台与海外，手工维护）。
//
// ⚠️ 境外点**必须用非全球任播的主机**，否则测出来的数字毫无地理意义。
//
// 踩过的坑：早先用 8.8.8.8 / 1.1.1.1 / 9.9.9.9 当「美国」「欧洲」，
// 从日本小鸡上测出**美国 0.8ms、欧洲 0.6ms**——因为它连到的是这两家
// 在东京的任播节点，跟美国、欧洲都无关。同一批里 4.2.2.1、64.6.64.6、
// 195.46.39.39、185.228.168.9 也全是任播（实测均 <1ms），一并换掉。
//
// 选点与验证方法（从日本东京的测试节点实测 ICMP 平均 RTT）：
// 判据是**延迟是否落在该地理位置的物理合理区间**。
// 从东京出发，真·美国应为 120~200ms、真·欧洲应为 200~240ms；
// 凡 <5ms 的一律判为全球任播，剔除（无论它标称在哪）。
//
// 香港/台湾/日本这几组经同样方法复核**不是任播**，予以保留：
// 香港 HKBN 46.9ms、香港通用 81.4ms、台湾 HiNet 32.1ms、
// 台湾 TWNIC 30.9ms、日本东京 2.9ms、日本神奈川 4.0ms。
var overseasTargets = []Target{
	{"中国香港", ISPHK, "中国香港 HKBN", "203.80.96.10", nil},     // 境外 DNS（香港宽频）；实测 46.9ms
	{"中国香港", ISPHK, "中国香港通用", "103.147.12.3", nil},        // 境外 DNS（ip.cn 香港）；实测 81.4ms
	{"中国台湾", ISPTW, "中国台湾 HiNet", "168.95.192.1", nil},    // 境外 DNS（中华电信）；实测 32.1ms
	{"中国台湾", ISPTW, "中国台湾 TWNIC", "101.101.101.101", nil}, // 境外 DNS（TWNIC Quad 101）；实测 30.9ms
	{"日本", ISPJP, "日本 东京", "211.10.168.1", nil},           // 境外 DNS（ip.cn 东京）；实测 2.9ms
	{"日本", ISPJP, "日本 神奈川", "122.210.62.171", nil},        // 境外 DNS（ip.cn 神奈川）；实测 4.0ms
	// 美国：换成单点落地的运营商/机构主机，实测均为真·跨太平洋延迟。
	{"美国", ISPUS, "美国 Comcast", "75.75.75.75", nil},    // Comcast 递归 DNS（美国本土）；实测 158ms
	{"美国", ISPUS, "美国 Cox", "68.105.28.11", nil},       // Cox 递归 DNS（美国本土）；实测 140ms
	{"美国", ISPUS, "美国 NIST", "132.163.96.5", nil},      // NIST 时间服务器（马里兰州，单点）；实测 192ms
	{"欧洲", ISPEU, "欧洲 DNS.WATCH", "84.200.69.80", nil}, // DNS.WATCH（德国）；实测 217ms
	{"欧洲", ISPEU, "欧洲 SWITCH", "130.59.31.248", nil},   // SWITCH 基金会（瑞士苏黎世）；实测 224ms
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
	for _, t := range Catalog() {
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

// Provinces 返回探测表内的省级行政区名，按官方省级行政区代码顺序排列。
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
	rank := make(map[string]int, len(provinceDisplayOrder))
	for i, name := range provinceDisplayOrder {
		rank[name] = i
	}
	sort.Slice(out, func(i, j int) bool {
		ri, iok := rank[out[i]]
		rj, jok := rank[out[j]]
		if iok != jok {
			return iok
		}
		if iok {
			return ri < rj
		}
		return out[i] < out[j]
	})
	return out
}
