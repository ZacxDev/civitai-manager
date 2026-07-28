package web

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/comfy"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// seedCapGeneration writes a real size-byte file under root and inserts the
// matching generation row with an explicit created_at, so eviction age ordering
// and byte accounting are both deterministic.
func seedCapGeneration(t *testing.T, srv *Server, root, prompt string, size int64, created time.Time) int64 {
	t.Helper()
	rel := prompt + "/0-x.png"
	dest := filepath.Join(root, prompt, "0-x.png")
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(dest, bytes.Repeat([]byte("x"), int(size)), 0o644); err != nil {
		t.Fatalf("write %s: %v", dest, err)
	}
	id, err := srv.store.InsertGeneration(context.Background(),
		&store.Generation{PromptID: prompt, CreatedAt: created},
		[]store.GenerationImage{{Idx: 0, RelPath: rel, Filename: "x.png", SizeBytes: size}})
	if err != nil {
		t.Fatalf("insert %s: %v", prompt, err)
	}
	return id
}

// genExists reports whether a generation row is still present.
func genExists(t *testing.T, srv *Server, id int64) bool {
	t.Helper()
	_, _, err := srv.store.GetGeneration(context.Background(), id)
	return err == nil
}

// TestOutputsCapEvictsOldestAndStops asserts eviction deletes rows AND files
// oldest-first and stops the moment the total is back under the cap.
func TestOutputsCapEvictsOldestAndStops(t *testing.T) {
	srv, root, _ := newCaptureServer(t, &fakeComfy{})
	srv.cfg.OutputsMaxBytes = 250

	base := time.Now().UTC().Add(-time.Hour)
	oldest := seedCapGeneration(t, srv, root, "old", 100, base)
	mid := seedCapGeneration(t, srv, root, "mid", 100, base.Add(time.Minute))
	newest := seedCapGeneration(t, srv, root, "new", 100, base.Add(2*time.Minute))

	// 300 > 250: evicting the single oldest brings it to 200, so exactly ONE
	// generation must go — not everything over the line.
	srv.enforceOutputsCap(0)

	if genExists(t, srv, oldest) {
		t.Error("oldest generation should have been evicted")
	}
	if !genExists(t, srv, mid) || !genExists(t, srv, newest) {
		t.Error("eviction must stop as soon as the total is back under the cap")
	}
	if _, err := os.Stat(filepath.Join(root, "old", "0-x.png")); !os.IsNotExist(err) {
		t.Errorf("evicted file should be unlinked, stat err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "mid", "0-x.png")); err != nil {
		t.Errorf("surviving file should remain: %v", err)
	}
	total, _ := srv.store.SumGenerationImageBytes(context.Background())
	if total != 200 {
		t.Errorf("total after eviction = %d, want 200", total)
	}
}

// TestOutputsCapUnderCapEvictsNothing pins the boundary: a total EXACTLY at the
// cap is not over it.
func TestOutputsCapUnderCapEvictsNothing(t *testing.T) {
	srv, root, _ := newCaptureServer(t, &fakeComfy{})
	srv.cfg.OutputsMaxBytes = 300

	base := time.Now().UTC().Add(-time.Hour)
	a := seedCapGeneration(t, srv, root, "a", 150, base)
	b := seedCapGeneration(t, srv, root, "b", 150, base.Add(time.Minute))

	srv.enforceOutputsCap(0)

	if !genExists(t, srv, a) || !genExists(t, srv, b) {
		t.Error("nothing may be evicted at or under the cap")
	}
}

// TestOutputsCapZeroIsUnlimited asserts a cap of 0 disables eviction entirely.
func TestOutputsCapZeroIsUnlimited(t *testing.T) {
	srv, root, _ := newCaptureServer(t, &fakeComfy{})
	srv.cfg.OutputsMaxBytes = 0 // unlimited

	base := time.Now().UTC().Add(-time.Hour)
	a := seedCapGeneration(t, srv, root, "a", 1000, base)
	b := seedCapGeneration(t, srv, root, "b", 1000, base.Add(time.Minute))

	srv.enforceOutputsCap(0)

	if !genExists(t, srv, a) || !genExists(t, srv, b) {
		t.Error("cap 0 means unlimited — nothing may be evicted")
	}
}

// TestOutputsCapNeverEvictsJustCaptured asserts the generation just inserted is
// skipped even when the tree is still over the cap after evicting everything else.
func TestOutputsCapNeverEvictsJustCaptured(t *testing.T) {
	srv, root, _ := newCaptureServer(t, &fakeComfy{})
	srv.cfg.OutputsMaxBytes = 50

	base := time.Now().UTC().Add(-time.Hour)
	oldest := seedCapGeneration(t, srv, root, "old", 100, base)
	// The just-captured one is the NEWEST, but make it old-looking too so ordering
	// alone would not protect it.
	keep := seedCapGeneration(t, srv, root, "keep", 100, base.Add(time.Second))

	srv.enforceOutputsCap(keep)

	if genExists(t, srv, oldest) {
		t.Error("the older generation should have been evicted")
	}
	if !genExists(t, srv, keep) {
		t.Fatal("the just-captured generation must NEVER be evicted")
	}
	if _, err := os.Stat(filepath.Join(root, "keep", "0-x.png")); err != nil {
		t.Errorf("just-captured file must survive: %v", err)
	}
}

// TestOutputsCapMissingFileStillRemovesRow asserts a file already gone from disk
// does not abort the pass: the row is still deleted and later candidates are
// still evicted.
func TestOutputsCapMissingFileStillRemovesRow(t *testing.T) {
	srv, root, _ := newCaptureServer(t, &fakeComfy{})
	srv.cfg.OutputsMaxBytes = 50

	base := time.Now().UTC().Add(-time.Hour)
	gone := seedCapGeneration(t, srv, root, "gone", 100, base)
	next := seedCapGeneration(t, srv, root, "next", 100, base.Add(time.Minute))
	survivor := seedCapGeneration(t, srv, root, "survivor", 40, base.Add(2*time.Minute))

	// Delete the oldest generation's file behind the store's back.
	if err := os.Remove(filepath.Join(root, "gone", "0-x.png")); err != nil {
		t.Fatalf("pre-remove file: %v", err)
	}

	srv.enforceOutputsCap(0)

	if genExists(t, srv, gone) {
		t.Error("a missing on-disk file must still remove the row")
	}
	if genExists(t, srv, next) {
		t.Error("eviction must continue past a missing file (240 → 140 → 40)")
	}
	if !genExists(t, srv, survivor) {
		t.Error("eviction should have stopped once under the cap")
	}
}

// TestOutputsCapNeverEvictsFresherThanKeep is the F2 regression: captures are NOT
// serialized against each other (the run job clears `running` before the capture
// runs off runMu), so a second capture can insert a FRESHER generation while this
// pass is running. Protecting only keepID by equality let that fresher row be
// evicted; nothing with an id >= keepID may go.
func TestOutputsCapNeverEvictsFresherThanKeep(t *testing.T) {
	srv, root, _ := newCaptureServer(t, &fakeComfy{})
	srv.cfg.OutputsMaxBytes = 50

	base := time.Now().UTC().Add(-time.Hour)
	old := seedCapGeneration(t, srv, root, "old", 100, base)
	keep := seedCapGeneration(t, srv, root, "keep", 100, base.Add(time.Minute))
	// Inserted AFTER keep (higher id) and newer — i.e. the other capture's row.
	fresher := seedCapGeneration(t, srv, root, "fresher", 100, base.Add(2*time.Minute))

	// 300 > 50 and evicting `old` alone leaves 200 > 50, so the loop WILL walk past
	// it — the id guard is the only thing protecting `fresher`.
	srv.enforceOutputsCap(keep)

	if genExists(t, srv, old) {
		t.Error("the genuinely oldest generation should have been evicted")
	}
	if !genExists(t, srv, keep) {
		t.Error("the just-captured generation must never be evicted")
	}
	if !genExists(t, srv, fresher) {
		t.Fatal("a generation newer than keepID (another capture's) must never be evicted")
	}
	if _, err := os.Stat(filepath.Join(root, "fresher", "0-x.png")); err != nil {
		t.Errorf("fresher capture's file must survive: %v", err)
	}
}

// TestOutputsCapZeroByteRowsAreNotEvicted is the F3 regression: a generation with
// size_bytes = 0 (a 0-byte /view body writes fine and records n=0) frees nothing,
// so counting its deletion as progress walked the loop straight past the cap and
// deleted everything.
func TestOutputsCapZeroByteRowsAreNotEvicted(t *testing.T) {
	srv, root, _ := newCaptureServer(t, &fakeComfy{})
	srv.cfg.OutputsMaxBytes = 100

	base := time.Now().UTC().Add(-time.Hour)
	var zeros []int64
	for i := 0; i < 5; i++ {
		zeros = append(zeros, seedCapGeneration(t, srv, root,
			fmt.Sprintf("zero%d", i), 0, base.Add(time.Duration(i)*time.Minute)))
	}
	big := seedCapGeneration(t, srv, root, "big", 150, base.Add(10*time.Minute))

	srv.enforceOutputsCap(0)

	if genExists(t, srv, big) {
		t.Error("the 150-byte generation should have been evicted (150 > 100)")
	}
	for i, id := range zeros {
		if !genExists(t, srv, id) {
			t.Errorf("zero-byte generation %d was evicted; deleting it frees nothing", i)
		}
	}
	total, _ := srv.store.SumGenerationImageBytes(context.Background())
	if total != 0 {
		t.Errorf("total = %d, want 0", total)
	}
	if n, _ := srv.store.CountGenerations(context.Background(), nil); n != 5 {
		t.Errorf("remaining generations = %d, want the 5 zero-byte rows", n)
	}
}

// TestOutputsCapConcurrentCapturesKeepEachOther exercises the REAL overlap: four
// captures run at once, each inserting its own generation and then enforcing the
// cap. Without serialization + the id guard, passes racing on the same candidate
// list hit ErrNotFound on rows a sibling already deleted, fail to decrement their
// stale total, walk past the old rows and delete each other's fresh generations.
// Every fresh capture must survive.
func TestOutputsCapConcurrentCapturesKeepEachOther(t *testing.T) {
	fake := &fakeComfy{viewData: []byte("PNGBYTES"), viewCT: "image/png"} // 8 bytes
	srv, root, wf := newCaptureServer(t, fake)
	srv.cfg.OutputsMaxBytes = 200

	// 6 × 100 B of older generations: over the cap, and enough candidates for the
	// racing passes to collide on.
	base := time.Now().UTC().Add(-time.Hour)
	for i := 0; i < 6; i++ {
		seedCapGeneration(t, srv, root, fmt.Sprintf("old%d", i), 100,
			base.Add(time.Duration(i)*time.Minute))
	}

	const captures = 4
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < captures; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start // fire together
			srv.captureGeneration(wf, runOptions{}, &runResult{
				PromptID: fmt.Sprintf("fresh%d", i),
				Images:   []comfy.ImageRef{{Filename: "a.png"}},
			})
		}(i)
	}
	close(start)
	wg.Wait()

	// Every concurrently-captured generation must still be there.
	gens, err := srv.store.ListGenerations(context.Background(), store.ListGenerationsOpts{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	seen := map[string]bool{}
	for _, g := range gens {
		seen[g.PromptID] = true
	}
	for i := 0; i < captures; i++ {
		name := fmt.Sprintf("fresh%d", i)
		if !seen[name] {
			t.Errorf("concurrent capture %q was evicted by a sibling pass", name)
		}
		if _, err := os.Stat(filepath.Join(root, name, "0-a.png")); err != nil {
			t.Errorf("file for %q missing: %v", name, err)
		}
	}

	// And the cap was actually enforced: the old rows were trimmed to under it.
	total, err := srv.store.SumGenerationImageBytes(context.Background())
	if err != nil {
		t.Fatalf("sum: %v", err)
	}
	if total > srv.cfg.OutputsMaxBytes {
		t.Errorf("total = %d, want <= cap %d", total, srv.cfg.OutputsMaxBytes)
	}
}

// TestCaptureEnforcesOutputsCap is the end-to-end proof that a successful capture
// runs eviction: the new generation lands, the over-cap old one is evicted.
func TestCaptureEnforcesOutputsCap(t *testing.T) {
	fake := &fakeComfy{viewData: []byte("PNGBYTES"), viewCT: "image/png"} // 8 bytes
	srv, root, wf := newCaptureServer(t, fake)
	srv.cfg.OutputsMaxBytes = 50

	old := seedCapGeneration(t, srv, root, "old", 100, time.Now().UTC().Add(-time.Hour))

	srv.captureGeneration(wf, runOptions{}, &runResult{
		PromptID: "fresh", Images: []comfy.ImageRef{{Filename: "a.png"}},
	})

	if genExists(t, srv, old) {
		t.Error("capture must evict the oldest generation when over the cap")
	}
	gens, err := srv.store.ListGenerations(context.Background(), store.ListGenerationsOpts{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(gens) != 1 || gens[0].PromptID != "fresh" {
		t.Fatalf("generations = %+v, want just the fresh capture", gens)
	}
	if _, err := os.Stat(filepath.Join(root, "fresh", "0-a.png")); err != nil {
		t.Errorf("fresh capture file must survive: %v", err)
	}
}
