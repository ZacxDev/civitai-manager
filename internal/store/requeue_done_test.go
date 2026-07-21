package store

import (
	"testing"
)

// TestRequeueDoneReenqueuesWithoutTrippingUniqueIndex proves the re-enqueue
// primitive behind `verify --repair`: a completed ('done') row can be
// transitioned back to 'queued' — resetting its per-attempt download state —
// without violating the ux_dlq_active partial-unique index (done→queued stays
// inside the active status set, and a row never conflicts with itself on UPDATE).
func TestRequeueDoneReenqueuesWithoutTrippingUniqueIndex(t *testing.T) {
	st := newTestStore(t)

	id, inserted, err := st.Enqueue(QueueItem{
		ModelID: 1, VersionID: 100, FileID: 500, FileName: "v1.safetensors",
		DownloadURL: "http://example/file", DestPath: "/models/v1.safetensors",
		Status: StatusQueued, SHA256Expected: "abc",
	})
	if err != nil || !inserted {
		t.Fatalf("enqueue: inserted=%v err=%v", inserted, err)
	}
	// Drive it to done with a recorded hash + bytes.
	if err := st.CompleteDownload(id, "abcactual", 2048); err != nil {
		t.Fatalf("complete: %v", err)
	}
	got, _ := st.GetQueueItem(id)
	if got.Status != StatusDone {
		t.Fatalf("precondition: want done, got %s", got.Status)
	}

	// Re-enqueue the done row.
	if err := st.RequeueDone(id); err != nil {
		t.Fatalf("RequeueDone: %v", err)
	}
	got, _ = st.GetQueueItem(id)
	if got.Status != StatusQueued {
		t.Errorf("status after RequeueDone = %s, want queued", got.Status)
	}
	if got.BytesDone != 0 {
		t.Errorf("bytes_done must be reset, got %d", got.BytesDone)
	}
	if got.SHA256Actual != "" {
		t.Errorf("sha256_actual must be reset, got %q", got.SHA256Actual)
	}
	if got.Attempts != 0 {
		t.Errorf("attempts must be reset, got %d", got.Attempts)
	}

	// The row is claimable again exactly once (proves no duplicate/blocked row and
	// that the active-status invariant still holds).
	claimed, err := st.ClaimNextQueued()
	if err != nil {
		t.Fatalf("claim after requeue: %v", err)
	}
	if claimed == nil || claimed.ID != id {
		t.Fatalf("re-enqueued row must be claimable, got %+v", claimed)
	}

	// There must still be exactly one row for this (version_id, file_id) — the
	// re-enqueue reused the row rather than creating a second one.
	all, _ := st.ListQueue()
	if len(all) != 1 {
		t.Errorf("want exactly one queue row after re-enqueue, got %d", len(all))
	}
}

// TestRequeueDoneOnlyActsOnDoneRows proves the status guard: RequeueDone is a
// no-op (ErrNotFound) on a row that is not 'done', so it can never disturb an
// in-flight (queued/downloading) download.
func TestRequeueDoneOnlyActsOnDoneRows(t *testing.T) {
	st := newTestStore(t)
	id, _, err := st.Enqueue(QueueItem{
		ModelID: 2, VersionID: 200, FileID: 600, FileName: "x.safetensors",
		DownloadURL: "http://example/x", DestPath: "/models/x.safetensors",
		Status: StatusQueued,
	})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if err := st.RequeueDone(id); err != ErrNotFound {
		t.Errorf("RequeueDone on a queued row should be ErrNotFound, got %v", err)
	}
	got, _ := st.GetQueueItem(id)
	if got.Status != StatusQueued {
		t.Errorf("queued row must be untouched, got %s", got.Status)
	}
}
