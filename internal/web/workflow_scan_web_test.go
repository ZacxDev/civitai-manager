package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/library"
)

// hasWorkflowScanPoller reports whether body is a workflow-scan scanning fragment
// (still polling): the re-arming poller targeting the stable container.
func hasWorkflowScanPoller(body string) bool {
	for _, want := range []string{
		`hx-get="/library/workflow-scan/status"`,
		`hx-trigger="load delay:1s"`,
		`hx-target="#workflow-scan-results"`,
	} {
		if !strings.Contains(body, want) {
			return false
		}
	}
	return true
}

func pollWorkflowScanUntilDone(t *testing.T, srv *Server) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		rec := get(t, srv, "/library/workflow-scan/status")
		if rec.Code != http.StatusOK {
			t.Fatalf("workflow-scan status = %d", rec.Code)
		}
		body := rec.Body.String()
		if !hasWorkflowScanPoller(body) {
			return body
		}
		if time.Now().After(deadline) {
			t.Fatalf("workflow scan did not finish; last body:\n%s", body)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestWorkflowScanStartAndComplete drives an injected seam through running →
// terminal and asserts the poller structure and terminal completion line.
func TestWorkflowScanStartAndComplete(t *testing.T) {
	srv := newLibraryTestServer(t, t.TempDir())
	release := make(chan struct{})
	srv.workflowScanFn = func(ctx context.Context, on func(library.WorkflowResult)) (*library.WorkflowScanReport, error) {
		<-release
		on(library.WorkflowResult{Path: "/w/a.json", Name: "a", Format: "ui",
			ModelID: intp(3), VersionID: intp(4), Linked: true})
		on(library.WorkflowResult{Path: "/w/b.json", Name: "b", Format: "api"})
		return &library.WorkflowScanReport{Found: 2, Linked: 1, Skipped: 1}, nil
	}

	rec := post(t, srv, "/library/workflow-scan", url.Values{}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("workflow-scan POST = %d", rec.Code)
	}
	if !hasWorkflowScanPoller(rec.Body.String()) {
		t.Fatalf("POST response should be the scanning fragment with a poller:\n%s", rec.Body.String())
	}
	if !srv.workflowScanJobState().Running {
		t.Fatal("job should be running after POST")
	}

	close(release)
	term := pollWorkflowScanUntilDone(t, srv)
	if !strings.Contains(term, "Scan complete") || !strings.Contains(term, "2 found · 1 linked") {
		t.Errorf("terminal missing completion line:\n%s", term)
	}
	if strings.Contains(term, `id="workflow-scan-poll"`) {
		t.Errorf("terminal must not carry a poller:\n%s", term)
	}
}

// TestWorkflowScanStop cancels a running scan and drives it to the stopped
// terminal fragment.
func TestWorkflowScanStop(t *testing.T) {
	srv := newLibraryTestServer(t, t.TempDir())
	entered := make(chan struct{})
	srv.workflowScanFn = func(ctx context.Context, on func(library.WorkflowResult)) (*library.WorkflowScanReport, error) {
		close(entered)
		<-ctx.Done()
		return &library.WorkflowScanReport{}, ctx.Err()
	}

	if rec := post(t, srv, "/library/workflow-scan", url.Values{}, true); rec.Code != http.StatusOK {
		t.Fatalf("start = %d", rec.Code)
	}
	<-entered

	rec := post(t, srv, "/library/workflow-scan/stop", url.Values{}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("stop = %d", rec.Code)
	}
	// The stop response is deterministically the poller-less terminal fragment.
	if strings.Contains(rec.Body.String(), `id="workflow-scan-poll"`) {
		t.Errorf("stop response must be poller-less:\n%s", rec.Body.String())
	}
	term := pollWorkflowScanUntilDone(t, srv)
	if !strings.Contains(term, "Scan stopped") {
		t.Errorf("expected 'Scan stopped' after stop:\n%s", term)
	}
}

// TestWorkflowScanCSRF rejects a POST without a token and starts no job.
func TestWorkflowScanCSRF(t *testing.T) {
	srv := newLibraryTestServer(t, t.TempDir())
	srv.workflowScanFn = func(ctx context.Context, on func(library.WorkflowResult)) (*library.WorkflowScanReport, error) {
		return &library.WorkflowScanReport{}, nil
	}
	rec := post(t, srv, "/library/workflow-scan", url.Values{}, false)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("CSRF-less workflow-scan = %d, want 403", rec.Code)
	}
	if srv.workflowScanJobState().Started {
		t.Error("CSRF-rejected POST must not start a job")
	}
}

// TestWorkflowScanLoopbackGate refuses the scan endpoints on a non-loopback bind.
func TestWorkflowScanLoopbackGate(t *testing.T) {
	srv := newLibraryTestServer(t, t.TempDir())
	srv.cfg.Addr = "0.0.0.0:8972" // non-loopback → gate closed
	srv.workflowScanFn = func(ctx context.Context, on func(library.WorkflowResult)) (*library.WorkflowScanReport, error) {
		return &library.WorkflowScanReport{}, nil
	}
	rec := post(t, srv, "/library/workflow-scan", url.Values{}, true)
	if rec.Code == http.StatusOK && srv.workflowScanJobState().Started {
		t.Fatal("non-loopback workflow-scan should be gated, not started")
	}
	if !strings.Contains(rec.Body.String(), "non-loopback") {
		t.Errorf("expected gating note, got:\n%s", rec.Body.String())
	}
	// The status endpoint is gated too.
	statusRec := get(t, srv, "/library/workflow-scan/status")
	if !strings.Contains(statusRec.Body.String(), "non-loopback") {
		t.Errorf("status should be gated off-loopback:\n%s", statusRec.Body.String())
	}
}
