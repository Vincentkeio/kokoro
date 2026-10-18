package store

import (
	"github.com/Vincentkeio/kokoro/internal/model"
	"testing"
	"time"
)

func TestConnectivityRoundTrip(t *testing.T) {
	st, err := Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	n := &model.Node{Name: "connectivity", TokenHash: "connectivity"}
	if err = st.CreateNode(n); err != nil {
		t.Fatal(err)
	}
	m := &model.Metrics{Ts: time.Now().UnixMilli(), Connectivity: &model.IPConnectivity{IPv4: true, IPv6: false, CheckedAt: 1234}}
	if err = st.InsertMetrics(n.ID, m); err != nil {
		t.Fatal(err)
	}
	latest, err := st.LatestMetrics(n.ID)
	if err != nil {
		t.Fatal(err)
	}
	if latest.Connectivity == nil || !latest.Connectivity.IPv4 || latest.Connectivity.IPv6 || latest.Connectivity.CheckedAt != 1234 {
		t.Fatalf("%+v", latest.Connectivity)
	}
}

func TestConnectivityMigrationFromV16(t *testing.T) {
	path := t.TempDir() + "/old.db"
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.DB().Exec("ALTER TABLE metrics_raw DROP COLUMN connectivity"); err != nil {
		t.Fatal(err)
	}
	if _, err = st.DB().Exec("ALTER TABLE nodes DROP COLUMN netq_ip_version; ALTER TABLE net_quality DROP COLUMN ip_version"); err != nil {
		t.Fatal(err)
	}
	if _, err = st.DB().Exec("PRAGMA user_version=16"); err != nil {
		t.Fatal(err)
	}
	st.Close()
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var value string
	if err = st.DB().QueryRow("SELECT dflt_value FROM pragma_table_info('metrics_raw') WHERE name='connectivity'").Scan(&value); err != nil {
		t.Fatal(err)
	}
	if value != "''" {
		t.Fatal(value)
	}
}
