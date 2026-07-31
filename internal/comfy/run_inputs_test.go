package comfy

import (
	"encoding/json"
	"testing"
)

// findRunInput returns the detected input for (nodeID, inputName), or nil.
func findRunInput(inputs []RunInput, nodeID, name string) *RunInput {
	for i := range inputs {
		if inputs[i].NodeID == nodeID && inputs[i].InputName == name {
			return &inputs[i]
		}
	}
	return nil
}

// txt2imgUIGraph is a minimal but realistic UI-format ("Save") graph with a positive
// and negative CLIPTextEncode (the positive's `text` is link-connected to test the
// skip), a KSampler (seed carries a control_after_generate slot), and an
// EmptyLatentImage. Node ids are the litegraph integer ids preserved into the api
// graph.
const txt2imgUIGraph = `{
  "nodes": [
    {"id": 6, "type": "CLIPTextEncode", "title": "Positive",
     "widgets_values": ["a scenic mountain"],
     "inputs": [{"name": "clip", "type": "CLIP", "link": 3}]},
    {"id": 7, "type": "CLIPTextEncode", "title": "Negative",
     "widgets_values": ["blurry, low quality"],
     "inputs": [{"name": "clip", "type": "CLIP", "link": 5},
                {"name": "text", "type": "STRING", "link": 42, "widget": {"name": "text"}}]},
    {"id": 3, "type": "KSampler",
     "widgets_values": [156680208700286, "randomize", 20, 8.0, "euler", "normal", 1.0],
     "inputs": [{"name": "model", "type": "MODEL", "link": 1},
                {"name": "positive", "type": "CONDITIONING", "link": 4},
                {"name": "negative", "type": "CONDITIONING", "link": 6},
                {"name": "latent_image", "type": "LATENT", "link": 2}]},
    {"id": 5, "type": "EmptyLatentImage",
     "widgets_values": [1024, 768, 2],
     "inputs": []}
  ],
  "links": []
}`

func TestDetectRunInputsCuratedSet(t *testing.T) {
	inputs := DetectRunInputs(json.RawMessage(txt2imgUIGraph), nil)

	// Positive prompt (node 6): exposed, titled so it disambiguates from negative.
	pos := findRunInput(inputs, "6", "text")
	if pos == nil {
		t.Fatalf("positive prompt not detected:\n%+v", inputs)
	}
	if pos.Kind != RunInputText || pos.Current != "a scenic mountain" {
		t.Errorf("positive prompt: kind=%q current=%q", pos.Kind, pos.Current)
	}
	if pos.Label != "Prompt (Positive)" {
		t.Errorf("positive prompt label should carry the node title, got %q", pos.Label)
	}

	// Node 7's `text` is LINK-CONNECTED → it must be skipped (value comes from a link).
	if neg := findRunInput(inputs, "7", "text"); neg != nil {
		t.Errorf("link-connected text input must be skipped, got %+v", neg)
	}

	// KSampler (node 3): seed value read PAST the control_after_generate slot, so
	// steps/cfg/denoise land on the right widgets_values positions.
	ks := map[string]struct {
		kind RunInputKind
		cur  string
	}{
		"seed":         {RunInputSeed, "156680208700286"},
		"steps":        {RunInputInt, "20"},
		"cfg":          {RunInputFloat, "8.0"},
		"sampler_name": {RunInputSelect, "euler"},
		"scheduler":    {RunInputSelect, "normal"},
		"denoise":      {RunInputFloat, "1.0"},
	}
	for name, want := range ks {
		ri := findRunInput(inputs, "3", name)
		if ri == nil {
			t.Errorf("KSampler input %q not detected", name)
			continue
		}
		if ri.Kind != want.kind || ri.Current != want.cur {
			t.Errorf("KSampler %q: kind=%q current=%q, want kind=%q current=%q",
				name, ri.Kind, ri.Current, want.kind, want.cur)
		}
	}

	// EmptyLatentImage (node 5): width/height/batch_size.
	for name, want := range map[string]string{"width": "1024", "height": "768", "batch_size": "2"} {
		ri := findRunInput(inputs, "5", name)
		if ri == nil || ri.Current != want {
			t.Errorf("EmptyLatentImage %q: got %+v, want current %q", name, ri, want)
		}
	}

	// Without object_info, selects have no choices (the UI degrades to text fields).
	if s := findRunInput(inputs, "3", "sampler_name"); s != nil && len(s.Choices) != 0 {
		t.Errorf("sampler choices should be empty without object_info, got %v", s.Choices)
	}
}

// TestDetectRunInputsSelectChoicesFromObjectInfo proves sampler_name/scheduler pick
// up their enum options when /object_info is supplied.
func TestDetectRunInputsSelectChoicesFromObjectInfo(t *testing.T) {
	const info = `{
	  "KSampler": {"input": {"required": {
	    "sampler_name": [["euler","dpmpp_2m","ddim"]],
	    "scheduler": [["normal","karras"]]
	  }}, "input_order": {"required": ["sampler_name","scheduler"]}}
	}`
	var oi ObjectInfo
	if err := json.Unmarshal([]byte(info), &oi); err != nil {
		t.Fatalf("object_info: %v", err)
	}
	inputs := DetectRunInputs(json.RawMessage(txt2imgUIGraph), oi)
	s := findRunInput(inputs, "3", "sampler_name")
	if s == nil || len(s.Choices) != 3 || s.Choices[1] != "dpmpp_2m" {
		t.Errorf("sampler choices from object_info: got %+v", s)
	}
	sch := findRunInput(inputs, "3", "scheduler")
	if sch == nil || len(sch.Choices) != 2 {
		t.Errorf("scheduler choices from object_info: got %+v", sch)
	}
}

// TestDetectRunInputsSkipsUninstantiatedSubgraph pins the remaining rule after
// subgraph interiors became reachable: a definition that NOTHING INSTANTIATES is dead
// weight in definitions.subgraphs[] — flattening never clones it, so it contributes no
// node to the submitted graph and must contribute no editable field either.
//
// (This test previously asserted that NO interior is ever scanned. That stopped being
// true when interiors of instantiated definitions became editable, and it kept passing
// only because its fixture had no instance and it looked up the BARE interior id "99"
// — which is not the id an interior input carries any more. Both halves below are now
// checked against the scoped id as well, so neither can go vacuous the same way.)
func TestDetectRunInputsSkipsUninstantiatedSubgraph(t *testing.T) {
	const graph = `{
	  "nodes": [
	    {"id": 1, "type": "EmptyLatentImage", "widgets_values": [512, 512, 1], "inputs": []}
	  ],
	  "links": [],
	  "definitions": {"subgraphs": [
	    {"id": "sg1", "nodes": [
	      {"id": 99, "type": "KSampler",
	       "widgets_values": [7, "fixed", 30, 7.5, "euler", "normal", 1.0], "inputs": []}
	    ]}
	  ]}
	}`
	inputs := DetectRunInputs(json.RawMessage(graph), nil)
	if findRunInput(inputs, "1", "width") == nil {
		t.Error("top-level EmptyLatentImage should be detected")
	}
	for _, ri := range inputs {
		if ri.ClassType == "KSampler" {
			t.Errorf("an UNINSTANTIATED definition's interior was exposed as %+v — "+
				"flattening emits no clone for it, so the edit could never land", ri)
		}
	}

	// The contrast that keeps the assertion above honest: add ONE instance of the very
	// same definition and the interior must appear, scoped to that instance. Without
	// this half, deleting the interior scan entirely would leave the test green.
	const instantiated = `{
	  "nodes": [
	    {"id": 1, "type": "EmptyLatentImage", "widgets_values": [512, 512, 1], "inputs": []},
	    {"id": 90, "type": "sg1", "mode": 0, "inputs": []}
	  ],
	  "links": [],
	  "definitions": {"subgraphs": [
	    {"id": "sg1", "nodes": [
	      {"id": 99, "type": "KSampler",
	       "widgets_values": [7, "fixed", 30, 7.5, "euler", "normal", 1.0], "inputs": []}
	    ]}
	  ]}
	}`
	got := DetectRunInputs(json.RawMessage(instantiated), nil)
	if findRunInput(got, "90:99", "steps") == nil {
		t.Errorf("an INSTANTIATED definition's interior must be exposed as 90:99, got %+v", got)
	}
	if findRunInput(got, "99", "steps") != nil {
		t.Error("the interior must never be addressed by its bare id — that collides " +
			"with a top-level node")
	}
}

// TestDetectRunInputsNoWidgetsAndAPIGraph proves an api-format graph (no
// widgets_values / no nodes[]) yields nothing rather than erroring.
func TestDetectRunInputsNoWidgetsAndAPIGraph(t *testing.T) {
	if got := DetectRunInputs(json.RawMessage(`{"3":{"class_type":"KSampler","inputs":{"steps":20}}}`), nil); len(got) != 0 {
		t.Errorf("api-format graph should yield no run inputs, got %+v", got)
	}
	if got := DetectRunInputs(json.RawMessage(`not json`), nil); got != nil {
		t.Errorf("malformed graph should yield nil, got %+v", got)
	}
}

// TestDetectRunInputsSDXLPrompts proves the SDXL encoder's two prompt boxes are read
// PAST the six leading sizing ints.
func TestDetectRunInputsSDXLPrompts(t *testing.T) {
	const graph = `{"nodes": [
	  {"id": 10, "type": "CLIPTextEncodeSDXL",
	   "widgets_values": [1024, 1024, 0, 0, 1024, 1024, "hello G", "hello L"],
	   "inputs": [{"name":"clip","type":"CLIP","link":1}]}
	], "links": []}`
	inputs := DetectRunInputs(json.RawMessage(graph), nil)
	g := findRunInput(inputs, "10", "text_g")
	l := findRunInput(inputs, "10", "text_l")
	if g == nil || g.Current != "hello G" {
		t.Errorf("text_g: got %+v", g)
	}
	if l == nil || l.Current != "hello L" {
		t.Errorf("text_l: got %+v", l)
	}
}
