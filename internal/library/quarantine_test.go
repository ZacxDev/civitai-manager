package library

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/hashutil"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// assertBytes fails unless the file at path contains exactly want.
func assertBytes(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(got) != want {
		t.Fatalf("%s bytes = %q, want %q", path, string(got), want)
	}
}

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

// TestQuarantineTwoRootsSameRelpathNoCollision proves the root-qualified trash
// layout (#1): two files with the SAME relpath under DIFFERENT scan roots, with
// DIFFERENT bytes, both quarantined in one batch land at DISTINCT trash paths,
// produce two distinct ledger rows, and restore returns BOTH to their correct
// origins with original bytes intact (no silent clobber, no corrupted undo).
func TestQuarantineTwoRootsSameRelpathNoCollision(t *testing.T) {
	root1 := t.TempDir()
	root2 := t.TempDir()
	trash := filepath.Join(t.TempDir(), "trash")
	st := newTestStore(t)

	foo1 := filepath.Join(root1, "lora", "foo.safetensors")
	foo2 := filepath.Join(root2, "lora", "foo.safetensors")
	keep1 := filepath.Join(root1, "keep.safetensors")
	keep2 := filepath.Join(root2, "keep.safetensors")
	writeFile(t, foo1, "AAAA-root1-distinct-bytes")
	writeFile(t, keep1, "AAAA-root1-distinct-bytes")
	writeFile(t, foo2, "BBBB-root2-distinct-bytes")
	writeFile(t, keep2, "BBBB-root2-distinct-bytes")

	mid, vid := 1, 1
	mk := func(path, sha, reason string) int64 {
		return upsertGetID(t, st, store.LocalFile{Path: path, SHA256: sha, ModelID: &mid, VersionID: &vid,
			SizeBytes: 24, Status: store.LocalStatusMatched, CandidateReason: reason, Kind: store.LocalKindModel})
	}
	mk(keep1, "sha-a", "")
	mk(keep2, "sha-b", "")
	id1 := mk(foo1, "sha-a", store.CandidateDuplicate)
	id2 := mk(foo2, "sha-b", store.CandidateDuplicate)

	sc := NewScanner(st, nil, Options{ModelRoot: root1, Paths: []string{root1, root2}, TrashDir: trash}, nil)
	plan, err := sc.Quarantine(context.Background(), []int64{id1, id2}, true)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Applied || len(plan.Moves) != 2 {
		t.Fatalf("want 2 applied moves, got applied=%v moves=%d skipped=%+v", plan.Applied, len(plan.Moves), plan.Skipped)
	}
	files, err := st.ListQuarantinedFiles(plan.BatchID)
	if err != nil || len(files) != 2 {
		t.Fatalf("ledger rows = %d (err %v), want 2 distinct rows", len(files), err)
	}
	if files[0].TrashPath == files[1].TrashPath {
		t.Fatalf("same-relpath files under two roots collided onto one trash path: %s", files[0].TrashPath)
	}
	for _, f := range files {
		if !exists(f.TrashPath) {
			t.Errorf("trash file missing: %s", f.TrashPath)
		}
	}

	res, err := sc.Restore(context.Background(), plan.BatchID)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Conflicts) != 0 {
		t.Fatalf("unexpected conflicts: %v", res.Conflicts)
	}
	assertBytes(t, foo1, "AAAA-root1-distinct-bytes")
	assertBytes(t, foo2, "BBBB-root2-distinct-bytes")
}

// TestQuarantineSharedBasenameSidecarMovedOnce proves the plan dedup (#3): two
// model files sharing a base name (foo.safetensors + foo.ckpt) both resolve the
// same foo.civitai.info sidecar, which must be scheduled — and moved — exactly
// once instead of failing the batch on a second (already-gone) move.
func TestQuarantineSharedBasenameSidecarMovedOnce(t *testing.T) {
	root := t.TempDir()
	trash := filepath.Join(t.TempDir(), "trash")
	st := newTestStore(t)

	safet := filepath.Join(root, "foo.safetensors")
	ckpt := filepath.Join(root, "foo.ckpt")
	sidecar := filepath.Join(root, "foo.civitai.info")
	keepA := filepath.Join(root, "keepA.safetensors")
	keepB := filepath.Join(root, "keepB.ckpt")
	writeFile(t, safet, "aaa")
	writeFile(t, ckpt, "bbb")
	writeFile(t, sidecar, `{"id":1}`)
	writeFile(t, keepA, "aaa")
	writeFile(t, keepB, "bbb")

	mid, vid := 1, 1
	mk := func(path, sha, reason string) int64 {
		return upsertGetID(t, st, store.LocalFile{Path: path, SHA256: sha, ModelID: &mid, VersionID: &vid,
			SizeBytes: 3, Status: store.LocalStatusMatched, CandidateReason: reason, Kind: store.LocalKindModel})
	}
	mk(keepA, "sha-a", "")
	mk(keepB, "sha-b", "")
	idS := mk(safet, "sha-a", store.CandidateDuplicate)
	idC := mk(ckpt, "sha-b", store.CandidateDuplicate)

	sc := NewScanner(st, nil, Options{ModelRoot: root, TrashDir: trash}, nil)
	plan, err := sc.Quarantine(context.Background(), []int64{idS, idC}, true)
	if err != nil {
		t.Fatalf("shared-basename sidecar must not abort the batch: %v", err)
	}
	// 2 model files + the ONE shared sidecar = 3 moves (not 4).
	sidecarMoves := 0
	for _, m := range plan.Moves {
		if m.OriginalPath == sidecar {
			sidecarMoves++
		}
	}
	if sidecarMoves != 1 {
		t.Fatalf("shared sidecar scheduled %d times, want exactly 1; moves=%+v", sidecarMoves, plan.Moves)
	}
	if len(plan.Moves) != 3 {
		t.Fatalf("moves = %d, want 3 (2 models + 1 shared sidecar)", len(plan.Moves))
	}
	if exists(sidecar) {
		t.Error("the shared sidecar should have been moved away")
	}
}

// TestCopyThenRemoveDurableAndPreserving proves the cross-filesystem copy
// fallback (#4) preserves the source's bytes, mode and mtime and removes the
// source only after a verified copy.
func TestCopyThenRemoveDurableAndPreserving(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.bin")
	dst := filepath.Join(dir, "sub", "dst.bin")
	writeFile(t, src, "durable move payload")
	if err := os.Chmod(src, 0o600); err != nil {
		t.Fatal(err)
	}
	mtime := time.Date(2021, 5, 6, 7, 8, 9, 0, time.UTC)
	if err := os.Chtimes(src, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	sha, err := hashutil.SumFile(src)
	if err != nil {
		t.Fatal(err)
	}

	if err := copyThenRemove(src, dst, sha); err != nil {
		t.Fatalf("copyThenRemove: %v", err)
	}
	if exists(src) {
		t.Fatal("source must be removed after a verified copy")
	}
	assertBytes(t, dst, "durable move payload")
	fi, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600 preserved", fi.Mode().Perm())
	}
	if !fi.ModTime().Equal(mtime) {
		t.Errorf("mtime = %v, want %v preserved", fi.ModTime(), mtime)
	}
}

// TestCopyThenRemoveVerifyFailureLeavesSourceIntact proves a failed verify (a
// wrong expected hash, standing in for a truncated/corrupt copy) removes the
// partial destination and leaves the source untouched — never a lost source.
func TestCopyThenRemoveVerifyFailureLeavesSourceIntact(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.bin")
	dst := filepath.Join(dir, "dst.bin")
	writeFile(t, src, "original")

	err := copyThenRemove(src, dst, "0000000000000000000000000000000000000000000000000000000000000000")
	if err == nil {
		t.Fatal("expected a hash-verification error")
	}
	if !exists(src) {
		t.Fatal("source must remain intact on verify failure")
	}
	if exists(dst) {
		t.Fatal("partial destination must be removed on verify failure")
	}
	assertBytes(t, src, "original")
}

// TestMoveFileRefusesToClobber proves moveFile never overwrites an existing
// destination (belt-and-suspenders against a trash-path collision, #1).
func TestMoveFileRefusesToClobber(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.bin")
	dst := filepath.Join(dir, "dst.bin")
	writeFile(t, src, "incoming")
	writeFile(t, dst, "existing-must-not-be-clobbered")

	if err := moveFile(src, dst, ""); err == nil {
		t.Fatal("moveFile must refuse to overwrite an existing destination")
	}
	assertBytes(t, dst, "existing-must-not-be-clobbered")
	assertBytes(t, src, "incoming")
}

// TestQuarantinePartialBatchIsRecoverable proves the transactional-batch +
// partial-failure behavior (#5/#6): a forced move failure on the 2nd of 3 files
// returns an error naming the batch id, records + makes restorable the file that
// DID move, and leaves the unmoved files (and their index rows) untouched.
func TestQuarantinePartialBatchIsRecoverable(t *testing.T) {
	root := t.TempDir()
	trash := filepath.Join(t.TempDir(), "trash")
	st := newTestStore(t)

	mid, vid := 1, 1
	keeper := filepath.Join(root, "keep.safetensors")
	writeFile(t, keeper, "same")
	upsertGetID(t, st, store.LocalFile{Path: keeper, SHA256: "same", ModelID: &mid, VersionID: &vid,
		SizeBytes: 4, Status: store.LocalStatusMatched, Kind: store.LocalKindModel})

	var ids []int64
	var paths []string
	for i := 0; i < 3; i++ {
		p := filepath.Join(root, fmt.Sprintf("dup%d.safetensors", i))
		writeFile(t, p, "same")
		paths = append(paths, p)
		ids = append(ids, upsertGetID(t, st, store.LocalFile{Path: p, SHA256: "same", ModelID: &mid, VersionID: &vid,
			SizeBytes: 4, Status: store.LocalStatusMatched, CandidateReason: store.CandidateDuplicate, Kind: store.LocalKindModel}))
	}

	sc := NewScanner(st, nil, Options{ModelRoot: root, TrashDir: trash}, nil)
	var calls int
	sc.moveFn = func(src, dst, sha string) error {
		calls++
		if calls == 2 {
			return fmt.Errorf("simulated disk failure")
		}
		return moveFile(src, dst, sha)
	}

	_, err := sc.Quarantine(context.Background(), ids, true)
	if err == nil {
		t.Fatal("expected a partial-batch error")
	}
	batches, _ := st.ListQuarantineBatches()
	if len(batches) != 1 {
		t.Fatalf("want exactly 1 batch header recorded, got %d", len(batches))
	}
	bid := batches[0].ID
	if !strings.Contains(err.Error(), fmt.Sprintf("batch %d", bid)) {
		t.Fatalf("error must name batch %d so restore can target it, got: %v", bid, err)
	}

	files, _ := st.ListQuarantinedFiles(bid)
	if len(files) != 1 || files[0].OriginalPath != paths[0] {
		t.Fatalf("ledger should record ONLY the moved file %s, got %+v", paths[0], files)
	}
	if exists(paths[0]) {
		t.Error("moved file #1 must be gone from its origin")
	}
	if !exists(paths[1]) || !exists(paths[2]) {
		t.Error("files not yet moved must stay put")
	}
	if got, _ := st.GetLocalFileByPath(paths[0]); got != nil {
		t.Error("moved file #1 index row must be deleted")
	}
	if got, _ := st.GetLocalFileByPath(paths[1]); got == nil {
		t.Error("unmoved file #2 index row must remain")
	}

	// The already-moved file is restorable via the named batch.
	sc.moveFn = moveFile // let restore actually move
	res, err := sc.Restore(context.Background(), bid)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Restored) != 1 || !exists(paths[0]) {
		t.Fatalf("restore should return the one moved file, got %+v", res.Restored)
	}
	assertBytes(t, paths[0], "same")
}

// TestQuarantineInTxSafetyCatchesConcurrentKeeperRemoval proves the keep-≥1-copy
// check runs against the transaction's snapshot (#6): if the keeper is removed
// between planning and apply (as a concurrent batch would), applying the stale
// plan is refused and nothing is moved or recorded.
func TestQuarantineInTxSafetyCatchesConcurrentKeeperRemoval(t *testing.T) {
	root := t.TempDir()
	st, dupID, dupe := dupPair(t, root)
	sc := NewScanner(st, nil, Options{ModelRoot: root}, nil)

	plan, err := sc.Quarantine(context.Background(), []int64{dupID}, false) // dry-run: validated plan
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Moves) == 0 {
		t.Fatal("expected a planned move")
	}

	// Simulate a concurrent batch removing the keeper after the plan was built.
	if err := st.DeleteLocalFileByPath(filepath.Join(root, "a.safetensors")); err != nil {
		t.Fatal(err)
	}

	if _, err := sc.applyQuarantine(plan, sc.Roots()); err == nil {
		t.Fatal("apply must refuse: the in-tx snapshot shows no copy would remain")
	}
	if !exists(dupe) {
		t.Fatal("the duplicate must NOT be moved when the safety check refuses")
	}
	if batches, _ := st.ListQuarantineBatches(); len(batches) != 0 {
		t.Fatalf("a refused batch must record no batch header, got %d", len(batches))
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
