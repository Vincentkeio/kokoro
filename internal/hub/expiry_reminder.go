package hub

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/Vincentkeio/kokoro/internal/notify"
	"github.com/Vincentkeio/kokoro/internal/store"
)

const (
	settingExpiryReminderOn  = "expiry_reminder_enabled"
	settingExpiryReminderDay = "expiry_reminder_days"
	defaultExpiryReminderDay = 7
	maxExpiryReminderDay     = 365
)

func (h *Hub) expiryReminderSettings() (bool, int) {
	on, _ := h.store.GetSetting(settingExpiryReminderOn)
	raw, _ := h.store.GetSetting(settingExpiryReminderDay)
	days, err := strconv.Atoi(raw)
	if err != nil || days < 1 || days > maxExpiryReminderDay {
		days = defaultExpiryReminderDay
	}
	return on == "1", days
}

func (h *Hub) startExpiryReminderLoop(ctx context.Context) {
	go func() {
		h.checkExpiryReminders(ctx)
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				h.checkExpiryReminders(ctx)
			}
		}
	}()
}

func (h *Hub) checkExpiryReminders(ctx context.Context) {
	// A save and the hourly timer may overlap. Drop overlapping scans rather than
	// queueing goroutines or delivering the same pending channel twice.
	if !h.expiryReminderMu.TryLock() {
		return
	}
	defer h.expiryReminderMu.Unlock()
	enabled, reminderDays := h.expiryReminderSettings()
	if !enabled {
		return
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		select {
		case <-h.done:
			cancel()
		case <-ctx.Done():
		}
	}()
	nodes, err := h.store.ListExpiringNodes()
	if err != nil {
		log.Printf("[hub] 到期提醒读取节点失败: %v", err)
		return
	}
	now := time.Now()
	for _, node := range nodes {
		if ctx.Err() != nil {
			return
		}
		daysLeft, ok := expiryDaysLeft(node.ExpireAt, now)
		if !ok || daysLeft > reminderDays {
			continue
		}
		expiry := expireDateValue(node.ExpireAt)
		key := "expiry:" + node.ID + ":" + expiry
		needsRetry := func(status string) bool { return status == "failed" || status == "quiet_hours" }
		if daysLeft < 0 {
			previous, err := h.store.GetExpiryReminderDelivery(key)
			if err != nil || (!needsRetry(previous.Telegram) && !needsRetry(previous.Webhook)) {
				continue
			}
		}
		remaining := "还有 " + strconv.Itoa(daysLeft) + " 天"
		if daysLeft == 0 {
			remaining = "今天到期"
		}
		if daysLeft < 0 {
			remaining = "已过期 " + strconv.Itoa(-daysLeft) + " 天"
		}
		msg := &store.SiteMessage{
			DedupeKey: key,
			Kind:      "expiry",
			NodeID:    node.ID,
			NodeName:  node.Name,
			Title:     "小鸡到期提醒",
			Body:      fmt.Sprintf("到期日：%s\n%s", expiry, remaining),
			Delivery:  "已保存到站内消息",
		}
		state, err := h.store.EnsureExpiryReminder(msg)
		if err != nil {
			log.Printf("[hub] 保存小鸡到期提醒失败: node=%s err=%v", node.ID, err)
			continue
		}
		if h.alerts == nil {
			continue
		}
		var pending []string
		if state.Telegram != "sent" && (daysLeft >= 0 || needsRetry(state.Telegram)) {
			pending = append(pending, notify.ChannelTelegram)
		}
		if state.Webhook != "sent" && (daysLeft >= 0 || needsRetry(state.Webhook)) {
			pending = append(pending, notify.ChannelWebhook)
		}
		if len(pending) == 0 {
			continue
		}
		sendCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
		delivery, sendErr := h.alerts.NotifyChannels(sendCtx, notify.Message{
			Title:    msg.Title,
			Body:     fmt.Sprintf("小鸡：%s\n到期日：%s\n%s", node.Name, expiry, remaining),
			Level:    notify.LevelWarning,
			NodeID:   node.ID,
			NodeName: node.Name,
			Channels: pending,
		})
		cancel()
		if state.Telegram == "sent" {
			delivery[notify.ChannelTelegram] = "sent"
		}
		if state.Webhook == "sent" {
			delivery[notify.ChannelWebhook] = "sent"
		}
		if sendErr != nil && (delivery[notify.ChannelTelegram] == "failed" || delivery[notify.ChannelWebhook] == "failed") {
			log.Printf("[hub] 小鸡到期外部通知失败 Telegram=%s Webhook=%s", delivery[notify.ChannelTelegram], delivery[notify.ChannelWebhook])
		}
		state.Telegram, state.Webhook = delivery[notify.ChannelTelegram], delivery[notify.ChannelWebhook]
		if err := h.store.UpdateExpiryReminderDelivery(key, state, siteDeliverySummary(delivery)); err != nil {
			log.Printf("[hub] 保存到期提醒投递状态失败: node=%s err=%v", node.ID, err)
		}
	}
}

func expiryDaysLeft(raw string, now time.Time) (int, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == expireForever {
		return 0, false
	}
	if i := strings.IndexAny(raw, "T "); i > 0 {
		raw = raw[:i]
	}
	date, err := time.ParseInLocation("2006-01-02", raw, time.Local)
	if err != nil {
		return 0, false
	}
	localNow := now.In(time.Local)
	// Compare calendar dates in UTC so DST's 23/25-hour days cannot change the
	// number of remaining days. The chosen calendar day still follows local time.
	today := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), 0, 0, 0, 0, time.UTC)
	date = time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, time.UTC)
	return int(date.Sub(today).Hours() / 24), true
}

func siteDeliverySummary(statuses map[string]string) string {
	label := func(status string) string {
		switch status {
		case "sent":
			return "已发送"
		case "quiet_hours":
			return "免打扰时段"
		case "failed":
			return "发送失败"
		default:
			return "未配置"
		}
	}
	return "站内已保存 · Telegram " + label(statuses[notify.ChannelTelegram]) + " · Webhook " + label(statuses[notify.ChannelWebhook])
}
