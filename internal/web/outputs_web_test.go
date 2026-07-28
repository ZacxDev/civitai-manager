package web

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/comfy"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// newOutputsServer builds a test server with a real outputs dir at the given
// listen addr (loopback vs not, to exercise the gate). Returns the server + the
// outputs root.
func newOutputsServer(t *testing.T, addr string) (*Server, string) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	root := t.TempDir()
	srv := NewServer(st, stubReader{}, stubSubscriber{},
		Config{Addr: addr, OutputsDir: root}, nil)
	return srv, root
}

// seedGen inserts a generation with one image and writes the image file to disk.
// It returns the generation id and the image id.
func seedGen(t *testing.T, srv *Server, root string, wfID *int64, wfName string, data []byte) (int64, int64) {
	t.Helper()
	relPath := "prompt-" + wfName + "/0-img.png"
	if _, err := writeOutputImage(root, relPath, data, maxOutputImageBytes); err != nil {
		t.Fatalf("write output file: %v", err)
	}
	genID, err := srv.store.InsertGeneration(context.Background(), &store.Generation{
		WorkflowID:   wfID,
		WorkflowName: wfName,
		PromptID:     "prompt-" + wfName,
		BaseModel:    "SDXL 1.0",
		Params:       `{"widget_overrides":[{"node_id":"3","input_name":"seed","value":"42"}]}`,
	}, []store.GenerationImage{
		{Idx: 0, RelPath: relPath, Filename: "img.png", ContentType: "image/png", SizeBytes: int64(len(data))},
	})
	if err != nil {
		t.Fatalf("insert generation: %v", err)
	}
	_, imgs, err := srv.store.GetGeneration(context.Background(), genID)
	if err != nil {
		t.Fatalf("get generation: %v", err)
	}
	return genID, imgs[0].ID
}

func seedWF(t *testing.T, srv *Server, name string) int64 {
	t.Helper()
	id, err := srv.store.InsertWorkflow(context.Background(), &store.Workflow{
		Name: name, Format: store.WorkflowFormatAPI,
		Graph: `{"1":{"class_type":"X","inputs":{}}}`, Source: store.WorkflowSourceImported,
	})
	if err != nil {
		t.Fatalf("insert workflow: %v", err)
	}
	return id
}

func TestOutputsGalleryEmptyState(t *testing.T) {
	srv, _ := newOutputsServer(t, "127.0.0.1:8787")
	rec := get(t, srv, "/outputs")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "No generations yet") {
		t.Error("empty gallery missing empty-state message")
	}
	// Nav shows the Outputs entry.
	if !strings.Contains(body, `href="/outputs"`) {
		t.Error("nav missing Outputs entry")
	}
}

func TestOutputsGalleryPopulatedAndFilter(t *testing.T) {
	srv, root := newOutputsServer(t, "127.0.0.1:8787")
	wfA := seedWF(t, srv, "alpha")
	wfB := seedWF(t, srv, "beta")
	seedGen(t, srv, root, &wfA, "alpha", []byte("A"))
	seedGen(t, srv, root, &wfB, "beta", []byte("B"))

	// Unfiltered: both workflow labels appear, plus filter options.
	body := get(t, srv, "/outputs").Body.String()
	if !strings.Contains(body, "alpha") || !strings.Contains(body, "beta") {
		t.Error("gallery missing one of the generations")
	}
	if !strings.Contains(body, `name="workflow"`) {
		t.Error("gallery missing workflow filter select")
	}
	// Tiles link to detail pages and thumbnails to the image route.
	if !strings.Contains(body, "/outputs/img/") {
		t.Error("gallery tile missing image thumbnail URL")
	}

	// Filtered to wfA: only alpha's generation.
	filtered := get(t, srv, "/outputs?workflow="+strconv.FormatInt(wfA, 10)).Body.String()
	if !strings.Contains(filtered, "alpha") {
		t.Error("filtered gallery missing alpha")
	}
	// beta appears as a filter OPTION but its tile/caption should be gone. The tile
	// caption uses the label text inside a truncate span; assert beta's detail link
	// count is what we expect by checking the image route count.
	if strings.Count(filtered, "/outputs/img/") != 1 {
		t.Errorf("filtered gallery should show exactly 1 tile, got %d image URLs", strings.Count(filtered, "/outputs/img/"))
	}
}

func TestOutputsRenderPlainNoBlur(t *testing.T) {
	srv, root := newOutputsServer(t, "127.0.0.1:8787")
	wf := seedWF(t, srv, "wf")
	seedGen(t, srv, root, &wf, "wf", []byte("X"))
	body := get(t, srv, "/outputs").Body.String()
	// Outputs are a deliberate render-plain surface — no blur/reveal markup.
	for _, bad := range []string{"cm-blur", "data-blurred", "cmReveal"} {
		if strings.Contains(body, bad) {
			t.Errorf("outputs gallery must render plain, found %q", bad)
		}
	}
}

func TestOutputsImageServe(t *testing.T) {
	srv, root := newOutputsServer(t, "127.0.0.1:8787")
	wf := seedWF(t, srv, "wf")
	_, imgID := seedGen(t, srv, root, &wf, "wf", []byte("PNGBYTES"))

	rec := get(t, srv, "/outputs/img/"+strconv.FormatInt(imgID, 10))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if rec.Body.String() != "PNGBYTES" {
		t.Errorf("body = %q, want PNGBYTES", rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/png" {
		t.Errorf("content-type = %q, want image/png", ct)
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("missing nosniff header")
	}

	// Unknown image id → 404.
	if rec := get(t, srv, "/outputs/img/99999"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown image status = %d, want 404", rec.Code)
	}
}

func TestOutputsImagePathContainment(t *testing.T) {
	srv, root := newOutputsServer(t, "127.0.0.1:8787")
	// Directly insert an image row with a TRAVERSAL rel_path (simulating a corrupted
	// row). The serve route must refuse it (404), never read outside the root.
	genID, err := srv.store.InsertGeneration(context.Background(), &store.Generation{PromptID: "p"},
		[]store.GenerationImage{{Idx: 0, RelPath: "../../../etc/passwd", Filename: "x"}})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	_, imgs, _ := srv.store.GetGeneration(context.Background(), genID)
	rec := get(t, srv, "/outputs/img/"+strconv.FormatInt(imgs[0].ID, 10))
	if rec.Code != http.StatusNotFound {
		t.Errorf("traversal rel_path status = %d, want 404", rec.Code)
	}
	_ = root
}

func TestGenerationDetailShowsParamsAndActions(t *testing.T) {
	srv, root := newOutputsServer(t, "127.0.0.1:8787")
	wf := seedWF(t, srv, "wf")
	genID, _ := seedGen(t, srv, root, &wf, "wf", []byte("X"))

	body := get(t, srv, "/outputs/"+strconv.FormatInt(genID, 10)).Body.String()
	// Params panel: the widget override snapshot is shown.
	if !strings.Contains(body, "Parameter edits") || !strings.Contains(body, "seed = 42") {
		t.Error("detail missing widget-override params")
	}
	// Re-run form (workflow present) + delete form.
	if !strings.Contains(body, "/outputs/"+strconv.FormatInt(genID, 10)+"/rerun") {
		t.Error("detail missing re-run action")
	}
	if !strings.Contains(body, "/outputs/"+strconv.FormatInt(genID, 10)+"/delete") {
		t.Error("detail missing delete action")
	}
	// Lightbox wired.
	if !strings.Contains(body, "cm-lightbox") || !strings.Contains(body, "cmOpenLightbox") {
		t.Error("detail missing lightbox")
	}
}

func TestGenerationDetailEscapesUntrustedName(t *testing.T) {
	srv, root := newOutputsServer(t, "127.0.0.1:8787")
	// A hostile workflow-name snapshot must be escaped on render.
	genID, _ := seedGen(t, srv, root, nil, `<script>alert(1)</script>`, []byte("X"))
	body := get(t, srv, "/outputs/"+strconv.FormatInt(genID, 10)).Body.String()
	if strings.Contains(body, "<script>alert(1)</script>") {
		t.Error("untrusted workflow name was not escaped")
	}
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Error("expected escaped workflow name in output")
	}
}

func TestGenerationDetailRerunDisabledForOrphan(t *testing.T) {
	srv, root := newOutputsServer(t, "127.0.0.1:8787")
	genID, _ := seedGen(t, srv, root, nil, "orphan", []byte("X")) // workflow_id NULL
	body := get(t, srv, "/outputs/"+strconv.FormatInt(genID, 10)).Body.String()
	// No rerun form action, and the disabled button carries the explanatory title.
	if strings.Contains(body, "/rerun") {
		t.Error("orphan generation must not offer a re-run action")
	}
	if !strings.Contains(body, "disabled") || !strings.Contains(body, "deleted") {
		t.Error("orphan detail should show a disabled re-run with a 'deleted' note")
	}
}

func TestRerunReconstructsAndStartsRun(t *testing.T) {
	srv, root := newOutputsServer(t, "127.0.0.1:8787")
	wf := seedWF(t, srv, "wf")
	genID, _ := seedGen(t, srv, root, &wf, "wf", []byte("X"))

	ran := make(chan runOptions, 1)
	srv.runFn = func(_ context.Context, _ *store.Workflow, _ runUpdater, opts runOptions) (*runResult, error) {
		ran <- opts
		return &runResult{}, nil // no images → no capture side effects
	}

	rec := post(t, srv, "/outputs/"+strconv.FormatInt(genID, 10)+"/rerun", url.Values{}, true)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("rerun status = %d, want 303", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/workflows/"+strconv.FormatInt(wf, 10) {
		t.Errorf("redirect = %q, want the workflow detail page", loc)
	}
	select {
	case opts := <-ran:
		// The stored widget override was reconstructed into runOptions.
		if opts.WidgetOverrides == nil {
			t.Fatal("run started without reconstructed WidgetOverrides")
		}
		if got := opts.WidgetOverrides[comfy.WidgetOverrideKey{NodeID: "3", InputName: "seed"}]; got != "42" {
			t.Errorf("reconstructed widget override = %q, want 42", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runFn was never invoked")
	}
}

// seedGenWithParams inserts a generation carrying an explicit params blob and graph
// hash, so the re-run staleness guard can be exercised.
func seedGenWithParams(t *testing.T, srv *Server, root string, wfID *int64, name, params, graphHash string) int64 {
	t.Helper()
	relPath := "prompt-" + name + "/0-img.png"
	if _, err := writeOutputImage(root, relPath, []byte("X"), maxOutputImageBytes); err != nil {
		t.Fatalf("write output file: %v", err)
	}
	genID, err := srv.store.InsertGeneration(context.Background(), &store.Generation{
		WorkflowID: wfID, WorkflowName: name, PromptID: "prompt-" + name,
		GraphHash: graphHash, Params: params,
	}, []store.GenerationImage{
		{Idx: 0, RelPath: relPath, Filename: "img.png", ContentType: "image/png", SizeBytes: 1},
	})
	if err != nil {
		t.Fatalf("insert generation: %v", err)
	}
	return genID
}

// TestRerunRefusesStalePositionalOverrides is the F2 regression at the HTTP layer: a
// generation whose workflow graph has been replaced (a rescan rewrites graph +
// graph_hash in place) must NOT be re-run with its stored (node, widget index)
// overrides — they would land on whatever widget now occupies that slot.
func TestRerunRefusesStalePositionalOverrides(t *testing.T) {
	srv, root := newOutputsServer(t, "127.0.0.1:8787")
	wf := seedWF(t, srv, "wf")
	const params = `{"ui_widget_overrides":[{"node_id":"3","widget":0,"value":"999"}]}`
	genID := seedGenWithParams(t, srv, root, &wf, "stale", params, "HASH-FROM-AN-OLDER-GRAPH")

	started := make(chan runOptions, 1)
	srv.runFn = func(_ context.Context, _ *store.Workflow, _ runUpdater, opts runOptions) (*runResult, error) {
		started <- opts
		return &runResult{}, nil
	}

	rec := post(t, srv, "/outputs/"+strconv.FormatInt(genID, 10)+"/rerun", url.Values{}, true)
	if rec.Code != http.StatusConflict {
		t.Fatalf("stale rerun status = %d, want 409", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "graph has changed") {
		t.Errorf("the user must be told why: %q", rec.Body.String())
	}
	select {
	case opts := <-started:
		t.Fatalf("no run may start with stale positional overrides: %+v", opts)
	case <-time.After(300 * time.Millisecond):
	}
}

// TestRerunAppliesPositionalOverridesWhenGraphUnchanged is the other half: when the
// hash still matches, the index-keyed overrides DO replay.
func TestRerunAppliesPositionalOverridesWhenGraphUnchanged(t *testing.T) {
	srv, root := newOutputsServer(t, "127.0.0.1:8787")
	wf := seedWF(t, srv, "wf")
	cur, err := srv.store.GetWorkflow(context.Background(), wf)
	if err != nil {
		t.Fatalf("get workflow: %v", err)
	}
	const params = `{"ui_widget_overrides":[{"node_id":"3","widget":2,"value":"999"}]}`
	genID := seedGenWithParams(t, srv, root, &wf, "fresh", params, cur.GraphHash)

	ran := make(chan runOptions, 1)
	srv.runFn = func(_ context.Context, _ *store.Workflow, _ runUpdater, opts runOptions) (*runResult, error) {
		ran <- opts
		return &runResult{}, nil
	}
	if rec := post(t, srv, "/outputs/"+strconv.FormatInt(genID, 10)+"/rerun", url.Values{}, true); rec.Code != http.StatusSeeOther {
		t.Fatalf("rerun status = %d, want 303", rec.Code)
	}
	select {
	case opts := <-ran:
		if opts.UIWidgetOverrides[comfy.UIWidgetKey{NodeID: "3", Widget: 2}] != "999" {
			t.Errorf("positional override not replayed: %+v", opts.UIWidgetOverrides)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runFn was never invoked")
	}
}

// TestGenerationDetailShowsBothOverrideSchemes is the F3 regression: the "Parameter
// edits" block must render for the CURRENT index-keyed snapshot as well as the legacy
// name-keyed one — showing only the legacy shape blanked the block for every new run.
func TestGenerationDetailShowsBothOverrideSchemes(t *testing.T) {
	srv, root := newOutputsServer(t, "127.0.0.1:8787")
	wf := seedWF(t, srv, "wf")

	newID := seedGenWithParams(t, srv, root, &wf, "newstyle",
		`{"ui_widget_overrides":[{"node_id":"3","widget":0,"value":"a new prompt"}]}`, "H")
	body := get(t, srv, "/outputs/"+strconv.FormatInt(newID, 10)).Body.String()
	if !strings.Contains(body, "Parameter edits") || !strings.Contains(body, "node 3 · widget 0 = a new prompt") {
		t.Errorf("index-keyed parameter edits not rendered:\n%s", body)
	}

	oldID := seedGenWithParams(t, srv, root, &wf, "oldstyle",
		`{"widget_overrides":[{"node_id":"7","input_name":"seed","value":"42"}]}`, "H")
	body = get(t, srv, "/outputs/"+strconv.FormatInt(oldID, 10)).Body.String()
	if !strings.Contains(body, "Parameter edits") || !strings.Contains(body, "node 7 · seed = 42") {
		t.Errorf("legacy parameter edits not rendered:\n%s", body)
	}
}

func TestRerun404WhenWorkflowDeleted(t *testing.T) {
	srv, root := newOutputsServer(t, "127.0.0.1:8787")
	genID, _ := seedGen(t, srv, root, nil, "orphan", []byte("X"))
	rec := post(t, srv, "/outputs/"+strconv.FormatInt(genID, 10)+"/rerun", url.Values{}, true)
	if rec.Code != http.StatusNotFound {
		t.Errorf("orphan rerun status = %d, want 404", rec.Code)
	}
}

func TestRerunRejectsWithoutCSRF(t *testing.T) {
	srv, root := newOutputsServer(t, "127.0.0.1:8787")
	wf := seedWF(t, srv, "wf")
	genID, _ := seedGen(t, srv, root, &wf, "wf", []byte("X"))
	rec := post(t, srv, "/outputs/"+strconv.FormatInt(genID, 10)+"/rerun", url.Values{}, false)
	if rec.Code != http.StatusForbidden {
		t.Errorf("rerun without CSRF status = %d, want 403", rec.Code)
	}
}

func TestRerunLoopbackGated(t *testing.T) {
	srv, root := newOutputsServer(t, "0.0.0.0:8787") // non-loopback bind
	wf := seedWF(t, srv, "wf")
	genID, _ := seedGen(t, srv, root, &wf, "wf", []byte("X"))
	ranFn := false
	srv.runFn = func(context.Context, *store.Workflow, runUpdater, runOptions) (*runResult, error) {
		ranFn = true
		return &runResult{}, nil
	}
	rec := post(t, srv, "/outputs/"+strconv.FormatInt(genID, 10)+"/rerun", url.Values{}, true)
	if !strings.Contains(rec.Body.String(), "non-loopback") {
		t.Errorf("rerun on a non-loopback bind should be gated; body = %q", rec.Body.String())
	}
	if ranFn {
		t.Error("run must not start when loopback-gated")
	}
}

func TestDeleteRemovesRowsAndFiles(t *testing.T) {
	srv, root := newOutputsServer(t, "127.0.0.1:8787")
	wf := seedWF(t, srv, "wf")
	genID, imgID := seedGen(t, srv, root, &wf, "wf", []byte("X"))

	filePath := filepath.Join(root, "prompt-wf", "0-img.png")
	if _, err := os.Stat(filePath); err != nil {
		t.Fatalf("seed file missing: %v", err)
	}

	rec := post(t, srv, "/outputs/"+strconv.FormatInt(genID, 10)+"/delete", url.Values{}, true)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("delete status = %d, want 303", rec.Code)
	}
	// Row gone.
	if _, _, err := srv.store.GetGeneration(context.Background(), genID); err != store.ErrNotFound {
		t.Errorf("generation still present after delete: %v", err)
	}
	// Image row gone (would 404 on serve).
	if rec := get(t, srv, "/outputs/img/"+strconv.FormatInt(imgID, 10)); rec.Code != http.StatusNotFound {
		t.Errorf("image still served after delete: %d", rec.Code)
	}
	// File gone.
	if _, err := os.Stat(filePath); !os.IsNotExist(err) {
		t.Errorf("output file still present after delete: %v", err)
	}
}

func TestDeleteRejectsWithoutCSRF(t *testing.T) {
	srv, root := newOutputsServer(t, "127.0.0.1:8787")
	wf := seedWF(t, srv, "wf")
	genID, _ := seedGen(t, srv, root, &wf, "wf", []byte("X"))
	rec := post(t, srv, "/outputs/"+strconv.FormatInt(genID, 10)+"/delete", url.Values{}, false)
	if rec.Code != http.StatusForbidden {
		t.Errorf("delete without CSRF status = %d, want 403", rec.Code)
	}
	// The generation must survive a rejected delete.
	if _, _, err := srv.store.GetGeneration(context.Background(), genID); err != nil {
		t.Errorf("generation should survive a CSRF-rejected delete: %v", err)
	}
}

func TestDeleteLoopbackGated(t *testing.T) {
	srv, root := newOutputsServer(t, "0.0.0.0:8787")
	wf := seedWF(t, srv, "wf")
	genID, _ := seedGen(t, srv, root, &wf, "wf", []byte("X"))
	rec := post(t, srv, "/outputs/"+strconv.FormatInt(genID, 10)+"/delete", url.Values{}, true)
	if !strings.Contains(rec.Body.String(), "non-loopback") {
		t.Errorf("delete on a non-loopback bind should be gated; body = %q", rec.Body.String())
	}
	if _, _, err := srv.store.GetGeneration(context.Background(), genID); err != nil {
		t.Errorf("generation should survive a gated delete: %v", err)
	}
}

func TestWorkflowDetailShowsOutputsSection(t *testing.T) {
	srv, root := newOutputsServer(t, "127.0.0.1:8787")
	// The workflow detail page needs a comfy URL configured to render the run
	// panel, but the outputs section is independent — seed a generation and assert
	// the section renders.
	wf := seedWF(t, srv, "wf")
	seedGen(t, srv, root, &wf, "wf", []byte("X"))
	body := get(t, srv, "/workflows/"+strconv.FormatInt(wf, 10)).Body.String()
	if !strings.Contains(body, "Recent outputs") {
		t.Error("workflow detail missing the per-workflow outputs section")
	}
	if !strings.Contains(body, "View all 1 →") {
		t.Error("outputs section missing the View all link")
	}
}
