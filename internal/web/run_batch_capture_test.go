package web

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/comfy"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// ── batch attribution, through the REAL capture path ─────────────────────────
//
// 🔴 The tests in run_batch_test.go that assert batch identity replace
// srv.captureFn, so they observe runOptions and NEVER execute captureGeneration —
// the exact seam where the attribution has to be copied onto the store.Generation.
// The suite below deliberately leaves captureFn nil so the real capture runs, and
// asserts on the PERSISTED row plus ListGenerationsByBatch. Without that, migration
// 0016's columns, ix_generations_batch and ListGenerationsByBatch are unreachable in
// the shipped binary while every test is green.

// newBatchCaptureServer builds a server whose captureFn is NIL (so captureGeneration
// runs for real) with an outputs dir and a fake comfy serving image bytes.
func newBatchCaptureServer(t *testing.T) (*Server, *store.Workflow) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	srv := NewServer(st, stubReader{}, stubSubscriber{}, Config{
		Addr: "127.0.0.1:8787", OutputsDir: t.TempDir(), ComfyURL: "http://127.0.0.1:8188",
	}, nil)
	srv.comfyClientFn = func() comfyClient {
		return &fakeComfy{viewData: []byte("PNGBYTES"), viewCT: "image/png"}
	}
	return srv, seedBatchWorkflow(t, srv, "")
}

// batchRunFnDistinctPrompts returns a runFn that hands every item its own prompt id,
// so the captured files land under distinct output directories exactly as a real
// batch's do.
func batchRunFnDistinctPrompts(calls *int32) func(context.Context, *store.Workflow, runUpdater, runOptions) (*runResult, error) {
	return func(context.Context, *store.Workflow, runUpdater, runOptions) (*runResult, error) {
		n := atomic.AddInt32(calls, 1)
		return &runResult{PromptID: fmt.Sprintf("prompt-%d", n), Images: batchImages()}, nil
	}
}

// waitGenerations polls until want generation rows exist. The LAST item's capture
// runs AFTER applyBatchOutcomeLocked has cleared `running`, so waitBatchDone alone
// returns while a capture is still in flight.
func waitGenerations(t *testing.T, srv *Server, want int) []store.Generation {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	var gens []store.Generation
	for time.Now().Before(deadline) {
		var err error
		gens, err = srv.store.ListGenerations(context.Background(), store.ListGenerationsOpts{})
		if err != nil {
			t.Fatalf("list generations: %v", err)
		}
		if len(gens) >= want {
			return gens
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("only %d of %d generations were captured", len(gens), want)
	return nil
}

// TestBatchCapturePersistsBatchIdentity is the 🔴 regression: it walks the REAL
// captureGeneration and asserts the columns migration 0016 added actually carry a
// value, then proves ListGenerationsByBatch — the query the gallery grouping is
// built on — returns the batch in run order.
func TestBatchCapturePersistsBatchIdentity(t *testing.T) {
	srv, wf := newBatchCaptureServer(t)
	var calls int32
	srv.runFn = batchRunFnDistinctPrompts(&calls)

	if ok, _ := srv.startBatch(wf, runOptions{}, batchSpec{Count: 3}); !ok {
		t.Fatal("batch did not start")
	}
	waitBatchDone(t, srv)
	gens := waitGenerations(t, srv, 3)
	if len(gens) != 3 {
		t.Fatalf("generations = %d, want 3", len(gens))
	}

	batchID := gens[0].BatchID
	if batchID == "" {
		t.Fatal("PERSISTED batch_id is empty — every row is NULL and the batch " +
			"cannot be grouped in the gallery")
	}
	if !store.ValidBatchID(batchID) {
		t.Errorf("persisted batch id %q is not URL-safe", batchID)
	}
	for _, g := range gens {
		if g.BatchID != batchID {
			t.Errorf("generation %d batch_id = %q, want %q", g.ID, g.BatchID, batchID)
		}
		if g.BatchTotal != 3 {
			t.Errorf("generation %d batch_total = %d, want 3", g.ID, g.BatchTotal)
		}
		if g.BatchIndex < 1 || g.BatchIndex > 3 {
			t.Errorf("generation %d batch_index = %d, want 1..3", g.ID, g.BatchIndex)
		}
	}

	// The store query the batch page is built on must return the batch in run order.
	byBatch, err := srv.store.ListGenerationsByBatch(context.Background(), batchID)
	if err != nil {
		t.Fatalf("ListGenerationsByBatch: %v", err)
	}
	if len(byBatch) != 3 {
		t.Fatalf("ListGenerationsByBatch returned %d rows, want 3", len(byBatch))
	}
	for i, g := range byBatch {
		if g.BatchIndex != i+1 {
			t.Errorf("row %d has batch_index %d, want %d (run order)", i, g.BatchIndex, i+1)
		}
		if g.BatchTotal != 3 || g.BatchID != batchID {
			t.Errorf("row %d identity = (%q,%d,%d)", i, g.BatchID, g.BatchIndex, g.BatchTotal)
		}
	}
}

// TestSingleRunPersistsNoBatchIdentity is the other half, also through the real
// capture: an ordinary run must leave all THREE columns NULL, so
// `batch_id IS NOT NULL` keeps meaning "this was a batch item" and no ordinary run
// grows a bogus "Batch 1 of 1" caption.
func TestSingleRunPersistsNoBatchIdentity(t *testing.T) {
	srv, wf := newBatchCaptureServer(t)
	var calls int32
	srv.runFn = batchRunFnDistinctPrompts(&calls)

	if !srv.startRun(wf, runOptions{}) {
		t.Fatal("run did not start")
	}
	waitBatchDone(t, srv)
	gens := waitGenerations(t, srv, 1)
	if len(gens) != 1 {
		t.Fatalf("generations = %d, want 1", len(gens))
	}
	if g := gens[0]; g.BatchID != "" || g.BatchIndex != 0 || g.BatchTotal != 0 {
		t.Errorf("single run persisted batch attribution: batch_id=%q index=%d total=%d",
			g.BatchID, g.BatchIndex, g.BatchTotal)
	}
	// nullPositiveInt + the `if b.id != ""` guard together mean the columns are SQL
	// NULL, not a stored 0/"" — proven by the batch query refusing to find the row
	// under an empty id (an invalid id issues no query at all).
	rows, err := srv.store.ListGenerationsByBatch(context.Background(), "")
	if err != nil {
		t.Fatalf("ListGenerationsByBatch(\"\"): %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("an empty batch id selected %d rows", len(rows))
	}
}

// TestBatchCaptureSnapshotsTheItemSeed pins that the per-item fresh seed reaches the
// PERSISTED params snapshot, not just runOptions: the batch's value is N different
// images, and the gallery has to be able to say which seed produced which.
func TestBatchCaptureSnapshotsTheItemSeed(t *testing.T) {
	srv, _ := newBatchCaptureServer(t)
	wf := seedBatchWorkflow(t, srv, batchSeedGraph)
	keys := comfy.SeedWidgetKeys([]byte(wf.Graph))
	if len(keys) != 1 {
		t.Fatalf("fixture should expose exactly one seed, got %d", len(keys))
	}
	var calls int32
	srv.runFn = batchRunFnDistinctPrompts(&calls)

	if ok, _ := srv.startBatch(wf, runOptions{}, batchSpec{Count: 3, SeedKeys: keys}); !ok {
		t.Fatal("batch did not start")
	}
	waitBatchDone(t, srv)
	gens := waitGenerations(t, srv, 3)

	seen := map[string]bool{}
	for _, g := range gens {
		snap := parseRunParams(g.Params)
		var seed string
		for _, o := range snap.UIWidgetOverrides {
			if o.NodeID == keys[0].NodeID {
				seed = o.Value
			}
		}
		if seed == "" {
			t.Fatalf("generation %d snapshotted no seed override: %s", g.ID, g.Params)
		}
		if seen[seed] {
			t.Errorf("seed %q captured twice — the items were not re-rolled", seed)
		}
		seen[seed] = true
	}
}
