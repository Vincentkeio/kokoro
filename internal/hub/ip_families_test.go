package hub

import (
	"github.com/Vincentkeio/kokoro/internal/model"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestIPFamilySelectionDispatch(t *testing.T) {
	for _, tc := range []struct {
		families []string
		kind     string
	}{
		{[]string{"4"}, "ipquality4"}, {[]string{"6"}, "ipquality6"}, {[]string{"4", "6"}, "ipquality"},
	} {
		h, st := newTestHub(t)
		n := mkNode(t, st, "family", "JP", "日本", "")
		form := url.Values{"node": {n.ID}, "kind": {"ipquality"}, "ip_family_selection": {"1"}, "ip_family": tc.families}
		req := httptest.NewRequest(http.MethodPost, "/admin/tasks", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		h.handleAdminTasks(w, req, true)
		tasks, _ := st.ListTasks(n.ID, 20)
		if len(tasks) != 1 || tasks[0].Kind != tc.kind {
			t.Fatalf("selection %v: %+v", tc.families, tasks)
		}
	}
}

func TestEmptyIPFamilySelectionDoesNotQueueTests(t *testing.T) {
	h, st := newTestHub(t)
	n := mkNode(t, st, "empty selection", "JP", "日本", "")
	form := url.Values{"node": {n.ID}, "kind": {"all"}, "ip_family_selection": {"1"}}
	req := httptest.NewRequest(http.MethodPost, "/admin/tasks", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.handleAdminTasks(w, req, true)
	tasks, _ := st.ListTasks(n.ID, 20)
	if len(tasks) != 0 {
		t.Fatal(tasks)
	}
	if !strings.Contains(w.Header().Get("Location"), "err=") {
		t.Fatal(w.Header())
	}
}

func TestAllTestsRespectSingleIPFamily(t *testing.T) {
	h, st := newTestHub(t)
	n := mkNode(t, st, "only six", "JP", "日本", "")
	count, err := h.dispatchAllIP(n.ID, "ipquality6")
	if err != nil || count != 3 {
		t.Fatal(count, err)
	}
	tasks, _ := st.ListTasks(n.ID, 20)
	seen := false
	for _, task := range tasks {
		if task.Kind == "ipquality" || task.Kind == "ipquality4" {
			t.Fatal(task.Kind)
		}
		if task.Kind == "ipquality6" {
			seen = true
		}
	}
	if !seen {
		t.Fatal("missing IPv6 task")
	}
}

func TestIPFamiliesRemainSeparateOnCards(t *testing.T) {
	h, st := newTestHub(t)
	n := mkNode(t, st, "dual", "JP", "日本", "")
	task, _ := h.DispatchTask(n.ID, "ipquality")
	out := `{"event":"result","test":"ip4","ok":true,"data":{"usage":"机房","risk_scamalytics":1,"unlocked":["Netflix"],"unlock_total":1}}` + "\n" + `{"event":"result","test":"ip6","ok":false,"error":"IPv6 无网络"}`
	if err := h.finishTaskFromResult(model.CommandResult{ID: task.ID, OK: true, Stdout: out}); err != nil {
		t.Fatal(err)
	}
	card := nodeCard{Node: *n}
	h.populateTaskCard(&card)
	if !strings.Contains(card.TaskSummary, "IPv4 IP质量") || !strings.Contains(card.TaskSummary, "IPv6 IP质量 检测失败") {
		t.Fatal(card.TaskSummary)
	}
	if len(card.TaskItems) < 2 {
		t.Fatal("missing details")
	}
	// A later IPv4-only run must preserve the independently measured IPv6 row.
	task, _ = h.DispatchTask(n.ID, "ipquality4")
	out = `{"event":"result","test":"ip4","ok":true,"data":{"risk_scamalytics":99}}`
	if err := h.finishTaskFromResult(model.CommandResult{ID: task.ID, OK: true, Stdout: out}); err != nil {
		t.Fatal(err)
	}
	card = nodeCard{Node: *n}
	h.populateTaskCard(&card)
	if strings.Count(card.TaskSummary, "IPv4") != 1 || strings.Count(card.TaskSummary, "IPv6") != 1 {
		t.Fatal(card.TaskSummary)
	}
}

func TestRouteCardCompactionAndBadges(t *testing.T) {
	for _, line := range []string{
		"北京 联通 9929（AS9929） / 上海 路径 ASN 不足，未识别 / 广东 未知",
		"北京 联通 9929 / 上海 联通 9929 / 深圳 未识别",
		"北京 联通 4837/169 / 上海 联通 9929 / 深圳 未识别",
		"北京 联通 9929 / 上海 联通 4837/169",
	} {
		if got := compactCardRoute(line); got != "9929" {
			t.Fatal(got)
		}
	}
	rows := makeTaskSummaryRows([]string{"回程 联通 9929", "回程 电信 CN2（GT/GIA未确认）", "回程 移动 CMIN2"})
	for i, class := range []string{"route-9929", "route-cn2", "route-cmin2"} {
		if len(rows[i].Badges) != 1 || rows[i].Badges[0].Class != class {
			t.Fatal(rows[i])
		}
	}
}

func TestRouteCardSelectsOneBackbone(t *testing.T) {
	for _, tc := range []struct{ line, want string }{
		{"北京 163 骨干 / 上海 CN2 GIA / 广东 CN2 GT", "CN2 GIA"},
		{"北京 移动 CMI / 上海 移动 CMIN2", "CMIN2"},
		{"北京 163 骨干 / 上海 4837/169", "163 骨干"},
		{"北京 未知 / 上海 4837/169 / 深圳 163 骨干", "4837/169"},
		{"北京 未知 / 上海 未识别", "未知"},
	} {
		if got := compactCardRoute(tc.line); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.line, got, tc.want)
		}
	}
}

func TestConnectivityIndicatorStates(t *testing.T) {
	c := nodeCard{Node: model.Node{Online: true}, Latest: &model.Metrics{Connectivity: &model.IPConnectivity{IPv4: true, IPv6: false, CheckedAt: time.Now().UnixMilli()}}}
	rows := c.ConnectivityIndicators()
	if rows[0].State != "online" || rows[1].State != "unreachable" {
		t.Fatal(rows)
	}
	c.Online = false
	rows = c.ConnectivityIndicators()
	if rows[0].State != "unknown" || rows[0].Text != "离线" {
		t.Fatal(rows)
	}
	c.Online = true
	c.Latest.Connectivity.CheckedAt = 1
	if c.ConnectivityIndicators()[0].Text != "待更新" {
		t.Fatal("stale green indicator")
	}
}
