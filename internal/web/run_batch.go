package web

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/comfy"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// ── Queue ×N — one job, N prompts, submitted SEQUENTIALLY ────────────────────
//
// Four decisions hold this together, and each is a constraint rather than an
// implementation detail:
//
//  1. THE EXISTING RUN SINGLETON STAYS. One batch job owns N prompts and submits
//     them one at a time (submit 1 → wait → submit 2 → …). N prompts are NEVER
//     fired into ComfyUI's queue at once, and the singleton is never lifted.
//     Everything else below follows from this: Stop has nothing to cancel beyond
//     the in-flight item, and a halt loses only runs that were never made.
//  2. EVERY ITEM GETS A NEW RANDOM SEED, everything else identical. The seed keys
//     come from comfy.SeedWidgetKeys (Kind == RunInputSeed) — see run_seed.go for
//     why isSeedControlSlot is the wrong hook and would make this a no-op.
//  3. THE FIRST NON-done ITEM HALTS THE BATCH, uniformly. No classification.
//  4. runSeq INCREMENTS ONCE PER BATCH, not per item, so data-run-seq keeps its
//     documented meaning ("this run attempt") across all N items.

// maxBatchCount is the HARD cap on N. Three independent arguments put it here:
//
//   - PROVABLE WALL CLOCK. Each item is bounded by runJobBudget (30 min), so 25 is
//     a 12.5-hour theoretical ceiling for a fully wedged batch — "bad day" rather
//     than "abandoned overnight". N=100 would be 50 hours.
//   - DISK, which is the real hazard. Every successful item captures its images and
//     then runs enforceOutputsCap, which EVICTS THE OLDEST generations to stay under
//     OutputsMaxBytes. A batch therefore has an N-fold amplified ability to churn
//     the user's earlier generations out of existence, so one click must not be able
//     to turn over a typical cap.
//   - UNWINDING IS MANUAL. Stop kills the remainder, but every already-captured
//     generation stays, and removing them is one delete each.
//
// 25 is a CAP, not a default: the control offers 2/4/8/16 plus a number input.
const maxBatchCount = 25

// batchQuickPicks are the one-click counts offered beside the number input.
var batchQuickPicks = []int{2, 4, 8, 16}

// batchCountField / batchConfirmNoSeedField are the queue endpoint's form fields.
const (
	batchCountField         = "count"
	batchConfirmNoSeedField = "confirm_no_seed"
)

// clampBatchCount parses and CLAMPS a requested count to [1, maxBatchCount]. The
// SERVER-SIDE clamp is the authority: the UI's own input can never produce an
// out-of-range value, and a hand-built request asking for 500 is clamped and TOLD
// rather than rejected.
//
// It reports whether the request was actually reduced, so the caller can say so.
// An unparseable value is 1 (the most conservative reading of "run this"), but a
// value that OVERFLOWS int64 is the cap, not 1 — "99999999999999999999" is
// unmistakably an ask for more than 25, and answering it with a single run would be
// the wrong kind of surprise.
func clampBatchCount(raw string) (n int, clamped bool) {
	s := strings.TrimSpace(raw)
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		if errors.Is(err, strconv.ErrRange) && !strings.HasPrefix(s, "-") {
			return maxBatchCount, true
		}
		return 1, false
	}
	switch {
	case v < 1:
		return 1, false
	case v > maxBatchCount:
		return maxBatchCount, true
	default:
		return int(v), false
	}
}

// batchSpec is what ONE startBatch call asks for. It is separate from runOptions
// because none of it is a per-run override: it describes the batch, not the graph.
type batchSpec struct {
	// Count is the requested item count. It is clamped again inside startBatch, so
	// a caller cannot smuggle an unbounded N past the cap.
	Count int
	// Message is the OPENING status line for item 1 only (a caller that reached the
	// run by a notable route — "the file was already installed" — says so there).
	// Server-authored; never reflected request input.
	Message string
	// SeedKeys are the widget keys to re-roll before EVERY item. Empty means "do not
	// randomise", which is what an ordinary single Run passes: only the Queue
	// endpoint randomises, because "Run once" must run the seed the user can see in
	// the field.
	SeedKeys []comfy.UIWidgetKey
}

// batchRefusal describes the batch that REFUSED a start, so the caller can render
// "A batch is already running (3 of 8)" instead of the silent no-op this used to be.
// Its zero value means "nothing refused anything".
type batchRefusal struct {
	Running    bool
	WorkflowID int64
	Index      int
	Total      int
}

// notice is the server-authored refusal line. Nothing from the request reaches it.
func (r batchRefusal) notice() string {
	if !r.Running {
		return ""
	}
	if r.Total > 1 {
		return fmt.Sprintf("A batch is already running (%d of %d). Stop it before starting another.",
			r.Index, r.Total)
	}
	return "A run is already in progress. Stop it before starting another."
}

// batchPlan is the IMMUTABLE plan the batch goroutine executes. Everything mutable
// lives on runJob under runMu; everything here is fixed before the goroutine starts
// and never written again, so it is safe to read without the lock.
type batchPlan struct {
	job    *runJob
	wf     *store.Workflow
	opts   runOptions
	spec   batchSpec
	id     string
	count  int
	ctx    context.Context
	cancel context.CancelFunc
	up     runUpdater
	run    func(ctx context.Context, wf *store.Workflow, up runUpdater, opts runOptions) (*runResult, error)
}

// startBatch launches ONE batch job for wf unless a run is already in flight.
//
// It is the single entry point for every run in this server: startRunWithMessage
// calls it with Count 1, so a single run IS a batch of one and there is exactly one
// settle path to reason about.
//
// It reports whether the job started and, when it did not, the progress of the
// batch that refused it — both read under the SAME runMu hold, so the refusal line
// can never describe a batch that had already finished by the time it was rendered.
func (s *Server) startBatch(wf *store.Workflow, opts runOptions, spec batchSpec) (bool, batchRefusal) {
	count := spec.Count
	if count < 1 {
		count = 1
	}
	if count > maxBatchCount {
		count = maxBatchCount
	}

	s.runMu.Lock()
	if s.runJob != nil && s.runJob.running {
		ref := batchRefusal{
			Running: true, WorkflowID: s.runJob.workflowID,
			Index: s.runJob.batchIndex, Total: s.runJob.batchTotal,
		}
		s.runMu.Unlock()
		return false, ref // one run at a time
	}

	base := s.baseCtx
	if base == nil {
		base = context.Background()
	}
	// The BATCH context carries NO total deadline: a legitimate 8 × 10-minute batch
	// is 80 minutes, and a 30-minute cap would kill it mid-way. Termination is still
	// provable — count <= maxBatchCount and every ITEM is bounded by runJobBudget, so
	// the worst case is 25 × 30 min. That bound is the primary argument for the cap.
	batchCtx, cancel := context.WithCancel(base)

	// ONCE PER BATCH — data-run-seq must stay stable across all N items, which is
	// what makes it "this run attempt" rather than "this prompt".
	s.runSeq++
	// A single run stores no batch id at all, so its generation row is NULL in all
	// three batch columns, exactly like every pre-0016 row.
	batchID := ""
	if count > 1 {
		batchID = comfy.NewID()
	}
	job := &runJob{
		running: true, workflowID: wf.ID, seq: s.runSeq, phase: runPhasePreparing,
		message: spec.Message, startedAt: time.Now(), cancel: cancel,
		uiFormat:   wf.Format == store.WorkflowFormatUI,
		batchID:    batchID,
		batchTotal: count,
		batchIndex: 1,
	}
	s.runJob = job

	run := s.runFn
	if run == nil {
		run = s.realRun
	}
	plan := batchPlan{
		job: job, wf: wf, opts: opts, spec: spec, id: batchID, count: count,
		ctx: batchCtx, cancel: cancel, up: s.newRunUpdater(job), run: run,
	}
	s.runMu.Unlock()

	go s.runBatch(plan)
	return true, batchRefusal{}
}

// runBatch executes the plan's items sequentially. It is the ONLY writer of the
// batch's progress, and every write happens under runMu.
//
// The ordering contract inside the loop is exactly the one settleAndCapture has
// always had, extended by one item:
//
//	lock:   observe Stop, reset the per-item fields, arm the item context
//	unlock
//	        run the item (UNCHANGED runFn — realRun does not know about batches)
//	lock:   classify the item; finish the BATCH iff it is terminal/stopped/last
//	unlock
//	        capture, OUTSIDE the mutex (network /view + disk writes)
//
// Capture running off the mutex means item i's capture can overlap item i+1's run —
// already true today for two consecutive runs, and safe because enforceOutputsCap
// never evicts an id >= the generation that triggered it.
func (s *Server) runBatch(b batchPlan) {
	defer b.cancel()
	for i := 1; i <= b.count; i++ {
		itemCtx, itemCancel, ok := s.beginBatchItem(b, i)
		if !ok {
			return // Stop landed between items — the remainder is never submitted
		}

		itemOpts := b.opts
		// Batch attribution is written ONLY for a real (multi-item) batch. A single
		// run must leave all THREE generation columns NULL — writing index=1,
		// total=1 with a NULL batch_id would make `batch_index IS NOT NULL` stop
		// meaning "this was a batch item" and give every ordinary run a bogus
		// "Batch 1/1" caption.
		if b.id != "" {
			itemOpts.BatchID, itemOpts.BatchIndex, itemOpts.BatchTotal = b.id, i, b.count
		}
		itemOpts = withFreshSeeds(itemOpts, b.spec.SeedKeys)

		res, err := runItemGuarded(itemCtx, b, itemOpts)
		itemCancel()

		s.runMu.Lock()
		terminal := s.applyItemOutcomeLocked(b.job, res, err)
		phase := b.job.phase
		if phase == runPhaseDone {
			b.job.batchDone++
		}
		// The batch ends here on a failure, on Stop, or on the last item. Anything
		// else keeps `running` true — that is the singleton staying held.
		finished := terminal || b.job.batchStop || i == b.count
		if finished {
			s.applyBatchOutcomeLocked(b.job)
		}
		s.runMu.Unlock()

		if phase == runPhaseDone && res != nil && len(res.Images) > 0 {
			s.captureBatchItem(b.wf, itemOpts, res)
		}
		if finished {
			return
		}
	}
}

// beginBatchItem opens item i under runMu: it observes Stop BEFORE any work, resets
// the per-item view fields, and arms the item context. ok=false means the batch has
// been stopped and has already been settled.
func (s *Server) beginBatchItem(b batchPlan, i int) (context.Context, context.CancelFunc, bool) {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	if b.job.batchStop {
		s.applyBatchOutcomeLocked(b.job)
		return nil, nil, false
	}
	// Derived from the BATCH context, so cancelling the batch also cancels the item;
	// bounded by runJobBudget, which is what makes the whole batch's worst case
	// provable.
	ctx, cancel := context.WithTimeout(b.ctx, runJobBudget)
	b.job.itemCancel = cancel
	b.job.batchIndex = i
	resetBatchItemLocked(b.job, i, b.spec.Message)
	return ctx, cancel, true
}

// resetBatchItemLocked clears item i-1's terminal detail off the job so the running
// fragment for item i never shows the previous item's failure panel or images. The
// caller MUST hold runMu.
//
// For item 1 this is a no-op over a freshly built job — deliberately: one reset path
// means a field added to runJob cannot be reset for later items but leaked into the
// first, or vice versa.
func resetBatchItemLocked(job *runJob, i int, opening string) {
	job.phase, job.queuePos = runPhasePreparing, 0
	if i > 1 {
		// The opening line belonged to item 1's route ("the file was already
		// installed…"); repeating it for item 5 would be false.
		job.message = "Starting run…"
	} else {
		job.message = opening
	}
	job.promptID = ""
	job.images = nil
	job.preflight = nil
	job.missingModels = nil
	job.missingResolved = nil
	job.libMeta = nil
	job.nodeAttr = nodeAttribution{}
	job.warnings = nil
	job.aborted = false
	job.err = nil
}

// runItemGuarded runs ONE item through the unchanged run seam, panic-guarded.
//
// The run path parses two large untrusted surfaces (the imported UI graph in
// ConvertUIToAPI and untrusted comfy JSON). A panic must fail THIS item — which
// halts the batch — not crash the server.
func runItemGuarded(ctx context.Context, b batchPlan, opts runOptions) (res *runResult, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("run panicked: %v", r)
		}
	}()
	return b.run(ctx, b.wf, b.up, opts)
}

// withFreshSeeds returns opts with a NEW random value on every seed key.
//
// The override map is COPIED, never mutated: the caller's map is shared by every
// item of the batch and is also snapshotted into each captured generation's params,
// so writing through it would retroactively rewrite the seed recorded for items
// that already finished.
//
// It overrides the preset's stored seed whether or not one was stored — "everything
// else identical" explicitly does not include the seed.
func withFreshSeeds(opts runOptions, keys []comfy.UIWidgetKey) runOptions {
	if len(keys) == 0 {
		return opts
	}
	m := make(map[comfy.UIWidgetKey]string, len(opts.UIWidgetOverrides)+len(keys))
	for k, v := range opts.UIWidgetOverrides {
		m[k] = v
	}
	for _, k := range keys {
		m[k] = comfy.NewSeed()
	}
	opts.UIWidgetOverrides = m
	return opts
}

// captureBatchItem runs the (best-effort, panic-guarded) output capture for one
// finished item. It mirrors settleAndCapture's capture half exactly — including the
// last-resort recover, because a capture failure must NEVER change or crash a
// settled run.
func (s *Server) captureBatchItem(wf *store.Workflow, opts runOptions, res *runResult) {
	defer func() {
		if r := recover(); r != nil {
			s.log.Error("output capture panicked", "err", r)
		}
	}()
	capture := s.captureFn
	if capture == nil {
		capture = s.captureGeneration
	}
	capture(wf, opts, res)
}
