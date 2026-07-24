package store

import (
	"context"
	"testing"
	"time"
)

// TestWorkflowScanMigrationApplied asserts 0009 ran (schema is at >= 9).
func TestWorkflowScanMigrationApplied(t *testing.T) {
	st := openTestStore(t)
	v, err := st.SchemaVersion()
	if err != nil {
		t.Fatalf("schema version: %v", err)
	}
	if v < 9 {
		t.Fatalf("schema version = %d, want >= 9 (0009_workflow_scan)", v)
	}
}

// TestUpsertWorkflowByPathInsertThenUpdate inserts a scanned workflow, then
// re-upserts with changed cache fields + graph and asserts the SAME row is
// updated (no duplicate) with the fresh cache/graph values.
func TestUpsertWorkflowByPathInsertThenUpdate(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	t0 := time.Unix(1_700_000_000, 0).UTC()

	wf := &Workflow{
		Name:       "flow",
		Format:     WorkflowFormatUI,
		Graph:      `{"nodes":[]}`,
		Source:     WorkflowSourceScanned,
		SourcePath: "/comfy/user/default/workflows/flow.json",
		SizeBytes:  100,
		Mtime:      &t0,
		Resources:  []string{"a.safetensors"},
	}
	id, updated, err := st.UpsertWorkflowByPath(ctx, wf)
	if err != nil {
		t.Fatalf("insert upsert: %v", err)
	}
	if updated {
		t.Fatalf("first upsert should be an insert, got updated=true")
	}

	// Re-upsert with new size/mtime/graph/resources — same path.
	t1 := t0.Add(time.Hour)
	wf2 := &Workflow{
		Name:       "flow",
		Format:     WorkflowFormatUI,
		Graph:      `{"nodes":[{"type":"CheckpointLoaderSimple"}]}`,
		Source:     WorkflowSourceScanned,
		SourcePath: "/comfy/user/default/workflows/flow.json",
		SizeBytes:  222,
		Mtime:      &t1,
		Resources:  []string{"b.safetensors"},
	}
	id2, updated2, err := st.UpsertWorkflowByPath(ctx, wf2)
	if err != nil {
		t.Fatalf("update upsert: %v", err)
	}
	if !updated2 {
		t.Errorf("second upsert should be an update, got updated=false")
	}
	if id2 != id {
		t.Errorf("upsert created a new row (id %d != %d)", id2, id)
	}
	all, _ := st.ListWorkflows(ctx)
	if len(all) != 1 {
		t.Fatalf("expected exactly 1 row after re-scan, got %d", len(all))
	}
	got := all[0]
	if got.SizeBytes != 222 || got.Mtime == nil || !got.Mtime.Equal(t1) {
		t.Errorf("cache fields not refreshed: size=%d mtime=%v", got.SizeBytes, got.Mtime)
	}
	if len(got.Resources) != 1 || got.Resources[0] != "b.safetensors" {
		t.Errorf("resources not refreshed: %v", got.Resources)
	}
	if got.Graph != wf2.Graph {
		t.Errorf("graph not refreshed: %q", got.Graph)
	}
}

// TestUpsertWorkflowByPathPreservesCuration asserts a re-scan never clobbers a
// manual attach or the golden flag (COALESCE keeps existing ids; is_golden
// untouched), and does not overwrite a user-renamed name.
func TestUpsertWorkflowByPathPreservesCuration(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	path := "/comfy/workflows/keep.json"

	id, _, err := st.UpsertWorkflowByPath(ctx, &Workflow{
		Name:       "auto-name",
		Format:     WorkflowFormatAPI,
		Graph:      "{}",
		Source:     WorkflowSourceScanned,
		SourcePath: path,
		SizeBytes:  1,
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	// User manually attaches to a version and marks golden, and renames it.
	if err := st.AttachWorkflow(ctx, int(id), intp(5), intp(6)); err != nil {
		t.Fatalf("attach: %v", err)
	}
	if err := st.SetGolden(ctx, int(id)); err != nil {
		t.Fatalf("golden: %v", err)
	}
	if _, err := st.db.ExecContext(ctx, `UPDATE workflows SET name = ? WHERE id = ?`, "my custom name", id); err != nil {
		t.Fatalf("rename: %v", err)
	}

	// Re-scan surfaces a DIFFERENT auto-link + a fresh derived name.
	_, updated, err := st.UpsertWorkflowByPath(ctx, &Workflow{
		Name:       "auto-name-2",
		Format:     WorkflowFormatAPI,
		Graph:      `{"1":{"class_type":"x"}}`,
		Source:     WorkflowSourceScanned,
		SourcePath: path,
		SizeBytes:  2,
		ModelID:    intp(99),
		VersionID:  intp(98),
	})
	if err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	if !updated {
		t.Fatal("expected update on re-scan")
	}
	got, _ := st.GetWorkflow(ctx, id)
	if got.ModelID == nil || *got.ModelID != 5 || got.VersionID == nil || *got.VersionID != 6 {
		t.Errorf("manual attach clobbered by re-scan: model=%v version=%v", got.ModelID, got.VersionID)
	}
	if !got.IsGolden {
		t.Error("golden flag lost on re-scan")
	}
	if got.Name != "my custom name" {
		t.Errorf("user name overwritten: %q", got.Name)
	}
}

// TestGetWorkflowByPath covers the miss (nil,nil) and hit paths.
func TestGetWorkflowByPath(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	got, err := st.GetWorkflowByPath(ctx, "/nope.json")
	if err != nil {
		t.Fatalf("miss err: %v", err)
	}
	if got != nil {
		t.Fatalf("miss should be nil, got %+v", got)
	}

	_, _, err = st.UpsertWorkflowByPath(ctx, &Workflow{
		Format: WorkflowFormatUI, Graph: "{}", Source: WorkflowSourceScanned,
		SourcePath: "/hit.json", SizeBytes: 3,
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	got, err = st.GetWorkflowByPath(ctx, "/hit.json")
	if err != nil {
		t.Fatalf("hit err: %v", err)
	}
	if got == nil || got.SourcePath != "/hit.json" {
		t.Fatalf("hit mismatch: %+v", got)
	}
}

// TestFindVersionByFileName covers a match, no-match, and case-insensitive match,
// and confirms a substring (myfoo vs foo) does NOT spuriously match.
func TestFindVersionByFileName(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	seed := func(path string, model, version int) {
		t.Helper()
		if err := st.UpsertLocalFile(LocalFile{
			Path: path, SHA256: "h" + path, ModelID: intp(model), VersionID: intp(version),
			Kind: LocalKindModel, Status: LocalStatusMatched,
		}); err != nil {
			t.Fatalf("seed %s: %v", path, err)
		}
	}
	seed("/models/checkpoints/SDXL.safetensors", 10, 20)
	seed("/models/loras/mylora.safetensors", 11, 21)
	// An UNMATCHED file must be ignored even on a basename hit.
	if err := st.UpsertLocalFile(LocalFile{
		Path: "/models/checkpoints/orphan.safetensors", Kind: LocalKindModel, Status: LocalStatusUnmatched,
	}); err != nil {
		t.Fatalf("seed orphan: %v", err)
	}

	// Case-insensitive match on basename.
	m, v, ok, err := st.FindVersionByFileName(ctx, "sdxl.safetensors")
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if !ok || m == nil || *m != 10 || v == nil || *v != 20 {
		t.Errorf("case-insensitive match failed: ok=%v m=%v v=%v", ok, m, v)
	}

	// No match.
	if _, _, ok, _ := st.FindVersionByFileName(ctx, "nope.safetensors"); ok {
		t.Error("expected no match for nope.safetensors")
	}

	// Unmatched file's basename does not link.
	if _, _, ok, _ := st.FindVersionByFileName(ctx, "orphan.safetensors"); ok {
		t.Error("unmatched local file must not auto-link")
	}

	// Substring must not match: "lora.safetensors" != "mylora.safetensors".
	if _, _, ok, _ := st.FindVersionByFileName(ctx, "lora.safetensors"); ok {
		t.Error("substring should not match a longer basename")
	}

	// Blank is a clean no-match.
	if _, _, ok, _ := st.FindVersionByFileName(ctx, "  "); ok {
		t.Error("blank basename should not match")
	}

	// Ambiguous: the same filename shipped by TWO different models must NOT
	// auto-link (guessing would confidently attach the wrong version).
	seed("/a/comfy/models/checkpoints/vae.safetensors", 30, 40)
	seed("/b/auto/models/VAE/vae.safetensors", 31, 41)
	if m, v, ok, _ := st.FindVersionByFileName(ctx, "vae.safetensors"); ok {
		t.Errorf("ambiguous basename must not link, got m=%v v=%v", m, v)
	}

	// The SAME version under two paths is NOT ambiguous — still links.
	seed("/x/dup.safetensors", 50, 60)
	seed("/y/dup.safetensors", 50, 60)
	if _, v, ok, _ := st.FindVersionByFileName(ctx, "dup.safetensors"); !ok || v == nil || *v != 60 {
		t.Errorf("same-version duplicate should link, ok=%v v=%v", ok, v)
	}
}
