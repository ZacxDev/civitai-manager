package web

import (
	"context"
	"errors"
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
	return newCloudTestServerDB(t, fake, ":memory:")
}

// newCloudTestServerDB is newCloudTestServer over an explicit database path. Reach
// for it when the test runs DDL — see newLibraryTestServerDB for why the default
// ":memory:" is shared across the whole process and why a DROP against it is a
// landmine for whichever server happens to be alive alongside.
func newCloudTestServerDB(t *testing.T, fake *fakeCloud, dbPath string) *Server {
	t.Helper()
	srv := newLibraryTestServerDB(t, t.TempDir(), dbPath)
	srv.cfg.ComfyCloud = boolp(true)
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

// uiCheckpointGraph + uiCheckpointInfo are a minimal UI-format workflow and the
// matching /object_info schema that ConvertUIToAPI converts to API format cleanly
// (no warnings). Shared by the cloud UI→API conversion tests.
const uiCheckpointGraph = `{"nodes":[{"id":4,"type":"CheckpointLoaderSimple","widgets_values":["good.safetensors"]}],"links":[]}`
const uiCheckpointInfo = `{"CheckpointLoaderSimple":{"input":{"required":{"ckpt_name":[["good.safetensors"],{}]}},"input_order":{"required":["ckpt_name"]}}}`

// TestCloudPanelUIFormatNoComfy asserts a UI-format workflow with NO reachable local
// ComfyUI shows the reworded reachability note (cloud converts via local ComfyUI),
// not a flat "API-format required".
func TestCloudPanelUIFormatNoComfy(t *testing.T) {
	srv := newCloudTestServer(t, &fakeCloud{}) // no comfyClientFn, ComfyURL empty → s.comfy()==nil
	id := seedWorkflow(t, srv, store.WorkflowFormatUI, uiCheckpointGraph)
	rec := get(t, srv, "/workflows/"+id+"/cloud")
	body := rec.Body.String()
	if !strings.Contains(body, "converts it to API format using your local ComfyUI") {
		t.Errorf("expected the local-ComfyUI conversion reachability note:\n%s", body)
	}
}

// TestCloudPanelUIFormatConvertsShowsResources asserts a UI-format workflow with a
// reachable local ComfyUI whose /object_info yields a clean conversion shows the
// resolved-resources table (from the CONVERTED graph) + a "will convert" note.
func TestCloudPanelUIFormatConvertsShowsResources(t *testing.T) {
	srv := newCloudTestServer(t, &fakeCloud{})
	srv.comfyClientFn = func() comfyClient {
		return &fakeComfy{info: mustObjectInfo(t, uiCheckpointInfo)}
	}
	// Seed the local file + model cache so the converted graph's ckpt resolves to a URN.
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
	id := seedWorkflow(t, srv, store.WorkflowFormatUI, uiCheckpointGraph)

	rec := get(t, srv, "/workflows/"+id+"/cloud")
	if rec.Code != http.StatusOK {
		t.Fatalf("cloud panel = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"converts it to API format",             // the will-convert note
		"good.safetensors",                      // resource filename from the CONVERTED graph
		"urn:air:sdxl:checkpoint:civitai:10@20", // derived URN
		"Estimate cost",                         // run controls present (runnable)
	} {
		if !strings.Contains(body, want) {
			t.Errorf("converted-panel missing %q:\n%s", want, body)
		}
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
		Addr: "0.0.0.0:8787", ComfyCloud: boolp(true), Token: "t",
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

// TestCloudWhatifZeroCostShowsPerSecondNotice covers the real CustomComfy case:
// whatif returns cost.base=0 (billing is per-second, computed post-run). We must
// NOT render a misleading fixed "0 Buzz" price — instead show the per-second
// notice and still allow Run.
func TestCloudWhatifZeroCostShowsPerSecondNotice(t *testing.T) {
	fake := &fakeCloud{whatifResp: &comfy.CloudWorkflow{
		ID: "wf-0", Status: "unassigned",
		Cost: &comfy.CloudCost{Base: 0},
	}}
	srv := newCloudTestServer(t, fake)
	id := seedWorkflow(t, srv, store.WorkflowFormatAPI, `{"1":{"class_type":"X","inputs":{}}}`)

	rec := post(t, srv, "/workflows/"+id+"/cloud/whatif", url.Values{"resources": {"u"}}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("whatif = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "per second of GPU time") {
		t.Errorf("zero-cost estimate must show the per-second billing notice:\n%s", body)
	}
	if strings.Contains(body, "0 Buzz") {
		t.Errorf("must NOT render a misleading fixed '0 Buzz' price:\n%s", body)
	}
	if !strings.Contains(body, "Run for real") {
		t.Errorf("Run-for-real must still be available:\n%s", body)
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
	if !strings.Contains(body, "Not enough Buzz") {
		t.Errorf("missing insufficient-Buzz state:\n%s", body)
	}
	if strings.Contains(body, "Run for real") {
		t.Errorf("Run-for-real must be disabled when Buzz is insufficient:\n%s", body)
	}
	// 🔴 The button's ABSENCE must not be the only signal. The old copy was a bare
	// "Your account does not have enough Buzz to run this workflow." — true, but it
	// never said that a control had been withheld, so a page missing its primary
	// action for an unstated reason reads as broken rather than as blocked.
	if !strings.Contains(body, "the run button is deliberately not shown") {
		t.Errorf("the block must SAY the run control was withheld, not just omit it:\n%s", body)
	}
	// …and it must name whose balance this is, because there is no sign-in on this
	// page and the account is entirely implicit otherwise.
	if !strings.Contains(body, "API token") {
		t.Errorf("the block must name the account the Buzz belongs to:\n%s", body)
	}
}

// TestCloudWhatifErrorEscaped asserts a NON-400 ProblemDetails error is surfaced as
// a generic estimate failure and HTML-escaped. (A 400 is the affordability path,
// covered separately.)
func TestCloudWhatifErrorEscaped(t *testing.T) {
	fake := &fakeCloud{whatifErr: &comfy.CloudProblem{
		StatusCode: 500, Title: "Server Error", Detail: `resource <script>x</script> missing`,
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

// gateOf returns the submitted template's minimumDurationSeconds (nil-safe: -1 when
// the template has no step, -2 when the gate is unset/nil).
func gateOf(tmpl comfy.CloudTemplate) int {
	if len(tmpl.Steps) == 0 {
		return -1
	}
	p := tmpl.Steps[0].Input.MinimumDurationSeconds
	if p == nil {
		return -2
	}
	return *p
}

// TestCloudWhatifAppliesGate asserts the whatif preview submits WITH the 5-min
// affordability gate so the user gets a free affordability preview.
func TestCloudWhatifAppliesGate(t *testing.T) {
	fake := &fakeCloud{whatifResp: &comfy.CloudWorkflow{ID: "wf-1", Status: "unassigned", Cost: &comfy.CloudCost{Base: 0}}}
	srv := newCloudTestServer(t, fake)
	id := seedWorkflow(t, srv, store.WorkflowFormatAPI, `{"1":{"class_type":"X","inputs":{}}}`)
	rec := post(t, srv, "/workflows/"+id+"/cloud/whatif", url.Values{"resources": {"u"}}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("whatif = %d", rec.Code)
	}
	if !fake.lastWhatif {
		t.Error("whatif=true expected")
	}
	if got := gateOf(fake.lastTemplate); got != 300 {
		t.Errorf("whatif template gate = %d, want 300", got)
	}
}

// TestCloudWhatifAffordabilityRejection covers a gated whatif that returns a 400
// affordability problem: the fragment must render the (escaped) detail + a Run-anyway
// button, NOT a generic "Estimate failed".
func TestCloudWhatifAffordabilityRejection(t *testing.T) {
	fake := &fakeCloud{whatifErr: &comfy.CloudProblem{
		StatusCode: 400, Title: "Bad Request",
		Detail: `You can only afford 42s <script>x</script> of the 300s minimum`,
	}}
	srv := newCloudTestServer(t, fake)
	id := seedWorkflow(t, srv, store.WorkflowFormatAPI, `{"1":{"class_type":"X","inputs":{}}}`)
	rec := post(t, srv, "/workflows/"+id+"/cloud/whatif", url.Values{"resources": {"u"}}, true)
	body := rec.Body.String()
	if strings.Contains(body, "Estimate failed") {
		t.Errorf("400 affordability must NOT be a generic estimate failure:\n%s", body)
	}
	if !strings.Contains(body, "You can only afford 42s") {
		t.Errorf("affordability detail missing:\n%s", body)
	}
	if strings.Contains(body, "<script>x</script>") {
		t.Errorf("untrusted detail not escaped:\n%s", body)
	}
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Errorf("detail should be HTML-escaped:\n%s", body)
	}
	if !strings.Contains(body, "run_anyway") || !strings.Contains(body, "Run anyway") {
		t.Errorf("affordability preview must offer a Run-anyway button:\n%s", body)
	}
}

// TestCloudRunDefaultAppliesGate asserts a default real run (no run_anyway) submits
// WITH minimumDurationSeconds=300.
func TestCloudRunDefaultAppliesGate(t *testing.T) {
	fake := &fakeCloud{runResp: &comfy.CloudWorkflow{ID: "wf-done", Status: "succeeded"}}
	srv := newCloudTestServer(t, fake)
	id := seedWorkflow(t, srv, store.WorkflowFormatAPI, `{"1":{"class_type":"X","inputs":{}}}`)
	if rec := post(t, srv, "/workflows/"+id+"/cloud/run", url.Values{"resources": {"u"}}, true); rec.Code != http.StatusOK {
		t.Fatalf("cloud run = %d", rec.Code)
	}
	pollCloudUntilDone(t, srv, id)
	if fake.lastWhatif {
		t.Error("real run must submit whatif=false")
	}
	if got := gateOf(fake.lastTemplate); got != 300 {
		t.Errorf("default run template gate = %d, want 300", got)
	}
}

// TestCloudRunAnywaySkipsGate asserts run_anyway=1 submits WITHOUT the gate (nil) and
// the run still starts and polls to completion.
func TestCloudRunAnywaySkipsGate(t *testing.T) {
	fake := &fakeCloud{runResp: &comfy.CloudWorkflow{
		ID: "wf-done", Status: "succeeded",
		Steps: []comfy.CloudWorkflowStep{{Name: "comfy", Status: "succeeded", Output: &comfy.CloudStepOutput{
			Blobs: []comfy.CloudBlob{{ID: "b1", URL: "https://image.civitai.com/ok.jpeg", Available: true}},
		}}},
	}}
	srv := newCloudTestServer(t, fake)
	id := seedWorkflow(t, srv, store.WorkflowFormatAPI, `{"1":{"class_type":"X","inputs":{}}}`)
	rec := post(t, srv, "/workflows/"+id+"/cloud/run",
		url.Values{"resources": {"u"}, "run_anyway": {"1"}}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("cloud run = %d", rec.Code)
	}
	body := pollCloudUntilDone(t, srv, id)
	if !strings.Contains(body, "Cloud run complete") {
		t.Errorf("run-anyway happy path should complete:\n%s", body)
	}
	if got := gateOf(fake.lastTemplate); got != -2 {
		t.Errorf("run_anyway template must OMIT the gate (nil), got %d", got)
	}
}

// TestCloudRunAffordabilityRejection covers a gated real submit returning a 400
// affordability problem: the terminal state renders the (escaped) detail + a
// Run-anyway button, NOT a plain "Cloud run failed".
func TestCloudRunAffordabilityRejection(t *testing.T) {
	fake := &fakeCloud{runErr: &comfy.CloudProblem{
		StatusCode: 400, Detail: `Only 10s affordable <script>y</script>, need 300s`,
	}}
	srv := newCloudTestServer(t, fake)
	id := seedWorkflow(t, srv, store.WorkflowFormatAPI, `{"1":{"class_type":"X","inputs":{}}}`)
	if rec := post(t, srv, "/workflows/"+id+"/cloud/run", url.Values{"resources": {"u"}}, true); rec.Code != http.StatusOK {
		t.Fatalf("cloud run = %d", rec.Code)
	}
	body := pollCloudUntilDone(t, srv, id)
	if strings.Contains(body, "Cloud run failed") {
		t.Errorf("affordability 400 must NOT be a plain failure:\n%s", body)
	}
	if !strings.Contains(body, "Only 10s affordable") {
		t.Errorf("affordability detail missing:\n%s", body)
	}
	if strings.Contains(body, "<script>y</script>") {
		t.Errorf("untrusted detail not escaped:\n%s", body)
	}
	if !strings.Contains(body, "run_anyway") || !strings.Contains(body, "Run anyway") {
		t.Errorf("affordability terminal must offer a Run-anyway button:\n%s", body)
	}
}

// TestCloudRunNon400StaysPlainFailure asserts a non-400 gated submit error stays a
// generic failure (no Run-anyway button).
func TestCloudRunNon400StaysPlainFailure(t *testing.T) {
	fake := &fakeCloud{runErr: &comfy.CloudProblem{StatusCode: 500, Detail: "internal error"}}
	srv := newCloudTestServer(t, fake)
	id := seedWorkflow(t, srv, store.WorkflowFormatAPI, `{"1":{"class_type":"X","inputs":{}}}`)
	if rec := post(t, srv, "/workflows/"+id+"/cloud/run", url.Values{"resources": {"u"}}, true); rec.Code != http.StatusOK {
		t.Fatalf("cloud run = %d", rec.Code)
	}
	body := pollCloudUntilDone(t, srv, id)
	if !strings.Contains(body, "Cloud run failed") {
		t.Errorf("non-400 error should be a plain failure:\n%s", body)
	}
	if strings.Contains(body, "Run anyway") {
		t.Errorf("non-400 failure must NOT offer Run-anyway:\n%s", body)
	}
}

// TestCloudRunAnywayNon400NoSecondRunAnyway asserts a run_anyway (gate-skipped) submit
// that STILL 400s is a plain failure (no infinite run-anyway loop).
func TestCloudRunAnywayNon400NoSecondRunAnyway(t *testing.T) {
	fake := &fakeCloud{runErr: &comfy.CloudProblem{StatusCode: 400, Detail: "bad graph"}}
	srv := newCloudTestServer(t, fake)
	id := seedWorkflow(t, srv, store.WorkflowFormatAPI, `{"1":{"class_type":"X","inputs":{}}}`)
	if rec := post(t, srv, "/workflows/"+id+"/cloud/run",
		url.Values{"resources": {"u"}, "run_anyway": {"1"}}, true); rec.Code != http.StatusOK {
		t.Fatalf("cloud run = %d", rec.Code)
	}
	body := pollCloudUntilDone(t, srv, id)
	if !strings.Contains(body, "Cloud run failed") {
		t.Errorf("gate-skipped 400 should be a plain failure:\n%s", body)
	}
	if strings.Contains(body, "Run anyway") {
		t.Errorf("gate-skipped 400 must NOT re-offer Run-anyway:\n%s", body)
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
	if !strings.Contains(body, "Buzz may still have been charged") {
		t.Errorf("stop response must surface the best-effort Buzz-spend caveat:\n%s", body)
	}
	if !fake.cancelCalled {
		t.Error("stop should best-effort cancel the remote cloud workflow")
	}
}

// uiWarnGraph is a UI-format workflow with one runnable node (CheckpointLoaderSimple)
// AND one unknown node (FooBarNode). Against uiCheckpointInfo the conversion produces
// a non-empty graph BUT a warning for the unknown node — the abort-rather-than-submit
// path.
const uiWarnGraph = `{"nodes":[{"id":4,"type":"CheckpointLoaderSimple","widgets_values":["good.safetensors"]},{"id":5,"type":"FooBarNode","widgets_values":[]}],"links":[]}`

// TestCloudWhatifUIFormatSubmitsConvertedGraph asserts a whatif on a UI-format
// workflow (reachable local ComfyUI, clean conversion) submits the CONVERTED API
// graph — not the raw UI graph.
func TestCloudWhatifUIFormatSubmitsConvertedGraph(t *testing.T) {
	fake := &fakeCloud{whatifResp: &comfy.CloudWorkflow{ID: "wf-1", Status: "unassigned", Cost: &comfy.CloudCost{Base: 0}}}
	srv := newCloudTestServer(t, fake)
	srv.comfyClientFn = func() comfyClient { return &fakeComfy{info: mustObjectInfo(t, uiCheckpointInfo)} }
	id := seedWorkflow(t, srv, store.WorkflowFormatUI, uiCheckpointGraph)

	rec := post(t, srv, "/workflows/"+id+"/cloud/whatif", url.Values{"resources": {"u"}}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("whatif = %d", rec.Code)
	}
	if fake.submitCalls != 1 {
		t.Fatalf("expected exactly one SubmitCloud, got %d", fake.submitCalls)
	}
	submitted := string(fake.lastTemplate.Steps[0].Input.Workflow)
	if !strings.Contains(submitted, "class_type") {
		t.Errorf("submitted graph is not API-format (no class_type):\n%s", submitted)
	}
	if strings.Contains(submitted, `"nodes"`) {
		t.Errorf("submitted graph is the RAW UI graph, not the converted API graph:\n%s", submitted)
	}
}

// TestCloudRunUIFormatUsesConvertedGraph asserts a real run of a UI-format workflow
// submits the CONVERTED API graph and completes.
func TestCloudRunUIFormatUsesConvertedGraph(t *testing.T) {
	fake := &fakeCloud{runResp: &comfy.CloudWorkflow{ID: "wf-done", Status: "succeeded"}}
	srv := newCloudTestServer(t, fake)
	srv.comfyClientFn = func() comfyClient { return &fakeComfy{info: mustObjectInfo(t, uiCheckpointInfo)} }
	id := seedWorkflow(t, srv, store.WorkflowFormatUI, uiCheckpointGraph)

	if rec := post(t, srv, "/workflows/"+id+"/cloud/run", url.Values{"resources": {"u"}}, true); rec.Code != http.StatusOK {
		t.Fatalf("cloud run = %d", rec.Code)
	}
	pollCloudUntilDone(t, srv, id)
	if fake.submitCalls != 1 {
		t.Fatalf("expected exactly one SubmitCloud, got %d", fake.submitCalls)
	}
	submitted := string(fake.lastTemplate.Steps[0].Input.Workflow)
	if !strings.Contains(submitted, "class_type") || strings.Contains(submitted, `"nodes"`) {
		t.Errorf("run must submit the converted API graph, not the raw UI graph:\n%s", submitted)
	}
}

// TestCloudWhatifUIFormatWarningsNoSubmit asserts a whatif on a UI-format workflow
// whose conversion produces warnings surfaces the warnings and does NOT submit.
func TestCloudWhatifUIFormatWarningsNoSubmit(t *testing.T) {
	fake := &fakeCloud{}
	srv := newCloudTestServer(t, fake)
	srv.comfyClientFn = func() comfyClient { return &fakeComfy{info: mustObjectInfo(t, uiCheckpointInfo)} }
	id := seedWorkflow(t, srv, store.WorkflowFormatUI, uiWarnGraph)

	rec := post(t, srv, "/workflows/"+id+"/cloud/whatif", url.Values{"resources": {"u"}}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("whatif = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "could not be converted") {
		t.Errorf("expected conversion-warnings alert:\n%s", body)
	}
	if !strings.Contains(body, "FooBarNode") {
		t.Errorf("expected the specific conversion warning:\n%s", body)
	}
	if fake.submitCalls != 0 {
		t.Errorf("conversion warnings must NOT submit to cloud (calls=%d)", fake.submitCalls)
	}
}

// TestCloudRunUIFormatWarningsNoSubmit asserts a real run on a UI-format workflow with
// conversion warnings surfaces them and never submits.
func TestCloudRunUIFormatWarningsNoSubmit(t *testing.T) {
	fake := &fakeCloud{}
	srv := newCloudTestServer(t, fake)
	srv.comfyClientFn = func() comfyClient { return &fakeComfy{info: mustObjectInfo(t, uiCheckpointInfo)} }
	id := seedWorkflow(t, srv, store.WorkflowFormatUI, uiWarnGraph)

	rec := post(t, srv, "/workflows/"+id+"/cloud/run", url.Values{"resources": {"u"}}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("cloud run = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "could not be converted") {
		t.Errorf("expected conversion-warnings alert:\n%s", body)
	}
	if fake.submitCalls != 0 {
		t.Errorf("conversion warnings must NOT submit to cloud (calls=%d)", fake.submitCalls)
	}
	if snap := srv.cloudJobState(); snap.Started {
		t.Errorf("no cloud job should have started on conversion warnings")
	}
}

// TestCloudWhatifUIFormatNoComfyNoSubmit asserts a whatif on a UI-format workflow with
// no reachable local ComfyUI shows the reachability note and does NOT submit.
func TestCloudWhatifUIFormatNoComfyNoSubmit(t *testing.T) {
	fake := &fakeCloud{}
	srv := newCloudTestServer(t, fake) // s.comfy()==nil
	id := seedWorkflow(t, srv, store.WorkflowFormatUI, uiCheckpointGraph)

	rec := post(t, srv, "/workflows/"+id+"/cloud/whatif", url.Values{"resources": {"u"}}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("whatif = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "converts it to API format using your local ComfyUI") {
		t.Errorf("expected reachability note:\n%s", rec.Body.String())
	}
	if fake.submitCalls != 0 {
		t.Errorf("unreachable ComfyUI must NOT submit to cloud (calls=%d)", fake.submitCalls)
	}
}

// TestCloudRunUIFormatObjectInfoErrorNoSubmit asserts a UI-format run whose local
// ComfyUI /object_info errors (reachable client but failing probe) falls back to the
// reachability note and never submits.
func TestCloudRunUIFormatObjectInfoErrorNoSubmit(t *testing.T) {
	fake := &fakeCloud{}
	srv := newCloudTestServer(t, fake)
	srv.cfg.ComfyURL = "http://127.0.0.1:8188" // so the "isn't reachable at <url>" branch is hit
	srv.comfyClientFn = func() comfyClient {
		return &fakeComfy{infoErr: errors.New("connection refused")}
	}
	id := seedWorkflow(t, srv, store.WorkflowFormatUI, uiCheckpointGraph)

	rec := post(t, srv, "/workflows/"+id+"/cloud/run", url.Values{"resources": {"u"}}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("cloud run = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "reachable at http://127.0.0.1:8188") {
		t.Errorf("expected reachability note on object_info error:\n%s", rec.Body.String())
	}
	if fake.submitCalls != 0 {
		t.Errorf("object_info error must NOT submit to cloud (calls=%d)", fake.submitCalls)
	}
}
