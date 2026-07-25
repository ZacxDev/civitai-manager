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
