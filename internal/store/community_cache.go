package store

import (
	"database/sql"
	"errors"
	"time"
)

// CommunityCacheEntry is one cached community-image feed snapshot (see the
// community_cache migration). Raw is the exact SearchImages response body for
// the (ModelID, VersionID) key; FetchedAt drives the caller's staleness /
// fail-open decision.
type CommunityCacheEntry struct {
	ModelID   int
	VersionID int
	Raw       []byte
	FetchedAt time.Time
}

// GetCommunityCache returns the cached community-feed snapshot for a
// (modelID, versionID), or (nil, nil) when there is no cached entry. The caller
// decides whether the entry is fresh enough (via FetchedAt) and whether to serve
// it stale on a fetch failure (fail-open).
func (s *Store) GetCommunityCache(modelID, versionID int) (*CommunityCacheEntry, error) {
	row := s.db.QueryRow(
		`SELECT model_id, version_id, raw, fetched_at FROM community_cache
			WHERE model_id = ? AND version_id = ?`, modelID, versionID)
	var (
		e       CommunityCacheEntry
		fetched string
	)
	if err := row.Scan(&e.ModelID, &e.VersionID, &e.Raw, &fetched); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	e.FetchedAt = parseTime(fetched)
	return &e, nil
}

// PutCommunityCache upserts a community-feed snapshot, stamping fetched_at to now
// so a subsequent staleness check measures from this fetch. Callers should only
// cache SUCCESSFUL, non-empty responses so the cache is never poisoned with an
// empty/error result the fail-open path would then serve.
func (s *Store) PutCommunityCache(modelID, versionID int, raw []byte) error {
	_, err := s.db.Exec(`
		INSERT INTO community_cache (model_id, version_id, raw, fetched_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (model_id, version_id) DO UPDATE SET
			raw = excluded.raw,
			fetched_at = excluded.fetched_at`,
		modelID, versionID, raw, nowRFC3339())
	return err
}
