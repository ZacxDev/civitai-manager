package library

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/hashutil"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// PlannedMove is one file the quarantine action would (or did) move.
type PlannedMove struct {
	OriginalPath string `json:"original_path"`
	TrashPath    string `json:"trash_path"`
	Reason       string `json:"reason"`
	SHA256       string `json:"sha256,omitempty"`
	SizeBytes    int64  `json:"size_bytes"`
	IsSidecar    bool   `json:"is_sidecar"`
	// fileID is the local_files row this move originates from (0 for a sidecar of
	// a model file). Internal; not serialized.
	fileID int64
}

// SkippedFile is a requested file the quarantine action refused to move, with
// the safety reason why.
type SkippedFile struct {
	Path   string
	ID     int64
	Reason string
}

// QuarantinePlan is the result of a quarantine call: what would move (dry-run)
// or did move (apply).
type QuarantinePlan struct {
	Applied    bool
	BatchID    int64
	Moves      []PlannedMove
	Skipped    []SkippedFile
	TotalBytes int64
}

// manifest is the on-disk undo record written per batch.
type manifest struct {
	BatchID   int64         `json:"batch_id"`
	CreatedAt string        `json:"created_at"`
	TrashDir  string        `json:"trash_dir"`
	Moves     []PlannedMove `json:"moves"`
}

// Quarantine soft-deletes the given flagged files: it MOVES each (and, for a
// model file, its sidecars) into the trash dir and writes an undo manifest.
// Nothing is ever hard-deleted. With apply=false it is a dry-run: it reports the
// exact plan and moves nothing.
//
// Safety invariants enforced here (never bypassed):
//   - a file whose absolute path is outside every configured scan root is refused;
//   - an unmatched / unmatched-pending file is refused (never a candidate);
//   - a non-candidate file is refused;
//   - a duplicate is refused unless ≥1 identical copy would remain;
//   - a superseded file that is the newest local version of its model is refused.
func (s *Scanner) Quarantine(ctx context.Context, ids []int64, apply bool) (*QuarantinePlan, error) {
	roots := s.Roots()
	// refuse's containment guard needs symlink-resolved roots; resolve them ONCE
	// here rather than per file. trashPathFor keeps the UNRESOLVED roots (it takes
	// a lexical Rel against the original, unresolved path to build the trash relpath).
	resolvedRoots := resolveRoots(roots)
	plan := &QuarantinePlan{}

	// Resolve requested rows and the full move set (for the duplicate-safety
	// count, we need to know every id being quarantined up front).
	requested := make([]store.LocalFile, 0, len(ids))
	moving := map[int64]bool{}
	for _, id := range ids {
		lf, err := s.store.GetLocalFile(id)
		if err != nil {
			plan.Skipped = append(plan.Skipped, SkippedFile{ID: id, Reason: "not found in index"})
			continue
		}
		requested = append(requested, *lf)
		moving[id] = true
	}

	allModels, err := s.modelFilesBySHAAndVersion()
	if err != nil {
		return nil, err
	}

	for _, lf := range requested {
		if skip := s.refuse(lf, resolvedRoots, moving, allModels); skip != "" {
			plan.Skipped = append(plan.Skipped, SkippedFile{ID: lf.ID, Path: lf.Path, Reason: skip})
			continue
		}
		// TOCTOU guard: the file was hashed/flagged at scan time; re-stat it now and
		// refuse to move it if its identity (size, and mtime when tracked) changed
		// since — the contents may no longer match the flag. This is per-file: one
		// changed file is skipped, the rest of the batch still proceeds.
		fi, err := os.Stat(lf.Path)
		if err != nil {
			plan.Skipped = append(plan.Skipped, SkippedFile{ID: lf.ID, Path: lf.Path, Reason: "missing on disk"})
			continue
		}
		if reason := changedSinceScan(lf, fi); reason != "" {
			plan.Skipped = append(plan.Skipped, SkippedFile{ID: lf.ID, Path: lf.Path, Reason: reason})
			continue
		}
		// The model file itself.
		plan.Moves = append(plan.Moves, PlannedMove{
			OriginalPath: lf.Path, Reason: lf.CandidateReason, SHA256: lf.SHA256,
			SizeBytes: lf.SizeBytes, IsSidecar: false, fileID: lf.ID,
		})
		// Its sidecars (model files only; a tracked broken sidecar has none).
		if lf.Kind == store.LocalKindModel {
			for _, sc := range modelSidecars(lf.Path, s.opts.Extensions) {
				size := int64(0)
				if fi, err := os.Stat(sc); err == nil {
					size = fi.Size()
				}
				plan.Moves = append(plan.Moves, PlannedMove{
					OriginalPath: sc, Reason: lf.CandidateReason, SizeBytes: size, IsSidecar: true,
				})
			}
		}
	}

	// Dedup by absolute OriginalPath: two model files sharing a basename in one
	// directory (e.g. foo.safetensors + foo.ckpt) resolve to the SAME sidecar
	// base, so the shared foo.civitai.info would otherwise be queued twice and the
	// second move (src already gone) would abort the batch mid-flight.
	plan.Moves = dedupMovesByPath(plan.Moves)

	for _, m := range plan.Moves {
		plan.TotalBytes += m.SizeBytes
	}

	if !apply {
		// Dry-run: fill indicative trash paths for display, move nothing.
		batchName := s.batchName(0)
		for i := range plan.Moves {
			plan.Moves[i].TrashPath = s.trashPathFor(plan.Moves[i].OriginalPath, roots, batchName)
		}
		return plan, nil
	}

	return s.applyQuarantine(plan, roots)
}

// applyQuarantine performs the moves for a validated plan and records the batch.
//
// The ledger writes (batch header + each file's quarantine record + its index
// deletion) run inside a SINGLE transaction so the record is never left half
// written, and the keep-≥1-copy safety check is re-run INSIDE that transaction
// against a consistent snapshot (so two concurrent batches cannot each remove
// the last surviving copy of a duplicate set).
//
// The filesystem moves themselves cannot be transactional. They are sequenced so
// that each file is moved and THEN recorded; if a move fails mid-batch, the
// records for the files already moved are committed (they physically live in the
// trash and must stay restorable) and an error naming the batch id is returned so
// `restore <batchID>` can roll back exactly what moved. Files not yet moved stay
// put with their index rows intact.
func (s *Scanner) applyQuarantine(plan *QuarantinePlan, roots []string) (*QuarantinePlan, error) {
	if len(plan.Moves) == 0 {
		return plan, nil
	}
	created := s.nowFn()

	tx, err := s.store.BeginTx()
	if err != nil {
		return nil, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	// Consistent-snapshot safety recheck: against the CURRENT db state (read in
	// this tx), no duplicate move may remove the last surviving byte-identical
	// copy. This closes the concurrent-batch race the pre-plan check cannot.
	if err := s.recheckDuplicateSafety(tx, plan); err != nil {
		return nil, err
	}

	batchID, err := tx.CreateQuarantineBatch(s.opts.TrashDir, "", quarantineReason(plan.Moves))
	if err != nil {
		return nil, err
	}
	batchName := s.batchName(batchID)
	batchDir := filepath.Join(s.opts.TrashDir, batchName)

	var (
		moved      []PlannedMove
		moveErr    error
		failedPath string
	)
	for i := range plan.Moves {
		m := &plan.Moves[i]
		// Root-qualified, guaranteed-unique destination: two files with the same
		// relpath under DIFFERENT scan roots must NEVER collide onto one trash
		// path (which would clobber one and corrupt the undo ledger).
		dst := uniqueDest(s.trashPathFor(m.OriginalPath, roots, batchName))
		m.TrashPath = dst
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			moveErr, failedPath = err, m.OriginalPath
			break
		}
		if err := s.moveFn(m.OriginalPath, dst, m.SHA256); err != nil {
			moveErr, failedPath = err, m.OriginalPath
			break
		}
		if _, err := tx.AddQuarantinedFile(store.QuarantinedFile{
			BatchID: batchID, OriginalPath: m.OriginalPath, TrashPath: dst,
			Reason: m.Reason, IsSidecar: m.IsSidecar, SHA256: m.SHA256, SizeBytes: m.SizeBytes,
		}); err != nil {
			moveErr, failedPath = err, m.OriginalPath
			break
		}
		// Drop the index row: the file no longer lives at OriginalPath.
		if err := tx.DeleteLocalFileByPath(m.OriginalPath); err != nil {
			moveErr, failedPath = err, m.OriginalPath
			break
		}
		moved = append(moved, *m)
	}

	// Commit whatever moved so the ledger reflects the files now in the trash
	// (they must be restorable even on a partial batch).
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("quarantine batch %d: commit ledger: %w", batchID, err)
	}
	committed = true

	// Write the undo manifest for the moves that actually happened. The DB is the
	// source of truth; the manifest is a best-effort convenience, so a manifest
	// write failure does not fail the batch.
	if len(moved) > 0 {
		manifestPath := filepath.Join(batchDir, "manifest.json")
		man := manifest{BatchID: batchID, CreatedAt: created.Format("2006-01-02T15:04:05Z07:00"), TrashDir: batchDir, Moves: moved}
		if err := writeManifest(manifestPath, man); err == nil {
			if err := s.store.SetBatchManifest(batchID, manifestPath); err != nil {
				s.log.Warn("quarantine: record manifest path failed", "batch", batchID, "err", err)
			}
		} else {
			s.log.Warn("quarantine: write manifest failed", "batch", batchID, "err", err)
		}
	}

	if moveErr != nil {
		return plan, fmt.Errorf("quarantine batch %d partially applied (move of %s failed); "+
			"restore %d to roll back the %d file(s) already moved: %w",
			batchID, failedPath, batchID, len(moved), moveErr)
	}

	plan.Applied = true
	plan.BatchID = batchID
	return plan, nil
}

// recheckDuplicateSafety re-validates, against the transaction's snapshot of the
// index, that no duplicate move in the plan would remove the last surviving
// byte-identical copy. It returns an error (rolling back the whole batch) if the
// invariant would be violated — e.g. because a concurrent batch already removed
// the copy this batch was counting on.
func (s *Scanner) recheckDuplicateSafety(tx *store.Tx, plan *QuarantinePlan) error {
	models, err := tx.ListModelFiles()
	if err != nil {
		return err
	}
	moving := map[int64]bool{}
	for _, m := range plan.Moves {
		if !m.IsSidecar {
			moving[m.fileID] = true
		}
	}
	for _, m := range plan.Moves {
		if m.IsSidecar || m.Reason != store.CandidateDuplicate {
			continue
		}
		lf := store.LocalFile{ID: m.fileID, SHA256: m.SHA256}
		if !copyWouldRemain(lf, moving, models) {
			return fmt.Errorf("refusing to quarantine %s: it is now the last remaining copy of its duplicate set", m.OriginalPath)
		}
	}
	return nil
}

// changedSinceScan reports (non-empty) if the on-disk file no longer matches the
// size/mtime recorded for lf at scan time — i.e. it was modified after being
// flagged, so acting on the (now stale) flag could move a file whose contents
// changed. An empty string means unchanged (safe to move).
//
// Size is compared whenever a size was recorded (a 0 in the index means the size
// was not captured, so it is not treated as a mismatch). Mtime is compared only
// when it was tracked (non-nil). A real scan always records both; the guards keep
// hand-built rows (and legacy rows) from producing false "changed" reports.
func changedSinceScan(lf store.LocalFile, fi os.FileInfo) string {
	if lf.SizeBytes != 0 && fi.Size() != lf.SizeBytes {
		return "changed since scan (size differs) — rescan and retry"
	}
	if lf.Mtime != nil && !fi.ModTime().Equal(*lf.Mtime) {
		return "changed since scan (mtime differs) — rescan and retry"
	}
	return ""
}

// refuse returns a non-empty safety reason if lf must not be quarantined.
func (s *Scanner) refuse(lf store.LocalFile, resolvedRoots []string, moving map[int64]bool, models []store.LocalFile) string {
	if lf.Status == store.LocalStatusUnmatched || lf.Status == store.LocalStatusUnmatchedPending {
		return "refusing to quarantine an unmatched file"
	}
	if !lf.IsCandidate() {
		return "not a deletion candidate"
	}
	if !withinRoots(lf.Path, resolvedRoots) {
		return "path is outside every configured scan root"
	}
	switch lf.CandidateReason {
	case store.CandidateDuplicate:
		if !copyWouldRemain(lf, moving, models) {
			return "refusing to quarantine the last remaining copy of a duplicate set"
		}
	case store.CandidateSuperseded:
		if isNewestLocalVersion(lf, models) {
			return "refusing to quarantine the newest local version"
		}
	}
	return ""
}

// copyWouldRemain reports whether at least one byte-identical copy of lf would
// survive after this quarantine batch (a copy NOT being moved).
func copyWouldRemain(lf store.LocalFile, moving map[int64]bool, models []store.LocalFile) bool {
	for _, m := range models {
		if m.ID == lf.ID || moving[m.ID] {
			continue
		}
		if m.SHA256 != "" && strings.EqualFold(m.SHA256, lf.SHA256) {
			return true
		}
	}
	return false
}

// isNewestLocalVersion reports whether lf holds the highest local version id for
// its model (which superseded-quarantine must never remove).
func isNewestLocalVersion(lf store.LocalFile, models []store.LocalFile) bool {
	if lf.ModelID == nil || lf.VersionID == nil {
		return false
	}
	maxVer := 0
	for _, m := range models {
		if m.ModelID != nil && *m.ModelID == *lf.ModelID && m.VersionID != nil && *m.VersionID > maxVer {
			maxVer = *m.VersionID
		}
	}
	return *lf.VersionID >= maxVer
}

// modelFilesBySHAAndVersion returns all model index rows (for the safety checks).
func (s *Scanner) modelFilesBySHAAndVersion() ([]store.LocalFile, error) {
	files, err := s.store.ListLocalFiles()
	if err != nil {
		return nil, err
	}
	var out []store.LocalFile
	for _, f := range files {
		if f.Kind == store.LocalKindModel {
			out = append(out, f)
		}
	}
	return out, nil
}

// RestoreResult reports the outcome of a restore.
type RestoreResult struct {
	BatchID   int64
	Restored  []string
	Conflicts []string // original paths still occupied; left in trash
}

// Restore moves a batch's files back to their original paths from the manifest.
// A file whose original path is now occupied is reported as a conflict and left
// in the trash (never overwritten).
func (s *Scanner) Restore(ctx context.Context, batchID int64) (*RestoreResult, error) {
	batch, err := s.store.GetQuarantineBatch(batchID)
	if err != nil {
		return nil, err
	}
	files, err := s.store.ListQuarantinedFiles(batchID)
	if err != nil {
		return nil, err
	}
	res := &RestoreResult{BatchID: batchID}
	for _, f := range files {
		if f.RestoredAt != nil {
			continue
		}
		if _, err := os.Stat(f.OriginalPath); err == nil {
			res.Conflicts = append(res.Conflicts, f.OriginalPath)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(f.OriginalPath), 0o755); err != nil {
			return res, fmt.Errorf("recreate dir for %s: %w", f.OriginalPath, err)
		}
		if err := s.moveFn(f.TrashPath, f.OriginalPath, f.SHA256); err != nil {
			return res, fmt.Errorf("restore %s: %w", f.OriginalPath, err)
		}
		if err := s.store.MarkFileRestored(f.ID); err != nil {
			return res, err
		}
		res.Restored = append(res.Restored, f.OriginalPath)
	}
	if len(res.Conflicts) == 0 && !batch.Restored() {
		if err := s.store.MarkBatchRestored(batchID); err != nil {
			return res, err
		}
	}
	return res, nil
}

// --- path helpers ---

// batchName is the timestamped trash subdirectory name for a batch. The batch id
// (0 for a dry-run preview) makes it unique across same-second batches.
func (s *Scanner) batchName(batchID int64) string {
	ts := s.nowFn().Format("20060102-150405")
	if batchID == 0 {
		return ts + "-preview"
	}
	return fmt.Sprintf("%s-%d", ts, batchID)
}

// trashPathFor maps an original path to its trash destination:
//
//	<TrashDir>/<batchName>/<rootSlug>/<relpath>
//
// The relpath preserves the file's path relative to its containing scan root, and
// the rootSlug (a sanitized, hash-qualified form of that scan root) namespaces it
// so two files with the SAME relpath under DIFFERENT scan roots land at distinct
// trash paths instead of colliding onto one (which would silently clobber one
// file and corrupt the undo ledger).
func (s *Scanner) trashPathFor(orig string, roots []string, batchName string) string {
	rel := filepath.Base(orig)
	slug := "unrooted"
	for _, root := range roots {
		if r, err := filepath.Rel(root, orig); err == nil && !strings.HasPrefix(r, "..") {
			rel = r
			slug = rootSlug(root)
			break
		}
	}
	return filepath.Join(s.opts.TrashDir, batchName, slug, rel)
}

// rootSlug is a filesystem-safe, collision-resistant identifier for a scan root:
// the root's (sanitized) base name plus a short hash of its full cleaned path, so
// two distinct roots that happen to share a base name still get distinct slugs.
func rootSlug(root string) string {
	clean := filepath.Clean(root)
	sum := sha256.Sum256([]byte(clean))
	base := sanitizeComponent(filepath.Base(clean))
	if base == "" {
		base = "root"
	}
	return base + "-" + hex.EncodeToString(sum[:])[:8]
}

// sanitizeComponent reduces a path base name to a safe single path component
// (alphanumerics, dot, dash and underscore preserved; everything else becomes
// an underscore).
func sanitizeComponent(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}

// uniqueDest returns dst if it does not exist, otherwise dst with a numeric
// suffix inserted before its extension (foo.bin -> foo-1.bin -> foo-2.bin …). It
// is belt-and-suspenders against any residual trash-path collision so a move can
// never be asked to overwrite an existing trash file.
func uniqueDest(dst string) string {
	if _, err := os.Stat(dst); os.IsNotExist(err) {
		return dst
	}
	ext := filepath.Ext(dst)
	stem := strings.TrimSuffix(dst, ext)
	for i := 1; ; i++ {
		cand := fmt.Sprintf("%s-%d%s", stem, i, ext)
		if _, err := os.Stat(cand); os.IsNotExist(err) {
			return cand
		}
	}
}

// dedupMovesByPath removes duplicate moves that target the same absolute
// OriginalPath (keeping the first), so a file scheduled to move more than once
// — e.g. a sidecar shared by two model files with the same base name — is moved
// exactly once instead of failing on the second (already-gone) move.
func dedupMovesByPath(moves []PlannedMove) []PlannedMove {
	seen := make(map[string]bool, len(moves))
	out := moves[:0:0]
	for _, m := range moves {
		key := filepath.Clean(m.OriginalPath)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, m)
	}
	return out
}

// modelSidecars returns the existing sidecar files (.civitai.info, .preview.png,
// user .png) that share a model file's base name.
func modelSidecars(modelPath string, exts map[string]bool) []string {
	ext := filepath.Ext(modelPath)
	base := modelPath
	if exts[strings.ToLower(ext)] {
		base = strings.TrimSuffix(modelPath, ext)
	}
	candidates := []string{base + sidecarInfo, base + sidecarPreview, base + sidecarPNG}
	var out []string
	for _, c := range candidates {
		if fi, err := os.Stat(c); err == nil && !fi.IsDir() {
			out = append(out, c)
		}
	}
	return out
}

// moveFile moves src to dst. It first tries an atomic os.Rename; across
// filesystems (EXDEV) it falls back to a DURABLE copy+remove. It never
// overwrites an existing dst.
//
// expectedSHA, when non-empty, is the source's known SHA256: the copy fallback
// verifies the destination hashes to it before the source is removed.
func moveFile(src, dst, expectedSHA string) error {
	// Never clobber an existing destination.
	if _, err := os.Stat(dst); err == nil {
		return fmt.Errorf("destination already exists: %s", dst)
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	// Cross-filesystem (or otherwise un-renamable): durable copy then remove.
	return copyThenRemove(src, dst, expectedSHA)
}

// copyThenRemove copies src to dst preserving mode+mtime, fsyncs the file and its
// directory, verifies the copy (size, and SHA256 when expectedSHA is known), and
// only then removes src. On ANY copy/verify error it removes the partial dst and
// leaves src intact — so a crash or failure can never lose the source with only a
// truncated trash copy to show for it.
func copyThenRemove(src, dst, expectedSHA string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	fi, err := in.Stat()
	if err != nil {
		return err
	}

	// O_EXCL: refuse to reuse an existing dst even here.
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fi.Mode().Perm())
	if err != nil {
		return err
	}
	// From here on, any failure must clean up the partial destination.
	fail := func(e error) error {
		_ = out.Close()
		_ = os.Remove(dst)
		return e
	}

	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, h), in)
	if err != nil {
		return fail(err)
	}
	// fsync the file's data before we trust it enough to delete the source.
	if err := out.Sync(); err != nil {
		return fail(err)
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(dst)
		return err
	}

	// Preserve permissions and modification time (os.Create/O_CREATE would have
	// reset mode to ~umask and mtime to now).
	if err := os.Chmod(dst, fi.Mode().Perm()); err != nil {
		_ = os.Remove(dst)
		return err
	}
	if err := os.Chtimes(dst, time.Now(), fi.ModTime()); err != nil {
		_ = os.Remove(dst)
		return err
	}

	// Verify the copy: size always; content hash when the source SHA is known.
	if n != fi.Size() {
		_ = os.Remove(dst)
		return fmt.Errorf("copy of %s is %d bytes, want %d", src, n, fi.Size())
	}
	if expectedSHA != "" && !hashutil.Equal(hex.EncodeToString(h.Sum(nil)), expectedSHA) {
		_ = os.Remove(dst)
		return fmt.Errorf("copy of %s failed hash verification", src)
	}

	// fsync the destination directory so the new entry itself survives a crash
	// before we remove the source.
	if err := fsyncDir(filepath.Dir(dst)); err != nil {
		_ = os.Remove(dst)
		return err
	}

	return os.Remove(src)
}

// fsyncDir flushes a directory's metadata (the newly created entry) to disk.
func fsyncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		// Some filesystems reject directory fsync; treat that as non-fatal so the
		// move still succeeds where a dir-sync is simply unsupported.
		if errors.Is(err, syscall.EINVAL) {
			return nil
		}
		return err
	}
	return nil
}

func writeManifest(path string, man manifest) error {
	data, err := json.MarshalIndent(man, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// quarantineReason summarizes a batch's reasons for its header row.
func quarantineReason(moves []PlannedMove) string {
	seen := map[string]bool{}
	var reasons []string
	for _, m := range moves {
		if m.Reason != "" && !seen[m.Reason] {
			seen[m.Reason] = true
			reasons = append(reasons, m.Reason)
		}
	}
	return strings.Join(reasons, ",")
}
