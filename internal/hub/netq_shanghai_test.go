package hub

import (
	"strings"
	"testing"
	"time"
)

func TestNetQShanghaiMandatoryForLegacySettingsAndUI(t *testing.T) {
	h, st := newTestHub(t)
	for _, raw := range []string{`[]`, `["北京"]`, `["上海","上海"]`} {
		if err := st.SetSetting(settingNetQProvinces, raw); err != nil {
			t.Fatal(err)
		}
		found := 0
		for _, p := range h.netqProvinces() {
			if p == "上海" {
				found++
			}
		}
		if found != 1 {
			t.Fatalf("legacy selection %s lost baseline", raw)
		}
	}
	_, body := getAsAdmin(t, h, "/admin")
	if !strings.Contains(body, `value="上海" checked disabled`) {
		t.Fatal("Shanghai checkbox must be checked and disabled")
	}
}

func TestNetQShanghaiBaselineExcludesOtherProvinces(t *testing.T) {
	_, st := newTestHub(t)
	n := mkNode(t, st, "mixed", "US", "美国", "")
	missing := mkNode(t, st, "no Shanghai", "JP", "日本", "")
	now := time.Now().UnixMilli()
	insert := func(id, province, isp string, ts int64, ok int, lat, loss float64) {
		t.Helper()
		_, err := st.DB().Exec(`INSERT INTO net_quality(node_id,ts,province,isp,ok,latency,loss) VALUES(?,?,?,?,?,?,?)`, id, ts, province, isp, ok, lat, loss)
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, isp := range []string{"telecom", "unicom", "mobile"} {
		insert(n.ID, "上海", isp, now-120000, 1, 120, 0)
		insert(n.ID, "上海", isp, now-60000, 1, 140, 0)
		// More than five newer non-Shanghai rounds must not displace Shanghai's window.
		for i := 0; i < 8; i++ {
			insert(n.ID, "北京", isp, now-int64(i)*1000, 0, 0, 100)
		}
		insert(n.ID, "广东", isp, now, 1, 999, 66)
		insert(missing.ID, "北京", isp, now, 1, 20, 0)
	}
	got, err := st.NetQByNode()
	if err != nil {
		t.Fatal(err)
	}
	if len(got[missing.ID]) != 0 {
		t.Fatalf("borrowed another province: %+v", got[missing.ID])
	}
	if len(got[n.ID]) != 3 {
		t.Fatal(got[n.ID])
	}
	for _, r := range got[n.ID] {
		if r.Latency != 130 || r.Loss != 0 || r.OKN != 2 || len(r.Hist) != 2 || r.Hist[0] != 120 || r.Hist[1] != 140 {
			t.Fatalf("mixed baseline: %+v", r)
		}
	}
	// Actual Shanghai failures still count as loss; excluding Beijing must not hide them.
	insert(n.ID, "上海", "telecom", now, 0, 0, 100)
	got, err = st.NetQByNode()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range got[n.ID] {
		if r.ISP == "telecom" && (r.Loss < 33.3 || r.Loss > 33.4) {
			t.Fatalf("Shanghai failure hidden: %+v", r)
		}
	}
}
