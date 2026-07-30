package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/comfy"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// ── The partial-capture wipe ─────────────────────────────────────────────────
//
// INVARIANT: a write must MERGE its capture into the preset's stored values, never
// replace them. A preset holds the FULL run-parameter set (design decision 4), but a
// save can only ever capture WHAT IS ON SCREEN, and which parameters are on screen
// depends on the selected workflow mode. presetEntriesFromForm returns the
// INTERSECTION of the posted keys and the live keys.
//
// Full-set storage + partial-set capture + wholesale replacement is structural loss,
// and the PARTIAL case is quieter than the zero case the previous round fixed: with
// 0 < captured < stored the preset is usually NOT drifted, so presetWriteHash stamps
// the workflow's CURRENT hash over the truncated set and the next open takes the
// EXACT fast path — Applied=1, Dropped=0, NewInputs=[], NeedsBanner=false (NewInputs
// is only appended when !hashMatch, comfy/run_presets.go). The loss is invisible.

// presetPartialModeGraph is the shape of a real multi-mode template pack: two
// mutually-exclusive pipelines declared by an ACTIVE rgthree bypasser, PLUS a node
// that sits outside both group boxes and is therefore live under either mode. That
// shared node is what makes a cross-mode save capture SOMETHING (so the previous
// round's len(entries)==0 guard does not fire) while still missing most of the set.
const presetPartialModeGraph = `{
  "nodes": [
    {"id": 1, "type": "Fast Groups Bypasser (rgthree)", "mode": 0, "pos": [0,0], "size": [100,50],
     "properties": {"matchColors": "purple", "matchTitle": "", "toggleRestriction": "max one"}},
    {"id": 2, "type": "KSampler", "mode": 4, "pos": [210,210], "size": [100,100],
     "widgets_values": [777, "randomize", 30, 7.5, "euler", "normal", 1.0], "inputs": []},
    {"id": 3, "type": "CLIPTextEncode", "title": "V2V", "mode": 4, "pos": [610,210], "size": [100,100],
     "widgets_values": ["video prompt"], "inputs": []},
    {"id": 4, "type": "CLIPTextEncode", "title": "SHARED POSITIVE", "mode": 0, "pos": [10,610], "size": [100,100],
     "widgets_values": ["a shared prompt"], "inputs": []}
  ],
  "links": [],
  "groups": [
    {"title": "TEXT2IMAGE", "bounding": [200,200,200,200], "color": "#a1309b"},
    {"title": "IMAGE2VIDEO", "bounding": [600,200,200,200], "color": "#a1309b"}
  ]
}`

// storedValues reads a preset's stored entries as key → value, so a test asserts the
// STORED blob rather than the response (the whole failure mode is a normal-looking
// response over a truncated store).
func storedValues(t *testing.T, srv *Server, pid int64) map[comfy.UIWidgetKey]string {
	t.Helper()
	p, err := srv.store.GetRunPreset(context.Background(), pid)
	if err != nil {
		t.Fatal(err)
	}
	out := map[comfy.UIWidgetKey]string{}
	for _, e := range presetEntries(p.Params) {
		if e.Malformed {
			continue
		}
		out[comfy.UIWidgetKey{NodeID: e.NodeID, Widget: e.Widget}] = e.Value
	}
	return out
}

// formForInputs builds the post an OPEN panel issues for the given live inputs: one
// (wp_node, wp_widget, wp_value) triple per rendered field, exactly as the panel
// renders them.
func formForInputs(pid int64, name string, inputs []comfy.RunInput, value func(comfy.RunInput) string) url.Values {
	form := url.Values{
		presetIDField:   {strconv.FormatInt(pid, 10)},
		presetNameField: {name},
	}
	for _, ri := range inputs {
		form.Add("wp_node", ri.NodeID)
		form.Add("wp_widget", strconv.Itoa(ri.WidgetIndex))
		form.Add("wp_value", value(ri))
	}
	return form
}

func liveInputs(graph string, modes map[string]string) []comfy.RunInput {
	g := json.RawMessage(graph)
	if len(modes) > 0 {
		g = comfy.ApplyModeSelection(g, modes)
	}
	return comfy.DetectRunInputs(g, nil)
}

func savePreset(t *testing.T, srv *Server, wf *store.Workflow, pid int64, form url.Values) string {
	t.Helper()
	code, body := doPresetPost(t, srv,
		"/workflows/"+strconv.FormatInt(wf.ID, 10)+"/run/presets/"+
			strconv.FormatInt(pid, 10)+"/save", form, true)
	if code != http.StatusOK {
		t.Fatalf("save = %d: %s", code, body)
	}
	return body
}

// TestPartialCaptureNeverDeletesTheRest is the 🔴 regression test. Both halves fail
// against wholesale replacement and pass against the merge.
func TestPartialCaptureNeverDeletesTheRest(t *testing.T) {
	t.Run("multi-mode: saving under mode B keeps mode A's values", func(t *testing.T) {
		srv := newTestServer(t)
		wf := seedPresetWorkflow(t, srv, "tmpl", presetPartialModeGraph)
		selKey, modeA, modeB := presetModeKeys(t, wf.Graph)

		// A preset holding the FULL set for mode A: the KSampler's parameters plus the
		// shared node that lives outside both groups.
		modesA := map[string]string{selKey: modeA}
		pid := seedModePreset(t, srv, wf, "Image", wf.GraphHash, selKey, modeA, "SAVED-A")
		before := storedValues(t, srv, pid)
		if len(before) < 3 {
			t.Fatalf("fixture: want a multi-value mode-A preset, got %d", len(before))
		}
		shared := comfy.UIWidgetKey{NodeID: "4", Widget: 0}
		if _, ok := before[shared]; !ok {
			t.Fatalf("fixture: the shared out-of-group node is not in the preset: %v", before)
		}

		// The mode-change race, exactly as it reaches the server: the picker already
		// reads mode B, the fields on screen are still mode A's. Every mode-A-only key
		// is rejected by the mode-B allow-list; the shared node's key survives.
		form := formForInputs(pid, "Image", liveInputs(wf.Graph, modesA),
			func(ri comfy.RunInput) string { return "TYPED-" + ri.InputName })
		form.Set(modeChoiceField, modeB)
		savePreset(t, srv, wf, pid, form)

		after := storedValues(t, srv, pid)
		if len(after) != len(before) {
			t.Fatalf("a partial capture deleted %d of %d stored values\nbefore: %v\nafter:  %v",
				len(before)-len(after), len(before), before, after)
		}
		if got := after[shared]; got != "TYPED-text" {
			t.Errorf("the CAPTURED key must win: shared value = %q, want the posted one", got)
		}
		for key, want := range before {
			if key == shared {
				continue
			}
			if got, ok := after[key]; !ok || got != want {
				t.Errorf("mode A's %v was lost or altered: %q → %q (present=%v)",
					key, want, got, ok)
			}
		}

		// And the state the wipe used to hide: reopening under mode A must show every
		// mode-A value back, FROM THE PRESET. Under replacement this render was the
		// "false clean" — Applied=1, Dropped=0, no banner, every KSampler field silently
		// showing the graph's own value.
		v := srv.buildPresetView(context.Background(), wf, pid, modesA, false)
		if v.Rec.Applied() != len(before) {
			t.Errorf("reopening under mode A applied %d of %d saved values (drift report: "+
				"exact=%v dropped=%d new=%v)", v.Rec.Applied(), len(before),
				v.Rec.Exact, len(v.Rec.Dropped), v.Rec.NewInputs)
		}
		for _, f := range v.Rec.Fields {
			if !f.FromPreset {
				t.Errorf("field %s.%s fell back to the graph's value %q — its saved value "+
					"was destroyed", f.Input.NodeID, f.Input.InputName, f.Value)
			}
		}
	})

	t.Run("single-mode: one posted field does not delete the other nine", func(t *testing.T) {
		srv := newTestServer(t)
		wf := seedPresetWorkflow(t, srv, "t2i", presetUIGraph)
		pid := seedPreset(t, srv, wf, "Base", wf.GraphHash,
			func(ri comfy.RunInput) string { return "OLD-" + ri.InputName })
		before := storedValues(t, srv, pid)
		if len(before) < 5 {
			t.Fatalf("fixture: want a multi-value preset, got %d", len(before))
		}

		// One field posted. (A hand-built request, a partially-rendered panel, or any
		// caller that does not send the whole set.)
		prompt := comfy.UIWidgetKey{NodeID: "6", Widget: 0}
		savePreset(t, srv, wf, pid, url.Values{
			presetIDField:   {strconv.FormatInt(pid, 10)},
			presetNameField: {"Base"},
			"wp_node":       {prompt.NodeID},
			"wp_widget":     {strconv.Itoa(prompt.Widget)},
			"wp_value":      {"NEW TEXT"},
		})

		after := storedValues(t, srv, pid)
		if len(after) != len(before) {
			t.Fatalf("a one-key post left %d of %d stored values\nbefore: %v\nafter:  %v",
				len(after), len(before), before, after)
		}
		if after[prompt] != "NEW TEXT" {
			t.Errorf("the captured key was not updated: %q", after[prompt])
		}
		for key, want := range before {
			if key == prompt {
				continue
			}
			if after[key] != want {
				t.Errorf("%v changed on a write that never captured it: %q → %q",
					key, want, after[key])
			}
		}

		// No false-clean state: every value still comes back on the next open.
		v := srv.buildPresetView(context.Background(), wf, pid, nil, true)
		if v.Rec.Applied() != len(before) || v.Rec.NeedsBanner() {
			t.Errorf("reopen: applied %d of %d, banner=%v (dropped=%d new=%v)",
				v.Rec.Applied(), len(before), v.Rec.NeedsBanner(),
				len(v.Rec.Dropped), v.Rec.NewInputs)
		}
		if got, ok := fieldValue(v.Rec, "6", "text"); !ok || got != "NEW TEXT" {
			t.Errorf("the edited field = %q (fromPreset=%v), want the saved text", got, ok)
		}
	})
}

// TestClearingAFieldStillClearsIt: the carry-forward must not resurrect a value the
// user deliberately emptied. A text input submits whether or not it holds text, so a
// cleared field IS a capture — and a capture wins for its own key.
func TestClearingAFieldStillClearsIt(t *testing.T) {
	srv := newTestServer(t)
	wf := seedPresetWorkflow(t, srv, "t2i", presetUIGraph)
	pid := seedPreset(t, srv, wf, "Base", wf.GraphHash,
		func(ri comfy.RunInput) string { return "OLD-" + ri.InputName })

	prompt := comfy.UIWidgetKey{NodeID: "6", Widget: 0}
	form := formForInputs(pid, "Base", liveInputs(wf.Graph, nil), func(ri comfy.RunInput) string {
		if ri.NodeID == prompt.NodeID && ri.WidgetIndex == prompt.Widget {
			return "" // the user selected the prompt and pressed delete
		}
		return "OLD-" + ri.InputName
	})
	savePreset(t, srv, wf, pid, form)

	after := storedValues(t, srv, pid)
	if v, ok := after[prompt]; !ok || v != "" {
		t.Errorf("the cleared field = %q (present=%v), want an empty stored value — the "+
			"merge must not carry the old text back", v, ok)
	}
	v := srv.buildPresetView(context.Background(), wf, pid, nil, true)
	if got, fromPreset := fieldValue(v.Rec, "6", "text"); got != "" || !fromPreset {
		t.Errorf("reopening re-filled the cleared field: %q (fromPreset=%v)", got, fromPreset)
	}
}

// TestMergeNeverStampsAHashThatMisdescribesTheEntries is the invariant an earlier
// round established, re-checked against the merge: a NON-BLANK stored graph_hash is
// a claim that every stored entry was captured against the graph with that hash,
// because the EXACT read path applies everything it covers with no per-entry check.
//
// The merge introduces entries the write did not capture, so the claim has to be
// re-earned in each of the three write shapes.
func TestMergeNeverStampsAHashThatMisdescribesTheEntries(t *testing.T) {
	t.Run("hash already matched: the same hash still describes the merged set", func(t *testing.T) {
		srv := newTestServer(t)
		wf := seedPresetWorkflow(t, srv, "t2i", presetUIGraph)
		pid := seedPreset(t, srv, wf, "Base", wf.GraphHash,
			func(ri comfy.RunInput) string { return "OLD-" + ri.InputName })

		savePreset(t, srv, wf, pid, url.Values{
			presetIDField:   {strconv.FormatInt(pid, 10)},
			presetNameField: {"Base"},
			"wp_node":       {"6"},
			"wp_widget":     {"0"},
			"wp_value":      {"NEW"},
		})

		got, _ := srv.store.GetRunPreset(context.Background(), pid)
		if got.GraphHash != wf.GraphHash {
			t.Fatalf("graph_hash = %q, want the workflow's %q", got.GraphHash, wf.GraphHash)
		}
		// The carried entries were captured under this very hash (the preset already
		// carried it), so every stored key is a live key of this graph.
		assertEveryEntryLive(t, wf, got, nil)
	})

	t.Run("drifted, no adopt: the stamp is BLANK, so no claim is made", func(t *testing.T) {
		srv := newTestServer(t)
		wf := seedPresetWorkflow(t, srv, "t2i", presetUIGraph)
		pid := seedPreset(t, srv, wf, "Base", wf.GraphHash,
			func(ri comfy.RunInput) string { return "OLD-" + ri.InputName })
		cur := replaceGraph(t, srv, wf.ID, presetUIGraphShifted)

		// One field of the NEW graph posted; the stored set belongs to the OLD one.
		savePreset(t, srv, cur, pid, url.Values{
			presetIDField:   {strconv.FormatInt(pid, 10)},
			presetNameField: {"Base"},
			"wp_node":       {"6"},
			"wp_widget":     {"0"},
			"wp_value":      {"NEW"},
		})

		got, _ := srv.store.GetRunPreset(context.Background(), pid)
		if got.GraphHash != "" {
			t.Fatalf("graph_hash = %q — a merge of old-graph entries must not be "+
				"certified against the new graph", got.GraphHash)
		}
		// Blank can never be proven equal, so the next open takes the per-entry tuple
		// path: mismatched carried entries are dropped and NAMED, never applied blind.
		v := srv.buildPresetView(context.Background(), cur, pid, nil, true)
		if v.Rec.Exact {
			t.Error("a blank hash must not read as EXACT")
		}
		if !v.Drifted || !v.Rec.NeedsBanner() {
			t.Errorf("the carried old-graph values must be reported: drifted=%v banner=%v",
				v.Drifted, v.Rec.NeedsBanner())
		}
	})

	t.Run("adopt: certifies ONLY what it captured, and says what it dropped", func(t *testing.T) {
		srv := newTestServer(t)
		wf := seedPresetWorkflow(t, srv, "tmpl", presetPartialModeGraph)
		selKey, modeA, modeB := presetModeKeys(t, wf.Graph)
		pid := seedModePreset(t, srv, wf, "Image", "STALEHASH", selKey, modeA, "SAVED-A")

		// Drifted (the stored hash names an older graph) and the user is on mode B, so
		// mode A's values are off-screen — the exact set an adoption cannot vouch for.
		p, _ := srv.store.GetRunPreset(context.Background(), pid)
		if presetHashMatch(p, wf) {
			t.Fatal("fixture: this case must run on the DRIFTED path")
		}
		modesB := map[string]string{selKey: modeB}
		form := formForInputs(pid, "Image", liveInputs(wf.Graph, modesB),
			func(ri comfy.RunInput) string { return "TYPED-" + ri.InputName })
		form.Set(modeChoiceField, modeB)
		form.Set(presetAdoptField, "1")
		body := savePreset(t, srv, wf, pid, form)

		got, _ := srv.store.GetRunPreset(context.Background(), pid)
		if got.GraphHash != wf.GraphHash {
			t.Fatalf("the adoption did not stamp: %q", got.GraphHash)
		}
		// THE INVARIANT: every entry now covered by that hash is a live key of the
		// graph it names, under the modes it was captured with.
		assertEveryEntryLive(t, wf, got, modesB)
		// And the loss is stated, not silent — those values were already named as
		// drops in the banner the user adopted from.
		if !strings.Contains(body, asRendered(t, "were not kept")) {
			t.Errorf("an adoption that discarded values must say so:\n%s", body)
		}
	})
}

// assertEveryEntryLive is the machine-checkable form of "the hash describes the
// entries": under a non-blank stamp, every stored positional key must resolve to a
// live input of that graph whose drift tuple still agrees — i.e. exactly what the
// EXACT read path assumes without checking.
func assertEveryEntryLive(t *testing.T, wf *store.Workflow, p *store.RunPreset, modes map[string]string) {
	t.Helper()
	if p.GraphHash == "" {
		t.Fatal("assertEveryEntryLive is only meaningful for a stamped preset")
	}
	if p.GraphHash != wf.GraphHash {
		t.Fatalf("stamped %q, workflow %q", p.GraphHash, wf.GraphHash)
	}
	live := map[comfy.UIWidgetKey]comfy.RunInput{}
	for _, ri := range liveInputs(wf.Graph, modes) {
		live[comfy.UIWidgetKey{NodeID: ri.NodeID, Widget: ri.WidgetIndex}] = ri
	}
	for _, e := range presetEntries(p.Params) {
		key := comfy.UIWidgetKey{NodeID: e.NodeID, Widget: e.Widget}
		ri, ok := live[key]
		if !ok {
			t.Errorf("stored entry %v is certified by %q but is not a live input of that "+
				"graph — the EXACT path would apply it with no check", key, p.GraphHash)
			continue
		}
		if e.Kind != ri.Kind || e.ClassType != ri.SourceClassType || e.InputName != ri.InputName {
			t.Errorf("stored entry %v has tuple (%s,%s,%s) but the live input is "+
				"(%s,%s,%s) — the stamp certifies a retarget", key,
				e.Kind, e.ClassType, e.InputName, ri.Kind, ri.SourceClassType, ri.InputName)
		}
	}
}

// TestTabSwitchMergesToo: persistOutgoing writes through the same rule, so a mode
// change followed by a tab click must not truncate the outgoing tab either.
func TestTabSwitchMergesToo(t *testing.T) {
	srv := newTestServer(t)
	wf := seedPresetWorkflow(t, srv, "tmpl", presetPartialModeGraph)
	selKey, modeA, modeB := presetModeKeys(t, wf.Graph)
	a := seedModePreset(t, srv, wf, "A", wf.GraphHash, selKey, modeA, "SAVED-A")
	b := seedModePreset(t, srv, wf, "B", wf.GraphHash, selKey, modeB, "SAVED-B")
	before := storedValues(t, srv, a)

	form := formForInputs(a, "A", liveInputs(wf.Graph, map[string]string{selKey: modeA}),
		func(ri comfy.RunInput) string { return "TYPED-" + ri.InputName })
	form.Set(modeChoiceField, modeB)
	code, body := doPresetPost(t, srv,
		"/workflows/"+strconv.FormatInt(wf.ID, 10)+"/run/presets/"+
			strconv.FormatInt(b, 10)+"/activate", form, true)
	if code != http.StatusOK {
		t.Fatalf("activate = %d: %s", code, body)
	}

	after := storedValues(t, srv, a)
	if len(after) != len(before) {
		t.Errorf("switching tabs truncated the outgoing tab: %d → %d\nbefore: %v\nafter:  %v",
			len(before), len(after), before, after)
	}
}

// TestMalformedStoredEntrySurvivesAMergeUnrepaired: a stored entry whose slot index
// cannot be decoded is carried across a merge BYTE FOR BYTE. Re-serializing it
// through an int would aim it at slot 0 of its node — a plausible-looking silent
// retarget, which is the one thing the reconciler must never be handed.
func TestMalformedStoredEntrySurvivesAMergeUnrepaired(t *testing.T) {
	srv := newTestServer(t)
	wf := seedPresetWorkflow(t, srv, "t2i", presetUIGraph)
	pid := seedRawPreset(t, srv, wf, "Broken", wf.GraphHash, `{"ui_widget_overrides":[
	  {"node_id":"6","widget":"x","value":"UNREADABLE"},
	  {"node_id":"3","widget":2,"value":"31","kind":"int","class_type":"KSampler","input_name":"steps"}
	]}`)

	savePreset(t, srv, wf, pid, url.Values{
		presetIDField:   {strconv.FormatInt(pid, 10)},
		presetNameField: {"Broken"},
		"wp_node":       {"6"},
		"wp_widget":     {"0"},
		"wp_value":      {"NEW TEXT"},
	})

	got, _ := srv.store.GetRunPreset(context.Background(), pid)
	var malformed, repaired int
	for _, e := range presetEntries(got.Params) {
		if e.Malformed {
			malformed++
			if e.Value != "UNREADABLE" {
				t.Errorf("the malformed entry's value changed: %q", e.Value)
			}
			continue
		}
		if e.NodeID == "6" && e.Widget == 0 && e.Value == "UNREADABLE" {
			repaired++
		}
	}
	if malformed != 1 {
		t.Errorf("the undecodable entry must survive the merge as-is (found %d): %s",
			malformed, got.Params)
	}
	if repaired > 0 {
		t.Errorf("the merge silently aimed an undecodable entry at slot 0: %s", got.Params)
	}
	// The readable ones are still there: the capture updated node 6, the untouched
	// node-3 entry was carried.
	vals := storedValues(t, srv, pid)
	if vals[comfy.UIWidgetKey{NodeID: "6", Widget: 0}] != "NEW TEXT" ||
		vals[comfy.UIWidgetKey{NodeID: "3", Widget: 2}] != "31" {
		t.Errorf("merge lost a readable entry: %v", vals)
	}
}

// TestNoAdoptHashAdviceMatchesWhatTheRowCanDo pins the 🟢 copy fix. Only a workflow
// with a source_path goes through UpsertWorkflowByPath, the one path that refreshes
// graph_hash in place; telling the owner of a pasted/PNG/civitai row to "re-scan the
// workflow" names an action that cannot be performed on it, leaving the adopt button
// permanently inert behind advice that fixes nothing.
func TestNoAdoptHashAdviceMatchesWhatTheRowCanDo(t *testing.T) {
	for _, tc := range []struct {
		name       string
		sourcePath string
		want, deny string
	}{
		{"scanned from disk", "/comfy/user/default/workflows/flow.json",
			"Re-scan the workflow", "nothing to re-scan"},
		{"imported, never on disk", "",
			"nothing to re-scan", "Re-scan the workflow"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := newTestServer(t)
			wf := seedPresetWorkflow(t, srv, "t2i", presetUIGraph)
			if tc.sourcePath != "" {
				if _, err := srv.store.DB().Exec(
					`UPDATE workflows SET source_path = ? WHERE id = ?`, tc.sourcePath, wf.ID); err != nil {
					t.Fatal(err)
				}
			}
			// A pre-0011 row: graph present, content hash never recorded.
			if _, err := srv.store.DB().Exec(
				`UPDATE workflows SET graph_hash = '' WHERE id = ?`, wf.ID); err != nil {
				t.Fatal(err)
			}
			cur, _ := srv.store.GetWorkflow(context.Background(), wf.ID)
			pid := seedPreset(t, srv, cur, "Base", "STALEHASH",
				func(ri comfy.RunInput) string { return ri.Current })

			body := savePreset(t, srv, cur, pid, url.Values{
				presetIDField:    {strconv.FormatInt(pid, 10)},
				presetNameField:  {"Base"},
				presetAdoptField: {"1"},
				"wp_node":        {"6"},
				"wp_widget":      {"0"},
				"wp_value":       {"kept text"},
			})
			if !strings.Contains(body, asRendered(t, tc.want)) {
				t.Errorf("the refusal must offer advice this row can act on (%q):\n%s", tc.want, body)
			}
			if strings.Contains(body, asRendered(t, tc.deny)) {
				t.Errorf("the refusal offers advice this row cannot act on (%q):\n%s", tc.deny, body)
			}
		})
	}
}

// TestGoneDropsGetTheirOwnLine pins the other 🟢 copy fix: a PresetDropGone entry
// produces NO rec.Fields row, so it was neither shown nor reset to anything. It must
// not be listed under "Reset to the workflow's current values" beside drops that
// really were reset.
func TestGoneDropsGetTheirOwnLine(t *testing.T) {
	srv := newTestServer(t)
	wf := seedPresetWorkflow(t, srv, "tmpl", presetPartialModeGraph)
	selKey, modeA, modeB := presetModeKeys(t, wf.Graph)
	pid := seedModePreset(t, srv, wf, "Image", wf.GraphHash, selKey, modeA, "SAVED-A")

	// Reopened under mode B: mode A's keys have no live field at all.
	v := srv.buildPresetView(context.Background(), wf, pid,
		map[string]string{selKey: modeB}, false)
	reset, gone, unreadable := splitDrops(v.Rec.Dropped)
	if len(gone) == 0 || len(reset) != 0 || len(unreadable) != 0 {
		t.Fatalf("fixture: want gone-only drops, got reset=%d gone=%d unreadable=%d",
			len(reset), len(gone), len(unreadable))
	}

	got := renderString(t, runPresetPanel(wf, "tok", v))
	if !strings.Contains(got, asRendered(t, "this workflow has no matching parameter right now")) {
		t.Errorf("a Gone drop needs its own line:\n%s", got)
	}
	if strings.Contains(got, asRendered(t, "Reset to the workflow's current values")) {
		t.Errorf("a Gone drop was not reset to anything — there is no field for it:\n%s", got)
	}
	// Every one of them is still named.
	names := make([]string, 0, len(gone))
	for _, d := range gone {
		names = append(names, d.Name)
		if !strings.Contains(got, "<strong>"+asRendered(t, d.Name)+"</strong>") {
			t.Errorf("gone drop %q was not named:\n%s", d.Name, got)
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		t.Fatal("no gone drops to name")
	}
}
