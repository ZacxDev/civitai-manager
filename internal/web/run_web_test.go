package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/comfy"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// fakeComfy is an injectable comfyClient for the web run tests. It records calls
// and returns programmed responses.
type fakeComfy struct {
	statsErr        error
	info            comfy.ObjectInfo
	infoErr         error
	submitErr       error
	history         *comfy.HistoryEntry
	viewData        []byte
	viewCT          string
	objectInfoCalls int
	submitCalled    bool
	submittedGraph  json.RawMessage
	interruptCalled bool
}

func (f *fakeComfy) SystemStats(context.Context) (*comfy.SystemStats, error) {
	if f.statsErr != nil {
		return nil, f.statsErr
	}
	return &comfy.SystemStats{ComfyUIVersion: "0.27.1"}, nil
}
func (f *fakeComfy) ObjectInfo(context.Context) (comfy.ObjectInfo, error) {
	f.objectInfoCalls++
	return f.info, f.infoErr
}
func (f *fakeComfy) Submit(_ context.Context, g json.RawMessage, _, promptID string) (*comfy.SubmitResult, error) {
	f.submitCalled = true
	f.submittedGraph = g
	return &comfy.SubmitResult{PromptID: promptID}, f.submitErr
}
func (f *fakeComfy) QueueState(context.Context, string) (bool, int, bool, error) {
	return false, 0, false, nil
}
func (f *fakeComfy) History(context.Context, string) (*comfy.HistoryEntry, error) {
	return f.history, nil
}
func (f *fakeComfy) View(context.Context, comfy.ImageRef) ([]byte, string, error) {
	return f.viewData, f.viewCT, nil
}
func (f *fakeComfy) Interrupt(context.Context) error {
	f.interruptCalled = true
	return nil
}

func seedWorkflow(t *testing.T, srv *Server, format, graph string) string {
	t.Helper()
	id, err := srv.store.InsertWorkflow(context.Background(), &store.Workflow{
		Name: "wf", Format: format, Graph: graph, Source: store.WorkflowSourceImported,
	})
	if err != nil {
		t.Fatalf("seed workflow: %v", err)
	}
	return strconv.FormatInt(id, 10)
}

func hasRunPoller(body string) bool {
	return strings.Contains(body, `id="run-poll"`) &&
		strings.Contains(body, `hx-trigger="load delay:1s"`) &&
		strings.Contains(body, `hx-target="#run-status"`)
}

func pollRunUntilDone(t *testing.T, srv *Server, id string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		rec := get(t, srv, "/workflows/"+id+"/run/status")
		if rec.Code != http.StatusOK {
			t.Fatalf("run status = %d", rec.Code)
		}
		body := rec.Body.String()
		if !hasRunPoller(body) {
			return body
		}
		if time.Now().After(deadline) {
			t.Fatalf("run did not finish; last body:\n%s", body)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestRunStartRunningThenDone drives an injected runFn through running → done and
// asserts the poller structure and the result gallery.
func TestRunStartRunningThenDone(t *testing.T) {
	srv := newLibraryTestServer(t, t.TempDir())
	id := seedWorkflow(t, srv, store.WorkflowFormatAPI, `{"3":{"class_type":"X","inputs":{}}}`)

	release := make(chan struct{})
	srv.runFn = func(ctx context.Context, wf *store.Workflow, up runUpdater) (*runResult, error) {
		up.setPromptID("p1")
		up.setPhase(runPhaseRunning, "Generating…", 0)
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return &runResult{
			Images:   []comfy.ImageRef{{Filename: "out.png", Subfolder: "", Type: "output"}},
			PromptID: "p1",
		}, nil
	}

	rec := post(t, srv, "/workflows/"+id+"/run", nil, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("run start = %d", rec.Code)
	}
	if !hasRunPoller(rec.Body.String()) {
		t.Fatalf("start response should be the running fragment:\n%s", rec.Body.String())
	}

	close(release)
	body := pollRunUntilDone(t, srv, id)
	if !strings.Contains(body, "Run complete") {
		t.Errorf("terminal body missing completion line:\n%s", body)
	}
	// A gallery <img> pointing at the view proxy for prompt p1.
	if !strings.Contains(body, "/workflows/run/view?") || !strings.Contains(body, "prompt=p1") {
		t.Errorf("terminal body missing result gallery img:\n%s", body)
	}
}

// TestRunPreflightAbortsBeforeSubmit exercises the REAL run path with a fake comfy
// client: a graph referencing an absent model must abort at preflight, never
// submit, and render the missing-models report.
func TestRunPreflightAbortsBeforeSubmit(t *testing.T) {
	srv := newLibraryTestServer(t, t.TempDir())
	fake := &fakeComfy{
		info: mustObjectInfo(t, `{"CheckpointLoaderSimple":{"input":{"required":{"ckpt_name":[["installed.safetensors"],{}]}},"input_order":{"required":["ckpt_name"]}}}`),
	}
	srv.comfyClientFn = func() comfyClient { return fake }

	id := seedWorkflow(t, srv, store.WorkflowFormatAPI,
		`{"4":{"class_type":"CheckpointLoaderSimple","inputs":{"ckpt_name":"absent.safetensors"}}}`)

	rec := post(t, srv, "/workflows/"+id+"/run", nil, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("run start = %d", rec.Code)
	}
	body := pollRunUntilDone(t, srv, id)
	if fake.submitCalled {
		t.Error("Submit must NOT be called when preflight fails")
	}
	if !strings.Contains(body, "Missing model files") || !strings.Contains(body, "absent.safetensors") {
		t.Errorf("terminal body missing preflight report:\n%s", body)
	}
}

// TestRunUIWorkflowConverts asserts a UI-format workflow is converted to api format
// in the run path (ObjectInfo fetched, and the submitted graph is api-format).
func TestRunUIWorkflowConverts(t *testing.T) {
	srv := newLibraryTestServer(t, t.TempDir())
	fake := &fakeComfy{
		info: mustObjectInfo(t, `{"CheckpointLoaderSimple":{"input":{"required":{"ckpt_name":[["a.safetensors"],{}]}},"input_order":{"required":["ckpt_name"]}}}`),
		history: &comfy.HistoryEntry{
			Outputs: map[string]comfy.NodeOutput{"9": {Images: []comfy.ImageRef{{Filename: "o.png", Type: "output"}}}},
			Status:  comfy.HistoryStatus{Completed: true, StatusStr: "success"},
		},
	}
	srv.comfyClientFn = func() comfyClient { return fake }

	ui := `{"nodes":[{"id":4,"type":"CheckpointLoaderSimple","mode":0,"widgets_values":["a.safetensors"]}],"links":[]}`
	id := seedWorkflow(t, srv, store.WorkflowFormatUI, ui)

	rec := post(t, srv, "/workflows/"+id+"/run", nil, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("run start = %d", rec.Code)
	}
	body := pollRunUntilDone(t, srv, id)
	if fake.objectInfoCalls == 0 {
		t.Error("conversion should have fetched /object_info")
	}
	if !fake.submitCalled {
		t.Fatal("Submit should have been called after a successful conversion + preflight")
	}
	if f, err := comfy.DetectFormat(fake.submittedGraph); err != nil || f != comfy.FormatAPI {
		t.Errorf("submitted graph is not api-format (f=%q err=%v): %s", f, err, fake.submittedGraph)
	}
	if !strings.Contains(string(fake.submittedGraph), "CheckpointLoaderSimple") {
		t.Errorf("submitted graph lost the class type: %s", fake.submittedGraph)
	}
	if !strings.Contains(body, "Run complete") {
		t.Errorf("terminal body missing completion:\n%s", body)
	}
}

// TestRunStopCancels starts a blocking run, stops it, and asserts the stopped
// terminal + a best-effort ComfyUI interrupt.
func TestRunStopCancels(t *testing.T) {
	srv := newLibraryTestServer(t, t.TempDir())
	fake := &fakeComfy{}
	srv.comfyClientFn = func() comfyClient { return fake }
	id := seedWorkflow(t, srv, store.WorkflowFormatAPI, `{"3":{"class_type":"X","inputs":{}}}`)

	started := make(chan struct{})
	srv.runFn = func(ctx context.Context, wf *store.Workflow, up runUpdater) (*runResult, error) {
		up.setPhase(runPhaseRunning, "Generating…", 0)
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}

	if rec := post(t, srv, "/workflows/"+id+"/run", nil, true); rec.Code != http.StatusOK {
		t.Fatalf("run start = %d", rec.Code)
	}
	<-started

	rec := post(t, srv, "/workflows/run/stop", url.Values{"workflow_id": {id}}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("run stop = %d", rec.Code)
	}
	body := rec.Body.String()
	if hasRunPoller(body) {
		t.Errorf("stop response should be poller-less:\n%s", body)
	}
	if !strings.Contains(body, "Run stopped") {
		t.Errorf("stop response missing 'Run stopped':\n%s", body)
	}
	if !fake.interruptCalled {
		t.Error("stop should best-effort interrupt ComfyUI")
	}
}

// TestRunViewProxy asserts the view proxy serves an active-run image and rejects a
// mismatched prompt id / non-output filename.
func TestRunViewProxy(t *testing.T) {
	srv := newLibraryTestServer(t, t.TempDir())
	fake := &fakeComfy{viewData: []byte("IMGBYTES"), viewCT: "image/png"}
	srv.comfyClientFn = func() comfyClient { return fake }
	id := seedWorkflow(t, srv, store.WorkflowFormatAPI, `{"3":{"class_type":"X","inputs":{}}}`)

	srv.runFn = func(ctx context.Context, wf *store.Workflow, up runUpdater) (*runResult, error) {
		return &runResult{
			Images:   []comfy.ImageRef{{Filename: "o.png", Subfolder: "", Type: "output"}},
			PromptID: "pX",
		}, nil
	}
	if rec := post(t, srv, "/workflows/"+id+"/run", nil, true); rec.Code != http.StatusOK {
		t.Fatalf("run start = %d", rec.Code)
	}
	pollRunUntilDone(t, srv, id)

	// Valid image of the active run → 200 + bytes.
	ok := get(t, srv, "/workflows/run/view?prompt=pX&filename=o.png&subfolder=&type=output")
	if ok.Code != http.StatusOK {
		t.Fatalf("valid view = %d", ok.Code)
	}
	if ok.Body.String() != "IMGBYTES" || ok.Header().Get("Content-Type") != "image/png" {
		t.Errorf("view body=%q ct=%q", ok.Body.String(), ok.Header().Get("Content-Type"))
	}

	// Wrong prompt id → 403.
	if bad := get(t, srv, "/workflows/run/view?prompt=OTHER&filename=o.png&subfolder=&type=output"); bad.Code != http.StatusForbidden {
		t.Errorf("mismatched prompt = %d, want 403", bad.Code)
	}
	// A filename that is not an output of the run → 403.
	if bad := get(t, srv, "/workflows/run/view?prompt=pX&filename=secret.png&subfolder=&type=output"); bad.Code != http.StatusForbidden {
		t.Errorf("non-output filename = %d, want 403", bad.Code)
	}
}

// TestRunCSRFRejected asserts the run start requires a CSRF token.
func TestRunCSRFRejected(t *testing.T) {
	srv := newLibraryTestServer(t, t.TempDir())
	id := seedWorkflow(t, srv, store.WorkflowFormatAPI, `{"3":{"class_type":"X","inputs":{}}}`)
	rec := post(t, srv, "/workflows/"+id+"/run", nil, false) // no CSRF
	if rec.Code != http.StatusForbidden {
		t.Fatalf("run without CSRF = %d, want 403", rec.Code)
	}
}

// TestRunLoopbackGated asserts the run endpoint is gated off a non-loopback bind.
func TestRunLoopbackGated(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	// A non-loopback Addr disables the arbitrary-reach capability.
	srv := NewServer(st, stubReader{}, stubSubscriber{}, Config{
		BaseURL: "https://civitai.com", DefaultPollInterval: time.Hour, Addr: "0.0.0.0:8787",
	}, nil)
	id := seedWorkflow(t, srv, store.WorkflowFormatAPI, `{"3":{"class_type":"X","inputs":{}}}`)

	rec := post(t, srv, "/workflows/"+id+"/run", nil, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("gated run = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "non-loopback") {
		t.Errorf("expected the gating note, got:\n%s", rec.Body.String())
	}
}

func mustObjectInfo(t *testing.T, raw string) comfy.ObjectInfo {
	t.Helper()
	var oi comfy.ObjectInfo
	if err := json.Unmarshal([]byte(raw), &oi); err != nil {
		t.Fatalf("parse object_info: %v", err)
	}
	return oi
}
