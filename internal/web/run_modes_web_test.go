package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/comfy"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// modeUIGraph is a two-pipeline template in the shape real packs use: an ACTIVE
// rgthree Fast Groups Bypasser with toggleRestriction "max one" over two purple
// groups, every pipeline node bypassed. Each pipeline holds ONE distinguishable
// node type so the submitted api graph proves WHICH mode ran.
const modeUIGraph = `{"nodes":[
  {"id":1,"type":"Fast Groups Bypasser (rgthree)","mode":0,"pos":[-800,-800],"size":[200,80],
   "title":"Workflows","properties":{"matchColors":"purple","matchTitle":"","toggleRestriction":"max one"}},
  {"id":10,"type":"EmptyLatentImage","mode":4,"pos":[50,80],"size":[200,100],"widgets_values":[512,512,1]},
  {"id":20,"type":"EmptySD3LatentImage","mode":4,"pos":[650,80],"size":[200,100],"widgets_values":[768,768,2]}],
 "links":[],
 "groups":[
  {"title":"TEXT2VIDEO","bounding":[0,0,500,400],"color":"#a1309b"},
  {"title":"IMAGE2VIDEO","bounding":[600,0,500,400],"color":"#a1309b"}]}`

// modeObjectInfo covers both pipelines' node types.
const modeObjectInfo = `{
  "EmptyLatentImage":{"input":{"required":{"width":["INT",{}],"height":["INT",{}],"batch_size":["INT",{}]}},"input_order":{"required":["width","height","batch_size"]}},
  "EmptySD3LatentImage":{"input":{"required":{"width":["INT",{}],"height":["INT",{}],"batch_size":["INT",{}]}},"input_order":{"required":["width","height","batch_size"]}}
}`

// plainUIGraph is an ORDINARY single-mode workflow with groups and a bypassed node —
// deliberately similar-looking, deliberately NOT multi-mode (no exclusive toggler).
const plainUIGraph = `{"nodes":[
  {"id":10,"type":"EmptyLatentImage","mode":0,"pos":[50,80],"size":[200,100],"widgets_values":[512,512,1]},
  {"id":20,"type":"EmptySD3LatentImage","mode":4,"pos":[650,80],"size":[200,100],"widgets_values":[768,768,2]}],
 "links":[],
 "groups":[
  {"title":"Sampling","bounding":[0,0,500,400],"color":"#a1309b"},
  {"title":"Optional","bounding":[600,0,500,400],"color":"#a1309b"}]}`

func newModeServer(t *testing.T, graph string) (*Server, *fakeComfy, string) {
	t.Helper()
	srv := newLibraryTestServer(t, t.TempDir())
	fake := &fakeComfy{
		info: mustObjectInfo(t, modeObjectInfo),
		history: &comfy.HistoryEntry{
			Outputs: map[string]comfy.NodeOutput{"10": {Images: []comfy.ImageRef{{Filename: "o.png", Type: "output"}}}},
			Status:  comfy.HistoryStatus{Completed: true, StatusStr: "success"},
		},
	}
	srv.comfyClientFn = func() comfyClient { return fake }
	return srv, fake, seedWorkflow(t, srv, store.WorkflowFormatUI, graph)
}

func modeKeys(t *testing.T, graph string) (selKey string, byTitle map[string]string) {
	t.Helper()
	sels := comfy.DetectModeSelectors(json.RawMessage(graph))
	if len(sels) != 1 {
		t.Fatalf("fixture should expose exactly 1 selector, got %d", len(sels))
	}
	byTitle = map[string]string{}
	for _, m := range sels[0].Modes {
		byTitle[m.Title] = m.Key
	}
	return sels[0].Key, byTitle
}

// TestRunModesPickerRendersOnRealTemplate renders the picker from the REAL reduced
// 581 graph (the same fixture internal/comfy tests) so the labels, the stable
// container and the single form field are pinned against real author data.
func TestRunModesPickerRendersOnRealTemplate(t *testing.T) {
	b, err := os.ReadFile("../comfy/testdata/wf581_modes_multimode.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	wf := &store.Workflow{ID: 42, Format: store.WorkflowFormatUI, Graph: string(b)}
	body := renderString(t, runModesPanel(wf, "csrf-tok"))

	for _, want := range []string{
		`id="run-modes"`,            // the STABLE hx-include container
		`name="mode_key"`,           // ONE field, value carries selector+group
		"Workflow mode",             // the section label
		"Worflows",                  // the toggler's own (untrusted) title, escaped
		"TEXT2VIDEO", "IMAGE2VIDEO", // real group titles as labels
		"FIRST2LASTFRAME",                   // …
		"Choose a mode…",                    // 581 ships every pipeline off → a placeholder
		`hx-get="/workflows/42/run/params"`, // re-render Parameters for the pick
		`hx-target="#run-params"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("picker missing %q:\n%s", want, body)
		}
	}
	// The picker must not be a form of its own — the run controls include it.
	if strings.Contains(body, "<form") {
		t.Errorf("picker must not introduce its own form:\n%s", body)
	}
	// Offline invariant.
	if strings.Contains(body, "<script") || strings.Contains(body, "http://") {
		t.Errorf("picker must not introduce a script or external reference:\n%s", body)
	}
}

// TestRunModesPickerAbsentForOrdinaryWorkflow is the regression guard at the UI
// level: an ordinary workflow renders the stable container and nothing else.
func TestRunModesPickerAbsentForOrdinaryWorkflow(t *testing.T) {
	for _, tc := range []struct{ name, format, graph string }{
		{"ui single-mode", store.WorkflowFormatUI, plainUIGraph},
		{"api graph", store.WorkflowFormatAPI, `{"3":{"class_type":"KSampler","inputs":{}}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wf := &store.Workflow{ID: 7, Format: tc.format, Graph: tc.graph}
			body := renderString(t, runModesPanel(wf, "csrf-tok"))
			if body != `<div id="run-modes"></div>` {
				t.Errorf("want an empty stable container, got:\n%s", body)
			}
		})
	}
}

// TestRunControlsIncludeModePicks pins that every run entry point carries the
// current picks along — otherwise selecting a mode would be silently ignored by
// whichever button the user actually pressed.
func TestRunControlsIncludeModePicks(t *testing.T) {
	for name, body := range map[string]string{
		"Run on ComfyUI": renderString(t, runComfyStatusFragment(5, "tok",
			comfyStatusView{configured: true, reachable: true})),
		"Run again": renderString(t, runAgainButton(5, "tok")),
		"Parameters form": renderString(t, runParametersPanel(
			&store.Workflow{ID: 5, Format: store.WorkflowFormatUI, Graph: plainUIGraph}, "tok")),
		"Incompatible options form": renderString(t, incompatibleOptionsSection(
			[]comfy.BadOption{{ClassType: "X", InputName: "y", Current: "z", Choices: []string{"a", "b"}}},
			5, "tok", false)),
	} {
		if !strings.Contains(body, `#run-modes select`) {
			t.Errorf("%s does not hx-include the mode picks:\n%s", name, body)
		}
	}
}

// TestRunWithModeConvertsAndSubmitsTheChosenPipeline is the end-to-end claim: the
// stored graph is unrunnable (everything bypassed), picking a mode makes it run, and
// the graph actually submitted is that mode's pipeline and no other.
func TestRunWithModeConvertsAndSubmitsTheChosenPipeline(t *testing.T) {
	selKey, byTitle := modeKeys(t, modeUIGraph)

	// Baseline: without a pick the run aborts exactly as it does today.
	srv, fake, id := newModeServer(t, modeUIGraph)
	if rec := post(t, srv, "/workflows/"+id+"/run", nil, true); rec.Code != http.StatusOK {
		t.Fatalf("run = %d", rec.Code)
	}
	body := pollRunUntilDone(t, srv, id)
	if fake.submitCalled {
		t.Fatal("an all-bypassed template must not submit anything without a mode pick")
	}
	if !strings.Contains(body, "disabled") && !strings.Contains(body, "nothing to run") {
		t.Errorf("expected the all-disabled abort report:\n%s", body)
	}

	for title, want := range map[string]string{
		"TEXT2VIDEO":  "EmptyLatentImage",
		"IMAGE2VIDEO": "EmptySD3LatentImage",
	} {
		t.Run(title, func(t *testing.T) {
			srv, fake, id := newModeServer(t, modeUIGraph)
			rec := post(t, srv, "/workflows/"+id+"/run",
				url.Values{"mode_key": {byTitle[title]}}, true)
			if rec.Code != http.StatusOK {
				t.Fatalf("run = %d", rec.Code)
			}
			body := pollRunUntilDone(t, srv, id)
			if !fake.submitCalled {
				t.Fatalf("picking %s should have produced a runnable graph:\n%s", title, body)
			}
			sg := string(fake.submittedGraph)
			if !strings.Contains(sg, want) {
				t.Errorf("submitted graph missing %s's node %q: %s", title, want, sg)
			}
			// The OTHER pipeline must not ride along.
			other := "EmptySD3LatentImage"
			if want == other {
				other = "EmptyLatentImage"
			}
			if strings.Contains(sg, `"class_type":"`+other+`"`) {
				t.Errorf("submitted graph also contains the other pipeline's %s: %s", other, sg)
			}

			// The STORED workflow must be byte-identical after the run.
			wfID, _ := strconv.ParseInt(id, 10, 64)
			wf, err := srv.store.GetWorkflow(context.Background(), wfID)
			if err != nil {
				t.Fatalf("reload workflow: %v", err)
			}
			if wf.Graph != modeUIGraph {
				t.Errorf("stored workflow was mutated by the run:\ngot:  %s\nwant: %s", wf.Graph, modeUIGraph)
			}
		})
	}
	_ = selKey
}

// TestRunRejectsHostileModeKey pins that a hand-built request cannot name a group
// the author did not wire into an exclusive switch — the run falls back to the
// stored graph and aborts as before.
func TestRunRejectsHostileModeKey(t *testing.T) {
	selKey, byTitle := modeKeys(t, modeUIGraph)
	for _, bad := range []string{
		"", "1:99", "999:0", selKey, "../../etc/passwd", byTitle["TEXT2VIDEO"] + "x",
	} {
		t.Run("key="+bad, func(t *testing.T) {
			srv, fake, id := newModeServer(t, modeUIGraph)
			rec := post(t, srv, "/workflows/"+id+"/run", url.Values{"mode_key": {bad}}, true)
			if rec.Code != http.StatusOK {
				t.Fatalf("run = %d", rec.Code)
			}
			pollRunUntilDone(t, srv, id)
			if fake.submitCalled {
				t.Errorf("hostile mode key %q produced a submit", bad)
			}
		})
	}
}

// TestRunParamsFragmentFollowsTheMode covers the reason the picker re-fetches the
// Parameters panel: a bypassed node exposes no editable inputs, so the pipeline's
// parameters only exist once its mode is chosen.
func TestRunParamsFragmentFollowsTheMode(t *testing.T) {
	_, byTitle := modeKeys(t, modeUIGraph)
	srv, _, id := newModeServer(t, modeUIGraph)

	// No pick → nothing editable (every node is bypassed).
	rec := get(t, srv, "/workflows/"+id+"/run/params")
	if rec.Code != http.StatusOK {
		t.Fatalf("run/params = %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), `name="wp_value"`) {
		t.Errorf("an all-bypassed template should expose no parameters:\n%s", rec.Body.String())
	}

	// With a pick → that pipeline's curated inputs appear, pre-filled from ITS values.
	rec = get(t, srv, "/workflows/"+id+"/run/params?mode_key="+url.QueryEscape(byTitle["IMAGE2VIDEO"]))
	if rec.Code != http.StatusOK {
		t.Fatalf("run/params = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{`name="wp_value"`, `name="wp_node"`, `value="768"`, "Width", "Height"} {
		if !strings.Contains(body, want) {
			t.Errorf("params fragment missing %q:\n%s", want, body)
		}
	}
	// It must NOT surface the OTHER pipeline's values.
	if strings.Contains(body, `value="512"`) {
		t.Errorf("params fragment leaked the unselected pipeline's values:\n%s", body)
	}
}

// TestRunWithParamsAppliesModeAndOverride proves the two ephemeral edits compose:
// the mode makes the pipeline runnable and the Parameters override lands on it.
func TestRunWithParamsAppliesModeAndOverride(t *testing.T) {
	_, byTitle := modeKeys(t, modeUIGraph)
	srv, fake, id := newModeServer(t, modeUIGraph)

	rec := post(t, srv, "/workflows/"+id+"/run-with-params", url.Values{
		"mode_key":  {byTitle["TEXT2VIDEO"]},
		"wp_node":   {"10"},
		"wp_widget": {"0"},
		"wp_value":  {"1024"},
	}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("run-with-params = %d", rec.Code)
	}
	pollRunUntilDone(t, srv, id)
	if !fake.submitCalled {
		t.Fatal("mode + params run should have submitted")
	}
	sg := string(fake.submittedGraph)
	if !strings.Contains(sg, "1024") {
		t.Errorf("submitted graph missing the width override: %s", sg)
	}
	wfID, _ := strconv.ParseInt(id, 10, 64)
	wf, _ := srv.store.GetWorkflow(context.Background(), wfID)
	if wf.Graph != modeUIGraph {
		t.Errorf("stored workflow mutated: %s", wf.Graph)
	}
}

// TestOrdinaryWorkflowRunUnaffectedByModePlumbing is the whole-path regression
// guard: an ordinary workflow runs identically whether or not a (meaningless)
// mode_key is posted.
func TestOrdinaryWorkflowRunUnaffectedByModePlumbing(t *testing.T) {
	for _, form := range []url.Values{nil, {"mode_key": {"1:0"}}} {
		srv, fake, id := newModeServer(t, plainUIGraph)
		if rec := post(t, srv, "/workflows/"+id+"/run", form, true); rec.Code != http.StatusOK {
			t.Fatalf("run = %d", rec.Code)
		}
		pollRunUntilDone(t, srv, id)
		if !fake.submitCalled {
			t.Fatalf("ordinary workflow failed to run with form %v", form)
		}
		sg := string(fake.submittedGraph)
		if !strings.Contains(sg, "EmptyLatentImage") {
			t.Errorf("ordinary workflow submitted the wrong graph: %s", sg)
		}
		// Its bypassed node must STAY bypassed — nothing un-bypasses without a selector.
		if strings.Contains(sg, "EmptySD3LatentImage") {
			t.Errorf("mode plumbing un-bypassed a node in an ordinary workflow: %s", sg)
		}
		wfID, _ := strconv.ParseInt(id, 10, 64)
		wf, _ := srv.store.GetWorkflow(context.Background(), wfID)
		if wf.Graph != plainUIGraph {
			t.Errorf("stored workflow mutated: %s", wf.Graph)
		}
	}
}
