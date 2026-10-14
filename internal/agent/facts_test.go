package agent

// 静态信息（BBR / 虚拟化 / CPU 型号）必须跟着机器上的实际改动走。
//
// 踩过的坑：这些字段原来只在接入时发一次，Hub 回了 facts_ok 就再也不发。
// 结果在机器上开了 BBR，卡片上还一直显示 cubic —— 而且不会报错，
// 看起来就像"这台机器没开 BBR"。

import (
	"testing"

	"github.com/Vincentkeio/kokoro/internal/model"
)

func TestSameFactsDetectsChange(t *testing.T) {
	base := &model.HostFacts{
		Virt: "kvm", CPUModel: "AMD EPYC", CPUCores: 1,
		TCPCC: "cubic", TCPQdisc: "fq_codel",
		LocalIPs: []string{"10.0.0.1", "172.16.0.2"},
	}

	// 内容一样就是一样
	same := &model.HostFacts{
		Virt: "kvm", CPUModel: "AMD EPYC", CPUCores: 1,
		TCPCC: "cubic", TCPQdisc: "fq_codel",
		LocalIPs: []string{"10.0.0.1", "172.16.0.2"},
	}
	if !sameFacts(base, same) {
		t.Error("内容相同的两份应该判为等价")
	}

	// 本地 IP 顺序不同不算变（map 迭代顺序本来就不保证）
	reordered := *same
	reordered.LocalIPs = []string{"172.16.0.2", "10.0.0.1"}
	if !sameFacts(base, &reordered) {
		t.Error("LocalIPs 只是顺序不同，不该算变")
	}

	// 每改一项都要能被发现 —— 尤其是 tcp_cc，这是我们踩的那个坑
	for name, mutate := range map[string]func(*model.HostFacts){
		"开 BBR":  func(f *model.HostFacts) { f.TCPCC = "bbr" },
		"换队列规则":  func(f *model.HostFacts) { f.TCPQdisc = "fq" },
		"换内核":    func(f *model.HostFacts) { f.Virt = "lxc" },
		"换 CPU":  func(f *model.HostFacts) { f.CPUModel = "Intel Xeon" },
		"加核":     func(f *model.HostFacts) { f.CPUCores = 2 },
		"网卡地址变了": func(f *model.HostFacts) { f.LocalIPs = []string{"10.0.0.9"} },
	} {
		changed := *base
		changed.LocalIPs = append([]string(nil), base.LocalIPs...)
		mutate(&changed)
		if sameFacts(base, &changed) {
			t.Errorf("%s 之后应判为「变了」，否则卡片上永远是旧值", name)
		}
	}

	// nil 安全：首轮上报时 lastFacts 是 nil
	if sameFacts(nil, base) || sameFacts(base, nil) {
		t.Error("任一侧为 nil 时应判为不等（首轮必须发出去）")
	}
}

// TestFactsRecheckIntervalSane 重查间隔不能太短也不能太长。
func TestFactsRecheckIntervalSane(t *testing.T) {
	// 上报默认 2 秒一条：小于 30 次（1 分钟）太频繁，读 /proc 是白花；
	// 大于 900 次（30 分钟）又太慢，改了 BBR 半小时后才显示。
	if factsRecheckEvery < 30 {
		t.Errorf("重查太频繁：每 %d 条一次", factsRecheckEvery)
	}
	if factsRecheckEvery > 900 {
		t.Errorf("重查太慢：每 %d 条一次", factsRecheckEvery)
	}
}
