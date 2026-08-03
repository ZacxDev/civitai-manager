package web

import (
	"net/http"
	"strings"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/store"
)

// ─────────────────────────────────────────────────────────────────────────────
// THE SHARED NEVER-SUBMIT GATE (run_gate.go).
//
// These tests exist to prove ONE thing no per-surface test can: realRun and the
// pre-click readiness line read the SAME predicate. Every payload below is asserted
// on BOTH surfaces, so mutating runGate.blocked() turns two suites red instead of
// one — which is the only evidence the consolidation is real rather than two copies
// that happen to agree today.
//
// 🔴 REGRESSION, NOT INVARIANT GUARD, on the readiness side. Before this change the
// readiness line rendered data-readiness="ready" — "Ready — every node type and
// model file this workflow references is installed" — for every payload in
// unusableGraphs. Red at 43ade22 (the PR head), green at HEAD.
// ─────────────────────────────────────────────────────────────────────────────

// unusableGraphs are the payloads that are NOT an api graph at all. comfy.Preflight
// reports every one of them with all three lists EMPTY — byte-identical to what it
// reports for a spotless graph. That is the case a count-driven readiness decision
// structurally cannot see.
//
// The format column matters: `format` is a free string on store.Workflow and
// handleWorkflowImportPNG stores comfy.FormatAPI WITHOUT calling DetectFormat, so a
// UI-shaped graph really does arrive here labelled api (and an unrecognised label
// takes the same non-UI branch). That lax import is PRE-EXISTING and deliberately
// out of scope; these cases are why the gate has to hold regardless of it.
//
// runWant is the substring the terminal run panel must carry. It is per-case on
// purpose: it proves WHICH branch of the gate fired, so a case cannot pass by
// bailing out somewhere earlier and never reaching the gate at all.
var unusableGraphs = []struct {
	name    string
	format  string
	graph   string
	runWant string
}{
	{"json array", store.WorkflowFormatAPI,
		`[{"class_type":"KSampler","inputs":{}}]`, "Preflight failed"},
	{"json string", store.WorkflowFormatAPI,
		`"this is not a graph"`, "Preflight failed"},
	{"garbage bytes", store.WorkflowFormatAPI,
		`}{ not json at all`, "Preflight failed"},
	{"truncated object", store.WorkflowFormatAPI,
		`{"1":{"class_type":"Foo","inputs":{`, "Preflight failed"},
	// 🔴 The one that PARSES. `{}` is valid JSON, decodes to an empty node map, and
	// comfy.Preflight answers OK:true about it — so `!report.OK` does not cover it and
	// runGate needs its own NoNodes condition. This is also the one case realRun used
	// to submit, leaving ComfyUI's own validation to reject it.
	{"empty object", store.WorkflowFormatAPI,
		`{}`, "contains no nodes this app can read"},
	{"ui graph stored as api", store.WorkflowFormatAPI,
		readinessUIReadyGraph, "Preflight failed"},
	{"ui graph under an unrecognised format", "pnginfo",
		readinessUIReadyGraph, "Preflight failed"},
}

// gateCleanAPIGraph is an api graph that is clean against missingPackInfo — the
// positive control's input on the run side.
const gateCleanAPIGraph = `{"4":{"class_type":"CheckpointLoaderSimple",` +
	`"inputs":{"ckpt_name":"present.safetensors"}}}`

// TestReadinessNeverCallsAnUnusableGraphReady is the readiness half.
func TestReadinessNeverCallsAnUnusableGraphReady(t *testing.T) {
	for _, tc := range unusableGraphs {
		t.Run(tc.name, func(t *testing.T) {
			srv := newReadinessServer(t)
			// A WARM, GOOD cache. Without it every case would answer "cold" and this test
			// would pass while measuring nothing about the graph at all.
			seedObjectInfo(t, srv, readinessInfo)
			id := seedWorkflow(t, srv, tc.format, tc.graph)

			wantState(t, readiness(t, srv, id), "unknown", "graph")
		})
	}
}

// TestReadinessWarmCacheStillReachesReady is the readiness half's POSITIVE CONTROL.
// Every case above must be UNKNOWN — and a workflowReadiness hard-wired to "unknown"
// satisfies all of them. Same server construction, same warm cache, a graph that is
// genuinely fine.
func TestReadinessWarmCacheStillReachesReady(t *testing.T) {
	srv := newReadinessServer(t)
	seedObjectInfo(t, srv, readinessInfo)
	id := seedWorkflow(t, srv, store.WorkflowFormatAPI, readinessReadyGraph)

	wantState(t, readiness(t, srv, id), "ready", "")
}

// TestRunNeverSubmitsAnUnusableGraph is the realRun half — the SAME predicate,
// through the real run endpoint against a fake ComfyUI.
//
// 🔴 THIS IS THE CONSOLIDATION EVIDENCE. Deleting `g.NoNodes` from
// runGate.blocked() must turn BOTH this test and
// TestReadinessNeverCallsAnUnusableGraphReady red. Two independent copies of the
// decision cannot do that.
func TestRunNeverSubmitsAnUnusableGraph(t *testing.T) {
	for _, tc := range unusableGraphs {
		t.Run(tc.name, func(t *testing.T) {
			srv := newLibraryTestServer(t, t.TempDir())
			fake := nodepackFakeComfy(t)
			srv.comfyClientFn = func() comfyClient { return fake }

			id := seedWorkflow(t, srv, tc.format, tc.graph)
			if r := post(t, srv, "/workflows/"+id+"/run", nil, true); r.Code != http.StatusOK {
				t.Fatalf("run start = %d", r.Code)
			}
			body := pollRunUntilDone(t, srv, id)

			if fake.submitCalled {
				t.Fatalf("SUBMITTED a graph that is not a graph: %s", fake.submittedGraph)
			}
			// INTERMEDIATE STATE: the run reached the GATE and stopped there. "Submit was
			// not called" is equally true of a run that fell over earlier for an unrelated
			// reason, and a zero from a counter wired to nothing looks the same too.
			if !strings.Contains(body, tc.runWant) {
				t.Errorf("the run did not stop at the gate (want %q):\n%s", tc.runWant, body)
			}
		})
	}
}

// TestRunStillSubmitsAGoodGraph is the run half's POSITIVE CONTROL, and it is not
// optional: four consecutive "0 submits" runs in this repo's history proved nothing
// about the never-submit gate until one clean workflow pushed exactly 1 through the
// same counter. A gate mutated to `return true` passes every assertion above.
func TestRunStillSubmitsAGoodGraph(t *testing.T) {
	srv := newLibraryTestServer(t, t.TempDir())
	fake := nodepackFakeComfy(t)
	srv.comfyClientFn = func() comfyClient { return fake }

	id := seedWorkflow(t, srv, store.WorkflowFormatAPI, gateCleanAPIGraph)
	if r := post(t, srv, "/workflows/"+id+"/run", nil, true); r.Code != http.StatusOK {
		t.Fatalf("run start = %d", r.Code)
	}
	pollRunUntilDone(t, srv, id)

	if !fake.submitCalled {
		t.Fatal("a clean graph was NOT submitted — the counter is wired to nothing, " +
			"so every 'did not submit' assertion in this file is vacuous")
	}
}
