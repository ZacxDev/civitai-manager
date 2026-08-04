package web

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/comfy"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// newCaptureServer builds a test server with an outputs dir + a fake comfy client,
// and returns it plus the outputs root and the seeded workflow.
func newCaptureServer(t *testing.T, fake *fakeComfy) (*Server, string, *store.Workflow) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	root := t.TempDir()
	srv := NewServer(st, stubReader{}, stubSubscriber{},
		Config{Addr: "127.0.0.1:8787", OutputsDir: root, ComfyURL: "http://127.0.0.1:8188"}, nil)
	srv.comfyClientFn = func() comfyClient { return fake }

	wfID, err := st.InsertWorkflow(context.Background(), &store.Workflow{
		Name: "my-wf", Format: store.WorkflowFormatAPI, BaseModel: "SDXL 1.0",
		Graph: `{"1":{"class_type":"X","inputs":{}}}`, Source: store.WorkflowSourceImported,
		Resources: []string{"sdxl.safetensors"},
	})
	if err != nil {
		t.Fatalf("seed workflow: %v", err)
	}
	wf, err := st.GetWorkflow(context.Background(), wfID)
	if err != nil {
		t.Fatalf("get workflow: %v", err)
	}
	return srv, root, wf
}

func TestCaptureGenerationWritesFilesAndRows(t *testing.T) {
	fake := &fakeComfy{viewData: []byte("PNGBYTES"), viewCT: "image/png"}
	srv, root, wf := newCaptureServer(t, fake)

	opts := runOptions{
		Substitute:      map[string]string{"missing.safetensors": "installed.safetensors"},
		WidgetOverrides: map[comfy.WidgetOverrideKey]string{{NodeID: "3", InputName: "seed"}: "42"},
	}
	res := &runResult{
		PromptID: "promptZZZ",
		Images: []comfy.ImageRef{
			{Filename: "a.png", Type: "output"},
			{Filename: "b.png", Type: "output"},
		},
	}
	srv.captureGeneration(wf, opts, res)

	// Two files landed under <root>/<prompt_id>/.
	for _, name := range []string{"0-a.png", "1-b.png"} {
		p := filepath.Join(root, "promptZZZ", name)
		got, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("expected file %s: %v", p, err)
		}
		if string(got) != "PNGBYTES" {
			t.Errorf("file %s content = %q", name, got)
		}
	}

	// One 'ready' generation row with 2 images and the params snapshot.
	gens, err := srv.store.ListGenerations(context.Background(), store.ListGenerationsOpts{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(gens) != 1 {
		t.Fatalf("generations = %d, want 1", len(gens))
	}
	g := gens[0]
	if g.Status != store.GenerationStatusReady {
		t.Errorf("status = %q, want ready", g.Status)
	}
	if g.ImageCount != 2 {
		t.Errorf("image_count = %d, want 2", g.ImageCount)
	}
	if g.WorkflowID == nil || *g.WorkflowID != wf.ID {
		t.Errorf("workflow_id = %v, want %d", g.WorkflowID, wf.ID)
	}
	if g.BaseModel != "SDXL 1.0" {
		t.Errorf("base_model snapshot = %q", g.BaseModel)
	}

	// The params snapshot round-trips the applied WidgetOverrides + Substitute.
	snap := parseRunParams(g.Params)
	if snap.Substitute["missing.safetensors"] != "installed.safetensors" {
		t.Errorf("substitute not snapshotted: %+v", snap.Substitute)
	}
	if len(snap.WidgetOverrides) != 1 || snap.WidgetOverrides[0].NodeID != "3" ||
		snap.WidgetOverrides[0].InputName != "seed" || snap.WidgetOverrides[0].Value != "42" {
		t.Errorf("widget overrides not snapshotted: %+v", snap.WidgetOverrides)
	}

	// runOptionsFromParams reconstructs the overrides for a re-run. The legacy
	// name-keyed set is position-independent, so it replays regardless of the graph.
	back, stale := runOptionsFromParams(g.Params, g.GraphHash, wf.GraphHash)
	if stale != "" {
		t.Fatalf("unchanged graph must not be reported stale: %s", stale)
	}
	if back.Substitute["missing.safetensors"] != "installed.safetensors" {
		t.Errorf("substitute not reconstructed: %+v", back.Substitute)
	}
	if back.WidgetOverrides[comfy.WidgetOverrideKey{NodeID: "3", InputName: "seed"}] != "42" {
		t.Errorf("widget override not reconstructed: %+v", back.WidgetOverrides)
	}
}

// TestRunParamsSnapshotRoundTripsUIWidgetOverrides is the F7 coverage gap: the NEW
// index-keyed override set must survive buildRunParamsSnapshot → JSON →
// runOptionsFromParams, be sorted deterministically, and coexist with a legacy
// name-keyed set (each applied through its own path — UI pre-conversion, legacy
// post-conversion).
func TestRunParamsSnapshotRoundTripsUIWidgetOverrides(t *testing.T) {
	wf := &store.Workflow{ID: 1, GraphHash: "HASH-A", Format: store.WorkflowFormatUI}
	opts := runOptions{
		UIWidgetOverrides: map[comfy.UIWidgetKey]string{
			{NodeID: "40", Widget: 0}: "999",
			{NodeID: "3", Widget: 1}:  "second",
			{NodeID: "3", Widget: 0}:  "first",
		},
		WidgetOverrides: map[comfy.WidgetOverrideKey]string{
			{NodeID: "9", InputName: "steps"}: "12",
			{NodeID: "9", InputName: "cfg"}:   "7",
		},
	}

	// Deterministic ordering: the same options must marshal byte-identically every
	// time (map iteration order must not leak into the persisted row).
	first := marshalRunParams(buildRunParamsSnapshot(wf, opts))
	for i := 0; i < 20; i++ {
		if got := marshalRunParams(buildRunParamsSnapshot(wf, opts)); got != first {
			t.Fatalf("params JSON is not stable across marshals:\n%s\n%s", first, got)
		}
	}
	snap := parseRunParams(first)
	if len(snap.UIWidgetOverrides) != 3 {
		t.Fatalf("ui overrides = %+v", snap.UIWidgetOverrides)
	}
	if snap.UIWidgetOverrides[0].NodeID != "3" || snap.UIWidgetOverrides[0].widgetDisplay() != "0" ||
		snap.UIWidgetOverrides[1].widgetDisplay() != "1" || snap.UIWidgetOverrides[2].NodeID != "40" {
		t.Errorf("ui overrides not sorted by (node, widget): %+v", snap.UIWidgetOverrides)
	}
	if len(snap.WidgetOverrides) != 2 || snap.WidgetOverrides[0].InputName != "cfg" {
		t.Errorf("legacy overrides not sorted by (node, input): %+v", snap.WidgetOverrides)
	}

	// Same graph hash → BOTH schemes reconstruct; each targets its own apply path.
	back, stale := runOptionsFromParams(first, "HASH-A", "HASH-A")
	if stale != "" {
		t.Fatalf("matching hashes must not be stale: %s", stale)
	}
	if back.UIWidgetOverrides[comfy.UIWidgetKey{NodeID: "3", Widget: 0}] != "first" ||
		back.UIWidgetOverrides[comfy.UIWidgetKey{NodeID: "40", Widget: 0}] != "999" {
		t.Errorf("ui overrides not reconstructed: %+v", back.UIWidgetOverrides)
	}
	if back.WidgetOverrides[comfy.WidgetOverrideKey{NodeID: "9", InputName: "steps"}] != "12" {
		t.Errorf("legacy overrides not reconstructed: %+v", back.WidgetOverrides)
	}
}

// TestRunOptionsFromParamsRefusesStalePositionalKeys is the F2 regression: a stored
// (node, widget index) override must NEVER be replayed against a graph that has
// changed since capture — the index would land on whatever widget now occupies that
// slot (a captured seed type-coerced over a prompt).
func TestRunOptionsFromParamsRefusesStalePositionalKeys(t *testing.T) {
	wf := &store.Workflow{ID: 1, GraphHash: "HASH-A"}
	params := marshalRunParams(buildRunParamsSnapshot(wf, runOptions{
		UIWidgetOverrides: map[comfy.UIWidgetKey]string{{NodeID: "3", Widget: 0}: "999"},
		Substitute:        map[string]string{"a": "b"},
	}))

	for _, tc := range []struct{ name, gen, cur string }{
		{"graph replaced by a rescan", "HASH-A", "HASH-B"},
		{"generation predates graph hashing", "", "HASH-B"},
		{"workflow has no hash", "HASH-A", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			back, stale := runOptionsFromParams(params, tc.gen, tc.cur)
			if stale == "" {
				t.Fatal("stale positional overrides must be refused")
			}
			if len(back.UIWidgetOverrides) != 0 {
				t.Errorf("positional overrides must not be reconstructed: %+v", back.UIWidgetOverrides)
			}
		})
	}
}

// TestRunOptionsFromParamsDropsMalformedWidgetIndex proves a corrupt/hand-edited
// widget field is dropped rather than defaulting the edit onto widget 0.
func TestRunOptionsFromParamsDropsMalformedWidgetIndex(t *testing.T) {
	const params = `{"ui_widget_overrides":[{"node_id":"3","widget":"x","value":"999"},
	  {"node_id":"","widget":2,"value":"v"},{"node_id":"4","widget":1,"value":"ok"}]}`
	back, stale := runOptionsFromParams(params, "H", "H")
	if stale != "" {
		t.Fatalf("unexpected stale: %s", stale)
	}
	if _, bad := back.UIWidgetOverrides[comfy.UIWidgetKey{NodeID: "3", Widget: 0}]; bad {
		t.Errorf("non-integer widget must not default to slot 0: %+v", back.UIWidgetOverrides)
	}
	if len(back.UIWidgetOverrides) != 1 ||
		back.UIWidgetOverrides[comfy.UIWidgetKey{NodeID: "4", Widget: 1}] != "ok" {
		t.Errorf("well-formed entry should survive: %+v", back.UIWidgetOverrides)
	}
}

func TestCapturePartialOnViewFailure(t *testing.T) {
	// First image fetches; second image's View fails → partial capture.
	fake := &fakeComfy{viewFunc: func(ref comfy.ImageRef) ([]byte, string, error) {
		if ref.Filename == "bad.png" {
			return nil, "", errors.New("view failed")
		}
		return []byte("OK"), "image/png", nil
	}}
	srv, root, wf := newCaptureServer(t, fake)
	res := &runResult{PromptID: "p", Images: []comfy.ImageRef{
		{Filename: "good.png"}, {Filename: "bad.png"},
	}}
	srv.captureGeneration(wf, runOptions{}, res)

	gens, _ := srv.store.ListGenerations(context.Background(), store.ListGenerationsOpts{})
	if len(gens) != 1 {
		t.Fatalf("generations = %d, want 1", len(gens))
	}
	if gens[0].Status != store.GenerationStatusPartial {
		t.Errorf("status = %q, want partial", gens[0].Status)
	}
	if gens[0].ImageCount != 1 {
		t.Errorf("image_count = %d, want 1", gens[0].ImageCount)
	}
	// Only the good image is on disk.
	if _, err := os.Stat(filepath.Join(root, "p", "0-good.png")); err != nil {
		t.Errorf("good image should exist: %v", err)
	}
}

func TestCaptureAllViewFailuresInsertsNothing(t *testing.T) {
	fake := &fakeComfy{viewFunc: func(comfy.ImageRef) ([]byte, string, error) {
		return nil, "", errors.New("all fail")
	}}
	srv, _, wf := newCaptureServer(t, fake)
	res := &runResult{PromptID: "p", Images: []comfy.ImageRef{{Filename: "a.png"}}}
	srv.captureGeneration(wf, runOptions{}, res)

	n, _ := srv.store.CountGenerations(context.Background(), nil)
	if n != 0 {
		t.Errorf("generations = %d, want 0 (nothing captured)", n)
	}
}

func TestCaptureDisabledWhenNoOutputsDir(t *testing.T) {
	fake := &fakeComfy{viewData: []byte("x"), viewCT: "image/png"}
	srv, _, wf := newCaptureServer(t, fake)
	srv.cfg.OutputsDir = "" // capture must no-op
	res := &runResult{PromptID: "p", Images: []comfy.ImageRef{{Filename: "a.png"}}}
	srv.captureGeneration(wf, runOptions{}, res)
	if n, _ := srv.store.CountGenerations(context.Background(), nil); n != 0 {
		t.Errorf("generations = %d, want 0 (capture disabled)", n)
	}
}

// waitForGenerations polls until the store holds want generations, or fails.
// Capture runs off the run mutex AFTER the job settles, so "the job is no longer
// running" is not a sufficient wait condition for the capture side effects.
func waitForGenerations(t *testing.T, srv *Server, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		n, _ := srv.store.CountGenerations(context.Background(), nil)
		if n == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("generations = %d, want %d", n, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// waitForRunSettled polls until the current run job is no longer running.
func waitForRunSettled(t *testing.T, srv *Server) runSnapshot {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		snap := srv.runJobState()
		if !snap.Running {
			return snap
		}
		if time.Now().After(deadline) {
			t.Fatal("run did not settle")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestDownloadAndRunCapturesGeneration is the regression test for the
// download-and-run path having NO capture: a successful "Download & run" must land
// in the gallery exactly like a plain run (both paths share settleAndCapture).
func TestDownloadAndRunCapturesGeneration(t *testing.T) {
	fake := &fakeComfy{viewData: []byte("PNGBYTES"), viewCT: "image/png"}
	srv, root, wf := newCaptureServer(t, fake)

	downloaded := 0
	srv.downloadFn = func(context.Context, pendingDownload, func(string)) error {
		downloaded++
		return nil
	}
	srv.runFn = func(context.Context, *store.Workflow, runUpdater, runOptions) (*runResult, error) {
		return &runResult{PromptID: "dlrun", Images: []comfy.ImageRef{{Filename: "a.png"}}}, nil
	}

	srv.startDownloadAndRun(wf, pendingDownload{FileName: "missing.safetensors"}, runOptions{})

	snap := waitForRunSettled(t, srv)
	if snap.Phase != runPhaseDone {
		t.Fatalf("phase = %q, want done", snap.Phase)
	}
	if downloaded != 1 {
		t.Fatalf("downloadFn calls = %d, want 1", downloaded)
	}

	waitForGenerations(t, srv, 1)
	gens, err := srv.store.ListGenerations(context.Background(), store.ListGenerationsOpts{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if gens[0].PromptID != "dlrun" {
		t.Errorf("prompt_id = %q, want dlrun", gens[0].PromptID)
	}
	if gens[0].WorkflowID == nil || *gens[0].WorkflowID != wf.ID {
		t.Errorf("workflow_id = %v, want %d", gens[0].WorkflowID, wf.ID)
	}
	if _, err := os.Stat(filepath.Join(root, "dlrun", "0-a.png")); err != nil {
		t.Errorf("captured file missing: %v", err)
	}
}

// TestDownloadAndRunFailureCapturesNothing asserts the capture stays strictly on
// the success path: a failed download settles the job as an error and inserts no
// generation.
func TestDownloadAndRunFailureCapturesNothing(t *testing.T) {
	fake := &fakeComfy{viewData: []byte("PNGBYTES"), viewCT: "image/png"}
	srv, _, wf := newCaptureServer(t, fake)

	srv.downloadFn = func(context.Context, pendingDownload, func(string)) error {
		return errors.New("download failed")
	}
	ran := false
	srv.runFn = func(context.Context, *store.Workflow, runUpdater, runOptions) (*runResult, error) {
		ran = true
		return &runResult{PromptID: "nope", Images: []comfy.ImageRef{{Filename: "a.png"}}}, nil
	}

	srv.startDownloadAndRun(wf, pendingDownload{FileName: "missing.safetensors"}, runOptions{})

	snap := waitForRunSettled(t, srv)
	if snap.Phase == runPhaseDone {
		t.Fatalf("phase = %q, want a failure phase", snap.Phase)
	}
	if ran {
		t.Error("run must not start after a failed download")
	}
	if n, _ := srv.store.CountGenerations(context.Background(), nil); n != 0 {
		t.Errorf("generations = %d, want 0", n)
	}
}

// TestStartRunCaptureBestEffortDoesNotAffectOutcome drives the FULL startRun
// goroutine with an injected runFn returning images, and asserts (a) the run
// settles to done and (b) a capture PANIC is swallowed — the run outcome is
// unchanged and nothing crashes.
func TestStartRunCapturePanicSwallowed(t *testing.T) {
	srv := newTestServer(t)
	// A runFn that returns a successful result with one image.
	srv.runFn = func(context.Context, *store.Workflow, runUpdater, runOptions) (*runResult, error) {
		return &runResult{PromptID: "p", Images: []comfy.ImageRef{{Filename: "a.png"}}}, nil
	}
	// A capture seam that panics — the goroutine's recover must swallow it.
	captured := make(chan struct{}, 1)
	srv.captureFn = func(*store.Workflow, runOptions, *runResult) {
		defer func() { captured <- struct{}{} }()
		panic("boom")
	}

	wf := &store.Workflow{ID: 1, Name: "wf", Format: store.WorkflowFormatAPI}
	srv.startRunWithMessage(wf, runOptions{}, "Starting run…")

	// Wait for the capture seam to have been reached.
	select {
	case <-captured:
	case <-time.After(5 * time.Second):
		t.Fatal("capture seam was never invoked")
	}

	// The run outcome is unaffected: the job settled to done with the image.
	deadline := time.Now().Add(5 * time.Second)
	for {
		snap := srv.runJobState()
		if !snap.Running {
			if snap.Phase != runPhaseDone {
				t.Fatalf("phase = %q, want done (capture panic must not change outcome)", snap.Phase)
			}
			if len(snap.Images) != 1 {
				t.Fatalf("images = %d, want 1", len(snap.Images))
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("run did not settle")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
