package web

import (
	"bytes"
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

// detectorBadOption is a model-FILE incompatible option (an Impact-Pack detector whose
// referenced file is not installed); wildcardBadOption is an inert enum drift.
var detectorBadOption = comfy.BadOption{
	NodeIDs: []string{"42"}, ClassType: "UltralyticsDetectorProvider", InputName: "model_name",
	Current: "bbox/face_yolov9c.pt", Choices: []string{"bbox/face_yolov8m.pt", "bbox/hand_yolov8s.pt"},
}

var wildcardBadOption = comfy.BadOption{
	NodeIDs: []string{"3"}, ClassType: "ImpactWildcardProcessor", InputName: "Select to add Wildcard",
	Current: "Select Wildcard 🟢 Full Cache", Choices: []string{"Select the Wildcard to add to the text"},
}

// TestBadOptionInstallActionRenders: a model-file bad option renders an enabled
// "Install <basename>" action posting to install-option-and-run (hx-including the
// section form) ALONGSIDE its substitute dropdown; the inert wildcard group is
// pick-only (no Install button). Eligible server (comfy_model_path set).
func TestBadOptionInstallActionRenders(t *testing.T) {
	section := renderString(t, incompatibleOptionsSection(
		[]comfy.BadOption{detectorBadOption, wildcardBadOption}, 7, "csrf-tok", true))

	for _, want := range []string{
		"Install face_yolov9c.pt",             // the install CTA (basename)
		"/workflows/7/install-option-and-run", // the combined endpoint
		// Pulls the other picks along — and, since multi-mode templates landed, the
		// mode picker's selects too, so an install-and-run keeps the chosen pipeline.
		`hx-include="closest form, #run-modes select"`,
		"install_filename",                     // the install target in hx-vals
		`<option value="bbox/face_yolov8m.pt"`, // the substitute dropdown still present
		"Or substitute an installed file",      // model-file dropdown is optional
		"Run with selected options",            // the pick-only submit still present
	} {
		if !strings.Contains(section, want) {
			t.Errorf("section missing %q:\n%s", want, section)
		}
	}
	// Exactly ONE Install action — the inert wildcard group must NOT get one.
	if n := strings.Count(section, "install-option-and-run"); n != 1 {
		t.Errorf("want exactly 1 Install action, got %d:\n%s", n, section)
	}
	// No external asset introduced.
	if strings.Contains(section, "<script") {
		t.Errorf("section must not introduce a <script>:\n%s", section)
	}
}

// TestBadOptionInstallInertOnly: a section with ONLY an inert enum drift renders no
// Install action (pick-only, unchanged behavior).
func TestBadOptionInstallInertOnly(t *testing.T) {
	section := renderString(t, incompatibleOptionsSection(
		[]comfy.BadOption{wildcardBadOption}, 7, "csrf-tok", true))
	if strings.Contains(section, "install-option-and-run") || strings.Contains(section, "Install ") {
		t.Errorf("an inert-only section must have NO Install action:\n%s", section)
	}
	if !strings.Contains(section, "Run with selected options") {
		t.Errorf("inert section should keep the pick-and-run submit:\n%s", section)
	}
}

// TestBadOptionInstallIneligibleFallback: with comfy_model_path unset (dlEligible
// false), the Install button is DISABLED with a reason and a "Search CivitAI" link
// (never hidden), and it does NOT POST install-option-and-run.
func TestBadOptionInstallIneligibleFallback(t *testing.T) {
	section := renderString(t, incompatibleOptionsSection(
		[]comfy.BadOption{detectorBadOption}, 7, "csrf-tok", false))
	if strings.Contains(section, "install-option-and-run") {
		t.Errorf("ineligible section must NOT POST install-option-and-run:\n%s", section)
	}
	if !strings.Contains(section, "Install face_yolov9c.pt") || !strings.Contains(section, "disabled") {
		t.Errorf("ineligible section should show a DISABLED Install action:\n%s", section)
	}
	if !strings.Contains(section, "Set comfy_model_path") {
		t.Errorf("ineligible section should explain why:\n%s", section)
	}
	if !strings.Contains(section, "Search CivitAI") {
		t.Errorf("ineligible section should offer a manual-fetch link:\n%s", section)
	}
}

// TestBadOptionInstallUnroutableFallback: a model-file bad option whose input does not
// route to a known ComfyUI subdir (e.g. an upscale model_name on a non-detector node)
// degrades to the disabled+link fallback even when eligible.
func TestBadOptionInstallUnroutableFallback(t *testing.T) {
	unroutable := comfy.BadOption{
		ClassType: "UpscaleModelLoader", InputName: "model_name",
		Current: "RealESRGAN_x4plus.pth", Choices: []string{"other.pth"},
	}
	section := renderString(t, incompatibleOptionsSection([]comfy.BadOption{unroutable}, 7, "csrf", true))
	if strings.Contains(section, "install-option-and-run") {
		t.Errorf("an unroutable model-file option must NOT offer an auto-install:\n%s", section)
	}
	if !strings.Contains(section, "Install RealESRGAN_x4plus.pth") || !strings.Contains(section, "disabled") {
		t.Errorf("unroutable option should show a disabled Install + link:\n%s", section)
	}
}

// TestBadOptionInstallActionEscapesUntrusted: an untrusted Current value never injects
// raw markup into the install control (escaped in hx-vals + text).
func TestBadOptionInstallActionEscapesUntrusted(t *testing.T) {
	evil := comfy.BadOption{
		ClassType: "CheckpointLoaderSimple", InputName: "ckpt_name",
		Current: `evil"<script>alert(1)</script>.safetensors`, Choices: []string{"a.safetensors"},
	}
	section := renderString(t, incompatibleOptionsSection([]comfy.BadOption{evil}, 7, "csrf", true))
	if strings.Contains(section, "<script>alert(1)</script>") {
		t.Errorf("untrusted Current value must be escaped:\n%s", section)
	}
}

// TestInstallOptionAndRunComposesInstallAndFixes drives the combined handler: it
// installs the detector FILE via the HF fallback AND applies the wildcard pick, drops
// the install target's OWN substitute pick, starts exactly one run with those fixes,
// and leaves the stored workflow byte-identical.
func TestInstallOptionAndRunComposesInstallAndFixes(t *testing.T) {
	body := []byte("YOLO-DETECTOR-WEIGHTS")
	fake := &fakeHFClient{match: curatedMatch(body), ok: true, body: body}
	srv, comfyModels := newHFServer(t, fake)
	rr := &runRecorder{}
	srv.runFn = rr.fn()
	const graph = `{"42":{"class_type":"UltralyticsDetectorProvider","inputs":{"model_name":"bbox/face_yolov9c.pt"}},"3":{"class_type":"ImpactWildcardProcessor","inputs":{"Select to add Wildcard":"Select Wildcard 🟢 Full Cache"}}}`
	wfID := seedWorkflow(t, srv, store.WorkflowFormatAPI, graph)

	rec := post(t, srv, "/workflows/"+wfID+"/install-option-and-run", url.Values{
		"install_filename": {"bbox/face_yolov9c.pt"},
		"install_type":     {""},
		// Section form fields for BOTH groups: the wildcard pick (applied) and a
		// substitute pick on the install target (must be DROPPED — we install it).
		"opt_input": {"Select to add Wildcard", "model_name"},
		"opt_old":   {"Select Wildcard 🟢 Full Cache", "bbox/face_yolov9c.pt"},
		"opt_new":   {"Select the Wildcard to add to the text", "bbox/face_yolov8m.pt"},
	}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("install-option-and-run = %d", rec.Code)
	}
	pollRunUntilDone(t, srv, wfID)

	// The detector file was resolved via HF and written into ultralytics/bbox.
	dest := filepath.Join(comfyModels, "ultralytics", "bbox", "face_yolov9c.pt")
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("detector not written to %s: %v", dest, err)
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
		t.Fatalf("runFn called %d times, want 1", rr.calls)
	}
	fixes := rr.opts[0].OptionFixes
	// The wildcard pick is applied.
	if v := fixes[comfy.OptionFixKey{InputName: "Select to add Wildcard", OldValue: "Select Wildcard 🟢 Full Cache"}]; v != "Select the Wildcard to add to the text" {
		t.Errorf("wildcard fix not applied: %q (fixes=%v)", v, fixes)
	}
	// The install target's OWN substitute pick was dropped (we installed the exact file).
	if _, ok := fixes[comfy.OptionFixKey{InputName: "model_name", OldValue: "bbox/face_yolov9c.pt"}]; ok {
		t.Errorf("install target's own fix must be dropped, got %v", fixes)
	}
	if len(fixes) != 1 {
		t.Errorf("want exactly 1 applied fix, got %d: %v", len(fixes), fixes)
	}

	// The stored workflow row must be byte-identical.
	wfid64, _ := strconv.ParseInt(wfID, 10, 64)
	wf, err := srv.store.GetWorkflow(context.Background(), wfid64)
	if err != nil {
		t.Fatalf("reload workflow: %v", err)
	}
	if wf.Graph != graph {
		t.Errorf("stored workflow graph mutated:\ngot:  %s\nwant: %s", wf.Graph, graph)
	}
}

// TestInstallOptionAndRunUnconfiguredFallback: comfy_model_path unset → link-only
// fallback, NO download, NO run (the section's disabled button should not have posted,
// but a direct POST must still degrade safely).
func TestInstallOptionAndRunUnconfiguredFallback(t *testing.T) {
	srv := newLibraryTestServer(t, t.TempDir()) // no ComfyModelPath
	srv.reader = &recordingSearchReader{result: resolveResult("Some Model")}
	var ran int
	srv.runFn = func(context.Context, *store.Workflow, runUpdater, runOptions) (*runResult, error) {
		ran++
		return &runResult{}, nil
	}
	wfID := seedWorkflow(t, srv, store.WorkflowFormatAPI,
		`{"4":{"class_type":"CheckpointLoaderSimple","inputs":{"ckpt_name":"foo.safetensors"}}}`)

	rec := post(t, srv, "/workflows/"+wfID+"/install-option-and-run", url.Values{
		"install_filename": {"foo.safetensors"}, "install_type": {"Checkpoint"},
	}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("install-option-and-run = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Search CivitAI") {
		t.Errorf("unconfigured should degrade to the resolve/link fragment:\n%s", rec.Body.String())
	}
	if ran != 0 {
		t.Errorf("run started %d times, want 0", ran)
	}
}

// TestInstallOptionAndRunGating: CSRF rejected + non-loopback bind → gated note (and
// no run).
func TestInstallOptionAndRunGating(t *testing.T) {
	fake := &fakeHFClient{match: curatedMatch([]byte("W")), ok: true, body: []byte("W")}
	srv, _ := newHFServer(t, fake)
	srv.runFn = (&runRecorder{}).fn()
	wfID := seedWorkflow(t, srv, store.WorkflowFormatAPI, `{"4":{"class_type":"X","inputs":{}}}`)
	form := url.Values{"install_filename": {"bbox/face_yolov9c.pt"}, "install_type": {""}}

	if rec := post(t, srv, "/workflows/"+wfID+"/install-option-and-run", form, false); rec.Code != http.StatusForbidden {
		t.Fatalf("install-option-and-run without CSRF = %d, want 403", rec.Code)
	}
	if fake.dlCalls != 0 {
		t.Errorf("HF downloader called %d times on CSRF reject, want 0", fake.dlCalls)
	}

	// Non-loopback bind → gated note.
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	gated := NewServer(st, stubReader{}, stubSubscriber{}, Config{
		BaseURL: "https://civitai.com", DefaultPollInterval: time.Hour, Addr: "0.0.0.0:8787",
		ComfyURL: "http://127.0.0.1:8188", ComfyModelPath: t.TempDir(),
	}, nil)
	gid := seedWorkflow(t, gated, store.WorkflowFormatAPI, `{"4":{"class_type":"X","inputs":{}}}`)
	rec := post(t, gated, "/workflows/"+gid+"/install-option-and-run", form, true)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "non-loopback") {
		t.Errorf("gated install-option-and-run = %d body=%s", rec.Code, rec.Body.String())
	}
}
