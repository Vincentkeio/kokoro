package hub

// 卡片上的延迟与流量。

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestCardShowsThreeISPLatency 三网延迟要按 电信→联通→移动 排。
func TestCardShowsThreeISPLatency(t *testing.T) {
	h, st := newTestHub(t)
	n := mkNode(t, st, "东京", "JP", "日本", "")

	// 故意乱序插入，验证卡片上会重排
	base := time.Now().UnixMilli()
	for _, row := range []struct {
		isp string
		ms  float64
	}{
		{"mobile", 68}, {"telecom", 45}, {"unicom", 52},
	} {
		if _, err := st.DB().Exec(
			`INSERT INTO net_quality (node_id, ts, mode, province, isp, city, host, ok, latency, jitter, loss)
			 VALUES (?,?,?,?,?,?,?,1,?,0,0)`,
			n.ID, base, "icmp", "北京", row.isp, "北京", "1.2.3.4", row.ms); err != nil {
			t.Fatal(err)
		}
	}

	got, err := st.NetQByNode()
	if err != nil {
		t.Fatal(err)
	}
	if len(got[n.ID]) != 3 {
		t.Fatalf("应有 3 个 ISP，实际 %d", len(got[n.ID]))
	}
	// 卡片上的顺序由 web.go 排；这里确认 store 给的原始数据是全的
	sum := 0.0
	for _, l := range got[n.ID] {
		sum += l.Latency
	}
	if want := 45.0 + 52 + 68; sum < want-0.1 || sum > want+0.1 {
		t.Errorf("延迟合计 = %.1f，应为 %.1f", sum, want)
	}

	// 端到端：卡片上的标签是「延迟」（boss 要求从「三网延迟」缩短）。
	seedMetrics(t, st, n.ID)
	body := renderBody(t, h, httptest.NewRequest(http.MethodGet, "/", nil))
	if !strings.Contains(body, `>延迟</span>`) {
		t.Error("卡片上应显示「延迟」标签")
	}
	if strings.Contains(body, "三网延迟") {
		t.Error("「三网延迟」已按要求缩短为「延迟」，不该再出现")
	}
}

// TestNetQOnlyLatestRound 只取**最近一轮**，不把历史混进来平均。
//
// 混进来的话，早上 200ms、现在 40ms 会显示成 120ms ——
// 而详情页用的是最近一轮（40ms）。两处口径不一致，
// 站长会以为其中一个在撒谎。
func TestNetQOnlyLatestRound(t *testing.T) {
	_, st := newTestHub(t)
	n := mkNode(t, st, "东京", "JP", "日本", "")
	old := time.Now().Add(-2 * time.Hour).UnixMilli()
	now := time.Now().UnixMilli()
	for _, ts := range []int64{old, now} {
		ms := 200.0
		if ts == now {
			ms = 40
		}
		if _, err := st.DB().Exec(
			`INSERT INTO net_quality (node_id, ts, mode, province, isp, city, host, ok, latency, jitter, loss)
			 VALUES (?,?,?,?,?,?,?,1,?,0,0)`,
			n.ID, ts, "icmp", "北京", "telecom", "北京", "1.2.3.4", ms); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := st.NetQByNode()
	if len(got[n.ID]) != 1 {
		t.Fatalf("应只返回 1 条（最近一轮），实际 %d", len(got[n.ID]))
	}
	if l := got[n.ID][0].Latency; l != 40 {
		t.Errorf("应取最近一轮的 40ms，实际 %.0fms —— 是不是把历史混进来平均了", l)
	}
}

// TestNetQIgnoresFailedProbes 不通的目标不能混进平均。
//
// 不通的延迟记 0，混进来会把平均值拉低 —— 看起来比实际更好。
func TestNetQIgnoresFailedProbes(t *testing.T) {
	_, st := newTestHub(t)
	n := mkNode(t, st, "东京", "JP", "日本", "")
	ts := time.Now().UnixMilli()
	// 一条通的 100ms，一条不通的 latency=0
	for _, ok := range []int{1, 0} {
		if _, err := st.DB().Exec(
			`INSERT INTO net_quality (node_id, ts, mode, province, isp, city, host, ok, latency, jitter, loss)
			 VALUES (?,?,?,?,?,?,?,?,?,0,100)`,
			n.ID, ts, "icmp", "北京", "telecom", "北京", "1.2.3.4", ok, 100.0); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := st.NetQByNode()
	if l := got[n.ID][0].Latency; l != 100 {
		t.Errorf("应只算通的那条（100ms），实际 %.0fms —— 不通的混进来了", l)
	}
}

// TestTrafficByNodeSince 流量按节点分开汇总，且只算窗口起点之后的。
func TestTrafficByNodeSince(t *testing.T) {
	_, st := newTestHub(t)
	a := mkNode(t, st, "A", "JP", "日本", "")
	b := mkNode(t, st, "B", "US", "美国", "")

	dayStart := dayStartMS(time.Now())
	ins := func(id string, bucket int64, up, down float64) {
		t.Helper()
		if _, err := st.DB().Exec(
			`INSERT OR REPLACE INTO metrics_5m (node_id, bucket, net_up_avg, net_down_avg, samples)
			 VALUES (?,?,?,?,142)`, id, bucket, up, down); err != nil {
			t.Fatal(err)
		}
	}
	ins(a.ID, dayStart+60000, 1000, 2000)    // 今天
	ins(b.ID, dayStart+60000, 500, 500)      // 今天
	ins(a.ID, dayStart-86400000, 9999, 9999) // 昨天，不该算进来

	got, err := st.TrafficByNodeSince(dayStart)
	if err != nil {
		t.Fatal(err)
	}
	// 1000 B/s × 300s = 300000；昨天那条不该出现
	if got[a.ID][0] != 300000 || got[a.ID][1] != 600000 {
		t.Errorf("A 的本日上行/下行 = %d/%d，应为 300000/600000", got[a.ID][0], got[a.ID][1])
	}
	if got[b.ID][0] != 150000 {
		t.Errorf("B 的本日上行 = %d，应为 150000", got[b.ID][0])
	}
}

// TestDayStartIsLocalMidnight 本日/本月起点要用**本机时区**的零点。
func TestDayStartIsLocalMidnight(t *testing.T) {
	now := time.Now()
	d := time.UnixMilli(dayStartMS(now)).In(now.Location())
	if d.Hour() != 0 || d.Minute() != 0 || d.Second() != 0 {
		t.Errorf("本日起点应归零到当天 00:00，实际 %v", d)
	}
	m := time.UnixMilli(monthStartMS(now)).In(now.Location())
	if m.Day() != 1 || m.Hour() != 0 {
		t.Errorf("本月起点应是 1 号 00:00，实际 %v", m)
	}
}

// TestNetLevelLowerIsBetter 延迟的档位方向跟占用率相反。
func TestNetLevelLowerIsBetter(t *testing.T) {
	if netLevel(30) != "ok" || netLevel(120) != "warn" || netLevel(300) != "bad" {
		t.Errorf("档位不对：30=%q 120=%q 300=%q",
			netLevel(30), netLevel(120), netLevel(300))
	}
	// 0 表示没测到，不该着色
	if netLevel(0) != "" {
		t.Errorf("0ms 应不加档位，实际 %q", netLevel(0))
	}
}

// TestCardShowsTraffic 卡片上要出现「本日 / 本月」两行流量。
//
// 端到端：塞今天的窗口 → 渲染首页 → HTML 里得有流量那一段。
// （只测 store 是不够的——接线断了 store 照样绿。）
func TestCardShowsTraffic(t *testing.T) {
	h, st := newTestHub(t)
	n := mkNode(t, st, "东京", "JP", "日本", "")

	// 塞两个今天的窗口：1000 B/s 与 3000 B/s
	base := dayStartMS(time.Now()) + 60000
	for i, v := range []float64{1000, 3000} {
		if _, err := st.DB().Exec(
			`INSERT OR REPLACE INTO metrics_5m (node_id, bucket, cpu_avg, mem_used_avg,
			    net_up_avg, net_down_avg, load1_avg, samples)
			 VALUES (?,?,10,100,?,?,0.5,142)`,
			n.ID, base+int64(i)*300000, v, v*2); err != nil {
			t.Fatal(err)
		}
	}
	// 首页要有一份"最新指标"才会走有数据的分支
	seedMetrics(t, st, n.ID)

	body := renderBody(t, h, httptest.NewRequest(http.MethodGet, "/", nil))
	if !strings.Contains(body, `class="netcard"`) {
		t.Fatal("首页没渲染出 netcard 块")
	}
	// boss 要求：删掉「流量」这个标题，把「今 / 月」写成「本日 / 本月」，
	// 各占一行。回退任何一处都该让这条红。
	if !strings.Contains(body, `>本日</span>`) {
		t.Error("卡片上没有「本日」流量行 —— 接线断了？")
	}
	if !strings.Contains(body, `>本月</span>`) {
		t.Error("卡片上没有「本月」流量行")
	}
	if strings.Contains(body, `>流量</span>`) {
		t.Error("卡片上不该再有「流量」标题")
	}
}
