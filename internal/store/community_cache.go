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
// communityCacheMaxRows bounds the community_cache table: this is a fail-open
// image-feed cache, not a system of record, so we keep only the most-recently
// fetched entries and prune the rest. Prevents unbounded growth from browsing
// many model versions (or a hostile enumerator on a non-loopback deployment).
var communityCacheMaxRows = 4000

func (s *Store) PutCommunityCache(modelID, versionID int, raw []byte) error {
	if _, err := s.db.Exec(`
		INSERT INTO community_cache (model_id, version_id, raw, fetched_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (model_id, version_id) DO UPDATE SET
			raw = excluded.raw,
			fetched_at = excluded.fetched_at`,
		modelID, versionID, raw, nowRFC3339()); err != nil {
		return err
	}
	// Opportunistic bound: drop everything but the newest N by fetched_at. Cheap
	// (runs only on a cache-miss write) and keeps the table from growing forever.
	// Tie-break by rowid (insert order) so "newest" is deterministic even when many
	// entries share the same second-granularity fetched_at.
	_, err := s.db.Exec(`
		DELETE FROM community_cache WHERE rowid NOT IN (
			SELECT rowid FROM community_cache ORDER BY fetched_at DESC, rowid DESC LIMIT ?
		)`, communityCacheMaxRows)
	return err
}
