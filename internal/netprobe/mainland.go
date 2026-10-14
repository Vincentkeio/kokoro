package netprobe

// 中国大陆的三网探测点来自社区维护的、**每日更新**的数据源。
//
// 来源：https://github.com/spiritLHLS/icmp_targets
//   「闭源爬虫解析 CN 全国三大运营商**可用 ICMP** 的 Ping 地址，每日更新」
//
// 为什么换掉手搓的表：原表是我们自己按公开 DNS 资料拼的（见 git 历史），
// 大量地址已经不通（2026-10-11 boss 反馈"很多探测点不通"）。这个仓库
// 专门收「确实能 ping 通」的地址，而且每省每网给 3 个候选 —— 探测时
// 依次回退，单点失效不再整格红。
//
// 同步：`python scripts/sync-icmp-targets.py`（拉最新 → 覆盖同目录的
// icmp_targets.json）。文件用 go:embed 打进二进制，所以更新表的完整动作
// 是「跑脚本 + 重新构建 + 部署」。

import (
	_ "embed"
	"encoding/json"
	"log"
	"sort"
)

//go:embed icmp_targets.json
var icmpTargetsJSON []byte

type icmpDoc struct {
	Meta struct {
		Source    string `json:"source"`
		FetchedAt string `json:"fetched_at"`
	} `json:"_meta"`
	Targets []struct {
		Province string   `json:"province"`
		ISP      string   `json:"isp"` // 电信 / 联通 / 移动
		IPs      []string `json:"ips"`
	} `json:"targets"`
}

// ispCode 把数据源里的中文运营商名映射成内部代码。
var ispCode = map[string]string{"电信": ISPTelecom, "联通": ISPUnicom, "移动": ISPMobile}

// ispRank 用于稳定排序（组内顺序固定为 电信 / 联通 / 移动）。
var ispRank = map[string]int{ISPTelecom: 0, ISPUnicom: 1, ISPMobile: 2}

// mainlandTargets 解析嵌入的「可用 ICMP」表，展开成 (省 × 网) 探测点。
//
// 解析失败**不 panic**：返回 nil（境外点照常可用），只留一条日志 ——
// 一张表解析不了不该让整个 agent 起不来；契约测试会在构建期把坏表拦下。
func mainlandTargets() []Target {
	var doc icmpDoc
	if err := json.Unmarshal(icmpTargetsJSON, &doc); err != nil {
		log.Printf("[netprobe] 解析 icmp_targets.json 失败，大陆探测点暂缺: %v", err)
		return nil
	}
	out := make([]Target, 0, len(doc.Targets))
	for _, g := range doc.Targets {
		code, ok := ispCode[g.ISP]
		if !ok || len(g.IPs) == 0 {
			continue
		}
		out = append(out, Target{
			Province: g.Province,
			ISP:      code,
			Name:     g.Province + g.ISP,
			Host:     g.IPs[0],
			Hosts:    append([]string(nil), g.IPs...),
		})
	}
	// 稳定顺序，保证每次构建、每台机器上的展示顺序一致。
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Province != out[j].Province {
			return out[i].Province < out[j].Province
		}
		return ispRank[out[i].ISP] < ispRank[out[j].ISP]
	})
	return out
}
