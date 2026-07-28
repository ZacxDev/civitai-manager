package web

import (
	"context"
	"strings"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/store"
)

// TestWorkflowItemCarriesAnchorID proves each rendered list item carries the
// stable id="wf-<id>" anchor the "View in library" deep-link targets.
func TestWorkflowItemCarriesAnchorID(t *testing.T) {
	srv := newWorkflowServer(t)
	id, err := srv.store.InsertWorkflow(context.Background(), &store.Workflow{
		Name: "anchored", Format: store.WorkflowFormatUI, Graph: "{}",
		Source: store.WorkflowSourceImported,
	})
	if err != nil {
		t.Fatalf("seed workflow: %v", err)
	}
	body := workflowsTabBody(t, srv)
	want := `id="wf-` + itoa64(id) + `"`
	if !strings.Contains(body, want) {
		t.Errorf("missing item anchor %q:\n%s", want, body)
	}
}

// TestWorkflowDeeplinkScriptPresent proves the workflows tab ships the deep-link
// handler + the highlight class it applies, so the scroll-to/highlight is wired.
func TestWorkflowDeeplinkScriptPresent(t *testing.T) {
	srv := newWorkflowServer(t)
	if _, err := srv.store.InsertWorkflow(context.Background(), &store.Workflow{
		Name: "x", Format: store.WorkflowFormatUI, Graph: "{}",
		Source: store.WorkflowSourceImported,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	body := workflowsTabBody(t, srv)
	for _, want := range []string{
		"function cmWfDeeplink(",
		"cm-wf-highlight",
		"#wf-",
		"htmx:afterSettle",
		"__cmWfDeeplinkBound", // the single-bind guard survives poller swaps
	} {
		if !strings.Contains(body, want) {
			t.Errorf("deep-link script missing %q", want)
		}
	}
}

// TestDiscoverImportResultDeeplinksToItem proves the single-import "View in
// library" outcome deep-links to the item anchor (/library?tab=workflows#wf-<id>)
// rather than the standalone detail page.
func TestDiscoverImportResultDeeplinksToItem(t *testing.T) {
	got := renderString(t, workflowImportResult(42, "Imported 1 workflow.", true, 7))
	if !strings.Contains(got, `href="/library?tab=workflows#wf-7"`) {
		t.Errorf("single-import result should deep-link to the item anchor:\n%s", got)
	}
	// Zero/multi import falls back to the plain tab (no per-item anchor).
	multi := renderString(t, workflowImportResult(42, "Imported 3 workflows.", true, 0))
	if !strings.Contains(multi, `href="/library?tab=workflows"`) {
		t.Errorf("multi-import result should link to the workflows tab:\n%s", multi)
	}
	if strings.Contains(multi, "#wf-") {
		t.Errorf("multi-import result must not carry a per-item anchor:\n%s", multi)
	}
}
