package comfy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// This file covers the CONVERTED-WIDGET case: a curated node's parameter has been
// turned into an input (litegraph "widget → input") and is driven from upstream, so
// the value does NOT live on the curated node. Detection must follow the chain to the
// node that holds it, and an override must land there (and round-trip through
// ConvertUIToAPI into the submitted api graph).

// findRunInputFor looks a detected input up by the CONSUMER's semantics (class +
// input name) rather than by node id — with upstream resolution the node id is the
// SOURCE's, which is what the test is asserting.
func findRunInputFor(inputs []RunInput, classType, name string) *RunInput {
	for i := range inputs {
		if inputs[i].ClassType == classType && inputs[i].InputName == name {
			return &inputs[i]
		}
	}
	return nil
}

func loadTestdata(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return b
}

// TestDetectRunInputsRealConvertedWidgetGraph is the regression fixture for the live
// bug: workflow 587's real graph shape, where EVERY parameter-bearing widget on the
// curated nodes has been converted to an input and wired from a primitive or a custom
// node. Before the upstream walk, this graph surfaced ONLY sampler_name + scheduler
// (the two KSampler widgets that stayed unlinked).
//
// The expectations below (node id, widgets_values index, value, label) were read off
// the REAL stored graph; the index → api-input-name mapping they imply was checked
// against the live /object_info in testdata/object_info_subset_wf587.json.
func TestDetectRunInputsRealConvertedWidgetGraph(t *testing.T) {
	graph := loadTestdata(t, "wf587_converted_widgets.json")
	inputs := DetectRunInputs(graph, nil)

	cases := []struct {
		class, input string
		wantNode     string
		wantWidget   int
		wantKind     RunInputKind
		wantCurrent  string
		wantLabel    string
		wantResolved bool
	}{
		// The prompt chain: ImpactWildcardProcessor → RegexReplace → CLIPTextEncode.text.
		// The text is TWO hops upstream and the CLIPTextEncode's own widget holds "".
		{"CLIPTextEncode", "text", "3", 0, RunInputText,
			"masterpiece, best quality, amazing quality, absurdres,\n1girl, solo, portrait,",
			"Prompt (POSITIVE)", true},
		// KSampler's four linked widgets resolve to their driving nodes.
		{"KSampler", "seed", "40", 0, RunInputSeed, "-1", "Seed", true},
		{"KSampler", "steps", "17", 0, RunInputInt, "28", "Steps", true},
		{"KSampler", "cfg", "18", 0, RunInputFloat, "6", "CFG", true},
		{"KSampler", "denoise", "35", 0, RunInputFloat, "1", "Denoise", true},
		// sampler_name / scheduler are NOT converted — the unchanged plain path, still
		// reading KSampler's own widgets_values (indices 4 and 5, past the seed's
		// control_after_generate slot).
		{"KSampler", "sampler_name", "41", 4, RunInputSelect, "euler_ancestral", "Sampler", false},
		{"KSampler", "scheduler", "41", 5, RunInputSelect, "normal", "Scheduler", false},
		// EmptyLatentImage's three dimensions are all linked to `easy int` nodes.
		{"EmptyLatentImage", "width", "1", 0, RunInputInt, "1024", "Width", true},
		{"EmptyLatentImage", "height", "11", 0, RunInputInt, "1536", "Height", true},
		{"EmptyLatentImage", "batch_size", "23", 0, RunInputInt, "1", "Batch size", true},
	}
	for _, tc := range cases {
		t.Run(tc.class+"."+tc.input+"@"+tc.wantNode, func(t *testing.T) {
			// Look the input up by the node the value is expected to live on: the graph
			// has TWO CLIPTextEncode prompts, resolving to two different source nodes.
			ri := findRunInput(inputs, tc.wantNode, tc.input)
			if ri == nil {
				t.Fatalf("%s.%s not detected; detected set:\n%+v", tc.class, tc.input, inputs)
			}
			if ri.ClassType != tc.class {
				t.Errorf("consumer class = %q, want %q", ri.ClassType, tc.class)
			}
			if ri.WidgetIndex != tc.wantWidget {
				t.Errorf("holder = node %s widget %d, want node %s widget %d",
					ri.NodeID, ri.WidgetIndex, tc.wantNode, tc.wantWidget)
			}
			if ri.Kind != tc.wantKind {
				t.Errorf("kind = %q, want %q", ri.Kind, tc.wantKind)
			}
			if ri.Current != tc.wantCurrent {
				t.Errorf("current = %q, want %q", ri.Current, tc.wantCurrent)
			}
			if ri.Label != tc.wantLabel {
				t.Errorf("label = %q, want %q", ri.Label, tc.wantLabel)
			}
			if ri.Resolved != tc.wantResolved {
				t.Errorf("resolved = %v, want %v", ri.Resolved, tc.wantResolved)
			}
		})
	}

	// The negative prompt resolves through its OWN chain to the other wildcard node,
	// and the source node's title is what distinguishes the two.
	neg := (*RunInput)(nil)
	for i := range inputs {
		if inputs[i].InputName == "text" && inputs[i].NodeID == "4" {
			neg = &inputs[i]
		}
	}
	if neg == nil {
		t.Fatalf("negative prompt chain not resolved; detected set:\n%+v", inputs)
	}
	if neg.Label != "Prompt (NEGATIVE)" || !strings.HasPrefix(neg.Current, "bad quality, worst quality") {
		t.Errorf("negative prompt: label=%q current=%q", neg.Label, neg.Current)
	}
	if neg.SourceClassType != "ImpactWildcardProcessor" {
		t.Errorf("negative prompt source class = %q", neg.SourceClassType)
	}
}

// TestApplyUIWidgetOverridesRoundTripsIntoAPIGraph is the end-to-end proof that an
// EDITED value actually reaches ComfyUI: override the resolved prompt/seed/steps slots
// on 587's real graph shape, convert with the live-captured /object_info, and assert
// the SUBMITTED api graph carries the new values on the right nodes AND inputs.
//
// This is the step most likely to silently no-op: the curated node's own widget slot
// is ignored by ComfyUI once the widget is linked, so an override written there would
// never be seen.
func TestApplyUIWidgetOverridesRoundTripsIntoAPIGraph(t *testing.T) {
	graph := loadTestdata(t, "wf587_converted_widgets.json")
	var info ObjectInfo
	if err := json.Unmarshal(loadTestdata(t, "object_info_subset_wf587.json"), &info); err != nil {
		t.Fatalf("object_info: %v", err)
	}
	inputs := DetectRunInputs(graph, nil)

	const newPrompt = "a completely different prompt, edited by the user"
	overrides := map[UIWidgetKey]string{}
	// (holder node id, consumer input name, new value) — the holder is what
	// DetectRunInputs resolved the parameter to.
	for _, want := range []struct {
		node, input, val string
	}{
		{"3", "text", newPrompt}, // the positive prompt, 2 hops upstream
		{"40", "seed", "123456789012345"},
		{"17", "steps", "42"},
		{"41", "sampler_name", "dpmpp_2m"}, // unconverted — lives on the KSampler
		{"1", "width", "768"},
	} {
		ri := findRunInput(inputs, want.node, want.input)
		if ri == nil {
			t.Fatalf("node %s input %s not detected", want.node, want.input)
		}
		overrides[UIWidgetKey{NodeID: ri.NodeID, Widget: ri.WidgetIndex}] = want.val
	}

	edited := ApplyUIWidgetOverrides(graph, overrides)
	if string(edited) == string(graph) {
		t.Fatal("ApplyUIWidgetOverrides made no change")
	}
	// The stored bytes must be untouched (the caller passes the stored workflow).
	if !strings.Contains(string(graph), `"masterpiece, best quality`) {
		t.Fatal("input graph was mutated in place")
	}

	api, _, err := ConvertUIToAPI(edited, info)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	var nodes map[string]struct {
		ClassType string                     `json:"class_type"`
		Inputs    map[string]json.RawMessage `json:"inputs"`
	}
	if err := json.Unmarshal(api, &nodes); err != nil {
		t.Fatalf("api graph: %v", err)
	}

	for _, tc := range []struct {
		node, input, want string
	}{
		// The prompt landed on the WILDCARD node's wildcard_text — the api-input name
		// the converter derived from the live schema for widgets_values[0].
		{"3", "wildcard_text", `"` + newPrompt + `"`},
		{"40", "seed", "123456789012345"}, // Seed (rgthree)
		{"17", "value", "42"},             // easy int driving KSampler.steps
		{"41", "sampler_name", `"dpmpp_2m"`},
		{"1", "value", "768"}, // easy int driving EmptyLatentImage.width
	} {
		n, ok := nodes[tc.node]
		if !ok {
			t.Errorf("api graph has no node %s", tc.node)
			continue
		}
		got := strings.TrimSpace(string(n.Inputs[tc.input]))
		if got != tc.want {
			t.Errorf("node %s (%s) input %q = %s, want %s", tc.node, n.ClassType, tc.input, got, tc.want)
		}
	}

	// The consumer's own slots stay LINKS — proof the value had to be written upstream.
	for _, tc := range []struct{ node, input string }{
		{"41", "seed"}, {"41", "steps"}, {"36", "text"}, {"25", "width"},
	} {
		raw := strings.TrimSpace(string(nodes[tc.node].Inputs[tc.input]))
		if !strings.HasPrefix(raw, "[") {
			t.Errorf("node %s input %q should still be a link ref, got %s", tc.node, tc.input, raw)
		}
	}
	// And the edited prompt must not have been smuggled onto the CLIPTextEncode.
	if strings.Contains(string(nodes["36"].Inputs["text"]), newPrompt) {
		t.Error("prompt override written to the consumer node instead of the source")
	}
}

// TestResolveUpstreamWidgetEdgeCases table-drives the defensive paths: a plain
// (unconverted) widget keeps working exactly as before, and every malformed or
// ambiguous chain resolves deterministically or is dropped rather than guessed at.
func TestResolveUpstreamWidgetEdgeCases(t *testing.T) {
	// wrap builds a one-KSampler graph whose `steps` widget is converted + linked to
	// link id 1, plus the caller's upstream nodes and links.
	wrap := func(extraNodes, extraLinks string) string {
		return `{"nodes":[
		  {"id":9,"type":"KSampler",
		   "widgets_values":[5,"fixed",20,8.0,"euler","normal",1.0],
		   "inputs":[{"name":"steps","type":"INT","widget":{"name":"steps"},"link":1}]}
		  ` + extraNodes + `],"links":[` + extraLinks + `]}`
	}

	tests := []struct {
		name       string
		graph      string
		wantNode   string // "" → the input must NOT be surfaced
		wantWidget int
		wantValue  string
	}{{
		name: "plain widget unchanged (no link)",
		graph: `{"nodes":[{"id":9,"type":"KSampler",
		  "widgets_values":[5,"fixed",20,8.0,"euler","normal",1.0],"inputs":[]}],"links":[]}`,
		wantNode: "9", wantWidget: 2, wantValue: "20",
	}, {
		name:     "single hop to a primitive",
		graph:    wrap(`,{"id":2,"type":"PrimitiveInt","widgets_values":[33],"inputs":[]}`, `[1,2,0,9,1,"INT"]`),
		wantNode: "2", wantWidget: 0, wantValue: "33",
	}, {
		name: "two hops through a passthrough node",
		graph: wrap(`,{"id":2,"type":"Passthrough",
		    "widgets_values":[0],
		    "inputs":[{"name":"value","type":"INT","widget":{"name":"value"},"link":2}]},
		  {"id":3,"type":"PrimitiveInt","widgets_values":[77],"inputs":[]}`,
			`[1,2,0,9,1,"INT"],[2,3,0,2,0,"INT"]`),
		wantNode: "3", wantWidget: 0, wantValue: "77",
	}, {
		name: "cycle is detected, nothing surfaced",
		graph: wrap(`,{"id":2,"type":"Passthrough","widgets_values":[0],
		    "inputs":[{"name":"value","type":"INT","widget":{"name":"value"},"link":2}]},
		  {"id":3,"type":"Passthrough","widgets_values":[0],
		    "inputs":[{"name":"value","type":"INT","widget":{"name":"value"},"link":3}]}`,
			`[1,2,0,9,1,"INT"],[2,3,0,2,0,"INT"],[3,2,0,3,0,"INT"]`),
		wantNode: "",
	}, {
		name:     "dangling link id",
		graph:    wrap(`,{"id":2,"type":"PrimitiveInt","widgets_values":[33],"inputs":[]}`, ``),
		wantNode: "",
	}, {
		name:     "missing upstream node",
		graph:    wrap(``, `[1,404,0,9,1,"INT"]`),
		wantNode: "",
	}, {
		name: "bypassed source is never editable",
		graph: wrap(`,{"id":2,"type":"PrimitiveInt","mode":4,"widgets_values":[33],"inputs":[]}`,
			`[1,2,0,9,1,"INT"]`),
		wantNode: "",
	}, {
		name: "muted source is never editable",
		graph: wrap(`,{"id":2,"type":"PrimitiveInt","mode":2,"widgets_values":[33],"inputs":[]}`,
			`[1,2,0,9,1,"INT"]`),
		wantNode: "",
	}, {
		name: "source with no type-compatible widget",
		graph: wrap(`,{"id":2,"type":"StringOnly","widgets_values":["nope","also nope"],"inputs":[]}`,
			`[1,2,0,9,1,"INT"]`),
		wantNode: "",
	}, {
		name: "multiple local candidates → first widget slot wins",
		graph: wrap(`,{"id":2,"type":"TwoInts","widgets_values":["label",11,22],"inputs":[]}`,
			`[1,2,0,9,1,"INT"]`),
		wantNode: "2", wantWidget: 1, wantValue: "11",
	}, {
		name: "multiple linked candidates → first inputs[] slot wins",
		graph: wrap(`,{"id":2,"type":"TwoWay","widgets_values":[0,0],
		    "inputs":[{"name":"a","type":"INT","widget":{"name":"a"},"link":2},
		              {"name":"b","type":"INT","widget":{"name":"b"},"link":3}]},
		  {"id":3,"type":"PrimitiveInt","widgets_values":[111],"inputs":[]},
		  {"id":4,"type":"PrimitiveInt","widgets_values":[222],"inputs":[]}`,
			`[1,2,0,9,1,"INT"],[2,3,0,2,0,"INT"],[3,4,0,2,1,"INT"]`),
		wantNode: "3", wantWidget: 0, wantValue: "111",
	}, {
		name: "a type-INCOMPATIBLE linked widget is ignored, the local slot wins",
		graph: wrap(`,{"id":2,"type":"Mixed","widgets_values":[55,"txt"],
		    "inputs":[{"name":"note","type":"STRING","widget":{"name":"note"},"link":2}]},
		  {"id":3,"type":"PrimitiveString","widgets_values":["s"],"inputs":[]}`,
			`[1,2,0,9,1,"INT"],[2,3,0,2,0,"STRING"]`),
		wantNode: "2", wantWidget: 0, wantValue: "55",
	}, {
		name: "COMBO array slot type is not followed",
		graph: wrap(`,{"id":2,"type":"Combo","widgets_values":[3],
		    "inputs":[{"name":"opt","type":["a","b"],"widget":{"name":"opt"},"link":2}]},
		  {"id":3,"type":"PrimitiveInt","widgets_values":[999],"inputs":[]}`,
			`[1,2,0,9,1,"INT"],[2,3,0,2,0,"INT"]`),
		wantNode: "2", wantWidget: 0, wantValue: "3",
	}}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ri := findRunInputFor(DetectRunInputs(json.RawMessage(tc.graph), nil), "KSampler", "steps")
			if tc.wantNode == "" {
				if ri != nil {
					t.Fatalf("steps should not be surfaced, got %+v", ri)
				}
				return
			}
			if ri == nil {
				t.Fatal("steps not detected")
			}
			if ri.NodeID != tc.wantNode || ri.WidgetIndex != tc.wantWidget || ri.Current != tc.wantValue {
				t.Errorf("got node %s widget %d value %q, want node %s widget %d value %q",
					ri.NodeID, ri.WidgetIndex, ri.Current, tc.wantNode, tc.wantWidget, tc.wantValue)
			}
		})
	}
}

// TestResolveUpstreamWidgetHopCap builds a passthrough chain LONGER than
// maxRunInputHops and proves the walk gives up (rather than recursing forever or
// surfacing a value the run would not use), while a chain exactly at the cap resolves.
func TestResolveUpstreamWidgetHopCap(t *testing.T) {
	// chain(n) wires KSampler.steps through n passthrough nodes to a primitive.
	chain := func(n int) string {
		var nodes, links []string
		nodes = append(nodes, `{"id":9,"type":"KSampler",`+
			`"widgets_values":[5,"fixed",20,8.0,"euler","normal",1.0],`+
			`"inputs":[{"name":"steps","type":"INT","widget":{"name":"steps"},"link":1}]}`)
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

	// maxRunInputHops hops == (maxRunInputHops-1) passthroughs + the primitive.
	atCap := DetectRunInputs(json.RawMessage(chain(maxRunInputHops-1)), nil)
	if ri := findRunInputFor(atCap, "KSampler", "steps"); ri == nil || ri.NodeID != "999" {
		t.Errorf("chain at the hop cap should resolve to the primitive, got %+v", ri)
	}
	overCap := DetectRunInputs(json.RawMessage(chain(maxRunInputHops+2)), nil)
	if ri := findRunInputFor(overCap, "KSampler", "steps"); ri != nil {
		t.Errorf("chain past the hop cap must not be surfaced, got %+v", ri)
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

// prevID is the node the i-th passthrough feeds: the KSampler (id 9) for the first,
// otherwise the previous passthrough.
func prevID(i, id int) int {
	if i == 1 {
		return 9
	}
	return id - 1
}

// TestDetectRunInputsSkipsInactiveConsumer proves a bypassed/muted CURATED node is not
// surfaced at all — it will not execute, so editing it would be a lie.
func TestDetectRunInputsSkipsInactiveConsumer(t *testing.T) {
	for _, mode := range []string{"2", "4"} {
		graph := `{"nodes":[{"id":9,"type":"KSampler","mode":` + mode + `,
		  "widgets_values":[5,"fixed",20,8.0,"euler","normal",1.0],"inputs":[]}],"links":[]}`
		if got := DetectRunInputs(json.RawMessage(graph), nil); len(got) != 0 {
			t.Errorf("mode %s consumer should surface nothing, got %+v", mode, got)
		}
	}
}

// TestDetectRunInputsTrueLinkInputStillSkipped proves a genuine link input (no
// `widget` marker — MODEL/LATENT/CLIP…) is skipped rather than chased upstream: there
// is no widget value behind it.
func TestDetectRunInputsTrueLinkInputStillSkipped(t *testing.T) {
	const graph = `{"nodes":[
	  {"id":9,"type":"CLIPTextEncode","widgets_values":["hi"],
	   "inputs":[{"name":"text","type":"STRING","link":1}]},
	  {"id":2,"type":"PrimitiveString","widgets_values":["upstream"],"inputs":[]}
	],"links":[[1,2,0,9,0,"STRING"]]}`
	if ri := findRunInputFor(DetectRunInputs(json.RawMessage(graph), nil), "CLIPTextEncode", "text"); ri != nil {
		t.Errorf("a non-widget link input must not be surfaced, got %+v", ri)
	}
}

// TestApplyUIWidgetOverridesNarrowness pins the applier's refusals: unknown node,
// out-of-range slot, non-array widgets_values, a non-numeric value for a numeric slot,
// and a bool/array slot are all left alone; a large integer keeps full precision.
func TestApplyUIWidgetOverridesNarrowness(t *testing.T) {
	const graph = `{"nodes":[
	  {"id":1,"type":"A","widgets_values":[10,"txt",true,[1,2]]},
	  {"id":2,"type":"B","widgets_values":{"keyed":1}}
	],"links":[]}`

	if got := ApplyUIWidgetOverrides(json.RawMessage(graph), nil); string(got) != graph {
		t.Error("empty override set must be a no-op")
	}
	for _, tc := range []struct {
		name string
		key  UIWidgetKey
		val  string
	}{
		{"unknown node", UIWidgetKey{NodeID: "77", Widget: 0}, "5"},
		{"out-of-range slot", UIWidgetKey{NodeID: "1", Widget: 99}, "5"},
		{"object-form widgets_values", UIWidgetKey{NodeID: "2", Widget: 0}, "5"},
		{"non-numeric for a number slot", UIWidgetKey{NodeID: "1", Widget: 0}, "abc"},
		{"bool slot", UIWidgetKey{NodeID: "1", Widget: 2}, "false"},
		{"array slot", UIWidgetKey{NodeID: "1", Widget: 3}, "x"},
	} {
		got := ApplyUIWidgetOverrides(json.RawMessage(graph), map[UIWidgetKey]string{tc.key: tc.val})
		if string(got) != graph {
			t.Errorf("%s: graph should be returned unchanged, got %s", tc.name, got)
		}
	}

	// Type preservation: string stays a string, a 19-digit seed keeps full precision.
	got := ApplyUIWidgetOverrides(json.RawMessage(graph), map[UIWidgetKey]string{
		{NodeID: "1", Widget: 0}: "1234567890123456789",
		{NodeID: "1", Widget: 1}: "new text",
	})
	if !strings.Contains(string(got), "1234567890123456789") {
		t.Errorf("large integer lost precision: %s", got)
	}
	if !strings.Contains(string(got), `"new text"`) {
		t.Errorf("string slot not rewritten: %s", got)
	}
	if !strings.Contains(string(got), "true") || !strings.Contains(string(got), "[1,2]") {
		t.Errorf("untargeted slots were altered: %s", got)
	}
}
