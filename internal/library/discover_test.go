package library

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

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
