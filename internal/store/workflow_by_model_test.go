package store

import (
	"context"
	"strconv"
	"testing"
)

// TestCountWorkflowsByModel proves the "already imported from this model?" count:
// it counts ONLY rows whose model_id matches, ignores rows imported from another
// model or with no source link at all, and never touches the DB for a
// non-positive id.
func TestCountWorkflowsByModel(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	insert := func(name, graph string, modelID *int) {
		t.Helper()
		if _, err := st.InsertWorkflow(ctx, &Workflow{
			Name: name, Format: WorkflowFormatAPI, Graph: graph,
			Source: WorkflowSourceCivitai, ModelID: modelID,
		}); err != nil {
			t.Fatal(err)
		}
	}
	seven, nine := 7, 9
	insert("a", `{"1":{"class_type":"KSampler","inputs":{"seed":1}}}`, &seven)
	insert("b", `{"1":{"class_type":"KSampler","inputs":{"seed":2}}}`, &seven)
	insert("c", `{"1":{"class_type":"KSampler","inputs":{"seed":3}}}`, &nine)
	insert("d", `{"1":{"class_type":"KSampler","inputs":{"seed":4}}}`, nil)

	cases := []struct {
		name    string
		modelID int
		want    int
	}{
		{"two imported from model 7", 7, 2},
		{"one imported from model 9", 9, 1},
		{"none from an unrelated model", 1234, 0},
		{"zero id short-circuits", 0, 0},
		{"negative id short-circuits", -5, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := st.CountWorkflowsByModel(ctx, c.modelID)
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Errorf("CountWorkflowsByModel(%d) = %d, want %d", c.modelID, got, c.want)
			}
		})
	}
}

// TestListWorkflowsByModel proves the list companion to CountWorkflowsByModel:
// the SAME model_id predicate, newest-first ordering, and — the part the model
// detail page depends on — a limit that is enforced in SQL, not by the caller.
func TestListWorkflowsByModel(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	seven, nine := 7, 9
	insert := func(name string, seed int, modelID *int) {
		t.Helper()
		if _, err := st.InsertWorkflow(ctx, &Workflow{
			Name: name, Format: WorkflowFormatAPI,
			Graph:  `{"1":{"class_type":"KSampler","inputs":{"seed":` + strconv.Itoa(seed) + `}}}`,
			Source: WorkflowSourceCivitai, ModelID: modelID,
		}); err != nil {
			t.Fatal(err)
		}
	}
	// Inserted oldest-first, so "newest first" means d, c, b, a for model 7.
	insert("a", 1, &seven)
	insert("b", 2, &seven)
	insert("c", 3, &seven)
	insert("d", 4, &seven)
	insert("other", 5, &nine)
	insert("unlinked", 6, nil)

	names := func(wfs []Workflow) []string {
		out := make([]string, 0, len(wfs))
		for _, wf := range wfs {
			out = append(out, wf.Name)
		}
		return out
	}

	cases := []struct {
		name    string
		modelID int
		limit   int
		want    []string
	}{
		{"newest first, under the limit", 7, 10, []string{"d", "c", "b", "a"}},
		{"limit truncates to the NEWEST n", 7, 2, []string{"d", "c"}},
		{"only this model's rows", 9, 10, []string{"other"}},
		{"unrelated model", 1234, 10, nil},
		{"zero limit short-circuits", 7, 0, nil},
		{"negative limit short-circuits", 7, -1, nil},
		{"zero id short-circuits", 0, 10, nil},
		{"negative id short-circuits", -5, 10, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := st.ListWorkflowsByModel(ctx, c.modelID, c.limit)
			if err != nil {
				t.Fatal(err)
			}
			gotNames := names(got)
			if len(gotNames) != len(c.want) {
				t.Fatalf("ListWorkflowsByModel(%d, %d) = %v, want %v",
					c.modelID, c.limit, gotNames, c.want)
			}
			for i := range c.want {
				if gotNames[i] != c.want[i] {
					t.Fatalf("ListWorkflowsByModel(%d, %d) = %v, want %v",
						c.modelID, c.limit, gotNames, c.want)
				}
			}
		})
	}
}
