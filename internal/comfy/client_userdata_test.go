package comfy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestSaveUserWorkflowEncodesPath proves SaveUserWorkflow POSTs the graph to the
// userdata endpoint with the "workflows/" prefix and slashes URL-encoded as %2F
// (the single-{file}-segment form ComfyUI expects — verified live against 0.27.1),
// writing the raw graph bytes as the body.
func TestSaveUserWorkflowEncodesPath(t *testing.T) {
	var gotURI, gotBody, gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURI = r.RequestURI
		gotMethod = r.Method
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`"workflows/civitai-manager/x.json"`))
	}))
	t.Cleanup(srv.Close)
	c := NewClient(srv.URL, "")

	if err := c.SaveUserWorkflow(context.Background(), "civitai-manager/x.json", json.RawMessage(`{"nodes":[]}`)); err != nil {
		t.Fatalf("SaveUserWorkflow: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %s, want POST", gotMethod)
	}
	if !strings.Contains(gotURI, "/userdata/workflows%2Fcivitai-manager%2Fx.json") {
		t.Errorf("request URI = %q, want encoded userdata path", gotURI)
	}
	if gotBody != `{"nodes":[]}` {
		t.Errorf("body = %q, want the raw graph", gotBody)
	}
}

// TestSaveUserWorkflowStatusError proves a non-2xx response surfaces an error.
func TestSaveUserWorkflowStatusError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom"))
	}))
	t.Cleanup(srv.Close)
	c := NewClient(srv.URL, "")
	if err := c.SaveUserWorkflow(context.Background(), "civitai-manager/x.json", json.RawMessage(`{}`)); err == nil {
		t.Fatal("expected an error on a 500 response")
	}
}
