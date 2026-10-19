package hub

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Vincentkeio/kokoro/internal/model"
)

func TestHubRejectsUnimplementedTLSModes(t *testing.T) {
	h, st := newTestHub(t)
	for _, mode := range []string{"selfsigned", "auto", "invalid"} {
		cfg := *h.cfg
		cfg.TLSMode = mode
		if _, err := New(&cfg, st); err == nil {
			t.Fatalf("TLS mode %q silently accepted without TLS", mode)
		}
	}
	for _, mode := range []string{"", "none", "proxy"} {
		cfg := *h.cfg
		cfg.TLSMode = mode
		if _, err := New(&cfg, st); err != nil {
			t.Fatalf("supported TLS mode %q: %v", mode, err)
		}
	}
}

func TestMetricClockCorrectionRestoresLatestSnapshot(t *testing.T) {
	h, st := newTestHub(t)
	token := "nt_clock_test"
	node := &model.Node{ID: "clock", Name: "Clock", TokenHash: hashToken(token)}
	if err := st.CreateNode(node); err != nil {
		t.Fatal(err)
	}
	report := func(ts int64, cpu int) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/report", strings.NewReader(fmt.Sprintf(`{"ts":%d,"cpu":{"usage":%d}}`, ts, cpu)))
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("report: %d %s", w.Code, w.Body.String())
		}
	}
	report(time.Now().Add(24*time.Hour).UnixMilli(), 10)
	latest, err := st.LatestMetrics(node.ID)
	if err != nil || latest == nil || latest.Ts > time.Now().UnixMilli() {
		t.Fatalf("future metric not corrected: %+v %v", latest, err)
	}
	time.Sleep(2 * time.Millisecond) // distinct millisecond sample after clock correction
	report(time.Now().UnixMilli(), 20)
	latest, err = st.LatestMetrics(node.ID)
	if err != nil || latest == nil || latest.CPU.Usage != 20 {
		t.Fatalf("corrected report hidden: %+v %v", latest, err)
	}
	// Data written by older releases must not freeze snapshots after upgrade.
	if err := st.InsertMetrics(node.ID, &model.Metrics{Ts: time.Now().Add(48 * time.Hour).UnixMilli(), CPU: model.CPUStat{Usage: 99}}); err != nil {
		t.Fatal(err)
	}
	latest, err = st.LatestMetrics(node.ID)
	if err != nil || latest == nil || latest.CPU.Usage != 20 {
		t.Fatalf("legacy future row hides latest: %+v %v", latest, err)
	}
	snapshot, err := st.LatestSnapshot()
	if err != nil || snapshot[node.ID] == nil || snapshot[node.ID].CPU.Usage != 20 {
		t.Fatalf("legacy future row hides SSE snapshot: %+v %v", snapshot, err)
	}
	// Normal buffered reports retain their historical time instead of being
	// counted as fresh samples at receipt time.
	replayTs := time.Now().Add(-10 * time.Minute).UnixMilli()
	report(replayTs, 30)
	var replayCPU float64
	if err := st.DB().QueryRow(`SELECT cpu FROM metrics_raw WHERE node_id=? AND ts=?`, node.ID, replayTs).Scan(&replayCPU); err != nil || replayCPU != 30 {
		t.Fatalf("replay collection time changed: cpu=%v err=%v", replayCPU, err)
	}
}

func TestShortRIFFUploadsReturnBadRequest(t *testing.T) {
	h, st := newTestHub(t)
	for size := 4; size < 12; size++ {
		req := uploadReq(t, h, st, "file", "broken.webp", append([]byte("RIFF"), make([]byte, size-4)...))
		req.Header.Set("Origin", "http://example.com")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%d-byte RIFF: status=%d", size, w.Code)
		}
	}
}

func TestPublicNodeVisitPreservesProfileMetadata(t *testing.T) {
	h, st := newTestHub(t)
	node := mkNode(t, st, "Visit metadata", "JP", "Tokyo", "")
	profile := &model.NodeProfile{NodeID: node.ID, Price: "99 CNY", ExpireAt: "2027-01-01", UpdatedAt: 1234, PV: 5}
	if err := st.SaveProfile(profile); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/n/"+node.Slug, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("node page status=%d", w.Code)
	}
	got, err := st.GetProfile(node.ID)
	if err != nil || got == nil || got.PV != 6 || got.Price != profile.Price || got.ExpireAt != profile.ExpireAt || got.UpdatedAt != profile.UpdatedAt {
		t.Fatalf("page view modified profile data: %+v %v", got, err)
	}
}
