package web

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/comfy"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// TestSnapshotRoundTripsModeSelection closes the documented deferred gap: a
// captured generation could not restore WHICH pipeline of a multi-mode template it
// ran, because generations.params had no field for it. It round-trips now.
func TestSnapshotRoundTripsModeSelection(t *testing.T) {
	wf := &store.Workflow{ID: 1, Format: store.WorkflowFormatUI, GraphHash: "h"}
	opts := runOptions{
		ModeSelection:     map[string]string{"12": "12:1"},
		UIWidgetOverrides: map[comfy.UIWidgetKey]string{{NodeID: "3", Widget: 0}: "42"},
	}
	blob := marshalRunParams(buildRunParamsSnapshot(wf, opts))
	if !strings.Contains(blob, `"mode_selection"`) {
		t.Fatalf("mode_selection missing from the params blob: %s", blob)
	}

	got, stale := runOptionsFromParams(blob, "h", "h")
	if stale != "" {
		t.Fatalf("unexpected staleReason %q", stale)
	}
	if got.ModeSelection["12"] != "12:1" {
		t.Errorf("ModeSelection = %v, want 12→12:1", got.ModeSelection)
	}
	if got.UIWidgetOverrides[comfy.UIWidgetKey{NodeID: "3", Widget: 0}] != "42" {
		t.Errorf("UIWidgetOverrides = %v", got.UIWidgetOverrides)
	}
}

// TestRunOptionsFromParamsGatesModeOnHash: a ModeGroup.Key is
// "<selector node id>:<group index>" — positional in the group array — so it must
// NOT cross a graph change. Every unprovable-hash shape withholds it AND reports a
// staleReason (which handleGenerationRerun turns into a 409).
func TestRunOptionsFromParamsGatesModeOnHash(t *testing.T) {
	wf := &store.Workflow{ID: 1, Format: store.WorkflowFormatUI}
	// ONLY a mode selection — no widget overrides — so the gate cannot be passing by
	// accident on the pre-existing UIWidgetOverrides branch.
	blob := marshalRunParams(buildRunParamsSnapshot(wf, runOptions{
		ModeSelection: map[string]string{"12": "12:1"},
	}))

	cases := []struct {
		name           string
		genHash, cur   string
		wantApplied    bool
		wantStaleEmpty bool
	}{
		{"equal hashes", "h", "h", true, true},
		{"different hashes", "h1", "h2", false, false},
		{"blank generation hash", "", "h", false, false},
		{"blank workflow hash", "h", "", false, false},
		{"both blank", "", "", false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, stale := runOptionsFromParams(blob, c.genHash, c.cur)
			if applied := len(got.ModeSelection) > 0; applied != c.wantApplied {
				t.Errorf("ModeSelection applied = %v, want %v (%v)", applied, c.wantApplied, got.ModeSelection)
			}
			if (stale == "") != c.wantStaleEmpty {
				t.Errorf("staleReason = %q, wantEmpty = %v", stale, c.wantStaleEmpty)
			}
		})
	}
}

// TestSnapshotWithoutModeSelectionIsUnchanged proves the gate did not widen: a
// generation captured with neither positional family still replays on ANY hash,
// exactly as before.
func TestSnapshotWithoutModeSelectionIsUnchanged(t *testing.T) {
	wf := &store.Workflow{ID: 1, Format: store.WorkflowFormatUI}
	blob := marshalRunParams(buildRunParamsSnapshot(wf, runOptions{
		Substitute: map[string]string{"a.safetensors": "b.safetensors"},
	}))
	if strings.Contains(blob, "mode_selection") {
		t.Errorf("an empty mode selection must be omitted: %s", blob)
	}
	got, stale := runOptionsFromParams(blob, "h1", "h2")
	if stale != "" {
		t.Errorf("name-keyed families must never be hash-gated, got %q", stale)
	}
	if got.Substitute["a.safetensors"] != "b.safetensors" {
		t.Errorf("Substitute = %v", got.Substitute)
	}
}

// TestSnapshotJSONIsStable: two builds of the same options must marshal
// byte-identically (map iteration is unordered; the existing sort discipline plus
// Go's sorted map-key marshaling covers the new field).
func TestSnapshotJSONIsStable(t *testing.T) {
	wf := &store.Workflow{ID: 1, Format: store.WorkflowFormatUI, Resources: []string{"a", "b"}}
	opts := runOptions{
		ModeSelection: map[string]string{"12": "12:1", "9": "9:0", "40": "40:2"},
		UIWidgetOverrides: map[comfy.UIWidgetKey]string{
			{NodeID: "3", Widget: 2}:  "20",
			{NodeID: "3", Widget: 0}:  "1",
			{NodeID: "10", Widget: 0}: "x",
		},
		Substitute: map[string]string{"z": "1", "a": "2"},
	}
	first := marshalRunParams(buildRunParamsSnapshot(wf, opts))
	for i := 0; i < 50; i++ {
		if got := marshalRunParams(buildRunParamsSnapshot(wf, opts)); got != first {
			t.Fatalf("params JSON is not stable:\n%s\n%s", first, got)
		}
	}
}

// TestPresetEntryTupleRoundTripsThroughJSON pins the drift-tuple fields survive
// the shared snapshot shape — without them a drifted preset degrades to
// "unverifiable" and drops every value.
func TestPresetEntryTupleRoundTripsThroughJSON(t *testing.T) {
	snap := runParamsSnapshot{UIWidgetOverrides: []uiWidgetOverrideEntry{{
		NodeID: "40", Widget: json.RawMessage("0"), Value: "42",
		Kind: string(comfy.RunInputSeed), ClassType: "Seed (rgthree)",
		InputName: "seed", Label: "Seed",
	}}}
	b, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	back := parseRunParams(string(b))
	if len(back.UIWidgetOverrides) != 1 {
		t.Fatalf("entries = %d", len(back.UIWidgetOverrides))
	}
	e := back.UIWidgetOverrides[0]
	if e.Kind != string(comfy.RunInputSeed) || e.ClassType != "Seed (rgthree)" ||
		e.InputName != "seed" || e.Label != "Seed" {
		t.Errorf("tuple lost through JSON: %+v", e)
	}
	// A generation capture writes no tuple; those keys must be absent, not "".
	plain := marshalRunParams(buildRunParamsSnapshot(nil, runOptions{
		UIWidgetOverrides: map[comfy.UIWidgetKey]string{{NodeID: "3", Widget: 0}: "9"},
	}))
	if strings.Contains(plain, `"kind"`) || strings.Contains(plain, `"class_type"`) {
		t.Errorf("tuple keys must be omitempty: %s", plain)
	}
}
