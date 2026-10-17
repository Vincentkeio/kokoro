package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// SiteMessage is a persisted notice shown in the administrator inbox.
type SiteMessage struct {
	ID        string
	DedupeKey string
	Kind      string
	NodeID    string
	NodeName  string
	Title     string
	Body      string
	Delivery  string
	ReadAt    int64
	CreatedAt int64
}

// AddSiteMessage inserts a message once. The dedupe key also prevents the
// expiry checker from notifying twice across restarts or overlapping runs.
func (s *Store) AddSiteMessage(m *SiteMessage) (bool, error) {
	return insertSiteMessage(s.db, m)
}

func insertSiteMessage(db interface {
	Exec(string, ...any) (sql.Result, error)
}, m *SiteMessage) (bool, error) {
	if m == nil || m.DedupeKey == "" || m.Kind == "" || m.Title == "" {
		return false, errors.New("site message is incomplete")
	}
	if m.ID == "" {
		m.ID = newID("sm_")
	}
	if m.CreatedAt == 0 {
		m.CreatedAt = nowMS()
	}
	res, err := db.Exec(`INSERT INTO site_messages
(id, dedupe_key, kind, node_id, node_name, title, body, delivery, read_at, created_at)
VALUES (?,?,?,?,?,?,?,?,?,?) ON CONFLICT (dedupe_key) DO NOTHING`,
		m.ID, m.DedupeKey, m.Kind, m.NodeID, m.NodeName, m.Title, m.Body, m.Delivery, m.ReadAt, m.CreatedAt)
	if err != nil {
		return false, fmt.Errorf("add site message: %w", err)
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

type ExpiryReminderDelivery struct {
	Telegram string
	Webhook  string
}

type ExpiringNode struct{ ID, Name, ExpireAt string }

// ListExpiringNodes avoids loading article Markdown, albums and Agent facts for
// the hourly reminder scan. Only configured deadlines participate.
func (s *Store) ListExpiringNodes() ([]ExpiringNode, error) {
	rows, err := s.db.Query(`SELECT n.id,n.name,p.expire_at FROM node_profile p JOIN nodes n ON n.id=p.node_id WHERE p.expire_at<>'' AND p.expire_at<>'永久'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var nodes []ExpiringNode
	for rows.Next() {
		var node ExpiringNode
		if err := rows.Scan(&node.ID, &node.Name, &node.ExpireAt); err != nil {
			return nil, err
		}
		nodes = append(nodes, node)
	}
	return nodes, rows.Err()
}

func (s *Store) GetExpiryReminderDelivery(key string) (ExpiryReminderDelivery, error) {
	var state ExpiryReminderDelivery
	err := s.db.QueryRow(`SELECT telegram_status,webhook_status FROM expiry_reminder_deliveries WHERE dedupe_key=?`, key).Scan(&state.Telegram, &state.Webhook)
	return state, err
}

// EnsureExpiryReminder creates the backup and durable receipt together. A deleted
// inbox item stays deleted, while an undelivered external channel can still retry.
func (s *Store) EnsureExpiryReminder(m *SiteMessage) (ExpiryReminderDelivery, error) {
	var state ExpiryReminderDelivery
	if m == nil || m.Kind != "expiry" || m.DedupeKey == "" {
		return state, errors.New("expiry reminder is incomplete")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return state, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`INSERT INTO expiry_reminder_deliveries (dedupe_key) VALUES (?) ON CONFLICT DO NOTHING`, m.DedupeKey)
	if err != nil {
		return state, err
	}
	added, err := res.RowsAffected()
	if err != nil {
		return state, err
	}
	if added > 0 {
		if _, err := insertSiteMessage(tx, m); err != nil {
			return state, err
		}
	}
	if err := tx.QueryRow(`SELECT telegram_status,webhook_status FROM expiry_reminder_deliveries WHERE dedupe_key=?`, m.DedupeKey).Scan(&state.Telegram, &state.Webhook); err != nil {
		return state, err
	}
	return state, tx.Commit()
}

func (s *Store) UpdateExpiryReminderDelivery(key string, state ExpiryReminderDelivery, summary string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE expiry_reminder_deliveries SET telegram_status=?,webhook_status=? WHERE dedupe_key=?`, state.Telegram, state.Webhook, key); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE site_messages SET delivery=? WHERE dedupe_key=?`, summary, key); err != nil {
		return err
	}
	return tx.Commit()
}

// UpdateSiteMessageDelivery records the latest external delivery summary.
func (s *Store) UpdateSiteMessageDelivery(dedupeKey, delivery string) error {
	_, err := s.db.Exec(`UPDATE site_messages SET delivery=? WHERE dedupe_key=?`, delivery, dedupeKey)
	return err
}

// MarkSiteMessageRead marks one inbox item as read.
func (s *Store) MarkSiteMessageRead(id string) error {
	res, err := s.db.Exec(`UPDATE site_messages SET read_at=? WHERE id=? AND read_at=0`, nowMS(), id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err == nil && n == 0 {
		var exists int
		if scanErr := s.db.QueryRow(`SELECT 1 FROM site_messages WHERE id=?`, id).Scan(&exists); scanErr != nil {
			return ErrNotFound
		}
	}
	return err
}

// MarkAllSiteMessagesRead marks every unread inbox item as read.
func (s *Store) MarkAllSiteMessagesRead() error {
	_, err := s.db.Exec(`UPDATE site_messages SET read_at=? WHERE read_at=0`, nowMS())
	return err
}

// DeleteSiteMessages removes only the explicitly selected inbox entries.
func (s *Store) DeleteSiteMessages(ids []string) (int64, error) {
	const maxIDs = 100
	args := make([]any, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		if len(args) >= maxIDs {
			return 0, errors.New("too many site message IDs")
		}
		args = append(args, id)
	}
	if len(args) == 0 {
		return 0, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(args)), ",")
	res, err := s.db.Exec(`DELETE FROM site_messages WHERE id IN (`+placeholders+`)`, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// CountUnreadSiteMessages returns the inbox badge count.
func (s *Store) CountUnreadSiteMessages() (int, error) {
	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM site_messages WHERE read_at=0`).Scan(&count)
	return count, err
}

// CountSiteMessages returns the total inbox size for pagination.
func (s *Store) CountSiteMessages() (int, error) {
	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM site_messages`).Scan(&count)
	return count, err
}

// ListSiteMessages returns one newest-first inbox page.
func (s *Store) ListSiteMessages(limit, offset int) ([]SiteMessage, error) {
	if limit <= 0 || limit > 500 {
		limit = 10
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := s.db.Query(`SELECT id, dedupe_key, kind, node_id, node_name, title, body, delivery, read_at, created_at
FROM site_messages ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SiteMessage
	for rows.Next() {
		var m SiteMessage
		if err := rows.Scan(&m.ID, &m.DedupeKey, &m.Kind, &m.NodeID, &m.NodeName,
			&m.Title, &m.Body, &m.Delivery, &m.ReadAt, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
