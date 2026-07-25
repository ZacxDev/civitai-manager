package comfy

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeComfy builds an httptest server that mimics the subset of the ComfyUI HTTP
// API the client uses. Handlers are provided per path; an unset path 404s.
func fakeComfy(t *testing.T, handlers map[string]http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	mux := http.NewServeMux()
	for path, h := range handlers {
		mux.HandleFunc(path, h)
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return NewClient(srv.URL, ""), srv
}

func TestClientSubmitSuccess(t *testing.T) {
	var gotBody map[string]json.RawMessage
	c, _ := fakeComfy(t, map[string]http.HandlerFunc{
		"/prompt": func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewDecoder(r.Body).Decode(&gotBody)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"prompt_id":"abc123","number":7,"node_errors":{}}`))
		},
	})
	res, err := c.Submit(context.Background(), json.RawMessage(`{"3":{"class_type":"X","inputs":{}}}`), "cli", "abc123")
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if res.PromptID != "abc123" || res.Number != 7 {
		t.Fatalf("unexpected result: %+v", res)
	}
	// The graph must be wrapped under "prompt", with client_id/prompt_id alongside.
	if _, ok := gotBody["prompt"]; !ok {
		t.Errorf("request body missing prompt key: %v", gotBody)
	}
	if string(gotBody["client_id"]) != `"cli"` {
		t.Errorf("client_id = %s", gotBody["client_id"])
	}
	if string(gotBody["prompt_id"]) != `"abc123"` {
		t.Errorf("prompt_id = %s", gotBody["prompt_id"])
	}
}

func TestClientSubmitValidationError(t *testing.T) {
	c, _ := fakeComfy(t, map[string]http.HandlerFunc{
		"/prompt": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"type":"prompt_outputs_failed_validation","message":"Prompt has no properly connected outputs"},"node_errors":{"12":{"errors":[{"type":"value_not_in_list","message":"nope"}]}}}`))
		},
	})
	_, err := c.Submit(context.Background(), json.RawMessage(`{}`), "", "")
	if err == nil {
		t.Fatal("expected validation error")
	}
	var ve *PromptValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected *PromptValidationError, got %T: %v", err, err)
	}
	if ve.Status != http.StatusBadRequest {
		t.Errorf("status = %d", ve.Status)
	}
	if !strings.Contains(ve.Message, "properly connected outputs") {
		t.Errorf("message = %q", ve.Message)
	}
	if _, ok := ve.NodeErrors["12"]; !ok {
		t.Errorf("node_errors missing node 12: %v", ve.NodeErrors)
	}
	if !strings.Contains(ve.Error(), "1 node error") {
		t.Errorf("Error() = %q", ve.Error())
	}
}

func TestClientObjectInfoAndIsWidget(t *testing.T) {
	// A synthetic object_info: one node with an INT widget, a combo widget, and a
	// MODEL link input.
	body := `{
		"Sampler": {
			"input": {
				"required": {
					"model": ["MODEL", {}],
					"steps": ["INT", {"default": 20, "min": 1, "max": 100}],
					"sampler": [["euler", "dpmpp_2m"], {}],
					"seed": ["INT", {"default": 0, "control_after_generate": true}]
				}
			},
			"input_order": {"required": ["model", "steps", "sampler", "seed"]}
		}
	}`
	c, _ := fakeComfy(t, map[string]http.HandlerFunc{
		"/object_info": func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(body))
		},
	})
	info, err := c.ObjectInfo(context.Background())
	if err != nil {
		t.Fatalf("ObjectInfo: %v", err)
	}
	sch, ok := info["Sampler"]
	if !ok {
		t.Fatal("Sampler schema missing")
	}
	req := sch.Input.Required
	if IsWidget(req["model"]) {
		t.Error("model (MODEL) should be a link input, not a widget")
	}
	if !IsWidget(req["steps"]) {
		t.Error("steps (INT) should be a widget")
	}
	if !IsWidget(req["sampler"]) {
		t.Error("sampler (combo list) should be a widget")
	}
	if !req["seed"].ControlAfterGenerate() {
		t.Error("seed should report control_after_generate")
	}
	if req["steps"].ControlAfterGenerate() {
		t.Error("steps should NOT report control_after_generate")
	}
	if got := sch.InputOrder.Required; len(got) != 4 || got[0] != "model" {
		t.Errorf("input_order = %v", got)
	}
}

func TestClientHistoryPresentAndAbsent(t *testing.T) {
	c, _ := fakeComfy(t, map[string]http.HandlerFunc{
		"/history/": func(w http.ResponseWriter, r *http.Request) {
			id := strings.TrimPrefix(r.URL.Path, "/history/")
			if id == "done" {
				_, _ = w.Write([]byte(`{"done":{"outputs":{"9":{"images":[{"filename":"o.png","subfolder":"","type":"output"}]}},"status":{"completed":true,"status_str":"success"}}}`))
				return
			}
			_, _ = w.Write([]byte(`{}`))
		},
	})
	entry, err := c.History(context.Background(), "done")
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if entry == nil {
		t.Fatal("expected a history entry for done")
	}
	imgs := entry.AllImages()
	if len(imgs) != 1 || imgs[0].Filename != "o.png" || imgs[0].Type != "output" {
		t.Fatalf("images = %+v", imgs)
	}
	if !entry.Status.Completed {
		t.Error("status.completed should be true")
	}

	absent, err := c.History(context.Background(), "pending")
	if err != nil {
		t.Fatalf("History(absent): %v", err)
	}
	if absent != nil {
		t.Errorf("absent id should yield nil entry, got %+v", absent)
	}
}

func TestClientQueueState(t *testing.T) {
	c, _ := fakeComfy(t, map[string]http.HandlerFunc{
		"/queue": func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{
				"queue_running":[[1,"run1",{},{}]],
				"queue_pending":[[2,"pend1",{},{}],[3,"pend2",{},{}]]
			}`))
		},
	})
	running, pos, found, err := c.QueueState(context.Background(), "run1")
	if err != nil || !found || !running {
		t.Fatalf("run1: running=%v pos=%d found=%v err=%v", running, pos, found, err)
	}
	running, pos, found, err = c.QueueState(context.Background(), "pend2")
	if err != nil || !found || running || pos != 1 {
		t.Fatalf("pend2: running=%v pos=%d found=%v err=%v", running, pos, found, err)
	}
	_, _, found, err = c.QueueState(context.Background(), "nope")
	if err != nil || found {
		t.Fatalf("nope: found=%v err=%v", found, err)
	}
}

func TestClientView(t *testing.T) {
	c, _ := fakeComfy(t, map[string]http.HandlerFunc{
		"/view": func(w http.ResponseWriter, r *http.Request) {
			q := r.URL.Query()
			// All three params are sent (subfolder may be empty).
			if q.Get("filename") == "" || q.Get("type") == "" {
				t.Errorf("missing view params: %v", q)
			}
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte("PNGDATA"))
		},
	})
	data, ct, err := c.View(context.Background(), ImageRef{Filename: "o.png", Subfolder: "", Type: "output"})
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	if string(data) != "PNGDATA" || ct != "image/png" {
		t.Fatalf("data=%q ct=%q", data, ct)
	}
}

func TestClientInterruptAndSystemStats(t *testing.T) {
	c, _ := fakeComfy(t, map[string]http.HandlerFunc{
		"/interrupt": func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				t.Errorf("interrupt method = %s", r.Method)
			}
			w.WriteHeader(http.StatusOK)
		},
		"/system_stats": func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"system":{"comfyui_version":"0.27.1","os":"posix"}}`))
		},
	})
	if err := c.Interrupt(context.Background()); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	stats, err := c.SystemStats(context.Background())
	if err != nil {
		t.Fatalf("SystemStats: %v", err)
	}
	if stats.ComfyUIVersion != "0.27.1" {
		t.Errorf("version = %q", stats.ComfyUIVersion)
	}
}

func TestClientBearerTokenAndUnauthorized(t *testing.T) {
	var gotAuth string
	mux := http.NewServeMux()
	mux.HandleFunc("/system_stats", func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if gotAuth != "Bearer sekret" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"login required"}`))
			return
		}
		_, _ = w.Write([]byte(`{"system":{"comfyui_version":"0.27.1"}}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	// With the right token, the bearer header is sent and the call succeeds.
	authed := NewClient(srv.URL, "sekret")
	if _, err := authed.SystemStats(context.Background()); err != nil {
		t.Fatalf("authed SystemStats: %v", err)
	}
	if gotAuth != "Bearer sekret" {
		t.Errorf("Authorization header = %q", gotAuth)
	}

	// Without a token, the server 401s and the client surfaces a clear error.
	anon := NewClient(srv.URL, "")
	_, err := anon.SystemStats(context.Background())
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("expected a 401 error, got %v", err)
	}
}
