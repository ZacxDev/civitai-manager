package store

import "testing"

func TestHasLocalFileNamed(t *testing.T) {
	st := openTestStore(t)

	if err := st.UpsertLocalFile(LocalFile{
		Path: "/models/checkpoints/Flux1-Dev.safetensors", SHA256: "h1",
		Kind: LocalKindModel, Status: LocalStatusMatched,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Case-insensitive basename hit (even for an unmatched-status file, since a
	// referenced model just needs to be ON DISK).
	if err := st.UpsertLocalFile(LocalFile{
		Path: "/other/loras/mylora.safetensors", SHA256: "h2",
		Kind: LocalKindModel, Status: LocalStatusUnmatched,
	}); err != nil {
		t.Fatalf("seed2: %v", err)
	}

	cases := map[string]bool{
		"flux1-dev.safetensors": true,  // case-insensitive
		"mylora.safetensors":    true,  // unmatched status still counts
		"mlora.safetensors":     false, // not a suffix-substring false positive
		"absent.safetensors":    false,
		"":                      false,
	}
	for name, want := range cases {
		got, err := st.HasLocalFileNamed(name)
		if err != nil {
			t.Fatalf("HasLocalFileNamed(%q): %v", name, err)
		}
		if got != want {
			t.Errorf("HasLocalFileNamed(%q) = %v, want %v", name, got, want)
		}
	}
}
