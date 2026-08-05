package web

import (
	"encoding/json"
	"strings"
	"testing"
)

// Coverage for the two probe diagnostics. These run in the ordinary suite — the
// probe itself is env-gated and never runs in CI, so without these the helpers
// would ship untested, which is the failure mode this repo keeps paying for.
//
// Every fixture value is pairwise DISTINCT: a fixture of repeated or default values
// collapses distinct implementations into identical output, and a mutant then
// survives for a reason that has nothing to do with the code being right.

// censusFixture is one UI graph with, deliberately, one node of every category the
// census must tell apart:
//
//	10 active + converted        (KSampler)
//	11 active + NOT converted    (a cut class)
//	12 bypassed  (mode 4)        (must NOT count as unconverted)
//	13 muted     (mode 2)        (must NOT count as unconverted)
//	14 active + NOT converted    (UI-only, a DIFFERENT class from 11)
const censusFixture = `{"nodes":[
  {"id":10,"type":"KSampler","mode":0,"widgets_values":["live.safetensors"]},
  {"id":11,"type":"UltimateSDUpscale","mode":0,"widgets_values":[]},
  {"id":12,"type":"LoraLoaderModelOnly","mode":4,"widgets_values":["bypassed.safetensors",0.5]},
  {"id":13,"type":"CheckpointLoaderSimple","mode":2,"widgets_values":["muted.ckpt"]},
  {"id":14,"type":"Label (rgthree)","mode":0,"widgets_values":[]}
]}`

// Only node 10 survived conversion.
const censusAPIFixture = `{"10":{"class_type":"KSampler","inputs":{"ckpt_name":"live.safetensors"}}}`

func TestProbeNodeCensusAccountsForEveryNode(t *testing.T) {
	c := probeNodeCensus(json.RawMessage(censusFixture), json.RawMessage(censusAPIFixture))

	if c.UINodes != 5 {
		t.Errorf("UINodes = %d; want 5", c.UINodes)
	}
	// The whole point of the split: inactive nodes are excluded ON PURPOSE and must
	// never be reported as something that went missing.
	if c.Inactive != 2 {
		t.Errorf("Inactive = %d; want 2 (one muted, one bypassed)", c.Inactive)
	}
	if c.Active != 3 {
		t.Errorf("Active = %d; want 3", c.Active)
	}
	if c.Converted != 1 {
		t.Errorf("Converted = %d; want 1", c.Converted)
	}
	// Precondition, not decoration: if the arithmetic below did not hold, every
	// assertion above could pass while the census was still lying about the whole.
	if c.Inactive+c.Active != c.UINodes {
		t.Fatalf("census does not account for every node: %d inactive + %d active != %d total",
			c.Inactive, c.Active, c.UINodes)
	}

	if got := len(c.Unconverted); got != 2 {
		t.Fatalf("Unconverted has %d classes (%v); want 2", got, c.Unconverted)
	}
	for class, wantIDs := range map[string][]string{
		"UltimateSDUpscale": {"11"},
		"Label (rgthree)":   {"14"},
	} {
		got := c.Unconverted[class]
		if strings.Join(got, ",") != strings.Join(wantIDs, ",") {
			t.Errorf("Unconverted[%q] = %v; want %v", class, got, wantIDs)
		}
	}
	// The load-bearing negative: a bypassed node is not "missing".
	for _, class := range []string{"LoraLoaderModelOnly", "CheckpointLoaderSimple"} {
		if ids, ok := c.Unconverted[class]; ok {
			t.Errorf("inactive class %q reported as unconverted (nodes %v) — bypassed is deliberate, not missing", class, ids)
		}
	}
}

// A clean graph must produce an EMPTY Unconverted set, or the census over-reports
// and every non-empty result becomes noise a reader learns to ignore.
func TestProbeNodeCensusReportsNothingWhenEverythingConverted(t *testing.T) {
	const ui = `{"nodes":[{"id":1,"type":"KSampler","mode":0},{"id":2,"type":"VAEDecode","mode":0}]}`
	const api = `{"1":{"class_type":"KSampler","inputs":{}},"2":{"class_type":"VAEDecode","inputs":{}}}`
	c := probeNodeCensus(json.RawMessage(ui), json.RawMessage(api))
	if c.Active != 2 || c.Converted != 2 {
		t.Fatalf("fixture did not reach the interesting state: active=%d converted=%d", c.Active, c.Converted)
	}
	if len(c.Unconverted) != 0 {
		t.Errorf("Unconverted = %v; want empty for a fully converted graph", c.Unconverted)
	}
}

// probeDormantResources must name exactly the models on bypassed/muted nodes — not
// the running one, and not nothing.
func TestProbeDormantResourcesNamesOnlyTheInactiveOnes(t *testing.T) {
	got := probeDormantResources("ui", json.RawMessage(censusFixture))

	want := []string{"bypassed.safetensors", "muted.ckpt"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("dormant = %v; want %v", got, want)
	}
	// 🔴 The discriminating assertion. A version of this that returned EVERY
	// referenced model would satisfy "the two dormant ones are present"; only
	// excluding the live one proves the set difference actually happened.
	for _, r := range got {
		if r == "live.safetensors" {
			t.Error("dormant includes live.safetensors — that model IS loaded by a running node")
		}
	}
}

// The control: when nothing is bypassed there is nothing dormant. Without this, a
// helper that returned everything would still pass the test above's first half.
func TestProbeDormantResourcesIsEmptyWhenNothingIsBypassed(t *testing.T) {
	const allLive = `{"nodes":[
      {"id":1,"type":"CheckpointLoaderSimple","mode":0,"widgets_values":["a.safetensors"]},
      {"id":2,"type":"LoraLoader","mode":0,"widgets_values":["b.safetensors",1.0]}
    ]}`
	// Precondition: the fixture must actually reference models, or "empty dormant"
	// would be true for the uninteresting reason that nothing was extracted at all.
	if all := probeDormantResources("ui", json.RawMessage(strings.Replace(allLive, `"mode":0`, `"mode":4`, -1))); len(all) != 2 {
		t.Fatalf("fixture check: bypassing every node should make both models dormant, got %v", all)
	}
	if got := probeDormantResources("ui", json.RawMessage(allLive)); len(got) != 0 {
		t.Errorf("dormant = %v; want empty when every node is active", got)
	}
}

// An api-format graph carries no modes at all — conversion has already dropped
// bypassed nodes — so "dormant" is empty there by construction, not by accident.
func TestProbeDormantResourcesIsEmptyForAnAPIGraph(t *testing.T) {
	if got := probeDormantResources("api", json.RawMessage(censusAPIFixture)); len(got) != 0 {
		t.Errorf("dormant = %v; want empty for an api graph", got)
	}
}

// Untrusted input: a graph that does not parse must yield an empty census and no
// dormant list rather than panicking. The probe reads real, author-supplied files.
func TestProbeDiagnosticsSurviveMalformedGraphs(t *testing.T) {
	for _, bad := range []string{``, `{`, `null`, `{"nodes":"not-an-array"}`, `[]`} {
		c := probeNodeCensus(json.RawMessage(bad), json.RawMessage(bad))
		if c.UINodes != 0 || len(c.Unconverted) != 0 {
			t.Errorf("probeNodeCensus(%q) = %+v; want a zero census", bad, c)
		}
		if got := probeDormantResources("ui", json.RawMessage(bad)); len(got) != 0 {
			t.Errorf("probeDormantResources(%q) = %v; want none", bad, got)
		}
	}
}
