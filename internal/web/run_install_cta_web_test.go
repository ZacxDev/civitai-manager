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

// jugGraph references jugFile, so the endpoint's workflow-binding check accepts it.
const jugGraph = `{"4":{"class_type":"CheckpointLoaderSimple","inputs":{"ckpt_name":"` + jugFile + `"}}}`

// jugRagnarok is the file Juggernaut XL's PRIMARY version actually ships — a different
// file from the one the workflow references, hence a substitution.
const jugRagnarok = "juggernautXL_ragnarok.safetensors"

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
	wfID = seedWorkflow(t, srv, store.WorkflowFormatAPI, jugGraph)
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

// downloadSeam wraps srv.downloadFn to record, per invocation, the job phase and the
// pendingDownload — so a test can assert a download really started (and with what)
// through the seam rather than inferring it from a 200.
type downloadSeam struct {
	mu     sync.Mutex
	phases []string
	pds    []pendingDownload
}

func (ds *downloadSeam) install(srv *Server) {
	real := srv.downloadModelFile
	srv.downloadFn = func(ctx context.Context, pd pendingDownload, cb func(string)) error {
		ds.mu.Lock()
		ds.phases = append(ds.phases, srv.runJobState().Phase)
		ds.pds = append(ds.pds, pd)
		ds.mu.Unlock()
		return real(ctx, pd, cb)
	}
}

func (ds *downloadSeam) count() int {
	ds.mu.Lock()
	defer ds.mu.Unlock()
	return len(ds.phases)
}

// assertNoJobStarted proves a click created NO download/run job. It is checked
// synchronously right after the POST on purpose: startDownloadAndRun/startRun publish
// the job under runMu BEFORE the handler returns, so "still not running" immediately
// after the response is a deterministic answer — waiting on the seam alone would race
// the download goroutine and could pass vacuously.
func assertNoJobStarted(t *testing.T, srv *Server, ds *downloadSeam) {
	t.Helper()
	st := srv.runJobState()
	if st.Running {
		t.Fatalf("a job is running after the click (phase %q) — nothing should have started", st.Phase)
	}
	if st.Phase == runPhaseDownloading {
		t.Fatalf("job entered the downloading phase — nothing should have been fetched")
	}
	// Belt: give any (wrongly) spawned goroutine a chance to reach the seam.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if ds.count() > 0 {
			t.Fatalf("a download started on this click — it must be confirmed first")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// jugSubstituteReader is the live-verified Juggernaut shape: the search finds the
// models, and model 1's detail body has NO file named jugFile — its primary version
// ships jugRagnarok instead.
func jugSubstituteReader(t *testing.T) *jugReader {
	t.Helper()
	return &jugReader{
		searchRaw: jugSearchRaw(t),
		details: map[string][]byte{"1": []byte(
			`{"id":1,"type":"Checkpoint","modelVersions":[{"id":10,"files":[{"name":"` + jugRagnarok +
				`","downloadUrl":"` + jugPrimaryURL + `","sizeKB":8,"primary":true}]}]}`)},
	}
}

// TestPrimaryInstallCTAOffersSubstitutionInsteadOfInstalling is the audited hazard:
// the primary card's model has no file named like the workflow's reference, so its
// primary version's file would be installed UNDER THAT NAME. That must be an OFFER —
// the first click downloads NOTHING and names both files.
func TestPrimaryInstallCTAOffersSubstitutionInsteadOfInstalling(t *testing.T) {
	reader := jugSubstituteReader(t)
	srv, dl, comfyModels := newJugFixServer(t, reader, []byte("RAGNAROKWEIGHTS"))
	seam := &downloadSeam{}
	seam.install(srv)

	wfID, panel := jugPopover(t, srv)
	rr := &runRecorder{}
	srv.runFn = rr.fn()
	ctas := downloadAndRunCTAs(t, panel)
	if len(ctas) == 0 {
		t.Fatalf("popover rendered no Install-and-run CTA:\n%s", panel)
	}
	primary := ctas[0]

	rec := post(t, srv, primary.path, primary.vals, false)
	if rec.Code != http.StatusOK {
		t.Fatalf("primary Install-and-run = %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	// NOTHING may have been fetched or written.
	assertNoJobStarted(t, srv, seam)
	if dl.calls != 0 {
		t.Errorf("downloader called %d times, want 0", dl.calls)
	}
	if _, err := os.Stat(filepath.Join(comfyModels, "checkpoints", jugFile)); err == nil {
		t.Error("a file was written for an unconfirmed substitution")
	}
	rr.mu.Lock()
	ran := rr.calls
	rr.mu.Unlock()
	if ran != 0 {
		t.Errorf("runFn called %d times, want 0", ran)
	}

	// The offer must name BOTH files concretely.
	for _, want := range []string{jugFile, jugRagnarok, "Nothing was downloaded"} {
		if !strings.Contains(body, want) {
			t.Errorf("substitution offer must name %q:\n%s", want, body)
		}
	}
	// And it must carry a CONFIRMING action — an explicit second click.
	confirms := downloadAndRunCTAs(t, body)
	if len(confirms) != 1 {
		t.Fatalf("offer must render exactly one confirming CTA, got %d:\n%s", len(confirms), body)
	}
	c := confirms[0]
	if c.vals.Get("confirm_substitute") != "1" {
		t.Errorf("confirming CTA must carry confirm_substitute=1, got %q", c.vals.Get("confirm_substitute"))
	}
	if c.vals.Get("model_id") != "1" || c.vals.Get("filename") != jugFile {
		t.Errorf("confirming CTA lost its target: %v", c.vals)
	}
	if c.vals.Get("csrf_token") == "" {
		t.Error("confirming CTA lost its CSRF token")
	}
	if want := "/workflows/" + wfID + "/download-and-run"; c.path != want {
		t.Errorf("confirming CTA posts to %q, want %q", c.path, want)
	}
}

// TestConfirmedSubstitutionInstallsAndNamesRealFile: the SECOND click installs, and
// every progress line names the file actually being fetched — never only the expected
// name.
func TestConfirmedSubstitutionInstallsAndNamesRealFile(t *testing.T) {
	reader := jugSubstituteReader(t)
	srv, dl, comfyModels := newJugFixServer(t, reader, []byte("RAGNAROKWEIGHTS"))
	seam := &downloadSeam{}
	seam.install(srv)

	wfID, panel := jugPopover(t, srv)
	rr := &runRecorder{}
	srv.runFn = rr.fn()
	primary := downloadAndRunCTAs(t, panel)[0]

	offer := post(t, srv, primary.path, primary.vals, false)
	confirm := downloadAndRunCTAs(t, offer.Body.String())[0]

	rec := post(t, srv, confirm.path, confirm.vals, false)
	if rec.Code != http.StatusOK {
		t.Fatalf("confirm = %d body=%s", rec.Code, rec.Body.String())
	}
	// The confirmation must NOT answer with the offer again.
	if strings.Contains(rec.Body.String(), "Search CivitAI") {
		t.Fatalf("confirmed substitution re-rendered the panel instead of installing:\n%s", rec.Body.String())
	}
	// The status text names BOTH files while the bytes stream.
	if !strings.Contains(rec.Body.String(), jugRagnarok) {
		t.Errorf("run status must name the REAL remote file %q:\n%s", jugRagnarok, rec.Body.String())
	}
	pollRunUntilDone(t, srv, wfID)

	seam.mu.Lock()
	phases, pds := append([]string(nil), seam.phases...), append([]pendingDownload(nil), seam.pds...)
	seam.mu.Unlock()
	if len(phases) != 1 {
		t.Fatalf("download seam hit %d times, want 1", len(phases))
	}
	if phases[0] != runPhaseDownloading {
		t.Errorf("job phase at the download seam = %q, want %q", phases[0], runPhaseDownloading)
	}
	if pds[0].URL != jugPrimaryURL {
		t.Errorf("downloaded %q, want %q", pds[0].URL, jugPrimaryURL)
	}
	// The progress label must name the real file AS the expected one.
	if got, want := pds[0].progressName(), jugRagnarok+" as "+jugFile; got != want {
		t.Errorf("progress label = %q, want %q", got, want)
	}
	if ids := reader.modelIDs(); len(ids) == 0 || ids[0] != "1" {
		t.Errorf("GetModel called with %v, want the primary card's model id 1", ids)
	}
	if dl.calls != 1 {
		t.Errorf("downloader called %d times, want 1", dl.calls)
	}
	// The bytes land under the REFERENCE name so the original graph resolves.
	dest := filepath.Join(comfyModels, "checkpoints", jugFile)
	if got, err := os.ReadFile(dest); err != nil || string(got) != "RAGNAROKWEIGHTS" {
		t.Fatalf("model not installed at %s (got %q, err %v)", dest, got, err)
	}
	rr.mu.Lock()
	defer rr.mu.Unlock()
	if rr.calls != 1 {
		t.Errorf("runFn called %d times, want 1 (install THEN run)", rr.calls)
	}
}

// TestExactMatchInstallsInOneClick is the regression guard for the case that is NOT a
// substitution: when the model really has the referenced file, the first click must
// download immediately — no confirmation friction, and the progress line names just
// the one file.
func TestExactMatchInstallsInOneClick(t *testing.T) {
	reader := &jugReader{
		searchRaw: jugSearchRaw(t),
		details: map[string][]byte{"1": []byte(
			`{"id":1,"type":"Checkpoint","modelVersions":[{"id":10,"files":[{"name":"` + jugFile +
				`","downloadUrl":"` + jugPrimaryURL + `","sizeKB":8,"primary":true}]}]}`)},
	}
	srv, dl, comfyModels := newJugFixServer(t, reader, []byte("EXACTWEIGHTS"))
	seam := &downloadSeam{}
	seam.install(srv)

	wfID, panel := jugPopover(t, srv)
	rr := &runRecorder{}
	srv.runFn = rr.fn()
	primary := downloadAndRunCTAs(t, panel)[0]

	rec := post(t, srv, primary.path, primary.vals, false)
	if rec.Code != http.StatusOK {
		t.Fatalf("install = %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "confirm_substitute") {
		t.Fatalf("an EXACT filename match must not ask for confirmation:\n%s", rec.Body.String())
	}
	pollRunUntilDone(t, srv, wfID)

	if seam.count() != 1 {
		t.Fatalf("download seam hit %d times, want 1 (exact match is one click)", seam.count())
	}
	seam.mu.Lock()
	label := seam.pds[0].progressName()
	seam.mu.Unlock()
	if label != jugFile {
		t.Errorf("progress label = %q, want just %q (nothing was substituted)", label, jugFile)
	}
	if dl.calls != 1 {
		t.Errorf("downloader called %d times, want 1", dl.calls)
	}
	if got, err := os.ReadFile(filepath.Join(comfyModels, "checkpoints", jugFile)); err != nil || string(got) != "EXACTWEIGHTS" {
		t.Fatalf("model not installed (got %q, err %v)", got, err)
	}
	rr.mu.Lock()
	defer rr.mu.Unlock()
	if rr.calls != 1 {
		t.Errorf("runFn called %d times, want 1", rr.calls)
	}
}

// TestInstallRefusesTypeMismatch: a model_id whose model is a LORA must never be
// written into checkpoints/ because the caller said type=Checkpoint.
func TestInstallRefusesTypeMismatch(t *testing.T) {
	reader := &jugReader{
		searchRaw: jugSearchRaw(t),
		details: map[string][]byte{"1": []byte(
			`{"id":1,"type":"LORA","modelVersions":[{"id":10,"files":[{"name":"` + jugFile +
				`","downloadUrl":"` + jugPrimaryURL + `","sizeKB":8,"primary":true}]}]}`)},
	}
	srv, dl, comfyModels := newJugFixServer(t, reader, []byte("LORAWEIGHTS"))
	seam := &downloadSeam{}
	seam.install(srv)
	srv.runFn = (&runRecorder{}).fn()
	wfID := seedWorkflow(t, srv, store.WorkflowFormatAPI, jugGraph)

	rec := post(t, srv, "/workflows/"+wfID+"/download-and-run", url.Values{
		"filename": {jugFile}, "type": {"Checkpoint"}, "model_id": {"1"},
	}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("download-and-run = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), resolveReasonWrongType) {
		t.Errorf("a type mismatch must be refused with its own reason:\n%s", rec.Body.String())
	}
	assertNoJobStarted(t, srv, seam)
	if dl.calls != 0 {
		t.Errorf("a LoRA must not be downloaded into checkpoints/ (dl=%d)", dl.calls)
	}
	if _, err := os.Stat(filepath.Join(comfyModels, "checkpoints", jugFile)); err == nil {
		t.Error("a mismatched-type model was written into checkpoints/")
	}
}

// TestInstallAllowsSameFolderTypes: LORA, LoCon and LyCORIS all live in loras/, and
// the resolver routinely pairs them — a workflow's lora_name input makes
// InferCivitaiType return "LORA", so a LoCon model's card posts type=LORA. Comparing
// raw type STRINGS hard-refuses that working card; the invariant is the destination
// FOLDER, so these must install.
func TestInstallAllowsSameFolderTypes(t *testing.T) {
	const loraFile = "someStyle_v1.safetensors"
	const loraGraph = `{"4":{"class_type":"LoraLoader","inputs":{"lora_name":"` + loraFile + `"}}}`

	for _, modelType := range []string{"LoCon", "LyCORIS", "LORA", "lora"} {
		t.Run(modelType, func(t *testing.T) {
			reader := &jugReader{
				searchRaw: jugSearchRaw(t),
				details: map[string][]byte{"1": []byte(
					`{"id":1,"type":"` + modelType + `","modelVersions":[{"id":10,"files":[{"name":"` + loraFile +
						`","downloadUrl":"` + jugPrimaryURL + `","sizeKB":8,"primary":true}]}]}`)},
			}
			srv, dl, comfyModels := newJugFixServer(t, reader, []byte("LORAWEIGHTS"))
			seam := &downloadSeam{}
			seam.install(srv)
			rr := &runRecorder{}
			srv.runFn = rr.fn()
			wfID := seedWorkflow(t, srv, store.WorkflowFormatAPI, loraGraph)

			rec := post(t, srv, "/workflows/"+wfID+"/download-and-run", url.Values{
				"filename": {loraFile}, "type": {"LORA"}, "model_id": {"1"},
			}, true)
			if rec.Code != http.StatusOK {
				t.Fatalf("download-and-run = %d", rec.Code)
			}
			if strings.Contains(rec.Body.String(), resolveReasonWrongType) {
				t.Fatalf("%s routes to loras/ exactly like LORA — it must NOT be refused:\n%s",
					modelType, rec.Body.String())
			}
			pollRunUntilDone(t, srv, wfID)

			if seam.count() != 1 || dl.calls != 1 {
				t.Fatalf("%s should install (seam=%d dl=%d)", modelType, seam.count(), dl.calls)
			}
			if got, err := os.ReadFile(filepath.Join(comfyModels, "loras", loraFile)); err != nil || string(got) != "LORAWEIGHTS" {
				t.Fatalf("%s not installed into loras/ (got %q, err %v)", modelType, got, err)
			}
		})
	}
}

// TestTypeDestinationMismatch pins the predicate directly: same folder → no mismatch,
// different folder → mismatch, and every "cannot tell" case concedes.
func TestTypeDestinationMismatch(t *testing.T) {
	cases := []struct {
		modelType, requested string
		want                 bool
	}{
		{"LoCon", "LORA", false},   // both → loras/
		{"LyCORIS", "LORA", false}, // both → loras/
		{"LORA", "LoCon", false},
		{"lora", "LORA", false}, // case-insensitive
		{"Checkpoint", "Checkpoint", false},
		{"LORA", "Checkpoint", true}, // loras/ vs checkpoints/ — the real hazard
		{"Checkpoint", "VAE", true},
		{"TextualInversion", "LORA", true},
		{"", "Checkpoint", false},          // absent model type → concede
		{"Checkpoint", "", false},          // absent requested type → concede
		{"Workflows", "Checkpoint", false}, // unmappable model type → concede
	}
	for _, c := range cases {
		got, _, _ := typeDestinationMismatch(c.modelType, c.requested)
		if got != c.want {
			t.Errorf("typeDestinationMismatch(%q, %q) = %v, want %v", c.modelType, c.requested, got, c.want)
		}
	}
}

// shiftingReader resolves model 1 to fileA on the FIRST GetModel and fileB after —
// modelling CivitAI promoting a new primary version between the offer and the confirm.
type shiftingReader struct {
	stubReader
	searchRaw    []byte
	fileA, fileB string
	mu           sync.Mutex
	calls        int
}

func (r *shiftingReader) SearchModels(context.Context, url.Values) (*civitai.ModelSearchResult, error) {
	return &civitai.ModelSearchResult{
		Items: []civitai.ModelListItem{{ID: 1, Name: "Juggernaut XL", Type: "Checkpoint"}},
		Raw:   r.searchRaw,
	}, nil
}

func (r *shiftingReader) GetModel(context.Context, string) (*civitai.ModelDetail, []byte, error) {
	r.mu.Lock()
	r.calls++
	name := r.fileA
	if r.calls > 1 {
		name = r.fileB
	}
	r.mu.Unlock()
	return &civitai.ModelDetail{ID: 1, Name: "Juggernaut XL"}, []byte(
		`{"id":1,"type":"Checkpoint","modelVersions":[{"id":10,"files":[{"name":"` + name +
			`","downloadUrl":"` + jugPrimaryURL + `","sizeKB":8,"primary":true}]}]}`), nil
}

// TestConfirmationBindsToTheApprovedFile: the approval is for ONE specific file. If
// re-resolution yields a different one between the two clicks, the confirmed click
// must re-offer — never install a file the user never saw.
func TestConfirmationBindsToTheApprovedFile(t *testing.T) {
	const promoted = "juggernautXL_ragnarokV2.safetensors"
	reader := &shiftingReader{searchRaw: jugSearchRaw(t), fileA: jugRagnarok, fileB: promoted}
	srv, dl, comfyModels := newJugFixServer(t, reader, []byte("SHIFTED"))
	seam := &downloadSeam{}
	seam.install(srv)
	rr := &runRecorder{}
	srv.runFn = rr.fn()
	wfID := seedWorkflow(t, srv, store.WorkflowFormatAPI, jugGraph)

	// First click → an offer naming jugRagnarok.
	offer := post(t, srv, "/workflows/"+wfID+"/download-and-run", url.Values{
		"filename": {jugFile}, "type": {"Checkpoint"}, "model_id": {"1"},
	}, true)
	if offer.Code != http.StatusOK {
		t.Fatalf("offer = %d", offer.Code)
	}
	confirm := downloadAndRunCTAs(t, offer.Body.String())[0]
	if got := confirm.vals.Get("confirm_file"); got != jugRagnarok {
		t.Fatalf("confirm CTA must name the approved file, got %q", got)
	}

	// Second click carries that approval — but CivitAI now resolves to `promoted`.
	rec := post(t, srv, confirm.path, confirm.vals, false)
	if rec.Code != http.StatusOK {
		t.Fatalf("confirm = %d", rec.Code)
	}
	body := rec.Body.String()

	assertNoJobStarted(t, srv, seam)
	if dl.calls != 0 {
		t.Errorf("a file the user never approved was downloaded (dl=%d)", dl.calls)
	}
	if _, err := os.Stat(filepath.Join(comfyModels, "checkpoints", jugFile)); err == nil {
		t.Error("an unapproved substitution was written to disk")
	}
	// A FRESH offer naming the NEW file must come back.
	if !strings.Contains(body, promoted) {
		t.Errorf("expected a fresh offer naming %q:\n%s", promoted, body)
	}
	reoffer := downloadAndRunCTAs(t, body)
	if len(reoffer) != 1 {
		t.Fatalf("expected exactly one re-offer CTA, got %d", len(reoffer))
	}
	if got := reoffer[0].vals.Get("confirm_file"); got != promoted {
		t.Errorf("re-offer must approve the NEW file, got %q", got)
	}
}

// TestConfirmWithoutApprovedFileReOffers: a confirm_substitute=1 that names no file
// (e.g. a hand-rolled request, or a stale button from before this binding existed)
// must not install — the approval has to say WHICH file.
func TestConfirmWithoutApprovedFileReOffers(t *testing.T) {
	srv, dl, _ := newJugFixServer(t, jugSubstituteReader(t), []byte("X"))
	seam := &downloadSeam{}
	seam.install(srv)
	srv.runFn = (&runRecorder{}).fn()
	wfID := seedWorkflow(t, srv, store.WorkflowFormatAPI, jugGraph)

	for _, approved := range []string{"", "somethingElse.safetensors"} {
		form := url.Values{
			"filename": {jugFile}, "type": {"Checkpoint"}, "model_id": {"1"},
			"confirm_substitute": {"1"},
		}
		if approved != "" {
			form.Set("confirm_file", approved)
		}
		rec := post(t, srv, "/workflows/"+wfID+"/download-and-run", form, true)
		if rec.Code != http.StatusOK {
			t.Fatalf("confirm_file=%q → %d", approved, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), jugRagnarok) {
			t.Errorf("confirm_file=%q should re-offer naming %q:\n%s", approved, jugRagnarok, rec.Body.String())
		}
	}
	assertNoJobStarted(t, srv, seam)
	if dl.calls != 0 {
		t.Errorf("an unbound confirmation installed something (dl=%d)", dl.calls)
	}
}

// TestInstallRefusesFilenameNotInWorkflow: filename and model_id are free-form form
// fields; the target must belong to the workflow the request names, or an arbitrary
// (workflow, file, model) triple could be installed.
func TestInstallRefusesFilenameNotInWorkflow(t *testing.T) {
	reader := jugSubstituteReader(t)
	srv, dl, _ := newJugFixServer(t, reader, []byte("X"))
	seam := &downloadSeam{}
	seam.install(srv)
	srv.runFn = (&runRecorder{}).fn()
	// This workflow references jugFile — NOT the file the request will ask for.
	wfID := seedWorkflow(t, srv, store.WorkflowFormatAPI, jugGraph)

	for _, tc := range []struct{ path, field string }{
		{"/download-and-run", "filename"},
		{"/install-option-and-run", "install_filename"},
	} {
		form := url.Values{"type": {"Checkpoint"}, "install_type": {"Checkpoint"}}
		form.Set(tc.field, "somethingElse.safetensors")
		rec := post(t, srv, "/workflows/"+wfID+tc.path, form, true)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s with an unreferenced filename = %d, want 400", tc.path, rec.Code)
		}
	}
	assertNoJobStarted(t, srv, seam)
	if dl.calls != 0 {
		t.Errorf("nothing may be fetched for an unbound target (dl=%d)", dl.calls)
	}
}

// TestInstallRefusesMalformedModelID: a malformed model_id must not silently read as
// "no model chosen" — those take different resolution paths.
func TestInstallRefusesMalformedModelID(t *testing.T) {
	srv, dl, _ := newJugFixServer(t, jugSubstituteReader(t), []byte("X"))
	srv.runFn = (&runRecorder{}).fn()
	wfID := seedWorkflow(t, srv, store.WorkflowFormatAPI, jugGraph)

	for _, bad := range []string{"abc", "-4", "1e3"} {
		rec := post(t, srv, "/workflows/"+wfID+"/download-and-run", url.Values{
			"filename": {jugFile}, "type": {"Checkpoint"}, "model_id": {bad},
		}, true)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("model_id=%q = %d, want 400", bad, rec.Code)
		}
	}
	if dl.calls != 0 {
		t.Errorf("downloader called %d times, want 0", dl.calls)
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
	wfID := seedWorkflow(t, srv, store.WorkflowFormatAPI, jugGraph)

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
	wfID := seedWorkflow(t, srv, store.WorkflowFormatAPI, jugGraph)

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
	wfID := seedWorkflow(t, srv, store.WorkflowFormatAPI, jugGraph)

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
	wfID := seedWorkflow(t, srv, store.WorkflowFormatAPI, jugGraph)

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
