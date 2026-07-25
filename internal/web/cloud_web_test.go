package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/comfy"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// fakeCloud is an injectable cloudClient for the web cloud-run tests.
type fakeCloud struct {
	whatifResp *comfy.CloudWorkflow
	whatifErr  error
	runResp    *comfy.CloudWorkflow
	runErr     error
	getResp    *comfy.CloudWorkflow
	getErr     error

	submitCalls  int
	lastWhatif   bool
	lastTemplate comfy.CloudTemplate
	cancelCalled bool
}

func (f *fakeCloud) SubmitCloud(_ context.Context, tmpl comfy.CloudTemplate, whatif bool) (*comfy.CloudWorkflow, error) {
	f.submitCalls++
	f.lastWhatif = whatif
	f.lastTemplate = tmpl
	if whatif {
		return f.whatifResp, f.whatifErr
	}
	return f.runResp, f.runErr
}

func (f *fakeCloud) GetCloudWorkflow(_ context.Context, _ string) (*comfy.CloudWorkflow, error) {
	return f.getResp, f.getErr
}

func (f *fakeCloud) CancelCloudWorkflow(_ context.Context, _ string) error {
	f.cancelCalled = true
	return nil
}

func boolp(b bool) *bool { return &b }

// newCloudTestServer builds a loopback server with comfy_cloud enabled and a fake
// cloud client injected.
func newCloudTestServer(t *testing.T, fake *fakeCloud) *Server {
	t.Helper()
	srv := newLibraryTestServer(t, t.TempDir())
	srv.cfg.ComfyCloud = true
	srv.cfg.Token = "test-token"
	if fake != nil {
		srv.cloudClientFn = func() cloudClient { return fake }
	}
	return srv
}

func hasCloudPoller(body string) bool {
	return strings.Contains(body, `id="cloud-poll"`) &&
		strings.Contains(body, `hx-target="#cloud-status"`)
}

func pollCloudUntilDone(t *testing.T, srv *Server, id string) string {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		rec := get(t, srv, "/workflows/cloud/status?workflow_id="+id)
		if rec.Code != http.StatusOK {
			t.Fatalf("cloud status = %d", rec.Code)
		}
		body := rec.Body.String()
		if !hasCloudPoller(body) {
			return body
		}
		if time.Now().After(deadline) {
			t.Fatalf("cloud run did not finish; last body:\n%s", body)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestCloudPanelRendersTableAndWarning seeds a resolvable resource and asserts the
// GET panel shows the egress warning, the resolved-resource row (have ✓), and the
// editable URN textarea prefilled with the derived URN.
func TestCloudPanelRendersTableAndWarning(t *testing.T) {
	srv := newCloudTestServer(t, &fakeCloud{})
	// Seed a local file linked to model 10 / version 20 + its model cache.
	if err := srv.store.UpsertLocalFile(store.LocalFile{
		Path: "/m/checkpoints/good.safetensors", SHA256: "h", ModelID: intp(10), VersionID: intp(20),
		Kind: store.LocalKindModel, Status: store.LocalStatusMatched,
	}); err != nil {
		t.Fatalf("seed local file: %v", err)
	}
	if err := srv.store.PutModelCache(10, "Good",
		[]byte(`{"id":10,"type":"Checkpoint","modelVersions":[{"id":20,"baseModel":"SDXL 1.0"}]}`)); err != nil {
		t.Fatalf("seed model cache: %v", err)
	}
	id := seedWorkflow(t, srv, store.WorkflowFormatAPI,
		`{"1":{"class_type":"CheckpointLoaderSimple","inputs":{"ckpt_name":"good.safetensors"}}}`)

	rec := get(t, srv, "/workflows/"+id+"/cloud")
	if rec.Code != http.StatusOK {
		t.Fatalf("cloud panel = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"spends Buzz",                           // egress + Buzz warning
		"good.safetensors",                      // resource filename
		"have ✓",                                // resolved status badge
		"urn:air:sdxl:checkpoint:civitai:10@20", // derived URN (in table + textarea)
		`name="resources"`,                      // editable textarea
		"Estimate cost",                         // estimate button
	} {
		if !strings.Contains(body, want) {
			t.Errorf("cloud panel missing %q:\n%s", want, body)
		}
	}
}

// TestCloudPanelDisabledWhenOff asserts the panel shows the enable-comfy_cloud note
// when the feature is off.
func TestCloudPanelDisabledWhenOff(t *testing.T) {
	srv := newLibraryTestServer(t, t.TempDir()) // comfy_cloud stays false
	id := seedWorkflow(t, srv, store.WorkflowFormatAPI, `{"1":{"class_type":"X","inputs":{}}}`)
	rec := get(t, srv, "/workflows/"+id+"/cloud")
	if !strings.Contains(rec.Body.String(), "comfy_cloud") {
		t.Errorf("expected enable-comfy_cloud note:\n%s", rec.Body.String())
	}
}

// TestCloudPanelUIFormatRequiresAPI asserts a UI-format workflow shows the
// API-format-required limitation instead of the run controls.
func TestCloudPanelUIFormatRequiresAPI(t *testing.T) {
	srv := newCloudTestServer(t, &fakeCloud{})
	ui := `{"nodes":[{"id":4,"type":"CheckpointLoaderSimple","widgets_values":["a.safetensors"]}],"links":[]}`
	id := seedWorkflow(t, srv, store.WorkflowFormatUI, ui)
	rec := get(t, srv, "/workflows/"+id+"/cloud")
	if !strings.Contains(rec.Body.String(), "API-format") {
		t.Errorf("expected API-format-required note:\n%s", rec.Body.String())
	}
}

// TestCloudWhatifCSRFBeforeSideEffect asserts a whatif POST without a CSRF token is
// rejected 403 and NEVER calls SubmitCloud.
func TestCloudWhatifCSRFBeforeSideEffect(t *testing.T) {
	fake := &fakeCloud{}
	srv := newCloudTestServer(t, fake)
	id := seedWorkflow(t, srv, store.WorkflowFormatAPI, `{"1":{"class_type":"X","inputs":{}}}`)
	rec := post(t, srv, "/workflows/"+id+"/cloud/whatif",
		url.Values{"resources": {"urn:air:sdxl:checkpoint:civitai:1@2"}}, false)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("whatif without CSRF = %d, want 403", rec.Code)
	}
	if fake.submitCalls != 0 {
		t.Errorf("SubmitCloud must NOT be called before CSRF passes (calls=%d)", fake.submitCalls)
	}
}

// TestCloudRunCSRFBeforeSideEffect asserts the real-run POST without CSRF is 403 and
// starts no job / makes no submit.
func TestCloudRunCSRFBeforeSideEffect(t *testing.T) {
	fake := &fakeCloud{}
	srv := newCloudTestServer(t, fake)
	id := seedWorkflow(t, srv, store.WorkflowFormatAPI, `{"1":{"class_type":"X","inputs":{}}}`)
	rec := post(t, srv, "/workflows/"+id+"/cloud/run",
		url.Values{"resources": {"urn:air:sdxl:checkpoint:civitai:1@2"}}, false)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("run without CSRF = %d, want 403", rec.Code)
	}
	if fake.submitCalls != 0 {
		t.Errorf("SubmitCloud must NOT be called before CSRF passes (calls=%d)", fake.submitCalls)
	}
	if snap := srv.cloudJobState(); snap.Started {
		t.Errorf("no cloud job should have started")
	}
}

// TestCloudLoopbackGated asserts the cloud endpoints are gated off a non-loopback
// bind.
func TestCloudLoopbackGated(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	srv := NewServer(st, stubReader{}, stubSubscriber{}, Config{
		BaseURL: "https://civitai.com", DefaultPollInterval: time.Hour,
		Addr: "0.0.0.0:8787", ComfyCloud: true, Token: "t",
	}, nil)
	id := seedWorkflow(t, srv, store.WorkflowFormatAPI, `{"1":{"class_type":"X","inputs":{}}}`)
	rec := get(t, srv, "/workflows/"+id+"/cloud")
	if !strings.Contains(rec.Body.String(), "non-loopback") {
		t.Errorf("expected gating note off-loopback:\n%s", rec.Body.String())
	}
}

// TestCloudWhatifRendersCost asserts a whatif estimate renders the cost + a
// Run-for-real control.
func TestCloudWhatifRendersCost(t *testing.T) {
	fake := &fakeCloud{whatifResp: &comfy.CloudWorkflow{
		ID: "wf-1", Status: "unassigned",
		Cost:        &comfy.CloudCost{Base: 120},
		Transaction: &comfy.CloudTransactions{InsufficientBuzz: boolp(false)},
	}}
	srv := newCloudTestServer(t, fake)
	id := seedWorkflow(t, srv, store.WorkflowFormatAPI, `{"1":{"class_type":"X","inputs":{}}}`)

	rec := post(t, srv, "/workflows/"+id+"/cloud/whatif",
		url.Values{"resources": {"urn:air:sdxl:checkpoint:civitai:1@2"}}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("whatif = %d", rec.Code)
	}
	body := rec.Body.String()
	if !fake.lastWhatif {
		t.Error("SubmitCloud should have been called with whatif=true")
	}
	if !strings.Contains(body, "120 Buzz") {
		t.Errorf("estimate missing cost:\n%s", body)
	}
	if !strings.Contains(body, "Run for real") {
		t.Errorf("estimate missing Run-for-real control:\n%s", body)
	}
	// The submitted template must carry the edited URN + trace none + customComfy.
	if len(fake.lastTemplate.Steps) != 1 || fake.lastTemplate.Steps[0].Type != "customComfy" {
		t.Errorf("template shape wrong: %+v", fake.lastTemplate)
	}
	if fake.lastTemplate.Steps[0].Input.Trace != "none" {
		t.Errorf("trace = %q", fake.lastTemplate.Steps[0].Input.Trace)
	}
	if got := fake.lastTemplate.Steps[0].Input.Resources; len(got) != 1 ||
		got[0] != "urn:air:sdxl:checkpoint:civitai:1@2" {
		t.Errorf("resources = %v", got)
	}
}

// TestCloudWhatifInsufficientBuzz asserts the insufficient-Buzz state disables the
// run (no Run-for-real button).
func TestCloudWhatifInsufficientBuzz(t *testing.T) {
	fake := &fakeCloud{whatifResp: &comfy.CloudWorkflow{
		ID: "wf-1", Cost: &comfy.CloudCost{Base: 9000},
		Transaction: &comfy.CloudTransactions{InsufficientBuzz: boolp(true)},
	}}
	srv := newCloudTestServer(t, fake)
	id := seedWorkflow(t, srv, store.WorkflowFormatAPI, `{"1":{"class_type":"X","inputs":{}}}`)
	rec := post(t, srv, "/workflows/"+id+"/cloud/whatif", url.Values{"resources": {"u"}}, true)
	body := rec.Body.String()
	if !strings.Contains(body, "Insufficient Buzz") {
		t.Errorf("missing insufficient-Buzz state:\n%s", body)
	}
	if strings.Contains(body, "Run for real") {
		t.Errorf("Run-for-real must be disabled when Buzz is insufficient:\n%s", body)
	}
}

// TestCloudWhatifErrorEscaped asserts a ProblemDetails error is surfaced and
// HTML-escaped.
func TestCloudWhatifErrorEscaped(t *testing.T) {
	fake := &fakeCloud{whatifErr: &comfy.CloudProblem{
		StatusCode: 400, Title: "Bad Request", Detail: `resource <script>x</script> missing`,
	}}
	srv := newCloudTestServer(t, fake)
	id := seedWorkflow(t, srv, store.WorkflowFormatAPI, `{"1":{"class_type":"X","inputs":{}}}`)
	rec := post(t, srv, "/workflows/"+id+"/cloud/whatif", url.Values{"resources": {"u"}}, true)
	body := rec.Body.String()
	if !strings.Contains(body, "Estimate failed") {
		t.Errorf("missing estimate-failed alert:\n%s", body)
	}
	if strings.Contains(body, "<script>x</script>") {
		t.Errorf("untrusted error detail not escaped:\n%s", body)
	}
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Errorf("error detail should be HTML-escaped:\n%s", body)
	}
}

// TestCloudRunTerminalGallery drives a real run whose submit returns a terminal
// succeeded workflow with blob URLs, and asserts the poll status renders the gallery.
func TestCloudRunTerminalGallery(t *testing.T) {
	fake := &fakeCloud{runResp: &comfy.CloudWorkflow{
		ID: "wf-done", Status: "succeeded",
		Steps: []comfy.CloudWorkflowStep{{Name: "comfy", Status: "succeeded", Output: &comfy.CloudStepOutput{
			Blobs: []comfy.CloudBlob{{ID: "b1", URL: "https://image.civitai.com/result.jpeg", Available: true}},
		}}},
	}}
	srv := newCloudTestServer(t, fake)
	id := seedWorkflow(t, srv, store.WorkflowFormatAPI, `{"1":{"class_type":"X","inputs":{}}}`)

	rec := post(t, srv, "/workflows/"+id+"/cloud/run", url.Values{"resources": {"u"}}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("cloud run = %d", rec.Code)
	}
	body := pollCloudUntilDone(t, srv, id)
	if !strings.Contains(body, "Cloud run complete") {
		t.Errorf("missing completion line:\n%s", body)
	}
	if !strings.Contains(body, "https://image.civitai.com/result.jpeg") {
		t.Errorf("gallery missing blob URL:\n%s", body)
	}
	if fake.lastWhatif {
		t.Error("real run must submit with whatif=false")
	}
}

// TestCloudRunRunningThenStop starts a run that stays running (submit non-terminal),
// asserts the running fragment + poller, then stops it (asserting the remote cancel).
func TestCloudRunRunningThenStop(t *testing.T) {
	// Submit returns non-terminal; Get keeps returning processing so the job stays
	// running until Stop cancels it.
	fake := &fakeCloud{
		runResp: &comfy.CloudWorkflow{ID: "wf-run", Status: "processing"},
		getResp: &comfy.CloudWorkflow{ID: "wf-run", Status: "processing"},
	}
	srv := newCloudTestServer(t, fake)
	// Shrink the poll cadence on THIS server instance (set before any run starts,
	// never mutated after) for a fast, deterministic transition — no shared global,
	// so the poll goroutine's read cannot race a write.
	srv.cloudPollInterval = 5 * time.Millisecond
	id := seedWorkflow(t, srv, store.WorkflowFormatAPI, `{"1":{"class_type":"X","inputs":{}}}`)

	rec := post(t, srv, "/workflows/"+id+"/cloud/run", url.Values{"resources": {"u"}}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("cloud run = %d", rec.Code)
	}
	if !hasCloudPoller(rec.Body.String()) {
		t.Fatalf("run start should be the running fragment:\n%s", rec.Body.String())
	}

	// The run goroutine records the remote workflow id asynchronously (after Submit
	// returns). Stop can only best-effort remote-cancel once that id exists, so wait
	// for it — otherwise this races the goroutine and the cancel assertion flakes.
	deadline := time.Now().Add(2 * time.Second)
	for srv.cloudJobState().CloudID == "" {
		if time.Now().After(deadline) {
			t.Fatal("run goroutine never recorded the remote cloud workflow id")
		}
		time.Sleep(time.Millisecond)
	}

	rec = post(t, srv, "/workflows/cloud/stop", url.Values{"workflow_id": {id}}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("cloud stop = %d", rec.Code)
	}
	body := rec.Body.String()
	if hasCloudPoller(body) {
		t.Errorf("stop response should be poller-less:\n%s", body)
	}
	if !strings.Contains(body, "canceled") {
		t.Errorf("stop response missing canceled state:\n%s", body)
	}
	if !fake.cancelCalled {
		t.Error("stop should best-effort cancel the remote cloud workflow")
	}
}
