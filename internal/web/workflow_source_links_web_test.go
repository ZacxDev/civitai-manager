package web

import (
	"strings"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/store"
)

// TestWorkflowSourceLinksModelLinked proves a model-linked workflow surfaces BOTH
// the external civitai.com link (with modelVersionId when set) AND the in-app
// model page link, plus a source chip.
func TestWorkflowSourceLinksModelLinked(t *testing.T) {
	wf := &store.Workflow{
		ID: 1, Name: "wf", Format: store.WorkflowFormatUI, Graph: "{}",
		Source: store.WorkflowSourceCivitai, ModelID: intp(42), VersionID: intp(99),
	}
	got := renderString(t, workflowSourceLinks(wf, workflowResolver{}))
	if !strings.Contains(got, `href="https://civitai.com/models/42?modelVersionId=99"`) {
		t.Errorf("missing external civitai link with modelVersionId:\n%s", got)
	}
	if !strings.Contains(got, "View on CivitAI ↗") {
		t.Errorf("missing external link label:\n%s", got)
	}
	if !strings.Contains(got, `href="/models/42"`) {
		t.Errorf("missing in-app model link:\n%s", got)
	}
	// Discovered source → "Discovered" chip.
	if !strings.Contains(got, "Discovered") {
		t.Errorf("missing source chip:\n%s", got)
	}
	// External link hardened.
	if !strings.Contains(got, `target="_blank"`) || !strings.Contains(got, `rel="noopener"`) {
		t.Errorf("external civitai link should be a hardened new-tab link:\n%s", got)
	}
}

// TestWorkflowSourceLinksModelNoVersion proves the external URL omits the
// modelVersionId when the workflow has no attached version.
func TestWorkflowSourceLinksModelNoVersion(t *testing.T) {
	wf := &store.Workflow{ID: 1, Name: "wf", Format: store.WorkflowFormatUI, Source: store.WorkflowSourceImported, ModelID: intp(7)}
	got := renderString(t, workflowSourceLinks(wf, workflowResolver{}))
	if !strings.Contains(got, `href="https://civitai.com/models/7"`) {
		t.Errorf("missing external link:\n%s", got)
	}
	if strings.Contains(got, "modelVersionId") {
		t.Errorf("no version → URL must not carry modelVersionId:\n%s", got)
	}
}

// TestWorkflowSourceLinksScannedPath proves a scanned workflow still shows its
// on-disk source path (escaped). PR C1 moved the path out of the always-visible
// provenance row and into the collapsed "Workflow metadata" disclosure — it is
// still rendered, just not in the reader's face — so the assertion follows it.
func TestWorkflowSourceLinksScannedPath(t *testing.T) {
	wf := &store.Workflow{
		ID: 2, Name: "scanned", Format: store.WorkflowFormatUI, Source: store.WorkflowSourceScanned,
		SourcePath: "/home/u/ComfyUI/user/default/workflows/x.json",
	}
	if got := renderString(t, workflowSourceLinks(wf, workflowResolver{})); !strings.Contains(got, "Scanned") {
		t.Errorf("missing Scanned source chip:\n%s", got)
	}
	reveal := renderString(t, workflowDetailsReveal(wf))
	if !strings.Contains(reveal, "/home/u/ComfyUI/user/default/workflows/x.json") {
		t.Errorf("scanned workflow should show its on-disk path in the metadata disclosure:\n%s", reveal)
	}
	if !strings.Contains(reveal, "On disk") {
		t.Errorf("the on-disk path should be labelled:\n%s", reveal)
	}
}

// TestWorkflowSourceLinksEscapesUntrusted proves an untrusted source path is
// HTML-escaped (never raw markup) wherever it is rendered.
func TestWorkflowSourceLinksEscapesUntrusted(t *testing.T) {
	wf := &store.Workflow{
		ID: 3, Name: "x", Format: store.WorkflowFormatUI, Source: store.WorkflowSourceScanned,
		SourcePath: `/x/<script>alert(1)</script>.json`,
	}
	got := renderString(t, workflowDetailsReveal(wf))
	if strings.Contains(got, "<script>alert(1)</script>") {
		t.Errorf("source path must be escaped:\n%s", got)
	}
	if !strings.Contains(got, "&lt;script&gt;") {
		t.Errorf("expected escaped source path:\n%s", got)
	}
}

// TestWorkflowSourceLinksNoModelNoLink proves a workflow with no model linkage
// renders only the source chip (no model links).
func TestWorkflowSourceLinksNoModelNoLink(t *testing.T) {
	wf := &store.Workflow{ID: 4, Name: "orphan", Format: store.WorkflowFormatUI, Source: store.WorkflowSourceAuthored}
	got := renderString(t, workflowSourceLinks(wf, workflowResolver{}))
	if strings.Contains(got, "civitai.com/models") || strings.Contains(got, `href="/models/`) {
		t.Errorf("model-less workflow must not render model links:\n%s", got)
	}
	if !strings.Contains(got, "Authored") {
		t.Errorf("missing Authored source chip:\n%s", got)
	}
}
