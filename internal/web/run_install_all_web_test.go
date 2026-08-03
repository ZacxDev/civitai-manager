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
	"strconv"
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
	body := renderString(t, runStatusFragment(twoMissingSnapshot(), 7, "tok", true, fullMaturityRange()))

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
	body := renderString(t, runStatusFragment(snap, 7, "tok", true, fullMaturityRange()))

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
	body := renderString(t, runStatusFragment(twoMissingSnapshot(), 7, "tok", false, fullMaturityRange()))

	if strings.Contains(body, "install-missing-and-run") {
		t.Errorf("ineligible failure state must not POST the batch install:\n%s", body)
	}
	if !strings.Contains(body, "Install 2 missing model files and run") || !strings.Contains(body, "disabled") {
		t.Errorf("expected a disabled primary action:\n%s", body)
	}
	// "Explain itself" now means OFFER THE FIX, not name a config key: the disabled
	// action carries the setup disclosure that makes it live.
	if !strings.Contains(body, `hx-get="/workflows/7/comfy-setup"`) {
		t.Errorf("disabled primary action must offer the setup step:\n%s", body)
	}
	if !strings.Contains(body, "where ComfyUI keeps its models") {
		t.Errorf("the setup step must say what it needs in plain words:\n%s", body)
	}
	// The lead is GATED on the CTA being able to deliver.
	if strings.Contains(body, "Nothing is broken") || strings.Contains(body, "Install them and it should run") {
		t.Errorf("lead must not promise an install the disabled CTA cannot perform:\n%s", body)
	}
	if !strings.Contains(body, "cannot fetch them for you") {
		t.Errorf("lead must name the real next step when installing is unavailable:\n%s", body)
	}
}

// TestBatchInstallKeepsUninferrableTypesAndFlagsThem is the REGRESSION GUARD for a
// real capability loss an earlier revision introduced.
//
// A reference whose CivitAI type could not be inferred was EXCLUDED from the batch on
// the theory that it was doomed. It is not: resolveInstallPlan falls back to
// HuggingFace when no model was chosen — the exact call the batch handler makes — and
// that path needs no CivitAI type. So such a file must stay IN the batch, be counted in
// the label, and only be FLAGGED as uncertain.
func TestBatchInstallKeepsUninferrableTypesAndFlagsThem(t *testing.T) {
	snap := twoMissingSnapshot()
	snap.Preflight = &comfy.PreflightReport{MissingModels: []string{
		"routable-MISSING.safetensors", "mystery-MISSING.bin"}}
	snap.MissingModels = []comfy.MissingModel{
		{Filename: "routable-MISSING.safetensors", Query: "routable", CivitaiType: "Checkpoint"},
		{Filename: "mystery-MISSING.bin", Query: "mystery", CivitaiType: ""}, // no inferred type
	}
	body := renderString(t, runStatusFragment(snap, 7, "tok", true, fullMaturityRange()))

	// Both files are attempted, and the label counts both.
	if !strings.Contains(body, "Install 2 missing model files and run") {
		t.Errorf("an uninferrable-type file must still be counted in the batch:\n%s", body)
	}
	for _, want := range []string{
		`name="missing_filename" value="routable-MISSING.safetensors"`,
		`name="missing_filename" value="mystery-MISSING.bin"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("batch must submit %q — excluding it removes real capability:\n%s", want, body)
		}
	}
	// It is FLAGGED, not excluded, and the note does not claim it cannot be installed.
	if !strings.Contains(body, "could not tell from the workflow which kind of model these are") {
		t.Errorf("an uncertain file must be flagged:\n%s", body)
	}
	if !strings.Contains(body, "mystery-MISSING.bin") {
		t.Errorf("the uncertain file must be named:\n%s", body)
	}
	if strings.Contains(body, "cannot be installed in one click") ||
		strings.Contains(body, "cannot tell which ComfyUI folder") {
		t.Errorf("the false \"cannot be installed\" claim is back:\n%s", body)
	}
	// The lead keeps its promise, because the CTA really can act.
	if !strings.Contains(body, "Nothing is broken") {
		t.Errorf("an available CTA must keep the reassuring lead:\n%s", body)
	}
}

// TestBatchInstallCuratedHFFamilyIsNotFlagged: a curated HuggingFace family (the
// flagship adetailer/ultralytics detectors) has no inferrable CivitAI type yet installs
// reliably, so it must be in the batch AND not flagged as uncertain.
func TestBatchInstallCuratedHFFamilyIsNotFlagged(t *testing.T) {
	detector := comfy.MissingModel{Filename: "bbox/face_yolov9c.pt", Query: "face yolov9c", CivitaiType: ""}
	if comfyTypeRoutable(detector.CivitaiType) {
		t.Fatal("fixture invalid: the detector should have no routable CivitAI type")
	}
	p := planBatchInstall([]comfy.MissingModel{detector}, true)
	if len(p.Installable) != 1 || !p.Available {
		t.Fatalf("curated HF family must be installable: %+v", p)
	}
	if len(p.Uncertain) != 0 {
		t.Errorf("a curated HF family match must NOT be flagged uncertain: %+v", p.Uncertain)
	}
	body := renderString(t, installAllMissingAction(p, 1, 7, "tok"))
	if !strings.Contains(body, `value="bbox/face_yolov9c.pt"`) {
		t.Errorf("the detector must ride in the batch:\n%s", body)
	}
	if strings.Contains(body, "could not tell from the workflow") {
		t.Errorf("no uncertainty advisory belongs on a curated family:\n%s", body)
	}
}

// TestInstallMissingAndRunInstallsViaHFFallback proves the capability the exclusion had
// silently removed, end to end: a file with NO inferrable CivitAI type, whose CivitAI
// search misses, is installed by the batch through the HuggingFace curated path.
func TestInstallMissingAndRunInstallsViaHFFallback(t *testing.T) {
	body := []byte("YOLO-DETECTOR-WEIGHTS")
	fake := &fakeHFClient{match: curatedMatch(body), ok: true, body: body}
	srv, comfyModels := newHFServer(t, fake)
	rr := &runRecorder{}
	srv.runFn = rr.fn()
	wfID := seedWorkflow(t, srv, store.WorkflowFormatAPI,
		`{"42":{"class_type":"UltralyticsDetectorProvider","inputs":{"model_name":"bbox/face_yolov9c.pt"}}}`)

	form := url.Values{}
	form.Add("missing_filename", "bbox/face_yolov9c.pt")
	form.Add("missing_type", "") // exactly what InferCivitaiType yields here
	rec := post(t, srv, "/workflows/"+wfID+"/install-missing-and-run", form, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("batch install of an HF-only file = %d (%s)", rec.Code, rec.Body.String())
	}
	pollRunUntilDone(t, srv, wfID)

	dest := filepath.Join(comfyModels, "ultralytics", "bbox", "face_yolov9c.pt")
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("batch did not install the HF-resolved file at %s: %v", dest, err)
	}
	if string(got) != string(body) {
		t.Errorf("installed content = %q", got)
	}
	rr.mu.Lock()
	defer rr.mu.Unlock()
	if rr.calls != 1 {
		t.Errorf("runFn called %d times, want 1", rr.calls)
	}
}

// TestBatchInstallDisabledOnlyWhenServerCannotInstall: the ONE decisive precondition is
// dlEligible, never the softer per-file uncertainty.
//
// The blocked branch used to be asserted by grepping for the string
// "comfy_model_path" — i.e. that the panel NAMED the config key. That expectation
// was inverted deliberately: naming the key in config-file jargon under a dead
// button IS the defect. The assertion now pins the recovery AFFORDANCE (the setup
// disclosure's GET route), which is the thing a user can act on.
func TestBatchInstallDisabledOnlyWhenServerCannotInstall(t *testing.T) {
	snap := twoMissingSnapshot()
	snap.Preflight = &comfy.PreflightReport{MissingModels: []string{"mystery-MISSING.bin"}}
	snap.MissingModels = []comfy.MissingModel{{Filename: "mystery-MISSING.bin", Query: "m", CivitaiType: ""}}

	// Eligible: an uncertain file alone does NOT disable the CTA.
	if body := renderString(t, runStatusFragment(snap, 7, "tok", true, fullMaturityRange())); !strings.Contains(
		body, "/workflows/7/install-missing-and-run") {
		t.Errorf("uncertainty alone must not disable the batch:\n%s", body)
	}
	// Ineligible: disabled, and the reason names the config blocker the user can fix.
	body := renderString(t, runStatusFragment(snap, 7, "tok", false, fullMaturityRange()))
	if strings.Contains(body, "install-missing-and-run") {
		t.Errorf("an ineligible server must not offer the POST:\n%s", body)
	}
	if !strings.Contains(body, `hx-get="/workflows/7/comfy-setup"`) {
		t.Errorf("a blocked CTA must offer the setup step, not just name a config key:\n%s", body)
	}
	// The button is still THERE, disabled — hiding it would delete the signpost for
	// the recovery path the disclosure unblocks.
	if !strings.Contains(body, "Install 1 missing model file and run") {
		t.Errorf("the blocked CTA must stay rendered and disabled:\n%s", body)
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

// TestPlanBatchInstallKeepsDistinctFilesSharingABasename is the REGRESSION GUARD for a
// basename-only de-dupe key. `SDXL/model.safetensors` as a Checkpoint and
// `flux/model.safetensors` as a LORA share a basename, but SafeModelDest routes them to
// checkpoints/ and loras/ — they are TWO installs. Collapsing them under-counted the
// batch ("Install 1 …" while the run still needed 2) and left the run broken with no
// explanation, which is the promise-vs-reality defect this panel exists to remove.
func TestPlanBatchInstallKeepsDistinctFilesSharingABasename(t *testing.T) {
	models := []comfy.MissingModel{
		{Filename: "SDXL/model.safetensors", CivitaiType: "Checkpoint"},
		{Filename: "flux/model.safetensors", CivitaiType: "LORA"},
	}
	p := planBatchInstall(models, true)
	if len(p.Installable) != 2 {
		t.Fatalf("same basename + different destination = two installs, got %d: %+v",
			len(p.Installable), p.Installable)
	}
	body := renderString(t, installAllMissingAction(p, 2, 7, "tok"))
	if !strings.Contains(body, "Install 2 missing model files and run") {
		t.Errorf("label must count both files:\n%s", body)
	}

	// A genuine duplicate (same basename AND same destination) still collapses. LORA and
	// LoCon both map to loras/ and both are in resolveTypeWhitelist. (LyCORIS also maps to
	// loras/ in comfy.typeSubdirs but is NOT whitelisted, so civitaiTypeParam normalizes it
	// to "" — it is deliberately not used as a "same destination" example.)
	same := []comfy.MissingModel{
		{Filename: "a/x.safetensors", CivitaiType: "LORA"},
		{Filename: "b/x.safetensors", CivitaiType: "LoCon"},
	}
	if got := len(planBatchInstall(same, true).Installable); got != 1 {
		t.Errorf("same basename + same destination must collapse, got %d", got)
	}
	// Two same-named files with UN-ROUTABLE types are ONE install: neither has a
	// destination, so both resolve through the same filename-keyed HuggingFace path.
	odd := []comfy.MissingModel{
		{Filename: "x.bin", CivitaiType: ""},
		{Filename: "x.bin", CivitaiType: "SomethingUnmapped"},
	}
	if got := len(planBatchInstall(odd, true).Installable); got != 1 {
		t.Errorf("same basename with no destination is one install, got %d", got)
	}
}

// TestInstallDedupeKeyIsCaseSensitiveLikeSafeModelDest pins the CASE decision, which is a
// deliberate platform trade rather than an oversight (see installDedupeKey).
//
// comfy.SafeModelDest preserves case, so `Model.safetensors` and `model.safetensors` are
// two destinations. Folding them would make the CTA offer "Install 1" for two missing
// files, install one, and leave the run failing with nothing said — the exact defect the
// key exists to prevent. On a case-insensitive filesystem the pair over-counts by one,
// which costs a redundant request that ErrDestExists refuses before any body is streamed.
func TestInstallDedupeKeyIsCaseSensitiveLikeSafeModelDest(t *testing.T) {
	upper, _ := installDedupeKey("Model.safetensors", "Checkpoint")
	lower, _ := installDedupeKey("model.safetensors", "Checkpoint")
	if upper == lower {
		t.Fatalf("case must be significant, matching SafeModelDest: %q == %q", upper, lower)
	}
	models := []comfy.MissingModel{
		{Filename: "Model.safetensors", CivitaiType: "Checkpoint"},
		{Filename: "model.safetensors", CivitaiType: "Checkpoint"},
	}
	p := planBatchInstall(models, true)
	if len(p.Installable) != 2 {
		t.Fatalf("two case-variant references are two installs, got %d: %+v", len(p.Installable), p.Installable)
	}
	body := renderString(t, installAllMissingAction(p, 2, 7, "tok"))
	if !strings.Contains(body, "Install 2 missing model files and run") {
		t.Errorf("both case variants must be offered:\n%s", body)
	}

	// The key is built from the SAME normalization the handler applies, so the two layers
	// cannot diverge. LyCORIS is the witness: routable per comfy.typeSubdirs, not
	// whitelisted per resolveTypeWhitelist.
	lyc, _ := installDedupeKey("x.safetensors", "LyCORIS")
	none, _ := installDedupeKey("x.safetensors", "")
	if lyc != none {
		t.Errorf("the key must normalize the type exactly as the handler does: %q != %q", lyc, none)
	}
}

// TestFailureHeadlineAgreesWithTheCTA: two references that are ONE install must not
// produce "Run failed — 2 model files missing" above "Install 1 … and run".
func TestFailureHeadlineAgreesWithTheCTA(t *testing.T) {
	snap := runSnapshot{
		Started: true, WorkflowID: 7, Phase: runPhaseFailed, Message: "Preflight failed.",
		Preflight: &comfy.PreflightReport{MissingModels: []string{
			"SDXL/dup.safetensors", "flux/dup.safetensors"}},
		MissingModels: []comfy.MissingModel{
			{Filename: "SDXL/dup.safetensors", CivitaiType: "Checkpoint"},
			{Filename: "flux/dup.safetensors", CivitaiType: "Checkpoint"},
		},
		MissingResolved: map[string]missingResolution{},
		LibMeta:         map[string]store.LocalModelMeta{},
	}
	body := renderString(t, runStatusFragment(snap, 7, "tok", true, fullMaturityRange()))

	if !strings.Contains(body, "Run failed — 1 model file missing") {
		t.Errorf("headline must count the DISTINCT set, like the CTA:\n%s", body)
	}
	if strings.Contains(body, "Run failed — 2 model files missing") {
		t.Errorf("headline still counts raw preflight strings:\n%s", body)
	}
	if !strings.Contains(body, "Install 1 missing model file and run") {
		t.Errorf("CTA count changed unexpectedly:\n%s", body)
	}
	if !strings.Contains(body, "1 model file that is not installed") {
		t.Errorf("the lead must agree too:\n%s", body)
	}

	// Fallback: an OLDER snapshot with no enriched analysis has no triage to count, so the
	// preflight count still drives the headline.
	old := snap
	old.MissingModels = nil
	oldBody := renderString(t, runStatusFragment(old, 7, "tok", true, fullMaturityRange()))
	if !strings.Contains(oldBody, "Run failed — 2 model files missing") {
		t.Errorf("with no enriched analysis the preflight count must stand:\n%s", oldBody)
	}
}

// TestInstallDedupeKeyDistinguishesDestinations pins the key directly.
func TestInstallDedupeKeyDistinguishesDestinations(t *testing.T) {
	ckpt, ok1 := installDedupeKey("SDXL/model.safetensors", "Checkpoint")
	lora, ok2 := installDedupeKey("flux/model.safetensors", "LORA")
	if !ok1 || !ok2 {
		t.Fatal("both references are installable")
	}
	if ckpt == lora {
		t.Errorf("keys must differ by destination: %q == %q", ckpt, lora)
	}
	// Same basename (byte-for-byte) + same destination → one key.
	a, _ := installDedupeKey("a/x.safetensors", "LORA")
	b, _ := installDedupeKey("b/x.safetensors", "LoCon")
	if a != b {
		t.Errorf("same basename + same destination must share a key: %q != %q", a, b)
	}
	// A case variant is a DIFFERENT destination (SafeModelDest preserves case) — see
	// TestInstallDedupeKeyIsCaseSensitiveLikeSafeModelDest.
	if c, _ := installDedupeKey("b/X.SAFETENSORS", "LoCon"); c == b {
		t.Errorf("case must be significant: %q == %q", c, b)
	}
	if _, ok := installDedupeKey("  ", "Checkpoint"); ok {
		t.Error("an empty basename is not installable")
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
			body := renderString(t, runStatusFragment(snap, 7, "tok", true, fullMaturityRange()))
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

// TestSingleFileInstallDroppedClickIsReported covers the SIBLING buttons directly below
// the batch CTA: "Install and run" (download-and-run) and the bad-option
// "Install <file>" (install-option-and-run). Both pay a CivitAI round-trip and were
// then silently discarded by the one-run-at-a-time guard, rendering the OTHER job's
// panel — finding 9's exact defect on the buttons next to the fixed one.
func TestSingleFileInstallDroppedClickIsReported(t *testing.T) {
	for name, tc := range map[string]struct {
		path string
		form url.Values
	}{
		"download-and-run": {"/download-and-run", url.Values{
			"filename": {"alpha-MISSING.safetensors"}, "type": {"Checkpoint"}}},
		"install-option-and-run": {"/install-option-and-run", url.Values{
			"install_filename": {"alpha-MISSING.safetensors"}, "install_type": {"Checkpoint"}}},
	} {
		t.Run(name, func(t *testing.T) {
			srv, dl, _ := newBatchInstallServer(t,
				map[string]string{"alpha-MISSING.safetensors": "https://dl.example/alpha"}, nil)
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
			seqBefore := srv.runJobState().Seq

			rec := post(t, srv, "/workflows/"+wfID+tc.path, tc.form, true)
			if rec.Code != http.StatusOK {
				t.Fatalf("contended %s = %d", name, rec.Code)
			}
			if !strings.Contains(rec.Body.String(), "another run or download is already in progress") {
				t.Errorf("%s dropped the click silently:\n%s", name, rec.Body.String())
			}
			if got := srv.runJobState().Seq; got != seqBefore {
				t.Errorf("%s must not start a second job (seq %d → %d)", name, seqBefore, got)
			}
			release()
			pollRunUntilDone(t, srv, wfID)
			_ = dl
		})
	}
}

// TestBatchCTAComposesWithPresetTabs is the COMPOSITION guard against the run-presets
// feature (tabs / Fork / reconciliation / mode capture), which merged into the same run
// UI this panel rewrote.
//
// It renders the WHOLE run panel — preset tab strip and all — with a settled
// missing-models failure, and pins the three ways the two features could collide:
//
//  1. the batch CTA still renders inside the new structure at all;
//  2. its <form> is NOT nested inside the preset form (nested forms are invalid HTML and
//     the inner one silently loses its fields) — the batch form must live in #run-status,
//     a SIBLING of the #run-params container the tab strip and preset form occupy;
//  3. the mode-picker hx-include target the CTA relies on is a real <select> inside
//     #run-modes, since the preset feature now pre-selects that picker.
func TestBatchCTAComposesWithPresetTabs(t *testing.T) {
	srv := newTestServer(t)
	wf := seedPresetWorkflow(t, srv, "t2i", presetUIGraph)
	seedPreset(t, srv, wf, "Base", wf.GraphHash, func(ri comfy.RunInput) string { return ri.Current })
	v := srv.buildPresetView(context.Background(), wf, 0, nil, true)

	snap := runSnapshot{
		Started: true, WorkflowID: wf.ID, Seq: 4, Phase: runPhaseFailed,
		Message:   "Preflight failed — this workflow references nodes or models that are not installed.",
		Preflight: &comfy.PreflightReport{MissingModels: []string{"alpha-MISSING.safetensors"}},
		MissingModels: []comfy.MissingModel{
			{Filename: "alpha-MISSING.safetensors", Query: "alpha", CivitaiType: "Checkpoint"}},
		MissingResolved: map[string]missingResolution{},
		LibMeta:         map[string]store.LocalModelMeta{},
	}
	// runPanel was folded into generateSection by PR C1 (Open in ComfyUI + Run + the
	// cloud run became ONE "Generate" section). Same render, wider signature: it also
	// takes whether ComfyUI is configured and the helper state, both of which only
	// affect sibling controls — the batch CTA this test is about is unchanged.
	page := renderString(t, generateSection(wf, snap, "tok", true, true /* dlEligible */, fullMaturityRange(), v,
		true /* comfyConfigured */, comfyHelperView{}))

	// 1. The CTA survived the merge.
	wfID := strconv.FormatInt(wf.ID, 10)
	action := "/workflows/" + wfID + "/install-missing-and-run"
	if !strings.Contains(page, action) {
		t.Fatalf("batch CTA missing from the preset-tab run panel:\n%s", page)
	}
	if !strings.Contains(page, "Install 1 missing model file and run") {
		t.Errorf("batch CTA label missing:\n%s", page)
	}

	// 2. The batch form is a SIBLING of the preset form, not nested inside it. The batch
	// form lives in #run-status; the preset form (and the tab strip) live in #run-params,
	// which closes before #run-status opens.
	presetForm := strings.Index(page, `id="`+runPresetFormID+`"`)
	status := strings.Index(page, `id="`+runStatusContainerID+`"`)
	cta := strings.Index(page, action)
	if presetForm < 0 || status < 0 || cta < 0 {
		t.Fatalf("anchors missing (presetForm=%d status=%d cta=%d)", presetForm, status, cta)
	}
	if !(presetForm < status && status < cta) {
		t.Errorf("the batch CTA must render inside #run-status, after the preset form "+
			"(presetForm=%d status=%d cta=%d)", presetForm, status, cta)
	}
	// Structural proof, not a heuristic: count <form>/</form> depth and require the batch
	// form to OPEN at depth 0. A bare strings.Contains(…, "</form>") would be satisfied by
	// any close — including the <form method="dialog"> wrappers inside the missing-model
	// rows — and so would not prove the preset form itself had closed.
	//
	// Measure at the batch form's own opening tag, NOT at its action attribute: the action
	// string sits inside that tag, so measuring at it counts the form itself and reads 1.
	batchFormStart := strings.LastIndex(page[:cta], "<form")
	if batchFormStart < 0 {
		t.Fatalf("the batch action is not inside a <form> at all")
	}
	if d := formDepthAt(page, batchFormStart); d != 0 {
		t.Errorf("the batch form opens at form-nesting depth %d, want 0 (nested forms lose "+
			"the inner form's fields)", d)
	}
	if d := formDepthAt(page, len(page)); d != 0 {
		t.Errorf("unbalanced form tags in the rendered panel (final depth %d)", d)
	}

	// 3. The hx-include target the CTA carries resolves to a real element. presetUIGraph
	// is a single-mode workflow, so #run-modes is the stable EMPTY container — which the
	// selector now NAMES DIRECTLY rather than reaching for a <select> inside it. That was
	// issue #28: the old "#run-modes select" matched nothing here, and htmx logs a console
	// error for an hx-include that resolves to zero elements. Nothing about the SUBMITTED
	// request changed (a single-mode workflow has no mode to send). See
	// TestRunPanelHxIncludesAlwaysMatch for the regression guard.
	if !strings.Contains(page, `id="`+runModesContainerID+`"`) {
		t.Errorf("the #run-modes container the CTA hx-includes is gone:\n%s", page)
	}
	if !strings.Contains(page, `hx-include="`+runModesInclude+`"`) {
		t.Errorf("the batch CTA lost its mode-picker include:\n%s", page)
	}
}

// formDepthAt returns the <form> nesting depth at byte offset upto — 0 means the offset
// sits outside every form. It scans opening/closing form tags in order rather than
// searching for any close, so it cannot be satisfied by an unrelated </form>.
func formDepthAt(page string, upto int) int {
	depth, i := 0, 0
	for i < upto {
		open := strings.Index(page[i:upto], "<form")
		closed := strings.Index(page[i:upto], "</form")
		switch {
		case open < 0 && closed < 0:
			return depth
		case closed < 0 || (open >= 0 && open < closed):
			depth++
			i += open + len("<form")
		default:
			depth--
			i += closed + len("</form")
		}
	}
	return depth
}

// TestFormDepthAtCountsNesting proves the helper above actually measures nesting — a
// broken counter would make the composition test vacuous.
func TestFormDepthAtCountsNesting(t *testing.T) {
	for name, tc := range map[string]struct {
		page string
		want int
	}{
		"outside":            {`<form></form>|`, 0},
		"inside":             {`<form>|`, 1},
		"nested":             {`<form><form>|`, 2},
		"closed then marker": {`<form method="dialog"></form><div>|`, 0},
		"sibling closes":     {`<form id="a"></form><form id="b">|`, 1},
	} {
		t.Run(name, func(t *testing.T) {
			if got := formDepthAt(tc.page, strings.Index(tc.page, "|")); got != tc.want {
				t.Errorf("formDepthAt = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestDetectorPrefixIsNotFlaggedUncertain closes the advisory's FALSE NEGATIVE: a
// non-curated detector ref carries a bbox//segm/ prefix that hf.searchSubdir routes on,
// so it can auto-install off a recognized-org search hit. Warning "may not be found"
// about a file that usually IS found is the kind of inaccuracy that makes the whole
// advisory untrustworthy.
func TestDetectorPrefixIsNotFlaggedUncertain(t *testing.T) {
	custom := comfy.MissingModel{Filename: "bbox/my_custom_det.pt", CivitaiType: ""}
	if comfyTypeRoutable(civitaiTypeParam(custom.CivitaiType)) {
		t.Fatal("fixture invalid: the ref must have no routable CivitAI type")
	}
	p := planBatchInstall([]comfy.MissingModel{custom}, true)
	if len(p.Installable) != 1 || !p.Available {
		t.Fatalf("a detector ref must be installable: %+v", p)
	}
	if len(p.Uncertain) != 0 {
		t.Errorf("a bbox/ detector ref must NOT be flagged uncertain: %+v", p.Uncertain)
	}
	plain := comfy.MissingModel{Filename: "mystery-MISSING.bin", CivitaiType: ""}
	if got := planBatchInstall([]comfy.MissingModel{plain}, true); len(got.Uncertain) != 1 {
		t.Errorf("a genuinely unroutable ref must stay flagged: %+v", got.Uncertain)
	}
}

// TestPostResolutionAlreadyInstalledDroppedClickIsReported covers the LAST two dropped-
// click sites: the already-installed-at-destination branches of download-and-run and
// install-option-and-run. They sit AFTER resolveInstallPlan, so the click has already
// paid a live resolution — my earlier claim that no remaining site did visible work
// before the guarded call was wrong about exactly these two.
//
// They are reached via the HuggingFace path: with no CivitAI type the pre-resolution fast
// path is skipped, resolution yields the HF destination, and that file already exists.
func TestPostResolutionAlreadyInstalledDroppedClickIsReported(t *testing.T) {
	for name, tc := range map[string]struct {
		path string
		form url.Values
	}{
		"download-and-run": {"/download-and-run", url.Values{
			"filename": {"bbox/face_yolov9c.pt"}}},
		"install-option-and-run": {"/install-option-and-run", url.Values{
			"install_filename": {"bbox/face_yolov9c.pt"}}},
	} {
		t.Run(name, func(t *testing.T) {
			body := []byte("YOLO-DETECTOR-WEIGHTS")
			fake := &fakeHFClient{match: curatedMatch(body), ok: true, body: body}
			srv, comfyModels := newHFServer(t, fake)
			rr := &runRecorder{}
			release := rr.hold() // park the first run so it stays "running"
			defer release()
			srv.runFn = rr.fn()
			wfID := seedWorkflow(t, srv, store.WorkflowFormatAPI,
				`{"42":{"class_type":"UltralyticsDetectorProvider","inputs":{"model_name":"bbox/face_yolov9c.pt"}}}`)

			// Pre-create the HF destination so the POST-resolution branch is the one taken.
			dir := filepath.Join(comfyModels, "ultralytics", "bbox")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "face_yolov9c.pt"), body, 0o644); err != nil {
				t.Fatal(err)
			}

			if rec := post(t, srv, "/workflows/"+wfID+"/run", nil, true); rec.Code != http.StatusOK {
				t.Fatalf("first run = %d", rec.Code)
			}
			if snap := srv.runJobState(); !snap.Running {
				t.Fatalf("expected an in-flight run to contend with, got %+v", snap)
			}
			seqBefore := srv.runJobState().Seq

			rec := post(t, srv, "/workflows/"+wfID+tc.path, tc.form, true)
			if rec.Code != http.StatusOK {
				t.Fatalf("contended %s = %d", name, rec.Code)
			}
			if fake.resolveHits == 0 {
				t.Fatalf("%s never resolved — the test is not exercising the post-resolution branch", name)
			}
			if !strings.Contains(rec.Body.String(), "another run or download is already in progress") {
				t.Errorf("%s dropped the click silently after paying for a resolution:\n%s",
					name, rec.Body.String())
			}
			if got := srv.runJobState().Seq; got != seqBefore {
				t.Errorf("%s must not start a second job (seq %d → %d)", name, seqBefore, got)
			}
			release()
			pollRunUntilDone(t, srv, wfID)
		})
	}
}
