package library

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"
)

// nestedInstallPath returns root/d1/d2/.../inst so that the returned leaf sits at
// exactly `depth` levels below root (depth-1 intermediate dirs + the leaf). It
// does NOT create anything — the caller lays down the install at the returned path.
func nestedInstallPath(root string, depth int, leaf string) string {
	parts := []string{root}
	for i := 1; i < depth; i++ {
		parts = append(parts, fmt.Sprintf("d%d", i))
	}
	parts = append(parts, leaf)
	return filepath.Join(parts...)
}

// mkdirAll is a fatal-on-error helper.
func mkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

// gitInit runs `git init` in dir, returning false when git is unavailable (so a
// caller can fall back to a bare .git directory). It also sets a deterministic
// identity so status/branch queries are stable.
func gitInit(t *testing.T, dir string) bool {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		return false
	}
	for _, args := range [][]string{
		{"-C", dir, "init", "-q"},
		{"-C", dir, "config", "user.email", "t@t.t"},
		{"-C", dir, "config", "user.name", "t"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v (%s)", args, err, out)
		}
	}
	return true
}

// buildFixtureTree lays out a temp directory with two genuine installs plus
// several decoys and a symlink pointing outside. It returns the tree root, the
// two install paths, and whether a real git repo was created.
func buildFixtureTree(t *testing.T) (root, comfy, a1111 string, realGit bool) {
	t.Helper()
	root = t.TempDir()

	// Genuine ComfyUI install (with a .git repo → high confidence).
	comfy = filepath.Join(root, "ComfyUI")
	mkdirAll(t, filepath.Join(comfy, "models", "checkpoints"))
	mkdirAll(t, filepath.Join(comfy, "models", "loras"))
	mkdirAll(t, filepath.Join(comfy, "custom_nodes"))
	writeFile(t, filepath.Join(comfy, "main.py"), "print('hi')\n")
	realGit = gitInit(t, comfy)
	if !realGit {
		mkdirAll(t, filepath.Join(comfy, ".git")) // fallback marker
	}

	// Genuine A1111/Forge install (no .git → low confidence).
	a1111 = filepath.Join(root, "webui")
	mkdirAll(t, filepath.Join(a1111, "models", "Stable-diffusion"))
	mkdirAll(t, filepath.Join(a1111, "models", "Lora"))
	writeFile(t, filepath.Join(a1111, "webui.py"), "x\n")

	// Decoy: a directory literally named "comfyui" with no markers.
	mkdirAll(t, filepath.Join(root, "comfyui"))
	writeFile(t, filepath.Join(root, "comfyui", "readme.txt"), "not an install\n")

	// Decoy: a .git repo that is NOT an install.
	plainRepo := filepath.Join(root, "plainrepo")
	mkdirAll(t, filepath.Join(plainRepo, ".git"))
	writeFile(t, filepath.Join(plainRepo, "file.txt"), "x\n")

	// Decoy: an install-shaped dir OUTSIDE the tree, reachable only via a symlink
	// — discovery must NOT follow the symlink into it.
	outside := t.TempDir()
	outComfy := filepath.Join(outside, "OutsideComfy")
	mkdirAll(t, filepath.Join(outComfy, "models", "checkpoints"))
	writeFile(t, filepath.Join(outComfy, "main.py"), "x\n")
	if runtime.GOOS != "windows" {
		if err := os.Symlink(outside, filepath.Join(root, "linkout")); err != nil {
			t.Fatalf("symlink: %v", err)
		}
	}
	return root, comfy, a1111, realGit
}

func discover(t *testing.T, ctx context.Context, root string, opts DiscoverOptions) []Install {
	t.Helper()
	got, err := DiscoverInstalls(ctx, []string{root}, opts)
	if err != nil && ctx.Err() == nil {
		t.Fatalf("DiscoverInstalls: %v", err)
	}
	return got
}

func TestDiscoverInstallsFindsGenuineInstalls(t *testing.T) {
	root, comfy, a1111, realGit := buildFixtureTree(t)

	got := discover(t, context.Background(), root, DiscoverOptions{})

	byPath := map[string]Install{}
	for _, in := range got {
		byPath[in.Path] = in
	}
	if len(got) != 2 {
		t.Fatalf("want exactly 2 installs, got %d: %+v", len(got), got)
	}

	c, ok := byPath[comfy]
	if !ok {
		t.Fatalf("ComfyUI install %s not found; got %+v", comfy, got)
	}
	if c.Kind != KindComfyUI {
		t.Errorf("ComfyUI kind = %q, want %q", c.Kind, KindComfyUI)
	}
	if c.Confidence != ConfidenceHigh {
		t.Errorf("ComfyUI confidence = %q, want high (has .git)", c.Confidence)
	}
	if c.Git == nil || !c.Git.IsRepo {
		t.Errorf("ComfyUI GitState not populated: %+v", c.Git)
	}
	if len(c.ModelDirs) == 0 {
		t.Errorf("ComfyUI ModelDirs empty")
	}
	if realGit {
		if c.Git.Branch == "" {
			t.Errorf("expected a git branch for the real repo")
		}
		if !c.Git.Dirty {
			t.Errorf("expected the repo to be dirty (untracked main.py etc.)")
		}
	}

	a, ok := byPath[a1111]
	if !ok {
		t.Fatalf("A1111 install %s not found; got %+v", a1111, got)
	}
	if a.Kind != KindA1111 {
		t.Errorf("A1111 kind = %q, want %q", a.Kind, KindA1111)
	}
	if a.Confidence != ConfidenceLow {
		t.Errorf("A1111 confidence = %q, want low (no .git)", a.Confidence)
	}
	if a.Git != nil {
		t.Errorf("A1111 should have no GitState, got %+v", a.Git)
	}
}

func TestDiscoverIgnoresDecoysAndSymlink(t *testing.T) {
	root, _, _, _ := buildFixtureTree(t)
	got := discover(t, context.Background(), root, DiscoverOptions{})
	for _, in := range got {
		base := filepath.Base(in.Path)
		if base == "comfyui" || base == "plainrepo" {
			t.Errorf("decoy %s was reported as an install", in.Path)
		}
		if base == "OutsideComfy" || filepath.Base(filepath.Dir(in.Path)) == "linkout" {
			t.Errorf("discovery followed the symlink to %s", in.Path)
		}
	}
}

func TestDiscoverRespectsSkip(t *testing.T) {
	root, comfy, a1111, _ := buildFixtureTree(t)
	got := discover(t, context.Background(), root, DiscoverOptions{Skip: []string{comfy}})
	for _, in := range got {
		if in.Path == comfy {
			t.Fatalf("skipped install %s should not be reported", comfy)
		}
	}
	found := false
	for _, in := range got {
		if in.Path == a1111 {
			found = true
		}
	}
	if !found {
		t.Fatalf("non-skipped install %s should still be reported", a1111)
	}
}

func TestDiscoverRespectsMaxDepth(t *testing.T) {
	root := t.TempDir()
	// Bury an install several levels deep.
	deep := filepath.Join(root, "a", "b", "c", "d", "e", "DeepComfy")
	mkdirAll(t, filepath.Join(deep, "models", "checkpoints"))
	writeFile(t, filepath.Join(deep, "main.py"), "x\n")

	got := discover(t, context.Background(), root, DiscoverOptions{MaxDepth: 2})
	if len(got) != 0 {
		t.Fatalf("MaxDepth=2 should not reach the deep install, got %+v", got)
	}
	// A generous depth finds it.
	got = discover(t, context.Background(), root, DiscoverOptions{MaxDepth: 10})
	if len(got) != 1 {
		t.Fatalf("MaxDepth=10 should find the deep install, got %+v", got)
	}
}

// TestDiscoverFindsDeeplyNestedInstall is the regression guard for the reported
// miss: a genuine ComfyUI install at depth 4 below the crawl root (mirroring the
// real $HOME/workspace/fast/comfyui/ComfyUI) MUST be found with the default depth,
// and MUST NOT be found at the old depth-3 cap — proving the raised default is
// what fixes the miss.
func TestDiscoverFindsDeeplyNestedInstall(t *testing.T) {
	root := t.TempDir()
	// root/a/b/comfyui/ComfyUI → the "ComfyUI" install dir is at depth 4.
	comfy := filepath.Join(root, "a", "b", "comfyui", "ComfyUI")
	makeComfy(t, comfy)
	want := mustAbs(t, comfy)

	// Old cap (3): the depth-4 install is missed — this is exactly the bug.
	old := discover(t, context.Background(), root, DiscoverOptions{MaxDepth: 3, gitProbe: stubProbe})
	for _, in := range old {
		if in.Path == want {
			t.Fatalf("MaxDepth=3 (old default) unexpectedly found the depth-4 install %s — regression sub-case is vacuous", want)
		}
	}
	if len(old) != 0 {
		t.Fatalf("MaxDepth=3 should find no installs in this tree, got %+v", old)
	}

	// New default (DefaultDiscoverMaxDepth=12): the install is found.
	got := discover(t, context.Background(), root, DiscoverOptions{gitProbe: stubProbe})
	if len(got) != 1 || got[0].Path != want {
		t.Fatalf("default depth should find the depth-4 install %s, got %+v", want, got)
	}
	// Guard the intent: the default must actually be generous enough for depth 4.
	if DefaultDiscoverMaxDepth < 4 {
		t.Fatalf("DefaultDiscoverMaxDepth=%d is too shallow to reach a depth-4 install", DefaultDiscoverMaxDepth)
	}
}

// TestDiscoverDepthCapBackstop proves the depth cap still exists and is honored: an
// install exactly AT the default cap is found, one just BEYOND it is not. This is
// the guard that raising the default did not remove the pathological-tree backstop.
func TestDiscoverDepthCapBackstop(t *testing.T) {
	// Install exactly at the cap depth → found.
	within := t.TempDir()
	atCap := nestedInstallPath(within, DefaultDiscoverMaxDepth, "AtCap")
	makeComfy(t, atCap)
	got := discover(t, context.Background(), within, DiscoverOptions{gitProbe: stubProbe})
	if len(got) != 1 || got[0].Path != mustAbs(t, atCap) {
		t.Fatalf("install at the cap depth %d (%s) should be found, got %+v", DefaultDiscoverMaxDepth, atCap, got)
	}

	// Install one level beyond the cap → not found (backstop honored).
	beyond := t.TempDir()
	overCap := nestedInstallPath(beyond, DefaultDiscoverMaxDepth+1, "OverCap")
	makeComfy(t, overCap)
	got = discover(t, context.Background(), beyond, DiscoverOptions{gitProbe: stubProbe})
	if len(got) != 0 {
		t.Fatalf("install one level beyond the cap depth %d (%s) must NOT be found, got %+v",
			DefaultDiscoverMaxDepth+1, overCap, got)
	}
}

// TestDiscoverPrunesInstallUnderPrunedDir confirms the raised depth did not weaken
// the prune list: an install buried under a pruned directory (node_modules) is
// skipped even though it is shallow, while a sibling real install is still found.
func TestDiscoverPrunesInstallUnderPrunedDir(t *testing.T) {
	root := t.TempDir()

	real := filepath.Join(root, "RealComfy") // depth 1, must be found
	makeComfy(t, real)

	pruned := filepath.Join(root, "node_modules", "ComfyUI") // under a pruned dir
	makeComfy(t, pruned)

	got := discover(t, context.Background(), root, DiscoverOptions{gitProbe: stubProbe})
	if len(got) != 1 || got[0].Path != mustAbs(t, real) {
		t.Fatalf("expected only the non-pruned install %s, got %+v", real, got)
	}
	for _, in := range got {
		if in.Path == mustAbs(t, pruned) {
			t.Fatalf("install under node_modules/ must be pruned, but was reported: %s", pruned)
		}
	}
}

func TestDiscoverRefusesSystemDir(t *testing.T) {
	got, err := DiscoverInstalls(context.Background(), []string{"/etc"}, DiscoverOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("a system-dir root must yield no installs, got %+v", got)
	}
}

func TestDiscoverAbortsOnCancelledContext(t *testing.T) {
	root, _, _, _ := buildFixtureTree(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled
	_, err := DiscoverInstalls(ctx, []string{root}, DiscoverOptions{})
	if err == nil {
		t.Fatalf("expected a context error on a cancelled context")
	}
}

func TestDiscoverAbortsOnExceededBudget(t *testing.T) {
	root := t.TempDir()
	// A broad tree so the walk has work to do.
	for i := 0; i < 200; i++ {
		mkdirAll(t, filepath.Join(root, "dir", string(rune('a'+i%26)), string(rune('a'+(i/26)%26))))
	}
	_, err := DiscoverInstalls(context.Background(), []string{root}, DiscoverOptions{Budget: time.Nanosecond})
	if err == nil {
		t.Fatalf("expected a deadline error with a 1ns budget")
	}
}

func TestCheckScanRootBlocklist(t *testing.T) {
	for _, bad := range []string{"/", "/etc", "/etc/ssl", "/usr/bin"} {
		if err := CheckScanRoot(bad); err == nil {
			t.Errorf("CheckScanRoot(%q) = nil, want a rejection", bad)
		}
	}
	// A subdirectory of a temp dir (not a system dir, not HOME) is allowed.
	ok := t.TempDir()
	if err := CheckScanRoot(ok); err != nil {
		t.Errorf("CheckScanRoot(%q) = %v, want nil", ok, err)
	}
}

func TestBlockedForBrowse(t *testing.T) {
	if !BlockedForBrowse("/etc/ssl") {
		t.Error("/etc/ssl should be blocked for browse")
	}
	// HOME itself is browsable (only its scanning is refused).
	home, err := os.UserHomeDir()
	if err == nil && home != "" && BlockedForBrowse(home) {
		t.Error("HOME should be browsable")
	}
}

func TestBrowseAllowed(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "sub")
	mkdirAll(t, child)

	// In-scope: model_root itself and a subdirectory of it.
	if !BrowseAllowed(root, []string{root}) {
		t.Error("model_root itself should be browsable")
	}
	if !BrowseAllowed(child, []string{root}) {
		t.Error("a subdir of model_root should be browsable")
	}

	// Out-of-scope: an unrelated dir not under HOME or an allowed root.
	outside := t.TempDir()
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if r, err := filepath.EvalSymlinks(outside); err == nil {
			outside = r
		}
		if isUnder(outside, resolveReal(home)) {
			t.Skip("TMPDIR is under $HOME; cannot construct an out-of-scope dir")
		}
	}
	if BrowseAllowed(outside, []string{root}) {
		t.Error("an unrelated dir outside HOME/model_root should be refused")
	}

	// A system dir is never browsable, even if somehow passed as an allowed root.
	if BrowseAllowed("/etc", []string{root}) {
		t.Error("a system dir must never be browsable")
	}

	// HOME is always in-scope.
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if !BrowseAllowed(home, nil) {
			t.Error("HOME should be browsable")
		}
	}
}

// TestGitProbeArgsHardened asserts the neutralizing flags are always present, in
// the correct order (config -c flags BEFORE -C <dir> and the subcommand). This
// is the belt-and-suspenders unit check that the hardening can't be silently
// dropped by a future edit to the arg builder.
func TestGitProbeArgsHardened(t *testing.T) {
	got := gitProbeArgs("/x/dir", "status", "--porcelain")
	want := []string{
		"-c", "core.fsmonitor=",
		"-c", "core.hooksPath=/dev/null",
		"-C", "/x/dir",
		"status", "--porcelain",
	}
	if len(got) != len(want) {
		t.Fatalf("gitProbeArgs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("gitProbeArgs[%d] = %q, want %q (full: %v)", i, got[i], want[i], got)
		}
	}
}

// TestProbeGitNeutralizesMaliciousFsmonitor is the security-critical test: a
// repo whose OWN .git/config sets core.fsmonitor to an attacker script must not
// execute that script when probeGit runs `git status`. It first proves the
// vector is real (an UNHARDENED status fires it) so the assertion isn't vacuous,
// then asserts probeGit does NOT fire it while still returning correct git state.
func TestProbeGitNeutralizesMaliciousFsmonitor(t *testing.T) {
	dir := t.TempDir()
	if !gitInit(t, dir) {
		t.Skip("git not available; cannot exercise the core.fsmonitor RCE vector")
	}

	// Plant a malicious core.fsmonitor program in the repo's own config. When git
	// status runs it, it writes a sentinel file (proof it executed).
	sentinel := filepath.Join(t.TempDir(), "pwned")
	script := filepath.Join(dir, "fsmon.sh")
	writeFile(t, script, "#!/bin/sh\ntouch "+sentinel+"\nexit 1\n")
	if err := os.Chmod(script, 0o700); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", dir, "config", "core.fsmonitor", script).CombinedOutput(); err != nil {
		t.Fatalf("git config core.fsmonitor: %v (%s)", err, out)
	}
	// An untracked file so status reports the repo as dirty.
	writeFile(t, filepath.Join(dir, "untracked.txt"), "x\n")

	// Control: an UNHARDENED status must fire the vector. If this git build does
	// not invoke core.fsmonitor on a plain status, the hardened assertion below
	// would be meaningless — skip loudly rather than pass vacuously.
	_ = exec.Command("git", "-C", dir, "status", "--porcelain").Run()
	if _, err := os.Stat(sentinel); err != nil {
		t.Skip("this git build did not invoke core.fsmonitor on a plain status; RCE vector not reproducible here")
	}
	if err := os.Remove(sentinel); err != nil {
		t.Fatal(err)
	}

	// Hardened probe: the malicious program must NOT run.
	st := probeGit(context.Background(), dir)
	if _, err := os.Stat(sentinel); err == nil {
		t.Fatalf("SECURITY: malicious core.fsmonitor executed during probeGit (sentinel created)")
	}
	// ...and the probe must still return correct git state.
	if st == nil || !st.IsRepo {
		t.Fatalf("probeGit did not report a repo: %+v", st)
	}
	if !st.Dirty {
		t.Errorf("probeGit should report the repo dirty (untracked files present), got %+v", st)
	}
	if st.Branch == "" {
		t.Errorf("probeGit should report a branch, got %+v", st)
	}
}

// TestDiscoverStreamsInstalls proves opts.OnInstall delivers each discovered
// install incrementally (first-writer-wins, from worker goroutines) and that the
// streamed set exactly matches the final returned slice.
func TestDiscoverStreamsInstalls(t *testing.T) {
	root, comfy, a1111, _ := buildFixtureTree(t)

	var mu sync.Mutex
	var streamed []Install
	opts := DiscoverOptions{
		gitProbe: func(context.Context, string) *GitState { return &GitState{IsRepo: true} },
		OnInstall: func(in Install) {
			mu.Lock()
			streamed = append(streamed, in)
			mu.Unlock()
		},
	}

	got, err := DiscoverInstalls(context.Background(), []string{root}, opts)
	if err != nil {
		t.Fatalf("DiscoverInstalls: %v", err)
	}

	// Both genuine installs must be found and returned.
	returnedPaths := map[string]bool{}
	for _, in := range got {
		returnedPaths[in.Path] = true
	}
	for _, want := range []string{comfy, a1111} {
		if !returnedPaths[want] {
			t.Errorf("returned set missing %q; got %v", want, got)
		}
	}

	// The streamed set must equal the returned set (no dup, no omission).
	mu.Lock()
	defer mu.Unlock()
	if len(streamed) != len(got) {
		t.Fatalf("streamed %d installs, returned %d — must match: streamed=%v returned=%v",
			len(streamed), len(got), streamed, got)
	}
	streamedPaths := map[string]bool{}
	for _, in := range streamed {
		if streamedPaths[in.Path] {
			t.Errorf("install %q streamed more than once", in.Path)
		}
		streamedPaths[in.Path] = true
	}
	for p := range returnedPaths {
		if !streamedPaths[p] {
			t.Errorf("install %q was returned but never streamed via OnInstall", p)
		}
	}
}
