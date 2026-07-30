package web

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

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

// TestBatchInstallHintScopesItsGuaranteeToMatching pins the CORRECTNESS of the
// promise, which is the same class of defect this whole panel exists to remove: the
// all-or-nothing guarantee holds for RESOLUTION only, so the hint must not imply that
// a failed download leaves nothing behind (downloadBatchError exists precisely
// because it does).
func TestBatchInstallHintScopesItsGuaranteeToMatching(t *testing.T) {
	if !strings.Contains(batchInstallHint, "cannot be matched, nothing is downloaded") {
		t.Errorf("hint should keep the (true) resolution guarantee: %q", batchInstallHint)
	}
	for _, want := range []string{
		"If a download fails part-way",
		"stay on disk",
		"how many landed",
	} {
		if !strings.Contains(batchInstallHint, want) {
			t.Errorf("hint must disclose what a mid-download failure leaves behind (%q): %q", want, batchInstallHint)
		}
	}
	// The unqualified promise must not come back.
	if strings.Contains(batchInstallHint, "Nothing is downloaded if any of them cannot be matched") {
		t.Errorf("hint reverted to the unscoped guarantee: %q", batchInstallHint)
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
// omission, and never a POST target. AND the lead must not promise the install: "…
// Install them and it should run" is false when the button is greyed out.
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
	// The lead is GATED on the CTA being able to deliver.
	if strings.Contains(body, "Nothing is broken") || strings.Contains(body, "Install them and it should run") {
		t.Errorf("lead must not promise an install the disabled CTA cannot perform:\n%s", body)
	}
	if !strings.Contains(body, "cannot fetch them for you") {
		t.Errorf("lead must name the real next step when installing is unavailable:\n%s", body)
	}
}

// TestBatchInstallExcludesUnroutableFiles: a reference whose CivitAI type could not be
// inferred has no destination subfolder, so a batch install can only ever fail on it.
// It must be EXCLUDED from the batch (and named), never silently included so the user
// pays N round-trips for a guaranteed decline.
func TestBatchInstallExcludesUnroutableFiles(t *testing.T) {
	snap := twoMissingSnapshot()
	snap.Preflight = &comfy.PreflightReport{MissingModels: []string{
		"routable-MISSING.safetensors", "mystery-MISSING.bin"}}
	snap.MissingModels = []comfy.MissingModel{
		{Filename: "routable-MISSING.safetensors", Query: "routable", CivitaiType: "Checkpoint"},
		{Filename: "mystery-MISSING.bin", Query: "mystery", CivitaiType: ""}, // no inferred type
	}
	body := renderString(t, runStatusFragment(snap, 7, "tok", true, NSFWBlur))

	if !strings.Contains(body, "Install 1 of 2 missing model files and run") {
		t.Errorf("partial batch must say so in the label:\n%s", body)
	}
	if !strings.Contains(body, `value="routable-MISSING.safetensors"`) {
		t.Errorf("the routable file must ride in the form:\n%s", body)
	}
	if strings.Contains(body, `name="missing_filename" value="mystery-MISSING.bin"`) {
		t.Errorf("an unroutable file must NOT be submitted to the batch:\n%s", body)
	}
	for _, want := range []string{"cannot be installed in one click", "mystery-MISSING.bin"} {
		if !strings.Contains(body, want) {
			t.Errorf("the excluded file must be named (%q):\n%s", want, body)
		}
	}
}

// TestBatchInstallDisabledWhenNothingIsRoutable: when NO file can be routed, the CTA
// is disabled with the real reason and the lead drops its promise.
func TestBatchInstallDisabledWhenNothingIsRoutable(t *testing.T) {
	snap := twoMissingSnapshot()
	snap.Preflight = &comfy.PreflightReport{MissingModels: []string{"mystery-MISSING.bin"}}
	snap.MissingModels = []comfy.MissingModel{{Filename: "mystery-MISSING.bin", Query: "m", CivitaiType: ""}}
	body := renderString(t, runStatusFragment(snap, 7, "tok", true, NSFWBlur))

	if strings.Contains(body, "install-missing-and-run") {
		t.Errorf("a doomed batch must not be offered as a POST:\n%s", body)
	}
	if !strings.Contains(body, "cannot tell which ComfyUI folder") {
		t.Errorf("disabled CTA must give the routing reason:\n%s", body)
	}
	if strings.Contains(body, "Nothing is broken") {
		t.Errorf("lead must not promise a one-click install here:\n%s", body)
	}
}

// TestPlanBatchInstallDeDupesAndCaps: the same file referenced by two loaders is ONE
// install, and one click is bounded by maxBatchInstallFiles.
func TestPlanBatchInstallDeDupesAndCaps(t *testing.T) {
	dupes := []comfy.MissingModel{
		{Filename: "a.safetensors", CivitaiType: "Checkpoint"},
		{Filename: "sub/dir/a.safetensors", CivitaiType: "Checkpoint"}, // same basename
		{Filename: "b.safetensors", CivitaiType: "Checkpoint"},
	}
	p := planBatchInstall(dupes, true)
	if len(p.Installable) != 2 {
		t.Errorf("duplicate references must collapse: got %d installable %v", len(p.Installable), p.Installable)
	}

	many := make([]comfy.MissingModel, 0, maxBatchInstallFiles+3)
	for i := 0; i < maxBatchInstallFiles+3; i++ {
		many = append(many, comfy.MissingModel{
			Filename: fmt.Sprintf("m%02d.safetensors", i), CivitaiType: "Checkpoint"})
	}
	p = planBatchInstall(many, true)
	if len(p.Installable) != maxBatchInstallFiles || p.Overflow != 3 {
		t.Errorf("cap not applied: installable=%d overflow=%d", len(p.Installable), p.Overflow)
	}
	if !p.Available {
		t.Error("a capped batch is still available")
	}
	// A cap that silently drops files would be the same defect again.
	body := renderString(t, installAllMissingAction(p, len(many), 7, "tok"))
	if !strings.Contains(body, "left for a second click") {
		t.Errorf("the capped-out remainder must be disclosed:\n%s", body)
	}
}

// TestBatchJobBudgetScalesWithFileCount: the runaway backstop must cover N downloads
// PLUS the run. A one-file budget guarding a 4-file batch cancels mid-batch and
// manufactures the partial-install state this flow avoids.
func TestBatchJobBudgetScalesWithFileCount(t *testing.T) {
	if got := batchJobBudget(1); got != runJobBudget {
		t.Errorf("single-file budget changed: %v, want %v", got, runJobBudget)
	}
	if got := batchJobBudget(0); got != runJobBudget {
		t.Errorf("empty budget = %v, want %v", got, runJobBudget)
	}
	for _, n := range []int{2, 4, maxBatchInstallFiles} {
		want := runJobBudget + time.Duration(n-1)*downloadFileBudget
		if got := batchJobBudget(n); got != want {
			t.Errorf("batchJobBudget(%d) = %v, want %v", n, got, want)
		}
		if batchJobBudget(n) <= batchJobBudget(n-1) {
			t.Errorf("budget must grow with n at n=%d", n)
		}
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
//
// DownloadFile runs on the download GOROUTINE while assertions read from the test
// goroutine, so the call log is mutex-guarded and read only through calls().
type batchDownloader struct {
	bodies   map[string][]byte
	failURLs map[string]bool

	mu       sync.Mutex
	callLog  []string
	blockOn  string
	released chan struct{}
}

func (d *batchDownloader) DownloadFile(_ context.Context, fileURL string) (*http.Response, error) {
	d.mu.Lock()
	d.callLog = append(d.callLog, fileURL)
	block := d.blockOn == fileURL
	rel := d.released
	d.mu.Unlock()
	if block && rel != nil {
		<-rel
	}
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

// calls returns a copy of the call log under the mutex.
func (d *batchDownloader) calls() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.callLog...)
}

// hold makes the download of one URL park until the returned func is called, so a
// test can observe the IN-FLIGHT job state deterministically.
func (d *batchDownloader) hold(url string) (release func()) {
	d.mu.Lock()
	ch := make(chan struct{})
	d.blockOn, d.released = url, ch
	d.mu.Unlock()
	var once sync.Once
	return func() { once.Do(func() { close(ch) }) }
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

// seedFailedPreflightRun drives the workflow to the SETTLED missing-models failure the
// batch CTA is offered from, so a declined batch has a real panel to re-render and the
// run-job seq has a known value to compare against.
func seedFailedPreflightRun(t *testing.T, srv *Server, wfID string, missing ...string) int64 {
	t.Helper()
	models := make([]comfy.MissingModel, 0, len(missing))
	for _, m := range missing {
		models = append(models, comfy.MissingModel{Filename: m, Query: m, CivitaiType: "Checkpoint"})
	}
	prev := srv.runFn
	srv.runFn = func(context.Context, *store.Workflow, runUpdater, runOptions) (*runResult, error) {
		return &runResult{
			Preflight:     &comfy.PreflightReport{MissingModels: missing},
			MissingModels: models,
		}, nil
	}
	if rec := post(t, srv, "/workflows/"+wfID+"/run", nil, true); rec.Code != http.StatusOK {
		t.Fatalf("seed run = %d", rec.Code)
	}
	pollRunUntilDone(t, srv, wfID)
	srv.runFn = prev
	return srv.runJobState().Seq
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
	if n := len(dl.calls()); n != 2 {
		t.Errorf("downloader called %d times, want 2 (%v)", n, dl.calls())
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
// and the response names the file that failed.
//
// The load-bearing assertion is the RUN-JOB SEQ: startDownloadsAndRun publishes its
// job (and bumps runSeq) SYNCHRONOUSLY before returning, so an unchanged seq after
// the POST proves no batch was started — deterministically, with no goroutine to race.
// The filesystem/downloader assertions that follow are only sound BECAUSE of it (no
// job ⇒ no download goroutine), and both were verified to fire when the decline branch
// is deleted.
func TestInstallMissingAndRunDeclinesWhenOneCannotBeMatched(t *testing.T) {
	// Only alpha exists on the (fake) CivitAI; beta resolves to nothing.
	srv, dl, comfyModels := newBatchInstallServer(t,
		map[string]string{"alpha-MISSING.safetensors": "https://dl.example/alpha"}, nil)
	rr := &runRecorder{}
	srv.runFn = rr.fn()
	wfID := seedWorkflow(t, srv, store.WorkflowFormatAPI, twoMissingGraph)
	seqBefore := seedFailedPreflightRun(t, srv, wfID,
		"alpha-MISSING.safetensors", "beta-MISSING.safetensors")

	rec := post(t, srv, "/workflows/"+wfID+"/install-missing-and-run",
		installMissingForm("alpha-MISSING.safetensors", "beta-MISSING.safetensors"), true)
	if rec.Code != http.StatusOK {
		t.Fatalf("install-missing-and-run = %d", rec.Code)
	}
	body := rec.Body.String()

	// No job was started — the synchronous, race-free proof that nothing was written.
	snap := srv.runJobState()
	if snap.Seq != seqBefore {
		t.Fatalf("all-or-nothing violated: a job was started (seq %d → %d)", seqBefore, snap.Seq)
	}
	if snap.Running || snap.Phase != runPhaseFailed {
		t.Fatalf("declined batch changed the run state: running=%v phase=%q", snap.Running, snap.Phase)
	}

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
	if calls := dl.calls(); len(calls) != 0 {
		t.Errorf("all-or-nothing violated: downloaded %v", calls)
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

	rec := post(t, srv, "/workflows/"+wfID+"/install-missing-and-run",
		installMissingForm("alpha-MISSING.safetensors", "beta-MISSING.safetensors"), true)
	if rec.Code != http.StatusOK {
		t.Fatalf("install-missing-and-run = %d", rec.Code)
	}
	body := pollRunUntilDone(t, srv, wfID)

	if !strings.Contains(body, "installed 1 of 2 model files, then failed") {
		t.Errorf("partial failure must report how many files landed:\n%s", body)
	}
	if n := len(dl.calls()); n != 2 {
		t.Errorf("downloader calls = %v, want alpha then beta", dl.calls())
	}
	rr.mu.Lock()
	defer rr.mu.Unlock()
	if rr.calls != 0 {
		t.Errorf("a failed install must not run the workflow, got %d calls", rr.calls)
	}
}

// TestInstallMissingAndRunMixedAlreadyPresentSaysSo: with one of two files already on
// disk, the user must be TOLD — the count they asked about is 2, and reporting the
// remaining one as "(1/1)" with no mention of the other silently rewrites the request.
func TestInstallMissingAndRunMixedAlreadyPresentSaysSo(t *testing.T) {
	files := map[string]string{
		"alpha-MISSING.safetensors": "https://dl.example/alpha",
		"beta-MISSING.safetensors":  "https://dl.example/beta",
	}
	srv, dl, comfyModels := newBatchInstallServer(t, files, nil)
	rr := &runRecorder{}
	srv.runFn = rr.fn()
	wfID := seedWorkflow(t, srv, store.WorkflowFormatAPI, twoMissingGraph)

	// alpha is already installed; only beta has to be fetched.
	ckpts := filepath.Join(comfyModels, "checkpoints")
	if err := os.MkdirAll(ckpts, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ckpts, "alpha-MISSING.safetensors"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Park the remaining download so the in-flight status line is observable.
	release := dl.hold("https://dl.example/beta")
	defer release()

	rec := post(t, srv, "/workflows/"+wfID+"/install-missing-and-run",
		installMissingForm("alpha-MISSING.safetensors", "beta-MISSING.safetensors"), true)
	if rec.Code != http.StatusOK {
		t.Fatalf("install-missing-and-run = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "1 model file is already installed") {
		t.Errorf("mixed batch must disclose the already-installed file:\n%s", body)
	}
	if !strings.Contains(body, "preparing to install the remaining 1") {
		t.Errorf("mixed batch must name what is left to fetch:\n%s", body)
	}
	release()
	pollRunUntilDone(t, srv, wfID)

	// Only the missing file was fetched.
	if calls := dl.calls(); len(calls) != 1 || calls[0] != "https://dl.example/beta" {
		t.Errorf("mixed batch fetched %v, want only beta", dl.calls())
	}
}

// TestDownloadStepMessageCountsTheWholeSet pins the progress prefix directly: an
// already-present file still occupies its slot in the count.
func TestDownloadStepMessageCountsTheWholeSet(t *testing.T) {
	if got := downloadStepMessage(0, 1, 0, "Downloading x…"); got != "Downloading x…" {
		t.Errorf("lone download must stay unprefixed, got %q", got)
	}
	if got := downloadStepMessage(0, 2, 0, "d"); got != "(1/2) d" {
		t.Errorf("got %q, want (1/2) d", got)
	}
	if got := downloadStepMessage(0, 1, 1, "d"); got != "(2/2) d" {
		t.Errorf("an already-present file must be counted, got %q, want (2/2) d", got)
	}
	if got := downloadBatchError(0, 1, 1, errors.New("boom")); !strings.Contains(got.Error(), "installed 1 of 2") {
		t.Errorf("error must count the already-present file, got %q", got)
	}
	if got := downloadBatchError(0, 1, 0, errors.New("boom")); got.Error() != "boom" {
		t.Errorf("single-file error must be unwrapped, got %q", got)
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
	if n := len(dl.calls()); n != 0 {
		t.Errorf("already-installed batch must not download: %v", dl.calls())
	}
	release()
	pollRunUntilDone(t, srv, wfID)
}

// TestInstallMissingAndRunDroppedClickIsReported: the one-run-at-a-time guard silently
// discards a click that lands mid-run. The user paid N resolutions for it, so the
// response must SAY it was dropped instead of rendering the other job's panel as if
// the install had started.
func TestInstallMissingAndRunDroppedClickIsReported(t *testing.T) {
	files := map[string]string{"alpha-MISSING.safetensors": "https://dl.example/alpha"}
	srv, dl, _ := newBatchInstallServer(t, files, nil)
	rr := &runRecorder{}
	release := rr.hold() // park the first run so it stays "running"
	defer release()
	srv.runFn = rr.fn()
	wfID := seedWorkflow(t, srv, store.WorkflowFormatAPI, twoMissingGraph)

	if rec := post(t, srv, "/workflows/"+wfID+"/run", nil, true); rec.Code != http.StatusOK {
		t.Fatalf("first run = %d", rec.Code)
	}
	if snap := srv.runJobState(); !snap.Running {
		t.Fatalf("expected an in-flight run to contend with, got %+v", snap)
	}

	rec := post(t, srv, "/workflows/"+wfID+"/install-missing-and-run",
		installMissingForm("alpha-MISSING.safetensors"), true)
	if rec.Code != http.StatusOK {
		t.Fatalf("contended install = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "another run or download is already in progress") {
		t.Errorf("a dropped click must be reported:\n%s", body)
	}
	if n := len(dl.calls()); n != 0 {
		t.Errorf("a dropped click must not download: %v", dl.calls())
	}
	release()
	pollRunUntilDone(t, srv, wfID)
}

// TestInstallMissingAndRunEnforcesCSRF: the endpoint performs real network fetches and
// filesystem writes, so a request without the token must be REFUSED (not merely
// rendered with a token field in the form).
func TestInstallMissingAndRunEnforcesCSRF(t *testing.T) {
	srv, dl, _ := newBatchInstallServer(t,
		map[string]string{"alpha-MISSING.safetensors": "https://dl.example/alpha"}, nil)
	rr := &runRecorder{}
	srv.runFn = rr.fn()
	wfID := seedWorkflow(t, srv, store.WorkflowFormatAPI, twoMissingGraph)

	rec := post(t, srv, "/workflows/"+wfID+"/install-missing-and-run",
		installMissingForm("alpha-MISSING.safetensors"), false /* no CSRF */)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("no-CSRF install = %d, want 403", rec.Code)
	}
	if n := len(dl.calls()); n != 0 {
		t.Errorf("a CSRF-refused request must not download: %v", dl.calls())
	}
	if snap := srv.runJobState(); snap.Started {
		t.Errorf("a CSRF-refused request must not start a job: %+v", snap)
	}

	// A WRONG token is refused too (constant-time compare, not a presence check).
	form := installMissingForm("alpha-MISSING.safetensors")
	req := httptest.NewRequest(http.MethodPost, "/workflows/"+wfID+"/install-missing-and-run",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-CSRF-Token", "not-the-token")
	rec2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec2, req)
	if rec2.Code != http.StatusForbidden {
		t.Fatalf("wrong-CSRF install = %d, want 403", rec2.Code)
	}
	if n := len(dl.calls()); n != 0 {
		t.Errorf("a wrong-token request must not download: %v", dl.calls())
	}
}

// TestInstallMissingAndRunRefusesMalformedBatches covers the request-shape guards: an
// unreferenced filename (free-form input that drives a real download + filesystem
// write), an OFFSET missing_type array (which would route bytes into another file's
// folder), and an over-cap batch.
func TestInstallMissingAndRunRefusesMalformedBatches(t *testing.T) {
	for name, form := range map[string]url.Values{
		"unreferenced filename": installMissingForm(
			"alpha-MISSING.safetensors", "not-in-this-workflow.safetensors"),
		"offset type array": func() url.Values {
			v := url.Values{}
			v.Add("missing_filename", "alpha-MISSING.safetensors")
			v.Add("missing_filename", "beta-MISSING.safetensors")
			v.Add("missing_type", "Checkpoint") // one type for two files
			return v
		}(),
		"over cap": func() url.Values {
			v := url.Values{}
			for i := 0; i <= maxBatchInstallFiles; i++ {
				v.Add("missing_filename", "alpha-MISSING.safetensors")
				v.Add("missing_type", "Checkpoint")
			}
			return v
		}(),
	} {
		t.Run(name, func(t *testing.T) {
			srv, dl, _ := newBatchInstallServer(t,
				map[string]string{"alpha-MISSING.safetensors": "https://dl.example/alpha"}, nil)
			wfID := seedWorkflow(t, srv, store.WorkflowFormatAPI, twoMissingGraph)
			rec := post(t, srv, "/workflows/"+wfID+"/install-missing-and-run", form, true)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("%s = %d, want 400", name, rec.Code)
			}
			if n := len(dl.calls()); n != 0 {
				t.Errorf("refused request must not download: %v", dl.calls())
			}
		})
	}
}

// TestInstallMissingAndRunDeDupesDuplicateReferences: the same file named twice (two
// loaders sharing a checkpoint) is fetched ONCE.
func TestInstallMissingAndRunDeDupesDuplicateReferences(t *testing.T) {
	files := map[string]string{"alpha-MISSING.safetensors": "https://dl.example/alpha"}
	srv, dl, _ := newBatchInstallServer(t, files, nil)
	rr := &runRecorder{}
	srv.runFn = rr.fn()
	wfID := seedWorkflow(t, srv, store.WorkflowFormatAPI, twoMissingGraph)

	rec := post(t, srv, "/workflows/"+wfID+"/install-missing-and-run",
		installMissingForm("alpha-MISSING.safetensors", "alpha-MISSING.safetensors"), true)
	if rec.Code != http.StatusOK {
		t.Fatalf("duplicate batch = %d", rec.Code)
	}
	pollRunUntilDone(t, srv, wfID)
	if calls := dl.calls(); len(calls) != 1 {
		t.Errorf("duplicate references must be fetched once, got %v", calls)
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
	if n := len(dl.calls()); n != 0 {
		t.Errorf("ineligible request must not download: %v", dl.calls())
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
