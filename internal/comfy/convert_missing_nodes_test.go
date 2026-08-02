package comfy

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// missingNodesInfo knows exactly ONE class, so every other node in the fixture
// below is "not installed" as far as the converter is concerned.
const missingNodesInfo = `{"CheckpointLoaderSimple":{"input":{"required":{"ckpt_name":[["a.safetensors"],{}]}},"input_order":{"required":["ckpt_name"]}}}`

// missingNodesUI is the shape of the real workflow-590 failure, compressed:
//
//   - one INSTALLED node, so the conversion is not empty;
//   - TWO active nodes of the SAME uninstalled class (distinctness);
//   - one active node of a SECOND uninstalled class (sorting);
//   - one BYPASSED and one MUTED node of uninstalled classes (must be excluded —
//     the user disabled them, so no node pack is needed);
//   - the five annotation / UI-only types that dominate a real graph (in wf 590:
//     MarkdownNote ×16, Note ×2, Label (rgthree) ×5, the Fast Bypasser family ×5).
//     None has a backend class BY DESIGN and none may ever be reported missing.
const missingNodesUI = `{"nodes":[
 {"id":4,"type":"CheckpointLoaderSimple","mode":0,"widgets_values":["a.safetensors"]},
 {"id":663,"type":"UltimateSDUpscale","mode":0},
 {"id":664,"type":"UltimateSDUpscale","mode":0},
 {"id":705,"type":"AaaUnknownNode","mode":0},
 {"id":665,"type":"SeedVR2BypassedNode","mode":4},
 {"id":666,"type":"MutedCustomNode","mode":2},
 {"id":700,"type":"Note","mode":0},
 {"id":701,"type":"MarkdownNote","mode":0},
 {"id":702,"type":"Label (rgthree)","mode":0},
 {"id":703,"type":"Fast Groups Bypasser (rgthree)","mode":0},
 {"id":704,"type":"Reroute","mode":0}
],"links":[]}`

// TestConversionResultReportsRemovedActiveClasses pins ConversionResult.MissingNodeTypes:
// the DISTINCT, sorted classes of the ACTIVE nodes the converter cut out — and nothing
// else. That set is what the run path synthesizes a PreflightReport from, so every
// wrong member becomes a node pack the user is told to install for no reason, and
// every missing member is a pack they are never told about.
func TestConversionResultReportsRemovedActiveClasses(t *testing.T) {
	info := buildInfo(t, missingNodesInfo)

	res, err := ConvertUIToAPIResult(json.RawMessage(missingNodesUI), info)
	if err != nil {
		t.Fatalf("ConvertUIToAPIResult: %v", err)
	}

	// INTERMEDIATE STATE FIRST: prove the fixture actually reached the removal
	// branch. A graph that still contained UltimateSDUpscale would make every
	// assertion below true for the wrong reason.
	var nodes map[string]apiOutNode
	if uerr := json.Unmarshal(res.APIGraph, &nodes); uerr != nil {
		t.Fatalf("decode api graph: %v", uerr)
	}
	if len(nodes) != 1 || nodes["4"].ClassType != "CheckpointLoaderSimple" {
		t.Fatalf("fixture did not reach the removal branch; emitted graph = %s", res.APIGraph)
	}
	if strings.Contains(string(res.APIGraph), "UltimateSDUpscale") {
		t.Fatalf("the unknown class survived into the api graph — the hole this field exists for was never created: %s", res.APIGraph)
	}

	want := []string{"AaaUnknownNode", "UltimateSDUpscale"}
	if got := res.MissingNodeTypes; !equalStrings(got, want) {
		t.Errorf("MissingNodeTypes = %v, want %v", got, want)
	}

	// Three nodes were removed but only two CLASSES: the field must be deduped, or
	// attribution issues a duplicate lookup per class and the panel lists it twice.
	warnCount := 0
	for _, w := range res.Warnings {
		if strings.Contains(w, "not available") {
			warnCount++
		}
	}
	if warnCount != 3 {
		t.Errorf("expected 3 not-available warnings (nodes 663, 664, 705), got %d: %v", warnCount, res.Warnings)
	}
}

// TestConversionResultExcludesDisabledAndAnnotationNodes states the exclusions of
// the previous test as their own failures, so a regression names the reason.
//
// The annotation half is not hypothetical: virtualNodeTypes already strips all 28
// UI-only nodes in the real workflow 590, which is why that graph emits ONE warning
// and not 29. If they leaked into MissingNodeTypes the user would be told to install
// node packs for "Note" and "MarkdownNote".
func TestConversionResultExcludesDisabledAndAnnotationNodes(t *testing.T) {
	res, err := ConvertUIToAPIResult(json.RawMessage(missingNodesUI), buildInfo(t, missingNodesInfo))
	if err != nil {
		t.Fatalf("ConvertUIToAPIResult: %v", err)
	}
	forbidden := map[string]string{
		"SeedVR2BypassedNode":            "a BYPASSED node was never going to run",
		"MutedCustomNode":                "a MUTED node was never going to run",
		"Note":                           "an annotation type has no backend class",
		"MarkdownNote":                   "an annotation type has no backend class",
		"Label (rgthree)":                "a canvas label has no backend class",
		"Fast Groups Bypasser (rgthree)": "an rgthree UI-only helper has no backend class",
		"Reroute":                        "a Reroute is spliced through, never executed",
	}
	for _, got := range res.MissingNodeTypes {
		if why, bad := forbidden[got]; bad {
			t.Errorf("MissingNodeTypes contains %q — %s", got, why)
		}
	}
}

// TestConversionResultPopulatedOnEmptyConversion covers the error return: when
// NOTHING converted, the caller still needs to know which classes were responsible,
// and the error path must not swallow them.
func TestConversionResultPopulatedOnEmptyConversion(t *testing.T) {
	ui := `{"nodes":[{"id":1,"type":"UnknownB","mode":0},{"id":2,"type":"UnknownA","mode":0}],"links":[]}`

	res, err := ConvertUIToAPIResult(json.RawMessage(ui), ObjectInfo{})
	var ece *ConversionEmptyError
	if !errors.As(err, &ece) {
		t.Fatalf("want *ConversionEmptyError, got %v", err)
	}
	if ece.Unknown != 2 {
		t.Errorf("ConversionEmptyError.Unknown = %d, want 2", ece.Unknown)
	}
	if want := []string{"UnknownA", "UnknownB"}; !equalStrings(res.MissingNodeTypes, want) {
		t.Errorf("MissingNodeTypes on the empty path = %v, want %v", res.MissingNodeTypes, want)
	}
}

// TestConvertUIToAPIWrapperIsUnchanged pins the three-value entry point the CivitAI
// cloud submit path (internal/web/cloud_handlers.go) still calls. The structured
// result was added as a NEW function precisely so that path needed no edit; this is
// the guard that it stayed byte-identical.
func TestConvertUIToAPIWrapperIsUnchanged(t *testing.T) {
	info := buildInfo(t, missingNodesInfo)

	api, warns, err := ConvertUIToAPI(json.RawMessage(missingNodesUI), info)
	if err != nil {
		t.Fatalf("ConvertUIToAPI: %v", err)
	}
	res, rerr := ConvertUIToAPIResult(json.RawMessage(missingNodesUI), info)
	if rerr != nil {
		t.Fatalf("ConvertUIToAPIResult: %v", rerr)
	}
	if string(api) != string(res.APIGraph) {
		t.Errorf("wrapper graph differs:\n wrapper = %s\n  result = %s", api, res.APIGraph)
	}
	if !equalStrings(warns, res.Warnings) {
		t.Errorf("wrapper warnings differ:\n wrapper = %v\n  result = %v", warns, res.Warnings)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
