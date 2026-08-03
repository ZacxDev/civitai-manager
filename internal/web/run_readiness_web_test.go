package web

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/store"
)

// ─────────────────────────────────────────────────────────────────────────────
// THE PRE-CLICK READINESS LINE (run_readiness.go).
//
// Every test here drives the REAL handler over the REAL route, so the route
// registration, the loopback gate, the 0019 cache read, the conversion, the
// preflight and the rendering are all in frame. Nothing calls workflowReadiness
// directly.
//
// 🔴 ASSERTIONS ARE ON data-readiness / data-reason, NOT ON PROSE. A marker that a
// failure headline can contain is how a non-vacuity check in this package became
// vacuous itself ("ComfyUI reachable" is a substring of "No ComfyUI reachable at
// …"). The state attributes exist for this.
//
// 🔴 EVERY FIXTURE'S COUNTS ARE PAIRWISE DISTINCT — 1 node type, 3 model files, 2
// bad options. With any two equal, swapping two counters at the render site
// produces byte-identical output and the whole file passes with the bug in.
// ─────────────────────────────────────────────────────────────────────────────

// readinessInfo is the cached /object_info. It installs exactly two classes:
//
//   - CheckpointLoaderSimple, a LOADER (ExtractResources only looks at loader
//     classes), whose ckpt_name combo lists ONE file. Anything else a loader
//     references is a missing MODEL.
//   - KSampler, a non-loader with two string combos. A drifted value on one of
//     these is a BAD OPTION, not a missing model — ExtractResources ignores it
//     because "euler" carries no model extension and KSampler is not a loader.
//
// input_order is load-bearing on every entry: without it the UI→API converter
// warns per node, which would send the UI fixtures below down the convert-warned
// branch instead of the one they are written to exercise.
const readinessInfo = `{
  "CheckpointLoaderSimple": {"python_module":"nodes",
    "input":{"required":{"ckpt_name":[["comfy_has_this.safetensors"],{}]}},
    "input_order":{"required":["ckpt_name"]}},
  "KSampler": {"python_module":"nodes",
    "input":{"required":{
      "sampler_name":[["euler","dpmpp_2m"],{}],
      "scheduler":[["normal","karras"],{}]}},
    "input_order":{"required":["sampler_name","scheduler"]}}
}`

// readinessAPIGraph is an api-format graph that needs 1 node type, 3 model files
// and carries 2 drifted option values.
//
//	node 1..3 — three DIFFERENT missing checkpoints. None is a substring of another
//	            and none matches the one installed choice.
//	node 4    — a class absent from readinessInfo ⇒ 1 missing node type.
//	node 5    — an INSTALLED KSampler whose two combo values are off-list ⇒ 2 bad
//	            options. Its node type is present, so it must not inflate the node
//	            count; that separation is the point of putting both on one fixture.
const readinessAPIGraph = `{
  "1":{"class_type":"CheckpointLoaderSimple","inputs":{"ckpt_name":"alpha_one.safetensors"}},
  "2":{"class_type":"CheckpointLoaderSimple","inputs":{"ckpt_name":"beta_two.safetensors"}},
  "3":{"class_type":"CheckpointLoaderSimple","inputs":{"ckpt_name":"gamma_three.safetensors"}},
  "4":{"class_type":"TotallyAbsentNodeClass","inputs":{}},
  "5":{"class_type":"KSampler","inputs":{"sampler_name":"drifted_sampler","scheduler":"drifted_scheduler"}}
}`

// readinessReadyGraph references only what readinessInfo installs, with on-list
// option values. Nothing is missing and the conversion is not involved (api format).
const readinessReadyGraph = `{
  "1":{"class_type":"CheckpointLoaderSimple","inputs":{"ckpt_name":"comfy_has_this.safetensors"}},
  "2":{"class_type":"KSampler","inputs":{"sampler_name":"euler","scheduler":"normal"}}
}`

// readinessUIGraph is a UI-format graph — the format of ALL 71 workflows on the
// operator's real database, and the one a fixture silently fails to cover.
//
// Node 2's class is absent from readinessInfo, so the CONVERTER cuts it out. That is
// the graph-incomplete case: preflight can no longer see the class at all (the node
// is gone from the document it validates), so the count comes from the converter's
// MissingNodeTypes and the model count becomes a LOWER bound.
const readinessUIGraph = `{"nodes":[
  {"id":1,"type":"CheckpointLoaderSimple","widgets_values":["delta_four.safetensors"]},
  {"id":2,"type":"TotallyAbsentNodeClass","widgets_values":[]}
],"links":[]}`

// readinessUIReadyGraph is a UI-format graph with nothing missing — the fixture that
// proves the UI path can reach "ready" at all, so a UI "needs" result is a real
// finding rather than a path that always fails.
const readinessUIReadyGraph = `{"nodes":[
  {"id":1,"type":"CheckpointLoaderSimple","widgets_values":["comfy_has_this.safetensors"]}
],"links":[]}`

// newReadinessServer builds a server on a PRIVATE file database.
//
// 🔴 NOT ":memory:". store.Open maps that to a process-global cache=shared handle,
// and these tests write the comfy_model_cache SINGLETON row — one shared row that
// every concurrently-live server in the package would see. One of them also DROPs
// the table. A tempdir file keeps the blast radius inside the test.
func newReadinessServer(t *testing.T) *Server {
	t.Helper()
	root := t.TempDir()
	return newLibraryTestServerDB(t, root, filepath.Join(t.TempDir(), "readiness.db"))
}

// seedObjectInfo writes the 0019 cache row — the ONLY node-schema source this
// feature is allowed to read.
func seedObjectInfo(t *testing.T, srv *Server, body string) {
	t.Helper()
	if err := srv.store.PutComfyObjectInfo([]byte(body)); err != nil {
		t.Fatalf("seed object_info: %v", err)
	}
}

// readiness fetches the fragment over the real route and returns its body.
func readiness(t *testing.T, srv *Server, id string) string {
	t.Helper()
	rec := get(t, srv, "/workflows/"+id+"/run/readiness")
	if rec.Code != http.StatusOK {
		t.Fatalf("readiness = %d, want 200", rec.Code)
	}
	return rec.Body.String()
}

// wantState fails unless the fragment carries exactly this data-readiness value,
// and (for unknown) this data-reason. Both are checked as the full attribute so a
// value cannot match as a substring of a neighbour.
func wantState(t *testing.T, body, state, reason string) {
	t.Helper()
	if !strings.Contains(body, `data-readiness="`+state+`"`) {
		t.Fatalf("want data-readiness=%q, got:\n%s", state, body)
	}
	for _, other := range []string{"ready", "needs", "unknown"} {
		if other == state {
			continue
		}
		if strings.Contains(body, `data-readiness="`+other+`"`) {
			t.Fatalf("fragment carries a SECOND state %q beside %q:\n%s", other, state, body)
		}
	}
	if reason == "" {
		if strings.Contains(body, `data-reason=`) {
			t.Fatalf("a %s answer must carry no data-reason:\n%s", state, body)
		}
		return
	}
	if !strings.Contains(body, `data-reason="`+reason+`"`) {
		t.Fatalf("want data-reason=%q, got:\n%s", reason, body)
	}
}

// ── THE WARM PATH ────────────────────────────────────────────────────────────

// TestReadinessCountsWhatIsMissing is the load-bearing warm-cache test: with a
// populated 0019 row it must report 1 node type, 3 model files and 2 drifted
// options, and it must say so in the "needs" state.
//
// 🔴 THIS IS THE TEST THE cache-is-ignored MUTATION MUST KILL. If
// GetComfyObjectInfo stops answering, the feature falls to the cold-cache branch
// and every count below vanishes — which is exactly how the last cache feature in
// this repo shipped inert while its tests stayed green.
func TestReadinessCountsWhatIsMissing(t *testing.T) {
	srv := newReadinessServer(t)
	seedObjectInfo(t, srv, readinessInfo)
	id := seedWorkflow(t, srv, store.WorkflowFormatAPI, readinessAPIGraph)

	body := readiness(t, srv, id)
	wantState(t, body, "needs", "")
	for _, want := range []string{
		"1 node type",
		"3 model files",
		"2 saved option values",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("readiness line is missing %q:\n%s", want, body)
		}
	}
	// The api-format path cuts nothing, so the count is EXACT and the copy must not
	// hedge — "at least" here would train the reader to discount a number that is
	// actually right.
	if strings.Contains(body, "at least") {
		t.Errorf("a complete graph must not hedge its counts:\n%s", body)
	}
}

// TestReadinessIsReadyWhenNothingIsMissing pins the positive control. Without it a
// "needs" assertion proves nothing — a counter wired to nothing also never says
// ready.
func TestReadinessIsReadyWhenNothingIsMissing(t *testing.T) {
	srv := newReadinessServer(t)
	seedObjectInfo(t, srv, readinessInfo)
	id := seedWorkflow(t, srv, store.WorkflowFormatAPI, readinessReadyGraph)

	wantState(t, readiness(t, srv, id), "ready", "")
}

// TestReadinessReadsTheLocalLibrary proves localHaveFile is actually consulted: the
// SAME graph that reports 3 missing files reports 2 once one of them is in the
// library. A file ComfyUI does not list but the library holds is satisfied — that is
// the whole reason Preflight takes a localHave predicate.
func TestReadinessReadsTheLocalLibrary(t *testing.T) {
	srv := newReadinessServer(t)
	seedObjectInfo(t, srv, readinessInfo)
	id := seedWorkflow(t, srv, store.WorkflowFormatAPI, readinessAPIGraph)

	// Precondition: without the file the count is 3. Asserting the intermediate
	// state is what stops this passing for the wrong reason.
	if before := readiness(t, srv, id); !strings.Contains(before, "3 model files") {
		t.Fatalf("precondition: want 3 missing files before seeding, got:\n%s", before)
	}
	if err := srv.store.UpsertLocalFile(store.LocalFile{
		Path: filepath.Join(t.TempDir(), "beta_two.safetensors"), SHA256: "abc",
	}); err != nil {
		t.Fatalf("seed local file: %v", err)
	}
	after := readiness(t, srv, id)
	wantState(t, after, "needs", "")
	if !strings.Contains(after, "2 model files") {
		t.Errorf("a library file must satisfy its reference; want 2 missing, got:\n%s", after)
	}
}

// ── THE UI-FORMAT PATH ───────────────────────────────────────────────────────

// TestReadinessUIFormatReportsTheDroppedNodeAndHedges covers the format ALL 71 real
// workflows use. The converter CUTS node 2 (its class is not installed), so:
//
//   - the missing node type still has to be counted (from ConversionResult's
//     MissingNodeTypes — preflight cannot see it, the node is gone), and
//   - the copy must hedge, because the cut node's own model references went with it.
func TestReadinessUIFormatReportsTheDroppedNodeAndHedges(t *testing.T) {
	srv := newReadinessServer(t)
	seedObjectInfo(t, srv, readinessInfo)
	id := seedWorkflow(t, srv, store.WorkflowFormatUI, readinessUIGraph)

	body := readiness(t, srv, id)
	wantState(t, body, "needs", "")
	if !strings.Contains(body, "1 node type") {
		t.Errorf("a node the CONVERTER cut must still be counted:\n%s", body)
	}
	if !strings.Contains(body, "at least") {
		t.Errorf("an incomplete graph must hedge its counts:\n%s", body)
	}
	if !strings.Contains(body, "dropped from the graph") {
		t.Errorf("the hedge must NAME why the count may be low:\n%s", body)
	}
}

// TestReadinessUIFormatCanReachReady is the UI path's positive control. Without it,
// a green UI "needs" test is equally consistent with a UI path that can only ever
// fail — which is precisely the shape that certified the wrong branch for four
// releases here.
func TestReadinessUIFormatCanReachReady(t *testing.T) {
	srv := newReadinessServer(t)
	seedObjectInfo(t, srv, readinessInfo)
	id := seedWorkflow(t, srv, store.WorkflowFormatUI, readinessUIReadyGraph)

	wantState(t, readiness(t, srv, id), "ready", "")
}

// ── THE FAIL DIRECTION ───────────────────────────────────────────────────────

// TestReadinessColdCacheIsUnknownNotReady is the fail-direction test. A fresh
// install has never run a workflow, so there is no 0019 row — and the graph below
// references a file NOTHING has. Answering "ready" there would be the maturity
// control's fail-OPEN bug in a new place.
//
// 🔴 THIS IS THE TEST THE fail-open MUTATION MUST KILL.
func TestReadinessColdCacheIsUnknownNotReady(t *testing.T) {
	srv := newReadinessServer(t) // no seedObjectInfo: the cache is cold
	id := seedWorkflow(t, srv, store.WorkflowFormatAPI, readinessAPIGraph)

	body := readiness(t, srv, id)
	wantState(t, body, "unknown", "cold")
	// The reason must be NAMED. "Could not check" with no cause is indistinguishable
	// from a bug in this code, and gives the reader nothing to act on.
	if !strings.Contains(body, "Run any workflow once") {
		t.Errorf("the cold-cache answer must say how to warm it:\n%s", body)
	}
}

// TestReadinessUndecodableBlobIsUnknown covers the other silent fail-direction
// case: a row exists but is not JSON.
func TestReadinessUndecodableBlobIsUnknown(t *testing.T) {
	srv := newReadinessServer(t)
	seedObjectInfo(t, srv, `{"CheckpointLoaderSimple": THIS IS NOT JSON`)
	id := seedWorkflow(t, srv, store.WorkflowFormatAPI, readinessReadyGraph)

	// readinessReadyGraph is the graph that renders READY on a good cache — so this
	// asserts the blob, not the graph, is what moves the answer.
	wantState(t, readiness(t, srv, id), "unknown", "schema")
}

// TestReadinessEmptySchemaIsUnknownNotEverythingMissing is the fail-direction case
// in the OTHER direction, and it is the one that looks harmless.
//
// A well-formed but EMPTY `{}` payload is not "nothing is installed" — it is a row
// we cannot trust. Treated as data it makes EVERY class in the graph look missing,
// so the line would render a confidently enormous and completely wrong count on a
// workflow that is perfectly fine.
func TestReadinessEmptySchemaIsUnknownNotEverythingMissing(t *testing.T) {
	srv := newReadinessServer(t)
	seedObjectInfo(t, srv, `{}`)
	id := seedWorkflow(t, srv, store.WorkflowFormatAPI, readinessReadyGraph)

	body := readiness(t, srv, id)
	wantState(t, body, "unknown", "schema")
	// Belt and braces: the failure this guards against renders counts.
	if strings.Contains(body, "node type") || strings.Contains(body, "model file") {
		t.Errorf("an empty schema must not be read as 'everything is missing':\n%s", body)
	}
}

// TestReadinessUnreadableCacheIsUnknown breaks the TABLE, which is the real
// production scenario (a read-only database, a failed write) rather than a
// hand-made bad value.
func TestReadinessUnreadableCacheIsUnknown(t *testing.T) {
	srv := newReadinessServer(t) // private file DB — the DROP must not reach a sibling
	seedObjectInfo(t, srv, readinessInfo)
	id := seedWorkflow(t, srv, store.WorkflowFormatAPI, readinessReadyGraph)

	// Precondition: it answers READY while the table is intact, so the DROP is
	// provably what moves the answer.
	wantState(t, readiness(t, srv, id), "ready", "")

	if _, err := srv.store.DB().Exec(`DROP TABLE comfy_model_cache`); err != nil {
		t.Fatalf("drop cache table: %v", err)
	}
	wantState(t, readiness(t, srv, id), "unknown", "schema")
}

// TestReadinessMissingWorkflowIsUnknown pins that a workflow deleted since the page
// rendered degrades to the advisory unknown state rather than 404-ing a fragment
// whose whole job is to be reassuring or not.
func TestReadinessMissingWorkflowIsUnknown(t *testing.T) {
	srv := newReadinessServer(t)
	seedObjectInfo(t, srv, readinessInfo)

	wantState(t, readiness(t, srv, "999999"), "unknown", "cold")
}

// ── WIRING ───────────────────────────────────────────────────────────────────

// TestReadinessContainerIsLazyAndStable pins the fragment plumbing on the real
// workflow page: a STABLE container that lazily loads the route and swaps its
// INNER html. An outerHTML swap would replace the polling/target node itself, which
// is the repo's streaming-fragment invariant.
func TestReadinessContainerIsLazyAndStable(t *testing.T) {
	srv := newReadinessServer(t)
	srv.comfyClientFn = func() comfyClient { return &fakeComfy{} }
	id := seedWorkflow(t, srv, store.WorkflowFormatUI, readinessUIReadyGraph)

	page := get(t, srv, "/workflows/"+id).Body.String()
	// The BRACE-BALANCED extent of the container, not a whole-page substring search:
	// an index comparison cannot tell "the attribute is on this element" from "the
	// attribute is on something else nearby".
	start, end := divExtent(t, page, `id="`+runReadinessID+`"`)
	ext := page[start:end]
	for _, want := range []string{
		`hx-get="/workflows/` + id + `/run/readiness"`,
		`hx-trigger="load"`,
		`hx-swap="innerHTML"`,
	} {
		if !strings.Contains(ext, want) {
			t.Errorf("#%s is missing %s:\n%s", runReadinessID, want, ext)
		}
	}
	// Rendering the answer inline would put a UI→API conversion and a multi-megabyte
	// object_info decode on the critical path of every workflow page.
	if strings.Contains(ext, "data-readiness=") {
		t.Errorf("#%s must load LAZILY, not carry a pre-computed answer:\n%s", runReadinessID, ext)
	}
}

// TestReadinessIsLoopbackGated pins that the fragment is gated exactly like the
// run zone that hosts it — generateSection renders nothing but a note off-loopback,
// so an ungated readiness line would describe a surface the caller cannot use.
func TestReadinessIsLoopbackGated(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "gated.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	srv := NewServer(st, stubReader{}, stubSubscriber{}, Config{
		BaseURL: "https://civitai.com", Addr: "0.0.0.0:8787",
	}, nil)
	seedObjectInfo(t, srv, readinessInfo)
	id := seedWorkflow(t, srv, store.WorkflowFormatAPI, readinessReadyGraph)

	body := get(t, srv, "/workflows/"+id+"/run/readiness").Body.String()
	if !strings.Contains(body, "non-loopback") {
		t.Errorf("readiness should be loopback-gated:\n%s", body)
	}
	if strings.Contains(body, "data-readiness=") {
		t.Errorf("a gated response must not leak an answer:\n%s", body)
	}
}
