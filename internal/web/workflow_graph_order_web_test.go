package web

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The api-format node listing sorts a RANDOMISED map range (structuredAPINodes
// ranges over map[string]apiNode), so its comparator must be a strict weak
// ordering or `sort.Slice` returns an arbitrary permutation of its input rather
// than an order. This file pins that.
//
// 🔴 THE FIXTURE MUST BE MIXED. An all-numeric or all-non-numeric id set is
// internally a total order under the naive "numeric if both parse, else lexical"
// comparator, so it CANNOT observe the defect — it is the control here, not the
// subject. The mixed set below contains a real cycle under the naive rule:
//
//	"12:3" < "9"  (lexical) ∧ "9" < "12" (numeric) ∧ "12" < "12:3" (lexical)
//
// Mixed ids are reachable, not theoretical: internal/comfy/convert_subgraph.go
// mints interior node ids as "<instance>:<interior>", and convert_test.go pins a
// converted api graph keyed {"4","17","100:1"}.
//
// Beyond render order the defect randomises WHICH nodes survive the gMaxNodes
// truncation (ids are cut to a prefix after the sort), so a large graph shows a
// different subset per process.

const mixedIDAPIGraph = `{
  "12:8": {"class_type":"VHS_VideoCombine","inputs":{}},
  "1":    {"class_type":"CheckpointLoaderSimple","inputs":{}},
  "12":   {"class_type":"KSampler","inputs":{}},
  "9":    {"class_type":"CLIPTextEncode","inputs":{}},
  "4":    {"class_type":"VAEDecode","inputs":{}},
  "12:3": {"class_type":"EmptyLatentImage","inputs":{}}
}`

const numericIDAPIGraph = `{
  "12": {"class_type":"KSampler","inputs":{}},
  "1":  {"class_type":"CheckpointLoaderSimple","inputs":{}},
  "9":  {"class_type":"CLIPTextEncode","inputs":{}},
  "4":  {"class_type":"VAEDecode","inputs":{}}
}`

// The card header's id span — deliberately anchored to that element's own class
// string rather than a bare "#id" substring, which an input row's "← #4[0]"
// connection label would also satisfy.
var apiCardIDRe = regexp.MustCompile(`class="text-xs font-mono text-slate-500">#([^<]+)<`)

// renderedAPINodeOrder returns the node ids in the order structuredAPINodes
// emitted them.
//
// ⚠ It must call structuredAPINodes DIRECTLY. structuredUINodes emits the same
// header markup verbatim (workflow_graph.go), so apiCardIDRe is not unique to an
// api card — repointing this at structuredGraphNodes would silently over-match a
// UI listing. The under-match direction is covered by each caller's exact-count
// precondition.
func renderedAPINodeOrder(t *testing.T, graph string) []string {
	t.Helper()
	node := structuredAPINodes([]byte(graph))
	if node == nil {
		t.Fatal("structuredAPINodes returned nil for an api-shaped graph")
	}
	ms := apiCardIDRe.FindAllStringSubmatch(renderGraphNode(t, node), -1)
	ids := make([]string, 0, len(ms))
	for _, m := range ms {
		ids = append(ids, m[1])
	}
	return ids
}

// distinctOrders counts the distinct sequences in runs.
func distinctOrders(runs [][]string) int {
	seen := map[string]struct{}{}
	for _, r := range runs {
		seen[strings.Join(r, ",")] = struct{}{}
	}
	return len(seen)
}

// TestStructuredAPINodesOrderIsDeterministicForMixedIDs is the regression guard
// for the third open-coding of the intransitive node-id comparator (the first two
// were internal/comfy's AllImages and ExtractResources). Measured on THIS fixture
// with the naive comparator reinstated: 3 distinct rendered orders across 500
// calls, stable across 8 independent runs; the all-numeric control reported 1.
// ⚠ The handoff doc's "5" is a measurement of a DIFFERENT mixed id set — the
// count is a property of the fixture, not run-to-run variance. Do not copy a
// number here from anywhere but a run of this file.
func TestStructuredAPINodesOrderIsDeterministicForMixedIDs(t *testing.T) {
	// Positive control for the counter itself: a "1" from a counter wired to
	// nothing is indistinguishable from a "1" from a deterministic sort.
	if n := distinctOrders([][]string{{"a", "b"}, {"b", "a"}}); n != 2 {
		t.Fatalf("distinctOrders cannot observe variation: reported %d for two different orders", n)
	}

	const calls = 500

	mixed := make([][]string, 0, calls)
	for i := 0; i < calls; i++ {
		got := renderedAPINodeOrder(t, mixedIDAPIGraph)
		// Precondition: the extractor must actually see every node, or "1
		// distinct order" would just mean "found nothing, every run".
		if len(got) != 6 {
			t.Fatalf("extracted %d ids (%v); the fixture has 6", len(got), got)
		}
		mixed = append(mixed, got)
	}
	if n := distinctOrders(mixed); n != 1 {
		t.Errorf("mixed-id graph rendered %d distinct node orders across %d calls; want 1", n, calls)
	}

	// Pin the RULE, not just stability: every numeric id first in value order,
	// then every non-numeric id lexically (comfy.LessNodeKey). A merely stable
	// but wrong comparator would pass the determinism check alone.
	want := []string{"1", "4", "9", "12", "12:3", "12:8"}
	if got := strings.Join(mixed[0], ","); got != strings.Join(want, ",") {
		t.Errorf("node order = %s; want %s", got, strings.Join(want, ","))
	}

	// Control: an all-numeric id set is a total order under ANY of the
	// comparators involved, so it must be — and always was — deterministic. It
	// fails only if something unrelated to the ordering rule broke.
	numeric := make([][]string, 0, calls)
	for i := 0; i < calls; i++ {
		got := renderedAPINodeOrder(t, numericIDAPIGraph)
		if len(got) != 4 {
			t.Fatalf("control extracted %d ids (%v); the fixture has 4", len(got), got)
		}
		numeric = append(numeric, got)
	}
	if n := distinctOrders(numeric); n != 1 {
		t.Errorf("all-numeric control rendered %d distinct orders across %d calls; want 1", n, calls)
	}
	if got := strings.Join(numeric[0], ","); got != "1,4,9,12" {
		t.Errorf("control order = %s; want 1,4,9,12", got)
	}
}

// TestStructuredAPINodesTruncatesTheDeterministicPrefix pins the OTHER half of
// the ordering bug: `ids` is cut to `ids[:gMaxNodes]` AFTER the sort, so an
// unsound comparator randomises WHICH nodes a large graph shows, not merely their
// order. That claim was previously asserted nowhere — the fixtures above are 6 and
// 4 nodes, so they never reach the cap, and TestGraphStructuredCapsHugeNodeCount
// asserts only the banner text. Both truncation mutants (keep the SUFFIX; take
// gMaxNodes-1) passed the entire internal/web package before this test existed.
//
// The fixture is over-cap AND mixed, so it pins the surviving SET, the prefix
// direction, and the numeric-before-non-numeric partition. Mutation-measured:
// keep-the-suffix RED, gMaxNodes-1 RED, LessNodeKey's partition inverted RED.
//
// ⚠ It does NOT observe comparator INTRANSITIVITY — with the naive comparator
// reinstated it was green 20/20, because one call cannot see a randomised
// permutation. That is the job of the 500-call test above; this one owns the
// truncation direction only. Do not read a green here as "the ordering is sound".
func TestStructuredAPINodesTruncatesTheDeterministicPrefix(t *testing.T) {
	const numerics = gMaxNodes + 50 // ids "0".."649" — 50 past the cap
	var b strings.Builder
	b.WriteByte('{')
	for i := 0; i < numerics; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`"`)
		b.WriteString(strconv.Itoa(i))
		b.WriteString(`":{"class_type":"KSampler","inputs":{}}`)
	}
	// Non-numeric (subgraph-minted) ids sort AFTER every numeric one, so a correct
	// prefix truncation drops all six.
	for i := 0; i < 6; i++ {
		b.WriteString(`,"700:`)
		b.WriteString(strconv.Itoa(i))
		b.WriteString(`":{"class_type":"VAEDecode","inputs":{}}`)
	}
	b.WriteByte('}')

	got := renderedAPINodeOrder(t, b.String())
	if len(got) != gMaxNodes {
		t.Fatalf("rendered %d node cards; the cap is %d", len(got), gMaxNodes)
	}
	// The surviving set is the numeric ids 0..gMaxNodes-1, in value order.
	if got[0] != "0" {
		t.Errorf("first surviving id = %q; want %q (truncation kept the wrong end)", got[0], "0")
	}
	if want := strconv.Itoa(gMaxNodes - 1); got[len(got)-1] != want {
		t.Errorf("last surviving id = %q; want %q", got[len(got)-1], want)
	}
	for _, id := range got {
		if strings.Contains(id, ":") {
			t.Fatalf("non-numeric id %q survived truncation; every numeric id sorts before it", id)
		}
	}
}
