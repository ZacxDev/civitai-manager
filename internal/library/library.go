// Package library scans a user's model directories, matches each weight file to
// its CivitAI model version, analyzes the collection for deletion candidates
// (superseded versions, exact duplicates, broken sidecars/partials), and can
// QUARANTINE flagged files into a trash dir with an undo manifest.
//
// Two hard invariants shape the design:
//
//   - The scan is REPORT-ONLY. It reads, hashes, matches, analyzes and records
//     to the store. It never moves or renames a user's model files.
//   - Quarantine NEVER hard-deletes. Acting on a candidate MOVES the file (and
//     its sidecars) into a trash dir; a manifest makes the move reversible.
//
// The scanner's expensive work (hashing multi-GB files) is skipped via an
// incremental cache: a file whose size AND mtime are unchanged from its stored
// row reuses the cached hash and match.
package library

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
	"github.com/ZacxDev/civitai-manager/internal/hashutil"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// DefaultExtensions is the model-weight file extension set. Keep this the single
// source of truth; the CLI/config may override it.
var DefaultExtensions = []string{
	".safetensors", ".ckpt", ".pt", ".pth", ".bin", ".gguf", ".onnx", ".sft",
}

// Sidecar suffixes the scanner recognises (ordered longest-first so
// ".civitai.info" and ".preview.png" match before a bare ".png").
const (
	sidecarInfo    = ".civitai.info"
	sidecarPreview = ".preview.png"
	sidecarPNG     = ".png"
	partSuffix     = ".part"
)

// ExtensionSet builds a lower-cased lookup set from a slice; an empty/nil slice
// yields the default set.
func ExtensionSet(exts []string) map[string]bool {
	if len(exts) == 0 {
		exts = DefaultExtensions
	}
	set := make(map[string]bool, len(exts))
	for _, e := range exts {
		e = strings.TrimSpace(strings.ToLower(e))
		if e == "" {
			continue
		}
		if !strings.HasPrefix(e, ".") {
			e = "." + e
		}
		set[e] = true
	}
	return set
}

// Options configures a scan.
type Options struct {
	// Paths are the directories to scan (each walked recursively). Empty means
	// "scan ModelRoot".
	Paths []string
	// ModelRoot is the canonical download root; it is always an allowed scan
	// root and the default trash-dir parent.
	ModelRoot string
	// TrashDir is where quarantine moves files (default <ModelRoot>/.trash). The
	// scan always skips it.
	TrashDir string
	// Extensions is the model-weight extension set (default DefaultExtensions).
	Extensions map[string]bool
	// NoRemote disables all CivitAI API calls: matching relies solely on valid
	// local .civitai.info sidecars; everything else is recorded unmatched. Local
	// analysis (duplicates, broken) still runs.
	NoRemote bool
	// MatchConcurrency caps the number of IN-FLIGHT CivitAI by-hash lookups the
	// scan may have outstanding at once, SHARED across the whole hashing worker
	// pool. Hashing stays fully parallel (scanWorkerCap); only the remote match
	// calls are throttled, so the disk win is preserved without bursting the API.
	// 0 or negative means the default (matchConcurrencyDefault, 3).
	MatchConcurrency int
	// MaxFiles bounds how many model-extension files the walk will collect before
	// aborting with ErrScanTooLarge. 0 means unlimited (the CLI default — the
	// operator typed the path knowingly). The web endpoint sets a finite cap so
	// its arbitrary-path walk cannot be unbounded.
	MaxFiles int
	// OnFile, when non-nil, STREAMS each model file's per-file result as it is
	// scanned (after its index row is upserted, BEFORE the cross-file
	// duplicate/superseded analysis runs). It mirrors DiscoverOptions.OnInstall:
	// the web layer appends each result into a background job so a /status poll
	// shows the growing list. Since the scan processes files with a bounded
	// concurrent worker pool, OnFile is invoked from MULTIPLE worker goroutines
	// CONCURRENTLY (and always OUTSIDE any scanner-internal lock), so the callback
	// MUST be safe to call concurrently — the web layer's appender guards itself
	// with its own mutex. It does NOT replace the ScanReport/local_files
	// persistence; it is an additional, incremental view.
	OnFile func(FileResult)
	// OnDiscovered, when non-nil, is invoked EXACTLY ONCE — right after the
	// directory walk completes, before any per-file streaming begins — with the
	// TOTAL number of model-weight files the walk found (len(walkResult.modelFiles),
	// which the completed report also reports as FilesScanned). It is the
	// denominator for progress: the web layer shows "N / total discovered" so the
	// user sees how far a scan has to go. On a large library the WALK itself takes
	// time (finding the files across TBs of directories), so the total only becomes
	// known once the walk finishes — until then the web layer shows "walking…".
	// Mirrors OnFile as an injectable streaming seam; a cancelled/failed walk
	// returns before it fires.
	OnDiscovered func(total int)
}

// FileResult is the per-file outcome streamed via Options.OnFile as a scan walks
// its roots. It carries only what is known at scan time (the match state and the
// row just written); the cross-file deletion-candidate classification
// (duplicate/superseded) is derived later, in analyze(), and surfaces in the
// completed ScanReport / local_files view, not here.
type FileResult struct {
	// Path is the model file's absolute path; Name is its on-disk base name.
	Path string
	Name string
	// SizeBytes is the file size; SHA256 is its content hash (possibly reused
	// from the incremental cache).
	SizeBytes int64
	SHA256    string
	// Status is the match state (store.LocalStatusMatched / Unmatched /
	// UnmatchedPending / Broken).
	Status string
	// ModelID / VersionID are the matched CivitAI ids, or nil when unmatched.
	ModelID   *int
	VersionID *int
	// HasPreview reports whether a sibling ".preview.png" image exists on disk.
	HasPreview bool
}

// Scanner runs the read-only scan/match/analyze pipeline and the quarantine
// mover against a store.
type Scanner struct {
	store  *store.Store
	reader civitai.Reader
	log    *slog.Logger
	opts   Options

	// Injectable seams (tests override these; production uses the defaults).
	//
	// hashFn computes a file's SHA256. Injecting it lets tests count how often a
	// file is actually re-hashed (the incremental-cache assertion).
	hashFn func(path string) (string, error)
	nowFn  func() time.Time
	// moveFn moves src to dst (durably across filesystems), verifying against
	// expectedSHA when non-empty. Injecting it lets tests force a mid-batch move
	// failure to exercise the partial-batch recovery path.
	moveFn func(src, dst, expectedSHA string) error
	// waitFn sleeps for d honouring ctx (backoff between by-hash retries).
	waitFn func(ctx context.Context, d time.Duration)
	// maxHashRetries bounds by-hash retries on rate-limit/transient errors before
	// a file is left unmatched-pending.
	maxHashRetries int
	// matchLimiter paces the remote by-hash lookups as ONE client: a shared
	// in-flight semaphore plus a pool-wide 429 cooldown. Shared by all workers.
	matchLimiter *matchLimiter
}

// NewScanner builds a Scanner. A nil logger discards output; a nil reader is
// allowed only with Options.NoRemote.
func NewScanner(st *store.Store, reader civitai.Reader, opts Options, log *slog.Logger) *Scanner {
	if log == nil {
		log = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	}
	if opts.Extensions == nil {
		opts.Extensions = ExtensionSet(nil)
	}
	if opts.TrashDir == "" && opts.ModelRoot != "" {
		opts.TrashDir = filepath.Join(opts.ModelRoot, ".trash")
	}
	if len(opts.Paths) == 0 && opts.ModelRoot != "" {
		opts.Paths = []string{opts.ModelRoot}
	}
	return &Scanner{
		store:          st,
		reader:         reader,
		log:            log,
		opts:           opts,
		hashFn:         hashutil.SumFile,
		nowFn:          func() time.Time { return time.Now().UTC() },
		moveFn:         moveFile,
		waitFn:         sleepCtx,
		maxHashRetries: 4,
		matchLimiter:   newMatchLimiter(opts.MatchConcurrency),
	}
}

// Roots returns the absolute, cleaned scan roots (ModelRoot plus configured
// Paths, de-duplicated). Used both for walking and for the quarantine
// containment check.
func (s *Scanner) Roots() []string {
	seen := map[string]bool{}
	var roots []string
	add := func(p string) {
		if p == "" {
			return
		}
		abs, err := filepath.Abs(p)
		if err != nil {
			abs = filepath.Clean(p)
		}
		if !seen[abs] {
			seen[abs] = true
			roots = append(roots, abs)
		}
	}
	for _, p := range s.opts.Paths {
		add(p)
	}
	add(s.opts.ModelRoot)
	return roots
}

// sleepCtx sleeps for d, returning early if ctx is cancelled.
func sleepCtx(ctx context.Context, d time.Duration) {
	if d <= 0 {
		return
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// resolveRoots symlink-resolves each scan root ONCE (via resolveReal) so the
// result can be reused across many withinRoots checks. The roots never change
// during a scan/quarantine pass, so resolving them per row (as an earlier
// version did) recomputed the same EvalSymlinks O(rows) times; callers now
// resolve here at setup and pass the result to withinRoots.
func resolveRoots(roots []string) []string {
	resolved := make([]string, len(roots))
	for i, root := range roots {
		resolved[i] = resolveReal(root)
	}
	return resolved
}

// withinRoots reports whether abs is equal to or nested under any of
// resolvedRoots. It is the containment guard the quarantine mover uses so it can
// never touch a file outside a configured scan root.
//
// resolvedRoots MUST already be symlink-resolved (see resolveRoots); withinRoots
// resolves only the candidate path each call. Containment is thus checked on
// symlink-RESOLVED real paths, so a path that lexically looks inside a root but
// reaches outside it through a symlinked component cannot escape the guard.
// Normal (non-symlink) paths resolve to themselves, preserving prior behavior.
func withinRoots(abs string, resolvedRoots []string) bool {
	abs = resolveReal(abs)
	for _, root := range resolvedRoots {
		if abs == root {
			return true
		}
		rel, err := filepath.Rel(root, abs)
		if err != nil {
			continue
		}
		if rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != "." {
			return true
		}
	}
	return false
}

// resolveReal returns the symlink-resolved, cleaned form of p. If p itself does
// not exist yet (a not-yet-created quarantine/trash destination), it resolves the
// NEAREST EXISTING ancestor directory and rejoins the remaining leaf components,
// so containment is judged against the real parent rather than a lexical guess.
// On any resolution failure it falls back to the lexically-cleaned path, keeping
// the guard defensive (never panicking, never erroring).
func resolveReal(p string) string {
	p = filepath.Clean(p)
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	dir := p
	tail := ""
	for {
		parent := filepath.Dir(dir)
		tail = filepath.Join(filepath.Base(dir), tail)
		if parent == dir {
			return p // no existing ancestor resolved: lexical fallback
		}
		if r, err := filepath.EvalSymlinks(parent); err == nil {
			return filepath.Join(r, tail)
		}
		dir = parent
	}
}
