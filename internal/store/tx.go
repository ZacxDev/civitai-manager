package store

import (
	"database/sql"
)

// dbtx is the subset of *sql.DB / *sql.Tx the shared quarantine helpers use, so
// the same SQL runs both auto-committed (via the DB handle) and inside an
// explicit transaction (via a *sql.Tx).
type dbtx interface {
	Exec(query string, args ...any) (sql.Result, error)
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

// Tx is a store transaction that exposes the mutations a quarantine batch makes,
// so the batch header, its per-file quarantine records, and the matching
// local-file index deletions commit atomically (all-or-nothing). It also lets
// the caller run the keep-≥1-copy safety check against a consistent snapshot
// read inside the same transaction, so two concurrent batches can never each
// remove the last surviving copy of a duplicate set.
type Tx struct {
	tx   *sql.Tx
	done bool
}

// BeginTx opens a store transaction.
func (s *Store) BeginTx() (*Tx, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	return &Tx{tx: tx}, nil
}

// Commit commits the transaction. It is a no-op after the first Commit/Rollback.
func (t *Tx) Commit() error {
	if t.done {
		return nil
	}
	t.done = true
	return t.tx.Commit()
}

// Rollback rolls the transaction back. It is a no-op after Commit/Rollback, so
// it is safe to `defer tx.Rollback()` and still Commit on the happy path.
func (t *Tx) Rollback() error {
	if t.done {
		return nil
	}
	t.done = true
	return t.tx.Rollback()
}

// CreateQuarantineBatch inserts a batch header within the transaction.
func (t *Tx) CreateQuarantineBatch(trashDir, manifest, reason string) (int64, error) {
	return execCreateQuarantineBatch(t.tx, trashDir, manifest, reason)
}

// AddQuarantinedFile records one moved file within the transaction.
func (t *Tx) AddQuarantinedFile(f QuarantinedFile) (int64, error) {
	return execAddQuarantinedFile(t.tx, f)
}

// DeleteLocalFileByPath removes a local-file index row within the transaction.
func (t *Tx) DeleteLocalFileByPath(path string) error {
	return execDeleteLocalFileByPath(t.tx, path)
}

// ListModelFiles returns every model-kind index row, read within the
// transaction (the consistent snapshot the keep-≥1-copy safety check uses).
func (t *Tx) ListModelFiles() ([]LocalFile, error) {
	return queryModelFiles(t.tx)
}

// --- shared statements (run against a DB handle or a Tx) ---

func execCreateQuarantineBatch(q dbtx, trashDir, manifest, reason string) (int64, error) {
	res, err := q.Exec(`INSERT INTO quarantine_batches (created_at, trash_dir, manifest, reason)
		VALUES (?, ?, ?, ?)`, nowRFC3339(), trashDir, manifest, reason)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func execAddQuarantinedFile(q dbtx, f QuarantinedFile) (int64, error) {
	res, err := q.Exec(`INSERT INTO quarantined_files
		(batch_id, original_path, trash_path, reason, is_sidecar, sha256, size_bytes, moved_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		f.BatchID, f.OriginalPath, f.TrashPath, f.Reason, boolToInt(f.IsSidecar),
		nullStr(f.SHA256), f.SizeBytes, nowRFC3339())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func execDeleteLocalFileByPath(q dbtx, path string) error {
	_, err := q.Exec(`DELETE FROM local_files WHERE path = ?`, path)
	return err
}

func queryModelFiles(q dbtx) ([]LocalFile, error) {
	rows, err := q.Query(`SELECT ` + localFileCols + ` FROM local_files
		WHERE kind = 'model' ORDER BY path`)
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
