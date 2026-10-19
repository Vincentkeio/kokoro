package store

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestMigrationFailureRollsBackSchemaAndVersion(t *testing.T) {
	s := openTmp(t)
	if _, err := s.db.Exec(`ALTER TABLE net_quality DROP COLUMN ip_version; PRAGMA user_version=25`); err != nil {
		t.Fatal(err)
	}
	last := len(migrations) - 1
	original := migrations[last].sql
	t.Cleanup(func() { migrations[last].sql = original })
	// Fail after the real ALTER executes, as a later I/O/statement failure
	// would. The same database must remain safe to reopen and retry.
	migrations[last].sql += "; SELECT * FROM audit_missing_table"
	if err := s.migrate(); err == nil {
		t.Fatal("injected migration failure was ignored")
	}
	var version, columns int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT count(*) FROM pragma_table_info('net_quality') WHERE name='ip_version'`).Scan(&columns); err != nil {
		t.Fatal(err)
	}
	if version != 25 || columns != 0 {
		t.Fatalf("partial upgrade survived: version=%d added columns=%d", version, columns)
	}
	migrations[last].sql = original
	if err := s.migrate(); err != nil {
		t.Fatalf("retry after failure: %v", err)
	}
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != schemaVersion {
		t.Fatalf("retry version=%d: %v", version, err)
	}
}

func TestInitialSchemaFailureLeavesNoPartialDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "initial.db")
	original := schemaSQL
	t.Cleanup(func() { schemaSQL = original })
	schemaSQL += "; SELECT * FROM audit_missing_table"
	if s, err := Open(path); err == nil {
		s.Close()
		t.Fatal("injected initialization failure was ignored")
	}
	db, err := sql.Open("sqlite", buildDSN(path))
	if err != nil {
		t.Fatal(err)
	}
	var count int
	err = db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name='nodes'`).Scan(&count)
	db.Close()
	if err != nil || count != 0 {
		t.Fatalf("partial initial schema persisted: count=%d err=%v", count, err)
	}
	schemaSQL = original
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
}
