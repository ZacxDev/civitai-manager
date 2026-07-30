package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/comfy"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// ── The mode-selection half of the partial-capture wipe ──────────────────────
//
// Same bug class as the widget merge, one field over. presetParamsFrom REPLACED
// snap.ModeSelection wholesale, so a write whose request carried no mode_key stored
// an empty selection over the user's saved pick — while the widget half of the very
// same write correctly carried its uncaptured entries forward. An internal
// inconsistency too: parseModeChoices' own comment reads `"" (keep as saved)`, and
// the write then did not keep it.
//
// "Captured" means something narrower for a mode than for a widget, and the
// difference is structural rather than a judgement call:
//
//   - a parameter field ALWAYS submits (a text input posts whether or not it holds
//     text), so an absent widget key means the field was not on screen;
//   - a mode <select> submits one value per selector, and parseModeChoices keeps only
//     values naming a real mode of a real selector. No mode_key — absent, blank
//     placeholder, unknown, hostile — is therefore the ABSENCE of a capture, and an
//     explicit live mode key IS the capture.
//
// ⚠ KNOWN LIMIT pinned by TestAModePickCannotBeRemovedByASave: "the user deselected
// the mode" is NOT representable in the posted form, so carry-forward means a stored
// pick can be REPLACED by a save but never REMOVED by one.

// storedModes reads a preset's stored mode selection straight out of the row — the
// whole failure mode is a normal-looking response over a truncated store.
func storedModes(t *testing.T, srv *Server, pid int64) map[string]string {
	t.Helper()
	p, err := srv.store.GetRunPreset(context.Background(), pid)
	if err != nil {
		t.Fatal(err)
	}
	return presetModes(p.Params)
}

// TestSaveWithNoModeKeyKeepsTheStoredModePick is the 🔴 regression test. It fails
// against wholesale replacement (modes=map[1:1:0] → modes=map[]) and passes against
// the merge, on BOTH hash paths.
func TestSaveWithNoModeKeyKeepsTheStoredModePick(t *testing.T) {
	for _, tc := range []struct{ name, seedHash string }{
		{"exact: the stored hash still describes the preset", ""},
		{"drifted: the stored hash names an older graph", "STALEHASH"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := newTestServer(t)
			wf := seedPresetWorkflow(t, srv, "tmpl", presetPartialModeGraph)
			selKey, modeA, _ := presetModeKeys(t, wf.Graph)

			hash := tc.seedHash
			if hash == "" {
				hash = wf.GraphHash
			}
			pid := seedModePreset(t, srv, wf, "Image", hash, selKey, modeA, "SAVED-A")
			beforeModes := storedModes(t, srv, pid)
			beforeVals := storedValues(t, srv, pid)
			if beforeModes[selKey] != modeA {
				t.Fatalf("fixture: seeded mode = %v, want %s=%s", beforeModes, selKey, modeA)
			}

			// The save the probe measured: fields captured, NO mode_key posted at all.
			// liveInputs(graph, nil) is what an all-bypassed template exposes — the node
			// outside both group boxes — so the capture is non-empty and the empty-capture
			// early return in presetEntryWrite does not fire.
			form := formForInputs(pid, "Image", liveInputs(wf.Graph, nil),
				func(ri comfy.RunInput) string { return "TYPED-" + ri.InputName })
			if len(form["wp_node"]) == 0 {
				t.Fatal("fixture: this save must capture at least one widget value")
			}
			if _, posted := form[modeChoiceField]; posted {
				t.Fatal("fixture: this save must post NO mode_key")
			}
			savePreset(t, srv, wf, pid, form)

			after := storedModes(t, srv, pid)
			if after[selKey] != modeA {
				t.Fatalf("a save that captured widget values but posted no mode_key lost the "+
					"stored mode pick: %v → %v", beforeModes, after)
			}
			if len(after) != len(beforeModes) {
				t.Errorf("the stored selection changed size: %v → %v", beforeModes, after)
			}
			// The widget half must not regress either — this write goes through the same
			// merge, and both families have to survive it together.
			if got := storedValues(t, srv, pid); len(got) != len(beforeVals) {
				t.Errorf("the widget entries were truncated alongside: %d → %d",
					len(beforeVals), len(got))
			}
		})
	}
}

// TestExplicitModeKeyStillWins: a posted mode key IS a capture, so it overwrites the
// stored pick. Without this the merge would be a one-way ratchet.
func TestExplicitModeKeyStillWins(t *testing.T) {
	srv := newTestServer(t)
	wf := seedPresetWorkflow(t, srv, "tmpl", presetPartialModeGraph)
	selKey, modeA, modeB := presetModeKeys(t, wf.Graph)
	pid := seedModePreset(t, srv, wf, "Image", wf.GraphHash, selKey, modeA, "SAVED-A")

	form := formForInputs(pid, "Image", liveInputs(wf.Graph, map[string]string{selKey: modeB}),
		func(ri comfy.RunInput) string { return "TYPED-" + ri.InputName })
	form.Set(modeChoiceField, modeB)
	savePreset(t, srv, wf, pid, form)

	if got := storedModes(t, srv, pid); got[selKey] != modeB {
		t.Fatalf("the posted mode must win: stored = %v, want %s=%s", got, selKey, modeB)
	}
}

// TestCarryForwardNeverResurrectsAChangedMode is RESOLVED decision 1(c) held against
// the merge: the page-level #run-modes picker is the source of truth, a preset's
// stored mode only PRE-SELECTS it. A carry-forward that outlived a picker change
// would invert that.
//
// Both paths, because they reach the stored mode differently: on the EXACT path
// buildPresetView pre-selects it through ModesOOB, on the DRIFTED path
// ResolvePresetModes withholds every stored pick and the picker stands alone.
func TestCarryForwardNeverResurrectsAChangedMode(t *testing.T) {
	t.Run("exact: the pre-selected mode is replaced, not merged back", func(t *testing.T) {
		srv := newTestServer(t)
		wf := seedPresetWorkflow(t, srv, "tmpl", presetPartialModeGraph)
		selKey, modeA, modeB := presetModeKeys(t, wf.Graph)
		pid := seedModePreset(t, srv, wf, "Image", wf.GraphHash, selKey, modeA, "SAVED-A")

		// Tab open: the stored mode pre-selects the picker (decision 1(c), rule 2).
		open := srv.buildPresetView(context.Background(), wf, pid, nil, true)
		if open.ModesOOB[selKey] != modeA {
			t.Fatalf("fixture: tab open must pre-select the stored mode, got %v", open.ModesOOB)
		}

		// The user changes the picker to B. Every run control (and the save form)
		// hx-includes "#run-modes select", so the save carries the NEW pick.
		form := formForInputs(pid, "Image", liveInputs(wf.Graph, map[string]string{selKey: modeB}),
			func(ri comfy.RunInput) string { return "TYPED-" + ri.InputName })
		form.Set(modeChoiceField, modeB)
		savePreset(t, srv, wf, pid, form)

		if got := storedModes(t, srv, pid); got[selKey] != modeB {
			t.Fatalf("the merge resurrected the mode the picker replaced: stored = %v", got)
		}
		// And the next tab open pre-selects B, so what the user sees and what a run
		// would convert agree.
		reopen := srv.buildPresetView(context.Background(), wf, pid, nil, true)
		if reopen.ModesOOB[selKey] != modeB {
			t.Errorf("reopening pre-selected %v, want %s=%s", reopen.ModesOOB, selKey, modeB)
		}
	})

	t.Run("drifted: the stored pick is withheld, so it cannot pre-empt the picker", func(t *testing.T) {
		srv := newTestServer(t)
		wf := seedPresetWorkflow(t, srv, "tmpl", presetPartialModeGraph)
		selKey, modeA, modeB := presetModeKeys(t, wf.Graph)
		pid := seedModePreset(t, srv, wf, "Image", "STALEHASH", selKey, modeA, "SAVED-A")

		// The drift path drops every stored mode wholesale (a mode key is positional),
		// names it, and leaves ModesOOB nil — the picker is the only source.
		open := srv.buildPresetView(context.Background(), wf, pid, nil, true)
		if open.ModesOOB != nil {
			t.Fatalf("a drifted preset must not pre-select its stored mode: %v", open.ModesOOB)
		}
		if len(open.Rec.DroppedModes) == 0 {
			t.Fatal("the withheld mode must be NAMED, not silently ignored")
		}

		form := formForInputs(pid, "Image", liveInputs(wf.Graph, map[string]string{selKey: modeB}),
			func(ri comfy.RunInput) string { return "TYPED-" + ri.InputName })
		form.Set(modeChoiceField, modeB)
		savePreset(t, srv, wf, pid, form)

		if got := storedModes(t, srv, pid); got[selKey] != modeB {
			t.Fatalf("the merge resurrected the mode the picker replaced: stored = %v", got)
		}
		// Still drifted (a plain save never adopts), so the freshly stored pick is
		// itself withheld on the next open — carry-forward cannot smuggle a mode past
		// the hash gate.
		reopen := srv.buildPresetView(context.Background(), wf, pid, nil, true)
		if reopen.ModesOOB != nil {
			t.Errorf("a save must not adopt: ModesOOB = %v", reopen.ModesOOB)
		}
		_ = modeA
	})
}

// TestAdoptionDoesNotCarryTheStoredMode: the adoption exclusion applies to
// ModeSelection for exactly the reason it applies to widget entries. A ModeGroup.Key
// is "<selector node id>:<group index>" — positional — and on the EXACT read path
// ResolvePresetModes' hash gate does not fire, so a carried pick would be applied
// against a graph it was never captured against. An adoption therefore certifies only
// the picks this request made, and the notice counts what it did not keep.
func TestAdoptionDoesNotCarryTheStoredMode(t *testing.T) {
	srv := newTestServer(t)
	wf := seedPresetWorkflow(t, srv, "tmpl", presetPartialModeGraph)
	selKey, modeA, _ := presetModeKeys(t, wf.Graph)
	pid := seedModePreset(t, srv, wf, "Image", "STALEHASH", selKey, modeA, "SAVED-A")
	before := storedValues(t, srv, pid)

	p, _ := srv.store.GetRunPreset(context.Background(), pid)
	if presetHashMatch(p, wf) {
		t.Fatal("fixture: an adoption only happens on the DRIFTED path")
	}

	// Adopt while no mode is picked: the capture is the out-of-group node only.
	form := formForInputs(pid, "Image", liveInputs(wf.Graph, nil),
		func(ri comfy.RunInput) string { return "TYPED-" + ri.InputName })
	form.Set(presetAdoptField, "1")
	body := savePreset(t, srv, wf, pid, form)

	got, _ := srv.store.GetRunPreset(context.Background(), pid)
	if got.GraphHash != wf.GraphHash {
		t.Fatalf("the adoption did not stamp: %q", got.GraphHash)
	}
	if modes := presetModes(got.Params); len(modes) != 0 {
		t.Errorf("an adoption certified a mode pick it never captured: %v", modes)
	}
	// The count in the notice covers every stored value the adoption did not keep —
	// the uncaptured widget entries AND the uncaptured mode pick.
	wantN := len(before) - 1 + 1
	if !strings.Contains(body, asRendered(t, strconv.Itoa(wantN)+" saved values that")) {
		t.Errorf("the notice must count the discarded mode pick too (want %d):\n%s", wantN, body)
	}
	if !strings.Contains(body, asRendered(t, "were not kept")) {
		t.Errorf("an adoption that discarded values must say so:\n%s", body)
	}
}

// TestModeCarryForwardNeverStampsAHashThatMisdescribesIt reproduces the stored-hash
// invariant for ModeSelection in all three write shapes. A non-blank stored
// graph_hash claims every stored POSITIONAL entry was captured against the graph with
// that hash; a mode key is as positional as a widget index, so the claim has to be
// re-earned once the merge starts carrying picks the write did not capture.
func TestModeCarryForwardNeverStampsAHashThatMisdescribesIt(t *testing.T) {
	t.Run("blank stamp: no claim is made, so carrying is free", func(t *testing.T) {
		srv := newTestServer(t)
		wf := seedPresetWorkflow(t, srv, "tmpl", presetPartialModeGraph)
		selKey, modeA, _ := presetModeKeys(t, wf.Graph)
		pid := seedModePreset(t, srv, wf, "Image", "STALEHASH", selKey, modeA, "SAVED-A")

		form := formForInputs(pid, "Image", liveInputs(wf.Graph, nil),
			func(ri comfy.RunInput) string { return "TYPED-" + ri.InputName })
		savePreset(t, srv, wf, pid, form)

		got, _ := srv.store.GetRunPreset(context.Background(), pid)
		if got.GraphHash != "" {
			t.Fatalf("graph_hash = %q, want blank on a drifted save", got.GraphHash)
		}
		if presetModes(got.Params)[selKey] != modeA {
			t.Fatalf("the carried pick was lost: %v", presetModes(got.Params))
		}
		// Blank can never be proven equal, so the carried pick is withheld AND NAMED on
		// every read until the user adopts.
		applicable, drops := comfy.ResolvePresetModes(
			json.RawMessage(wf.Graph), presetModes(got.Params), presetHashMatch(got, wf))
		if len(applicable) != 0 || len(drops) != 1 {
			t.Errorf("a carried pick under a blank stamp must be withheld and named: "+
				"applied=%v drops=%v", applicable, drops)
		}
		if drops[0].Reason != comfy.PresetDropModeDrifted {
			t.Errorf("drop reason = %v, want the drift reason", drops[0].Reason)
		}
	})

	t.Run("matching stamp: the pick was captured under this very hash", func(t *testing.T) {
		srv := newTestServer(t)
		wf := seedPresetWorkflow(t, srv, "tmpl", presetPartialModeGraph)
		selKey, modeA, _ := presetModeKeys(t, wf.Graph)
		pid := seedModePreset(t, srv, wf, "Image", wf.GraphHash, selKey, modeA, "SAVED-A")

		form := formForInputs(pid, "Image", liveInputs(wf.Graph, nil),
			func(ri comfy.RunInput) string { return "TYPED-" + ri.InputName })
		savePreset(t, srv, wf, pid, form)

		got, _ := srv.store.GetRunPreset(context.Background(), pid)
		if got.GraphHash != wf.GraphHash {
			t.Fatalf("graph_hash = %q, want the workflow's %q", got.GraphHash, wf.GraphHash)
		}
		assertStoredModeCertified(t, wf, got)
		if presetModes(got.Params)[selKey] != modeA {
			t.Errorf("the carried pick was lost: %v", presetModes(got.Params))
		}
	})

	t.Run("adoption: the stamp covers only the picks this request captured", func(t *testing.T) {
		srv := newTestServer(t)
		wf := seedPresetWorkflow(t, srv, "tmpl", presetPartialModeGraph)
		selKey, modeA, modeB := presetModeKeys(t, wf.Graph)
		pid := seedModePreset(t, srv, wf, "Image", "STALEHASH", selKey, modeA, "SAVED-A")

		form := formForInputs(pid, "Image", liveInputs(wf.Graph, map[string]string{selKey: modeB}),
			func(ri comfy.RunInput) string { return "TYPED-" + ri.InputName })
		form.Set(modeChoiceField, modeB)
		form.Set(presetAdoptField, "1")
		savePreset(t, srv, wf, pid, form)

		got, _ := srv.store.GetRunPreset(context.Background(), pid)
		if got.GraphHash != wf.GraphHash {
			t.Fatalf("the adoption did not stamp: %q", got.GraphHash)
		}
		stored := presetModes(got.Params)
		if len(stored) != 1 || stored[selKey] != modeB {
			t.Fatalf("the stamp must cover ONLY what this request captured: %v", stored)
		}
		if stored[selKey] == modeA {
			t.Error("an older-graph pick was newly certified")
		}
		assertStoredModeCertified(t, wf, got)
	})
}

// assertStoredModeCertified is the machine-checkable half of "the hash describes the
// picks": under a non-blank stamp every stored mode key must still be surfaced by
// DetectModeSelectors of that graph UNDER THE SAME SELECTOR, i.e. exactly what the
// EXACT read path applies once the hash gate stops firing.
//
// Honest limit: structure is all a test can check here. That "<node>:<index>" still
// names the same PIPELINE is precisely what the hash is for — which is why an
// adoption may not carry an uncaptured pick in the first place.
func assertStoredModeCertified(t *testing.T, wf *store.Workflow, p *store.RunPreset) {
	t.Helper()
	if p.GraphHash == "" {
		t.Fatal("assertStoredModeCertified is only meaningful for a stamped preset")
	}
	if p.GraphHash != wf.GraphHash {
		t.Fatalf("stamped %q, workflow %q", p.GraphHash, wf.GraphHash)
	}
	stored := presetModes(p.Params)
	applicable, drops := comfy.ResolvePresetModes(json.RawMessage(wf.Graph), stored, true)
	if len(drops) != 0 {
		t.Errorf("stored picks %v are certified by %q but %d of them do not resolve in "+
			"that graph: %v", stored, p.GraphHash, len(drops), drops)
	}
	if len(applicable) != len(stored) {
		t.Errorf("certified %d picks, only %d apply: stored=%v applied=%v",
			len(stored), len(applicable), stored, applicable)
	}
}

// TestAModePickCannotBeRemovedByASave states the ⚠ KNOWN LIMIT as an executable
// fact rather than a comment: there is no posted form value meaning "I deselected the
// mode", so carry-forward makes a stored pick REPLACEABLE but not REMOVABLE.
//
// Two independent reasons, both pinned below:
//
//   - runModeSelect renders the blank "Choose a mode…" option ONLY while nothing is
//     selected, so a picker showing a mode offers the user no way back to blank;
//   - parseModeChoices maps a blank/unknown value to nil — byte-identical to posting
//     no mode_key at all — which its own comment already calls "keep as saved".
//
// A sentinel meaning "clear it" is deliberately NOT invented here: that is new UI, not
// a merge fix. Deleting the tab remains the way to clear a stored pick.
func TestAModePickCannotBeRemovedByASave(t *testing.T) {
	srv := newTestServer(t)
	wf := seedPresetWorkflow(t, srv, "tmpl", presetPartialModeGraph)
	selKey, modeA, _ := presetModeKeys(t, wf.Graph)

	// (1) A picker showing a selected mode renders no blank option to go back to.
	sels := comfy.DetectModeSelectors(json.RawMessage(wf.Graph))
	withPick := renderString(t, runModeSelect("1", "tok", 0, sels[0], modeA))
	if strings.Contains(withPick, `<option value=""`) {
		t.Errorf("the picker offers a blank option once a mode is selected — a deselect "+
			"IS representable and this limit is stale:\n%s", withPick)
	}
	noPick := renderString(t, runModeSelect("1", "tok", 0, sels[0], ""))
	if !strings.Contains(noPick, `<option value=""`) {
		t.Errorf("fixture: the placeholder should render while nothing is picked:\n%s", noPick)
	}

	// (2) A blank post is indistinguishable from no post at all.
	blank := parseModeChoices(url.Values{modeChoiceField: {""}}, wf)
	absent := parseModeChoices(url.Values{}, wf)
	if len(blank) != 0 || len(absent) != 0 {
		t.Fatalf("a blank mode_key must parse as no capture: blank=%v absent=%v", blank, absent)
	}

	// (3) Consequence, end to end: posting a blank mode_key keeps the stored pick.
	pid := seedModePreset(t, srv, wf, "Image", wf.GraphHash, selKey, modeA, "SAVED-A")
	form := formForInputs(pid, "Image", liveInputs(wf.Graph, nil),
		func(ri comfy.RunInput) string { return "TYPED-" + ri.InputName })
	form.Set(modeChoiceField, "")
	savePreset(t, srv, wf, pid, form)
	if got := storedModes(t, srv, pid); got[selKey] != modeA {
		t.Fatalf("a blank mode_key removed the stored pick: %v", got)
	}
}

// TestTabSwitchCarriesTheModeToo: persistOutgoing writes through presetEntryWrite as
// well, so switching tabs after a save that carried no mode must not truncate the
// outgoing tab's mode either.
func TestTabSwitchCarriesTheModeToo(t *testing.T) {
	srv := newTestServer(t)
	wf := seedPresetWorkflow(t, srv, "tmpl", presetPartialModeGraph)
	selKey, modeA, modeB := presetModeKeys(t, wf.Graph)
	a := seedModePreset(t, srv, wf, "A", wf.GraphHash, selKey, modeA, "SAVED-A")
	b := seedModePreset(t, srv, wf, "B", wf.GraphHash, selKey, modeB, "SAVED-B")

	// Leaving tab A with no mode_key in flight (the picker matched nothing).
	form := formForInputs(a, "A", liveInputs(wf.Graph, nil),
		func(ri comfy.RunInput) string { return "TYPED-" + ri.InputName })
	code, body := doPresetPost(t, srv,
		"/workflows/"+strconv.FormatInt(wf.ID, 10)+"/run/presets/"+
			strconv.FormatInt(b, 10)+"/activate", form, true)
	if code != http.StatusOK {
		t.Fatalf("activate = %d: %s", code, body)
	}

	if got := storedModes(t, srv, a); got[selKey] != modeA {
		t.Errorf("switching tabs dropped the outgoing tab's mode: %v", got)
	}
	if got := storedModes(t, srv, b); got[selKey] != modeB {
		t.Errorf("the incoming tab's mode changed: %v", got)
	}
}
