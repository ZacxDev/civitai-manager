package library

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// stubProbe is a deterministic gitProbe so concurrency tests never shell out to
// a real git binary (keeping them hermetic and fast).
func stubProbe(_ context.Context, _ string) *GitState { return &GitState{IsRepo: true} }

// makeComfy lays down a minimal genuine ComfyUI install at dir.
func makeComfy(t *testing.T, dir string) {
	t.Helper()
	mkdirAll(t, filepath.Join(dir, "models", "checkpoints"))
	mkdirAll(t, filepath.Join(dir, "models", "loras"))
	writeFile(t, filepath.Join(dir, "main.py"), "x\n")
}

// serialDiscover is an INDEPENDENT single-threaded oracle: a plain filepath.WalkDir
// applying the same depth cap, prune list, system-dir blocklist, no-symlink
// descent, and install detection. It exists solely to prove the concurrent
// walker returns the exact same install set.
func serialDiscover(t *testing.T, root string, maxDepth int) []string {
	t.Helper()
	var out []string
	rootDepth := strings.Count(filepath.Clean(root), string(filepath.Separator))
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if !d.IsDir() {
			return nil // symlinked dir reported as non-dir → not followed
		}
		if p != root {
			base := d.Name()
			if strings.HasPrefix(base, ".") {
				return fs.SkipDir
			}
			if discoveryPruneDirs[base] {
				return fs.SkipDir
			}
		}
		if isSystemPath(resolveReal(p)) {
			return fs.SkipDir
		}
		depth := strings.Count(filepath.Clean(p), string(filepath.Separator)) - rootDepth
		if depth > maxDepth {
			return fs.SkipDir
		}
		if in, ok := detectInstall(context.Background(), p, stubProbe); ok {
			out = append(out, in.Path)
			return fs.SkipDir // an install is a leaf
		}
		return nil
	})
	sort.Strings(out)
	return out
}

func installPaths(installs []Install) []string {
	out := make([]string, 0, len(installs))
	for _, in := range installs {
		out = append(out, in.Path)
	}
	sort.Strings(out)
	return out
}

// TestDiscoverConcurrentSerialParity proves the bounded concurrent walker finds
// EXACTLY the same install set (order-independent) as a single-threaded oracle
// over a fixed tree — no dropped or duplicated results from the concurrency.
func TestDiscoverConcurrentSerialParity(t *testing.T) {
	root := t.TempDir()
	// A spread of installs at several depths, plus noise directories.
	makeComfy(t, filepath.Join(root, "ComfyUI"))                  // depth 1
	makeComfy(t, filepath.Join(root, "projects", "comfy2"))       // depth 2
	makeComfy(t, filepath.Join(root, "a", "b", "comfy3"))         // depth 3
	makeComfy(t, filepath.Join(root, "a", "b", "c", "d", "deep")) // depth 5 (beyond default 3)
	// Noise: many empty dirs to give the walker real breadth.
	for i := 0; i < 50; i++ {
		mkdirAll(t, filepath.Join(root, "noise", string(rune('a'+i%26)), string(rune('a'+(i/26)%26))))
	}

	const maxDepth = 3
	got, err := DiscoverInstalls(context.Background(), []string{root},
		DiscoverOptions{MaxDepth: maxDepth, gitProbe: stubProbe})
	if err != nil {
		t.Fatalf("DiscoverInstalls: %v", err)
	}
	want := serialDiscover(t, root, maxDepth)
	if !reflect.DeepEqual(installPaths(got), want) {
		t.Fatalf("concurrent vs serial mismatch:\nconcurrent=%v\nserial=   %v", installPaths(got), want)
	}
	// Sanity: the depth-3 installs are present, the depth-5 one is not.
	if len(want) != 3 {
		t.Fatalf("expected the 3 within-depth installs, got %v", want)
	}
}

// TestDiscoverConcurrentSortedDeterministic asserts the returned slice is stable
// (sorted by path) across repeated runs despite nondeterministic worker order.
func TestDiscoverConcurrentSortedDeterministic(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"z-comfy", "m-comfy", "a-comfy", "q-comfy"} {
		makeComfy(t, filepath.Join(root, name))
	}
	var first []string
	for i := 0; i < 8; i++ {
		got, err := DiscoverInstalls(context.Background(), []string{root},
			DiscoverOptions{gitProbe: stubProbe})
		if err != nil {
			t.Fatalf("DiscoverInstalls: %v", err)
		}
		paths := make([]string, len(got))
		for j, in := range got {
			paths[j] = in.Path
		}
		if !sort.StringsAreSorted(paths) {
			t.Fatalf("results not sorted by path: %v", paths)
		}
		if first == nil {
			first = paths
		} else if !reflect.DeepEqual(first, paths) {
			t.Fatalf("nondeterministic output across runs:\n%v\n%v", first, paths)
		}
	}
	if len(first) != 4 {
		t.Fatalf("expected 4 installs, got %v", first)
	}
}

// TestDiscoverConcurrentNoRace runs discovery over a broad multi-install tree.
// Under `go test -race` it is the guard that the shared results collection and
// the walker's queue/visited state are properly synchronized.
func TestDiscoverConcurrentNoRace(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 12; i++ {
		makeComfy(t, filepath.Join(root, "grp", string(rune('a'+i)), "inst"))
	}
	for i := 0; i < 200; i++ {
		mkdirAll(t, filepath.Join(root, "fan", string(rune('a'+i%26)), string(rune('a'+(i/26)%26))))
	}
	got, err := DiscoverInstalls(context.Background(), []string{root},
		DiscoverOptions{MaxDepth: 5, gitProbe: stubProbe})
	if err != nil {
		t.Fatalf("DiscoverInstalls: %v", err)
	}
	if len(got) != 12 {
		t.Fatalf("expected 12 installs, got %d", len(got))
	}
}

// blockingReadDir returns a readDir seam that blocks (ignoring ctx, as a real
// syscall would) on any directory whose basename is in slow, until release is
// closed or a long fallback elapses. Non-slow dirs use the real os.ReadDir.
func blockingReadDir(release <-chan struct{}, slow ...string) func(string) ([]os.DirEntry, error) {
	slowSet := map[string]bool{}
	for _, s := range slow {
		slowSet[s] = true
	}
	return func(name string) ([]os.DirEntry, error) {
		if slowSet[filepath.Base(name)] {
			select {
			case <-release:
			case <-time.After(10 * time.Second):
			}
		}
		return os.ReadDir(name)
	}
}

// TestDiscoverHardDeadlineOnBlockedReadDir is the core regression guard for the
// 28.5s bug: a walk whose ReadDir blocks in a syscall (ignoring ctx) must NOT
// make the caller overrun the deadline. The old in-callback-only ctx check would
// return only after the block cleared; the goroutine + select-on-ctx.Done hard
// cap returns AT the budget instead.
func TestDiscoverHardDeadlineOnBlockedReadDir(t *testing.T) {
	root := t.TempDir()
	mkdirAll(t, filepath.Join(root, "slowroot"))
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })

	const budget = 200 * time.Millisecond
	// Block on the crawl root itself so NOTHING can complete before the deadline.
	start := time.Now()
	_, err := DiscoverInstalls(context.Background(), []string{root},
		DiscoverOptions{
			Budget:   budget,
			gitProbe: stubProbe,
			readDir:  blockingReadDir(release, filepath.Base(root)),
		})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatalf("expected a deadline error when the walk is blocked past the budget")
	}
	if elapsed > budget+2*time.Second {
		t.Fatalf("HARD DEADLINE VIOLATED: returned after %v (budget %v) — a blocked ReadDir overran the deadline", elapsed, budget)
	}
}

// TestDiscoverPartialResultsOnDeadline proves a budget-truncated crawl returns
// the installs found SO FAR (not an empty set) plus the context error, so the
// caller can render partial results.
func TestDiscoverPartialResultsOnDeadline(t *testing.T) {
	root := t.TempDir()
	// "ComfyUI" (0x43) sorts before "zzz-slow" (0x7a), so with the FIFO walker the
	// install is enqueued and processed before the blocking sibling regardless of
	// worker count.
	comfy := filepath.Join(root, "ComfyUI")
	makeComfy(t, comfy)
	mkdirAll(t, filepath.Join(root, "zzz-slow", "child"))
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })

	const budget = 400 * time.Millisecond
	start := time.Now()
	got, err := DiscoverInstalls(context.Background(), []string{root},
		DiscoverOptions{
			Budget:   budget,
			gitProbe: stubProbe,
			readDir:  blockingReadDir(release, "zzz-slow"),
		})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatalf("expected a deadline error (the crawl was truncated)")
	}
	if elapsed > budget+2*time.Second {
		t.Fatalf("returned after %v, budget %v — deadline not enforced", elapsed, budget)
	}
	if len(got) != 1 || got[0].Path != mustAbs(t, comfy) {
		t.Fatalf("expected the shallow install %s in the partial results, got %+v", comfy, got)
	}
}

func mustAbs(t *testing.T, p string) string {
	t.Helper()
	abs, err := filepath.Abs(p)
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

// TestDedupeRoots asserts an ancestor and its descendant collapse to just the
// ancestor, exact duplicates are removed, and unrelated roots are preserved.
func TestDedupeRoots(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "workspace")
	mkdirAll(t, sub)
	other := t.TempDir()

	got := dedupeRoots([]string{root, sub, root, other})
	sort.Strings(got)
	want := []string{other, root}
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("dedupeRoots = %v, want %v (descendant %s and duplicate should be dropped)", got, want, sub)
	}
}

// TestDiscoverOverlappingRootsNoDoubleWalk proves that when overlapping roots are
// passed, no directory is read twice — the root dedupe plus the walker's visited
// set guarantee each directory is processed at most once.
func TestDiscoverOverlappingRootsNoDoubleWalk(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "workspace")
	makeComfy(t, filepath.Join(sub, "ComfyUI"))
	for i := 0; i < 10; i++ {
		mkdirAll(t, filepath.Join(sub, "d", string(rune('a'+i))))
	}

	var counts sync.Map // path -> *int64
	countingReadDir := func(name string) ([]os.DirEntry, error) {
		key := resolveReal(name)
		v, _ := counts.LoadOrStore(key, new(int64))
		atomic.AddInt64(v.(*int64), 1)
		return os.ReadDir(name)
	}

	got, err := DiscoverInstalls(context.Background(), []string{root, sub, root},
		DiscoverOptions{MaxDepth: 5, gitProbe: stubProbe, readDir: countingReadDir})
	if err != nil {
		t.Fatalf("DiscoverInstalls: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected the single install, got %d: %+v", len(got), got)
	}
	counts.Range(func(k, v any) bool {
		if n := atomic.LoadInt64(v.(*int64)); n > 1 {
			t.Errorf("directory %s was read %d times (want ≤1) — overlapping roots double-walked", k, n)
		}
		return true
	})
}

// TestDiscoverNoDeadlockShapes exercises degenerate tree shapes to prove the
// worker pool always terminates (a deadlock/leak would hang the test): an empty
// tree, a single directory, and a deep-and-wide tree.
func TestDiscoverNoDeadlockShapes(t *testing.T) {
	done := make(chan struct{})
	go func() {
		defer close(done)

		// Empty root (no children).
		empty := t.TempDir()
		if _, err := DiscoverInstalls(context.Background(), []string{empty},
			DiscoverOptions{gitProbe: stubProbe}); err != nil {
			t.Errorf("empty tree: %v", err)
		}

		// Single install directory as the root.
		single := t.TempDir()
		makeComfy(t, single)
		got, err := DiscoverInstalls(context.Background(), []string{single},
			DiscoverOptions{gitProbe: stubProbe})
		if err != nil {
			t.Errorf("single dir: %v", err)
		}
		if len(got) != 1 {
			t.Errorf("single install root should be detected, got %d", len(got))
		}

		// Deep + wide tree.
		deep := t.TempDir()
		cur := deep
		for i := 0; i < 8; i++ {
			cur = filepath.Join(cur, "lvl")
		}
		makeComfy(t, filepath.Join(cur, "inst"))
		for i := 0; i < 100; i++ {
			mkdirAll(t, filepath.Join(deep, "w", string(rune('a'+i%26)), string(rune('a'+(i/26)%26))))
		}
		if _, err := DiscoverInstalls(context.Background(), []string{deep},
			DiscoverOptions{MaxDepth: 12, gitProbe: stubProbe}); err != nil {
			t.Errorf("deep tree: %v", err)
		}
	}()

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("discovery deadlocked / leaked: shapes test did not finish in 30s")
	}
}
