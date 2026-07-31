package store

import (
	"context"
	"strconv"
	"testing"
)

// ===========================================================================
// CountWorkflowsByModels — the BATCHED "already imported?" lookup.
// ===========================================================================
//
// It exists so a browse grid can answer the question for a whole PAGE of cards in
// ONE query. Its correctness contract is the same `model_id` predicate as
// CountWorkflowsByModel (so the card label and the /library?tab=workflows&model=
// list it links to describe one set), plus the input hygiene that keeps the
// statement well-formed and bounded: duplicates collapse, non-positive ids are
// dropped, and an empty set never reaches the DB (`IN ()` is a SQLite syntax
// error).

func insertWF(t *testing.T, st *Store, name string, seed int, modelID *int) {
	t.Helper()
	if _, err := st.InsertWorkflow(context.Background(), &Workflow{
		Name: name, Format: WorkflowFormatAPI,
		Graph:  `{"1":{"class_type":"KSampler","inputs":{"seed":` + strconv.Itoa(seed) + `}}}`,
		Source: WorkflowSourceCivitai, ModelID: modelID,
	}); err != nil {
		t.Fatal(err)
	}
}

// TestCountWorkflowsByModelsAgreesWithTheSingleForm pins the batched result
// against the single-id form row for row. If the two ever disagree, a card could
// say "in library" while the page it links to is empty (or the reverse).
func TestCountWorkflowsByModelsAgreesWithTheSingleForm(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	seven, nine := 7, 9
	insertWF(t, st, "a", 1, &seven)
	insertWF(t, st, "b", 2, &seven)
	insertWF(t, st, "c", 3, &seven)
	insertWF(t, st, "d", 4, &nine)
	insertWF(t, st, "unlinked", 5, nil)

	ids := []int{7, 9, 1234}
	batch, err := st.CountWorkflowsByModels(ctx, ids)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		single, err := st.CountWorkflowsByModel(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if batch[id] != single {
			t.Errorf("model %d: batched count = %d, single-id count = %d — the two forms "+
				"must carry the IDENTICAL model_id predicate, or a card's label and the "+
				"library list it links to describe different sets", id, batch[id], single)
		}
	}
	// A model with nothing imported is ABSENT, not present-with-zero: the caller
	// reads the map with a zero-value lookup, so absent == 0 == "offer Import".
	if _, present := batch[1234]; present {
		t.Errorf("a model with no imported workflows must be absent from the map, got %v", batch)
	}
	// The unlinked row (model_id NULL) must not be counted under any id.
	total := 0
	for _, n := range batch {
		total += n
	}
	if total != 4 {
		t.Errorf("counted %d rows across %v, want 4 — the model_id IS NULL row must not "+
			"be attributed to anything", total, batch)
	}
}

// TestCountWorkflowsByModelsInputHygiene covers the cases that would otherwise
// produce a malformed or unbounded statement.
func TestCountWorkflowsByModelsInputHygiene(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	seven := 7
	insertWF(t, st, "a", 1, &seven)
	insertWF(t, st, "b", 2, &seven)

	// Empty / all-invalid input must NOT reach the DB: `WHERE model_id IN ()` is a
	// SQLite syntax error, so a page of zero cards would 500 the whole render.
	for _, in := range [][]int{nil, {}, {0}, {-1, 0, -99}} {
		got, err := st.CountWorkflowsByModels(ctx, in)
		if err != nil {
			t.Fatalf("CountWorkflowsByModels(%v) errored: %v — an empty/invalid id set must "+
				"short-circuit rather than build `IN ()`", in, err)
		}
		if len(got) != 0 {
			t.Errorf("CountWorkflowsByModels(%v) = %v, want an empty map", in, got)
		}
	}

	// Duplicates and junk mixed with a real id: one placeholder per DISTINCT
	// positive id, and the answer is unaffected.
	got, err := st.CountWorkflowsByModels(ctx, []int{7, 7, 7, 0, -3, 7})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[7] != 2 {
		t.Errorf("CountWorkflowsByModels with duplicates = %v, want map[7:2]", got)
	}
}
