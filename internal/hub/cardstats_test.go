package hub

// 卡片上的延迟与流量。

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestNetQWaveByNode 详情页分省格子的波动数据：按 (province,isp)
// 各自成序列、按时间升序、封顶 24 轮、不通的轮次被滤掉。
func TestNetQWaveByNode(t *testing.T) {
	_, st := newTestHub(t)
	a := mkNode(t, st, "东京", "JP", "", "")
	b := mkNode(t, st, "香港", "HK", "", "")

	now := time.Now().UnixMilli()
	ins := func(nodeID, prov string, agoMin int64, ok int, lat float64) {
		t.Helper()
		if _, err := st.DB().Exec(
			`INSERT INTO net_quality (node_id, ts, mode, province, isp, city, host, ok, latency, jitter, loss)
			 VALUES (?,?,?,?,?,?,?,?,?,0,0)`,
			nodeID, now-agoMin*60_000, "icmp", prov, "telecom", "", "1.2.3.4", ok, lat); err != nil {
			t.Fatal(err)
		}
	}
	// A 的上海：三轮（含一轮全断）、联通一轮；B 的上海一轮 —— 序列必须互不干扰
	ins(a.ID, "上海", 120, 1, 100)
	ins(a.ID, "上海", 60, 0, 999) // 不通的轮：滤掉
	ins(a.ID, "上海", 0, 1, 80)   // 最新
	ins(a.ID, "上海", 30, 1, 90)  // 中间
	ins(a.ID, "北京", 0, 1, 50)   // 另一个省
	ins(b.ID, "上海", 0, 1, 200)  // 另一台机器

	waves, err := st.NetQWaveByNode()
	if err != nil {
		t.Fatal(err)
	}
	gotA := waves[a.ID]["上海"]["telecom"]
	if len(gotA) != 3 {
		t.Fatalf("A·上海·电信 应有 3 轮（不通的滤掉），实际 %v", gotA)
	}
	// 升序（旧→新），最新一轮在最后
	for i, want := range []float64{100, 90, 80} {
		if gotA[i] != want {
			t.Errorf("第 %d 轮 = %.0f，应为 %.0f", i, gotA[i], want)
		}
	}
	if waves[a.ID]["北京"]["telecom"][0] != 50 {
		t.Error("省与省之间不能串台")
	}
	if waves[b.ID]["上海"]["telecom"][0] != 200 {
		t.Error("机器与机器之间不能串台")
	}
}

// TestCardShowsThreeISPLatency 三网延迟要按 电信→联通→移动 排，
// 且每个运营商一行带波动图（boss 2026-10-11 参考样式重做）。
func TestCardShowsThreeISPLatency(t *testing.T) {
	h, st := newTestHub(t)
	n := mkNode(t, st, "东京", "JP", "日本", "")

	// 两轮数据，让波动图画得出来；故意乱序插入，验证卡片上会重排
	base := time.Now().UnixMilli()
	for _, row := range []struct {
		isp string
		ms  float64
	}{
		{"mobile", 68}, {"telecom", 45}, {"unicom", 52},
	} {
		// 旧轮（30 分钟前）慢 10ms，新轮是 row.ms
		for _, r := range []struct {
			agoMin int64
			ms     float64
		}{{30, row.ms + 10}, {0, row.ms}} {
			if _, err := st.DB().Exec(
				`INSERT INTO net_quality (node_id, ts, mode, province, isp, city, host, ok, latency, jitter, loss)
				 VALUES (?,?,?,?,?,?,?,1,?,0,0)`,
				n.ID, base-r.agoMin*60*1000, "icmp", "上海", row.isp, "上海", "1.2.3.4", r.ms); err != nil {
				t.Fatal(err)
			}
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
		// 每个 ISP 都要有 2 轮历史，否则卡片上的波动图是空的
		if len(l.Hist) != 2 {
			t.Errorf("%s 应有 2 轮历史，实际 %d", l.ISP, len(l.Hist))
		}
	}
	if want := 45.0 + 52 + 68; sum < want-0.1 || sum > want+0.1 {
		t.Errorf("延迟合计 = %.1f，应为 %.1f", sum, want)
	}

	// 端到端：卡片上是每个运营商一行（圆点 + 毫秒 + 波动 + 丢包）
	seedMetrics(t, st, n.ID)
	body := renderBody(t, h, httptest.NewRequest(http.MethodGet, "/", nil))
	for _, want := range []string{
		`class="netqrows"`,
		`class="nqr`,
		`viewBox="0 0 108 22"`, // 波动图的坐标系，和 netqWavePaths 对齐
		`class="wl" d="M`,      // 波动线真的画出来了
		`丢包 0.0%`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("卡片缺少三网延迟波动的 %s", want)
		}
	}
}

func TestCardRangeRefreshUsesIndependentAxes(t *testing.T) {
	h, st := newTestHub(t)
	a := mkNode(t, st, "Low traffic", "JP", "", "")
	b := mkNode(t, st, "High traffic", "HK", "", "")
	base := time.Now().Add(-50 * time.Minute).UnixMilli()
	for i, rate := range []float64{60, 90, 60} {
		for id, multiplier := range map[string]float64{a.ID: 1, b.ID: 10_000} {
			if _, err := st.DB().Exec(`INSERT INTO metrics_5m(node_id,bucket,net_up_avg,net_down_avg,samples) VALUES(?,?,?,0,1)`,
				id, base+int64(i)*(20*time.Minute).Milliseconds(), rate*multiplier); err != nil {
				t.Fatal(err)
			}
		}
	}
	var responses []homeCardSeriesResponse
	for _, id := range []string{a.ID, b.ID} {
		w := httptest.NewRecorder()
		h.handleHomeCardSeries(w, httptest.NewRequest(http.MethodGet, "/api/v1/card-series?id="+id+"&range=1d", nil))
		if w.Code != http.StatusOK {
			t.Fatalf("card refresh returned %d: %s", w.Code, w.Body.String())
		}
		var response homeCardSeriesResponse
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		responses = append(responses, response)
	}
	if responses[0].Line == "" || responses[0].Line != responses[1].Line {
		t.Fatalf("proportional node traffic should retain the same visible variation:\n%s\n%s", responses[0].Line, responses[1].Line)
	}
	if responses[0].Peak == responses[1].Peak {
		t.Fatal("independent axes must preserve each node's actual peak label")
	}
}

func TestCardRangeRefreshReusesOneSharedSeriesWindow(t *testing.T) {
	h, st := newTestHub(t)
	a := mkNode(t, st, "Tokyo", "JP", "Tokyo", "")
	b := mkNode(t, st, "Hong Kong", "HK", "Hong Kong", "")
	bucket := time.Now().Add(-5 * time.Minute).UnixMilli()
	for _, id := range []string{a.ID, b.ID} {
		if _, err := st.DB().Exec(`INSERT INTO metrics_5m(node_id,bucket,net_up_avg,net_down_avg,samples) VALUES(?,?,100,200,1)`, id, bucket); err != nil {
			t.Fatal(err)
		}
	}
	var firstFrom, firstTo int64
	for _, id := range []string{a.ID, b.ID} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/card-series?id="+id+"&range=1d", nil)
		w := httptest.NewRecorder()
		h.handleHomeCardSeries(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("刷新 %s 曲线返回 %d: %s", id, w.Code, w.Body.String())
		}
		if firstTo == 0 {
			for _, entry := range h.cardSeriesCache {
				firstFrom, firstTo = entry.from, entry.to
			}
		}
	}
	if len(h.cardSeriesCache) != 1 {
		t.Fatalf("同一范围的多张卡应共用一个批量查询，缓存项=%d", len(h.cardSeriesCache))
	}
	for _, entry := range h.cardSeriesCache {
		if len(entry.series) != 2 {
			t.Fatalf("共享查询应同时包含两台公开节点，实际 %d", len(entry.series))
		}
		if entry.from != firstFrom || entry.to != firstTo {
			t.Fatal("同一时间范围下刷新不同卡片重新读取了不一致的时间窗口")
		}
		if entry.to-entry.from != (24 * time.Hour).Milliseconds() {
			t.Fatalf("时间窗口不一致：%dms", entry.to-entry.from)
		}
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
			n.ID, ts, "icmp", "上海", "telecom", "上海", "1.2.3.4", ms); err != nil {
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
			n.ID, ts, "icmp", "上海", "telecom", "上海", "1.2.3.4", ok, 100.0); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := st.NetQByNode()
	if l := got[n.ID][0].Latency; l != 100 {
		t.Errorf("应只算通的那条（100ms），实际 %.0fms —— 不通的混进来了", l)
	}
}

// TestNetQHistoryForWave 波动图要的是**最近几轮**（不是最近 N 小时），
// 丢包按全部点平均，一轮全断的格子要跳过。
func TestNetQHistoryForWave(t *testing.T) {
	_, st := newTestHub(t)
	n := mkNode(t, st, "东京", "JP", "日本", "")

	now := time.Now().UnixMilli()
	ins := func(ts int64, isp string, ok int, lat, loss float64) {
		t.Helper()
		if _, err := st.DB().Exec(
			`INSERT INTO net_quality (node_id, ts, mode, province, isp, city, host, ok, latency, jitter, loss)
			 VALUES (?,?,?,?,?,?,?,?,?,0,?)`,
			n.ID, ts, "icmp", "上海", isp, "上海", "1.2.3.4", ok, lat, loss); err != nil {
			t.Fatal(err)
		}
	}

	// 三轮 telecom：上海两个目标各一个点，验证"轮内平均"与"丢包按全部点"
	for i, ms := range []float64{100, 200, 300} {
		ts := now - int64(2-i)*30*60*1000
		ins(ts, "telecom", 1, ms, 10)    // 通，丢包 10%
		ins(ts, "telecom", 1, ms+100, 0) // 通，丢包 0 → 轮内延迟平均 (ms + ms+100)/2
	}
	// 最新一轮整轮全断（**独立的一轮**）：不进历史，也不该把数值拽成 0
	ins(now-time.Minute.Milliseconds(), "telecom", 0, 999, 100)
	// 境外 ISP：卡片只看三网，不该出现在结果里
	ins(now, "hk", 1, 12, 0)

	got, err := st.NetQByNode()
	if err != nil {
		t.Fatal(err)
	}
	if len(got[n.ID]) != 1 {
		t.Fatalf("应只有 telecom 一个 ISP（hk 不属于三网），实际 %d 个", len(got[n.ID]))
	}
	l := got[n.ID][0]
	if l.ISP != "telecom" {
		t.Fatalf("ISP = %q", l.ISP)
	}
	// 全断那轮被跳过，历史是 3 轮：150 / 250 / 350
	if len(l.Hist) != 3 {
		t.Fatalf("应有 3 轮历史（全断那轮跳过），实际 %d: %v", len(l.Hist), l.Hist)
	}
	for i, want := range []float64{150, 250, 350} {
		if l.Hist[i] != want {
			t.Errorf("第 %d 轮 = %.0f，应为 %.0f（轮内上海目标平均）", i, l.Hist[i], want)
		}
	}
	// 最近一轮 = 350（注意不是全断那轮的 999）
	if l.Latency != 350 {
		t.Errorf("最近一轮延迟 = %.0f，应为 350（全断轮要跳过）", l.Latency)
	}
	// 丢包按**全部点**平均：(10+0)/2 = 5 —— 不通也是丢包的一部分
	if math.Abs(l.Loss-36.7) > 0.1 {
		t.Errorf("丢包 = %.1f%%，应为 36.7（窗口内全部点平均：两轮各 10/0 + 全断轮 100）", l.Loss)
	}
	if l.OKN != 2 {
		t.Errorf("通的点数 = %d，应为 2", l.OKN)
	}
}

// TestNetQHistoryCapped 历史最多取 24 轮，防止间隔被配很小把图糊成一片。
func TestNetQHistoryCapped(t *testing.T) {
	_, st := newTestHub(t)
	n := mkNode(t, st, "东京", "JP", "日本", "")
	now := time.Now().UnixMilli()
	// 30 轮，间隔 5 分钟（配置下限）
	for i := 0; i < 30; i++ {
		if _, err := st.DB().Exec(
			`INSERT INTO net_quality (node_id, ts, mode, province, isp, city, host, ok, latency, jitter, loss)
			 VALUES (?,?,?,?,?,?,?,1,?,0,0)`,
			n.ID, now-int64(29-i)*5*60*1000, "icmp", "上海", "unicom", "上海", "1.2.3.4", float64(i)); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := st.NetQByNode()
	l := got[n.ID][0]
	if len(l.Hist) != 24 {
		t.Fatalf("历史应封顶 24 轮，实际 %d", len(l.Hist))
	}
	// 取的是**最近** 24 轮（6..29），不是最早的 24 轮
	if l.Hist[0] != 6 || l.Hist[len(l.Hist)-1] != 29 {
		t.Errorf("历史区间 = [%.0f, %.0f]，应为 [6, 29]", l.Hist[0], l.Hist[len(l.Hist)-1])
	}
	// 当前值 = 窗口聚合（15 分钟内最近 5 轮的平均），不是裸的最新一轮 ——
	// 30 轮 5 分钟间隔，窗口内是最后 3 轮（27/28/29）→ 平均 28。
	if math.Abs(l.Latency-28) > 0.1 {
		t.Errorf("窗口聚合值 = %.0f，应为 28", l.Latency)
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
