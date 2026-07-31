package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

// TestMigration0016AppliesOnPopulatedDB applies 0016 to a database that is already
// at 0015 AND already carries generation rows, then confirms the old rows survive
// untouched, the three new columns exist and read NULL on them, and re-opening is
// idempotent.
//
// A migration that works on an EMPTY file can still fail on a populated one — the
// SQLite rule that ALTER TABLE ADD COLUMN may only carry a NULL default is exactly
// the kind of thing a fresh-DB test never exercises.
func TestMigration0016AppliesOnPopulatedDB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "populated.db")

	db := openStoreAtVersion(t, path, 15)
	if _, err := db.Exec(`INSERT INTO workflows (name, format, graph, source, created_at, updated_at)
		VALUES ('wf', 'api', '{}', 'imported', ?, ?)`, nowRFC3339(), nowRFC3339()); err != nil {
		t.Fatalf("seed workflow: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO generations
		(workflow_id, workflow_name, prompt_id, status, image_count, created_at)
		VALUES (1, 'wf', 'p-legacy', 'ready', 1, ?)`, nowRFC3339()); err != nil {
		t.Fatalf("seed generation: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close raw db: %v", err)
	}

	st, err := Open(path)
	if err != nil {
		t.Fatalf("open (migrate to head): %v", err)
	}
	defer func() { _ = st.Close() }()

	v, err := st.SchemaVersion()
	if err != nil {
		t.Fatalf("schema version: %v", err)
	}
	if v != 18 {
		t.Fatalf("schema version after migrate = %d, want 18", v)
	}

	// The pre-existing row survived, and its new columns are NULL — not 0, which
	// would read as a real (impossible) 1-based batch position.
	var (
		promptID   string
		batchID    sql.NullString
		batchIndex sql.NullInt64
		batchTotal sql.NullInt64
	)
	if err := st.db.QueryRow(
		`SELECT prompt_id, batch_id, batch_index, batch_total FROM generations WHERE prompt_id = 'p-legacy'`,
	).Scan(&promptID, &batchID, &batchIndex, &batchTotal); err != nil {
		t.Fatalf("read migrated row: %v", err)
	}
	if batchID.Valid || batchIndex.Valid || batchTotal.Valid {
		t.Errorf("pre-0016 row has non-NULL batch columns: id=%v index=%v total=%v",
			batchID, batchIndex, batchTotal)
	}

	// The index the batch view scans exists.
	var idxName string
	if err := st.db.QueryRow(
		`SELECT name FROM sqlite_master WHERE type='index' AND name='ix_generations_batch'`,
	).Scan(&idxName); err != nil {
		t.Fatalf("ix_generations_batch missing: %v", err)
	}

	// Re-opening applies nothing further.
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	st2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = st2.Close() }()
	v2, err := st2.SchemaVersion()
	if err != nil {
		t.Fatalf("schema version 2: %v", err)
	}
	if v2 != 18 {
		t.Fatalf("schema version after reopen = %d, want 18", v2)
	}
}

// insertBatchGeneration inserts one captured generation belonging to a batch.
func insertBatchGeneration(t *testing.T, st *Store, wfID int64, batchID string, idx, total int) int64 {
	t.Helper()
	id, err := st.InsertGeneration(context.Background(), &Generation{
		WorkflowID:   &wfID,
		WorkflowName: "wf",
		PromptID:     "p-" + batchID + "-" + strings.Repeat("i", idx),
		BatchID:      batchID,
		BatchIndex:   idx,
		BatchTotal:   total,
	}, []GenerationImage{{Idx: 0, RelPath: "a/b.png", Filename: "b.png", SizeBytes: 10}})
	if err != nil {
		t.Fatalf("insert batch generation: %v", err)
	}
	return id
}

// TestGenerationBatchColumnsRoundTrip pins that the batch identity written at
// capture time comes back out of every read path — Get, List and ListByBatch.
// A column present in the INSERT but missing from a scan's column list is a
// silent mis-alignment, not an error.
func TestGenerationBatchColumnsRoundTrip(t *testing.T) {
	st := newGenTestStore(t)
	ctx := context.Background()
	wfID := insertTestWorkflow(t, st, "wf")

	id := insertBatchGeneration(t, st, wfID, "bat01", 3, 8)

	gen, _, err := st.GetGeneration(ctx, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if gen.BatchID != "bat01" || gen.BatchIndex != 3 || gen.BatchTotal != 8 {
		t.Fatalf("GetGeneration batch = (%q,%d,%d), want (bat01,3,8)",
			gen.BatchID, gen.BatchIndex, gen.BatchTotal)
	}

	list, err := st.ListGenerations(ctx, ListGenerationsOpts{Limit: 10})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("list len = %d, want 1", len(list))
	}
	if list[0].BatchID != "bat01" || list[0].BatchIndex != 3 || list[0].BatchTotal != 8 {
		t.Errorf("ListGenerations batch = (%q,%d,%d), want (bat01,3,8)",
			list[0].BatchID, list[0].BatchIndex, list[0].BatchTotal)
	}
	// The rest of the row must still be intact — a mis-aligned scan usually shows
	// up as a neighbouring column, not as an error.
	if list[0].WorkflowName != "wf" || list[0].Status != GenerationStatusReady || list[0].ImageCount != 1 {
		t.Errorf("neighbouring columns corrupted: name=%q status=%q count=%d",
			list[0].WorkflowName, list[0].Status, list[0].ImageCount)
	}
	if list[0].FirstImageID == 0 {
		t.Error("FirstImageID not populated — the extra thumbnail column mis-aligned")
	}
}

// TestGenerationWithoutBatchStoresNull pins that an ordinary single run is
// indistinguishable from every pre-0016 row: all three columns NULL.
func TestGenerationWithoutBatchStoresNull(t *testing.T) {
	st := newGenTestStore(t)
	ctx := context.Background()
	wfID := insertTestWorkflow(t, st, "wf")

	id, err := st.InsertGeneration(ctx, &Generation{
		WorkflowID: &wfID, WorkflowName: "wf", PromptID: "solo",
	}, []GenerationImage{{Idx: 0, RelPath: "a/s.png", Filename: "s.png", SizeBytes: 1}})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	var batchID sql.NullString
	var batchIndex, batchTotal sql.NullInt64
	if err := st.db.QueryRow(
		`SELECT batch_id, batch_index, batch_total FROM generations WHERE id = ?`, id,
	).Scan(&batchID, &batchIndex, &batchTotal); err != nil {
		t.Fatalf("read: %v", err)
	}
	if batchID.Valid || batchIndex.Valid || batchTotal.Valid {
		t.Errorf("single run stored non-NULL batch columns: %v %v %v",
			batchID, batchIndex, batchTotal)
	}
}

// TestListGenerationsByBatch pins the ordering, the isolation between batches,
// and — the part that matters for a hostile URL — that an unknown or malformed id
// yields zero rows and NO error.
func TestListGenerationsByBatch(t *testing.T) {
	st := newGenTestStore(t)
	ctx := context.Background()
	wfID := insertTestWorkflow(t, st, "wf")

	// Inserted out of order on purpose: the query must order by batch_index, not by
	// insertion order or created_at (all three rows share a second).
	insertBatchGeneration(t, st, wfID, "batA", 3, 3)
	insertBatchGeneration(t, st, wfID, "batA", 1, 3)
	insertBatchGeneration(t, st, wfID, "batA", 2, 3)
	insertBatchGeneration(t, st, wfID, "batB", 1, 2)

	got, err := st.ListGenerationsByBatch(ctx, "batA")
	if err != nil {
		t.Fatalf("list by batch: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3 (batB must not leak in)", len(got))
	}
	for i, gen := range got {
		if gen.BatchIndex != i+1 {
			t.Errorf("row %d has batch_index %d, want %d (not ordered by batch_index)",
				i, gen.BatchIndex, i+1)
		}
		if gen.BatchID != "batA" {
			t.Errorf("row %d belongs to batch %q", i, gen.BatchID)
		}
	}

	for _, id := range []string{"nosuchbatch", "", "../../etc/passwd", "a'; DROP TABLE generations;--",
		strings.Repeat("x", 65), "has space", "quote\"d"} {
		rows, err := st.ListGenerationsByBatch(ctx, id)
		if err != nil {
			t.Errorf("id %q returned an error: %v (must be zero rows, never an error)", id, err)
		}
		if len(rows) != 0 {
			t.Errorf("id %q returned %d rows", id, len(rows))
		}
	}
	// The hostile ids above must not have destroyed anything.
	if rows, _ := st.ListGenerationsByBatch(ctx, "batA"); len(rows) != 3 {
		t.Fatalf("batA lost rows after the hostile-id pass: %d", len(rows))
	}
}

func TestValidBatchID(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"abc123", true},
		{"A-b_c", true},
		{strings.Repeat("x", 64), true},
		{strings.Repeat("x", 65), false},
		{"", false},
		{"a b", false},
		{"a/b", false},
		{"a.b", false},
		{"a'b", false},
		{"a\x00b", false},
		{"../x", false},
	} {
		if got := ValidBatchID(tc.in); got != tc.want {
			t.Errorf("ValidBatchID(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
