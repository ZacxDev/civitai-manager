package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/store"
)

// workflowsTabBody GETs the Workflows library tab and returns its HTML.
func workflowsTabBody(t *testing.T, srv *Server) string {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/library?tab=workflows", nil)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("workflows tab = %d", rec.Code)
	}
	return rec.Body.String()
}

// TestWorkflowListResolvesCachedModelAndVersion proves a list item whose model is
// in model_cache renders the linked model NAME + the version NAME (parsed from the
// cached raw detail) instead of bare ids.
func TestWorkflowListResolvesCachedModelAndVersion(t *testing.T) {
	srv := newWorkflowServer(t)
	ctx := context.Background()

	raw := []byte(`{"id":42,"name":"Cool Model","modelVersions":[{"id":99,"name":"v2.0 fp16"}]}`)
	if err := srv.store.PutModelCache(42, "Cool Model", raw); err != nil {
		t.Fatalf("cache model: %v", err)
	}
	if _, err := srv.store.InsertWorkflow(ctx, &store.Workflow{
		Name: "portrait", Format: store.WorkflowFormatAPI, Graph: "{}",
		Source: store.WorkflowSourceImported, ModelID: intp(42), VersionID: intp(99),
	}); err != nil {
		t.Fatalf("seed workflow: %v", err)
	}

	body := workflowsTabBody(t, srv)
	if !strings.Contains(body, `href="/models/42"`) {
		t.Errorf("missing model link:\n%s", body)
	}
	if !strings.Contains(body, "Cool Model") {
		t.Errorf("cached model name not rendered inline")
	}
	if !strings.Contains(body, "v2.0 fp16") {
		t.Errorf("version name not resolved from cached raw")
	}
	// A cached model name is inline, NOT lazy-loaded.
	if strings.Contains(body, `hx-get="/models/42/title"`) {
		t.Errorf("cached model should not lazy-load its title")
	}
}

// TestWorkflowListLazyLoadsUncachedModel proves a list item whose model is NOT
// cached falls back to the lazy /models/{id}/title load (the existing pattern) and
// a bare "version {id}" for the version.
func TestWorkflowListLazyLoadsUncachedModel(t *testing.T) {
	srv := newWorkflowServer(t)
	if _, err := srv.store.InsertWorkflow(context.Background(), &store.Workflow{
		Name: "uncached", Format: store.WorkflowFormatAPI, Graph: "{}",
		Source: store.WorkflowSourceImported, ModelID: intp(7), VersionID: intp(13),
	}); err != nil {
		t.Fatalf("seed workflow: %v", err)
	}

	body := workflowsTabBody(t, srv)
	if !strings.Contains(body, `hx-get="/models/7/title"`) || !strings.Contains(body, `hx-trigger="load"`) {
		t.Errorf("uncached model should lazy-load its title:\n%s", body)
	}
	if !strings.Contains(body, "version 13") {
		t.Errorf("uncached version should fall back to 'version 13'")
	}
}

// TestWorkflowListResourcesDisclosure proves the resource list expands into a
// <details> with per-file have ✓ / missing ✗ badges from the local-file check.
func TestWorkflowListResourcesDisclosure(t *testing.T) {
	srv := newWorkflowServer(t)
	ctx := context.Background()

	// One resource present locally, one absent.
	if err := srv.store.UpsertLocalFile(store.LocalFile{
		Path: "/models/present.safetensors", SizeBytes: 10,
		Status: store.LocalStatusMatched, Kind: store.LocalKindModel,
	}); err != nil {
		t.Fatalf("seed local file: %v", err)
	}
	if _, err := srv.store.InsertWorkflow(ctx, &store.Workflow{
		Name: "res", Format: store.WorkflowFormatAPI, Graph: "{}",
		Source:    store.WorkflowSourceImported,
		Resources: []string{"present.safetensors", "absent.safetensors"},
	}); err != nil {
		t.Fatalf("seed workflow: %v", err)
	}

	body := workflowsTabBody(t, srv)
	if !strings.Contains(body, "<details") || !strings.Contains(body, "<summary") {
		t.Errorf("resources should render as a <details> disclosure:\n%s", body)
	}
	if !strings.Contains(body, "2 resources") {
		t.Errorf("disclosure summary missing count")
	}
	if !strings.Contains(body, "present.safetensors") || !strings.Contains(body, "absent.safetensors") {
		t.Errorf("resource filenames missing")
	}
	if !strings.Contains(body, "have ✓") {
		t.Errorf("present resource should show a have ✓ badge")
	}
	if !strings.Contains(body, "missing ✗") {
		t.Errorf("absent resource should show a missing ✗ badge")
	}
}

// TestWorkflowListNoModelRendersCleanly proves a workflow with no model_id renders
// without crashing and without a stray model link.
func TestWorkflowListNoModelRendersCleanly(t *testing.T) {
	srv := newWorkflowServer(t)
	if _, err := srv.store.InsertWorkflow(context.Background(), &store.Workflow{
		Name: "orphan", Format: store.WorkflowFormatUI, Graph: "{}",
		Source: store.WorkflowSourceImported,
	}); err != nil {
		t.Fatalf("seed workflow: %v", err)
	}

	body := workflowsTabBody(t, srv)
	if !strings.Contains(body, "orphan") {
		t.Errorf("workflow name should render")
	}
	if strings.Contains(body, `href="/models/`) {
		t.Errorf("a model-less workflow should not render a model link")
	}
}

// TestWorkflowListEscapesModelName proves a cached model name is HTML-escaped in
// the list item (untrusted civitai text).
func TestWorkflowListEscapesModelName(t *testing.T) {
	srv := newWorkflowServer(t)
	ctx := context.Background()
	raw := []byte(`{"id":5,"name":"<script>x</script>","modelVersions":[]}`)
	if err := srv.store.PutModelCache(5, "<script>x</script>", raw); err != nil {
		t.Fatalf("cache model: %v", err)
	}
	if _, err := srv.store.InsertWorkflow(ctx, &store.Workflow{
		Name: "xss", Format: store.WorkflowFormatAPI, Graph: "{}",
		Source: store.WorkflowSourceImported, ModelID: intp(5),
	}); err != nil {
		t.Fatalf("seed workflow: %v", err)
	}
	body := workflowsTabBody(t, srv)
	if strings.Contains(body, "<script>x</script>") {
		t.Errorf("model name must be escaped, found raw <script>:\n%s", body)
	}
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Errorf("expected escaped model name")
	}
}
