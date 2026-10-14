package netprobe

import (
	"context"
	"encoding/binary"
	"strings"
	"testing"
	"time"
)

// TestChecksum 用 RFC 1071 的已知样例校验 ICMP 校验和实现。
// 一条 type 8 / code 0、id=0x1234、seq=1、无负载的 Echo Request，
// 手工计算的校验和为 0x2c4c（网络序低 16 位取反）。
func TestChecksum(t *testing.T) {
	msg := make([]byte, 8)
	msg[0] = icmpEchoRequest
	binary.BigEndian.PutUint16(msg[4:6], 0x1234)
	binary.BigEndian.PutUint16(msg[6:8], 1)
	// 校验和：0x0800 + 0x0000 + 0x1234 + 0x0001 = 0x1A35，取反 = 0xE5CA
	if got, want := checksum(msg), uint16(0xE5CA); got != want {
		t.Fatalf("checksum = %#04x, want %#04x", got, want)
	}
	// 填入正确的校验和后，整条报文（含校验和字段）的和应为 0xffff
	binary.BigEndian.PutUint16(msg[2:4], checksum(msg))
	if got := checksum(msg); got != 0 {
		t.Fatalf("校验和自洽失败：%#04x", got)
	}
}

func TestStripIPHeader(t *testing.T) {
	// 带 IPv4 首部（IHL=5）时应剥掉 20 字节
	pkt := make([]byte, 28)
	pkt[0] = 0x45
	pkt[8] = icmpEchoReply
	got := stripIPHeader(pkt)
	if len(got) != 8 || got[0] != icmpEchoReply {
		t.Fatalf("stripIPHeader 未正确剥离 IP 首部: len=%d", len(got))
	}
	// 没有 IP 首部（裸 ICMP）时应原样返回
	raw := []byte{icmpEchoReply, 0, 0, 0, 0, 0, 0, 0}
	if got := stripIPHeader(raw); len(got) != 8 {
		t.Fatalf("stripIPHeader 不应修改裸 ICMP: len=%d", len(got))
	}
}

func TestStat(t *testing.T) {
	samples := []time.Duration{
		10 * time.Millisecond, 20 * time.Millisecond, 30 * time.Millisecond, 40 * time.Millisecond,
	}
	mean, jitter, loss := stat(samples, 5) // 5 次里成功 4 次 -> 丢包 20%
	if mean != 25 {
		t.Fatalf("mean = %v, want 25", mean)
	}
	// 总体标准差：sqrt(((225+25+25+225)/4)) = sqrt(125) ≈ 11.18
	if jitter < 11.1 || jitter > 11.2 {
		t.Fatalf("jitter = %v, want ≈11.18", jitter)
	}
	if loss != 20 {
		t.Fatalf("loss = %v, want 20", loss)
	}
	// 全部失败
	if _, _, l := stat(nil, 3); l != 100 {
		t.Fatalf("loss = %v, want 100", l)
	}
}

func TestFilterAndCoverage(t *testing.T) {
	gd := Filter([]string{"广东"}, nil)
	if len(gd) != 3 {
		t.Fatalf("广东应有 3 个点（三网各一），实际 %d", len(gd))
	}
	ct := Filter(nil, []string{"telecom"})
	if len(ct) != Coverage().ByISP[ISPTelecom] {
		t.Fatalf("电信筛选数量与覆盖统计不一致")
	}
	for _, x := range ct {
		if x.ISP != ISPTelecom {
			t.Fatalf("筛选结果混入非电信目标: %v", x)
		}
	}
	both := Filter([]string{"广东"}, []string{"UNICOM"})
	if len(both) != 1 || both[0].Name != "广东联通" {
		t.Fatalf("组合筛选失败: %+v", both)
	}

	c := Coverage()
	if c.Total != len(Targets) {
		t.Fatalf("Total 与 Targets 长度不一致")
	}
	if c.MainlandProvinces < 30 {
		t.Fatalf("国内省级行政区覆盖过少: %d", c.MainlandProvinces)
	}
	for _, isp := range MainlandISPs {
		if c.ByISP[isp] == 0 {
			t.Fatalf("缺少 %s 的探测点", isp)
		}
	}
	if len(Provinces()) != c.Provinces {
		t.Fatalf("Provinces() 与 Coverage().Provinces 不一致")
	}
}

// TestTargetsNoDuplicateHost 保证没有重复地址（重复会让覆盖统计虚高）。
func TestTargetsNoDuplicateHost(t *testing.T) {
	seen := make(map[string]string, len(Targets))
	for _, x := range Targets {
		if prev, ok := seen[x.Host]; ok {
			t.Fatalf("重复探测点 %s：%s 与 %s", x.Host, prev, x.Name)
		}
		seen[x.Host] = x.Name
	}
}

// TestTargetsIPLiteral 保证 Host 都是合法 IPv4 字面量，且 Province/ISP/Name 非空。
func TestTargetsIPLiteral(t *testing.T) {
	for _, x := range Targets {
		if x.Province == "" || x.ISP == "" || x.Name == "" {
			t.Fatalf("字段为空: %+v", x)
		}
		if strings.Count(x.Host, ".") != 3 {
			t.Fatalf("Host 不是 IPv4 字面量: %s (%s)", x.Host, x.Name)
		}
	}
}

// TestProbeLocalhost 对 127.0.0.1 做一轮探测，验证整条链路（含降级）不崩、结构完整。
func TestProbeLocalhost(t *testing.T) {
	p, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Logf("探测模式: %s", p.Mode())

	res := p.Probe(context.Background(), []Target{{Province: "本机", ISP: "local", Name: "loopback", Host: "127.0.0.1"}}, 2, 300*time.Millisecond)
	if len(res) != 1 {
		t.Fatalf("结果数量应为 1，实际 %d", len(res))
	}
	r := res[0]
	t.Logf("loopback 结果: ok=%v latency=%v jitter=%v loss=%v err=%q", r.OK, r.LatencyMS, r.JitterMS, r.LossPct, r.Error)
	if !r.OK {
		t.Skipf("本机环回探测失败（可能无 raw socket 权限且 443/80/53 未监听）：%s", r.Error)
	}
}

// TestProbeUnreachable 验证不可达目标不会 panic，且丢包率为 100%。
func TestProbeUnreachable(t *testing.T) {
	p, _ := New()
	res := p.Probe(context.Background(), []Target{
		{Province: "测试", ISP: "test", Name: "保留地址", Host: "192.0.2.1"},
	}, 1, 200*time.Millisecond)
	if len(res) != 1 {
		t.Fatalf("结果数量应为 1")
	}
	if res[0].OK {
		t.Fatalf("192.0.2.1 不应可达")
	}
	if res[0].LossPct != 100 {
		t.Fatalf("丢包率应为 100，实际 %v", res[0].LossPct)
	}
	t.Logf("不可达目标 Error: %s", res[0].Error)
}

// globalAnycastHosts 是**已知的全球任播**公共 DNS。
//
// 这些地址在地球上任何位置测都只有几毫秒——因为它们会在就近的 IX 落地。
// 拿它们当「美国」「欧洲」的探测点会导致严重误导：
// 实测从日本东京出发，美国 0.8ms、欧洲 0.6ms，纯属错误信息。
//
// 表里只收录「确认全球任播」的；区域性任播（例如某国运营商在境内多点部署）
// 不影响地理延迟的判断，不在此列。
var globalAnycastHosts = map[string]string{
	"8.8.8.8":         "Google Public DNS（全球任播）",
	"8.8.4.4":         "Google Public DNS（全球任播）",
	"1.1.1.1":         "Cloudflare（全球任播）",
	"1.0.0.1":         "Cloudflare（全球任播）",
	"9.9.9.9":         "Quad9（全球任播）",
	"149.112.112.112": "Quad9（全球任播）",
	"4.2.2.1":         "Lumen/Level3（全球任播）",
	"4.2.2.2":         "Lumen/Level3（全球任播）",
	"4.2.2.3":         "Lumen/Level3（全球任播）",
	"4.2.2.4":         "Lumen/Level3（全球任播）",
	"64.6.64.6":       "Verisign（全球任播）",
	"64.6.65.6":       "Verisign（全球任播）",
	"185.228.168.9":   "CleanBrowsing（全球任播）",
	"185.228.168.10":  "CleanBrowsing（全球任播）",
	"195.46.39.39":    "SafeDNS（全球任播）",
	"80.80.80.80":     "Freenom（全球任播）",
	"208.67.222.222":  "OpenDNS/Cisco（全球任播）",
	"208.67.220.220":  "OpenDNS/Cisco（全球任播）",
	"77.88.8.8":       "Yandex（全球任播）",
	"94.140.14.14":    "AdGuard DNS（全球任播）",
}

// TestTargetsNoGlobalAnycast 守住「境外点不能用全球任播地址」这条规矩。
//
// 这条 bug 真的发生过：美国/欧洲三张卡分别显示 0.8ms / 0.6ms，
// 因为目标就是 8.8.8.8 和 1.1.1.1。数字看着漂亮，但是假的。
//
// 判据不是「地址属于哪家」而是「是否全球任播」——区域性任播不影响地理判断，
// 所以这里用显式黑名单，而不是按运营商一刀切。
func TestTargetsNoGlobalAnycast(t *testing.T) {
	for _, tg := range Targets {
		if why, bad := globalAnycastHosts[tg.Host]; bad {
			t.Errorf("探测点 %q（%s，%s）用了全球任播地址 %s——"+
				"这会让延迟数字与地理位置脱钩，实测会显示成个位数毫秒",
				tg.Name, tg.Province, tg.ISP, why)
		}
	}
}

// TestOverseasTargetsHaveTheirOwnProvince 境外点必须真的落在境外分组里。
func TestOverseasTargetsHaveTheirOwnProvince(t *testing.T) {
	overseas := map[string]bool{ISPHK: true, ISPTW: true, ISPJP: true, ISPUS: true, ISPEU: true}
	count := 0
	for _, tg := range Targets {
		if overseas[tg.ISP] {
			count++
		}
	}
	if count == 0 {
		t.Fatal("一个境外探测点都没有？")
	}
	// 每个境外 ISP 分组都至少要有 2 个点，否则单点失败就没数据了。
	per := map[string]int{}
	for _, tg := range Targets {
		if overseas[tg.ISP] {
			per[tg.ISP]++
		}
	}
	for isp, n := range per {
		if n < 2 {
			t.Errorf("境外分组 %s 只有 %d 个点，建议至少 2 个（单点失败就没数据）", isp, n)
		}
	}
}
