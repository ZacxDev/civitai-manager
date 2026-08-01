package store

import (
	"testing"
)

func TestComfyCacheEmpty(t *testing.T) {
	st := openTestStore(t)
	ent, err := st.GetComfyObjectInfo()
	if err != nil {
		t.Fatalf("GetComfyObjectInfo on empty cache: %v", err)
	}
	if ent != nil {
		t.Fatalf("expected nil entry on empty cache, got %+v", ent)
	}
}

func TestComfyCachePutAndGet(t *testing.T) {
	st := openTestStore(t)
	payload := []byte(`{"CheckpointLoaderSimple":{"input":{"required":{"ckpt_name":[["a.safetensors"],{}]}}}}`)
	if err := st.PutComfyObjectInfo(payload); err != nil {
		t.Fatalf("PutComfyObjectInfo: %v", err)
	}
	ent, err := st.GetComfyObjectInfo()
	if err != nil {
		t.Fatalf("GetComfyObjectInfo: %v", err)
	}
	if ent == nil {
		t.Fatal("expected non-nil entry after put")
	}
	if string(ent.ObjectInfoJSON) != string(payload) {
		t.Errorf("ObjectInfoJSON = %s, want %s", ent.ObjectInfoJSON, payload)
	}
	if ent.UpdatedAt.IsZero() {
		t.Error("UpdatedAt must be set")
	}
}

func TestComfyCacheUpsert(t *testing.T) {
	st := openTestStore(t)
	if err := st.PutComfyObjectInfo([]byte(`{"v1":{}}`)); err != nil {
		t.Fatal(err)
	}
	if err := st.PutComfyObjectInfo([]byte(`{"v2":{}}`)); err != nil {
		t.Fatal(err)
	}
	ent, err := st.GetComfyObjectInfo()
	if err != nil {
		t.Fatal(err)
	}
	if string(ent.ObjectInfoJSON) != `{"v2":{}}` {
		t.Errorf("upsert should overwrite, got %s", ent.ObjectInfoJSON)
	}
}

func TestComfyCacheInvalidate(t *testing.T) {
	st := openTestStore(t)
	if err := st.PutComfyObjectInfo([]byte(`{"x":{}}`)); err != nil {
		t.Fatal(err)
	}
	if err := st.InvalidateComfyModelCache(); err != nil {
		t.Fatalf("InvalidateComfyModelCache: %v", err)
	}
	ent, err := st.GetComfyObjectInfo()
	if err != nil {
		t.Fatal(err)
	}
	if ent != nil {
		t.Errorf("expected nil after invalidation, got %+v", ent)
	}
}

func TestComfyCacheInvalidateEmpty(t *testing.T) {
	st := openTestStore(t)
	// Invalidating an already-empty cache must not error.
	if err := st.InvalidateComfyModelCache(); err != nil {
		t.Fatalf("InvalidateComfyModelCache on empty: %v", err)
	}
}

func TestComfyCacheEmptyJSON(t *testing.T) {
	st := openTestStore(t)
	if err := st.PutComfyObjectInfo([]byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	ent, err := st.GetComfyObjectInfo()
	if err != nil {
		t.Fatal(err)
	}
	if string(ent.ObjectInfoJSON) != `{}` {
		t.Errorf("expected empty JSON object, got %s", ent.ObjectInfoJSON)
	}
}

func TestComfyCacheLargePayload(t *testing.T) {
	st := openTestStore(t)
	// Simulate a realistic object_info payload — a large JSON blob.
	payload := make([]byte, 1<<20) // 1 MiB
	for i := range payload {
		payload[i] = byte('a' + (i % 26))
	}
	if err := st.PutComfyObjectInfo(payload); err != nil {
		t.Fatal(err)
	}
	ent, err := st.GetComfyObjectInfo()
	if err != nil {
		t.Fatal(err)
	}
	if len(ent.ObjectInfoJSON) != len(payload) {
		t.Errorf("payload size = %d, want %d", len(ent.ObjectInfoJSON), len(payload))
	}
}

func TestComfyCacheMigrationApplied(t *testing.T) {
	st := openTestStore(t)
	v, err := st.SchemaVersion()
	if err != nil {
		t.Fatalf("schema version: %v", err)
	}
	if v < 19 {
		t.Fatalf("schema version = %d, want >= 19 (0019_comfy_model_cache)", v)
	}
}
