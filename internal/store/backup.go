package store

import (
	"context"
	"fmt"
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
