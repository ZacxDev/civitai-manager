package web

import (
	"strings"
	"testing"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/store"
)

// TestWorkflowsTabPrimaryAndEmptyState proves the Workflows tab shows ONE clear
// primary ("Add a workflow") wired to the existing import dialog, a guided empty
// state with a CTA that opens the SAME dialog, and that all the existing plumbing
// (both import forms, CSRF, the stable scan-poll container) is intact.
func TestWorkflowsTabPrimaryAndEmptyState(t *testing.T) {
	srv := newWorkflowServer(t) // loopback → import/scan enabled, no workflows seeded
	body := workflowsTabBody(t, srv)

	if !strings.Contains(body, "Add a workflow") {
		t.Errorf("missing the single primary 'Add a workflow' affordance:\n%s", body)
	}
	// Guided empty state.
	if !strings.Contains(body, "No workflows yet") {
		t.Errorf("missing the guided empty state")
	}
	// A dialog-opening CTA is present (the onclick's quotes are HTML-escaped, so
	// assert on the stable showModal() call the import trigger + empty-state CTA share).
	if !strings.Contains(body, "showModal()") {
		t.Errorf("expected a CTA that opens the import dialog")
	}
	// Existing plumbing preserved: dialog, both import forms, CSRF, poll container.
	for _, want := range []string{
		`id="` + workflowImportDialogID + `"`,
		`action="/workflows/import"`,
		`action="/workflows/import-png"`,
		`name="csrf_token"`,
		`id="` + workflowScanResultsID + `"`, // the stable scan-poll container
		`hx-post="/library/workflow-scan"`,   // the scan form endpoint (in the container)
	} {
		if !strings.Contains(body, want) {
			t.Errorf("workflows tab lost existing plumbing %q", want)
		}
	}
}

// TestWorkflowsTabGatedOffLoopback proves the import/scan affordances are gated on
// a non-loopback bind: no "Add a workflow" CTA, the gating note is shown, and the
// empty state omits the dialog CTA (which would open a non-rendered dialog).
func TestWorkflowsTabGatedOffLoopback(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	srv := NewServer(st, stubReader{}, stubSubscriber{},
		Config{BaseURL: "https://civitai.com", DefaultPollInterval: time.Hour, Addr: "0.0.0.0:8972"}, nil)

	body := workflowsTabBody(t, srv)
	if strings.Contains(body, "Add a workflow") {
		t.Errorf("non-loopback bind must not offer the import primary:\n%s", body)
	}
	if !strings.Contains(body, "non-loopback") {
		t.Errorf("expected the loopback-gating note")
	}
	if strings.Contains(body, ".showModal()") {
		t.Errorf("gated empty state must not wire a dialog CTA")
	}
}

// TestModelFilesTabEmptyStateCTA proves the Model-files tab empty state (no install
// dirs) offers a single guided CTA over to the Install-directories tab and does NOT
// render a bare scan button.
func TestModelFilesTabEmptyStateCTA(t *testing.T) {
	out := renderString(t, libraryPage(buildLibraryView(nil), "csrf", true, nil, "files", nil, false, nil, fullMaturityRange(), libraryWorkflowsView{}))
	if !strings.Contains(out, "No install directories yet") {
		t.Errorf("missing guided empty state:\n%s", out)
	}
	if !strings.Contains(out, `href="/library?tab=sources"`) {
		t.Errorf("empty-state CTA should link to the Install-directories tab")
	}
	if strings.Contains(out, "Scan for model files") {
		t.Errorf("empty state must not render a bare scan button")
	}
}

// TestModelFilesTabPrimaryAndPollContainer proves that once a dir is added the tab
// shows the scan primary + the match-CivitAI toggle, with CSRF + the stable
// #scan-results poll container intact.
func TestModelFilesTabPrimaryAndPollContainer(t *testing.T) {
	out := renderString(t, libraryPage(buildLibraryView(nil), "csrf", true, []string{"/some/dir"}, "files", nil, false, nil, fullMaturityRange(), libraryWorkflowsView{}))
	for _, want := range []string{
		"Scan for model files",    // the primary
		`name="match_remote"`,     // the match-CivitAI toggle
		`hx-post="/library/scan"`, // the scan endpoint
		`id="scan-results"`,       // the stable poll container
		`name="csrf_token"`,       // CSRF
	} {
		if !strings.Contains(out, want) {
			t.Errorf("model-files tab missing %q:\n%s", want, out)
		}
	}
}

// TestSourcesTabPrimaryAndPollContainer proves the Install-directories tab keeps its
// discover primary + the stable #discover-results / #selected-dirs containers + a
// guided empty state, with CSRF intact.
func TestSourcesTabPrimaryAndPollContainer(t *testing.T) {
	out := renderString(t, libraryPage(buildLibraryView(nil), "csrf", true, nil, "sources", nil, false, nil, fullMaturityRange(), libraryWorkflowsView{}))
	for _, want := range []string{
		"Discover installs",           // the primary
		`hx-post="/library/discover"`, // the discover endpoint
		`id="discover-results"`,       // the stable discover poll container
		`id="selected-dirs"`,          // the persisted selection container
		"No install directories yet",  // guided empty copy
	} {
		if !strings.Contains(out, want) {
			t.Errorf("sources tab missing %q:\n%s", want, out)
		}
	}
}

// TestSourcesTabGatedOffLoopback proves the discover/browse affordances are gated on
// a non-loopback bind.
func TestSourcesTabGatedOffLoopback(t *testing.T) {
	out := renderString(t, libraryPage(buildLibraryView(nil), "csrf", false, nil, "sources", nil, false, nil, fullMaturityRange(), libraryWorkflowsView{}))
	if strings.Contains(out, "Discover installs") {
		t.Errorf("non-loopback bind must not offer discovery:\n%s", out)
	}
	if !strings.Contains(out, "non-loopback") {
		t.Errorf("expected the loopback-gating note")
	}
}
