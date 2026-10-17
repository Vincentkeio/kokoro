package hub

// 三网分省探测范围（设置 + 命令下发）的回归测试。
//
// 这条链路跟任务下发共用「上报响应带命令」的通道，但触发条件是
// **范围不一致**，所以单独测：改了设置要能出命令、范围一致时
// 一条都不发（否则每 2 秒一条会刷屏）。

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Vincentkeio/kokoro/internal/netprobe"
)

func TestNetQProvincesDefault(t *testing.T) {
	h, _ := newTestHub(t)
	// 没设过 = 三网基线（上海 × 电信/联通/移动，boss 2026-10-12 定）
	got := h.netqProvinces()
	if len(got) != 1 || got[0] != "上海" {
		t.Fatalf("默认范围 = %v，应为 [上海]", got)
	}
}

func TestNetQSaveValidatesAndDedups(t *testing.T) {
	h, _ := newTestHub(t)
	saved, err := h.netqSaveProvinces([]string{"广东", "浙江", "广东", "不存在的省", ""})
	if err != nil {
		t.Fatal(err)
	}
	// 去重 + 丢掉表里没有的 + 排序
	want := "[上海 浙江 广东]"
	if got := "[" + strings.Join(saved, " ") + "]"; got != want {
		t.Errorf("保存后 = %s，应为 %s（去重、剔除未知省）", got, want)
	}
	if got := h.netqProvinces(); len(got) != 3 {
		t.Errorf("回读 = %v，应含上海及两个可选省", got)
	}
	if n := netqTargetCount(saved); n != 9 {
		t.Errorf("探测点数 = %d，应为 9（3 省 × 3 网）", n)
	}
}

func TestNetQEmptyKeepsShanghai(t *testing.T) {
	h, _ := newTestHub(t)
	saved, err := h.netqSaveProvinces(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(saved) != 1 || saved[0] != "上海" {
		t.Fatalf("清空可选省份后应保留上海，实际 %v", saved)
	}
	if got := h.netqProvinces(); len(got) != 1 || got[0] != "上海" {
		t.Errorf("空选择回读 = %v，应为上海", got)
	}
}

// TestNetQTargetsCommandOnMismatch 范围不一致才下发，一致就不发。
func TestNetQTargetsCommandOnMismatch(t *testing.T) {
	h, _ := newTestHub(t)

	// 默认范围是 [上海]：agent 报 [上海]、间隔也和 Hub 当前一致 → 不下发
	if c := h.netqTargetsCommand([]string{"上海"}, h.netqInterval()); c != nil {
		t.Errorf("范围一致时不该下发命令：%+v", c)
	}
	// agent 报空（刚重启、还是默认）→ 不一致 → 下发一条 netq_targets
	c := h.netqTargetsCommand(nil, 0)
	if c == nil {
		t.Fatal("范围不一致时应下发 netq_targets 命令")
	}
	if c.Type != "netq_targets" {
		t.Errorf("命令类型 = %s，应为 netq_targets", c.Type)
	}
	list, ok := c.Payload["provinces"].([]any)
	if !ok || len(list) != 1 || list[0] != "上海" {
		t.Errorf("命令范围 = %v，应为 [北京]", c.Payload["provinces"])
	}
}

// TestNetQCommandRidesReportResponse 保存后，下一条上报响应里要带上命令。
func TestNetQCommandRidesReportResponse(t *testing.T) {
	h, st := newTestHub(t)
	n := mkNode(t, st, "测试节点 JP", "JP", "日本 · 东京", "")
	if _, err := h.netqSaveProvinces([]string{"广东", "上海"}); err != nil {
		t.Fatal(err)
	}
	cmds := h.pendingCommands(n.ID, []string{"上海"}, "", 0) // agent 还停在默认
	var found bool
	for _, c := range cmds {
		if c.Type == "netq_targets" {
			found = true
		}
	}
	if !found {
		t.Fatalf("保存新范围后，上报响应里应带 netq_targets 命令，实际 %+v", cmds)
	}
}

// TestNetQCatalogCommandOnVersionMismatch 覆盖目录：没设过不发；设过且
// 版本不一致才发；一致就不发（避免每 2 秒一条刷屏）。
func TestNetQCatalogCommandOnVersionMismatch(t *testing.T) {
	h, st := newTestHub(t)

	// 没设过覆盖件 → 永远不发（内置表各机器自带）
	if c := h.netqCatalogCommand("whatever"); c != nil {
		t.Fatalf("没有覆盖件时不该下发目录命令：%+v", c)
	}

	// 落一份覆盖件
	file := netprobe.CatalogFile{
		FetchedAt: "2026-10-11T00:00:00Z",
		Source:    "test",
		Targets: []netprobe.Target{
			{Province: "北京", ISP: netprobe.ISPTelecom, Name: "北京电信", Host: "1.2.3.4"},
		},
	}
	buf, err := json.Marshal(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting("netq_catalog", string(buf)); err != nil {
		t.Fatal(err)
	}

	// 版本不一致 → 发，且负载里带目录 JSON
	c := h.netqCatalogCommand("stale-version")
	if c == nil {
		t.Fatal("版本不一致时应下发 netq_catalog")
	}
	if c.Type != "netq_catalog" {
		t.Errorf("命令类型 = %s", c.Type)
	}
	if s, ok := c.Payload["catalog"].(string); !ok || !strings.Contains(s, "北京电信") {
		t.Errorf("负载里应带目录 JSON，实际 %v", c.Payload["catalog"])
	}

	// 版本一致 → 不发
	if c := h.netqCatalogCommand(netprobe.CatalogVersion()); c != nil {
		t.Errorf("版本一致时不该下发：%+v", c)
	}
}

// TestPendingCommandsIncludesTaskAndNetQ 两条命令可以同一次带下去。
func TestPendingCommandsIncludesTaskAndNetQ(t *testing.T) {
	h, st := newTestHub(t)
	n := mkNode(t, st, "测试节点 JP", "JP", "日本 · 东京", "")
	if _, err := h.DispatchTask(n.ID, "ipquality"); err != nil {
		t.Fatal(err)
	}
	cmds := h.pendingCommands(n.ID, []string{"广东"}, "", 0) // 范围不一致
	types := map[string]int{}
	for _, c := range cmds {
		types[c.Type]++
	}
	// 任务命令是 Type=test（执行配方在 agent 侧，Hub 不再下发 shell 文本），
	// 加上三网目标命令，两条同一次带下去。
	if types["test"] != 1 || types["netq_targets"] != 1 {
		t.Fatalf("应同时带 test + netq_targets，实际 %+v", cmds)
	}
}

// TestBroadcastNetQPushesRenderedHTML 探测结果入库后要立刻广播 netq 事件，
// payload 是**服务端渲染好的 HTML**（首页卡片行 + 详情页区块）——
// front-end 直接 outerHTML 替换，和 SSR 首屏同一套模板。
//
// 这是 boss 2026-10-12"主面板每秒刷新"的落点：数据一到就推，开着
// 页面也看得到，不用手刷。传输层由 SSE 负责，这里钉住 Hub 侧的行为。
func TestBroadcastNetQPushesRenderedHTML(t *testing.T) {
	h, st := newTestHub(t)
	n := mkNode(t, st, "东京", "JP", "", "")

	ts := time.Now().UnixMilli()
	// 两轮数据：让卡片有波可画、详情页表格有行
	for _, p := range []string{"上海", "北京"} {
		for rnd, ms := range []float64{40, 55} {
			if _, err := st.DB().Exec(
				`INSERT INTO net_quality (node_id, ts, mode, province, isp, city, host, ok, latency, jitter, loss)
				 VALUES (?,?,?,?,?,?,?,1,?,0,0)`,
				n.ID, ts-int64(rnd)*60_000, "icmp", p, "telecom", "", "1.2.3.4", ms); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := h.netqSaveProvinces([]string{"上海", "北京"}); err != nil {
		t.Fatal(err)
	}

	// 订阅一个频道再触发广播：broker.latest 现在只缓存 metrics 快照
	// （任务卡等增量事件不复播给新打开的页面），netq 帧要从订阅端收。
	ch := h.broker.subscribe()
	defer h.broker.unsubscribe(ch)
	h.broadcastNetQ(n.ID)

	var msg []byte
	wait := time.After(2 * time.Second)
recv:
	for {
		select {
		case m := <-ch:
			if bytes.HasPrefix(m, []byte("event: netq\n")) {
				msg = m
				break recv
			}
			// 其它事件不是本测试的对象，继续等 netq 帧
		case <-wait:
			t.Fatal("应广播 netq 事件，2s 内没收到")
		}
	}
	i := bytes.Index(msg, []byte("data: "))
	if i < 0 {
		t.Fatal("帧里没有 data")
	}
	var payload struct {
		Node   string `json:"node"`
		Rows   string `json:"rows"`
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(msg[i+len("data: "):], &payload); err != nil {
		t.Fatalf("payload 解析失败: %v", err)
	}
	if payload.Node != n.ID {
		t.Errorf("node = %q，应为 %q", payload.Node, n.ID)
	}
	// 卡片行：外层 div 带 id（前端靠它定位要换的那块），里面是三网行
	if !strings.Contains(payload.Rows, `id="netq-rows-`+n.ID+`"`) {
		t.Error("rows 应带 netq-rows-<节点ID>（前端替换的钩子）")
	}
	if !strings.Contains(payload.Rows, `class="nqr`) {
		t.Error("rows 里应含三网行")
	}
	// 详情页：整块 section（前端 outerHTML 整体替换）
	if !strings.Contains(payload.Detail, `id="nq-detail"`) {
		t.Error("detail 应带 nq-detail id")
	}
	if !strings.Contains(payload.Detail, "上海") {
		t.Error("detail 表格应只含选中范围内的省（上海/北京）")
	}

	// 没有可推内容时不广播。⚠️ "全不选"在新规则下会强制保留上海基准
	// （requiredNetQProvinces），不再产生空选择 —— 换一台没有探测数据的
	// 节点来走"rows/detail 都没有 → 早退"这条分支。
	other := mkNode(t, st, "空数据节点 HK", "HK", "中国 · 香港", "")
	h.broadcastNetQ(other.ID)
	deadline := time.After(150 * time.Millisecond)
drain:
	for {
		select {
		case m := <-ch:
			if bytes.HasPrefix(m, []byte("event: netq\n")) {
				t.Error("没有可推的内容时不该广播 netq")
				break drain
			}
		case <-deadline:
			break drain
		}
	}
}

// 兜底意义大于日常：agent 收到 netq_targets 才收缩上报，但旧版 agent
// 不认这条命令（2026-10-12 boss 撞见：选了 4 个点、详情还是 31 省）。
// Hub 侧先过滤，页面立刻只剩选中的；agent 更新后两边自然对齐。
func TestLoadNetQFiltersBySelection(t *testing.T) {
	h, st := newTestHub(t)
	n := mkNode(t, st, "测试节点 JP", "JP", "日本 · 东京", "")

	// 后台只选：上海 + 日本
	if _, err := h.netqSaveProvinces([]string{"上海", "日本"}); err != nil {
		t.Fatal(err)
	}
	ts := time.Now().UnixMilli()
	ins := func(province, isp string) {
		t.Helper()
		if _, err := st.DB().Exec(
			`INSERT INTO net_quality (node_id, ts, mode, province, isp, city, host, ok, latency, jitter, loss)
			 VALUES (?,?,?,?,?,?,?,1,?,0,0)`,
			n.ID, ts, "icmp", province, isp, "", "1.2.3.4", 30); err != nil {
			t.Fatal(err)
		}
	}
	// 四个国内省，只有上海在选中范围里
	for _, p := range []string{"上海", "北京", "广东", "四川"} {
		ins(p, netprobe.ISPTelecom)
	}
	// 三个境外地区，只有日本在选中范围里
	ins("", netprobe.ISPTW)
	ins("", netprobe.ISPUS)
	ins("", netprobe.ISPJP)

	sum := h.LoadNetQ(n.ID)
	if sum == nil {
		t.Fatal("有数据应返回汇总")
	}
	if len(sum.Rows) != 1 || sum.Rows[0].Province != "上海" {
		t.Fatalf("国内行应只剩上海，实际 %+v", sum.Rows)
	}
	if len(sum.Overseas) != 1 || sum.Overseas[0].Province != "日本" {
		t.Fatalf("境外应只剩日本，实际 %+v", sum.Overseas)
	}
	// 统计口径（覆盖数/探测点数）也按选中范围算
	if sum.Total != 2 || sum.Provinces != 1 {
		t.Errorf("Total=2（上海电信+日本）、Provinces=1，实际 %d/%d", sum.Total, sum.Provinces)
	}
}
