package web

import (
	"net/url"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/comfy"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// subgraphKeyGraph has a top-level KSampler (#3) and a subgraph definition — itself
// instantiated once, by node #90 — holding another KSampler whose id COLLIDES with the
// top-level one (#3). The interior sampler is addressed "90:3".
const subgraphKeyGraph = `{
  "nodes": [
    {"id": 3, "type": "KSampler", "mode": 0,
     "widgets_values": [111, "fixed", 20, 8, "euler", "normal", 1], "inputs": []},
    {"id": 90, "type": "sg-web", "mode": 0, "inputs": []}
  ],
  "links": [],
  "definitions": {"subgraphs": [
    {"id": "sg-web", "name": "Once", "inputNode": {"id": -10}, "outputNode": {"id": -20},
     "inputs": [], "outputs": [], "links": [],
     "nodes": [
       {"id": 3, "type": "KSampler", "mode": 0,
        "widgets_values": [222, "fixed", 20, 8, "euler", "normal", 1], "inputs": []}
     ]}
  ]}
}`

// TestSubgraphScopedKeySurvivesTheFormAndParamsRoundTrip pins the two hops a subgraph
// interior key has to survive OUTSIDE internal/comfy, both of which carry the node id
// as an opaque string: the wp_node form field (allow-listed against DetectRunInputs)
// and the persisted generations.params `node_id`.
//
// The colliding ids make a leak observable: if either hop dropped the "90:" scope, the
// interior edit would land on the top-level sampler instead.
func TestSubgraphScopedKeySurvivesTheFormAndParamsRoundTrip(t *testing.T) {
	wf := &store.Workflow{
		ID: 1, GraphHash: "HASH-A", Format: store.WorkflowFormatUI, Graph: subgraphKeyGraph,
	}

	// Hop 1 — the form. The panel posts the scoped id verbatim; the allow-list is
	// re-derived from the graph, so the scoped key must be in it and a fabricated one
	// must not.
	form := url.Values{
		"wp_node":   {"3", "90:3", "90:999"},
		"wp_widget": {"0", "0", "0"},
		"wp_value":  {"777", "888", "666"},
	}
	got := parseWidgetOverrides(form, wf)
	if got[comfy.UIWidgetKey{NodeID: "3", Widget: 0}] != "777" {
		t.Errorf("top-level key rejected by the allow-list: %+v", got)
	}
	if got[comfy.UIWidgetKey{NodeID: "90:3", Widget: 0}] != "888" {
		t.Errorf("subgraph-scoped key rejected by the allow-list: %+v", got)
	}
	if _, bad := got[comfy.UIWidgetKey{NodeID: "90:999", Widget: 0}]; bad {
		t.Error("a fabricated interior key passed the allow-list")
	}

	// Both land where they are addressed, and nowhere else.
	applied := comfy.ApplyUIWidgetOverrides([]byte(subgraphKeyGraph), got)
	seeds := comfy.SeedRunInputs(applied)
	if len(seeds) != 2 {
		t.Fatalf("seeds = %+v, want the top-level and the interior sampler", seeds)
	}
	if seeds[0].NodeID != "3" || seeds[0].Current != "777" {
		t.Errorf("top-level seed = %+v, want 777 on #3", seeds[0])
	}
	if seeds[1].NodeID != "90:3" || seeds[1].Current != "888" {
		t.Errorf("interior seed = %+v, want 888 on #90:3", seeds[1])
	}

	// Hop 2 — the persisted params row.
	params := marshalRunParams(buildRunParamsSnapshot(wf, runOptions{UIWidgetOverrides: got}))
	back, stale := runOptionsFromParams(params, "HASH-A", "HASH-A")
	if stale != "" {
		t.Fatalf("matching hashes must not be stale: %s", stale)
	}
	if back.UIWidgetOverrides[comfy.UIWidgetKey{NodeID: "90:3", Widget: 0}] != "888" {
		t.Errorf("scoped key did not survive the params round-trip: %+v", back.UIWidgetOverrides)
	}
	if back.UIWidgetOverrides[comfy.UIWidgetKey{NodeID: "3", Widget: 0}] != "777" {
		t.Errorf("top-level key did not survive the params round-trip: %+v", back.UIWidgetOverrides)
	}
}

// TestBatchFreshSeedsReachSubgraphInteriors is the batch-level assertion at the web
// layer: withFreshSeeds walks the keys resolved from the graph, and every item's
// applied graph must carry a DIFFERENT interior seed. Before subgraph reach these 13
// library workflows exposed no seed at all, so "Queue ×N" could only offer N identical
// runs.
func TestBatchFreshSeedsReachSubgraphInteriors(t *testing.T) {
	keys := comfy.SeedWidgetKeys([]byte(subgraphKeyGraph))
	if len(keys) != 2 {
		t.Fatalf("seed keys = %+v, want two", keys)
	}
	seen := map[string]bool{}
	for i := 0; i < 6; i++ {
		opts := withFreshSeeds(runOptions{}, keys)
		applied := comfy.ApplyUIWidgetOverrides([]byte(subgraphKeyGraph), opts.UIWidgetOverrides)
		seeds := comfy.SeedRunInputs(applied)
		if len(seeds) != 2 {
			t.Fatalf("item %d: seeds = %+v", i, seeds)
		}
		set := seeds[0].Current + "/" + seeds[1].Current
		if seen[set] {
			t.Fatalf("item %d repeated the seed set %s", i, set)
		}
		seen[set] = true
	}
}
