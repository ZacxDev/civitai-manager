package store

import (
	"testing"
	"time"
)

func intp(i int) *int { return &i }

func TestMigration0003AddsLibraryColumnsAndTables(t *testing.T) {
	st := newTestStore(t)

	// The new local_files columns must be queryable.
	if _, err := st.DB().Exec(`INSERT INTO local_files
		(path, sha256, size_bytes, mtime, status, candidate_reason, kind)
		VALUES ('p', 'h', 1, '2026-01-01T00:00:00Z', 'matched', 'duplicate', 'model')`); err != nil {
		t.Fatalf("insert with new columns: %v", err)
	}
	// The quarantine tables must exist.
	for _, q := range []string{
		`SELECT COUNT(*) FROM quarantine_batches`,
		`SELECT COUNT(*) FROM quarantined_files`,
	} {
		if _, err := st.DB().Exec(q); err != nil {
			t.Fatalf("query %q: %v", q, err)
		}
	}
}

func TestUpsertLocalFileRoundTripsNewFields(t *testing.T) {
	st := newTestStore(t)
	mtime := time.Date(2026, 3, 4, 5, 6, 7, 123456789, time.UTC)
	lf := LocalFile{
		Path: "/models/x.safetensors", SHA256: "abc", AutoV2: "av2",
		ModelID: intp(10), VersionID: intp(20), SizeBytes: 4096, Mtime: &mtime,
		Status: LocalStatusMatched, Kind: LocalKindModel,
	}
	if err := st.UpsertLocalFile(lf); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetLocalFileByPath(lf.Path)
	if err != nil || got == nil {
		t.Fatalf("reload: %v", err)
	}
	if got.SHA256 != "abc" || got.Status != LocalStatusMatched || *got.ModelID != 10 || *got.VersionID != 20 {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
	if got.Mtime == nil || !got.Mtime.Equal(mtime) {
		t.Fatalf("mtime round-trip lost precision: got %v want %v", got.Mtime, mtime)
	}
	if got.ID == 0 {
		t.Fatal("ID (rowid) should be populated on read")
	}
}

func TestCandidateQueriesAndClear(t *testing.T) {
	st := newTestStore(t)
	seed := func(path, reason string) int64 {
		lf := LocalFile{Path: path, SHA256: "h", Status: LocalStatusMatched, Kind: LocalKindModel}
		if err := st.UpsertLocalFile(lf); err != nil {
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
	seed("/a", CandidateSuperseded)
	seed("/b", CandidateDuplicate)
	seed("/c", "") // not a candidate

	all, _ := st.ListCandidates("")
	if len(all) != 2 {
		t.Fatalf("all candidates = %d, want 2", len(all))
	}
	sup, _ := st.ListCandidates(CandidateSuperseded)
	if len(sup) != 1 || sup[0].CandidateReason != CandidateSuperseded {
		t.Fatalf("superseded filter = %+v", sup)
	}
	// SetCandidateReason(superseded) keeps is_superseded in sync.
	if !sup[0].IsSuperseded {
		t.Error("is_superseded should track the superseded candidate reason")
	}

	if err := st.ClearCandidates(); err != nil {
		t.Fatal(err)
	}
	if left, _ := st.ListCandidates(""); len(left) != 0 {
		t.Fatalf("ClearCandidates left %d flags", len(left))
	}
}

func TestActiveDownloadForDest(t *testing.T) {
	st := newTestStore(t)
	dest := "/models/dl.safetensors"
	if ok, _ := st.ActiveDownloadForDest(dest); ok {
		t.Fatal("no queue row yet; should be inactive")
	}
	id, _, err := st.Enqueue(QueueItem{ModelID: 1, VersionID: 1, FileID: 1, FileName: "dl",
		DownloadURL: "http://x", DestPath: dest, Status: StatusQueued})
	if err != nil {
		t.Fatal(err)
	}
	if ok, _ := st.ActiveDownloadForDest(dest); !ok {
		t.Fatal("queued row should count as active")
	}
	// A finished download no longer gates the .part as active.
	if err := st.CompleteDownload(id, "sha", 1); err != nil {
		t.Fatal(err)
	}
	if ok, _ := st.ActiveDownloadForDest(dest); ok {
		t.Fatal("completed download should not be active")
	}
}

func TestQuarantineBatchLifecycle(t *testing.T) {
	st := newTestStore(t)
	batchID, err := st.CreateQuarantineBatch("/trash/b1", "", "duplicate")
	if err != nil {
		t.Fatal(err)
	}
	fid, err := st.AddQuarantinedFile(QuarantinedFile{
		BatchID: batchID, OriginalPath: "/models/x", TrashPath: "/trash/b1/x",
		Reason: "duplicate", SHA256: "h", SizeBytes: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	files, err := st.ListQuarantinedFiles(batchID)
	if err != nil || len(files) != 1 || files[0].ID != fid {
		t.Fatalf("list quarantined files = %+v (err %v)", files, err)
	}

	batches, _ := st.ListQuarantineBatches()
	if len(batches) != 1 || batches[0].Restored() {
		t.Fatalf("expected 1 un-restored batch, got %+v", batches)
	}

	if err := st.MarkFileRestored(fid); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkBatchRestored(batchID); err != nil {
		t.Fatal(err)
	}
	b, err := st.GetQuarantineBatch(batchID)
	if err != nil {
		t.Fatal(err)
	}
	if !b.Restored() {
		t.Fatal("batch should read back restored")
	}
	files, _ = st.ListQuarantinedFiles(batchID)
	if files[0].RestoredAt == nil {
		t.Fatal("file should read back restored")
	}
}

func TestGetQuarantineBatchNotFound(t *testing.T) {
	st := newTestStore(t)
	if _, err := st.GetQuarantineBatch(999); err != ErrNotFound {
		t.Fatalf("missing batch err = %v, want ErrNotFound", err)
	}
}
