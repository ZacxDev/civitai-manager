package web

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/library"
)

// When the user has marked install dirs, the workflow scan uses them directly —
// deriving each ComfyUI root's user/default/workflows — and MUST NOT run the slow,
// non-deterministic $HOME discovery crawl.
func TestResolveWorkflowScanDirsUsesMarkedInstall(t *testing.T) {
	srv := newTestServer(t)
	root := t.TempDir()
	wf := filepath.Join(root, "user", "default", "workflows")
	if err := os.MkdirAll(wf, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := srv.store.AddScanDir(root); err != nil {
		t.Fatalf("AddScanDir: %v", err)
	}
	srv.crawlFn = func(context.Context, []string, library.DiscoverOptions) ([]library.Install, error) {
		t.Fatal("discovery crawl must be SKIPPED when install dirs are marked")
		return nil, nil
	}

	dirs := srv.resolveWorkflowScanDirs(context.Background())
	if len(dirs) != 1 || dirs[0] != wf {
		t.Fatalf("resolveWorkflowScanDirs = %v, want [%s] (the marked install's user-workflows dir)", dirs, wf)
	}
}

// A marked arbitrary dir (no ComfyUI layout) is scanned as-is.
func TestResolveWorkflowScanDirsMarkedArbitrary(t *testing.T) {
	srv := newTestServer(t)
	dir := t.TempDir()
	if err := srv.store.AddScanDir(dir); err != nil {
		t.Fatalf("AddScanDir: %v", err)
	}
	srv.crawlFn = func(context.Context, []string, library.DiscoverOptions) ([]library.Install, error) {
		t.Fatal("no crawl expected")
		return nil, nil
	}
	dirs := srv.resolveWorkflowScanDirs(context.Background())
	if len(dirs) != 1 || dirs[0] != dir {
		t.Fatalf("resolveWorkflowScanDirs = %v, want [%s]", dirs, dir)
	}
}

// With NOTHING marked, it falls back to the auto-discovery crawl.
func TestResolveWorkflowScanDirsFallsBackToDiscovery(t *testing.T) {
	srv := newTestServer(t)
	disc := filepath.Join(t.TempDir(), "user", "default", "workflows")
	called := false
	srv.crawlFn = func(context.Context, []string, library.DiscoverOptions) ([]library.Install, error) {
		called = true
		return []library.Install{{Kind: library.KindComfyUI, WorkflowDirs: []string{disc}}}, nil
	}
	dirs := srv.resolveWorkflowScanDirs(context.Background())
	if !called {
		t.Fatal("expected the discovery fallback when no install dirs are marked")
	}
	if len(dirs) != 1 || dirs[0] != disc {
		t.Fatalf("resolveWorkflowScanDirs = %v, want [%s]", dirs, disc)
	}
}
