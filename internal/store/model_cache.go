package store

import (
	"database/sql"
	"errors"
	"time"
)

// ModelCacheEntry is one cached CivitAI model-detail snapshot (see the
// model_cache migration). Raw is the exact GetModel JSON body; FetchedAt drives
// the caller's staleness check.
type ModelCacheEntry struct {
	ModelID   int
	Name      string
	Raw       []byte
	FetchedAt time.Time
}

// GetModelCache returns the cached model-detail snapshot for id, or (nil, nil)
// when there is no cached entry. The caller decides whether the entry is fresh
// enough (via FetchedAt) or should be refetched.
func (s *Store) GetModelCache(id int) (*ModelCacheEntry, error) {
	row := s.db.QueryRow(
		`SELECT model_id, name, raw, fetched_at FROM model_cache WHERE model_id = ?`, id)
	var (
		e       ModelCacheEntry
		fetched string
	)
	if err := row.Scan(&e.ModelID, &e.Name, &e.Raw, &fetched); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	e.FetchedAt = parseTime(fetched)
	return &e, nil
}

// PutModelCache upserts a model-detail snapshot, stamping fetched_at to now so a
// subsequent staleness check measures from this fetch.
func (s *Store) PutModelCache(id int, name string, raw []byte) error {
	_, err := s.db.Exec(`
		INSERT INTO model_cache (model_id, name, raw, fetched_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (model_id) DO UPDATE SET
			name = excluded.name,
			raw = excluded.raw,
			fetched_at = excluded.fetched_at`,
		id, name, raw, nowRFC3339())
	return err
}
