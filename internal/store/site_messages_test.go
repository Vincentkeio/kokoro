package store

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Vincentkeio/kokoro/internal/model"
)

func TestBuyQuotaConcurrentAndBackupAtomic(t *testing.T) {
	s := openTmp(t)
	n := &model.Node{Name: "test", TokenHash: "quota-test"}
	if err := s.CreateNode(n); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var added atomic.Int32
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := s.AddBuyIntentWithRateLimits(&BuyIntent{NodeID: n.ID, IPHash: "same-ip"}, time.Now().Add(-time.Hour).UnixMilli(), 3, time.Now().Add(-5*time.Minute).UnixMilli(), 2, &SiteMessage{Kind: "buy", Title: "buy"})
			if err != nil {
				t.Error(err)
			}
			if ok {
				added.Add(1)
			}
		}()
	}
	wg.Wait()
	if added.Load() != 2 {
		t.Fatalf("quota admitted %d instead of 2", added.Load())
	}
	if count, _ := s.CountSiteMessages(); count != 2 {
		t.Fatalf("backup count=%d", count)
	}
	// An invalid backup must roll back the purchase as well, so a successful
	// public response always has an inbox item available to the administrator.
	_, err := s.AddBuyIntentWithRateLimits(&BuyIntent{NodeID: n.ID, IPHash: "other-ip"}, 1, 3, 1, 2, &SiteMessage{})
	if err == nil {
		t.Fatal("invalid backup unexpectedly committed")
	}
	if got := s.CountBuyRecent("other-ip", 1); got != 0 {
		t.Fatal("purchase was committed without its backup")
	}
}

func TestInboxPaginationReadAndDelete(t *testing.T) {
	s := openTmp(t)
	for i := 0; i < 21; i++ {
		if _, err := s.AddSiteMessage(&SiteMessage{DedupeKey: fmt.Sprint(i), Kind: "buy", Title: "buy", CreatedAt: 100}); err != nil {
			t.Fatal(err)
		}
	}
	first, _ := s.ListSiteMessages(10, 0)
	second, _ := s.ListSiteMessages(10, 10)
	last, _ := s.ListSiteMessages(10, 20)
	if len(first) != 10 || len(second) != 10 || len(last) != 1 {
		t.Fatal("incorrect page sizes")
	}
	seen := map[string]bool{}
	for _, m := range append(append(first, second...), last...) {
		if seen[m.ID] {
			t.Fatal("duplicate across pages")
		}
		seen[m.ID] = true
	}
	if err := s.MarkSiteMessageRead(first[0].ID); err != nil {
		t.Fatal(err)
	}
	if unread, _ := s.CountUnreadSiteMessages(); unread != 20 {
		t.Fatal("single read count", unread)
	}
	if err := s.MarkAllSiteMessagesRead(); err != nil {
		t.Fatal(err)
	}
	if unread, _ := s.CountUnreadSiteMessages(); unread != 0 {
		t.Fatal("read all count", unread)
	}
	if count, err := s.DeleteSiteMessages([]string{first[0].ID, first[0].ID, second[0].ID}); err != nil || count != 2 {
		t.Fatal("delete count", count, err)
	}
}

func TestExpiryReceiptMigrationPreservesDelivery(t *testing.T) {
	s := openTmp(t)
	m := &SiteMessage{DedupeKey: "expiry:node:2026-10-25", Kind: "expiry", Title: "expiry", Delivery: "站内已保存 · Telegram 已发送 · Webhook 发送失败"}
	if _, err := s.AddSiteMessage(m); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`DROP TABLE expiry_reminder_deliveries; PRAGMA user_version=22`); err != nil {
		t.Fatal(err)
	}
	if err := s.migrate(); err != nil {
		t.Fatal(err)
	}
	state, err := s.EnsureExpiryReminder(m)
	if err != nil || state.Telegram != "sent" || state.Webhook == "sent" {
		t.Fatalf("migration lost receipt: %+v %v", state, err)
	}
	if _, err := s.DeleteSiteMessages([]string{m.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnsureExpiryReminder(m); err != nil {
		t.Fatal(err)
	}
	if count, _ := s.CountSiteMessages(); count != 0 {
		t.Fatal("receipt recreated a deleted backup")
	}
}
