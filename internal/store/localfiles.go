package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"time"
)

// localFileCols is the read column list, exposing the SQLite rowid as ID so the
// library CLI/UI can reference a file by a stable integer id.
const localFileCols = `rowid, path, sha256, autov2, model_id, version_id,
	size_bytes, is_superseded, mtime, status, candidate_reason, kind, matched_at, scan_root`

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
		&lf.Kind, &matchedAt, &lf.ScanRoot); err != nil {
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
			 mtime, status, candidate_reason, kind, matched_at, scan_root)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
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
			matched_at = excluded.matched_at,
			-- Preserve a previously-recorded scan_root when the writer does not set
			-- one (the download worker upserts without a scan_root; a re-scan sets
			-- it). Never clobber a real root with a blank.
			scan_root = CASE WHEN excluded.scan_root <> '' THEN excluded.scan_root
			                 ELSE local_files.scan_root END`,
		lf.Path, nullStr(lf.SHA256), nullStr(lf.AutoV2),
		nullInt(lf.ModelID), nullInt(lf.VersionID), lf.SizeBytes,
		boolToInt(lf.IsSuperseded), nullTimeNanoStr(lf.Mtime), lf.Status,
		lf.CandidateReason, lf.Kind, nullTimeStr(orNow(lf.MatchedAt)), lf.ScanRoot)
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

// ListLocalFilesByModel returns the indexed files for a single model id, ordered
// by path. It hits idx_local_files_model (a partial index on non-null model_id)
// instead of scanning the whole table — the per-model hot path on the library's
// matched cards. The returned rows are the SAME shape as ListLocalFiles; callers
// still apply any kind filter (e.g. LocalKindModel) in Go.
func (s *Store) ListLocalFilesByModel(modelID int) ([]LocalFile, error) {
	return s.queryLocalFiles(`SELECT `+localFileCols+`
		FROM local_files WHERE model_id = ? ORDER BY path`, modelID)
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

// HasLocalFileNamed reports whether the library indexes any file whose BASENAME
// matches basename (case-insensitively). It backs the ComfyUI run preflight's
// "do I have this referenced model on disk?" check: a workflow references a model
// by bare filename, and a match here (under any scanned directory) means the file
// is present even if ComfyUI's own models/ folder does not yet list it. The SQL
// LIKE narrows candidate rows by path suffix; the exact case-insensitive basename
// compare in Go is the real gate (so "vae.safetensors" cannot match
// "myvae.safetensors").
func (s *Store) HasLocalFileNamed(basename string) (bool, error) {
	basename = strings.TrimSpace(basename)
	if basename == "" {
		return false, nil
	}
	like := "%" + string(filepath.Separator) + basename
	rows, err := s.db.Query(
		`SELECT path FROM local_files WHERE path LIKE ? OR path = ?`, like, basename)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	lowWant := strings.ToLower(basename)
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return false, err
		}
		if strings.ToLower(filepath.Base(path)) == lowWant {
			return true, nil
		}
	}
	return false, rows.Err()
}

// LocalFileByBasename returns the indexed file whose BASENAME matches basename
// (case-insensitively), for the ComfyUI cloud-run resource resolver: a workflow
// references a model by bare filename, and this maps it back to the file's
// civitai model/version linkage. It mirrors HasLocalFileNamed's basename match
// (SQL LIKE narrows by path suffix; the exact case-insensitive basename compare
// in Go is the real gate) but returns the matched *LocalFile.
//
// Ambiguity guard: when MULTIPLE indexed files share the basename but disagree on
// their (model_id, version_id) linkage, the match is AMBIGUOUS and cannot be
// resolved to a single URN — this returns (nil, nil) (treated as unresolved by the
// caller) rather than guessing. Rows that agree (or a lone match) return that file.
// No match returns (nil, nil).
func (s *Store) LocalFileByBasename(basename string) (*LocalFile, error) {
	basename = strings.TrimSpace(basename)
	if basename == "" {
		return nil, nil
	}
	like := "%" + string(filepath.Separator) + basename
	files, err := s.queryLocalFiles(
		`SELECT `+localFileCols+` FROM local_files WHERE path LIKE ? OR path = ?`, like, basename)
	if err != nil {
		return nil, err
	}
	lowWant := strings.ToLower(basename)
	var match *LocalFile
	for i := range files {
		if strings.ToLower(filepath.Base(files[i].Path)) != lowWant {
			continue // LIKE false-positive (e.g. "myvae.safetensors" for "vae.safetensors")
		}
		f := files[i]
		if match == nil {
			match = &f
			continue
		}
		if !sameLinkage(match, &f) {
			return nil, nil // ambiguous — differing model/version linkage
		}
	}
	return match, nil
}

// sameLinkage reports whether two local files carry the same (model_id, version_id)
// civitai linkage (both nil counts as same).
func sameLinkage(a, b *LocalFile) bool {
	return intPtrEq(a.ModelID, b.ModelID) && intPtrEq(a.VersionID, b.VersionID)
}

func intPtrEq(a, b *int) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
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
