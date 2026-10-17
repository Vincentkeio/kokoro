package store

import "fmt"

// SetNodeSortOrder updates only the administrator-owned ordering value.
// This avoids overwriting fields that may have changed in a concurrent Agent report.
func (s *Store) SetNodeSortOrder(id string, sortOrder int) error {
	result, err := s.db.Exec("UPDATE nodes SET sort_order=? WHERE id=?", sortOrder, id)
	if err != nil {
		return fmt.Errorf("更新节点排序失败: %w", err)
	}
	n, err := result.RowsAffected()
	if err == nil && n == 0 {
		return ErrNotFound
	}
	return err
}
