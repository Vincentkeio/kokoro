package hub

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Vincentkeio/kokoro/internal/netprobe"
)

func TestNetQCatalogRefreshPreservesIPv6AndStableSyncVersion(t *testing.T) {
	h, st := newTestHub(t)
	previous := append([]netprobe.Target(nil), netprobe.Catalog()...)
	t.Cleanup(func() { netprobe.SetCatalog(previous) })
	original := http.DefaultTransport
	http.DefaultTransport = autoUpdateTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != netqUpstreamURL {
			t.Errorf("unexpected catalog source: %s", r.URL)
		}
		body := `[{"province":"上海市","isp_code":"ct","ip_version":"v4","ips":"1.1.1.1,8.8.8.8"}]`
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = original })
	file, err := h.refreshNetQCatalog(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var v4, v6 bool
	for _, target := range file.Targets {
		v4 = v4 || target.IPVersion == "v4"
		v6 = v6 || target.IPVersion == "v6"
	}
	if !v4 || !v6 {
		t.Fatal("an IPv4-only upstream must not remove the embedded IPv6 catalog")
	}
	raw, _ := st.GetSetting(settingNetQCatalog)
	var stored netprobe.CatalogFile
	if err := json.Unmarshal([]byte(raw), &stored); err != nil {
		t.Fatal(err)
	}
	if len(stored.Targets) != len(file.Targets) {
		t.Fatal("stored catalog differs from the active one")
	}
	hubVersion := netprobe.CatalogVersion()
	command := h.netqCatalogCommand("old-catalog")
	if command == nil {
		t.Fatal("updated catalog should synchronize to an outdated agent")
	}
	var delivered netprobe.CatalogFile
	if err := json.Unmarshal([]byte(command.Payload["catalog"].(string)), &delivered); err != nil {
		t.Fatal(err)
	}
	netprobe.SetCatalog(delivered.Targets)
	if agentVersion := netprobe.CatalogVersion(); agentVersion != hubVersion {
		t.Fatalf("Hub/agent hashes diverged: %s vs %s", hubVersion, agentVersion)
	}
	if h.netqCatalogCommand(hubVersion) != nil {
		t.Fatal("an up-to-date agent should not keep receiving the same catalog")
	}
}

func TestNetQCatalogAutomaticFailureKeepsOldListAndWaits24Hours(t *testing.T) {
	h, st := newTestHub(t)
	old := `{"fetched_at":"2026-10-18T00:00:00Z","source":"old","targets":[{"province":"上海","isp":"ct","host":"1.1.1.1"}]}`
	if err := st.SetSetting(settingNetQCatalog, old); err != nil {
		t.Fatal(err)
	}
	beforeVersion := netprobe.CatalogVersion()
	var calls atomic.Int32
	original := http.DefaultTransport
	http.DefaultTransport = autoUpdateTransport(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{StatusCode: 503, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("unavailable"))}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = original })
	if _, err := h.refreshNetQCatalog(context.Background()); err == nil {
		t.Fatal("expected upstream failure")
	}
	if raw, _ := st.GetSetting(settingNetQCatalog); raw != old {
		t.Fatal("failure replaced the saved catalog")
	}
	if netprobe.CatalogVersion() != beforeVersion {
		t.Fatal("failure changed the active catalog")
	}
	if last, _ := st.GetSetting(settingNetQCatalogLastAttempt); last == "" {
		t.Fatal("failed attempt must also reserve a 24-hour interval")
	}
	// Restarting the scheduler after a failed fetch must not retry immediately.
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	h.netqCatalogRefreshLoop(ctx)
	if calls.Load() != 1 {
		t.Fatalf("automatic failure was immediately retried: %d calls", calls.Load())
	}
}
