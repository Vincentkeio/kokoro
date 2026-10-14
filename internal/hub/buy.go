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
	"strings"
	"time"

	"github.com/Vincentkeio/kokoro/internal/model"
	"github.com/Vincentkeio/kokoro/internal/notify"
	"github.com/Vincentkeio/kokoro/internal/store"
)

const (
	// buyMaxPerHour 同一个来源一小时最多留几条。
	buyMaxPerHour = 3
	// buyContactMax 联系方式的字数上限。
	// 挡住"把整篇文章粘进来"这种——那既不是联系方式，也会让 TG 消息没法看。
	buyContactMax = 120
	// buyNoteMax 备注上限。
	buyNoteMax = 200
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
	ipHash := h.visitorHash(r)
	if ipHash != "" && h.store.CountBuyRecent(ipHash, time.Now().Add(-time.Hour).UnixMilli()) >= buyMaxPerHour {
		writeErr(w, http.StatusTooManyRequests, "留得太频繁了，请稍后再试")
		return
	}

	// ---- ① 先落库 ----
	bi := &store.BuyIntent{
		NodeID:  n.ID,
		Contact: contact,
		Note:    note,
		IPHash:  ipHash,
	}
	if err := h.store.AddBuyIntent(bi); err != nil {
		log.Printf("[hub] 保存购买意向失败: %v", err)
		writeErr(w, http.StatusInternalServerError, "购买意向暂时未能保存，你可以去评论区留言联系站长")
		return
	}
	_ = h.store.AddAudit("visitor", "buy.intent", n.ID, contact)

	// ---- ② 分渠道推送，逐项告诉访客实际送达情况 ----
	delivery := map[string]string{
		notify.ChannelTelegram: "unconfigured",
		notify.ChannelWebhook:  "unconfigured",
	}
	if h.alerts != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
		defer cancel()
		body := "有人想买这台机器：\n" + n.Name
		if n.Region != "" {
			body += "（" + n.Region + "）"
		}
		body += "\n\n联系方式：" + contact
		if note != "" {
			body += "\n备注：" + note
		}
		body += "\n\n时间：" + time.Now().Format("2006-01-02 15:04:05")
		statuses, err := h.alerts.NotifyChannels(ctx, notify.Message{
			Title:  "💰 有人想买它 / " + n.Name,
			Body:   body,
			Level:  notify.LevelInfo,
			NodeID: n.ID,
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
