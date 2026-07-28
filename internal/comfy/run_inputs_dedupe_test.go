package comfy

import (
	"encoding/json"
	"strings"
	"testing"
)

// sharedPrimitiveGraph: ONE `easy int` drives BOTH KSamplers' steps (a base+refiner
// pair — a very common shape). Both consumers resolve to the same holding widget, so
// they are ONE editable value: emitting a field per consumer would post the same
// (node, widget) key twice and the override map would keep only the last, silently
// discarding whichever field the user actually edited.
const sharedPrimitiveGraph = `{"nodes":[
  {"id":2,"type":"easy int","title":"Steps","widgets_values":[28],"outputs":[{"name":"int"}]},
  {"id":9,"type":"KSampler","title":"Base",
   "widgets_values":[5,"fixed",20,8.0,"euler","normal",1.0],
   "inputs":[{"name":"steps","type":"INT","widget":{"name":"steps"},"link":1}]},
  {"id":10,"type":"KSampler","title":"Refiner",
   "widgets_values":[5,"fixed",20,8.0,"euler","normal",1.0],
   "inputs":[{"name":"steps","type":"INT","widget":{"name":"steps"},"link":2}]}
],"links":[[1,2,0,9,1,"INT"],[2,2,0,10,1,"INT"]]}`

// TestDetectRunInputsDedupesSharedUpstreamWidget is the F1 regression: two consumers
// of one upstream widget must collapse to a SINGLE RunInput carrying the consumer
// count, so exactly one form field is rendered and one override key is posted.
func TestDetectRunInputsDedupesSharedUpstreamWidget(t *testing.T) {
	inputs := DetectRunInputs(json.RawMessage(sharedPrimitiveGraph), nil)

	var steps []RunInput
	for _, ri := range inputs {
		if ri.NodeID == "2" && ri.WidgetIndex == 0 {
			steps = append(steps, ri)
		}
	}
	if len(steps) != 1 {
		t.Fatalf("shared upstream widget must yield exactly ONE run input, got %d:\n%+v", len(steps), steps)
	}
	if steps[0].Consumers != 2 {
		t.Errorf("consumers = %d, want 2 (the field drives both KSamplers)", steps[0].Consumers)
	}
	if steps[0].Current != "28" || steps[0].Kind != RunInputInt {
		t.Errorf("prefill wrong: %+v", steps[0])
	}
	// The label comes from the FIRST consumer in nodes[] order — deterministic.
	if steps[0].Label != "Steps" {
		t.Errorf("label = %q", steps[0].Label)
	}

	// No two run inputs may EVER share an override key — that is the invariant the
	// whole (node id, widget index) scheme rests on.
	seen := map[UIWidgetKey]bool{}
	for _, ri := range inputs {
		k := UIWidgetKey{NodeID: ri.NodeID, Widget: ri.WidgetIndex}
		if seen[k] {
			t.Errorf("duplicate override key %+v in the detected set", k)
		}
		seen[k] = true
	}
}

// TestDetectRunInputsDedupesSDXLPromptPair covers the other common shape: one string
// node feeding an SDXL encoder's text_g AND text_l.
func TestDetectRunInputsDedupesSDXLPromptPair(t *testing.T) {
	const graph = `{"nodes":[
	  {"id":2,"type":"PrimitiveString","widgets_values":["one shared prompt"],"outputs":[{"name":"s"}]},
	  {"id":10,"type":"CLIPTextEncodeSDXL",
	   "widgets_values":[1024,1024,0,0,1024,1024,"","" ],
	   "inputs":[{"name":"text_g","type":"STRING","widget":{"name":"text_g"},"link":1},
	             {"name":"text_l","type":"STRING","widget":{"name":"text_l"},"link":2}]}
	],"links":[[1,2,0,10,0,"STRING"],[2,2,0,10,1,"STRING"]]}`

	inputs := DetectRunInputs(json.RawMessage(graph), nil)
	if len(inputs) != 1 {
		t.Fatalf("one shared prompt node → one field, got %d:\n%+v", len(inputs), inputs)
	}
	if inputs[0].Consumers != 2 || inputs[0].Current != "one shared prompt" {
		t.Errorf("got %+v", inputs[0])
	}
}

// TestRunInputSourceViaReportsTheFollowedPath is the F6 visibility requirement: the
// chain the resolver followed (which upstream INPUT it took at each pass-through hop)
// is reported, so a wrong structural pick is visible rather than silent.
func TestRunInputSourceViaReportsTheFollowedPath(t *testing.T) {
	graph := loadTestdata(t, "wf587_converted_widgets.json")
	ri := findRunInput(DetectRunInputs(graph, nil), "3", "text")
	if ri == nil {
		t.Fatal("positive prompt not detected")
	}
	if len(ri.SourceVia) != 1 || !strings.Contains(ri.SourceVia[0], "RegexReplace.string") {
		t.Errorf("SourceVia should name the pass-through hop, got %v", ri.SourceVia)
	}
	// A single-hop resolution has no pass-through to report.
	seed := findRunInput(DetectRunInputs(graph, nil), "40", "seed")
	if seed == nil || len(seed.SourceVia) != 0 {
		t.Errorf("single-hop resolution should report no via hops, got %+v", seed)
	}
}

// TestChooseSourceWidgetPrefersSameNamedSlot proves the tie-break between several
// type-compatible linked widgets prefers the slot NAMED like the consumer's input
// (the same semantic value) over mere inputs[] order.
func TestChooseSourceWidgetPrefersSameNamedSlot(t *testing.T) {
	const graph = `{"nodes":[
	  {"id":9,"type":"CLIPTextEncode","widgets_values":[""],
	   "inputs":[{"name":"text","type":"STRING","widget":{"name":"text"},"link":1}]},
	  {"id":2,"type":"Combiner","widgets_values":["",""],
	   "inputs":[{"name":"prefix","type":"STRING","widget":{"name":"prefix"},"link":2},
	             {"name":"text","type":"STRING","widget":{"name":"text"},"link":3}]},
	  {"id":3,"type":"PrimitiveString","widgets_values":["WRONG prefix"],"outputs":[{"name":"s"}]},
	  {"id":4,"type":"PrimitiveString","widgets_values":["the real prompt"],"outputs":[{"name":"s"}]}
	],"links":[[1,2,0,9,0,"STRING"],[2,3,0,2,0,"STRING"],[3,4,0,2,1,"STRING"]]}`

	ri := findRunInputFor(DetectRunInputs(json.RawMessage(graph), nil), "CLIPTextEncode", "text")
	if ri == nil {
		t.Fatal("prompt not detected")
	}
	if ri.NodeID != "4" || ri.Current != "the real prompt" {
		t.Errorf("should follow the same-named `text` slot, got node %s = %q", ri.NodeID, ri.Current)
	}
}

// TestHopCapBoundary pins the exact hop-cap boundary (the audit noted it was tested
// at cap-1 and cap+2 only): a chain needing exactly maxRunInputHops hops resolves, one
// needing maxRunInputHops+1 does not.
func TestHopCapBoundary(t *testing.T) {
	// passthroughChain(n) needs n+1 hops: n pass-throughs then the primitive.
	for _, tc := range []struct {
		passthroughs int
		wantResolved bool
	}{
		{maxRunInputHops - 2, true},  // 7 hops
		{maxRunInputHops - 1, true},  // 8 hops — exactly the cap
		{maxRunInputHops, false},     // 9 hops
		{maxRunInputHops + 1, false}, // 10 hops
	} {
		graph := passthroughChain(tc.passthroughs)
		ri := findRunInputFor(DetectRunInputs(json.RawMessage(graph), nil), "KSampler", "steps")
		if got := ri != nil; got != tc.wantResolved {
			t.Errorf("%d pass-throughs (%d hops): resolved=%v, want %v",
				tc.passthroughs, tc.passthroughs+1, got, tc.wantResolved)
		}
	}
}

// passthroughChain wires KSampler.steps through n pass-through nodes to a primitive.
func passthroughChain(n int) string {
	nodes := []string{`{"id":9,"type":"KSampler",` +
		`"widgets_values":[5,"fixed",20,8.0,"euler","normal",1.0],` +
		`"inputs":[{"name":"steps","type":"INT","widget":{"name":"steps"},"link":1}]}`}
	var links []string
	for i := 1; i <= n; i++ {
		id := 100 + i
		nodes = append(nodes, `{"id":`+itoa(id)+`,"type":"Passthrough","widgets_values":[0],`+
			`"inputs":[{"name":"value","type":"INT","widget":{"name":"value"},"link":`+itoa(i+1)+`}]}`)
		links = append(links, `[`+itoa(i)+`,`+itoa(id)+`,0,`+itoa(prevID(i, id))+`,0,"INT"]`)
	}
	nodes = append(nodes, `{"id":999,"type":"PrimitiveInt","widgets_values":[7],"inputs":[]}`)
	links = append(links, `[`+itoa(n+1)+`,999,0,`+itoa(100+n)+`,0,"INT"]`)
	return `{"nodes":[` + strings.Join(nodes, ",") + `],"links":[` + strings.Join(links, ",") + `]}`
}
