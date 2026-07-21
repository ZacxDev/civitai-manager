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

// Enqueue inserts a queued download row and returns its id.
func (s *Store) Enqueue(it QueueItem) (int64, error) {
	now := formatTime(time.Now().UTC())
	if it.Status == "" {
		it.Status = StatusQueued
	}
	res, err := s.db.Exec(`
		INSERT INTO download_queue
			(subscription_id, model_id, version_id, file_id, file_name, download_url,
			 dest_path, status, bytes_done, size_kb, sha256_expected, not_before, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		nullInt64(it.SubscriptionID), it.ModelID, it.VersionID, it.FileID,
		it.FileName, it.DownloadURL, it.DestPath, string(it.Status),
		it.BytesDone, it.SizeKB, nullStr(it.SHA256Expected), nullTimeStr(it.NotBefore), now, now)
	if err != nil {
		return 0, fmt.Errorf("enqueue download: %w", err)
	}
	return res.LastInsertId()
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
	var id int64
	err = tx.QueryRow(`SELECT id FROM download_queue
		WHERE status = ? AND (not_before IS NULL OR not_before <= ?)
		ORDER BY id ASC LIMIT 1`, string(StatusQueued), now).Scan(&id)
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
