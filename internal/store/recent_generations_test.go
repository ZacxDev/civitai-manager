package store

import (
	"context"
	"testing"
	"time"
)

// seedRecentGens inserts n generations for one workflow, oldest first, each with a
// single image so FirstImageID is populated. It returns their ids in insert order.
func seedRecentGens(t *testing.T, st *Store, wfID int64, n int) []int64 {
	t.Helper()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	ids := make([]int64, 0, n)
	for i := 0; i < n; i++ {
		gen := &Generation{
			WorkflowID:   &wfID,
			WorkflowName: "wf",
			PromptID:     "p",
			CreatedAt:    base.Add(time.Duration(i) * time.Minute),
		}
		id, err := st.InsertGeneration(context.Background(), gen, []GenerationImage{
			{Idx: 0, RelPath: "p/0.png", Filename: "0.png", SizeBytes: 1},
		})
		if err != nil {
			t.Fatalf("insert generation %d: %v", i, err)
		}
		ids = append(ids, id)
	}
	return ids
}

func TestListRecentGenerationsIsBounded(t *testing.T) {
	st := newGenTestStore(t)
	wf := insertTestWorkflow(t, st, "wf")
	ids := seedRecentGens(t, st, wf, 20)

	t.Run("returns at most limit", func(t *testing.T) {
		got, err := st.ListRecentGenerations(context.Background(), 12)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(got) != 12 {
			t.Fatalf("len = %d, want exactly the requested 12", len(got))
		}
	})

	t.Run("newest first", func(t *testing.T) {
		got, err := st.ListRecentGenerations(context.Background(), 3)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		want := []int64{ids[19], ids[18], ids[17]}
		for i, id := range want {
			if got[i].ID != id {
				t.Errorf("row %d id = %d, want %d (newest-first order)", i, got[i].ID, id)
			}
		}
		if got[0].FirstImageID == 0 {
			t.Error("FirstImageID not populated — the rail thumbnail would be blank")
		}
	})

	t.Run("limit clamped to the hard cap", func(t *testing.T) {
		// Ask for far more than the cap; the clamp must hold even though the table
		// has fewer rows than the request (the SQL LIMIT is what is bounded).
		more := seedRecentGens(t, st, wf, 45) // 65 rows total, cap is 50
		_ = more
		got, err := st.ListRecentGenerations(context.Background(), 10000)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(got) != recentGenerationsCap {
			t.Fatalf("len = %d, want the clamped %d", len(got), recentGenerationsCap)
		}
	})

	t.Run("non-positive limit queries nothing", func(t *testing.T) {
		for _, lim := range []int{0, -1} {
			got, err := st.ListRecentGenerations(context.Background(), lim)
			if err != nil {
				t.Fatalf("list(%d): %v", lim, err)
			}
			if len(got) != 0 {
				t.Errorf("list(%d) len = %d, want 0", lim, len(got))
			}
		}
	})
}

func TestListRecentGenerationsEmpty(t *testing.T) {
	st := newGenTestStore(t)
	got, err := st.ListRecentGenerations(context.Background(), 12)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("fresh install: len = %d, want 0", len(got))
	}
}
