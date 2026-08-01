package comfy

import (
	"encoding/json"
	"testing"
)

func TestPreflightMissingNode(t *testing.T) {
	info := buildInfo(t, `{
		"CheckpointLoaderSimple": {"input":{"required":{"ckpt_name":[["a.safetensors"],{}]}},"input_order":{"required":["ckpt_name"]}}
	}`)
	api := json.RawMessage(`{
		"4":{"class_type":"CheckpointLoaderSimple","inputs":{"ckpt_name":"a.safetensors"}},
		"5":{"class_type":"SomeCustomNode","inputs":{}}
	}`)
	rep := Preflight(api, info, func(string) bool { return true })
	if rep.OK {
		t.Error("report should not be OK (a node is missing)")
	}
	if len(rep.MissingNodes) != 1 || rep.MissingNodes[0] != "SomeCustomNode" {
		t.Errorf("MissingNodes = %v", rep.MissingNodes)
	}
}

func TestPreflightModelInChoicesOK(t *testing.T) {
	// The referenced checkpoint is present in the loader's object_info choices → OK,
	// even with no local library.
	info := buildInfo(t, `{
		"CheckpointLoaderSimple": {"input":{"required":{"ckpt_name":[["a.safetensors","b.safetensors"],{}]}},"input_order":{"required":["ckpt_name"]}}
	}`)
	api := json.RawMessage(`{"4":{"class_type":"CheckpointLoaderSimple","inputs":{"ckpt_name":"b.safetensors"}}}`)
	rep := Preflight(api, info, nil)
	if !rep.OK {
		t.Errorf("report should be OK: %+v", rep)
	}
	if len(rep.MissingModels) != 0 {
		t.Errorf("MissingModels = %v", rep.MissingModels)
	}
}

func TestPreflightModelViaLocalHaveOK(t *testing.T) {
	// The referenced checkpoint is NOT in choices but IS in the local library.
	info := buildInfo(t, `{
		"CheckpointLoaderSimple": {"input":{"required":{"ckpt_name":[["other.safetensors"],{}]}},"input_order":{"required":["ckpt_name"]}}
	}`)
	api := json.RawMessage(`{"4":{"class_type":"CheckpointLoaderSimple","inputs":{"ckpt_name":"mine.safetensors"}}}`)
	rep := Preflight(api, info, func(name string) bool { return name == "mine.safetensors" })
	if !rep.OK {
		t.Errorf("report should be OK via localHave: %+v", rep)
	}
}

func TestPreflightModelMissingEverywhere(t *testing.T) {
	info := buildInfo(t, `{
		"CheckpointLoaderSimple": {"input":{"required":{"ckpt_name":[["other.safetensors"],{}]}},"input_order":{"required":["ckpt_name"]}}
	}`)
	api := json.RawMessage(`{"4":{"class_type":"CheckpointLoaderSimple","inputs":{"ckpt_name":"absent.safetensors"}}}`)
	rep := Preflight(api, info, func(string) bool { return false })
	if rep.OK {
		t.Error("report should not be OK (model missing everywhere)")
	}
	if len(rep.MissingModels) != 1 || rep.MissingModels[0] != "absent.safetensors" {
		t.Errorf("MissingModels = %v", rep.MissingModels)
	}
}

// --- ChoicesContain (exported) ------------------------------------------------

func TestChoicesContainExactMatch(t *testing.T) {
	info := buildInfo(t, `{
		"CheckpointLoaderSimple": {"input":{"required":{"ckpt_name":[["model.safetensors","other.pt"],{}]}},"input_order":{"required":["ckpt_name"]}}
	}`)
	if !ChoicesContain(info["CheckpointLoaderSimple"], "model.safetensors") {
		t.Error("expected exact match to succeed")
	}
}

func TestChoicesContainBasenameMatch(t *testing.T) {
	info := buildInfo(t, `{
		"CheckpointLoaderSimple": {"input":{"required":{"ckpt_name":[["flux/flux1-dev.safetensors"],{}]}},"input_order":{"required":["ckpt_name"]}}
	}`)
	// A basename-only reference should match a subdirectory-prefixed choice.
	if !ChoicesContain(info["CheckpointLoaderSimple"], "flux1-dev.safetensors") {
		t.Error("expected basename match to succeed")
	}
}

func TestChoicesContainSubdirBasenameMatch(t *testing.T) {
	info := buildInfo(t, `{
		"Loader": {"input":{"required":{"model":[["model.safetensors"],{}]}},"input_order":{"required":["model"]}}
	}`)
	// A subdirectory-qualified reference should match a plain basename choice.
	if !ChoicesContain(info["Loader"], "subdir/model.safetensors") {
		t.Error("expected subdirectory basename match to succeed")
	}
}

func TestChoicesContainMissing(t *testing.T) {
	info := buildInfo(t, `{
		"Loader": {"input":{"required":{"ckpt_name":[["a.safetensors"],{}]}},"input_order":{"required":["ckpt_name"]}}
	}`)
	if ChoicesContain(info["Loader"], "absent.safetensors") {
		t.Error("expected missing file to return false")
	}
}

func TestChoicesContainOptional(t *testing.T) {
	info := buildInfo(t, `{
		"Loader": {"input":{"optional":{"lora_name":[["style_lora.safetensors"],{}]}},"input_order":{"optional":["lora_name"]}}
	}`)
	if !ChoicesContain(info["Loader"], "style_lora.safetensors") {
		t.Error("expected optional input choices to be searched")
	}
}

func TestChoicesContainEmptyChoices(t *testing.T) {
	info := buildInfo(t, `{
		"Loader": {"input":{"required":{"model":[[],{}]}},"input_order":{"required":["model"]}}
	}`)
	if ChoicesContain(info["Loader"], "anything.pt") {
		t.Error("empty choices should return false")
	}
}

func TestChoicesContainNonComboInput(t *testing.T) {
	// A non-combo (primitive type) input — "steps" is an INT, not a list of
	// choices. ChoicesContain should never match against a type name.
	info := buildInfo(t, `{
		"KSampler": {"input":{"required":{"steps":["INT",{"default":20,"min":1,"max":100}]},"input_order":{"required":["steps"]}}}}`)
	sch, ok := info["KSampler"]
	if !ok {
		t.Fatal("KSampler not found in object_info")
	}
	spec, ok := sch.Input.Required["steps"]
	if !ok {
		t.Fatal("steps not found in KSampler required inputs")
	}
	if spec.IsCombo {
		t.Fatal("steps should NOT be a combo input")
	}
	if ChoicesContain(sch, "INT") {
		t.Error("non-combo input should not match a type name")
	}
}
