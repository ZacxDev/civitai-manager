package store

import (
	"context"
	"errors"
	"testing"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// TestWorkflowMigrationApplied asserts 0008 ran (schema is at >= 8).
func TestWorkflowMigrationApplied(t *testing.T) {
	st := openTestStore(t)
	v, err := st.SchemaVersion()
	if err != nil {
		t.Fatalf("schema version: %v", err)
	}
	if v < 8 {
		t.Fatalf("schema version = %d, want >= 8 (0008_workflows)", v)
	}
}

// TestInsertGetWorkflow round-trips a workflow including the resources JSON
// column and the nullable civitai linkage.
func TestInsertGetWorkflow(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	wf := &Workflow{
		Name:      "portrait",
		Format:    WorkflowFormatAPI,
		Graph:     `{"1":{"class_type":"CheckpointLoaderSimple","inputs":{}}}`,
		Source:    WorkflowSourceImported,
		ModelID:   intp(10),
		VersionID: intp(20),
		BaseModel: "SDXL 1.0",
		Resources: []string{"a.safetensors", "b.safetensors"},
	}
	id, err := st.InsertWorkflow(ctx, wf)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	got, err := st.GetWorkflow(ctx, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Name != "portrait" || got.Format != WorkflowFormatAPI || got.Source != WorkflowSourceImported {
		t.Errorf("scalar mismatch: %+v", got)
	}
	if got.ModelID == nil || *got.ModelID != 10 || got.VersionID == nil || *got.VersionID != 20 {
		t.Errorf("linkage mismatch: %+v", got)
	}
	if got.BaseModel != "SDXL 1.0" {
		t.Errorf("base_model = %q", got.BaseModel)
	}
	if len(got.Resources) != 2 || got.Resources[0] != "a.safetensors" || got.Resources[1] != "b.safetensors" {
		t.Errorf("resources = %v", got.Resources)
	}
	if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
		t.Errorf("timestamps not set: %+v", got)
	}
	if !got.Runnable() {
		t.Errorf("api workflow should be Runnable")
	}
}

// TestGetWorkflowNotFound asserts a missing id is ErrNotFound.
func TestGetWorkflowNotFound(t *testing.T) {
	st := openTestStore(t)
	if _, err := st.GetWorkflow(context.Background(), 999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// TestListWorkflowsNewestFirst asserts ordering and ByVersion filtering.
func TestListWorkflowsNewestFirst(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	id1, _ := st.InsertWorkflow(ctx, &Workflow{Name: "first", Format: WorkflowFormatUI, Graph: "{}", Source: WorkflowSourceImported, VersionID: intp(5)})
	id2, _ := st.InsertWorkflow(ctx, &Workflow{Name: "second", Format: WorkflowFormatAPI, Graph: "{}", Source: WorkflowSourceImported, VersionID: intp(6)})

	list, err := st.ListWorkflows(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 2 || list[0].ID != id2 || list[1].ID != id1 {
		t.Fatalf("expected newest-first [%d,%d], got %+v", id2, id1, list)
	}

	byVer, err := st.ListWorkflowsByVersion(ctx, 5)
	if err != nil {
		t.Fatalf("by version: %v", err)
	}
	if len(byVer) != 1 || byVer[0].ID != id1 {
		t.Fatalf("by version(5) = %+v, want just id1", byVer)
	}
}

// TestDeleteWorkflow removes a row; a missing id is ErrNotFound.
func TestDeleteWorkflow(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	id, _ := st.InsertWorkflow(ctx, &Workflow{Format: WorkflowFormatAPI, Graph: "{}", Source: WorkflowSourceImported})
	if err := st.DeleteWorkflow(ctx, id); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := st.GetWorkflow(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete err = %v, want ErrNotFound", err)
	}
	if err := st.DeleteWorkflow(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("double delete err = %v, want ErrNotFound", err)
	}
}

// TestAttachWorkflow sets and clears the civitai linkage; detaching a version
// clears golden.
func TestAttachWorkflow(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	id, _ := st.InsertWorkflow(ctx, &Workflow{Format: WorkflowFormatAPI, Graph: "{}", Source: WorkflowSourceImported})

	if err := st.AttachWorkflow(ctx, int(id), intp(100), intp(200)); err != nil {
		t.Fatalf("attach: %v", err)
	}
	if err := st.SetGolden(ctx, int(id)); err != nil {
		t.Fatalf("set golden: %v", err)
	}
	got, _ := st.GetWorkflow(ctx, id)
	if got.VersionID == nil || *got.VersionID != 200 || !got.IsGolden {
		t.Fatalf("after attach+golden: %+v", got)
	}

	// Detach the version → golden must clear.
	if err := st.AttachWorkflow(ctx, int(id), nil, nil); err != nil {
		t.Fatalf("detach: %v", err)
	}
	got, _ = st.GetWorkflow(ctx, id)
	if got.VersionID != nil || got.ModelID != nil {
		t.Fatalf("linkage not cleared: %+v", got)
	}
	if got.IsGolden {
		t.Fatalf("golden should be cleared on detach: %+v", got)
	}

	if err := st.AttachWorkflow(ctx, 999, intp(1), intp(1)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("attach missing err = %v, want ErrNotFound", err)
	}
}

// TestSetGoldenRequiresVersion asserts golden without an attached version errors.
func TestSetGoldenRequiresVersion(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	id, _ := st.InsertWorkflow(ctx, &Workflow{Format: WorkflowFormatAPI, Graph: "{}", Source: WorkflowSourceImported})
	if err := st.SetGolden(ctx, int(id)); !errors.Is(err, ErrGoldenNeedsVersion) {
		t.Fatalf("err = %v, want ErrGoldenNeedsVersion", err)
	}
}

// TestSetGoldenSingletonPerVersion asserts at most one golden per version: setting
// a second workflow golden clears the first, and the partial unique index never
// trips.
func TestSetGoldenSingletonPerVersion(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	a, _ := st.InsertWorkflow(ctx, &Workflow{Name: "a", Format: WorkflowFormatAPI, Graph: "{}", Source: WorkflowSourceImported, VersionID: intp(42)})
	b, _ := st.InsertWorkflow(ctx, &Workflow{Name: "b", Format: WorkflowFormatAPI, Graph: "{}", Source: WorkflowSourceImported, VersionID: intp(42)})

	if err := st.SetGolden(ctx, int(a)); err != nil {
		t.Fatalf("set golden a: %v", err)
	}
	if err := st.SetGolden(ctx, int(b)); err != nil {
		t.Fatalf("set golden b: %v", err)
	}
	ga, _ := st.GetWorkflow(ctx, a)
	gb, _ := st.GetWorkflow(ctx, b)
	if ga.IsGolden {
		t.Errorf("a should no longer be golden after b took it")
	}
	if !gb.IsGolden {
		t.Errorf("b should be golden")
	}

	// Unset restores no-golden for the version.
	if err := st.UnsetGolden(ctx, int(b)); err != nil {
		t.Fatalf("unset: %v", err)
	}
	gb, _ = st.GetWorkflow(ctx, b)
	if gb.IsGolden {
		t.Errorf("b should be un-golden after UnsetGolden")
	}
}
