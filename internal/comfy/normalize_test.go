package comfy

import (
	"encoding/json"
	"strings"
	"testing"
)

// hasNormalizeWarning reports whether any warning looks like a normalization note.
func hasNormalizeWarning(warns []string) bool {
	for _, w := range warns {
		if strings.Contains(w, "normalized") {
			return true
		}
	}
	return false
}

// TestNormalizeSingleChoiceComboDrift is the 587 wildcard shape: a single-choice
// combo whose saved value has drifted is normalized to the one valid choice, and
// the converted graph then has ZERO BadOptions.
func TestNormalizeSingleChoiceComboDrift(t *testing.T) {
	info := buildInfo(t, `{
		"ImpactWildcardProcessor": {
			"input": {"required": {
				"wildcard_text": ["STRING", {}],
				"Select to add Wildcard": [["Select the Wildcard to add to the text"], {}]
			}},
			"input_order": {"required": ["wildcard_text","Select to add Wildcard"]}
		}
	}`)
	ui := `{"nodes":[
		{"id":3,"type":"ImpactWildcardProcessor","mode":0,
		 "widgets_values":["some text","Select Wildcard 🟢 Full Cache"]}
	],"links":[]}`
	nodes, warns := convertNodes(t, ui, info)
	// Normalization is SILENT — it must NOT emit a warning (the run path aborts on
	// any warning). The emitted value + zero BadOptions are the real signals.
	if hasNormalizeWarning(warns) {
		t.Errorf("normalization must be silent, got warning: %v", warns)
	}
	got := scalarString(t, nodes["3"].Inputs["Select to add Wildcard"])
	if got != "Select the Wildcard to add to the text" {
		t.Errorf("Select to add Wildcard = %q, want normalized placeholder", got)
	}
	// wildcard_text (free-text STRING) must be untouched.
	if v := scalarString(t, nodes["3"].Inputs["wildcard_text"]); v != "some text" {
		t.Errorf("wildcard_text = %q, want unchanged", v)
	}
	// Converted graph must have NO BadOptions.
	assertNoBadOptions(t, nodes, info)
}

// TestNormalizeMultiChoiceInertPicker: a curated inert picker that is MULTI-choice
// with a drifted value is normalized to the placeholder (Choices[0]).
func TestNormalizeMultiChoiceInertPicker(t *testing.T) {
	info := buildInfo(t, `{
		"EditDetailerPipe": {
			"input": {"required": {
				"wildcard": ["STRING", {}],
				"Select to add LoRA": [["Select the LoRA to add to the text","a.safetensors","b.safetensors"], {}]
			}},
			"input_order": {"required": ["wildcard","Select to add LoRA"]}
		}
	}`)
	ui := `{"nodes":[
		{"id":5,"type":"EditDetailerPipe","mode":0,
		 "widgets_values":["","Select LoRA 🟢 renamed placeholder"]}
	],"links":[]}`
	nodes, warns := convertNodes(t, ui, info)
	if hasNormalizeWarning(warns) {
		t.Errorf("normalization must be silent, got warning: %v", warns)
	}
	got := scalarString(t, nodes["5"].Inputs["Select to add LoRA"])
	if got != "Select the LoRA to add to the text" {
		t.Errorf("Select to add LoRA = %q, want placeholder Choices[0]", got)
	}
	assertNoBadOptions(t, nodes, info)
}

// TestNormalizeModelComboGuard is the CRITICAL guard: a single-choice combo whose
// drifted value is a MODEL FILE must NOT be normalized (silently swapping a model
// changes output). It stays the drifted value and remains a BadOption.
func TestNormalizeModelComboGuard(t *testing.T) {
	info := buildInfo(t, `{
		"UltralyticsDetectorProvider": {
			"input": {"required": {
				"model_name": [["bbox/face_yolov9c.pt"], {}]
			}},
			"input_order": {"required": ["model_name"]}
		}
	}`)
	ui := `{"nodes":[
		{"id":7,"type":"UltralyticsDetectorProvider","mode":0,
		 "widgets_values":["bbox/some_other_detector.pt"]}
	],"links":[]}`
	nodes, warns := convertNodes(t, ui, info)
	if hasNormalizeWarning(warns) {
		t.Errorf("model combo must NOT be normalized, but got warning: %v", warns)
	}
	got := scalarString(t, nodes["7"].Inputs["model_name"])
	if got != "bbox/some_other_detector.pt" {
		t.Errorf("model_name = %q, want the drifted value UNCHANGED", got)
	}
	// It must still surface as a BadOption downstream.
	report := preflightNodes(t, nodes, info)
	if len(report.BadOptions) != 1 {
		t.Fatalf("expected 1 BadOption for the drifted model combo, got %d: %+v",
			len(report.BadOptions), report.BadOptions)
	}
	if report.BadOptions[0].InputName != "model_name" || report.BadOptions[0].Current != "bbox/some_other_detector.pt" {
		t.Errorf("BadOption = %+v, want model_name drift", report.BadOptions[0])
	}
}

// TestNormalizeGenuineMultiChoiceUntouched: a genuine (non-inert) multi-choice
// combo with a drifted value is left unchanged and stays a BadOption — the user
// must choose.
func TestNormalizeGenuineMultiChoiceUntouched(t *testing.T) {
	info := buildInfo(t, `{
		"KSampler": {
			"input": {"required": {
				"scheduler": [["normal","karras","exponential","sgm_uniform"], {}]
			}},
			"input_order": {"required": ["scheduler"]}
		}
	}`)
	ui := `{"nodes":[
		{"id":9,"type":"KSampler","mode":0,"widgets_values":["some_removed_scheduler"]}
	],"links":[]}`
	nodes, warns := convertNodes(t, ui, info)
	if hasNormalizeWarning(warns) {
		t.Errorf("genuine multi-choice drift must NOT be normalized, got: %v", warns)
	}
	if got := scalarString(t, nodes["9"].Inputs["scheduler"]); got != "some_removed_scheduler" {
		t.Errorf("scheduler = %q, want unchanged", got)
	}
	report := preflightNodes(t, nodes, info)
	if len(report.BadOptions) != 1 {
		t.Fatalf("expected 1 BadOption for genuine scheduler drift, got %d: %+v",
			len(report.BadOptions), report.BadOptions)
	}
}

// TestNormalizeValidValueUnchanged: a VALID combo value is passed through
// untouched, with no warning; a free-text STRING is untouched too.
func TestNormalizeValidValueUnchanged(t *testing.T) {
	info := buildInfo(t, `{
		"KSampler": {
			"input": {"required": {
				"prompt": ["STRING", {}],
				"sampler_name": [["euler","dpmpp_2m","dpmpp_sde"], {}]
			}},
			"input_order": {"required": ["prompt","sampler_name"]}
		}
	}`)
	ui := `{"nodes":[
		{"id":2,"type":"KSampler","mode":0,"widgets_values":["a photo","dpmpp_2m"]}
	],"links":[]}`
	nodes, warns := convertNodes(t, ui, info)
	if len(warns) != 0 {
		t.Errorf("unexpected warnings for valid value: %v", warns)
	}
	if got := scalarString(t, nodes["2"].Inputs["sampler_name"]); got != "dpmpp_2m" {
		t.Errorf("sampler_name = %q, want unchanged valid value", got)
	}
	if got := scalarString(t, nodes["2"].Inputs["prompt"]); got != "a photo" {
		t.Errorf("prompt = %q, want unchanged free text", got)
	}
}

// TestNormalizeSingleChoiceNonModelStillGuardsModelValue: a single-choice combo
// whose choices are model files and whose drifted value is ALSO a model file must
// stay a BadOption (guard applies to single-choice model loaders too, e.g. a
// single installed checkpoint).
func TestNormalizeSingleChoiceModelLoaderGuard(t *testing.T) {
	info := buildInfo(t, `{
		"CheckpointLoaderSimple": {
			"input": {"required": {"ckpt_name": [["only_installed.safetensors"], {}]}},
			"input_order": {"required": ["ckpt_name"]}
		}
	}`)
	ui := `{"nodes":[
		{"id":4,"type":"CheckpointLoaderSimple","mode":0,"widgets_values":["was_uninstalled.safetensors"]}
	],"links":[]}`
	nodes, warns := convertNodes(t, ui, info)
	if hasNormalizeWarning(warns) {
		t.Errorf("single-choice MODEL loader must NOT be normalized, got: %v", warns)
	}
	if got := scalarString(t, nodes["4"].Inputs["ckpt_name"]); got != "was_uninstalled.safetensors" {
		t.Errorf("ckpt_name = %q, want the drifted model value UNCHANGED", got)
	}
}

// TestNormalize587Integration reproduces 587's drift class end-to-end through
// Preflight: EditDetailerPipe ×2 and ImpactWildcardProcessor ×2 all carry the
// drifted "Select to add Wildcard". After conversion, Preflight reports ZERO
// BadOptions (the drift was normalized away in the converter, mirroring ComfyUI).
func TestNormalize587Integration(t *testing.T) {
	info := buildInfo(t, `{
		"EditDetailerPipe": {
			"input": {"required": {
				"Select to add Wildcard": [["Select the Wildcard to add to the text"], {}]
			}},
			"input_order": {"required": ["Select to add Wildcard"]}
		},
		"ImpactWildcardProcessor": {
			"input": {"required": {
				"wildcard_text": ["STRING", {}],
				"Select to add Wildcard": [["Select the Wildcard to add to the text"], {}]
			}},
			"input_order": {"required": ["wildcard_text","Select to add Wildcard"]}
		}
	}`)
	ui := `{"nodes":[
		{"id":12,"type":"EditDetailerPipe","mode":0,"widgets_values":["Select Wildcard 🟢 Full Cache"]},
		{"id":13,"type":"EditDetailerPipe","mode":0,"widgets_values":["Select Wildcard 🟢 Full Cache"]},
		{"id":3,"type":"ImpactWildcardProcessor","mode":0,"widgets_values":["prompt","Select Wildcard 🟢 Full Cache"]},
		{"id":4,"type":"ImpactWildcardProcessor","mode":0,"widgets_values":["prompt","Select Wildcard 🟢 Full Cache"]}
	],"links":[]}`
	api, warns, err := ConvertUIToAPI(json.RawMessage(ui), info)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	// Silent: no normalization warning (would abort the run), and 0 BadOptions.
	if hasNormalizeWarning(warns) {
		t.Errorf("normalization must be silent, got warnings: %v", warns)
	}
	report := Preflight(api, info, nil)
	if len(report.BadOptions) != 0 {
		t.Fatalf("587 drift: expected 0 BadOptions after conversion, got %d: %+v",
			len(report.BadOptions), report.BadOptions)
	}
}

// assertNoBadOptions marshals the converted nodes to an api graph and asserts
// Preflight finds no BadOptions.
func assertNoBadOptions(t *testing.T, nodes map[string]apiOutNode, info ObjectInfo) {
	t.Helper()
	report := preflightNodes(t, nodes, info)
	if len(report.BadOptions) != 0 {
		t.Fatalf("expected 0 BadOptions, got %d: %+v", len(report.BadOptions), report.BadOptions)
	}
}

// preflightNodes re-marshals converted nodes and runs Preflight (localHave nil).
func preflightNodes(t *testing.T, nodes map[string]apiOutNode, info ObjectInfo) PreflightReport {
	t.Helper()
	api, err := json.Marshal(nodes)
	if err != nil {
		t.Fatalf("marshal nodes: %v", err)
	}
	return Preflight(api, info, nil)
}
