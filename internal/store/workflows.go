package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// ErrGoldenNeedsVersion is returned by SetGolden when the workflow has no
// attached version_id: a golden workflow is the recommended one FOR a version, so
// it is meaningless (and the partial unique index un-scopable) without one.
var ErrGoldenNeedsVersion = errors.New("cannot set golden without an attached version")

const workflowCols = `id, name, format, graph, source, model_id, version_id,
	base_model, is_golden, resources, source_path, size_bytes, mtime,
	created_at, updated_at`

func scanWorkflow(sc scanner) (Workflow, error) {
	var (
		wf         Workflow
		modelID    sql.NullInt64
		versionID  sql.NullInt64
		baseModel  sql.NullString
		isGolden   int
		resources  sql.NullString
		sourcePath sql.NullString
		mtime      sql.NullString
		createdAt  string
		updatedAt  string
	)
	if err := sc.Scan(&wf.ID, &wf.Name, &wf.Format, &wf.Graph, &wf.Source,
		&modelID, &versionID, &baseModel, &isGolden, &resources,
		&sourcePath, &wf.SizeBytes, &mtime,
		&createdAt, &updatedAt); err != nil {
		return Workflow{}, err
	}
	if modelID.Valid {
		v := int(modelID.Int64)
		wf.ModelID = &v
	}
	if versionID.Valid {
		v := int(versionID.Int64)
		wf.VersionID = &v
	}
	wf.BaseModel = baseModel.String
	wf.IsGolden = isGolden != 0
	if resources.Valid && resources.String != "" {
		// A malformed resources column must not break a read: fall back to no
		// resources rather than failing the whole list.
		var list []string
		if err := json.Unmarshal([]byte(resources.String), &list); err == nil {
			wf.Resources = list
		}
	}
	wf.SourcePath = sourcePath.String
	if mtime.Valid {
		if t := parseTimeNano(mtime.String); !t.IsZero() {
			wf.Mtime = &t
		}
	}
	wf.CreatedAt = parseTime(createdAt)
	wf.UpdatedAt = parseTime(updatedAt)
	return wf, nil
}

// marshalResources renders the resource filename list as a JSON-array TEXT value,
// or SQL NULL when empty.
func marshalResources(res []string) sql.NullString {
	if len(res) == 0 {
		return sql.NullString{}
	}
	b, err := json.Marshal(res)
	if err != nil {
		return sql.NullString{}
	}
	return sql.NullString{String: string(b), Valid: true}
}

// InsertWorkflow inserts a workflow and returns its id. CreatedAt/UpdatedAt are
// set to now when the caller leaves them zero.
func (s *Store) InsertWorkflow(ctx context.Context, wf *Workflow) (int64, error) {
	now := time.Now().UTC()
	if wf.CreatedAt.IsZero() {
		wf.CreatedAt = now
	}
	if wf.UpdatedAt.IsZero() {
		wf.UpdatedAt = now
	}
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO workflows
			(name, format, graph, source, model_id, version_id, base_model,
			 is_golden, resources, source_path, size_bytes, mtime, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		wf.Name, wf.Format, wf.Graph, wf.Source,
		nullInt(wf.ModelID), nullInt(wf.VersionID), nullStr(wf.BaseModel),
		boolToInt(wf.IsGolden), marshalResources(wf.Resources),
		nullStr(wf.SourcePath), wf.SizeBytes, nullTimeNanoStr(wf.Mtime),
		formatTime(wf.CreatedAt), formatTime(wf.UpdatedAt))
	if err != nil {
		return 0, fmt.Errorf("insert workflow: %w", err)
	}
	return res.LastInsertId()
}

// UpsertWorkflowByPath records (or updates) a SCANNED workflow keyed by its
// absolute source_path. On a re-scan (ON CONFLICT(source_path)) it refreshes the
// parsed graph and the incremental-cache fields but DELIBERATELY PRESERVES the
// user's curation: the name is not overwritten once set, a manual model/version
// attachment survives (COALESCE keeps the existing id over the freshly auto-linked
// one), and is_golden is never touched. It reports whether an existing row was
// updated (updated=true) vs. a fresh row inserted.
func (s *Store) UpsertWorkflowByPath(ctx context.Context, wf *Workflow) (id int64, updated bool, err error) {
	if wf.SourcePath == "" {
		return 0, false, errors.New("UpsertWorkflowByPath: empty source_path")
	}
	now := time.Now().UTC()
	if wf.CreatedAt.IsZero() {
		wf.CreatedAt = now
	}
	if wf.UpdatedAt.IsZero() {
		wf.UpdatedAt = now
	}
	// Detect insert-vs-update up front so we can report it (RowsAffected is 1 for
	// both an insert and an ON CONFLICT update under SQLite, so it cannot tell them
	// apart). This read + write pair runs against the single-conn WAL store.
	var existing int64
	err = s.db.QueryRowContext(ctx,
		`SELECT id FROM workflows WHERE source_path = ?`, wf.SourcePath).Scan(&existing)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		updated = false
	case err != nil:
		return 0, false, err
	default:
		updated = true
	}

	res, err := s.db.ExecContext(ctx, `
		INSERT INTO workflows
			(name, format, graph, source, model_id, version_id, base_model,
			 is_golden, resources, source_path, size_bytes, mtime, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(source_path) WHERE source_path IS NOT NULL DO UPDATE SET
			-- Keep a user-set name; only adopt the derived filename on the first insert.
			name = CASE WHEN workflows.name = '' THEN excluded.name ELSE workflows.name END,
			format = excluded.format,
			graph = excluded.graph,
			-- Never clobber a manual attach (or a golden's linkage) with a re-scan's
			-- auto-link: keep the existing id when present.
			model_id = COALESCE(workflows.model_id, excluded.model_id),
			version_id = COALESCE(workflows.version_id, excluded.version_id),
			resources = excluded.resources,
			size_bytes = excluded.size_bytes,
			mtime = excluded.mtime,
			updated_at = excluded.updated_at`,
		wf.Name, wf.Format, wf.Graph, wf.Source,
		nullInt(wf.ModelID), nullInt(wf.VersionID), nullStr(wf.BaseModel),
		boolToInt(wf.IsGolden), marshalResources(wf.Resources),
		nullStr(wf.SourcePath), wf.SizeBytes, nullTimeNanoStr(wf.Mtime),
		formatTime(wf.CreatedAt), formatTime(wf.UpdatedAt))
	if err != nil {
		return 0, false, fmt.Errorf("upsert workflow by path: %w", err)
	}
	if !updated {
		id, err = res.LastInsertId()
		return id, false, err
	}
	return existing, true, nil
}

// GetWorkflowByPath fetches one scanned workflow by its absolute source_path, or
// (nil, nil) if none is recorded (mirrors GetLocalFileByPath).
func (s *Store) GetWorkflowByPath(ctx context.Context, path string) (*Workflow, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+workflowCols+` FROM workflows WHERE source_path = ?`, path)
	wf, err := scanWorkflow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &wf, nil
}

// FindVersionByFileName resolves a referenced model FILENAME (a bare basename,
// e.g. "sdxl.safetensors") to a civitai (model_id, version_id) by matching it
// case-insensitively against the basenames of matched local_files. It powers the
// workflow scanner's auto-link. It returns the first match (ordered by path for
// determinism); ok=false when nothing matches or the basename is blank.
//
// The candidate set is narrowed in SQL to matched files whose path ends in the
// basename (a LIKE on '%'||sep||name), then filtered by an exact case-insensitive
// filepath.Base compare in Go so "foo.safetensors" cannot spuriously match
// "myfoo.safetensors".
func (s *Store) FindVersionByFileName(ctx context.Context, basename string) (modelID, versionID *int, ok bool, err error) {
	basename = strings.TrimSpace(basename)
	if basename == "" {
		return nil, nil, false, nil
	}
	// Match either "<dir>/<basename>" or a bare "<basename>" path. LIKE is
	// case-insensitive for ASCII in SQLite; the exact Go compare below is the real
	// gate, this only narrows the candidate rows.
	like := "%" + string(filepath.Separator) + basename
	rows, err := s.db.QueryContext(ctx, `
		SELECT model_id, version_id, path FROM local_files
		WHERE model_id IS NOT NULL AND (path LIKE ? OR path = ?)
		ORDER BY path`, like, basename)
	if err != nil {
		return nil, nil, false, err
	}
	defer rows.Close()
	lowWant := strings.ToLower(basename)
	for rows.Next() {
		var (
			mID, vID sql.NullInt64
			path     string
		)
		if err := rows.Scan(&mID, &vID, &path); err != nil {
			return nil, nil, false, err
		}
		if strings.ToLower(filepath.Base(path)) != lowWant {
			continue
		}
		if mID.Valid {
			v := int(mID.Int64)
			modelID = &v
		}
		if vID.Valid {
			v := int(vID.Int64)
			versionID = &v
		}
		return modelID, versionID, true, nil
	}
	return nil, nil, false, rows.Err()
}

// GetWorkflow fetches one workflow by id, or ErrNotFound.
func (s *Store) GetWorkflow(ctx context.Context, id int64) (*Workflow, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+workflowCols+` FROM workflows WHERE id = ?`, id)
	wf, err := scanWorkflow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &wf, nil
}

// ListWorkflows returns every workflow, newest first.
func (s *Store) ListWorkflows(ctx context.Context) ([]Workflow, error) {
	return s.queryWorkflows(ctx, `SELECT `+workflowCols+` FROM workflows ORDER BY id DESC`)
}

// ListWorkflowsByVersion returns the workflows attached to a civitai version id,
// newest first.
func (s *Store) ListWorkflowsByVersion(ctx context.Context, versionID int) ([]Workflow, error) {
	return s.queryWorkflows(ctx,
		`SELECT `+workflowCols+` FROM workflows WHERE version_id = ? ORDER BY id DESC`, versionID)
}

func (s *Store) queryWorkflows(ctx context.Context, q string, args ...any) ([]Workflow, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Workflow
	for rows.Next() {
		wf, err := scanWorkflow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, wf)
	}
	return out, rows.Err()
}

// DeleteWorkflow removes a workflow by id. A missing id yields ErrNotFound.
func (s *Store) DeleteWorkflow(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM workflows WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// AttachWorkflow sets (or clears, with nil) a workflow's civitai model/version
// linkage. Detaching a version (versionID nil) also clears the golden flag, since
// golden is meaningless without a version and the partial unique index is
// scoped by version_id.
func (s *Store) AttachWorkflow(ctx context.Context, id int, modelID, versionID *int) error {
	clearGolden := versionID == nil
	res, err := s.db.ExecContext(ctx, `UPDATE workflows
		SET model_id = ?, version_id = ?, updated_at = ?,
		    is_golden = CASE WHEN ? THEN 0 ELSE is_golden END
		WHERE id = ?`,
		nullInt(modelID), nullInt(versionID), formatTime(time.Now().UTC()),
		boolToInt(clearGolden), id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetGolden marks a workflow as the golden (recommended) one for its version. It
// runs in a transaction: it clears is_golden on any OTHER row with the same
// version_id first, then sets it on this row, so the ux_workflows_golden partial
// unique index never trips. A workflow with no attached version_id is rejected
// with ErrGoldenNeedsVersion.
func (s *Store) SetGolden(ctx context.Context, id int) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var versionID sql.NullInt64
	err = tx.QueryRowContext(ctx, `SELECT version_id FROM workflows WHERE id = ?`, id).Scan(&versionID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if !versionID.Valid {
		return ErrGoldenNeedsVersion
	}

	now := formatTime(time.Now().UTC())
	// Clear any existing golden on the same version FIRST so setting this one can
	// never collide on the partial unique index.
	if _, err := tx.ExecContext(ctx, `UPDATE workflows
		SET is_golden = 0, updated_at = ?
		WHERE version_id = ? AND is_golden = 1 AND id <> ?`,
		now, versionID.Int64, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE workflows
		SET is_golden = 1, updated_at = ? WHERE id = ?`, now, id); err != nil {
		return err
	}
	return tx.Commit()
}

// UnsetGolden clears a workflow's golden flag. Clearing a non-golden or missing
// row is a harmless no-op.
func (s *Store) UnsetGolden(ctx context.Context, id int) error {
	_, err := s.db.ExecContext(ctx, `UPDATE workflows
		SET is_golden = 0, updated_at = ? WHERE id = ?`,
		formatTime(time.Now().UTC()), id)
	return err
}
