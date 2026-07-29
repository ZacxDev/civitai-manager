package web

import (
	"context"
	"encoding/json"
	"html"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// The scenario these tests pin is the one that shipped broken: a workflow that
// references juggernautXL_v9Rundiffusion.safetensors. A live CivitAI search for the
// cleaned query returns several Juggernaut models and NOT ONE of their files is named
// exactly juggernautXL_v9Rundiffusion.safetensors (the closest are
// juggernautXL_v9Rundiffusionphoto2 and juggernautXL_v8Rundiffusion). The old primary
// CTA omitted model_id and asked the endpoint to re-resolve by filename, which found
// no exact basename → nothing downloaded → the SAME panel re-rendered → dead button.
const jugFile = "juggernautXL_v9Rundiffusion.safetensors"

// jugSearchRaw mirrors that live shape: four plausible models, none of whose file
// basenames equals the reference. Model 1 (the primary card) has a downloadable
// primary file under a DIFFERENT name.
func jugSearchRaw(t *testing.T) []byte {
	t.Helper()
	return searchRawJSON(t, []any{
		map[string]any{"id": 1, "name": "Juggernaut XL", "type": "Checkpoint",
			"modelVersions": []any{map[string]any{"id": 10, "files": []any{
				map[string]any{"name": "juggernautXL_ragnarok.safetensors", "downloadUrl": jugPrimaryURL, "sizeKB": 8, "primary": true},
			}}}},
		map[string]any{"id": 2, "name": "Juggernaut XL inpainting", "type": "Checkpoint",
			"modelVersions": []any{map[string]any{"id": 20, "files": []any{
				map[string]any{"name": "juggernautXL_versionXInpaint.safetensors", "downloadUrl": "https://dl.example/inpaint", "sizeKB": 8, "primary": true},
			}}}},
		map[string]any{"id": 3, "name": "Juggernaut XL Inpainting (Updated)", "type": "Checkpoint"},
		map[string]any{"id": 4, "name": "Juggernaut", "type": "Checkpoint"},
	})
}

const jugPrimaryURL = "https://dl.example/juggernaut-primary"

// jugReader serves that search body, and for GetModel serves the requested model's
// detail body (recording the ids asked for, so a download PROVES the card's model_id
// drove the resolution rather than a filename re-guess).
type jugReader struct {
	stubReader
	searchRaw []byte
	details   map[string][]byte
	mu        sync.Mutex
	getIDs    []string
	searches  int
}

func (r *jugReader) SearchModels(context.Context, url.Values) (*civitai.ModelSearchResult, error) {
	r.mu.Lock()
	r.searches++
	r.mu.Unlock()
	return &civitai.ModelSearchResult{
		Items: []civitai.ModelListItem{
			{ID: 1, Name: "Juggernaut XL", Type: "Checkpoint"},
			{ID: 2, Name: "Juggernaut XL inpainting", Type: "Checkpoint"},
			{ID: 3, Name: "Juggernaut XL Inpainting (Updated)", Type: "Checkpoint"},
			{ID: 4, Name: "Juggernaut", Type: "Checkpoint"},
		},
		Raw: r.searchRaw,
	}, nil
}

func (r *jugReader) GetModel(_ context.Context, id string) (*civitai.ModelDetail, []byte, error) {
	r.mu.Lock()
	r.getIDs = append(r.getIDs, id)
	r.mu.Unlock()
	return &civitai.ModelDetail{ID: 1, Name: "Juggernaut XL"}, r.details[id], nil
}

func (r *jugReader) modelIDs() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.getIDs))
	copy(out, r.getIDs)
	return out
}

// ctaRequest is one "Install and run" button reduced to the request it issues: an
// htmx button's hx-post + hx-vals ARE the POST, so driving them is the closest a
// browser-less test gets to the real click.
type ctaRequest struct {
	path string
	vals url.Values
}

// downloadAndRunCTAs extracts every download-and-run CTA from a rendered fragment in
// DOM order. It walks hx-post attributes and pairs each with the hx-vals that follows
// it inside the same button, so the test posts EXACTLY what the browser would.
func downloadAndRunCTAs(t *testing.T, body string) []ctaRequest {
	t.Helper()
	var out []ctaRequest
	rest := body
	for {
		i := strings.Index(rest, `hx-post="`)
		if i < 0 {
			return out
		}
		rest = rest[i+len(`hx-post="`):]
		end := strings.Index(rest, `"`)
		if end < 0 {
			return out
		}
		path := html.UnescapeString(rest[:end])
		rest = rest[end+1:]
		if !strings.HasSuffix(path, "/download-and-run") {
			continue
		}
		// hx-vals must belong to THIS button: it may not cross the next hx-post.
		v := strings.Index(rest, `hx-vals="`)
		if v < 0 {
			t.Fatalf("CTA %s has no hx-vals — it cannot carry CSRF or a target", path)
		}
		if nxt := strings.Index(rest, `hx-post="`); nxt >= 0 && nxt < v {
			t.Fatalf("CTA %s has no hx-vals before the next button — it posts an empty body", path)
		}
		valsRaw := rest[v+len(`hx-vals="`):]
		ve := strings.Index(valsRaw, `"`)
		if ve < 0 {
			t.Fatalf("unterminated hx-vals for %s", path)
		}
		var m map[string]string
		if err := json.Unmarshal([]byte(html.UnescapeString(valsRaw[:ve])), &m); err != nil {
			t.Fatalf("hx-vals for %s is not JSON: %v", path, err)
		}
		form := url.Values{}
		for k, val := range m {
			form.Set(k, val)
		}
		out = append(out, ctaRequest{path: path, vals: form})
	}
}

// newJugFixServer wires the Fix-popover flow around the juggernaut scenario: a
// preflight that fails on the missing checkpoint, a loopback ComfyUI, a writable
// comfy_model_path, and a fake downloader serving bytes for jugPrimaryURL.
func newJugFixServer(t *testing.T, reader civitai.Reader, body []byte) (*Server, *fakeDownloader, string) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	comfyModels := t.TempDir()
	srv := NewServer(st, reader, stubSubscriber{}, Config{
		BaseURL: "https://civitai.com", DefaultPollInterval: time.Hour,
		Addr: "127.0.0.1:8787", ComfyURL: "http://127.0.0.1:8188",
		ComfyModelPath: comfyModels,
	}, nil)
	base, cancel := context.WithCancel(context.Background())
	srv.SetBaseContext(base)
	t.Cleanup(cancel)
	srv.comfyClientFn = func() comfyClient { return &fakeComfy{info: mustObjectInfo(t, substituteObjectInfo)} }
	dl := &fakeDownloader{zips: map[string][]byte{jugPrimaryURL: body}}
	srv.downloaderFn = func() civitai.Downloader { return dl }
	return srv, dl, comfyModels
}

// jugPopover runs a workflow referencing the missing checkpoint and returns the
// terminal Fix popover markup (the panel the user is looking at before clicking).
func jugPopover(t *testing.T, srv *Server) (wfID, body string) {
	t.Helper()
	wfID = seedWorkflow(t, srv, store.WorkflowFormatAPI,
		`{"4":{"class_type":"CheckpointLoaderSimple","inputs":{"ckpt_name":"`+jugFile+`"}}}`)
	if rec := post(t, srv, "/workflows/"+wfID+"/run", nil, true); rec.Code != http.StatusOK {
		t.Fatalf("run = %d", rec.Code)
	}
	return wfID, pollRunUntilDone(t, srv, wfID)
}

// TestInstallAndRunCTAAlwaysCarriesModelID pins the invariant that broke: a CTA
// rendered on a model CARD must carry enough information to act on its own, i.e. the
// id of the model it is showing. A refactor that drops model_id from any card CTA
// (the primary is the one that historically did) fails here.
func TestInstallAndRunCTAAlwaysCarriesModelID(t *testing.T) {
	mm := fixTestModel()
	section := renderString(t, civitaiMatchSection(mm,
		missingResolution{Reached: true, Result: resolveResult("Primary Match", "Alt1", "Alt2", "Alt3")},
		7, "tok-csrf", true, NSFWBlur))

	ctas := downloadAndRunCTAs(t, section)
	if len(ctas) != fixAltCap+1 {
		t.Fatalf("got %d install CTAs, want %d (primary + %d alternates):\n%s", len(ctas), fixAltCap+1, fixAltCap, section)
	}
	for i, c := range ctas {
		if got := c.vals.Get("model_id"); got == "" || got == "0" {
			t.Errorf("CTA #%d carries model_id=%q — a card CTA MUST name the model it shows, "+
				"otherwise the endpoint re-guesses from the filename and dead-ends", i, got)
		}
		if c.vals.Get("csrf_token") == "" {
			t.Errorf("CTA #%d lost its CSRF token", i)
		}
		if c.vals.Get("filename") != mm.Filename {
			t.Errorf("CTA #%d filename = %q, want %q", i, c.vals.Get("filename"), mm.Filename)
		}
	}
	// The primary card is first in DOM order and must name the primary model (id 1).
	if got := ctas[0].vals.Get("model_id"); got != "1" {
		t.Errorf("primary CTA model_id = %q, want 1 (the model its card renders)", got)
	}
}

// TestPrimaryInstallCTAStartsDownload is the live-reproduced bug as a test: an
// ambiguous filename (no CivitAI file basename matches the reference), the REAL
// popover, and the REAL request the primary button issues. It must actually start a
// download+run — not re-render the panel.
func TestPrimaryInstallCTAStartsDownload(t *testing.T) {
	reader := &jugReader{
		searchRaw: jugSearchRaw(t),
		details: map[string][]byte{"1": []byte(
			`{"id":1,"modelVersions":[{"id":10,"files":[{"name":"juggernautXL_ragnarok.safetensors","downloadUrl":"` +
				jugPrimaryURL + `","sizeKB":8,"primary":true}]}]}`)},
	}
	srv, dl, comfyModels := newJugFixServer(t, reader, []byte("JUGGERNAUTWEIGHTS"))

	// Record the job phase observed at the download seam: the job must be IN the
	// downloading phase by the time bytes are fetched.
	var seamMu sync.Mutex
	var seamPhases []string
	var seamURLs []string
	realDownload := srv.downloadModelFile
	srv.downloadFn = func(ctx context.Context, pd pendingDownload, cb func(string)) error {
		seamMu.Lock()
		seamPhases = append(seamPhases, srv.runJobState().Phase)
		seamURLs = append(seamURLs, pd.URL)
		seamMu.Unlock()
		return realDownload(ctx, pd, cb)
	}

	// The popover comes from a REAL run whose preflight fails on the missing
	// checkpoint (runFn unstubbed), so the CTA under test is the one users see.
	wfID, panel := jugPopover(t, srv)
	// Only the post-install run is stubbed (the fake ComfyUI would fail preflight again).
	rr := &runRecorder{}
	srv.runFn = rr.fn()
	ctas := downloadAndRunCTAs(t, panel)
	if len(ctas) == 0 {
		t.Fatalf("popover rendered no Install-and-run CTA:\n%s", panel)
	}
	primary := ctas[0]
	if want := "/workflows/" + wfID + "/download-and-run"; primary.path != want {
		t.Fatalf("primary CTA posts to %q, want %q", primary.path, want)
	}

	// withCSRF=false on purpose: the token must come from the BUTTON's own hx-vals,
	// exactly as the browser would send it.
	rec := post(t, srv, primary.path, primary.vals, false)
	if rec.Code != http.StatusOK {
		t.Fatalf("primary Install-and-run = %d body=%s", rec.Code, rec.Body.String())
	}
	// The click must NOT answer with the resolver panel again.
	if strings.Contains(rec.Body.String(), "Search CivitAI") {
		t.Fatalf("primary CTA re-rendered the resolve panel instead of installing:\n%s", rec.Body.String())
	}
	pollRunUntilDone(t, srv, wfID)

	seamMu.Lock()
	phases, urls := append([]string(nil), seamPhases...), append([]string(nil), seamURLs...)
	seamMu.Unlock()
	if len(phases) != 1 {
		t.Fatalf("download seam hit %d times, want 1 (the click must start exactly one download)", len(phases))
	}
	if phases[0] != runPhaseDownloading {
		t.Errorf("job phase at the download seam = %q, want %q", phases[0], runPhaseDownloading)
	}
	if urls[0] != jugPrimaryURL {
		t.Errorf("downloaded %q, want the PRIMARY card's model file %q", urls[0], jugPrimaryURL)
	}
	if ids := reader.modelIDs(); len(ids) == 0 || ids[0] != "1" {
		t.Errorf("GetModel called with %v, want the primary card's model id 1", ids)
	}
	if dl.calls != 1 {
		t.Errorf("downloader called %d times, want 1", dl.calls)
	}
	// The bytes land under the reference name so the ORIGINAL graph resolves.
	dest := filepath.Join(comfyModels, "checkpoints", jugFile)
	if got, err := os.ReadFile(dest); err != nil || string(got) != "JUGGERNAUTWEIGHTS" {
		t.Fatalf("model not installed at %s (got %q, err %v)", dest, got, err)
	}
	rr.mu.Lock()
	defer rr.mu.Unlock()
	if rr.calls != 1 {
		t.Errorf("runFn called %d times, want 1 (install THEN run)", rr.calls)
	}
}

// TestResolveFallbackExplainsWhyAndDiffersFromPanel: when an install action really
// cannot resolve anything, the answer must SAY SO. Before the fix this response was
// byte-identical to the panel the user was already looking at.
func TestResolveFallbackExplainsWhyAndDiffersFromPanel(t *testing.T) {
	reader := &jugReader{searchRaw: jugSearchRaw(t)}
	srv, dl, _ := newJugFixServer(t, reader, []byte("X"))
	var ran int
	srv.runFn = func(context.Context, *store.Workflow, runUpdater, runOptions) (*runResult, error) {
		ran++
		return &runResult{}, nil
	}
	wfID := seedWorkflow(t, srv, store.WorkflowFormatAPI, `{"4":{"class_type":"X","inputs":{}}}`)

	// The pre-click panel: the same fragment renderer, no action taken.
	pre := get(t, srv, "/workflows/run/resolve-model?filename="+url.QueryEscape(jugFile)+"&type=Checkpoint")
	if pre.Code != http.StatusOK {
		t.Fatalf("resolve-model = %d", pre.Code)
	}

	// The click: no model_id (the HuggingFace-fallback / bad-option CTA shape), and
	// nothing resolves by filename alone.
	rec := post(t, srv, "/workflows/"+wfID+"/download-and-run", url.Values{
		"filename": {jugFile}, "type": {"Checkpoint"},
	}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("download-and-run = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, resolveReasonNoMatch) {
		t.Errorf("fallback must explain why nothing installed; want %q in:\n%s", resolveReasonNoMatch, body)
	}
	if body == pre.Body.String() {
		t.Error("the click answered with a byte-identical panel — indistinguishable from a dead button")
	}
	// Still a real fallback: the cards and the search link survive.
	if !strings.Contains(body, "Search CivitAI") || !strings.Contains(body, "Juggernaut XL") {
		t.Errorf("fallback lost its cards / search link:\n%s", body)
	}
	if dl.calls != 0 || ran != 0 {
		t.Errorf("nothing must be downloaded or run (downloads=%d runs=%d)", dl.calls, ran)
	}
}

// TestResolveFallbackChosenModelReason: a click that DID name a model but yielded no
// downloadable file gets its own, accurate reason — not the generic filename one.
func TestResolveFallbackChosenModelReason(t *testing.T) {
	reader := &jugReader{
		searchRaw: jugSearchRaw(t),
		details:   map[string][]byte{"1": []byte(`{"id":1,"modelVersions":[]}`)}, // no files
	}
	srv, dl, _ := newJugFixServer(t, reader, []byte("X"))
	srv.runFn = (&runRecorder{}).fn()
	wfID := seedWorkflow(t, srv, store.WorkflowFormatAPI, `{"4":{"class_type":"X","inputs":{}}}`)

	rec := post(t, srv, "/workflows/"+wfID+"/download-and-run", url.Values{
		"filename": {jugFile}, "type": {"Checkpoint"}, "model_id": {"1"},
	}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("download-and-run = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, resolveReasonChosenModel) {
		t.Errorf("want the chosen-model reason %q in:\n%s", resolveReasonChosenModel, body)
	}
	if strings.Contains(body, resolveReasonNoMatch) {
		t.Errorf("a chosen model must NOT get the filename-ambiguity reason:\n%s", body)
	}
	if dl.calls != 0 {
		t.Errorf("downloader called %d times, want 0", dl.calls)
	}
}

// TestResolveFallbackNotEligibleReason: the not-eligible degrade (no comfy_model_path
// / remote ComfyUI) also explains itself rather than silently re-rendering.
func TestResolveFallbackNotEligibleReason(t *testing.T) {
	reader := &jugReader{searchRaw: jugSearchRaw(t)}
	srv, dl, _ := newJugFixServer(t, reader, []byte("X"))
	srv.cfg.ComfyURL = "http://192.168.1.50:8188" // remote ComfyUI → not eligible
	srv.runFn = (&runRecorder{}).fn()
	wfID := seedWorkflow(t, srv, store.WorkflowFormatAPI, `{"4":{"class_type":"X","inputs":{}}}`)

	for _, path := range []string{"/download-and-run", "/install-option-and-run"} {
		form := url.Values{"filename": {jugFile}, "type": {"Checkpoint"},
			"install_filename": {jugFile}, "install_type": {"Checkpoint"}}
		rec := post(t, srv, "/workflows/"+wfID+path, form, true)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s = %d", path, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), resolveReasonNotEligible) {
			t.Errorf("%s must explain the ineligibility:\n%s", path, rec.Body.String())
		}
	}
	if dl.calls != 0 {
		t.Errorf("downloader called %d times, want 0", dl.calls)
	}
}

// TestInstallOptionAndRunFallbackExplainsWhy: the bad-option install shares the
// resolve-by-filename shape (it has no model card, so model_id=0 is correct there) —
// it must therefore never dead-end silently either.
func TestInstallOptionAndRunFallbackExplainsWhy(t *testing.T) {
	reader := &jugReader{searchRaw: jugSearchRaw(t)}
	srv, dl, _ := newJugFixServer(t, reader, []byte("X"))
	srv.runFn = (&runRecorder{}).fn()
	wfID := seedWorkflow(t, srv, store.WorkflowFormatAPI, `{"4":{"class_type":"X","inputs":{}}}`)

	rec := post(t, srv, "/workflows/"+wfID+"/install-option-and-run", url.Values{
		"install_filename": {jugFile}, "install_type": {"Checkpoint"},
	}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("install-option-and-run = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), resolveReasonNoMatch) {
		t.Errorf("install-option fallback must explain why nothing installed:\n%s", rec.Body.String())
	}
	if dl.calls != 0 {
		t.Errorf("downloader called %d times, want 0", dl.calls)
	}
}

// TestResolveModelGETCarriesNoReason: the FIRST display of the panel is not the
// result of an action, so it must stay reason-free (an unconditional warning line
// would cry wolf on every open).
func TestResolveModelGETCarriesNoReason(t *testing.T) {
	srv := newModelServer(t, &recordingSearchReader{result: resolveResult("Juggernaut XL")})
	rec := get(t, srv, "/workflows/run/resolve-model?filename="+url.QueryEscape(jugFile)+"&type=Checkpoint")
	if rec.Code != http.StatusOK {
		t.Fatalf("resolve-model = %d", rec.Code)
	}
	for _, reason := range []string{resolveReasonNoMatch, resolveReasonNotEligible, resolveReasonChosenModel} {
		if strings.Contains(rec.Body.String(), reason) {
			t.Errorf("first display must not carry an action reason (%q)", reason)
		}
	}
}
