package store

// 事件流与节点测试任务的存取。
//
// 这两块放一个文件：任务完成时也会写一条 event（"跑分完成"要出现在动态流里），
// 两者是一起用的。

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/Vincentkeio/kokoro/internal/model"
)

// ==================== 事件流 ====================

// AddEvent 记一条事件。
//
// 失败只返回 error 不 panic：事件流是"锦上添花"，
// 它写不进去不该连累主流程（上报、上下线）。
func (s *Store) AddEvent(nodeID, kind, text, ref string) error {
	if kind == "" {
		return errors.New("event.kind 为空")
	}
	_, err := s.db.Exec(
		`INSERT INTO events (ts, node_id, kind, text, ref) VALUES (?,?,?,?,?)`,
		nowMS(), nodeID, kind, text, ref)
	if err != nil {
		return fmt.Errorf("写事件失败: %w", err)
	}
	return nil
}

// ListEvents 取最近的事件。
func (s *Store) ListEvents(limit int) ([]model.Event, error) {
	if limit <= 0 {
		limit = 30
	}
	rows, err := s.db.Query(
		`SELECT id, ts, node_id, kind, text, ref FROM events ORDER BY ts DESC, id DESC LIMIT ?`,
		limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Event
	for rows.Next() {
		var e model.Event
		if err := rows.Scan(&e.ID, &e.TS, &e.NodeID, &e.Kind, &e.Text, &e.Ref); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ==================== 节点测试任务 ====================

// CreateTask 入队一条测试任务。
func (s *Store) CreateTask(t *model.NodeTask) error {
	if t == nil || t.NodeID == "" || t.Cmd == "" {
		return errors.New("任务缺少 node_id 或 cmd")
	}
	if t.ID == "" {
		t.ID = "tk_" + randHex(12)
	}
	if t.CreatedAt == 0 {
		t.CreatedAt = nowMS()
	}
	if t.Status == "" {
		t.Status = model.TaskQueued
	}
	_, err := s.db.Exec(`INSERT INTO node_tasks
(id, node_id, kind, title, cmd, status, summary, detail, error, created_at, started_at, finished_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		t.ID, t.NodeID, t.Kind, t.Title, t.Cmd, string(t.Status),
		t.Summary, t.Detail, t.Error, t.CreatedAt, t.StartedAt, t.FinishedAt)
	if err != nil {
		return fmt.Errorf("建任务失败: %w", err)
	}
	return nil
}

// ClaimQueuedTask 取一条待下发的任务并置为 running。
//
// 一次只取一条：跑分脚本动辄十几分钟，同一台机器上并发跑多个测试
// 会互相抢 CPU 把结果跑偏。取到就置 running，避免下次上报重复下发。
func (s *Store) ClaimQueuedTask(nodeID string) (*model.NodeTask, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var t model.NodeTask
	var st string
	err = tx.QueryRow(`SELECT id, node_id, kind, title, cmd, status, summary, detail, error,
created_at, started_at, finished_at
FROM node_tasks WHERE node_id = ? AND status = ? ORDER BY created_at LIMIT 1`,
		nodeID, string(model.TaskQueued)).Scan(
		&t.ID, &t.NodeID, &t.Kind, &t.Title, &t.Cmd, &st, &t.Summary, &t.Detail, &t.Error,
		&t.CreatedAt, &t.StartedAt, &t.FinishedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	t.Status = model.TaskRunning
	t.StartedAt = nowMS()
	if _, err := tx.Exec(`UPDATE node_tasks SET status=?, started_at=? WHERE id=?`,
		string(model.TaskRunning), t.StartedAt, t.ID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &t, nil
}

// FinishTask 落一条任务结果。
func (s *Store) FinishTask(id, summary, detail, errMsg string, ok bool) error {
	status := model.TaskDone
	if !ok {
		status = model.TaskFailed
	}
	res, err := s.db.Exec(`UPDATE node_tasks
SET status=?, summary=?, detail=?, error=?, finished_at=? WHERE id=?`,
		string(status), summary, detail, errMsg, nowMS(), id)
	if err != nil {
		return fmt.Errorf("落任务结果失败: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("任务不存在: %s", id)
	}
	return nil
}

// GetTask 按 id 取任务。
func (s *Store) GetTask(id string) (*model.NodeTask, error) {
	return s.scanTask(s.db.QueryRow(`SELECT id, node_id, kind, title, cmd, status, summary,
detail, error, created_at, started_at, finished_at FROM node_tasks WHERE id = ?`, id))
}

// LatestTaskSummary 取某台机器**最近一条已完成**的测试摘要，给卡片用。
//
// 只看 done：失败的任务没有摘要可展示，running 的还没结果。
func (s *Store) LatestTaskSummary(nodeID string) (*model.NodeTask, error) {
	return s.scanTask(s.db.QueryRow(`SELECT id, node_id, kind, title, cmd, status, summary,
detail, error, created_at, started_at, finished_at FROM node_tasks
WHERE node_id = ? AND status = ? ORDER BY finished_at DESC LIMIT 1`,
		nodeID, string(model.TaskDone)))
}

// ListTasks 取某台机器的任务历史（nodeID 为空则取全部）。
func (s *Store) ListTasks(nodeID string, limit int) ([]model.NodeTask, error) {
	if limit <= 0 {
		limit = 20
	}
	q := `SELECT id, node_id, kind, title, cmd, status, summary, detail, error,
created_at, started_at, finished_at FROM node_tasks`
	var args []any
	if nodeID != "" {
		q += " WHERE node_id = ?"
		args = append(args, nodeID)
	}
	q += " ORDER BY created_at DESC LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.NodeTask
	for rows.Next() {
		var t model.NodeTask
		var st string
		if err := rows.Scan(&t.ID, &t.NodeID, &t.Kind, &t.Title, &t.Cmd, &st, &t.Summary,
			&t.Detail, &t.Error, &t.CreatedAt, &t.StartedAt, &t.FinishedAt); err != nil {
			return nil, err
		}
		t.Status = model.TaskStatus(st)
		out = append(out, t)
	}
	return out, rows.Err()
}

// RunningTaskCount 统计某台机器正在跑的任务数，卡片上显示"测试中"用。
func (s *Store) RunningTaskCount(nodeID string) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM node_tasks WHERE node_id=? AND status IN (?,?)`,
		nodeID, string(model.TaskQueued), string(model.TaskRunning)).Scan(&n)
	return n, err
}

func (s *Store) scanTask(row *sql.Row) (*model.NodeTask, error) {
	var t model.NodeTask
	var st string
	err := row.Scan(&t.ID, &t.NodeID, &t.Kind, &t.Title, &t.Cmd, &st, &t.Summary,
		&t.Detail, &t.Error, &t.CreatedAt, &t.StartedAt, &t.FinishedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	t.Status = model.TaskStatus(st)
	return &t, nil
}

// 供 store 内部使用的时间戳（与 store.go 里的 nowMS 保持一致）。
var _ = time.Now

// ==================== 最新文章（给动态流用） ====================

// RecentProfile 是「最近更新过内容的节点」的轻量视图。
type RecentProfile struct {
	NodeID    string
	Summary   string
	UpdatedAt int64
	HasBody   bool
}

// ListRecentProfiles 取最近更新过内容的节点，给首页动态流用。
//
// 只要"有内容"的：一篇被清空的文章不该出现在动态里。
func (s *Store) ListRecentProfiles(limit int) ([]RecentProfile, error) {
	if limit <= 0 {
		limit = 10
	}
	rows, err := s.db.Query(`SELECT node_id, summary, updated_at, content_md <> ''
FROM node_profile WHERE content_md <> '' ORDER BY updated_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RecentProfile
	for rows.Next() {
		var r RecentProfile
		if err := rows.Scan(&r.NodeID, &r.Summary, &r.UpdatedAt, &r.HasBody); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
