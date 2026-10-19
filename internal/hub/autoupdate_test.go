package hub

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Vincentkeio/kokoro/internal/model"
	"github.com/Vincentkeio/kokoro/internal/panelupdate"
)

type autoUpdateTransport func(*http.Request) (*http.Response, error)

func (f autoUpdateTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestAutoUpdateOnlineOnceAndHubLastAcrossRestart(t *testing.T) {
	h, st := newTestHub(t)
	oldVersion := Version
	Version = "v1.0.0"
	t.Cleanup(func() { Version = oldVersion })
	var requests, hubLaunches atomic.Int32
	originalTransport := http.DefaultTransport
	http.DefaultTransport = autoUpdateTransport(func(r *http.Request) (*http.Response, error) {
		requests.Add(1)
		if r.URL.Host != "api.github.com" || !strings.HasSuffix(r.URL.Path, "/releases") {
			return nil, fmt.Errorf("unexpected request: %s", r.URL)
		}
		body := `[{"tag_name":"v9.9.1","published_at":"2026-10-19T12:00:00Z"},{"tag_name":"v1.0.0","published_at":"2026-10-18T12:00:00Z"}]`
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = originalTransport })
	online := mkNode(t, st, "online-auto", "JP", "", "")
	offline := mkNode(t, st, "offline-auto", "JP", "", "")
	stale := mkNode(t, st, "stale-auto", "JP", "", "")
	for _, n := range []*model.Node{online, offline, stale} {
		if err := st.UpdateAgentVersion(n.ID, Version); err != nil {
			t.Fatal(err)
		}
		if err := st.TouchNode(n.ID, time.Now().UnixMilli(), ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.SetNodeOnline(offline.ID, false); err != nil {
		t.Fatal(err)
	}
	if err := st.TouchNode(stale.ID, time.Now().Add(-time.Minute).UnixMilli(), ""); err != nil {
		t.Fatal(err)
	}
	launcher := func(s panelupdate.Spec) error {
		if s.Role != "hub" || s.Target != "v9.9.1" {
			t.Fatalf("unexpected launch: %+v", s)
		}
		hubLaunches.Add(1)
		return nil
	}
	h.updateLauncher = launcher
	if err := h.runAutoUpdateCheck(context.Background()); err != nil {
		t.Fatal(err)
	}
	if hubLaunches.Load() != 0 {
		t.Fatal("Hub must wait for agent jobs")
	}
	if h.updatePlan == nil || len(h.updatePlan.Nodes) != 1 || h.updatePlan.Nodes[0].ID != online.ID {
		t.Fatalf("only the online node should be queued: %+v", h.updatePlan)
	}
	if requests.Load() != 1 {
		t.Fatalf("one release listing should cover all versions, got %d requests", requests.Load())
	}
	last, _ := st.GetSetting(autoUpdateLastCheckKey)
	lastMS, _ := strconv.ParseInt(last, 10, 64)
	next, _ := st.GetSetting(autoUpdateNextCheckKey)
	nextMS, _ := strconv.ParseInt(next, 10, 64)
	if lastMS == 0 || nextMS != nextAutoUpdateCheck(time.UnixMilli(lastMS)).UnixMilli() {
		t.Fatalf("daily 01:00 check not persisted: last=%d, next=%d", lastMS, nextMS)
	}

	// Reconstructing the Hub uses only the saved plan and pending self-update.
	restarted, err := New(h.cfg, st)
	if err != nil {
		t.Fatal(err)
	}
	restarted.updateLauncher = launcher
	if !restarted.resumeAutoHubUpdate(context.Background()) || hubLaunches.Load() != 0 {
		t.Fatal("restart must preserve pending Hub update and keep waiting")
	}
	metrics := &model.Metrics{AgentVersion: Version, UpdateVersion: 1}
	command := restarted.updateCommand(online, metrics)
	if command == nil {
		t.Fatal("online agent did not receive its command")
	}
	// Simulate more than a minute passing; the previous updater resent here.
	restarted.updatePlan.Nodes[0].LastSent = time.Now().Add(-2 * time.Minute).UnixMilli()
	if err := restarted.saveUpdateLocked(); err != nil {
		t.Fatal(err)
	}
	reloaded, err := New(h.cfg, st)
	if err != nil {
		t.Fatal(err)
	}
	reloaded.updateLauncher = launcher
	if again := reloaded.updateCommand(online, metrics); again != nil {
		t.Fatal("a saved dispatch must never be resent in the same plan")
	}
	if reloaded.updateCommand(offline, metrics) != nil {
		t.Fatal("offline nodes must not be added when they reconnect")
	}
	if !reloaded.updateResult(online.ID, model.CommandResult{ID: command.ID, OK: false, Stderr: "simulated failure"}) {
		t.Fatal("failure was not recorded")
	}
	if again := reloaded.updateCommand(online, metrics); again != nil {
		t.Fatal("failed command was resent")
	}
	if reloaded.resumeAutoHubUpdate(context.Background()) || hubLaunches.Load() != 1 {
		t.Fatal("Hub should launch once after agent failure is terminal")
	}
	reloaded.resumeAutoHubUpdate(context.Background())
	if hubLaunches.Load() != 1 {
		t.Fatal("Hub update was submitted twice")
	}
	// The next daily check may create one fresh attempt for a still-online node.
	if err := st.SetSetting(autoUpdateLastCheckKey, strconv.FormatInt(time.Now().Add(-25*time.Hour).UnixMilli(), 10)); err != nil {
		t.Fatal(err)
	}
	if err := reloaded.runAutoUpdateCheck(context.Background()); err != nil {
		t.Fatal(err)
	}
	if next := reloaded.updateCommand(online, metrics); next == nil || next.ID == command.ID {
		t.Fatal("next daily check should allow one new attempt")
	}
}

func TestAutoUpdateTogglePersistsAndCancelsPendingHub(t *testing.T) {
	h, st := newTestHub(t)
	if !h.autoUpdateEnabled() {
		t.Fatal("automatic updates should default to enabled")
	}
	if err := st.SetSetting(autoUpdatePendingHubKey, `{"target":"v9.9.2","planned_at":1}`); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/admin/update", strings.NewReader("action=auto&enabled=0"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	h.handleAdminUpdate(response, request)
	if response.Code != 200 {
		t.Fatalf("toggle failed: %s", response.Body.String())
	}
	restarted, err := New(h.cfg, st)
	if err != nil {
		t.Fatal(err)
	}
	if restarted.autoUpdateEnabled() {
		t.Fatal("disabled choice was lost on restart")
	}
	if err := restarted.runAutoUpdateCheck(context.Background()); err != nil {
		t.Fatal(err)
	}
	if lastCheck, _ := st.GetSetting(autoUpdateLastCheckKey); lastCheck != "" {
		t.Fatal("a disabled scheduler performed an update check")
	}
	if pending, _ := st.GetSetting(autoUpdatePendingHubKey); pending != "" {
		t.Fatal("disabling must cancel a pending automatic Hub update")
	}
	invalid := httptest.NewRequest(http.MethodPost, "/admin/update", strings.NewReader("action=auto&enabled=maybe"))
	invalid.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response = httptest.NewRecorder()
	h.handleAdminUpdate(response, invalid)
	if response.Code != 400 || h.autoUpdateEnabled() {
		t.Fatal("invalid settings must not enable updates")
	}
	// The existing authenticated route also protects the new switch.
	response = httptest.NewRecorder()
	h.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/admin/update", strings.NewReader("action=auto&enabled=1")))
	if response.Code == 200 || h.autoUpdateEnabled() {
		t.Fatal("unauthenticated visitor changed the update setting")
	}
}

func TestPendingHubUpdateWaitsForAgentTimeout(t *testing.T) {
	h, st := newTestHub(t)
	now := time.Now()
	h.updateLoaded = true
	h.updatePlan = &agentUpdatePlan{ID: randomMigrationID(), Target: "v9.9.2", StartedAt: now.UnixMilli(), Nodes: []agentUpdateNode{{ID: "waiting-node", State: "waiting"}}}
	if err := h.saveUpdateLocked(); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting(autoUpdatePendingHubKey, fmt.Sprintf(`{"target":"v9.9.2","planned_at":%d}`, now.UnixMilli())); err != nil {
		t.Fatal(err)
	}
	launches := 0
	h.updateLauncher = func(panelupdate.Spec) error { launches++; return nil }
	if !h.resumeAutoHubUpdate(context.Background()) || launches != 0 {
		t.Fatal("Hub restarted while an agent command was pending")
	}
	h.updatePlan.StartedAt = now.Add(-21 * time.Minute).UnixMilli()
	if err := h.saveUpdateLocked(); err != nil {
		t.Fatal(err)
	}
	if h.resumeAutoHubUpdate(context.Background()) || launches != 1 {
		t.Fatal("agent timeout should release the pending Hub update")
	}
	h.resumeAutoHubUpdate(context.Background())
	if launches != 1 {
		t.Fatal("Hub update repeated after timeout")
	}
}

func TestPendingHubDoesNotRestartWhenAgentPlanCannotBeRead(t *testing.T) {
	h, st := newTestHub(t)
	if err := st.SetSetting(agentUpdateKey, "broken-json"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting(autoUpdatePendingHubKey, fmt.Sprintf(`{"target":"v9.9.2","planned_at":%d}`, time.Now().UnixMilli())); err != nil {
		t.Fatal(err)
	}
	h.updateLauncher = func(panelupdate.Spec) error {
		t.Fatal("Hub restarted without knowing whether agent jobs were complete")
		return nil
	}
	if !h.resumeAutoHubUpdate(context.Background()) {
		t.Fatal("unreadable plan should keep Hub update pending")
	}
}

func TestAutoUpdateDailyScheduleUsesBeijingOneAM(t *testing.T) {
	for _, tc := range []struct {
		name string
		now  string
		want string
	}{
		{"startup in evening waits until 01:00", "2026-10-19T20:30:00+08:00", "2026-10-20T01:00:00+08:00"},
		{"before 01:00", "2026-10-19T00:59:00+08:00", "2026-10-19T01:00:00+08:00"},
		{"at 01:00 next attempt is tomorrow", "2026-10-19T01:00:00+08:00", "2026-10-20T01:00:00+08:00"},
		{"restart after missed schedule", "2026-10-19T01:01:00+08:00", "2026-10-20T01:00:00+08:00"},
		{"host timezone does not change schedule", "2026-10-19T05:30:00-07:00", "2026-10-20T01:00:00+08:00"},
	} {
		now, _ := time.Parse(time.RFC3339, tc.now)
		want, _ := time.Parse(time.RFC3339, tc.want)
		if got := nextAutoUpdateCheck(now); !got.Equal(want) {
			t.Errorf("%s: got %v, want %v", tc.name, got, want)
		}
	}
}
