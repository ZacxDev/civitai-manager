package web

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/comfy"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// uiTxt2imgGraph is a UI-format graph with a titled positive CLIPTextEncode, a
// KSampler (seed carries a control_after_generate slot), and an EmptyLatentImage —
// enough to exercise the Parameters detection + panel.
const uiTxt2imgGraph = `{
  "nodes": [
    {"id": 6, "type": "CLIPTextEncode", "title": "Positive",
     "widgets_values": ["a scenic mountain"],
     "inputs": [{"name": "clip", "type": "CLIP", "link": 3}]},
    {"id": 3, "type": "KSampler",
     "widgets_values": [156680208700286, "randomize", 20, 8.0, "euler", "normal", 1.0],
     "inputs": [{"name": "model", "type": "MODEL", "link": 1}]},
    {"id": 5, "type": "EmptyLatentImage", "widgets_values": [1024, 768, 2], "inputs": []}
  ],
  "links": []
}`

// TestRunParametersPanelRendersPrefilled proves the panel renders the curated,
// pre-filled fields, the randomize/reset controls, the CSRF token, the parallel
// wp_node/wp_widget/wp_value fields, and the run-with-params endpoint.
func TestRunParametersPanelRendersPrefilled(t *testing.T) {
	wf := &store.Workflow{ID: 7, Name: "t2i", Format: store.WorkflowFormatUI, Graph: uiTxt2imgGraph}
	got := renderString(t, runParametersPanel(wf, "tok"))

	for _, want := range []string{
		"Parameters",
		"a scenic mountain",       // prompt pre-filled
		"Prompt (Positive)",       // titled label disambiguates
		`value="156680208700286"`, // seed pre-filled
		`value="20"`,              // steps
		`value="8.0"`,             // cfg
		"cmRandomSeed(",           // randomize seed control
		`type="reset"`,            // reset restores pre-filled values
		`name="csrf_token"`, `value="tok"`,
		`name="wp_node"`, `name="wp_widget"`, `name="wp_value"`,
		// KSampler's steps sits at widgets_values[2] — index 1 is the seed's
		// control_after_generate slot, which the walk must skip.
		`<input type="hidden" name="wp_node" value="3"><input type="hidden" name="wp_widget" value="2">`,
		"/workflows/7/run-with-params",
		"the saved workflow is unchanged",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("panel missing %q:\n%s", want, got)
		}
	}
}

// sharedPrimitiveUIGraph: one `easy int` drives BOTH KSamplers' steps.
const sharedPrimitiveUIGraph = `{"nodes":[
  {"id":2,"type":"easy int","title":"Steps","widgets_values":[28],"outputs":[{"name":"int"}]},
  {"id":9,"type":"KSampler","title":"Base","widgets_values":[5,"fixed",20,8.0,"euler","normal",1.0],
   "inputs":[{"name":"steps","type":"INT","widget":{"name":"steps"},"link":1}]},
  {"id":10,"type":"KSampler","title":"Refiner","widgets_values":[5,"fixed",20,8.0,"euler","normal",1.0],
   "inputs":[{"name":"steps","type":"INT","widget":{"name":"steps"},"link":2}]}
],"links":[[1,2,0,9,1,"INT"],[2,2,0,10,1,"INT"]]}`

// TestRunParametersPanelRendersOneFieldPerSharedWidget is the F1 render-side
// regression: two consumers of one upstream widget must produce ONE control, labelled
// so the user knows it drives both — not two controls that secretly collapse on submit.
func TestRunParametersPanelRendersOneFieldPerSharedWidget(t *testing.T) {
	wf := &store.Workflow{ID: 7, Format: store.WorkflowFormatUI, Graph: sharedPrimitiveUIGraph}
	got := renderString(t, runParametersPanel(wf, "tok"))

	if n := strings.Count(got, `name="wp_node" value="2"`); n != 1 {
		t.Errorf("shared widget should render exactly ONE field, got %d:\n%s", n, got)
	}
	if !strings.Contains(got, "drives 2 inputs") {
		t.Errorf("the field must say it drives both consumers:\n%s", got)
	}
	if !strings.Contains(got, "from #2 easy int widget 0") {
		t.Errorf("the field must name the holding node AND widget slot:\n%s", got)
	}
}

// TestParseWidgetOverridesRejectsConflictingDuplicateKeys proves a hand-built request
// that posts the same key twice with DIFFERENT values drops that key rather than
// silently letting one value win (the F1 failure mode at the parse layer).
func TestParseWidgetOverridesRejectsConflictingDuplicateKeys(t *testing.T) {
	wf := &store.Workflow{ID: 7, Format: store.WorkflowFormatUI, Graph: sharedPrimitiveUIGraph}

	conflicting := parseWidgetOverridesForModes(url.Values{
		"wp_node":   {"2", "2"},
		"wp_widget": {"0", "0"},
		"wp_value":  {"50", "33"},
	}, wf, nil)
	if _, ok := conflicting[comfy.UIWidgetKey{NodeID: "2", Widget: 0}]; ok {
		t.Errorf("conflicting duplicate keys must be dropped, got %+v", conflicting)
	}

	// Identical repeats are not a conflict — they carry one unambiguous value.
	agreeing := parseWidgetOverridesForModes(url.Values{
		"wp_node":   {"2", "2"},
		"wp_widget": {"0", "0"},
		"wp_value":  {"50", "50"},
	}, wf, nil)
	if agreeing[comfy.UIWidgetKey{NodeID: "2", Widget: 0}] != "50" {
		t.Errorf("agreeing duplicates should apply, got %+v", agreeing)
	}
}

// TestRunParametersPanelAbsentForAPIGraph proves an api-format graph (no widgets)
// yields no panel.
func TestRunParametersPanelAbsentForAPIGraph(t *testing.T) {
	wf := &store.Workflow{ID: 8, Format: store.WorkflowFormatAPI,
		Graph: `{"3":{"class_type":"KSampler","inputs":{"steps":20}}}`}
	if got := runParametersPanel(wf, "tok"); got != nil {
		t.Errorf("api-format graph should have no Parameters panel, got %v", got)
	}
}

// TestRunWithParamsAppliesOverrides drives the endpoint end-to-end through a
// recording runFn: the parsed overrides reach startRunWithMessage, curated keys are kept, and a
// non-curated (link) input is dropped by the allowlist.
func TestRunWithParamsAppliesOverrides(t *testing.T) {
	srv := newLibraryTestServer(t, t.TempDir())
	rr := &runRecorder{}
	srv.runFn = rr.fn()
	id := seedWorkflow(t, srv, store.WorkflowFormatUI, uiTxt2imgGraph)

	// Parallel arrays, index-aligned (as the panel emits them). Node 3 widget 1 is the
	// seed's control_after_generate slot — never surfaced, so the allowlist drops it.
	form := url.Values{
		"wp_node":   {"3", "6", "3"},
		"wp_widget": {"0", "0", "1"},
		"wp_value":  {"999", "edited prompt", "hijack"},
	}
	if rec := post(t, srv, "/workflows/"+id+"/run-with-params", form, true); rec.Code != http.StatusOK {
		t.Fatalf("run-with-params = %d", rec.Code)
	}
	pollRunUntilDone(t, srv, id)

	rr.mu.Lock()
	defer rr.mu.Unlock()
	if len(rr.opts) == 0 {
		t.Fatal("run never started")
	}
	ov := rr.opts[0].UIWidgetOverrides
	if ov[comfy.UIWidgetKey{NodeID: "3", Widget: 0}] != "999" {
		t.Errorf("seed override not passed: %+v", ov)
	}
	if ov[comfy.UIWidgetKey{NodeID: "6", Widget: 0}] != "edited prompt" {
		t.Errorf("prompt override not passed: %+v", ov)
	}
	if _, ok := ov[comfy.UIWidgetKey{NodeID: "3", Widget: 1}]; ok {
		t.Errorf("non-surfaced widget slot must be dropped by the allowlist: %+v", ov)
	}
}

// TestRunWithParamsStoredWorkflowUntouched proves the run overrides never mutate the
// stored workflow row.
func TestRunWithParamsStoredWorkflowUntouched(t *testing.T) {
	srv := newLibraryTestServer(t, t.TempDir())
	srv.runFn = (&runRecorder{}).fn()
	id := seedWorkflow(t, srv, store.WorkflowFormatUI, uiTxt2imgGraph)

	if rec := post(t, srv, "/workflows/"+id+"/run-with-params", url.Values{
		"wp_node": {"3"}, "wp_widget": {"0"}, "wp_value": {"424242"},
	}, true); rec.Code != http.StatusOK {
		t.Fatalf("run-with-params = %d", rec.Code)
	}
	pollRunUntilDone(t, srv, id)

	wfID, _ := strconv.ParseInt(id, 10, 64)
	wf, err := srv.store.GetWorkflow(context.Background(), wfID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if wf.Graph != uiTxt2imgGraph {
		t.Errorf("stored graph was mutated:\n got %s", wf.Graph)
	}
	if strings.Contains(wf.Graph, "424242") {
		t.Error("override value leaked into the stored workflow")
	}
}

// TestRunWithParamsGating asserts CSRF-reject + loopback-gating for the endpoint.
func TestRunWithParamsGating(t *testing.T) {
	srv := newLibraryTestServer(t, t.TempDir())
	id := seedWorkflow(t, srv, store.WorkflowFormatUI, uiTxt2imgGraph)
	form := url.Values{"wp_node": {"3"}, "wp_widget": {"0"}, "wp_value": {"1"}}

	if rec := post(t, srv, "/workflows/"+id+"/run-with-params", form, false); rec.Code != http.StatusForbidden {
		t.Fatalf("without CSRF = %d, want 403", rec.Code)
	}

	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	gated := NewServer(st, stubReader{}, stubSubscriber{}, Config{
		BaseURL: "https://civitai.com", DefaultPollInterval: time.Hour, Addr: "0.0.0.0:8787",
	}, nil)
	gid := seedWorkflow(t, gated, store.WorkflowFormatUI, uiTxt2imgGraph)
	rec := post(t, gated, "/workflows/"+gid+"/run-with-params", form, true)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "non-loopback") {
		t.Errorf("gated run-with-params = %d body=%s", rec.Code, rec.Body.String())
	}
}
