// Package store is the SQLite persistence layer: the subscription list, the
// seen-versions diff ledger, the download queue, the local-file index, and the
// activity event feed. It uses the pure-Go modernc.org/sqlite driver so the
// binary cross-compiles without cgo.
package store

import (
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Store wraps the database handle.
type Store struct {
	db *sql.DB
}

// Open opens (creating parent dirs and the file as needed) the SQLite database
// at path, applies pragmas, and runs any pending migrations. Pass ":memory:"
// for an ephemeral in-memory database (used by tests).
func Open(path string) (*Store, error) {
	if path != ":memory:" {
		if dir := filepath.Dir(path); dir != "" && dir != "." {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return nil, fmt.Errorf("create db dir: %w", err)
			}
		}
	}

	dsn := path
	if path == ":memory:" {
		// A shared cache keeps a single logical in-memory DB across the pool's
		// connections for the lifetime of the handle.
		dsn = "file::memory:?cache=shared"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// modernc's driver serializes writes; a single-ish pool avoids
	// "database is locked" churn while WAL still allows concurrent readers.
	db.SetMaxOpenConns(1)

	pragmas := []string{
		"PRAGMA journal_mode = WAL",
		"PRAGMA foreign_keys = ON",
		"PRAGMA busy_timeout = 5000",
		"PRAGMA synchronous = NORMAL",
	}
	for _, p := range pragmas {
		if _, err := db.Exec(p); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("pragma %q: %w", p, err)
		}
	}

	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }

// DB exposes the raw handle (used by tests that assert schema state).
func (s *Store) DB() *sql.DB { return s.db }

// migrate applies embedded migrations in filename order, tracking the highest
// applied version in schema_migrations. Each migration runs in its own
// transaction so a failure leaves the DB at the last good version.
func (s *Store) migrate() error {
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		applied_at TEXT NOT NULL
	)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	var current int
	if err := s.db.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&current); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}

	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("read migrations: %w", err)
	}
	files := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)

	for _, name := range files {
		version, err := migrationVersion(name)
		if err != nil {
			return err
		}
		if version <= current {
			continue
		}
		sqlBytes, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}
		tx, err := s.db.Begin()
		if err != nil {
			return fmt.Errorf("begin migration %s: %w", name, err)
		}
		if _, err := tx.Exec(string(sqlBytes)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("apply migration %s: %w", name, err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`,
			version, nowRFC3339()); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("record migration %s: %w", name, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %s: %w", name, err)
		}
		current = version
	}
	return nil
}

// SchemaVersion returns the highest applied migration version.
func (s *Store) SchemaVersion() (int, error) {
	var v int
	err := s.db.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&v)
	return v, err
}

// migrationVersion parses the leading integer of a migration filename
// ("0001_init.sql" -> 1).
func migrationVersion(name string) (int, error) {
	base := name
	if i := strings.IndexByte(base, '_'); i >= 0 {
		base = base[:i]
	}
	var v int
	if _, err := fmt.Sscanf(base, "%d", &v); err != nil {
		return 0, fmt.Errorf("migration %q has no leading version number: %w", name, err)
	}
	return v, nil
}

// --- time helpers: all timestamps are stored as RFC3339 UTC strings ---

func nowRFC3339() string { return time.Now().UTC().Format(time.RFC3339) }

func formatTime(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// parseTime parses an RFC3339 string; a blank string yields the zero time.
func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// formatTimeNano renders a time as an RFC3339Nano UTC string, preserving
// sub-second precision so the scanner's mtime cache compares exactly against a
// file's os.FileInfo modification time.
func formatTimeNano(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

// parseTimeNano parses an RFC3339Nano string; a blank/invalid string yields the
// zero time.
func parseTimeNano(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// nullTimeNanoStr converts a nil/zero time to NULL, else an RFC3339Nano string.
func nullTimeNanoStr(t *time.Time) sql.NullString {
	if t == nil || t.IsZero() {
		return sql.NullString{}
	}
	return sql.NullString{String: formatTimeNano(*t), Valid: true}
}

// nullStr converts an empty string to a NULL-storing sql.NullString.
func nullStr(s string) sql.NullString {
	return sql.NullString{String: s, Valid: s != ""}
}

// nullTime converts a nil time to a NULL-storing value.
func nullTimeStr(t *time.Time) sql.NullString {
	if t == nil || t.IsZero() {
		return sql.NullString{}
	}
	return sql.NullString{String: formatTime(*t), Valid: true}
}
