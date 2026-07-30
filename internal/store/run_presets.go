package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// MaxRunPresetsPerWorkflow caps how many saved run presets ("tabs") one workflow
// may carry. The tabs are a horizontal strip: past ~12 it stops being scannable
// and would need its own overflow UI. The cap is enforced HERE (not only in the
// UI) so a hand-built request cannot grow the strip without bound; at the cap the
// UI renders Fork disabled with the reason rather than evicting the oldest preset
// — presets are user data and are never silently deleted.
const MaxRunPresetsPerWorkflow = 12

// ErrPresetCapReached is returned by CreateRunPreset when the workflow already
// holds MaxRunPresetsPerWorkflow presets.
var ErrPresetCapReached = errors.New("this workflow already has the maximum number of run presets")

// RunPreset is one saved run-parameter set for a workflow — the run panel's tab.
//
// GraphHash names the graph Params was CAPTURED AGAINST — never a different one.
// Params is keyed POSITIONALLY ((node id, widgets_values index) + "<selector node
// id>:<group index>" mode keys), so the hash is the proof that those positions
// still mean what they meant; a blank hash on either side can never be proven equal
// and is treated as drift. See comfy.ReconcileRunPreset and the invariant note on
// UpdateRunPreset.
type RunPreset struct {
	ID         int64
	WorkflowID int64
	// Name is UNTRUSTED user text (escaped at render time). Blank renders as a
	// positional fallback label.
	Name      string
	Position  int
	GraphHash string
	// Params is the JSON runParamsSnapshot blob. Never NULL — '{}' at worst.
	Params    string
	CreatedAt time.Time
	UpdatedAt time.Time
}

const runPresetCols = `id, workflow_id, name, position, graph_hash, params, created_at, updated_at`

func scanRunPreset(sc scanner) (RunPreset, error) {
	var (
		p         RunPreset
		createdAt string
		updatedAt string
	)
	if err := sc.Scan(&p.ID, &p.WorkflowID, &p.Name, &p.Position, &p.GraphHash,
		&p.Params, &createdAt, &updatedAt); err != nil {
		return RunPreset{}, err
	}
	p.CreatedAt = parseTime(createdAt)
	p.UpdatedAt = parseTime(updatedAt)
	return p, nil
}

// CreateRunPreset inserts a preset for p.WorkflowID and returns its new id.
//
// The cap check and the insert run in ONE transaction so two concurrent creates
// cannot both observe "11 presets" and both insert. Position defaults to the next
// free slot (max(position)+1) when it is given as a negative number, which is what
// every "add a tab" caller wants. A nonexistent workflow_id fails on the foreign
// key (PRAGMA foreign_keys is ON).
func (s *Store) CreateRunPreset(ctx context.Context, p RunPreset) (int64, error) {
	now := time.Now().UTC()
	if p.CreatedAt.IsZero() {
		p.CreatedAt = now
	}
	if p.UpdatedAt.IsZero() {
		p.UpdatedAt = p.CreatedAt
	}
	if p.Params == "" {
		p.Params = "{}"
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	var count int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM run_presets WHERE workflow_id = ?`, p.WorkflowID).Scan(&count); err != nil {
		return 0, err
	}
	if count >= MaxRunPresetsPerWorkflow {
		return 0, ErrPresetCapReached
	}
	if p.Position < 0 {
		var next sql.NullInt64
		if err := tx.QueryRowContext(ctx,
			`SELECT MAX(position) FROM run_presets WHERE workflow_id = ?`, p.WorkflowID).Scan(&next); err != nil {
			return 0, err
		}
		p.Position = 0
		if next.Valid {
			p.Position = int(next.Int64) + 1
		}
	}

	res, err := tx.ExecContext(ctx, `
		INSERT INTO run_presets (workflow_id, name, position, graph_hash, params, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		p.WorkflowID, p.Name, p.Position, p.GraphHash, p.Params,
		formatTime(p.CreatedAt), formatTime(p.UpdatedAt))
	if err != nil {
		return 0, fmt.Errorf("insert run preset: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit run preset: %w", err)
	}
	return id, nil
}

// ListRunPresets returns a workflow's presets in tab order: position, then id so
// ties are stable and the strip never reorders itself between renders.
func (s *Store) ListRunPresets(ctx context.Context, workflowID int64) ([]RunPreset, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+runPresetCols+` FROM run_presets WHERE workflow_id = ? ORDER BY position, id`, workflowID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RunPreset
	for rows.Next() {
		p, err := scanRunPreset(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetRunPreset loads one preset by id. A missing id yields ErrNotFound.
//
// It deliberately does NOT filter by workflow: the caller must compare
// WorkflowID itself and 404 on a mismatch, so a cross-workflow id guess is a
// visible, testable check rather than an empty result that looks like "deleted".
func (s *Store) GetRunPreset(ctx context.Context, id int64) (*RunPreset, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+runPresetCols+` FROM run_presets WHERE id = ?`, id)
	p, err := scanRunPreset(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// UpdateRunPreset writes a preset's name, params AND graph_hash together, bumping
// updated_at. It is the ONE entry-replacing writer, and params + graph_hash are
// deliberately a SINGLE argument list rather than two functions.
//
// 🔴 THE INVARIANT THIS SIGNATURE ENFORCES: graph_hash must always describe the
// graph the stored params were captured against.
//
// The earlier split (UpdateRunPreset never touching graph_hash, AdoptRunPresetGraph
// re-stamping it) made the opposite easy: replacing the params wholesale with values
// captured from the CURRENT graph while leaving a hash naming an OLDER one. That is
// not a stale label — GraphHash is a canonicalized CONTENT hash, so if the workflow
// ever returns to the earlier graph (a revert, a re-import, a rescan) the hash
// matches EXACTLY, comfy.ReconcileRunPreset takes the exact path, and every stored
// value is applied by position with no per-entry tuple check. A false certificate.
//
// Callers therefore have to name the hash on every write. The two truthful answers
// are the hash of the graph the values came from, or "" — which can never be proven
// equal and so forces the tuple-checked drift path. Never a third graph's hash.
func (s *Store) UpdateRunPreset(ctx context.Context, id int64, name, params, graphHash string) error {
	if params == "" {
		params = "{}"
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE run_presets SET name = ?, params = ?, graph_hash = ?, updated_at = ? WHERE id = ?`,
		name, params, graphHash, nowRFC3339(), id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteRunPreset removes a preset by id. A missing id yields ErrNotFound.
// Generations that reference it keep their images and their snapshotted
// preset_name; only generations.preset_id is NULLed (ON DELETE SET NULL).
func (s *Store) DeleteRunPreset(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM run_presets WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// CountRunPresets returns how many presets a workflow holds (for the cap's UI
// affordance — the Fork control renders disabled with a reason at the cap).
func (s *Store) CountRunPresets(ctx context.Context, workflowID int64) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM run_presets WHERE workflow_id = ?`, workflowID).Scan(&n)
	return n, err
}
