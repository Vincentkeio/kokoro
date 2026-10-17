package store

import (
	"strings"
)

// UpdateNodeLabels changes only the administrator-supplied fields present in
// the form. Agent facts, heartbeat, visibility and ordering remain independent.
func (s *Store) UpdateNodeLabels(id string, name *string, tags *[]string) error {
	var fields []string
	var args []any
	if name != nil {
		fields = append(fields, "name=?")
		args = append(args, *name)
	}
	if tags != nil {
		encoded, err := encodeJSON(*tags)
		if err != nil {
			return err
		}
		fields = append(fields, "tags=?")
		args = append(args, encoded)
	}
	if len(fields) == 0 {
		return nil
	}
	args = append(args, id)
	result, err := s.db.Exec("UPDATE nodes SET "+strings.Join(fields, ",")+" WHERE id=?", args...)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n == 0 {
		return ErrNotFound
	}
	return err
}
