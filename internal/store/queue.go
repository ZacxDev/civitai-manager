package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

const queueCols = `id, subscription_id, model_id, version_id, file_id, file_name,
	download_url, dest_path, status, bytes_done, size_kb, sha256_expected,
	sha256_actual, attempts, last_error, not_before, created_at, updated_at`

func scanQueueItem(sc scanner) (QueueItem, error) {
	var (
		it        QueueItem
		subID     sql.NullInt64
		shaExp    sql.NullString
		shaAct    sql.NullString
		lastErr   sql.NullString
		notBefore sql.NullString
		status    string
		createdAt string
		updatedAt string
	)
	if err := sc.Scan(&it.ID, &subID, &it.ModelID, &it.VersionID, &it.FileID,
		&it.FileName, &it.DownloadURL, &it.DestPath, &status, &it.BytesDone,
		&it.SizeKB, &shaExp, &shaAct, &it.Attempts, &lastErr, &notBefore,
		&createdAt, &updatedAt); err != nil {
		return QueueItem{}, err
	}
	if subID.Valid {
		it.SubscriptionID = &subID.Int64
	}
	it.Status = QueueStatus(status)
	it.SHA256Expected = shaExp.String
	it.SHA256Actual = shaAct.String
	it.LastError = lastErr.String
	if notBefore.Valid {
		t := parseTime(notBefore.String)
		if !t.IsZero() {
			it.NotBefore = &t
		}
	}
	it.CreatedAt = parseTime(createdAt)
	it.UpdatedAt = parseTime(updatedAt)
	return it, nil
}

// Enqueue inserts a queued download row. The insert is guarded by the partial
// unique index ux_dlq_active (see migration 0004): a (version_id, file_id) that
// already has a row in an ACTIVE status ('queued'/'downloading'/'done') hits
// ON CONFLICT DO NOTHING and is skipped, so two racing enqueues can no longer
// create duplicate rows. This is the atomic replacement for the previous
// non-atomic ActiveQueueItemExists check-then-insert.
//
// It returns (id, inserted, err): inserted is true only when a NEW row was
// created (id is its rowid); inserted is false when the row was skipped as a
// duplicate (id is 0). A terminal 'failed'/'skipped' row does not block a fresh
// enqueue, so a retry after failure still inserts.
func (s *Store) Enqueue(it QueueItem) (int64, bool, error) {
	now := formatTime(time.Now().UTC())
	if it.Status == "" {
		it.Status = StatusQueued
	}
	res, err := s.db.Exec(`
		INSERT INTO download_queue
			(subscription_id, model_id, version_id, file_id, file_name, download_url,
			 dest_path, status, bytes_done, size_kb, sha256_expected, not_before, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (version_id, file_id)
			WHERE status IN ('queued', 'downloading', 'done')
			DO NOTHING`,
		nullInt64(it.SubscriptionID), it.ModelID, it.VersionID, it.FileID,
		it.FileName, it.DownloadURL, it.DestPath, string(it.Status),
		it.BytesDone, it.SizeKB, nullStr(it.SHA256Expected), nullTimeStr(it.NotBefore), now, now)
	if err != nil {
		return 0, false, fmt.Errorf("enqueue download: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return 0, false, fmt.Errorf("enqueue download: %w", err)
	}
	if affected == 0 {
		// ON CONFLICT DO NOTHING: an active row for this (version_id, file_id)
		// already exists. Not inserted.
		return 0, false, nil
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, false, fmt.Errorf("enqueue download: %w", err)
	}
	return id, true, nil
}

// ActiveQueueItemExists reports whether a (version_id, file_id) already has a
// row in one of the given statuses -- the dedup guard so a re-poll or restart
// never enqueues the same file twice.
func (s *Store) ActiveQueueItemExists(versionID, fileID int, statuses ...QueueStatus) (bool, error) {
	if len(statuses) == 0 {
		statuses = []QueueStatus{StatusQueued, StatusDownloading, StatusDone}
	}
	placeholders := make([]string, len(statuses))
	args := []any{versionID, fileID}
	for i, st := range statuses {
		placeholders[i] = "?"
		args = append(args, string(st))
	}
	q := fmt.Sprintf(`SELECT 1 FROM download_queue
		WHERE version_id = ? AND file_id = ? AND status IN (%s) LIMIT 1`,
		strings.Join(placeholders, ", "))
	var one int
	err := s.db.QueryRow(q, args...).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// FindActiveQueueItem returns the ACTIVE ('queued'/'downloading'/'done') queue
// row for a (version_id, file_id), or (nil, nil) when none exists. It is the
// row-returning counterpart to ActiveQueueItemExists: the partial-unique index
// ux_dlq_active guarantees at most one active row per (version_id, file_id), so
// this returns that single row. Used by the backfill path to inspect WHY a
// re-enqueue was skipped (e.g. a 'done' row whose file is missing on disk), so
// the CLI can distinguish "already on disk" from "downloaded then deleted".
func (s *Store) FindActiveQueueItem(versionID, fileID int) (*QueueItem, error) {
	row := s.db.QueryRow(`SELECT `+queueCols+` FROM download_queue
		WHERE version_id = ? AND file_id = ? AND status IN ('queued', 'downloading', 'done')
		ORDER BY id DESC LIMIT 1`, versionID, fileID)
	it, err := scanQueueItem(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &it, nil
}

// ListQueue returns queue rows, optionally filtered to the given statuses,
// newest first.
func (s *Store) ListQueue(statuses ...QueueStatus) ([]QueueItem, error) {
	q := `SELECT ` + queueCols + ` FROM download_queue`
	var args []any
	if len(statuses) > 0 {
		placeholders := make([]string, len(statuses))
		for i, st := range statuses {
			placeholders[i] = "?"
			args = append(args, string(st))
		}
		q += ` WHERE status IN (` + strings.Join(placeholders, ", ") + `)`
	}
	q += ` ORDER BY id DESC`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []QueueItem
	for rows.Next() {
		it, err := scanQueueItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// ClaimNextQueued atomically transitions the oldest queued row to downloading
// (incrementing attempts) and returns it. It returns (nil, nil) when the queue
// is empty. The claim is a single UPDATE ... WHERE id = (SELECT ...) so two
// workers never claim the same row.
func (s *Store) ClaimNextQueued() (*QueueItem, error) {
	return s.claimNext(nil, nil)
}

// ClaimNextQueuedForSubscription is ClaimNextQueued scoped to a single
// subscription: it only claims a due queued row whose subscription_id matches
// subID. Used by `subscribe --backfill-latest` so the synchronous drain never
// picks up another subscription's queued backlog (e.g. rows left by a prior
// `check` without --download, or auto-download rows whose jitter has elapsed).
func (s *Store) ClaimNextQueuedForSubscription(subID int64) (*QueueItem, error) {
	return s.claimNext(&subID, nil)
}

// ClaimNextQueuedForIDs is ClaimNextQueued scoped to an explicit set of row ids:
// it only claims a due queued row whose id is in ids. Used by `verify --repair`
// so the synchronous drain touches ONLY the rows the repair just re-enqueued and
// never claims an unrelated queued backlog (e.g. another subscription's
// jitter-elapsed auto-downloads, or a prior `check`'s queued rows). An empty ids
// set matches nothing (returns nil, nil).
func (s *Store) ClaimNextQueuedForIDs(ids []int64) (*QueueItem, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	return s.claimNext(nil, ids)
}

// claimNext is the shared claim body for ClaimNextQueued and its scoped
// variants. When subID is non-nil the candidate row is additionally filtered to
// that subscription; when ids is non-empty it is filtered to that id set. The
// not_before gating, attempt-increment, and single-transaction claim are
// otherwise identical.
func (s *Store) claimNext(subID *int64, ids []int64) (*QueueItem, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	// "now" is taken once, up front, and used for both the not_before gate and
	// the claim's updated_at so a single wall-clock read governs the claim.
	now := formatTime(time.Now().UTC())

	// Skip rows whose not_before gate is still in the future: they are not yet
	// due (fleet anti-stampede jitter), so the worker moves on to the next
	// eligible row. NULL not_before means immediately claimable.
	query := `SELECT id FROM download_queue
		WHERE status = ? AND (not_before IS NULL OR not_before <= ?)`
	args := []any{string(StatusQueued), now}
	if subID != nil {
		query += ` AND subscription_id = ?`
		args = append(args, *subID)
	}
	if len(ids) > 0 {
		placeholders := make([]string, len(ids))
		for i, rid := range ids {
			placeholders[i] = "?"
			args = append(args, rid)
		}
		query += ` AND id IN (` + strings.Join(placeholders, ", ") + `)`
	}
	query += ` ORDER BY id ASC LIMIT 1`

	var id int64
	err = tx.QueryRow(query, args...).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`UPDATE download_queue
		SET status = ?, attempts = attempts + 1, updated_at = ? WHERE id = ?`,
		string(StatusDownloading), now, id); err != nil {
		return nil, err
	}
	row := tx.QueryRow(`SELECT `+queueCols+` FROM download_queue WHERE id = ?`, id)
	it, err := scanQueueItem(row)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &it, nil
}

// GetQueueItem fetches one queue row by id.
func (s *Store) GetQueueItem(id int64) (*QueueItem, error) {
	row := s.db.QueryRow(`SELECT `+queueCols+` FROM download_queue WHERE id = ?`, id)
	it, err := scanQueueItem(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &it, nil
}

// UpdateProgress records bytes streamed so far for a downloading row.
func (s *Store) UpdateProgress(id, bytesDone int64) error {
	_, err := s.db.Exec(`UPDATE download_queue SET bytes_done = ?, updated_at = ? WHERE id = ?`,
		bytesDone, formatTime(time.Now().UTC()), id)
	return err
}

// CompleteDownload marks a row done, recording the verified hash and final size.
func (s *Store) CompleteDownload(id int64, sha256Actual string, bytesDone int64) error {
	_, err := s.db.Exec(`UPDATE download_queue
		SET status = ?, sha256_actual = ?, bytes_done = ?, last_error = NULL, updated_at = ?
		WHERE id = ?`,
		string(StatusDone), nullStr(sha256Actual), bytesDone, formatTime(time.Now().UTC()), id)
	return err
}

// FailDownload marks a row failed with an error message. sha256Actual may be
// empty; on a hash mismatch the caller records what it computed.
func (s *Store) FailDownload(id int64, message, sha256Actual string) error {
	_, err := s.db.Exec(`UPDATE download_queue
		SET status = ?, last_error = ?, sha256_actual = ?, updated_at = ?
		WHERE id = ?`,
		string(StatusFailed), nullStr(message), nullStr(sha256Actual),
		formatTime(time.Now().UTC()), id)
	return err
}

// SetQueueStatus sets a row's status (used for skipped, or requeue).
func (s *Store) SetQueueStatus(id int64, status QueueStatus) error {
	_, err := s.db.Exec(`UPDATE download_queue SET status = ?, updated_at = ? WHERE id = ?`,
		string(status), formatTime(time.Now().UTC()), id)
	return err
}

// RequeueWithError returns a row to the queued state, recording the transient
// error that will be retried. Used for bounded retry of network/IO failures
// (as opposed to FailDownload, which is terminal, e.g. a hash mismatch).
func (s *Store) RequeueWithError(id int64, message string) error {
	_, err := s.db.Exec(`UPDATE download_queue
		SET status = ?, last_error = ?, updated_at = ? WHERE id = ?`,
		string(StatusQueued), nullStr(message), formatTime(time.Now().UTC()), id)
	return err
}

// RequeueCanceled returns a row interrupted by a graceful shutdown to the
// queued state WITHOUT counting the attempt: it undoes the increment
// ClaimNextQueued applied when the row was claimed, so a download aborted by
// SIGINT/SIGTERM is retried cleanly on restart rather than being marked failed
// (with its version already in the seen ledger, never to be re-downloaded).
func (s *Store) RequeueCanceled(id int64) error {
	_, err := s.db.Exec(`UPDATE download_queue
		SET status = ?, attempts = MAX(attempts - 1, 0), last_error = NULL, updated_at = ?
		WHERE id = ?`,
		string(StatusQueued), formatTime(time.Now().UTC()), id)
	return err
}

// RequeueDone transitions a completed ('done') row back to 'queued' so the
// download worker re-fetches it, resetting the per-attempt download state
// (bytes_done, sha256_actual, last_error, attempts, not_before). It is the
// re-enqueue primitive behind `verify --repair`: a file the tool downloaded but
// the user has since deleted/moved (or that no longer matches its hash) is
// otherwise un-recoverable because its version is already in seen_versions.
//
// done→queued keeps the row inside the ACTIVE status set, so the ux_dlq_active
// partial-unique index sees no new (version_id, file_id) — a row never conflicts
// with itself on UPDATE, and the index guarantees at most one active row exists.
// The guard `AND status = 'done'` makes it a no-op (ErrNotFound) on any other
// status, so it can never disturb an in-flight download.
func (s *Store) RequeueDone(id int64) error {
	return s.requeueForRepair(id, StatusDone)
}

// RequeueFailed transitions a terminally 'failed' row back to 'queued' so the
// download worker re-attempts it, resetting the per-attempt download state. It
// is the sibling of RequeueDone for the repair path: `verify --repair` on a row
// whose PREVIOUS repair download failed (404/gone/exhausted retries/hash
// mismatch) must be able to re-attempt it, otherwise a failed repair strands the
// file forever (its version is already in seen_versions, and ListQueue(done)
// alone would never surface it again).
//
// failed→queued moves the row INTO the active status set. The ux_dlq_active
// partial-unique index therefore applies; the guard `AND status = 'failed'`
// keeps it from disturbing an in-flight download, and in the repair scenario the
// row is the sole row for its (version_id, file_id) so the index sees no
// conflict. Should a separate active row exist for the same (version_id,
// file_id) the UPDATE surfaces the constraint error to the caller rather than
// silently corrupting state.
func (s *Store) RequeueFailed(id int64) error {
	return s.requeueForRepair(id, StatusFailed)
}

// requeueForRepair is the shared body behind RequeueDone/RequeueFailed: it
// transitions a row in the given `from` status back to 'queued', resetting the
// per-attempt download state (bytes_done, sha256_actual, last_error, attempts,
// not_before). The `AND status = from` guard makes it a no-op (ErrNotFound) on
// any other status, so it can never disturb an in-flight download.
func (s *Store) requeueForRepair(id int64, from QueueStatus) error {
	res, err := s.db.Exec(`UPDATE download_queue
		SET status = ?, bytes_done = 0, sha256_actual = NULL, last_error = NULL,
			attempts = 0, not_before = NULL, updated_at = ?
		WHERE id = ? AND status = ?`,
		string(StatusQueued), formatTime(time.Now().UTC()), id, string(from))
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// RequeueInterrupted resets any rows left in the downloading state (e.g. a
// crash mid-transfer) back to queued so the worker re-processes them. Returns
// the number of rows reset.
func (s *Store) RequeueInterrupted() (int64, error) {
	res, err := s.db.Exec(`UPDATE download_queue SET status = ?, updated_at = ?
		WHERE status = ?`,
		string(StatusQueued), formatTime(time.Now().UTC()), string(StatusDownloading))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func nullInt64(p *int64) sql.NullInt64 {
	if p == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: *p, Valid: true}
}
