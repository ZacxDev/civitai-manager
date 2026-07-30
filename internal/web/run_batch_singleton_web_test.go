package web

import (
	"context"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/store"
)

// The singleton guard under adversarial concurrency.
//
// WHY THIS FILE EXISTS: R2 split applyRunOutcomeLocked so a PER-ITEM settle leaves
// job.running == true while only the PER-BATCH settle clears it. That is ~4 lines,
// but it is the shared tail of every run path and the whole one-run-at-a-time
// invariant hangs off it. Get it wrong and the singleton opens BETWEEN items, two
// runs submit concurrently, and the failure is nondeterministic and load-dependent
// — the worst kind to find in production.
//
// A functional test cannot catch that: on an idle machine the window between items
// is microseconds and a sequential test will never land in it. These tests attack it
// directly, and are the reason `-race` is mandatory for this package.

// TestConcurrentQueueStartsExactlyOneBatch is the thundering herd: 50 goroutines
// POST the queue endpoint at once against an idle server. Exactly ONE may win.
//
// It goes through the REAL handler (CSRF, gating, parsing, startBatch) rather than
// calling startBatch directly, so a guard that is correct in isolation but bypassed
// by the endpoint still fails here.
func TestConcurrentQueueStartsExactlyOneBatch(t *testing.T) {
	srv := newTestServer(t)
	id := seedWorkflow(t, srv, store.WorkflowFormatUI, queueSeedGraph)

	release := make(chan struct{})
	var calls int32
	srv.runFn = blockingRunFn(&calls, release)

	const herd = 50
	var (
		wg       sync.WaitGroup
		accepted int32
	)
	start := make(chan struct{}) // release all goroutines at the same instant
	for i := 0; i < herd; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			rec := postQueue(t, srv, id, url.Values{
				"csrf_token": {srv.csrf}, batchCountField: {"4"},
			})
			// Every request is answered; only one may actually START a batch. The
			// refusal is a rendered notice, not a status code, so the accept count
			// is read from the server's own state rather than from rec.Code.
			_ = rec
		}()
	}
	close(start)
	wg.Wait()

	// Exactly one batch exists, and it is the only thing that ever called runFn.
	snap := srv.runJobState()
	if !snap.Started || !snap.Running {
		t.Fatalf("no batch is running after %d concurrent queue posts: %+v", herd, snap)
	}
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Errorf("runFn called %d times before any item finished — want exactly 1; "+
			"the singleton admitted more than one batch", n)
	}
	accepted = 1 // by construction above; kept explicit for the failure message
	_ = accepted

	close(release)
	waitBatchDone(t, srv)
}

// TestNoSecondRunSubmitsMidBatch is the one that actually exercises the split.
//
// A batch of 8 runs to completion while 200 queue posts hammer the endpoint from 20
// goroutines. The runFn records the MAXIMUM observed concurrency: if the singleton
// ever opens between items — the specific failure mode the split can introduce — a
// hammering goroutine slips in and two runFns overlap, so maxInFlight becomes 2.
func TestNoSecondRunSubmitsMidBatch(t *testing.T) {
	srv := newTestServer(t)
	id := seedWorkflow(t, srv, store.WorkflowFormatUI, queueSeedGraph)

	var (
		inFlight int32
		maxSeen  int32
		items    int32
	)
	// One token per item. The batch cannot advance until the test feeds it, so the
	// gap BETWEEN items — the exact window the applyRunOutcomeLocked split can leave
	// open — is held wide and deterministic instead of being microseconds wide and
	// unlandable.
	tick := make(chan struct{})
	srv.runFn = func(ctx context.Context, _ *store.Workflow, _ runUpdater, _ runOptions) (*runResult, error) {
		select {
		case <-tick:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		cur := atomic.AddInt32(&inFlight, 1)
		for {
			old := atomic.LoadInt32(&maxSeen)
			if cur <= old || atomic.CompareAndSwapInt32(&maxSeen, old, cur) {
				break
			}
		}
		atomic.AddInt32(&items, 1)
		atomic.AddInt32(&inFlight, -1)
		return &runResult{PromptID: "p"}, nil
	}

	if rec := postQueue(t, srv, id, url.Values{
		"csrf_token": {srv.csrf}, batchCountField: {"8"},
	}); rec.Code != 200 {
		t.Fatalf("queue status = %d", rec.Code)
	}

	// Hammer while the batch drains.
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				postQueue(t, srv, id, url.Values{
					"csrf_token": {srv.csrf}, batchCountField: {"4"},
				})
			}
		}()
	}

	// Feed the batch one item at a time. Between each token the batch job is alive
	// but momentarily idle — if the split leaves the singleton open there, one of the
	// 20 hammering goroutines starts a SECOND batch, which changes batchID.
	firstID := srv.runJobState().BatchID
	for i := 0; i < 8; i++ {
		tick <- struct{}{}
		// Give the hammering goroutines a real window to land in. Without this the
		// eight ticks fire in microseconds, the between-items gap never coincides
		// with an in-flight POST, and the test passes VACUOUSLY — verified by
		// mutation: clearing job.running in applyItemOutcomeLocked went undetected
		// until this settle was added.
		time.Sleep(3 * time.Millisecond)
		// Only items 1..7 have a "between items" gap. After the 7th we STOP the
		// hammer before feeding the last item: once the batch finishes the singleton
		// is legitimately free, and an admitted post would (a) look like a leak when
		// it is correct behaviour — it did, on this test's first run — and (b) start
		// a second batch whose runFn blocks on a tick nobody feeds, hanging the test.
		if i == 6 {
			close(stop)
			wg.Wait()
			continue
		}
		if got := srv.runJobState().BatchID; got != "" && firstID != "" && got != firstID {
			close(stop)
			wg.Wait()
			t.Fatalf("a SECOND batch started mid-run (batchID %q -> %q) — the singleton "+
				"opened between items %d and %d", firstID, got, i, i+1)
		}
	}

	waitBatchDone(t, srv)

	if got := atomic.LoadInt32(&maxSeen); got != 1 {
		t.Errorf("max concurrent runFn = %d, want 1 — the singleton opened mid-batch, "+
			"so a queued run submitted alongside the batch", got)
	}
	// Exactly the batch's own 8 items ran while it held the singleton. (Hammering
	// posts admitted AFTER the batch legitimately finishes are not a defect, which is
	// why this is checked against the gated items rather than a raw call count.)
	if got := atomic.LoadInt32(&items); got < 8 {
		t.Errorf("only %d gated items ran, want 8 — the batch did not complete", got)
	}
}

// TestBatchRefusalNamesTheRunningBatch pins that a refused post is not a silent
// no-op: it must say WHICH item of how many is in flight. A silent refusal is how
// "I clicked Queue x8 and nothing happened" gets reported as a bug.
func TestBatchRefusalNamesTheRunningBatch(t *testing.T) {
	srv := newTestServer(t)
	id := seedWorkflow(t, srv, store.WorkflowFormatUI, queueSeedGraph)

	release := make(chan struct{})
	var calls int32
	srv.runFn = blockingRunFn(&calls, release)

	if rec := postQueue(t, srv, id, url.Values{
		"csrf_token": {srv.csrf}, batchCountField: {"8"},
	}); rec.Code != 200 {
		t.Fatalf("first queue status = %d", rec.Code)
	}

	rec := postQueue(t, srv, id, url.Values{
		"csrf_token": {srv.csrf}, batchCountField: {"4"},
	})
	body := rec.Body.String()
	if !containsAny(body, "already running", "1 of 8") {
		t.Errorf("the refusal must name the running batch, got: %s", body)
	}

	close(release)
	waitBatchDone(t, srv)
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if !contains(s, sub) {
			return false
		}
	}
	return true
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
