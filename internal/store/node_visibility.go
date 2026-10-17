package store

import (
	"fmt"

	"github.com/Vincentkeio/kokoro/internal/model"
)

// SetNodeVisibility only changes the administrator-owned visibility field.
// Rewriting an entire stale Node here could overwrite a concurrent Agent report.
func (s *Store) SetNodeVisibility(id string, visibility model.Visibility) error {
	if visibility != model.VisibilityPublic && visibility != model.VisibilityPrivate && visibility != model.VisibilityUnlisted {
		return fmt.Errorf("无效的展示状态")
	}
	result, err := s.db.Exec("UPDATE nodes SET visibility=? WHERE id=?", visibility, id)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n == 0 {
		return ErrNotFound
	}
	return err
}
