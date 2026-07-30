package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/comfy"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// presetUIGraph is the fixture every preset test renders against: a titled
// positive prompt, a KSampler (whose seed carries the control_after_generate slot)
// and an EmptyLatentImage.
const presetUIGraph = `{
  "nodes": [
    {"id": 6, "type": "CLIPTextEncode", "title": "POSITIVE",
     "widgets_values": ["a scenic mountain"], "inputs": []},
    {"id": 3, "type": "KSampler",
     "widgets_values": [1234, "randomize", 20, 8.0, "euler", "normal", 1.0], "inputs": []},
    {"id": 5, "type": "EmptyLatentImage", "widgets_values": [1024, 768, 2], "inputs": []}
  ],
  "links": []
}`

// presetUIGraphRetargeted keeps node 3 but makes it a PROMPT: the key {3,0} used
// to be a seed. This is the drift the tuple check must catch.
const presetUIGraphRetargeted = `{
  "nodes": [
    {"id": 6, "type": "CLIPTextEncode", "title": "POSITIVE",
     "widgets_values": ["a scenic mountain"], "inputs": []},
    {"id": 3, "type": "CLIPTextEncode", "title": "NEGATIVE",
     "widgets_values": ["blurry"], "inputs": []}
  ],
  "links": []
}`

// presetUIGraphShifted is the SAME node ids and the SAME node types as
// presetUIGraph, but node 3's KSampler carries no control_after_generate slot, so
// every widget index after the seed shifts down by one:
//
//	presetUIGraph   {3,0}=seed {3,2}=steps {3,3}=cfg {3,4}=sampler {3,5}=scheduler
//	shifted         {3,0}=seed {3,1}=steps {3,2}=cfg {3,3}=sampler {3,4}=scheduler
//
// Nothing about the node identities changed — only the layout. That is what makes
// values captured against one graph land on a DIFFERENT parameter in the other,
// with every tuple check still available to catch it.
const presetUIGraphShifted = `{
  "nodes": [
    {"id": 6, "type": "CLIPTextEncode", "title": "POSITIVE",
     "widgets_values": ["a scenic mountain"], "inputs": []},
    {"id": 3, "type": "KSampler",
     "widgets_values": [1234, 20, 8.0, "euler", "normal", 1.0], "inputs": []},
    {"id": 5, "type": "EmptyLatentImage", "widgets_values": [1024, 768, 2], "inputs": []}
  ],
  "links": []
}`

// replaceGraph swaps a workflow's graph IN PLACE, exactly the way a rescan does
// (store.UpsertWorkflowByPath), and returns the reloaded row.
func replaceGraph(t *testing.T, srv *Server, wfID int64, graph string) *store.Workflow {
	t.Helper()
	if _, err := srv.store.DB().Exec(`UPDATE workflows SET graph = ?, graph_hash = ? WHERE id = ?`,
		graph, store.GraphHash(graph), wfID); err != nil {
		t.Fatalf("replace graph: %v", err)
	}
	got, err := srv.store.GetWorkflow(context.Background(), wfID)
	if err != nil {
		t.Fatalf("get workflow: %v", err)
	}
	return got
}

// widgetOf returns the widget index of the live input named input on node.
func widgetOf(t *testing.T, graph, node, input string) string {
	t.Helper()
	for _, ri := range comfy.DetectRunInputs(json.RawMessage(graph), nil) {
		if ri.NodeID == node && ri.InputName == input {
			return strconv.Itoa(ri.WidgetIndex)
		}
	}
	t.Fatalf("fixture: %s.%s is not a live input", node, input)
	return ""
}

// fieldValue returns the pre-filled value of the reconciled field for input.
func fieldValue(rec comfy.PresetReconciliation, node, input string) (string, bool) {
	for _, f := range rec.Fields {
		if f.Input.NodeID == node && f.Input.InputName == input {
			return f.Value, f.FromPreset
		}
	}
	return "", false
}

// seedWorkflow stores a UI workflow and returns it (with graph_hash populated by
// the insert path).
func seedPresetWorkflow(t *testing.T, srv *Server, name, graph string) *store.Workflow {
	t.Helper()
	wf := store.Workflow{
		Name: name, Format: store.WorkflowFormatUI, Graph: graph,
		Source: store.WorkflowSourceImported,
	}
	id, err := srv.store.InsertWorkflow(context.Background(), &wf)
	if err != nil {
		t.Fatalf("insert workflow: %v", err)
	}
	got, err := srv.store.GetWorkflow(context.Background(), id)
	if err != nil {
		t.Fatalf("get workflow: %v", err)
	}
	return got
}

// seedPreset stores a preset whose values come from the graph's live inputs
// transformed by value(), stamped with graphHash.
func seedPreset(t *testing.T, srv *Server, wf *store.Workflow, name, graphHash string, value func(comfy.RunInput) string) int64 {
	t.Helper()
	var entries []comfy.PresetEntry
	for _, ri := range comfy.DetectRunInputs(json.RawMessage(wf.Graph), nil) {
		entries = append(entries, comfy.PresetEntryFor(ri, value(ri)))
	}
	id, err := srv.store.CreateRunPreset(context.Background(), store.RunPreset{
		WorkflowID: wf.ID, Name: name, Position: -1, GraphHash: graphHash,
		Params: presetParamsWith("", entries, nil),
	})
	if err != nil {
		t.Fatalf("create preset: %v", err)
	}
	return id
}

// doPresetPost issues one preset POST (form-encoded, CSRF via the header like the
// other web tests) and returns the status + body.
func doPresetPost(t *testing.T, srv *Server, path string, form url.Values, withCSRF bool) (int, string) {
	t.Helper()
	rec := post(t, srv, path, form, withCSRF)
	return rec.Code, rec.Body.String()
}

// ── render states ────────────────────────────────────────────────────────────

// TestRunPanelNoPresetsRendersImplicitTab: a workflow with nothing saved still
// renders one tab, seeded from the graph's current values, and a page render never
// writes to the database to make that true.
func TestRunPanelNoPresetsRendersImplicitTab(t *testing.T) {
	srv := newTestServer(t)
	wf := seedPresetWorkflow(t, srv, "t2i", presetUIGraph)

	v := srv.buildPresetView(context.Background(), wf, 0, nil, true)
	got := renderString(t, runPresetPanel(wf, "tok", v))

	for _, want := range []string{
		`role="tablist"`, `role="tab"`, `aria-selected="true"`,
		"Preset 1",
		"+ Fork",
		"a scenic mountain", // seeded from the graph
		`value="1234"`,      // the seed's CURRENT value
		`name="preset_id" value="0"`,
		"Save as preset",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("implicit tab missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Delete preset") {
		t.Error("the implicit tab has nothing to delete")
	}
	n, err := srv.store.CountRunPresets(context.Background(), wf.ID)
	if err != nil || n != 0 {
		t.Errorf("rendering must not write a preset row (count=%d err=%v)", n, err)
	}
}

// TestRunPanelRendersTabStrip: one role=tab per preset, exactly ONE
// aria-selected="true".
func TestRunPanelRendersTabStrip(t *testing.T) {
	srv := newTestServer(t)
	wf := seedPresetWorkflow(t, srv, "t2i", presetUIGraph)
	a := seedPreset(t, srv, wf, "Base", wf.GraphHash, func(ri comfy.RunInput) string { return ri.Current })
	b := seedPreset(t, srv, wf, "Hi-res", wf.GraphHash, func(ri comfy.RunInput) string { return ri.Current })

	v := srv.buildPresetView(context.Background(), wf, b, nil, true)
	got := renderString(t, runPresetPanel(wf, "tok", v))

	if n := strings.Count(got, `role="tab"`); n != 2 {
		t.Errorf("role=tab count = %d, want 2:\n%s", n, got)
	}
	if n := strings.Count(got, `aria-selected="true"`); n != 1 {
		t.Errorf("aria-selected=true count = %d, want exactly 1", n)
	}
	for _, want := range []string{
		"Base", "Hi-res",
		"/workflows/" + strconv.FormatInt(wf.ID, 10) + "/run/presets/" + strconv.FormatInt(a, 10) + "/activate",
		`name="preset_id" value="` + strconv.FormatInt(b, 10) + `"`,
		"Delete preset",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("strip missing %q", want)
		}
	}
}

// TestForkRendersDisabledAtCap: at 12 presets Fork is disabled with the reason —
// no silent eviction of the oldest, because presets are user data.
func TestForkRendersDisabledAtCap(t *testing.T) {
	srv := newTestServer(t)
	wf := seedPresetWorkflow(t, srv, "t2i", presetUIGraph)
	for i := 0; i < store.MaxRunPresetsPerWorkflow; i++ {
		seedPreset(t, srv, wf, "p"+strconv.Itoa(i), wf.GraphHash, func(ri comfy.RunInput) string { return ri.Current })
	}
	v := srv.buildPresetView(context.Background(), wf, 0, nil, true)
	if !v.AtCap {
		t.Fatal("AtCap = false at the cap")
	}
	got := renderString(t, runPresetPanel(wf, "tok", v))
	if !strings.Contains(got, "disabled") || !strings.Contains(got, "the maximum") {
		t.Errorf("Fork must render disabled with the reason:\n%s", got)
	}
}

// TestNoDriftBannerOnExactHash: an unchanged preset renders NO warning banner.
// A banner that fires on every open stops being read.
func TestNoDriftBannerOnExactHash(t *testing.T) {
	srv := newTestServer(t)
	wf := seedPresetWorkflow(t, srv, "t2i", presetUIGraph)
	id := seedPreset(t, srv, wf, "Base", wf.GraphHash, func(ri comfy.RunInput) string { return "SAVED" })

	v := srv.buildPresetView(context.Background(), wf, id, nil, true)
	got := renderString(t, runPresetPanel(wf, "tok", v))

	if strings.Contains(got, `data-color="warning"`) {
		t.Errorf("exact hash must render no drift banner:\n%s", got)
	}
	if strings.Contains(got, "Adopt current graph") {
		t.Error("Adopt must not be offered when nothing drifted")
	}
	if !strings.Contains(got, "SAVED") {
		t.Error("the saved value should be pre-filled")
	}
}

// TestDriftBannerNamesEveryDroppedField: the banner must say WHAT changed, not
// just that something did — and it names both sides of a retarget.
func TestDriftBannerNamesEveryDroppedField(t *testing.T) {
	srv := newTestServer(t)
	// Save against the ORIGINAL graph, then replace the workflow's graph so node 3
	// is a prompt (the {3,0} key is retargeted) and the latent node disappears.
	wf := seedPresetWorkflow(t, srv, "t2i", presetUIGraph)
	oldHash := wf.GraphHash
	id := seedPreset(t, srv, wf, "Base", oldHash, func(ri comfy.RunInput) string { return "STORED" })

	// Replace the workflow's graph IN PLACE, exactly the way a rescan does
	// (store.UpsertWorkflowByPath) — that is what makes a stored positional key
	// point somewhere else.
	if _, err := srv.store.DB().Exec(`UPDATE workflows SET graph = ?, graph_hash = ? WHERE id = ?`,
		presetUIGraphRetargeted, store.GraphHash(presetUIGraphRetargeted), wf.ID); err != nil {
		t.Fatalf("replace graph: %v", err)
	}
	cur, _ := srv.store.GetWorkflow(context.Background(), wf.ID)
	if cur.GraphHash == oldHash {
		t.Fatal("fixture: the graph hash did not move")
	}

	v := srv.buildPresetView(context.Background(), cur, id, nil, true)
	if !v.Drifted {
		t.Fatal("Drifted = false after replacing the graph")
	}
	got := renderString(t, runPresetPanel(cur, "tok", v))

	if !strings.Contains(got, `data-color="warning"`) {
		t.Fatalf("expected a drift banner:\n%s", got)
	}
	for _, want := range []string{
		"graph changed since this preset was saved",
		"Seed",              // the stored label of the retargeted entry
		"Prompt (NEGATIVE)", // what that slot drives NOW
		"Width",             // a dropped (vanished) entry
		"Adopt current graph",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("banner missing %q:\n%s", want, got)
		}
	}
	// The retargeted field must show the GRAPH's value, never the stored one.
	if !strings.Contains(got, "blurry") {
		t.Error("retargeted field must default to the graph's current value")
	}
}

// TestMalformedStoredEntryIsDroppedAndNamed: a stored entry whose positional key
// cannot be decoded (blank node id, non-integer widget slot) must be dropped AND
// NAMED. Discarding it in the decoder, before the reconciler ever saw it, made it
// the one drop in this surface the user was never told about.
func TestMalformedStoredEntryIsDroppedAndNamed(t *testing.T) {
	srv := newTestServer(t)
	wf := seedPresetWorkflow(t, srv, "t2i", presetUIGraph)

	// A hand-written blob: one good entry, one with a non-integer widget slot, one
	// with no node id at all. This is what a pre-0014 / corrupted row looks like.
	params := `{"ui_widget_overrides":[
	  {"node_id":"6","widget":0,"value":"GOOD","kind":"text","class_type":"CLIPTextEncode","input_name":"text","label":"Prompt (POSITIVE)"},
	  {"node_id":"3","widget":"not-a-number","value":"BROKEN WIDGET","label":"Steps"},
	  {"node_id":"","widget":0,"value":"BROKEN NODE"}
	]}`
	id, err := srv.store.CreateRunPreset(context.Background(), store.RunPreset{
		WorkflowID: wf.ID, Name: "Base", Position: -1, GraphHash: wf.GraphHash, Params: params,
	})
	if err != nil {
		t.Fatal(err)
	}

	v := srv.buildPresetView(context.Background(), wf, id, nil, true)
	if len(v.Rec.Dropped) != 2 {
		t.Fatalf("dropped = %+v, want the two malformed entries named", v.Rec.Dropped)
	}
	for _, d := range v.Rec.Dropped {
		if d.Reason != comfy.PresetDropMalformed {
			t.Errorf("drop reason = %q, want malformed", d.Reason)
		}
	}
	got := renderString(t, runPresetPanel(wf, "tok", v))
	for _, want := range []string{`data-color="warning"`, "Steps", "a saved value"} {
		if !strings.Contains(got, want) {
			t.Errorf("banner must name the malformed entry (%q missing):\n%s", want, got)
		}
	}
	for _, bad := range []string{"BROKEN WIDGET", "BROKEN NODE"} {
		if strings.Contains(got, bad) {
			t.Errorf("a malformed value reached a field: %q", bad)
		}
	}
	if !strings.Contains(got, "GOOD") {
		t.Error("the well-formed entry must still apply")
	}
}

// TestUntrustedPresetNameEscaped: a stored XSS payload in a tab label renders
// escaped everywhere it appears.
func TestUntrustedPresetNameEscaped(t *testing.T) {
	srv := newTestServer(t)
	wf := seedPresetWorkflow(t, srv, "t2i", presetUIGraph)
	const payload = `<img src=x onerror=alert(1)>`
	id := seedPreset(t, srv, wf, payload, wf.GraphHash, func(ri comfy.RunInput) string { return ri.Current })

	v := srv.buildPresetView(context.Background(), wf, id, nil, true)
	got := renderString(t, runPresetPanel(wf, "tok", v))

	if strings.Contains(got, "<img src=x") {
		t.Fatalf("unescaped preset name rendered:\n%s", got)
	}
	if !strings.Contains(got, "&lt;img src=x") {
		t.Errorf("escaped name not found:\n%s", got)
	}
}

// TestTabStripOutsideRunStatus is the streaming invariant: the tabs live inside
// #run-params, a SIBLING of #run-status, so the 1 s run poller cannot clobber a
// half-typed prompt.
func TestTabStripOutsideRunStatus(t *testing.T) {
	srv := newTestServer(t)
	wf := seedPresetWorkflow(t, srv, "t2i", presetUIGraph)
	seedPreset(t, srv, wf, "Base", wf.GraphHash, func(ri comfy.RunInput) string { return ri.Current })
	v := srv.buildPresetView(context.Background(), wf, 0, nil, true)

	page := renderString(t, runPanel(wf, runSnapshot{}, "tok", true, false, "blur", v))
	params := strings.Index(page, `id="`+runParamsContainerID+`"`)
	status := strings.Index(page, `id="`+runStatusContainerID+`"`)
	tabs := strings.Index(page, `role="tablist"`)
	if params < 0 || status < 0 || tabs < 0 {
		t.Fatalf("containers missing (params=%d status=%d tabs=%d)", params, status, tabs)
	}
	if !(params < tabs && tabs < status) {
		t.Errorf("tablist must sit inside #run-params, before #run-status (params=%d tabs=%d status=%d)",
			params, tabs, status)
	}
	// The preset form targets #run-status for the RUN and #run-params for tab
	// actions; nothing outerHTML-replaces a polling node.
	if strings.Contains(page, `hx-swap="outerHTML"`) {
		t.Error("no preset control may outerHTML-swap")
	}
}

// ── endpoint behaviour ───────────────────────────────────────────────────────

// TestPresetActivateSavesOutgoingTab is requirement "unrun drafts survive":
// typing in tab A then clicking tab B persists A's values.
func TestPresetActivateSavesOutgoingTab(t *testing.T) {
	srv := newTestServer(t)
	wf := seedPresetWorkflow(t, srv, "t2i", presetUIGraph)
	a := seedPreset(t, srv, wf, "A", wf.GraphHash, func(ri comfy.RunInput) string { return ri.Current })
	b := seedPreset(t, srv, wf, "B", wf.GraphHash, func(ri comfy.RunInput) string { return ri.Current })

	form := url.Values{
		presetIDField:   {strconv.FormatInt(a, 10)},
		presetNameField: {"A"},
		"wp_node":       {"6"},
		"wp_widget":     {"0"},
		"wp_value":      {"typed but never run"},
	}
	code, body := doPresetPost(t, srv,
		"/workflows/"+strconv.FormatInt(wf.ID, 10)+"/run/presets/"+strconv.FormatInt(b, 10)+"/activate",
		form, true)
	if code != http.StatusOK {
		t.Fatalf("activate status = %d", code)
	}
	if !strings.Contains(body, `name="preset_id" value="`+strconv.FormatInt(b, 10)+`"`) {
		t.Errorf("response should activate B:\n%s", body)
	}

	got, err := srv.store.GetRunPreset(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Params, "typed but never run") {
		t.Errorf("the outgoing tab's draft was lost: %s", got.Params)
	}
	if got.GraphHash != wf.GraphHash {
		t.Errorf("activate must not move graph_hash")
	}
}

// TestPresetForkDeepCopies: editing the fork must not mutate the source.
func TestPresetForkDeepCopies(t *testing.T) {
	srv := newTestServer(t)
	wf := seedPresetWorkflow(t, srv, "t2i", presetUIGraph)
	src := seedPreset(t, srv, wf, "Base", wf.GraphHash, func(ri comfy.RunInput) string { return "ORIGINAL" })

	form := url.Values{
		presetIDField:   {strconv.FormatInt(src, 10)},
		presetFromField: {strconv.FormatInt(src, 10)},
	}
	code, body := doPresetPost(t, srv, "/workflows/"+strconv.FormatInt(wf.ID, 10)+"/run/presets", form, true)
	if code != http.StatusOK {
		t.Fatalf("fork status = %d: %s", code, body)
	}
	list, _ := srv.store.ListRunPresets(context.Background(), wf.ID)
	if len(list) != 2 {
		t.Fatalf("presets after fork = %d, want 2", len(list))
	}
	fork := list[1]
	if fork.Name != "Base copy" {
		t.Errorf("fork name = %q", fork.Name)
	}
	if !strings.Contains(fork.Params, "ORIGINAL") {
		t.Errorf("fork did not copy the source values: %s", fork.Params)
	}

	// Now edit the FORK and confirm the source is untouched.
	edit := url.Values{
		presetIDField:   {strconv.FormatInt(fork.ID, 10)},
		presetNameField: {"Forked"},
		"wp_node":       {"6"},
		"wp_widget":     {"0"},
		"wp_value":      {"CHANGED"},
	}
	if code, body := doPresetPost(t, srv,
		"/workflows/"+strconv.FormatInt(wf.ID, 10)+"/run/presets/"+strconv.FormatInt(fork.ID, 10)+"/save",
		edit, true); code != http.StatusOK {
		t.Fatalf("save fork = %d: %s", code, body)
	}
	srcNow, _ := srv.store.GetRunPreset(context.Background(), src)
	if strings.Contains(srcNow.Params, "CHANGED") {
		t.Errorf("editing the fork mutated the source: %s", srcNow.Params)
	}
	if !strings.Contains(srcNow.Params, "ORIGINAL") {
		t.Errorf("source lost its own values: %s", srcNow.Params)
	}
}

// TestForkRefusedAtCap: the 13th fork inserts nothing and says why.
func TestForkRefusedAtCap(t *testing.T) {
	srv := newTestServer(t)
	wf := seedPresetWorkflow(t, srv, "t2i", presetUIGraph)
	for i := 0; i < store.MaxRunPresetsPerWorkflow; i++ {
		seedPreset(t, srv, wf, "p"+strconv.Itoa(i), wf.GraphHash, func(ri comfy.RunInput) string { return ri.Current })
	}
	before, _ := srv.store.CountRunPresets(context.Background(), wf.ID)

	code, body := doPresetPost(t, srv, "/workflows/"+strconv.FormatInt(wf.ID, 10)+"/run/presets",
		url.Values{presetIDField: {"0"}}, true)
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if !strings.Contains(body, "the maximum") {
		t.Errorf("refusal must say why:\n%s", body)
	}
	after, _ := srv.store.CountRunPresets(context.Background(), wf.ID)
	if after != before {
		t.Errorf("preset count moved %d → %d on a refused create", before, after)
	}
}

// TestPresetSaveWithoutAdoptDoesNotRestampHash is decision 7: clicking Save means
// "save my text", not "I certify this parameter set against a graph I did not
// inspect". Only an explicit adopt_graph=1 stamps the current graph's hash.
//
// It does NOT mean the pre-edit hash survives: Save replaces the stored entries
// with values captured against the CURRENT graph, so keeping a hash naming the
// earlier one would certify these values against a graph they never came from. A
// non-adopt save therefore blanks it — the drift banner, the un-certified state and
// the "Adopt current graph" offer all survive, which is everything decision 7 asks
// for.
func TestPresetSaveWithoutAdoptDoesNotRestampHash(t *testing.T) {
	srv := newTestServer(t)
	wf := seedPresetWorkflow(t, srv, "t2i", presetUIGraph)
	id := seedPreset(t, srv, wf, "Base", "STALEHASH", func(ri comfy.RunInput) string { return ri.Current })

	base := "/workflows/" + strconv.FormatInt(wf.ID, 10) + "/run/presets/" + strconv.FormatInt(id, 10) + "/save"
	form := url.Values{
		presetIDField:   {strconv.FormatInt(id, 10)},
		presetNameField: {"Base"},
		"wp_node":       {"6"},
		"wp_widget":     {"0"},
		"wp_value":      {"kept text"},
	}
	code, body := doPresetPost(t, srv, base, form, true)
	if code != http.StatusOK {
		t.Fatalf("save = %d", code)
	}
	got, _ := srv.store.GetRunPreset(context.Background(), id)
	if got.GraphHash == wf.GraphHash {
		t.Fatalf("plain Save adopted the current graph (%q) — it must never do that", got.GraphHash)
	}
	if got.GraphHash != "" {
		t.Fatalf("graph_hash = %q, want blank: the stored entries were re-captured "+
			"against the current graph, so the old hash no longer describes them", got.GraphHash)
	}
	if !strings.Contains(got.Params, "kept text") {
		t.Errorf("Save must still persist the text: %s", got.Params)
	}
	if !strings.Contains(body, "Adopt current graph") {
		t.Errorf("the adoption must still be OFFERED after a plain save:\n%s", body)
	}

	// Second click, carrying the explicit confirmation.
	form.Set(presetAdoptField, "1")
	if code, _ := doPresetPost(t, srv, base, form, true); code != http.StatusOK {
		t.Fatalf("adopt = %d", code)
	}
	got, _ = srv.store.GetRunPreset(context.Background(), id)
	if got.GraphHash != wf.GraphHash {
		t.Errorf("adopt did not re-stamp: %q, want %q", got.GraphHash, wf.GraphHash)
	}
}

// TestPresetDeleteActivatesNeighbour.
func TestPresetDeleteActivatesNeighbour(t *testing.T) {
	srv := newTestServer(t)
	wf := seedPresetWorkflow(t, srv, "t2i", presetUIGraph)
	a := seedPreset(t, srv, wf, "A", wf.GraphHash, func(ri comfy.RunInput) string { return ri.Current })
	b := seedPreset(t, srv, wf, "B", wf.GraphHash, func(ri comfy.RunInput) string { return ri.Current })

	code, body := doPresetPost(t, srv,
		"/workflows/"+strconv.FormatInt(wf.ID, 10)+"/run/presets/"+strconv.FormatInt(b, 10)+"/delete",
		url.Values{presetIDField: {strconv.FormatInt(b, 10)}}, true)
	if code != http.StatusOK {
		t.Fatalf("delete = %d: %s", code, body)
	}
	list, _ := srv.store.ListRunPresets(context.Background(), wf.ID)
	if len(list) != 1 || list[0].ID != a {
		t.Fatalf("presets after delete = %+v", list)
	}
	if !strings.Contains(body, `name="preset_id" value="`+strconv.FormatInt(a, 10)+`"`) {
		t.Errorf("the neighbouring tab should be active:\n%s", body)
	}
}

// ── security / gating ────────────────────────────────────────────────────────

// TestPresetEndpointsRejectCSRF: every preset POST without (or with a wrong)
// token is 403 AND mutates nothing.
func TestPresetEndpointsRejectCSRF(t *testing.T) {
	srv := newTestServer(t)
	wf := seedPresetWorkflow(t, srv, "t2i", presetUIGraph)
	id := seedPreset(t, srv, wf, "Base", wf.GraphHash, func(ri comfy.RunInput) string { return "ORIG" })
	wid := strconv.FormatInt(wf.ID, 10)
	pid := strconv.FormatInt(id, 10)

	paths := []string{
		"/workflows/" + wid + "/run/presets",
		"/workflows/" + wid + "/run/presets/" + pid + "/activate",
		"/workflows/" + wid + "/run/presets/" + pid + "/save",
		"/workflows/" + wid + "/run/presets/" + pid + "/delete",
	}
	for _, p := range paths {
		t.Run("missing "+p, func(t *testing.T) {
			code, _ := doPresetPost(t, srv, p, url.Values{presetIDField: {pid}, "wp_node": {"6"},
				"wp_widget": {"0"}, "wp_value": {"HOSTILE"}}, false)
			if code != http.StatusForbidden {
				t.Errorf("status = %d, want 403", code)
			}
		})
		t.Run("wrong "+p, func(t *testing.T) {
			f := url.Values{presetIDField: {pid}, "csrf_token": {"nope"}}
			code, _ := doPresetPost(t, srv, p, f, false)
			if code != http.StatusForbidden {
				t.Errorf("status = %d, want 403", code)
			}
		})
	}
	list, _ := srv.store.ListRunPresets(context.Background(), wf.ID)
	if len(list) != 1 {
		t.Errorf("preset count = %d, want 1 (no CSRF-less create/delete)", len(list))
	}
	if !strings.Contains(list[0].Params, "ORIG") {
		t.Errorf("a CSRF-less save mutated the preset: %s", list[0].Params)
	}
}

// TestPresetEndpointsRejectNonLoopback: bound off-loopback every preset endpoint
// returns the gate note and mutates nothing, even with a valid token (CSRF is not
// an auth boundary).
func TestPresetEndpointsRejectNonLoopback(t *testing.T) {
	srv, _ := gateTestServer(t, "0.0.0.0:8787")
	wf := seedPresetWorkflow(t, srv, "t2i", presetUIGraph)
	id := seedPreset(t, srv, wf, "Base", wf.GraphHash, func(ri comfy.RunInput) string { return "ORIG" })
	wid := strconv.FormatInt(wf.ID, 10)
	pid := strconv.FormatInt(id, 10)

	for _, p := range []string{
		"/workflows/" + wid + "/run/presets",
		"/workflows/" + wid + "/run/presets/" + pid + "/activate",
		"/workflows/" + wid + "/run/presets/" + pid + "/save",
		"/workflows/" + wid + "/run/presets/" + pid + "/delete",
	} {
		code, body := doPresetPost(t, srv, p, url.Values{presetIDField: {pid}}, true)
		if code != http.StatusOK || !strings.Contains(body, gateMsg) {
			t.Errorf("%s: status=%d body=%s, want the gate note", p, code, body)
		}
	}
	// The GET is gated too.
	rec := get(t, srv, "/workflows/"+wid+"/run/params")
	if !strings.Contains(rec.Body.String(), gateMsg) {
		t.Errorf("GET /run/params must be gated off-loopback: %s", rec.Body.String())
	}

	list, _ := srv.store.ListRunPresets(context.Background(), wf.ID)
	if len(list) != 1 || !strings.Contains(list[0].Params, "ORIG") {
		t.Errorf("a gated endpoint mutated state: %+v", list)
	}
}

// TestPresetCrossWorkflowRejected: a preset id belonging to ANOTHER workflow is a
// 404 with no read and no write — the id space is global and guessable.
func TestPresetCrossWorkflowRejected(t *testing.T) {
	srv := newTestServer(t)
	a := seedPresetWorkflow(t, srv, "A", presetUIGraph)
	b := seedPresetWorkflow(t, srv, "B", presetUIGraph)
	bp := seedPreset(t, srv, b, "B-preset", b.GraphHash, func(ri comfy.RunInput) string { return "B-VALUE" })

	aid := strconv.FormatInt(a.ID, 10)
	pid := strconv.FormatInt(bp, 10)
	for _, p := range []string{
		"/workflows/" + aid + "/run/presets/" + pid + "/activate",
		"/workflows/" + aid + "/run/presets/" + pid + "/save",
		"/workflows/" + aid + "/run/presets/" + pid + "/delete",
	} {
		code, _ := doPresetPost(t, srv, p, url.Values{
			presetIDField: {pid}, presetNameField: {"stolen"},
			"wp_node": {"6"}, "wp_widget": {"0"}, "wp_value": {"HOSTILE"},
		}, true)
		if code != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404", p, code)
		}
	}
	// Fork from a foreign preset is refused too.
	code, _ := doPresetPost(t, srv, "/workflows/"+aid+"/run/presets",
		url.Values{presetFromField: {pid}}, true)
	if code != http.StatusNotFound {
		t.Errorf("fork from a foreign preset: status = %d, want 404", code)
	}

	got, _ := srv.store.GetRunPreset(context.Background(), bp)
	if got.Name != "B-preset" || !strings.Contains(got.Params, "B-VALUE") {
		t.Errorf("workflow B's preset was modified through workflow A: %+v", got)
	}
	if n, _ := srv.store.CountRunPresets(context.Background(), a.ID); n != 0 {
		t.Errorf("workflow A gained %d presets from a cross-workflow request", n)
	}
}

// TestPresetCrossWorkflowFormIDRejected: the OUTGOING preset id travels in the
// form, not the path — it needs the same ownership check.
func TestPresetCrossWorkflowFormIDRejected(t *testing.T) {
	srv := newTestServer(t)
	a := seedPresetWorkflow(t, srv, "A", presetUIGraph)
	b := seedPresetWorkflow(t, srv, "B", presetUIGraph)
	ap := seedPreset(t, srv, a, "A-preset", a.GraphHash, func(ri comfy.RunInput) string { return ri.Current })
	bp := seedPreset(t, srv, b, "B-preset", b.GraphHash, func(ri comfy.RunInput) string { return "B-VALUE" })

	code, _ := doPresetPost(t, srv,
		"/workflows/"+strconv.FormatInt(a.ID, 10)+"/run/presets/"+strconv.FormatInt(ap, 10)+"/activate",
		url.Values{
			presetIDField: {strconv.FormatInt(bp, 10)}, // ← another workflow's preset
			"wp_node":     {"6"}, "wp_widget": {"0"}, "wp_value": {"HOSTILE"},
		}, true)
	if code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", code)
	}
	got, _ := srv.store.GetRunPreset(context.Background(), bp)
	if strings.Contains(got.Params, "HOSTILE") {
		t.Errorf("workflow B's preset was written through workflow A: %s", got.Params)
	}
}

// TestPresetsAreUIFormatOnly: an api-format workflow keeps today's single Run
// button — no tabs, no Fork, and the create endpoint 404s.
func TestPresetsAreUIFormatOnly(t *testing.T) {
	srv := newTestServer(t)
	wf := store.Workflow{
		Name: "api", Format: store.WorkflowFormatAPI,
		Graph:  `{"3":{"class_type":"KSampler","inputs":{"seed":1}}}`,
		Source: store.WorkflowSourceImported,
	}
	id, err := srv.store.InsertWorkflow(context.Background(), &wf)
	if err != nil {
		t.Fatal(err)
	}
	cur, _ := srv.store.GetWorkflow(context.Background(), id)

	v := srv.buildPresetView(context.Background(), cur, 0, nil, true)
	if v.UIFormat {
		t.Error("UIFormat = true for an api graph")
	}
	if runPresetPanel(cur, "tok", v) != nil {
		t.Error("an api workflow must render no preset panel")
	}
	code, _ := doPresetPost(t, srv, "/workflows/"+strconv.FormatInt(id, 10)+"/run/presets",
		url.Values{}, true)
	if code != http.StatusNotFound {
		t.Errorf("create on an api workflow: status = %d, want 404", code)
	}
}

// TestPresetSubmitAllowListUnchanged is the second, STRUCTURAL guard: a
// hand-built request naming a widget outside the curated editable set stores
// nothing for that key, no matter what the reconciler did.
func TestPresetSubmitAllowListUnchanged(t *testing.T) {
	srv := newTestServer(t)
	wf := seedPresetWorkflow(t, srv, "t2i", presetUIGraph)
	id := seedPreset(t, srv, wf, "Base", wf.GraphHash, func(ri comfy.RunInput) string { return ri.Current })

	form := url.Values{
		presetIDField: {strconv.FormatInt(id, 10)},
		"wp_node":     {"6", "999"},
		"wp_widget":   {"0", "0"},
		"wp_value":    {"legit", "OUT OF SET"},
	}
	if code, _ := doPresetPost(t, srv,
		"/workflows/"+strconv.FormatInt(wf.ID, 10)+"/run/presets/"+strconv.FormatInt(id, 10)+"/save",
		form, true); code != http.StatusOK {
		t.Fatal("save failed")
	}
	got, _ := srv.store.GetRunPreset(context.Background(), id)
	if strings.Contains(got.Params, "OUT OF SET") {
		t.Errorf("a key outside the curated set was stored: %s", got.Params)
	}
	if !strings.Contains(got.Params, "legit") {
		t.Errorf("the in-set key was dropped: %s", got.Params)
	}
}

// TestPresetTimestampsMove is a small liveness check on the store wiring.
func TestPresetTimestampsMove(t *testing.T) {
	srv := newTestServer(t)
	wf := seedPresetWorkflow(t, srv, "t2i", presetUIGraph)
	id := seedPreset(t, srv, wf, "Base", wf.GraphHash, func(ri comfy.RunInput) string { return ri.Current })
	before, _ := srv.store.GetRunPreset(context.Background(), id)
	if before.CreatedAt.IsZero() || before.UpdatedAt.IsZero() {
		t.Errorf("timestamps not set: %+v", before)
	}
	if time.Since(before.CreatedAt) > time.Minute {
		t.Errorf("created_at looks wrong: %v", before.CreatedAt)
	}
}
