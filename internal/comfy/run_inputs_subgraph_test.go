package comfy

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
)

// ── the real graph ───────────────────────────────────────────────────────────

// loadWF557 is a real WAN 2.2 i2v graph from the dogfood library: one of the 13
// workflows that exposed NO seed at all before subgraph reach existed. Its ONLY
// samplers are two plain KSamplerAdvanced nodes living inside a subgraph definition
// ("Sample", instantiated exactly once, by top-level node 93) — which is precisely
// the shape this file exists to cover, and why a hand-built graph would not do.
//
// The GRAPH STRUCTURE is verbatim; two user-content strings were replaced with
// neutral placeholders (the CLIPTextEncode prompt and a scratch input filename)
// because this repo is PUBLIC and the source workflow is personal. Nothing here
// asserts on either string, so the substitution costs the fixture nothing — keep it
// that way if you refresh the fixture.
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
// Mutation-verified two ways: dropping the instance scoping in DetectRunInputs
// collapses the two keys into one ("seed keys = [{12 0}]"), and a writer that searches
// every definition for a plain key leaks the edit into the interior ("interior seed =
// 777"). The THIRD guard in splitUIWidgetOverrides — top-level ids winning over the
// separator — needs a top-level id that literally contains ":" and is pinned
// separately by TestSeparatorShapedTopLevelIDWinsOverThePath.
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

// separatorShapedIDGraph gives a TOP-LEVEL node the id "90:12" — the exact string the
// path scheme would compose for interior node 12 of the subgraph instantiated by node
// 90, which this graph also contains.
//
// Node ids are integers in every graph ComfyUI itself writes, so this is unreachable
// from the app's own data; it is reachable from a hand-edited or third-party graph, and
// UI node ids are `json.RawMessage` precisely because their shape is not guaranteed.
const separatorShapedIDGraph = `{
  "nodes": [
    {"id": "90:12", "type": "KSampler", "mode": 0,
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

// TestSeparatorShapedTopLevelIDWinsOverThePath pins the third backward-compatibility
// guard: in splitUIWidgetOverrides a key that names an existing TOP-LEVEL node is
// routed to the top level even when it also parses as a path.
//
// The two addresses genuinely collide here, and DetectRunInputs resolves that by
// dedupe: nodes[] order puts the top-level node first, so ONE field is emitted, keyed
// "90:12", pre-filled with the TOP-LEVEL value (111). What must hold is that the write
// then lands where the panel said it would — on the node whose value the user is
// looking at. Without the guard the field still SHOWS 111 while the edit silently goes
// into the subgraph definition instead: a control that writes somewhere other than the
// value it displays, which is the exact class of silent retarget this addressing exists
// to prevent.
//
// Mutation-verified: deleting `topIDs[key.NodeID] ||` from splitUIWidgetOverrides fails
// this test.
func TestSeparatorShapedTopLevelIDWinsOverThePath(t *testing.T) {
	graph := json.RawMessage(separatorShapedIDGraph)

	// One field, keyed "90:12", showing the TOP-LEVEL node's value.
	keys := SeedWidgetKeys(graph)
	if len(keys) != 1 || keys[0] != (UIWidgetKey{NodeID: "90:12", Widget: 0}) {
		t.Fatalf("seed keys = %+v, want exactly one {90:12 0} (the two addresses collide "+
			"and dedupe to the top-level node)", keys)
	}
	seeds := SeedRunInputs(graph)
	if seeds[0].Current != "111" {
		t.Fatalf("the field pre-fills with %q, want the top-level 111 — the assertion "+
			"below is about the write matching THIS displayed value", seeds[0].Current)
	}

	applied := ApplyUIWidgetOverrides(graph, map[UIWidgetKey]string{
		{NodeID: "90:12", Widget: 0}: "777",
	})
	if v := scalarWidgetString(topLevelWidgets(t, applied)["90:12"][0]); v != "777" {
		t.Errorf("top-level node \"90:12\" seed = %q, want 777 — the edit did not land on "+
			"the node whose value the panel displayed", v)
	}
	if v := scalarWidgetString(interiorWidgets(t, applied, 0)["12"][0]); v != "222" {
		t.Errorf("interior seed = %q, want the untouched 222 — the edit was silently "+
			"retargeted into the subgraph definition", v)
	}
}

// TestSubgraphInteriorLabelsAreDisambiguated pins the user-visible half. These graphs
// carry TWO KSamplerAdvanced in one subgraph (a high-noise/low-noise WAN pair), so
// without the subgraph suffix the panel renders "CFG / Sampler / Scheduler" twice with
// nothing to tell the passes apart and an edit can silently hit the wrong stage.
func TestSubgraphInteriorLabelsAreDisambiguated(t *testing.T) {
	inputs := DetectRunInputs(loadWF557(t), nil)

	// Every collision INVOLVING an interior input must be gone. Deliberately not
	// "every label is unique": two untitled top-level CLIPTextEncode nodes both render
	// "Prompt" in this fixture, which is PRE-EXISTING top-level ambiguity this change
	// does not own and does not touch (measured across the 70-workflow library:
	// interior-involving collisions 60 → 0; the 72 purely top-level ones are unmoved).
	seen := map[string]string{}
	for _, ri := range inputs {
		prev, dup := seen[ri.Label]
		seen[ri.Label] = ri.NodeID
		if !dup {
			continue
		}
		if strings.Contains(ri.NodeID, subgraphKeySep) || strings.Contains(prev, subgraphKeySep) {
			t.Errorf("label %q is shared by %s and %s, at least one of them inside a "+
				"subgraph — the user cannot tell which stage they are editing",
				ri.Label, prev, ri.NodeID)
		}
	}

	// The exact shape, so a reformat is a deliberate change and not a drift.
	for _, want := range []struct{ nodeID, label string }{
		{"93:12", "Noise seed · Sample #12"},
		{"93:10", "Noise seed · Sample #10"},
		{"93:12", "CFG · Sample #12"},
		{"93:10", "CFG · Sample #10"},
		{"93:141", "Steps · Sample #141"},
	} {
		found := false
		for _, ri := range inputs {
			if ri.NodeID == want.nodeID && ri.Label == want.label {
				found = true
			}
		}
		if !found {
			t.Errorf("no input %s labelled %q; got %v", want.nodeID, want.label, labelsOf(inputs))
		}
	}

	// TOP-LEVEL labels must be byte-identical to before — the suffix is for interiors
	// only, and nothing that exists today may move.
	for _, ri := range inputs {
		if !strings.Contains(ri.NodeID, subgraphKeySep) && strings.Contains(ri.Label, " · ") {
			t.Errorf("top-level input %s gained a subgraph suffix: %q", ri.NodeID, ri.Label)
		}
	}

	// An UNNAMED definition degrades to "· #<id>" rather than rendering a dangling dot.
	unnamed := DetectRunInputs(json.RawMessage(strings.Replace(
		collidingIDGraph, `"name": "Once", `, "", 1)), nil)
	got := ""
	for _, ri := range unnamed {
		if ri.NodeID == "90:12" && ri.InputName == "cfg" {
			got = ri.Label
		}
	}
	if got != "CFG · #12" {
		t.Errorf("unnamed-definition label = %q, want %q", got, "CFG · #12")
	}
}

// TestSubgraphScopedIDsAreRealFlattenedNodes is the DRIFT GUARD for the addressing
// scheme, and it is what makes the whole thing self-checking.
//
// subgraphKeySep here and the separator sgExpander.expand builds into a clone's id
// (`prefix + ":" + interiorID`) are two INDEPENDENT literals in two files with nothing
// tying them together. If either moves, every interior override silently addresses a
// node that does not exist in the submitted graph — ApplyUIWidgetOverrides would still
// write into the definition, but the field would no longer name the node the run
// actually executes, and the connection between the panel and the api graph is lost.
//
// So: run the REAL flattener over the real fixture and require every scoped
// RunInput.NodeID to name a node it emitted. (The same check over the whole 70-workflow
// library passed 129/129 scoped ids.)
//
// Mutation-verified: changing subgraphKeySep to "/" fails this.
func TestSubgraphScopedIDsAreRealFlattenedNodes(t *testing.T) {
	graph := loadWF557(t)
	var g uiConvGraph
	if err := json.Unmarshal(graph, &g); err != nil {
		t.Fatal(err)
	}
	links := map[int64]uiLink{}
	for _, raw := range g.Links {
		if l, ok := parseLink(raw); ok {
			links[l.ID] = l
		}
	}
	nodes, _, _, _, err := flattenSubgraphs(&g, links)
	if err != nil {
		t.Fatalf("flatten: %v", err)
	}
	flat := make(map[string]bool, len(nodes))
	for _, n := range nodes {
		flat[idToString(n.ID)] = true
	}

	scoped := 0
	for _, ri := range DetectRunInputs(graph, nil) {
		if !strings.Contains(ri.NodeID, subgraphKeySep) {
			continue
		}
		scoped++
		if !flat[ri.NodeID] {
			t.Errorf("run input %q (%s.%s) names no node in the flattened graph — the "+
				"detector's separator and the converter's have drifted apart",
				ri.NodeID, ri.ClassType, ri.InputName)
		}
	}
	if scoped == 0 {
		t.Fatal("no scoped ids in the fixture — this guard would pass vacuously")
	}
	if !t.Failed() {
		t.Logf("%d scoped ids, all present in the flattened graph", scoped)
	}
}

// ── helpers ──────────────────────────────────────────────────────────────────

func labelsOf(inputs []RunInput) []string {
	out := make([]string, 0, len(inputs))
	for _, ri := range inputs {
		out = append(out, ri.Label)
	}
	return out
}

// topLevelWidgets indexes a graph's TOP-LEVEL nodes by id → widgets_values. It keys on
// idToString (not the raw JSON) so a STRING node id resolves the same way the
// production code resolves it.
func topLevelWidgets(t *testing.T, graph json.RawMessage) map[string][]json.RawMessage {
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
		out[idToString(n.ID)] = n.WidgetsValues
	}
	return out
}

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
