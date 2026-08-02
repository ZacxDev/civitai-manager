package comfy

import (
	"bytes"
	"encoding/json"
	"testing"
)

// objectInfoJSON builds a minimal /object_info body mapping class_type →
// python_module, in the shape ComfyUI actually serves.
//
// ⚠ IT ALWAYS EMITS THE KEY, even for "". An empty string and an ABSENT
// python_module are different inputs to the decoder — Go's zero value makes them
// land on the same branch, but only one of them is what a real payload from an
// older/odd ComfyUI would carry, and a fixture that cannot express the absent case
// cannot test it. objectInfoJSONMissingModule below is the other one; use it
// rather than reading `""` as covering both.
func objectInfoJSON(t *testing.T, modules map[string]string) []byte {
	t.Helper()
	doc := map[string]map[string]any{}
	for class, mod := range modules {
		doc[class] = map[string]any{"python_module": mod, "input": map[string]any{}}
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	return raw
}

// objectInfoJSONMissingModule builds an entry with NO python_module key at all.
func objectInfoJSONMissingModule(t *testing.T, class string) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]map[string]any{
		class: {"input": map[string]any{}},
	})
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	if bytes.Contains(raw, []byte("python_module")) {
		t.Fatalf("fixture broken: it must NOT emit python_module at all: %s", raw)
	}
	return raw
}

// TestNodeOriginsIsADenyListOnCustomNodes is the DISCRIMINATING test for the rule
// this fix turns on. The obvious alternative spelling — an ALLOW-list of
// `comfy_extras.*` and `nodes` — passes every other test in this file and fails
// only here.
//
// `comfy_api_nodes.*` is the case that separates them, and it is not
// hypothetical: it is 224 of the live instance's 2462 node types (the four live
// roots are custom_nodes 1672 / comfy_extras 501 / comfy_api_nodes 224 /
// nodes 65). An allow-list would call all 224 custom.
func TestNodeOriginsIsADenyListOnCustomNodes(t *testing.T) {
	raw := objectInfoJSON(t, map[string]string{
		"CoreBare":       "nodes",
		"CoreExtra":      "comfy_extras.nodes_wan",
		"CoreAPINode":    "comfy_api_nodes.nodes_openai",
		"FutureCoreNS":   "comfy_brand_new_namespace.things",
		"RealCustomNode": "custom_nodes.comfyui-frame-interpolation",
	})
	idx := NodeOrigins(raw)
	if len(idx) != 5 {
		t.Fatalf("fixture did not decode: got %d entries, want 5", len(idx))
	}

	for _, class := range []string{"CoreBare", "CoreExtra", "CoreAPINode", "FutureCoreNS"} {
		if got := OriginOf(idx, class); got != NodeOriginBuiltin {
			t.Errorf("OriginOf(%q) = %v, want NodeOriginBuiltin — the rule must EXCLUDE "+
				"custom_nodes, not enumerate core namespaces", class, got)
		}
	}
	if got := OriginOf(idx, "RealCustomNode"); got != NodeOriginCustom {
		t.Errorf("OriginOf(RealCustomNode) = %v, want NodeOriginCustom", got)
	}
}

// TestNodeOriginsMatchesTheFirstDotSegment pins that classification splits on a
// dot boundary rather than doing a string-prefix compare, which would also match
// a module merely STARTING with the word.
//
// 🔴 `Impostor` ALONE CANNOT DISCRIMINATE — `BareCustomNodes` is what does.
// `custom_nodesomething.pack` is classified identically by first-dot-segment
// matching and by strings.HasPrefix(m, "custom_nodes.")  (both say built-in: the
// first segment is `custom_nodesomething`, and the prefix WITH its trailing dot
// does not match). The case that separates them is a BARE `custom_nodes` with no
// dot: first-segment matching calls it custom, the HasPrefix spelling calls it
// built-in. Impostor is kept because it rules out the OTHER wrong spelling,
// HasPrefix without the dot — the two fixtures pin different mistakes.
func TestNodeOriginsMatchesTheFirstDotSegment(t *testing.T) {
	raw := objectInfoJSON(t, map[string]string{
		"Impostor":        "custom_nodesomething.pack", // NOT custom_nodes/
		"Genuine":         "custom_nodes.pack",
		"BareCustomNodes": "custom_nodes", // no dot at all
		"BareNodes":       "nodes",
		"NoModule":        "",
	})
	idx := NodeOrigins(raw)
	if len(idx) != 5 {
		t.Fatalf("fixture did not decode: got %d entries, want 5", len(idx))
	}

	if got := OriginOf(idx, "Impostor"); got != NodeOriginBuiltin {
		t.Errorf("OriginOf(Impostor) = %v, want NodeOriginBuiltin — "+
			"`custom_nodesomething` is not the `custom_nodes` package", got)
	}
	if got := OriginOf(idx, "Genuine"); got != NodeOriginCustom {
		t.Errorf("OriginOf(Genuine) = %v, want NodeOriginCustom", got)
	}
	if got := OriginOf(idx, "BareCustomNodes"); got != NodeOriginCustom {
		t.Errorf("OriginOf(BareCustomNodes) = %v, want NodeOriginCustom — a dot-free "+
			"`custom_nodes` is the module ROOT itself. This is the fixture that "+
			"separates first-dot-segment matching from "+
			"strings.HasPrefix(m, \"custom_nodes.\"), which would call it built-in", got)
	}
	if got := OriginOf(idx, "BareNodes"); got != NodeOriginBuiltin {
		t.Errorf("OriginOf(BareNodes) = %v, want NodeOriginBuiltin", got)
	}
	// An EMPTY python_module is an absence of evidence, not evidence of core.
	// ⚠ This covers `"python_module": ""`, NOT a missing key — see
	// TestNodeOriginsTreatsAMissingPythonModuleAsUnknown for that one.
	if got := OriginOf(idx, "NoModule"); got != NodeOriginUnknown {
		t.Errorf("OriginOf(NoModule) = %v, want NodeOriginUnknown", got)
	}
}

// TestNodeOriginsTreatsAMissingPythonModuleAsUnknown covers the input the fixture
// above structurally cannot produce: an entry with NO python_module key.
//
// Go decodes both an absent key and `""` to the zero value, so this and the `""`
// case do land on the same branch today — but that is a property of the current
// decode shape, not a guarantee. Pinning both means a future decoder that
// distinguishes them (a *string, a json.RawMessage, a presence flag) cannot
// silently start answering Builtin for a payload that stated nothing.
func TestNodeOriginsTreatsAMissingPythonModuleAsUnknown(t *testing.T) {
	idx := NodeOrigins(objectInfoJSONMissingModule(t, "NoModuleKey"))
	if len(idx) != 1 {
		t.Fatalf("fixture did not decode: got %d entries, want 1", len(idx))
	}
	if got := OriginOf(idx, "NoModuleKey"); got != NodeOriginUnknown {
		t.Errorf("OriginOf(NoModuleKey) = %v, want NodeOriginUnknown — a payload that "+
			"reported no module at all tells us nothing, and answering built-in there "+
			"would assert on absent evidence", got)
	}
}

// TestNodeOriginsUnknownForAbsentAndUndecodable pins the two ways we end up with
// no observation. Both must read Unknown so the caller falls back to the table.
func TestNodeOriginsUnknownForAbsentAndUndecodable(t *testing.T) {
	idx := NodeOrigins(objectInfoJSON(t, map[string]string{"Known": "nodes"}))
	if got := OriginOf(idx, "NeverHeardOfIt"); got != NodeOriginUnknown {
		t.Errorf("absent class = %v, want NodeOriginUnknown", got)
	}
	if got := OriginOf(nil, "Known"); got != NodeOriginUnknown {
		t.Errorf("nil index = %v, want NodeOriginUnknown", got)
	}
	for name, raw := range map[string][]byte{
		"corrupt": []byte(`{not json`),
		"empty":   nil,
		"noNodes": []byte(`{}`),
	} {
		if idx := NodeOrigins(raw); idx != nil {
			t.Errorf("%s payload yielded a non-nil index (%d entries); a caller must "+
				"fall back to coreNodeClasses, not treat everything as unknown-but-present",
				name, len(idx))
		}
	}
}

// resolveGraph is a 2×2 fixture over (in coreNodeClasses?) × (what /object_info
// says). Every cell is a DIFFERENT class name so no assertion can be satisfied by
// the wrong one, and the two dimensions are deliberately OPPOSED in the top row:
// `WanImageToVideo` is a real built-in the table does NOT know, and
// `KSampler` is a table entry the fixture reports as custom. If the code consulted
// only the table, or only the payload, a different set of rows comes back.
const resolveGraph = `{
	"1": {"class_type":"WanImageToVideo","inputs":{}},
	"2": {"class_type":"KSampler","inputs":{}},
	"3": {"class_type":"PrimitiveNode","inputs":{}},
	"4": {"class_type":"TotallyUnheardOfNode","inputs":{}}
}`

func customRows(t *testing.T, graph string, origins map[string]NodeOrigin) map[string]bool {
	t.Helper()
	rows, err := ResolveResources(json.RawMessage(graph), nil, origins)
	if err != nil {
		t.Fatalf("ResolveResources: %v", err)
	}
	got := map[string]bool{}
	for _, r := range rows {
		if r.Status == ResolveCustomNode {
			got[r.Filename] = true
		}
	}
	return got
}

// TestResolveResourcesPrefersObjectInfoOverTheTable is the end-to-end guard: the
// observed payload must WIN over coreNodeClasses in BOTH directions.
func TestResolveResourcesPrefersObjectInfoOverTheTable(t *testing.T) {
	// Precondition — assert the fixture actually expresses the contest, so a
	// future edit to coreNodeClasses cannot make this test vacuous.
	if coreNodeClasses["WanImageToVideo"] {
		t.Fatal("fixture broken: WanImageToVideo must NOT be in coreNodeClasses")
	}
	if !coreNodeClasses["KSampler"] {
		t.Fatal("fixture broken: KSampler must be in coreNodeClasses")
	}
	if !coreNodeClasses["PrimitiveNode"] {
		t.Fatal("fixture broken: PrimitiveNode must be in coreNodeClasses")
	}

	idx := NodeOrigins(objectInfoJSON(t, map[string]string{
		"WanImageToVideo": "comfy_extras.nodes_wan", // built-in the table misses
		"KSampler":        "custom_nodes.impostor",  // table says core, payload says custom
		// PrimitiveNode + TotallyUnheardOfNode deliberately absent → Unknown → table.
	}))
	// Precondition: the payload really does classify these two the opposed way.
	if OriginOf(idx, "WanImageToVideo") != NodeOriginBuiltin ||
		OriginOf(idx, "KSampler") != NodeOriginCustom {
		t.Fatal("fixture broken: the payload does not oppose the table")
	}

	got := customRows(t, resolveGraph, idx)

	if got["WanImageToVideo"] {
		t.Error("WanImageToVideo was flagged custom — a real ComfyUI built-in " +
			"(comfy_extras.nodes_wan) must not be, this is the bug being fixed")
	}
	if !got["KSampler"] {
		t.Error("KSampler was NOT flagged custom — the observed payload must " +
			"override coreNodeClasses, not merely supplement it")
	}
	if got["PrimitiveNode"] {
		t.Error("PrimitiveNode was flagged custom — a class absent from the payload " +
			"must fall back to coreNodeClasses, which excludes it")
	}
	if !got["TotallyUnheardOfNode"] {
		t.Error("TotallyUnheardOfNode was NOT flagged custom — an unclassifiable " +
			"class absent from the table must keep the pre-existing behaviour")
	}
	if len(got) != 2 {
		t.Errorf("got %d custom rows (%v), want exactly 2", len(got), got)
	}
}

// TestResolveResourcesFallsBackToTableWithoutObjectInfo pins that a nil index —
// no ComfyUI, cold cache, corrupt row — reproduces the PRE-EXISTING behaviour
// exactly. This is the fail-direction decision: unknown holds the status quo.
func TestResolveResourcesFallsBackToTableWithoutObjectInfo(t *testing.T) {
	got := customRows(t, resolveGraph, nil)

	// Table entries excluded, non-entries flagged — i.e. the old detector.
	if got["KSampler"] || got["PrimitiveNode"] {
		t.Errorf("with no payload, coreNodeClasses entries must be excluded; got %v", got)
	}
	if !got["WanImageToVideo"] || !got["TotallyUnheardOfNode"] {
		t.Errorf("with no payload, non-table classes must still be flagged; got %v", got)
	}
	if len(got) != 2 {
		t.Errorf("got %d custom rows (%v), want exactly 2", len(got), got)
	}
}
