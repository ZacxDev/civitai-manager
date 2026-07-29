package comfy

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

// TestReferencesModelFile covers the binding predicate the web layer uses to confirm a
// (workflow, filename) pair belongs together before that filename drives a fetch+write.
func TestReferencesModelFile(t *testing.T) {
	const apiLoader = `{"4":{"class_type":"CheckpointLoaderSimple","inputs":{"ckpt_name":"jug_v9.safetensors"}}}`
	// A NON-loader class: ExtractResources ignores these, which is exactly why this
	// predicate does not reuse it — a real bad-option install target lives here.
	const apiNonLoader = `{"42":{"class_type":"UltralyticsDetectorProvider","inputs":{"model_name":"bbox/face_yolov9c.pt"}}}`
	const uiFlat = `{"nodes":[{"type":"CheckpointLoaderSimple","widgets_values":["jug_v9.safetensors",1]}]}`
	// Object-form widgets_values (v0.1.46 reality) — a []RawMessage decode would fail
	// here and wrongly report "not referenced".
	const uiObject = `{"nodes":[{"type":"X","widgets_values":{"ckpt_name":"jug_v9.safetensors"}}]}`
	const uiNested = `{"nodes":[{"type":"X","widgets_values":[["a",["jug_v9.safetensors"]]]}]}`

	cases := []struct {
		name, format, graph, filename string
		want                          bool
	}{
		{"api loader exact", FormatAPI, apiLoader, "jug_v9.safetensors", true},
		{"api loader case-insensitive", FormatAPI, apiLoader, "JUG_V9.SAFETENSORS", true},
		{"api non-loader class", FormatAPI, apiNonLoader, "bbox/face_yolov9c.pt", true},
		{"api non-loader by basename", FormatAPI, apiNonLoader, "face_yolov9c.pt", true},
		{"api basename matches subfoldered ref", FormatAPI, apiLoader, "sub/jug_v9.safetensors", true},
		{"api unreferenced", FormatAPI, apiLoader, "somethingElse.safetensors", false},
		{"ui flat", FormatUI, uiFlat, "jug_v9.safetensors", true},
		{"ui object-form widgets", FormatUI, uiObject, "jug_v9.safetensors", true},
		{"ui nested arrays", FormatUI, uiNested, "jug_v9.safetensors", true},
		{"ui unreferenced", FormatUI, uiFlat, "other.safetensors", false},
		{"empty filename", FormatAPI, apiLoader, "", false},
		{"whitespace filename", FormatAPI, apiLoader, "   ", false},
		{"unparseable graph", FormatAPI, `{not json`, "jug_v9.safetensors", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ReferencesModelFile(c.format, json.RawMessage(c.graph), c.filename); got != c.want {
				t.Errorf("ReferencesModelFile(%q) = %v, want %v", c.filename, got, c.want)
			}
		})
	}
}

// TestReferencesModelFileFormatAgnostic: the predicate must not depend on the declared
// format string (a mislabeled workflow must not silently become "references nothing",
// which would refuse a legitimate install).
func TestReferencesModelFileFormatAgnostic(t *testing.T) {
	const g = `{"4":{"class_type":"CheckpointLoaderSimple","inputs":{"ckpt_name":"jug_v9.safetensors"}}}`
	for _, format := range []string{FormatAPI, FormatUI, "", "nonsense"} {
		if !ReferencesModelFile(format, json.RawMessage(g), "jug_v9.safetensors") {
			t.Errorf("format %q: want true", format)
		}
	}
}

// TestReferencesModelFileDepthBound: a pathologically nested graph terminates (bounded
// recursion) rather than blowing the stack.
func TestReferencesModelFileDepthBound(t *testing.T) {
	deep := strings.Repeat(`[`, 5000) + `"jug_v9.safetensors"` + strings.Repeat(`]`, 5000)
	// Past the bound the answer is false, but the point is that it RETURNS.
	if ReferencesModelFile(FormatUI, json.RawMessage(deep), "jug_v9.safetensors") {
		t.Error("a graph nested past referenceWalkMaxDepth should not match")
	}
	// Just inside the bound it still finds the value.
	n := referenceWalkMaxDepth - 2
	shallow := strings.Repeat(`[`, n) + `"jug_v9.safetensors"` + strings.Repeat(`]`, n)
	if !ReferencesModelFile(FormatUI, json.RawMessage(shallow), "jug_v9.safetensors") {
		t.Errorf("nesting of %s should still match", strconv.Itoa(n))
	}
}
