package comfy

import (
	"encoding/json"
	"os"
	"strconv"
	"testing"
)

func loadWF587(t *testing.T) json.RawMessage {
	t.Helper()
	b, err := os.ReadFile("testdata/wf587_converted_widgets.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return json.RawMessage(b)
}

// TestBatchSeedInputsSelectedByKind is THE regression test for the trap that would have
// made "Queue xN with a fresh seed" a silent no-op.
//
// Over a real captured graph, the seed lives on node 40 (an upstream rgthree Seed
// node) at widget 0 and holds a NUMBER. The KSampler that consumes it (node 41)
// holds a control_after_generate STRING "randomize" at widget 1 — which is exactly
// what isSeedControlSlot matches, and it is NOT the seed.
//
// So the test pins three things at once:
//  1. Kind == RunInputSeed selects exactly {node 40, widget 0};
//  2. that slot is a number, and isSeedControlSlot is FALSE for it — an
//     isSeedControlSlot-driven implementation would not select it at all;
//  3. isSeedControlSlot IS true for node 41 widget 1, i.e. the wrong implementation
//     has a plausible-looking non-empty answer, which is why it survives review.
func TestBatchSeedInputsSelectedByKind(t *testing.T) {
	graph := loadWF587(t)

	keys := SeedWidgetKeys(graph)
	if len(keys) != 1 {
		t.Fatalf("seed keys = %+v, want exactly one (node 40, widget 0)", keys)
	}
	if keys[0] != (UIWidgetKey{NodeID: "40", Widget: 0}) {
		t.Fatalf("seed key = %+v, want {40 0}", keys[0])
	}

	nodes := fixtureWidgets(t, graph)

	// (2) the REAL seed slot is a number and does NOT look like a control slot.
	seedSlot := nodes["40"][0]
	if !isJSONNumberValue(seedSlot) {
		t.Fatalf("node 40 widget 0 = %s, want a JSON number (the seed value)", seedSlot)
	}
	if isSeedControlSlot(seedSlot) {
		t.Fatal("isSeedControlSlot matched the real seed slot — the fixture no longer " +
			"exercises the trap this test exists for")
	}

	// (3) the DECOY slot: what an isSeedControlSlot-driven implementation would find.
	decoy := nodes["41"][1]
	if !isSeedControlSlot(decoy) {
		t.Fatalf("node 41 widget 1 = %s, expected the control_after_generate string "+
			"that makes the wrong implementation look plausible", decoy)
	}
}

// TestSeedOverrideActuallyReachesTheGraph is the end-to-end half: it applies the
// override the batch would submit and proves the graph's seed VALUE changed.
//
// This is the assertion an isSeedControlSlot-based implementation cannot pass. That
// implementation writes a number into a string slot; typedOverrideValue preserves
// the slot's JSON type and silently REFUSES the replacement, so the submitted graph
// comes back byte-identical and every batch item renders the same image — with
// every unit test still green.
func TestSeedOverrideActuallyReachesTheGraph(t *testing.T) {
	graph := loadWF587(t)

	before := SeedRunInputs(graph)
	if len(before) != 1 {
		t.Fatalf("want one seed input, got %d", len(before))
	}

	overrides := freshSeedOverrides(graph)
	if len(overrides) != 1 {
		t.Fatalf("freshSeedOverrides = %+v, want one entry", overrides)
	}
	applied := ApplyUIWidgetOverrides(graph, overrides)

	after := SeedRunInputs(applied)
	if len(after) != 1 {
		t.Fatalf("seed input disappeared after applying the override: %+v", after)
	}
	if after[0].Current == before[0].Current {
		t.Fatalf("seed value unchanged after the override (%q) — the override was "+
			"silently refused, which is exactly the isSeedControlSlot no-op",
			after[0].Current)
	}
	want := overrides[UIWidgetKey{NodeID: "40", Widget: 0}]
	if after[0].Current != want {
		t.Fatalf("seed value = %q, want the generated %q", after[0].Current, want)
	}

	// The WRONG implementation, run for contrast: aim the same fresh value at the
	// control_after_generate slot and confirm the seed does not move. If this ever
	// starts changing the seed, the type guard has regressed.
	wrong := ApplyUIWidgetOverrides(graph, map[UIWidgetKey]string{
		{NodeID: "41", Widget: 1}: NewSeed(),
	})
	if got := SeedRunInputs(wrong); len(got) != 1 || got[0].Current != before[0].Current {
		t.Fatalf("writing to the control slot changed the seed (%+v) — the fixture or "+
			"the type guard changed and this contrast no longer holds", got)
	}
}

// TestRandomSeedsDifferPerItem pins that a batch of 25 gets 25 different seeds, all
// inside the range the widget slot can carry exactly.
func TestRandomSeedsDifferPerItem(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 25; i++ {
		s := NewSeed()
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			t.Fatalf("seed %q is not an integer: %v", s, err)
		}
		if n < 0 || n >= maxSeedExclusive {
			t.Fatalf("seed %d outside [0, 1e15)", n)
		}
		// 1e15 < 2^53, so the value survives any JS that touches the widget slot.
		if n >= 1<<53 {
			t.Fatalf("seed %d exceeds 2^53 and would be mangled by the browser", n)
		}
		if seen[s] {
			t.Fatalf("duplicate seed %q in 25 draws — a batch would repeat itself", s)
		}
		seen[s] = true
	}
}

// TestSeedWidgetKeysEmptyWithoutSeed pins that a graph exposing no seed yields NIL,
// not an empty-but-non-nil slice: the caller decides between "offer the identical
// batch confirmation" and "randomise", and it must not be able to read a no-op as a
// success. The derived override map is nil for the same input, for the same reason.
func TestSeedWidgetKeysEmptyWithoutSeed(t *testing.T) {
	// An api-format graph exposes no run inputs at all.
	const apiGraph = `{"1":{"class_type":"KSampler","inputs":{"seed":1}}}`
	if got := SeedWidgetKeys(json.RawMessage(apiGraph)); got != nil {
		t.Errorf("SeedWidgetKeys(api graph) = %+v, want nil", got)
	}
	if got := freshSeedOverrides(json.RawMessage(apiGraph)); got != nil {
		t.Errorf("freshSeedOverrides(api graph) = %+v, want nil", got)
	}
	if got := SeedWidgetKeys(json.RawMessage(`not json`)); got != nil {
		t.Errorf("SeedWidgetKeys(garbage) = %+v, want nil", got)
	}
}

// freshSeedOverrides is the composition PRODUCTION performs — internal/web's
// withFreshSeeds stamping comfy.NewSeed() on every SeedWidgetKeys entry — expressed
// once for the tests below. It lives HERE, not in run_seed.go, because it has no
// production caller: web already holds the keys it resolved from the mode-applied
// graph and never wants a graph-taking shortcut past that.
func freshSeedOverrides(graph json.RawMessage) map[UIWidgetKey]string {
	keys := SeedWidgetKeys(graph)
	if len(keys) == 0 {
		return nil
	}
	out := make(map[UIWidgetKey]string, len(keys))
	for _, k := range keys {
		out[k] = NewSeed()
	}
	return out
}

// TestBatchSeedSelectionRespectsControlSlotAlignment re-pins the widget-index alignment
// the control slot forces: on the inline txt2img graph the KSampler's own seed is at
// widget 0 and sampler_name/scheduler land at 4/5, NOT 3/4. An implementation that
// forgets the control slot randomises `steps`.
func TestBatchSeedSelectionRespectsControlSlotAlignment(t *testing.T) {
	inputs := DetectRunInputs(json.RawMessage(txt2imgUIGraph), nil)
	seeds := 0
	for _, ri := range inputs {
		if ri.Kind != RunInputSeed {
			continue
		}
		seeds++
		if ri.NodeID != "3" || ri.WidgetIndex != 0 {
			t.Errorf("seed input at (%s,%d), want (3,0)", ri.NodeID, ri.WidgetIndex)
		}
	}
	if seeds != 1 {
		t.Fatalf("seed inputs = %d, want 1", seeds)
	}
	for _, tc := range []struct {
		input string
		want  int
	}{{"sampler_name", 4}, {"scheduler", 5}} {
		ri := findRunInput(inputs, "3", tc.input)
		if ri == nil {
			t.Fatalf("%s not detected", tc.input)
		}
		if ri.WidgetIndex != tc.want {
			t.Errorf("%s at widget %d, want %d (the control_after_generate slot must "+
				"consume index 1)", tc.input, ri.WidgetIndex, tc.want)
		}
	}
}

// fixtureWidgets indexes a UI graph's top-level nodes by id → widgets_values.
func fixtureWidgets(t *testing.T, graph json.RawMessage) map[string][]json.RawMessage {
	t.Helper()
	var g struct {
		Nodes []struct {
			ID            json.RawMessage   `json:"id"`
			WidgetsValues []json.RawMessage `json:"widgets_values"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal(graph, &g); err != nil {
		t.Fatalf("decode graph: %v", err)
	}
	out := make(map[string][]json.RawMessage, len(g.Nodes))
	for _, n := range g.Nodes {
		out[string(n.ID)] = n.WidgetsValues
	}
	return out
}
