package hub

// 状态时间轴（一天一格）的测试。
//
// 数据来自 metrics_5m：某一格有数据 = 那 5 分钟机器是活的。
// 用事件表不行 —— 它是今天才建的，没有历史。

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// seedBuckets 往 metrics_5m 里塞若干天的数据。
// dayAgo：几天前；perDay：这一天塞多少格（满格 288）；samples：每格多少次上报。
//
// ⚠️ 同时把节点创建时间提前到 40 天前 —— 在线率的分母是
// 「这一天从几点开始**应该**有数据」，节点刚建的话分母就是 0，
// 怎么塞都算不出东西。
func seedBuckets(t *testing.T, h *Hub, nodeID string, dayAgo int, perDay, samples int) {
	t.Helper()
	loc := time.Now().Location()
	n := time.Now().In(loc)
	// 按**面板本地时区**取当天零点，跟 store 里切天的方式保持一致
	day := time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, -dayAgo)
	if _, err := h.store.DB().Exec(
		`UPDATE nodes SET created_at = ? WHERE id = ?`,
		time.Now().AddDate(0, 0, -40).UnixMilli(), nodeID); err != nil {
		t.Fatal(err)
	}
	const bucketMS = 5 * 60 * 1000
	for i := 0; i < perDay; i++ {
		b := day.UnixMilli() + int64(i)*bucketMS
		if _, err := h.store.DB().Exec(
			`INSERT OR REPLACE INTO metrics_5m (node_id, bucket, samples) VALUES (?,?,?)`,
			nodeID, b, samples); err != nil {
			t.Fatalf("塞聚合数据失败: %v", err)
		}
	}
}

// TestUptimeAllGood 整月都正常时，在线率应接近 100% 且是绿色。
func TestUptimeAllGood(t *testing.T) {
	h, st := newTestHub(t)
	n := mkNode(t, st, "东京 zouter", "JP", "日本 · 东京", "")
	// 前 5 天各塞满一天
	for d := 1; d <= 5; d++ {
		seedBuckets(t, h, n.ID, d, 288, 142)
	}

	sum := h.loadUptime(n.ID)
	if sum == nil {
		t.Fatal("时间轴为空")
	}
	if len(sum.Cells) != uptimeDays {
		t.Errorf("格子数 = %d，应为 %d", len(sum.Cells), uptimeDays)
	}
	if sum.Pct != "100.00%" {
		t.Errorf("全正常时应为 100.00%%，实际 %s", sum.Pct)
	}
	if sum.Level != "ok" {
		t.Errorf("全正常应为 ok，实际 %s", sum.Level)
	}
}

// TestUptimeDetectsGap 掉线的那些天要变成红格，不能因为"有数据"就算在线。
//
// 这条是核心：只有 50% 的格子有数据，就该显示约 50% 在线。
func TestUptimeDetectsGap(t *testing.T) {
	h, st := newTestHub(t)
	n := mkNode(t, st, "东京 zouter", "JP", "日本 · 东京", "")
	seedBuckets(t, h, n.ID, 1, 288, 142) // 完整的一天
	seedBuckets(t, h, n.ID, 2, 144, 142) // 只跑了一半

	sum := h.loadUptime(n.ID)
	if sum == nil {
		t.Fatal("时间轴为空")
	}
	// 格子是**按日期升序**排的，最后一个才是今天。
	// 所以 dayAgo=1（塞满）在倒数第 2 格，dayAgo=2（半天）在倒数第 3 格。
	//
	// ⚠️ 我第一版把这两个写反了，测试红了还以为是算错了 ——
	// 其实是断言看错了格子。
	fullDay := sum.Cells[len(sum.Cells)-2] // 昨天，塞了 288 格
	halfDay := sum.Cells[len(sum.Cells)-3] // 前天，只塞了 144 格

	if fullDay.Level != "ok" {
		t.Errorf("完整的一天应为 ok: %+v", fullDay)
	}
	// 关键：只跑了一半的那天必须被判成不正常。
	// 这条守住"中午挂了就再没起来"不会被算成 100%。
	if halfDay.Level == "ok" {
		t.Errorf("只跑了一半的那天不该判成正常: %+v", halfDay)
	}
	if !strings.HasPrefix(halfDay.Pct, "5") {
		t.Errorf("在线率应约 50%%，实际 %s", halfDay.Pct)
	}
}

// TestUptimeEmptyDaysAreShown 没数据的天要占位显示成"无数据"，不能直接跳过。
//
// 空格子本身就是信息 —— 那天机器是挂的。
func TestUptimeEmptyDaysAreShown(t *testing.T) {
	h, st := newTestHub(t)
	n := mkNode(t, st, "东京 zouter", "JP", "日本 · 东京", "")
	seedBuckets(t, h, n.ID, 10, 288, 142) // 只有 10 天前有数据

	sum := h.loadUptime(n.ID)
	if sum == nil {
		t.Fatal("时间轴为空")
	}
	if len(sum.Cells) != uptimeDays {
		t.Fatalf("格子数 = %d，应为 %d（没数据的天也要占位）", len(sum.Cells), uptimeDays)
	}
	none := 0
	for _, c := range sum.Cells {
		if c.Level == "none" {
			none++
		}
	}
	if none == 0 {
		t.Error("应该有很多『无数据』的格子")
	}
}

// TestUptimeNoDataReturnsNil 从没上报过的机器不该显示一条全灰的时间轴。
func TestUptimeNoDataReturnsNil(t *testing.T) {
	h, st := newTestHub(t)
	n := mkNode(t, st, "新机器", "JP", "日本 · 东京", "")
	if sum := h.loadUptime(n.ID); sum != nil {
		t.Errorf("没上报过数据时不该渲染时间轴: %+v", sum)
	}
}

// TestUptimeRendersOnCardAndDetail 卡片和详情页都要有这条时间轴。
func TestUptimeRendersOnCardAndDetail(t *testing.T) {
	h, st := newTestHub(t)
	n := mkNode(t, st, "东京 zouter", "JP", "日本 · 东京", "")
	for d := 1; d <= 3; d++ {
		seedBuckets(t, h, n.ID, d, 288, 142)
	}

	home := renderBody(t, h, httptest.NewRequest(http.MethodGet, "/", nil))
	for _, want := range []string{"uptime-bar", "在线率", "uptime-legend", "近 30 天"} {
		if !strings.Contains(home, want) {
			t.Errorf("首页卡片缺少 %q", want)
		}
	}
	// 悬停提示里要带日期和百分比
	if !strings.Contains(home, "在线率 100.00%") {
		t.Error("格子的悬停提示里应有具体在线率")
	}

	node := renderBody(t, h, httptest.NewRequest(http.MethodGet, "/n/"+n.Slug, nil))
	if !strings.Contains(node, "uptime-bar") {
		t.Error("详情页缺少状态时间轴")
	}
}

// TestUptimeLevelThresholds 分档阈值要偏严 —— 探针的在线率本来就该接近 100%。
func TestUptimeLevelThresholds(t *testing.T) {
	cases := []struct {
		pct  float64
		want string
	}{
		{100, "ok"}, {99.5, "ok"}, {99.4, "warn"},
		{95, "warn"}, {94.9, "bad"}, {0, "bad"},
	}
	for _, c := range cases {
		if got := uptimeLevel(c.pct); got != c.want {
			t.Errorf("uptimeLevel(%.1f) = %s，应为 %s", c.pct, got, c.want)
		}
	}
}
