package web

import (
	"context"
	"strings"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/store"
)

// TestWorkflowListOrgControls proves the Workflows library tab renders the
// sort/filter/group controls bar and that each workflow card carries the data-*
// attributes the (browser-only) inline script drives. The JS behaviour itself is
// NOT exercised here — only the server-emitted markup the JS reads is asserted.
func TestWorkflowListOrgControls(t *testing.T) {
	srv := newWorkflowServer(t)
	ctx := context.Background()

	seed := []store.Workflow{
		{Name: "alpha portrait", Format: store.WorkflowFormatAPI, Graph: "{}",
			Source: store.WorkflowSourceImported, BaseModel: "SDXL 1.0"},
		{Name: "zeta landscape", Format: store.WorkflowFormatUI, Graph: "{}",
			Source: store.WorkflowSourceScanned, BaseModel: "Pony"},
		{Name: "mid upscale", Format: store.WorkflowFormatAPI, Graph: "{}",
			Source: store.WorkflowSourceCivitai}, // empty BaseModel → "Unspecified" bucket
	}
	for i := range seed {
		if _, err := srv.store.InsertWorkflow(ctx, &seed[i]); err != nil {
			t.Fatalf("seed workflow %d: %v", i, err)
		}
	}

	body := workflowsTabBody(t, srv)

	// --- Controls bar present ---
	for _, id := range []string{`id="cm-wf-sort"`, `id="cm-wf-q"`, `id="cm-wf-source"`,
		`id="cm-wf-format"`, `id="cm-wf-group"`, `id="cm-wf-list"`} {
		if !strings.Contains(body, id) {
			t.Errorf("missing control %s:\n%s", id, body)
		}
	}
	// Sort options.
	for _, label := range []string{"Imported (newest first)", "Imported (oldest first)",
		"Name A→Z", "Name Z→A"} {
		if !strings.Contains(body, label) {
			t.Errorf("missing sort option %q", label)
		}
	}
	// Source + format filter option labels.
	for _, label := range []string{"All sources", "Discovered", "Scanned",
		"All formats", "Runnable API"} {
		if !strings.Contains(body, label) {
			t.Errorf("missing filter option %q", label)
		}
	}
	// The group toggle control + its script hook.
	if !strings.Contains(body, "Group by base model") {
		t.Errorf("missing group-by-base toggle label")
	}
	if !strings.Contains(body, "cmWfApply()") {
		t.Errorf("controls should be wired to the client-side cmWfApply() driver")
	}
	if !strings.Contains(body, "cm-wf-group-header") {
		t.Errorf("the grouping script (which emits .cm-wf-group-header sections) should be present")
	}

	// --- Per-card data-* attributes the JS drives ---
	for _, want := range []string{
		`class="cm-wf-item"`,
		`data-name="alpha portrait"`,
		`data-name="zeta landscape"`,
		`data-source="imported"`,
		`data-source="scanned"`,
		`data-source="civitai"`,
		`data-format="api"`,
		`data-format="ui"`,
		`data-base="SDXL 1.0"`,
		`data-base="Pony"`,
		`data-base=""`, // empty base model buckets under "Unspecified" client-side
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing expected card attribute/markup %q:\n%s", want, body)
		}
	}
	// data-created must be a (sortable) numeric epoch, not blank.
	if !strings.Contains(body, `data-created="`) {
		t.Errorf("cards should carry a sortable data-created epoch")
	}
}

// TestWorkflowListEmptyStateHasNoControls proves the controls bar is omitted when
// there are no workflows (nothing to sort/filter/group).
func TestWorkflowListEmptyStateHasNoControls(t *testing.T) {
	srv := newWorkflowServer(t)
	body := workflowsTabBody(t, srv)
	if strings.Contains(body, `id="cm-wf-sort"`) {
		t.Errorf("empty workflow list should not render the controls bar")
	}
	if !strings.Contains(body, "No workflows yet") {
		t.Errorf("empty workflow list should show the empty state")
	}
}
