package store

func (s *Store) UpdateAgentVersion(id, version string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, e := s.db.Exec(`UPDATE nodes SET agent_ver=? WHERE id=? AND agent_ver<>?`, version, id, version)
	return e
}
