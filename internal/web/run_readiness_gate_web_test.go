package web

import (
	"net/http"
	"strings"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/comfy"
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
// 🔴 REGRESSION, NOT INVARIANT GUARD, on the readiness side — and the baseline is
// PER CASE, so read the two apart rather than quoting one ref for the table:
//
//   - Most of unusableGraphs is red at 43ade22 (the last commit before the gate was
//     consolidated) and green from ca3d19d on. Before that the readiness line
//     rendered data-readiness="ready" — "Ready — every node type and model file this
//     workflow references is installed" — for all of them.
//   - The two class_type-less cases are red at 43ade22 AND STILL RED at b7f9be8,
//     i.e. they survived the consolidation: the gate was reading Preflight's Nodes
//     count faithfully, and that count was itself wrong. They go green only with the
//     usableAPINodes change in comfy. Verified with `go build ./...` passing, so the
//     red is an assertion and not a compiler-caught false red.
//
// Both surfaces iterate this ONE table, which is what makes the consolidation claim
// testable rather than asserted.
// ─────────────────────────────────────────────────────────────────────────────

// unusableGraphs are the payloads that are NOT an api graph at all. comfy.Preflight
// reports every one of them with all three lists EMPTY — byte-identical to what it
// reports for a spotless graph. That is the case a count-driven readiness decision
// structurally cannot see.
//
// The format column matters: `format` is a free string on store.Workflow with no DB
// CHECK constraint, so a UI-shaped graph really can arrive here labelled api (and an
// unrecognised label takes the same non-UI branch).
//
// ⚠ This paragraph used to name handleWorkflowImportPNG as the live source of such
// rows — "stores comfy.FormatAPI WITHOUT calling DetectFormat … PRE-EXISTING and
// deliberately out of scope". That import path was FIXED: it now classifies both
// extracted chunks with comfy.DetectFormat and refuses what neither classifies (see
// workflow_import_png_format_web_test.go). The cases below are unaffected and stay,
// because the gate must hold for a mislabelled row whatever wrote it — every row
// imported by that path before the fix is still in users' databases, and `format`
// remains an unconstrained column.
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
	// 🔴 The two that parse AND are non-empty. These decode to a node map with
	// entries, so `len(nodes) > 0` — the shape comfy.PreflightReport.Nodes counted
	// until this change. Every entry lacks a class_type, so MissingNodes skips them,
	// ExtractResources finds no loader, detectBadOptions finds no schema, and
	// Preflight answers OK:true with all three lists empty. Both used to render
	// "Ready" and be SUBMITTED. Preflight now counts only nodes carrying a usable
	// class_type — the same rule comfy.DetectFormat has always applied.
	{"object of class_type-less entries", store.WorkflowFormatAPI,
		`{"a":{}}`, "contains no nodes this app can read"},
	// The realistic one: a PNG whose prompt chunk holds {"prompt": <graph>} rather
	// than the graph. handleWorkflowImportPNG stores that verbatim as format=api
	// without calling DetectFormat, so the wrapper becomes the ONE node — named
	// "prompt", carrying no class_type — and the real graph inside is never seen.
	{"api graph under a prompt wrapper", store.WorkflowFormatAPI,
		`{"prompt":{"4":{"class_type":"CheckpointLoaderSimple",` +
			`"inputs":{"ckpt_name":"present.safetensors"}}}}`,
		"contains no nodes this app can read"},
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

// TestRunNeverSubmitsAWarnOnlyGraph is an INVARIANT GUARD, not regression coverage —
// labelled explicitly so it is never counted in a regression tally. It PASSES at
// 43ade22: ConvWarned was already in the old run-path gate, so the run has always
// refused a warn-only graph and this test pins that it keeps doing so. Its value is
// mutation DISCRIMINATION, below, not a bug it caught.
//
// It closes a coverage hole this change's own
// mutation sweep found: deleting `g.ConvWarned` from runGate.blocked() turned the
// READINESS suite red and left the RUN suite entirely green, because every existing
// run-side warning fixture ALSO has a missing node type and is therefore blocked
// twice over. A shared predicate only stays shared if both surfaces can discriminate
// it.
//
// readinessWarnInfo installs the node WITHOUT input_order, so the converter warns
// while nothing at all is missing — the only combination that isolates ConvWarned.
func TestRunNeverSubmitsAWarnOnlyGraph(t *testing.T) {
	srv := newLibraryTestServer(t, t.TempDir())
	fake := &fakeComfy{
		info: mustObjectInfo(t, readinessWarnInfo),
		// Programmed history: without it a WRONGLY submitted graph polls /history until
		// pollRunUntilDone's deadline and the failure reads "run did not finish" — a red
		// for the wrong reason. See nodepackFakeComfy.
		history: &comfy.HistoryEntry{
			Status: comfy.HistoryStatus{Completed: true, StatusStr: "success"},
		},
	}
	srv.comfyClientFn = func() comfyClient { return fake }

	id := seedWorkflow(t, srv, store.WorkflowFormatUI, readinessWarnGraph)
	if r := post(t, srv, "/workflows/"+id+"/run", nil, true); r.Code != http.StatusOK {
		t.Fatalf("run start = %d", r.Code)
	}
	body := pollRunUntilDone(t, srv, id)

	if fake.submitCalled {
		t.Fatalf("SUBMITTED a graph whose conversion only WARNED: %s", fake.submittedGraph)
	}
	// INTERMEDIATE STATE: the fixture really did reach the warn-only case. If nothing
	// were missing AND nothing warned, this would be the ready path and "did not
	// submit" would be measuring a run that was blocked for some other reason.
	if !strings.Contains(body, "input_order") {
		t.Errorf("fixture reached the wrong branch — no conversion warning was raised:\n%s", body)
	}
	if strings.Contains(body, "Preflight failed") {
		t.Errorf("fixture reached the wrong branch — something was MISSING, so ConvWarned "+
			"is not the condition under test:\n%s", body)
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
