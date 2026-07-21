package cli

import (
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/store"
)

func TestValidateReason(t *testing.T) {
	for _, ok := range []string{"", "superseded", "duplicate", "broken"} {
		if err := validateReason(ok); err != nil {
			t.Errorf("validateReason(%q) = %v, want nil", ok, err)
		}
	}
	if err := validateReason("bogus"); err == nil {
		t.Error("validateReason(bogus) should error")
	}
}

func seedCandidate(t *testing.T, st *store.Store, path, reason string) int64 {
	t.Helper()
	if err := st.UpsertLocalFile(store.LocalFile{
		Path: path, SHA256: "h", Status: store.LocalStatusMatched, Kind: store.LocalKindModel,
	}); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetLocalFileByPath(path)
	if reason != "" {
		if err := st.SetCandidateReason(got.ID, reason); err != nil {
			t.Fatal(err)
		}
	}
	return got.ID
}

func TestResolveTargetIDs(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	supID := seedCandidate(t, st, "/a", store.CandidateSuperseded)
	dupID := seedCandidate(t, st, "/b", store.CandidateDuplicate)
	seedCandidate(t, st, "/c", "") // not a candidate

	// Explicit --id wins.
	if got, _ := resolveTargetIDs(st, []int64{99}, "", false); len(got) != 1 || got[0] != 99 {
		t.Fatalf("explicit ids = %v, want [99]", got)
	}
	// --reason selects that reason only.
	sup, _ := resolveTargetIDs(st, nil, store.CandidateSuperseded, false)
	if len(sup) != 1 || sup[0] != supID {
		t.Fatalf("reason=superseded ids = %v, want [%d]", sup, supID)
	}
	// --all selects every candidate (both), not the non-candidate.
	all, _ := resolveTargetIDs(st, nil, "", true)
	if len(all) != 2 {
		t.Fatalf("--all ids = %v, want 2", all)
	}
	_ = dupID
}

func TestHumanBytes(t *testing.T) {
	cases := map[int64]string{0: "0 B", 512: "512 B", 1024: "1.0 KB", 1048576: "1.0 MB"}
	for in, want := range cases {
		if got := humanBytes(in); got != want {
			t.Errorf("humanBytes(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestGroupByModelOrders(t *testing.T) {
	files := []store.LocalFile{
		{Path: "/z", ModelID: intp(30)},
		{Path: "/a", ModelID: intp(10)},
		{Path: "/b", ModelID: intp(10)},
		{Path: "/n", ModelID: nil}, // unmatched -> group 0
	}
	groups := groupByModel(files)
	if len(groups) != 3 {
		t.Fatalf("groups = %d, want 3", len(groups))
	}
	if groups[0].modelID != 0 || groups[1].modelID != 10 || groups[2].modelID != 30 {
		t.Fatalf("group order = %d/%d/%d, want 0/10/30", groups[0].modelID, groups[1].modelID, groups[2].modelID)
	}
	if len(groups[1].files) != 2 {
		t.Fatalf("model 10 should have 2 files, got %d", len(groups[1].files))
	}
}

func intp(i int) *int { return &i }
