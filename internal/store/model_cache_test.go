package store

import (
	"testing"
)

func TestModelCacheMissThenHit(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	// Miss: no entry yet.
	if got, err := st.GetModelCache(42); err != nil || got != nil {
		t.Fatalf("expected (nil,nil) miss, got (%v,%v)", got, err)
	}

	raw := []byte(`{"id":42,"name":"Cool Model"}`)
	if err := st.PutModelCache(42, "Cool Model", raw); err != nil {
		t.Fatalf("put: %v", err)
	}

	got, err := st.GetModelCache(42)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got == nil {
		t.Fatal("expected a cache hit after put")
	}
	if got.ModelID != 42 || got.Name != "Cool Model" || string(got.Raw) != string(raw) {
		t.Fatalf("entry mismatch: %+v", got)
	}
	if got.FetchedAt.IsZero() {
		t.Fatal("fetched_at should be set")
	}
}

func TestModelCacheUpsertReplaces(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	if err := st.PutModelCache(1, "old", []byte(`{"name":"old"}`)); err != nil {
		t.Fatal(err)
	}
	if err := st.PutModelCache(1, "new", []byte(`{"name":"new"}`)); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetModelCache(1)
	if err != nil || got == nil {
		t.Fatalf("get: %v %v", got, err)
	}
	if got.Name != "new" || string(got.Raw) != `{"name":"new"}` {
		t.Fatalf("upsert did not replace: %+v", got)
	}
}

// TestModelCacheMigrationApplies proves migration 0007 lands the model_cache
// table on a populated DB (Open runs every pending migration).
func TestModelCacheMigrationApplies(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	v, err := st.SchemaVersion()
	if err != nil {
		t.Fatal(err)
	}
	if v < 7 {
		t.Fatalf("schema version = %d, want >= 7 (0007_model_cache)", v)
	}
	var name string
	err = st.DB().QueryRow(
		`SELECT name FROM sqlite_master WHERE type='table' AND name='model_cache'`).Scan(&name)
	if err != nil {
		t.Fatalf("model_cache table missing after migrate: %v", err)
	}
}
