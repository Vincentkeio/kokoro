package hub

import (
	"github.com/Vincentkeio/kokoro/internal/model"
	"strings"
	"testing"
)

func TestRouteCardDualStackRows(t *testing.T) {
	h, st := newTestHub(t)
	n := mkNode(t, st, "dual route", "JP", "日本", "")
	task, _ := h.DispatchTask(n.ID, "netquality")
	output := `{"event":"result","test":"net","ok":true,"data":{"down_mbps":10}}
{"event":"result","test":"route4","ok":true,"data":{"电信":{"line":"北京 电信 CN2 GIA（AS4809） / 上海 163 骨干"},"联通":{"line":"北京 联通 4837 / 上海 联通 9929（AS9929）"},"移动":{"line":"移动 CMIN2（AS58807）"}}}
{"event":"result","test":"route6","ok":true,"data":{"电信":{"line":"电信 163（AS4134）"},"联通":{"line":"联通 4837（AS4837）"},"移动":{"line":"移动 CMI（AS9808）"}}}`
	if err := h.finishTaskFromResult(model.CommandResult{ID: task.ID, OK: true, Stdout: output}); err != nil {
		t.Fatal(err)
	}
	saved, _ := st.GetTask(task.ID)
	if saved.Status != model.TaskDone {
		t.Fatal(saved.Error)
	}
	c := nodeCard{Node: *n}
	h.populateTaskCard(&c)
	routes := []taskSummaryRow{}
	for _, row := range c.TaskSummaryRows {
		if row.Route {
			routes = append(routes, row)
		}
	}
	if len(routes) != 3 {
		t.Fatal(routes)
	}
	if routes[0].Label != "电信" || routes[0].Value != "V4 CN2 GIA V6 163" {
		t.Fatal(routes[0])
	}
	if routes[1].Value != "V4 9929 V6 4837" {
		t.Fatal(routes[1])
	}
	for _, row := range routes {
		if strings.Contains(row.Label, "回程") || len(row.Badges) != 4 {
			t.Fatal(row)
		}
	}
}

func TestLegacyRoutesNeverBecomeIPv6(t *testing.T) {
	families := map[string]map[string]string{}
	collectCardRoutes(parseBenchNDJSON(`{"event":"result","test":"route","ok":true,"data":{"电信":{"line":"CN2 GIA"}}}`), families)
	if families["IPv4"]["电信"] != "CN2 GIA" || families["IPv6"] != nil {
		t.Fatal(families)
	}
	row, ok := dualRouteCardRow("回程 电信 V4 CN2 GIA V6 无")
	if !ok || row.Label != "电信" || row.Value != "V4 CN2 GIA V6 无" || row.Badges[3].Text != "无" {
		t.Fatal(row)
	}
}

func TestDualRouteCardShowsOneFinalLinePerFamily(t *testing.T) {
	cases := []struct{ input, want string }{
		{"回程 电信 V4 Softbank → 电信 163（AS4134） V6 —", "V4 163 V6 —"},
		{"回程 联通 V4 — V6 NTT -> 联通 4837（AS4837）", "V4 — V6 NTT"},
		{"回程 移动 V4 Level3 -> CMI V6 Softbank -> CMI", "V4 CMI V6 CMI"},
	}
	for _, tc := range cases {
		row, ok := dualRouteCardRow(tc.input)
		if !ok || row.Value != tc.want || strings.Contains(row.Value, "→") || strings.Contains(row.Value, " -> ") {
			t.Errorf("%q => %#v, want one resolved line per family: %q", tc.input, row, tc.want)
		}
	}
}

func TestDualRouteCardCompactsMissingASNStatus(t *testing.T) {
	row, ok := dualRouteCardRow("回程 电信 V4 CN2 GIA V6 路由有响应，但 ASN 数据缺失")
	if !ok || row.Badges[len(row.Badges)-1].Text != "未知" || row.Value != "V4 CN2 GIA V6 未知" {
		t.Fatalf("expected concise one-line IPv6 status, got %#v", row)
	}
}

func TestRouteCardUnknownDescriptionsStayShort(t *testing.T) {
	for _, line := range []string{
		"未能定位中国大陆路由，线路 ASN 不可见",
		"未定位到中国大陆路由；仅识别到 AS12345",
		"路径 ASN 不足，未识别",
		"北京 未能定位中国大陆路由，线路 ASN 不可见 / 上海 未识别",
	} {
		if got := compactCardRoute(line); got != "未知" {
			t.Fatalf("%q => %q", line, got)
		}
	}
	if got := compactCardRoute("北京 未能定位中国大陆路由 / 上海 联通 9929"); got != "9929" {
		t.Fatal(got)
	}
	if got := compactCardRoute(""); got != "" {
		t.Fatal("empty must remain empty", got)
	}
	if got := compactCardRoute("CN2（GT/GIA未确认）"); got != "CN2（GT/GIA未确认）" {
		t.Fatal("known backbone lost", got)
	}
}

func TestRouteCardShowsOneProviderWithoutTransitChain(t *testing.T) {
	cases := []struct{ input, want string }{
		{"Softbank → 电信 163（AS4134）", "163"},
		{"NTT -> 联通 4837（AS4837）", "NTT"},
		{"Level3 ⇒ CMI", "CMI"},
		{"北京 NTT -> 联通 4837 / 上海 NTT -> 联通 4837 / 广东 NTT -> 联通 4837", "NTT"},
		{"北京 Cogent -> 联通 4837 / 上海 NTT -> 联通 4837", "NTT"},
		{"AS3356 -> AS4134", "163"},
		{"AS2914 -> AS4134", "163"},
		{"AS3356 -> AS4809", "CN2"},
		{"AS3356 -> AS23764", "CTGNet"},
		{"AS58453 -> AS58807", "CMIN2"},
		{"AS4837 -> AS9929", "9929"},
		{"AS9929 -> AS4837", "9929"},
		{"未定位到中国大陆路由；仅识别到 AS2914", "NTT"},
		{"Example Transit → Other Network", "Example Transit"},
		{"Example Transit->Other Network → Third Network", "Example Transit"},
		{"AS1740 -> AS29140", "AS1740"},
	}
	for _, tc := range cases {
		if got := cardBackboneName(tc.input); got != tc.want {
			t.Errorf("%q => %q, want one provider %q", tc.input, got, tc.want)
		}
		badges := cardRouteBadges(tc.input)
		if len(badges) != 1 || badges[0].Text != tc.want {
			t.Errorf("route badges for %q => %#v, want only %q", tc.input, badges, tc.want)
		}
	}
}

func TestUnavailableIPv6ReplacesOldResult(t *testing.T) {
	families := map[string]map[string]string{}
	collectCardRoutes(parseBenchNDJSON(`{"event":"result","test":"route6","ok":true,"data":{"available":false}}`), families)
	collectCardRoutes(parseBenchNDJSON(`{"event":"result","test":"route6","ok":true,"data":{"电信":{"line":"CN2 GIA"}}}`), families)
	if values, present := families["IPv6"]; !present || values != nil {
		t.Fatal(families)
	}
}
