package comfy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	if isJSONObjectNonEmpty(raw) || strings.HasPrefix(strings.TrimSpace(string(raw)), "{") {
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
