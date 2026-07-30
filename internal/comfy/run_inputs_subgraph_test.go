package comfy

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
)

// ── the real graph ───────────────────────────────────────────────────────────

// loadWF557 is workflow 557 "seduce" from the dogfood library, verbatim. It is one of
// the 13 library workflows that exposed NO seed at all before subgraph reach existed:
// its ONLY samplers are two plain KSamplerAdvanced nodes living inside a subgraph
// definition ("Sample", instantiated exactly once, by top-level node 93).
func loadWF557(t *testing.T) json.RawMessage {
	t.Helper()
	b, err := os.ReadFile("testdata/wf557_subgraph_samplers.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return json.RawMessage(b)
}

// TestSubgraphInteriorSeedsDetected pins the whole point of the feature on real data:
// BOTH interior samplers' seeds are surfaced, addressed in the instance namespace.
func TestSubgraphInteriorSeedsDetected(t *testing.T) {
	graph := loadWF557(t)

	keys := SeedWidgetKeys(graph)
	want := []UIWidgetKey{{NodeID: "93:12", Widget: 1}, {NodeID: "93:10", Widget: 1}}
	if len(keys) != len(want) {
		t.Fatalf("seed keys = %+v, want exactly two (both interior KSamplerAdvanced)", keys)
	}
	for i := range want {
		if keys[i] != want[i] {
			t.Fatalf("seed key[%d] = %+v, want %+v", i, keys[i], want[i])
		}
	}

	// Both are real, DIFFERENT seeds — the graph ships 0 and 80085 — so a batch has
	// two independent values to randomise, not one value seen twice.
	seeds := SeedRunInputs(graph)
	if seeds[0].Current == seeds[1].Current {
		t.Fatalf("both seeds pre-fill to %q; the fixture no longer proves they are "+
			"independent values", seeds[0].Current)
	}
	for _, ri := range seeds {
		if ri.ClassType != "KSamplerAdvanced" || ri.InputName != "noise_seed" {
			t.Errorf("seed %+v is not the interior KSamplerAdvanced.noise_seed", ri)
		}
	}

	// The interior ids MUST be scoped. Interior node 12 and interior node 10 both have
	// plausible top-level twins in other graphs (and node 12 is a real top-level id in
	// several of the 13); emitting a bare "12" is the collision this addressing exists
	// to avoid.
	for _, ri := range DetectRunInputs(graph, nil) {
		if ri.SourceClassType == "KSamplerAdvanced" && !strings.HasPrefix(ri.NodeID, "93:") {
			t.Errorf("interior input %+v is addressed by a bare id — it would collide "+
				"with a top-level node", ri)
		}
	}
}

// TestSubgraphInteriorUpstreamResolutionStaysInside pins that the upstream walk works
// in the interior's OWN id namespace and that its result is scoped too: interior node
// 12's `steps` is a converted widget wired to the interior PrimitiveInt #141, and both
// samplers share it — so it is ONE field with two consumers, addressed "93:141".
func TestSubgraphInteriorUpstreamResolutionStaysInside(t *testing.T) {
	inputs := DetectRunInputs(loadWF557(t), nil)
	var steps *RunInput
	for i := range inputs {
		if inputs[i].InputName != "steps" {
			continue
		}
		if steps != nil {
			t.Fatalf("steps surfaced twice (%+v and %+v) — the shared upstream widget "+
				"must dedupe to one field", *steps, inputs[i])
		}
		steps = &inputs[i]
	}
	if steps == nil {
		t.Fatal("steps not detected; the interior upstream walk found nothing")
	}
	if steps.NodeID != "93:141" || steps.WidgetIndex != 0 {
		t.Errorf("steps at (%s,%d), want (93:141,0) — the interior PrimitiveInt",
			steps.NodeID, steps.WidgetIndex)
	}
	if !steps.Resolved || steps.SourceClassType != "PrimitiveInt" {
		t.Errorf("steps = %+v, want a resolved PrimitiveInt source", *steps)
	}
	if steps.Consumers != 2 {
		t.Errorf("steps consumers = %d, want 2 (both interior samplers)", steps.Consumers)
	}
}

// TestSubgraphOverrideLandsInTheDefinition is the write half on real data: the seed
// override must be written into definitions.subgraphs[].nodes[], the only place it
// survives flattening, and the stored graph must come back untouched.
func TestSubgraphOverrideLandsInTheDefinition(t *testing.T) {
	graph := loadWF557(t)
	before := string(graph)

	overrides := map[UIWidgetKey]string{
		{NodeID: "93:12", Widget: 1}: "111111",
		{NodeID: "93:10", Widget: 1}: "222222",
	}
	applied := ApplyUIWidgetOverrides(graph, overrides)

	got := interiorWidgets(t, applied, 0)
	if v := scalarWidgetString(got["12"][1]); v != "111111" {
		t.Errorf("interior 12 noise_seed = %q, want 111111 — the override did not reach "+
			"definitions.subgraphs[].nodes[]", v)
	}
	if v := scalarWidgetString(got["10"][1]); v != "222222" {
		t.Errorf("interior 10 noise_seed = %q, want 222222", v)
	}
	// Re-detecting the APPLIED graph must read the new values back, which is what the
	// batch's own "did the seed move" check relies on.
	seeds := SeedRunInputs(applied)
	if len(seeds) != 2 || seeds[0].Current != "111111" || seeds[1].Current != "222222" {
		t.Errorf("re-detected seeds = %+v, want the applied 111111/222222", seeds)
	}
	if string(graph) != before {
		t.Error("ApplyUIWidgetOverrides mutated the input graph")
	}
}

// TestSubgraphBatchProducesDistinctSeedSets is the batch-level assertion: N items must
// each get a DIFFERENT pair of seeds, and the two seeds within one item must differ
// from each other. A single-seed implementation (or one that wrote the same value to
// both slots) passes neither half.
func TestSubgraphBatchProducesDistinctSeedSets(t *testing.T) {
	graph := loadWF557(t)
	const items = 8

	seen := map[string]bool{}
	for i := 0; i < items; i++ {
		applied := ApplyUIWidgetOverrides(graph, freshSeedOverrides(graph))
		seeds := SeedRunInputs(applied)
		if len(seeds) != 2 {
			t.Fatalf("item %d exposed %d seeds, want 2", i, len(seeds))
		}
		if seeds[0].Current == seeds[1].Current {
			t.Fatalf("item %d got the same value in both samplers (%q) — the two seeds "+
				"are not being randomised independently", i, seeds[0].Current)
		}
		set := seeds[0].Current + "/" + seeds[1].Current
		if seen[set] {
			t.Fatalf("item %d repeated the seed set %s — the batch would render "+
				"duplicate images", i, set)
		}
		seen[set] = true
	}
}

// TestSubgraphBypassedInstanceNotExposed: mode application runs BEFORE seed detection
// and marks a switched-off pipeline's top-level nodes — a subgraph INSTANCE included.
// Randomising a seed inside a pipeline that will not run is the identical-batch
// near-miss TestQueueSeedKeysComeFromTheModeAppliedGraph pins at the web layer.
func TestSubgraphBypassedInstanceNotExposed(t *testing.T) {
	graph := loadWF557(t)
	if len(SeedWidgetKeys(graph)) != 2 {
		t.Fatal("fixture no longer exposes the interior seeds; the contrast below is vacuous")
	}
	for _, mode := range []int{modeBypass, modeMuted} {
		off := setTopLevelMode(t, graph, "93", mode)
		if keys := SeedWidgetKeys(off); len(keys) != 0 {
			t.Errorf("instance mode %d still exposes %+v", mode, keys)
		}
		// The writer must refuse it too, not merely the detector.
		applied := ApplyUIWidgetOverrides(off, map[UIWidgetKey]string{
			{NodeID: "93:12", Widget: 1}: "999999",
		})
		if v := scalarWidgetString(interiorWidgets(t, applied, 0)["12"][1]); v == "999999" {
			t.Errorf("instance mode %d: the override was written into the definition anyway", mode)
		}
	}
}

// ── hand-built graphs for the cases no real workflow exercises ───────────────

// twoInstanceGraph instantiates ONE subgraph definition TWICE. Its interior holds a
// single KSampler whose seed sits at widget 0.
//
// No workflow in the dogfood library does this — every definition there has exactly
// one instance — so live data can NEVER catch a regression here. This graph is the
// only guard.
const twoInstanceGraph = `{
  "nodes": [
    {"id": 3, "type": "KSampler", "mode": 0,
     "widgets_values": [1, "fixed", 20, 8, "euler", "normal", 1], "inputs": []},
    {"id": 90, "type": "sg-dup", "mode": 0, "inputs": []},
    {"id": 91, "type": "sg-dup", "mode": 0, "inputs": []}
  ],
  "links": [],
  "definitions": {"subgraphs": [
    {"id": "sg-dup", "name": "Twice", "inputNode": {"id": -10}, "outputNode": {"id": -20},
     "inputs": [], "outputs": [], "links": [],
     "nodes": [
       {"id": 12, "type": "KSampler", "mode": 0,
        "widgets_values": [555, "fixed", 20, 8, "euler", "normal", 1], "inputs": []}
     ]}
  ]}
}`

// TestSubgraphMultiInstanceNotExposed pins the refusal. An override is written into
// the DEFINITION, and flattening then clones the edited interior into BOTH instances —
// so one "seed" field would silently drive two independent pipelines and a batch could
// never give them different values. There is no per-instance widgets_values to write
// to, so the honest answer is to expose nothing.
//
// Mutation-verified: dropping the `count[n.Type] != 1` guard in subgraphRunTargets
// makes both halves of this test fail.
func TestSubgraphMultiInstanceNotExposed(t *testing.T) {
	graph := json.RawMessage(twoInstanceGraph)

	// The TOP-LEVEL sampler is still surfaced — the refusal is scoped to the interior,
	// not a blanket "this graph has subgraphs, give up".
	inputs := DetectRunInputs(graph, nil)
	if len(inputs) == 0 {
		t.Fatal("no inputs at all; the fixture is not exercising the top-level path")
	}
	for _, ri := range inputs {
		if strings.Contains(ri.NodeID, ":") {
			t.Errorf("interior input %+v exposed for a definition with TWO instances — "+
				"one field would drive both pipelines", ri)
		}
	}
	if keys := SeedWidgetKeys(graph); len(keys) != 1 || keys[0].NodeID != "3" {
		t.Errorf("seed keys = %+v, want only the top-level KSampler #3", keys)
	}

	// The writer refuses independently: a hand-built override map that never went
	// through DetectRunInputs must not reach the shared definition either.
	applied := ApplyUIWidgetOverrides(graph, map[UIWidgetKey]string{
		{NodeID: "90:12", Widget: 0}: "999999",
		{NodeID: "91:12", Widget: 0}: "888888",
	})
	if v := scalarWidgetString(interiorWidgets(t, applied, 0)["12"][0]); v != "555" {
		t.Errorf("interior seed = %q, want the untouched 555 — a write reached a "+
			"definition that is instantiated twice", v)
	}
}

// collidingIDGraph has a top-level node and a subgraph-interior node with the SAME id
// (12) and the same class, each holding a different seed. This is the exact collision
// the "<instance>:<interior>" addressing exists for.
const collidingIDGraph = `{
  "nodes": [
    {"id": 12, "type": "KSampler", "mode": 0,
     "widgets_values": [111, "fixed", 20, 8, "euler", "normal", 1], "inputs": []},
    {"id": 90, "type": "sg-one", "mode": 0, "inputs": []}
  ],
  "links": [],
  "definitions": {"subgraphs": [
    {"id": "sg-one", "name": "Once", "inputNode": {"id": -10}, "outputNode": {"id": -20},
     "inputs": [], "outputs": [], "links": [],
     "nodes": [
       {"id": 12, "type": "KSampler", "mode": 0,
        "widgets_values": [222, "fixed", 20, 8, "euler", "normal", 1], "inputs": []}
     ]}
  ]}
}`

// TestPlainNodeIDKeyStillTargetsTheTopLevelNode is the BACKWARD-COMPATIBILITY guard.
//
// Every run preset and generations.params row written before subgraph reach existed
// carries a PLAIN top-level node id. Those keys must keep resolving to the top-level
// node and to nothing else — a stored preset that silently started writing into a
// subgraph interior (or stopped writing at all) is exactly the silent corruption the
// preset code is built to prevent.
//
// The graph makes the failure observable: top-level #12 and interior #12 are the same
// class with different seeds, so a key that leaks into the interior changes 222 and a
// key that stops resolving leaves 111 alone.
//
// Mutation-verified: making splitUIWidgetOverrides route on the separator BEFORE
// consulting the top-level id table (or dropping the top-level branch) fails this.
func TestPlainNodeIDKeyStillTargetsTheTopLevelNode(t *testing.T) {
	graph := json.RawMessage(collidingIDGraph)

	// Detection keeps the two apart: the top-level seed is "12", the interior "90:12".
	keys := SeedWidgetKeys(graph)
	if len(keys) != 2 {
		t.Fatalf("seed keys = %+v, want the top-level and the interior sampler", keys)
	}
	if keys[0] != (UIWidgetKey{NodeID: "12", Widget: 0}) {
		t.Errorf("top-level seed key = %+v, want {12 0} — an old preset keys on exactly "+
			"this and it must not move", keys[0])
	}
	if keys[1] != (UIWidgetKey{NodeID: "90:12", Widget: 0}) {
		t.Errorf("interior seed key = %+v, want {90:12 0}", keys[1])
	}

	// The old-format write: plain "12" targets the top-level node ONLY.
	applied := ApplyUIWidgetOverrides(graph, map[UIWidgetKey]string{
		{NodeID: "12", Widget: 0}: "777",
	})
	top := fixtureWidgets(t, applied)
	if v := scalarWidgetString(top["12"][0]); v != "777" {
		t.Errorf("top-level seed = %q, want 777 — an old stored key stopped resolving", v)
	}
	if v := scalarWidgetString(interiorWidgets(t, applied, 0)["12"][0]); v != "222" {
		t.Errorf("interior seed = %q, want the untouched 222 — an old stored key leaked "+
			"into the subgraph definition", v)
	}

	// And the mirror: the scoped key hits the interior and leaves the top level alone.
	applied = ApplyUIWidgetOverrides(graph, map[UIWidgetKey]string{
		{NodeID: "90:12", Widget: 0}: "333",
	})
	if v := scalarWidgetString(interiorWidgets(t, applied, 0)["12"][0]); v != "333" {
		t.Errorf("interior seed = %q, want 333", v)
	}
	if v := scalarWidgetString(fixtureWidgets(t, applied)["12"][0]); v != "111" {
		t.Errorf("top-level seed = %q, want the untouched 111", v)
	}
}

// TestSubgraphKeyRoundTripsAsAPlainString pins the property that makes the scheme cost
// no migration: the composed id is an ordinary string, so it survives PresetEntry and
// the params-JSON `node_id` field unchanged.
func TestSubgraphKeyRoundTripsAsAPlainString(t *testing.T) {
	seeds := SeedRunInputs(loadWF557(t))
	e := PresetEntryFor(seeds[0], "424242")
	b, err := json.Marshal(struct {
		NodeID string `json:"node_id"`
		Widget int    `json:"widget"`
	}{e.NodeID, e.Widget})
	if err != nil {
		t.Fatal(err)
	}
	var back struct {
		NodeID string `json:"node_id"`
		Widget int    `json:"widget"`
	}
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if got := (UIWidgetKey{NodeID: back.NodeID, Widget: back.Widget}); got != (UIWidgetKey{NodeID: "93:12", Widget: 1}) {
		t.Fatalf("round-tripped key = %+v, want {93:12 1}", got)
	}
	if e.displayName() == "" {
		t.Error("a dropped interior entry would be reported as an empty name")
	}
}

// ── helpers ──────────────────────────────────────────────────────────────────

// interiorWidgets indexes definitions.subgraphs[i]'s interior nodes by id →
// widgets_values.
func interiorWidgets(t *testing.T, graph json.RawMessage, i int) map[string][]json.RawMessage {
	t.Helper()
	var g struct {
		Definitions struct {
			Subgraphs []struct {
				Nodes []struct {
					ID            json.RawMessage   `json:"id"`
					WidgetsValues []json.RawMessage `json:"widgets_values"`
				} `json:"nodes"`
			} `json:"subgraphs"`
		} `json:"definitions"`
	}
	if err := json.Unmarshal(graph, &g); err != nil {
		t.Fatalf("decode graph: %v", err)
	}
	if i >= len(g.Definitions.Subgraphs) {
		t.Fatalf("subgraph definition %d missing", i)
	}
	out := map[string][]json.RawMessage{}
	for _, n := range g.Definitions.Subgraphs[i].Nodes {
		out[idToString(n.ID)] = n.WidgetsValues
	}
	return out
}

// setTopLevelMode returns a copy of graph with top-level node id's `mode` set, which
// is exactly how ApplyModeSelection switches a pipeline off.
func setTopLevelMode(t *testing.T, graph json.RawMessage, id string, mode int) json.RawMessage {
	t.Helper()
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(graph, &doc); err != nil {
		t.Fatalf("decode graph: %v", err)
	}
	var nodes []json.RawMessage
	if err := json.Unmarshal(doc["nodes"], &nodes); err != nil {
		t.Fatalf("decode nodes: %v", err)
	}
	found := false
	for i, raw := range nodes {
		var n map[string]json.RawMessage
		if json.Unmarshal(raw, &n) != nil || idToString(n["id"]) != id {
			continue
		}
		n["mode"] = json.RawMessage(strconv.Itoa(mode))
		b, err := json.Marshal(n)
		if err != nil {
			t.Fatal(err)
		}
		nodes[i], found = b, true
	}
	if !found {
		t.Fatalf("top-level node %s not in the graph", id)
	}
	nb, err := json.Marshal(nodes)
	if err != nil {
		t.Fatal(err)
	}
	doc["nodes"] = nb
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return out
}
