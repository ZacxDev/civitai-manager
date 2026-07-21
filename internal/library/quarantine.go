package library

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

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
		if skip := s.refuse(lf, roots, moving, allModels); skip != "" {
			plan.Skipped = append(plan.Skipped, SkippedFile{ID: lf.ID, Path: lf.Path, Reason: skip})
			continue
		}
		if _, err := os.Stat(lf.Path); err != nil {
			plan.Skipped = append(plan.Skipped, SkippedFile{ID: lf.ID, Path: lf.Path, Reason: "missing on disk"})
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
func (s *Scanner) applyQuarantine(plan *QuarantinePlan, roots []string) (*QuarantinePlan, error) {
	if len(plan.Moves) == 0 {
		return plan, nil
	}
	created := s.nowFn()
	batchID, err := s.store.CreateQuarantineBatch(s.opts.TrashDir, "", quarantineReason(plan.Moves))
	if err != nil {
		return nil, err
	}
	batchName := s.batchName(batchID)
	batchDir := filepath.Join(s.opts.TrashDir, batchName)

	for i := range plan.Moves {
		m := &plan.Moves[i]
		dst := s.trashPathFor(m.OriginalPath, roots, batchName)
		m.TrashPath = dst
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return nil, fmt.Errorf("create trash dir: %w", err)
		}
		if err := moveFile(m.OriginalPath, dst); err != nil {
			return nil, fmt.Errorf("move %s: %w", m.OriginalPath, err)
		}
		if _, err := s.store.AddQuarantinedFile(store.QuarantinedFile{
			BatchID: batchID, OriginalPath: m.OriginalPath, TrashPath: dst,
			Reason: m.Reason, IsSidecar: m.IsSidecar, SHA256: m.SHA256, SizeBytes: m.SizeBytes,
		}); err != nil {
			return nil, err
		}
		// Drop the index row: the file no longer lives at OriginalPath.
		if err := s.store.DeleteLocalFileByPath(m.OriginalPath); err != nil {
			return nil, err
		}
	}

	// Write the undo manifest.
	manifestPath := filepath.Join(batchDir, "manifest.json")
	man := manifest{BatchID: batchID, CreatedAt: created.Format("2006-01-02T15:04:05Z07:00"), TrashDir: batchDir, Moves: plan.Moves}
	if err := writeManifest(manifestPath, man); err != nil {
		return nil, err
	}
	if err := s.store.SetBatchManifest(batchID, manifestPath); err != nil {
		return nil, err
	}

	plan.Applied = true
	plan.BatchID = batchID
	return plan, nil
}

// refuse returns a non-empty safety reason if lf must not be quarantined.
func (s *Scanner) refuse(lf store.LocalFile, roots []string, moving map[int64]bool, models []store.LocalFile) string {
	if lf.Status == store.LocalStatusUnmatched || lf.Status == store.LocalStatusUnmatchedPending {
		return "refusing to quarantine an unmatched file"
	}
	if !lf.IsCandidate() {
		return "not a deletion candidate"
	}
	if !withinRoots(lf.Path, roots) {
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
		if err := moveFile(f.TrashPath, f.OriginalPath); err != nil {
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

// trashPathFor maps an original path to its trash destination, preserving the
// file's path relative to its containing scan root.
func (s *Scanner) trashPathFor(orig string, roots []string, batchName string) string {
	rel := filepath.Base(orig)
	for _, root := range roots {
		if r, err := filepath.Rel(root, orig); err == nil && !strings.HasPrefix(r, "..") {
			rel = r
			break
		}
	}
	return filepath.Join(s.opts.TrashDir, batchName, rel)
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

// moveFile renames src to dst, falling back to copy+remove across filesystems.
func moveFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Remove(src)
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
