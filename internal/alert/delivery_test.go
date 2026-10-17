package alert

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Vincentkeio/kokoro/internal/model"
	"github.com/Vincentkeio/kokoro/internal/notify"
	"github.com/Vincentkeio/kokoro/internal/store"
)

type deliveryTransport func(*http.Request) (*http.Response, error)

func (f deliveryTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestDeliveryTriggerRecoveryAndNotificationChannels(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "alerts.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var mu sync.Mutex
	telegram, webhook := 0, 0
	rejectWebhook := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if rejectWebhook {
			w.WriteHeader(500)
			return
		}
		var p map[string]any
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			t.Error(err)
		}
		if r.Method != "POST" || r.Header.Get("Content-Type") != "application/json" || p["title"] == nil || p["node_id"] == nil || p["ts"] == nil {
			t.Error("invalid webhook payload", p)
		}
		webhook++
		w.WriteHeader(204)
	}))
	defer server.Close()
	original := http.DefaultTransport
	http.DefaultTransport = deliveryTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "api.telegram.org" {
			return original.RoundTrip(r)
		}
		var p map[string]any
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			t.Error(err)
		}
		if !strings.HasSuffix(r.URL.Path, "/sendMessage") || p["chat_id"] != "test-chat" || p["text"] == nil {
			t.Error("invalid Telegram request")
		}
		mu.Lock()
		telegram++
		mu.Unlock()
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{"message_id":1}}`))}, nil
	})
	defer func() { http.DefaultTransport = original }()
	cfg := notify.Config{TelegramEnabled: true, TelegramBotToken: "test-token", TelegramChatID: "test-chat", WebhookEnabled: true, WebhookURL: server.URL, QuietHoursStart: -1, QuietHoursEnd: -1}
	if err := notify.SaveConfig(st, cfg); err != nil {
		t.Fatal(err)
	}
	n := &model.Node{Name: "test node", TokenHash: "test-delivery", Online: true, LastSeen: time.Now().UnixMilli()}
	if err := st.CreateNode(n); err != nil {
		t.Fatal(err)
	}
	ts := time.Now().UnixMilli()
	put := func(cpu float64) {
		ts++
		if err := st.InsertMetrics(n.ID, &model.Metrics{Ts: ts, CPU: model.CPUStat{Usage: cpu}}); err != nil {
			t.Fatal(err)
		}
	}
	put(95)
	rule := &model.AlertRule{NodeID: n.ID, Metric: "cpu", Op: "gt", Threshold: 80, Duration: 60}
	if err := st.SaveAlertRule(rule); err != nil {
		t.Fatal(err)
	}
	m := New(st, "https://panel.example")
	ctx := context.Background()
	m.check(ctx)
	if telegram != 0 || webhook != 0 {
		t.Fatal("fired before duration")
	}
	m.states[rule.ID+"|"+n.ID].since = time.Now().Add(-61 * time.Second)
	m.check(ctx)
	if telegram != 1 || webhook != 1 {
		t.Fatalf("trigger not delivered: tg=%d webhook=%d", telegram, webhook)
	}
	m.check(ctx)
	// Restart recovers firing state and must not immediately send a duplicate.
	m = New(st, "https://panel.example")
	m.check(ctx)
	if telegram != 1 || webhook != 1 {
		t.Fatal("duplicate alert within cooldown")
	}
	put(20)
	m.check(ctx)
	if telegram != 2 || webhook != 2 {
		t.Fatalf("recovery not delivered: tg=%d webhook=%d", telegram, webhook)
	}
	var total, resolved int
	if err := st.DB().QueryRow("select count(*),sum(case when resolved>0 then 1 else 0 end) from alert_events").Scan(&total, &resolved); err != nil {
		t.Fatal(err)
	}
	if total != 1 || resolved != 1 {
		t.Fatal("event did not resolve", total, resolved)
	}
	// Explicit tests bypass quiet hours, while ordinary notifications honor them.
	cfg.QuietHoursStart = time.Now().Hour()
	cfg.QuietHoursEnd = (cfg.QuietHoursStart + 1) % 24
	if err := notify.New(cfg).Send(ctx, notify.Message{Title: "quiet"}); err != nil {
		t.Fatal(err)
	}
	if telegram != 2 || webhook != 2 {
		t.Fatal("quiet hours ignored")
	}
	if err := notify.Test(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if telegram != 3 || webhook != 3 {
		t.Fatal("test button channels not connected")
	}
	mu.Lock()
	rejectWebhook = true
	mu.Unlock()
	if err := notify.TestWebhook(ctx, cfg); err == nil {
		t.Fatal("HTTP 500 incorrectly reported success")
	}
}
