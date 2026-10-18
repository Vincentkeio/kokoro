package store

import (
	"context"
	"fmt"
	"os"
	"strings"
)

const SchemaVersion = schemaVersion

// Snapshot creates a consistent SQLite copy, including committed WAL contents.
// VACUUM INTO streams to disk and does not load monitoring history into RAM.
func (s *Store) Snapshot(ctx context.Context, path string) error {
	// SQLite supplies the snapshot transaction. Do not hold the application
	// write mutex while copying history: WAL writers must keep accepting reports.
	_, err := s.db.ExecContext(ctx, "VACUUM INTO ?", path)
	return err
}

func (s *Store) ValidateBackup(ctx context.Context) error {
	var check string
	if err := s.db.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&check); err != nil {
		return err
	}
	if check != "ok" {
		return fmt.Errorf("数据库完整性校验失败")
	}
	rows, err := s.db.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return err
	}
	bad := rows.Next()
	rows.Close()
	if bad {
		return fmt.Errorf("数据库关联校验失败")
	}
	var triggers int
	if err = s.db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='trigger'").Scan(&triggers); err != nil {
		return err
	}
	if triggers != 0 {
		return fmt.Errorf("备份数据库包含不支持的触发器")
	}
	return nil
}

// PrepareBackupRestore invalidates browser sessions and queued/running work.
// Jobs exported from the source must never run a second time on a restored Hub.
func (s *Store) PrepareBackupRestore(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM settings WHERE key LIKE 'adm_sess:%' OR key IN ('panel_migration_plan','panel_agent_update_plan'); DELETE FROM sessions; UPDATE node_tasks SET status='failed', error='面板迁移后已取消旧任务' WHERE status IN ('queued','running')")
	return err
}

// DeleteSettings removes the listed settings rows. Which keys belong to the
// machine rather than to the data is the caller's call, so this stays key-agnostic.
func (s *Store) DeleteSettings(ctx context.Context, keys ...string) error {
	if len(keys) == 0 {
		return nil
	}
	placeholders := make([]string, 0, len(keys))
	args := make([]any, 0, len(keys))
	for _, key := range keys {
		placeholders = append(placeholders, "?")
		args = append(args, key)
	}
	_, err := s.db.ExecContext(ctx,
		"DELETE FROM settings WHERE key IN ("+strings.Join(placeholders, ",")+")", args...)
	return err
}

// safeTableName 只接受小写字母和下划线。表名会拼进 SQL，白名单由调用方选，
// 这里再做一道形状检查，避免任何拼接进注入。
func safeTableName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z') && r != '_' {
			return false
		}
	}
	return true
}

// SnapshotSelection 生成只含所选表的一致性快照。
//
// 先按当前 schema 建一个空库，再在同一个事务里把选中的表复制进去，所以
// 历史数据不会出现跨表不一致。settingsKeys 非空且没整表复制 settings 时，
// 只复制这些键（用于"只要节点身份、不要整份配置"的导出）。
func (s *Store) SnapshotSelection(ctx context.Context, path string, tables, settingsKeys []string) error {
	if len(tables) == 0 {
		return fmt.Errorf("没有选择任何备份内容")
	}
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("快照目标已存在：%s", path)
	}
	empty, err := Open(path)
	if err != nil {
		return fmt.Errorf("创建快照数据库失败: %w", err)
	}
	if err := empty.Close(); err != nil {
		return err
	}

	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "ATTACH DATABASE ? AS snapshotdb", path); err != nil {
		return fmt.Errorf("挂载快照失败: %w", err)
	}
	defer func() {
		_, _ = conn.ExecContext(context.Background(), "DETACH DATABASE snapshotdb")
	}()

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	settingsCopied := false
	for _, table := range tables {
		if !safeTableName(table) {
			return fmt.Errorf("非法的表名：%q", table)
		}
		if table == "settings" {
			settingsCopied = true
		}
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO snapshotdb."+table+" SELECT * FROM main."+table); err != nil {
			return fmt.Errorf("复制 %s 失败: %w", table, err)
		}
	}
	if len(settingsKeys) > 0 && !settingsCopied {
		placeholders := make([]string, 0, len(settingsKeys))
		args := make([]any, 0, len(settingsKeys))
		for _, key := range settingsKeys {
			placeholders = append(placeholders, "?")
			args = append(args, key)
		}
		query := "INSERT INTO snapshotdb.settings SELECT * FROM main.settings WHERE key IN (" +
			strings.Join(placeholders, ",") + ")"
		if _, err := tx.ExecContext(ctx, query, args...); err != nil {
			return fmt.Errorf("复制迁移身份失败: %w", err)
		}
	}
	return tx.Commit()
}
