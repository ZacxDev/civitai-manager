package store

import (
	"context"
	"testing"
	"time"
)

// mkSizedGeneration inserts a generation whose single image has the given size,
// with an explicit created_at so age ordering is deterministic.
func mkSizedGeneration(t *testing.T, st *Store, prompt string, size int64, created time.Time) int64 {
	t.Helper()
	id, err := st.InsertGeneration(context.Background(),
		&Generation{PromptID: prompt, CreatedAt: created},
		[]GenerationImage{{Idx: 0, RelPath: prompt + "/0-x.png", Filename: "x.png", SizeBytes: size}})
	if err != nil {
		t.Fatalf("insert %s: %v", prompt, err)
	}
	return id
}

func TestSumGenerationImageBytes(t *testing.T) {
	st := newGenTestStore(t)
	ctx := context.Background()

	// An empty gallery sums to 0 (COALESCE — not a NULL scan error).
	total, err := st.SumGenerationImageBytes(ctx)
	if err != nil {
		t.Fatalf("sum (empty): %v", err)
	}
	if total != 0 {
		t.Errorf("sum (empty) = %d, want 0", total)
	}

	base := time.Now().UTC().Add(-time.Hour)
	mkSizedGeneration(t, st, "p1", 100, base)
	mkSizedGeneration(t, st, "p2", 250, base.Add(time.Minute))

	total, err = st.SumGenerationImageBytes(ctx)
	if err != nil {
		t.Fatalf("sum: %v", err)
	}
	if total != 350 {
		t.Errorf("sum = %d, want 350", total)
	}
}

func TestListOldestGenerations(t *testing.T) {
	st := newGenTestStore(t)
	ctx := context.Background()
	base := time.Now().UTC().Add(-time.Hour)
	oldest := mkSizedGeneration(t, st, "old", 10, base)
	mid := mkSizedGeneration(t, st, "mid", 20, base.Add(time.Minute))
	newest := mkSizedGeneration(t, st, "new", 30, base.Add(2*time.Minute))

	got, err := st.ListOldestGenerations(ctx, 10)
	if err != nil {
		t.Fatalf("list oldest: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	want := []GenerationSize{{ID: oldest, Bytes: 10}, {ID: mid, Bytes: 20}, {ID: newest, Bytes: 30}}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("row %d = %+v, want %+v", i, got[i], w)
		}
	}

	// The limit truncates from the NEWEST end (oldest-first ordering).
	got, err = st.ListOldestGenerations(ctx, 1)
	if err != nil {
		t.Fatalf("list oldest (limit 1): %v", err)
	}
	if len(got) != 1 || got[0].ID != oldest {
		t.Errorf("limit 1 = %+v, want just the oldest (%d)", got, oldest)
	}

	// A non-positive limit returns nothing (the caller must bound the batch).
	if got, err := st.ListOldestGenerations(ctx, 0); err != nil || got != nil {
		t.Errorf("limit 0 = (%+v, %v), want (nil, nil)", got, err)
	}
}

// TestListOldestGenerationsCountsAllImages asserts the per-generation byte total
// sums EVERY image, not just the first.
func TestListOldestGenerationsCountsAllImages(t *testing.T) {
	st := newGenTestStore(t)
	ctx := context.Background()
	id, err := st.InsertGeneration(ctx, &Generation{PromptID: "multi"}, []GenerationImage{
		{Idx: 0, RelPath: "multi/0-a.png", Filename: "a.png", SizeBytes: 7},
		{Idx: 1, RelPath: "multi/1-b.png", Filename: "b.png", SizeBytes: 11},
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	got, err := st.ListOldestGenerations(ctx, 10)
	if err != nil {
		t.Fatalf("list oldest: %v", err)
	}
	if len(got) != 1 || got[0].ID != id || got[0].Bytes != 18 {
		t.Errorf("got %+v, want one row {%d 18}", got, id)
	}
}
