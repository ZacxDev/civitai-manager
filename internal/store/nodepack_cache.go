package store

import (
	"database/sql"
	"errors"
	"strings"
	"time"
)

// NodePackCacheEntry is one cached custom-node attribution index (see the
// nodepack_cache migration). Raw is the exact upstream response body for Source;
// FetchedAt drives the caller's staleness / fail-open decision.
type NodePackCacheEntry struct {
	Source    string
	Raw       []byte
	FetchedAt time.Time
}

// Cache source keys. Both indexes are public, read-only third-party data.
const (
	// NodePackSourceExtensionNodeMap is the static extension-node-map fallback
	// index (one row, refetched on staleness).
	NodePackSourceExtensionNodeMap = "extension-node-map"
	// nodePackSourceRegistryPrefix namespaces per-class Comfy Registry lookups.
	nodePackSourceRegistryPrefix = "registry:"
)

// NodePackSourceRegistryClass is the cache key for one Comfy Registry class
// lookup. The Registry has NO batch endpoint, so a workflow with N missing
// classes costs N requests; caching each answer — including a miss — is what
// keeps re-rendering a report from re-hitting the network N times.
func NodePackSourceRegistryClass(class string) string {
	return nodePackSourceRegistryPrefix + strings.TrimSpace(class)
}

// GetNodePackCache returns the cached index for source, or (nil, nil) when there
// is no cached entry. The caller decides whether the entry is fresh enough (via
// FetchedAt) and whether to serve it STALE on a fetch failure (fail-open), the
// same contract as the community cache.
func (s *Store) GetNodePackCache(source string) (*NodePackCacheEntry, error) {
	row := s.db.QueryRow(
		`SELECT source, raw, fetched_at FROM nodepack_cache WHERE source = ?`, source)
	var (
		e       NodePackCacheEntry
		fetched string
	)
	if err := row.Scan(&e.Source, &e.Raw, &fetched); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	e.FetchedAt = parseTime(fetched)
	return &e, nil
}

// GetNodePackRaw / PutNodePackRaw are the flat adapter form of the two methods
// above, satisfying comfy.NodePackCache without internal/comfy having to import
// this package (the dependency runs the other way for every other package).
//
// A cache MISS is (nil, zero time, nil), never an error — the resolver treats an
// error as a miss too, so a broken cache degrades to a network fetch rather than
// failing attribution.
func (s *Store) GetNodePackRaw(source string) ([]byte, time.Time, error) {
	e, err := s.GetNodePackCache(source)
	if err != nil || e == nil {
		return nil, time.Time{}, err
	}
	return e.Raw, e.FetchedAt, nil
}

// PutNodePackRaw stores a successful response body for source.
func (s *Store) PutNodePackRaw(source string, raw []byte) error {
	return s.PutNodePackCache(source, raw)
}

// nodePackCacheMaxRows bounds the nodepack_cache table. This is a fail-open
// attribution cache, not a system of record, so only the most-recently fetched
// entries are kept. The per-class Registry rows are what make this grow: every
// distinct missing class across every workflow the user opens adds one.
var nodePackCacheMaxRows = 5000

// PutNodePackCache upserts an index snapshot, stamping fetched_at to now so a
// subsequent staleness check measures from this fetch.
//
// Callers must only cache SUCCESSFUL responses, so the fail-open path can never
// serve a poisoned empty/error body as though it were a real index. A Registry
// MISS is a successful answer and SHOULD be cached (as a JSON null) — an
// unattributable class is a stable fact, not a failure.
func (s *Store) PutNodePackCache(source string, raw []byte) error {
	if _, err := s.db.Exec(`
		INSERT INTO nodepack_cache (source, raw, fetched_at) VALUES (?, ?, ?)
		ON CONFLICT (source) DO UPDATE SET
			raw = excluded.raw,
			fetched_at = excluded.fetched_at`,
		source, raw, nowRFC3339()); err != nil {
		return err
	}
	// Opportunistic bound: drop everything but the newest N by fetched_at. Cheap
	// (runs only on a cache write) and keeps the table from growing forever.
	// Tie-break by rowid (insert order) so "newest" is deterministic even when
	// many entries share the same second-granularity fetched_at.
	_, err := s.db.Exec(`
		DELETE FROM nodepack_cache WHERE rowid NOT IN (
			SELECT rowid FROM nodepack_cache ORDER BY fetched_at DESC, rowid DESC LIMIT ?
		)`, nodePackCacheMaxRows)
	return err
}
