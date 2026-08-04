package comfy

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// buildInfo assembles an ObjectInfo from a compact JSON literal for synthetic tests.
func buildInfo(t *testing.T, raw string) ObjectInfo {
	t.Helper()
	var oi ObjectInfo
	if err := json.Unmarshal([]byte(raw), &oi); err != nil {
		t.Fatalf("parse synthetic object_info: %v", err)
	}
	return oi
}

// convertNodes converts and returns the api graph decoded into a node map.
func convertNodes(t *testing.T, ui string, info ObjectInfo) (map[string]apiOutNode, []string) {
	t.Helper()
	api, warns, err := ConvertUIToAPI(json.RawMessage(ui), info)
	if err != nil {
		t.Fatalf("ConvertUIToAPI: %v (warnings: %v)", err, warns)
	}
	if f, derr := DetectFormat(api); derr != nil || f != FormatAPI {
		t.Fatalf("output is not api-format (f=%q err=%v): %s", f, derr, api)
	}
	var nodes map[string]apiOutNode
	if err := json.Unmarshal(api, &nodes); err != nil {
		t.Fatalf("decode api graph: %v", err)
	}
	return nodes, warns
}

// scalarString returns the string value of an input that is a JSON string scalar.
func scalarString(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("input %s is not a string scalar: %v", raw, err)
	}
	return s
}

func TestConvertCheckpointWidget(t *testing.T) {
	info := buildInfo(t, `{
		"CheckpointLoaderSimple": {
			"input": {"required": {"ckpt_name": [["a.safetensors","b.safetensors"], {}]}},
			"input_order": {"required": ["ckpt_name"]}
		}
	}`)
	ui := `{"nodes":[
		{"id":4,"type":"CheckpointLoaderSimple","mode":0,"widgets_values":["b.safetensors"]}
	],"links":[]}`
	nodes, warns := convertNodes(t, ui, info)
	if len(warns) != 0 {
		t.Errorf("unexpected warnings: %v", warns)
	}
	n, ok := nodes["4"]
	if !ok {
		t.Fatalf("node 4 missing: %v", nodes)
	}
	if n.ClassType != "CheckpointLoaderSimple" {
		t.Errorf("class_type = %q", n.ClassType)
	}
	if got := scalarString(t, n.Inputs["ckpt_name"]); got != "b.safetensors" {
		t.Errorf("ckpt_name = %q", got)
	}
}

// TestConvertSeedControlOffByOne is the off-by-one guard: a KSampler whose seed
// widget carries control_after_generate must SKIP the control value so the
// following steps/cfg/sampler widgets land in the right inputs.
func TestConvertSeedControlOffByOne(t *testing.T) {
	info := buildInfo(t, `{
		"KSampler": {
			"input": {"required": {
				"model": ["MODEL", {}],
				"seed": ["INT", {"default": 0, "control_after_generate": true}],
				"steps": ["INT", {"default": 20}],
				"cfg": ["FLOAT", {"default": 8.0}],
				"sampler_name": [["euler","dpmpp_2m"], {}]
			}},
			"input_order": {"required": ["model","seed","steps","cfg","sampler_name"]}
		}
	}`)
	// widgets_values = [seed, control, steps, cfg, sampler] — the control string sits
	// between seed and steps and must be skipped.
	ui := `{"nodes":[
		{"id":3,"type":"KSampler","mode":0,
		 "widgets_values":[123456789, "randomize", 25, 7.5, "dpmpp_2m"],
		 "inputs":[{"name":"model","type":"MODEL","link":10}]},
		{"id":4,"type":"CheckpointLoaderSimple","mode":0,"widgets_values":["m.safetensors"]}
	],"links":[[10,4,0,3,0,"MODEL"]]}`
	infoWithCkpt := info
	infoWithCkpt["CheckpointLoaderSimple"] = NodeSchema{}
	// give the ckpt node a schema so it is kept (empty schema → no widgets, fine)
	nodes, warns := convertNodes(t, ui, infoWithCkpt)
	if len(warns) != 0 {
		t.Errorf("unexpected warnings: %v", warns)
	}
	k := nodes["3"]
	// seed value preserved
	if got := string(k.Inputs["seed"]); got != "123456789" {
		t.Errorf("seed = %s", got)
	}
	// control value skipped → steps/cfg/sampler correct
	if got := string(k.Inputs["steps"]); got != "25" {
		t.Errorf("steps = %s (control value likely NOT skipped)", got)
	}
	if got := string(k.Inputs["cfg"]); got != "7.5" {
		t.Errorf("cfg = %s", got)
	}
	if got := scalarString(t, k.Inputs["sampler_name"]); got != "dpmpp_2m" {
		t.Errorf("sampler_name = %q", got)
	}
	// model is a link ref ["4", 0]
	assertLinkRef(t, k.Inputs["model"], "4", 0)
}

// TestConvertPromotedWidgetKeepsSlot reproduces the 587 "Basic_V37" KSampler
// misalignment: several WIDGET inputs (seed/steps/cfg/denoise) are converted to
// input slots ("widget promotion") AND link-connected. In modern ComfyUI those
// promoted widgets KEEP their slot in widgets_values (marked by the input's
// `widget` field). The converter must consume those slots — including the seed's
// control_after_generate extra — even though the value comes from the link, or
// every later widget (sampler_name/scheduler) shifts onto the wrong slot. Before
// the fix this emitted sampler_name=<seed number> and scheduler="randomize",
// which ComfyUI rejected with value_not_in_list.
func TestConvertPromotedWidgetKeepsSlot(t *testing.T) {
	info := buildInfo(t, `{
		"KSampler": {
			"input": {"required": {
				"model": ["MODEL", {}],
				"seed": ["INT", {"default": 0, "control_after_generate": true}],
				"steps": ["INT", {"default": 20}],
				"cfg": ["FLOAT", {"default": 8.0}],
				"sampler_name": [["euler","euler_ancestral","dpmpp_2m"], {}],
				"scheduler": [["normal","karras","simple"], {}],
				"positive": ["CONDITIONING", {}],
				"negative": ["CONDITIONING", {}],
				"latent_image": ["LATENT", {}],
				"denoise": ["FLOAT", {"default": 1.0}]
			}},
			"input_order": {"required": ["model","seed","steps","cfg","sampler_name","scheduler","positive","negative","latent_image","denoise"]}
		},
		"Src": {"input":{"required":{}},"input_order":{"required":[]}}
	}`)
	// widgets_values retains ALL widget slots in order:
	//   [seed, control, steps, cfg, sampler_name, scheduler, denoise]
	// seed/steps/cfg/denoise are promoted widgets (carry a "widget" field) AND linked.
	// Only sampler_name + scheduler are plain widgets — they must receive
	// "euler_ancestral" and "normal", NOT the shifted seed/control values.
	ui := `{"nodes":[
		{"id":100,"type":"Src","mode":0},
		{"id":41,"type":"KSampler","mode":0,
		 "widgets_values":[1022776966395948,"randomize",20,8,"euler_ancestral","normal",1],
		 "inputs":[
			{"name":"model","type":"MODEL","link":49},
			{"name":"positive","type":"CONDITIONING","link":76},
			{"name":"negative","type":"CONDITIONING","link":78},
			{"name":"latent_image","type":"LATENT","link":93},
			{"name":"seed","type":"INT","link":29,"widget":{"name":"seed"}},
			{"name":"steps","type":"INT","link":109,"widget":{"name":"steps"}},
			{"name":"cfg","type":"FLOAT","link":110,"widget":{"name":"cfg"}},
			{"name":"denoise","type":"FLOAT","link":137,"widget":{"name":"denoise"}}
		 ]}
	],"links":[
		[49,100,0,41,0,"MODEL"],
		[76,100,1,41,1,"CONDITIONING"],
		[78,100,2,41,2,"CONDITIONING"],
		[93,100,3,41,3,"LATENT"],
		[29,100,4,41,4,"INT"],
		[109,100,5,41,5,"INT"],
		[110,100,6,41,6,"FLOAT"],
		[137,100,7,41,7,"FLOAT"]
	]}`
	nodes, warns := convertNodes(t, ui, info)
	if len(warns) != 0 {
		t.Errorf("unexpected warnings: %v", warns)
	}
	k := nodes["41"]
	// The two plain widgets must get their CORRECT (unshifted) values.
	if got := scalarString(t, k.Inputs["sampler_name"]); got != "euler_ancestral" {
		t.Errorf("sampler_name = %q, want %q (promoted-widget slot not consumed → value shifted)", got, "euler_ancestral")
	}
	if got := scalarString(t, k.Inputs["scheduler"]); got != "normal" {
		t.Errorf("scheduler = %q, want %q (control_after_generate slot leaked)", got, "normal")
	}
	// The promoted+linked widgets must be link refs, not the widget scalars.
	assertLinkRef(t, k.Inputs["seed"], "100", 4)
	assertLinkRef(t, k.Inputs["steps"], "100", 5)
	assertLinkRef(t, k.Inputs["cfg"], "100", 6)
	assertLinkRef(t, k.Inputs["denoise"], "100", 7)
}

// TestConvertOldFormatPromotedWidgetNoSlot guards the backward-compatible path:
// when a widget-type input is linked but carries NO `widget` marker (old ComfyUI
// removed the promoted widget's value from widgets_values), the converter must NOT
// consume a slot for it — otherwise the remaining widget values shift the other
// way. Here seed is linked without a marker, so widgets_values holds only the two
// plain widgets [sampler, scheduler].
func TestConvertOldFormatPromotedWidgetNoSlot(t *testing.T) {
	info := buildInfo(t, `{
		"KSampler": {
			"input": {"required": {
				"model": ["MODEL", {}],
				"seed": ["INT", {"default": 0, "control_after_generate": true}],
				"sampler_name": [["euler","dpmpp_2m"], {}],
				"scheduler": [["normal","karras"], {}]
			}},
			"input_order": {"required": ["model","seed","sampler_name","scheduler"]}
		},
		"Src": {"input":{"required":{}},"input_order":{"required":[]}}
	}`)
	ui := `{"nodes":[
		{"id":100,"type":"Src","mode":0},
		{"id":41,"type":"KSampler","mode":0,
		 "widgets_values":["dpmpp_2m","karras"],
		 "inputs":[
			{"name":"model","type":"MODEL","link":49},
			{"name":"seed","type":"INT","link":29}
		 ]}
	],"links":[
		[49,100,0,41,0,"MODEL"],
		[29,100,1,41,1,"INT"]
	]}`
	nodes, _ := convertNodes(t, ui, info)
	k := nodes["41"]
	if got := scalarString(t, k.Inputs["sampler_name"]); got != "dpmpp_2m" {
		t.Errorf("sampler_name = %q, want %q (old-format slot wrongly consumed)", got, "dpmpp_2m")
	}
	if got := scalarString(t, k.Inputs["scheduler"]); got != "karras" {
		t.Errorf("scheduler = %q, want %q", got, "karras")
	}
	assertLinkRef(t, k.Inputs["seed"], "100", 1)
}

func TestConvertLinkInput(t *testing.T) {
	info := buildInfo(t, `{
		"CheckpointLoaderSimple": {"input":{"required":{"ckpt_name":[["a.safetensors"],{}]}},"input_order":{"required":["ckpt_name"]}},
		"VAEDecode": {"input":{"required":{"samples":["LATENT",{}],"vae":["VAE",{}]}},"input_order":{"required":["samples","vae"]}}
	}`)
	ui := `{"nodes":[
		{"id":4,"type":"CheckpointLoaderSimple","mode":0,"widgets_values":["a.safetensors"]},
		{"id":8,"type":"VAEDecode","mode":0,"inputs":[
			{"name":"samples","type":"LATENT","link":null},
			{"name":"vae","type":"VAE","link":5}]}
	],"links":[[5,4,2,8,1,"VAE"]]}`
	nodes, _ := convertNodes(t, ui, info)
	assertLinkRef(t, nodes["8"].Inputs["vae"], "4", 2)
	if _, has := nodes["8"].Inputs["samples"]; has {
		t.Errorf("samples had a null link and should be unset, got %s", nodes["8"].Inputs["samples"])
	}
}

func TestConvertRerouteSplice(t *testing.T) {
	info := buildInfo(t, `{
		"CheckpointLoaderSimple": {"input":{"required":{"ckpt_name":[["a.safetensors"],{}]}},"input_order":{"required":["ckpt_name"]}},
		"BasicScheduler": {"input":{"required":{"model":["MODEL",{}]}},"input_order":{"required":["model"]}}
	}`)
	// checkpoint(out 0) -> reroute(in) -> reroute(out) -> scheduler.model
	ui := `{"nodes":[
		{"id":4,"type":"CheckpointLoaderSimple","mode":0,"widgets_values":["a.safetensors"]},
		{"id":50,"type":"Reroute","mode":0,"inputs":[{"name":"","type":"*","link":1}]},
		{"id":17,"type":"BasicScheduler","mode":0,"inputs":[{"name":"model","type":"MODEL","link":2}]}
	],"links":[
		[1,4,0,50,0,"MODEL"],
		[2,50,0,17,0,"MODEL"]
	]}`
	nodes, warns := convertNodes(t, ui, info)
	if len(warns) != 0 {
		t.Errorf("unexpected warnings: %v", warns)
	}
	if _, has := nodes["50"]; has {
		t.Error("reroute node should not appear in api graph")
	}
	// scheduler.model must resolve THROUGH the reroute to the checkpoint's slot 0.
	assertLinkRef(t, nodes["17"].Inputs["model"], "4", 0)
}

func TestConvertMutedNodeWarns(t *testing.T) {
	info := buildInfo(t, `{
		"CheckpointLoaderSimple": {"input":{"required":{"ckpt_name":[["a.safetensors"],{}]}},"input_order":{"required":["ckpt_name"]}},
		"BasicScheduler": {"input":{"required":{"model":["MODEL",{}]}},"input_order":{"required":["model"]}}
	}`)
	// checkpoint is MUTED (mode 2); the scheduler consuming its output must warn and
	// leave model unset, but still convert.
	ui := `{"nodes":[
		{"id":4,"type":"CheckpointLoaderSimple","mode":2,"widgets_values":["a.safetensors"]},
		{"id":17,"type":"BasicScheduler","mode":0,"inputs":[{"name":"model","type":"MODEL","link":2}]}
	],"links":[[2,4,0,17,0,"MODEL"]]}`
	nodes, warns := convertNodes(t, ui, info)
	if _, has := nodes["4"]; has {
		t.Error("muted node 4 should be dropped")
	}
	if _, has := nodes["17"].Inputs["model"]; has {
		t.Error("scheduler.model should be unset (origin muted)")
	}
	if !containsSubstr(warns, "muted node 4") {
		t.Errorf("expected a muted-node warning, got %v", warns)
	}
}

func TestConvertUnknownNodeOmitted(t *testing.T) {
	info := buildInfo(t, `{
		"CheckpointLoaderSimple": {"input":{"required":{"ckpt_name":[["a.safetensors"],{}]}},"input_order":{"required":["ckpt_name"]}}
	}`)
	ui := `{"nodes":[
		{"id":4,"type":"CheckpointLoaderSimple","mode":0,"widgets_values":["a.safetensors"]},
		{"id":65,"type":"SDXL Resolutions (JPS)","mode":0,"widgets_values":["portrait"]}
	],"links":[]}`
	nodes, warns := convertNodes(t, ui, info)
	if _, has := nodes["65"]; has {
		t.Error("unknown node 65 should be omitted")
	}
	if _, has := nodes["4"]; !has {
		t.Error("known node 4 should still convert")
	}
	if !containsSubstr(warns, `type "SDXL Resolutions (JPS)" not available`) {
		t.Errorf("expected an unknown-type warning, got %v", warns)
	}
}

// TestConvertRgthreeUIHelpersDropped asserts the three rgthree UI-only helper
// nodes (Fast Groups Muter / Fast Bypasser / Bookmark) are dropped SILENTLY (no
// warning, like Note) and that dropping them does not strand any link a kept node
// depends on. A real rgthree node present in object_info must still convert.
func TestConvertRgthreeUIHelpersDropped(t *testing.T) {
	info := buildInfo(t, `{
		"CheckpointLoaderSimple": {"input":{"required":{"ckpt_name":[["a.safetensors"],{}]}},"input_order":{"required":["ckpt_name"]}},
		"BasicScheduler": {"input":{"required":{"model":["MODEL",{}]}},"input_order":{"required":["model"]}}
	}`)
	// checkpoint -> scheduler.model (a real link that must survive), plus three
	// rgthree UI helpers that carry no execution links.
	ui := `{"nodes":[
		{"id":4,"type":"CheckpointLoaderSimple","mode":0,"widgets_values":["a.safetensors"]},
		{"id":17,"type":"BasicScheduler","mode":0,"inputs":[{"name":"model","type":"MODEL","link":1}]},
		{"id":90,"type":"Fast Groups Muter (rgthree)","mode":0},
		{"id":91,"type":"Fast Bypasser (rgthree)","mode":0},
		{"id":92,"type":"Bookmark (rgthree)","mode":0,"widgets_values":["1"]}
	],"links":[[1,4,0,17,0,"MODEL"]]}`
	nodes, warns := convertNodes(t, ui, info)
	if len(warns) != 0 {
		t.Errorf("rgthree UI helpers must be dropped silently, got warnings: %v", warns)
	}
	for _, id := range []string{"90", "91", "92"} {
		if _, has := nodes[id]; has {
			t.Errorf("rgthree UI helper node %s should be dropped", id)
		}
	}
	// The real link is not stranded: scheduler.model resolves to the checkpoint.
	assertLinkRef(t, nodes["17"].Inputs["model"], "4", 0)
	if _, has := nodes["4"]; !has {
		t.Error("checkpoint node 4 should still convert")
	}
}

// TestConvertGetSetTeleport verifies a link routed source -> SetNode("x") ...
// GetNode("x") -> consumer lowers to source -> consumer directly, and the Get/Set
// nodes are dropped with no warnings.
func TestConvertGetSetTeleport(t *testing.T) {
	info := buildInfo(t, `{
		"CheckpointLoaderSimple": {"input":{"required":{"ckpt_name":[["a.safetensors"],{}]}},"input_order":{"required":["ckpt_name"]}},
		"BasicScheduler": {"input":{"required":{"model":["MODEL",{}]}},"input_order":{"required":["model"]}}
	}`)
	// checkpoint(out 0) -> SetNode("themodel") ; GetNode("themodel") -> scheduler.model
	ui := `{"nodes":[
		{"id":4,"type":"CheckpointLoaderSimple","mode":0,"widgets_values":["a.safetensors"]},
		{"id":70,"type":"SetNode","mode":0,"title":"Set_themodel","widgets_values":["themodel"],
		 "inputs":[{"name":"MODEL","type":"MODEL","link":1}]},
		{"id":71,"type":"GetNode","mode":0,"title":"Get_themodel","widgets_values":["themodel"]},
		{"id":17,"type":"BasicScheduler","mode":0,"inputs":[{"name":"model","type":"MODEL","link":2}]}
	],"links":[
		[1,4,0,70,0,"MODEL"],
		[2,71,0,17,0,"MODEL"]
	]}`
	nodes, warns := convertNodes(t, ui, info)
	if len(warns) != 0 {
		t.Errorf("unexpected warnings: %v", warns)
	}
	if _, has := nodes["70"]; has {
		t.Error("SetNode should not appear in api graph")
	}
	if _, has := nodes["71"]; has {
		t.Error("GetNode should not appear in api graph")
	}
	// scheduler.model must resolve THROUGH Get/Set back to the checkpoint slot 0.
	assertLinkRef(t, nodes["17"].Inputs["model"], "4", 0)
}

// TestConvertGetSetTeleportWidgetOnlyName verifies the name is read from
// widgets_values when the title carries no Set_/Get_ prefix, and that a teleport
// chained through a Reroute still resolves.
func TestConvertGetSetTeleportThroughReroute(t *testing.T) {
	info := buildInfo(t, `{
		"CheckpointLoaderSimple": {"input":{"required":{"ckpt_name":[["a.safetensors"],{}]}},"input_order":{"required":["ckpt_name"]}},
		"BasicScheduler": {"input":{"required":{"model":["MODEL",{}]}},"input_order":{"required":["model"]}}
	}`)
	// checkpoint -> reroute -> SetNode("m") ; GetNode("m") -> scheduler
	ui := `{"nodes":[
		{"id":4,"type":"CheckpointLoaderSimple","mode":0,"widgets_values":["a.safetensors"]},
		{"id":50,"type":"Reroute","mode":0,"inputs":[{"name":"","type":"*","link":1}]},
		{"id":70,"type":"SetNode","mode":0,"widgets_values":["m"],"inputs":[{"name":"MODEL","type":"MODEL","link":2}]},
		{"id":71,"type":"GetNode","mode":0,"widgets_values":["m"]},
		{"id":17,"type":"BasicScheduler","mode":0,"inputs":[{"name":"model","type":"MODEL","link":3}]}
	],"links":[
		[1,4,0,50,0,"MODEL"],
		[2,50,0,70,0,"MODEL"],
		[3,71,0,17,0,"MODEL"]
	]}`
	nodes, warns := convertNodes(t, ui, info)
	if len(warns) != 0 {
		t.Errorf("unexpected warnings: %v", warns)
	}
	assertLinkRef(t, nodes["17"].Inputs["model"], "4", 0)
}

// TestConvertGetNodeMissingSet verifies a GetNode with no matching SetNode warns
// and leaves the consumer input unset (rather than panicking or mis-wiring).
func TestConvertGetNodeMissingSet(t *testing.T) {
	info := buildInfo(t, `{
		"BasicScheduler": {"input":{"required":{"model":["MODEL",{}]}},"input_order":{"required":["model"]}}
	}`)
	ui := `{"nodes":[
		{"id":71,"type":"GetNode","mode":0,"widgets_values":["ghost"]},
		{"id":17,"type":"BasicScheduler","mode":0,"inputs":[{"name":"model","type":"MODEL","link":3}]}
	],"links":[[3,71,0,17,0,"MODEL"]]}`
	nodes, warns := convertNodes(t, ui, info)
	if _, has := nodes["17"].Inputs["model"]; has {
		t.Error("scheduler.model should be unset (GetNode has no SetNode)")
	}
	if !containsSubstr(warns, `SetNode name "ghost"`) {
		t.Errorf("expected an unresolved-GetNode warning, got %v", warns)
	}
}

// TestConvertGetNodeAmbiguous verifies two SetNodes sharing a name make a GetNode
// resolution refuse (warn + unset) rather than pick a wrong source.
func TestConvertGetNodeAmbiguous(t *testing.T) {
	info := buildInfo(t, `{
		"CheckpointLoaderSimple": {"input":{"required":{"ckpt_name":[["a.safetensors"],{}]}},"input_order":{"required":["ckpt_name"]}},
		"BasicScheduler": {"input":{"required":{"model":["MODEL",{}]}},"input_order":{"required":["model"]}}
	}`)
	ui := `{"nodes":[
		{"id":4,"type":"CheckpointLoaderSimple","mode":0,"widgets_values":["a.safetensors"]},
		{"id":5,"type":"CheckpointLoaderSimple","mode":0,"widgets_values":["b.safetensors"]},
		{"id":70,"type":"SetNode","mode":0,"widgets_values":["dup"],"inputs":[{"name":"MODEL","type":"MODEL","link":1}]},
		{"id":72,"type":"SetNode","mode":0,"widgets_values":["dup"],"inputs":[{"name":"MODEL","type":"MODEL","link":2}]},
		{"id":71,"type":"GetNode","mode":0,"widgets_values":["dup"]},
		{"id":17,"type":"BasicScheduler","mode":0,"inputs":[{"name":"model","type":"MODEL","link":3}]}
	],"links":[
		[1,4,0,70,0,"MODEL"],
		[2,5,0,72,0,"MODEL"],
		[3,71,0,17,0,"MODEL"]
	]}`
	nodes, warns := convertNodes(t, ui, info)
	if _, has := nodes["17"].Inputs["model"]; has {
		t.Error("scheduler.model should be unset (ambiguous SetNode name)")
	}
	if !containsSubstr(warns, "ambiguous") {
		t.Errorf("expected an ambiguity warning, got %v", warns)
	}
}

// TestConvertBypassedSourceCleanDeadEnd is the Basic_V37 (587) case: a bypassed
// SOURCE node with ZERO connected inputs (a loader/primitive) feeding a kept
// node's input is a clean dead-end — the consumer input is legitimately left
// unset, with NO warning (mirrors ComfyUI dropping the link when a source is
// bypassed). Before the fix this warned "bypassed node N could not be spliced".
func TestConvertBypassedSourceCleanDeadEnd(t *testing.T) {
	info := buildInfo(t, `{
		"LoadImage": {"input":{"required":{"image":[["a.png"],{}]}},"input_order":{"required":["image"]}},
		"BasicScheduler": {"input":{"required":{"model":["MODEL",{}]}},"input_order":{"required":["model"]}}
	}`)
	// bypassed LoadImage (id 46, no connected inputs) -> scheduler.model (kept).
	ui := `{"nodes":[
		{"id":46,"type":"LoadImage","mode":4,"widgets_values":["a.png"]},
		{"id":17,"type":"BasicScheduler","mode":0,"inputs":[{"name":"model","type":"MODEL","link":1}]}
	],"links":[[1,46,0,17,0,"MODEL"]]}`
	nodes, warns := convertNodes(t, ui, info)
	if len(warns) != 0 {
		t.Errorf("a bypassed zero-input source is a clean dead-end; expected NO warnings, got %v", warns)
	}
	if _, has := nodes["46"]; has {
		t.Error("bypassed source node 46 should be dropped")
	}
	if _, has := nodes["17"].Inputs["model"]; has {
		t.Error("scheduler.model should be unset (bypassed source drops the link)")
	}
	if _, has := nodes["17"]; !has {
		t.Error("kept consumer node 17 should still convert")
	}
}

// TestConvertBypassedAmbiguousNamesConsumer covers the genuinely-ambiguous bypass:
// a bypassed node with ≥1 connected input but no clean 1:1 passthrough for the
// requested output slot. This DOES warn, and the warning must name the CONSUMER
// (active node + input) that loses its source, not just the bypassed node id.
func TestConvertBypassedAmbiguousNamesConsumer(t *testing.T) {
	info := buildInfo(t, `{
		"CheckpointLoaderSimple": {"input":{"required":{"ckpt_name":[["a.safetensors"],{}]}},"input_order":{"required":["ckpt_name"]}},
		"BasicScheduler": {"input":{"required":{"model":["MODEL",{}]}},"input_order":{"required":["model"]}}
	}`)
	// Bypassed node 60 has TWO connected inputs (from 4 and 5); the consumer asks
	// for its output slot 2 — no 1:1 passthrough, not a sole input → ambiguous.
	ui := `{"nodes":[
		{"id":4,"type":"CheckpointLoaderSimple","mode":0,"widgets_values":["a.safetensors"]},
		{"id":5,"type":"CheckpointLoaderSimple","mode":0,"widgets_values":["b.safetensors"]},
		{"id":60,"type":"SomeMerge","mode":4,"inputs":[
			{"name":"in0","type":"MODEL","link":1},
			{"name":"in1","type":"MODEL","link":2}]},
		{"id":17,"type":"BasicScheduler","mode":0,"inputs":[{"name":"model","type":"MODEL","link":3}]}
	],"links":[
		[1,4,0,60,0,"MODEL"],
		[2,5,0,60,1,"MODEL"],
		[3,60,2,17,0,"MODEL"]
	]}`
	nodes, warns := convertNodes(t, ui, info)
	if _, has := nodes["17"].Inputs["model"]; has {
		t.Error("scheduler.model should be unset (ambiguous bypass)")
	}
	// The warning must name the consumer node + input AND the bypassed node.
	if !containsSubstr(warns, `node 17 (BasicScheduler) input "model" has no source`) {
		t.Errorf("expected a consumer-named ambiguous-bypass warning, got %v", warns)
	}
	if !containsSubstr(warns, "bypassed node 60") {
		t.Errorf("warning should name the ambiguous bypassed node 60, got %v", warns)
	}
	// The old, unactionable wording must be gone.
	if containsSubstr(warns, "could not be spliced (no clean input)") {
		t.Errorf("old unactionable wording should be replaced, got %v", warns)
	}
}

// TestConvertFastGroupsBypasserDropped asserts the rgthree "Fast Groups Bypasser"
// UI-only helper is dropped SILENTLY (like its sibling "Fast Groups Muter"), with
// no "type not available" warning — it has no backend class / no execution links.
func TestConvertFastGroupsBypasserDropped(t *testing.T) {
	info := buildInfo(t, `{
		"CheckpointLoaderSimple": {"input":{"required":{"ckpt_name":[["a.safetensors"],{}]}},"input_order":{"required":["ckpt_name"]}}
	}`)
	ui := `{"nodes":[
		{"id":4,"type":"CheckpointLoaderSimple","mode":0,"widgets_values":["a.safetensors"]},
		{"id":2,"type":"Fast Groups Bypasser (rgthree)","mode":0}
	],"links":[]}`
	nodes, warns := convertNodes(t, ui, info)
	if len(warns) != 0 {
		t.Errorf("Fast Groups Bypasser must be dropped silently, got warnings: %v", warns)
	}
	if _, has := nodes["2"]; has {
		t.Error("Fast Groups Bypasser node 2 should be dropped")
	}
	if _, has := nodes["4"]; !has {
		t.Error("checkpoint node 4 should still convert")
	}
}

// TestConvertAnnotationNodesDropped asserts the on-canvas text annotation nodes
// "Label (rgthree)" and "Note Plus (mtb)" are dropped SILENTLY (like the built-in
// Note): they have no backend class / no execution links, so they must not warn
// "type not available".
func TestConvertAnnotationNodesDropped(t *testing.T) {
	info := buildInfo(t, `{
		"CheckpointLoaderSimple": {"input":{"required":{"ckpt_name":[["a.safetensors"],{}]}},"input_order":{"required":["ckpt_name"]}}
	}`)
	ui := `{"nodes":[
		{"id":4,"type":"CheckpointLoaderSimple","mode":0,"widgets_values":["a.safetensors"]},
		{"id":8,"type":"Label (rgthree)","mode":0},
		{"id":9,"type":"Note Plus (mtb)","mode":0}
	],"links":[]}`
	nodes, warns := convertNodes(t, ui, info)
	if len(warns) != 0 {
		t.Errorf("annotation nodes must be dropped silently, got warnings: %v", warns)
	}
	for _, id := range []string{"8", "9"} {
		if _, has := nodes[id]; has {
			t.Errorf("annotation node %s should be dropped", id)
		}
	}
	if _, has := nodes["4"]; !has {
		t.Error("checkpoint node 4 should still convert")
	}
}

// subgraphTestInfo is the object_info used by the subgraph tests: a checkpoint
// loader, a pass-through node (one MODEL link input, no widgets), and a consumer.
func subgraphTestInfo(t *testing.T) ObjectInfo {
	return buildInfo(t, `{
		"CheckpointLoaderSimple": {"input":{"required":{"ckpt_name":[["a.safetensors"],{}]}},"input_order":{"required":["ckpt_name"]}},
		"PassModel": {"input":{"required":{"model":["MODEL",{}]}},"input_order":{"required":["model"]}},
		"BasicScheduler": {"input":{"required":{"model":["MODEL",{}]}},"input_order":{"required":["model"]}}
	}`)
}

// TestConvertSubgraphSingle expands a single subgraph instance. It exercises a
// boundary input (external -> interior), an interior node, and an internal->external
// output boundary link (interior -> boundary output -> external consumer).
func TestConvertSubgraphSingle(t *testing.T) {
	info := subgraphTestInfo(t)
	ui := `{
		"nodes":[
			{"id":4,"type":"CheckpointLoaderSimple","mode":0,"widgets_values":["a.safetensors"]},
			{"id":100,"type":"SG","mode":0,"inputs":[{"name":"model","type":"MODEL","link":1}]},
			{"id":17,"type":"BasicScheduler","mode":0,"inputs":[{"name":"model","type":"MODEL","link":2}]}
		],
		"links":[[1,4,0,100,0,"MODEL"],[2,100,0,17,0,"MODEL"]],
		"definitions":{"subgraphs":[
			{"id":"SG","name":"Passer",
			 "inputNode":{"id":-10},"outputNode":{"id":-20},
			 "inputs":[{"id":"i0","name":"model","type":"MODEL"}],
			 "outputs":[{"id":"o0","name":"MODEL","type":"MODEL"}],
			 "nodes":[{"id":1,"type":"PassModel","mode":0,"inputs":[{"name":"model","type":"MODEL","link":11}]}],
			 "links":[[11,-10,0,1,0,"MODEL"],[12,1,0,-20,0,"MODEL"]]}
		]}
	}`
	nodes, warns := convertNodes(t, ui, info)
	if len(warns) != 0 {
		t.Errorf("unexpected warnings: %v", warns)
	}
	if _, has := nodes["100"]; has {
		t.Error("subgraph instance node 100 should be removed")
	}
	if _, has := nodes["SG"]; has {
		t.Error("subgraph definition should not appear as a node")
	}
	pm, ok := nodes["100:1"]
	if !ok {
		t.Fatalf("inlined interior node 100:1 missing: %v", keysOf(nodes))
	}
	if pm.ClassType != "PassModel" {
		t.Errorf("interior node class_type = %q", pm.ClassType)
	}
	// interior node's model comes from the external checkpoint (boundary input).
	assertLinkRef(t, pm.Inputs["model"], "4", 0)
	// external consumer resolves through the boundary output to the interior node.
	assertLinkRef(t, nodes["17"].Inputs["model"], "100:1", 0)
}

// TestConvertSubgraphPassthrough covers a subgraph whose boundary input is wired
// DIRECTLY to its boundary output (no interior node between them): the external
// consumer must resolve straight to the external input source.
func TestConvertSubgraphPassthrough(t *testing.T) {
	info := subgraphTestInfo(t)
	ui := `{
		"nodes":[
			{"id":4,"type":"CheckpointLoaderSimple","mode":0,"widgets_values":["a.safetensors"]},
			{"id":100,"type":"SG","mode":0,"inputs":[{"name":"model","type":"MODEL","link":1}]},
			{"id":17,"type":"BasicScheduler","mode":0,"inputs":[{"name":"model","type":"MODEL","link":2}]}
		],
		"links":[[1,4,0,100,0,"MODEL"],[2,100,0,17,0,"MODEL"]],
		"definitions":{"subgraphs":[
			{"id":"SG","name":"Wire",
			 "inputNode":{"id":-10},"outputNode":{"id":-20},
			 "inputs":[{"id":"i0","name":"model","type":"MODEL"}],
			 "outputs":[{"id":"o0","name":"MODEL","type":"MODEL"}],
			 "nodes":[],
			 "links":[[11,-10,0,-20,0,"MODEL"]]}
		]}
	}`
	nodes, warns := convertNodes(t, ui, info)
	if len(warns) != 0 {
		t.Errorf("unexpected warnings: %v", warns)
	}
	// consumer.model resolves straight through the empty subgraph to the checkpoint.
	assertLinkRef(t, nodes["17"].Inputs["model"], "4", 0)
}

// TestConvertSubgraphNested expands a subgraph that itself instantiates another
// subgraph, asserting unique prefixed ids and end-to-end wiring across two
// boundary crossings.
func TestConvertSubgraphNested(t *testing.T) {
	info := subgraphTestInfo(t)
	ui := `{
		"nodes":[
			{"id":4,"type":"CheckpointLoaderSimple","mode":0,"widgets_values":["a.safetensors"]},
			{"id":200,"type":"OUTER","mode":0,"inputs":[{"name":"model","type":"MODEL","link":1}]},
			{"id":17,"type":"BasicScheduler","mode":0,"inputs":[{"name":"model","type":"MODEL","link":2}]}
		],
		"links":[[1,4,0,200,0,"MODEL"],[2,200,0,17,0,"MODEL"]],
		"definitions":{"subgraphs":[
			{"id":"OUTER","name":"Outer",
			 "inputNode":{"id":-10},"outputNode":{"id":-20},
			 "inputs":[{"id":"i0","name":"model","type":"MODEL"}],
			 "outputs":[{"id":"o0","name":"MODEL","type":"MODEL"}],
			 "nodes":[{"id":1,"type":"INNER","mode":0,"inputs":[{"name":"model","type":"MODEL","link":21}]}],
			 "links":[[21,-10,0,1,0,"MODEL"],[22,1,0,-20,0,"MODEL"]]},
			{"id":"INNER","name":"Inner",
			 "inputNode":{"id":-10},"outputNode":{"id":-20},
			 "inputs":[{"id":"i0","name":"model","type":"MODEL"}],
			 "outputs":[{"id":"o0","name":"MODEL","type":"MODEL"}],
			 "nodes":[{"id":1,"type":"PassModel","mode":0,"inputs":[{"name":"model","type":"MODEL","link":31}]}],
			 "links":[[31,-10,0,1,0,"MODEL"],[32,1,0,-20,0,"MODEL"]]}
		]}
	}`
	nodes, warns := convertNodes(t, ui, info)
	if len(warns) != 0 {
		t.Errorf("unexpected warnings: %v", warns)
	}
	pm, ok := nodes["200:1:1"]
	if !ok {
		t.Fatalf("doubly-nested interior node 200:1:1 missing: %v", keysOf(nodes))
	}
	if pm.ClassType != "PassModel" {
		t.Errorf("nested interior class_type = %q", pm.ClassType)
	}
	// The deep interior node's model comes from the top-level checkpoint.
	assertLinkRef(t, pm.Inputs["model"], "4", 0)
	// The top-level consumer resolves through both boundaries to the deep interior.
	assertLinkRef(t, nodes["17"].Inputs["model"], "200:1:1", 0)
}

// TestConvertSubgraphDepthBounded guards against a self-instantiating subgraph
// (cycle): expansion must stop at the depth bound and warn rather than loop.
func TestConvertSubgraphDepthBounded(t *testing.T) {
	info := subgraphTestInfo(t)
	// A subgraph that instantiates ITSELF — expansion must be depth-bounded.
	ui := `{
		"nodes":[
			{"id":4,"type":"CheckpointLoaderSimple","mode":0,"widgets_values":["a.safetensors"]},
			{"id":100,"type":"LOOP","mode":0,"inputs":[{"name":"model","type":"MODEL","link":1}]}
		],
		"links":[[1,4,0,100,0,"MODEL"]],
		"definitions":{"subgraphs":[
			{"id":"LOOP","name":"Loop",
			 "inputNode":{"id":-10},"outputNode":{"id":-20},
			 "inputs":[{"id":"i0","name":"model","type":"MODEL"}],
			 "outputs":[{"id":"o0","name":"MODEL","type":"MODEL"}],
			 "nodes":[{"id":1,"type":"LOOP","mode":0,"inputs":[{"name":"model","type":"MODEL","link":11}]}],
			 "links":[[11,-10,0,1,0,"MODEL"]]}
		]}
	}`
	// Must not hang or panic; the checkpoint still converts.
	api, warns, err := ConvertUIToAPI(json.RawMessage(ui), info)
	if err != nil {
		t.Fatalf("ConvertUIToAPI: %v", err)
	}
	if !containsSubstr(warns, "nested deeper than") {
		t.Errorf("expected a depth-bound warning, got %v", warns)
	}
	var nodes map[string]apiOutNode
	if err := json.Unmarshal(api, &nodes); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, has := nodes["4"]; !has {
		t.Error("checkpoint node 4 should still convert")
	}
}

// TestConvertSubgraphNodeCapExponentialClones guards the total-work cap against a
// self-referential subgraph that DOES emit clones: each instance holds an ordinary
// node PLUS two self-typed child instances, so without the cap it fans out to
// ~2^depth clones. Conversion must return the cap error QUICKLY (bounded work), not
// hang.
func TestConvertSubgraphNodeCapExponentialClones(t *testing.T) {
	info := subgraphTestInfo(t)
	// FANOUT: one ordinary interior node (a checkpoint, which IS in info) + two
	// self-typed child instances → exponential emitted-clone fan-out.
	ui := `{
		"nodes":[
			{"id":100,"type":"FANOUT","mode":0,"inputs":[]}
		],
		"links":[],
		"definitions":{"subgraphs":[
			{"id":"FANOUT","name":"Fanout",
			 "inputNode":{"id":-10},"outputNode":{"id":-20},
			 "inputs":[],"outputs":[],
			 "nodes":[
				{"id":1,"type":"CheckpointLoaderSimple","mode":0,"widgets_values":["a.safetensors"]},
				{"id":2,"type":"FANOUT","mode":0,"inputs":[]},
				{"id":3,"type":"FANOUT","mode":0,"inputs":[]}
			 ],
			 "links":[]}
		]}
	}`
	done := make(chan struct{})
	var api json.RawMessage
	var err error
	go func() {
		api, _, err = ConvertUIToAPI(json.RawMessage(ui), info)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("ConvertUIToAPI did not return within 10s — cap did not bound the fan-out")
	}
	if err == nil {
		t.Fatalf("expected a cap error, got nil (api len=%d)", len(api))
	}
	if !strings.Contains(err.Error(), "workflow too large") {
		t.Fatalf("expected 'workflow too large' cap error, got: %v", err)
	}
}

// TestConvertSubgraphNodeCapPureRecursion guards the WORK cap against a subgraph
// whose interior is ONLY nested self-instances (no ordinary node): it emits zero
// clones yet recurses exponentially, so an emitted-node-only cap would miss it.
// Conversion must still return the cap error quickly.
func TestConvertSubgraphNodeCapPureRecursion(t *testing.T) {
	info := subgraphTestInfo(t)
	ui := `{
		"nodes":[
			{"id":100,"type":"REC","mode":0,"inputs":[]}
		],
		"links":[],
		"definitions":{"subgraphs":[
			{"id":"REC","name":"Rec",
			 "inputNode":{"id":-10},"outputNode":{"id":-20},
			 "inputs":[],"outputs":[],
			 "nodes":[
				{"id":1,"type":"REC","mode":0,"inputs":[]},
				{"id":2,"type":"REC","mode":0,"inputs":[]}
			 ],
			 "links":[]}
		]}
	}`
	done := make(chan struct{})
	var err error
	go func() {
		_, _, err = ConvertUIToAPI(json.RawMessage(ui), info)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("ConvertUIToAPI did not return within 10s — work cap did not bound pure recursion")
	}
	if err == nil || !strings.Contains(err.Error(), "workflow too large") {
		t.Fatalf("expected 'workflow too large' cap error, got: %v", err)
	}
}

func keysOf(m map[string]apiOutNode) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestConvertObjectFormWidgetsPowerLora lowers the rgthree Power Lora Loader
// shape: its widgets_values is an object whose lora_N entries (dynamic widgets
// absent from object_info) must pass through as inputs, while the model link is
// honored and the object key that names a non-widget input is not clobbered.
func TestConvertObjectFormWidgetsPowerLora(t *testing.T) {
	info := buildInfo(t, `{
		"CheckpointLoaderSimple": {"input":{"required":{"ckpt_name":[["a.safetensors"],{}]}},"input_order":{"required":["ckpt_name"]}},
		"Power Lora Loader (rgthree)": {"input":{"required":{"model":["MODEL",{}]},"optional":{"clip":["CLIP",{}]}},"input_order":{"required":["model"],"optional":["clip"]}}
	}`)
	ui := `{"nodes":[
		{"id":4,"type":"CheckpointLoaderSimple","mode":0,"widgets_values":["a.safetensors"]},
		{"id":50,"type":"Power Lora Loader (rgthree)","mode":0,
		 "inputs":[{"name":"model","type":"MODEL","link":1},{"name":"clip","type":"CLIP","link":null}],
		 "widgets_values":{"PowerLoraLoaderHeaderWidget":{"type":"header"},"lora_1":{"on":true,"lora":"cool.safetensors","strength":0.8}}}
	],"links":[[1,4,0,50,0,"MODEL"]]}`
	nodes, warns := convertNodes(t, ui, info)
	if len(warns) != 0 {
		t.Errorf("object-form widgets should map without warnings, got %v", warns)
	}
	n := nodes["50"]
	assertLinkRef(t, n.Inputs["model"], "4", 0)
	if _, has := n.Inputs["clip"]; has {
		t.Error("clip (unconnected link input, no widget key) should be unset")
	}
	lj, has := n.Inputs["lora_1"]
	if !has {
		t.Fatal("lora_1 dynamic-widget entry should be mapped into inputs")
	}
	var lora struct {
		On   bool   `json:"on"`
		Lora string `json:"lora"`
	}
	if err := json.Unmarshal(lj, &lora); err != nil {
		t.Fatalf("lora_1 payload not preserved as object: %v", err)
	}
	if !lora.On || lora.Lora != "cool.safetensors" {
		t.Errorf("lora_1 payload wrong: %s", lj)
	}
}

// TestConvertObjectFormWidgetsSchemaMapped covers the general object-form case:
// keys matching widget inputs are mapped; a key naming a LINK input carrying a
// stray value is refused (not stuffed onto the link input).
func TestConvertObjectFormWidgetsSchemaMapped(t *testing.T) {
	info := buildInfo(t, `{
		"Widgety": {"input":{"required":{"text":["STRING",{}],"count":["INT",{}],"model":["MODEL",{}]}},"input_order":{"required":["text","count","model"]}}
	}`)
	ui := `{"nodes":[
		{"id":9,"type":"Widgety","mode":0,"widgets_values":{"text":"hello","count":5,"model":"junk"}}
	],"links":[]}`
	nodes, warns := convertNodes(t, ui, info)
	if len(warns) != 0 {
		t.Errorf("unexpected warnings: %v", warns)
	}
	n := nodes["9"]
	if got := scalarString(t, n.Inputs["text"]); got != "hello" {
		t.Errorf("text = %q", got)
	}
	if got := string(n.Inputs["count"]); got != "5" {
		t.Errorf("count = %s", got)
	}
	if _, has := n.Inputs["model"]; has {
		t.Error("model is a link input; an object widget value must not be mapped onto it")
	}
}

// TestConvertArrayFormWidgetsUnchanged is the regression guard: the array-form
// widget path (the 41-clean-workflow shape) still maps positionally as before.
func TestConvertArrayFormWidgetsUnchanged(t *testing.T) {
	info := buildInfo(t, `{
		"KSampler": {"input":{"required":{"seed":["INT",{"control_after_generate":true}],"steps":["INT",{}]}},"input_order":{"required":["seed","steps"]}}
	}`)
	ui := `{"nodes":[
		{"id":3,"type":"KSampler","mode":0,"widgets_values":[42,"randomize",20]}
	],"links":[]}`
	nodes, warns := convertNodes(t, ui, info)
	if len(warns) != 0 {
		t.Errorf("unexpected warnings: %v", warns)
	}
	n := nodes["3"]
	if got := string(n.Inputs["seed"]); got != "42" {
		t.Errorf("seed = %s", got)
	}
	if got := string(n.Inputs["steps"]); got != "20" {
		t.Errorf("steps = %s (array-form control off-by-one regressed)", got)
	}
}

// TestConvertRealFluxWorkflow converts the REAL 17-node civitai UI workflow against
// the REAL /object_info subset and asserts structural + key-preservation properties
// (not a brittle whole-graph equality).
func TestConvertRealFluxWorkflow(t *testing.T) {
	ui := readFixture(t, "real_ui_workflow_flux.json")
	infoRaw := readFixture(t, "object_info_subset_flux.json")
	var info ObjectInfo
	if err := json.Unmarshal(infoRaw, &info); err != nil {
		t.Fatalf("parse object_info fixture: %v", err)
	}

	api, warns, err := ConvertUIToAPI(ui, info)
	if err != nil {
		t.Fatalf("ConvertUIToAPI: %v", err)
	}
	if f, derr := DetectFormat(api); derr != nil || f != FormatAPI {
		t.Fatalf("output not api-format: f=%q err=%v", f, derr)
	}
	// The custom node absent from object_info must be reported.
	if !containsSubstr(warns, `type "SDXL Resolutions (JPS)" not available`) {
		t.Errorf("expected a warning for the missing custom node, got %v", warns)
	}

	var nodes map[string]apiOutNode
	if err := json.Unmarshal(api, &nodes); err != nil {
		t.Fatalf("decode api graph: %v", err)
	}
	// The unknown node must be omitted.
	if _, has := nodes["65"]; has {
		t.Error("unknown node 65 (SDXL Resolutions) should be omitted")
	}

	// A model-filename widget must be preserved verbatim. UNETLoader.unet_name is
	// "flux1-dev.sft" in the fixture.
	unet, ok := nodes["12"]
	if !ok {
		t.Fatal("UNETLoader node 12 missing")
	}
	if got := scalarString(t, unet.Inputs["unet_name"]); got != "flux1-dev.sft" {
		t.Errorf("unet_name = %q, want flux1-dev.sft", got)
	}

	// RandomNoise (node 25) carries a noise_seed with control_after_generate; its
	// seed value must be preserved and it must NOT gain a spurious extra input.
	rn, ok := nodes["25"]
	if !ok {
		t.Fatal("RandomNoise node 25 missing")
	}
	if got := string(rn.Inputs["noise_seed"]); got != "375202696791763" {
		t.Errorf("noise_seed = %s", got)
	}
	if len(rn.Inputs) != 1 {
		t.Errorf("RandomNoise should have exactly one input (noise_seed), got %v", rn.Inputs)
	}

	// Structural validity: every emitted input value is a JSON scalar OR a 2-element
	// [string,int] link ref.
	for nid, n := range nodes {
		for name, val := range n.Inputs {
			assertScalarOrLinkRef(t, nid, name, val)
		}
	}
}

// --- assertion helpers ---

func assertLinkRef(t *testing.T, raw json.RawMessage, wantID string, wantSlot int) {
	t.Helper()
	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err != nil || len(arr) != 2 {
		t.Fatalf("not a 2-element link ref: %s", raw)
	}
	var id string
	if err := json.Unmarshal(arr[0], &id); err != nil {
		t.Fatalf("link ref origin id not a string: %s", raw)
	}
	var slot int
	if err := json.Unmarshal(arr[1], &slot); err != nil {
		t.Fatalf("link ref slot not an int: %s", raw)
	}
	if id != wantID || slot != wantSlot {
		t.Errorf("link ref = [%q,%d], want [%q,%d]", id, slot, wantID, wantSlot)
	}
}

func assertScalarOrLinkRef(t *testing.T, nodeID, name string, raw json.RawMessage) {
	t.Helper()
	// A JSON array must be a valid 2-element [string,int] link ref.
	if isJSONArray(raw) {
		var arr []json.RawMessage
		if json.Unmarshal(raw, &arr) != nil || len(arr) != 2 {
			t.Errorf("node %s input %s: array is not a 2-element link ref: %s", nodeID, name, raw)
			return
		}
		var id string
		var slot int
		if json.Unmarshal(arr[0], &id) != nil || json.Unmarshal(arr[1], &slot) != nil {
			t.Errorf("node %s input %s: link ref not [string,int]: %s", nodeID, name, raw)
		}
		return
	}
	// Otherwise it must be a JSON scalar (string/number/bool/null) — not an object.
	if strings.HasPrefix(strings.TrimSpace(string(raw)), "{") {
		t.Errorf("node %s input %s: value is a JSON object, expected scalar or link ref: %s", nodeID, name, raw)
	}
}

func containsSubstr(warns []string, sub string) bool {
	for _, w := range warns {
		if strings.Contains(w, sub) {
			return true
		}
	}
	return false
}

func readFixture(t *testing.T, name string) json.RawMessage {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return json.RawMessage(b)
}

// TestConvertArrayTypedInput reproduces a real run failure: a node input slot's
// declared `type` is usually a string ("IMAGE","COMBO") but for a COMBO/enum slot
// ComfyUI serializes it as the ARRAY of allowed values (e.g. a VHS_VideoCombine
// pix_fmt slot: ["yuv420p","yuv420p10le"]). Before the fix uiConvInput.Type was
// typed `string`, so the whole-graph json.Unmarshal failed with "cannot unmarshal
// array into Go struct field ... type string" and the entire conversion aborted.
// The graph here mixes an array-typed slot with a normal string-typed slot and a
// string "COMBO"-typed slot and must convert cleanly.
func TestConvertArrayTypedInput(t *testing.T) {
	info := buildInfo(t, `{
		"CheckpointLoaderSimple": {"input":{"required":{"ckpt_name":[["a.safetensors"],{}]}},"input_order":{"required":["ckpt_name"]}},
		"VHS_VideoCombine": {"input":{"required":{"images":["IMAGE",{}]}},"input_order":{"required":["images"]}}
	}`)
	ui := `{"nodes":[
		{"id":4,"type":"CheckpointLoaderSimple","mode":0,"widgets_values":["a.safetensors"]},
		{"id":9,"type":"VHS_VideoCombine","mode":0,"inputs":[
			{"name":"images","type":"IMAGE","link":5},
			{"name":"pix_fmt","type":["yuv420p","yuv420p10le"],"link":null},
			{"name":"format","type":"COMBO","link":null}
		]}
	],"links":[[5,4,0,9,0,"IMAGE"]]}`
	nodes, warns := convertNodes(t, ui, info)
	if len(warns) != 0 {
		t.Errorf("unexpected warnings: %v", warns)
	}
	n, ok := nodes["9"]
	if !ok {
		t.Fatalf("node 9 (VHS_VideoCombine) missing: %v", keysOf(nodes))
	}
	if n.ClassType != "VHS_VideoCombine" {
		t.Errorf("class_type = %q", n.ClassType)
	}
	// The linked input still resolves normally past the array-typed slot.
	assertLinkRef(t, n.Inputs["images"], "4", 0)
}

// TestConvertSubgraphArrayTypedSlot hardens the sibling sgIOSlot.Type fix: an
// array-typed `type` can appear on a subgraph instance's input slot, on a boundary
// I/O slot (sgIOSlot), and on an interior node's input slot — all of which are
// decoded by the one graph-level json.Unmarshal. It must parse and wire correctly.
func TestConvertSubgraphArrayTypedSlot(t *testing.T) {
	info := subgraphTestInfo(t)
	ui := `{
		"nodes":[
			{"id":4,"type":"CheckpointLoaderSimple","mode":0,"widgets_values":["a.safetensors"]},
			{"id":100,"type":"SG","mode":0,"inputs":[{"name":"model","type":["MODEL","OTHER"],"link":1}]},
			{"id":17,"type":"BasicScheduler","mode":0,"inputs":[{"name":"model","type":"MODEL","link":2}]}
		],
		"links":[[1,4,0,100,0,"MODEL"],[2,100,0,17,0,"MODEL"]],
		"definitions":{"subgraphs":[
			{"id":"SG","name":"Passer",
			 "inputNode":{"id":-10},"outputNode":{"id":-20},
			 "inputs":[{"id":"i0","name":"model","type":["MODEL","LATENT"]}],
			 "outputs":[{"id":"o0","name":"MODEL","type":"MODEL"}],
			 "nodes":[{"id":1,"type":"PassModel","mode":0,"inputs":[{"name":"model","type":["MODEL","X"],"link":11}]}],
			 "links":[[11,-10,0,1,0,"MODEL"],[12,1,0,-20,0,"MODEL"]]}
		]}
	}`
	nodes, warns := convertNodes(t, ui, info)
	if len(warns) != 0 {
		t.Errorf("unexpected warnings: %v", warns)
	}
	pm, ok := nodes["100:1"]
	if !ok {
		t.Fatalf("inlined interior node 100:1 missing: %v", keysOf(nodes))
	}
	if pm.ClassType != "PassModel" {
		t.Errorf("interior node class_type = %q", pm.ClassType)
	}
	assertLinkRef(t, pm.Inputs["model"], "4", 0)
	assertLinkRef(t, nodes["17"].Inputs["model"], "100:1", 0)
}

// TestConvertSubgraphInteriorPromotedWidget reproduces the workflow-521 subgraph
// sibling of the v0.1.56 widget-promotion bug: a subgraph-INTERIOR sampler has a
// WIDGET input ("steps") that was converted to an input slot AND link-connected.
// In modern ComfyUI such a promoted widget KEEPS its slot in widgets_values,
// marked by the input's `widget` field. flattenSubgraphs clones interior nodes,
// and if the clone drops that `widget` marker, buildInputs no longer treats the
// slot as widget-backed, so it fails to CONSUME the promoted slot and every later
// widget value shifts — exactly how the interior KSamplerAdvanced ended up with a
// scheduler enum string ("simple") in its INT end_at_step (invalid_input_type).
// Must fail without carrying Widget into the clone, pass with it.
func TestConvertSubgraphInteriorPromotedWidget(t *testing.T) {
	info := buildInfo(t, `{
		"CheckpointLoaderSimple": {"input":{"required":{"ckpt_name":[["a.safetensors"],{}]}},"input_order":{"required":["ckpt_name"]}},
		"IntSrc": {"input":{"required":{}},"input_order":{"required":[]}},
		"AdvSampler": {
			"input":{"required":{
				"model":["MODEL",{}],
				"steps":["INT",{"default":20}],
				"sampler_name":[["euler","dpmpp_2m"],{}],
				"scheduler":[["normal","simple"],{}],
				"end_at_step":["INT",{"default":10000}]
			}},
			"input_order":{"required":["model","steps","sampler_name","scheduler","end_at_step"]}
		},
		"BasicScheduler": {"input":{"required":{"model":["MODEL",{}]}},"input_order":{"required":["model"]}}
	}`)
	// Interior node 5's widgets_values retains ALL widget slots in order:
	//   [steps=20, sampler_name="euler", scheduler="simple", end_at_step=10000]
	// "steps" is a promoted widget (carries a "widget" field) AND linked to the
	// external IntSrc via boundary input 1. If the promotion marker survives the
	// clone, its slot is consumed (emit nothing) and the plain widgets land right;
	// otherwise sampler_name/scheduler/end_at_step all shift by one and end_at_step
	// receives "simple".
	ui := `{
		"nodes":[
			{"id":4,"type":"CheckpointLoaderSimple","mode":0,"widgets_values":["a.safetensors"]},
			{"id":6,"type":"IntSrc","mode":0},
			{"id":100,"type":"SG","mode":0,"inputs":[
				{"name":"model","type":"MODEL","link":1},
				{"name":"steps","type":"INT","link":2}
			]},
			{"id":17,"type":"BasicScheduler","mode":0,"inputs":[{"name":"model","type":"MODEL","link":3}]}
		],
		"links":[[1,4,0,100,0,"MODEL"],[2,6,0,100,1,"INT"],[3,100,0,17,0,"MODEL"]],
		"definitions":{"subgraphs":[
			{"id":"SG","name":"Sampler",
			 "inputNode":{"id":-10},"outputNode":{"id":-20},
			 "inputs":[{"id":"i0","name":"model","type":"MODEL"},{"id":"i1","name":"steps","type":"INT"}],
			 "outputs":[{"id":"o0","name":"LATENT","type":"LATENT"}],
			 "nodes":[
				{"id":5,"type":"AdvSampler","mode":0,
				 "widgets_values":[20,"euler","simple",10000],
				 "inputs":[
					{"name":"model","type":"MODEL","link":11},
					{"name":"steps","type":"INT","link":12,"widget":{"name":"steps"}}
				 ]}
			 ],
			 "links":[[11,-10,0,5,0,"MODEL"],[12,-10,1,5,1,"INT"],[13,5,0,-20,0,"LATENT"]]}
		]}
	}`
	nodes, warns := convertNodes(t, ui, info)
	if len(warns) != 0 {
		t.Errorf("unexpected warnings: %v", warns)
	}
	s, ok := nodes["100:5"]
	if !ok {
		t.Fatalf("inlined interior sampler 100:5 missing: %v", keysOf(nodes))
	}
	// Plain widgets must receive their CORRECT (unshifted) values.
	if got := scalarString(t, s.Inputs["sampler_name"]); got != "euler" {
		t.Errorf("sampler_name = %q, want %q (interior promoted-widget slot not consumed → value shifted)", got, "euler")
	}
	if got := scalarString(t, s.Inputs["scheduler"]); got != "simple" {
		t.Errorf("scheduler = %q, want %q", got, "simple")
	}
	// The exact live symptom: end_at_step must be the INTEGER 10000, not the
	// shifted scheduler enum string "simple".
	var end int
	if err := json.Unmarshal(s.Inputs["end_at_step"], &end); err != nil {
		t.Fatalf("end_at_step = %s, want integer 10000 (widget shifted an enum string into an INT input): %v", s.Inputs["end_at_step"], err)
	}
	if end != 10000 {
		t.Errorf("end_at_step = %d, want 10000", end)
	}
	// The promoted+linked widget must be a link ref (to the external IntSrc), not a scalar.
	assertLinkRef(t, s.Inputs["steps"], "6", 0)
}

// TestConvertSubgraphInteriorNormalLinkedInput is the regression guard: a
// subgraph-interior node with a NORMAL (non-promoted, no `widget` marker) linked
// input plus plain widget values must still convert unchanged after the clone
// carries the (absent) Widget field. The link resolves and the widgets are not
// shifted.
func TestConvertSubgraphInteriorNormalLinkedInput(t *testing.T) {
	info := buildInfo(t, `{
		"CheckpointLoaderSimple": {"input":{"required":{"ckpt_name":[["a.safetensors"],{}]}},"input_order":{"required":["ckpt_name"]}},
		"PlainSampler": {
			"input":{"required":{
				"model":["MODEL",{}],
				"sampler_name":[["euler","dpmpp_2m"],{}],
				"scheduler":[["normal","simple"],{}]
			}},
			"input_order":{"required":["model","sampler_name","scheduler"]}
		},
		"BasicScheduler": {"input":{"required":{"model":["MODEL",{}]}},"input_order":{"required":["model"]}}
	}`)
	ui := `{
		"nodes":[
			{"id":4,"type":"CheckpointLoaderSimple","mode":0,"widgets_values":["a.safetensors"]},
			{"id":100,"type":"SG","mode":0,"inputs":[{"name":"model","type":"MODEL","link":1}]},
			{"id":17,"type":"BasicScheduler","mode":0,"inputs":[{"name":"model","type":"MODEL","link":3}]}
		],
		"links":[[1,4,0,100,0,"MODEL"],[3,100,0,17,0,"LATENT"]],
		"definitions":{"subgraphs":[
			{"id":"SG","name":"Sampler",
			 "inputNode":{"id":-10},"outputNode":{"id":-20},
			 "inputs":[{"id":"i0","name":"model","type":"MODEL"}],
			 "outputs":[{"id":"o0","name":"LATENT","type":"LATENT"}],
			 "nodes":[
				{"id":5,"type":"PlainSampler","mode":0,
				 "widgets_values":["euler","simple"],
				 "inputs":[{"name":"model","type":"MODEL","link":11}]}
			 ],
			 "links":[[11,-10,0,5,0,"MODEL"],[13,5,0,-20,0,"LATENT"]]}
		]}
	}`
	nodes, warns := convertNodes(t, ui, info)
	if len(warns) != 0 {
		t.Errorf("unexpected warnings: %v", warns)
	}
	s, ok := nodes["100:5"]
	if !ok {
		t.Fatalf("inlined interior sampler 100:5 missing: %v", keysOf(nodes))
	}
	// Normal linked input resolves to the external checkpoint; widgets map straight.
	assertLinkRef(t, s.Inputs["model"], "4", 0)
	if got := scalarString(t, s.Inputs["sampler_name"]); got != "euler" {
		t.Errorf("sampler_name = %q, want %q", got, "euler")
	}
	if got := scalarString(t, s.Inputs["scheduler"]); got != "simple" {
		t.Errorf("scheduler = %q, want %q", got, "simple")
	}
}

// TestConvertEmptyAllBypassed is the WAN-template case: every executable node is
// bypassed (mode 4) with a KNOWN type. The conversion must yield a
// *ConversionEmptyError classified as all-suppressed, with the actionable
// "enable"/"re-save" guidance.
func TestConvertEmptyAllBypassed(t *testing.T) {
	info := buildInfo(t, `{
		"CheckpointLoaderSimple": {"input":{"required":{"ckpt_name":[["a.safetensors"],{}]}},"input_order":{"required":["ckpt_name"]}},
		"KSampler": {"input":{"required":{"seed":["INT",{}]}},"input_order":{"required":["seed"]}}
	}`)
	// Two known executable nodes, both bypassed; a Note (virtual) is not counted.
	ui := `{"nodes":[
		{"id":4,"type":"CheckpointLoaderSimple","mode":4,"widgets_values":["a.safetensors"]},
		{"id":3,"type":"KSampler","mode":4,"widgets_values":[123]},
		{"id":9,"type":"Note","mode":0,"widgets_values":["hi"]}
	],"links":[]}`

	api, _, err := ConvertUIToAPI(json.RawMessage(ui), info)
	if api != nil {
		t.Errorf("expected nil api graph, got: %s", api)
	}
	var ece *ConversionEmptyError
	if !errors.As(err, &ece) {
		t.Fatalf("error is not *ConversionEmptyError: %v", err)
	}
	if ece.Suppressed != 2 {
		t.Errorf("Suppressed = %d, want 2", ece.Suppressed)
	}
	if ece.Unknown != 0 {
		t.Errorf("Unknown = %d, want 0", ece.Unknown)
	}
	msg := ece.Error()
	for _, want := range []string{"disabled", "enable", "re-save"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message missing %q guidance: %q", want, msg)
		}
	}
}

// TestConvertEmptyAllUnknown is the no-installed-node-types case: every node's
// type is absent from /object_info. The conversion must yield a
// *ConversionEmptyError classified as all-unknown, with the "not installed"
// custom-node guidance.
func TestConvertEmptyAllUnknown(t *testing.T) {
	info := buildInfo(t, `{}`) // nothing installed
	ui := `{"nodes":[
		{"id":1,"type":"SomeCustomNode","mode":0,"widgets_values":[]},
		{"id":2,"type":"AnotherCustomNode","mode":0,"widgets_values":[]}
	],"links":[]}`

	api, _, err := ConvertUIToAPI(json.RawMessage(ui), info)
	if api != nil {
		t.Errorf("expected nil api graph, got: %s", api)
	}
	var ece *ConversionEmptyError
	if !errors.As(err, &ece) {
		t.Fatalf("error is not *ConversionEmptyError: %v", err)
	}
	if ece.Suppressed != 0 {
		t.Errorf("Suppressed = %d, want 0", ece.Suppressed)
	}
	if ece.Unknown != 2 {
		t.Errorf("Unknown = %d, want 2", ece.Unknown)
	}
	msg := ece.Error()
	for _, want := range []string{"not installed", "custom nodes"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message missing %q guidance: %q", want, msg)
		}
	}
}

// TestConvertNonEmptyHappyPathUnchanged is the regression guard: a graph with at
// least one active, known node still converts successfully (no error), so the
// empty-classification change did not touch the happy path.
func TestConvertNonEmptyHappyPathUnchanged(t *testing.T) {
	info := buildInfo(t, `{
		"CheckpointLoaderSimple": {"input":{"required":{"ckpt_name":[["a.safetensors"],{}]}},"input_order":{"required":["ckpt_name"]}}
	}`)
	// One active known node + one bypassed known node: converts to exactly one node.
	ui := `{"nodes":[
		{"id":4,"type":"CheckpointLoaderSimple","mode":0,"widgets_values":["a.safetensors"]},
		{"id":5,"type":"CheckpointLoaderSimple","mode":4,"widgets_values":["b.safetensors"]}
	],"links":[]}`
	nodes, warns := convertNodes(t, ui, info)
	if len(warns) != 0 {
		t.Errorf("unexpected warnings: %v", warns)
	}
	if len(nodes) != 1 {
		t.Fatalf("node count = %d, want 1: %v", len(nodes), keysOf(nodes))
	}
	if _, ok := nodes["4"]; !ok {
		t.Errorf("active node 4 missing: %v", keysOf(nodes))
	}
}
