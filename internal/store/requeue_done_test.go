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

// TestRequeueFailedReenqueuesAndFindActive proves the failed-repair retry
// primitive behind `verify --repair`: a terminally 'failed' row (a prior repair
// whose re-download failed) can be transitioned back to 'queued' and re-claimed,
// and FindActiveQueueItem surfaces the active row for a (version_id, file_id) so
// the backfill path can inspect its status/dest_path.
func TestRequeueFailedReenqueuesAndFindActive(t *testing.T) {
	st := newTestStore(t)

	id, inserted, err := st.Enqueue(QueueItem{
		ModelID: 3, VersionID: 300, FileID: 700, FileName: "v.safetensors",
		DownloadURL: "http://example/v", DestPath: "/models/v.safetensors",
		Status: StatusQueued, SHA256Expected: "abc",
	})
	if err != nil || !inserted {
		t.Fatalf("enqueue: inserted=%v err=%v", inserted, err)
	}

	// A 'failed' row is NOT active, so FindActiveQueueItem returns nil for it.
	if err := st.FailDownload(id, "boom", ""); err != nil {
		t.Fatalf("fail: %v", err)
	}
	if active, err := st.FindActiveQueueItem(300, 700); err != nil || active != nil {
		t.Fatalf("failed row must not be active, got %+v err=%v", active, err)
	}

	// RequeueFailed transitions failed→queued, resetting per-attempt state.
	if err := st.RequeueFailed(id); err != nil {
		t.Fatalf("RequeueFailed: %v", err)
	}
	got, _ := st.GetQueueItem(id)
	if got.Status != StatusQueued {
		t.Errorf("status after RequeueFailed = %s, want queued", got.Status)
	}
	if got.Attempts != 0 || got.LastError != "" {
		t.Errorf("per-attempt state must reset, attempts=%d lastErr=%q", got.Attempts, got.LastError)
	}

	// Now it is active again and re-claimable.
	active, err := st.FindActiveQueueItem(300, 700)
	if err != nil || active == nil || active.ID != id {
		t.Fatalf("re-enqueued row must be active, got %+v err=%v", active, err)
	}
	claimed, err := st.ClaimNextQueued()
	if err != nil || claimed == nil || claimed.ID != id {
		t.Fatalf("re-enqueued row must be claimable, got %+v err=%v", claimed, err)
	}

	// RequeueFailed is a no-op on a non-failed row.
	if err := st.RequeueFailed(id); err != ErrNotFound {
		t.Errorf("RequeueFailed on a downloading row should be ErrNotFound, got %v", err)
	}
}

// TestClaimNextQueuedForIDsIsScoped proves the id-scoped claim behind
// `verify --repair`: it only claims a row whose id is in the given set, so an
// unrelated due queued row is never touched.
func TestClaimNextQueuedForIDsIsScoped(t *testing.T) {
	st := newTestStore(t)
	repairID, _, err := st.Enqueue(QueueItem{
		ModelID: 1, VersionID: 100, FileID: 500, FileName: "a.safetensors",
		DownloadURL: "http://example/a", DestPath: "/models/a.safetensors", Status: StatusQueued,
	})
	if err != nil {
		t.Fatalf("enqueue repair row: %v", err)
	}
	otherID, _, err := st.Enqueue(QueueItem{
		ModelID: 2, VersionID: 200, FileID: 600, FileName: "b.safetensors",
		DownloadURL: "http://example/b", DestPath: "/models/b.safetensors", Status: StatusQueued,
	})
	if err != nil {
		t.Fatalf("enqueue other row: %v", err)
	}

	// Scoped to [repairID]: claims repairID, then nil — never the other row.
	claimed, err := st.ClaimNextQueuedForIDs([]int64{repairID})
	if err != nil || claimed == nil || claimed.ID != repairID {
		t.Fatalf("scoped claim should return the repair row, got %+v err=%v", claimed, err)
	}
	next, err := st.ClaimNextQueuedForIDs([]int64{repairID})
	if err != nil || next != nil {
		t.Fatalf("scoped claim must not spill to the other row, got %+v err=%v", next, err)
	}
	// The other row is untouched (still queued, never claimed).
	other, _ := st.GetQueueItem(otherID)
	if other.Status != StatusQueued || other.Attempts != 0 {
		t.Errorf("unrelated row must be untouched, status=%s attempts=%d", other.Status, other.Attempts)
	}
	// An empty id set matches nothing.
	if it, err := st.ClaimNextQueuedForIDs(nil); err != nil || it != nil {
		t.Errorf("empty id set must match nothing, got %+v err=%v", it, err)
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
