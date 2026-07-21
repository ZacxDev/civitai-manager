package store

import (
	"database/sql"
	"errors"
	"time"
)

// localFileCols is the read column list, exposing the SQLite rowid as ID so the
// library CLI/UI can reference a file by a stable integer id.
const localFileCols = `rowid, path, sha256, autov2, model_id, version_id,
	size_bytes, is_superseded, mtime, status, candidate_reason, kind, matched_at`

func scanLocalFile(sc scanner) (LocalFile, error) {
	var (
		lf        LocalFile
		sha       sql.NullString
		autov2    sql.NullString
		modelID   sql.NullInt64
		versionID sql.NullInt64
		mtime     sql.NullString
		matchedAt sql.NullString
	)
	if err := sc.Scan(&lf.ID, &lf.Path, &sha, &autov2, &modelID, &versionID,
		&lf.SizeBytes, &lf.IsSuperseded, &mtime, &lf.Status, &lf.CandidateReason,
		&lf.Kind, &matchedAt); err != nil {
		return LocalFile{}, err
	}
	lf.SHA256 = sha.String
	lf.AutoV2 = autov2.String
	if modelID.Valid {
		v := int(modelID.Int64)
		lf.ModelID = &v
	}
	if versionID.Valid {
		v := int(versionID.Int64)
		lf.VersionID = &v
	}
	if mtime.Valid {
		if t := parseTimeNano(mtime.String); !t.IsZero() {
			lf.Mtime = &t
		}
	}
	if matchedAt.Valid {
		if t := parseTime(matchedAt.String); !t.IsZero() {
			lf.MatchedAt = &t
		}
	}
	return lf, nil
}

// UpsertLocalFile records (or updates) a file in the local library index, keyed
// by path. It preserves the incremental-scan cache fields (mtime), the match
// status, the candidate flag, and the kind.
func (s *Store) UpsertLocalFile(lf LocalFile) error {
	if lf.Kind == "" {
		lf.Kind = LocalKindModel
	}
	if lf.Status == "" && lf.ModelID != nil {
		// A file with a resolved model id is, by definition, matched. This keeps
		// the download worker's upsert (which sets no status) correct.
		lf.Status = LocalStatusMatched
	}
	_, err := s.db.Exec(`
		INSERT INTO local_files
			(path, sha256, autov2, model_id, version_id, size_bytes, is_superseded,
			 mtime, status, candidate_reason, kind, matched_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (path) DO UPDATE SET
			sha256 = excluded.sha256,
			autov2 = excluded.autov2,
			model_id = excluded.model_id,
			version_id = excluded.version_id,
			size_bytes = excluded.size_bytes,
			is_superseded = excluded.is_superseded,
			mtime = excluded.mtime,
			status = excluded.status,
			candidate_reason = excluded.candidate_reason,
			kind = excluded.kind,
			matched_at = excluded.matched_at`,
		lf.Path, nullStr(lf.SHA256), nullStr(lf.AutoV2),
		nullInt(lf.ModelID), nullInt(lf.VersionID), lf.SizeBytes,
		boolToInt(lf.IsSuperseded), nullTimeNanoStr(lf.Mtime), lf.Status,
		lf.CandidateReason, lf.Kind, nullTimeStr(orNow(lf.MatchedAt)))
	return err
}

// GetLocalFileByPath fetches one indexed file by path, or (nil, nil) if absent.
func (s *Store) GetLocalFileByPath(path string) (*LocalFile, error) {
	row := s.db.QueryRow(`SELECT `+localFileCols+` FROM local_files WHERE path = ?`, path)
	lf, err := scanLocalFile(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &lf, nil
}

// GetLocalFile fetches one indexed file by id (rowid), or ErrNotFound.
func (s *Store) GetLocalFile(id int64) (*LocalFile, error) {
	row := s.db.QueryRow(`SELECT `+localFileCols+` FROM local_files WHERE rowid = ?`, id)
	lf, err := scanLocalFile(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &lf, nil
}

// ListLocalFiles returns every indexed file ordered by path.
func (s *Store) ListLocalFiles() ([]LocalFile, error) {
	return s.queryLocalFiles(`SELECT ` + localFileCols + ` FROM local_files ORDER BY path`)
}

// ListCandidates returns flagged deletion candidates, optionally filtered to a
// single reason (empty = all candidates), ordered by path.
func (s *Store) ListCandidates(reason string) ([]LocalFile, error) {
	if reason == "" {
		return s.queryLocalFiles(`SELECT ` + localFileCols + `
			FROM local_files WHERE candidate_reason <> '' ORDER BY path`)
	}
	return s.queryLocalFiles(`SELECT `+localFileCols+`
		FROM local_files WHERE candidate_reason = ? ORDER BY path`, reason)
}

func (s *Store) queryLocalFiles(q string, args ...any) ([]LocalFile, error) {
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LocalFile
	for rows.Next() {
		lf, err := scanLocalFile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, lf)
	}
	return out, rows.Err()
}

// SetCandidateReason flags (or, with an empty reason, clears) a file's
// deletion-candidate status. It keeps the legacy is_superseded flag in sync.
func (s *Store) SetCandidateReason(id int64, reason string) error {
	_, err := s.db.Exec(`UPDATE local_files
		SET candidate_reason = ?, is_superseded = ? WHERE rowid = ?`,
		reason, boolToInt(reason == CandidateSuperseded), id)
	return err
}

// ClearCandidates resets every candidate flag (used at the start of a fresh
// analysis pass so stale flags never linger).
func (s *Store) ClearCandidates() error {
	_, err := s.db.Exec(`UPDATE local_files SET candidate_reason = '', is_superseded = 0
		WHERE candidate_reason <> '' OR is_superseded <> 0`)
	return err
}

// DeleteLocalFileByPath removes a file's index row (e.g. after it is quarantined
// off disk). A missing path is not an error.
func (s *Store) DeleteLocalFileByPath(path string) error {
	return execDeleteLocalFileByPath(s.db, path)
}

// CountLocalFiles returns how many files are indexed.
func (s *Store) CountLocalFiles() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM local_files`).Scan(&n)
	return n, err
}

// ActiveDownloadForDest reports whether the download queue has an in-flight row
// (queued or downloading) targeting destPath. The library analyzer uses it to
// decide whether a stray `.part` file belongs to a live download (keep) or is
// abandoned (flag broken).
func (s *Store) ActiveDownloadForDest(destPath string) (bool, error) {
	var one int
	err := s.db.QueryRow(`SELECT 1 FROM download_queue
		WHERE dest_path = ? AND status IN (?, ?) LIMIT 1`,
		destPath, string(StatusQueued), string(StatusDownloading)).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func orNow(t *time.Time) *time.Time {
	if t == nil {
		now := time.Now().UTC()
		return &now
	}
	return t
}
