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

// withinRoots reports whether abs is equal to or nested under any of roots. It
// is the containment guard the quarantine mover uses so it can never touch a
// file outside a configured scan root.
//
// Containment is checked on symlink-RESOLVED real paths (resolveReal), so a path
// that lexically looks inside a root but reaches outside it through a symlinked
// component cannot escape the guard. Normal (non-symlink) paths resolve to
// themselves, preserving prior behavior.
func withinRoots(abs string, roots []string) bool {
	abs = resolveReal(abs)
	for _, root := range roots {
		root = resolveReal(root)
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
