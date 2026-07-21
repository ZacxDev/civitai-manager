package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
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

// seedDuplicateOnDisk writes a keeper + a flagged duplicate (byte-identical,
// matched) under the app's model root and returns the duplicate's on-disk path.
// SizeBytes is left 0 so the quarantine TOCTOU guard treats size as unknown.
func seedDuplicateOnDisk(t *testing.T, a *app) string {
	t.Helper()
	root := a.cfg.ModelRoot
	keeper := filepath.Join(root, "keep.safetensors")
	dupe := filepath.Join(root, "dupe.safetensors")
	for _, p := range []string{keeper, dupe} {
		if err := os.WriteFile(p, []byte("same-bytes"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mid, vid := 1, 1
	if err := a.store.UpsertLocalFile(store.LocalFile{Path: keeper, SHA256: "same", ModelID: &mid, VersionID: &vid,
		Status: store.LocalStatusMatched, Kind: store.LocalKindModel}); err != nil {
		t.Fatal(err)
	}
	if err := a.store.UpsertLocalFile(store.LocalFile{Path: dupe, SHA256: "same", ModelID: &mid, VersionID: &vid,
		Status: store.LocalStatusMatched, CandidateReason: store.CandidateDuplicate, Kind: store.LocalKindModel}); err != nil {
		t.Fatal(err)
	}
	return dupe
}

// TestQuarantineBareDryRunsAllCandidates proves finding #3: a bare
// `library quarantine` (no --id/--reason/--all, no --apply) DRY-RUNS over every
// current candidate — it exits 0, prints a plan, and moves nothing — matching the
// --help contract (rather than erroring "specify --id, --reason, or --all").
func TestQuarantineBareDryRunsAllCandidates(t *testing.T) {
	a := newTestApp(t, &cliFakeClient{})
	dupe := seedDuplicateOnDisk(t, a)

	var out bytes.Buffer
	if err := quarantineRun(context.Background(), a, &out, nil, "", false, false, nil); err != nil {
		t.Fatalf("bare quarantine should not error, got %v", err)
	}
	if !strings.Contains(out.String(), "Would move") {
		t.Errorf("bare quarantine should print a dry-run plan, got %q", out.String())
	}
	if !strings.Contains(out.String(), "Dry-run") {
		t.Errorf("bare quarantine should label itself a dry-run, got %q", out.String())
	}
	// Nothing moved.
	if _, err := os.Stat(dupe); err != nil {
		t.Fatalf("a dry-run must not move the candidate: %v", err)
	}
	if batches, _ := a.store.ListQuarantineBatches(); len(batches) != 0 {
		t.Fatalf("a dry-run must record no batches, got %d", len(batches))
	}
}

// TestQuarantineApplyWithoutSelectorRefused proves the destructive path keeps its
// guard: `library quarantine --apply` with NO selector is refused so it can never
// implicitly move every candidate.
func TestQuarantineApplyWithoutSelectorRefused(t *testing.T) {
	a := newTestApp(t, &cliFakeClient{})
	dupe := seedDuplicateOnDisk(t, a)

	var out bytes.Buffer
	err := quarantineRun(context.Background(), a, &out, nil, "", false, true, nil) // apply, no selector
	if err == nil {
		t.Fatal("--apply with no selector must be refused")
	}
	if !strings.Contains(err.Error(), "selector") {
		t.Errorf("error should explain a selector is required, got %v", err)
	}
	if _, serr := os.Stat(dupe); serr != nil {
		t.Fatalf("nothing must be moved when --apply is refused: %v", serr)
	}
}

// TestQuarantineRunPathFlagMakesCandidateActionable proves the `--path` flag is
// wired through quarantineRun to the scanner's allowed roots: a candidate outside
// model_root (and without a persisted scan_root) is refused with the actionable
// skip message by default, and becomes movable when its directory is passed via
// --path.
func TestQuarantineRunPathFlagMakesCandidateActionable(t *testing.T) {
	a := newTestApp(t, &cliFakeClient{})
	extra := t.TempDir() // outside model_root

	keeper := filepath.Join(a.cfg.ModelRoot, "keep.safetensors")
	dupe := filepath.Join(extra, "dupe.safetensors")
	for _, p := range []string{keeper, dupe} {
		if err := os.WriteFile(p, []byte("same-bytes"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mid, vid := 1, 1
	if err := a.store.UpsertLocalFile(store.LocalFile{Path: keeper, SHA256: "same", ModelID: &mid, VersionID: &vid,
		Status: store.LocalStatusMatched, Kind: store.LocalKindModel}); err != nil {
		t.Fatal(err)
	}
	if err := a.store.UpsertLocalFile(store.LocalFile{Path: dupe, SHA256: "same", ModelID: &mid, VersionID: &vid,
		Status: store.LocalStatusMatched, CandidateReason: store.CandidateDuplicate, Kind: store.LocalKindModel}); err != nil {
		t.Fatal(err)
	}
	dupRow, _ := a.store.GetLocalFileByPath(dupe)

	// Default (no --path): refused, with the actionable skip message.
	var out bytes.Buffer
	if err := quarantineRun(context.Background(), a, &out, []int64{dupRow.ID}, "", false, true, nil); err != nil {
		t.Fatalf("quarantineRun: %v", err)
	}
	if !strings.Contains(out.String(), "SKIPPED") || !strings.Contains(out.String(), "outside the scanned roots") {
		t.Fatalf("expected an actionable 'outside the scanned roots' skip message, got %q", out.String())
	}
	if _, err := os.Stat(dupe); err != nil {
		t.Fatalf("the refused candidate must remain: %v", err)
	}

	// With --path=extra: actionable and moved.
	var out2 bytes.Buffer
	if err := quarantineRun(context.Background(), a, &out2, []int64{dupRow.ID}, "", false, true, []string{extra}); err != nil {
		t.Fatalf("quarantineRun with --path: %v", err)
	}
	if !strings.Contains(out2.String(), "Moved") {
		t.Fatalf("expected a Moved summary once --path covered the candidate, got %q", out2.String())
	}
	if _, err := os.Stat(dupe); !os.IsNotExist(err) {
		t.Fatalf("the candidate should have been quarantined away with --path, stat err=%v", err)
	}
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
