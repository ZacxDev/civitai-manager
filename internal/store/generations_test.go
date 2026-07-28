package store

import (
	"context"
	"testing"
)

func newGenTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// insertTestWorkflow inserts a minimal workflow and returns its id.
func insertTestWorkflow(t *testing.T, st *Store, name string) int64 {
	t.Helper()
	id, err := st.InsertWorkflow(context.Background(), &Workflow{
		Name:   name,
		Format: WorkflowFormatAPI,
		Graph:  `{"1":{"class_type":"X","inputs":{}}}`,
		Source: WorkflowSourceImported,
	})
	if err != nil {
		t.Fatalf("insert workflow: %v", err)
	}
	return id
}

func TestMigration0012Applies(t *testing.T) {
	st := newGenTestStore(t)
	v, err := st.SchemaVersion()
	if err != nil {
		t.Fatalf("schema version: %v", err)
	}
	if v < 12 {
		t.Fatalf("schema version = %d, want >= 12", v)
	}
	// Both tables must exist and be queryable.
	for _, tbl := range []string{"generations", "generation_images"} {
		if _, err := st.DB().Exec("SELECT * FROM " + tbl + " LIMIT 0"); err != nil {
			t.Errorf("table %s not usable: %v", tbl, err)
		}
	}
}

func TestInsertAndGetGeneration(t *testing.T) {
	st := newGenTestStore(t)
	ctx := context.Background()
	wfID := insertTestWorkflow(t, st, "wf-a")

	gen := &Generation{
		WorkflowID:   &wfID,
		WorkflowName: "wf-a",
		PromptID:     "prompt-1",
		BaseModel:    "SDXL 1.0",
		GraphHash:    "abc123",
		Params:       `{"substitute":{}}`,
	}
	images := []GenerationImage{
		{Idx: 0, RelPath: "prompt-1/0-a.png", Filename: "a.png", ContentType: "image/png", SizeBytes: 111},
		{Idx: 1, RelPath: "prompt-1/1-b.png", Filename: "b.png", ContentType: "image/png", SizeBytes: 222},
	}
	genID, err := st.InsertGeneration(ctx, gen, images)
	if err != nil {
		t.Fatalf("insert generation: %v", err)
	}
	if genID == 0 {
		t.Fatal("insert returned id 0")
	}

	got, imgs, err := st.GetGeneration(ctx, genID)
	if err != nil {
		t.Fatalf("get generation: %v", err)
	}
	if got.ImageCount != 2 {
		t.Errorf("image_count = %d, want 2", got.ImageCount)
	}
	if got.Status != GenerationStatusReady {
		t.Errorf("status = %q, want ready (default)", got.Status)
	}
	if got.WorkflowID == nil || *got.WorkflowID != wfID {
		t.Errorf("workflow_id = %v, want %d", got.WorkflowID, wfID)
	}
	if got.BaseModel != "SDXL 1.0" || got.GraphHash != "abc123" || got.PromptID != "prompt-1" {
		t.Errorf("snapshot fields wrong: %+v", got)
	}
	if len(imgs) != 2 {
		t.Fatalf("got %d images, want 2", len(imgs))
	}
	if imgs[0].Idx != 0 || imgs[0].RelPath != "prompt-1/0-a.png" || imgs[0].SizeBytes != 111 {
		t.Errorf("image[0] wrong: %+v", imgs[0])
	}
	if imgs[1].Idx != 1 || imgs[1].Filename != "b.png" {
		t.Errorf("image[1] wrong: %+v", imgs[1])
	}
}

func TestGetGenerationNotFound(t *testing.T) {
	st := newGenTestStore(t)
	if _, _, err := st.GetGeneration(context.Background(), 999); err != ErrNotFound {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestListGenerationsOrderingAndFilter(t *testing.T) {
	st := newGenTestStore(t)
	ctx := context.Background()
	wfA := insertTestWorkflow(t, st, "wf-a")
	wfB := insertTestWorkflow(t, st, "wf-b")

	// Insert three generations; created_at ascending by insertion so newest-first
	// ordering is deterministic on (created_at DESC, id DESC).
	mk := func(wfID int64, prompt string, imgs int) int64 {
		gi := make([]GenerationImage, imgs)
		for i := range gi {
			gi[i] = GenerationImage{Idx: i, RelPath: prompt + "/x.png", Filename: "x.png"}
		}
		id, err := st.InsertGeneration(ctx, &Generation{WorkflowID: &wfID, PromptID: prompt}, gi)
		if err != nil {
			t.Fatalf("insert %s: %v", prompt, err)
		}
		return id
	}
	id1 := mk(wfA, "p1", 1)
	id2 := mk(wfB, "p2", 2)
	id3 := mk(wfA, "p3", 3)

	all, err := st.ListGenerations(ctx, ListGenerationsOpts{})
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("list all = %d, want 3", len(all))
	}
	// Newest first: id3, id2, id1.
	if all[0].ID != id3 || all[1].ID != id2 || all[2].ID != id1 {
		t.Errorf("ordering = [%d %d %d], want [%d %d %d]", all[0].ID, all[1].ID, all[2].ID, id3, id2, id1)
	}
	if all[0].FirstImageID == 0 {
		t.Error("FirstImageID not populated for a generation with images")
	}

	// Workflow filter.
	onlyA, err := st.ListGenerations(ctx, ListGenerationsOpts{WorkflowID: &wfA})
	if err != nil {
		t.Fatalf("list filtered: %v", err)
	}
	if len(onlyA) != 2 {
		t.Fatalf("filtered = %d, want 2", len(onlyA))
	}
	for _, g := range onlyA {
		if g.WorkflowID == nil || *g.WorkflowID != wfA {
			t.Errorf("filter leaked a non-wfA generation: %+v", g)
		}
	}

	// Pagination bounds.
	page, err := st.ListGenerations(ctx, ListGenerationsOpts{Limit: 1, Offset: 1})
	if err != nil {
		t.Fatalf("list paged: %v", err)
	}
	if len(page) != 1 || page[0].ID != id2 {
		t.Errorf("page = %v, want [%d]", page, id2)
	}

	// Counts.
	if n, _ := st.CountGenerations(ctx, nil); n != 3 {
		t.Errorf("count all = %d, want 3", n)
	}
	if n, _ := st.CountGenerations(ctx, &wfA); n != 2 {
		t.Errorf("count wfA = %d, want 2", n)
	}
}

func TestGetGenerationImageByID(t *testing.T) {
	st := newGenTestStore(t)
	ctx := context.Background()
	genID, err := st.InsertGeneration(ctx, &Generation{PromptID: "p"}, []GenerationImage{
		{Idx: 0, RelPath: "p/0-a.png", Filename: "a.png", ContentType: "image/jpeg", SizeBytes: 7},
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	_, imgs, _ := st.GetGeneration(ctx, genID)
	img, err := st.GetGenerationImage(ctx, imgs[0].ID)
	if err != nil {
		t.Fatalf("get image: %v", err)
	}
	if img.RelPath != "p/0-a.png" || img.ContentType != "image/jpeg" || img.GenerationID != genID {
		t.Errorf("image wrong: %+v", img)
	}
	if _, err := st.GetGenerationImage(ctx, 4242); err != ErrNotFound {
		t.Errorf("missing image err = %v, want ErrNotFound", err)
	}
}

func TestDeleteGenerationCascade(t *testing.T) {
	st := newGenTestStore(t)
	ctx := context.Background()
	genID, err := st.InsertGeneration(ctx, &Generation{PromptID: "p"}, []GenerationImage{
		{Idx: 0, RelPath: "p/0-a.png", Filename: "a.png"},
		{Idx: 1, RelPath: "p/1-b.png", Filename: "b.png"},
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	paths, err := st.DeleteGeneration(ctx, genID)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if len(paths) != 2 {
		t.Fatalf("returned %d rel_paths, want 2", len(paths))
	}
	// Image rows CASCADE-dropped.
	var n int
	if err := st.DB().QueryRow(`SELECT COUNT(*) FROM generation_images WHERE generation_id = ?`, genID).Scan(&n); err != nil {
		t.Fatalf("count images: %v", err)
	}
	if n != 0 {
		t.Errorf("image rows after delete = %d, want 0 (CASCADE)", n)
	}
	// Deleting again → ErrNotFound.
	if _, err := st.DeleteGeneration(ctx, genID); err != ErrNotFound {
		t.Errorf("re-delete err = %v, want ErrNotFound", err)
	}
}

func TestWorkflowDeleteSetsGenerationNull(t *testing.T) {
	st := newGenTestStore(t)
	ctx := context.Background()
	wfID := insertTestWorkflow(t, st, "wf")
	genID, err := st.InsertGeneration(ctx, &Generation{
		WorkflowID: &wfID, WorkflowName: "wf", PromptID: "p",
	}, []GenerationImage{{Idx: 0, RelPath: "p/0-a.png", Filename: "a.png"}})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	// Delete the source workflow — the generation must SURVIVE with workflow_id NULL
	// and its name snapshot intact (ON DELETE SET NULL).
	if err := st.DeleteWorkflow(ctx, wfID); err != nil {
		t.Fatalf("delete workflow: %v", err)
	}
	got, imgs, err := st.GetGeneration(ctx, genID)
	if err != nil {
		t.Fatalf("get generation after workflow delete: %v", err)
	}
	if got.WorkflowID != nil {
		t.Errorf("workflow_id = %v, want NULL after workflow delete", *got.WorkflowID)
	}
	if got.WorkflowName != "wf" {
		t.Errorf("workflow_name snapshot lost: %q", got.WorkflowName)
	}
	if len(imgs) != 1 {
		t.Errorf("images lost after workflow delete: %d", len(imgs))
	}
}

func TestDeleteGenerationsByWorkflow(t *testing.T) {
	st := newGenTestStore(t)
	ctx := context.Background()
	wfA := insertTestWorkflow(t, st, "a")
	wfB := insertTestWorkflow(t, st, "b")
	mk := func(wfID int64, prompt string) {
		if _, err := st.InsertGeneration(ctx, &Generation{WorkflowID: &wfID, PromptID: prompt},
			[]GenerationImage{{Idx: 0, RelPath: prompt + "/0.png", Filename: "0.png"}}); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	mk(wfA, "a1")
	mk(wfA, "a2")
	mk(wfB, "b1")

	paths, err := st.DeleteGenerationsByWorkflow(ctx, wfA)
	if err != nil {
		t.Fatalf("delete by workflow: %v", err)
	}
	if len(paths) != 2 {
		t.Errorf("rel_paths = %d, want 2", len(paths))
	}
	if n, _ := st.CountGenerations(ctx, nil); n != 1 {
		t.Errorf("remaining generations = %d, want 1 (wfB)", n)
	}
	// No-op on a workflow with no generations.
	empty, err := st.DeleteGenerationsByWorkflow(ctx, 98765)
	if err != nil {
		t.Fatalf("delete by workflow (none): %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("expected no rel_paths, got %d", len(empty))
	}
}
