package comfy

import (
	"encoding/json"
	"fmt"
	"strconv"
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

// 🔴 TestExtractResourcesIsDeterministicWithMixedNodeIDs is the case the first
// version of this file COULD NOT REACH, and it is where the real bug lived.
//
// The original fixture used all-numeric ids, and the "non-numeric" test used
// all-non-numeric ids. Both are the SAFE cases: each is internally a total order.
// The defect only appears when numeric and non-numeric ids are MIXED, because the
// naive "numeric if both parse, else lexical" comparator is then intransitive —
// {"9","10","5abc"}: 9 < 10, "10" < "5abc", "5abc" < "9" — and `sort.Slice` on an
// intransitive comparator returns an arbitrary permutation of its (randomised)
// input. A guard can be correct, and green, and still never construct the input
// that breaks the thing it guards.
//
// The mixed set is production-reachable: convert_subgraph.go mints interior node
// ids as "<instance>:<interior>", so any UI workflow with a subgraph containing a
// loader converts to a graph keyed like {"4","17","100:1"}.
func TestExtractResourcesIsDeterministicWithMixedNodeIDs(t *testing.T) {
	graph := json.RawMessage(`{
	  "1":    {"class_type":"CheckpointLoaderSimple","inputs":{"ckpt_name":"aaa.safetensors"}},
	  "4":    {"class_type":"VAELoader","inputs":{"vae_name":"bbb.safetensors"}},
	  "9":    {"class_type":"LoraLoader","inputs":{"lora_name":"ccc.safetensors"}},
	  "12":   {"class_type":"ControlNetLoader","inputs":{"control_net_name":"ddd.safetensors"}},
	  "12:3": {"class_type":"CLIPVisionLoader","inputs":{"clip_name":"eee.safetensors"}},
	  "12:8": {"class_type":"UNETLoader","inputs":{"unet_name":"fff.safetensors"}}
	}`)

	got, _ := ExtractResources(graph)
	if len(got) != 6 {
		t.Fatalf("precondition: want 6 resources from the mixed-id fixture, got %d (%v)",
			len(got), got)
	}
	// 🔴 Precondition: the fixture must actually MIX parseable and unparseable ids,
	// and this MUST be derived from the fixture's own keys.
	//
	// An earlier version asserted `strconv.Atoi("12:3")` errors — a property of a
	// STRING LITERAL, not of the fixture, and so unconditionally true. Measured:
	// with that version, swapping the "12:3"/"12:8" keys for "13"/"14" — degrading
	// this to exactly the all-numeric SAFE case the doc above says cannot observe
	// the defect — left the whole test GREEN, precondition and `want` assertion
	// included. The guard silently stopped guarding.
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(graph, &keys); err != nil {
		t.Fatalf("precondition: fixture must parse as an object: %v", err)
	}
	var numeric, nonNumeric int
	for id := range keys {
		if _, err := strconv.Atoi(id); err == nil {
			numeric++
		} else {
			nonNumeric++
		}
	}
	if numeric == 0 || nonNumeric == 0 {
		t.Fatalf("precondition: fixture must MIX id kinds to observe the defect, got "+
			"%d numeric / %d non-numeric — an all-one-kind set is internally a total "+
			"order and cannot expose an intransitive comparator", numeric, nonNumeric)
	}

	if n := distinctOrderings(t, 500, func() []string {
		out, _ := ExtractResources(graph)
		return out
	}); n != 1 {
		t.Errorf("MIXED node ids are NON-DETERMINISTIC: %d distinct orderings across 500 calls — "+
			"the comparator is not a strict weak ordering, so sort.Slice returns an "+
			"arbitrary permutation of the randomised map range", n)
	}

	// lessNodeKey puts every numeric id before every non-numeric one, then lexical.
	want := "aaa.safetensors,bbb.safetensors,ccc.safetensors,ddd.safetensors,eee.safetensors,fff.safetensors"
	if strings.Join(got, ",") != want {
		t.Errorf("wrong mixed-id order\n got: %v\nwant: %s", got, want)
	}
}

// TestExtractResourcesIsDeterministicWithLeadingZeroIDs covers the other tie the
// naive comparator lost: "7", "07" and "007" all Atoi to 7, so a bare `na < nb`
// returns false both ways and `sort.Slice` — which is NOT stable — resolves the tie
// from the randomised input. lessNodeKey tie-breaks equal values by string.
func TestExtractResourcesIsDeterministicWithLeadingZeroIDs(t *testing.T) {
	graph := json.RawMessage(`{
	  "7":   {"class_type":"VAELoader","inputs":{"vae_name":"ccc.safetensors"}},
	  "07":  {"class_type":"VAELoader","inputs":{"vae_name":"bbb.safetensors"}},
	  "007": {"class_type":"VAELoader","inputs":{"vae_name":"aaa.safetensors"}}
	}`)

	got, _ := ExtractResources(graph)
	if len(got) != 3 {
		t.Fatalf("precondition: want 3 resources, got %d (%v)", len(got), got)
	}
	if n := distinctOrderings(t, 500, func() []string {
		out, _ := ExtractResources(graph)
		return out
	}); n != 1 {
		t.Errorf("equal-value node ids are NON-DETERMINISTIC: %d distinct orderings across "+
			"500 calls — sort.Slice is unstable, so an untie-broken equal pair is "+
			"resolved by the randomised input", n)
	}
	// "007" < "07" < "7" by string, which is how lessNodeKey breaks the tie.
	want := "aaa.safetensors,bbb.safetensors,ccc.safetensors"
	if strings.Join(got, ",") != want {
		t.Errorf("wrong equal-value order\n got: %v\nwant: %s", got, want)
	}
}

// TestPrimaryCheckpointAPIIsDeterministic covers the SAME defect in the sibling
// function. It matters more than the resource list: autoLink prepends this value to
// its candidate list and takes the first hit, so a random answer randomises the
// persisted model_id/version_id a re-scanned workflow acquires.
func TestPrimaryCheckpointAPIIsDeterministic(t *testing.T) {
	graph := json.RawMessage(`{
	  "9":   {"class_type":"CheckpointLoaderSimple","inputs":{"ckpt_name":"ccc.safetensors"}},
	  "2":   {"class_type":"CheckpointLoaderSimple","inputs":{"ckpt_name":"aaa.safetensors"}},
	  "5":   {"class_type":"CheckpointLoader","inputs":{"ckpt_name":"bbb.safetensors"}},
	  "5:1": {"class_type":"CheckpointLoaderSimple","inputs":{"ckpt_name":"ddd.safetensors"}}
	}`)

	first, ok := PrimaryCheckpoint(FormatAPI, graph)
	if !ok {
		t.Fatal("precondition: fixture must yield a checkpoint")
	}
	seen := map[string]bool{}
	for i := 0; i < 500; i++ {
		got, _ := PrimaryCheckpoint(FormatAPI, graph)
		seen[got] = true
	}
	if len(seen) != 1 {
		t.Errorf("PrimaryCheckpoint(api) is NON-DETERMINISTIC: %d distinct answers across 500 "+
			"calls; this feeds autoLink's persisted model_id/version_id", len(seen))
	}
	if first != "aaa.safetensors" {
		t.Errorf("PrimaryCheckpoint(api) = %q, want the LOWEST node id's checkpoint "+
			"(node 2 = aaa.safetensors)", first)
	}
}

// TestExtractResourcesHandlesNonNumericNodeIDs pins the fallback: ids that are not
// integers sort lexically rather than panicking or silently grouping.
//
// ⚠ This is the SAFE case — all ids are non-numeric, so the comparison is pure
// lexical and total. It cannot observe the intransitivity bug; see
// TestExtractResourcesIsDeterministicWithMixedNodeIDs for that.
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
