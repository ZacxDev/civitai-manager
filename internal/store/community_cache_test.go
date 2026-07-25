package store

import (
	"testing"
)

func TestCommunityCacheMissThenHit(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	// Miss: no entry yet.
	if got, err := st.GetCommunityCache(7, 11); err != nil || got != nil {
		t.Fatalf("expected (nil,nil) miss, got (%v,%v)", got, err)
	}

	raw := []byte(`{"items":[{"id":1,"url":"https://image.civitai.com/x.jpeg"}]}`)
	if err := st.PutCommunityCache(7, 11, raw); err != nil {
		t.Fatalf("put: %v", err)
	}

	got, err := st.GetCommunityCache(7, 11)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got == nil {
		t.Fatal("expected a cache hit after put")
	}
	if got.ModelID != 7 || got.VersionID != 11 || string(got.Raw) != string(raw) {
		t.Fatalf("entry mismatch: %+v", got)
	}
	if got.FetchedAt.IsZero() {
		t.Fatal("fetched_at should be set")
	}
	// A different version id is a distinct key (miss).
	if other, err := st.GetCommunityCache(7, 10); err != nil || other != nil {
		t.Fatalf("expected miss for a different version, got (%v,%v)", other, err)
	}
}

func TestCommunityCacheUpsertReplaces(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	if err := st.PutCommunityCache(1, 2, []byte(`{"items":["old"]}`)); err != nil {
		t.Fatal(err)
	}
	if err := st.PutCommunityCache(1, 2, []byte(`{"items":["new"]}`)); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetCommunityCache(1, 2)
	if err != nil || got == nil {
		t.Fatalf("get: %v %v", got, err)
	}
	if string(got.Raw) != `{"items":["new"]}` {
		t.Fatalf("upsert did not replace: %+v", got)
	}
}

// TestCommunityCacheMigrationApplies proves migration 0010 lands the
// community_cache table (Open runs every pending migration) and the schema
// version is at least 10.
func TestCommunityCacheMigrationApplies(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	v, err := st.SchemaVersion()
	if err != nil {
		t.Fatal(err)
	}
	if v < 10 {
		t.Fatalf("schema version = %d, want >= 10 (0010_community_cache)", v)
	}
	var name string
	err = st.DB().QueryRow(
		`SELECT name FROM sqlite_master WHERE type='table' AND name='community_cache'`).Scan(&name)
	if err != nil {
		t.Fatalf("community_cache table missing after migrate: %v", err)
	}
}
