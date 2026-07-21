package store

import (
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestMigrationApplies(t *testing.T) {
	st := newTestStore(t)
	v, err := st.SchemaVersion()
	if err != nil {
		t.Fatal(err)
	}
	if v != 1 {
		t.Fatalf("schema version = %d, want 1", v)
	}
	// Re-running migrate (via a second Open on a file) is idempotent; here we
	// just confirm the core tables exist by exercising them below.
}

func TestSubscriptionCRUD(t *testing.T) {
	st := newTestStore(t)
	mid := 4201
	id, err := st.CreateSubscription(Subscription{
		Kind: KindModel, ModelID: &mid, AutoDownload: true, PollIntervalSecs: 3600,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := st.GetSubscription(id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Kind != KindModel || got.ModelID == nil || *got.ModelID != mid {
		t.Fatalf("roundtrip mismatch: %+v", got)
	}
	if !got.AutoDownload {
		t.Error("auto_download not persisted")
	}
	if got.Layout != "default" {
		t.Errorf("layout default not applied: %q", got.Layout)
	}

	// Toggle flags.
	if err := st.SetSubscriptionFlags(id, false, true); err != nil {
		t.Fatal(err)
	}
	got, _ = st.GetSubscription(id)
	if got.AutoDownload || !got.NotifyOnly {
		t.Errorf("flags not updated: %+v", got)
	}

	// List.
	subs, err := st.ListSubscriptions()
	if err != nil {
		t.Fatal(err)
	}
	if len(subs) != 1 {
		t.Fatalf("list len = %d", len(subs))
	}

	// Delete.
	if err := st.DeleteSubscription(id); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetSubscription(id); err != ErrNotFound {
		t.Errorf("expected ErrNotFound after delete, got %v", err)
	}
}

func TestSubscriptionDedupConstraint(t *testing.T) {
	st := newTestStore(t)
	mid := 100
	if _, err := st.CreateSubscription(Subscription{Kind: KindModel, ModelID: &mid, PollIntervalSecs: 3600}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateSubscription(Subscription{Kind: KindModel, ModelID: &mid, PollIntervalSecs: 3600}); err == nil {
		t.Error("expected unique-constraint failure on duplicate model subscription")
	}

	if _, err := st.CreateSubscription(Subscription{Kind: KindCreator, Username: "alice", PollIntervalSecs: 3600}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateSubscription(Subscription{Kind: KindCreator, Username: "alice", PollIntervalSecs: 3600}); err == nil {
		t.Error("expected unique-constraint failure on duplicate creator subscription")
	}
}

func TestSeenVersions(t *testing.T) {
	st := newTestStore(t)
	mid := 7
	subID, _ := st.CreateSubscription(Subscription{Kind: KindModel, ModelID: &mid, PollIntervalSecs: 3600})

	n, _ := st.CountSeen(subID)
	if n != 0 {
		t.Fatalf("fresh sub CountSeen = %d, want 0", n)
	}

	if err := st.MarkSeen(subID, 111, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkSeen(subID, 222, time.Now()); err != nil {
		t.Fatal(err)
	}
	// Idempotent re-mark.
	if err := st.MarkSeen(subID, 111, time.Time{}); err != nil {
		t.Fatal(err)
	}

	seen, _ := st.SeenVersionIDs(subID)
	if len(seen) != 2 || !seen[111] || !seen[222] {
		t.Fatalf("seen set wrong: %+v", seen)
	}
	n, _ = st.CountSeen(subID)
	if n != 2 {
		t.Fatalf("CountSeen = %d, want 2", n)
	}
}

func TestQueueLifecycle(t *testing.T) {
	st := newTestStore(t)
	id, err := st.Enqueue(QueueItem{
		ModelID: 1, VersionID: 10, FileID: 100, FileName: "m.safetensors",
		DownloadURL: "https://x/f", DestPath: "/tmp/m.safetensors",
		SHA256Expected: "ABC", SizeKB: 2048,
	})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	// Dedup guard sees it.
	exists, _ := st.ActiveQueueItemExists(10, 100)
	if !exists {
		t.Error("ActiveQueueItemExists should be true for queued item")
	}
	exists, _ = st.ActiveQueueItemExists(10, 999)
	if exists {
		t.Error("ActiveQueueItemExists should be false for unknown file")
	}

	// Claim transitions queued -> downloading and increments attempts.
	claimed, err := st.ClaimNextQueued()
	if err != nil || claimed == nil {
		t.Fatalf("claim: %v item=%v", err, claimed)
	}
	if claimed.ID != id || claimed.Status != StatusDownloading || claimed.Attempts != 1 {
		t.Fatalf("claim state wrong: %+v", claimed)
	}
	// Queue now empty for claiming.
	if next, _ := st.ClaimNextQueued(); next != nil {
		t.Errorf("expected empty queue, got %+v", next)
	}

	if err := st.UpdateProgress(id, 512); err != nil {
		t.Fatal(err)
	}
	if err := st.CompleteDownload(id, "abc", 2097152); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetQueueItem(id)
	if got.Status != StatusDone || got.SHA256Actual != "abc" || got.BytesDone != 2097152 {
		t.Fatalf("complete state wrong: %+v", got)
	}
}

func TestQueueFailAndRequeue(t *testing.T) {
	st := newTestStore(t)
	id, _ := st.Enqueue(QueueItem{ModelID: 1, VersionID: 2, FileID: 3, FileName: "f", DownloadURL: "u", DestPath: "/p"})

	if err := st.FailDownload(id, "boom", "deadbeef"); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetQueueItem(id)
	if got.Status != StatusFailed || got.LastError != "boom" {
		t.Fatalf("fail state wrong: %+v", got)
	}

	// RequeueInterrupted only touches downloading rows.
	if err := st.SetQueueStatus(id, StatusDownloading); err != nil {
		t.Fatal(err)
	}
	n, err := st.RequeueInterrupted()
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("RequeueInterrupted reset %d rows, want 1", n)
	}
	got, _ = st.GetQueueItem(id)
	if got.Status != StatusQueued {
		t.Fatalf("after requeue status = %s", got.Status)
	}
}

func TestEvents(t *testing.T) {
	st := newTestStore(t)
	mid := 5
	vid := 9
	if err := st.AddEvent(Event{Level: LevelInfo, Kind: "new_version", ModelID: &mid, VersionID: &vid, Message: "hi"}); err != nil {
		t.Fatal(err)
	}
	evs, err := st.RecentEvents(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || evs[0].Message != "hi" || evs[0].ModelID == nil || *evs[0].ModelID != 5 {
		t.Fatalf("event roundtrip wrong: %+v", evs)
	}
}
