package hub

// 「我想买它」—— 访客留联系方式，站长通过已配置的通知渠道收到提醒。
//
// 这是一个**公开**接口（访客没登录），所以三件事必须做：
//
//  1. **先落库，再推送**。免打扰时段推送会被抑制；如果先推送，凌晨留下的线索
//     会彻底消失。落库之后即使推送没成，站长第二天在后台还能看到。
//  2. **限流**。不挡的话一个人能把站长的 TG 刷爆。
//  3. **只存 IP 的哈希**。评论那里就是这么做的，保持一致 —— 不存访客的原 IP。

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Vincentkeio/kokoro/internal/model"
	"github.com/Vincentkeio/kokoro/internal/notify"
	"github.com/Vincentkeio/kokoro/internal/store"
)

const (
	// buyMaxPerHour 同一个来源一小时最多留几条。
	buyMaxPerHour  = 3
	perNodeSendMax = 2
	perNodeWindow  = 5 * time.Minute
	// 联系方式与留言的字数上限。
	buyContactMax = 20
	buyNoteMax    = 100
)

// handleBuy 处理 POST /api/v1/buy。
func (h *Hub) handleBuy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "只接受 POST")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	if err := r.ParseForm(); err != nil {
		writeErr(w, http.StatusBadRequest, "内容太大")
		return
	}

	slug := strings.TrimSpace(r.FormValue("slug"))
	contact := strings.TrimSpace(r.FormValue("contact"))
	note := strings.TrimSpace(r.FormValue("note"))

	if slug == "" {
		writeErr(w, http.StatusBadRequest, "缺少机器")
		return
	}
	if contact == "" {
		writeErr(w, http.StatusBadRequest, "请填一下联系方式")
		return
	}
	if len([]rune(contact)) > buyContactMax {
		writeErr(w, http.StatusBadRequest,
			fmt.Sprintf("联系方式最多 %d 个字", buyContactMax))
		return
	}
	if len([]rune(note)) > buyNoteMax {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("备注最多 %d 个字", buyNoteMax))
		return
	}

	n, err := h.store.GetNodeBySlug(slug)
	if err != nil || n == nil {
		writeErr(w, http.StatusNotFound, "找不到这台机器")
		return
	}
	// 私有机器不该被联系
	if n.Visibility == model.VisibilityPrivate {
		writeErr(w, http.StatusNotFound, "找不到这台机器")
		return
	}

	// ---- 限流 ----
	ipHash := h.rateLimitIPHash(r)

	// ---- ① 先落库 ----
	bi := &store.BuyIntent{
		NodeID:  n.ID,
		Contact: contact,
		Note:    note,
		IPHash:  ipHash,
	}
	buyBody := "联系方式：" + contact
	if note != "" {
		buyBody += "\n备注：" + note
	}
	backup := &store.SiteMessage{
		Kind: "buy", NodeID: n.ID, NodeName: n.Name,
		Title: "有人想购买", Body: buyBody, Delivery: "站内已保存",
	}
	allowed, err := h.store.AddBuyIntentWithRateLimits(bi,
		time.Now().Add(-time.Hour).UnixMilli(), buyMaxPerHour,
		time.Now().Add(-perNodeWindow).UnixMilli(), perNodeSendMax, backup)
	if err != nil {
		log.Printf("[hub] 保存购买意向失败: %v", err)
		writeErr(w, http.StatusInternalServerError, "购买意向暂时未能保存，你可以去评论区留言联系站长")
		return
	}
	if !allowed {
		writeErr(w, http.StatusTooManyRequests, "发送过于频繁：同一 IP 每台小鸡 5 分钟最多 2 条，所有小鸡合计每小时最多 3 条，请稍后再试")
		return
	}
	buyMessageKey := "buy:" + bi.ID
	_ = h.store.AddAudit("visitor", "buy.intent", n.ID, contact)

	// ---- ② 分渠道推送，逐项告诉访客实际送达情况 ----
	delivery := map[string]string{
		notify.ChannelTelegram: "unconfigured",
		notify.ChannelWebhook:  "unconfigured",
	}
	if h.alerts != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
		defer cancel()
		body := "联系方式：" + contact
		if n.Region != "" {
			body = "地区：" + n.Region + "\n" + body
		}
		if note != "" {
			body += "\n备注：" + note
		}
		body += "\n提交时间：" + time.Now().Format("2006-01-02 15:04:05")
		nodeURL := ""
		if h.cfg != nil && strings.TrimSpace(h.cfg.Domain) != "" {
			slug := n.Slug
			if slug == "" {
				slug = n.ID
			}
			nodeURL = strings.TrimRight(h.siteBaseURL(r), "/") + "/n/" + url.PathEscape(slug)
		}
		statuses, err := h.alerts.NotifyChannels(ctx, notify.Message{
			Title:    "有人想购买",
			Body:     body,
			Level:    notify.LevelInfo,
			NodeID:   n.ID,
			NodeName: n.Name,
			NodeURL:  nodeURL,
		})
		for channel, status := range statuses {
			delivery[channel] = status
		}
		if err != nil && (delivery[notify.ChannelTelegram] == "failed" || delivery[notify.ChannelWebhook] == "failed") {
			// Webhook URL 本身可能含密钥，不把底层网络错误原文写入日志。
			log.Printf("[hub] 购买意向推送失败 Telegram=%s Webhook=%s", delivery[notify.ChannelTelegram], delivery[notify.ChannelWebhook])
		}
		if delivery[notify.ChannelTelegram] == "sent" || delivery[notify.ChannelWebhook] == "sent" {
			h.store.MarkBuyNotified(bi.ID)
		}
	}
	_ = h.store.UpdateSiteMessageDelivery(buyMessageKey, siteDeliverySummary(delivery))

	notifyStatus := "failed"
	if delivery[notify.ChannelTelegram] == "sent" || delivery[notify.ChannelWebhook] == "sent" {
		notifyStatus = "sent"
	} else if delivery[notify.ChannelTelegram] == "unconfigured" && delivery[notify.ChannelWebhook] == "unconfigured" {
		notifyStatus = "unconfigured"
	} else if delivery[notify.ChannelTelegram] == "quiet_hours" || delivery[notify.ChannelWebhook] == "quiet_hours" {
		notifyStatus = "quiet_hours"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "notify_status": notifyStatus,
		"telegram_status": delivery[notify.ChannelTelegram],
		"webhook_status":  delivery[notify.ChannelWebhook],
	})
}
