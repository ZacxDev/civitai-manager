package comfy

import "testing"

// objectInfoWithSamplersAndLoaders is the shape that matters: ONE loader whose
// combo lists model files, and ONE sampler whose combos list label enums. The
// enum labels are the false-positive source the extension filter exists to kill.
const objectInfoWithSamplersAndLoaders = `{
	"CheckpointLoaderSimple": {"input":{"required":{"ckpt_name":[["flux/flux1-dev.safetensors","v1-5-pruned.ckpt"],{}]}}},
	"LoraLoader": {"input":{"optional":{"lora_name":[["styles\\anime.safetensors"],{}]}}},
	"KSampler": {"input":{"required":{
		"sampler_name":[["euler","dpmpp_2m","normal"],{}],
		"scheduler":[["karras","exponential"],{}],
		"steps":["INT",{"default":20}]
	}}}
}`

func TestModelFileChoicesIndexesFilesByBasename(t *testing.T) {
	idx := ModelFileChoices(buildInfo(t, objectInfoWithSamplersAndLoaders))

	for _, want := range []string{"flux1-dev.safetensors", "v1-5-pruned.ckpt", "anime.safetensors"} {
		if _, ok := idx[want]; !ok {
			t.Errorf("index is missing %q — got %v", want, idx)
		}
	}
	// A subdirectory prefix is stripped, so the raw choice string is NOT a key.
	if _, ok := idx["flux/flux1-dev.safetensors"]; ok {
		t.Errorf("the index must be keyed by BASENAME, not by the raw choice: %v", idx)
	}
}

// 🔴 The regression this whole file exists for: the previous shape scanned every
// node schema's combo choices, so a resource named after a sampler or scheduler
// option reported "ComfyUI has this file".
func TestModelFileChoicesExcludesLabelEnums(t *testing.T) {
	idx := ModelFileChoices(buildInfo(t, objectInfoWithSamplersAndLoaders))

	for _, label := range []string{"euler", "dpmpp_2m", "normal", "karras", "exponential"} {
		if _, ok := idx[label]; ok {
			t.Errorf("sampler/scheduler label %q was indexed as a FILE — a resource with "+
				"that name would falsely report as present in ComfyUI. index=%v", label, idx)
		}
		if HasModelFile(idx, label) {
			t.Errorf("HasModelFile(%q) = true; a label enum is not a file", label)
		}
	}
	// A non-combo input's TYPE name is never a choice, so it can never be indexed.
	if _, ok := idx["INT"]; ok {
		t.Errorf("a primitive input's type name leaked into the index: %v", idx)
	}
	if n := len(idx); n != 3 {
		t.Errorf("expected exactly the 3 file-like choices, got %d: %v", n, idx)
	}
}

func TestHasModelFileNormalisesBothSides(t *testing.T) {
	idx := ModelFileChoices(buildInfo(t, objectInfoWithSamplersAndLoaders))

	for _, ref := range []string{
		"flux1-dev.safetensors",                // bare basename vs a prefixed choice
		"somewhere/else/flux1-dev.safetensors", // prefixed reference vs the same choice
		`packs\anime.safetensors`,              // a Windows-separated reference
	} {
		if !HasModelFile(idx, ref) {
			t.Errorf("HasModelFile(%q) = false, want true (index=%v)", ref, idx)
		}
	}
	for _, ref := range []string{"", "absent.safetensors", "flux1-dev"} {
		if HasModelFile(idx, ref) {
			t.Errorf("HasModelFile(%q) = true, want false", ref)
		}
	}
}

// An extensionless choice is deliberately NOT indexed — the documented
// fail-CLOSED trade. Pinned so the trade cannot be silently reversed.
func TestModelFileChoicesSkipsExtensionlessChoices(t *testing.T) {
	idx := ModelFileChoices(buildInfo(t, `{
		"DiffusersLoader": {"input":{"required":{"model_path":[["SDXL-Turbo","stable-diffusion-v1-5"],{}]}}}
	}`))
	if len(idx) != 0 {
		t.Errorf("extensionless choices must not be indexed, got %v", idx)
	}
}

// A NUMERIC combo (RIFE VFI's scale_factor is the real one) leaves Choices nil by
// design — the index must simply skip it rather than materialise phantom keys.
func TestModelFileChoicesIgnoresNumericCombos(t *testing.T) {
	info := buildInfo(t, `{
		"RIFE VFI": {"input":{"required":{"scale_factor":[[0.25,0.5,1.0],{}]}}}
	}`)
	if spec := info["RIFE VFI"].Input.Required["scale_factor"]; !spec.IsCombo || spec.Choices != nil {
		t.Fatalf("fixture precondition: want IsCombo=true Choices=nil, got IsCombo=%v Choices=%#v",
			spec.IsCombo, spec.Choices)
	}
	if idx := ModelFileChoices(info); len(idx) != 0 {
		t.Errorf("a numeric combo must contribute nothing, got %v", idx)
	}
}
