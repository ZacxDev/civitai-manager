package library

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/ZacxDev/civitai-manager/internal/store"
)

// analyze flags deletion candidates on the current index: superseded versions,
// exact duplicates, and broken sidecars/partials. It only ever flags MATCHED
// model files (unmatched/orphan files are recorded but never a candidate) plus
// tracked broken non-model files. It records but never moves anything.
func (s *Scanner) analyze(wr *walkResult, report *ScanReport) error {
	files, err := s.store.ListLocalFiles()
	if err != nil {
		return err
	}
	var models []store.LocalFile
	for _, f := range files {
		if f.Kind == store.LocalKindModel {
			models = append(models, f)
		}
	}

	reason := map[int64]string{}
	flagDuplicates(models, reason, s.Roots(), s.opts.Extensions)
	flagSuperseded(models, reason)

	for id, r := range reason {
		if err := s.store.SetCandidateReason(id, r); err != nil {
			return err
		}
	}

	return s.analyzeBroken(wr)
}

// flagDuplicates flags all-but-one file in each set of byte-identical (same
// SHA256) files. This is a pure local-hash signal, so it works OFFLINE and does
// NOT require a CivitAI match — two identical copies are provably redundant. A
// file still resolving (unmatched-pending) is excluded so a transient rate limit
// never produces a false flag.
//
// The keeper is the BEST-ORGANIZED copy, so quarantine removes the ad-hoc copy
// and retains the canonical one (see pickKeeper for the ranking). It stays
// deterministic — the final tiebreak is shortest path, then lexical — so re-runs
// pick the same survivor.
//
// Note: an UNMATCHED duplicate is reported here but the quarantine mover still
// refuses to move an unmatched file (see quarantine.go) — so offline duplicate
// analysis surfaces the redundancy, while acting on it requires an online match.
func flagDuplicates(models []store.LocalFile, reason map[int64]string, roots []string, exts map[string]bool) {
	bySHA := map[string][]store.LocalFile{}
	for _, f := range models {
		if f.SHA256 == "" || f.Status == store.LocalStatusUnmatchedPending {
			continue
		}
		bySHA[strings.ToLower(f.SHA256)] = append(bySHA[strings.ToLower(f.SHA256)], f)
	}
	for _, group := range bySHA {
		if len(group) < 2 {
			continue
		}
		keeper := pickKeeper(group, roots, exts)
		for _, f := range group {
			if f.ID != keeper.ID {
				reason[f.ID] = store.CandidateDuplicate
			}
		}
	}
}

// flagSuperseded flags MATCHED files whose version id is below the highest LOCAL
// version id for the same model (a newer local copy exists). A single local
// version is never flagged. A file already flagged as a duplicate keeps that
// (stronger, byte-identical) reason.
func flagSuperseded(models []store.LocalFile, reason map[int64]string) {
	byModel := map[int][]store.LocalFile{}
	for _, f := range models {
		if f.Status != store.LocalStatusMatched || f.ModelID == nil || f.VersionID == nil {
			continue
		}
		byModel[*f.ModelID] = append(byModel[*f.ModelID], f)
	}
	for _, group := range byModel {
		maxVer := 0
		distinct := map[int]bool{}
		for _, f := range group {
			distinct[*f.VersionID] = true
			if *f.VersionID > maxVer {
				maxVer = *f.VersionID
			}
		}
		if len(distinct) < 2 {
			continue // only one local version of this model: keep it
		}
		for _, f := range group {
			if *f.VersionID < maxVer {
				if _, already := reason[f.ID]; !already {
					reason[f.ID] = store.CandidateSuperseded
				}
			}
		}
	}
}

// pickKeeper returns the survivor of a duplicate set: the best-organized copy,
// so acting on the flags removes the loose ad-hoc copy and keeps the canonical
// one. Copies are ranked, in order, by:
//
//	(a) residing in the canonical <type>/<creator>/<model>/<file> layout under a
//	    scan root (matching the tool's DestPath structure);
//	(b) having the most sidecars present (.civitai.info, .preview.png);
//	(c) a deterministic final tiebreak — shortest path, then lexical — so
//	    equally-organized copies always pick the same survivor across re-runs.
func pickKeeper(group []store.LocalFile, roots []string, exts map[string]bool) store.LocalFile {
	best := group[0]
	bestRank := keeperRankOf(best, roots, exts)
	for _, f := range group[1:] {
		r := keeperRankOf(f, roots, exts)
		if r.better(bestRank) {
			best, bestRank = f, r
		}
	}
	return best
}

// keeperRank scores a duplicate copy for keeper selection (higher canonical/
// sidecars is better; shorter/lexically-smaller path is the deterministic
// tiebreak).
type keeperRank struct {
	canonical bool
	sidecars  int
	path      string
}

// better reports whether a is a strictly better keeper than b.
func (a keeperRank) better(b keeperRank) bool {
	if a.canonical != b.canonical {
		return a.canonical
	}
	if a.sidecars != b.sidecars {
		return a.sidecars > b.sidecars
	}
	if len(a.path) != len(b.path) {
		return len(a.path) < len(b.path)
	}
	return a.path < b.path
}

func keeperRankOf(f store.LocalFile, roots []string, exts map[string]bool) keeperRank {
	return keeperRank{
		canonical: inCanonicalLayout(f.Path, roots),
		sidecars:  len(modelSidecars(f.Path, exts)),
		path:      f.Path,
	}
}

// inCanonicalLayout reports whether path sits at the tool's download layout
// depth — <root>/<type>/<creator>/<model>/<file> — under any scan root (i.e. its
// path relative to a root has exactly four components). A loose copy dropped
// directly under a root, or nested at any other depth, is not canonical.
func inCanonicalLayout(path string, roots []string) bool {
	for _, root := range roots {
		rel, err := filepath.Rel(root, path)
		if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
			continue
		}
		if len(strings.Split(rel, string(filepath.Separator))) == 4 {
			return true
		}
	}
	return false
}

// analyzeBroken (re)computes the tracked broken non-model files: abandoned
// `.part` downloads, empty `.civitai.info` sidecars, and orphan previews. It
// clears prior broken rows under the scanned roots first, then records the
// current set — so a fixed condition disappears on the next scan.
func (s *Scanner) analyzeBroken(wr *walkResult) error {
	roots := resolveRoots(s.Roots())
	existing, err := s.store.ListLocalFiles()
	if err != nil {
		return err
	}
	for _, f := range existing {
		if f.Kind == store.LocalKindSidecar && withinRoots(f.Path, roots) {
			if err := s.store.DeleteLocalFileByPath(f.Path); err != nil {
				return err
			}
		}
	}

	modelBases := modelBaseSet(wr.modelFiles, s.opts.Extensions)

	record := func(path string) error {
		size := int64(0)
		if fi, err := os.Stat(path); err == nil {
			size = fi.Size()
		}
		return s.store.UpsertLocalFile(store.LocalFile{
			Path:            path,
			SizeBytes:       size,
			Status:          store.LocalStatusBroken,
			CandidateReason: store.CandidateBroken,
			Kind:            store.LocalKindSidecar,
		})
	}

	// Abandoned .part files: a stray partial with no in-flight download row.
	for _, part := range wr.parts {
		dest := strings.TrimSuffix(part, partSuffix)
		active, err := s.store.ActiveDownloadForDest(dest)
		if err != nil {
			return err
		}
		if !active {
			if err := record(part); err != nil {
				return err
			}
		}
	}

	// Empty .civitai.info sidecars.
	for _, info := range wr.infos {
		if isEmptyFile(info) {
			if err := record(info); err != nil {
				return err
			}
		}
	}

	// Orphan previews: a preview/user image whose sibling model file is gone.
	for _, prev := range wr.previews {
		if !modelBases[previewBase(prev)] {
			if err := record(prev); err != nil {
				return err
			}
		}
	}
	return nil
}

// modelBaseSet maps each model file to its base (path minus the model
// extension) for orphan-preview sibling detection.
func modelBaseSet(modelFiles []string, exts map[string]bool) map[string]bool {
	set := make(map[string]bool, len(modelFiles))
	for _, m := range modelFiles {
		ext := filepath.Ext(m)
		if exts[strings.ToLower(ext)] {
			set[strings.TrimSuffix(m, ext)] = true
		} else {
			set[m] = true
		}
	}
	return set
}

// previewBase strips a preview/user-image suffix to the model base it belongs
// to (".preview.png" before a bare ".png").
func previewBase(prev string) string {
	lower := strings.ToLower(prev)
	switch {
	case strings.HasSuffix(lower, sidecarPreview):
		return prev[:len(prev)-len(sidecarPreview)]
	case strings.HasSuffix(lower, sidecarPNG):
		return prev[:len(prev)-len(sidecarPNG)]
	}
	return prev
}

// isEmptyFile reports whether a file is missing or holds only whitespace.
func isEmptyFile(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false // unreadable: don't flag (avoid a false positive)
	}
	return len(strings.TrimSpace(string(data))) == 0
}
