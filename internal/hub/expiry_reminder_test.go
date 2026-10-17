package hub

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Vincentkeio/kokoro/internal/model"
	"github.com/Vincentkeio/kokoro/internal/notify"
	"github.com/Vincentkeio/kokoro/internal/store"
)

type reminderTransport func(*http.Request) (*http.Response, error)

func (f reminderTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestExpiryReminderRetriesOnlyUndeliveredChannelsAndSurvivesDeletion(t *testing.T) {
	h, st := newTestHub(t)
	n := mkNode(t, st, "expiry", "JP", "Tokyo", "")
	if err := st.SaveProfile(&model.NodeProfile{NodeID: n.ID, ExpireAt: time.Now().AddDate(0, 0, 7).Format("2006-01-02")}); err != nil {
		t.Fatal(err)
	}
	st.SetSetting(settingExpiryReminderOn, "1")
	var telegram, webhooks atomic.Int32
	var reject atomic.Bool
	reject.Store(true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		webhooks.Add(1)
		if reject.Load() {
			w.WriteHeader(500)
		} else {
			w.WriteHeader(204)
		}
	}))
	defer server.Close()
	original := http.DefaultTransport
	http.DefaultTransport = reminderTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "api.telegram.org" {
			return original.RoundTrip(r)
		}
		telegram.Add(1)
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{"message_id":1}}`))}, nil
	})
	defer func() { http.DefaultTransport = original }()
	cfg := notify.Config{TelegramEnabled: true, TelegramBotToken: "test", TelegramChatID: "test", WebhookEnabled: true, WebhookURL: server.URL, QuietHoursStart: time.Now().Hour(), QuietHoursEnd: (time.Now().Hour() + 1) % 24}
	if err := notify.SaveConfig(st, cfg); err != nil {
		t.Fatal(err)
	}
	h.checkExpiryReminders(context.Background())
	if telegram.Load() != 0 || webhooks.Load() != 0 {
		t.Fatal("quiet hours must defer external delivery")
	}
	cfg.QuietHoursStart, cfg.QuietHoursEnd = -1, -1
	if err := notify.SaveConfig(st, cfg); err != nil {
		t.Fatal(err)
	}
	h.checkExpiryReminders(context.Background())
	if telegram.Load() != 1 || webhooks.Load() != 1 {
		t.Fatalf("first attempt: telegram=%d webhook=%d", telegram.Load(), webhooks.Load())
	}
	reject.Store(false)
	h.checkExpiryReminders(context.Background())
	if telegram.Load() != 1 || webhooks.Load() != 2 {
		t.Fatal("retry must only send the failed channel")
	}
	items, err := st.ListSiteMessages(10, 0)
	if err != nil || len(items) != 1 || !strings.Contains(items[0].Delivery, "Webhook 已发送") {
		t.Fatalf("backup: %+v %v", items, err)
	}
	if _, err := st.DeleteSiteMessages([]string{items[0].ID}); err != nil {
		t.Fatal(err)
	}
	restarted, err := New(h.cfg, st)
	if err != nil {
		t.Fatal(err)
	}
	restarted.checkExpiryReminders(context.Background())
	if telegram.Load() != 1 || webhooks.Load() != 2 {
		t.Fatal("restart or deleting the inbox must not resend")
	}
	if count, _ := st.CountSiteMessages(); count != 0 {
		t.Fatal("deleted backup was recreated")
	}
}

func TestExpiryDaysLeftUsesCalendarDatesAcrossDST(t *testing.T) {
	zone, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	original := time.Local
	time.Local = zone
	defer func() { time.Local = original }()
	for _, tc := range []struct {
		today, expiry string
		days          int
	}{
		{"2026-03-08", "2026-03-09", 1},
		{"2026-11-01", "2026-11-02", 1},
		{"2026-03-09", "2026-03-08", -1},
	} {
		now, _ := time.ParseInLocation("2006-01-02", tc.today, zone)
		got, ok := expiryDaysLeft(tc.expiry, now.Add(12*time.Hour))
		if !ok || got != tc.days {
			t.Errorf("%s to %s: %d want %d", tc.today, tc.expiry, got, tc.days)
		}
	}
}

func TestExpiryDeferredDeliveryCanFinishAfterDeadline(t *testing.T) {
	h, st := newTestHub(t)
	expiry := time.Now().AddDate(0, 0, -1).Format("2006-01-02")
	deferred := mkNode(t, st, "deferred", "JP", "Tokyo", "")
	old := mkNode(t, st, "already expired", "US", "LA", "")
	for _, node := range []*model.Node{deferred, old} {
		if err := st.SaveProfile(&model.NodeProfile{NodeID: node.ID, ExpireAt: expiry}); err != nil {
			t.Fatal(err)
		}
	}
	st.SetSetting(settingExpiryReminderOn, "1")
	key := "expiry:" + deferred.ID + ":" + expiry
	if _, err := st.EnsureExpiryReminder(&store.SiteMessage{DedupeKey: key, Kind: "expiry", Title: "expiry"}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateExpiryReminderDelivery(key, store.ExpiryReminderDelivery{Webhook: "quiet_hours"}, "deferred"); err != nil {
		t.Fatal(err)
	}
	var sent atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { sent.Add(1); w.WriteHeader(204) }))
	defer server.Close()
	if err := notify.SaveConfig(st, notify.Config{WebhookEnabled: true, WebhookURL: server.URL, QuietHoursStart: -1, QuietHoursEnd: -1}); err != nil {
		t.Fatal(err)
	}
	h.checkExpiryReminders(context.Background())
	h.checkExpiryReminders(context.Background())
	if sent.Load() != 1 {
		t.Fatalf("deferred reminder count=%d", sent.Load())
	}
	if count, _ := st.CountSiteMessages(); count != 1 {
		t.Fatal("created a new reminder for a previously expired node")
	}
}
