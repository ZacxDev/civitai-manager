package store

import "time"

// UpsertLocalFile records (or updates) a file in the local library index. This
// is written by the download worker after a verified download so the library is
// queryable; the full `scan` reconciliation is a post-MVP feature.
func (s *Store) UpsertLocalFile(lf LocalFile) error {
	_, err := s.db.Exec(`
		INSERT INTO local_files (path, sha256, autov2, model_id, version_id, size_bytes, is_superseded, matched_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (path) DO UPDATE SET
			sha256 = excluded.sha256,
			autov2 = excluded.autov2,
			model_id = excluded.model_id,
			version_id = excluded.version_id,
			size_bytes = excluded.size_bytes,
			is_superseded = excluded.is_superseded,
			matched_at = excluded.matched_at`,
		lf.Path, nullStr(lf.SHA256), nullStr(lf.AutoV2),
		nullInt(lf.ModelID), nullInt(lf.VersionID), lf.SizeBytes,
		boolToInt(lf.IsSuperseded), nullTimeStr(orNow(lf.MatchedAt)))
	return err
}

// CountLocalFiles returns how many files are indexed.
func (s *Store) CountLocalFiles() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM local_files`).Scan(&n)
	return n, err
}

func orNow(t *time.Time) *time.Time {
	if t == nil {
		now := time.Now().UTC()
		return &now
	}
	return t
}
