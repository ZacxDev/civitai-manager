package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/comfy"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// This file guards the fix for: "the node-pack install feature is UNREACHABLE for
// UI-format workflows."
//
// realRun used to abort on conversion warnings BEFORE comfy.Preflight ran, leaving
// snap.Preflight nil — and the whole actionable failure UI in run_pages.go is behind
// `snap.Preflight != nil`. attributeMissingNodes, missingNodesPanel, the
// ComfyUI-Manager one-click install and the manual-command fallback were therefore
// dead code on that path, and the user saw one sentence plus a collapsed drawer
// holding a class name.
//
// The shape below mirrors the real report (workflow 590): an installed loader plus
// the uninstalled `UltimateSDUpscale`, which ships in ssitu's
// ComfyUI_UltimateSDUpscale pack.

// missingPackInfo installs ONE class. `ckpt_name` choices contain the file the
// fixtures reference, so preflight finds NO missing model unless a test asks for one.
const missingPackInfo = `{"CheckpointLoaderSimple":{"input":{"required":{"ckpt_name":[["present.safetensors"],{}]}},"input_order":{"required":["ckpt_name"]}}}`

// uiGraphWithUnknownNode is a UI-format graph whose only defect is one ACTIVE node
// of an uninstalled class. Everything else converts and preflights cleanly — which
// is the entire point: after the converter deletes node 663, preflight sees a valid
// graph and answers OK. Only an explicit gate can stop this from being submitted.
const uiGraphWithUnknownNode = `{"nodes":[
 {"id":4,"type":"CheckpointLoaderSimple","mode":0,"widgets_values":["present.safetensors"]},
 {"id":663,"type":"UltimateSDUpscale","mode":0}
],"links":[]}`

// uiGraphWithUnknownNodeAndMissingModel adds an absent model file to the same
// graph, so ONE failed run has to report both categories.
const uiGraphWithUnknownNodeAndMissingModel = `{"nodes":[
 {"id":4,"type":"CheckpointLoaderSimple","mode":0,"widgets_values":["absent-4xNomosWebPhoto.safetensors"]},
 {"id":663,"type":"UltimateSDUpscale","mode":0}
],"links":[]}`

// recordingAttribution is the attribution seam plus a record of what it was ASKED.
// The classes it receives are the assertion that the previously-dead attribution
// call is now reached at all, and with the right input.
type recordingAttribution struct {
	mu      sync.Mutex
	calls   int
	classes []string
}

func (r *recordingAttribution) fn(attr nodeAttribution) func(context.Context, []string) nodeAttribution {
	return func(_ context.Context, classes []string) nodeAttribution {
		r.mu.Lock()
		r.calls++
		r.classes = append([]string(nil), classes...)
		r.mu.Unlock()
		return attr
	}
}

func (r *recordingAttribution) snapshot() (int, []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls, append([]string(nil), r.classes...)
}

// nodepackFakeComfy is the fake ComfyUI these tests drive.
//
// 🔴 The programmed `history` is LOAD-BEARING for the never-submit guards, and it
// is not there to model a passing run. Without it a wrongly-submitted graph polls
// /history forever, the test dies on pollRunUntilDone's 30 s deadline, and the
// failure reads "run did not finish" — a red for the wrong reason that hides WHICH
// invariant broke. With it, a submitted graph settles immediately and the guard's
// own "SUBMITTED a graph with a hole in it" is the message you get. Measured: the
// gate mutation produced a 30 s timeout before this was added.
func nodepackFakeComfy(t *testing.T) *fakeComfy {
	t.Helper()
	return &fakeComfy{
		info: mustObjectInfo(t, missingPackInfo),
		history: &comfy.HistoryEntry{
			Status: comfy.HistoryStatus{Completed: true, StatusStr: "success"},
		},
	}
}

// ultimatePack is the real attribution for UltimateSDUpscale (ssitu), as
// ComfyUI-Manager V3.41 resolves it.
func ultimatePack() nodeAttribution {
	return nodeAttribution{
		ManagerPresent: true,
		Packs: []comfy.Pack{{
			ID:          "comfyui_ultimatesdupscale",
			Title:       "UltimateSDUpscale",
			Repository:  "https://github.com/ssitu/ComfyUI_UltimateSDUpscale",
			Version:     "1.2.0",
			Installable: true,
			Classes:     []string{"UltimateSDUpscale"},
			Source:      comfy.SourceMap,
		}},
	}
}

// TestRunUIWorkflowReachesTheMissingNodesPanel is the REGRESSION test for the
// reported bug. On pre-change code realRun returns at `len(warnings) > 0`, so
// Preflight stays nil, attributeMissingNodes is never called and the panel is never
// rendered — all three assertions below fail.
func TestRunUIWorkflowReachesTheMissingNodesPanel(t *testing.T) {
	srv := newLibraryTestServer(t, t.TempDir())
	fake := nodepackFakeComfy(t)
	srv.comfyClientFn = func() comfyClient { return fake }
	rec := &recordingAttribution{}
	srv.attributeFn = rec.fn(ultimatePack())

	id := seedWorkflow(t, srv, store.WorkflowFormatUI, uiGraphWithUnknownNode)
	if r := post(t, srv, "/workflows/"+id+"/run", nil, true); r.Code != http.StatusOK {
		t.Fatalf("run start = %d", r.Code)
	}
	body := pollRunUntilDone(t, srv, id)

	if fake.submitCalled {
		t.Fatal("a graph the converter had to cut a node out of must never be submitted")
	}

	// INTERMEDIATE STATE: attribution ran, and it was asked for exactly the class the
	// converter removed. Without this, the render assertions could pass off a canned
	// panel that the run path never actually reached.
	calls, classes := rec.snapshot()
	if calls != 1 {
		t.Fatalf("attributeMissingNodes called %d times, want exactly 1 (at settle, never per poll)", calls)
	}
	if len(classes) != 1 || classes[0] != "UltimateSDUpscale" {
		t.Fatalf("attribution asked for %v, want [UltimateSDUpscale]", classes)
	}

	for _, want := range []string{
		"Missing custom nodes", // the panel heading
		"UltimateSDUpscale",    // the class / pack
		"https://github.com/ssitu/ComfyUI_UltimateSDUpscale", // the repo the user needs
		"1 custom node missing",                              // the headline count
	} {
		if !strings.Contains(body, want) {
			t.Errorf("failure body is missing %q — the actionable node-pack UI did not render:\n%s", want, body)
		}
	}
	// The raw engine text is still available, subordinated.
	if !strings.Contains(body, "not available") {
		t.Errorf("failure body dropped the converter's own warning text:\n%s", body)
	}
}

// TestRunUIWorkflowReportsMissingModelsInTheSameRound covers proposal B: once the
// conversion abort falls through to preflight, the missing model files surface
// alongside the node pack instead of costing the user a second failed run.
func TestRunUIWorkflowReportsMissingModelsInTheSameRound(t *testing.T) {
	srv := newLibraryTestServer(t, t.TempDir())
	fake := nodepackFakeComfy(t)
	srv.comfyClientFn = func() comfyClient { return fake }
	srv.attributeFn = (&recordingAttribution{}).fn(ultimatePack())

	id := seedWorkflow(t, srv, store.WorkflowFormatUI, uiGraphWithUnknownNodeAndMissingModel)
	if r := post(t, srv, "/workflows/"+id+"/run", nil, true); r.Code != http.StatusOK {
		t.Fatalf("run start = %d", r.Code)
	}
	body := pollRunUntilDone(t, srv, id)

	if fake.submitCalled {
		t.Fatal("Submit must not be called")
	}
	if !strings.Contains(body, "absent-4xNomosWebPhoto.safetensors") {
		t.Errorf("the missing MODEL was not reported in the same round:\n%s", body)
	}
	if !strings.Contains(body, "Missing custom nodes") {
		t.Errorf("the missing NODE was not reported in the same round:\n%s", body)
	}
	// Both categories in one headline is what makes it one round-trip.
	if !strings.Contains(body, "1 model file and 1 custom node are missing") {
		t.Errorf("headline does not name both categories:\n%s", body)
	}
	// 🔴 END-TO-END honesty check. The render-level guard
	// (TestFailureCardStatesTheModelListIsALowerBound) proves the caveat renders for a
	// snapshot that carries the flag; this proves the REAL run path actually SETS it —
	// realRun → runResult → runJob → runSnapshot. Node 663 was deleted from the graph,
	// so whatever models it referenced are unknowable and the list above is bounded.
	if !strings.Contains(body, lowerBoundNotice190Marker) {
		t.Errorf("a real run that dropped a node did not mark its model list as bounded:\n%s", body)
	}
}

// TestRunNeverSubmitsAConversionBlockedGraph is the NEVER-SUBMIT INVARIANT GUARD.
// It is labelled as an invariant guard, not regression coverage: pre-change code
// also never submitted these graphs (it returned even earlier). Its job is to stop a
// FUTURE edit — the whole failure branch is now shared with `!report.OK`, and the
// gate deliberately does not derive never-submit from report.OK.
//
// Case 1 is the one that matters: after the converter deletes the unknown node the
// graph is VALID, so preflight would answer OK on its own.
//
// ⚠ An earlier version of this comment said "Mutating the gate to `if !report.OK`
// submits it." **Measured: it does NOT** — that mutation alone leaves this guard
// GREEN, because `report.OK = false` is also forced just above (see
// run_handlers.go, the graphIncomplete assignment). There are TWO independent
// covers and either one alone still refuses the submit; you have to delete both
// before this test goes red with its own `SUBMITTED a graph with a hole in it`.
//
// That makes the invariant STRONGER than the old comment claimed — but the claim
// mattered, because someone removing one cover would have read "the gate is what
// stops this", seen the suite stay green, and concluded they had broken nothing.
// Both covers are deliberate; do not collapse them into one.
func TestRunNeverSubmitsAConversionBlockedGraph(t *testing.T) {
	cases := map[string]struct {
		graph string
		// preflightWouldPass records the property that makes the case interesting.
		preflightWouldPass bool
	}{
		"unknown active node — preflight on the cut graph is clean": {
			graph:              uiGraphWithUnknownNode,
			preflightWouldPass: true,
		},
		"unknown active node AND a missing model": {
			graph:              uiGraphWithUnknownNodeAndMissingModel,
			preflightWouldPass: false,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			srv := newLibraryTestServer(t, t.TempDir())
			fake := nodepackFakeComfy(t)
			srv.comfyClientFn = func() comfyClient { return fake }
			srv.attributeFn = (&recordingAttribution{}).fn(ultimatePack())

			id := seedWorkflow(t, srv, store.WorkflowFormatUI, tc.graph)
			if r := post(t, srv, "/workflows/"+id+"/run", nil, true); r.Code != http.StatusOK {
				t.Fatalf("run start = %d", r.Code)
			}
			pollRunUntilDone(t, srv, id)

			if fake.submitCalled {
				t.Fatalf("SUBMITTED a graph with a hole in it (preflightWouldPass=%v): %s",
					tc.preflightWouldPass, fake.submittedGraph)
			}
		})
	}
}

// TestRunBatchNeverSubmitsAConversionBlockedGraph extends the invariant to the
// "Queue ×N" path. It is a SEPARATE endpoint that drives the same runFn N times, and
// a batch that halts on item 1 must have submitted nothing at all.
func TestRunBatchNeverSubmitsAConversionBlockedGraph(t *testing.T) {
	srv := newLibraryTestServer(t, t.TempDir())
	fake := nodepackFakeComfy(t)
	srv.comfyClientFn = func() comfyClient { return fake }
	srv.attributeFn = (&recordingAttribution{}).fn(ultimatePack())

	id := seedWorkflow(t, srv, store.WorkflowFormatUI, uiGraphWithUnknownNode)
	// The fixture graph exposes no seed widget, so the endpoint would otherwise
	// answer with its "batch without a seed?" offer and start nothing. Confirming it
	// is what gets us to the code path under test.
	form := url.Values{
		"csrf_token": {srv.csrf}, batchCountField: {"3"},
		batchConfirmNoSeedField: {"1"},
	}
	if r := postQueue(t, srv, id, form); r.Code != http.StatusOK {
		t.Fatalf("queue start = %d", r.Code)
	}
	body := pollRunUntilDone(t, srv, id)

	// INTERMEDIATE STATE: a batch of 3 really was created and really halted on the
	// first item. Without this, "nothing was submitted" would also be satisfied by a
	// batch that never started at all — which is exactly what the endpoint's no-seed
	// offer does if the confirm field above is dropped.
	snap := srv.runJobState()
	if snap.BatchTotal != 3 {
		t.Fatalf("BatchTotal = %d, want 3 — the batch path was not exercised", snap.BatchTotal)
	}
	if snap.BatchDone != 0 {
		t.Errorf("BatchDone = %d, want 0 — the batch must halt on the first blocked item", snap.BatchDone)
	}

	if fake.submitCalled {
		t.Fatalf("the batch path submitted a graph with a hole in it: %s", fake.submittedGraph)
	}
	if !strings.Contains(body, "Missing custom nodes") {
		t.Errorf("the batch path did not render the actionable node-pack panel:\n%s", body)
	}
}

// TestRunAPIFormatMissingNodesUnchanged is the REGRESSION guard for the untouched
// path: an api-format workflow keeps the unknown class_type verbatim, preflight
// finds it directly, and NO lower-bound caveat is shown — nothing was dropped, so
// the lists really are complete and the caveat would be a lie in the other
// direction.
func TestRunAPIFormatMissingNodesUnchanged(t *testing.T) {
	srv := newLibraryTestServer(t, t.TempDir())
	fake := nodepackFakeComfy(t)
	srv.comfyClientFn = func() comfyClient { return fake }
	rec := &recordingAttribution{}
	srv.attributeFn = rec.fn(ultimatePack())

	id := seedWorkflow(t, srv, store.WorkflowFormatAPI,
		`{"663":{"class_type":"UltimateSDUpscale","inputs":{}}}`)
	if r := post(t, srv, "/workflows/"+id+"/run", nil, true); r.Code != http.StatusOK {
		t.Fatalf("run start = %d", r.Code)
	}
	body := pollRunUntilDone(t, srv, id)

	if fake.submitCalled {
		t.Fatal("Submit must not be called when preflight reports a missing node")
	}
	_, classes := rec.snapshot()
	if len(classes) != 1 || classes[0] != "UltimateSDUpscale" {
		t.Fatalf("attribution asked for %v, want [UltimateSDUpscale]", classes)
	}
	if !strings.Contains(body, "Missing custom nodes") {
		t.Errorf("api-format path lost its node-pack panel:\n%s", body)
	}
	if strings.Contains(body, lowerBoundNotice190Marker) {
		t.Errorf("api-format path must NOT claim its list is a lower bound — nothing was dropped:\n%s", body)
	}
}

// lowerBoundNotice190Marker is the distinctive phrase of lowerBoundNoticeText.
// Asserting a SLICE of the real constant (rather than a hand-copied sentence) means
// a reworded caveat cannot silently stop being asserted, while still failing if the
// bound-naming clause itself is dropped.
const lowerBoundNotice190Marker = "a lower bound, not a complete list"

// TestFailureCardStatesTheModelListIsALowerBound pins proposal B's honesty
// requirement: a dropped node took its own model references out of the graph with
// it, so the missing-model list is a LOWER BOUND and the copy must not read as
// "these are all the files you need".
func TestFailureCardStatesTheModelListIsALowerBound(t *testing.T) {
	if !strings.Contains(lowerBoundNoticeText, lowerBoundNotice190Marker) {
		t.Fatalf("the caveat no longer names its bound; got: %q", lowerBoundNoticeText)
	}
	// It must not hedge the claim into meaninglessness either.
	for _, banned := range []string{"all the files", "complete set of", "everything you need"} {
		if strings.Contains(strings.ToLower(lowerBoundNoticeText), banned) {
			t.Errorf("the caveat claims completeness via %q: %q", banned, lowerBoundNoticeText)
		}
	}

	base := runSnapshot{
		Started: true, WorkflowID: 9, UIFormat: true,
		Phase: runPhaseFailed, Message: "Preflight failed.",
		Preflight: &comfy.PreflightReport{
			MissingNodes:  []string{"UltimateSDUpscale"},
			MissingModels: []string{"4xNomosWebPhoto_RealPLKSR.pth"},
		},
	}

	incomplete := base
	incomplete.GraphIncomplete = true
	withNotice := renderString(t, runStatusFragment(incomplete, 9, "csrf-tok", false, fullMaturityRange()))
	if !strings.Contains(withNotice, lowerBoundNotice190Marker) {
		t.Errorf("a failure whose graph lost nodes does not warn that the list is bounded:\n%s", withNotice)
	}

	whole := base // GraphIncomplete stays false
	withoutNotice := renderString(t, runStatusFragment(whole, 9, "csrf-tok", false, fullMaturityRange()))
	if strings.Contains(withoutNotice, lowerBoundNotice190Marker) {
		t.Errorf("a failure whose graph is INTACT must not claim its list is bounded:\n%s", withoutNotice)
	}
}
