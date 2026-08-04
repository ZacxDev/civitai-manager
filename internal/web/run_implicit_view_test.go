package web

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/comfy"
	"github.com/ZacxDev/civitai-manager/internal/store"
	g "maragu.dev/gomponents"
)

// TEST SCAFFOLDING, DELIBERATELY IN A _test.go FILE.
//
// implicitPresetView and runParametersPanel used to live in run_presets.go /
// run_params.go, where the tier-B deadcode gate correctly reported both as having
// NO production caller: production only ever reaches the run panel through
// (*Server).buildPresetView, which reads the workflow's saved presets from the
// database. Nothing outside these tests wanted a DB-free "no presets saved yet"
// view, and implicitPresetView's own comment claiming "the preset-free callers"
// named callers that do not exist.
//
// They are kept rather than inlined because the alternative is worse. Most of the
// run-panel tests are pure renderers with no store — rewriting them onto
// buildPresetView would mean standing up a server and a database for assertions
// about markup. What that would buy is the guarantee that the tests render what
// production renders, and TestImplicitPresetViewMatchesTheProductionNoPresetView
// below buys exactly that guarantee directly, by pinning the two against each other.
//
// 🔴 So: this is a SECOND view builder for a state production also builds, which is
// this repo's classic "green tests over a path production does not take" trap. The
// equivalence guard is the whole reason it is tolerable. If you change
// buildPresetView's no-preset branch, that guard is what will tell you these
// helpers drifted — do not delete or weaken it, and do not add a THIRD builder.

// implicitPresetView is the preset-free view: the IMPLICIT tab, every field seeded
// from the graph's current values under the given mode selection. It is what a
// workflow with nothing saved renders — see the guard below, which pins that
// against buildPresetView rather than asserting it in a comment.
func implicitPresetView(wf *store.Workflow, modes map[string]string) presetTabView {
	v := presetTabView{UIFormat: wf.Format == store.WorkflowFormatUI}
	if !v.UIFormat {
		return v
	}
	// No stored entries and hashMatch=true: nothing to reconcile, nothing to warn
	// about — every field is simply the graph's own value.
	v.Rec = comfy.ReconcileRunPreset(json.RawMessage(wf.Graph), modes, nil, nil, true)
	return v
}

// runParametersPanel renders the run "Parameters" panel for a workflow with NO
// preset context: the IMPLICIT tab, seeded from the graph's current values.
//
// It is a thin wrapper over runPresetPanel — the SAME renderer production uses —
// deliberately, so a test asserting this markup is asserting live markup. A second,
// parallel renderer would be exactly the "green tests over a dead production path"
// trap this repo keeps paying for.
//
// It returns nil when the graph exposes no editable inputs (an api-format graph, or
// a UI graph with none of the curated nodes) so the panel is simply absent.
//
// Sampler/scheduler render as free-text inputs here: choices come from /object_info,
// which the render path does not fetch (offline/no slow network in render), so they
// degrade to text — an invalid enum is caught by preflight's existing
// incompatible-options flow. DetectRunInputs still accepts object_info elsewhere, so
// object_info-backed selects can be added without changing the wiring.
func runParametersPanel(wf *store.Workflow, csrf string) g.Node {
	return runPresetPanel(wf, csrf, implicitPresetView(wf, nil))
}

// renderPanel renders a run-params panel, mapping the legitimate nil (a graph with
// no editable inputs) to the empty string instead of panicking in renderString.
func renderPanel(t *testing.T, n g.Node) string {
	t.Helper()
	if n == nil {
		return ""
	}
	return renderString(t, n)
}

// TestImplicitPresetViewMatchesTheProductionNoPresetView is the SEAM guard for the
// two helpers above: it pins the test-only builder against the production one
// (buildPresetView) in the state production reaches when a workflow has no saved
// presets. Without it, a change to buildPresetView's no-preset branch would leave
// every run-panel render test asserting markup no user ever sees — silently, and
// with a full green suite.
//
// It compares RENDERED MARKUP rather than the structs, on purpose: the markup is
// what the tests actually assert, and reflect.DeepEqual on presetTabView would
// report a spurious difference between a nil Presets slice and an empty one.
//
// The multi-mode rows are not decoration: modes is implicitPresetView's only
// parameter, and presetModeUIGraph is a fixture where a mode pick genuinely changes
// which fields exist (both pipelines ship bypassed, so the panel is EMPTY until one
// is picked). wantBody asserts each row reached the state it is supposed to compare,
// so no row can pass by comparing two blanks.
func TestImplicitPresetViewMatchesTheProductionNoPresetView(t *testing.T) {
	srv := newLibraryTestServer(t, t.TempDir())
	ctx := context.Background()

	selKey, modeA, _ := presetModeKeys(t, presetModeUIGraph)

	for _, tc := range []struct {
		name     string
		format   string
		graph    string
		modes    map[string]string
		wantBody bool // the panel must render SOMETHING, not compare blank to blank
	}{
		{"ui graph, no modes", store.WorkflowFormatUI, uiTxt2imgGraph, nil, true},
		{"multi-mode template, a mode pick", store.WorkflowFormatUI, presetModeUIGraph,
			map[string]string{selKey: modeA}, true},
		{"multi-mode template, no pick", store.WorkflowFormatUI, presetModeUIGraph, nil, false},
		{"api graph", store.WorkflowFormatAPI,
			`{"3":{"class_type":"KSampler","inputs":{"steps":20}}}`, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wf := &store.Workflow{ID: 41, Name: "seam", Format: tc.format, Graph: tc.graph}

			// Production's builder, in the state it reaches for a workflow with nothing
			// saved: activeID 0, the picker authoritative (preferPresetModes=false).
			prod := srv.buildPresetView(ctx, wf, 0, tc.modes, false)
			if prod.Active != nil || len(prod.Presets) != 0 {
				t.Fatalf("precondition: the fixture store must hold NO presets for this "+
					"workflow, got Active=%v Presets=%d", prod.Active, len(prod.Presets))
			}

			want := renderPanel(t, runPresetPanel(wf, "tok", prod))
			if (want != "") != tc.wantBody {
				t.Fatalf("precondition: panel rendered %d bytes, wantBody=%v — this row is "+
					"comparing the wrong thing", len(want), tc.wantBody)
			}

			got := renderPanel(t, runPresetPanel(wf, "tok", implicitPresetView(wf, tc.modes)))
			if got != want {
				t.Errorf("implicitPresetView has drifted from buildPresetView's no-preset "+
					"branch — every run-panel render test is now asserting markup production "+
					"does not emit.\n--- test helper ---\n%s\n--- production ---\n%s", got, want)
			}
		})
	}
}

// TestImplicitPresetViewGuardIsNotVacuous is the POSITIVE CONTROL for the guard
// above, which passes when two renders are EQUAL. A comparison that can only ever
// see equality is indistinguishable from one wired to nothing, so this proves the
// same comparison goes RED on a real difference, in the two axes that matter:
//
//   - the PRODUCTION side changing (a saved preset makes buildPresetView emit a
//     different panel — that is the drift the guard exists to catch); and
//   - the HELPER's own parameter changing (two different mode picks over one
//     workflow must not render identically).
func TestImplicitPresetViewGuardIsNotVacuous(t *testing.T) {
	srv := newLibraryTestServer(t, t.TempDir())
	ctx := context.Background()
	selKey, modeA, modeB := presetModeKeys(t, presetModeUIGraph)

	t.Run("a changed production view is observed", func(t *testing.T) {
		// A real row: run_presets carries a FOREIGN KEY onto workflows.
		base := &store.Workflow{Name: "control", Format: store.WorkflowFormatUI, Graph: uiTxt2imgGraph}
		id, err := srv.store.InsertWorkflow(ctx, base)
		if err != nil {
			t.Fatalf("insert workflow: %v", err)
		}
		base.ID = id

		before := renderPanel(t, runPresetPanel(base, "tok",
			srv.buildPresetView(ctx, base, 0, nil, false)))
		if _, err := srv.store.CreateRunPreset(ctx, store.RunPreset{
			WorkflowID: base.ID, Name: "saved", Position: -1,
		}); err != nil {
			t.Fatalf("create preset: %v", err)
		}
		after := renderPanel(t, runPresetPanel(base, "tok",
			srv.buildPresetView(ctx, base, 0, nil, false)))
		if before == "" || before == after {
			t.Fatalf("saving a preset did not change buildPresetView's panel (before=%d "+
				"bytes, equal=%v) — the equivalence guard cannot see production drift",
				len(before), before == after)
		}
		// …and that changed production view is exactly what the guard would flag.
		if renderPanel(t, runPresetPanel(base, "tok", implicitPresetView(base, nil))) == after {
			t.Error("the test helper matched the WITH-preset view; the guard's equality " +
				"would then be meaningless")
		}
	})

	t.Run("a changed mode pick is observed", func(t *testing.T) {
		wf := &store.Workflow{ID: 42, Format: store.WorkflowFormatUI, Graph: presetModeUIGraph}
		a := renderPanel(t, runPresetPanel(wf, "tok",
			implicitPresetView(wf, map[string]string{selKey: modeA})))
		b := renderPanel(t, runPresetPanel(wf, "tok",
			implicitPresetView(wf, map[string]string{selKey: modeB})))
		if a == "" || b == "" {
			t.Fatalf("precondition: both mode picks must render a panel (a=%d b=%d bytes)",
				len(a), len(b))
		}
		if a == b {
			t.Errorf("two DIFFERENT mode selections rendered byte-identical markup — the "+
				"guard's mode rows cannot observe a mode difference.\nmodes %q vs %q",
				modeA, modeB)
		}
	})
}
