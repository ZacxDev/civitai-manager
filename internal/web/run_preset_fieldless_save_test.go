package web

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/comfy"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// ── The fieldless-save wipe ──────────────────────────────────────────────────
//
// INVARIANT: a write that captured NOTHING must never REPLACE a preset's stored
// values. An empty capture is "this request could not tell me anything", not "the
// user cleared every field" — no control in this panel can produce the latter (a
// text input always submits, empty or not).
//
// Why it is a 🔴 rather than an edge case: presetWriteHash returns the WORKFLOW's
// hash whenever the preset is not drifted, so replacing the entries with nothing
// stamped the empty result with a VALID CURRENT hash. The next open then takes the
// EXACT fast path: no drift, no banner, no drop list — every saved value gone with
// nothing on screen saying so.
//
// And it is reachable through ordinary interaction, not only through a corrupt
// blob: parseWidgetOverridesAgainst returns nil when no posted triple survives its
// allow-list, which happens whenever a page is posting against a graph it was not
// rendered from. Both realistic triggers are modelled below.

// presetUIGraphReimported is what a RE-IMPORT of the same workflow looks like: the
// same kinds of nodes, RENUMBERED. Every key an already-open tab holds (nodes 3, 5,
// 6) therefore resolves to nothing — the stale-tab trigger, in one fixture.
const presetUIGraphReimported = `{
  "nodes": [
    {"id": 11, "type": "CLIPTextEncode", "title": "POSITIVE",
     "widgets_values": ["a different scene"], "inputs": []},
    {"id": 12, "type": "KSampler",
     "widgets_values": [42, "randomize", 25, 7.0, "euler", "normal", 1.0], "inputs": []}
  ],
  "links": []
}`

// staleTabForm is the post an OPEN tab issues: keys captured from the graph it was
// rendered against, which is no longer the graph the server holds.
func staleTabForm(t *testing.T, activeID int64) url.Values {
	t.Helper()
	return url.Values{
		presetIDField:   {strconv.FormatInt(activeID, 10)},
		presetNameField: {"Base"},
		"wp_node":       {"6", "3"},
		"wp_widget": {
			widgetOf(t, presetUIGraph, "6", "text"),
			widgetOf(t, presetUIGraph, "3", "steps"),
		},
		"wp_value": {"typed on the stale page", "31"},
	}
}

// TestFieldlessSaveNeverWipesStoredValues is the 🔴 regression test. It asserts the
// STORED params after the write, not the response: the whole failure mode is that
// the response looks perfectly normal.
func TestFieldlessSaveNeverWipesStoredValues(t *testing.T) {
	t.Run("stale tab, drifted preset", func(t *testing.T) {
		srv := newTestServer(t)
		wf := seedPresetWorkflow(t, srv, "t2i", presetUIGraph)
		pid := seedPreset(t, srv, wf, "Base", wf.GraphHash,
			func(ri comfy.RunInput) string { return "SAVED-" + ri.InputName })
		before, err := srv.store.GetRunPreset(context.Background(), pid)
		if err != nil {
			t.Fatal(err)
		}

		// The workflow is re-imported under the open tab: same nodes, new ids.
		cur := replaceGraph(t, srv, wf.ID, presetUIGraphReimported)
		if cur.GraphHash == wf.GraphHash {
			t.Fatal("fixture: the graph hash did not move")
		}

		code, body := doPresetPost(t, srv,
			"/workflows/"+strconv.FormatInt(wf.ID, 10)+"/run/presets/"+
				strconv.FormatInt(pid, 10)+"/save", staleTabForm(t, pid), true)
		if code != http.StatusOK {
			t.Fatalf("save = %d: %s", code, body)
		}

		assertPresetIntact(t, srv, pid, before)
		if !strings.Contains(body, presetNoFieldsNotice) {
			t.Errorf("a save that captured nothing must SAY so:\n%s", body)
		}
	})

	t.Run("mode picker race, hash still matches", func(t *testing.T) {
		// The mode <select>'s re-render of #run-params is asynchronous. Clicking Save
		// before it lands posts the NEW mode_key with the OLD mode's fields — and the
		// allow-list is derived from the mode-applied graph, so not one of them
		// survives. The preset is NOT drifted here, which is exactly why the wipe used
		// to be stamped with a valid hash and vanished without a banner.
		srv := newTestServer(t)
		wf := seedPresetWorkflow(t, srv, "tmpl", presetModeUIGraph)
		selKey, modeA, modeB := presetModeKeys(t, wf.Graph)
		pid := seedModePreset(t, srv, wf, "Image", wf.GraphHash, selKey, modeA, "SAVED")
		before, err := srv.store.GetRunPreset(context.Background(), pid)
		if err != nil {
			t.Fatal(err)
		}
		if !presetHashMatch(before, wf) {
			t.Fatal("fixture: this case must run on the NON-drifted path")
		}

		// Mode A's fields (KSampler node 2) posted alongside mode B's key.
		form := url.Values{
			presetIDField:   {strconv.FormatInt(pid, 10)},
			presetNameField: {"Image"},
			modeChoiceField: {modeB},
			"wp_node":       {"2"},
			"wp_widget":     {"0"},
			"wp_value":      {"12345"},
		}
		code, body := doPresetPost(t, srv,
			"/workflows/"+strconv.FormatInt(wf.ID, 10)+"/run/presets/"+
				strconv.FormatInt(pid, 10)+"/save", form, true)
		if code != http.StatusOK {
			t.Fatalf("save = %d: %s", code, body)
		}

		assertPresetIntact(t, srv, pid, before)
		if !strings.Contains(before.Params, modeA) {
			t.Fatalf("fixture: the stored mode pick is missing: %s", before.Params)
		}
	})
}

// assertPresetIntact is the whole point: the stored blob must be untouched, hash
// included, and the values must still come back on the next open.
func assertPresetIntact(t *testing.T, srv *Server, pid int64, before *store.RunPreset) {
	t.Helper()
	got, err := srv.store.GetRunPreset(context.Background(), pid)
	if err != nil {
		t.Fatal(err)
	}
	if got.Params != before.Params {
		t.Errorf("a save that captured NOTHING replaced the stored values:\nbefore: %s\nafter:  %s",
			before.Params, got.Params)
	}
	if got.GraphHash != before.GraphHash {
		t.Errorf("graph_hash moved on a write that stored no new entries: %q → %q",
			before.GraphHash, got.GraphHash)
	}
	if len(presetEntries(got.Params)) == 0 {
		t.Error("every stored value was destroyed")
	}
}

// TestNameOnlySaveKeepsStoredValues: carrying the old entries through must not
// turn into "skip the write" — a save that only renames the tab still has to
// rename it, and must not touch the values or the hash.
func TestNameOnlySaveKeepsStoredValues(t *testing.T) {
	srv := newTestServer(t)
	wf := seedPresetWorkflow(t, srv, "t2i", presetUIGraph)
	pid := seedPreset(t, srv, wf, "Base", wf.GraphHash,
		func(ri comfy.RunInput) string { return "SAVED-" + ri.InputName })
	before, _ := srv.store.GetRunPreset(context.Background(), pid)

	code, body := doPresetPost(t, srv,
		"/workflows/"+strconv.FormatInt(wf.ID, 10)+"/run/presets/"+
			strconv.FormatInt(pid, 10)+"/save",
		url.Values{
			presetIDField:   {strconv.FormatInt(pid, 10)},
			presetNameField: {"Renamed"},
		}, true)
	if code != http.StatusOK {
		t.Fatalf("save = %d: %s", code, body)
	}

	got, _ := srv.store.GetRunPreset(context.Background(), pid)
	if got.Name != "Renamed" {
		t.Errorf("name = %q, want the posted one", got.Name)
	}
	if got.Params != before.Params || got.GraphHash != before.GraphHash {
		t.Errorf("a rename touched the values/hash:\nbefore: %s %q\nafter:  %s %q",
			before.Params, before.GraphHash, got.Params, got.GraphHash)
	}
	// A form that carried no parameter controls at all is not the out-of-date case,
	// so it must not be reported as one.
	if strings.Contains(body, presetNoFieldsNotice) {
		t.Errorf("a name-only save must not claim the page is out of date:\n%s", body)
	}
}

// TestTabSwitchWithUnresolvableFieldsKeepsValues: persistOutgoing writes through
// the same rule. A stale tab whose keys no longer resolve must not lose the draft
// it is carrying when the user clicks another tab.
func TestTabSwitchWithUnresolvableFieldsKeepsValues(t *testing.T) {
	srv := newTestServer(t)
	wf := seedPresetWorkflow(t, srv, "t2i", presetUIGraph)
	a := seedPreset(t, srv, wf, "A", wf.GraphHash,
		func(ri comfy.RunInput) string { return "SAVED-" + ri.InputName })
	b := seedPreset(t, srv, wf, "B", wf.GraphHash,
		func(ri comfy.RunInput) string { return ri.Current })
	before, _ := srv.store.GetRunPreset(context.Background(), a)

	replaceGraph(t, srv, wf.ID, presetUIGraphReimported)

	code, body := doPresetPost(t, srv,
		"/workflows/"+strconv.FormatInt(wf.ID, 10)+"/run/presets/"+
			strconv.FormatInt(b, 10)+"/activate", staleTabForm(t, a), true)
	if code != http.StatusOK {
		t.Fatalf("activate = %d: %s", code, body)
	}
	assertPresetIntact(t, srv, a, before)
}

// TestSaveStillStoresWhatItCanCapture is the other half of the invariant: the
// carry-through must never swallow a REAL save. One surviving key is a save.
//
// This is ALSO the partial-capture case — one key in, one key out of the curated
// set, against a preset holding the full set — so it asserts the MERGE, not the
// replacement it used to accept. Asserting only "the surviving key was stored" is
// what let the wipe of the other nine pass green through three rounds.
func TestSaveStillStoresWhatItCanCapture(t *testing.T) {
	srv := newTestServer(t)
	wf := seedPresetWorkflow(t, srv, "t2i", presetUIGraph)
	pid := seedPreset(t, srv, wf, "Base", wf.GraphHash,
		func(ri comfy.RunInput) string { return "OLD-" + ri.InputName })
	before := storedValues(t, srv, pid)
	if len(before) < 5 {
		t.Fatalf("fixture: want a multi-value preset, got %d", len(before))
	}

	form := url.Values{
		presetIDField:   {strconv.FormatInt(pid, 10)},
		presetNameField: {"Base"},
		"wp_node":       {"6", "999"},
		"wp_widget":     {"0", "0"},
		"wp_value":      {"NEW TEXT", "OUT OF SET"},
	}
	code, body := doPresetPost(t, srv,
		"/workflows/"+strconv.FormatInt(wf.ID, 10)+"/run/presets/"+
			strconv.FormatInt(pid, 10)+"/save", form, true)
	if code != http.StatusOK {
		t.Fatalf("save = %d: %s", code, body)
	}
	got, _ := srv.store.GetRunPreset(context.Background(), pid)
	if !strings.Contains(got.Params, "NEW TEXT") {
		t.Errorf("a save with one surviving key stored nothing: %s", got.Params)
	}
	if strings.Contains(got.Params, "OUT OF SET") {
		t.Errorf("a key outside the curated set was stored: %s", got.Params)
	}
	if strings.Contains(body, presetNoFieldsNotice) {
		t.Errorf("a save that DID capture values must not report the out-of-date case:\n%s", body)
	}

	// The capture UPDATED its key and every other stored key survived untouched.
	after := storedValues(t, srv, pid)
	prompt := comfy.UIWidgetKey{NodeID: "6", Widget: 0}
	if after[prompt] != "NEW TEXT" {
		t.Errorf("the captured key = %q, want the posted value", after[prompt])
	}
	if len(after) != len(before) {
		t.Fatalf("a one-key save left %d of %d stored values\nbefore: %v\nafter:  %v",
			len(after), len(before), before, after)
	}
	for key, want := range before {
		if key == prompt {
			continue
		}
		if after[key] != want {
			t.Errorf("%v was deleted or altered by a save that never captured it: "+
				"%q → %q", key, want, after[key])
		}
	}
}

// TestModePickerDisablesTheParamsPanelWhileItRefetches closes the race at its
// source: while the picker's GET /run/params is in flight the panel's buttons —
// Save included — are disabled, so the user cannot post the previous mode's fields
// against the new mode_key. The server-side MERGE is still the guarantee; this is
// the mitigation that keeps the user from meeting it.
//
// HONEST LIMIT: this asserts the ATTRIBUTE htmx acts on, not the browser behaviour
// (no browser is available here). htmx collects a request's input values BEFORE it
// disables anything, so the picker's own mode_key still travels.
func TestModePickerDisablesTheParamsPanelWhileItRefetches(t *testing.T) {
	srv := newTestServer(t)
	wf := seedPresetWorkflow(t, srv, "tmpl", presetModeUIGraph)

	got := renderString(t, runModesPanelSelected(wf, "tok", nil))
	want := `hx-disabled-elt="#` + runParamsContainerID + `, #` + runParamsContainerID + ` button"`
	if !strings.Contains(got, want) {
		t.Errorf("the mode picker must disable the parameter panel while it refetches "+
			"(%s missing):\n%s", want, got)
	}
}

// TestModePickerNeverDisablesItself is the 🟡 hazard this selector must not create.
//
// htmx's value collector SKIPS disabled controls, and EVERY run control carries the
// mode picks by hx-include-ing `#run-modes select`. A picker that disabled itself
// would therefore make a Run / Run-again / Run-with-options clicked during the
// panel refetch post with NO mode_key — which converts a multi-mode template with
// every pipeline bypassed and aborts as "nothing to run", or runs the wrong one.
//
// The old value said "this, #run-params button". htmx special-cases "this" only as
// the WHOLE attribute value, so in a comma list it was a raw CSS type selector that
// matched nothing — inert, but one edit away from being the hazard above. Pinned
// here so nobody "fixes" the token into working.
func TestModePickerNeverDisablesItself(t *testing.T) {
	srv := newTestServer(t)
	wf := seedPresetWorkflow(t, srv, "tmpl", presetModeUIGraph)

	got := renderString(t, runModesPanelSelected(wf, "tok", nil))
	sel := disabledEltOf(t, got)
	for _, forbidden := range []string{"this", "select", "#" + runModesContainerID, "cm-mode-"} {
		if strings.Contains(sel, forbidden) {
			t.Errorf("hx-disabled-elt %q names %q: a disabled mode <select> drops out of "+
				"every run control's hx-include, so a concurrent Run would post no %s",
				sel, forbidden, modeChoiceField)
		}
	}
	// Every token must be a plain CSS selector rooted at the stable params container,
	// so htmx resolves them through querySelectorAll and nothing else.
	for _, tok := range strings.Split(sel, ",") {
		if !strings.HasPrefix(strings.TrimSpace(tok), "#"+runParamsContainerID) {
			t.Errorf("hx-disabled-elt token %q is not scoped to #%s: %q",
				tok, runParamsContainerID, sel)
		}
	}
	// The picker's OWN request still carries its mode_key: the select keeps its name
	// and is not in the disabled set.
	if !strings.Contains(got, `name="`+modeChoiceField+`"`) {
		t.Errorf("the picker must still submit %s:\n%s", modeChoiceField, got)
	}

	// And the reason it matters: a run control's mode_key comes from that very select
	// by hx-include, so disabling it would silently empty the run's mode selection.
	selKey, modeA, _ := presetModeKeys(t, wf.Graph)
	v := srv.buildPresetView(context.Background(), wf, 0, map[string]string{selKey: modeA}, false)
	panel := renderString(t, runParamsBody(wf, "tok", v))
	if !strings.Contains(panel, `hx-include="`+runModesInclude+`"`) &&
		!strings.Contains(panel, runModesInclude+`"`) {
		t.Errorf("the run control must hx-include %q — that is how a Run carries %s:\n%s",
			runModesInclude, modeChoiceField, panel)
	}
}

// TestModePickerDisabledSelectorAlwaysMatches: htmx logs 'The selector … returned
// no matches!' and disables NOTHING when a selector matches nothing. "#run-params
// button" alone matches nothing exactly when a multi-mode template has no mode
// picked yet — i.e. the first time the picker is ever used — so the stable
// container is included to guarantee a match.
func TestModePickerDisabledSelectorAlwaysMatches(t *testing.T) {
	srv := newTestServer(t)
	wf := seedPresetWorkflow(t, srv, "tmpl", presetModeUIGraph)

	// No mode picked: DetectRunInputs surfaces nothing, runPresetPanel returns nil,
	// and the params body is an empty div carrying no buttons at all.
	v := srv.buildPresetView(context.Background(), wf, 0, nil, true)
	if len(v.Rec.Fields) != 0 {
		t.Fatalf("fixture: want an empty panel, got %d fields", len(v.Rec.Fields))
	}
	if body := renderString(t, runParamsBody(wf, "tok", v)); strings.Contains(body, "<button") {
		t.Fatalf("fixture: the empty panel is supposed to have no buttons:\n%s", body)
	}

	sel := disabledEltOf(t, renderString(t, runModesPanelSelected(wf, "tok", nil)))
	if !strings.Contains(sel, "#"+runParamsContainerID+",") &&
		!strings.HasSuffix(sel, "#"+runParamsContainerID) {
		t.Errorf("hx-disabled-elt %q must name the always-present #%s container, or it "+
			"matches nothing (and disables nothing) before a mode is picked",
			sel, runParamsContainerID)
	}
}

// disabledEltOf extracts the rendered hx-disabled-elt value.
func disabledEltOf(t *testing.T, markup string) string {
	t.Helper()
	const attr = `hx-disabled-elt="`
	i := strings.Index(markup, attr)
	if i < 0 {
		t.Fatalf("no hx-disabled-elt in:\n%s", markup)
	}
	rest := markup[i+len(attr):]
	j := strings.Index(rest, `"`)
	if j < 0 {
		t.Fatalf("unterminated hx-disabled-elt in:\n%s", markup)
	}
	return rest[:j]
}
