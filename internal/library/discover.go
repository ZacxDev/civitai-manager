package library

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
}

const (
	// DefaultDiscoverMaxDepth bounds a discovery walk's depth below each root.
	DefaultDiscoverMaxDepth = 6
	// DefaultDiscoverBudget bounds a discovery crawl's wall-clock time.
	DefaultDiscoverBudget = 15 * time.Second
)

// discoveryPruneDirs are directory basenames the crawl never descends into:
// obvious noise (VCS internals, package/venv caches) that cannot be an install
// root and would only waste the time budget. ".git" is still stat-probed at the
// candidate level (for GitState) — this only stops DESCENDING into it.
var discoveryPruneDirs = map[string]bool{
	".git": true, "node_modules": true, "venv": true, ".venv": true,
	".cache": true, "__pycache__": true, ".hg": true, ".svn": true,
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
// defaults: $HOME and common install locations) for ComfyUI and
// Automatic1111/Forge installations, returning each candidate with its kind,
// confidence, model-dir count and git state.
//
// It is deliberately BOUNDED and SAFE, reusing the scan hardening: a max depth,
// a wall-clock budget (ctx deadline), the system-dir blocklist, a
// context-cancellable walk, and it NEVER descends symlinked directories
// (filepath.WalkDir uses Lstat, so a symlinked dir is reported but not
// followed). Discovery only stats/marker-checks — it never hashes a file.
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
	// Apply our own budget only when the caller's context has no deadline.
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.Budget)
		defer cancel()
	}

	if len(roots) == 0 {
		roots = defaultDiscoverRoots()
	}

	skip := map[string]bool{}
	for _, s := range opts.Skip {
		skip[resolveReal(filepath.Clean(s))] = true
	}

	found := map[string]Install{} // keyed by resolved path, first-writer-wins
	var order []string

	record := func(dir string, in Install) {
		key := resolveReal(dir)
		if skip[key] {
			return
		}
		if _, ok := found[key]; ok {
			return
		}
		found[key] = in
		order = append(order, key)
	}

	for _, root := range roots {
		if ctx.Err() != nil {
			break
		}
		root = filepath.Clean(root)
		fi, err := os.Stat(root)
		if err != nil || !fi.IsDir() {
			continue
		}
		if isSystemPath(resolveReal(root)) {
			continue
		}
		if err := crawlRoot(ctx, root, opts, record); err != nil {
			// Assemble partial results and surface the cancellation/deadline.
			return assembleInstalls(found, order), err
		}
	}
	return assembleInstalls(found, order), ctx.Err()
}

func assembleInstalls(found map[string]Install, order []string) []Install {
	out := make([]Install, 0, len(order))
	for _, k := range order {
		out = append(out, found[k])
	}
	return out
}

// crawlRoot walks one root, bounded by depth + ctx, recording detected installs.
func crawlRoot(ctx context.Context, root string, opts DiscoverOptions, record func(string, Install)) error {
	rootDepth := strings.Count(filepath.Clean(root), string(filepath.Separator))

	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		if err != nil {
			// Unreadable subtree: skip it, never abort the whole crawl.
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if !d.IsDir() {
			return nil // WalkDir reports a symlinked dir as a non-dir → not followed
		}
		if path != root {
			base := d.Name()
			if strings.HasPrefix(base, ".") && base != "." {
				// Hidden dir: prune (covers .git, .cache, .venv, etc.).
				return fs.SkipDir
			}
			if discoveryPruneDirs[base] {
				return fs.SkipDir
			}
		}
		resolved := resolveReal(path)
		if isSystemPath(resolved) {
			return fs.SkipDir
		}
		// Depth bound: stop descending past MaxDepth below the root.
		depth := strings.Count(filepath.Clean(path), string(filepath.Separator)) - rootDepth
		if depth > opts.MaxDepth {
			return fs.SkipDir
		}

		if in, ok := detectInstall(ctx, path, opts.gitProbe); ok {
			record(path, in)
			// An install root is a leaf for discovery: its models/ subtree holds
			// no nested installs and would only burn the budget.
			return fs.SkipDir
		}
		return nil
	})
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

// defaultDiscoverRoots returns the fast-path probe locations plus $HOME. The
// specific paths are stat-cheap fast-paths probed first; the $HOME entry drives
// the bounded general walk.
func defaultDiscoverRoots() []string {
	var roots []string
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		roots = append(roots,
			filepath.Join(home, "ComfyUI"),
			filepath.Join(home, "comfyui"),
			filepath.Join(home, "stable-diffusion-webui"),
			filepath.Join(home, "workspace"),
			filepath.Join(home, "ai"),
		)
		roots = append(roots, home) // bounded general walk, last
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
// -C dir, the short per-call timeout) is preserved.
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
