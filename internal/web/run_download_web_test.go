package web

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// dlRunReader serves a canned model-search raw body (with modelVersions/files)
// and, for GetModel, a canned model-detail raw body. Both feed the resolver.
type dlRunReader struct {
	stubReader
	searchRaw []byte
	modelRaw  []byte
}

func (r dlRunReader) SearchModels(context.Context, url.Values) (*civitai.ModelSearchResult, error) {
	return &civitai.ModelSearchResult{
		Items: []civitai.ModelListItem{{ID: 1, Name: "Fabricated XL", Type: "Checkpoint"}},
		Raw:   r.searchRaw,
	}, nil
}

func (r dlRunReader) GetModel(context.Context, string) (*civitai.ModelDetail, []byte, error) {
	return &civitai.ModelDetail{ID: 1, Name: "Fabricated XL", Type: "Checkpoint"}, r.modelRaw, nil
}

// searchRawWithFile builds a models-list raw body embedding one model → one
// version → one file with the given name + download URL.
func searchRawWithFile(t *testing.T, fileName, dlURL string) []byte {
	t.Helper()
	return searchRawJSON(t, []any{
		map[string]any{
			"id": 1, "name": "Fabricated XL", "type": "Checkpoint",
			"modelVersions": []any{
				map[string]any{"id": 10, "files": []any{
					map[string]any{"name": fileName, "downloadUrl": dlURL, "sizeKB": 4, "primary": true},
				}},
			},
		},
	})
}

// newDownloadServer builds a server wired for the Download & run flow: a reader
// serving the given search raw, a fake downloader serving bytes for dlURL, a
// loopback comfy_url, and comfyModelPath pointing at a fresh writable temp dir
// (returned). runFn is left to the caller to stub.
func newDownloadServer(t *testing.T, reader civitai.Reader, dlURL string, body []byte) (*Server, *fakeDownloader, string) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	comfyModels := t.TempDir()
	srv := NewServer(st, reader, stubSubscriber{}, Config{
		BaseURL: "https://civitai.com", DefaultPollInterval: time.Hour,
		Addr:           "127.0.0.1:8787",
		ComfyURL:       "http://127.0.0.1:8188",
		ComfyModelPath: comfyModels,
	}, nil)
	base, cancel := context.WithCancel(context.Background())
	srv.SetBaseContext(base)
	t.Cleanup(cancel)
	dl := &fakeDownloader{zips: map[string][]byte{dlURL: body}}
	srv.downloaderFn = func() civitai.Downloader { return dl }
	return srv, dl, comfyModels
}

// runRecorder captures the runFn invocations for the download-then-run job.
type runRecorder struct {
	mu    sync.Mutex
	calls int
	opts  []runOptions
	wfIDs []int64
}

func (rr *runRecorder) fn() func(context.Context, *store.Workflow, runUpdater, runOptions) (*runResult, error) {
	return func(_ context.Context, wf *store.Workflow, up runUpdater, opts runOptions) (*runResult, error) {
		rr.mu.Lock()
		rr.calls++
		rr.opts = append(rr.opts, opts)
		rr.wfIDs = append(rr.wfIDs, wf.ID)
		rr.mu.Unlock()
		up.setPhase(runPhaseRunning, "Generating…", 0)
		return &runResult{PromptID: "p1"}, nil
	}
}

// TestDownloadAndRunWritesAndRuns: eligible → resolves the file, writes it into
// comfy_model_path/checkpoints/<name>, then runs the ORIGINAL workflow (empty
// Substitute).
func TestDownloadAndRunWritesAndRuns(t *testing.T) {
	const dlURL = "http://dl.example/checkpoint"
	reader := dlRunReader{searchRaw: searchRawWithFile(t, "fabricatedXL_v70.safetensors", dlURL)}
	srv, dl, comfyModels := newDownloadServer(t, reader, dlURL, []byte("MODELWEIGHTS"))
	rr := &runRecorder{}
	srv.runFn = rr.fn()
	wfID := seedWorkflow(t, srv, store.WorkflowFormatAPI,
		`{"4":{"class_type":"CheckpointLoaderSimple","inputs":{"ckpt_name":"fabricatedXL_v70.safetensors"}}}`)

	rec := post(t, srv, "/workflows/"+wfID+"/download-and-run", url.Values{
		"filename": {"fabricatedXL_v70.safetensors"},
		"type":     {"Checkpoint"},
	}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("download-and-run = %d", rec.Code)
	}
	pollRunUntilDone(t, srv, wfID)

	dest := filepath.Join(comfyModels, "checkpoints", "fabricatedXL_v70.safetensors")
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("model not written to %s: %v", dest, err)
	}
	if string(got) != "MODELWEIGHTS" {
		t.Errorf("dest content = %q, want MODELWEIGHTS", got)
	}
	if dl.calls != 1 {
		t.Errorf("downloader called %d times, want 1", dl.calls)
	}
	rr.mu.Lock()
	defer rr.mu.Unlock()
	if rr.calls != 1 {
		t.Fatalf("runFn called %d times, want 1", rr.calls)
	}
	if len(rr.opts[0].Substitute) != 0 {
		t.Errorf("run should use the ORIGINAL workflow (no substitute), got %v", rr.opts[0].Substitute)
	}
	wfid64, _ := strconv.ParseInt(wfID, 10, 64)
	if rr.wfIDs[0] != wfid64 {
		t.Errorf("ran workflow %d, want %d", rr.wfIDs[0], wfid64)
	}
}

// TestDownloadAndRunTraversalStaysInside: a traversal-laden reference still writes
// only its basename inside comfy_model_path/checkpoints (never escapes).
func TestDownloadAndRunTraversalStaysInside(t *testing.T) {
	const dlURL = "http://dl.example/evil"
	// The workflow references a traversal path; the file on CivitAI has that same
	// name. The write must collapse to the basename under checkpoints/.
	reader := dlRunReader{searchRaw: searchRawWithFile(t, "../../etc/evil.safetensors", dlURL)}
	srv, dl, comfyModels := newDownloadServer(t, reader, dlURL, []byte("X"))
	srv.runFn = (&runRecorder{}).fn()
	wfID := seedWorkflow(t, srv, store.WorkflowFormatAPI, `{"4":{"class_type":"CheckpointLoaderSimple","inputs":{}}}`)

	rec := post(t, srv, "/workflows/"+wfID+"/download-and-run", url.Values{
		"filename": {"../../etc/evil.safetensors"},
		"type":     {"Checkpoint"},
	}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("download-and-run = %d", rec.Code)
	}
	pollRunUntilDone(t, srv, wfID)

	inside := filepath.Join(comfyModels, "checkpoints", "evil.safetensors")
	if _, err := os.Stat(inside); err != nil {
		t.Errorf("expected the basename write inside checkpoints/, missing: %v", err)
	}
	// Nothing must be created outside comfy_model_path.
	if _, err := os.Stat(filepath.Join(filepath.Dir(comfyModels), "etc")); err == nil {
		t.Error("a file was written outside comfy_model_path")
	}
	if dl.calls != 1 {
		t.Errorf("downloader called %d times, want 1", dl.calls)
	}
}

// TestDownloadAndRunExistingFileSkipsDownload: the file already present → no
// download, but the run still starts on the original workflow.
func TestDownloadAndRunExistingFileSkipsDownload(t *testing.T) {
	const dlURL = "http://dl.example/x"
	reader := dlRunReader{searchRaw: searchRawWithFile(t, "present.safetensors", dlURL)}
	srv, dl, comfyModels := newDownloadServer(t, reader, dlURL, []byte("NEW"))
	rr := &runRecorder{}
	srv.runFn = rr.fn()
	// Pre-create the destination.
	dest := filepath.Join(comfyModels, "checkpoints", "present.safetensors")
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest, []byte("ORIGINAL"), 0o644); err != nil {
		t.Fatal(err)
	}
	wfID := seedWorkflow(t, srv, store.WorkflowFormatAPI, `{"4":{"class_type":"CheckpointLoaderSimple","inputs":{}}}`)

	post(t, srv, "/workflows/"+wfID+"/download-and-run", url.Values{
		"filename": {"present.safetensors"}, "type": {"Checkpoint"},
	}, true)
	pollRunUntilDone(t, srv, wfID)

	if dl.calls != 0 {
		t.Errorf("downloader called %d times, want 0 (file already present)", dl.calls)
	}
	if got, _ := os.ReadFile(dest); string(got) != "ORIGINAL" {
		t.Errorf("existing file changed: %q", got)
	}
	rr.mu.Lock()
	defer rr.mu.Unlock()
	if rr.calls != 1 {
		t.Errorf("runFn called %d times, want 1", rr.calls)
	}
}

// TestDownloadAndRunUnconfiguredFallback: no comfy_model_path → link-only
// fallback, NO download, NO run.
func TestDownloadAndRunUnconfiguredFallback(t *testing.T) {
	const dlURL = "http://dl.example/x"
	reader := &recordingSearchReader{result: resolveResult("Some Model")}
	srv := newLibraryTestServer(t, t.TempDir()) // no ComfyModelPath, empty ComfyURL
	srv.reader = reader
	dl := &fakeDownloader{zips: map[string][]byte{dlURL: []byte("D")}}
	srv.downloaderFn = func() civitai.Downloader { return dl }
	var ranCalls int
	srv.runFn = func(context.Context, *store.Workflow, runUpdater, runOptions) (*runResult, error) {
		ranCalls++
		return &runResult{}, nil
	}
	wfID := seedWorkflow(t, srv, store.WorkflowFormatAPI, `{"4":{"class_type":"X","inputs":{}}}`)

	rec := post(t, srv, "/workflows/"+wfID+"/download-and-run", url.Values{
		"filename": {"foo.safetensors"}, "type": {"Checkpoint"},
	}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("download-and-run = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Search CivitAI") {
		t.Errorf("unconfigured should degrade to the resolve/link fragment:\n%s", body)
	}
	if dl.calls != 0 {
		t.Errorf("downloader called %d times, want 0 (flow disabled)", dl.calls)
	}
	if ranCalls != 0 {
		t.Errorf("run started %d times, want 0", ranCalls)
	}
}

// TestDownloadAndRunRemoteComfyFallback: comfy_model_path set but a non-loopback
// comfy_url → not local → link-only fallback, no download.
func TestDownloadAndRunRemoteComfyFallback(t *testing.T) {
	const dlURL = "http://dl.example/x"
	reader := dlRunReader{searchRaw: searchRawWithFile(t, "foo.safetensors", dlURL)}
	srv, dl, _ := newDownloadServer(t, reader, dlURL, []byte("D"))
	srv.cfg.ComfyURL = "http://192.168.1.50:8188" // remote ComfyUI
	srv.runFn = (&runRecorder{}).fn()
	wfID := seedWorkflow(t, srv, store.WorkflowFormatAPI, `{"4":{"class_type":"X","inputs":{}}}`)

	rec := post(t, srv, "/workflows/"+wfID+"/download-and-run", url.Values{
		"filename": {"foo.safetensors"}, "type": {"Checkpoint"},
	}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("download-and-run = %d", rec.Code)
	}
	if dl.calls != 0 {
		t.Errorf("downloader called %d times, want 0 (remote ComfyUI)", dl.calls)
	}
	if !strings.Contains(rec.Body.String(), "Search CivitAI") {
		t.Errorf("remote ComfyUI should degrade to link-only:\n%s", rec.Body.String())
	}
}

// TestDownloadAndRunUnknownTypeFallback: an unroutable type → link-only, no
// download.
func TestDownloadAndRunUnknownTypeFallback(t *testing.T) {
	const dlURL = "http://dl.example/x"
	reader := dlRunReader{searchRaw: searchRawWithFile(t, "foo.safetensors", dlURL)}
	srv, dl, _ := newDownloadServer(t, reader, dlURL, []byte("D"))
	srv.runFn = (&runRecorder{}).fn()
	wfID := seedWorkflow(t, srv, store.WorkflowFormatAPI, `{"4":{"class_type":"X","inputs":{}}}`)

	// No/blank type → civitaiTypeParam yields "" → not routable.
	rec := post(t, srv, "/workflows/"+wfID+"/download-and-run", url.Values{
		"filename": {"foo.safetensors"},
	}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("download-and-run = %d", rec.Code)
	}
	if dl.calls != 0 {
		t.Errorf("downloader called %d times, want 0 (unroutable type)", dl.calls)
	}
}

// TestDownloadAndRunAmbiguousShowsCards: eligible, but NO file basename matches
// the reference → no auto-download; the resolve cards are shown instead.
func TestDownloadAndRunAmbiguousShowsCards(t *testing.T) {
	const dlURL = "http://dl.example/other"
	// The search returns a model whose file name does NOT equal the reference.
	reader := dlRunReader{searchRaw: searchRawWithFile(t, "different.safetensors", dlURL)}
	srv, dl, _ := newDownloadServer(t, reader, dlURL, []byte("D"))
	var ranCalls int
	srv.runFn = func(context.Context, *store.Workflow, runUpdater, runOptions) (*runResult, error) {
		ranCalls++
		return &runResult{}, nil
	}
	wfID := seedWorkflow(t, srv, store.WorkflowFormatAPI, `{"4":{"class_type":"X","inputs":{}}}`)

	rec := post(t, srv, "/workflows/"+wfID+"/download-and-run", url.Values{
		"filename": {"wanted.safetensors"}, "type": {"Checkpoint"},
	}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("download-and-run = %d", rec.Code)
	}
	if dl.calls != 0 {
		t.Errorf("downloader called %d times, want 0 (ambiguous resolution)", dl.calls)
	}
	if ranCalls != 0 {
		t.Errorf("run started %d times, want 0 (ambiguous resolution)", ranCalls)
	}
	// Fragment should carry the resolve cards / fallback link.
	if !strings.Contains(rec.Body.String(), "Search CivitAI") {
		t.Errorf("ambiguous resolution should show the resolve fragment:\n%s", rec.Body.String())
	}
}

// TestDownloadAndRunGating: CSRF rejected + non-loopback bind → gated note.
func TestDownloadAndRunGating(t *testing.T) {
	const dlURL = "http://dl.example/x"
	reader := dlRunReader{searchRaw: searchRawWithFile(t, "foo.safetensors", dlURL)}
	srv, dl, _ := newDownloadServer(t, reader, dlURL, []byte("D"))
	wfID := seedWorkflow(t, srv, store.WorkflowFormatAPI, `{"4":{"class_type":"X","inputs":{}}}`)
	form := url.Values{"filename": {"foo.safetensors"}, "type": {"Checkpoint"}}

	if rec := post(t, srv, "/workflows/"+wfID+"/download-and-run", form, false); rec.Code != http.StatusForbidden {
		t.Fatalf("download-and-run without CSRF = %d, want 403", rec.Code)
	}
	if dl.calls != 0 {
		t.Errorf("downloader called %d times on CSRF reject, want 0", dl.calls)
	}

	// Non-loopback bind → gated note (and no download).
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	gated := NewServer(st, reader, stubSubscriber{}, Config{
		BaseURL: "https://civitai.com", DefaultPollInterval: time.Hour, Addr: "0.0.0.0:8787",
		ComfyURL: "http://127.0.0.1:8188", ComfyModelPath: t.TempDir(),
	}, nil)
	gdl := &fakeDownloader{zips: map[string][]byte{dlURL: []byte("D")}}
	gated.downloaderFn = func() civitai.Downloader { return gdl }
	gid := seedWorkflow(t, gated, store.WorkflowFormatAPI, `{"4":{"class_type":"X","inputs":{}}}`)
	rec := post(t, gated, "/workflows/"+gid+"/download-and-run", form, true)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "non-loopback") {
		t.Errorf("gated download-and-run = %d body=%s", rec.Code, rec.Body.String())
	}
	if gdl.calls != 0 {
		t.Errorf("downloader called %d times on gated bind, want 0", gdl.calls)
	}
}

// TestInstallAndRunEligibilityFallback drives the real preflight-fail popover with
// a resolved CivitAI match and asserts: an eligible server posts the enabled
// "Install and run" to download-and-run; an ineligible server renders the CivitAI
// card with a DISABLED Install-and-run + reason + a "View on CivitAI ↗" link (never
// hidden), and no download-and-run POST.
func TestInstallAndRunEligibilityFallback(t *testing.T) {
	graph := `{"4":{"class_type":"CheckpointLoaderSimple","inputs":{"ckpt_name":"absent.safetensors"}}}`

	render := func(t *testing.T, eligible bool) string {
		st, err := store.Open(":memory:")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = st.Close() })
		cfg := Config{
			BaseURL: "https://civitai.com", DefaultPollInterval: time.Hour,
			Addr: "127.0.0.1:8787", ComfyURL: "http://127.0.0.1:8188",
		}
		if eligible {
			cfg.ComfyModelPath = t.TempDir()
		}
		srv := NewServer(st, &recordingSearchReader{result: resolveResult("Fabricated XL")}, stubSubscriber{}, cfg, nil)
		base, cancel := context.WithCancel(context.Background())
		srv.SetBaseContext(base)
		t.Cleanup(cancel)
		srv.comfyClientFn = func() comfyClient { return &fakeComfy{info: mustObjectInfo(t, substituteObjectInfo)} }
		id := seedWorkflow(t, srv, store.WorkflowFormatAPI, graph)
		if rec := post(t, srv, "/workflows/"+id+"/run", nil, true); rec.Code != http.StatusOK {
			t.Fatalf("run = %d", rec.Code)
		}
		return pollRunUntilDone(t, srv, id)
	}

	elig := render(t, true)
	if !strings.Contains(elig, "Install and run") || !strings.Contains(elig, "/download-and-run") {
		t.Errorf("eligible popover missing an enabled Install-and-run posting to download-and-run:\n%s", elig)
	}
	if strings.Contains(elig, "Set comfy_model_path to install here") {
		t.Errorf("eligible popover must NOT show the disabled reason:\n%s", elig)
	}

	inelig := render(t, false)
	if strings.Contains(inelig, "/download-and-run") {
		t.Errorf("ineligible popover must NOT POST download-and-run:\n%s", inelig)
	}
	if !strings.Contains(inelig, "Install and run") || !strings.Contains(inelig, "Set comfy_model_path to install here") {
		t.Errorf("ineligible popover should show a DISABLED Install-and-run + reason:\n%s", inelig)
	}
	if !strings.Contains(inelig, "View on CivitAI") {
		t.Errorf("ineligible popover should show a View-on-CivitAI link:\n%s", inelig)
	}
}

// TestResolveCacheBound asserts the resolution TTL cache does not grow past its
// cap (audit nit): after many distinct-key resolutions, it is bounded.
func TestResolveCacheBound(t *testing.T) {
	srv := newModelServer(t, &recordingSearchReader{result: resolveResult("x")})
	for i := 0; i < resolveCacheMax+50; i++ {
		srv.resolveModels(context.Background(), "query-"+strconv.Itoa(i), "")
	}
	srv.resolveMu.Lock()
	n := len(srv.resolveVal)
	m := len(srv.resolveExp)
	srv.resolveMu.Unlock()
	if n > resolveCacheMax {
		t.Errorf("resolveVal grew to %d, want <= %d", n, resolveCacheMax)
	}
	if m > resolveCacheMax {
		t.Errorf("resolveExp grew to %d, want <= %d", m, resolveCacheMax)
	}
}
