package library

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/store"
)

func intp(i int) *int { return &i }

// seedMatchedFile records a matched local_files row so the workflow scanner's
// auto-link can resolve a referenced filename to a version.
func seedMatchedFile(t *testing.T, st *store.Store, path string, model, version int) {
	t.Helper()
	if err := st.UpsertLocalFile(store.LocalFile{
		Path: path, SHA256: "h" + path, ModelID: intp(model), VersionID: intp(version),
		Kind: store.LocalKindModel, Status: store.LocalStatusMatched,
	}); err != nil {
		t.Fatalf("seed local file: %v", err)
	}
}

func TestScanWorkflowsAutoLinksUI(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	// The checkpoint the workflow references IS present locally and matched.
	seedMatchedFile(t, st, "/models/checkpoints/sdxl_base.safetensors", 100, 200)

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "portrait.json"),
		`{"nodes":[{"type":"CheckpointLoaderSimple","widgets_values":["sdxl_base.safetensors"]}]}`)

	ws := NewWorkflowScanner(st, nil)
	var streamed []WorkflowResult
	rep, err := ws.ScanWorkflows(ctx, []string{dir}, WorkflowScanOptions{
		OnWorkflow: func(r WorkflowResult) { streamed = append(streamed, r) },
	})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if rep.Found != 1 || rep.Linked != 1 {
		t.Fatalf("report = %+v, want Found=1 Linked=1", rep)
	}
	wf, err := st.GetWorkflowByPath(ctx, filepath.Join(dir, "portrait.json"))
	if err != nil || wf == nil {
		t.Fatalf("workflow not stored: %v", err)
	}
	if wf.Format != store.WorkflowFormatUI || wf.Source != store.WorkflowSourceScanned {
		t.Errorf("format/source wrong: %+v", wf)
	}
	if wf.Name != "portrait" {
		t.Errorf("name = %q, want portrait", wf.Name)
	}
	if wf.ModelID == nil || *wf.ModelID != 100 || wf.VersionID == nil || *wf.VersionID != 200 {
		t.Errorf("auto-link wrong: model=%v version=%v", wf.ModelID, wf.VersionID)
	}
	if len(streamed) != 1 || !streamed[0].Linked {
		t.Errorf("stream = %+v", streamed)
	}
}

func TestScanWorkflowsAPIFormat(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "api.json"),
		`{"3":{"class_type":"CheckpointLoaderSimple","inputs":{"ckpt_name":"foo.ckpt"}}}`)

	ws := NewWorkflowScanner(st, nil)
	rep, err := ws.ScanWorkflows(ctx, []string{dir}, WorkflowScanOptions{})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if rep.Found != 1 {
		t.Fatalf("report = %+v, want Found=1", rep)
	}
	wf, _ := st.GetWorkflowByPath(ctx, filepath.Join(dir, "api.json"))
	if wf == nil || wf.Format != store.WorkflowFormatAPI {
		t.Fatalf("api workflow not stored correctly: %+v", wf)
	}
	if len(wf.Resources) != 1 || wf.Resources[0] != "foo.ckpt" {
		t.Errorf("resources = %v", wf.Resources)
	}
}

func TestScanWorkflowsSkipsNonWorkflow(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "config.json"), `{"foo":1}`)

	ws := NewWorkflowScanner(st, nil)
	rep, err := ws.ScanWorkflows(ctx, []string{dir}, WorkflowScanOptions{})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if rep.Found != 0 || rep.Skipped != 1 {
		t.Errorf("report = %+v, want Found=0 Skipped=1", rep)
	}
	all, _ := st.ListWorkflows(ctx)
	if len(all) != 0 {
		t.Errorf("non-workflow json stored: %d rows", len(all))
	}
}

func TestScanWorkflowsRescanUnchanged(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	dir := t.TempDir()
	p := filepath.Join(dir, "flow.json")
	writeFile(t, p, `{"nodes":[]}`)
	// Pin a fixed mtime so the cache key is stable across the two scans.
	fixed := mustStatMtime(t, p)

	ws := NewWorkflowScanner(st, nil)
	if _, err := ws.ScanWorkflows(ctx, []string{dir}, WorkflowScanOptions{}); err != nil {
		t.Fatalf("scan1: %v", err)
	}
	// Restore the exact mtime (WalkDir/Upsert don't change it, but be explicit).
	_ = os.Chtimes(p, fixed, fixed)

	rep, err := ws.ScanWorkflows(ctx, []string{dir}, WorkflowScanOptions{})
	if err != nil {
		t.Fatalf("scan2: %v", err)
	}
	if rep.Unchanged != 1 || rep.Found != 0 {
		t.Errorf("re-scan report = %+v, want Unchanged=1 Found=0", rep)
	}
	all, _ := st.ListWorkflows(ctx)
	if len(all) != 1 {
		t.Errorf("re-scan duplicated the row: %d", len(all))
	}
}

func TestScanWorkflowsUnlinkedWhenAbsent(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	dir := t.TempDir()
	// References a checkpoint that is NOT in local_files → stored but unlinked.
	writeFile(t, filepath.Join(dir, "x.json"),
		`{"nodes":[{"type":"CheckpointLoaderSimple","widgets_values":["absent.safetensors"]}]}`)

	ws := NewWorkflowScanner(st, nil)
	rep, err := ws.ScanWorkflows(ctx, []string{dir}, WorkflowScanOptions{})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if rep.Found != 1 || rep.Linked != 0 {
		t.Errorf("report = %+v, want Found=1 Linked=0", rep)
	}
	wf, _ := st.GetWorkflowByPath(ctx, filepath.Join(dir, "x.json"))
	if wf == nil || wf.VersionID != nil {
		t.Errorf("should be stored unlinked: %+v", wf)
	}
}

func mustStatMtime(t *testing.T, path string) time.Time {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	return fi.ModTime()
}
