package uxaudit

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/comfy"
	"github.com/ZacxDev/civitai-manager/internal/store"
	"github.com/ZacxDev/civitai-manager/internal/web"
)

// heroWorkflowGraph is a seeded API-format ComfyUI graph. Its loader nodes
// reference two model files (a checkpoint + a LoRA) that the fake ComfyUI's
// /object_info does NOT list as installed and that are absent from the seeded
// local library — so comfy.Preflight reports both as MISSING and the run's
// "missing models" HERO panel renders. Every class_type here exists in
// fakeObjectInfoJSON so nothing is flagged as a missing NODE.
const heroWorkflowGraph = `{
  "1": {"class_type": "CheckpointLoaderSimple", "inputs": {"ckpt_name": "dreamshaperXL-MISSING.safetensors"}},
  "2": {"class_type": "LoraLoader", "inputs": {
    "lora_name": "detailer-MISSING.safetensors",
    "model": ["1", 0], "clip": ["1", 1],
    "strength_model": 0.8, "strength_clip": 1.0
  }},
  "3": {"class_type": "CLIPTextEncode", "inputs": {"text": "a portrait of a cat, studio light", "clip": ["2", 1]}},
  "4": {"class_type": "CLIPTextEncode", "inputs": {"text": "blurry, low quality", "clip": ["2", 1]}},
  "5": {"class_type": "EmptyLatentImage", "inputs": {"width": 1024, "height": 1024, "batch_size": 1}},
  "6": {"class_type": "KSampler", "inputs": {"model": ["2", 0], "positive": ["3", 0], "negative": ["4", 0], "latent_image": ["5", 0]}},
  "7": {"class_type": "VAEDecode", "inputs": {"samples": ["6", 0], "vae": ["1", 2]}},
  "8": {"class_type": "SaveImage", "inputs": {"images": ["7", 0], "filename_prefix": "uxaudit"}}
}`

// App is a booted, hermetic civitai-manager instance for the walk: an in-process
// web server (httptest) backed by a temp SQLite store seeded with deterministic
// fixtures, plus a fake ComfyUI the server-side run path talks to. The browser
// only ever talks to URL — no external network is used.
type App struct {
	URL        string // base URL of the in-process civitai-manager web server
	WorkflowID int64  // the seeded hero workflow (references missing models)
	ScanDir    string // a seeded install dir the library scanner can walk offline

	ts     *httptest.Server
	comfy  *httptest.Server
	store  *store.Store
	cancel context.CancelFunc
}

// Close tears down the web server, the fake ComfyUI, the base context, and the
// store, in that order.
func (a *App) Close() {
	if a.ts != nil {
		a.ts.Close()
	}
	if a.comfy != nil {
		a.comfy.Close()
	}
	if a.cancel != nil {
		a.cancel()
	}
	if a.store != nil {
		_ = a.store.Close()
	}
}

// Boot builds a hermetic civitai-manager instance under workDir (fixtures + a
// SQLite file live there). It seeds: a hero workflow that references missing
// models, an install dir with a couple of model files for the library scanner,
// and a subscription for a non-empty dashboard. It stands up a fake ComfyUI and
// wires cfg.ComfyURL at it so the run/missing-models path works offline.
func Boot(workDir string) (*App, error) {
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return nil, err
	}

	dbPath := filepath.Join(workDir, "uxaudit.db")
	st, err := store.Open(dbPath)
	if err != nil {
		return nil, fmt.Errorf("open store: %w", err)
	}

	// Seed the hero workflow (API format, references un-installed models).
	resources, _ := comfy.ExtractResources([]byte(heroWorkflowGraph))
	wfID, err := st.InsertWorkflow(context.Background(), &store.Workflow{
		Name:      "SDXL Portrait (missing models demo)",
		Format:    store.WorkflowFormatAPI,
		Graph:     heroWorkflowGraph,
		Source:    store.WorkflowSourceImported,
		BaseModel: "SDXL 1.0",
		Resources: resources,
	})
	if err != nil {
		_ = st.Close()
		return nil, fmt.Errorf("seed workflow: %w", err)
	}

	// Seed an install dir with a couple of model files, plus a cross-dir duplicate,
	// so the "Scan for model files" tab has a selectable dir and an offline scan
	// yields a non-empty Summary (with a duplicate candidate).
	scanDir := filepath.Join(workDir, "models")
	if err := os.MkdirAll(filepath.Join(scanDir, "checkpoints"), 0o755); err != nil {
		_ = st.Close()
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(scanDir, "loras"), 0o755); err != nil {
		_ = st.Close()
		return nil, err
	}
	seedFiles := map[string]string{
		filepath.Join(scanDir, "checkpoints", "installed-sdxl-base.safetensors"): "weights-sdxl-base",
		filepath.Join(scanDir, "loras", "installed-detail-lora.safetensors"):     "weights-detail-lora",
		// A byte-identical duplicate of the base checkpoint → a duplicate candidate.
		filepath.Join(scanDir, "checkpoints", "installed-sdxl-base-copy.safetensors"): "weights-sdxl-base",
	}
	for p, body := range seedFiles {
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			_ = st.Close()
			return nil, err
		}
	}
	if err := st.AddScanDir(scanDir); err != nil {
		_ = st.Close()
		return nil, fmt.Errorf("add scan dir: %w", err)
	}
	// Persist match_remote=false so an offline scan never reaches civitai.com.
	if err := st.SetSetting("match_remote", "false"); err != nil {
		_ = st.Close()
		return nil, fmt.Errorf("disable remote match: %w", err)
	}

	// Seed a subscription so the dashboard renders a non-empty subscriptions list.
	modelID := 4384
	if _, err := st.CreateSubscription(store.Subscription{
		Kind: store.KindModel, ModelID: &modelID, AutoDownload: true,
		PollIntervalSecs: 3600, CreatedAt: time.Now().UTC(),
	}); err != nil {
		_ = st.Close()
		return nil, fmt.Errorf("seed subscription: %w", err)
	}

	// The fake ComfyUI the server-side run path talks to (never the browser).
	comfySrv := newFakeComfyUI()

	modelPath := filepath.Join(workDir, "comfy-models")
	_ = os.MkdirAll(modelPath, 0o755)

	srv := web.NewServer(st, fakeReader{}, fakeSubscriber{}, web.Config{
		BaseURL:             "https://civitai.com",
		DefaultPollInterval: time.Hour,
		// A loopback Addr unlocks the extra-scan-path + run/comfy affordances (the
		// gating keys off the CONFIGURED bind, not the ephemeral httptest port).
		Addr:            "127.0.0.1:8787",
		ModelRoot:       scanDir,
		TrashDir:        filepath.Join(workDir, ".trash"),
		WebScanTimeout:  2 * time.Minute,
		WebScanMaxFiles: 10000,
		ComfyURL:        comfySrv.URL,
		ComfyModelPath:  modelPath,
		HFFallback:      false,
	}, nil)

	base, cancel := context.WithCancel(context.Background())
	srv.SetBaseContext(base)

	ts := httptest.NewServer(srv.Handler())

	return &App{
		URL:        ts.URL,
		WorkflowID: wfID,
		ScanDir:    scanDir,
		ts:         ts,
		comfy:      comfySrv,
		store:      st,
		cancel:     cancel,
	}, nil
}
