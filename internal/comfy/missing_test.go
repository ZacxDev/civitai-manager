package comfy

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestCleanModelQuery(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"seg-a/fabricatedXL_v70.safetensors", "fabricated XL"},
		{"fabricatedXL_v70.safetensors", "fabricated XL"},
		{"sd_xl_base_1.0.safetensors", "sd xl base"},
		{"detail_tweaker_xl.safetensors", "detail tweaker xl"},
		{"vae-ft-mse-840000-ema-pruned.safetensors", "vae ft mse 840000 ema pruned"},
		{"add_detail.safetensors", "add detail"},
		{"epicRealism_naturalSinRC1VAE.safetensors", "epic Realism natural Sin RC1 VAE"},
		{"models/loras/GoodHands-beta2.pt", "Good Hands beta2"},
		{"foo.ckpt", "foo"},
		{"", ""},
	}
	for _, c := range cases {
		if got := CleanModelQuery(c.in); got != c.want {
			t.Errorf("CleanModelQuery(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestInferCivitaiType(t *testing.T) {
	cases := []struct {
		inputName, classType, want string
	}{
		{"ckpt_name", "CheckpointLoaderSimple", "Checkpoint"},
		{"unet_name", "UNETLoader", "Checkpoint"},
		{"lora_name", "LoraLoader", "LORA"},
		{"vae_name", "VAELoader", "VAE"},
		{"control_net_name", "ControlNetLoader", "Controlnet"},
		{"embedding_name", "SomeEmbeddingLoader", "TextualInversion"},
		// Fall back to class_type when the input name is unknown.
		{"", "CheckpointLoaderSimple", "Checkpoint"},
		{"model", "ControlNetLoader", "Controlnet"},
		{"clip_name", "CLIPLoader", ""},
		{"", "UpscaleModelLoader", ""},
		{"", "", ""},
	}
	for _, c := range cases {
		if got := InferCivitaiType(c.inputName, c.classType); got != c.want {
			t.Errorf("InferCivitaiType(%q,%q) = %q, want %q", c.inputName, c.classType, got, c.want)
		}
	}
}

// missingMapObjectInfo has a checkpoint, a lora, and a vae loader, each with a
// combo choices list of installed files.
const missingMapObjectInfo = `{
  "CheckpointLoaderSimple": {"input":{"required":{"ckpt_name":[["haveA.safetensors","haveB.safetensors"],{}]}},"input_order":{"required":["ckpt_name"]}},
  "LoraLoader": {"input":{"required":{"lora_name":[["seg-a/loraX.safetensors","loraY.safetensors"],{}]}},"input_order":{"required":["lora_name"]}},
  "VAELoader": {"input":{"required":{"vae_name":[["vaeA.safetensors"],{}]}},"input_order":{"required":["vae_name"]}}
}`

func TestMapMissingModels(t *testing.T) {
	graph := json.RawMessage(`{
	  "1": {"class_type":"CheckpointLoaderSimple","inputs":{"ckpt_name":"missingCkpt.safetensors"}},
	  "2": {"class_type":"LoraLoader","inputs":{"lora_name":"missingLora.safetensors","strength_model":1.0}},
	  "3": {"class_type":"LoraLoader","inputs":{"lora_name":"missingLora.safetensors"}},
	  "4": {"class_type":"VAELoader","inputs":{"vae_name":"missingVae.safetensors"}}
	}`)
	info := mustOI(t, missingMapObjectInfo)

	refs := MapMissingModels(graph, info, []string{"missingCkpt.safetensors", "missingLora.safetensors", "missingVae.safetensors"})
	if len(refs) != 3 {
		t.Fatalf("got %d refs, want 3", len(refs))
	}

	// Checkpoint ref.
	if refs[0].InputName != "ckpt_name" || refs[0].ClassType != "CheckpointLoaderSimple" || refs[0].CivitaiType != "Checkpoint" {
		t.Errorf("ckpt ref = %+v", refs[0])
	}
	if !reflect.DeepEqual(refs[0].NodeIDs, []string{"1"}) {
		t.Errorf("ckpt NodeIDs = %v, want [1]", refs[0].NodeIDs)
	}
	if !reflect.DeepEqual(refs[0].Candidates, []string{"haveA.safetensors", "haveB.safetensors"}) {
		t.Errorf("ckpt candidates = %v", refs[0].Candidates)
	}

	// Lora ref: referenced by nodes 2 AND 3 (same filename on two nodes).
	if refs[1].CivitaiType != "LORA" {
		t.Errorf("lora type = %q", refs[1].CivitaiType)
	}
	if !reflect.DeepEqual(refs[1].NodeIDs, []string{"2", "3"}) {
		t.Errorf("lora NodeIDs = %v, want [2 3]", refs[1].NodeIDs)
	}
	// Candidates keep the exact choice strings including subfolder prefixes.
	if !reflect.DeepEqual(refs[1].Candidates, []string{"seg-a/loraX.safetensors", "loraY.safetensors"}) {
		t.Errorf("lora candidates = %v", refs[1].Candidates)
	}

	// VAE ref.
	if refs[2].CivitaiType != "VAE" || !reflect.DeepEqual(refs[2].NodeIDs, []string{"4"}) {
		t.Errorf("vae ref = %+v", refs[2])
	}
}

func TestAnalyzeMissingModelsBaseOrdering(t *testing.T) {
	// A checkpoint loader whose installed choices span two base-model families.
	info := mustOI(t, `{"CheckpointLoaderSimple":{"input":{"required":{"ckpt_name":[["ponyRealism_v22.safetensors","juggernautXL_v9.safetensors","dreamshaper_8.safetensors"],{}]}},"input_order":{"required":["ckpt_name"]}}}`)
	graph := json.RawMessage(`{"1":{"class_type":"CheckpointLoaderSimple","inputs":{"ckpt_name":"absent.safetensors"}}}`)

	// With an XL base model, the XL candidate is surfaced first (SameBase); the
	// others land in OtherCandidates.
	got := AnalyzeMissingModels(graph, info, []string{"absent.safetensors"}, "SDXL 1.0")
	if len(got) != 1 {
		t.Fatalf("got %d, want 1", len(got))
	}
	mm := got[0]
	if mm.Query != "absent" || mm.CivitaiType != "Checkpoint" {
		t.Errorf("query/type = %q/%q", mm.Query, mm.CivitaiType)
	}
	if !reflect.DeepEqual(mm.SameBase, []string{"juggernautXL_v9.safetensors"}) {
		t.Errorf("SameBase = %v, want [juggernautXL_v9.safetensors]", mm.SameBase)
	}
	if !reflect.DeepEqual(mm.OtherCandidates, []string{"ponyRealism_v22.safetensors", "dreamshaper_8.safetensors"}) {
		t.Errorf("OtherCandidates = %v", mm.OtherCandidates)
	}

	// With NO base model, all candidates are Other (no split).
	none := AnalyzeMissingModels(graph, info, []string{"absent.safetensors"}, "")
	if len(none[0].SameBase) != 0 || len(none[0].OtherCandidates) != 3 {
		t.Errorf("no-base split wrong: same=%v other=%v", none[0].SameBase, none[0].OtherCandidates)
	}
}

func TestApplySubstitutions(t *testing.T) {
	// The missing lora is referenced on TWO loader nodes; a KSampler carries the
	// same-looking string that must NOT be swapped (not a loader). _meta preserved.
	graph := json.RawMessage(`{
	  "1": {"class_type":"CheckpointLoaderSimple","inputs":{"ckpt_name":"keep.safetensors"},"_meta":{"title":"Load Checkpoint"}},
	  "2": {"class_type":"LoraLoader","inputs":{"lora_name":"missing.safetensors","strength_model":1.0}},
	  "3": {"class_type":"LoraLoader","inputs":{"lora_name":"missing.safetensors"}},
	  "4": {"class_type":"KSampler","inputs":{"sampler_name":"missing.safetensors"}}
	}`)

	out := ApplySubstitutions(graph, map[string]string{"missing.safetensors": "have.safetensors"})

	var nodes map[string]struct {
		ClassType string                     `json:"class_type"`
		Inputs    map[string]json.RawMessage `json:"inputs"`
		Meta      json.RawMessage            `json:"_meta"`
	}
	if err := json.Unmarshal(out, &nodes); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	str := func(id, key string) string {
		var s string
		_ = json.Unmarshal(nodes[id].Inputs[key], &s)
		return s
	}
	// Both loader nodes swapped.
	if str("2", "lora_name") != "have.safetensors" || str("3", "lora_name") != "have.safetensors" {
		t.Errorf("loader nodes not both swapped: 2=%q 3=%q", str("2", "lora_name"), str("3", "lora_name"))
	}
	// Untouched loader input preserved.
	if str("1", "ckpt_name") != "keep.safetensors" {
		t.Errorf("unrelated loader input changed: %q", str("1", "ckpt_name"))
	}
	// Non-loader node left alone.
	if str("4", "sampler_name") != "missing.safetensors" {
		t.Errorf("non-loader node was swapped: %q", str("4", "sampler_name"))
	}
	// _meta preserved on the swapped node's node object.
	if string(nodes["1"].Meta) == "" {
		t.Errorf("_meta dropped from node 1")
	}

	// Empty sub → identical bytes returned (no reserialization).
	same := ApplySubstitutions(graph, nil)
	if string(same) != string(graph) {
		t.Errorf("empty sub should return input unchanged")
	}
}

func mustOI(t *testing.T, raw string) ObjectInfo {
	t.Helper()
	var oi ObjectInfo
	if err := json.Unmarshal([]byte(raw), &oi); err != nil {
		t.Fatalf("parse object_info: %v", err)
	}
	return oi
}
