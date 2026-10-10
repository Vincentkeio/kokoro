package store

// 购买意向：访客点「我想买它」后留下的联系方式。
//
// ⚠️ **先落库，再推送**。顺序不能反：
// 免打扰时段（比如凌晨）推送会被抑制，如果先推送再落库，那条线索就**没了**。
// 落库之后即使推送失败或没到点，站长第二天在后台还能看到。

import (
	"fmt"
	"time"
)

// BuyIntent 是一条购买意向。
type BuyIntent struct {
	ID        string
	NodeID    string
	Contact   string
	Note      string
	IPHash    string
	CreatedAt int64
	Notified  bool
}

// AddBuyIntent 存一条购买意向。返回新建的 id。
func (s *Store) AddBuyIntent(b *BuyIntent) error {
	if b == nil {
		return fmt.Errorf("购买意向为空")
	}
	if b.ID == "" {
		b.ID = newID("bi_")
	}
	if b.CreatedAt == 0 {
		b.CreatedAt = time.Now().UnixMilli()
	}
	_, err := s.db.Exec(`INSERT INTO buy_intents
(id, node_id, contact, note, ip_hash, created_at, notified)
VALUES (?,?,?,?,?,?,0)`, b.ID, b.NodeID, b.Contact, b.Note, b.IPHash, b.CreatedAt)
	if err != nil {
		return fmt.Errorf("保存购买意向失败: %w", err)
	}
	return nil
}

// MarkBuyNotified 标记这条已经推送给站长了。
func (s *Store) MarkBuyNotified(id string) {
	_, _ = s.db.Exec(`UPDATE buy_intents SET notified = 1 WHERE id = ?`, id)
}

// CountBuyRecent 统计某个来源（IP 哈希）最近 since 毫秒内留了几条 —— 用来挡刷。
//
// 这是个**公开**接口，不挡的话一个人能把站长的 TG 刷爆。
func (s *Store) CountBuyRecent(ipHash string, since int64) int {
	if ipHash == "" || since <= 0 {
		return 0
	}
	var n int
	_ = s.db.QueryRow(
		`SELECT COUNT(*) FROM buy_intents WHERE ip_hash = ? AND created_at >= ?`,
		ipHash, since).Scan(&n)
	return n
}

// ListBuyIntents 给后台看最近若干条购买意向。
func (s *Store) ListBuyIntents(limit int) ([]BuyIntent, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(`SELECT id, node_id, contact, note, ip_hash, created_at, notified
FROM buy_intents ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BuyIntent
	for rows.Next() {
		var b BuyIntent
		var notif int
		if err := rows.Scan(&b.ID, &b.NodeID, &b.Contact, &b.Note, &b.IPHash, &b.CreatedAt, &notif); err != nil {
			return nil, err
		}
		b.Notified = notif != 0
		out = append(out, b)
	}
	return out, rows.Err()
}
