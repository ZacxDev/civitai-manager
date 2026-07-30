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

// ── The graph_hash invariant ─────────────────────────────────────────────────
//
// INVARIANT: a preset's stored graph_hash must always describe the graph its
// stored ENTRIES were captured against. Never a hash that names an older graph.
//
// Why it is not merely tidy: store.GraphHash is a canonicalized CONTENT hash, so a
// graph that is reverted, re-imported, or re-scanned back to its earlier form
// produces the EXACT same hash. A hash left naming graph G1 while the entries were
// captured against G2 is therefore not a harmless stale label — it is a FALSE
// CERTIFICATE that comes back to life the moment the workflow returns to G1:
// presetHashMatch reports EXACT, ReconcileRunPreset skips every per-entry tuple
// check, and each stored value is applied BY POSITION to whatever now sits at that
// slot. No drops, no banner, wrong values.
//
// Resolution: an entry-replacing write that is NOT an explicit adopt BLANKS the
// hash. Blank can never be proven equal, so reconciliation always takes the
// tuple-checked drift path — fail-safe, and matching entries still apply. Only an
// explicit "Adopt current graph" stamps the real hash and re-enables the fast path.
// That keeps RESOLVED decision 7 (plain Save never silently adopts) intact.

// presetForm builds the posted preset form for the shifted graph: distinctive
// values on the two slots whose MEANING differs between the two layouts.
func shiftedPresetForm(t *testing.T, activeID int64) url.Values {
	t.Helper()
	return url.Values{
		presetIDField:   {strconv.FormatInt(activeID, 10)},
		presetNameField: {"Base"},
		"wp_node":       {"3", "3"},
		"wp_widget": {
			widgetOf(t, presetUIGraphShifted, "3", "steps"),
			widgetOf(t, presetUIGraphShifted, "3", "cfg"),
		},
		"wp_value": {"99", "3.5"},
	}
}

// TestEntryReplacingWriteNeverLeavesStaleHash is the 🔴 regression test.
//
// It captures entries against the SHIFTED graph while the preset's stored hash
// still names the ORIGINAL one, then puts the workflow back on the original graph —
// the revert/re-import/rescan case a content hash makes bit-exact. The reconciler
// must NOT take the exact path, and the cfg value must not land on steps.
func TestEntryReplacingWriteNeverLeavesStaleHash(t *testing.T) {
	cases := []struct {
		name string
		// write performs one entry-replacing write against wf (currently the shifted
		// graph) for preset pid, and returns the path it posted to.
		write func(t *testing.T, srv *Server, wf *store.Workflow, pid, other int64)
	}{
		{
			name: "plain save",
			write: func(t *testing.T, srv *Server, wf *store.Workflow, pid, _ int64) {
				code, body := doPresetPost(t, srv,
					"/workflows/"+strconv.FormatInt(wf.ID, 10)+"/run/presets/"+
						strconv.FormatInt(pid, 10)+"/save",
					shiftedPresetForm(t, pid), true)
				if code != http.StatusOK {
					t.Fatalf("save = %d: %s", code, body)
				}
			},
		},
		{
			name: "tab switch (persistOutgoing)",
			write: func(t *testing.T, srv *Server, wf *store.Workflow, pid, other int64) {
				code, body := doPresetPost(t, srv,
					"/workflows/"+strconv.FormatInt(wf.ID, 10)+"/run/presets/"+
						strconv.FormatInt(other, 10)+"/activate",
					shiftedPresetForm(t, pid), true)
				if code != http.StatusOK {
					t.Fatalf("activate = %d: %s", code, body)
				}
			},
		},
		{
			name: "delete another tab (persistOutgoing)",
			write: func(t *testing.T, srv *Server, wf *store.Workflow, pid, other int64) {
				code, body := doPresetPost(t, srv,
					"/workflows/"+strconv.FormatInt(wf.ID, 10)+"/run/presets/"+
						strconv.FormatInt(other, 10)+"/delete",
					shiftedPresetForm(t, pid), true)
				if code != http.StatusOK {
					t.Fatalf("delete = %d: %s", code, body)
				}
			},
		},
		{
			name: "fork (persistOutgoing)",
			write: func(t *testing.T, srv *Server, wf *store.Workflow, pid, _ int64) {
				f := shiftedPresetForm(t, pid)
				f.Set(presetFromField, strconv.FormatInt(pid, 10))
				code, body := doPresetPost(t, srv,
					"/workflows/"+strconv.FormatInt(wf.ID, 10)+"/run/presets", f, true)
				if code != http.StatusOK {
					t.Fatalf("fork = %d: %s", code, body)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newTestServer(t)
			wf := seedPresetWorkflow(t, srv, "t2i", presetUIGraph)
			origHash := wf.GraphHash

			pid := seedPreset(t, srv, wf, "Base", origHash,
				func(ri comfy.RunInput) string { return ri.Current })
			other := seedPreset(t, srv, wf, "Other", origHash,
				func(ri comfy.RunInput) string { return ri.Current })

			// The graph moves under the preset (a rescan / re-import).
			shifted := replaceGraph(t, srv, wf.ID, presetUIGraphShifted)
			if shifted.GraphHash == origHash {
				t.Fatal("fixture: the graph hash did not move")
			}

			// The user edits and the values are captured against the SHIFTED graph.
			tc.write(t, srv, shifted, pid, other)

			got, err := srv.store.GetRunPreset(context.Background(), pid)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(got.Params, "3.5") {
				t.Fatalf("fixture: the write did not store the posted values: %s", got.Params)
			}
			// THE INVARIANT: the stored hash must not name a graph these entries were
			// not captured against.
			if got.GraphHash == origHash {
				t.Errorf("graph_hash still names the PRE-EDIT graph %q while the entries "+
					"were captured against %q — a false certificate", origHash, shifted.GraphHash)
			}
			if got.GraphHash != "" {
				t.Errorf("a non-adopt write must blank graph_hash, got %q", got.GraphHash)
			}

			// The workflow returns to its earlier graph — a revert, a re-import, a
			// rescan. A content hash makes that bit-exact.
			back := replaceGraph(t, srv, wf.ID, presetUIGraph)
			if back.GraphHash != origHash {
				t.Fatalf("fixture: reverting did not restore the hash (%q != %q)",
					back.GraphHash, origHash)
			}

			v := srv.buildPresetView(context.Background(), back, pid, nil, true)
			if v.Rec.Exact {
				t.Error("reconciliation took the EXACT path on entries captured against " +
					"another graph — every value would be applied by position, unchecked")
			}
			if val, fromPreset := fieldValue(v.Rec, "3", "steps"); fromPreset || val == "3.5" {
				t.Errorf("the stored CFG value landed on Steps (value=%q fromPreset=%v)",
					val, fromPreset)
			}
			if len(v.Rec.Dropped) == 0 {
				t.Error("the mismatched entries must be dropped AND named")
			}
		})
	}
}

// TestAdoptStampsRealHashAndRestoresExactPath: the explicit adopt is the ONLY way
// back to the fast path, and it stamps the hash of the graph the entries were
// actually captured against.
func TestAdoptStampsRealHashAndRestoresExactPath(t *testing.T) {
	srv := newTestServer(t)
	wf := seedPresetWorkflow(t, srv, "t2i", presetUIGraph)
	pid := seedPreset(t, srv, wf, "Base", wf.GraphHash,
		func(ri comfy.RunInput) string { return ri.Current })

	shifted := replaceGraph(t, srv, wf.ID, presetUIGraphShifted)

	form := shiftedPresetForm(t, pid)
	form.Set(presetAdoptField, "1")
	code, body := doPresetPost(t, srv,
		"/workflows/"+strconv.FormatInt(wf.ID, 10)+"/run/presets/"+
			strconv.FormatInt(pid, 10)+"/save", form, true)
	if code != http.StatusOK {
		t.Fatalf("adopt = %d: %s", code, body)
	}

	got, _ := srv.store.GetRunPreset(context.Background(), pid)
	if got.GraphHash != shifted.GraphHash {
		t.Fatalf("adopt stamped %q, want the CURRENT graph's hash %q",
			got.GraphHash, shifted.GraphHash)
	}

	v := srv.buildPresetView(context.Background(), shifted, pid, nil, true)
	if !v.Rec.Exact {
		t.Error("after an explicit adopt the exact fast path must be back")
	}
	if v.Drifted {
		t.Error("an adopted preset is not drifted")
	}
	if val, fromPreset := fieldValue(v.Rec, "3", "cfg"); !fromPreset || val != "3.5" {
		t.Errorf("adopted CFG = %q (fromPreset=%v), want the saved 3.5", val, fromPreset)
	}
}

// TestUndriftedWriteKeepsItsHash: the invariant is "the hash describes the entries'
// graph", NOT "always blank". A save against an UNCHANGED graph keeps its hash, so
// the ordinary edit loop never degrades itself into permanent drift.
func TestUndriftedWriteKeepsItsHash(t *testing.T) {
	srv := newTestServer(t)
	wf := seedPresetWorkflow(t, srv, "t2i", presetUIGraph)
	pid := seedPreset(t, srv, wf, "Base", wf.GraphHash,
		func(ri comfy.RunInput) string { return ri.Current })

	form := url.Values{
		presetIDField:   {strconv.FormatInt(pid, 10)},
		presetNameField: {"Base"},
		"wp_node":       {"6"},
		"wp_widget":     {"0"},
		"wp_value":      {"still mine"},
	}
	code, _ := doPresetPost(t, srv,
		"/workflows/"+strconv.FormatInt(wf.ID, 10)+"/run/presets/"+
			strconv.FormatInt(pid, 10)+"/save", form, true)
	if code != http.StatusOK {
		t.Fatalf("save = %d", code)
	}
	got, _ := srv.store.GetRunPreset(context.Background(), pid)
	if got.GraphHash != wf.GraphHash {
		t.Errorf("graph_hash = %q, want the unchanged %q — a save on an unchanged "+
			"graph must not manufacture drift", got.GraphHash, wf.GraphHash)
	}
	v := srv.buildPresetView(context.Background(), wf, pid, nil, true)
	if !v.Rec.Exact || v.Drifted {
		t.Error("an unchanged graph must stay on the exact path")
	}
}

// TestForkInheritsTheHashOfTheEntriesItCopied: Fork copies params AND graph_hash as
// one pair, so the invariant holds for the copy too — including when the source was
// just rewritten by persistOutgoing in the same request.
func TestForkInheritsTheHashOfTheEntriesItCopied(t *testing.T) {
	srv := newTestServer(t)
	wf := seedPresetWorkflow(t, srv, "t2i", presetUIGraph)
	src := seedPreset(t, srv, wf, "Base", wf.GraphHash,
		func(ri comfy.RunInput) string { return ri.Current })

	shifted := replaceGraph(t, srv, wf.ID, presetUIGraphShifted)

	f := shiftedPresetForm(t, src)
	f.Set(presetFromField, strconv.FormatInt(src, 10))
	code, body := doPresetPost(t, srv,
		"/workflows/"+strconv.FormatInt(wf.ID, 10)+"/run/presets", f, true)
	if code != http.StatusOK {
		t.Fatalf("fork = %d: %s", code, body)
	}

	list, _ := srv.store.ListRunPresets(context.Background(), wf.ID)
	if len(list) != 2 {
		t.Fatalf("presets after fork = %d, want 2", len(list))
	}
	fork := list[1]
	if !strings.Contains(fork.Params, "3.5") {
		t.Fatalf("fork did not copy the just-persisted values: %s", fork.Params)
	}
	if fork.GraphHash == wf.GraphHash {
		t.Errorf("the fork inherited the PRE-EDIT hash %q for entries captured "+
			"against %q", fork.GraphHash, shifted.GraphHash)
	}
	if fork.GraphHash != "" {
		t.Errorf("fork graph_hash = %q, want the source's blanked hash", fork.GraphHash)
	}
}

// TestFreshPresetIsStampedWithTheGraphItWasSeededFrom: a brand-new tab's entries
// come from the CURRENT graph, so stamping the current hash is truthful (this is a
// create, not an adopt of someone else's drift).
func TestFreshPresetIsStampedWithTheGraphItWasSeededFrom(t *testing.T) {
	srv := newTestServer(t)
	wf := seedPresetWorkflow(t, srv, "t2i", presetUIGraph)

	code, body := doPresetPost(t, srv,
		"/workflows/"+strconv.FormatInt(wf.ID, 10)+"/run/presets",
		url.Values{presetIDField: {"0"}, presetNameField: {"New"}}, true)
	if code != http.StatusOK {
		t.Fatalf("create = %d: %s", code, body)
	}
	list, _ := srv.store.ListRunPresets(context.Background(), wf.ID)
	if len(list) != 1 {
		t.Fatalf("presets = %d, want 1", len(list))
	}
	if list[0].GraphHash != wf.GraphHash {
		t.Errorf("new preset graph_hash = %q, want the graph it was seeded from %q",
			list[0].GraphHash, wf.GraphHash)
	}
}
