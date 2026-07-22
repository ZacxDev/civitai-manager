package store

import (
	"database/sql"
	"errors"
	"sort"
)

// GetSetting returns the value for a UI settings key. The bool reports whether
// the key was present (so a caller can distinguish "unset" from "set to empty").
func (s *Store) GetSetting(key string) (string, bool, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return v, true, nil
}

// GetSettingDefault returns the stored value for key, or def when it is unset.
func (s *Store) GetSettingDefault(key, def string) (string, error) {
	v, ok, err := s.GetSetting(key)
	if err != nil {
		return def, err
	}
	if !ok {
		return def, nil
	}
	return v, nil
}

// SetSetting upserts a UI settings key/value.
func (s *Store) SetSetting(key, value string) error {
	_, err := s.db.Exec(`
		INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		key, value, nowRFC3339())
	return err
}

// ListScanDirs returns the persisted, selected extra scan directories, sorted.
func (s *Store) ListScanDirs() ([]string, error) {
	rows, err := s.db.Query(`SELECT path FROM scan_dirs ORDER BY path`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// AddScanDir persists one extra scan directory (idempotent).
func (s *Store) AddScanDir(path string) error {
	if path == "" {
		return nil
	}
	_, err := s.db.Exec(`INSERT INTO scan_dirs (path, added_at) VALUES (?, ?)
		ON CONFLICT (path) DO NOTHING`, path, nowRFC3339())
	return err
}

// RemoveScanDir drops one persisted scan directory (a missing path is not an
// error).
func (s *Store) RemoveScanDir(path string) error {
	_, err := s.db.Exec(`DELETE FROM scan_dirs WHERE path = ?`, path)
	return err
}

// SetScanDirs replaces the entire persisted scan-directory selection with paths
// (de-duplicated) in a single transaction, so the stored set always exactly
// mirrors what the user last confirmed.
func (s *Store) SetScanDirs(paths []string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(`DELETE FROM scan_dirs`); err != nil {
		return err
	}
	now := nowRFC3339()
	seen := map[string]bool{}
	// Insert in sorted order for a stable on-disk layout.
	sorted := append([]string{}, paths...)
	sort.Strings(sorted)
	for _, p := range sorted {
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		if _, err := tx.Exec(`INSERT INTO scan_dirs (path, added_at) VALUES (?, ?)`, p, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}
