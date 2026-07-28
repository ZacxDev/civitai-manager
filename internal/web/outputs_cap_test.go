package web

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
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
