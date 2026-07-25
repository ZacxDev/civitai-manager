package web

import (
	"strings"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/store"
)

// matchedModelNames resolves cached display names (cache-only); uncached ids are
// absent/empty so the card falls back to the "Model #id" placeholder.
func TestMatchedModelNames(t *testing.T) {
	srv := newTestServer(t)
	if err := srv.store.PutModelCache(100, "Dreamshaper XL", []byte(`{"id":100,"name":"Dreamshaper XL"}`)); err != nil {
		t.Fatalf("seed cache: %v", err)
	}
	v := libraryView{Files: []store.LocalFile{
		{ModelID: intp(100)}, {ModelID: intp(100)}, // duplicate id → resolved once
		{ModelID: intp(200)},            // uncached
		{ModelID: nil, SHA256: "loose"}, // unmatched → skipped
	}}

	names := srv.matchedModelNames(v)
	if names[100] != "Dreamshaper XL" {
		t.Errorf("names[100] = %q, want cached name", names[100])
	}
	if names[200] != "" {
		t.Errorf("names[200] = %q, want empty (uncached)", names[200])
	}
}

// modelCardLazy shows the cached name immediately when provided, else the
// "Model #id" placeholder (which the lazy full-card load then replaces).
func TestModelCardLazyName(t *testing.T) {
	gr := fileGroup{modelID: 42, files: []store.LocalFile{{SizeBytes: 1024}}}

	withName := renderString(t, modelCardLazy(gr, "My Cool Model"))
	if !strings.Contains(withName, "My Cool Model") {
		t.Errorf("card missing the resolved name:\n%s", withName)
	}
	if strings.Contains(withName, "Model #42") {
		t.Errorf("card should not show the placeholder when a name is known")
	}

	placeholder := renderString(t, modelCardLazy(gr, ""))
	if !strings.Contains(placeholder, "Model #42") {
		t.Errorf("card missing the placeholder when name is empty:\n%s", placeholder)
	}
	// The lazy full-card load must still be wired in both cases.
	if !strings.Contains(placeholder, "/library/model-card/42") {
		t.Errorf("card missing the lazy detail load")
	}
}

// A cached name containing markup is escaped when rendered on a matched card.
func TestModelCardLazyEscapesName(t *testing.T) {
	gr := fileGroup{modelID: 7, files: []store.LocalFile{{SizeBytes: 1}}}
	out := renderString(t, modelCardLazy(gr, "<script>alert(1)</script>"))
	if strings.Contains(out, "<script>alert(1)</script>") {
		t.Errorf("model name not escaped:\n%s", out)
	}
	if !strings.Contains(out, "&lt;script&gt;") {
		t.Errorf("expected escaped name, got:\n%s", out)
	}
}
