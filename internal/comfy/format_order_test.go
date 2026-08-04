package comfy

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// Guards the DETERMINISTIC ordering contract of ExtractResources.
//
// 🔴 WHY THIS EXISTS. The api path decodes a JSON object into map[string]apiNode and
// used to range it directly — twice, once over nodes and once over each node's
// inputs. Go randomises map iteration per process, so the same graph produced a
// different list on every call. Measured before the fix on the 5-loader fixture
// below: FIVE distinct orderings across 200 calls. That list is persisted to
// `workflows.resources` and, via ExtractActiveResources, to `ResourcesUsed` — which
// is a provenance claim about a specific run.
//
// 🔴 A "1 distinct ordering" result is a ZERO-SHAPED answer, and this repo has been
// burned repeatedly by counting harnesses that were wired to nothing. So
// TestExtractResourcesOrderProbeCanObserveDisorder is the POSITIVE CONTROL: it feeds
// the same detector a deliberately shuffled producer and requires it to report >1.
// Read the two together — a control at 1 would mean the probe cannot see disorder,
// and the real result would be worthless.

// orderFixture is a 5-loader graph whose node ids are deliberately NOT in lexical
// order relative to their numeric order (2, 10, 1, 20, 3), so a lexical sort and a
// numeric sort disagree and the test can tell them apart.
const orderFixture = `{
  "2":  {"class_type":"CheckpointLoaderSimple","inputs":{"ckpt_name":"bbb.safetensors"}},
  "10": {"class_type":"LoraLoader","inputs":{"lora_name":"eee.safetensors"}},
  "1":  {"class_type":"VAELoader","inputs":{"vae_name":"aaa.safetensors"}},
  "20": {"class_type":"CLIPVisionLoader","inputs":{"clip_name":"fff.safetensors"}},
  "3":  {"class_type":"ControlNetLoader","inputs":{"control_net_name":"ccc.safetensors"}}
}`

// distinctOrderings calls fn n times and returns how many distinct results it saw.
func distinctOrderings(t *testing.T, n int, fn func() []string) int {
	t.Helper()
	seen := map[string]bool{}
	for i := 0; i < n; i++ {
		b, err := json.Marshal(fn())
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		seen[string(b)] = true
	}
	return len(seen)
}

func TestExtractResourcesIsDeterministic(t *testing.T) {
	graph := json.RawMessage(orderFixture)

	// Precondition: the fixture must actually yield several resources, or "one
	// ordering" is trivially true and the guard proves nothing.
	first, err := ExtractResources(graph)
	if err != nil {
		t.Fatalf("ExtractResources: %v", err)
	}
	if len(first) < 5 {
		t.Fatalf("precondition: fixture must yield >=5 resources to have an order at all, got %d (%v)",
			len(first), first)
	}

	if got := distinctOrderings(t, 200, func() []string {
		out, _ := ExtractResources(graph)
		return out
	}); got != 1 {
		t.Errorf("ExtractResources is NON-DETERMINISTIC: %d distinct orderings across 200 calls; "+
			"this list is persisted to workflows.resources and ResourcesUsed", got)
	}

	// And the order is the documented one: ascending NUMERIC node id.
	//
	// The fixture's ids are 2, 10, 1, 20, 3 precisely so numeric and lexical
	// DISAGREE — lexical would give 1, 10, 2, 20, 3 → aaa, eee, bbb, fff, ccc.
	// Without that disagreement the assertion would pass under either sort and
	// prove nothing about which one is in force.
	want := []string{
		"aaa.safetensors", // node 1
		"bbb.safetensors", // node 2
		"ccc.safetensors", // node 3
		"eee.safetensors", // node 10
		"fff.safetensors", // node 20
	}
	if strings.Join(first, ",") != strings.Join(want, ",") {
		t.Errorf("wrong order\n got: %v\nwant: %v (ascending numeric node id)", first, want)
	}
}

// TestExtractResourcesOrdersInputsWithinANode covers the SECOND randomisation source.
// Fixing only the node loop would leave a node carrying two model-valued inputs
// nondeterministic, and a single-input-per-node fixture cannot observe that.
func TestExtractResourcesOrdersInputsWithinANode(t *testing.T) {
	graph := json.RawMessage(`{
	  "1": {"class_type":"CheckpointLoaderSimple","inputs":{
	          "zzz_name":"zzz.safetensors",
	          "aaa_name":"aaa.safetensors",
	          "mmm_name":"mmm.safetensors"}}
	}`)

	got, _ := ExtractResources(graph)
	if len(got) != 3 {
		t.Fatalf("precondition: want 3 resources from ONE node, got %d (%v) — "+
			"this fixture cannot observe input ordering otherwise", len(got), got)
	}
	want := "aaa.safetensors,mmm.safetensors,zzz.safetensors"
	if strings.Join(got, ",") != want {
		t.Errorf("inputs not ordered within a node\n got: %v\nwant: %s", got, want)
	}
	if n := distinctOrderings(t, 200, func() []string {
		out, _ := ExtractResources(graph)
		return out
	}); n != 1 {
		t.Errorf("input iteration is NON-DETERMINISTIC: %d distinct orderings", n)
	}
}

// TestExtractResourcesOrderProbeCanObserveDisorder is the POSITIVE CONTROL for the
// two tests above. Their reassuring answer is a 1, and a 1 is indistinguishable from
// a probe that cannot see disorder at all. This feeds distinctOrderings a producer
// that is deliberately unstable and requires it to report more than one.
func TestExtractResourcesOrderProbeCanObserveDisorder(t *testing.T) {
	// A map range over >1 key is the same mechanism the bug used.
	shuffling := map[string]bool{"a": true, "b": true, "c": true, "d": true, "e": true}
	got := distinctOrderings(t, 200, func() []string {
		var out []string
		for k := range shuffling {
			out = append(out, k)
		}
		return out
	})
	if got <= 1 {
		t.Fatalf("POSITIVE CONTROL FAILED: the probe reported %d distinct orderings for a "+
			"deliberately unstable producer, so it cannot observe disorder and the "+
			"determinism results above prove nothing", got)
	}
	t.Logf("positive control: probe observed %d distinct orderings for an unstable producer", got)
}

// TestExtractResourcesHandlesNonNumericNodeIDs pins the fallback: ids that are not
// integers sort lexically rather than panicking or silently grouping.
func TestExtractResourcesHandlesNonNumericNodeIDs(t *testing.T) {
	graph := json.RawMessage(fmt.Sprintf(`{
	  %q: {"class_type":"VAELoader","inputs":{"vae_name":"bbb.safetensors"}},
	  %q: {"class_type":"VAELoader","inputs":{"vae_name":"aaa.safetensors"}}
	}`, "node-b", "node-a"))

	got, _ := ExtractResources(graph)
	want := "aaa.safetensors,bbb.safetensors" // node-a before node-b
	if strings.Join(got, ",") != want {
		t.Errorf("non-numeric ids should sort lexically\n got: %v\nwant: %s", got, want)
	}
}
