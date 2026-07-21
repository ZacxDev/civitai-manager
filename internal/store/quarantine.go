package store

import (
	"database/sql"
	"errors"
	"time"
)

// QuarantineBatch is a row of the quarantine_batches table: one soft-delete
// action that moved a set of flagged files into a timestamped trash dir.
type QuarantineBatch struct {
	ID         int64
	CreatedAt  time.Time
	TrashDir   string
	Manifest   string
	Reason     string
	RestoredAt *time.Time
}

// Restored reports whether the batch has been fully restored.
func (b QuarantineBatch) Restored() bool { return b.RestoredAt != nil && !b.RestoredAt.IsZero() }

// QuarantinedFile is a row of the quarantined_files table: one moved file within
// a batch, with the metadata needed to undo the move.
type QuarantinedFile struct {
	ID           int64
	BatchID      int64
	OriginalPath string
	TrashPath    string
	Reason       string
	IsSidecar    bool
	SHA256       string
	SizeBytes    int64
	MovedAt      time.Time
	RestoredAt   *time.Time
}

// CreateQuarantineBatch inserts a batch header and returns its id.
func (s *Store) CreateQuarantineBatch(trashDir, manifest, reason string) (int64, error) {
	res, err := s.db.Exec(`INSERT INTO quarantine_batches (created_at, trash_dir, manifest, reason)
		VALUES (?, ?, ?, ?)`, nowRFC3339(), trashDir, manifest, reason)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// AddQuarantinedFile records one moved file in a batch and returns its id.
func (s *Store) AddQuarantinedFile(f QuarantinedFile) (int64, error) {
	res, err := s.db.Exec(`INSERT INTO quarantined_files
		(batch_id, original_path, trash_path, reason, is_sidecar, sha256, size_bytes, moved_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		f.BatchID, f.OriginalPath, f.TrashPath, f.Reason, boolToInt(f.IsSidecar),
		nullStr(f.SHA256), f.SizeBytes, nowRFC3339())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// SetBatchManifest updates a batch's manifest path (written after the moves so
// the manifest can enumerate the moved files).
func (s *Store) SetBatchManifest(batchID int64, manifest string) error {
	_, err := s.db.Exec(`UPDATE quarantine_batches SET manifest = ? WHERE id = ?`, manifest, batchID)
	return err
}

// ListQuarantineBatches returns all batches, newest first.
func (s *Store) ListQuarantineBatches() ([]QuarantineBatch, error) {
	rows, err := s.db.Query(`SELECT id, created_at, trash_dir, manifest, reason, restored_at
		FROM quarantine_batches ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []QuarantineBatch
	for rows.Next() {
		b, err := scanBatch(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// GetQuarantineBatch fetches one batch by id, or ErrNotFound.
func (s *Store) GetQuarantineBatch(id int64) (*QuarantineBatch, error) {
	row := s.db.QueryRow(`SELECT id, created_at, trash_dir, manifest, reason, restored_at
		FROM quarantine_batches WHERE id = ?`, id)
	b, err := scanBatch(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &b, nil
}

func scanBatch(sc scanner) (QuarantineBatch, error) {
	var (
		b          QuarantineBatch
		createdAt  string
		restoredAt sql.NullString
	)
	if err := sc.Scan(&b.ID, &createdAt, &b.TrashDir, &b.Manifest, &b.Reason, &restoredAt); err != nil {
		return QuarantineBatch{}, err
	}
	b.CreatedAt = parseTime(createdAt)
	if restoredAt.Valid {
		if t := parseTime(restoredAt.String); !t.IsZero() {
			b.RestoredAt = &t
		}
	}
	return b, nil
}

// ListQuarantinedFiles returns the files moved in a batch, in move order.
func (s *Store) ListQuarantinedFiles(batchID int64) ([]QuarantinedFile, error) {
	rows, err := s.db.Query(`SELECT id, batch_id, original_path, trash_path, reason,
		is_sidecar, sha256, size_bytes, moved_at, restored_at
		FROM quarantined_files WHERE batch_id = ? ORDER BY id ASC`, batchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []QuarantinedFile
	for rows.Next() {
		var (
			f          QuarantinedFile
			reason     string
			sidecar    int
			sha        sql.NullString
			movedAt    string
			restoredAt sql.NullString
		)
		if err := rows.Scan(&f.ID, &f.BatchID, &f.OriginalPath, &f.TrashPath, &reason,
			&sidecar, &sha, &f.SizeBytes, &movedAt, &restoredAt); err != nil {
			return nil, err
		}
		f.Reason = reason
		f.IsSidecar = sidecar != 0
		f.SHA256 = sha.String
		f.MovedAt = parseTime(movedAt)
		if restoredAt.Valid {
			if t := parseTime(restoredAt.String); !t.IsZero() {
				f.RestoredAt = &t
			}
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// MarkFileRestored records that a quarantined file was moved back to its origin.
func (s *Store) MarkFileRestored(fileID int64) error {
	_, err := s.db.Exec(`UPDATE quarantined_files SET restored_at = ? WHERE id = ?`,
		nowRFC3339(), fileID)
	return err
}

// MarkBatchRestored records that an entire batch was restored.
func (s *Store) MarkBatchRestored(batchID int64) error {
	_, err := s.db.Exec(`UPDATE quarantine_batches SET restored_at = ? WHERE id = ?`,
		nowRFC3339(), batchID)
	return err
}
