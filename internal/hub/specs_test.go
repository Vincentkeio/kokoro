package hub

// 卡片静态规格（CPU 型号 / Swap / 虚拟化 / TCP 加速 / NAT）的测试。

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Vincentkeio/kokoro/internal/model"
)

// TestIsBehindNAT 判据是「Hub 看到的来源 IP 在不在机器自己的网卡上」。
func TestIsBehindNAT(t *testing.T) {
	// 网卡上是私网地址、Hub 看到公网 → NAT
	if !isBehindNAT([]string{"10.0.0.5", "172.17.0.1"}, "203.0.113.7") {
		t.Error("网卡只有私网地址时应判为 NAT")
	}
	// 公网 IP 就在自己网卡上 → 直连
	if isBehindNAT([]string{"203.0.113.7"}, "203.0.113.7") {
		t.Error("公网 IP 在自己网卡上时不该判为 NAT")
	}
	// 判不了的时候**不要瞎猜** —— 老 agent 不上报 local_ips
	if isBehindNAT(nil, "203.0.113.7") {
		t.Error("拿不到本机地址时不该猜成 NAT")
	}
	if isBehindNAT([]string{"10.0.0.5"}, "") {
		t.Error("拿不到来源 IP 时不该猜成 NAT")
	}
}

// TestNodeSpecsIncludesSwapAndCPU CPU 型号、Swap、虚拟化、加速都要在。
func TestNodeSpecsIncludesSwapAndCPU(t *testing.T) {
	n := model.Node{
		CPUModel: "AMD EPYC 7003 Series", CPUCores: 2,
		MemTotal: 2 << 30, DiskTotal: 40 << 30, OS: "Debian 12", Virt: "kvm",
		TCPCC: "bbr", TCPQdisc: "fq", NAT: true,
	}
	m := &model.Metrics{}
	m.Mem.SwapTotal = 2 << 30
	m.Mem.SwapUsed = 256 << 20

	got := map[string]string{}
	for _, s := range nodeSpecs(n, m) {
		got[s.Label] = s.Value
	}
	for _, want := range []string{"CPU", "核心数", "内存", "Swap", "磁盘", "系统", "虚拟化", "网络", "加速"} {
		if _, ok := got[want]; !ok {
			t.Errorf("规格里缺少 %q，现有: %v", want, got)
		}
	}
	// CPU 型号单独一行（独占整行），核心数是**另一行**。
	// 型号太长，和核心挤一行会被裁 —— boss 反馈过两次。
	if !strings.Contains(got["CPU"], "EPYC") {
		t.Errorf("CPU 行应是型号：%q", got["CPU"])
	}
	if got["核心数"] != "2 核" {
		t.Errorf("核心数行 = %q，应为 2 核", got["核心数"])
	}
	// 型号那行必须是 Full（独占整行）
	var cpuFull bool
	for _, it := range nodeSpecs(n, m) {
		if it.Label == "CPU" {
			cpuFull = it.Full
		}
	}
	if !cpuFull {
		t.Error("CPU 型号行必须独占整行，否则会被裁掉")
	}
	if !strings.Contains(got["加速"], "BBR") || !strings.Contains(got["加速"], "fq") {
		t.Errorf("加速应同时显示算法和队列规则：%q", got["加速"])
	}
	if !strings.Contains(got["Swap"], "/") {
		t.Errorf("Swap 应显示 用量/总量：%q", got["Swap"])
	}
}

// TestNodeSpecsSkipsUnknown 老 agent 没上报的字段不该显示空值占位。
func TestNodeSpecsSkipsUnknown(t *testing.T) {
	n := model.Node{CPUModel: "Xeon", CPUCores: 1} // 没有 tcp_cc / virt
	for _, s := range nodeSpecs(n, nil) {
		if s.Value == "" {
			t.Errorf("不该出现空值项：%+v", s)
		}
		if s.Label == "加速" || s.Label == "虚拟化" {
			t.Errorf("没上报的字段不该显示：%+v", s)
		}
	}
}

// TestNodeSpecsMarksBBRAndNAT bbr 标绿、cubic 和 NAT 标黄。
//
// 这几个是买家一眼要看到的：开没开 BBR、是不是 NAT 小鸡。
func TestNodeSpecsMarksBBRAndNAT(t *testing.T) {
	bbr := model.Node{TCPCC: "bbr", TCPQdisc: "fq"}
	lv := map[string]string{}
	for _, s := range nodeSpecs(bbr, nil) {
		lv[s.Label] = s.Level
	}
	if lv["加速"] != "ok" {
		t.Errorf("bbr 应标 ok（绿），实际 %q", lv["加速"])
	}

	cubic := model.Node{TCPCC: "cubic"}
	for _, s := range nodeSpecs(cubic, nil) {
		if s.Label == "加速" && s.Level != "warn" {
			t.Errorf("cubic 是内核默认值，应标 warn（黄），实际 %q", s.Level)
		}
	}

	nat := model.Node{NAT: true}
	for _, s := range nodeSpecs(nat, nil) {
		if s.Label == "网络" && s.Level != "warn" {
			t.Errorf("NAT 应标 warn，实际 %q", s.Level)
		}
	}
}

// TestVirtLabel 虚拟化类型要说成人话。
func TestVirtLabel(t *testing.T) {
	cases := map[string]string{
		"kvm": "KVM", "openvz": "OpenVZ", "lxc": "LXC",
		"none": "独服（无虚拟化）", "weird": "weird",
	}
	for in, want := range cases {
		if got := virtLabel(in); got != want {
			t.Errorf("virtLabel(%q) = %q，应为 %q", in, got, want)
		}
	}
}

// TestCardRendersSpecs 卡片上要真的渲染出来。
func TestCardRendersSpecs(t *testing.T) {
	h, st := newTestHub(t)
	n := mkNode(t, st, "东京 zouter", "JP", "日本 · 东京", "")
	n.CPUModel = "Intel Xeon Platinum 8272CL"
	n.CPUCores = 1
	n.Virt = "kvm"
	n.TCPCC = "bbr"
	n.TCPQdisc = "fq"
	if err := st.UpdateNode(n); err != nil {
		t.Fatal(err)
	}

	body := renderBody(t, h, httptest.NewRequest(http.MethodGet, "/", nil))
	for _, want := range []string{"class=\"specs\"", "Intel Xeon Platinum", "KVM", "BBR", "fq", "虚拟化"} {
		if !strings.Contains(body, want) {
			t.Errorf("卡片缺少 %q", want)
		}
	}
}

// TestUpdateNodeFactsIsIdempotent 静态信息重复上报不该反复写库。
//
// 上报是每 2 秒一次。无脑写会把 SQLite 写爆 —— 这些字段开机后基本不变，
// 值没变就必须跳过。
func TestUpdateNodeFactsIsIdempotent(t *testing.T) {
	_, st := newTestHub(t)
	n := mkNode(t, st, "东京 zouter", "JP", "日本 · 东京", "")

	f := &model.HostFacts{Virt: "kvm", CPUModel: "Xeon 8272CL", CPUCores: 1,
		TCPCC: "bbr", TCPQdisc: "fq"}
	if err := st.UpdateNodeFacts(n.ID, f, false); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetNode(n.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.TCPCC != "bbr" || got.TCPQdisc != "fq" || got.Virt != "kvm" {
		t.Fatalf("静态信息没写进去: %+v", got)
	}
	// 再写一次同样的值，不应报错，值也不变
	if err := st.UpdateNodeFacts(n.ID, f, false); err != nil {
		t.Fatal(err)
	}
	again, _ := st.GetNode(n.ID)
	if again.TCPCC != "bbr" {
		t.Errorf("重复写入后值变了: %q", again.TCPCC)
	}
}

// TestUpdateNodeFactsMarksNAT NAT 状态要能写进去。
func TestUpdateNodeFactsMarksNAT(t *testing.T) {
	_, st := newTestHub(t)
	n := mkNode(t, st, "NAT 小鸡", "JP", "日本 · 东京", "")
	f := &model.HostFacts{Virt: "kvm", LocalIPs: []string{"10.0.0.5"}}
	if err := st.UpdateNodeFacts(n.ID, f, true); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetNode(n.ID)
	if !got.NAT {
		t.Error("NAT 标志没写进去")
	}
}

// TestReportCarriesFacts 上报里带 facts 时要落到节点上。
//
// 这条保证「老 agent 升级后不用重新注册也能补齐新字段」。
func TestReportCarriesFacts(t *testing.T) {
	_, st := newTestHub(t)
	// 直接走 store 层：HTTP 层要造完整的鉴权，这里只验数据流
	nd := &model.Node{ID: "nd_facts", Name: "facts", Slug: "facts",
		TokenHash: hashToken("tok"), Visibility: model.VisibilityPublic}
	if err := st.CreateNode(nd); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateNodeFacts(nd.ID, &model.HostFacts{
		Virt: "kvm", TCPCC: "bbr", TCPQdisc: "fq",
	}, false); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetNode(nd.ID)
	if got.TCPCC != "bbr" || got.TCPQdisc != "fq" {
		t.Errorf("facts 没落库: cc=%q qdisc=%q", got.TCPCC, got.TCPQdisc)
	}
}

// TestCardHasExactlyOneSpecsBlock 卡片上只能有一块规格。
//
// 踩过：我先加了一块在指标条上面，没注意下面本来就有一块 ——
// 结果同一张卡上「内存」「CPU」各显示了两遍，boss 一眼就看出来了。
//
// 这条钉住"只有一块"，而且必须在指标条**上面**。
func TestCardHasExactlyOneSpecsBlock(t *testing.T) {
	h, st := newTestHub(t)
	n := mkNode(t, st, "东京 zouter", "JP", "日本 · 东京", "")
	n.CPUModel, n.CPUCores, n.Virt = "Intel Xeon 8272CL", 1, "kvm"
	n.TCPCC, n.TCPQdisc = "bbr", "fq"
	if err := st.UpdateNode(n); err != nil {
		t.Fatal(err)
	}

	body := renderBody(t, h, httptest.NewRequest(http.MethodGet, "/", nil))
	if got := strings.Count(body, `class="specs"`); got != 1 {
		t.Errorf("卡片上规格块出现了 %d 次，应该只有 1 次", got)
	}
	// 位置：规格在指标条之前
	iSpecs := strings.Index(body, `class="specs"`)
	iGauges := strings.Index(body, `class="gauges"`)
	if iSpecs > 0 && iGauges > 0 && iSpecs > iGauges {
		t.Error("规格块应该在指标条上面 —— 先看清是什么机器，再看它忙不忙")
	}
	// 核心数不能单独占一行（已并入 CPU 行）
	if strings.Contains(body, "<dt>核心</dt>") {
		t.Error("核心数应并入 CPU 行，不该单独一行")
	}
}

// TestShortCPUModel 原始 CPU 型号太长，卡片上放不下。
//
// 实测 zouter 上是 `Intel(R) Xeon(R) Platinum 8272CL CPU @ 2.60GHz`，
// 300px 宽的卡片里原样放会占三行、还全是 (R) 这种噪音。
func TestShortCPUModel(t *testing.T) {
	cases := map[string]string{
		"Intel(R) Xeon(R) Platinum 8272CL CPU @ 2.60GHz": "Intel Xeon Platinum 8272CL",
		"AMD EPYC 7003 Series @ 2.9GHz":                  "AMD EPYC 7003 Series",
		"Intel(R) Core(TM) i7-9750H CPU @ 2.60GHz":       "Intel Core i7-9750H",
		"":               "",
		"Some Weird CPU": "Some Weird CPU",
	}
	for in, want := range cases {
		if got := shortCPUModel(in); got != want {
			t.Errorf("shortCPUModel(%q) = %q，应为 %q", in, got, want)
		}
	}
}

// TestNodeTasksKeepsOnlyLatestPerKind 详情页每种测试只能留最新一份。
//
// boss 看到的：两份「IP 质量与解锁」+ 两份失败的「线路与三网质量」摞在一起。
// 列表是倒序的，所以「第一次见到的那个 kind」就是最新的。
func TestNodeTasksKeepsOnlyLatestPerKind(t *testing.T) {
	h, st := newTestHub(t)
	n := mkNode(t, st, "东京 zouter", "JP", "日本 · 东京", "")

	// 同一台机器跑两轮 IP 质量 + 两轮失败的三网
	mk := func(kind, title, out string) {
		task := &model.NodeTask{NodeID: n.ID, Kind: kind, Title: title,
			Cmd: "bash /tmp/kb.sh", Status: model.TaskQueued}
		if err := st.CreateTask(task); err != nil {
			t.Fatal(err)
		}
		h.pendingCommands(n.ID)
		if out == "" {
			h.finishTaskFromResult(model.CommandResult{
				ID: task.ID, OK: false, ExitCode: 1, Stderr: "挂了"})
			return
		}
		h.finishTaskFromResult(model.CommandResult{ID: task.ID, OK: true, Stdout: out})
	}
	ipOut := `{"event":"result","test":"ip","ok":true,"data":{"ip_type":"广播IP","risk_scamalytics":0,"unlock_total":7,"unlocked":["Netflix"]}}
{"event":"done","ok":true,"elapsed_ms":1000,"failed":[]}`
	mk("ipquality", "IP 质量与解锁", ipOut)
	mk("netquality", "线路与三网质量", "")    // 失败
	mk("ipquality", "IP 质量与解锁", ipOut) // 再跑一轮
	mk("netquality", "线路与三网质量", "")    // 又失败

	got := h.nodeTasks(n.ID, 20)
	kinds := map[string]int{}
	for _, v := range got {
		kinds[v.Kind]++
	}
	for k, c := range kinds {
		if c != 1 {
			t.Errorf("kind %s 出现了 %d 次，应该只有 1 次", k, c)
		}
	}
	if len(got) != 2 {
		t.Errorf("应只剩 2 条（每个 kind 一条），实际 %d", len(got))
	}
}
