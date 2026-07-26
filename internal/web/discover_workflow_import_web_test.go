package web

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// --- fakes ---

// fakeDownloader serves canned zip bytes per download URL without touching the
// network. It counts calls so a test can assert no fetch happens on the
// CSRF/gate/token guard paths.
type fakeDownloader struct {
	zips   map[string][]byte // downloadURL -> zip bytes
	calls  int
	err    error // when set, DownloadFile returns it
	status int   // when non-zero, the response status code (default 200)
}

func (d *fakeDownloader) DownloadFile(_ context.Context, url string) (*http.Response, error) {
	d.calls++
	if d.err != nil {
		return nil, d.err
	}
	data, ok := d.zips[url]
	if !ok {
		// Convenience: fall back to any single canned zip when the URL is unmatched.
		for _, v := range d.zips {
			data, ok = v, true
			break
		}
	}
	if !ok {
		data = nil
	}
	status := d.status
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Body:       io.NopCloser(bytes.NewReader(data)),
		Header:     make(http.Header),
	}, nil
}

// buildZip builds an in-memory zip from name->content.
func buildZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

const (
	importUIGraph  = `{"nodes":[{"type":"CheckpointLoaderSimple","widgets_values":["m.safetensors"]}]}`
	importUIGraph2 = `{"nodes":[{"type":"LoraLoader","widgets_values":["lora.safetensors"]}]}`
	importAPIGraph = `{"1":{"class_type":"CheckpointLoaderSimple","inputs":{"ckpt_name":"a.safetensors"}}}`
)

// workflowsModel builds a Workflows-type ModelDetail whose primary version ships
// the given Archive files (name -> downloadURL). Callers register matching zip
// bytes on the fakeDownloader keyed by the same URLs.
func workflowsModel(archives map[string]string) *civitai.ModelDetail {
	var files []civitai.ModelVersionFile
	i := 0
	for name, url := range archives {
		i++
		files = append(files, civitai.ModelVersionFile{
			ID: i, Name: name, Type: "Archive", DownloadURL: url,
		})
	}
	return &civitai.ModelDetail{
		ID: 1818841, Name: "WAN Workflow", Type: "Workflows",
		ModelVersions: []civitai.ModelVersionSummary{{
			ID: 991, Name: "v1", BaseModel: "Wan Video 2.2", Files: files,
		}},
	}
}

// newImportServer wires a server with an in-memory store, a fakeReader returning
// the given model, and (optionally) the download seam.
func newImportServer(t *testing.T, model *civitai.ModelDetail, dl civitai.Downloader, token, addr string) *Server {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if addr == "" {
		addr = "127.0.0.1:8787"
	}
	srv := NewServer(st, fakeReader{model: model}, stubSubscriber{}, Config{
		BaseURL: "https://civitai.com", Addr: addr, Token: token, DefaultPollInterval: time.Hour,
	}, nil)
	if dl != nil {
		srv.downloaderFn = func() civitai.Downloader { return dl }
	}
	return srv
}

// postImport issues the import POST (HX so the response is the inline result
// fragment). withCSRF toggles the CSRF token.
func postImport(srv *Server, modelID int, withCSRF bool) *httptest.ResponseRecorder {
	form := ""
	if withCSRF {
		form = "csrf_token=" + srv.csrf
	}
	req := httptest.NewRequest(http.MethodPost,
		fmt.Sprintf("/workflows/discover/%d/import", modelID), strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func listWorkflows(t *testing.T, srv *Server) []store.Workflow {
	t.Helper()
	wfs, err := srv.store.ListWorkflows(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return wfs
}

// --- tests ---

// TestImportStoresWorkflows proves the happy path: fetch → unzip → store N rows,
// each Source=civitai, pre-linked to the model+version, format detected, graph_hash set.
func TestImportStoresWorkflows(t *testing.T) {
	url := "https://civitai.com/api/download/1"
	dl := &fakeDownloader{zips: map[string][]byte{
		url: buildZip(t, map[string]string{
			"flux_img2img.json":          importUIGraph,
			"flux_img2img_hiresfix.json": importUIGraph2,
		}),
	}}
	srv := newImportServer(t, workflowsModel(map[string]string{"wf.zip": url}), dl, "tok", "")

	rec := postImport(srv, 1818841, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("import = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Imported 2 workflow(s), 0 already present, 0 skipped") {
		t.Fatalf("unexpected result: %s", rec.Body.String())
	}
	wfs := listWorkflows(t, srv)
	if len(wfs) != 2 {
		t.Fatalf("stored %d workflows, want 2", len(wfs))
	}
	for _, wf := range wfs {
		if wf.Source != store.WorkflowSourceCivitai {
			t.Errorf("source = %q, want civitai", wf.Source)
		}
		if wf.ModelID == nil || *wf.ModelID != 1818841 {
			t.Errorf("model_id not pre-linked: %v", wf.ModelID)
		}
		if wf.VersionID == nil || *wf.VersionID != 991 {
			t.Errorf("version_id not pre-linked: %v", wf.VersionID)
		}
		if wf.Format != "ui" {
			t.Errorf("format = %q, want ui", wf.Format)
		}
		if wf.GraphHash == "" {
			t.Error("graph_hash not set")
		}
		if wf.BaseModel != "Wan Video 2.2" {
			t.Errorf("base_model = %q", wf.BaseModel)
		}
	}
}

// TestImportIsIdempotent proves re-importing the same model stores rows once: the
// second import reports "0 imported, N already present" and does not grow the table.
func TestImportIsIdempotent(t *testing.T) {
	url := "https://civitai.com/api/download/1"
	dl := &fakeDownloader{zips: map[string][]byte{
		url: buildZip(t, map[string]string{"a.json": importUIGraph, "b.json": importAPIGraph}),
	}}
	srv := newImportServer(t, workflowsModel(map[string]string{"wf.zip": url}), dl, "tok", "")

	if rec := postImport(srv, 1818841, true); !strings.Contains(rec.Body.String(), "Imported 2 workflow(s), 0 already present") {
		t.Fatalf("first import: %s", rec.Body.String())
	}
	if n := len(listWorkflows(t, srv)); n != 2 {
		t.Fatalf("after first import: %d rows, want 2", n)
	}
	rec := postImport(srv, 1818841, true)
	if !strings.Contains(rec.Body.String(), "Imported 0 workflow(s), 2 already present, 0 skipped") {
		t.Fatalf("second import should be a no-op: %s", rec.Body.String())
	}
	if n := len(listWorkflows(t, srv)); n != 2 {
		t.Fatalf("after second import: %d rows, want 2 (idempotent)", n)
	}
}

// TestImportMultipleArchives proves a version with multiple Archive files imports
// every zip's workflows.
func TestImportMultipleArchives(t *testing.T) {
	u1, u2 := "https://civitai.com/api/download/1", "https://civitai.com/api/download/2"
	dl := &fakeDownloader{zips: map[string][]byte{
		u1: buildZip(t, map[string]string{"one.json": importUIGraph}),
		u2: buildZip(t, map[string]string{"two.json": importAPIGraph}),
	}}
	srv := newImportServer(t, workflowsModel(map[string]string{"a.zip": u1, "b.zip": u2}), dl, "tok", "")

	rec := postImport(srv, 1818841, true)
	if !strings.Contains(rec.Body.String(), "Imported 2 workflow(s)") {
		t.Fatalf("multi-archive import: %s", rec.Body.String())
	}
	if n := len(listWorkflows(t, srv)); n != 2 {
		t.Fatalf("stored %d, want 2 (one per archive)", n)
	}
	if dl.calls != 2 {
		t.Errorf("downloader called %d times, want 2 (one per archive)", dl.calls)
	}
}

// TestImportSkipsNonWorkflowEntries proves non-.json and unparseable entries are
// counted as skipped, not errored.
func TestImportSkipsNonWorkflowEntries(t *testing.T) {
	url := "https://civitai.com/api/download/1"
	dl := &fakeDownloader{zips: map[string][]byte{
		url: buildZip(t, map[string]string{
			"good.json":     importUIGraph,       // valid
			"readme.txt":    "not a workflow",    // non-json → skipped
			"broken.json":   "{ not json",        // unparseable → skipped
			"notgraph.json": `{"hello":"world"}`, // json but not a comfy graph → skipped
		}),
	}}
	srv := newImportServer(t, workflowsModel(map[string]string{"wf.zip": url}), dl, "tok", "")

	rec := postImport(srv, 1818841, true)
	body := rec.Body.String()
	if !strings.Contains(body, "Imported 1 workflow(s), 0 already present, 3 skipped") {
		t.Fatalf("unexpected result: %s", body)
	}
	if n := len(listWorkflows(t, srv)); n != 1 {
		t.Fatalf("stored %d, want 1", n)
	}
}

// TestImportZipBombEntryRefused proves a single entry exceeding the per-entry
// uncompressed cap is refused with a clear error and no OOM (a limited copy).
func TestImportZipBombEntryRefused(t *testing.T) {
	url := "https://civitai.com/api/download/1"
	huge := string(bytes.Repeat([]byte("a"), maxWorkflowEntryBytes+64)) // > per-entry cap
	dl := &fakeDownloader{zips: map[string][]byte{
		url: buildZip(t, map[string]string{"bomb.json": huge}),
	}}
	srv := newImportServer(t, workflowsModel(map[string]string{"wf.zip": url}), dl, "tok", "")

	rec := postImport(srv, 1818841, true)
	body := rec.Body.String()
	if !strings.Contains(body, "per-entry size cap") {
		t.Fatalf("zip-bomb entry should be refused with a clear error, got: %s", body)
	}
	if n := len(listWorkflows(t, srv)); n != 0 {
		t.Fatalf("no workflow should be stored on a refused zip bomb, got %d", n)
	}
}

// TestImportTooManyEntriesRefused proves an archive over the entry-count cap is refused.
func TestImportTooManyEntriesRefused(t *testing.T) {
	url := "https://civitai.com/api/download/1"
	files := make(map[string]string, maxWorkflowZipEntries+1)
	for i := 0; i <= maxWorkflowZipEntries; i++ {
		files[fmt.Sprintf("wf%d.json", i)] = importUIGraph
	}
	dl := &fakeDownloader{zips: map[string][]byte{url: buildZip(t, files)}}
	srv := newImportServer(t, workflowsModel(map[string]string{"wf.zip": url}), dl, "tok", "")

	rec := postImport(srv, 1818841, true)
	if !strings.Contains(rec.Body.String(), "too many entries") {
		t.Fatalf("over-count archive should be refused: %s", rec.Body.String())
	}
	if n := len(listWorkflows(t, srv)); n != 0 {
		t.Fatalf("no rows should be stored when the archive is refused, got %d", n)
	}
}

// TestImportNoTokenErrors proves a missing CivitAI token yields a clear
// "configure token" error and NO fetch attempt.
func TestImportNoTokenErrors(t *testing.T) {
	dl := &fakeDownloader{zips: map[string][]byte{"u": buildZip(t, map[string]string{"a.json": importUIGraph})}}
	srv := newImportServer(t, workflowsModel(map[string]string{"wf.zip": "u"}), dl, "", "") // empty token

	rec := postImport(srv, 1818841, true)
	if !strings.Contains(rec.Body.String(), "Configure your CivitAI token") {
		t.Fatalf("expected configure-token error, got: %s", rec.Body.String())
	}
	if dl.calls != 0 {
		t.Errorf("no download must be attempted without a token, got %d calls", dl.calls)
	}
}

// TestImportCSRFEnforced proves the POST is rejected (403) without a CSRF token
// and that no fetch happens on that path.
func TestImportCSRFEnforced(t *testing.T) {
	dl := &fakeDownloader{zips: map[string][]byte{"u": buildZip(t, map[string]string{"a.json": importUIGraph})}}
	srv := newImportServer(t, workflowsModel(map[string]string{"wf.zip": "u"}), dl, "tok", "")

	rec := postImport(srv, 1818841, false) // no CSRF
	if rec.Code != http.StatusForbidden {
		t.Fatalf("import without CSRF = %d, want 403", rec.Code)
	}
	if dl.calls != 0 {
		t.Errorf("no download must happen on a CSRF failure, got %d calls", dl.calls)
	}
	if n := len(listWorkflows(t, srv)); n != 0 {
		t.Errorf("no rows on CSRF failure, got %d", n)
	}
}

// TestImportLoopbackGated proves the endpoint is disabled on a non-loopback bind
// (no fetch, gating note rendered).
func TestImportLoopbackGated(t *testing.T) {
	dl := &fakeDownloader{zips: map[string][]byte{"u": buildZip(t, map[string]string{"a.json": importUIGraph})}}
	srv := newImportServer(t, workflowsModel(map[string]string{"wf.zip": "u"}), dl, "tok", "192.168.1.5:8787")

	rec := postImport(srv, 1818841, true)
	if !strings.Contains(rec.Body.String(), "non-loopback address") {
		t.Fatalf("expected gating note, got: %s", rec.Body.String())
	}
	if dl.calls != 0 {
		t.Errorf("no download must happen when gated, got %d calls", dl.calls)
	}
}

// TestImportBadModelID proves a non-numeric model id is a 400 (no panic).
func TestImportBadModelID(t *testing.T) {
	srv := newImportServer(t, nil, &fakeDownloader{}, "tok", "")
	req := httptest.NewRequest(http.MethodPost, "/workflows/discover/abc/import",
		strings.NewReader("csrf_token="+srv.csrf))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad model id = %d, want 400", rec.Code)
	}
}

// TestImportNonWorkflowsModelRejected proves the import endpoint refuses a
// non-Workflows model (defence-in-depth for a hand-crafted POST) BEFORE fetching,
// even if that model happens to carry an Archive file.
func TestImportNonWorkflowsModelRejected(t *testing.T) {
	model := &civitai.ModelDetail{
		ID: 4242, Name: "A Checkpoint", Type: "Checkpoint",
		ModelVersions: []civitai.ModelVersionSummary{{
			ID: 5, Files: []civitai.ModelVersionFile{
				{ID: 1, Name: "bundle.zip", Type: "Archive", DownloadURL: "u"},
			},
		}},
	}
	dl := &fakeDownloader{}
	srv := newImportServer(t, model, dl, "tok", "")
	rec := postImport(srv, 4242, true)
	if !strings.Contains(rec.Body.String(), "not a Workflows-type model") {
		t.Fatalf("expected non-Workflows rejection, got: %s", rec.Body.String())
	}
	if dl.calls != 0 {
		t.Errorf("must not download for a non-Workflows model, got %d calls", dl.calls)
	}
}

// TestImportNoArchiveFile proves a Workflows model whose primary version has no
// Archive file yields a clear error (never downloads weights).
func TestImportNoArchiveFile(t *testing.T) {
	model := &civitai.ModelDetail{
		ID: 1818841, Name: "No Archive", Type: "Workflows",
		ModelVersions: []civitai.ModelVersionSummary{{
			ID: 991, Files: []civitai.ModelVersionFile{
				{ID: 1, Name: "weights.safetensors", Type: "Model", DownloadURL: "u"},
			},
		}},
	}
	dl := &fakeDownloader{}
	srv := newImportServer(t, model, dl, "tok", "")
	rec := postImport(srv, 1818841, true)
	if !strings.Contains(rec.Body.String(), "No workflow archive") {
		t.Fatalf("expected no-archive error, got: %s", rec.Body.String())
	}
	if dl.calls != 0 {
		t.Errorf("no download for a model with no archive, got %d calls", dl.calls)
	}
}
