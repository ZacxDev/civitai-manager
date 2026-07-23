package library

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/ZacxDev/civitai-manager/internal/store"
)

// scanWorkerCap bounds the concurrent per-file processing pool. Hashing is
// disk-bound and the CivitAI by-hash lookup is network-bound, so overlapping
// them across a handful of workers hides API latency behind parallel SSD reads
// and cuts wall-clock several-fold on a real library. The cap is 8: beyond that,
// parallel multi-GB hashing thrashes the disk and the extra API concurrency buys
// little (the store serializes writes at MaxOpenConns(1)), so min(NumCPU, 8) is
// the sweet spot — mirroring the discovery walker's bounded pool (discoverWorkerCap).
const scanWorkerCap = 8

// ErrScanTooLarge is returned when a scan walks more model-extension files than
// the configured Options.MaxFiles budget. It aborts the walk BEFORE any hashing
// or store mutation, so a too-broad (or adversarial) path cannot tie up the
// process; the caller surfaces a "narrow the path" message.
var ErrScanTooLarge = errors.New("scan too large; narrow the path")

// ScanReport summarizes a completed scan.
type ScanReport struct {
	Roots        []string
	FilesScanned int
	Hashed       int // model files actually (re)hashed this run
	Reused       int // model files served from the incremental cache
	Matched      int
	Unmatched    int
	Pending      int // unmatched-pending (rate-limited/transient; retry later)
	Broken       int
	Superseded   int
	Duplicate    int
	Reclaimable  int64             // total bytes of all flagged candidates
	Files        []store.LocalFile // all model files, ordered by path
	Candidates   []store.LocalFile // flagged deletion candidates
}

// walkResult is the raw file inventory a directory walk collects.
type walkResult struct {
	modelFiles []string
	parts      []string
	infos      []string
	previews   []string
	// modelRoots maps each model file's absolute path to the scan root it was
	// found under (the first root that reached it, in Roots() order). Recorded on
	// each file's index row so quarantine can act on files scanned via an extra
	// --path without re-specifying that directory.
	modelRoots map[string]string
}

// walk inventories the scan roots, collecting model-weight files (by extension)
// plus the sidecars/partials the broken-file analysis needs. It skips hidden
// directories and the trash dir, and never mutates anything.
//
// The walk is context-cancellable: ctx.Err() is checked INSIDE the WalkDir
// callback, so a client disconnect, a deadline, or Ctrl-C aborts the (possibly
// long) walk phase promptly rather than only after it finishes. When
// Options.MaxFiles > 0 the walk aborts with ErrScanTooLarge once that many
// model-extension files have been seen, bounding the arbitrary-path primitive
// the web endpoint exposes.
func (s *Scanner) walk(ctx context.Context) (walkResult, error) {
	var wr walkResult
	wr.modelRoots = map[string]string{}
	seen := map[string]bool{}
	trash := filepath.Clean(s.opts.TrashDir)
	modelCount := 0

	for _, root := range s.Roots() {
		root := root
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if cerr := ctx.Err(); cerr != nil {
				// Abort the walk on cancel/deadline; returning the ctx error stops
				// WalkDir immediately and propagates cleanly to the caller.
				return cerr
			}
			if err != nil {
				// A permission error on one subtree must not abort the whole scan.
				s.log.Warn("scan: skipping unreadable path", "path", path, "err", err)
				if d != nil && d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if d.IsDir() {
				if path != root && strings.HasPrefix(d.Name(), ".") {
					return fs.SkipDir // hidden dir (also covers a hidden trash dir)
				}
				if trash != "" && (path == trash || isUnder(path, trash)) {
					return fs.SkipDir
				}
				return nil
			}
			abs, aerr := filepath.Abs(path)
			if aerr != nil {
				abs = filepath.Clean(path)
			}
			if seen[abs] {
				return nil
			}
			seen[abs] = true
			if s.opts.Extensions[strings.ToLower(filepath.Ext(abs))] {
				modelCount++
				if s.opts.MaxFiles > 0 && modelCount > s.opts.MaxFiles {
					return fmt.Errorf("%w (limit %d model files)", ErrScanTooLarge, s.opts.MaxFiles)
				}
			}
			classify(&wr, abs, s.opts.Extensions, root)
			return nil
		})
		if err != nil {
			return wr, err
		}
	}
	return wr, nil
}

// classify buckets one file by its name/extension. root is the scan root the
// file was found under (recorded for model files so quarantine knows which root
// covered them).
func classify(wr *walkResult, abs string, exts map[string]bool, root string) {
	lower := strings.ToLower(abs)
	switch {
	case strings.HasSuffix(lower, partSuffix):
		wr.parts = append(wr.parts, abs)
	case strings.HasSuffix(lower, sidecarInfo):
		wr.infos = append(wr.infos, abs)
	case strings.HasSuffix(lower, sidecarPreview), strings.HasSuffix(lower, sidecarPNG):
		wr.previews = append(wr.previews, abs)
	default:
		if exts[strings.ToLower(filepath.Ext(abs))] {
			wr.modelFiles = append(wr.modelFiles, abs)
			wr.modelRoots[abs] = root
		}
	}
}

// Scan runs the full read-only pipeline: walk, hash (incrementally), match, then
// analyze for deletion candidates. It records everything to the store and
// returns a report. It never moves or renames a user file.
func (s *Scanner) Scan(ctx context.Context) (*ScanReport, error) {
	wr, err := s.walk(ctx)
	if err != nil {
		return nil, err
	}

	// The walk is done: the total discovered model-file count is now known. Report
	// it ONCE, before per-file streaming, so the web layer can show progress against
	// this denominator ("N / total discovered"). Fires only on a successful walk (a
	// cancelled/errored walk returns above), and before OnFile ever fires.
	if s.opts.OnDiscovered != nil {
		s.opts.OnDiscovered(len(wr.modelFiles))
	}

	report := &ScanReport{Roots: s.Roots()}

	// NOTE: candidate flags are deliberately NOT cleared here. Clearing up front
	// meant an aborted/failed scan (a cancelled walk, a per-file error, a store
	// hiccup) wiped the prior candidate flags AND left local_files half-rewritten.
	// Instead the walk+process+prune below rebuild the index first, and the stale
	// flags are cleared only immediately before analyze() re-derives them (see
	// below) — so a failed scan leaves the previous candidate state intact.

	// Pre-build the seen-set from the full model-file inventory (known up front):
	// pruneMissingModels only needs "which paths were walked", so building it here
	// is equivalent to the old per-file insertion and needs no locking during the
	// concurrent pass below.
	seenPaths := make(map[string]bool, len(wr.modelFiles))
	for _, path := range wr.modelFiles {
		seenPaths[path] = true
	}

	// PHASE 1 — WALK+HASH: concurrently hash and LOCALLY resolve every model file.
	// Hashing is the disk cost (unchanged); this pass makes NO network call. Each
	// file lands in prepared[i] either fully resolved (incremental-cache hit, a
	// valid .civitai.info sidecar, offline mode, or unchanged bytes) or flagged
	// needsRemote with its SHA collected for the single batch lookup below.
	prepared := s.hashAndPrepare(ctx, wr.modelFiles, wr.modelRoots)

	// PHASE 2 — BATCH-MATCH: one GetModelVersionsByHashes call (the SDK chunks to
	// <=10k hashes/request and merges) resolves EVERY needsRemote file at once —
	// the batch replacement for the retired N sequential by-hash GETs. On failure
	// the affected files are marked UnmatchedPending below, never falsely flagged.
	batchMap, batchErr := s.batchMatch(ctx, prepared)

	// PHASE 3 — STREAM: resolve each file from the prefetched map, persist its row,
	// stream its card, and tally — in walk order. The store serializes writes
	// (MaxOpenConns(1)+WAL), so a sequential persist pass is simple and correct;
	// the per-file OnFile card now appears as fast as hashing + the single batch
	// allow, not gated on N sequential network calls.
	var tally scanTally
	for _, pf := range prepared {
		if pf == nil {
			continue // a file that failed to stat/hash (already logged) — skip it
		}
		if ctx.Err() != nil {
			break // a Stop/shutdown aborts the persist pass promptly
		}
		result := pf.result
		if pf.needsRemote {
			result = resolveFromBatch(pf.sha, batchMap, batchErr)
		}
		mtime := pf.fi.ModTime().UTC()
		lf := store.LocalFile{
			Path:      pf.path,
			SHA256:    pf.sha,
			AutoV2:    result.autov2,
			ModelID:   result.modelID,
			VersionID: result.versionID,
			SizeBytes: pf.fi.Size(),
			Mtime:     &mtime,
			Status:    result.status,
			Kind:      store.LocalKindModel,
			ScanRoot:  pf.scanRoot,
		}
		if err := s.store.UpsertLocalFile(lf); err != nil {
			s.log.Warn("scan: upsert failed", "path", pf.path, "err", err)
			continue
		}
		st := pf.stats
		st.status = result.status
		tally.add(st)
		// STREAM the per-file result AFTER the row is persisted (mirrors how the
		// discovery collector calls OnInstall).
		if s.opts.OnFile != nil {
			s.opts.OnFile(FileResult{
				Path:       lf.Path,
				Name:       filepath.Base(lf.Path),
				SizeBytes:  lf.SizeBytes,
				SHA256:     lf.SHA256,
				Status:     lf.Status,
				ModelID:    lf.ModelID,
				VersionID:  lf.VersionID,
				HasPreview: hasPreviewSibling(pf.path, s.opts.Extensions),
			})
		}
	}
	tally.mergeInto(report)

	// A cancelled scan returns partial results with the ctx error, mirroring the
	// old sequential early-return (prune/analyze are skipped on abort).
	if err := ctx.Err(); err != nil {
		return report, err
	}
	report.FilesScanned = len(wr.modelFiles)

	// Prune index rows for model files that no longer exist under a scanned root
	// (deleted/renamed since the last scan) so duplicate/superseded analysis is
	// not skewed by phantom entries.
	if err := s.pruneMissingModels(seenPaths); err != nil {
		s.log.Warn("scan: prune failed", "err", err)
	}

	// The walk+process+prune completed: only now is it safe to reset stale
	// candidate flags, immediately before analyze() re-derives them from the
	// refreshed index. Clearing here (rather than up front) keeps the prior
	// candidate state intact on any earlier abort/failure.
	if err := s.store.ClearCandidates(); err != nil {
		return report, err
	}

	// Analyze the (now-current) index for deletion candidates.
	if err := s.analyze(&wr, report); err != nil {
		return report, err
	}

	// Assemble the report from the store's authoritative view.
	files, err := s.store.ListLocalFiles()
	if err != nil {
		return report, err
	}
	for _, f := range files {
		if f.Kind == store.LocalKindModel {
			report.Files = append(report.Files, f)
		}
		if f.IsCandidate() {
			report.Candidates = append(report.Candidates, f)
			report.Reclaimable += f.SizeBytes
			switch f.CandidateReason {
			case store.CandidateSuperseded:
				report.Superseded++
			case store.CandidateDuplicate:
				report.Duplicate++
			case store.CandidateBroken:
				report.Broken++
			}
		}
	}
	return report, nil
}

// fileStats reports what one prepared file contributes to the ScanReport hashed/
// reused/status counters. prepareModelFile fills hashed/reused during the
// concurrent hash pass; the persist pass sets status once the match is resolved.
type fileStats struct {
	hashed bool   // hashFn was invoked (a real (re)hash)
	reused bool   // served from the incremental cache (no hash, no lookup)
	status string // final match status → the matched/pending/unmatched counters
}

// scanTally accumulates the ScanReport match/hash counters across the sequential
// persist pass, folded into the report once at the end.
type scanTally struct {
	hashed, reused, matched, pending, unmatched int
}

func (t *scanTally) add(st fileStats) {
	if st.reused {
		t.reused++
	} else if st.hashed {
		t.hashed++
	}
	switch st.status {
	case store.LocalStatusMatched:
		t.matched++
	case store.LocalStatusUnmatchedPending:
		t.pending++
	default:
		t.unmatched++
	}
}

func (t *scanTally) mergeInto(r *ScanReport) {
	r.Hashed += t.hashed
	r.Reused += t.reused
	r.Matched += t.matched
	r.Pending += t.pending
	r.Unmatched += t.unmatched
}

// preparedFile is one model file after the concurrent hash+local-resolve pass
// (phase 1), carrying everything the sequential persist pass (phase 3) needs. A
// file is EITHER fully resolved locally (result set, needsRemote false) — an
// incremental-cache hit, a verified sidecar, offline mode, or unchanged bytes —
// OR flagged needsRemote, in which case its final match comes from the batch
// by-hash map (phase 2) keyed by sha.
type preparedFile struct {
	path        string
	scanRoot    string
	fi          os.FileInfo
	sha         string
	stats       fileStats
	result      matchResult // the resolved match when needsRemote is false
	needsRemote bool        // true → resolve from the batch by-hash map by sha
}

// hashAndPrepare runs phase 1: a BOUNDED CONCURRENT worker pool hashes and
// locally resolves every model file (see scanWorkerCap for the cap rationale),
// returning a slice parallel to modelFiles (prepared[i] corresponds to
// modelFiles[i]; a nil entry is a file that failed to stat/hash and was logged).
// The indexed write is lock-free — each worker owns disjoint indices — and the
// pass makes NO network call, so disk-bound hashing still overlaps across workers.
func (s *Scanner) hashAndPrepare(ctx context.Context, modelFiles []string, modelRoots map[string]string) []*preparedFile {
	prepared := make([]*preparedFile, len(modelFiles))

	numWorkers := runtime.NumCPU()
	if numWorkers > scanWorkerCap {
		numWorkers = scanWorkerCap
	}
	if numWorkers < 1 {
		numWorkers = 1
	}

	type job struct {
		i    int
		path string
	}
	// The feeder selects on ctx.Done so a cancelled scan stops dispatching
	// promptly and never blocks on an undrained channel.
	jobCh := make(chan job)
	go func() {
		defer close(jobCh)
		for i, path := range modelFiles {
			select {
			case jobCh <- job{i: i, path: path}:
			case <-ctx.Done():
				return
			}
		}
	}()

	var wg sync.WaitGroup
	for w := 0; w < numWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobCh {
				// Abort BEFORE hashing the next file so a Stop/shutdown drains
				// promptly (a hung hash mid-Stop still lets the other workers exit).
				if ctx.Err() != nil {
					continue
				}
				pf, err := s.prepareModelFile(j.path, modelRoots[j.path])
				if err != nil {
					s.log.Warn("scan: file failed", "path", j.path, "err", err)
					continue
				}
				prepared[j.i] = pf
			}
		}()
	}
	wg.Wait()
	return prepared
}

// prepareModelFile hashes (or reuses the cached hash of) one model file and
// resolves as much of its match as it can WITHOUT a network call, returning a
// preparedFile. The incremental cache is the key optimization: a file whose size
// AND mtime match its stored row skips the expensive re-hash and re-uses the
// stored match. A file that still needs the authoritative remote lookup is
// flagged needsRemote (its SHA is batch-resolved later); nothing here touches the
// network.
//
// It only reads the store (GetLocalFileByPath) plus os.Stat and the hash — safe
// under concurrency: the Store holds no shared in-memory state and serializes at
// the driver. It mutates NO shared report.
func (s *Scanner) prepareModelFile(path, scanRoot string) (*preparedFile, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	cached, err := s.store.GetLocalFileByPath(path)
	if err != nil {
		return nil, err
	}

	pf := &preparedFile{path: path, scanRoot: scanRoot, fi: fi}

	if s.cacheHit(cached, fi) {
		// Size + mtime unchanged and the cached row is in a settled state: reuse
		// the stored hash AND match (no re-hash, no lookup).
		pf.sha = cached.SHA256
		pf.result = matchResult{status: cached.Status, modelID: cached.ModelID, versionID: cached.VersionID, autov2: cached.AutoV2}
		pf.stats.reused = true
		return pf, nil
	}

	sum, herr := s.hashFn(path)
	if herr != nil {
		return nil, herr
	}
	pf.sha = sum
	pf.stats.hashed = true

	if cached != nil && cached.SHA256 == sum && cached.Status != store.LocalStatusUnmatchedPending {
		// The bytes are unchanged (only mtime/size metadata differed) and the
		// cached match is settled: keep it instead of re-resolving. A cached
		// PENDING row falls through to be re-resolved (locally or via the batch).
		pf.result = matchResult{status: cached.Status, modelID: cached.ModelID, versionID: cached.VersionID, autov2: cached.AutoV2}
		return pf, nil
	}

	// Fresh match needed: resolve locally (sidecar / offline) or flag for the batch.
	pf.result, pf.needsRemote = s.resolveLocalMatch(path, sum)
	return pf, nil
}

// SetOnFile installs (or clears, with nil) the per-file streaming callback after
// construction. The web layer uses it to stream results into a background scan
// job without threading OnFile through NewScanner's every call site.
func (s *Scanner) SetOnFile(fn func(FileResult)) { s.opts.OnFile = fn }

// SetOnDiscovered installs (or clears, with nil) the walk-total callback after
// construction — the OnDiscovered twin of SetOnFile. The web layer uses it to
// record a scan's discovered denominator without threading OnDiscovered through
// NewScanner's every call site.
func (s *Scanner) SetOnDiscovered(fn func(total int)) { s.opts.OnDiscovered = fn }

// hasPreviewSibling reports whether a ".preview.png" image sits next to the
// model file (the Civitai-Helper preview convention: the model path with its
// weight extension replaced by ".preview.png"). It is a cheap os.Stat used only
// to annotate the streamed FileResult; a missing/unreadable sibling is reported
// absent.
func hasPreviewSibling(modelPath string, exts map[string]bool) bool {
	ext := filepath.Ext(modelPath)
	base := modelPath
	if exts[strings.ToLower(ext)] {
		base = strings.TrimSuffix(modelPath, ext)
	}
	fi, err := os.Stat(base + sidecarPreview)
	return err == nil && !fi.IsDir()
}

// cacheHit reports whether the cached row can be trusted without re-hashing: it
// exists, has a stored hash, its size and mtime match the file on disk, and it
// is in a settled match state (a pending row must be retried, so it is not a
// hit).
func (s *Scanner) cacheHit(cached *store.LocalFile, fi os.FileInfo) bool {
	if cached == nil || cached.SHA256 == "" || cached.Mtime == nil {
		return false
	}
	if cached.SizeBytes != fi.Size() {
		return false
	}
	if !cached.Mtime.Equal(fi.ModTime()) {
		return false
	}
	return cached.Status == store.LocalStatusMatched || cached.Status == store.LocalStatusUnmatched
}

// pruneMissingModels deletes model index rows whose path lives under a scanned
// root but was not seen this run (the file was deleted or moved).
func (s *Scanner) pruneMissingModels(seen map[string]bool) error {
	files, err := s.store.ListLocalFiles()
	if err != nil {
		return err
	}
	roots := resolveRoots(s.Roots())
	for _, f := range files {
		if f.Kind != store.LocalKindModel {
			continue
		}
		if seen[f.Path] {
			continue
		}
		if withinRoots(f.Path, roots) {
			if err := s.store.DeleteLocalFileByPath(f.Path); err != nil {
				return err
			}
		}
	}
	return nil
}

// isUnder reports whether path is nested under dir.
func isUnder(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
