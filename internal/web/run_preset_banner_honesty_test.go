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
	g "maragu.dev/gomponents"
)

// ── What the banner is allowed to say ────────────────────────────────────────
//
// Every line in the drift banner is a claim about what just happened to the user's
// saved values. Three of them could be false while the code underneath was working
// correctly, which is the worst kind: the mechanism is fine and the sentence lies.

const (
	modeBlameLine   = "no longer apply to the selected workflow mode"
	allStillMatches = "Every saved value still matches"
	unreadableLine  = "could not be read"
)

// asRendered escapes a server-authored string exactly the way the renderer does,
// so an assertion can name the SENTENCE the user reads without hard-coding entity
// escapes (the notices carry quotes and apostrophes).
func asRendered(t *testing.T, s string) string {
	t.Helper()
	return renderString(t, g.Text(s))
}

// seedRawPreset stores a hand-written params blob — the only way to model a row
// that a previous version (or a corrupted write) left behind.
func seedRawPreset(t *testing.T, srv *Server, wf *store.Workflow, name, graphHash, params string) int64 {
	t.Helper()
	id, err := srv.store.CreateRunPreset(context.Background(), store.RunPreset{
		WorkflowID: wf.ID, Name: name, Position: -1, GraphHash: graphHash, Params: params,
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// TestMalformedDropOnExactPathDoesNotBlameTheMode: an equal hash PROVES the graph
// did not move, so a drop on that path used to be titled "some saved values no
// longer apply to the selected workflow mode". An undecodable positional key has
// nothing to do with mode selection — and on a single-mode workflow it points the
// user at a picker that does not exist.
func TestMalformedDropOnExactPathDoesNotBlameTheMode(t *testing.T) {
	srv := newTestServer(t)
	wf := seedPresetWorkflow(t, srv, "t2i", presetUIGraph)
	id := seedRawPreset(t, srv, wf, "Base", wf.GraphHash, `{"ui_widget_overrides":[
	  {"node_id":"3","widget":"not-a-number","value":"BROKEN","label":"Steps"}
	]}`)

	v := srv.buildPresetView(context.Background(), wf, id, nil, true)
	if !v.Rec.Exact || v.Drifted {
		t.Fatalf("fixture: want the EXACT path (exact=%v drifted=%v)", v.Rec.Exact, v.Drifted)
	}
	if !v.Rec.NeedsBanner() {
		t.Fatal("fixture: a malformed entry must still raise the banner")
	}

	got := renderString(t, runPresetPanel(wf, "tok", v))
	if strings.Contains(got, modeBlameLine) {
		t.Errorf("an unreadable saved value must not be blamed on the workflow mode:\n%s", got)
	}
	for _, want := range []string{
		unreadableLine, // its own, accurate title
		"Steps",        // still named
		"Re-enter",     // and says what fixes it
		"0 of 1 saved values were re-applied.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("banner missing %q:\n%s", want, got)
		}
	}
}

// TestExactPathBannerNamesBothCausesWhenBothHappen: a mode-driven drop and an
// unreadable entry in the same render must produce a title that covers both rather
// than silently picking one.
func TestExactPathBannerNamesBothCausesWhenBothHappen(t *testing.T) {
	srv := newTestServer(t)
	wf := seedPresetWorkflow(t, srv, "tmpl", presetModeUIGraph)
	selKey, modeA, _ := presetModeKeys(t, wf.Graph)
	// Values belonging to mode B's node 3, plus one undecodable entry.
	id := seedRawPreset(t, srv, wf, "Video", wf.GraphHash, `{"ui_widget_overrides":[
	  {"node_id":"3","widget":0,"value":"B-VAL","kind":"text","class_type":"CLIPTextEncode","input_name":"text","label":"Prompt (V2V)"},
	  {"node_id":"9","widget":"not-a-number","value":"BROKEN","label":"Steps"}
	]}`)

	// The PICKER selects mode A, so node 3 is bypassed and its value has nowhere to
	// go — a genuine mode-caused drop, on an exact hash.
	v := srv.buildPresetView(context.Background(), wf, id, map[string]string{selKey: modeA}, false)
	if !v.Rec.Exact || len(v.Rec.Dropped) != 2 {
		t.Fatalf("fixture: want two drops on the exact path, got exact=%v drops=%+v",
			v.Rec.Exact, v.Rec.Dropped)
	}

	got := renderString(t, runPresetPanel(wf, "tok", v))
	if !strings.Contains(got, unreadableLine) || !strings.Contains(got, modeBlameLine) {
		t.Errorf("both causes must be named when both occurred:\n%s", got)
	}
	// And they stay in separate lines: the unreadable one is not a "reset to the
	// workflow's current value", it never resolved to a field at all.
	if !strings.Contains(got, "Prompt (V2V)") || !strings.Contains(got, "Steps") {
		t.Errorf("both drops must still be named individually:\n%s", got)
	}
}

// TestZeroEntryPresetNeverClaimsEveryValueStillMatches: "Every saved value still
// matches" is vacuously true of a preset holding nothing, and reads as reassurance
// about values that do not exist. A preset can still legitimately be empty (a
// workflow whose graph exposed no editable inputs when the tab was created), so the
// copy has to be honest rather than merely unreachable.
func TestZeroEntryPresetNeverClaimsEveryValueStillMatches(t *testing.T) {
	srv := newTestServer(t)
	wf := seedPresetWorkflow(t, srv, "t2i", presetUIGraph)
	id := seedRawPreset(t, srv, wf, "Empty", "STALEHASH", `{}`)

	v := srv.buildPresetView(context.Background(), wf, id, nil, true)
	if !v.Drifted || v.Rec.NeedsBanner() || v.Rec.Applied() != 0 {
		t.Fatalf("fixture: want the quiet drift line over an empty preset "+
			"(drifted=%v banner=%v applied=%d)", v.Drifted, v.Rec.NeedsBanner(), v.Rec.Applied())
	}

	got := renderString(t, runPresetPanel(wf, "tok", v))
	if strings.Contains(got, allStillMatches) {
		t.Errorf("an empty preset must not be told its values still match:\n%s", got)
	}
	if !strings.Contains(got, "holds no saved values") {
		t.Errorf("the empty case must say so:\n%s", got)
	}

	// The same over the wire, since that is the byte stream the user reads.
	body := get(t, srv, "/workflows/"+strconv.FormatInt(wf.ID, 10)+"/run/params?"+
		presetIDField+"="+strconv.FormatInt(id, 10)).Body.String()
	if strings.Contains(body, allStillMatches) {
		t.Errorf("GET /run/params renders the false reassurance:\n%s", body)
	}
}

// TestFieldlessSaveNoLongerProducesTheEmptyPreset is the other half of the #3 check
// the re-audit asked for: the fieldless save was the way a NON-empty preset became
// an empty one and then read "Every saved value still matches". With the values
// carried through, that route is gone.
func TestFieldlessSaveNoLongerProducesTheEmptyPreset(t *testing.T) {
	srv := newTestServer(t)
	wf := seedPresetWorkflow(t, srv, "t2i", presetUIGraph)
	pid := seedPreset(t, srv, wf, "Base", wf.GraphHash,
		func(ri comfy.RunInput) string { return "SAVED-" + ri.InputName })
	replaceGraph(t, srv, wf.ID, presetUIGraphReimported)

	wid := strconv.FormatInt(wf.ID, 10)
	sid := strconv.FormatInt(pid, 10)
	code, _ := doPresetPost(t, srv,
		"/workflows/"+wid+"/run/presets/"+sid+"/save", staleTabForm(t, pid), true)
	if code != http.StatusOK {
		t.Fatalf("save = %d", code)
	}

	body := get(t, srv, "/workflows/"+wid+"/run/params?"+presetIDField+"="+sid).Body.String()
	if strings.Contains(body, allStillMatches) {
		t.Errorf("the preset still holds values — this line would be a lie:\n%s", body)
	}
	// The values survived, so the next open reports them as drops against the new
	// graph instead of pretending the preset was always empty.
	if !strings.Contains(body, asRendered(t, "Reset to the workflow's current values")) {
		t.Errorf("the carried-through values must be reconciled and named:\n%s", body)
	}
	got, _ := srv.store.GetRunPreset(context.Background(), pid)
	if len(presetEntries(got.Params)) == 0 {
		t.Errorf("the stored values were lost after all: %s", got.Params)
	}
}

// TestAdoptWithoutAWorkflowHashIsRefusedHonestly pins the #4 decision: adoption is
// REFUSED (with the fix stated) rather than reported as done. graph_hash arrived in
// migration 0011, so a workflow row that has not been re-scanned since carries
// none; stamping it left the preset permanently drifted while the response claimed
// it had adopted the current graph, and the button could be clicked forever.
//
// The button is deliberately still rendered: the drift is real, and a user who
// clicks it now learns what to do (re-scan) instead of finding a control that
// vanished for no stated reason.
func TestAdoptWithoutAWorkflowHashIsRefusedHonestly(t *testing.T) {
	srv := newTestServer(t)
	wf := seedPresetWorkflow(t, srv, "t2i", presetUIGraph)
	pid := seedPreset(t, srv, wf, "Base", "STALEHASH",
		func(ri comfy.RunInput) string { return ri.Current })

	// A pre-0011 workflow row: graph present, content hash never recorded.
	if _, err := srv.store.DB().Exec(`UPDATE workflows SET graph_hash = '' WHERE id = ?`, wf.ID); err != nil {
		t.Fatal(err)
	}
	cur, _ := srv.store.GetWorkflow(context.Background(), wf.ID)
	if cur.GraphHash != "" {
		t.Fatal("fixture: the workflow still has a hash")
	}

	form := url.Values{
		presetIDField:      {strconv.FormatInt(pid, 10)},
		presetNameField:    {"Base"},
		presetAdoptField:   {"1"},
		"wp_node":          {"6"},
		"wp_widget":        {"0"},
		"wp_value":         {"kept text"},
		modeChoiceField:    {""},
		"unused_extra_key": {""},
	}
	code, body := doPresetPost(t, srv,
		"/workflows/"+strconv.FormatInt(wf.ID, 10)+"/run/presets/"+
			strconv.FormatInt(pid, 10)+"/save", form, true)
	if code != http.StatusOK {
		t.Fatalf("save = %d: %s", code, body)
	}

	if strings.Contains(body, "adopted the current graph") {
		t.Errorf("an adoption that stamped nothing must not claim it happened:\n%s", body)
	}
	if !strings.Contains(body, asRendered(t, presetNoAdoptHashNotice)) {
		t.Errorf("the refusal must say what would fix it:\n%s", body)
	}
	got, _ := srv.store.GetRunPreset(context.Background(), pid)
	if got.GraphHash != "" {
		t.Errorf("graph_hash = %q, want blank — there was nothing to stamp", got.GraphHash)
	}
	if !strings.Contains(got.Params, "kept text") {
		t.Errorf("the refused adoption must still have SAVED: %s", got.Params)
	}
	v := srv.buildPresetView(context.Background(), cur, pid, nil, true)
	if !v.Drifted {
		t.Error("the preset must stay drifted — nothing certified it")
	}
}

// TestAdoptFromAnOutOfDatePageIsRefused: adoption certifies the STORED entries
// against the CURRENT graph. A page whose posted keys no longer resolve is showing
// a different graph, so certifying from it is exactly the "certify a param set
// against a graph I did not inspect" hazard decision 7 exists to prevent.
func TestAdoptFromAnOutOfDatePageIsRefused(t *testing.T) {
	srv := newTestServer(t)
	wf := seedPresetWorkflow(t, srv, "t2i", presetUIGraph)
	pid := seedPreset(t, srv, wf, "Base", wf.GraphHash,
		func(ri comfy.RunInput) string { return "SAVED-" + ri.InputName })
	cur := replaceGraph(t, srv, wf.ID, presetUIGraphReimported)

	form := staleTabForm(t, pid)
	form.Set(presetAdoptField, "1")
	code, body := doPresetPost(t, srv,
		"/workflows/"+strconv.FormatInt(wf.ID, 10)+"/run/presets/"+
			strconv.FormatInt(pid, 10)+"/save", form, true)
	if code != http.StatusOK {
		t.Fatalf("save = %d: %s", code, body)
	}
	if strings.Contains(body, "adopted the current graph") {
		t.Errorf("adoption from an out-of-date page must be refused:\n%s", body)
	}
	got, _ := srv.store.GetRunPreset(context.Background(), pid)
	if got.GraphHash == cur.GraphHash {
		t.Errorf("the stale page's click certified the stored values against %q", cur.GraphHash)
	}
	if len(presetEntries(got.Params)) == 0 {
		t.Error("and it must not have wiped them either")
	}
}
