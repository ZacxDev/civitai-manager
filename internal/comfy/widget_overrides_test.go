package comfy

import (
	"encoding/json"
	"testing"
)

// apiGraphForOverride is a converted api-format graph with a KSampler whose `model`
// input is a LINK (array) and whose seed/steps/cfg are scalar widgets, plus a
// CLIPTextEncode with a scalar `text`.
const apiGraphForOverride = `{
  "3": {"class_type": "KSampler", "inputs": {
    "model": ["4",0], "seed": 111, "steps": 20, "cfg": 8.0, "sampler_name": "euler"}},
  "6": {"class_type": "CLIPTextEncode", "inputs": {"text": "old prompt", "clip": ["4",1]}}
}`

func TestApplyWidgetOverridesRewritesTargetedInputsOnly(t *testing.T) {
	orig := json.RawMessage(apiGraphForOverride)
	// Keep a byte copy to prove the input graph is never mutated.
	before := append([]byte(nil), orig...)

	overrides := map[WidgetOverrideKey]string{
		{NodeID: "3", InputName: "seed"}:  "999",
		{NodeID: "3", InputName: "steps"}: "30",
		{NodeID: "3", InputName: "cfg"}:   "6.5",
		{NodeID: "6", InputName: "text"}:  "a new prompt",
	}
	out := ApplyWidgetOverrides(orig, overrides)

	// The stored/input graph is byte-identical (never mutated).
	if string(orig) != string(before) {
		t.Fatalf("input graph was mutated:\n got %s\nwant %s", orig, before)
	}

	var nodes map[string]map[string]json.RawMessage
	if err := json.Unmarshal(out, &nodes); err != nil {
		t.Fatalf("parse out: %v", err)
	}
	ks := map[string]json.RawMessage{}
	_ = json.Unmarshal(nodes["3"]["inputs"], &ks)

	// Numbers stay JSON numbers (not strings): seed 999 (int), steps 30 (int), cfg 6.5.
	if string(ks["seed"]) != "999" {
		t.Errorf("seed = %s, want 999 (bare number)", ks["seed"])
	}
	if string(ks["steps"]) != "30" {
		t.Errorf("steps = %s, want 30", ks["steps"])
	}
	if string(ks["cfg"]) != "6.5" {
		t.Errorf("cfg = %s, want 6.5", ks["cfg"])
	}
	// The link input is untouched.
	if string(ks["model"]) != `["4",0]` {
		t.Errorf("model link should be untouched, got %s", ks["model"])
	}
	// sampler_name was not overridden — unchanged.
	if string(ks["sampler_name"]) != `"euler"` {
		t.Errorf("sampler_name should be unchanged, got %s", ks["sampler_name"])
	}
	// The string prompt is rewritten and stays a JSON string.
	var enc map[string]json.RawMessage
	_ = json.Unmarshal(nodes["6"]["inputs"], &enc)
	if string(enc["text"]) != `"a new prompt"` {
		t.Errorf("text = %s, want quoted 'a new prompt'", enc["text"])
	}
}

func TestApplyWidgetOverridesIgnoresUnknownAndLinks(t *testing.T) {
	orig := json.RawMessage(apiGraphForOverride)
	overrides := map[WidgetOverrideKey]string{
		{NodeID: "999", InputName: "seed"}:   "1",            // unknown node
		{NodeID: "3", InputName: "nonesuch"}: "1",            // unknown input — never ADD
		{NodeID: "3", InputName: "model"}:    "hijack",       // link input — never touch
		{NodeID: "6", InputName: "clip"}:     "hijack",       // link input — never touch
		{NodeID: "3", InputName: "steps"}:    "not-a-number", // number field, bad value → skip
	}
	out := ApplyWidgetOverrides(orig, overrides)

	var nodes map[string]map[string]json.RawMessage
	_ = json.Unmarshal(out, &nodes)
	ks := map[string]json.RawMessage{}
	_ = json.Unmarshal(nodes["3"]["inputs"], &ks)

	if _, added := ks["nonesuch"]; added {
		t.Error("must never ADD an input key")
	}
	if string(ks["model"]) != `["4",0]` {
		t.Errorf("link input model was altered: %s", ks["model"])
	}
	if string(ks["steps"]) != "20" {
		t.Errorf("bad-number override should leave steps unchanged, got %s", ks["steps"])
	}
	var enc map[string]json.RawMessage
	_ = json.Unmarshal(nodes["6"]["inputs"], &enc)
	if string(enc["clip"]) != `["4",1]` {
		t.Errorf("link input clip was altered: %s", enc["clip"])
	}
}

func TestApplyWidgetOverridesEmptyIsNoop(t *testing.T) {
	orig := json.RawMessage(apiGraphForOverride)
	if got := ApplyWidgetOverrides(orig, nil); string(got) != string(orig) {
		t.Errorf("empty overrides should return the graph unchanged")
	}
}

// TestApplyWidgetOverridesPreservesLargeIntSeed proves a large integer seed is
// emitted verbatim (full precision, no float64 rounding).
func TestApplyWidgetOverridesPreservesLargeIntSeed(t *testing.T) {
	orig := json.RawMessage(`{"3":{"class_type":"KSampler","inputs":{"seed":1}}}`)
	out := ApplyWidgetOverrides(orig, map[WidgetOverrideKey]string{
		{NodeID: "3", InputName: "seed"}: "18446744073709551615",
	})
	var nodes map[string]map[string]json.RawMessage
	_ = json.Unmarshal(out, &nodes)
	ks := map[string]json.RawMessage{}
	_ = json.Unmarshal(nodes["3"]["inputs"], &ks)
	if string(ks["seed"]) != "18446744073709551615" {
		t.Errorf("large seed lost precision: %s", ks["seed"])
	}
}
