package store

import (
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/comfy"
)

// *Store must satisfy comfy.NodePackCache so the resolver can be wired to the
// real database without an adapter. The interface lives in internal/comfy (which
// deliberately does NOT import this package); this compile-time assertion is what
// keeps the two halves from drifting apart silently. It is in a _test.go file so
// no production dependency is created in either direction.
var _ comfy.NodePackCache = (*Store)(nil)

// TestNodePackRawAdapter pins the flat adapter form the resolver consumes: a
// MISS must be (nil, zero, nil) and NOT an error, or the resolver would treat
// every cold cache as a broken cache.
func TestNodePackRawAdapter(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	raw, at, err := st.GetNodePackRaw(NodePackSourceExtensionNodeMap)
	if err != nil {
		t.Fatalf("a cache miss must not be an error: %v", err)
	}
	if raw != nil || !at.IsZero() {
		t.Fatalf("miss = (%v, %v), want (nil, zero)", raw, at)
	}

	if err := st.PutNodePackRaw(NodePackSourceExtensionNodeMap, []byte(`{"a":1}`)); err != nil {
		t.Fatal(err)
	}
	raw, at, err = st.GetNodePackRaw(NodePackSourceExtensionNodeMap)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"a":1}` {
		t.Errorf("raw = %s", raw)
	}
	if at.IsZero() {
		t.Error("fetched_at must be set so the resolver can judge freshness")
	}
	// The keys this package writes must be the keys the resolver reads.
	if NodePackSourceExtensionNodeMap != "extension-node-map" {
		t.Errorf("static-index key drifted: %q", NodePackSourceExtensionNodeMap)
	}
	if got := NodePackSourceRegistryClass("MMAudioSampler"); got != "registry:MMAudioSampler" {
		t.Errorf("registry key drifted: %q", got)
	}
}

func TestNodePackCacheMissThenHit(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	// Miss: no entry yet.
	if got, err := st.GetNodePackCache(NodePackSourceExtensionNodeMap); err != nil || got != nil {
		t.Fatalf("expected (nil,nil) miss, got (%v,%v)", got, err)
	}

	raw := []byte(`{"https://github.com/melMass/comfy_mtb":[["Pick From Batch (mtb)"],{"title_aux":"MTB Nodes"}]}`)
	if err := st.PutNodePackCache(NodePackSourceExtensionNodeMap, raw); err != nil {
		t.Fatalf("put: %v", err)
	}

	got, err := st.GetNodePackCache(NodePackSourceExtensionNodeMap)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got == nil {
		t.Fatal("expected a cache hit after put")
	}
	if got.Source != NodePackSourceExtensionNodeMap || string(got.Raw) != string(raw) {
		t.Fatalf("entry mismatch: %+v", got)
	}
	if got.FetchedAt.IsZero() {
		t.Fatal("fetched_at should be set")
	}
	// A different source is a distinct key (miss).
	if other, err := st.GetNodePackCache(NodePackSourceRegistryClass("MMAudioSampler")); err != nil || other != nil {
		t.Fatalf("expected a miss for a different source, got (%v,%v)", other, err)
	}
}

func TestNodePackCacheUpsertReplaces(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	src := NodePackSourceRegistryClass("MMAudioSampler")
	if err := st.PutNodePackCache(src, []byte(`{"id":"old"}`)); err != nil {
		t.Fatal(err)
	}
	if err := st.PutNodePackCache(src, []byte(`{"id":"comfyui-mmaudio"}`)); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetNodePackCache(src)
	if err != nil || got == nil {
		t.Fatalf("get: %v %v", got, err)
	}
	if string(got.Raw) != `{"id":"comfyui-mmaudio"}` {
		t.Fatalf("upsert did not replace: %+v", got)
	}
}

// TestNodePackCacheRegistryKeys: per-class Registry rows must not collide, and a
// class with spaces or punctuation must round-trip as its own key.
func TestNodePackCacheRegistryKeys(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	classes := []string{
		"MMAudioSampler",
		"CR Float To Integer",
		"Pick From Batch (mtb)",
		"Note Plus (mtb)",
	}
	for i, cls := range classes {
		body := []byte(`{"n":` + string(rune('0'+i)) + `}`)
		if err := st.PutNodePackCache(NodePackSourceRegistryClass(cls), body); err != nil {
			t.Fatalf("put %q: %v", cls, err)
		}
	}
	for i, cls := range classes {
		got, err := st.GetNodePackCache(NodePackSourceRegistryClass(cls))
		if err != nil || got == nil {
			t.Fatalf("get %q: %v %v", cls, got, err)
		}
		want := `{"n":` + string(rune('0'+i)) + `}`
		if string(got.Raw) != want {
			t.Errorf("class %q: raw = %s, want %s", cls, got.Raw, want)
		}
	}
	// The static index key must never collide with a registry key.
	if got, _ := st.GetNodePackCache(NodePackSourceExtensionNodeMap); got != nil {
		t.Error("the static-index key was written by a registry put")
	}
}

// TestNodePackCacheStoresAMiss: an unattributable class is a stable FACT, so a
// Registry 404 is cached (as a JSON null) rather than refetched every render.
func TestNodePackCacheStoresAMiss(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	src := NodePackSourceRegistryClass("ZZZ_unrelated_class_9f3")
	if err := st.PutNodePackCache(src, []byte(`null`)); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetNodePackCache(src)
	if err != nil || got == nil {
		t.Fatalf("a cached miss must be distinguishable from no entry at all: %v %v", got, err)
	}
	if string(got.Raw) != "null" {
		t.Errorf("raw = %s", got.Raw)
	}
}

// TestNodePackCacheMigrationApplies proves migration 0013 lands the table and the
// schema version advanced.
func TestNodePackCacheMigrationApplies(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	v, err := st.SchemaVersion()
	if err != nil {
		t.Fatal(err)
	}
	if v < 13 {
		t.Fatalf("schema version = %d, want >= 13 (0013_nodepack_cache)", v)
	}
	var name string
	err = st.DB().QueryRow(
		`SELECT name FROM sqlite_master WHERE type='table' AND name='nodepack_cache'`).Scan(&name)
	if err != nil {
		t.Fatalf("nodepack_cache table missing after migrate: %v", err)
	}
}

// TestNodePackCachePrunesToCap: the per-class Registry rows would otherwise grow
// without bound (one per distinct missing class the user ever encounters).
func TestNodePackCachePrunesToCap(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	orig := nodePackCacheMaxRows
	nodePackCacheMaxRows = 5
	defer func() { nodePackCacheMaxRows = orig }()

	for i := 1; i <= 12; i++ {
		src := NodePackSourceRegistryClass(string(rune('a'+i)) + "Class")
		if err := st.PutNodePackCache(src, []byte(`{"id":"p"}`)); err != nil {
			t.Fatalf("put %d: %v", i, err)
		}
	}

	var n int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM nodepack_cache`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != nodePackCacheMaxRows {
		t.Fatalf("row count = %d, want cap %d", n, nodePackCacheMaxRows)
	}
	// The most recently written entry survives; an early one is pruned.
	if got, _ := st.GetNodePackCache(NodePackSourceRegistryClass("mClass")); got == nil {
		t.Error("the newest entry was pruned")
	}
	if got, _ := st.GetNodePackCache(NodePackSourceRegistryClass("bClass")); got != nil {
		t.Error("the oldest entry should have been pruned")
	}
}
