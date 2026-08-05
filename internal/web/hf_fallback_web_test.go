package web

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
	"github.com/ZacxDev/civitai-manager/internal/comfy"
	"github.com/ZacxDev/civitai-manager/internal/hf"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// fakeHFClient is the web-test seam for the HuggingFace fallback: it returns a canned
// Match for any ref and serves canned bytes for its download URL, recording calls.
type fakeHFClient struct {
	match       *hf.Match
	ok          bool
	body        []byte
	resolveHits int
	dlCalls     int
	status      int

	// inRepo is the canned answer for ResolveInRepo (the note-links path). It is a
	// SEPARATE field from match on purpose: the whole premise of that path is that
	// the search-driven Resolve cannot find the file, so a fake where the two share
	// an answer could not express the case under test. inRepoCalls / inRepoArgs
	// record the calls so a test can prove egress did or did not happen.
	inRepo     *hf.Match
	inRepoOK   bool
	inRepoErr  error
	inRepoArgs [][2]string

	// dlURLs records the URL of every DownloadFile call, so a test can prove WHICH
	// url was fetched. Without it a plan built from the note's own unpinned /main/
	// URL — instead of the commit-pinned one the lookup returned — passes every
	// assertion, because the fake serves the same bytes whatever it is handed.
	dlURLs []string
}

func (f *fakeHFClient) Resolve(context.Context, string) (*hf.Match, bool, error) {
	f.resolveHits++
	return f.match, f.ok, nil
}

func (f *fakeHFClient) ResolveInRepo(_ context.Context, repo, basename string) (*hf.Match, bool, error) {
	f.inRepoArgs = append(f.inRepoArgs, [2]string{repo, basename})
	return f.inRepo, f.inRepoOK, f.inRepoErr
}

func (f *fakeHFClient) DownloadFile(_ context.Context, fileURL string) (*http.Response, error) {
	f.dlCalls++
	f.dlURLs = append(f.dlURLs, fileURL)
	status := f.status
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{
		StatusCode:    status,
		Status:        fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Body:          io.NopCloser(bytes.NewReader(f.body)),
		ContentLength: int64(len(f.body)),
		Header:        make(http.Header),
	}, nil
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// emptySearchReader reaches CivitAI but returns zero items (the trigger for the HF
// fallback).
type emptySearchReader struct{ stubReader }

func (emptySearchReader) SearchModels(context.Context, url.Values) (*civitai.ModelSearchResult, error) {
	return &civitai.ModelSearchResult{}, nil
}

// newHFServer builds a Download & run server whose CivitAI search misses and whose HF
// fallback is the given fake. comfy_model_path is a fresh writable temp dir (returned).
func newHFServer(t *testing.T, fake *fakeHFClient) (*Server, string) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	comfyModels := t.TempDir()
	srv := NewServer(st, emptySearchReader{}, stubSubscriber{}, Config{
		BaseURL: "https://civitai.com", DefaultPollInterval: time.Hour,
		Addr:           "127.0.0.1:8787",
		ComfyURL:       "http://127.0.0.1:8188",
		ComfyModelPath: comfyModels,
		HFFallback:     true,
	}, nil)
	base, cancel := context.WithCancel(context.Background())
	srv.SetBaseContext(base)
	t.Cleanup(cancel)
	srv.hfClientFn = func() hfClient { return fake }
	return srv, comfyModels
}

// curatedMatch builds an auto-eligible curated HF match for the detector file.
func curatedMatch(body []byte) *hf.Match {
	return &hf.Match{
		Repo: "Bingsu/adetailer", Path: "face_yolov9c.pt", FileName: "face_yolov9c.pt",
		Revision: "53cc19de", URL: "https://huggingface.co/Bingsu/adetailer/resolve/53cc19de/face_yolov9c.pt",
		SHA256: sha256Hex(body), Gated: false, Source: hf.SourceCurated, RecognizedOrg: true,
		Subdir: "ultralytics/bbox", License: "apache-2.0", Downloads: 10700000,
	}
}

// TestHFFallbackInstallsAndVerifies: CivitAI misses → auto-eligible HF match →
// downloads through the HF client, sha-verifies, writes into ultralytics/bbox, runs.
func TestHFFallbackInstallsAndVerifies(t *testing.T) {
	body := []byte("YOLO-DETECTOR-WEIGHTS")
	fake := &fakeHFClient{match: curatedMatch(body), ok: true, body: body}
	srv, comfyModels := newHFServer(t, fake)
	rr := &runRecorder{}
	srv.runFn = rr.fn()
	wfID := seedWorkflow(t, srv, store.WorkflowFormatAPI,
		`{"42":{"class_type":"UltralyticsDetectorProvider","inputs":{"model_name":"bbox/face_yolov9c.pt"}}}`)

	rec := post(t, srv, "/workflows/"+wfID+"/download-and-run", url.Values{
		"filename": {"bbox/face_yolov9c.pt"},
	}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("download-and-run = %d", rec.Code)
	}
	pollRunUntilDone(t, srv, wfID)

	dest := filepath.Join(comfyModels, "ultralytics", "bbox", "face_yolov9c.pt")
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("HF model not written to %s: %v", dest, err)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("dest content = %q, want %q", got, body)
	}
	if fake.dlCalls != 1 {
		t.Errorf("HF downloader called %d times, want 1", fake.dlCalls)
	}
	rr.mu.Lock()
	defer rr.mu.Unlock()
	if rr.calls != 1 {
		t.Errorf("runFn called %d times, want 1", rr.calls)
	}
}

// TestHFFallbackShaMismatchRefused: the HF match's sha does not match the served
// bytes → the download is refused (no file written), and the run does NOT start.
func TestHFFallbackShaMismatchRefused(t *testing.T) {
	body := []byte("REAL-BYTES")
	m := curatedMatch(body)
	m.SHA256 = sha256Hex([]byte("SOMETHING ELSE")) // wrong expected hash
	fake := &fakeHFClient{match: m, ok: true, body: body}
	srv, comfyModels := newHFServer(t, fake)
	var ran int
	srv.runFn = func(context.Context, *store.Workflow, runUpdater, runOptions) (*runResult, error) {
		ran++
		return &runResult{}, nil
	}
	wfID := seedWorkflow(t, srv, store.WorkflowFormatAPI,
		`{"42":{"class_type":"UltralyticsDetectorProvider","inputs":{"model_name":"bbox/face_yolov9c.pt"}}}`)

	rec := post(t, srv, "/workflows/"+wfID+"/download-and-run", url.Values{
		"filename": {"bbox/face_yolov9c.pt"},
	}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("download-and-run = %d", rec.Code)
	}
	pollRunUntilDone(t, srv, wfID)

	dest := filepath.Join(comfyModels, "ultralytics", "bbox", "face_yolov9c.pt")
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Error("a sha-mismatched HF download must NOT be written")
	}
	if ran != 0 {
		t.Errorf("run started %d times after a failed verified download, want 0", ran)
	}
}

// TestHFFallbackGatedNotAutoInstalled: a gated HF match is NOT auto-installed via the
// POST handler (no download); resolveInstallPlan returns not-ok → resolve fragment.
func TestHFFallbackGatedNotAutoInstalled(t *testing.T) {
	body := []byte("W")
	m := curatedMatch(body)
	m.Gated = true
	fake := &fakeHFClient{match: m, ok: true, body: body}
	srv, comfyModels := newHFServer(t, fake)
	var ran int
	srv.runFn = func(context.Context, *store.Workflow, runUpdater, runOptions) (*runResult, error) {
		ran++
		return &runResult{}, nil
	}
	wfID := seedWorkflow(t, srv, store.WorkflowFormatAPI,
		`{"42":{"class_type":"UltralyticsDetectorProvider","inputs":{"model_name":"bbox/face_yolov9c.pt"}}}`)

	rec := post(t, srv, "/workflows/"+wfID+"/download-and-run", url.Values{
		"filename": {"bbox/face_yolov9c.pt"},
	}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("download-and-run = %d", rec.Code)
	}
	if fake.dlCalls != 0 {
		t.Errorf("a gated HF match must not be downloaded (dlCalls=%d)", fake.dlCalls)
	}
	if _, err := os.Stat(filepath.Join(comfyModels, "ultralytics", "bbox", "face_yolov9c.pt")); !os.IsNotExist(err) {
		t.Error("a gated HF match must not write a file")
	}
	if ran != 0 {
		t.Errorf("run started %d times for a gated match, want 0", ran)
	}
}

// TestHFFallbackDisabledNoEgress: with hf_fallback off, the fallback client is never
// consulted even when CivitAI misses.
func TestHFFallbackDisabledNoEgress(t *testing.T) {
	body := []byte("W")
	fake := &fakeHFClient{match: curatedMatch(body), ok: true, body: body}
	srv, _ := newHFServer(t, fake)
	srv.cfg.HFFallback = false
	// With the explicit seam set, hfClientOrNil returns it regardless of the flag; so
	// null out the seam to exercise the production gate.
	srv.hfClientFn = nil
	srv.runFn = (&runRecorder{}).fn()
	wfID := seedWorkflow(t, srv, store.WorkflowFormatAPI,
		`{"42":{"class_type":"UltralyticsDetectorProvider","inputs":{"model_name":"bbox/face_yolov9c.pt"}}}`)

	if got := srv.resolveHF(context.Background(), "bbox/face_yolov9c.pt"); got != nil {
		t.Errorf("resolveHF must return nil when the fallback is disabled, got %+v", got)
	}
	_ = wfID
}

// TestResolveMissingModelsPopulatesHF: at run-settle, a missing model that CivitAI
// cannot match gets its HF fallback resolved and eligibility computed; the terminal
// popover section then shows it.
func TestResolveMissingModelsPopulatesHF(t *testing.T) {
	body := []byte("W")
	fake := &fakeHFClient{match: curatedMatch(body), ok: true, body: body}
	srv, _ := newHFServer(t, fake)

	models := []comfy.MissingModel{{Filename: "bbox/face_yolov9c.pt", Query: "face yolov9c"}}
	res, _ := srv.resolveMissingModels(context.Background(), models)
	mr := res["bbox/face_yolov9c.pt"]
	if mr.HF == nil {
		t.Fatal("HF fallback match should be populated when CivitAI has no items")
	}
	if !mr.HFInstallEligible {
		t.Error("a curated non-gated sha-pinned match with comfy_model_path set should be install-eligible")
	}
	if fake.resolveHits == 0 {
		t.Error("the HF resolver should have been consulted")
	}

	// The rendered popover section shows the HF match.
	section := renderString(t, civitaiMatchSection(models[0], mr, 3, "csrf", true, fullMaturityRange()))
	if !strings.Contains(section, "Found on HuggingFace") || !strings.Contains(section, "Install and run") {
		t.Errorf("popover should surface the HF match with Install-and-run:\n%s", section)
	}
}

// TestHFMatchSectionRendering: the Fix popover renders an eligible HF match with an
// enabled Install-and-run posting to download-and-run; an ineligible (gated) match
// renders an "Open on HuggingFace" link and NO download-and-run POST. Untrusted repo
// strings are escaped.
func TestHFMatchSectionRendering(t *testing.T) {
	mm := comfy.MissingModel{Filename: "bbox/face_yolov9c.pt"}

	eligible := renderString(t, hfMatchSection(mm, curatedMatch([]byte("x")), true, 7, "csrf-tok"))
	if !strings.Contains(eligible, "Install and run") || !strings.Contains(eligible, "/workflows/7/download-and-run") {
		t.Errorf("eligible HF section missing enabled Install-and-run:\n%s", eligible)
	}
	if strings.Contains(eligible, "Open on HuggingFace") {
		t.Errorf("eligible HF section should not show the link-only degrade:\n%s", eligible)
	}
	if !strings.Contains(eligible, "Bingsu/adetailer/face_yolov9c.pt") {
		t.Errorf("eligible HF section should show the repo/path:\n%s", eligible)
	}

	gated := curatedMatch([]byte("x"))
	gated.Gated = true
	linkOnly := renderString(t, hfMatchSection(mm, gated, false, 7, "csrf-tok"))
	if strings.Contains(linkOnly, "/download-and-run") {
		t.Errorf("link-only HF section must NOT POST download-and-run:\n%s", linkOnly)
	}
	if !strings.Contains(linkOnly, "Open on HuggingFace") || !strings.Contains(linkOnly, `href="https://huggingface.co/Bingsu/adetailer"`) {
		t.Errorf("link-only HF section should show a scheme-validated HF link:\n%s", linkOnly)
	}
	if !strings.Contains(linkOnly, "gated") {
		t.Errorf("gated section should say it is gated:\n%s", linkOnly)
	}

	// Untrusted repo name must be HTML-escaped.
	xss := curatedMatch([]byte("x"))
	xss.Repo = `evil"<script>`
	xss.Subdir = "" // force link-only
	out := renderString(t, hfMatchSection(mm, xss, false, 7, "csrf-tok"))
	if strings.Contains(out, "<script>") {
		t.Errorf("untrusted repo name must be escaped:\n%s", out)
	}
}
