package web

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/store"
)

// seedUIWorkflow inserts a UI-format workflow (the "Open in ComfyUI" path only
// accepts UI graphs) and returns its id.
func seedUIWorkflow(t *testing.T, srv *Server, name, graph string) int64 {
	t.Helper()
	id, err := srv.store.InsertWorkflow(context.Background(), &store.Workflow{
		Name: name, Format: store.WorkflowFormatUI, Graph: graph, Source: store.WorkflowSourceImported,
	})
	if err != nil {
		t.Fatalf("seed ui workflow: %v", err)
	}
	return id
}

// TestSanitizeWorkflowFilename proves the userdata filename is traversal-safe: no
// path separators, no "..", always "<safe>-<id>.json".
func TestSanitizeWorkflowFilename(t *testing.T) {
	cases := []string{
		"../../etc/passwd", `a\b/c`, "   ", "", "normal name!", "汉字 only",
		"..", "./.", "a/../../b", strings.Repeat("x", 200),
	}
	for _, name := range cases {
		got := sanitizeWorkflowFilename(name, 7)
		if strings.ContainsAny(got, `/\`) {
			t.Errorf("name %q → %q contains a path separator", name, got)
		}
		if strings.Contains(got, "..") {
			t.Errorf("name %q → %q contains ..", name, got)
		}
		if !strings.HasSuffix(got, "-7.json") {
			t.Errorf("name %q → %q should end with -7.json", name, got)
		}
	}
}

// TestOpenComfyWritesSanitizedNamespacedPath proves the endpoint writes the UI
// graph to ComfyUI userdata under a sanitized, namespaced path and returns a
// scheme-validated open URL that the client opens in a new tab.
func TestOpenComfyWritesSanitizedNamespacedPath(t *testing.T) {
	srv := newWorkflowServer(t)
	fake := &fakeComfy{}
	srv.comfyClientFn = func() comfyClient { return fake }
	srv.cfg.ComfyURL = "http://127.0.0.1:8188"

	id := seedUIWorkflow(t, srv, "../evil name", "{\"nodes\":[]}")
	rec := postForm(srv, "/workflows/"+itoa64(id)+"/open-in-comfyui", "", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !fake.saveCalled {
		t.Fatal("SaveUserWorkflow was not called")
	}
	if !strings.HasPrefix(fake.savedRelPath, "civitai-manager/") {
		t.Errorf("saved path %q not namespaced under civitai-manager/", fake.savedRelPath)
	}
	if strings.Contains(fake.savedRelPath, "..") || strings.Contains(strings.TrimPrefix(fake.savedRelPath, "civitai-manager/"), "/") {
		t.Errorf("saved path %q must be traversal-safe", fake.savedRelPath)
	}
	if string(fake.savedGraph) != "{\"nodes\":[]}" {
		t.Errorf("saved graph mismatch: %s", fake.savedGraph)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `href="http://127.0.0.1:8188/?workflow=`) {
		t.Errorf("missing scheme-validated open URL anchor:\n%s", body)
	}
	if !strings.Contains(body, "window.open(") {
		t.Errorf("missing window.open new-tab open:\n%s", body)
	}
	if !strings.Contains(body, `target="_blank"`) || !strings.Contains(body, `rel="noopener"`) {
		t.Errorf("open anchor should be a hardened new-tab link:\n%s", body)
	}
}

// TestOpenComfyCSRFRejected proves a POST without a CSRF token is refused and does
// NOT reach ComfyUI.
func TestOpenComfyCSRFRejected(t *testing.T) {
	srv := newWorkflowServer(t)
	fake := &fakeComfy{}
	srv.comfyClientFn = func() comfyClient { return fake }
	srv.cfg.ComfyURL = "http://127.0.0.1:8188"
	id := seedUIWorkflow(t, srv, "wf", "{}")

	rec := postForm(srv, "/workflows/"+itoa64(id)+"/open-in-comfyui", "", false)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if fake.saveCalled {
		t.Error("CSRF-rejected open must not reach ComfyUI")
	}
}

// TestOpenComfyLoopbackGated proves a non-loopback bind gates the endpoint (it
// never writes to ComfyUI).
func TestOpenComfyLoopbackGated(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	srv := NewServer(st, stubReader{}, stubSubscriber{},
		Config{BaseURL: "https://civitai.com", DefaultPollInterval: time.Hour, Addr: "0.0.0.0:8972", ComfyURL: "http://127.0.0.1:8188"}, nil)
	fake := &fakeComfy{}
	srv.comfyClientFn = func() comfyClient { return fake }
	id := seedUIWorkflow(t, srv, "wf", "{}")

	rec := postForm(srv, "/workflows/"+itoa64(id)+"/open-in-comfyui", "", true)
	if !strings.Contains(rec.Body.String(), "non-loopback") {
		t.Errorf("expected loopback-gating note, got:\n%s", rec.Body.String())
	}
	if fake.saveCalled {
		t.Error("gated open must not reach ComfyUI")
	}
}

// TestOpenComfyAPIFormatRefused proves an API-format workflow is refused (it does
// not load into the editor) without reaching ComfyUI.
func TestOpenComfyAPIFormatRefused(t *testing.T) {
	srv := newWorkflowServer(t)
	fake := &fakeComfy{}
	srv.comfyClientFn = func() comfyClient { return fake }
	srv.cfg.ComfyURL = "http://127.0.0.1:8188"
	id, err := srv.store.InsertWorkflow(context.Background(), &store.Workflow{
		Name: "api", Format: store.WorkflowFormatAPI, Graph: testAPIGraph, Source: store.WorkflowSourceImported,
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	rec := postForm(srv, "/workflows/"+itoa64(id)+"/open-in-comfyui", "", true)
	if !strings.Contains(rec.Body.String(), "UI-format") {
		t.Errorf("API-format open should be refused with a reason:\n%s", rec.Body.String())
	}
	if fake.saveCalled {
		t.Error("API-format open must not reach ComfyUI")
	}
}

// TestOpenComfyUnreachable proves an unreachable ComfyUI degrades to a clear error
// (no crash, 200).
func TestOpenComfyUnreachable(t *testing.T) {
	srv := newWorkflowServer(t)
	fake := &fakeComfy{saveErr: errors.New("connection refused")}
	srv.comfyClientFn = func() comfyClient { return fake }
	srv.cfg.ComfyURL = "http://127.0.0.1:8188"
	id := seedUIWorkflow(t, srv, "wf", "{}")

	rec := postForm(srv, "/workflows/"+itoa64(id)+"/open-in-comfyui", "", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Could not reach ComfyUI") {
		t.Errorf("unreachable ComfyUI should show a clear error:\n%s", rec.Body.String())
	}
}

// TestWorkflowDetailOpenComfyButtonGating proves the button is shown ONLY for
// UI-format workflows with a configured comfy_url.
func TestWorkflowDetailOpenComfyButtonGating(t *testing.T) {
	uiWF := &store.Workflow{ID: 3, Name: "ui", Format: store.WorkflowFormatUI, Graph: "{}", Source: store.WorkflowSourceImported}
	apiWF := &store.Workflow{ID: 4, Name: "api", Format: store.WorkflowFormatAPI, Graph: "{}", Source: store.WorkflowSourceImported}

	// UI + comfy configured → button present.
	got := renderString(t, workflowDetailPage(uiWF, "{}", "csrf", "dark", "blur", nil, true, workflowResolver{}))
	if !strings.Contains(got, "/workflows/3/open-in-comfyui") || !strings.Contains(got, "Open in ComfyUI") {
		t.Errorf("UI + comfy should show the Open-in-ComfyUI button:\n%s", got)
	}
	// API + comfy configured → no button (API graphs don't load into the editor).
	gotAPI := renderString(t, workflowDetailPage(apiWF, "{}", "csrf", "dark", "blur", nil, true, workflowResolver{}))
	if strings.Contains(gotAPI, "open-in-comfyui") {
		t.Errorf("API-format detail must not show the Open-in-ComfyUI button:\n%s", gotAPI)
	}
	// UI + comfy NOT configured → no button.
	gotNoComfy := renderString(t, workflowDetailPage(uiWF, "{}", "csrf", "dark", "blur", nil, false, workflowResolver{}))
	if strings.Contains(gotNoComfy, "open-in-comfyui") {
		t.Errorf("no comfy_url should hide the Open-in-ComfyUI button:\n%s", gotNoComfy)
	}
}
