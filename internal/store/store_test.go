package store

import (
	"database/sql"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
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
	if v != 5 {
		t.Fatalf("schema version = %d, want 5", v)
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
	id, _, err := st.Enqueue(QueueItem{
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
	id, _, _ := st.Enqueue(QueueItem{ModelID: 1, VersionID: 2, FileID: 3, FileName: "f", DownloadURL: "u", DestPath: "/p"})

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

// TestRequeueCanceledUndoesAttempt proves the finding-#1 store primitive: a
// cancelled (graceful-shutdown) download returns to queued WITHOUT counting the
// claim's attempt increment.
func TestRequeueCanceledUndoesAttempt(t *testing.T) {
	st := newTestStore(t)
	id, _, _ := st.Enqueue(QueueItem{ModelID: 1, VersionID: 2, FileID: 3, FileName: "f", DownloadURL: "u", DestPath: "/p"})

	claimed, err := st.ClaimNextQueued()
	if err != nil {
		t.Fatal(err)
	}
	if claimed.Attempts != 1 {
		t.Fatalf("claim should increment attempts to 1, got %d", claimed.Attempts)
	}

	if err := st.RequeueCanceled(id); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetQueueItem(id)
	if got.Status != StatusQueued {
		t.Fatalf("after cancel-requeue status = %s, want queued", got.Status)
	}
	if got.Attempts != 0 {
		t.Fatalf("cancelled attempt must be undone, attempts = %d want 0", got.Attempts)
	}
}

// TestClaimNextQueuedNotBeforeGate proves the anti-stampede claim gate: a row
// whose not_before is in the future is skipped (the worker moves to the next
// eligible row), while NULL and past not_before rows are claimable.
func TestClaimNextQueuedNotBeforeGate(t *testing.T) {
	st := newTestStore(t)
	future := time.Now().UTC().Add(time.Hour)
	past := time.Now().UTC().Add(-time.Minute)

	// Inserted future-first (lowest id) to prove the gate skips it rather than
	// blocking the whole queue.
	futID, _, _ := st.Enqueue(QueueItem{ModelID: 1, VersionID: 1, FileID: 1, FileName: "fut", DownloadURL: "u", DestPath: "/fut", NotBefore: &future})
	pastID, _, _ := st.Enqueue(QueueItem{ModelID: 1, VersionID: 2, FileID: 2, FileName: "past", DownloadURL: "u", DestPath: "/past", NotBefore: &past})
	nilID, _, _ := st.Enqueue(QueueItem{ModelID: 1, VersionID: 3, FileID: 3, FileName: "nil", DownloadURL: "u", DestPath: "/nil"})

	// First claim skips the not-yet-due future row and takes the oldest eligible.
	it, err := st.ClaimNextQueued()
	if err != nil {
		t.Fatal(err)
	}
	if it == nil || it.ID != pastID {
		t.Fatalf("expected past-gated row %d claimed first, got %+v", pastID, it)
	}
	// Then the ungated (NULL) row.
	it, _ = st.ClaimNextQueued()
	if it == nil || it.ID != nilID {
		t.Fatalf("expected NULL not_before row %d claimed next, got %+v", nilID, it)
	}
	// Only the future-gated row remains: not claimable yet.
	it, _ = st.ClaimNextQueued()
	if it != nil {
		t.Fatalf("future-gated row %d must not be claimed before its time, got %+v", futID, it)
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

// TestEnqueueDedupsActiveDuplicate proves the item-#2 fix: enqueuing the same
// (version_id, file_id) twice while the first is still ACTIVE inserts only ONE
// row, and the second call reports it was NOT inserted (the atomic ON CONFLICT
// replacement for the old non-atomic check-then-insert).
func TestEnqueueDedupsActiveDuplicate(t *testing.T) {
	st := newTestStore(t)
	item := QueueItem{ModelID: 1, VersionID: 7, FileID: 70, FileName: "f", DownloadURL: "u", DestPath: "/p"}

	id, inserted, err := st.Enqueue(item)
	if err != nil || !inserted || id == 0 {
		t.Fatalf("first enqueue: id=%d inserted=%v err=%v", id, inserted, err)
	}

	id2, inserted2, err := st.Enqueue(item)
	if err != nil {
		t.Fatalf("second enqueue errored: %v", err)
	}
	if inserted2 {
		t.Errorf("duplicate active enqueue must be a no-op, but inserted=true id=%d", id2)
	}
	if id2 != 0 {
		t.Errorf("skipped enqueue must return id 0, got %d", id2)
	}

	q, _ := st.ListQueue()
	if len(q) != 1 {
		t.Fatalf("exactly one row must remain after a duplicate enqueue, got %d", len(q))
	}
}

// TestEnqueueConcurrentSameFileYieldsOneRow proves the concurrent-enqueue race
// from item #2 is closed: many goroutines racing to enqueue the SAME
// (version_id, file_id) end with exactly ONE row and exactly ONE reported
// insertion — the partial-unique index makes the insert atomic, unlike the old
// check-then-insert.
func TestEnqueueConcurrentSameFileYieldsOneRow(t *testing.T) {
	st := newTestStore(t)
	item := QueueItem{ModelID: 1, VersionID: 99, FileID: 990, FileName: "f", DownloadURL: "u", DestPath: "/p"}

	const n = 16
	var wg sync.WaitGroup
	var inserts atomic.Int64
	errs := make(chan error, n)
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			_, inserted, err := st.Enqueue(item)
			if err != nil {
				errs <- err
				return
			}
			if inserted {
				inserts.Add(1)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent enqueue errored: %v", err)
	}

	if got := inserts.Load(); got != 1 {
		t.Errorf("exactly one concurrent enqueue should report inserted, got %d", got)
	}
	q, _ := st.ListQueue()
	if len(q) != 1 {
		t.Fatalf("exactly one row must exist after concurrent enqueues, got %d", len(q))
	}
}

// TestEnqueueDoneBlocksReEnqueue proves a 'done' row (an active status) blocks a
// re-enqueue — the invariant backfill idempotency relies on.
func TestEnqueueDoneBlocksReEnqueue(t *testing.T) {
	st := newTestStore(t)
	item := QueueItem{ModelID: 1, VersionID: 11, FileID: 110, FileName: "f", DownloadURL: "u", DestPath: "/p"}
	id, inserted, err := st.Enqueue(item)
	if err != nil || !inserted {
		t.Fatalf("enqueue: inserted=%v err=%v", inserted, err)
	}
	if err := st.CompleteDownload(id, "abc", 1); err != nil {
		t.Fatal(err)
	}
	_, inserted2, err := st.Enqueue(item)
	if err != nil {
		t.Fatal(err)
	}
	if inserted2 {
		t.Error("enqueue over a done row must be skipped")
	}
}

// TestEnqueueAllowsRetryAfterFailed proves a FAILED row (a non-active, terminal
// status) does NOT block a fresh enqueue: the partial index only spans the
// active statuses, so a retry after a terminal failure inserts a new row.
func TestEnqueueAllowsRetryAfterFailed(t *testing.T) {
	st := newTestStore(t)
	item := QueueItem{ModelID: 1, VersionID: 8, FileID: 80, FileName: "f", DownloadURL: "u", DestPath: "/p"}

	id, inserted, err := st.Enqueue(item)
	if err != nil || !inserted {
		t.Fatalf("first enqueue: inserted=%v err=%v", inserted, err)
	}
	if err := st.FailDownload(id, "boom", ""); err != nil {
		t.Fatal(err)
	}

	id2, inserted2, err := st.Enqueue(item)
	if err != nil {
		t.Fatalf("retry enqueue errored: %v", err)
	}
	if !inserted2 {
		t.Fatal("retry after a failed row must insert a new row")
	}
	if id2 == id {
		t.Errorf("retry must be a new row, got the same id %d", id2)
	}
}

// applyMigrationsUpTo opens a fresh temp-file DB and applies the embedded
// migrations 0001..maxVersion (inclusive) WITHOUT the store's automatic
// run-to-latest, so a test can seed rows at an intermediate schema version and
// then apply a later migration against a populated DB.
func applyMigrationsUpTo(t *testing.T, maxVersion int) *sql.DB {
	t.Helper()
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)

	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	var files []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".sql") {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)
	for _, name := range files {
		v, err := migrationVersion(name)
		if err != nil {
			t.Fatal(err)
		}
		if v > maxVersion {
			continue
		}
		b, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(b)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
	}
	return db
}

// TestMigration0004DedupesPreexistingActiveDuplicates proves the migration is
// SAFE on a populated DB that already contains active-status duplicates: it
// dedupes them (keeping the most-progressed active row, leaving terminal rows
// untouched) BEFORE creating the unique index, so the CREATE cannot fail.
func TestMigration0004DedupesPreexistingActiveDuplicates(t *testing.T) {
	db := applyMigrationsUpTo(t, 3)

	ins := func(vid, fid int, status string) {
		if _, err := db.Exec(`INSERT INTO download_queue
			(model_id, version_id, file_id, file_name, download_url, dest_path, status, created_at, updated_at)
			VALUES (1, ?, ?, 'f', 'u', '/p', ?, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
			vid, fid, status); err != nil {
			t.Fatalf("seed (%d,%d,%s): %v", vid, fid, status, err)
		}
	}
	// (10,100): three active duplicates in different statuses + one terminal failed.
	ins(10, 100, "queued")
	ins(10, 100, "downloading")
	ins(10, 100, "done")
	ins(10, 100, "failed")
	// (20,200): two identical queued duplicates.
	ins(20, 200, "queued")
	ins(20, 200, "queued")

	b, err := migrationsFS.ReadFile("migrations/0004_queue_active_unique.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(b)); err != nil {
		t.Fatalf("migration 0004 must apply cleanly on a populated DB with active duplicates: %v", err)
	}

	countActive := func(vid, fid int) int {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM download_queue
			WHERE version_id=? AND file_id=? AND status IN ('queued','downloading','done')`, vid, fid).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	if n := countActive(10, 100); n != 1 {
		t.Fatalf("(10,100) active rows after dedupe = %d, want 1", n)
	}
	var kept string
	if err := db.QueryRow(`SELECT status FROM download_queue
		WHERE version_id=10 AND file_id=100 AND status IN ('queued','downloading','done')`).Scan(&kept); err != nil {
		t.Fatal(err)
	}
	if kept != "done" {
		t.Errorf("dedupe should keep the most-progressed active row 'done', kept %q", kept)
	}
	// The terminal 'failed' row must be untouched.
	var failed int
	if err := db.QueryRow(`SELECT COUNT(*) FROM download_queue
		WHERE version_id=10 AND file_id=100 AND status='failed'`).Scan(&failed); err != nil {
		t.Fatal(err)
	}
	if failed != 1 {
		t.Errorf("terminal failed row must survive dedupe, got %d", failed)
	}
	if n := countActive(20, 200); n != 1 {
		t.Fatalf("(20,200) active rows after dedupe = %d, want 1", n)
	}

	// The unique index now blocks a raw duplicate active insert.
	if _, err := db.Exec(`INSERT INTO download_queue
		(model_id, version_id, file_id, file_name, download_url, dest_path, status, created_at, updated_at)
		VALUES (1, 20, 200, 'f', 'u', '/p', 'queued', 't', 't')`); err == nil {
		t.Fatal("expected a UNIQUE violation inserting a duplicate active row after 0004")
	}
}

// TestMigration0005AppliesOnPopulatedDB proves migration 0005 (the per-file
// scan_root column) applies cleanly on a DB already populated at the pre-0005
// schema: the existing rows get the NOT NULL DEFAULT ” backfill and the new
// column is readable/writable.
func TestMigration0005AppliesOnPopulatedDB(t *testing.T) {
	db := applyMigrationsUpTo(t, 4)

	// Seed a local_files row at the pre-0005 schema (no scan_root column exists yet).
	if _, err := db.Exec(`INSERT INTO local_files
		(path, sha256, size_bytes, status, candidate_reason, kind, matched_at)
		VALUES ('/models/a.safetensors', 'abc', 10, 'matched', 'duplicate', 'model', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("seed pre-0005 row: %v", err)
	}

	b, err := migrationsFS.ReadFile("migrations/0005_scan_root.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(b)); err != nil {
		t.Fatalf("migration 0005 must apply cleanly on a populated DB: %v", err)
	}

	// The pre-existing row backfills to the empty-string default.
	var sr string
	if err := db.QueryRow(`SELECT scan_root FROM local_files WHERE path='/models/a.safetensors'`).Scan(&sr); err != nil {
		t.Fatalf("read scan_root: %v", err)
	}
	if sr != "" {
		t.Errorf("pre-0005 row should default scan_root to '', got %q", sr)
	}

	// The new column is writable.
	if _, err := db.Exec(`UPDATE local_files SET scan_root='/extra' WHERE path='/models/a.safetensors'`); err != nil {
		t.Fatalf("update scan_root: %v", err)
	}
	if err := db.QueryRow(`SELECT scan_root FROM local_files WHERE path='/models/a.safetensors'`).Scan(&sr); err != nil {
		t.Fatal(err)
	}
	if sr != "/extra" {
		t.Errorf("scan_root after update = %q, want /extra", sr)
	}
}

// TestUpsertLocalFilePreservesScanRootOnEmpty proves the ON CONFLICT rule: a
// writer that upserts the same path WITHOUT a scan_root (the download worker) must
// not clobber a scan_root a prior scan recorded — while a writer that DOES set one
// updates it.
func TestUpsertLocalFilePreservesScanRootOnEmpty(t *testing.T) {
	st := newTestStore(t)
	mid, vid := 1, 1

	// A scan records the file under /extra.
	if err := st.UpsertLocalFile(LocalFile{
		Path: "/extra/x.safetensors", SHA256: "h", ModelID: &mid, VersionID: &vid,
		Status: LocalStatusMatched, Kind: LocalKindModel, ScanRoot: "/extra",
	}); err != nil {
		t.Fatal(err)
	}
	// The download-worker-style upsert (no ScanRoot) must preserve /extra.
	if err := st.UpsertLocalFile(LocalFile{
		Path: "/extra/x.safetensors", SHA256: "h", ModelID: &mid, VersionID: &vid,
		Status: LocalStatusMatched, Kind: LocalKindModel,
	}); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetLocalFileByPath("/extra/x.safetensors")
	if err != nil || got == nil {
		t.Fatalf("reload: %v", err)
	}
	if got.ScanRoot != "/extra" {
		t.Errorf("a blank-scan_root upsert clobbered the recorded root: got %q, want /extra", got.ScanRoot)
	}
	// A later upsert that DOES set a scan_root updates it.
	if err := st.UpsertLocalFile(LocalFile{
		Path: "/extra/x.safetensors", SHA256: "h", ModelID: &mid, VersionID: &vid,
		Status: LocalStatusMatched, Kind: LocalKindModel, ScanRoot: "/other",
	}); err != nil {
		t.Fatal(err)
	}
	got, _ = st.GetLocalFileByPath("/extra/x.safetensors")
	if got.ScanRoot != "/other" {
		t.Errorf("a non-empty scan_root upsert should update it: got %q, want /other", got.ScanRoot)
	}
}
