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

	// runOptionsFromParams reconstructs the overrides for a re-run.
	back := runOptionsFromParams(g.Params)
	if back.Substitute["missing.safetensors"] != "installed.safetensors" {
		t.Errorf("substitute not reconstructed: %+v", back.Substitute)
	}
	if back.WidgetOverrides[comfy.WidgetOverrideKey{NodeID: "3", InputName: "seed"}] != "42" {
		t.Errorf("widget override not reconstructed: %+v", back.WidgetOverrides)
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
	srv.startRun(wf, runOptions{})

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
