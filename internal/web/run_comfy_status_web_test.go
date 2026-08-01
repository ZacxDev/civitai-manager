package web

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/comfy"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// TestComfyStatusReachable asserts a healthy ComfyUI yields the green pill (with
// version) and an ENABLED Run button.
func TestComfyStatusReachable(t *testing.T) {
	srv := newLibraryTestServer(t, t.TempDir())
	srv.comfyClientFn = func() comfyClient { return &fakeComfy{} } // stats OK, v0.27.1
	id := seedWorkflow(t, srv, store.WorkflowFormatAPI, `{"3":{"class_type":"X","inputs":{}}}`)

	rec := get(t, srv, "/workflows/"+id+"/run/comfy-status")
	if rec.Code != http.StatusOK {
		t.Fatalf("comfy-status = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "ComfyUI reachable") {
		t.Errorf("missing green pill:\n%s", body)
	}
	if !strings.Contains(body, "0.27.1") {
		t.Errorf("green pill should show the ComfyUI version")
	}
	// The enabled Run button posts to the params endpoint (this fixture is an
	// API-format workflow, which the batch endpoint refuses); the disabled tooltip
	// must be absent.
	if !strings.Contains(body, `hx-post="/workflows/`+id+`/run-with-params"`) {
		t.Errorf("reachable fragment should carry an enabled Run button")
	}
	if strings.Contains(body, "is not reachable") {
		t.Errorf("reachable fragment should not render the disabled tooltip")
	}
}

// TestComfyStatusUnreachable asserts a failed probe yields the red pill, a DISABLED
// Run button (with tooltip), and a Recheck control.
func TestComfyStatusUnreachable(t *testing.T) {
	srv := newLibraryTestServer(t, t.TempDir())
	srv.cfg.ComfyURL = "http://127.0.0.1:8188"
	srv.comfyClientFn = func() comfyClient {
		return &fakeComfy{statsErr: errors.New("connection refused")}
	}
	id := seedWorkflow(t, srv, store.WorkflowFormatAPI, `{"3":{"class_type":"X","inputs":{}}}`)

	body := get(t, srv, "/workflows/"+id+"/run/comfy-status").Body.String()
	if !strings.Contains(body, "No ComfyUI reachable at http://127.0.0.1:8188") {
		t.Errorf("missing red pill with comfy_url:\n%s", body)
	}
	if !strings.Contains(body, `title="ComfyUI is not reachable`) {
		t.Errorf("unreachable fragment should render a DISABLED Run button with a tooltip")
	}
	if !strings.Contains(body, "disabled") {
		t.Errorf("Run button should be disabled when unreachable")
	}
	if !strings.Contains(body, "Recheck") {
		t.Errorf("unreachable fragment should offer a Recheck")
	}
	if !strings.Contains(body, `hx-get="/workflows/`+id+`/run/comfy-status"`) {
		t.Errorf("Recheck should re-fetch the reachability fragment")
	}
	// A disabled Run button must NOT carry the live post.
	if strings.Contains(body, `hx-post="/workflows/`+id+`/run"`) {
		t.Errorf("disabled Run must not carry the run POST")
	}
}

// TestComfyStatusEscapesComfyURL asserts the (config-sourced) comfy_url is escaped
// in the red pill.
func TestComfyStatusEscapesComfyURL(t *testing.T) {
	srv := newLibraryTestServer(t, t.TempDir())
	srv.cfg.ComfyURL = `http://x/"><script>alert(1)</script>`
	srv.comfyClientFn = func() comfyClient {
		return &fakeComfy{statsErr: errors.New("down")}
	}
	id := seedWorkflow(t, srv, store.WorkflowFormatAPI, `{"3":{"class_type":"X","inputs":{}}}`)
	body := get(t, srv, "/workflows/"+id+"/run/comfy-status").Body.String()
	if strings.Contains(body, "<script>alert(1)</script>") {
		t.Errorf("comfy_url must be escaped:\n%s", body)
	}
}

// TestComfyStatusNotConfigured asserts a nil comfy client yields the neutral note
// (no pill, no Run button).
func TestComfyStatusNotConfigured(t *testing.T) {
	srv := newLibraryTestServer(t, t.TempDir()) // no comfyClientFn, blank ComfyURL → nil client
	id := seedWorkflow(t, srv, store.WorkflowFormatAPI, `{"3":{"class_type":"X","inputs":{}}}`)

	body := get(t, srv, "/workflows/"+id+"/run/comfy-status").Body.String()
	if !strings.Contains(body, "not configured") {
		t.Errorf("nil client should render the neutral note:\n%s", body)
	}
	if strings.Contains(body, "ComfyUI reachable") || strings.Contains(body, "No ComfyUI reachable") {
		t.Errorf("unconfigured fragment should render neither pill")
	}
}

// TestComfyStatusLoopbackGated asserts the comfy-status endpoint is gated off a
// non-loopback bind.
func TestComfyStatusLoopbackGated(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	srv := NewServer(st, stubReader{}, stubSubscriber{}, Config{
		BaseURL: "https://civitai.com", DefaultPollInterval: time.Hour, Addr: "0.0.0.0:8787",
	}, nil)
	id := seedWorkflow(t, srv, store.WorkflowFormatAPI, `{"3":{"class_type":"X","inputs":{}}}`)

	body := get(t, srv, "/workflows/"+id+"/run/comfy-status").Body.String()
	if !strings.Contains(body, "non-loopback") {
		t.Errorf("comfy-status should be loopback-gated:\n%s", body)
	}
}

// TestComfyStatusDoesNotHang asserts the probe returns promptly even when the
// client blocks past the short timeout (the endpoint uses its own deadline).
func TestComfyStatusDoesNotHang(t *testing.T) {
	srv := newLibraryTestServer(t, t.TempDir())
	srv.comfyClientFn = func() comfyClient { return &blockingComfy{} }
	id := seedWorkflow(t, srv, store.WorkflowFormatAPI, `{"3":{"class_type":"X","inputs":{}}}`)

	done := make(chan string, 1)
	go func() { done <- get(t, srv, "/workflows/"+id+"/run/comfy-status").Body.String() }()
	select {
	case body := <-done:
		// The blocking client honors ctx cancellation → treated as unreachable.
		if !strings.Contains(body, "No ComfyUI reachable") {
			t.Errorf("a timed-out probe should render the unreachable pill:\n%s", body)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("comfy-status hung past the short probe timeout")
	}
}

// blockingComfy is a comfyClient whose SystemStats blocks until the context is
// cancelled (simulating a hung ComfyUI); all other methods are unused here.
type blockingComfy struct{ fakeComfy }

func (b *blockingComfy) SystemStats(ctx context.Context) (*comfy.SystemStats, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}
