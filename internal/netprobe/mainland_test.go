package netprobe

// 大陆探测点表的契约测试。
//
// 这张表来自外部每日更新的数据源（spiritLHLS/icmp_targets），用
// scripts/sync-icmp-targets.py 同步 → 覆盖 icmp_targets.json → 重新构建。
// 所以这里守的是「同步之后表仍然是好的」：
//   - 能解析；
//   - 覆盖够全（31 个省级行政区 × 三网）；
//   - 每个点是合法 IPv4，且**至少有一个候选**（回退机制的前提）；
//   - 首选地址不重复（重复说明同步脚本的优选/去重坏了 —— 重复首选会让
//     两省的格子测的是同一台机器，数字互相污染）。
//
// ⚠️ 不要在这里断言"某个省具体是哪个 IP" —— 表是每日更新的，
//    写死具体地址等于给一个会变的文件加枷锁，只会天天红。

import (
	"net"
	"testing"
)

func TestMainlandTableCoversAllProvincesAndISPs(t *testing.T) {
	prov := map[string]map[string]bool{}
	for _, tg := range Targets {
		if tg.ISP != ISPTelecom && tg.ISP != ISPUnicom && tg.ISP != ISPMobile {
			continue
		}
		if prov[tg.Province] == nil {
			prov[tg.Province] = map[string]bool{}
		}
		prov[tg.Province][tg.ISP] = true
	}
	if len(prov) < 31 {
		t.Errorf("大陆省份只有 %d 个，应覆盖 31 个省级行政区（同步后表是不是缺了？）", len(prov))
	}
	for name, isps := range prov {
		for _, isp := range MainlandISPs {
			if !isps[isp] {
				t.Errorf("%s 缺少 %s 探测点", name, isp)
			}
		}
	}
}

func TestMainlandTargetsAreValidIPv4WithFallbacks(t *testing.T) {
	for _, tg := range Targets {
		if tg.ISP != ISPTelecom && tg.ISP != ISPUnicom && tg.ISP != ISPMobile {
			continue // 境外点在这里不校验（它们由人手维护，另有测试）
		}
		if len(tg.Hosts) == 0 {
			t.Errorf("%s（%s）没有候选地址 —— 回退机制的前提是至少一个", tg.Name, tg.Province)
			continue
		}
		if tg.Host != tg.Hosts[0] {
			t.Errorf("%s 的 Host(%s) 应等于 Hosts[0](%s)", tg.Name, tg.Host, tg.Hosts[0])
		}
		for _, h := range tg.Hosts {
			if ip := net.ParseIP(h); ip == nil || ip.To4() == nil {
				t.Errorf("%s 的候选地址不是合法 IPv4: %q", tg.Name, h)
			}
		}
	}
}

func TestMainlandPreferredHostsAreUnique(t *testing.T) {
	seen := map[string]string{}
	for _, tg := range Targets {
		if tg.ISP != ISPTelecom && tg.ISP != ISPUnicom && tg.ISP != ISPMobile {
			continue
		}
		if prev, ok := seen[tg.Host]; ok {
			t.Errorf("首选地址重复：%s 与 %s 都是 %s（同步脚本的优选去重坏了？）",
				tg.Name, prev, tg.Host)
		}
		seen[tg.Host] = tg.Name
	}
}

func TestMainlandEmbeddedMetaIsPresent(t *testing.T) {
	// 嵌入的 JSON 里带 _meta（来源 + 抓取时间）：同步脚本写的，缺了说明
	// 文件被手改过或同步脚本退化了。
	if len(icmpTargetsJSON) == 0 {
		t.Fatal("icmp_targets.json 没有被嵌入（go:embed 路径对不对？）")
	}
	if n := len(mainlandTargets()); n == 0 {
		t.Fatal("大陆探测点解析为空 —— JSON 结构变了？")
	}
}
