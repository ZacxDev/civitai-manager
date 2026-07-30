package store

import (
	"context"
	"errors"
	"testing"
)

// seedPresetWorkflow inserts a minimal UI workflow and returns its id.
func seedPresetWorkflow(t *testing.T, st *Store, name string) int64 {
	t.Helper()
	wf := Workflow{
		Name: name, Format: WorkflowFormatUI, Graph: `{"nodes":[]}`, Source: WorkflowSourceImported,
	}
	id, err := st.InsertWorkflow(context.Background(), &wf)
	if err != nil {
		t.Fatalf("create workflow: %v", err)
	}
	return id
}

// TestMigration0014AppliesOnPopulatedDB proves 0014 is safe on a DB that already
// holds generations rows: the ALTER TABLE ADD COLUMN … REFERENCES clauses are
// permitted only because their default is NULL, and the existing rows survive
// with the new columns NULL.
func TestMigration0014AppliesOnPopulatedDB(t *testing.T) {
	db := applyMigrationsUpTo(t, 13)

	if _, err := db.Exec(`INSERT INTO workflows
		(name, format, graph, source, is_golden, created_at, updated_at)
		VALUES ('wf', 'ui', '{}', 'paste', 0, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("seed workflow: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO generations
		(workflow_id, workflow_name, prompt_id, status, image_count, created_at)
		VALUES (1, 'wf', 'p1', 'ready', 1, '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("seed generation: %v", err)
	}

	b, err := migrationsFS.ReadFile("migrations/0014_run_presets.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(b)); err != nil {
		t.Fatalf("apply 0014 on a populated DB: %v", err)
	}

	var (
		n          int
		presetID   any
		presetName any
	)
	if err := db.QueryRow(`SELECT COUNT(*) FROM generations`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("generations rows after migrate = %d, want 1 (existing rows must survive)", n)
	}
	if err := db.QueryRow(`SELECT preset_id, preset_name FROM generations WHERE id = 1`).
		Scan(&presetID, &presetName); err != nil {
		t.Fatal(err)
	}
	if presetID != nil || presetName != nil {
		t.Errorf("pre-existing row got preset_id=%v preset_name=%v, want both NULL", presetID, presetName)
	}
	// The new table exists and is usable.
	if _, err := db.Exec(`INSERT INTO run_presets (workflow_id, created_at, updated_at)
		VALUES (1, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("run_presets unusable after migration: %v", err)
	}
	var params string
	if err := db.QueryRow(`SELECT params FROM run_presets WHERE id = 1`).Scan(&params); err != nil {
		t.Fatal(err)
	}
	if params != "{}" {
		t.Errorf("params default = %q, want %q", params, "{}")
	}
}

// TestRunPresetCRUD covers insert (with the next-free position), list ordering by
// (position, id), update, adopt, and delete.
func TestRunPresetCRUD(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	wfID := seedPresetWorkflow(t, st, "wf")

	// Position -1 asks for the next free slot.
	a, err := st.CreateRunPreset(ctx, RunPreset{WorkflowID: wfID, Name: "Base", Position: -1, GraphHash: "h1", Params: `{"x":1}`})
	if err != nil {
		t.Fatalf("create a: %v", err)
	}
	b, err := st.CreateRunPreset(ctx, RunPreset{WorkflowID: wfID, Name: "Hi-res", Position: -1, GraphHash: "h1"})
	if err != nil {
		t.Fatalf("create b: %v", err)
	}

	list, err := st.ListRunPresets(ctx, wfID)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].ID != a || list[1].ID != b {
		t.Fatalf("list = %+v, want [%d %d] in (position,id) order", list, a, b)
	}
	if list[0].Position != 0 || list[1].Position != 1 {
		t.Errorf("positions = %d,%d, want 0,1", list[0].Position, list[1].Position)
	}
	if list[1].Params != "{}" {
		t.Errorf("empty params stored as %q, want %q", list[1].Params, "{}")
	}
	if list[0].Params != `{"x":1}` {
		t.Errorf("params = %q", list[0].Params)
	}

	// Update writes name + params + graph_hash TOGETHER: params and the hash that
	// describes the graph they were captured against are one indivisible write, so a
	// caller cannot replace the values while leaving a hash naming an older graph.
	// A blank hash is a first-class value ("cannot be proven equal" — the fail-safe
	// a non-adopt write stores when the preset had drifted).
	if err := st.UpdateRunPreset(ctx, a, "Renamed", `{"y":2}`, ""); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, err := st.GetRunPreset(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Renamed" || got.Params != `{"y":2}` {
		t.Errorf("after update: %+v", got)
	}
	if got.GraphHash != "" {
		t.Errorf("graph_hash = %q, want the blank the caller asked for", got.GraphHash)
	}

	// The same call stamps a real hash — that is what an explicit adopt does.
	if err := st.UpdateRunPreset(ctx, a, "Renamed", `{"y":2}`, "h2"); err != nil {
		t.Fatalf("adopt: %v", err)
	}
	got, _ = st.GetRunPreset(ctx, a)
	if got.GraphHash != "h2" {
		t.Errorf("graph_hash after adopt = %q, want h2", got.GraphHash)
	}

	if err := st.DeleteRunPreset(ctx, a); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := st.GetRunPreset(ctx, a); !errors.Is(err, ErrNotFound) {
		t.Errorf("get after delete: err = %v, want ErrNotFound", err)
	}
	if err := st.DeleteRunPreset(ctx, a); !errors.Is(err, ErrNotFound) {
		t.Errorf("double delete: err = %v, want ErrNotFound", err)
	}
	if err := st.UpdateRunPreset(ctx, a, "x", "{}", "h1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("update missing: err = %v, want ErrNotFound", err)
	}
}

// TestPresetCountCapEnforced proves the 12-preset cap is a STORE rule, not a
// client-only one: a hand-built 13th create is refused.
func TestPresetCountCapEnforced(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	wfID := seedPresetWorkflow(t, st, "wf")

	for i := 0; i < MaxRunPresetsPerWorkflow; i++ {
		if _, err := st.CreateRunPreset(ctx, RunPreset{WorkflowID: wfID, Position: -1}); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
	}
	if _, err := st.CreateRunPreset(ctx, RunPreset{WorkflowID: wfID, Position: -1}); !errors.Is(err, ErrPresetCapReached) {
		t.Fatalf("13th create: err = %v, want ErrPresetCapReached", err)
	}
	n, err := st.CountRunPresets(ctx, wfID)
	if err != nil {
		t.Fatal(err)
	}
	if n != MaxRunPresetsPerWorkflow {
		t.Errorf("count = %d, want %d (the refused create must insert nothing)", n, MaxRunPresetsPerWorkflow)
	}
	// The cap is PER WORKFLOW.
	other := seedPresetWorkflow(t, st, "other")
	if _, err := st.CreateRunPreset(ctx, RunPreset{WorkflowID: other, Position: -1}); err != nil {
		t.Errorf("cap must be per-workflow, got %v", err)
	}
}

// TestForeignKeysActuallyEnforced pins that PRAGMA foreign_keys is on: every
// CASCADE / SET NULL guarantee below depends on it.
func TestForeignKeysActuallyEnforced(t *testing.T) {
	st := newTestStore(t)
	if _, err := st.CreateRunPreset(context.Background(), RunPreset{WorkflowID: 999999}); err == nil {
		t.Fatal("inserting a preset for a nonexistent workflow must fail on the FK")
	}
}

// TestRunPresetCascadeOnWorkflowDelete proves the deliberate asymmetry: deleting a
// workflow DELETES its presets (a preset is a pointer into that graph and means
// nothing without it) but KEEPS its generations (durable artifacts) with
// workflow_id NULLed.
func TestRunPresetCascadeOnWorkflowDelete(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	wfID := seedPresetWorkflow(t, st, "wf")

	if _, err := st.CreateRunPreset(ctx, RunPreset{WorkflowID: wfID, Name: "p", Position: -1}); err != nil {
		t.Fatal(err)
	}
	genID, err := st.InsertGeneration(ctx, &Generation{
		WorkflowID: &wfID, WorkflowName: "wf", PromptID: "p1",
	}, []GenerationImage{{Idx: 0, RelPath: "a/b.png", Filename: "b.png", SizeBytes: 10}})
	if err != nil {
		t.Fatal(err)
	}

	if err := st.DeleteWorkflow(ctx, wfID); err != nil {
		t.Fatal(err)
	}

	presets, err := st.ListRunPresets(ctx, wfID)
	if err != nil {
		t.Fatal(err)
	}
	if len(presets) != 0 {
		t.Errorf("presets after workflow delete = %d, want 0 (ON DELETE CASCADE)", len(presets))
	}
	gen, _, err := st.GetGeneration(ctx, genID)
	if err != nil {
		t.Fatalf("the generation must survive its workflow: %v", err)
	}
	if gen.WorkflowID != nil {
		t.Errorf("generation.workflow_id = %v, want NULL (ON DELETE SET NULL)", *gen.WorkflowID)
	}
}

// TestGenerationPresetIDSetNullOnPresetDelete proves deleting a preset never
// deletes images: preset_id is NULLed and the snapshotted preset_name survives so
// the run stays labeled.
func TestGenerationPresetIDSetNullOnPresetDelete(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	wfID := seedPresetWorkflow(t, st, "wf")

	pid, err := st.CreateRunPreset(ctx, RunPreset{WorkflowID: wfID, Name: "Hi-res 8-step", Position: -1})
	if err != nil {
		t.Fatal(err)
	}
	genID, err := st.InsertGeneration(ctx, &Generation{
		WorkflowID: &wfID, WorkflowName: "wf", PromptID: "p1",
		PresetID: &pid, PresetName: "Hi-res 8-step",
	}, []GenerationImage{{Idx: 0, RelPath: "a/b.png", Filename: "b.png", SizeBytes: 10}})
	if err != nil {
		t.Fatal(err)
	}

	gen, _, err := st.GetGeneration(ctx, genID)
	if err != nil {
		t.Fatal(err)
	}
	if gen.PresetID == nil || *gen.PresetID != pid {
		t.Fatalf("preset_id round-trip = %v, want %d", gen.PresetID, pid)
	}
	if gen.PresetName != "Hi-res 8-step" {
		t.Errorf("preset_name = %q", gen.PresetName)
	}

	if err := st.DeleteRunPreset(ctx, pid); err != nil {
		t.Fatal(err)
	}
	gen, imgs, err := st.GetGeneration(ctx, genID)
	if err != nil {
		t.Fatalf("deleting a preset must never delete its generations: %v", err)
	}
	if gen.PresetID != nil {
		t.Errorf("preset_id after preset delete = %v, want NULL", *gen.PresetID)
	}
	if gen.PresetName != "Hi-res 8-step" {
		t.Errorf("preset_name after preset delete = %q, want the snapshot to survive", gen.PresetName)
	}
	if len(imgs) != 1 {
		t.Errorf("images after preset delete = %d, want 1", len(imgs))
	}
}
