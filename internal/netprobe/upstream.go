package netprobe

// 上游探测点数据的解析。
//
// 上游 https://github.com/spiritLHLS/icmp_targets 的 nodes.json 是原始形态
// （省份全称 + 运营商代码 + ips 列表）；我们内部用简称 + 代码。这里做转换，
// 是「后台按钮更新探测点」与「构建时 vendor（脚本）」两条路的共同语义。

import (
	"encoding/json"
	"errors"
	"fmt"
)

// CatalogFile 是落库 / 下发用的目录文件（已归一化）。
// 后台更新按钮存它，命令通道把它推给探针，两边结构一致。
type CatalogFile struct {
	FetchedAt string   `json:"fetched_at"`
	Source    string   `json:"source"`
	Targets   []Target `json:"targets"`
}

// provinceShort 上游全称 → 站点简称（与 scripts/sync-icmp-targets.py 保持一致）。
var provinceShort = map[string]string{
	"北京市": "北京", "天津市": "天津", "上海市": "上海", "重庆市": "重庆",
	"河北省": "河北", "山西省": "山西", "辽宁省": "辽宁", "吉林省": "吉林",
	"黑龙江省": "黑龙江", "江苏省": "江苏", "浙江省": "浙江", "安徽省": "安徽",
	"福建省": "福建", "江西省": "江西", "山东省": "山东", "河南省": "河南",
	"湖北省": "湖北", "湖南省": "湖南", "广东省": "广东", "海南省": "海南",
	"四川省": "四川", "贵州省": "贵州", "云南省": "云南", "陕西省": "陕西",
	"甘肃省": "甘肃", "青海省": "青海", "内蒙古自治区": "内蒙古",
	"广西壮族自治区": "广西", "西藏自治区": "西藏",
	"宁夏回族自治区": "宁夏", "新疆维吾尔自治区": "新疆",
}

type upstreamNode struct {
	Province  string `json:"province"`
	ISPCode   string `json:"isp_code"`
	IPVersion string `json:"ip_version"`
	IPs       string `json:"ips"`
}

// ParseUpstreamNodes 解析上游原始 nodes.json → 探测点列表。
//
// 同时保留 IPv4 与 IPv6；每省每网每种协议族最多留 3 个候选，
// 顺序沿用上游给的优先序（它已按自己观测的延迟排过）。
func ParseUpstreamNodes(raw []byte) ([]Target, error) {
	var rows []upstreamNode
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, fmt.Errorf("解析上游数据失败: %w", err)
	}
	type key struct{ prov, isp, family string }
	order := []key{}
	seen := map[key]int{}
	var out []Target
	for _, r := range rows {
		family := normalizeIPVersion(r.IPVersion)
		if r.IPVersion != "" && r.IPVersion != "v4" && r.IPVersion != "v6" {
			continue
		}
		prov, ok := provinceShort[r.Province]
		if !ok {
			continue // 不认识的省名（上游加新地区）直接跳过，别猜
		}
		code, ok := map[string]string{"ct": ISPTelecom, "cu": ISPUnicom, "cm": ISPMobile}[r.ISPCode]
		if !ok {
			continue
		}
		var ips []string
		for _, s := range splitComma(r.IPs) {
			ips = append(ips, s)
			if len(ips) == 3 {
				break
			}
		}
		if len(ips) == 0 {
			continue
		}
		k := key{prov, code, family}
		if i, ok := seen[k]; ok {
			// 同一组出现多行：合并候选（去重），最多 3 个
			for _, ip := range ips {
				out[i].Hosts = appendUnique(out[i].Hosts, ip, 3)
			}
			out[i].Host = out[i].Hosts[0]
			continue
		}
		seen[k] = len(out)
		order = append(order, k)
		out = append(out, Target{
			Province: prov, ISP: code, IPVersion: family, Name: prov + ispNameZh(code),
			Host: ips[0], Hosts: ips,
		})
	}
	if len(out) == 0 {
		return nil, errors.New("上游数据里没有可用的探测点（省名/运营商代码对不上？）")
	}
	return out, nil
}

func splitComma(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		if r == ',' {
			if cur != "" {
				out = append(out, cur)
			}
			cur = ""
			continue
		}
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			continue
		}
		cur += string(r)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

func appendUnique(list []string, v string, max int) []string {
	for _, x := range list {
		if x == v {
			return list
		}
	}
	if len(list) >= max {
		return list
	}
	return append(list, v)
}

func ispNameZh(code string) string {
	switch code {
	case ISPTelecom:
		return "电信"
	case ISPUnicom:
		return "联通"
	case ISPMobile:
		return "移动"
	}
	return code
}
