package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Generation statuses.
const (
	// GenerationStatusReady marks a capture where every output image was copied.
	GenerationStatusReady = "ready"
	// GenerationStatusPartial marks a capture where the run succeeded but one or
	// more images could not be fetched/written (best-effort capture).
	GenerationStatusPartial = "partial"
)

// Generation is one captured ComfyUI run: a durable record grouping N output
// images. workflow_name/base_model/graph_hash are snapshots taken at capture time
// so an orphaned generation (source workflow deleted → WorkflowID nil) stays
// fully labeled and viewable.
type Generation struct {
	ID int64
	// WorkflowID is the source civitai-manager workflow id, or nil once that
	// workflow has been deleted (ON DELETE SET NULL). A nil WorkflowID disables
	// "Re-run" but never removes the generation.
	WorkflowID   *int64
	WorkflowName string
	PromptID     string
	BaseModel    string
	GraphHash    string
	// Params is the JSON snapshot of the applied run options (substitutions,
	// option fixes, widget overrides) + resource/base info, so a re-run reproduces
	// the parameterized run.
	Params     string
	Status     string
	ImageCount int
	CreatedAt  time.Time
	// FirstImageID is the id of the generation's first image (lowest idx), for the
	// grid thumbnail. Populated by ListGenerations; 0 when the generation has no
	// images. Not persisted — a derived convenience field.
	FirstImageID int64
}

// GenerationImage is one output image belonging to a Generation. rel_path is
// ALWAYS relative to the outputs root (never absolute) so rows stay portable and
// every read is forced through the containment check.
type GenerationImage struct {
	ID           int64
	GenerationID int64
	Idx          int
	RelPath      string
	Filename     string
	ContentType  string
	SizeBytes    int64
	SHA256       string
	CreatedAt    time.Time
}

// ListGenerationsOpts parameterizes ListGenerations. A nil WorkflowID lists every
// generation; a non-nil one filters to that workflow. Limit<=0 means no limit.
type ListGenerationsOpts struct {
	WorkflowID *int64
	Limit      int
	Offset     int
}

const generationCols = `id, workflow_id, workflow_name, prompt_id, base_model,
	graph_hash, params, status, image_count, created_at`

// scanGeneration reads the core generation columns (generationCols order). It does
// NOT populate FirstImageID (ListGenerations selects that separately).
func scanGeneration(sc scanner) (Generation, error) {
	var (
		gen        Generation
		workflowID sql.NullInt64
		baseModel  sql.NullString
		graphHash  sql.NullString
		params     sql.NullString
		createdAt  string
	)
	if err := sc.Scan(&gen.ID, &workflowID, &gen.WorkflowName, &gen.PromptID,
		&baseModel, &graphHash, &params, &gen.Status, &gen.ImageCount, &createdAt); err != nil {
		return Generation{}, err
	}
	if workflowID.Valid {
		gen.WorkflowID = &workflowID.Int64
	}
	gen.BaseModel = baseModel.String
	gen.GraphHash = graphHash.String
	gen.Params = params.String
	gen.CreatedAt = parseTime(createdAt)
	return gen, nil
}

func scanGenerationImage(sc scanner) (GenerationImage, error) {
	var (
		img         GenerationImage
		contentType sql.NullString
		sha         sql.NullString
		createdAt   string
	)
	if err := sc.Scan(&img.ID, &img.GenerationID, &img.Idx, &img.RelPath,
		&img.Filename, &contentType, &img.SizeBytes, &sha, &createdAt); err != nil {
		return GenerationImage{}, err
	}
	img.ContentType = contentType.String
	img.SHA256 = sha.String
	img.CreatedAt = parseTime(createdAt)
	return img, nil
}

// InsertGeneration inserts a generation and its images in ONE transaction. It sets
// CreatedAt (parent + each image) to now when zero, denormalizes image_count to
// len(images), and returns the new generation id. Each image's Idx is stored as
// given (the caller assigns 0-based positions). status defaults to 'ready' when
// empty.
func (s *Store) InsertGeneration(ctx context.Context, gen *Generation, images []GenerationImage) (int64, error) {
	if gen == nil {
		return 0, errors.New("InsertGeneration: nil generation")
	}
	now := time.Now().UTC()
	if gen.CreatedAt.IsZero() {
		gen.CreatedAt = now
	}
	if gen.Status == "" {
		gen.Status = GenerationStatusReady
	}
	gen.ImageCount = len(images)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx, `
		INSERT INTO generations
			(workflow_id, workflow_name, prompt_id, base_model, graph_hash,
			 params, status, image_count, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		nullInt64(gen.WorkflowID), gen.WorkflowName, gen.PromptID,
		nullStr(gen.BaseModel), nullStr(gen.GraphHash), nullStr(gen.Params),
		gen.Status, gen.ImageCount, formatTime(gen.CreatedAt))
	if err != nil {
		return 0, fmt.Errorf("insert generation: %w", err)
	}
	genID, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}

	for i := range images {
		img := &images[i]
		if img.CreatedAt.IsZero() {
			img.CreatedAt = now
		}
		if img.ContentType == "" {
			img.ContentType = "image/png"
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO generation_images
				(generation_id, idx, rel_path, filename, content_type, size_bytes, sha256, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			genID, img.Idx, img.RelPath, img.Filename, img.ContentType,
			img.SizeBytes, nullStr(img.SHA256), formatTime(img.CreatedAt)); err != nil {
			return 0, fmt.Errorf("insert generation image: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit generation: %w", err)
	}
	gen.ID = genID
	return genID, nil
}

// ListGenerations returns generations newest-first (created_at DESC, id DESC),
// optionally filtered to one workflow, with LIMIT/OFFSET pagination. Each row's
// FirstImageID is populated via a correlated subquery so the grid can render a
// thumbnail without a second round-trip.
func (s *Store) ListGenerations(ctx context.Context, opts ListGenerationsOpts) ([]Generation, error) {
	q := `SELECT ` + generationCols + `,
		COALESCE((SELECT gi.id FROM generation_images gi
		          WHERE gi.generation_id = generations.id
		          ORDER BY gi.idx, gi.id LIMIT 1), 0) AS first_image_id
		FROM generations`
	var args []any
	if opts.WorkflowID != nil {
		q += ` WHERE workflow_id = ?`
		args = append(args, *opts.WorkflowID)
	}
	q += ` ORDER BY created_at DESC, id DESC`
	if opts.Limit > 0 {
		q += ` LIMIT ?`
		args = append(args, opts.Limit)
		if opts.Offset > 0 {
			q += ` OFFSET ?`
			args = append(args, opts.Offset)
		}
	}

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Generation
	for rows.Next() {
		var (
			gen        Generation
			workflowID sql.NullInt64
			baseModel  sql.NullString
			graphHash  sql.NullString
			params     sql.NullString
			createdAt  string
			firstImg   int64
		)
		if err := rows.Scan(&gen.ID, &workflowID, &gen.WorkflowName, &gen.PromptID,
			&baseModel, &graphHash, &params, &gen.Status, &gen.ImageCount,
			&createdAt, &firstImg); err != nil {
			return nil, err
		}
		if workflowID.Valid {
			gen.WorkflowID = &workflowID.Int64
		}
		gen.BaseModel = baseModel.String
		gen.GraphHash = graphHash.String
		gen.Params = params.String
		gen.CreatedAt = parseTime(createdAt)
		gen.FirstImageID = firstImg
		out = append(out, gen)
	}
	return out, rows.Err()
}

// CountGenerations counts generations, optionally filtered to one workflow (nil =
// all). Used for pagination and the per-workflow section header.
func (s *Store) CountGenerations(ctx context.Context, workflowID *int64) (int, error) {
	q := `SELECT COUNT(*) FROM generations`
	var args []any
	if workflowID != nil {
		q += ` WHERE workflow_id = ?`
		args = append(args, *workflowID)
	}
	var n int
	err := s.db.QueryRowContext(ctx, q, args...).Scan(&n)
	return n, err
}

// GetGeneration fetches one generation and its images (ordered by idx), or
// ErrNotFound.
func (s *Store) GetGeneration(ctx context.Context, id int64) (*Generation, []GenerationImage, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+generationCols+` FROM generations WHERE id = ?`, id)
	gen, err := scanGeneration(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, ErrNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	imgs, err := s.listGenerationImages(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	return &gen, imgs, nil
}

func (s *Store) listGenerationImages(ctx context.Context, genID int64) ([]GenerationImage, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, generation_id, idx, rel_path, filename, content_type, size_bytes, sha256, created_at
		FROM generation_images WHERE generation_id = ? ORDER BY idx, id`, genID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GenerationImage
	for rows.Next() {
		img, err := scanGenerationImage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, img)
	}
	return out, rows.Err()
}

// GetGenerationImage fetches one image row by id (for the byte-serving route), or
// ErrNotFound.
func (s *Store) GetGenerationImage(ctx context.Context, imageID int64) (*GenerationImage, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, generation_id, idx, rel_path, filename, content_type, size_bytes, sha256, created_at
		FROM generation_images WHERE id = ?`, imageID)
	img, err := scanGenerationImage(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &img, nil
}

// DeleteGeneration deletes a generation by id and returns the rel_paths of its
// images so the caller can unlink the on-disk files (the DB CASCADE drops the
// image rows). It returns ErrNotFound when no such generation exists. The caller
// should remove files AFTER this returns (an orphaned file is a benign leak; an
// orphaned row that 404s on serve is worse).
func (s *Store) DeleteGeneration(ctx context.Context, id int64) ([]string, error) {
	paths, err := s.relPathsForGenerations(ctx, `SELECT rel_path FROM generation_images WHERE generation_id = ?`, id)
	if err != nil {
		return nil, err
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM generations WHERE id = ?`, id)
	if err != nil {
		return nil, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return nil, ErrNotFound
	}
	return paths, nil
}

// DeleteGenerationsByWorkflow deletes every generation for a workflow and returns
// all their image rel_paths for file cleanup. Deleting a workflow with no
// generations is a harmless no-op (empty slice, nil error).
func (s *Store) DeleteGenerationsByWorkflow(ctx context.Context, workflowID int64) ([]string, error) {
	paths, err := s.relPathsForGenerations(ctx, `
		SELECT gi.rel_path FROM generation_images gi
		JOIN generations g ON g.id = gi.generation_id
		WHERE g.workflow_id = ?`, workflowID)
	if err != nil {
		return nil, err
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM generations WHERE workflow_id = ?`, workflowID); err != nil {
		return nil, err
	}
	return paths, nil
}

// GenerationWorkflowRef is a distinct source workflow that has at least one
// generation, for the gallery's workflow filter select.
type GenerationWorkflowRef struct {
	WorkflowID int64
	Name       string
}

// ListGenerationWorkflowRefs returns the distinct non-null source workflows that
// have generations, with the most recent snapshot name for each, ordered by name.
// It powers the "filter by workflow" select on the outputs gallery.
func (s *Store) ListGenerationWorkflowRefs(ctx context.Context) ([]GenerationWorkflowRef, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT workflow_id, MAX(workflow_name) AS name
		FROM generations
		WHERE workflow_id IS NOT NULL
		GROUP BY workflow_id
		ORDER BY name COLLATE NOCASE, workflow_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GenerationWorkflowRef
	for rows.Next() {
		var ref GenerationWorkflowRef
		var name sql.NullString
		if err := rows.Scan(&ref.WorkflowID, &name); err != nil {
			return nil, err
		}
		ref.Name = name.String
		out = append(out, ref)
	}
	return out, rows.Err()
}

func (s *Store) relPathsForGenerations(ctx context.Context, query string, args ...any) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var paths []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		paths = append(paths, p)
	}
	return paths, rows.Err()
}
