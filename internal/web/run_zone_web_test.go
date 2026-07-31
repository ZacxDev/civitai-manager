package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/comfy"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// hxIncludeValues returns every hx-include attribute VALUE in a rendered fragment,
// in document order. Tests use it instead of a raw Contains over the whole body
// because a selector like "#run-modes" also appears as that container's own id=,
// so a substring search can pass while no control includes anything.
func hxIncludeValues(body string) []string {
	const key = `hx-include="`
	var out []string
	for i := 0; ; {
		j := strings.Index(body[i:], key)
		if j < 0 {
			return out
		}
		start := i + j + len(key)
		end := strings.Index(body[start:], `"`)
		if end < 0 {
			return out
		}
		out = append(out, body[start:start+end])
		i = start + end
	}
}

// splitSelectors splits an hx-include value into its comma-separated selectors.
func splitSelectors(inc string) []string {
	parts := strings.Split(inc, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

// ── the consolidation itself ─────────────────────────────────────────────────

// TestOnlyOneControlStartsALocalRun is THE consolidation guard.
//
// The page used to carry two run-starting buttons with DIFFERENT behaviour: a big
// "Generate" that hx-included only #run-modes (so it discarded every parameter
// edit) and a smaller "Run with these parameters" submit that did not. Whichever a
// user pressed decided whether their typing counted.
//
// The assertion is on the rendered PAGE, and it counts the controls that post to a
// run-STARTING endpoint. It deliberately covers the reachability fragment too (by
// rendering it separately), because that is where the surviving primary lives.
func TestOnlyOneControlStartsALocalRun(t *testing.T) {
	srv := newTestServer(t)
	id := seedWorkflow(t, srv, store.WorkflowFormatUI, queueSeedGraph)
	page := get(t, srv, "/workflows/"+id).Body.String()

	// The lazily-loaded fragment carries the ONE primary. Splice it in where the
	// browser would put it, so the count below is what a real page ends up with.
	frag := renderString(t, runComfyStatusFragment(1, srv.csrf,
		comfyStatusView{configured: true, reachable: true, canQueue: true}))
	full := page + frag

	starters := 0
	for _, ep := range []string{"/run/queue", "/run-with-params"} {
		starters += strings.Count(full, `hx-post="/workflows/1`+ep+`"`)
	}
	// The preset FORM's own hx-post to /run-with-params is one of them: it is the
	// Enter-key path, not a button, and it does exactly what a count of 1 does.
	// One button + one form = 2. A third would be a rival primary.
	if starters != 2 {
		t.Errorf("want exactly 2 run-starting controls (the primary button + the form's "+
			"Enter-key submit), got %d:\n%s", starters, full)
	}
	if strings.Contains(full, "Run with these parameters") {
		t.Error("the rival 'Run with these parameters' submit is back")
	}
	if strings.Contains(full, "Queue ×N") {
		t.Error("the separate 'Queue ×N' block is back")
	}
}

// TestPrimaryRunControlCarriesTheParametersAndTheCount pins the exact defect the
// consolidation fixed: the prominent button must submit the panel's fields AND the
// count segment, not just the mode picker.
func TestPrimaryRunControlCarriesTheParametersAndTheCount(t *testing.T) {
	body := renderString(t, runComfyStatusFragment(5, "tok",
		comfyStatusView{configured: true, reachable: true, canQueue: true}))
	incs := hxIncludeValues(body)
	if len(incs) != 1 {
		t.Fatalf("want exactly one hx-include on the primary control, got %d: %v", len(incs), incs)
	}
	sel := splitSelectors(incs[0])
	for _, want := range []string{
		"#" + runPresetFormID, // the parameters the user typed
		runModesInclude,       // the multi-mode pick
		"#" + runCountGroupID, // the count segment
	} {
		if !slices.Contains(sel, want) {
			t.Errorf("the primary run control does not include %q (hx-include=%q)", want, incs[0])
		}
	}
}

// TestRunZoneSitsBetweenParamsAndStatus replaces
// TestQueueControlRendersOutsideRunStatus. The structural property is unchanged and
// is what keeps the 1 s poller from clobbering a half-typed prompt: the run zone is
// nested in NEITHER #run-params nor #run-status.
func TestRunZoneSitsBetweenParamsAndStatus(t *testing.T) {
	srv := newTestServer(t)
	id := seedWorkflow(t, srv, store.WorkflowFormatUI, queueSeedGraph)
	body := get(t, srv, "/workflows/"+id).Body.String()

	params := strings.Index(body, `id="run-params"`)
	zone := strings.Index(body, `id="`+runZoneID+`"`)
	status := strings.Index(body, `id="`+runStatusContainerID+`"`)
	if params < 0 || zone < 0 || status < 0 {
		t.Fatalf("containers missing: params=%d zone=%d status=%d", params, zone, status)
	}
	if !(params < zone && zone < status) {
		t.Errorf("the run zone must sit AFTER #run-params and BEFORE #run-status "+
			"(params=%d zone=%d status=%d)", params, zone, status)
	}
	// Every quick pick the old block offered is still one click away, now as a
	// value on the one control rather than a button of its own.
	for _, n := range batchQuickPicks {
		if !strings.Contains(body, `id="cm-count-`+strconv.Itoa(n)+`"`) {
			t.Errorf("quick pick ×%d missing from the count segment", n)
		}
	}
	if !strings.Contains(body, `max="`+strconv.Itoa(maxBatchCount)+`"`) {
		t.Error("the custom count input does not carry the cap")
	}
	// `1` must be a real, SELECTED option — the old block's only way to say "once"
	// was to clear a number field, which is how it shipped a control labelled
	// "Queue ×N" that quietly did a single run.
	if !strings.Contains(body, `id="cm-count-1" type="radio" name="count" value="1" checked`) {
		t.Errorf("the count segment must default to a checked `1`:\n%s", body)
	}
}

// TestCountSegmentDefaultsAreNotSilentlyOne is the surviving half of
// TestQueueCountInputCarriesABatchDefault: the CUSTOM field still pre-fills a real
// batch count, so picking "Custom" and pressing Generate cannot quietly mean 1.
func TestCountSegmentDefaultsAreNotSilentlyOne(t *testing.T) {
	if defaultBatchCount < 2 {
		t.Fatalf("defaultBatchCount = %d — picking Custom would run once", defaultBatchCount)
	}
	srv := newTestServer(t)
	id := seedWorkflow(t, srv, store.WorkflowFormatUI, queueSeedGraph)
	body := get(t, srv, "/workflows/"+id).Body.String()

	i := strings.Index(body, `id="cm-count-custom-input"`)
	if i < 0 {
		t.Fatalf("the custom count input is not rendered:\n%s", body)
	}
	end := i + 300
	if end > len(body) {
		end = len(body)
	}
	input := body[i:end]
	if !strings.Contains(input, `value="`+strconv.Itoa(defaultBatchCount)+`"`) {
		t.Errorf("the custom count input has no batch default: %s", input)
	}
	if strings.Contains(input, `value=""`) {
		t.Errorf("the custom count input is empty: %s", input)
	}
}

// ── the two-field count, end to end ──────────────────────────────────────────

// TestCustomCountReachesTheBatch is the guard on batchCountFromForm. The segment
// posts count=custom + count_custom=<n>, and a server that read only `count` would
// see an unparseable value and clamp it to ONE — a user asking for 12 runs getting
// one, silently.
func TestCustomCountReachesTheBatch(t *testing.T) {
	for _, tc := range []struct {
		name string
		form url.Values
		want int
	}{
		{"custom sentinel", url.Values{batchCountField: {batchCountCustom}, batchCountCustomField: {"12"}}, 12},
		{"plain numeric count still works", url.Values{batchCountField: {"4"}}, 4},
		// The segment always submits count_custom (it is only display-hidden), so a
		// numeric pick must WIN over whatever is sitting in that field.
		{"numeric pick beats a stale custom field", url.Values{batchCountField: {"2"}, batchCountCustomField: {"16"}}, 2},
		{"custom over the cap is clamped", url.Values{batchCountField: {batchCountCustom}, batchCountCustomField: {"9999"}}, maxBatchCount},
		{"custom left blank is one run", url.Values{batchCountField: {batchCountCustom}, batchCountCustomField: {""}}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := newTestServer(t)
			id := seedWorkflow(t, srv, store.WorkflowFormatUI, queueSeedGraph)
			release := make(chan struct{})
			var calls int32
			srv.runFn = blockingRunFn(&calls, release)

			form := url.Values{"csrf_token": {srv.csrf}}
			for k, v := range tc.form {
				form[k] = v
			}
			if rec := postQueue(t, srv, id, form); rec.Code != http.StatusOK {
				t.Fatalf("status = %d", rec.Code)
			}
			if got := srv.runJobState().BatchTotal; got != tc.want {
				t.Errorf("BatchTotal = %d, want %d", got, tc.want)
			}
			close(release)
			waitBatchDone(t, srv)
		})
	}
}

// ── "Run once" must run the seed you can SEE ─────────────────────────────────

// TestSingleRunKeepsTheVisibleSeed is the mutation guard on the count<=1 branch in
// handleWorkflowRunQueue.
//
// This endpoint is now the ONE primary run control's endpoint, so the ordinary
// "Generate" click lands here with count==1. It used to pass the graph's seed keys
// to startBatch unconditionally — correct while it was only reachable from a "Queue
// ×N" block, and wrong now: every single run would silently overwrite the seed the
// user can read in the Parameters field, making that field a lie and making a
// deliberate re-run of a specific seed impossible.
//
// The fixture REACHES the interesting case, and the test asserts that: queueSeedGraph
// exposes exactly one seed, so seedKeysSeen below is non-empty for count==2. Without
// that check a graph with no seed at all would make this pass vacuously.
func TestSingleRunKeepsTheVisibleSeed(t *testing.T) {
	const visibleSeed = "424242"

	run := func(t *testing.T, count string) (seedValue string, sawSeedKeys bool) {
		t.Helper()
		srv := newTestServer(t)
		id := seedWorkflow(t, srv, store.WorkflowFormatUI, queueSeedGraph)

		// The seed lives at node 3, widgets_values[0] in queueSeedGraph.
		key := comfy.UIWidgetKey{NodeID: "3", Widget: 0}
		var got atomic.Value
		srv.runFn = func(_ context.Context, _ *store.Workflow, _ runUpdater, opts runOptions) (*runResult, error) {
			got.Store(opts.UIWidgetOverrides[key])
			return &runResult{PromptID: "p"}, nil
		}
		form := url.Values{
			"csrf_token":    {srv.csrf},
			batchCountField: {count},
			// The Parameters panel's own field for that seed.
			"wp_node":   {"3"},
			"wp_widget": {"0"},
			"wp_value":  {visibleSeed},
		}
		if rec := postQueue(t, srv, id, form); rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
		waitBatchDone(t, srv)
		v, _ := got.Load().(string)
		return v, len(comfy.SeedWidgetKeys(json.RawMessage(queueSeedGraph))) > 0
	}

	// The interesting case is REACHED: this graph really does expose a seed, so a
	// count>1 really does re-roll it.
	batchSeed, hadKeys := run(t, "2")
	if !hadKeys {
		t.Fatal("fixture exposes no seed at all — this test could not observe a re-roll")
	}
	if batchSeed == visibleSeed {
		t.Errorf("a batch must re-roll the seed, but item 1 ran the typed value %q", batchSeed)
	}

	// …and a count of ONE must NOT.
	single, _ := run(t, "1")
	if single != visibleSeed {
		t.Errorf("a single run overwrote the seed the user can see: ran %q, want %q",
			single, visibleSeed)
	}
}

// TestSingleRunThroughTheQueueEndpointSaysNothing replaces
// TestQueueWithoutACountSaysItRanOnce. That test pinned a "Started a single run —
// nothing was queued" scold, which was right when the only way here was a block
// labelled "Queue ×N". `1` is now a visible, selectable option on the one run
// control, so the response must be indistinguishable from an ordinary run's — and a
// batch of 2 must still announce itself.
func TestSingleRunThroughTheQueueEndpointSaysNothing(t *testing.T) {
	srv := newTestServer(t)
	id := seedWorkflow(t, srv, store.WorkflowFormatUI, queueSeedGraph)
	release := make(chan struct{})
	var calls int32
	srv.runFn = blockingRunFn(&calls, release)

	body := postQueue(t, srv, id, url.Values{"csrf_token": {srv.csrf}}).Body.String()
	if strings.Contains(body, "nothing was queued") || strings.Contains(body, "Queued ") {
		t.Errorf("a count of 1 must render no batch notice at all:\n%s", body)
	}
	if snap := srv.runJobState(); snap.BatchTotal != 1 {
		t.Errorf("BatchTotal = %d, want 1", snap.BatchTotal)
	}
	close(release)
	waitBatchDone(t, srv)
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Errorf("runFn calls = %d, want 1", n)
	}

	// The other half of the property: a real batch is still announced, so the
	// silence above is about the count, not about the notice being gone.
	srv2 := newTestServer(t)
	id2 := seedWorkflow(t, srv2, store.WorkflowFormatUI, queueSeedGraph)
	rel2 := make(chan struct{})
	var calls2 int32
	srv2.runFn = blockingRunFn(&calls2, rel2)
	body2 := postQueue(t, srv2, id2, url.Values{
		"csrf_token": {srv2.csrf}, batchCountField: {"4"},
	}).Body.String()
	if !strings.Contains(body2, "Queued 4 runs") {
		t.Errorf("a real batch must still say so:\n%s", body2)
	}
	close(rel2)
	waitBatchDone(t, srv2)
}

// ── keyboard + a11y of the segment ───────────────────────────────────────────

// TestCountSegmentStaysAKeyboardRadiogroup pins the reason this is radios rather
// than five buttons: the browser supplies the group semantics and arrow-key
// navigation. A `display:none` / `[hidden]` radio would take both away silently —
// the page would still LOOK identical.
func TestCountSegmentStaysAKeyboardRadiogroup(t *testing.T) {
	body := renderString(t, runCountSegment())
	if !strings.Contains(body, `role="radiogroup"`) || !strings.Contains(body, `aria-label="How many runs"`) {
		t.Errorf("the count segment is not an announced radiogroup:\n%s", body)
	}
	if strings.Contains(body, `hidden`) {
		t.Errorf("a hidden radio drops out of the tab order and the a11y tree:\n%s", body)
	}
	// Every pill is a <label for=> of a real radio, so a click and a keypress take
	// the same path.
	for _, v := range []string{"1", "2", "4", "8", "16", batchCountCustom} {
		if !strings.Contains(body, `for="`+runCountRadioID(v)+`"`) {
			t.Errorf("count option %q has no label bound to its radio:\n%s", v, body)
		}
	}
	// The clipping rule is what keeps the radio focusable. If the class ever loses
	// its rule the radios become full-size native dots — visible, but the guard here
	// is that the class is REFERENCED, and class_coverage_web_test.go proves it has
	// a rule.
	if !strings.Contains(body, `class="cm-count-radio"`) {
		t.Errorf("the radios lost their clipping class:\n%s", body)
	}
}

// TestRunZoneIsNotBehindADisclosure keeps the v0.1.96 property that the run control
// is visible on load. It used to be guaranteed by the Parameters panel not being a
// <details>; the control has since moved OUT of that panel, so the property needs
// its own guard at its new home.
func TestRunZoneIsNotBehindADisclosure(t *testing.T) {
	srv := newTestServer(t)
	id := seedWorkflow(t, srv, store.WorkflowFormatUI, queueSeedGraph)
	body := get(t, srv, "/workflows/"+id).Body.String()

	zone := strings.Index(body, `id="`+runZoneID+`"`)
	if zone < 0 {
		t.Fatalf("no run zone:\n%s", body)
	}
	// Count <details> opens and closes before the zone: if any is still open at the
	// zone's position, the zone is inside a disclosure.
	head := body[:zone]
	if opens, closes := strings.Count(head, "<details"), strings.Count(head, "</details>"); opens != closes {
		t.Errorf("the run zone is inside a <details> (%d open, %d closed before it)", opens, closes)
	}
}

// TestRunZoneOffersNoCountForAnAPIGraph pins that the count segment is absent
// exactly where the batch endpoint would 404 — an API-format workflow — and that the
// primary control routes to the endpoint that accepts one instead.
func TestRunZoneOffersNoCountForAnAPIGraph(t *testing.T) {
	srv := newTestServer(t)
	id := seedWorkflow(t, srv, store.WorkflowFormatAPI, `{"3":{"class_type":"X","inputs":{}}}`)
	body := get(t, srv, "/workflows/"+id).Body.String()

	if strings.Contains(body, `id="`+runCountGroupID+`"`) {
		t.Errorf("an API-format workflow must not offer a count — /run/queue 404s for it:\n%s", body)
	}
	if !strings.Contains(body, `id="`+runZoneID+`"`) {
		t.Errorf("the run zone itself must still render:\n%s", body)
	}
	// And the endpoint the primary posts to is the one that accepts an API graph.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/workflows/"+id+"/run/comfy-status", nil)
	srv.Handler().ServeHTTP(rec, req)
	if strings.Contains(rec.Body.String(), "/run/queue") {
		t.Errorf("the primary control must not post an API graph to the batch endpoint:\n%s",
			rec.Body.String())
	}
}
