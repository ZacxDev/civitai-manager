package store

import (
	"testing"
)

// seedLinkedFile records a local file linked to a model plus the model's cached detail.
func seedLinkedFile(t *testing.T, s *Store, path string, modelID, versionID int, raw string) {
	t.Helper()
	if err := s.UpsertLocalFile(LocalFile{
		Path: path, ModelID: intp(modelID), VersionID: intp(versionID),
		Kind: LocalKindModel, Status: LocalStatusMatched,
	}); err != nil {
		t.Fatalf("upsert %s: %v", path, err)
	}
	if raw != "" {
		if err := s.PutModelCache(modelID, "cache-name", []byte(raw)); err != nil {
			t.Fatalf("put model cache %d: %v", modelID, err)
		}
	}
}

const metaRaw = `{"id":42,"name":"Hassaku XL","modelVersions":[
	{"id":100,"baseModel":"Pony","images":[{"url":"https://image.civitai.com/a/pony.jpeg","nsfwLevel":1}]},
	{"id":200,"baseModel":"Illustrious","images":[{"url":"https://image.civitai.com/a/ill.jpeg","nsfwLevel":4}]}
]}`

func TestLocalModelMetaByBasenamesMatched(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Linked to model 42, version 200 (Illustrious).
	seedLinkedFile(t, s, "/models/checkpoints/hassakuXL_v30.safetensors", 42, 200, metaRaw)

	got, err := s.LocalModelMetaByBasenames([]string{"seg-a/hassakuXL_v30.safetensors"})
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	m, ok := got["hassakuxl_v30.safetensors"]
	if !ok {
		t.Fatalf("expected a match keyed by lowercased basename; got %v", got)
	}
	// Name comes from the model_cache.name column.
	if m.Name != "cache-name" {
		t.Errorf("name = %q, want cache-name", m.Name)
	}
	// baseModel + image come from the LINKED version (200 → Illustrious).
	if m.BaseModel != "Illustrious" {
		t.Errorf("baseModel = %q, want Illustrious (linked version)", m.BaseModel)
	}
	if m.ImageURL != "https://image.civitai.com/a/ill.jpeg" {
		t.Errorf("imageURL = %q, want linked-version image", m.ImageURL)
	}
	if !m.NSFWLevelKnown || m.NSFWLevel != 4 {
		t.Errorf("nsfwLevel = %d known=%v, want 4/true", m.NSFWLevel, m.NSFWLevelKnown)
	}
	if m.ModelID != 42 || m.VersionID != 200 {
		t.Errorf("linkage = model %d ver %d, want 42/200", m.ModelID, m.VersionID)
	}
}

func TestLocalModelMetaByBasenamesUnmatched(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// A file with NO civitai linkage (model_id NULL).
	if err := s.UpsertLocalFile(LocalFile{Path: "/x/orphan.safetensors", Kind: LocalKindModel}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	// A linked file whose model has NO cached detail row.
	if err := s.UpsertLocalFile(LocalFile{
		Path: "/x/nocache.safetensors", ModelID: intp(7), Kind: LocalKindModel, Status: LocalStatusMatched,
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	got, err := s.LocalModelMetaByBasenames([]string{
		"orphan.safetensors", "nocache.safetensors", "totally-absent.safetensors",
	})
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("unmatched/uncached/absent basenames must be absent from result; got %v", got)
	}
}

func TestLocalModelMetaByBasenamesCollision(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Two files share a basename but link to DIFFERENT models → ambiguous → dropped.
	seedLinkedFile(t, s, "/a/dup.safetensors", 42, 100, metaRaw)
	seedLinkedFile(t, s, "/b/dup.safetensors", 99, 0, `{"id":99,"name":"Other","modelVersions":[{"id":1,"baseModel":"SDXL 1.0","images":[]}]}`)

	got, err := s.LocalModelMetaByBasenames([]string{"dup.safetensors"})
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if _, ok := got["dup.safetensors"]; ok {
		t.Errorf("ambiguous basename (two models) must be dropped; got %v", got)
	}
}

func TestLocalModelMetaByBasenamesEmpty(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	got, err := s.LocalModelMetaByBasenames(nil)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if got == nil || len(got) != 0 {
		t.Errorf("empty input → empty non-nil map, got %v", got)
	}
}
