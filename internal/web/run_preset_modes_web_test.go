package web

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/comfy"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// presetModeUIGraph packs two mutually-exclusive pipelines into one file, declared
// by an ACTIVE rgthree bypasser with toggleRestriction "max one". BOTH ship
// bypassed, so the stored graph exposes NOTHING until a mode is picked — which is
// exactly what makes "render against one graph, run against another" fatal.
const presetModeUIGraph = `{
  "nodes": [
    {"id": 1, "type": "Fast Groups Bypasser (rgthree)", "mode": 0, "pos": [0,0], "size": [100,50],
     "properties": {"matchColors": "purple", "matchTitle": "", "toggleRestriction": "max one"}},
    {"id": 2, "type": "KSampler", "mode": 4, "pos": [210,210], "size": [100,100],
     "widgets_values": [777, "randomize", 30, 7.5, "euler", "normal", 1.0], "inputs": []},
    {"id": 3, "type": "CLIPTextEncode", "title": "V2V", "mode": 4, "pos": [610,210], "size": [100,100],
     "widgets_values": ["video prompt"], "inputs": []}
  ],
  "links": [],
  "groups": [
    {"title": "TEXT2IMAGE", "bounding": [200,200,200,200], "color": "#a1309b"},
    {"title": "IMAGE2VIDEO", "bounding": [600,200,200,200], "color": "#a1309b"}
  ]
}`

func presetModeKeys(t *testing.T, graph string) (selKey, modeA, modeB string) {
	t.Helper()
	sels := comfy.DetectModeSelectors(json.RawMessage(graph))
	if len(sels) != 1 || len(sels[0].Modes) != 2 {
		t.Fatalf("fixture: selectors = %+v", sels)
	}
	return sels[0].Key, sels[0].Modes[0].Key, sels[0].Modes[1].Key
}

// seedModePreset stores a preset carrying a mode pick plus the values that mode's
// pipeline exposes.
func seedModePreset(t *testing.T, srv *Server, wf *store.Workflow, name, graphHash, selKey, modeKey, value string) int64 {
	t.Helper()
	modes := map[string]string{selKey: modeKey}
	var entries []comfy.PresetEntry
	for _, ri := range comfy.DetectRunInputs(comfy.ApplyModeSelection(json.RawMessage(wf.Graph), modes), nil) {
		entries = append(entries, comfy.PresetEntryFor(ri, value))
	}
	if len(entries) == 0 {
		t.Fatal("fixture: the selected mode exposes no inputs")
	}
	id, err := srv.store.CreateRunPreset(context.Background(), store.RunPreset{
		WorkflowID: wf.ID, Name: name, Position: -1, GraphHash: graphHash,
		Params: presetParamsWith("", entries, modes),
	})
	if err != nil {
		t.Fatalf("create preset: %v", err)
	}
	return id
}

// TestPresetModePreSelectsThePicker is mode-ownership decision (c) end to end: a
// preset's stored mode PRE-SELECTS the page-level picker when the tab is opened
// (an out-of-band #run-modes swap), and the panel is reconciled against that same
// mode — so the graph the user sees and the graph a run would convert agree.
func TestPresetModePreSelectsThePicker(t *testing.T) {
	srv := newTestServer(t)
	wf := seedPresetWorkflow(t, srv, "tmpl", presetModeUIGraph)
	selKey, _, modeB := presetModeKeys(t, wf.Graph)
	id := seedModePreset(t, srv, wf, "Video", wf.GraphHash, selKey, modeB, "SAVED")

	v := srv.buildPresetView(context.Background(), wf, id, nil, true)
	if v.ModesOOB[selKey] != modeB {
		t.Fatalf("ModesOOB = %v, want %s=%s", v.ModesOOB, selKey, modeB)
	}
	if len(v.Rec.Fields) == 0 {
		t.Fatal("the panel must be reconciled against the MODE-APPLIED graph")
	}
	if v.Rec.Applied() != len(v.Rec.Fields) {
		t.Errorf("applied %d of %d fields", v.Rec.Applied(), len(v.Rec.Fields))
	}

	oob := renderString(t, runModesOOB(wf, "tok", v))
	for _, want := range []string{
		`hx-swap-oob="true"`,
		`id="` + runModesContainerID + `"`,
		`value="` + modeB + `" selected`,
	} {
		if !strings.Contains(oob, want) {
			t.Errorf("OOB picker missing %q:\n%s", want, oob)
		}
	}
	// And the panel SAYS the picker is authoritative.
	panel := renderString(t, runPresetPanel(wf, "tok", v))
	if !strings.Contains(panel, "Workflow mode comes from the picker above") {
		t.Errorf("the panel must make the picker's authority visible:\n%s", panel)
	}
}

// TestPickerOverridesPresetMode: once the picker is changed it wins. The
// picker-changed entry point (GET /run/params) must NOT re-assert the preset's
// stored mode, or the user's change would be silently undone.
func TestPickerOverridesPresetMode(t *testing.T) {
	srv := newTestServer(t)
	wf := seedPresetWorkflow(t, srv, "tmpl", presetModeUIGraph)
	selKey, modeA, modeB := presetModeKeys(t, wf.Graph)
	id := seedModePreset(t, srv, wf, "Video", wf.GraphHash, selKey, modeB, "SAVED")

	v := srv.buildPresetView(context.Background(), wf, id,
		map[string]string{selKey: modeA}, false /* picker changed */)
	if len(v.ModesOOB) != 0 {
		t.Errorf("the picker-changed path must not swap the picker back: %v", v.ModesOOB)
	}
	if v.Rec.Modes[selKey] != modeA {
		t.Fatalf("reconciled against %v, want the PICKER's %s", v.Rec.Modes, modeA)
	}
	// Mode A's inputs are a different set, so the stored values have nowhere to go
	// and the user is TOLD rather than left with a silently-empty tab.
	if v.Rec.Applied() != 0 {
		t.Errorf("mode B's values must not leak into mode A: applied=%d", v.Rec.Applied())
	}
	if len(v.Rec.Dropped) == 0 {
		t.Error("the un-appliable stored values must be named")
	}

	// The HTTP path agrees.
	rec := get(t, srv, "/workflows/"+strconv.FormatInt(wf.ID, 10)+
		"/run/params?"+url.Values{"mode_key": {modeA}, presetIDField: {strconv.FormatInt(id, 10)}}.Encode())
	body := rec.Body.String()
	if strings.Contains(body, "hx-swap-oob") {
		t.Errorf("GET /run/params must not emit an OOB picker swap:\n%s", body)
	}
	if !strings.Contains(body, `name="preset_id" value="`+strconv.FormatInt(id, 10)+`"`) {
		t.Errorf("a mode change must keep the active tab:\n%s", body)
	}
}

// TestPresetModeDroppedAndNamedOnDrift: a mode key is
// "<selector node id>:<group index>" — positional — so it is withheld when the
// hash cannot prove the graph is unchanged, and NAMED. A silent drop would
// convert an all-bypassed graph and abort as a baffling "nothing to run".
func TestPresetModeDroppedAndNamedOnDrift(t *testing.T) {
	srv := newTestServer(t)
	wf := seedPresetWorkflow(t, srv, "tmpl", presetModeUIGraph)
	selKey, _, modeB := presetModeKeys(t, wf.Graph)
	id := seedModePreset(t, srv, wf, "Video", "STALEHASH", selKey, modeB, "SAVED")

	v := srv.buildPresetView(context.Background(), wf, id, nil, true)
	if len(v.ModesOOB) != 0 {
		t.Errorf("a drifted mode must not pre-select the picker: %v", v.ModesOOB)
	}
	if len(v.Rec.DroppedModes) != 1 {
		t.Fatalf("DroppedModes = %+v, want one", v.Rec.DroppedModes)
	}
	if v.Rec.DroppedModes[0].Reason != comfy.PresetDropModeDrifted {
		t.Errorf("reason = %q", v.Rec.DroppedModes[0].Reason)
	}
	// With no mode applied the template exposes nothing, so the panel is absent —
	// but the picker above still lets the user choose, and that is what the banner
	// tells them. Assert the naming through the reconciliation, which is what the
	// banner renders from.
	if v.Rec.DroppedModes[0].Name != "IMAGE2VIDEO" {
		t.Errorf("the dropped mode must be named by its group title, got %q", v.Rec.DroppedModes[0].Name)
	}
}

// TestPresetModeBannerNamesTheMode renders the banner for a drifted mode against a
// graph that still exposes fields, so the amber banner is actually shown.
func TestPresetModeBannerNamesTheMode(t *testing.T) {
	srv := newTestServer(t)
	wf := seedPresetWorkflow(t, srv, "tmpl", presetModeUIGraph)
	selKey, _, modeB := presetModeKeys(t, wf.Graph)
	id := seedModePreset(t, srv, wf, "Video", "STALEHASH", selKey, modeB, "SAVED")

	// The PICKER supplies a mode, so fields exist; the preset's own (drifted) mode
	// is still reported as dropped.
	_, modeA, _ := presetModeKeys(t, wf.Graph)
	v := srv.buildPresetView(context.Background(), wf, id, map[string]string{selKey: modeA}, true)
	got := renderString(t, runPresetPanel(wf, "tok", v))
	if !strings.Contains(got, `data-color="warning"`) {
		t.Fatalf("expected the drift banner:\n%s", got)
	}
	for _, want := range []string{"Workflow mode not applied", "IMAGE2VIDEO", "pick a mode above"} {
		if !strings.Contains(got, want) {
			t.Errorf("banner missing %q:\n%s", want, got)
		}
	}
}

// TestOnlyActiveTabIsReconciled: inactive tabs are LABEL-ONLY. Reconciling all
// twelve on every render would be a 12x regression the preset cap's reasoning
// depends on not happening — and it is observable: only ONE tab's fields exist.
func TestOnlyActiveTabIsReconciled(t *testing.T) {
	srv := newTestServer(t)
	wf := seedPresetWorkflow(t, srv, "t2i", presetUIGraph)
	for i := 0; i < store.MaxRunPresetsPerWorkflow; i++ {
		seedPreset(t, srv, wf, "p"+strconv.Itoa(i), wf.GraphHash,
			func(ri comfy.RunInput) string { return "V" + strconv.Itoa(i) })
	}
	v := srv.buildPresetView(context.Background(), wf, 0, nil, true)
	got := renderString(t, runPresetPanel(wf, "tok", v))

	live := comfy.DetectRunInputs(json.RawMessage(wf.Graph), nil)
	if n := strings.Count(got, `name="wp_node"`); n != len(live) {
		t.Errorf("wp_node fields = %d, want %d (exactly ONE tab's worth)", n, len(live))
	}
	// Only the FIRST preset's values are rendered.
	if !strings.Contains(got, "V0") {
		t.Error("the active tab's values are missing")
	}
	for i := 1; i < store.MaxRunPresetsPerWorkflow; i++ {
		if strings.Contains(got, `value="V`+strconv.Itoa(i)+`"`) {
			t.Errorf("inactive preset %d's values were rendered", i)
		}
	}
}

// TestRunFromPresetIsAttributed: a run started from a preset tab carries the
// preset id + a snapshot of its name into runOptions, so the captured generation
// stays labeled after the preset is deleted.
func TestRunFromPresetIsAttributed(t *testing.T) {
	srv := newTestServer(t)
	wf := seedPresetWorkflow(t, srv, "t2i", presetUIGraph)
	id := seedPreset(t, srv, wf, "Hi-res 8-step", wf.GraphHash, func(ri comfy.RunInput) string { return ri.Current })

	captured := make(chan runOptions, 1)
	srv.runFn = func(ctx context.Context, w *store.Workflow, up runUpdater, opts runOptions) (*runResult, error) {
		captured <- opts
		return &runResult{PromptID: "p1"}, nil
	}
	code, _ := doPresetPost(t, srv, "/workflows/"+strconv.FormatInt(wf.ID, 10)+"/run-with-params",
		url.Values{presetIDField: {strconv.FormatInt(id, 10)}, "wp_node": {"6"},
			"wp_widget": {"0"}, "wp_value": {"x"}}, true)
	if code != 200 {
		t.Fatalf("run status = %d", code)
	}
	opts := <-captured
	if opts.PresetID != id || opts.PresetName != "Hi-res 8-step" {
		t.Errorf("attribution = (%d, %q), want (%d, %q)", opts.PresetID, opts.PresetName, id, "Hi-res 8-step")
	}
}

// TestRunWithForeignPresetIDRejected: the run endpoint applies the same
// cross-workflow rule as the CRUD endpoints, and starts nothing.
func TestRunWithForeignPresetIDRejected(t *testing.T) {
	srv := newTestServer(t)
	a := seedPresetWorkflow(t, srv, "A", presetUIGraph)
	b := seedPresetWorkflow(t, srv, "B", presetUIGraph)
	bp := seedPreset(t, srv, b, "B-preset", b.GraphHash, func(ri comfy.RunInput) string { return ri.Current })

	started := 0
	srv.runFn = func(ctx context.Context, w *store.Workflow, up runUpdater, opts runOptions) (*runResult, error) {
		started++
		return &runResult{PromptID: "p"}, nil
	}
	code, _ := doPresetPost(t, srv, "/workflows/"+strconv.FormatInt(a.ID, 10)+"/run-with-params",
		url.Values{presetIDField: {strconv.FormatInt(bp, 10)}}, true)
	if code != 404 {
		t.Errorf("status = %d, want 404", code)
	}
	if started != 0 {
		t.Errorf("a rejected request started %d runs", started)
	}
}
