package web

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
	"github.com/ZacxDev/civitai-manager/internal/comfy"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// twoMissingSnapshot is the failure state four independent persona walkthroughs
// flagged: a settled run blocked by TWO un-installed model files.
func twoMissingSnapshot() runSnapshot {
	return runSnapshot{
		Started: true, WorkflowID: 7, Seq: 3, Phase: runPhaseFailed,
		Message: "Preflight failed — this workflow references nodes or models that are not installed.",
		Preflight: &comfy.PreflightReport{MissingModels: []string{
			"dreamshaperXL-MISSING.safetensors", "detailer-MISSING.safetensors"}},
		MissingModels: []comfy.MissingModel{
			{Filename: "dreamshaperXL-MISSING.safetensors", Query: "dreamshaper XL", CivitaiType: "Checkpoint"},
			{Filename: "detailer-MISSING.safetensors", Query: "detailer", CivitaiType: "Checkpoint"},
		},
		MissingResolved: map[string]missingResolution{},
		LibMeta:         map[string]store.LocalModelMeta{},
	}
}

// TestRunFailureLeadsWithSummaryThenOnePrimaryAction pins the whole information
// hierarchy of the missing-models failure state, which is the actual deliverable
// here: a plain-language summary FIRST, then exactly ONE primary recovery action for
// the whole failure, then the per-file secondary path, with the raw engine sentence
// demoted into a disclosure.
func TestRunFailureLeadsWithSummaryThenOnePrimaryAction(t *testing.T) {
	body := renderString(t, runStatusFragment(twoMissingSnapshot(), 7, "tok", true, NSFWBlur))

	// 1. Plain-language headline + lead naming the count in user terms.
	for _, want := range []string{
		"Run failed — 2 model files missing",
		"Nothing is broken",
		"2 model files that are not installed in ComfyUI yet",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("failure state missing plain-language copy %q:\n%s", want, body)
		}
	}

	// 2. Exactly ONE primary recovery action for the whole failure.
	if n := strings.Count(body, "/workflows/7/install-missing-and-run"); n != 1 {
		t.Errorf("want exactly 1 batch-install action, got %d:\n%s", n, body)
	}
	if !strings.Contains(body, "Install 2 missing model files and run") {
		t.Errorf("primary action label missing:\n%s", body)
	}
	// It carries BOTH filenames + their types, and the CSRF token.
	for _, want := range []string{
		`name="missing_filename" value="dreamshaperXL-MISSING.safetensors"`,
		`name="missing_filename" value="detailer-MISSING.safetensors"`,
		`name="missing_type" value="Checkpoint"`,
		`name="csrf_token" value="tok"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("primary action missing field %q:\n%s", want, body)
		}
	}

	// 3. The primary action comes BEFORE the per-file rows (hierarchy, not just
	// presence) — the reported confusion was "no clear primary action or next step".
	iPrimary := strings.Index(body, "install-missing-and-run")
	iRows := strings.Index(body, "Missing model files")
	if iPrimary < 0 || iRows < 0 || iPrimary > iRows {
		t.Errorf("primary action must render before the per-file list (primary=%d rows=%d)", iPrimary, iRows)
	}

	// 4. Per-file buttons say what they DO (they used to be two identical "Fix"es).
	if strings.Contains(body, ">Fix<") {
		t.Errorf("bare \"Fix\" label is back:\n%s", body)
	}
	if n := strings.Count(body, "Choose a model…"); n != 2 {
		t.Errorf("want a descriptive per-file label on each of the 2 rows, got %d:\n%s", n, body)
	}
	if !strings.Contains(body, `aria-label="Choose a model for detailer-MISSING.safetensors"`) {
		t.Errorf("per-file control needs a filename-specific accessible name:\n%s", body)
	}

	// 5. The raw engine sentence is SUBORDINATED into the disclosure, not the lead.
	if !strings.Contains(body, "Technical details") {
		t.Errorf("technical detail disclosure missing:\n%s", body)
	}
	iSummary := strings.Index(body, "Nothing is broken")
	iRaw := strings.Index(body, "Preflight failed — this workflow references")
	iDetails := strings.Index(body, "Technical details")
	if iRaw < 0 || iSummary > iRaw || iDetails > iRaw {
		t.Errorf("raw preflight sentence must sit inside the trailing disclosure (summary=%d details=%d raw=%d)",
			iSummary, iDetails, iRaw)
	}

	// 6. The failure is distinguishable by SHAPE, not tint alone — and the glyph is
	// hidden from assistive tech (role=alert + the title already say it).
	if !strings.Contains(body, `aria-hidden="true"`) || !strings.Contains(body, "⚠") {
		t.Errorf("failure state needs a non-color-only marker:\n%s", body)
	}
	if !strings.Contains(body, `role="alert"`) || !strings.Contains(body, `data-color="error"`) {
		t.Errorf("failure state lost its alert semantics:\n%s", body)
	}
}

// TestRunFailureSingularCopy: the generated copy has to read correctly for one file.
func TestRunFailureSingularCopy(t *testing.T) {
	snap := twoMissingSnapshot()
	snap.Preflight = &comfy.PreflightReport{MissingModels: []string{"only-MISSING.safetensors"}}
	snap.MissingModels = snap.MissingModels[:1]
	body := renderString(t, runStatusFragment(snap, 7, "tok", true, NSFWBlur))

	for _, want := range []string{
		"Run failed — 1 model file missing",
		"1 model file that is not installed",
		"Install it and it should run",
		"Install 1 missing model file and run",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("singular copy missing %q:\n%s", want, body)
		}
	}
}

// TestRunFailurePrimaryActionDisabledWhenIneligible: a server that cannot install
// files must still SHOW the action, disabled, with the reason — never a silent
// omission, and never a POST target.
func TestRunFailurePrimaryActionDisabledWhenIneligible(t *testing.T) {
	body := renderString(t, runStatusFragment(twoMissingSnapshot(), 7, "tok", false, NSFWBlur))

	if strings.Contains(body, "install-missing-and-run") {
		t.Errorf("ineligible failure state must not POST the batch install:\n%s", body)
	}
	if !strings.Contains(body, "Install 2 missing model files and run") || !strings.Contains(body, "disabled") {
		t.Errorf("expected a disabled primary action:\n%s", body)
	}
	if !strings.Contains(body, "comfy_model_path") {
		t.Errorf("disabled primary action must explain itself:\n%s", body)
	}
}

// TestRunFailureNodeAndOptionCopy covers the other preflight categories' summaries
// (they share the same lead/title machinery).
func TestRunFailureNodeAndOptionCopy(t *testing.T) {
	for name, tc := range map[string]struct {
		report *comfy.PreflightReport
		want   []string
	}{
		"nodes only": {
			&comfy.PreflightReport{MissingNodes: []string{"CR Float To Integer"}},
			[]string{"Run failed — 1 custom node missing", "1 custom node that is not installed"},
		},
		"models and nodes": {
			&comfy.PreflightReport{MissingModels: []string{"a.safetensors"}, MissingNodes: []string{"N1", "N2"}},
			[]string{"Run failed — 1 model file and 2 custom nodes are missing"},
		},
		"bad options only": {
			&comfy.PreflightReport{BadOptions: []comfy.BadOption{
				{ClassType: "X", InputName: "model_name", Current: "a", Choices: []string{"b"}}}},
			[]string{"Run failed — some saved settings no longer exist", "no longer exist on your installed nodes"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			snap := runSnapshot{Started: true, WorkflowID: 7, Phase: runPhaseFailed,
				Message: "Preflight failed.", Preflight: tc.report}
			body := renderString(t, runStatusFragment(snap, 7, "tok", true, NSFWBlur))
			for _, want := range tc.want {
				if !strings.Contains(body, want) {
					t.Errorf("missing %q:\n%s", want, body)
				}
			}
		})
	}
}

// batchDownloader is a precise per-URL fake: it serves bytes for the URLs it knows
// and fails the URLs listed in failURLs, so a MID-BATCH failure is reproducible.
// (fakeDownloader deliberately falls back to "any canned body" for an unknown URL,
// which cannot express "this one file fails".)
type batchDownloader struct {
	bodies   map[string][]byte
	failURLs map[string]bool
	calls    []string
}

func (d *batchDownloader) DownloadFile(_ context.Context, fileURL string) (*http.Response, error) {
	d.calls = append(d.calls, fileURL)
	if d.failURLs[fileURL] {
		return nil, errors.New("simulated transport failure")
	}
	body, ok := d.bodies[fileURL]
	if !ok {
		return nil, fmt.Errorf("no canned body for %s", fileURL)
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Body:       io.NopCloser(bytes.NewReader(body)),
		Header:     make(http.Header),
	}, nil
}

// twoFileSearchRaw is a models-list body containing one model per named file, so
// filename-only resolution finds an EXACT basename match for each.
func twoFileSearchRaw(t *testing.T, files map[string]string) []byte {
	t.Helper()
	items := make([]any, 0, len(files))
	i := 1
	for name, dlURL := range files {
		items = append(items, map[string]any{
			"id": i, "name": "Model " + name, "type": "Checkpoint",
			"modelVersions": []any{map[string]any{"id": 10 + i, "files": []any{
				map[string]any{"name": name, "downloadUrl": dlURL, "sizeKB": 8, "primary": true},
			}}},
		})
		i++
	}
	return searchRawJSON(t, items)
}

// newBatchInstallServer wires the batch-install flow: a reader resolving both
// filenames, the per-URL downloader, a loopback comfy_url and a writable
// comfy_model_path (returned).
func newBatchInstallServer(t *testing.T, files map[string]string, failURLs map[string]bool) (*Server, *batchDownloader, string) {
	t.Helper()
	reader := dlRunReader{searchRaw: twoFileSearchRaw(t, files)}
	srv, _, comfyModels := newDownloadServer(t, reader, "", nil)
	dl := &batchDownloader{bodies: map[string][]byte{}, failURLs: failURLs}
	for name, u := range files {
		dl.bodies[u] = []byte("WEIGHTS:" + name)
	}
	srv.downloaderFn = func() civitai.Downloader { return dl }
	return srv, dl, comfyModels
}

const twoMissingGraph = `{"4":{"class_type":"CheckpointLoaderSimple","inputs":{"ckpt_name":"alpha-MISSING.safetensors"}},` +
	`"5":{"class_type":"CheckpointLoaderSimple","inputs":{"ckpt_name":"beta-MISSING.safetensors"}}}`

func installMissingForm(names ...string) url.Values {
	v := url.Values{}
	for _, n := range names {
		v.Add("missing_filename", n)
		v.Add("missing_type", "Checkpoint")
	}
	return v
}

// TestInstallMissingAndRunInstallsAllThenRuns is the happy path: both files resolve,
// both are written into comfy_model_path/checkpoints, and the ORIGINAL workflow runs
// exactly once (no substitution — the referenced names now exist on disk).
func TestInstallMissingAndRunInstallsAllThenRuns(t *testing.T) {
	files := map[string]string{
		"alpha-MISSING.safetensors": "https://dl.example/alpha",
		"beta-MISSING.safetensors":  "https://dl.example/beta",
	}
	srv, dl, comfyModels := newBatchInstallServer(t, files, nil)
	rr := &runRecorder{}
	srv.runFn = rr.fn()
	wfID := seedWorkflow(t, srv, store.WorkflowFormatAPI, twoMissingGraph)

	rec := post(t, srv, "/workflows/"+wfID+"/install-missing-and-run",
		installMissingForm("alpha-MISSING.safetensors", "beta-MISSING.safetensors"), true)
	if rec.Code != http.StatusOK {
		t.Fatalf("install-missing-and-run = %d (%s)", rec.Code, rec.Body.String())
	}
	pollRunUntilDone(t, srv, wfID)

	for name := range files {
		dest := filepath.Join(comfyModels, "checkpoints", name)
		got, err := os.ReadFile(dest)
		if err != nil {
			t.Fatalf("%s not installed at %s: %v", name, dest, err)
		}
		if string(got) != "WEIGHTS:"+name {
			t.Errorf("%s content = %q", name, got)
		}
	}
	if len(dl.calls) != 2 {
		t.Errorf("downloader called %d times, want 2 (%v)", len(dl.calls), dl.calls)
	}
	rr.mu.Lock()
	defer rr.mu.Unlock()
	if rr.calls != 1 {
		t.Fatalf("runFn called %d times, want 1 (ONE run after ALL installs)", rr.calls)
	}
	if len(rr.opts[0].Substitute) != 0 {
		t.Errorf("batch install must run the ORIGINAL graph, got substitutes %v", rr.opts[0].Substitute)
	}
}

// TestInstallMissingAndRunDeclinesWhenOneCannotBeMatched: resolution is
// ALL-OR-NOTHING. One unmatched file means NOTHING is downloaded, no run is started,
// and the response names the file that failed — the honest report, not a silent
// half-install that leaves the run failing anyway.
func TestInstallMissingAndRunDeclinesWhenOneCannotBeMatched(t *testing.T) {
	// Only alpha exists on the (fake) CivitAI; beta resolves to nothing.
	srv, dl, comfyModels := newBatchInstallServer(t,
		map[string]string{"alpha-MISSING.safetensors": "https://dl.example/alpha"}, nil)
	rr := &runRecorder{}
	srv.runFn = rr.fn()
	wfID := seedWorkflow(t, srv, store.WorkflowFormatAPI, twoMissingGraph)

	// Reach the real state this action is offered from: a SETTLED run whose preflight
	// reported both files missing. The declined response replaces that whole panel, so
	// the panel has to exist for the assertion below to mean anything.
	srv.runFn = func(context.Context, *store.Workflow, runUpdater, runOptions) (*runResult, error) {
		return &runResult{
			Preflight: &comfy.PreflightReport{MissingModels: []string{
				"alpha-MISSING.safetensors", "beta-MISSING.safetensors"}},
			MissingModels: []comfy.MissingModel{
				{Filename: "alpha-MISSING.safetensors", Query: "alpha", CivitaiType: "Checkpoint"},
				{Filename: "beta-MISSING.safetensors", Query: "beta", CivitaiType: "Checkpoint"},
			},
		}, nil
	}
	if rec := post(t, srv, "/workflows/"+wfID+"/run", nil, true); rec.Code != http.StatusOK {
		t.Fatalf("seed run = %d", rec.Code)
	}
	pollRunUntilDone(t, srv, wfID)
	srv.runFn = rr.fn()

	rec := post(t, srv, "/workflows/"+wfID+"/install-missing-and-run",
		installMissingForm("alpha-MISSING.safetensors", "beta-MISSING.safetensors"), true)
	if rec.Code != http.StatusOK {
		t.Fatalf("install-missing-and-run = %d", rec.Code)
	}
	body := rec.Body.String()

	for _, want := range []string{
		"Nothing was downloaded",
		"1 of 2 files could not be matched",
		"beta-MISSING.safetensors",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("declined response missing %q:\n%s", want, body)
		}
	}
	// The per-file fallback the message points at must still be on screen (this
	// response replaces the whole #run-status container).
	if !strings.Contains(body, "Missing model files") {
		t.Errorf("declined response deleted the per-file panel it refers to:\n%s", body)
	}
	if len(dl.calls) != 0 {
		t.Errorf("all-or-nothing violated: downloaded %v", dl.calls)
	}
	if _, err := os.Stat(filepath.Join(comfyModels, "checkpoints", "alpha-MISSING.safetensors")); !os.IsNotExist(err) {
		t.Errorf("all-or-nothing violated: alpha was written (err=%v)", err)
	}
	rr.mu.Lock()
	defer rr.mu.Unlock()
	if rr.calls != 0 {
		t.Errorf("no run may start when nothing was installed, got %d calls", rr.calls)
	}
}

// TestInstallMissingAndRunPartialDownloadReportsHonestly: both files resolve, the
// SECOND download fails. The run must NOT proceed, and the failure has to say how
// many files did land — those bytes are on disk permanently.
func TestInstallMissingAndRunPartialDownloadReportsHonestly(t *testing.T) {
	files := map[string]string{
		"alpha-MISSING.safetensors": "https://dl.example/alpha",
		"beta-MISSING.safetensors":  "https://dl.example/beta",
	}
	srv, dl, _ := newBatchInstallServer(t, files, map[string]bool{"https://dl.example/beta": true})
	rr := &runRecorder{}
	srv.runFn = rr.fn()
	wfID := seedWorkflow(t, srv, store.WorkflowFormatAPI, twoMissingGraph)

	// Order the batch so the failing file is second.
	rec := post(t, srv, "/workflows/"+wfID+"/install-missing-and-run",
		installMissingForm("alpha-MISSING.safetensors", "beta-MISSING.safetensors"), true)
	if rec.Code != http.StatusOK {
		t.Fatalf("install-missing-and-run = %d", rec.Code)
	}
	body := pollRunUntilDone(t, srv, wfID)

	if !strings.Contains(body, "installed 1 of 2 model files, then failed") {
		t.Errorf("partial failure must report how many files landed:\n%s", body)
	}
	if len(dl.calls) != 2 {
		t.Errorf("downloader calls = %v, want alpha then beta", dl.calls)
	}
	rr.mu.Lock()
	defer rr.mu.Unlock()
	if rr.calls != 0 {
		t.Errorf("a failed install must not run the workflow, got %d calls", rr.calls)
	}
}

// TestInstallMissingAndRunAlreadyInstalledSaysSo: every requested file is already on
// disk → run, and say plainly that nothing was downloaded (the same honesty rule
// alreadyInstalledNote enforces for the single-file path).
func TestInstallMissingAndRunAlreadyInstalledSaysSo(t *testing.T) {
	files := map[string]string{"alpha-MISSING.safetensors": "https://dl.example/alpha"}
	srv, dl, comfyModels := newBatchInstallServer(t, files, nil)
	rr := &runRecorder{}
	release := rr.hold()
	defer release()
	srv.runFn = rr.fn()
	wfID := seedWorkflow(t, srv, store.WorkflowFormatAPI, twoMissingGraph)

	ckpts := filepath.Join(comfyModels, "checkpoints")
	if err := os.MkdirAll(ckpts, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ckpts, "alpha-MISSING.safetensors"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	rec := post(t, srv, "/workflows/"+wfID+"/install-missing-and-run",
		installMissingForm("alpha-MISSING.safetensors"), true)
	if rec.Code != http.StatusOK {
		t.Fatalf("install-missing-and-run = %d", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, "nothing was downloaded") {
		t.Errorf("already-installed batch must say nothing was downloaded:\n%s", body)
	}
	if len(dl.calls) != 0 {
		t.Errorf("already-installed batch must not download: %v", dl.calls)
	}
	release()
	pollRunUntilDone(t, srv, wfID)
}

// TestInstallMissingAndRunRefusesUnreferencedFile: missing_filename is free-form
// input that drives a real download + filesystem write, so it must be bound to the
// workflow that names it (the contract the single-file endpoint documents).
func TestInstallMissingAndRunRefusesUnreferencedFile(t *testing.T) {
	srv, dl, _ := newBatchInstallServer(t,
		map[string]string{"alpha-MISSING.safetensors": "https://dl.example/alpha"}, nil)
	wfID := seedWorkflow(t, srv, store.WorkflowFormatAPI, twoMissingGraph)

	rec := post(t, srv, "/workflows/"+wfID+"/install-missing-and-run",
		installMissingForm("alpha-MISSING.safetensors", "not-in-this-workflow.safetensors"), true)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unreferenced filename = %d, want 400", rec.Code)
	}
	if len(dl.calls) != 0 {
		t.Errorf("refused request must not download: %v", dl.calls)
	}
}

// TestInstallMissingAndRunIneligibleWritesNothing: without comfy_model_path the
// endpoint must decline (and explain), never attempt a write.
func TestInstallMissingAndRunIneligibleWritesNothing(t *testing.T) {
	srv, dl, _ := newBatchInstallServer(t,
		map[string]string{"alpha-MISSING.safetensors": "https://dl.example/alpha"}, nil)
	srv.cfg.ComfyModelPath = "" // not eligible
	wfID := seedWorkflow(t, srv, store.WorkflowFormatAPI, twoMissingGraph)

	rec := post(t, srv, "/workflows/"+wfID+"/install-missing-and-run",
		installMissingForm("alpha-MISSING.safetensors"), true)
	if rec.Code != http.StatusOK {
		t.Fatalf("ineligible = %d", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, "Nothing was downloaded") ||
		!strings.Contains(body, "comfy_model_path") {
		t.Errorf("ineligible response must explain itself:\n%s", rec.Body.String())
	}
	if len(dl.calls) != 0 {
		t.Errorf("ineligible request must not download: %v", dl.calls)
	}
}

// TestCloudOffGivesARealNextStep: "enable comfy_cloud in your config" is a dead end
// without saying where that config is — the state must link the docs.
func TestCloudOffGivesARealNextStep(t *testing.T) {
	body := renderString(t, cloudPanelFragment(cloudPanelView{wfID: 7, enabled: false}, "tok"))

	for _, want := range []string{
		"Cloud run is off",
		"comfy_cloud: true",
		"docs/configuration.md",
		`target="_blank"`,
		`rel="noopener noreferrer"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("cloud-off state missing %q:\n%s", want, body)
		}
	}
	// Self-hosted, no paid tier: the state must not grow pricing/upsell copy.
	for _, forbidden := range []string{"upgrade", "Upgrade", "per month", "pricing"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("cloud-off state must not carry upsell copy (%q):\n%s", forbidden, body)
		}
	}
}
