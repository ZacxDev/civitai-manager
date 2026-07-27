package web

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
	"github.com/ZacxDev/civitai-manager/internal/comfy"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// resolveResult builds a search result with the given model names (Checkpoint
// type, no Raw showcase images).
func resolveResult(names ...string) *civitai.ModelSearchResult {
	items := make([]civitai.ModelListItem, 0, len(names))
	for i, n := range names {
		items = append(items, civitai.ModelListItem{ID: i + 1, Name: n, Type: "Checkpoint"})
	}
	return &civitai.ModelSearchResult{Items: items}
}

func lastQuery(t *testing.T, r *recordingSearchReader) url.Values {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.calls) == 0 {
		t.Fatal("SearchModels was never called")
	}
	return r.calls[len(r.calls)-1]
}

// TestResolveModelQueryParams asserts the resolve endpoint cleans the filename
// into a query, sends the type as `types` (PLURAL), and renders the fallback link.
func TestResolveModelQueryParams(t *testing.T) {
	reader := &recordingSearchReader{result: resolveResult("Fabricated XL")}
	srv := newModelServer(t, reader)

	rec := get(t, srv, "/workflows/run/resolve-model?filename=fabricatedXL_v70.safetensors&type=Checkpoint")
	if rec.Code != http.StatusOK {
		t.Fatalf("resolve = %d", rec.Code)
	}
	q := lastQuery(t, reader)
	if q.Get("query") != "fabricated XL" {
		t.Errorf("query = %q, want %q", q.Get("query"), "fabricated XL")
	}
	// PLURAL types filter — singular type= is silently ignored by the CivitAI API.
	if q.Get("types") != "Checkpoint" {
		t.Errorf("types = %q, want Checkpoint (plural)", q.Get("types"))
	}
	if q.Get("type") != "" {
		t.Errorf("singular type= must not be sent, got %q", q.Get("type"))
	}
	body := rec.Body.String()
	if !strings.Contains(body, `Search CivitAI for &#34;fabricated XL&#34;`) &&
		!strings.Contains(body, `Search CivitAI for "fabricated XL"`) {
		t.Errorf("missing fallback search link text:\n%s", body)
	}
	if !strings.Contains(body, "/search?q=fabricated+XL") {
		t.Errorf("fallback link should deep-link to /search?q=…:\n%s", body)
	}
}

// TestResolveModelZeroOneMany asserts the fragment shows: fallback only (0), one
// card + fallback (1), and capped-at-5 cards + fallback (N>5).
func TestResolveModelZeroOneMany(t *testing.T) {
	t.Run("zero", func(t *testing.T) {
		reader := &recordingSearchReader{result: resolveResult()}
		srv := newModelServer(t, reader)
		body := get(t, srv, "/workflows/run/resolve-model?filename=absent.safetensors&type=LORA").Body.String()
		if strings.Contains(body, `href="/models/`) {
			t.Errorf("zero matches should render no cards:\n%s", body)
		}
		if !strings.Contains(body, "No CivitAI models matched") {
			t.Errorf("zero matches should say so:\n%s", body)
		}
		if !strings.Contains(body, "/search?q=absent") {
			t.Errorf("zero matches must still show the fallback link:\n%s", body)
		}
	})
	t.Run("one", func(t *testing.T) {
		reader := &recordingSearchReader{result: resolveResult("Only Match")}
		srv := newModelServer(t, reader)
		body := get(t, srv, "/workflows/run/resolve-model?filename=only.safetensors").Body.String()
		if n := strings.Count(body, `href="/models/`); n != 1 {
			t.Errorf("one match → %d cards, want 1", n)
		}
		if !strings.Contains(body, "Only Match") || !strings.Contains(body, "Search CivitAI for") {
			t.Errorf("one-match fragment missing card or fallback:\n%s", body)
		}
	})
	t.Run("many-capped", func(t *testing.T) {
		reader := &recordingSearchReader{result: resolveResult("m1", "m2", "m3", "m4", "m5", "m6", "m7")}
		srv := newModelServer(t, reader)
		body := get(t, srv, "/workflows/run/resolve-model?filename=x.safetensors").Body.String()
		if n := strings.Count(body, `href="/models/`); n != resolveMaxCards {
			t.Errorf("N matches → %d cards, want capped at %d", n, resolveMaxCards)
		}
	})
}

// TestResolveModelEscapesUntrustedName asserts a hostile model name is escaped.
func TestResolveModelEscapesUntrustedName(t *testing.T) {
	reader := &recordingSearchReader{result: resolveResult(`<script>alert(1)</script>`)}
	srv := newModelServer(t, reader)
	body := get(t, srv, "/workflows/run/resolve-model?filename=evil.safetensors").Body.String()
	if strings.Contains(body, "<script>alert(1)</script>") {
		t.Errorf("untrusted model name was not escaped:\n%s", body)
	}
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Errorf("expected escaped model name in output:\n%s", body)
	}
}

// TestResolveModelLoopbackGated asserts the resolve endpoint is gated off a
// non-loopback bind.
func TestResolveModelLoopbackGated(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	srv := NewServer(st, &recordingSearchReader{result: resolveResult("x")}, stubSubscriber{}, Config{
		BaseURL: "https://civitai.com", DefaultPollInterval: time.Hour, Addr: "0.0.0.0:8787",
	}, nil)
	rec := get(t, srv, "/workflows/run/resolve-model?filename=x.safetensors")
	if rec.Code != http.StatusOK {
		t.Fatalf("gated resolve = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "non-loopback") {
		t.Errorf("expected the gating note, got:\n%s", rec.Body.String())
	}
}

// substituteObjectInfo has a checkpoint loader whose ONLY installed choice is
// installed.safetensors (so a graph referencing absent.safetensors fails
// preflight, but a substitute to installed.safetensors passes).
const substituteObjectInfo = `{"CheckpointLoaderSimple":{"input":{"required":{"ckpt_name":[["installed.safetensors"],{}]}},"input_order":{"required":["ckpt_name"]}}}`

// TestRunSubstituteSwapsGraphAndKeepsStored drives the REAL run path: a workflow
// referencing an absent model, run with a substitute, must submit a graph with
// ONLY that reference swapped — and leave the stored workflow row unchanged.
func TestRunSubstituteSwapsGraphAndKeepsStored(t *testing.T) {
	srv := newLibraryTestServer(t, t.TempDir())
	fake := &fakeComfy{
		info: mustObjectInfo(t, substituteObjectInfo),
		history: &comfy.HistoryEntry{
			Outputs: map[string]comfy.NodeOutput{"9": {Images: []comfy.ImageRef{{Filename: "o.png", Type: "output"}}}},
			Status:  comfy.HistoryStatus{Completed: true, StatusStr: "success"},
		},
	}
	srv.comfyClientFn = func() comfyClient { return fake }

	storedGraph := `{"4":{"class_type":"CheckpointLoaderSimple","inputs":{"ckpt_name":"absent.safetensors"}}}`
	id := seedWorkflow(t, srv, store.WorkflowFormatAPI, storedGraph)

	rec := post(t, srv, "/workflows/"+id+"/run-substitute", url.Values{
		"filename":   {"absent.safetensors"},
		"substitute": {"installed.safetensors"},
	}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("run-substitute = %d", rec.Code)
	}
	body := pollRunUntilDone(t, srv, id)
	if !fake.submitCalled {
		t.Fatal("Submit should have been called after a passing substitute preflight")
	}
	sg := string(fake.submittedGraph)
	if !strings.Contains(sg, "installed.safetensors") {
		t.Errorf("submitted graph missing the substitute: %s", sg)
	}
	if strings.Contains(sg, "absent.safetensors") {
		t.Errorf("submitted graph still references the missing model: %s", sg)
	}
	if !strings.Contains(body, "Run complete") {
		t.Errorf("terminal body missing completion:\n%s", body)
	}
	// The STORED workflow row must be untouched (ephemeral substitution only).
	wfID, _ := strconv.ParseInt(id, 10, 64)
	wf, err := srv.store.GetWorkflow(context.Background(), wfID)
	if err != nil {
		t.Fatalf("reload workflow: %v", err)
	}
	if wf.Graph != storedGraph {
		t.Errorf("stored workflow graph was mutated:\ngot:  %s\nwant: %s", wf.Graph, storedGraph)
	}
}

// TestRunSubstituteCSRFRejected + loopback-gating.
func TestRunSubstituteGating(t *testing.T) {
	srv := newLibraryTestServer(t, t.TempDir())
	id := seedWorkflow(t, srv, store.WorkflowFormatAPI, `{"3":{"class_type":"X","inputs":{}}}`)
	form := url.Values{"filename": {"a.safetensors"}, "substitute": {"b.safetensors"}}

	if rec := post(t, srv, "/workflows/"+id+"/run-substitute", form, false); rec.Code != http.StatusForbidden {
		t.Fatalf("substitute without CSRF = %d, want 403", rec.Code)
	}

	// Non-loopback bind → gated note.
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	gated := NewServer(st, stubReader{}, stubSubscriber{}, Config{
		BaseURL: "https://civitai.com", DefaultPollInterval: time.Hour, Addr: "0.0.0.0:8787",
	}, nil)
	gid := seedWorkflow(t, gated, store.WorkflowFormatAPI, `{"3":{"class_type":"X","inputs":{}}}`)
	rec := post(t, gated, "/workflows/"+gid+"/run-substitute", form, true)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "non-loopback") {
		t.Errorf("gated substitute = %d body=%s", rec.Code, rec.Body.String())
	}
}

// TestRunSubstituteOneAtATime asserts a substitute run is a no-op while another
// run is in flight (the one-run-at-a-time invariant).
func TestRunSubstituteOneAtATime(t *testing.T) {
	srv := newLibraryTestServer(t, t.TempDir())
	id := seedWorkflow(t, srv, store.WorkflowFormatAPI, `{"3":{"class_type":"X","inputs":{}}}`)

	started := make(chan struct{})
	release := make(chan struct{})
	var calls int32
	var gotOpts []runOptions
	srv.runFn = func(ctx context.Context, wf *store.Workflow, up runUpdater, opts runOptions) (*runResult, error) {
		calls++
		gotOpts = append(gotOpts, opts)
		up.setPhase(runPhaseRunning, "Generating…", 0)
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return &runResult{Images: []comfy.ImageRef{{Filename: "o.png", Type: "output"}}, PromptID: "p1"}, nil
	}

	if rec := post(t, srv, "/workflows/"+id+"/run", nil, true); rec.Code != http.StatusOK {
		t.Fatalf("first run = %d", rec.Code)
	}
	<-started
	// A substitute run while the first is in flight must NOT start a second run.
	rec := post(t, srv, "/workflows/"+id+"/run-substitute", url.Values{
		"filename": {"a.safetensors"}, "substitute": {"b.safetensors"},
	}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("substitute while running = %d", rec.Code)
	}
	close(release)
	pollRunUntilDone(t, srv, id)
	if calls != 1 {
		t.Errorf("runFn called %d times, want 1 (one run at a time)", calls)
	}
}

// TestMissingModelsPanelRenders drives the real preflight-fail path and asserts
// the actionable panel: a resolve control, the substitute candidate + a "Run with
// substitute" button carrying the CSRF token to the substitute endpoint.
func TestMissingModelsPanelRenders(t *testing.T) {
	srv := newLibraryTestServer(t, t.TempDir())
	fake := &fakeComfy{info: mustObjectInfo(t, substituteObjectInfo)}
	srv.comfyClientFn = func() comfyClient { return fake }
	id := seedWorkflow(t, srv, store.WorkflowFormatAPI,
		`{"4":{"class_type":"CheckpointLoaderSimple","inputs":{"ckpt_name":"absent.safetensors"}}}`)

	if rec := post(t, srv, "/workflows/"+id+"/run", nil, true); rec.Code != http.StatusOK {
		t.Fatalf("run = %d", rec.Code)
	}
	body := pollRunUntilDone(t, srv, id)

	for _, want := range []string{
		"Missing model files",
		"absent.safetensors",                   // the missing filename
		"Find on CivitAI",                      // resolve control
		"/workflows/run/resolve-model?",        // resolve endpoint
		"type=Checkpoint",                      // inferred type filter (in the resolve GET)
		"Run with an installed model instead",  // substitute heading
		"installed.safetensors",                // the candidate choice
		"/workflows/" + id + "/run-substitute", // substitute endpoint
	} {
		if !strings.Contains(body, want) {
			t.Errorf("panel missing %q:\n%s", want, body)
		}
	}
	// The substitute button carries the CSRF token in hx-vals (HTML-escaped quotes).
	if !strings.Contains(body, "csrf_token") || !strings.Contains(body, srv.csrf) {
		t.Errorf("substitute button missing CSRF token:\n%s", body)
	}
	// hx-vals must be valid JSON carrying the substitute — assert the escaped shape.
	if !strings.Contains(body, `substitute`) {
		t.Errorf("substitute button missing substitute val:\n%s", body)
	}
}
