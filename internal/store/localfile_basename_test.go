package store

import "testing"

func TestLocalFileByBasename(t *testing.T) {
	st := openTestStore(t)

	// A matched file with civitai linkage.
	if err := st.UpsertLocalFile(LocalFile{
		Path: "/models/checkpoints/Flux1-Dev.safetensors", SHA256: "h1",
		ModelID: intp(10), VersionID: intp(20),
		Kind: LocalKindModel, Status: LocalStatusMatched,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// An unlinked file (no model/version).
	if err := st.UpsertLocalFile(LocalFile{
		Path: "/other/loras/mylora.safetensors", SHA256: "h2",
		Kind: LocalKindModel, Status: LocalStatusUnmatched,
	}); err != nil {
		t.Fatalf("seed2: %v", err)
	}

	// Case-insensitive basename hit returns the linkage.
	m, err := st.LocalFileByBasename("flux1-dev.safetensors")
	if err != nil {
		t.Fatalf("LocalFileByBasename: %v", err)
	}
	if m == nil || m.ModelID == nil || *m.ModelID != 10 || m.VersionID == nil || *m.VersionID != 20 {
		t.Fatalf("expected model 10/version 20, got %+v", m)
	}

	// Unlinked file still returns the row (caller decides it's unresolved).
	m, err = st.LocalFileByBasename("mylora.safetensors")
	if err != nil {
		t.Fatalf("LocalFileByBasename unlinked: %v", err)
	}
	if m == nil || m.ModelID != nil {
		t.Fatalf("expected unlinked row, got %+v", m)
	}

	// No match / empty → nil.
	for _, name := range []string{"absent.safetensors", "mlora.safetensors", ""} {
		m, err := st.LocalFileByBasename(name)
		if err != nil {
			t.Fatalf("LocalFileByBasename(%q): %v", name, err)
		}
		if m != nil {
			t.Errorf("LocalFileByBasename(%q) = %+v, want nil", name, m)
		}
	}
}

func TestLocalFileByBasename_Ambiguous(t *testing.T) {
	st := openTestStore(t)

	// Two files with the SAME basename but DIFFERENT model/version linkage.
	if err := st.UpsertLocalFile(LocalFile{
		Path: "/a/dup.safetensors", SHA256: "x1", ModelID: intp(1), VersionID: intp(2),
		Kind: LocalKindModel, Status: LocalStatusMatched,
	}); err != nil {
		t.Fatalf("seed a: %v", err)
	}
	if err := st.UpsertLocalFile(LocalFile{
		Path: "/b/dup.safetensors", SHA256: "x2", ModelID: intp(3), VersionID: intp(4),
		Kind: LocalKindModel, Status: LocalStatusMatched,
	}); err != nil {
		t.Fatalf("seed b: %v", err)
	}

	m, err := st.LocalFileByBasename("dup.safetensors")
	if err != nil {
		t.Fatalf("LocalFileByBasename ambiguous: %v", err)
	}
	if m != nil {
		t.Errorf("ambiguous basename should return nil, got %+v", m)
	}
}

func TestLocalFileByBasename_DuplicateSameLinkage(t *testing.T) {
	st := openTestStore(t)

	// Same basename, SAME linkage in two dirs → not ambiguous, returns a match.
	if err := st.UpsertLocalFile(LocalFile{
		Path: "/a/same.safetensors", SHA256: "y1", ModelID: intp(7), VersionID: intp(8),
		Kind: LocalKindModel, Status: LocalStatusMatched,
	}); err != nil {
		t.Fatalf("seed a: %v", err)
	}
	if err := st.UpsertLocalFile(LocalFile{
		Path: "/b/same.safetensors", SHA256: "y2", ModelID: intp(7), VersionID: intp(8),
		Kind: LocalKindModel, Status: LocalStatusMatched,
	}); err != nil {
		t.Fatalf("seed b: %v", err)
	}

	m, err := st.LocalFileByBasename("same.safetensors")
	if err != nil {
		t.Fatalf("LocalFileByBasename: %v", err)
	}
	if m == nil || m.ModelID == nil || *m.ModelID != 7 {
		t.Fatalf("expected model 7 match, got %+v", m)
	}
}
