// Package notify 是 Kokoro 的通知渠道层：把一条告警消息投递到 Telegram Bot 和/或 Webhook。
//
// 设计要点：
//   - 只用标准库。Telegram Bot API 本质就是一次 HTTPS POST，不需要任何 SDK；
//   - bot token 是凭据：任何日志、错误信息、HTTP 响应里都不得出现。出错只报 HTTP 状态码
//     和 Telegram 返回的 description，且统一经过 redact 脱敏；
//   - 免打扰时段在 Send 入口判断，命中直接跳过（返回 nil，写一条日志说明被抑制）；
//   - 多渠道时「任一成功即视为送达」，全部失败才返回错误。
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/kokoro-probe/kokoro/internal/store"
)

const (
	// settingKey 通知配置在 settings 表中的键（key 前缀 notify.）。
	settingKey = "notify.config"
	// timeout 单次投递超时。
	timeout = 10 * time.Second
	// telegramAPIBase Telegram Bot API 地址。
	telegramAPIBase = "https://api.telegram.org"
	// maxResp 读取上游响应体的上限，防止异常响应把内存吃光。
	maxResp = 1 << 20
)

// 消息级别。
const (
	LevelInfo     = "info"
	LevelWarning  = "warning"
	LevelCritical = "critical"
)

// 渠道名，用于 Message.Channels 过滤。
const (
	ChannelTelegram = "telegram"
	ChannelWebhook  = "webhook"
)

// Config 通知配置，持久化在 settings 表（key: notify.config，整体 JSON）。
type Config struct {
	TelegramEnabled  bool   `json:"telegram_enabled"`
	TelegramBotToken string `json:"telegram_bot_token"` // 不落日志、不返给前端
	TelegramChatID   string `json:"telegram_chat_id"`
	TelegramSilent   bool   `json:"telegram_silent"`
	WebhookEnabled   bool   `json:"webhook_enabled"`
	WebhookURL       string `json:"webhook_url"`
	QuietHoursStart  int    `json:"quiet_hours_start"` // 0-23，本地时区；-1 表示不设
	QuietHoursEnd    int    `json:"quiet_hours_end"`
}

// Message 一条待投递的通知。
type Message struct {
	Title   string
	Body    string
	Level   string // info | warning | critical
	NodeID  string
	NodeURL string
	// Channels 限定投递渠道（telegram / webhook）；为空表示全部已启用渠道。
	Channels []string
}

// Notifier 投递一条消息。
type Notifier interface {
	Send(ctx context.Context, m Message) error
}

// namedNotifier 给渠道带上名字，便于按 Message.Channels 过滤。
type namedNotifier struct {
	name string
	sub  Notifier
}

// multiNotifier 按顺序投递到多个渠道：任一成功即视为送达。
type multiNotifier struct {
	cfg  Config
	subs []namedNotifier
}

// New 按配置构造通知器。未启用任何渠道时返回一个「什么都不做」的通知器（Send 恒返回 nil）。
// 免打扰时段由 Send 内部判断。
func New(cfg Config) Notifier {
	m := &multiNotifier{cfg: cfg}
	if cfg.telegramOK() {
		m.subs = append(m.subs, namedNotifier{ChannelTelegram, &telegramSender{cfg: cfg, client: &http.Client{Timeout: timeout}}})
	}
	if cfg.webhookOK() {
		m.subs = append(m.subs, namedNotifier{ChannelWebhook, &webhookSender{cfg: cfg, client: &http.Client{Timeout: timeout}}})
	}
	return m
}

// Send 投递消息。命中免打扰时段返回 nil（并写日志）；多渠道时任一成功即返回 nil。
func (m *multiNotifier) Send(ctx context.Context, msg Message) error {
	if !ShouldNotify(time.Now(), m.cfg) {
		log.Printf("[notify] 免打扰时段 %02d:00-%02d:00，已抑制通知: %s",
			m.cfg.QuietHoursStart, m.cfg.QuietHoursEnd, msg.Title)
		return nil
	}
	subs := m.subs
	if len(msg.Channels) > 0 {
		subs = nil
		for _, s := range m.subs {
			for _, want := range msg.Channels {
				if strings.EqualFold(strings.TrimSpace(want), s.name) {
					subs = append(subs, s)
					break
				}
			}
		}
	}
	if len(subs) == 0 {
		return nil
	}

	var errs []error
	sent := false
	for _, s := range subs {
		if err := s.sub.Send(ctx, msg); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", s.name, err))
			continue
		}
		sent = true
	}
	if sent {
		return nil
	}
	return errors.Join(errs...)
}

// telegramOK 判断 Telegram 渠道是否可用（开关 + 凭据齐全）。
func (c Config) telegramOK() bool {
	return c.TelegramEnabled && c.TelegramBotToken != "" && c.TelegramChatID != ""
}

// webhookOK 判断 Webhook 渠道是否可用。
func (c Config) webhookOK() bool {
	return c.WebhookEnabled && strings.TrimSpace(c.WebhookURL) != ""
}

// ShouldNotify 判断给定时刻是否允许发送通知。
// 免打扰规则：
//   - 起止任一为 -1（或起止相等）视为未启用，恒返回 true；
//   - start < end：落在 [start, end) 内为免打扰；
//   - start > end：跨零点，落在 [start, 24) 或 [0, end) 为免打扰。
func ShouldNotify(now time.Time, cfg Config) bool {
	s, e := cfg.QuietHoursStart, cfg.QuietHoursEnd
	if s < 0 || e < 0 || s == e {
		return true
	}
	if s > 23 || e > 23 {
		return true
	}
	h := now.Hour()
	if s < e {
		return !(h >= s && h < e)
	}
	return !(h >= s || h < e)
}

// ==================== Telegram ====================

type telegramSender struct {
	cfg    Config
	client *http.Client
}

// telegramRequest 是 sendMessage 的请求体。
// disable_notification 对应「静音发送」，disable_web_page_preview 关掉链接预览让消息更紧凑。
type telegramRequest struct {
	ChatID                string `json:"chat_id"`
	Text                  string `json:"text"`
	ParseMode             string `json:"parse_mode"`
	DisableWebPagePreview bool   `json:"disable_web_page_preview"`
	DisableNotification   bool   `json:"disable_notification"`
}

type telegramResponse struct {
	OK          bool   `json:"ok"`
	ErrorCode   int    `json:"error_code"`
	Description string `json:"description"`
}

func (t *telegramSender) Send(ctx context.Context, m Message) error {
	return t.post(ctx, m, t.cfg.TelegramSilent)
}

// post 真正发起一次 sendMessage。所有错误都经 redact 脱敏，绝不回显 token。
func (t *telegramSender) post(ctx context.Context, m Message, silent bool) error {
	if t.cfg.TelegramBotToken == "" || t.cfg.TelegramChatID == "" {
		return errors.New("telegram 配置不完整：缺少 bot token 或 chat id")
	}
	body, err := json.Marshal(telegramRequest{
		ChatID:                t.cfg.TelegramChatID,
		Text:                  t.buildText(m),
		ParseMode:             "HTML",
		DisableWebPagePreview: true,
		DisableNotification:   silent,
	})
	if err != nil {
		return fmt.Errorf("序列化 telegram 请求失败: %w", err)
	}

	endpoint := telegramAPIBase + "/bot" + t.cfg.TelegramBotToken + "/sendMessage"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("构造 telegram 请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := t.client.Do(req)
	if err != nil {
		// url.Error 会把完整 URL（含 token）带进来，必须脱敏。
		return fmt.Errorf("telegram 请求失败: %s", redact(err.Error(), t.cfg.TelegramBotToken))
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxResp))
	var out telegramResponse
	_ = json.Unmarshal(raw, &out)
	if resp.StatusCode != http.StatusOK || !out.OK {
		desc := strings.TrimSpace(out.Description)
		if len(desc) > 300 {
			desc = desc[:300]
		}
		if desc == "" {
			return fmt.Errorf("telegram 返回 HTTP %d", resp.StatusCode)
		}
		return fmt.Errorf("telegram 返回 HTTP %d: %s", resp.StatusCode, desc)
	}
	return nil
}

// buildText 拼装 HTML 排版的消息正文。
// 标题加粗；正文含换行时用 <pre> 等宽排版（对齐的数值更好读）；末尾附节点链接。
func (t *telegramSender) buildText(m Message) string {
	var sb strings.Builder

	title := strings.TrimSpace(m.Title)
	if title == "" {
		title = "Kokoro 通知"
	}
	if !hasIconPrefix(title) {
		// 标题自带图标（告警引擎的文案已经带了）就不再重复。
		sb.WriteString(levelIcon(m.Level))
		sb.WriteString(" ")
	}
	sb.WriteString("<b>")
	sb.WriteString(escapeHTML(title))
	sb.WriteString("</b>")

	if body := strings.TrimSpace(m.Body); body != "" {
		sb.WriteString("\n")
		if strings.Contains(body, "\n") {
			sb.WriteString("<pre>")
			sb.WriteString(escapeHTML(body))
			sb.WriteString("</pre>")
		} else {
			sb.WriteString(escapeHTML(body))
		}
	}
	if m.NodeID != "" {
		sb.WriteString("\n<code>")
		sb.WriteString(escapeHTML(m.NodeID))
		sb.WriteString("</code>")
	}
	if u, ok := safeLink(m.NodeURL); ok {
		sb.WriteString("\n<a href=\"" + u + "\">查看节点</a>")
	}
	return sb.String()
}

// ==================== Webhook ====================

type webhookSender struct {
	cfg    Config
	client *http.Client
}

// webhookPayload 是通用 Webhook 的请求体。
type webhookPayload struct {
	Title  string `json:"title"`
	Body   string `json:"body"`
	Level  string `json:"level"`
	NodeID string `json:"node_id"`
	Ts     int64  `json:"ts"` // 毫秒时间戳，与项目其余部分一致
}

func (w *webhookSender) Send(ctx context.Context, m Message) error {
	u, ok := safeLink(w.cfg.WebhookURL)
	if !ok {
		return errors.New("webhook 地址无效（只接受 http/https）")
	}
	body, err := json.Marshal(webhookPayload{
		Title:  m.Title,
		Body:   m.Body,
		Level:  m.Level,
		NodeID: m.NodeID,
		Ts:     time.Now().UnixMilli(),
	})
	if err != nil {
		return fmt.Errorf("序列化 webhook 请求失败: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("构造 webhook 请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Kokoro/0.1")

	resp, err := w.client.Do(req)
	if err != nil {
		return fmt.Errorf("webhook 请求失败: %v", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResp))
	if resp.StatusCode >= 300 {
		return fmt.Errorf("webhook 返回 HTTP %d", resp.StatusCode)
	}
	return nil
}

// ==================== 测试发送 ====================

// TestTelegram 用给定配置发一条「Kokoro 测试消息」。
// 供后台"测试"按钮调用，因此不受免打扰时段限制。
func TestTelegram(ctx context.Context, cfg Config) error {
	if cfg.TelegramBotToken == "" || cfg.TelegramChatID == "" {
		return errors.New("telegram 未配置：缺少 bot token 或 chat id")
	}
	t := &telegramSender{cfg: cfg, client: &http.Client{Timeout: timeout}}
	return t.Send(ctx, Message{
		Title: "Kokoro 测试消息",
		Body:  "如果你看到这条消息，说明通知渠道配置正确。\n时间: " + time.Now().Format("2006-01-02 15:04:05"),
		Level: LevelInfo,
	})
}

// TestWebhook 用给定配置向 Webhook 发一条测试消息（不受免打扰限制）。
func TestWebhook(ctx context.Context, cfg Config) error {
	if !cfg.webhookOK() {
		return errors.New("webhook 未启用或地址为空")
	}
	w := &webhookSender{cfg: cfg, client: &http.Client{Timeout: timeout}}
	return w.Send(ctx, Message{
		Title: "Kokoro 测试消息",
		Body:  "如果你收到这条消息，说明通知渠道配置正确。",
		Level: LevelInfo,
	})
}

// Test 对全部已启用渠道发测试消息（不受免打扰限制）。
// 没有启用任何渠道时返回错误；多个渠道时返回合并后的错误。
func Test(ctx context.Context, cfg Config) error {
	var errs []error
	sent := false
	if cfg.telegramOK() {
		if err := TestTelegram(ctx, cfg); err != nil {
			errs = append(errs, fmt.Errorf("telegram: %w", err))
		} else {
			sent = true
		}
	}
	if cfg.webhookOK() {
		if err := TestWebhook(ctx, cfg); err != nil {
			errs = append(errs, fmt.Errorf("webhook: %w", err))
		} else {
			sent = true
		}
	}
	if !sent && len(errs) == 0 {
		return errors.New("没有启用任何通知渠道")
	}
	return errors.Join(errs...)
}

// ==================== 配置存取 ====================

// LoadConfig 从 settings 读取通知配置；未配置时返回零值配置。
func LoadConfig(st *store.Store) (Config, error) {
	var c Config
	if st == nil {
		return c, errors.New("notify: store 为空")
	}
	raw, err := st.GetSetting(settingKey)
	if err != nil {
		return c, fmt.Errorf("notify: 读取通知配置失败: %w", err)
	}
	if strings.TrimSpace(raw) == "" {
		return c, nil
	}
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		return Config{}, fmt.Errorf("notify: 解析通知配置失败: %w", err)
	}
	return c, nil
}

// SaveConfig 把通知配置整体写入 settings。
func SaveConfig(st *store.Store, c Config) error {
	if st == nil {
		return errors.New("notify: store 为空")
	}
	b, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("notify: 序列化通知配置失败: %w", err)
	}
	if err := st.SetSetting(settingKey, string(b)); err != nil {
		return fmt.Errorf("notify: 保存通知配置失败: %w", err)
	}
	return nil
}

// MaskToken 给 bot token 打码：只保留前后各 4 位，用于返回给前端。
func MaskToken(tok string) string {
	if tok == "" {
		return ""
	}
	if len(tok) <= 8 {
		return strings.Repeat("*", len(tok))
	}
	return tok[:4] + "****" + tok[len(tok)-4:]
}

// ==================== 内部工具 ====================

// levelIcon 按级别返回前缀图标。
func levelIcon(level string) string {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case LevelCritical:
		return "🚨"
	case LevelWarning:
		return "⚠️"
	default:
		return "✅"
	}
}

// hasIconPrefix 判断标题是否已经带级别图标，避免渲染出两个图标。
func hasIconPrefix(s string) bool {
	for _, p := range []string{"⚠", "✅", "🚨", "❌"} {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// escapeHTML 转义 Telegram HTML 模式下的特殊字符，否则会 400。
func escapeHTML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

// safeLink 校验外链：只接受 http/https，且不含会破坏 HTML 属性的字符。
func safeLink(raw string) (string, bool) {
	u := strings.TrimSpace(raw)
	if u == "" {
		return "", false
	}
	if strings.ContainsAny(u, "\"'<>") {
		return "", false
	}
	parsed, err := url.Parse(u)
	if err != nil || parsed.Host == "" {
		return "", false
	}
	switch parsed.Scheme {
	case "http", "https":
		return u, true
	}
	return "", false
}

// redact 把文本里出现的 bot token 抹掉，保证日志与错误信息不泄露凭据。
func redact(s, token string) string {
	if token == "" {
		return s
	}
	return strings.ReplaceAll(s, token, "***")
}
