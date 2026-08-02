package comfy

import (
	"encoding/json"
	"testing"
)

// fakeLookup is an in-memory ResourceLookup for resolver tests.
type fakeLookup struct {
	byBasename map[string]*LocalMatch
	models     map[[2]int][2]string // {modelID,versionID} -> {modelType, baseModel}
}

func (f *fakeLookup) LocalFileByBasename(basename string) (*LocalMatch, error) {
	return f.byBasename[basename], nil
}

func (f *fakeLookup) ModelTypeBaseModel(modelID, versionID int) (string, string, bool) {
	v, ok := f.models[[2]int{modelID, versionID}]
	if !ok {
		return "", "", false
	}
	return v[0], v[1], true
}

func findRes(rs []ResolvedResource, filename string) (ResolvedResource, bool) {
	for _, r := range rs {
		if r.Filename == filename {
			return r, true
		}
	}
	return ResolvedResource{}, false
}

func TestResolveResources_AllPaths(t *testing.T) {
	graph := json.RawMessage(`{
		"1": {"class_type":"CheckpointLoaderSimple","inputs":{"ckpt_name":"good.safetensors"}},
		"2": {"class_type":"LoraLoader","inputs":{"lora_name":"guess.safetensors"}},
		"3": {"class_type":"VAELoader","inputs":{"vae_name":"missing.safetensors"}},
		"4": {"class_type":"CheckpointLoaderSimple","inputs":{"ckpt_name":"nocache.safetensors"}},
		"5": {"class_type":"KSampler","inputs":{}},
		"6": {"class_type":"MyCustomSamplerNode","inputs":{}},
		"7": {"class_type":"ImpactWildcardProcessor","inputs":{}}
	}`)

	lk := &fakeLookup{
		byBasename: map[string]*LocalMatch{
			"good.safetensors":    {ModelID: 10, VersionID: 20},
			"guess.safetensors":   {ModelID: 11, VersionID: 21},
			"nocache.safetensors": {ModelID: 12, VersionID: 22},
			// missing.safetensors intentionally absent (→ unresolved)
		},
		models: map[[2]int][2]string{
			{10, 20}: {"Checkpoint", "SDXL 1.0"},    // known ecosystem → resolved
			{11, 21}: {"LORA", "Some Future Model"}, // unknown ecosystem → guessed
			// {12,22} absent → no cache → unresolved
		},
	}

	rs, err := ResolveResources(graph, lk, nil)
	if err != nil {
		t.Fatalf("ResolveResources: %v", err)
	}

	// Resolved.
	if r, ok := findRes(rs, "good.safetensors"); !ok || r.Status != ResolveResolved ||
		r.URN != "urn:air:sdxl:checkpoint:civitai:10@20" {
		t.Errorf("good: %+v ok=%v", r, ok)
	}
	// Guessed ecosystem.
	if r, ok := findRes(rs, "guess.safetensors"); !ok || r.Status != ResolveGuessed ||
		r.URN != "urn:air:somefuturemodel:lora:civitai:11@21" {
		t.Errorf("guess: %+v ok=%v", r, ok)
	}
	// Missing from library → unresolved, empty URN.
	if r, ok := findRes(rs, "missing.safetensors"); !ok || r.Status != ResolveUnresolved || r.URN != "" {
		t.Errorf("missing: %+v ok=%v", r, ok)
	}
	// In library but no cache → unresolved.
	if r, ok := findRes(rs, "nocache.safetensors"); !ok || r.Status != ResolveUnresolved || r.URN != "" {
		t.Errorf("nocache: %+v ok=%v", r, ok)
	}
	// Custom nodes (class_types outside core set), empty URN.
	for _, ct := range []string{"MyCustomSamplerNode", "ImpactWildcardProcessor"} {
		if r, ok := findRes(rs, ct); !ok || r.Status != ResolveCustomNode || r.URN != "" {
			t.Errorf("custom node %s: %+v ok=%v", ct, r, ok)
		}
	}
	// Core nodes must NOT be listed as custom.
	if _, ok := findRes(rs, "KSampler"); ok {
		t.Errorf("KSampler (core) should not be a custom-node row")
	}
	if _, ok := findRes(rs, "CheckpointLoaderSimple"); ok {
		t.Errorf("CheckpointLoaderSimple (core) should not be a custom-node row")
	}
}

func TestResolveResources_KnownEcosystemUnknownTypeIsGuessed(t *testing.T) {
	// A known ecosystem (SDXL 1.0) but an unmapped ModelType yields a ":unknown:"
	// type segment the orchestrator rejects — it must degrade to guessed (never a
	// green resolved ✓), and the URN must carry the unknown type verbatim.
	graph := json.RawMessage(`{"1":{"class_type":"CheckpointLoaderSimple","inputs":{"ckpt_name":"weird.safetensors"}}}`)
	lk := &fakeLookup{
		byBasename: map[string]*LocalMatch{"weird.safetensors": {ModelID: 30, VersionID: 40}},
		models:     map[[2]int][2]string{{30, 40}: {"Poses", "SDXL 1.0"}},
	}
	rs, err := ResolveResources(graph, lk, nil)
	if err != nil {
		t.Fatalf("ResolveResources: %v", err)
	}
	if r, ok := findRes(rs, "weird.safetensors"); !ok || r.Status != ResolveGuessed ||
		r.URN != "urn:air:sdxl:unknown:civitai:30@40" {
		t.Errorf("known-eco+unknown-type should be guessed with :unknown: URN: %+v ok=%v", r, ok)
	}
}

func TestResolveResources_AmbiguousIsUnresolved(t *testing.T) {
	// The store method returns nil for an ambiguous basename; resolve then treats
	// it as unresolved. Simulate by having the lookup return nil for the basename.
	graph := json.RawMessage(`{"1":{"class_type":"LoraLoader","inputs":{"lora_name":"ambig.safetensors"}}}`)
	lk := &fakeLookup{byBasename: map[string]*LocalMatch{ /* ambig absent → nil */ }}
	rs, _ := ResolveResources(graph, lk, nil)
	if r, ok := findRes(rs, "ambig.safetensors"); !ok || r.Status != ResolveUnresolved {
		t.Errorf("ambiguous should be unresolved: %+v ok=%v", r, ok)
	}
}

func TestResolveResources_MatchWithoutLinkage(t *testing.T) {
	// A local file matched but with ModelID/VersionID 0 (unlinked) → unresolved.
	graph := json.RawMessage(`{"1":{"class_type":"CheckpointLoaderSimple","inputs":{"ckpt_name":"unlinked.ckpt"}}}`)
	lk := &fakeLookup{byBasename: map[string]*LocalMatch{"unlinked.ckpt": {ModelID: 0, VersionID: 0}}}
	rs, _ := ResolveResources(graph, lk, nil)
	if r, _ := findRes(rs, "unlinked.ckpt"); r.Status != ResolveUnresolved {
		t.Errorf("unlinked should be unresolved: %+v", r)
	}
}

func TestResolveResources_NilLookup(t *testing.T) {
	graph := json.RawMessage(`{"1":{"class_type":"CheckpointLoaderSimple","inputs":{"ckpt_name":"x.safetensors"}}}`)
	rs, err := ResolveResources(graph, nil, nil)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if r, _ := findRes(rs, "x.safetensors"); r.Status != ResolveUnresolved {
		t.Errorf("nil lookup should yield unresolved: %+v", r)
	}
}

func TestResolveResources_BasenameFromPath(t *testing.T) {
	// A ref carrying a subdir prefix resolves by basename.
	graph := json.RawMessage(`{"1":{"class_type":"CheckpointLoaderSimple","inputs":{"ckpt_name":"sdxl/model.safetensors"}}}`)
	lk := &fakeLookup{
		byBasename: map[string]*LocalMatch{"model.safetensors": {ModelID: 5, VersionID: 6}},
		models:     map[[2]int][2]string{{5, 6}: {"Checkpoint", "SDXL 1.0"}},
	}
	rs, _ := ResolveResources(graph, lk, nil)
	if r, _ := findRes(rs, "sdxl/model.safetensors"); r.Status != ResolveResolved ||
		r.URN != "urn:air:sdxl:checkpoint:civitai:5@6" {
		t.Errorf("basename resolution: %+v", r)
	}
}

func TestResolveResources_Unparseable(t *testing.T) {
	rs, err := ResolveResources(json.RawMessage(`not json`), &fakeLookup{}, nil)
	if err != nil {
		t.Fatalf("should not error on bad graph: %v", err)
	}
	if len(rs) != 0 {
		t.Errorf("bad graph should yield no resources, got %v", rs)
	}
}
