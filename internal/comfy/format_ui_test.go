package comfy

import (
	"encoding/json"
	"reflect"
	"testing"
)

// A representative ui-format graph: a checkpoint loader node, a lora loader node,
// a vae loader node, plus non-model widget strings and numbers that must be
// ignored.
const uiTestGraph = `{
  "nodes": [
    {"type": "CheckpointLoaderSimple", "widgets_values": ["sdxl_base.safetensors"]},
    {"type": "LoraLoader", "widgets_values": ["detail.safetensors", 0.8, 1.0]},
    {"type": "VAELoader", "widgets_values": ["sdxl.vae.pt"]},
    {"type": "CLIPTextEncode", "widgets_values": ["a beautiful landscape, masterpiece"]},
    {"type": "EmptyLatentImage", "widgets_values": [1024, 1024, 1]}
  ]
}`

func TestExtractResourcesUI(t *testing.T) {
	got, err := ExtractResourcesAny(FormatUI, json.RawMessage(uiTestGraph))
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	want := []string{"sdxl_base.safetensors", "detail.safetensors", "sdxl.vae.pt"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("resources = %v, want %v (prompt string + numbers must be ignored)", got, want)
	}
}

func TestExtractResourcesAnyAPI(t *testing.T) {
	api := `{"3":{"class_type":"CheckpointLoaderSimple","inputs":{"ckpt_name":"foo.ckpt"}}}`
	got, err := ExtractResourcesAny(FormatAPI, json.RawMessage(api))
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if len(got) != 1 || got[0] != "foo.ckpt" {
		t.Errorf("api resources = %v, want [foo.ckpt]", got)
	}
}

func TestExtractResourcesAnyUnknownFormat(t *testing.T) {
	if _, err := ExtractResourcesAny("xml", json.RawMessage(`{}`)); err == nil {
		t.Error("unknown format should error")
	}
}

func TestPrimaryCheckpointUI(t *testing.T) {
	got, ok := PrimaryCheckpoint(FormatUI, json.RawMessage(uiTestGraph))
	if !ok || got != "sdxl_base.safetensors" {
		t.Errorf("ui primary checkpoint = %q ok=%v, want sdxl_base.safetensors", got, ok)
	}
}

func TestPrimaryCheckpointAPI(t *testing.T) {
	api := `{
      "3":{"class_type":"CheckpointLoaderSimple","inputs":{"ckpt_name":"dream.safetensors"}},
      "5":{"class_type":"LoraLoader","inputs":{"lora_name":"x.safetensors"}}
    }`
	got, ok := PrimaryCheckpoint(FormatAPI, json.RawMessage(api))
	if !ok || got != "dream.safetensors" {
		t.Errorf("api primary checkpoint = %q ok=%v, want dream.safetensors", got, ok)
	}
}

func TestPrimaryCheckpointNone(t *testing.T) {
	// A graph with no checkpoint loader yields ok=false.
	ui := `{"nodes":[{"type":"LoraLoader","widgets_values":["x.safetensors"]}]}`
	if _, ok := PrimaryCheckpoint(FormatUI, json.RawMessage(ui)); ok {
		t.Error("expected no primary checkpoint")
	}
}
