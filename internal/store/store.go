// Package store owns the SQLite index: schema, migrations, and queries.
// It uses the pure-Go modernc.org/sqlite driver (no CGO) and FTS5 for
// full-text search across transcript messages.
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"

	_ "modernc.org/sqlite"
)

// Store wraps the SQLite connection.
type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the SQLite database at path and applies
// migrations. The database file is kept at 0600, since its full-text index
// holds the text of every transcript.
func Open(path string) (*Store, error) {
	if err := makePrivate(path); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("opening sqlite at %s: %w", path, err)
	}
	// One writer at a time keeps things simple and avoids "database is locked".
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA journal_mode=WAL; PRAGMA foreign_keys=ON;`); err != nil {
		db.Close()
		return nil, fmt.Errorf("setting pragmas: %w", err)
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrating: %w", err)
	}
	return s, nil
}

// makePrivate creates the database file at 0600 if it is missing and tightens
// an existing one, and its WAL files, to 0600. SQLite gives the -wal and -shm
// files it creates the main file's mode, so creating it first covers them too.
func makePrivate(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("creating %s: %w", path, err)
	}
	f.Close()
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		if err := os.Chmod(p, 0o600); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("restricting %s: %w", p, err)
		}
	}
	return nil
}

// Close closes the underlying database.
func (s *Store) Close() error {
	return s.db.Close()
}
