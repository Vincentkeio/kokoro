package store

import "strings"

func normalizeNetQIPVersion(version string) string {
	if strings.EqualFold(strings.TrimSpace(version), "v6") || strings.EqualFold(strings.TrimSpace(version), "ipv6") {
		return "v6"
	}
	return "v4"
}

// SetNodeNetQIPVersion persists the per-node three-network probe family.
func (s *Store) SetNodeNetQIPVersion(id, version string) error {
	result, err := s.db.Exec("UPDATE nodes SET netq_ip_version=? WHERE id=?", normalizeNetQIPVersion(version), id)
	if err != nil {
		return err
	}
	if count, err := result.RowsAffected(); err == nil && count == 0 {
		return ErrNotFound
	}
	return err
}
