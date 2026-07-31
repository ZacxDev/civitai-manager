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
	Params string
	// PresetID is the run preset ("tab") this run was started from, or nil for a
	// run that did not come from one. ON DELETE SET NULL — deleting a preset must
	// never delete the images it produced.
	PresetID *int64
	// PresetName is a SNAPSHOT of the preset's label at run time (the same idiom as
	// WorkflowName), so a deleted preset's runs stay labeled.
	PresetName string
	// BatchID groups the N runs one "Queue ×N" click produced; "" for an ordinary
	// single run. BatchIndex is 1-based within the batch and BatchTotal is N AS
	// REQUESTED (not as captured), so a batch halted at item 3 of 8 still reports
	// eight — counting captured rows would hide that five runs were never made.
	// All three are zero/"" together: a run either belongs to a batch or does not.
	BatchID    string
	BatchIndex int
	BatchTotal int
	Status     string
	ImageCount int
	CreatedAt  time.Time
	// FirstImageID is the id of the generation's first image (lowest idx), for the
	// grid thumbnail. Populated by ListGenerations; 0 when the generation has no
	// images. Not persisted — a derived convenience field.
	FirstImageID int64
	// FirstImageContentType is that same first output's stored content_type, so a
	// grid/rail tile can pick <img> vs <video> WITHOUT a second query per tile. It
	// rides the same correlated subquery as FirstImageID and is populated by exactly
	// the same callers; "" when there is no image (or for a pre-video row whose
	// column is NULL, which the render side treats as an image).
	//
	// This is why the feature needed NO migration: generation_images.content_type has
	// existed since 0012, and "is this a video" is derivable from it. A separate
	// `kind` column would be a second source of truth for the same fact.
	FirstImageContentType string
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
	graph_hash, params, preset_id, preset_name, batch_id, batch_index, batch_total,
	status, image_count, created_at`

// generationThumbCols are the trailing columns every LIST query appends: the first
// output's id and its content_type, so a tile can choose <img> vs <video> without a
// per-tile lookup.
//
// The two correlated subqueries MUST keep the SAME `ORDER BY gi.idx, gi.id LIMIT 1`
// — they are two reads of what has to be ONE row, and if they ever diverged a tile
// would render output A's bytes with output B's media kind. Kept as one shared
// const for that reason (it also de-duplicates what was already copied into both
// list queries).
const generationThumbCols = `
	COALESCE((SELECT gi.id FROM generation_images gi
	          WHERE gi.generation_id = generations.id
	          ORDER BY gi.idx, gi.id LIMIT 1), 0) AS first_image_id,
	COALESCE((SELECT gi.content_type FROM generation_images gi
	          WHERE gi.generation_id = generations.id
	          ORDER BY gi.idx, gi.id LIMIT 1), '') AS first_image_content_type`

// genNulls is the set of landing pads for a generation row's NULLable columns.
//
// It exists so `generationCols`, the scan destinations and the null→field mapping
// live in ONE place. They used to be spelled out twice (scanGeneration and
// ListGenerations' inline loop), and a column added to the list but to only one of
// the two scans is a silent mis-alignment — every subsequent column reads the wrong
// value with no error at all.
type genNulls struct {
	workflowID sql.NullInt64
	baseModel  sql.NullString
	graphHash  sql.NullString
	params     sql.NullString
	presetID   sql.NullInt64
	presetName sql.NullString
	batchID    sql.NullString
	batchIndex sql.NullInt64
	batchTotal sql.NullInt64
	createdAt  string
}

// dest returns the scan destinations for generationCols, IN THAT EXACT ORDER.
func (n *genNulls) dest(gen *Generation) []any {
	return []any{
		&gen.ID, &n.workflowID, &gen.WorkflowName, &gen.PromptID,
		&n.baseModel, &n.graphHash, &n.params, &n.presetID, &n.presetName,
		&n.batchID, &n.batchIndex, &n.batchTotal,
		&gen.Status, &gen.ImageCount, &n.createdAt,
	}
}

// apply copies the scanned nullable values onto gen.
func (n *genNulls) apply(gen *Generation) {
	if n.workflowID.Valid {
		id := n.workflowID.Int64
		gen.WorkflowID = &id
	}
	if n.presetID.Valid {
		id := n.presetID.Int64
		gen.PresetID = &id
	}
	gen.BaseModel = n.baseModel.String
	gen.GraphHash = n.graphHash.String
	gen.Params = n.params.String
	gen.PresetName = n.presetName.String
	gen.BatchID = n.batchID.String
	gen.BatchIndex = int(n.batchIndex.Int64)
	gen.BatchTotal = int(n.batchTotal.Int64)
	gen.CreatedAt = parseTime(n.createdAt)
}

// scanGeneration reads the core generation columns (generationCols order). It does
// NOT populate FirstImageID (ListGenerations selects that separately).
func scanGeneration(sc scanner) (Generation, error) {
	var (
		gen Generation
		n   genNulls
	)
	if err := sc.Scan(n.dest(&gen)...); err != nil {
		return Generation{}, err
	}
	n.apply(&gen)
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
			 params, preset_id, preset_name, batch_id, batch_index, batch_total,
			 status, image_count, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		nullInt64(gen.WorkflowID), gen.WorkflowName, gen.PromptID,
		nullStr(gen.BaseModel), nullStr(gen.GraphHash), nullStr(gen.Params),
		nullInt64(gen.PresetID), nullStr(gen.PresetName),
		// A run with no batch stores NULL in all three, matching every pre-0016 row
		// (and keeping `batch_id IS NOT NULL` a truthful "this was a batch item").
		nullStr(gen.BatchID), nullPositiveInt(gen.BatchIndex), nullPositiveInt(gen.BatchTotal),
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
	q := `SELECT ` + generationCols + `,` + generationThumbCols + `
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

	return scanGenerationsWithThumb(rows)
}

// scanGenerationsWithThumb drains rows of `generationCols, generationThumbCols`.
// Both list queries (the gallery/rail list and the per-batch list) go through it,
// so they can never disagree about column order.
func scanGenerationsWithThumb(rows *sql.Rows) ([]Generation, error) {
	var out []Generation
	for rows.Next() {
		var (
			gen      Generation
			n        genNulls
			firstImg int64
			firstCT  string
		)
		if err := rows.Scan(append(n.dest(&gen), &firstImg, &firstCT)...); err != nil {
			return nil, err
		}
		n.apply(&gen)
		gen.FirstImageID = firstImg
		gen.FirstImageContentType = firstCT
		out = append(out, gen)
	}
	return out, rows.Err()
}

// nullPositiveInt maps a non-positive count to SQL NULL. batch_index/batch_total
// are 1-based when present, so 0 means "not a batch item" and must not be stored
// as a literal 0 that later reads as a real (impossible) position.
func nullPositiveInt(n int) sql.NullInt64 {
	if n <= 0 {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: int64(n), Valid: true}
}

// maxBatchIDLen bounds an accepted batch id. comfy.NewID() is far shorter; the
// bound exists so a hostile URL segment can never become a long bound parameter.
const maxBatchIDLen = 64

// ValidBatchID reports whether s is a plausible batch id: a bare
// [A-Za-z0-9_-]{1,64}. batch_id reaches the store from a URL PATH SEGMENT
// (/outputs/batch/{id}), so it is untrusted input; everything else is rejected
// before it is ever bound into a query. An id that is well-formed but unknown is
// NOT an error — it simply selects zero rows.
func ValidBatchID(s string) bool {
	if len(s) == 0 || len(s) > maxBatchIDLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '_', c == '-':
		default:
			return false
		}
	}
	return true
}

// ListGenerationsByBatch returns the captured generations of ONE batch in run
// order (batch_index ASC, id ASC), each with its FirstImageID for a thumbnail.
//
// An invalid id returns nil with NO query issued, and a well-formed but unknown id
// returns zero rows rather than an error — the batch page must 404/empty on a
// stale or guessed id, never 500.
func (s *Store) ListGenerationsByBatch(ctx context.Context, batchID string) ([]Generation, error) {
	if !ValidBatchID(batchID) {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+generationCols+`,`+generationThumbCols+`
		FROM generations
		WHERE batch_id = ?
		ORDER BY batch_index ASC, id ASC`, batchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanGenerationsWithThumb(rows)
}

// recentGenerationsCap is the HARD upper bound on ListRecentGenerations' limit.
// The global "Recent outputs" rail issues that query on EVERY page render, so the
// clamp guarantees a caller can never turn per-request chrome into an unbounded
// scan of the generations table.
const recentGenerationsCap = 50

// ListRecentGenerations returns AT MOST limit generations, newest-first, across
// every workflow — the bounded query behind the global outputs rail. limit <= 0
// returns nil (nothing asked for, nothing queried); a limit above
// recentGenerationsCap is clamped to it.
func (s *Store) ListRecentGenerations(ctx context.Context, limit int) ([]Generation, error) {
	if limit <= 0 {
		return nil, nil
	}
	if limit > recentGenerationsCap {
		limit = recentGenerationsCap
	}
	return s.ListGenerations(ctx, ListGenerationsOpts{Limit: limit})
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

// SumGenerationImageBytes returns the TOTAL stored bytes of every captured output
// image (the sum of generation_images.size_bytes). It is the measured size of the
// outputs tree used to enforce the disk cap — DB-side rather than a filesystem
// walk, so it stays O(1) work for the capture path. An empty gallery yields 0.
func (s *Store) SumGenerationImageBytes(ctx context.Context) (int64, error) {
	var total int64
	err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(size_bytes), 0) FROM generation_images`).Scan(&total)
	return total, err
}

// GenerationSize pairs a generation id with the total bytes of its images, for
// eviction accounting (delete the oldest until the tree is back under the cap).
type GenerationSize struct {
	ID    int64
	Bytes int64
}

// ListOldestEvictableGenerations returns up to limit generations OLDEST-FIRST
// (created_at ASC, id ASC — the reverse of the gallery order) with each one's
// total image bytes, so the cap enforcer can evict in age order and account for
// what it reclaimed. limit<=0 returns nil (the caller must bound the eviction
// batch).
//
// It returns only generations whose recorded size is > 0 — that is what
// "evictable" means here. Deleting a zero-byte generation frees nothing, so it is
// never a useful eviction candidate; more importantly, letting such rows occupy
// slots in the caller's bounded batch would starve the batch of the real
// candidates and make the cap silently unenforceable once enough of them exist.
// The filter therefore belongs in SQL, not only in the caller's loop.
func (s *Store) ListOldestEvictableGenerations(ctx context.Context, limit int) ([]GenerationSize, error) {
	if limit <= 0 {
		return nil, nil
	}
	// An INNER JOIN drops image-less generations (0 bytes by definition) and the
	// HAVING drops those whose images are all zero-length.
	rows, err := s.db.QueryContext(ctx, `
		SELECT g.id, SUM(gi.size_bytes) AS bytes
		FROM generations g
		JOIN generation_images gi ON gi.generation_id = g.id
		GROUP BY g.id, g.created_at
		HAVING SUM(gi.size_bytes) > 0
		ORDER BY g.created_at ASC, g.id ASC
		LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GenerationSize
	for rows.Next() {
		var gs GenerationSize
		if err := rows.Scan(&gs.ID, &gs.Bytes); err != nil {
			return nil, err
		}
		out = append(out, gs)
	}
	return out, rows.Err()
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
