package library

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/store"
)

func upsertGetID(t *testing.T, st *store.Store, lf store.LocalFile) int64 {
	t.Helper()
	if err := st.UpsertLocalFile(lf); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetLocalFileByPath(lf.Path)
	if err != nil || got == nil {
		t.Fatalf("reload %s: %v", lf.Path, err)
	}
	return got.ID
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// dupPair sets up a keeper + a flagged duplicate (with two sidecars) under root
// and returns the store and the duplicate's id.
func dupPair(t *testing.T, root string) (*store.Store, int64, string) {
	st := newTestStore(t)
	keeper := filepath.Join(root, "a.safetensors")
	dupe := filepath.Join(root, "sub", "b.safetensors")
	writeFile(t, keeper, "same-bytes")
	writeFile(t, dupe, "same-bytes")
	writeFile(t, dupe[:len(dupe)-len(".safetensors")]+".civitai.info", `{"id":1}`)
	writeFile(t, dupe[:len(dupe)-len(".safetensors")]+".preview.png", "img")

	mid, vid := 300, 30
	upsertGetID(t, st, store.LocalFile{Path: keeper, SHA256: "same", ModelID: &mid, VersionID: &vid,
		SizeBytes: 10, Status: store.LocalStatusMatched, Kind: store.LocalKindModel})
	dupID := upsertGetID(t, st, store.LocalFile{Path: dupe, SHA256: "same", ModelID: &mid, VersionID: &vid,
		SizeBytes: 10, Status: store.LocalStatusMatched, CandidateReason: store.CandidateDuplicate, Kind: store.LocalKindModel})
	return st, dupID, dupe
}

func TestQuarantineDryRunMovesNothing(t *testing.T) {
	root := t.TempDir()
	st, dupID, dupe := dupPair(t, root)
	sc := NewScanner(st, nil, Options{ModelRoot: root}, nil)

	plan, err := sc.Quarantine(context.Background(), []int64{dupID}, false)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Applied {
		t.Fatal("dry-run must not apply")
	}
	if len(plan.Moves) == 0 {
		t.Fatal("dry-run should still report the planned moves")
	}
	if !exists(dupe) {
		t.Fatal("dry-run moved a file; it must not")
	}
	batches, _ := st.ListQuarantineBatches()
	if len(batches) != 0 {
		t.Fatalf("dry-run recorded %d batches; want 0", len(batches))
	}
}

func TestQuarantineApplyMovesFileAndSidecarsWithManifest(t *testing.T) {
	root := t.TempDir()
	st, dupID, dupe := dupPair(t, root)
	sc := NewScanner(st, nil, Options{ModelRoot: root}, nil)

	plan, err := sc.Quarantine(context.Background(), []int64{dupID}, true)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Applied || plan.BatchID == 0 {
		t.Fatalf("apply should set Applied+BatchID, got %+v", plan)
	}
	// The model file and both sidecars moved (3 moves).
	if len(plan.Moves) != 3 {
		t.Fatalf("moves = %d, want 3 (model + 2 sidecars)", len(plan.Moves))
	}
	// Originals gone.
	base := dupe[:len(dupe)-len(".safetensors")]
	for _, p := range []string{dupe, base + ".civitai.info", base + ".preview.png"} {
		if exists(p) {
			t.Errorf("original %s should have been moved away", p)
		}
	}
	// Keeper untouched.
	if !exists(filepath.Join(root, "a.safetensors")) {
		t.Error("keeper copy must remain")
	}
	// Trash preserves the relative path, and the manifest exists.
	files, err := st.ListQuarantinedFiles(plan.BatchID)
	if err != nil || len(files) != 3 {
		t.Fatalf("quarantined_files = %d (err %v), want 3", len(files), err)
	}
	for _, f := range files {
		if !exists(f.TrashPath) {
			t.Errorf("trash file missing: %s", f.TrashPath)
		}
	}
	if !hasTrashRel(files, filepath.Join("sub", "b.safetensors")) {
		t.Errorf("trash path should preserve sub/ relpath, got %+v", trashPaths(files))
	}
	batch, err := st.GetQuarantineBatch(plan.BatchID)
	if err != nil {
		t.Fatal(err)
	}
	if !exists(batch.Manifest) {
		t.Fatalf("manifest.json not written at %s", batch.Manifest)
	}
	// Index row for the moved model file is gone.
	if got, _ := st.GetLocalFileByPath(dupe); got != nil {
		t.Error("index row for a quarantined file should be deleted")
	}
}

func TestQuarantineRestoreRoundTrips(t *testing.T) {
	root := t.TempDir()
	st, dupID, dupe := dupPair(t, root)
	sc := NewScanner(st, nil, Options{ModelRoot: root}, nil)

	plan, err := sc.Quarantine(context.Background(), []int64{dupID}, true)
	if err != nil {
		t.Fatal(err)
	}
	res, err := sc.Restore(context.Background(), plan.BatchID)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Conflicts) != 0 {
		t.Fatalf("unexpected conflicts: %v", res.Conflicts)
	}
	if !exists(dupe) {
		t.Fatal("restore did not return the model file to its original path")
	}
	batch, _ := st.GetQuarantineBatch(plan.BatchID)
	if !batch.Restored() {
		t.Fatal("batch should be marked restored")
	}
}

func TestQuarantineRestoreRefusesOccupiedOriginal(t *testing.T) {
	root := t.TempDir()
	st, dupID, dupe := dupPair(t, root)
	sc := NewScanner(st, nil, Options{ModelRoot: root}, nil)

	plan, err := sc.Quarantine(context.Background(), []int64{dupID}, true)
	if err != nil {
		t.Fatal(err)
	}
	// Re-occupy the original model path so restore must refuse it.
	writeFile(t, dupe, "something new lives here now")

	res, err := sc.Restore(context.Background(), plan.BatchID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range res.Conflicts {
		if c == dupe {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a conflict for the occupied original, got %+v", res.Conflicts)
	}
}

func TestQuarantineRefusesOutsideScanRoot(t *testing.T) {
	root := t.TempDir()
	st := newTestStore(t)
	outside := filepath.Join(t.TempDir(), "stray.safetensors") // different tree
	writeFile(t, outside, "x")
	mid, vid := 1, 1
	id := upsertGetID(t, st, store.LocalFile{Path: outside, SHA256: "h", ModelID: &mid, VersionID: &vid,
		Status: store.LocalStatusMatched, CandidateReason: store.CandidateDuplicate, Kind: store.LocalKindModel})

	sc := NewScanner(st, nil, Options{ModelRoot: root}, nil)
	plan, err := sc.Quarantine(context.Background(), []int64{id}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Moves) != 0 {
		t.Fatal("a file outside the scan roots must never be moved")
	}
	if len(plan.Skipped) != 1 || !strings.Contains(plan.Skipped[0].Reason, "outside") {
		t.Fatalf("expected an 'outside scan root' skip, got %+v", plan.Skipped)
	}
	if !exists(outside) {
		t.Fatal("the outside file must remain untouched")
	}
}

func TestQuarantineDuplicateAlwaysLeavesAtLeastOne(t *testing.T) {
	root := t.TempDir()
	st := newTestStore(t)
	a := filepath.Join(root, "a.safetensors")
	b := filepath.Join(root, "b.safetensors")
	writeFile(t, a, "same")
	writeFile(t, b, "same")
	mid, vid := 1, 1
	// Pathological: BOTH copies flagged and requested. The mover must refuse to
	// remove the last remaining copy, leaving both in place.
	idA := upsertGetID(t, st, store.LocalFile{Path: a, SHA256: "same", ModelID: &mid, VersionID: &vid,
		Status: store.LocalStatusMatched, CandidateReason: store.CandidateDuplicate, Kind: store.LocalKindModel})
	idB := upsertGetID(t, st, store.LocalFile{Path: b, SHA256: "same", ModelID: &mid, VersionID: &vid,
		Status: store.LocalStatusMatched, CandidateReason: store.CandidateDuplicate, Kind: store.LocalKindModel})

	sc := NewScanner(st, nil, Options{ModelRoot: root}, nil)
	plan, err := sc.Quarantine(context.Background(), []int64{idA, idB}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Moves) != 0 {
		t.Fatalf("moving both duplicate copies would leave zero; must be refused, got %d moves", len(plan.Moves))
	}
	if !exists(a) || !exists(b) {
		t.Fatal("at least one duplicate copy must always survive (both survived here)")
	}
}

func TestQuarantineRefusesUnmatched(t *testing.T) {
	root := t.TempDir()
	st := newTestStore(t)
	p := filepath.Join(root, "orphan.safetensors")
	writeFile(t, p, "x")
	id := upsertGetID(t, st, store.LocalFile{Path: p, SHA256: "h", SizeBytes: 1,
		Status: store.LocalStatusUnmatched, Kind: store.LocalKindModel})

	sc := NewScanner(st, nil, Options{ModelRoot: root}, nil)
	plan, err := sc.Quarantine(context.Background(), []int64{id}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Moves) != 0 || len(plan.Skipped) != 1 || !strings.Contains(plan.Skipped[0].Reason, "unmatched") {
		t.Fatalf("unmatched file must be refused, got moves=%d skipped=%+v", len(plan.Moves), plan.Skipped)
	}
	if !exists(p) {
		t.Fatal("unmatched file must not be moved")
	}
}

// TestQuarantineRefusesUnmatchedDuplicate confirms that a duplicate flagged
// offline (both copies unmatched) is reported but NOT moved: quarantine still
// refuses an unmatched file, so acting on the redundancy requires an online
// match first. This keeps both locked rules true at once.
func TestQuarantineRefusesUnmatchedDuplicate(t *testing.T) {
	root := t.TempDir()
	st := newTestStore(t)
	a := filepath.Join(root, "a.safetensors")
	b := filepath.Join(root, "b-copy.safetensors")
	writeFile(t, a, "same")
	writeFile(t, b, "same")
	// Keeper stays unflagged; the duplicate is unmatched + flagged.
	upsertGetID(t, st, store.LocalFile{Path: a, SHA256: "same", Status: store.LocalStatusUnmatched, Kind: store.LocalKindModel})
	dupID := upsertGetID(t, st, store.LocalFile{Path: b, SHA256: "same",
		Status: store.LocalStatusUnmatched, CandidateReason: store.CandidateDuplicate, Kind: store.LocalKindModel})

	sc := NewScanner(st, nil, Options{ModelRoot: root}, nil)
	plan, err := sc.Quarantine(context.Background(), []int64{dupID}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Moves) != 0 || len(plan.Skipped) != 1 || !strings.Contains(plan.Skipped[0].Reason, "unmatched") {
		t.Fatalf("unmatched duplicate must be refused, got moves=%d skipped=%+v", len(plan.Moves), plan.Skipped)
	}
	if !exists(b) {
		t.Fatal("unmatched duplicate must not be moved")
	}
}

func TestQuarantineSupersededRefusesNewest(t *testing.T) {
	root := t.TempDir()
	st := newTestStore(t)
	oldP := filepath.Join(root, "old.safetensors")
	newP := filepath.Join(root, "new.safetensors")
	writeFile(t, oldP, "old")
	writeFile(t, newP, "new")
	mid := 100
	v10, v20 := 10, 20
	upsertGetID(t, st, store.LocalFile{Path: oldP, SHA256: "o", ModelID: &mid, VersionID: &v10,
		Status: store.LocalStatusMatched, CandidateReason: store.CandidateSuperseded, Kind: store.LocalKindModel})
	// Force-flag the NEWEST version too (a misuse); the mover must still refuse it.
	newestID := upsertGetID(t, st, store.LocalFile{Path: newP, SHA256: "n", ModelID: &mid, VersionID: &v20,
		Status: store.LocalStatusMatched, CandidateReason: store.CandidateSuperseded, Kind: store.LocalKindModel})

	sc := NewScanner(st, nil, Options{ModelRoot: root}, nil)
	plan, err := sc.Quarantine(context.Background(), []int64{newestID}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Moves) != 0 || len(plan.Skipped) != 1 || !strings.Contains(plan.Skipped[0].Reason, "newest") {
		t.Fatalf("newest local version must be refused, got moves=%d skipped=%+v", len(plan.Moves), plan.Skipped)
	}
	if !exists(newP) {
		t.Fatal("newest version must remain on disk")
	}
}

// --- small test helpers ---

func trashPaths(files []store.QuarantinedFile) []string {
	var out []string
	for _, f := range files {
		out = append(out, f.TrashPath)
	}
	return out
}

func hasTrashRel(files []store.QuarantinedFile, rel string) bool {
	for _, f := range files {
		if strings.Contains(f.TrashPath, rel) {
			return true
		}
	}
	return false
}
