package library

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

// Install kinds.
const (
	KindComfyUI = "comfyui"
	KindA1111   = "a1111"
)

// Confidence levels for a discovered install. A marker match backed by a .git
// repo is a "high"-confidence genuine install; markers alone are "low".
const (
	ConfidenceHigh = "high"
	ConfidenceLow  = "low"
)

// GitState captures the cheap git facts about a discovered install directory,
// populated only when a .git entry is present. Branch/Dirty are best-effort: a
// missing git binary or a non-repo leaves them zero-valued but IsRepo true.
type GitState struct {
	IsRepo bool
	Branch string
	Dirty  bool
}

// Install is one auto-discovered ComfyUI / Automatic1111 (or Forge) installation.
type Install struct {
	// Path is the install's root directory (absolute).
	Path string
	// Kind is KindComfyUI or KindA1111.
	Kind string
	// Confidence is ConfidenceHigh (.git + markers) or ConfidenceLow (markers only).
	Confidence string
	// ModelDirs are the immediate model subdirectories under the install's
	// models/ folder (e.g. checkpoints, loras, Stable-diffusion, Lora, vae).
	ModelDirs []string
	// Git is the repo state when a .git entry exists, else nil.
	Git *GitState
}

// DiscoverOptions bounds and configures a discovery crawl.
type DiscoverOptions struct {
	// MaxDepth caps how deep below each crawl root the walk descends (default
	// DefaultDiscoverMaxDepth). It bounds the arbitrary-path walk.
	MaxDepth int
	// Budget is the wall-clock deadline applied to the crawl when the passed-in
	// context has no deadline of its own (default DefaultDiscoverBudget).
	Budget time.Duration
	// Skip is a set of directories to EXCLUDE from the results (typically the
	// tool's own model_root plus configured library_paths), so an install the
	// tool already tracks is not re-offered.
	Skip []string
	// gitProbe resolves a directory's GitState. Nil uses the real git-backed
	// probe; tests inject a deterministic stub.
	gitProbe func(ctx context.Context, dir string) *GitState
	// readDir lists a directory's entries; nil uses os.ReadDir. Tests inject a
	// seam here to simulate a blocked/slow ReadDir so the hard-deadline guarantee
	// (the crawl returns AT the deadline even when a worker is stuck in a syscall)
	// can be exercised deterministically.
	readDir func(name string) ([]os.DirEntry, error)
}

const (
	// DefaultDiscoverMaxDepth bounds a discovery walk's depth below each root. It
	// is deliberately shallow: genuine installs live at or near the top of $HOME
	// (or under /opt), and a deep general walk of a large $HOME is the dominant
	// cost. Known locations ($HOME/ComfyUI, $HOME/stable-diffusion-webui,
	// $HOME/workspace/*, /opt/*) all sit within this depth.
	DefaultDiscoverMaxDepth = 3
	// DefaultDiscoverBudget bounds a discovery crawl's wall-clock time. Now that
	// the budget is HARD-enforced (the crawl returns at the deadline even if a
	// worker is blocked in a ReadDir syscall) it can be snappy.
	DefaultDiscoverBudget = 6 * time.Second
	// discoverWorkerCap caps the concurrent-walk worker pool. Discovery is
	// I/O-bound (ReadDir/stat), so a handful of workers parallelizes the crawl and
	// cuts wall-clock several-fold on SSD without exhausting file descriptors.
	discoverWorkerCap = 12
)

// discoveryPruneDirs are directory basenames the crawl never descends into:
// obvious noise (VCS internals, package/venv/tool caches) that cannot be an
// install root and would only waste the time budget. ".git" is still stat-probed
// at the candidate level (for GitState) — this only stops DESCENDING into it.
// Note the crawl also prunes ALL dot-directories (so .cache/.cargo/.rustup/.npm
// etc. are pruned by the hidden-dir rule); the non-hidden heavies below are the
// ones that rule does not already cover.
var discoveryPruneDirs = map[string]bool{
	".git": true, "node_modules": true, "venv": true, ".venv": true,
	".cache": true, "__pycache__": true, ".hg": true, ".svn": true,
	// Non-hidden dev-tool heavies that are never an AI-model install root but can
	// hold tens of thousands of directories (e.g. ~/go/pkg/mod). Cheap to prune,
	// large to walk.
	"go": true, "site-packages": true, "dist-packages": true,
	// Belt-and-suspenders: also list the hidden package caches explicitly so the
	// intent survives even if the hidden-dir rule is ever changed.
	".cargo": true, ".rustup": true, ".npm": true, ".pnpm-store": true,
}

// systemDirs are top-level system roots a scan/discovery must never walk:
// hashing or crawling them is a huge, sensitive, irrelevant footgun (and, on a
// non-loopback bind, a remote read/DoS primitive).
var systemDirs = []string{
	"/proc", "/sys", "/dev", "/etc", "/boot", "/run",
	"/var", "/usr", "/bin", "/sbin", "/lib",
}

// BlockedForBrowse reports whether p (symlink-resolved) is a system directory
// the server-side directory browser must refuse to list. Unlike CheckScanRoot it
// does NOT reject HOME or "/" — the browser only enumerates immediate
// subdirectories (no file contents, no descent), so navigating from HOME is
// allowed; only the sensitive system subtree is off-limits.
func BlockedForBrowse(p string) bool {
	return isSystemPath(resolveReal(filepath.Clean(p)))
}

// BrowseAllowed reports whether the interactive directory browser may list p
// (symlink-resolved). Beyond refusing system dirs (BlockedForBrowse), the
// browser is CONSTRAINED to the directories a user could plausibly want to
// scan: $HOME plus the tool's own model_root and configured library_paths
// (passed as allowedRoots). Anything outside that set — /root, /home/otheruser,
// an unrelated top-level dir like /mnt/other — is refused even though it is not
// a system dir, closing the "enumerate any non-system directory" gap. The check
// is on the symlink-RESOLVED real path, so a symlink from an allowed dir to an
// outside dir does not escape the constraint. This bounds only the interactive
// browser; the discovery crawl (which legitimately probes common locations) is
// unaffected.
func BrowseAllowed(p string, allowedRoots []string) bool {
	resolved := resolveReal(filepath.Clean(p))
	if isSystemPath(resolved) {
		return false
	}
	roots := make([]string, 0, len(allowedRoots)+1)
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		roots = append(roots, home)
	}
	roots = append(roots, allowedRoots...)
	for _, root := range roots {
		if strings.TrimSpace(root) == "" {
			continue
		}
		r := resolveReal(filepath.Clean(root))
		if resolved == r || isUnder(resolved, r) {
			return true
		}
	}
	return false
}

// isSystemPath reports whether resolved is a system dir or nested under one.
func isSystemPath(resolved string) bool {
	for _, d := range systemDirs {
		if resolved == d || isUnder(resolved, d) {
			return true
		}
	}
	return false
}

// CheckScanRoot rejects an obviously-dangerous scan root. It judges the CLEANED,
// SYMLINK-RESOLVED absolute path (resolved first so a symlink pointing at "/" or
// a system dir is caught, not the innocent-looking link name). It rejects:
//   - "/" itself,
//   - any system dir (see systemDirs) or a path nested under one,
//   - the user's HOME directory ITSELF (too broad to hash wholesale); a
//     subdirectory of HOME is allowed.
//
// It is the single source of truth for the web "Scan now" path guard, shared so
// discovery and the scan form enforce one identical blocklist.
func CheckScanRoot(p string) error {
	resolved := p
	if r, err := filepath.EvalSymlinks(p); err == nil {
		resolved = r
	}
	resolved = filepath.Clean(resolved)

	if resolved == string(filepath.Separator) {
		return fmt.Errorf("refusing to scan the filesystem root: %s", p)
	}
	for _, d := range systemDirs {
		if resolved == d || isUnder(resolved, d) {
			return fmt.Errorf("refusing to scan a system directory (%s): %s", d, p)
		}
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if hr, err := filepath.EvalSymlinks(home); err == nil {
			home = hr
		}
		if resolved == filepath.Clean(home) {
			return fmt.Errorf("refusing to scan your entire home directory (pick a subdirectory): %s", p)
		}
	}
	return nil
}

// DiscoverInstalls crawls roots (plus, when roots is empty, a set of sensible
// defaults: $HOME and /opt) for ComfyUI and Automatic1111/Forge installations,
// returning each candidate with its kind, confidence, model-dir count and git
// state, SORTED by path for deterministic output.
//
// It is deliberately BOUNDED and SAFE, reusing the scan hardening: a max depth,
// a HARD wall-clock budget, the system-dir blocklist, a ctx-cancellable
// concurrent walk, and it NEVER descends symlinked directories (os.ReadDir
// reports a symlinked dir with a symlink type, so d.IsDir() is false and it is
// not followed). Discovery only stats/marker-checks — it never hashes a file.
//
// Concurrency: the crawl runs a bounded worker pool over a directory queue so
// the I/O-bound walk of a large $HOME parallelizes and finishes inside the
// budget instead of timing out with partial results.
//
// Hard deadline: the crawl runs in a background goroutine and DiscoverInstalls
// returns as soon as either the crawl finishes OR ctx is done — so even if a
// worker is blocked in a ReadDir syscall past the deadline, the caller returns
// on time with the installs found so far. The background workers cannot corrupt
// the returned slice: results are collected through a mutex-guarded collector,
// never a bare shared map.
//
// On a cancelled context or exceeded budget it returns the installs found so far
// together with the context error, so a caller can render partial results and
// note the truncation.
func DiscoverInstalls(ctx context.Context, roots []string, opts DiscoverOptions) ([]Install, error) {
	if opts.MaxDepth <= 0 {
		opts.MaxDepth = DefaultDiscoverMaxDepth
	}
	if opts.Budget <= 0 {
		opts.Budget = DefaultDiscoverBudget
	}
	if opts.gitProbe == nil {
		opts.gitProbe = probeGit
	}
	if opts.readDir == nil {
		opts.readDir = os.ReadDir
	}
	// Apply our own budget only when the caller's context has no deadline.
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.Budget)
		defer cancel()
	}

	if len(roots) == 0 {
		roots = defaultDiscoverRoots()
	}
	// Dedupe so an ancestor and its descendant are never both walked (e.g. $HOME
	// and $HOME/workspace collapse to $HOME).
	roots = dedupeRoots(roots)

	// Pre-filter to existing, non-system directories.
	var seeds []string
	for _, r := range roots {
		r = filepath.Clean(r)
		fi, err := os.Stat(r)
		if err != nil || !fi.IsDir() {
			continue
		}
		if isSystemPath(resolveReal(r)) {
			continue
		}
		seeds = append(seeds, r)
	}

	skip := map[string]bool{}
	for _, s := range opts.Skip {
		skip[resolveReal(filepath.Clean(s))] = true
	}
	coll := &collector{skip: skip, found: map[string]Install{}}

	done := make(chan struct{})
	go func() {
		defer close(done)
		concurrentWalk(ctx, seeds, opts, coll)
	}()

	// Hard deadline: return the moment the crawl finishes OR ctx fires, whichever
	// comes first. Straggler workers (still blocked in a syscall) only ever write
	// to the mutex-guarded collector, so reading it here is race-free.
	select {
	case <-done:
	case <-ctx.Done():
	}
	return coll.installs(), ctx.Err()
}

// collector accumulates discovered installs from concurrent workers. All access
// is mutex-guarded so workers and the deadline reader never race on the map.
type collector struct {
	mu    sync.Mutex
	skip  map[string]bool
	found map[string]Install // keyed by resolved path, first-writer-wins
}

// record adds an install unless its resolved path is skipped or already present.
func (c *collector) record(dir string, in Install) {
	key := resolveReal(dir)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.skip[key] {
		return
	}
	if _, ok := c.found[key]; ok {
		return
	}
	c.found[key] = in
}

// installs returns a stable, path-sorted snapshot of the installs found so far.
// Safe to call concurrently with in-flight workers.
func (c *collector) installs() []Install {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]Install, 0, len(c.found))
	for _, in := range c.found {
		out = append(out, in)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// walkItem is one directory queued for processing.
type walkItem struct {
	path  string
	depth int
}

// dirWalker is the bounded concurrent directory walker. The queue is an
// unbounded FIFO slice guarded by mu; a sync.Cond wakes idle workers when work
// arrives, when the last active worker drains the queue (termination), or when
// ctx is cancelled. This scheme cannot deadlock (no bounded channel a worker can
// block on while holding others) nor panic (no channel to close-with-pending-
// senders), and the fixed worker count bounds concurrent ReadDir/fd use.
//
// The queue is FIFO (breadth-first) so shallow directories — where the common
// installs live ($HOME/ComfyUI, $HOME/stable-diffusion-webui) — are processed
// before the deep tail. On a budget-truncated crawl that maximizes the chance
// the well-known installs are already in the partial results.
type dirWalker struct {
	readDir  func(name string) ([]os.DirEntry, error)
	maxDepth int
	gitProbe func(context.Context, string) *GitState
	coll     *collector

	mu        sync.Mutex
	cond      *sync.Cond
	queue     []walkItem
	active    int             // workers currently processing an item
	cancelled bool            // ctx fired: stop promptly
	visited   map[string]bool // resolved paths already processed (dedupe overlapping subtrees)
}

// concurrentWalk crawls seeds with a bounded worker pool, recording installs via
// coll. It honors the depth cap, prune list, system-dir blocklist, no-symlink
// descent, and prompt ctx cancellation. It returns when the crawl is exhausted
// or ctx is done.
func concurrentWalk(ctx context.Context, seeds []string, opts DiscoverOptions, coll *collector) {
	numWorkers := runtime.NumCPU()
	if numWorkers > discoverWorkerCap {
		numWorkers = discoverWorkerCap
	}
	if numWorkers < 1 {
		numWorkers = 1
	}

	w := &dirWalker{
		readDir:  opts.readDir,
		maxDepth: opts.MaxDepth,
		gitProbe: opts.gitProbe,
		coll:     coll,
		visited:  map[string]bool{},
	}
	w.cond = sync.NewCond(&w.mu)

	w.mu.Lock()
	for _, s := range seeds {
		w.queue = append(w.queue, walkItem{path: s, depth: 0})
	}
	w.mu.Unlock()

	// Cancellation waker: on ctx.Done, flip the flag and wake every worker so a
	// worker idling in cond.Wait exits promptly (a worker blocked in a ReadDir
	// syscall exits after the syscall returns; the outer DiscoverInstalls has
	// already returned via its own ctx.Done select, so that straggler is harmless).
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
			w.mu.Lock()
			w.cancelled = true
			w.cond.Broadcast()
			w.mu.Unlock()
		case <-stop:
		}
	}()

	var wg sync.WaitGroup
	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w.run(ctx)
		}()
	}
	wg.Wait()
}

// run is one worker: pull a directory, process it, repeat until the queue is
// drained (with no active workers) or ctx is cancelled.
func (w *dirWalker) run(ctx context.Context) {
	for {
		w.mu.Lock()
		for len(w.queue) == 0 && w.active > 0 && !w.cancelled {
			w.cond.Wait()
		}
		if w.cancelled || (len(w.queue) == 0 && w.active == 0) {
			w.mu.Unlock()
			return
		}
		it := w.queue[0]
		w.queue = w.queue[1:] // FIFO: breadth-first
		w.active++
		w.mu.Unlock()

		w.process(ctx, it)

		w.mu.Lock()
		w.active--
		if len(w.queue) == 0 && w.active == 0 {
			w.cond.Broadcast() // last worker drained the queue: everyone can exit
		}
		w.mu.Unlock()
	}
}

// process handles one directory: dedupe, blocklist, install-detect, then enqueue
// non-pruned subdirectories (bounded by depth). It checks ctx before every
// syscall and between entries so cancellation is prompt.
func (w *dirWalker) process(ctx context.Context, it walkItem) {
	if ctx.Err() != nil {
		return
	}
	resolved := resolveReal(it.path)

	w.mu.Lock()
	if w.visited[resolved] {
		w.mu.Unlock()
		return // already processed via another (overlapping) root — never double-walk
	}
	w.visited[resolved] = true
	w.mu.Unlock()

	if isSystemPath(resolved) {
		return
	}

	// An install root is a leaf for discovery: its models/ subtree holds no
	// nested installs and would only burn the budget.
	if in, ok := detectInstall(ctx, it.path, w.gitProbe); ok {
		w.coll.record(it.path, in)
		return
	}

	if it.depth >= w.maxDepth {
		return // depth bound: do not enqueue this dir's children
	}
	if ctx.Err() != nil {
		return
	}
	entries, err := w.readDir(it.path)
	if err != nil {
		return // unreadable subtree: skip it, never abort the whole crawl
	}

	var children []walkItem
	for _, e := range entries {
		if ctx.Err() != nil {
			return
		}
		// os.ReadDir reports a symlinked dir with a symlink type (not a dir type),
		// so IsDir() is false → symlinked directories are never descended.
		if !e.IsDir() {
			continue
		}
		base := e.Name()
		if strings.HasPrefix(base, ".") {
			continue // hidden dir: prune (covers .git, .cache, .venv, .cargo, …)
		}
		if discoveryPruneDirs[base] {
			continue
		}
		children = append(children, walkItem{path: filepath.Join(it.path, base), depth: it.depth + 1})
	}
	if len(children) == 0 {
		return
	}
	w.mu.Lock()
	w.queue = append(w.queue, children...)
	w.mu.Unlock()
	for range children {
		w.cond.Signal() // wake up to len(children) idle workers
	}
}

// dedupeRoots normalizes roots (clean + symlink-resolve) and drops both exact
// duplicates and any root that is nested under another root, so an ancestor and
// its descendant are never both walked. The ORIGINAL (unresolved) path of each
// surviving root is returned, preserving the caller's intended crawl targets.
func dedupeRoots(roots []string) []string {
	type nr struct{ orig, real string }
	var norm []nr
	seen := map[string]bool{}
	for _, r := range roots {
		r = filepath.Clean(r)
		real := resolveReal(r)
		if seen[real] {
			continue
		}
		seen[real] = true
		norm = append(norm, nr{orig: r, real: real})
	}
	var out []string
	for i, a := range norm {
		descendant := false
		for j, b := range norm {
			if i == j {
				continue
			}
			if a.real != b.real && isUnder(a.real, b.real) {
				descendant = true
				break
			}
		}
		if !descendant {
			out = append(out, a.orig)
		}
	}
	return out
}

// detectInstall marker-checks a single directory for a ComfyUI or A1111 install.
func detectInstall(ctx context.Context, dir string, gitProbe func(context.Context, string) *GitState) (Install, bool) {
	if kind, modelDirs, ok := detectComfyUI(dir); ok {
		return buildInstall(ctx, dir, kind, modelDirs, gitProbe), true
	}
	if kind, modelDirs, ok := detectA1111(dir); ok {
		return buildInstall(ctx, dir, kind, modelDirs, gitProbe), true
	}
	return Install{}, false
}

func buildInstall(ctx context.Context, dir, kind string, modelDirs []string, gitProbe func(context.Context, string) *GitState) Install {
	abs, err := filepath.Abs(dir)
	if err != nil {
		abs = filepath.Clean(dir)
	}
	in := Install{Path: abs, Kind: kind, ModelDirs: modelDirs, Confidence: ConfidenceLow}
	if dirExists(filepath.Join(dir, ".git")) {
		in.Git = gitProbe(ctx, dir)
		if in.Git == nil {
			in.Git = &GitState{IsRepo: true}
		}
		in.Confidence = ConfidenceHigh
	}
	return in
}

// detectComfyUI: main.py + a models/ dir holding checkpoints and/or loras (and
// custom_nodes/ as an extra signal).
func detectComfyUI(dir string) (string, []string, bool) {
	if !fileExists(filepath.Join(dir, "main.py")) {
		return "", nil, false
	}
	models := filepath.Join(dir, "models")
	if !dirExists(models) {
		return "", nil, false
	}
	if !dirExists(filepath.Join(models, "checkpoints")) && !dirExists(filepath.Join(models, "loras")) {
		return "", nil, false
	}
	return KindComfyUI, listModelDirs(models), true
}

// detectA1111: webui.py or launch.py + models/Stable-diffusion + models/Lora.
func detectA1111(dir string) (string, []string, bool) {
	if !fileExists(filepath.Join(dir, "webui.py")) && !fileExists(filepath.Join(dir, "launch.py")) {
		return "", nil, false
	}
	models := filepath.Join(dir, "models")
	if !dirExists(filepath.Join(models, "Stable-diffusion")) || !dirExists(filepath.Join(models, "Lora")) {
		return "", nil, false
	}
	return KindA1111, listModelDirs(models), true
}

// listModelDirs returns the immediate subdirectory names under a models/ dir.
func listModelDirs(models string) []string {
	entries, err := os.ReadDir(models)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			out = append(out, e.Name())
		}
	}
	return out
}

// defaultDiscoverRoots returns the general-walk roots: $HOME and /opt. Known
// install locations ($HOME/ComfyUI, $HOME/stable-diffusion-webui,
// $HOME/workspace/*, /opt/*) all fall within DefaultDiscoverMaxDepth of these
// roots, so the bounded concurrent walk finds them without a separate probe
// list; dedupeRoots collapses any overlap.
func defaultDiscoverRoots() []string {
	var roots []string
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		roots = append(roots, home)
	}
	roots = append(roots, "/opt")
	return roots
}

// gitProbeHardeningFlags neutralize repo-local, config- and hook-driven code
// execution when we probe an UNTRUSTED .git directory. Discovery crawls dirs
// under $HOME, so a .git/config planted by an attacker (in a dir shaped like a
// ComfyUI/A1111 install) is untrusted input. `git status` runs the program
// named by the repo's own core.fsmonitor config, and hooks named by
// core.hooksPath — so a bare `git -C <dir> status` on such a dir is a local RCE
// the moment the user clicks "Discover installs". These flags override both to
// empty/inert (they win over the repo config), so no repo-local program runs.
// DO NOT REMOVE without an equivalent mitigation.
var gitProbeHardeningFlags = []string{"-c", "core.fsmonitor=", "-c", "core.hooksPath=/dev/null"}

// gitProbeArgs builds the full argv for a hardened git probe in dir: the
// neutralizing -c flags FIRST (they must precede the subcommand to take
// effect), then -C <dir> (so a leading-dash dir name can't be read as a flag),
// then the subcommand. Kept as a separate function so a unit test can assert the
// hardening flags are always present. The resulting form is a valid git
// invocation: `git -c k=v -c k2=v2 -C <dir> <subcommand...>`.
func gitProbeArgs(dir string, sub ...string) []string {
	args := make([]string, 0, len(gitProbeHardeningFlags)+2+len(sub))
	args = append(args, gitProbeHardeningFlags...)
	args = append(args, "-C", dir)
	args = append(args, sub...)
	return args
}

// probeGit is the default GitState resolver: it runs cheap, short-timeout git
// queries and tolerates git being absent or the dir not really being a repo.
//
// SECURITY: every invocation is hardened (see gitProbeHardeningFlags) because
// the probed .git dir is untrusted (attacker-plantable under $HOME). We also
// set GIT_OPTIONAL_LOCKS=0 so probing a repo the user is mid-operation on never
// creates/touches index.lock. The existing safety (CommandContext, fixed args,
// -C dir, the short per-call timeout) is preserved. The per-call timeout is
// derived from ctx, so git probes are also bounded by the overall crawl deadline.
func probeGit(ctx context.Context, dir string) *GitState {
	st := &GitState{IsRepo: true}
	git, err := exec.LookPath("git")
	if err != nil {
		return st // git not installed: report repo presence only
	}
	run := func(sub ...string) (string, bool) {
		cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		cmd := exec.CommandContext(cctx, git, gitProbeArgs(dir, sub...)...)
		// GIT_OPTIONAL_LOCKS=0: never touch index.lock while merely probing.
		cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
		out, err := cmd.Output()
		if err != nil {
			return "", false
		}
		return strings.TrimSpace(string(out)), true
	}
	// `branch --show-current` reports the checked-out branch even in a repo with
	// no commits yet (unlike `rev-parse --abbrev-ref HEAD`, which errors there).
	if branch, ok := run("branch", "--show-current"); ok && branch != "" {
		st.Branch = branch
	} else if b, ok := run("rev-parse", "--abbrev-ref", "HEAD"); ok {
		st.Branch = b
	}
	if porcelain, ok := run("status", "--porcelain"); ok {
		st.Dirty = porcelain != ""
	}
	return st
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

func dirExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}
