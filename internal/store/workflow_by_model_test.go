package store

import (
	"context"
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
