package web

import (
	"bytes"
	"context"
	"encoding/binary"
	"hash/crc32"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/store"
)

// newWorkflowServer builds a test server bound to a loopback address so the
// import endpoints' loopback gate is OPEN (the default newTestServer leaves Addr
// blank, which reads as non-loopback and would gate imports off).
func newWorkflowServer(t *testing.T) *Server {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return NewServer(st, stubReader{}, stubSubscriber{},
		Config{BaseURL: "https://civitai.com", DefaultPollInterval: time.Hour, Addr: "127.0.0.1:8972"}, nil)
}

const testAPIGraph = `{"3":{"class_type":"CheckpointLoaderSimple","inputs":{"ckpt_name":"sdxl.safetensors"}}}`

// buildTestPNG assembles a minimal valid PNG with the given tEXt keyword/value.
func buildTestPNG(keyword, value string) []byte {
	chunk := func(typ string, data []byte) []byte {
		var b bytes.Buffer
		_ = binary.Write(&b, binary.BigEndian, uint32(len(data)))
		b.WriteString(typ)
		b.Write(data)
		crc := crc32.NewIEEE()
		crc.Write([]byte(typ))
		crc.Write(data)
		_ = binary.Write(&b, binary.BigEndian, crc.Sum32())
		return b.Bytes()
	}
	var b bytes.Buffer
	b.Write([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'})
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:4], 1)
	binary.BigEndian.PutUint32(ihdr[4:8], 1)
	ihdr[8], ihdr[9] = 8, 6
	b.Write(chunk("IHDR", ihdr))
	text := append([]byte(keyword), 0)
	text = append(text, []byte(value)...)
	b.Write(chunk("tEXt", text))
	b.Write(chunk("IEND", nil))
	return b.Bytes()
}

func postForm(srv *Server, path, body string, withCSRF bool) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if withCSRF {
		req.Header.Set("X-CSRF-Token", srv.csrf)
	}
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func countWorkflows(t *testing.T, srv *Server) int {
	t.Helper()
	wfs, err := srv.store.ListWorkflows(context.Background())
	if err != nil {
		t.Fatalf("list workflows: %v", err)
	}
	return len(wfs)
}

// TestWorkflowsPageRedirects proves the legacy standalone /workflows page now
// 303-redirects to the Workflows Library tab (the UI moved into /library).
func TestWorkflowsPageRedirects(t *testing.T) {
	srv := newWorkflowServer(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/workflows", nil)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/library?tab=workflows" {
		t.Errorf("redirect = %q, want /library?tab=workflows", loc)
	}
}

// TestWorkflowsLibraryTabRenders proves the Workflows tab of /library renders the
// import affordances, the workflow-scan control, and the tab strip entry.
func TestWorkflowsLibraryTabRenders(t *testing.T) {
	srv := newWorkflowServer(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/library?tab=workflows", nil)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"Import a workflow", "/workflows/import", "/workflows/import-png",
		"Scan workflows", `hx-post="/library/workflow-scan"`,
		`/library?tab=workflows`, // the tab-strip link
	} {
		if !strings.Contains(body, want) {
			t.Errorf("workflows tab missing %q", want)
		}
	}
}

func TestWorkflowImportHappyPath(t *testing.T) {
	srv := newWorkflowServer(t)
	body := "name=mywf&graph=" + urlEsc(testAPIGraph) + "&csrf_token=" + srv.csrf
	rec := postForm(srv, "/workflows/import", body, true)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body=%s", rec.Code, rec.Body.String())
	}
	wfs, _ := srv.store.ListWorkflows(context.Background())
	if len(wfs) != 1 {
		t.Fatalf("expected 1 workflow, got %d", len(wfs))
	}
	got := wfs[0]
	if got.Name != "mywf" || got.Format != store.WorkflowFormatAPI || got.Source != store.WorkflowSourceImported {
		t.Errorf("stored workflow wrong: %+v", got)
	}
	if len(got.Resources) != 1 || got.Resources[0] != "sdxl.safetensors" {
		t.Errorf("resources not extracted: %v", got.Resources)
	}
}

func TestWorkflowImportBadJSON(t *testing.T) {
	srv := newWorkflowServer(t)
	body := "graph=" + urlEsc("this is not json") + "&csrf_token=" + srv.csrf
	rec := postForm(srv, "/workflows/import", body, true)
	// Bad JSON still redirects (with an error flash) — but no row is created.
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	if strings.Contains(rec.Header().Get("Location"), "level=error") == false {
		t.Errorf("expected error flash in redirect, got %q", rec.Header().Get("Location"))
	}
	if n := countWorkflows(t, srv); n != 0 {
		t.Errorf("expected no workflow row on bad JSON, got %d", n)
	}
}

func TestWorkflowImportCSRFRejected(t *testing.T) {
	srv := newWorkflowServer(t)
	body := "graph=" + urlEsc(testAPIGraph) // no csrf
	rec := postForm(srv, "/workflows/import", body, false)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if n := countWorkflows(t, srv); n != 0 {
		t.Errorf("CSRF-rejected import must not create a row, got %d", n)
	}
}

func TestWorkflowImportLoopbackGate(t *testing.T) {
	// A non-loopback bind gates the import endpoint even with a valid CSRF token.
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	srv := NewServer(st, stubReader{}, stubSubscriber{},
		Config{BaseURL: "https://civitai.com", DefaultPollInterval: time.Hour, Addr: "0.0.0.0:8972"}, nil)

	body := "graph=" + urlEsc(testAPIGraph) + "&csrf_token=" + srv.csrf
	rec := postForm(srv, "/workflows/import", body, true)
	// gate() renders a note with 200 and does NOT insert.
	if rec.Code == http.StatusSeeOther {
		t.Fatalf("non-loopback import should be gated, not redirected (got %d)", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "non-loopback") {
		t.Errorf("expected gating note, got %q", rec.Body.String())
	}
	if n := countWorkflows(t, srv); n != 0 {
		t.Errorf("gated import must not create a row, got %d", n)
	}
}

func TestWorkflowImportPNG(t *testing.T) {
	srv := newWorkflowServer(t)
	png := buildTestPNG("prompt", testAPIGraph)

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("csrf_token", srv.csrf)
	fw, _ := mw.CreateFormFile("png", "cool.png")
	_, _ = fw.Write(png)
	_ = mw.Close()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/workflows/import-png", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body=%s", rec.Code, rec.Body.String())
	}
	wfs, _ := srv.store.ListWorkflows(context.Background())
	if len(wfs) != 1 {
		t.Fatalf("expected 1 workflow from PNG, got %d", len(wfs))
	}
	got := wfs[0]
	if got.Format != store.WorkflowFormatAPI || got.Source != store.WorkflowSourceExtractedPNG {
		t.Errorf("png workflow wrong: %+v", got)
	}
	if got.Name != "cool" {
		t.Errorf("name = %q, want derived from filename 'cool'", got.Name)
	}
}

func TestWorkflowImportPNG_A1111(t *testing.T) {
	srv := newWorkflowServer(t)
	png := buildTestPNG("parameters", "masterpiece\nSteps: 20")

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("csrf_token", srv.csrf)
	fw, _ := mw.CreateFormFile("png", "a1111.png")
	_, _ = fw.Write(png)
	_ = mw.Close()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/workflows/import-png", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	if !strings.Contains(rec.Header().Get("Location"), "level=error") {
		t.Errorf("A1111 png should flash an error, got %q", rec.Header().Get("Location"))
	}
	if n := countWorkflows(t, srv); n != 0 {
		t.Errorf("A1111 png must not create a workflow, got %d", n)
	}
}

func TestWorkflowDelete(t *testing.T) {
	srv := newWorkflowServer(t)
	id, err := srv.store.InsertWorkflow(context.Background(), &store.Workflow{
		Format: store.WorkflowFormatAPI, Graph: "{}", Source: store.WorkflowSourceImported})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	rec := postForm(srv, "/workflows/"+itoa64(id)+"/delete", "csrf_token="+srv.csrf, true)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	if n := countWorkflows(t, srv); n != 0 {
		t.Errorf("expected 0 workflows after delete, got %d", n)
	}
}

func TestWorkflowSetGoldenRequiresAttachment(t *testing.T) {
	srv := newWorkflowServer(t)
	ctx := context.Background()
	id, _ := srv.store.InsertWorkflow(ctx, &store.Workflow{
		Format: store.WorkflowFormatAPI, Graph: "{}", Source: store.WorkflowSourceImported})

	// Golden without a version → error flash, not golden.
	rec := postForm(srv, "/workflows/"+itoa64(id)+"/golden", "action=set&csrf_token="+srv.csrf, true)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	if !strings.Contains(rec.Header().Get("Location"), "level=error") {
		t.Errorf("golden-without-version should flash error, got %q", rec.Header().Get("Location"))
	}
	wf, _ := srv.store.GetWorkflow(ctx, id)
	if wf.IsGolden {
		t.Fatal("workflow should not be golden without a version")
	}

	// Attach a version, then golden succeeds.
	if err := srv.store.AttachWorkflow(ctx, int(id), intp(1), intp(2)); err != nil {
		t.Fatalf("attach: %v", err)
	}
	rec = postForm(srv, "/workflows/"+itoa64(id)+"/golden", "action=set&csrf_token="+srv.csrf, true)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	wf, _ = srv.store.GetWorkflow(ctx, id)
	if !wf.IsGolden {
		t.Fatal("workflow should be golden after attachment + set")
	}
}

func TestWorkflowAttach(t *testing.T) {
	srv := newWorkflowServer(t)
	ctx := context.Background()
	id, _ := srv.store.InsertWorkflow(ctx, &store.Workflow{
		Format: store.WorkflowFormatAPI, Graph: "{}", Source: store.WorkflowSourceImported})

	rec := postForm(srv, "/workflows/"+itoa64(id)+"/attach",
		"model_id=100&version_id=200&csrf_token="+srv.csrf, true)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	wf, _ := srv.store.GetWorkflow(ctx, id)
	if wf.ModelID == nil || *wf.ModelID != 100 || wf.VersionID == nil || *wf.VersionID != 200 {
		t.Errorf("attach not applied: %+v", wf)
	}
}

// small local helpers (avoid importing strconv/url into the test file).
func urlEsc(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == ' ':
			b.WriteByte('+')
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') ||
			r == '-' || r == '_' || r == '.' || r == '~':
			b.WriteRune(r)
		default:
			for _, by := range []byte(string(r)) {
				b.WriteByte('%')
				const hexdig = "0123456789ABCDEF"
				b.WriteByte(hexdig[by>>4])
				b.WriteByte(hexdig[by&0xf])
			}
		}
	}
	return b.String()
}
