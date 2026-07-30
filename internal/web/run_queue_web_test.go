package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/comfy"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// queueSeedGraph exposes exactly one seed (KSampler.seed at widgets_values[0], with
// the control_after_generate decoy at [1]) plus a prompt, so the panel renders.
const queueSeedGraph = `{"nodes":[
  {"id":3,"type":"KSampler","widgets_values":[42,"randomize",20,8,"euler","normal",1]},
  {"id":6,"type":"CLIPTextEncode","widgets_values":["a cat"]}
]}`

// queueNoSeedGraph exposes an editable prompt but NO seed.
const queueNoSeedGraph = `{"nodes":[
  {"id":6,"type":"CLIPTextEncode","widgets_values":["a cat"]}
]}`

func postQueue(t *testing.T, srv *Server, wfID string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/workflows/"+wfID+"/run/queue",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

// blockingRunFn returns a runFn that counts calls and blocks until release closes.
func blockingRunFn(calls *int32, release <-chan struct{}) func(context.Context, *store.Workflow, runUpdater, runOptions) (*runResult, error) {
	return func(ctx context.Context, _ *store.Workflow, _ runUpdater, _ runOptions) (*runResult, error) {
		atomic.AddInt32(calls, 1)
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return &runResult{PromptID: "p"}, nil
	}
}

// ── gating & CSRF ────────────────────────────────────────────────────────────

// TestQueueRejectsMissingOrBadCSRF pins that the batch trigger — which drives real
// GPU work — cannot be fired cross-origin, and starts NOTHING when refused.
func TestQueueRejectsMissingOrBadCSRF(t *testing.T) {
	for _, tc := range []struct{ name, token string }{
		{"missing", ""},
		{"wrong", "not-the-token"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := newTestServer(t)
			id := seedWorkflow(t, srv, store.WorkflowFormatUI, queueSeedGraph)
			var calls int32
			srv.runFn = func(context.Context, *store.Workflow, runUpdater, runOptions) (*runResult, error) {
				atomic.AddInt32(&calls, 1)
				return &runResult{PromptID: "p"}, nil
			}
			form := url.Values{batchCountField: {"8"}}
			if tc.token != "" {
				form.Set("csrf_token", tc.token)
			}
			rec := postQueue(t, srv, id, form)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403", rec.Code)
			}
			if srv.runJobState().Started {
				t.Error("a run job was created despite the CSRF refusal")
			}
			if n := atomic.LoadInt32(&calls); n != 0 {
				t.Errorf("runFn called %d times despite the CSRF refusal", n)
			}
		})
	}
}

// TestQueueGatedOffLoopback pins that a LAN-exposed server cannot be made to run 25
// GPU jobs by a remote caller, even with a valid CSRF token (CSRF is not an auth
// boundary — the same rule every other run endpoint follows).
func TestQueueGatedOffLoopback(t *testing.T) {
	srv, _ := gateTestServer(t, "0.0.0.0:8787")
	id := seedWorkflow(t, srv, store.WorkflowFormatUI, queueSeedGraph)
	var calls int32
	srv.runFn = func(context.Context, *store.Workflow, runUpdater, runOptions) (*runResult, error) {
		atomic.AddInt32(&calls, 1)
		return &runResult{PromptID: "p"}, nil
	}
	rec := postQueue(t, srv, id, url.Values{
		"csrf_token": {srv.csrf}, batchCountField: {"8"},
	})
	if !strings.Contains(rec.Body.String(), gateMsg) {
		t.Fatalf("expected the loopback gate note, got: %s", rec.Body.String())
	}
	if srv.runJobState().Started {
		t.Error("a run job was created off-loopback")
	}
	if n := atomic.LoadInt32(&calls); n != 0 {
		t.Errorf("runFn called %d times off-loopback", n)
	}
}

// TestQueueRefusesAPIFormatWorkflow pins rule 1: an api graph has no editable
// parameters and no seed, so there is nothing to batch.
func TestQueueRefusesAPIFormatWorkflow(t *testing.T) {
	srv := newTestServer(t)
	id := seedWorkflow(t, srv, store.WorkflowFormatAPI, `{"1":{"class_type":"KSampler","inputs":{}}}`)
	rec := postQueue(t, srv, id, url.Values{"csrf_token": {srv.csrf}, batchCountField: {"4"}})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if srv.runJobState().Started {
		t.Error("a run job was created for an api workflow")
	}
}

// TestQueueRejectsForeignPresetID pins that a guessed preset id belonging to ANOTHER
// workflow is a 404 and starts nothing — running workflow 1's graph while attributed
// to workflow 2's preset would be a cross-workflow read.
func TestQueueRejectsForeignPresetID(t *testing.T) {
	srv := newTestServer(t)
	idA := seedWorkflow(t, srv, store.WorkflowFormatUI, queueSeedGraph)
	idB := seedWorkflow(t, srv, store.WorkflowFormatUI, queueSeedGraph)
	otherWF, _ := strconv.ParseInt(idB, 10, 64)
	pid, err := srv.store.CreateRunPreset(context.Background(), store.RunPreset{
		WorkflowID: otherWF, Name: "other", Params: "{}",
	})
	if err != nil {
		t.Fatalf("create preset: %v", err)
	}

	rec := postQueue(t, srv, idA, url.Values{
		"csrf_token": {srv.csrf}, batchCountField: {"4"},
		presetIDField: {strconv.FormatInt(pid, 10)},
	})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if srv.runJobState().Started {
		t.Error("a batch started with another workflow's preset id")
	}
}

// ── the cap, told not rejected ───────────────────────────────────────────────

// TestQueueClampsHostileCount pins that a hand-built request for 500 is CLAMPED and
// the user is told, rather than 500 GPU runs being queued or the request 400ing.
func TestQueueClampsHostileCount(t *testing.T) {
	srv := newTestServer(t)
	id := seedWorkflow(t, srv, store.WorkflowFormatUI, queueSeedGraph)
	release := make(chan struct{})
	defer close(release)
	var calls int32
	srv.runFn = blockingRunFn(&calls, release)

	rec := postQueue(t, srv, id, url.Values{"csrf_token": {srv.csrf}, batchCountField: {"500"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Queued 25 runs — the maximum per batch.") {
		t.Errorf("clamp not reported to the user: %s", body)
	}
	snap := srv.runJobState()
	if snap.BatchTotal != maxBatchCount {
		t.Errorf("BatchTotal = %d, want %d", snap.BatchTotal, maxBatchCount)
	}
	// The eviction consequence must be visible, not a surprise.
	if !strings.Contains(body, "evict your OLDEST captured generations") {
		t.Errorf("the outputs-cap consequence is not stated: %s", body)
	}
	srv.stopRun()
	waitBatchDone(t, srv)
}

// TestQueueQuickPickStartsBatch walks each quick pick as the button issues it.
func TestQueueQuickPickStartsBatch(t *testing.T) {
	for _, n := range batchQuickPicks {
		t.Run(strconv.Itoa(n), func(t *testing.T) {
			srv := newTestServer(t)
			id := seedWorkflow(t, srv, store.WorkflowFormatUI, queueSeedGraph)
			release := make(chan struct{})
			var calls int32
			srv.runFn = blockingRunFn(&calls, release)

			rec := postQueue(t, srv, id, url.Values{
				"csrf_token": {srv.csrf}, batchCountField: {strconv.Itoa(n)},
			})
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d", rec.Code)
			}
			snap := srv.runJobState()
			if snap.BatchTotal != n {
				t.Fatalf("BatchTotal = %d, want %d", snap.BatchTotal, n)
			}
			body := rec.Body.String()
			if !strings.Contains(body, `data-batch-total="`+strconv.Itoa(n)+`"`) {
				t.Errorf("running fragment lacks data-batch-total: %s", body)
			}
			if !strings.Contains(body, `data-batch-index="1"`) {
				t.Errorf("running fragment lacks data-batch-index: %s", body)
			}
			if !strings.Contains(body, "Item 1 of "+strconv.Itoa(n)) {
				t.Errorf("running fragment lacks the progress line: %s", body)
			}
			if !strings.Contains(body, "Stop batch") {
				t.Errorf("Stop must say it cancels the whole batch: %s", body)
			}
			if !hasRunPoller(body) {
				t.Errorf("running fragment lacks the poller: %s", body)
			}
			close(release)
			waitBatchDone(t, srv)
		})
	}
}

// ── the no-seed offer ────────────────────────────────────────────────────────

// TestQueueOffersNoSeedConfirm pins "offer, do not perform": a seedless graph's
// first Queue click starts NOTHING and names N.
func TestQueueOffersNoSeedConfirm(t *testing.T) {
	srv := newTestServer(t)
	id := seedWorkflow(t, srv, store.WorkflowFormatUI, queueNoSeedGraph)
	var calls int32
	srv.runFn = func(context.Context, *store.Workflow, runUpdater, runOptions) (*runResult, error) {
		atomic.AddInt32(&calls, 1)
		return &runResult{PromptID: "p"}, nil
	}
	rec := postQueue(t, srv, id, url.Values{"csrf_token": {srv.csrf}, batchCountField: {"8"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "no editable seed") {
		t.Errorf("offer missing: %s", body)
	}
	if !strings.Contains(body, "Queue 8 identical runs anyway") {
		t.Errorf("offer does not name N: %s", body)
	}
	if !strings.Contains(body, "Run once instead") {
		t.Errorf("offer has no way out: %s", body)
	}
	if srv.runJobState().Started {
		t.Fatal("the offer STARTED a batch — it must offer, not perform")
	}
	if n := atomic.LoadInt32(&calls); n != 0 {
		t.Errorf("runFn called %d times from the offer", n)
	}
}

// TestQueueSecondClickWithConfirmStarts pins that the offer has a way through.
func TestQueueSecondClickWithConfirmStarts(t *testing.T) {
	srv := newTestServer(t)
	id := seedWorkflow(t, srv, store.WorkflowFormatUI, queueNoSeedGraph)
	release := make(chan struct{})
	var calls int32
	srv.runFn = blockingRunFn(&calls, release)

	rec := postQueue(t, srv, id, url.Values{
		"csrf_token": {srv.csrf}, batchCountField: {"3"},
		batchConfirmNoSeedField: {"1"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	snap := srv.runJobState()
	if !snap.Started || snap.BatchTotal != 3 {
		t.Fatalf("confirmed batch did not start: %+v", snap)
	}
	if !strings.Contains(rec.Body.String(), "no editable seed, so every run uses identical parameters") {
		t.Errorf("the confirmed batch must still SAY the runs are identical: %s", rec.Body.String())
	}
	close(release)
	waitBatchDone(t, srv)
}

// TestQueueSingleRunNeedsNoSeedConfirm pins that the offer is about a BATCH: a
// count of 1 on a seedless graph is just an ordinary run.
func TestQueueSingleRunNeedsNoSeedConfirm(t *testing.T) {
	srv := newTestServer(t)
	id := seedWorkflow(t, srv, store.WorkflowFormatUI, queueNoSeedGraph)
	release := make(chan struct{})
	var calls int32
	srv.runFn = blockingRunFn(&calls, release)

	rec := postQueue(t, srv, id, url.Values{"csrf_token": {srv.csrf}, batchCountField: {"1"}})
	if strings.Contains(rec.Body.String(), "identical runs anyway") {
		t.Errorf("a single run must not be offered the identical-batch confirmation")
	}
	if !srv.runJobState().Started {
		t.Fatal("the single run did not start")
	}
	close(release)
	waitBatchDone(t, srv)
}

// TestQueueBusyRefusesBeforeTheNoSeedOffer pins that the singleton is checked BEFORE
// the offer. The trigger is real: a batch on a seedless workflow is running (it got
// through with confirm_no_seed=1), the user clicks Queue again. Answering with the
// offer — which is rendered ALONE into #run-status with hx-swap=innerHTML and carries
// neither a poller nor a Stop control — killed the 1 s poll loop and the only "Stop
// batch" button, so the running batch looked like it had vanished. The request was
// going to be refused anyway.
func TestQueueBusyRefusesBeforeTheNoSeedOffer(t *testing.T) {
	srv := newTestServer(t)
	id := seedWorkflow(t, srv, store.WorkflowFormatUI, queueNoSeedGraph)
	release := make(chan struct{})
	var calls int32
	srv.runFn = blockingRunFn(&calls, release)

	if rec := postQueue(t, srv, id, url.Values{
		"csrf_token": {srv.csrf}, batchCountField: {"3"}, batchConfirmNoSeedField: {"1"},
	}); rec.Code != http.StatusOK {
		t.Fatalf("first queue status = %d", rec.Code)
	}
	waitCallCount(t, &calls, 1)

	// The second click: seedless, count > 1, no confirm — the offer's exact trigger.
	rec := postQueue(t, srv, id, url.Values{"csrf_token": {srv.csrf}, batchCountField: {"3"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "identical runs anyway") {
		t.Errorf("the offer was rendered over a RUNNING batch: %s", body)
	}
	if !strings.Contains(body, "A batch is already running (1 of 3)") {
		t.Errorf("the refusal is not reported: %s", body)
	}
	if !hasRunPoller(body) {
		t.Errorf("the response dropped the poller — the batch stops updating: %s", body)
	}
	if !strings.Contains(body, "Stop batch") {
		t.Errorf("the response dropped the only Stop control: %s", body)
	}
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Errorf("runFn called %d times — the refused click started a run", n)
	}

	close(release)
	waitBatchDone(t, srv)
}

// TestQueueNoSeedOfferIsASiblingOfTheStatusFragment pins the second half: the offer
// is rendered ABOVE runStatusFragment, never instead of it. #run-status is swapped
// innerHTML, so an offer returned alone deletes whatever the run region held — here
// a stopped run's terminal panel and its "Run again" way forward. It also closes the
// window between the singleton check and this branch.
func TestQueueNoSeedOfferIsASiblingOfTheStatusFragment(t *testing.T) {
	srv := newTestServer(t)
	srv.comfyClientFn = func() comfyClient { return &fakeComfy{} }
	id := seedWorkflow(t, srv, store.WorkflowFormatUI, queueNoSeedGraph)
	release := make(chan struct{})
	var calls int32
	srv.runFn = blockingRunFn(&calls, release)

	if rec := postQueue(t, srv, id, url.Values{
		"csrf_token": {srv.csrf}, batchCountField: {"3"}, batchConfirmNoSeedField: {"1"},
	}); rec.Code != http.StatusOK {
		t.Fatalf("first queue status = %d", rec.Code)
	}
	waitCallCount(t, &calls, 1)
	srv.stopRun()
	close(release)
	waitBatchDone(t, srv)

	rec := postQueue(t, srv, id, url.Values{"csrf_token": {srv.csrf}, batchCountField: {"3"}})
	body := rec.Body.String()
	if !strings.Contains(body, "Queue 3 identical runs anyway") {
		t.Fatalf("the offer is missing: %s", body)
	}
	if !strings.Contains(body, "Run stopped.") {
		t.Errorf("the offer REPLACED the run status region instead of sitting above "+
			"it: %s", body)
	}
	if srv.runJobState().Running {
		t.Error("the offer started something")
	}
}

// ── the refusal that replaced the silent no-op ───────────────────────────────

// TestSecondBatchRefusedWithMessage pins the fix for "I clicked Queue ×8 and
// nothing happened": every run entry point now RENDERS the refusal, and starts
// nothing.
func TestSecondBatchRefusedWithMessage(t *testing.T) {
	srv := newTestServer(t)
	id := seedWorkflow(t, srv, store.WorkflowFormatUI, queueSeedGraph)
	release := make(chan struct{})
	var calls int32
	srv.runFn = blockingRunFn(&calls, release)

	if rec := postQueue(t, srv, id, url.Values{
		"csrf_token": {srv.csrf}, batchCountField: {"8"},
	}); rec.Code != http.StatusOK {
		t.Fatalf("first queue status = %d", rec.Code)
	}
	waitCallCount(t, &calls, 1)

	// Every fragment-returning run entry point, not just the queue one.
	for _, path := range []string{
		"/workflows/" + id + "/run/queue",
		"/workflows/" + id + "/run",
		"/workflows/" + id + "/run-with-params",
		"/workflows/" + id + "/run-with-options",
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, path,
			strings.NewReader(url.Values{"csrf_token": {srv.csrf}, batchCountField: {"2"}}.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("%s status = %d", path, rec.Code)
			continue
		}
		if !strings.Contains(rec.Body.String(), "A batch is already running (1 of 8)") {
			t.Errorf("%s did not report the running batch: %s", path, rec.Body.String())
		}
	}
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Errorf("runFn called %d times — a refused click started a run", n)
	}
	snap := srv.runJobState()
	if snap.BatchTotal != 8 {
		t.Errorf("the running batch was replaced: %+v", snap)
	}

	close(release)
	waitBatchDone(t, srv)
}

// ── mid-batch terminal rendering ─────────────────────────────────────────────

// TestHaltedFragmentKeepsFailurePanelAndBatchLine pins that the batch wrapper does
// not swallow the failing item's interactive fix affordances — those are what let
// the user fix it and re-queue — while still saying which item failed.
func TestHaltedFragmentKeepsFailurePanelAndBatchLine(t *testing.T) {
	srv := newTestServer(t)
	id := seedWorkflow(t, srv, store.WorkflowFormatUI, queueSeedGraph)

	var calls int32
	srv.runFn = func(context.Context, *store.Workflow, runUpdater, runOptions) (*runResult, error) {
		if atomic.AddInt32(&calls, 1) == 3 {
			// A missing MODEL is the realistic mid-batch halt: it is deterministic
			// across items (every item references the same graph), which is exactly
			// why the batch halts rather than skipping — item 4 would fail
			// identically. Matches the idiom in run_failure_open_comfy_web_test.go.
			return &runResult{Preflight: &comfy.PreflightReport{
				MissingModels: []string{"totally-missing.safetensors"},
			}}, nil
		}
		return &runResult{PromptID: "p"}, nil
	}

	if rec := postQueue(t, srv, id, url.Values{
		"csrf_token": {srv.csrf}, batchCountField: {"8"},
	}); rec.Code != http.StatusOK {
		t.Fatalf("queue status = %d", rec.Code)
	}
	waitBatchDone(t, srv)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/workflows/"+id+"/run/status", nil)
	srv.Handler().ServeHTTP(rec, req)
	body := rec.Body.String()

	if !strings.Contains(body, "Batch halted at item 3 of 8 — 2 completed, 5 not started.") {
		t.Errorf("halt summary missing: %s", body)
	}
	// The failing item's own panel, unchanged: the missing filename must still be
	// named, and the run-again affordance still offered.
	if !strings.Contains(body, "totally-missing.safetensors") {
		t.Errorf("the failing item's failure panel was swallowed: %s", body)
	}
	if hasRunPoller(body) {
		t.Errorf("a terminal fragment must carry NO poller: %s", body)
	}
	if n := atomic.LoadInt32(&calls); n != 3 {
		t.Errorf("runFn called %d times — the batch did not halt on the first failure", n)
	}
}

// TestStoppedFragmentCarriesBatchSummary pins the stopped terminal: no poller, the
// batch accounting, and the "Run again" way forward.
func TestStoppedFragmentCarriesBatchSummary(t *testing.T) {
	srv := newTestServer(t)
	fake := &fakeComfy{}
	srv.comfyClientFn = func() comfyClient { return fake }
	id := seedWorkflow(t, srv, store.WorkflowFormatUI, queueSeedGraph)

	release := make(chan struct{})
	var calls int32
	srv.runFn = blockingRunFn(&calls, release)
	if rec := postQueue(t, srv, id, url.Values{
		"csrf_token": {srv.csrf}, batchCountField: {"8"},
	}); rec.Code != http.StatusOK {
		t.Fatalf("queue status = %d", rec.Code)
	}
	waitCallCount(t, &calls, 1)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/workflows/run/stop",
		strings.NewReader(url.Values{"csrf_token": {srv.csrf}, "workflow_id": {id}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	srv.Handler().ServeHTTP(rec, req)
	body := rec.Body.String()

	if !strings.Contains(body, "Run stopped.") {
		t.Errorf("stopped fragment missing: %s", body)
	}
	if hasRunPoller(body) {
		t.Errorf("the stopped fragment must carry NO poller: %s", body)
	}
	if !fake.interruptCalled {
		t.Error("Interrupt was not sent for the in-flight prompt")
	}
	close(release)
	waitBatchDone(t, srv)

	// The final accounting appears once the job settles (Stop renders synchronously,
	// before the goroutine has settled — which is exactly why it is keyed off
	// `stopped`).
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/workflows/"+id+"/run/status", nil)
		srv.Handler().ServeHTTP(rec, req)
		if strings.Contains(rec.Body.String(), "Batch stopped — 0 of 8 completed, 8 cancelled.") {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Error("the stopped batch never reported its accounting")
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Errorf("runFn called %d times after Stop, want 1", n)
	}
}

// TestQueueControlRendersOutsideRunStatus pins the structural invariant: the Queue
// control (and the whole tab strip) lives in #run-params, a SIBLING of #run-status,
// so the 1 s poller cannot clobber a half-typed prompt.
func TestQueueControlRendersOutsideRunStatus(t *testing.T) {
	srv := newTestServer(t)
	id := seedWorkflow(t, srv, store.WorkflowFormatUI, queueSeedGraph)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/workflows/"+id, nil)
	srv.Handler().ServeHTTP(rec, req)
	body := rec.Body.String()

	if !strings.Contains(body, "Queue ×N") {
		t.Fatalf("the Queue control is not rendered: %s", body)
	}
	for _, n := range batchQuickPicks {
		if !strings.Contains(body, "×"+strconv.Itoa(n)) {
			t.Errorf("quick pick ×%d missing", n)
		}
	}
	if !strings.Contains(body, `max="`+strconv.Itoa(maxBatchCount)+`"`) {
		t.Error("the custom count input does not carry the cap")
	}
	if !strings.Contains(body, "/run/queue") {
		t.Error("the Queue control does not post to the queue endpoint")
	}

	params := strings.Index(body, `id="run-params"`)
	status := strings.Index(body, `id="run-status"`)
	queue := strings.Index(body, "Queue ×N")
	if params < 0 || status < 0 || queue < 0 {
		t.Fatalf("containers missing: params=%d status=%d queue=%d", params, status, queue)
	}
	if !(params < queue && queue < status) {
		t.Errorf("the Queue control must sit inside #run-params, BEFORE #run-status "+
			"(params=%d queue=%d status=%d)", params, queue, status)
	}
}
