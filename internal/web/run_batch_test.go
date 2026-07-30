package web

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/comfy"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// seedBatchWorkflow inserts a UI-format workflow and returns the row, which is what
// startBatch takes (the HTTP handlers load the same thing).
func seedBatchWorkflow(t *testing.T, srv *Server, graph string) *store.Workflow {
	t.Helper()
	if graph == "" {
		graph = `{"nodes":[]}`
	}
	id, err := srv.store.InsertWorkflow(context.Background(), &store.Workflow{
		Name: "wf", Format: store.WorkflowFormatUI, Graph: graph,
		Source: store.WorkflowSourceImported,
	})
	if err != nil {
		t.Fatalf("seed workflow: %v", err)
	}
	wf, err := srv.store.GetWorkflow(context.Background(), id)
	if err != nil {
		t.Fatalf("load workflow: %v", err)
	}
	return wf
}

// waitBatchDone blocks until the run job settles, or fails the test.
func waitBatchDone(t *testing.T, srv *Server) runSnapshot {
	t.Helper()
	// Generous: -race under parallel-suite load is ~10x slower, so a tight deadline
	// flakes as a timeout rather than surfacing a real defect.
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if snap := srv.runJobState(); snap.Started && !snap.Running {
			return snap
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("batch did not settle: %+v", srv.runJobState())
	return runSnapshot{}
}

// waitCallCount blocks until the runFn call counter reaches n.
func waitCallCount(t *testing.T, calls *int32, n int32) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(calls) >= n {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("runFn call count stuck at %d, want %d", atomic.LoadInt32(calls), n)
}

func batchImages() []comfy.ImageRef {
	return []comfy.ImageRef{{Filename: "out.png", Type: "output"}}
}

// ── the cap ──────────────────────────────────────────────────────────────────

func TestClampBatchCount(t *testing.T) {
	for _, tc := range []struct {
		in      string
		want    int
		clamped bool
	}{
		{"", 1, false},
		{"0", 1, false},
		{"1", 1, false},
		{"2", 2, false},
		{"25", 25, false},
		{"26", 25, true},
		{"500", 25, true},
		{"-3", 1, false},
		{"abc", 1, false},
		{"8.5", 1, false},
		{" 8 ", 8, false},
		{"1e9", 1, false},
		// Overflows int64: unmistakably an ask for more than the cap, so it clamps
		// rather than degrading to a single run.
		{"99999999999999999999999", 25, true},
		{"-99999999999999999999999", 1, false},
	} {
		got, clamped := clampBatchCount(tc.in)
		if got != tc.want || clamped != tc.clamped {
			t.Errorf("clampBatchCount(%q) = (%d,%v), want (%d,%v)",
				tc.in, got, clamped, tc.want, tc.clamped)
		}
	}
}

// TestBatchCountCappedAtStart pins that the cap is enforced by startBatch itself,
// not only by the HTTP parse — a caller cannot smuggle an unbounded N past it.
func TestBatchCountCappedAtStart(t *testing.T) {
	srv := newTestServer(t)
	wf := seedBatchWorkflow(t, srv, "")
	var calls int32
	srv.runFn = func(context.Context, *store.Workflow, runUpdater, runOptions) (*runResult, error) {
		atomic.AddInt32(&calls, 1)
		return &runResult{PromptID: "p"}, nil
	}
	started, _ := srv.startBatch(wf, runOptions{}, batchSpec{Count: 10000})
	if !started {
		t.Fatal("batch did not start")
	}
	snap := waitBatchDone(t, srv)
	if snap.BatchTotal != maxBatchCount {
		t.Errorf("BatchTotal = %d, want %d", snap.BatchTotal, maxBatchCount)
	}
	if got := atomic.LoadInt32(&calls); got != maxBatchCount {
		t.Errorf("runFn calls = %d, want %d", got, maxBatchCount)
	}
}

// ── 🔴 the singleton, which is what the applyRunOutcomeLocked split risks ─────

// TestBatchSingletonHeldAcrossItems is the DETERMINISTIC proof that the
// applyItemOutcomeLocked / applyBatchOutcomeLocked split never opens the singleton.
//
// It probes the two windows separately:
//
//   - DURING an item, from inside runFn;
//   - BETWEEN items, from inside captureFn — which runBatch invokes off runMu after
//     item i has settled and before item i+1 begins. That is EXACTLY the gap the
//     old code would have opened by clearing `running` in the shared settle.
//
// A startRun that succeeds in either window means two runs would be submitting into
// ComfyUI at once.
func TestBatchSingletonHeldAcrossItems(t *testing.T) {
	srv := newTestServer(t)
	wf := seedBatchWorkflow(t, srv, "")

	var duringItem, betweenItems int32
	var probes int32

	srv.runFn = func(context.Context, *store.Workflow, runUpdater, runOptions) (*runResult, error) {
		atomic.AddInt32(&probes, 1)
		if ok, _ := srv.startBatch(wf, runOptions{}, batchSpec{Count: 1}); ok {
			atomic.AddInt32(&duringItem, 1)
		}
		return &runResult{PromptID: "p", Images: batchImages()}, nil
	}
	srv.captureFn = func(_ *store.Workflow, opts runOptions, _ *runResult) {
		// The LAST item's capture legitimately runs after the batch has settled (that
		// is today's behaviour for every single run too), so probe only the gaps that
		// are genuinely BETWEEN items. opts carries the item's own index, so the
		// decision does not depend on the job state under test.
		if opts.BatchIndex >= opts.BatchTotal {
			return
		}
		atomic.AddInt32(&probes, 1)
		if ok, _ := srv.startBatch(wf, runOptions{}, batchSpec{Count: 1}); ok {
			atomic.AddInt32(&betweenItems, 1)
		}
	}

	started, _ := srv.startBatch(wf, runOptions{}, batchSpec{Count: 6})
	if !started {
		t.Fatal("batch did not start")
	}
	snap := waitBatchDone(t, srv)

	if n := atomic.LoadInt32(&duringItem); n != 0 {
		t.Errorf("%d startRun calls succeeded WHILE an item was running", n)
	}
	if n := atomic.LoadInt32(&betweenItems); n != 0 {
		t.Errorf("%d startRun calls succeeded BETWEEN items — the singleton opened in "+
			"the settle gap, which is exactly what applyItemOutcomeLocked must prevent", n)
	}
	// 6 runs + 5 inter-item gaps. If the probes never fired the assertions above are
	// vacuous.
	if n := atomic.LoadInt32(&probes); n != 11 {
		t.Fatalf("probes fired %d times, want 11 — the test did not exercise both windows", n)
	}
	if snap.BatchDone != 6 {
		t.Errorf("BatchDone = %d, want 6", snap.BatchDone)
	}
}

// TestBatchConcurrentStartRunRace hammers startRun from 50 goroutines for the whole
// life of a batch, across every inter-item boundary, while a second set of
// goroutines polls runJobState concurrently.
//
// Exactly ZERO of the 50 may start, and no two items may ever be in flight at once.
// The hammering is stopped BEFORE the final item is released, so a start after the
// batch legitimately ends can never be mistaken for a defect.
//
// Run under -race, this is the test that would catch the singleton opening between
// items — a nondeterministic, load-dependent failure that no ordinary unit test sees.
func TestBatchConcurrentStartRunRace(t *testing.T) {
	srv := newTestServer(t)
	wf := seedBatchWorkflow(t, srv, "")

	const items = 8
	step := make(chan struct{})
	var calls, inFlight, overlap, sneakedIn, torn int32

	srv.runFn = func(context.Context, *store.Workflow, runUpdater, runOptions) (*runResult, error) {
		atomic.AddInt32(&calls, 1)
		if atomic.AddInt32(&inFlight, 1) > 1 {
			atomic.StoreInt32(&overlap, 1)
		}
		<-step // the test releases exactly one item at a time
		atomic.AddInt32(&inFlight, -1)
		return &runResult{PromptID: "p"}, nil
	}

	started, _ := srv.startBatch(wf, runOptions{}, batchSpec{Count: items})
	if !started {
		t.Fatal("batch did not start")
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				if ok, ref := srv.startBatch(wf, runOptions{}, batchSpec{Count: 4}); ok {
					atomic.AddInt32(&sneakedIn, 1)
					return
				} else if !ref.Running {
					// A refusal must always describe the batch that refused it.
					atomic.AddInt32(&torn, 1)
				}
			}
		}()
	}
	// Concurrent readers: a snapshot must never be internally inconsistent.
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				snap := srv.runJobState()
				if snap.BatchIndex > snap.BatchTotal || snap.BatchDone > snap.BatchIndex {
					atomic.AddInt32(&torn, 1)
				}
			}
		}()
	}

	// Release items 1..items-1, hammering across every gap between them.
	for i := 1; i < items; i++ {
		waitCallCount(t, &calls, int32(i))
		step <- struct{}{}
	}
	// Stop hammering while the batch is still provably running, THEN release the
	// last item.
	waitCallCount(t, &calls, items)
	close(stop)
	wg.Wait()
	step <- struct{}{}

	snap := waitBatchDone(t, srv)

	if n := atomic.LoadInt32(&sneakedIn); n != 0 {
		t.Errorf("%d concurrent startRun calls STARTED during the batch — the singleton "+
			"opened between items", n)
	}
	if atomic.LoadInt32(&overlap) != 0 {
		t.Error("two items were in flight at once — a second prompt was submitted mid-batch")
	}
	if n := atomic.LoadInt32(&torn); n != 0 {
		t.Errorf("%d torn/incoherent observations of the batch state", n)
	}
	if got := atomic.LoadInt32(&calls); got != items {
		t.Errorf("runFn calls = %d, want %d", got, items)
	}
	if snap.BatchDone != items || snap.BatchTotal != items {
		t.Errorf("terminal = %d of %d, want %d of %d",
			snap.BatchDone, snap.BatchTotal, items, items)
	}
}

// ── state machine ────────────────────────────────────────────────────────────

// TestBatchStateTransitions is the table over the three terminal shapes, including
// the single-run-as-a-batch-of-1 case that must stay byte-identical to today.
func TestBatchStateTransitions(t *testing.T) {
	for _, tc := range []struct {
		name      string
		count     int
		failAt    int // 0 = never
		stopAt    int // 0 = never
		wantCalls int
		wantDone  int
		wantIndex int
		wantSum   string
	}{
		{
			name: "single run is a batch of one", count: 1,
			wantCalls: 1, wantDone: 1, wantIndex: 1, wantSum: "",
		},
		{
			name: "all eight complete", count: 8,
			wantCalls: 8, wantDone: 8, wantIndex: 8, wantSum: "8 of 8 complete.",
		},
		{
			name: "failure at item three halts", count: 8, failAt: 3,
			wantCalls: 3, wantDone: 2, wantIndex: 3,
			wantSum: "Batch halted at item 3 of 8 — 2 completed, 5 not started.",
		},
		{
			name: "single run failure has no batch line", count: 1, failAt: 1,
			wantCalls: 1, wantDone: 0, wantIndex: 1, wantSum: "",
		},
		{
			name: "stop during item three", count: 8, stopAt: 3,
			wantCalls: 3, wantDone: 2, wantIndex: 3,
			wantSum: "Batch stopped — 2 of 8 completed, 6 cancelled.",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := newTestServer(t)
			fake := &fakeComfy{}
			srv.comfyClientFn = func() comfyClient { return fake }
			wf := seedBatchWorkflow(t, srv, "")

			var calls int32
			srv.runFn = func(ctx context.Context, _ *store.Workflow, _ runUpdater, _ runOptions) (*runResult, error) {
				n := int(atomic.AddInt32(&calls, 1))
				if tc.failAt > 0 && n == tc.failAt {
					return nil, errors.New("boom")
				}
				if tc.stopAt > 0 && n == tc.stopAt {
					// Stop arrives WHILE this item is in flight, exactly as a user click
					// does: it cancels the item context and interrupts ComfyUI.
					srv.stopRun()
					<-ctx.Done()
					return nil, ctx.Err()
				}
				return &runResult{PromptID: "p"}, nil
			}

			started, _ := srv.startBatch(wf, runOptions{}, batchSpec{Count: tc.count})
			if !started {
				t.Fatal("batch did not start")
			}
			snap := waitBatchDone(t, srv)

			if got := int(atomic.LoadInt32(&calls)); got != tc.wantCalls {
				t.Errorf("runFn calls = %d, want %d (the remainder must never be submitted)",
					got, tc.wantCalls)
			}
			if snap.BatchDone != tc.wantDone {
				t.Errorf("BatchDone = %d, want %d", snap.BatchDone, tc.wantDone)
			}
			if snap.BatchIndex != tc.wantIndex {
				t.Errorf("BatchIndex = %d, want %d", snap.BatchIndex, tc.wantIndex)
			}
			if snap.BatchSummary != tc.wantSum {
				t.Errorf("BatchSummary = %q, want %q", snap.BatchSummary, tc.wantSum)
			}
			if tc.stopAt > 0 {
				if !snap.Stopped {
					t.Error("snapshot not marked Stopped")
				}
				if !fake.interruptCalled {
					t.Error("Interrupt was not sent to ComfyUI for the in-flight prompt")
				}
			}
			if tc.count == 1 {
				if snap.BatchID != "" {
					t.Errorf("a single run must carry NO batch id, got %q", snap.BatchID)
				}
				if snap.IsBatch() {
					t.Error("a single run must not report as a batch")
				}
			}
		})
	}
}

// TestBatchStopBetweenItemsSubmitsNoMore pins the OTHER stop window: the click lands
// after item 3 settled and before item 4 was submitted. Nothing is queued ahead, so
// there is nothing to cancel — the remainder is simply never sent.
func TestBatchStopBetweenItemsSubmitsNoMore(t *testing.T) {
	srv := newTestServer(t)
	fake := &fakeComfy{}
	srv.comfyClientFn = func() comfyClient { return fake }
	wf := seedBatchWorkflow(t, srv, "")

	var calls int32
	srv.runFn = func(context.Context, *store.Workflow, runUpdater, runOptions) (*runResult, error) {
		atomic.AddInt32(&calls, 1)
		return &runResult{PromptID: "p", Images: batchImages()}, nil
	}
	// captureFn runs BETWEEN items, off runMu — the exact gap a Stop can land in.
	srv.captureFn = func(*store.Workflow, runOptions, *runResult) {
		if atomic.LoadInt32(&calls) == 3 {
			srv.stopRun()
		}
	}

	if ok, _ := srv.startBatch(wf, runOptions{}, batchSpec{Count: 8}); !ok {
		t.Fatal("batch did not start")
	}
	snap := waitBatchDone(t, srv)

	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Errorf("runFn calls = %d, want 3 — the remainder was submitted after Stop", got)
	}
	if !snap.Stopped {
		t.Error("not marked Stopped")
	}
	// Items 1-3 all reached done before the stop, so they all count as completed.
	if snap.BatchDone != 3 {
		t.Errorf("BatchDone = %d, want 3 (already-captured items are KEPT)", snap.BatchDone)
	}
	if snap.BatchSummary != "Batch stopped — 3 of 8 completed, 5 cancelled." {
		t.Errorf("summary = %q", snap.BatchSummary)
	}
}

// TestStopIsIdempotent pins that a double-click sends ONE Interrupt. A second one
// could land on whatever ComfyUI picked up next — including a prompt the user
// submitted from their own editor tab.
func TestStopIsIdempotent(t *testing.T) {
	srv := newTestServer(t)
	interrupts := 0
	var mu sync.Mutex
	srv.comfyClientFn = func() comfyClient { return &countingInterruptComfy{n: &interrupts, mu: &mu} }
	wf := seedBatchWorkflow(t, srv, "")

	release := make(chan struct{})
	srv.runFn = func(ctx context.Context, _ *store.Workflow, _ runUpdater, _ runOptions) (*runResult, error) {
		<-release
		return nil, ctx.Err()
	}
	if ok, _ := srv.startBatch(wf, runOptions{}, batchSpec{Count: 4}); !ok {
		t.Fatal("batch did not start")
	}
	// Both stops land while item 1 is still in flight.
	srv.stopRun()
	srv.stopRun()
	srv.stopRun()
	close(release)
	snap := waitBatchDone(t, srv)

	mu.Lock()
	got := interrupts
	mu.Unlock()
	if got != 1 {
		t.Errorf("Interrupt called %d times, want exactly 1", got)
	}
	if !snap.Stopped {
		t.Error("not marked Stopped")
	}
}

type countingInterruptComfy struct {
	fakeComfy
	n  *int
	mu *sync.Mutex
}

func (c *countingInterruptComfy) Interrupt(context.Context) error {
	c.mu.Lock()
	*c.n++
	c.mu.Unlock()
	return nil
}

// TestBatchSeqIsOnePerBatch pins the data-run-seq contract: ONE identity for the
// whole batch, not one per item. A per-item increment would make every poll look
// like a different run to anything keying on it.
func TestBatchSeqIsOnePerBatch(t *testing.T) {
	srv := newTestServer(t)
	wf := seedBatchWorkflow(t, srv, "")

	var seqs []int64
	var mu sync.Mutex
	srv.runFn = func(context.Context, *store.Workflow, runUpdater, runOptions) (*runResult, error) {
		snap := srv.runJobState()
		mu.Lock()
		seqs = append(seqs, snap.Seq)
		mu.Unlock()
		return &runResult{PromptID: "p"}, nil
	}

	before := srv.runJobState().Seq
	if ok, _ := srv.startBatch(wf, runOptions{}, batchSpec{Count: 8}); !ok {
		t.Fatal("batch did not start")
	}
	snap := waitBatchDone(t, srv)

	if snap.Seq != before+1 {
		t.Errorf("seq = %d, want %d — runSeq must increment ONCE per batch", snap.Seq, before+1)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seqs) != 8 {
		t.Fatalf("observed %d items, want 8", len(seqs))
	}
	for i, s := range seqs {
		if s != snap.Seq {
			t.Errorf("item %d observed seq %d, want a constant %d", i+1, s, snap.Seq)
		}
	}
}

// ── seeds ────────────────────────────────────────────────────────────────────

// TestBatchFreshSeedPerItem pins decision 2 end to end: every item submits a
// DIFFERENT seed, the value actually lands on the seed widget, and the preset's
// stored seed is overridden rather than reused.
//
// It asserts on the graph the run would CONVERT, not just on the override map: an
// implementation keyed on isSeedControlSlot would populate a map and change nothing
// in the graph.
func TestBatchFreshSeedPerItem(t *testing.T) {
	srv := newTestServer(t)
	wf := seedBatchWorkflow(t, srv, batchSeedGraph)

	seedKeys := comfy.SeedWidgetKeys([]byte(wf.Graph))
	if len(seedKeys) != 1 {
		t.Fatalf("fixture exposes %d seed inputs, want 1", len(seedKeys))
	}

	var mu sync.Mutex
	var seeds []string
	srv.runFn = func(_ context.Context, w *store.Workflow, _ runUpdater, opts runOptions) (*runResult, error) {
		// Apply the overrides exactly as realRun does, then read the seed back off
		// the resulting graph. This is the assertion a no-op implementation fails.
		applied := comfy.ApplyUIWidgetOverrides([]byte(w.Graph), opts.UIWidgetOverrides)
		got := comfy.SeedRunInputs(applied)
		if len(got) != 1 {
			t.Errorf("seed input vanished from the submitted graph: %+v", got)
			return &runResult{PromptID: "p"}, nil
		}
		mu.Lock()
		seeds = append(seeds, got[0].Current)
		mu.Unlock()
		return &runResult{PromptID: "p"}, nil
	}

	// The preset PINS a seed; every item must still get a fresh one.
	opts := runOptions{UIWidgetOverrides: map[comfy.UIWidgetKey]string{seedKeys[0]: "12345"}}
	if ok, _ := srv.startBatch(wf, opts, batchSpec{Count: 5, SeedKeys: seedKeys}); !ok {
		t.Fatal("batch did not start")
	}
	waitBatchDone(t, srv)

	mu.Lock()
	defer mu.Unlock()
	if len(seeds) != 5 {
		t.Fatalf("observed %d seeds, want 5", len(seeds))
	}
	seen := map[string]bool{}
	for i, s := range seeds {
		if s == "12345" {
			t.Errorf("item %d reused the preset's pinned seed — the batch is not randomising", i+1)
		}
		if seen[s] {
			t.Errorf("item %d repeated seed %q", i+1, s)
		}
		seen[s] = true
	}

	// The caller's map must be untouched: it is shared by every item AND snapshotted
	// into each captured generation's params.
	if opts.UIWidgetOverrides[seedKeys[0]] != "12345" {
		t.Errorf("the caller's override map was mutated: %v", opts.UIWidgetOverrides)
	}
}

// TestBatchWithoutSeedKeysDoesNotRandomise pins that an ordinary "Run once" (and any
// batch started with no seed keys) submits the seed the user can see in the field.
func TestBatchWithoutSeedKeysDoesNotRandomise(t *testing.T) {
	srv := newTestServer(t)
	wf := seedBatchWorkflow(t, srv, batchSeedGraph)
	keys := comfy.SeedWidgetKeys([]byte(wf.Graph))

	var mu sync.Mutex
	var seen []string
	srv.runFn = func(_ context.Context, _ *store.Workflow, _ runUpdater, opts runOptions) (*runResult, error) {
		mu.Lock()
		seen = append(seen, opts.UIWidgetOverrides[keys[0]])
		mu.Unlock()
		return &runResult{PromptID: "p"}, nil
	}
	opts := runOptions{UIWidgetOverrides: map[comfy.UIWidgetKey]string{keys[0]: "777"}}
	if ok, _ := srv.startBatch(wf, opts, batchSpec{Count: 3}); !ok {
		t.Fatal("batch did not start")
	}
	waitBatchDone(t, srv)

	mu.Lock()
	defer mu.Unlock()
	for i, s := range seen {
		if s != "777" {
			t.Errorf("item %d seed = %q, want the unmodified 777", i+1, s)
		}
	}
}

// batchSeedGraph is a minimal UI graph exposing exactly one seed (KSampler.seed at
// widgets_values[0], with the control_after_generate decoy at [1]).
const batchSeedGraph = `{"nodes":[
  {"id":3,"type":"KSampler","widgets_values":[42,"randomize",20,8,"euler","normal",1]}
]}`

// ── capture attribution ──────────────────────────────────────────────────────

// TestBatchCaptureRecordsBatchIdentity pins that every item's captured generation
// carries the SAME batch id, its own 1-based index, and N as requested — which is
// the whole point of problem 4 (an ungroupable gallery).
func TestBatchCaptureRecordsBatchIdentity(t *testing.T) {
	srv := newTestServer(t)
	wf := seedBatchWorkflow(t, srv, "")

	srv.runFn = func(context.Context, *store.Workflow, runUpdater, runOptions) (*runResult, error) {
		return &runResult{PromptID: "p", Images: batchImages()}, nil
	}
	var mu sync.Mutex
	var got []runOptions
	srv.captureFn = func(_ *store.Workflow, opts runOptions, _ *runResult) {
		mu.Lock()
		got = append(got, opts)
		mu.Unlock()
	}

	if ok, _ := srv.startBatch(wf, runOptions{}, batchSpec{Count: 4}); !ok {
		t.Fatal("batch did not start")
	}
	waitBatchDone(t, srv)

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 4 {
		t.Fatalf("captures = %d, want 4", len(got))
	}
	id := got[0].BatchID
	if id == "" {
		t.Fatal("batch id is empty — the captures cannot be grouped")
	}
	if !store.ValidBatchID(id) {
		t.Errorf("generated batch id %q is not URL-safe", id)
	}
	for i, o := range got {
		if o.BatchID != id {
			t.Errorf("item %d batch id = %q, want %q", i+1, o.BatchID, id)
		}
		if o.BatchIndex != i+1 {
			t.Errorf("item %d index = %d, want %d", i+1, o.BatchIndex, i+1)
		}
		if o.BatchTotal != 4 {
			t.Errorf("item %d total = %d, want 4", i+1, o.BatchTotal)
		}
	}
}

// TestSingleRunCapturesNoBatchIdentity pins that an ordinary run's generation row is
// indistinguishable from every pre-0016 row.
func TestSingleRunCapturesNoBatchIdentity(t *testing.T) {
	srv := newTestServer(t)
	wf := seedBatchWorkflow(t, srv, "")
	srv.runFn = func(context.Context, *store.Workflow, runUpdater, runOptions) (*runResult, error) {
		return &runResult{PromptID: "p", Images: batchImages()}, nil
	}
	var mu sync.Mutex
	var got runOptions
	srv.captureFn = func(_ *store.Workflow, opts runOptions, _ *runResult) {
		mu.Lock()
		got = opts
		mu.Unlock()
	}
	if !srv.startRun(wf, runOptions{}) {
		t.Fatal("run did not start")
	}
	waitBatchDone(t, srv)

	mu.Lock()
	defer mu.Unlock()
	if got.BatchID != "" || got.BatchIndex != 0 || got.BatchTotal != 0 {
		t.Errorf("single run carried batch attribution: %q %d %d",
			got.BatchID, got.BatchIndex, got.BatchTotal)
	}
}

// TestBatchItemStateIsResetBetweenItems pins that item i's terminal detail — the
// failure panel, the images, the prompt id — never leaks into item i+1's running
// view. A stale preflight report would render a failure panel over a healthy run.
func TestBatchItemStateIsResetBetweenItems(t *testing.T) {
	srv := newTestServer(t)
	wf := seedBatchWorkflow(t, srv, "")

	var calls int32
	var leaked int32
	srv.runFn = func(context.Context, *store.Workflow, runUpdater, runOptions) (*runResult, error) {
		n := atomic.AddInt32(&calls, 1)
		snap := srv.runJobState()
		if n > 1 {
			// runJobState copies into fresh slices, so an empty one is non-nil — check
			// LENGTH, not nil-ness.
			if len(snap.Images) > 0 || snap.PromptID != "" || len(snap.Warnings) > 0 ||
				snap.Preflight != nil || snap.Aborted || snap.Phase != runPhasePreparing {
				atomic.AddInt32(&leaked, 1)
			}
		}
		return &runResult{PromptID: "p", Images: batchImages()}, nil
	}
	if ok, _ := srv.startBatch(wf, runOptions{}, batchSpec{Count: 4}); !ok {
		t.Fatal("batch did not start")
	}
	waitBatchDone(t, srv)
	if n := atomic.LoadInt32(&leaked); n != 0 {
		t.Errorf("%d items started with the previous item's terminal state still on the job", n)
	}
}

// TestBatchRefusalDescribesTheRunningBatch pins the message that replaced the silent
// no-op. "I clicked Queue ×8 and nothing happened" is the failure it fixes.
func TestBatchRefusalDescribesTheRunningBatch(t *testing.T) {
	srv := newTestServer(t)
	wf := seedBatchWorkflow(t, srv, "")

	release := make(chan struct{})
	var calls int32
	srv.runFn = func(context.Context, *store.Workflow, runUpdater, runOptions) (*runResult, error) {
		atomic.AddInt32(&calls, 1)
		<-release
		return &runResult{PromptID: "p"}, nil
	}
	if ok, _ := srv.startBatch(wf, runOptions{}, batchSpec{Count: 8}); !ok {
		t.Fatal("batch did not start")
	}
	waitCallCount(t, &calls, 1)

	ok, ref := srv.startBatch(wf, runOptions{}, batchSpec{Count: 2})
	if ok {
		t.Fatal("a second batch started while one was running")
	}
	if !ref.Running || ref.Total != 8 || ref.Index != 1 {
		t.Fatalf("refusal = %+v, want a running batch at 1 of 8", ref)
	}
	if got := ref.notice(); got != "A batch is already running (1 of 8). Stop it before starting another." {
		t.Errorf("notice = %q", got)
	}

	// A refused SINGLE run says so without inventing a batch.
	single := batchRefusal{Running: true, Index: 1, Total: 1}
	if got := single.notice(); !strings.Contains(got, "A run is already in progress") {
		t.Errorf("single-run refusal = %q", got)
	}
	if (batchRefusal{}).notice() != "" {
		t.Error("the zero refusal must render nothing")
	}

	close(release)
	waitBatchDone(t, srv)
	if got := atomic.LoadInt32(&calls); got != 8 {
		t.Errorf("runFn calls = %d, want 8", got)
	}
}
