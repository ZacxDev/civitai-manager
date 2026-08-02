package store

import (
	"database/sql"
	"errors"
	"time"
)

// ComfyModelCacheEntry holds the cached /object_info JSON payload. The full
// ObjectInfo is stored so that per-basename lookups can be derived at query
// time without a live ComfyUI connection.
type ComfyModelCacheEntry struct {
	ObjectInfoJSON []byte
	UpdatedAt      time.Time
}

// GetComfyObjectInfo returns the cached /object_info JSON, or (nil, nil) when
// the cache is empty.
func (s *Store) GetComfyObjectInfo() (*ComfyModelCacheEntry, error) {
	row := s.db.QueryRow(
		`SELECT object_info_json, updated_at FROM comfy_model_cache WHERE id = 'singleton'`)
	var (
		e         ComfyModelCacheEntry
		updatedAt string
	)
	if err := row.Scan(&e.ObjectInfoJSON, &updatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	e.UpdatedAt = parseTime(updatedAt)
	return &e, nil
}

// PutComfyObjectInfo upserts the /object_info JSON, stamping updated_at.
func (s *Store) PutComfyObjectInfo(json []byte) error {
	_, err := s.db.Exec(`
		INSERT INTO comfy_model_cache (id, object_info_json, updated_at)
		VALUES ('singleton', ?, ?)
		ON CONFLICT (id) DO UPDATE SET
			object_info_json = excluded.object_info_json,
			updated_at = excluded.updated_at`,
		string(json), nowRFC3339())
	return err
}

// InvalidateComfyModelCache clears the cached /object_info so the next
// resolution triggers a fresh fetch.
func (s *Store) InvalidateComfyModelCache() error {
	_, err := s.db.Exec(`DELETE FROM comfy_model_cache WHERE id = 'singleton'`)
	return err
}
