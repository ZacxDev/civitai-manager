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

// heroWorkflowGraphUI is the SECOND seeded hero graph: the same pipeline as
// heroWorkflowGraph, expressed in the editor's UI ("Save") format.
//
// 🔴 Why a second fixture rather than a replacement. Measured against the
// operator's real library: `SELECT format, COUNT(*) FROM workflows GROUP BY
// format` → `ui|71`. ALL of it is UI-format; NONE is API-format. Seeding only the
// API graph made the walk audit a surface real users cannot reach and skip the one
// they can:
//
//   - The batch count segment (runCountSegment — the ×1/×2/×4/×8/Custom pills plus
//     the custom number input) is emitted ONLY when canQueue is true, i.e. only for
//     a UI-format workflow. With an API-only fixture the walk never screenshot it,
//     never axe-scanned it, and never posted to /run/queue.
//   - Worse, an INVERSION: realRun returns early with a warnings panel, BEFORE
//     comfy.Preflight, only for a UI conversion that produced warnings. An
//     API-format graph always reaches preflight — so `run-missing-models` was
//     auditing a failure panel reachable only by the format nobody has.
//
// Both formats stay seeded because the two take genuinely different branches
// through realRun and both are worth auditing.
//
// The graph is written to convert CLEANLY (no ConvertUIToAPI warnings) against
// fakeObjectInfoJSON, so the run reaches preflight and renders the SAME missing-
// models hero panel — the one a real UI-format user actually hits. Its KSampler
// widgets_values follows the real ComfyUI serialization, including the
// control_after_generate slot ("fixed") that immediately follows `seed`; drop it
// and every later widget value shifts by one. sampler_name/scheduler values are
// deliberately IN fakeObjectInfoJSON's combo choices so preflight reports missing
// MODELS only, never a BadOption.
const heroWorkflowGraphUI = `{
  "last_node_id": 8,
  "last_link_id": 11,
  "nodes": [
    {"id": 1, "type": "CheckpointLoaderSimple", "mode": 0, "pos": [40, 80], "size": [340, 100],
     "inputs": [],
     "outputs": [{"name": "MODEL", "type": "MODEL", "links": [1]},
                 {"name": "CLIP", "type": "CLIP", "links": [2]},
                 {"name": "VAE", "type": "VAE", "links": [9]}],
     "widgets_values": ["dreamshaperXL-MISSING.safetensors"]},
    {"id": 2, "type": "LoraLoader", "mode": 0, "pos": [430, 80], "size": [340, 140],
     "inputs": [{"name": "model", "type": "MODEL", "link": 1},
                {"name": "clip", "type": "CLIP", "link": 2}],
     "outputs": [{"name": "MODEL", "type": "MODEL", "links": [3]},
                 {"name": "CLIP", "type": "CLIP", "links": [4, 5]}],
     "widgets_values": ["detailer-MISSING.safetensors", 0.8, 1.0]},
    {"id": 3, "type": "CLIPTextEncode", "mode": 0, "pos": [820, 40], "size": [400, 120],
     "inputs": [{"name": "clip", "type": "CLIP", "link": 4}],
     "outputs": [{"name": "CONDITIONING", "type": "CONDITIONING", "links": [6]}],
     "widgets_values": ["a portrait of a cat, studio light"]},
    {"id": 4, "type": "CLIPTextEncode", "mode": 0, "pos": [820, 210], "size": [400, 120],
     "inputs": [{"name": "clip", "type": "CLIP", "link": 5}],
     "outputs": [{"name": "CONDITIONING", "type": "CONDITIONING", "links": [7]}],
     "widgets_values": ["blurry, low quality"]},
    {"id": 5, "type": "EmptyLatentImage", "mode": 0, "pos": [820, 380], "size": [340, 110],
     "inputs": [],
     "outputs": [{"name": "LATENT", "type": "LATENT", "links": [8]}],
     "widgets_values": [1024, 1024, 1]},
    {"id": 6, "type": "KSampler", "mode": 0, "pos": [1270, 80], "size": [340, 280],
     "inputs": [{"name": "model", "type": "MODEL", "link": 3},
                {"name": "positive", "type": "CONDITIONING", "link": 6},
                {"name": "negative", "type": "CONDITIONING", "link": 7},
                {"name": "latent_image", "type": "LATENT", "link": 8}],
     "outputs": [{"name": "LATENT", "type": "LATENT", "links": [10]}],
     "widgets_values": [123456789, "fixed", 20, 8, "euler", "normal", 1]},
    {"id": 7, "type": "VAEDecode", "mode": 0, "pos": [1660, 80], "size": [260, 60],
     "inputs": [{"name": "samples", "type": "LATENT", "link": 10},
                {"name": "vae", "type": "VAE", "link": 9}],
     "outputs": [{"name": "IMAGE", "type": "IMAGE", "links": [11]}]},
    {"id": 8, "type": "SaveImage", "mode": 0, "pos": [1960, 80], "size": [340, 270],
     "inputs": [{"name": "images", "type": "IMAGE", "link": 11}],
     "widgets_values": ["uxaudit-ui"]}
  ],
  "links": [
    [1, 1, 0, 2, 0, "MODEL"],
    [2, 1, 1, 2, 1, "CLIP"],
    [3, 2, 0, 6, 0, "MODEL"],
    [4, 2, 1, 3, 0, "CLIP"],
    [5, 2, 1, 4, 0, "CLIP"],
    [6, 3, 0, 6, 1, "CONDITIONING"],
    [7, 4, 0, 6, 2, "CONDITIONING"],
    [8, 5, 0, 6, 3, "LATENT"],
    [9, 1, 2, 7, 1, "VAE"],
    [10, 6, 0, 7, 0, "LATENT"],
    [11, 7, 0, 8, 0, "IMAGE"]
  ],
  "groups": [],
  "config": {},
  "extra": {},
  "version": 0.4
}`

// App is a booted, hermetic civitai-manager instance for the walk: an in-process
// web server (httptest) backed by a temp SQLite store seeded with deterministic
// fixtures, plus a fake ComfyUI the server-side run path talks to. The browser
// only ever talks to URL — no external network is used.
type App struct {
	URL string // base URL of the in-process civitai-manager web server
	// WorkflowID is the seeded API-format hero workflow (references missing models).
	// Its run control posts to RunPostPathAPI and it renders NO count segment.
	WorkflowID int64
	// WorkflowUIID is the seeded UI-format hero workflow — the format 100% of the
	// operator's real library is in. Its run control posts to RunPostPathUI (the
	// batch endpoint) and it DOES render the count segment.
	WorkflowUIID int64
	ScanDir      string // a seeded install dir the library scanner can walk offline

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
// SQLite file live there). It seeds: TWO hero workflows that reference missing
// models — one API-format, one UI-format, because the app's run surface branches on
// wf.Format and only the UI branch is what real libraries contain — an install dir
// with a couple of model files for the library scanner, and a subscription for a
// non-empty dashboard. It stands up a fake ComfyUI and wires cfg.ComfyURL at it so
// the run/missing-models path works offline.
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

	// Seed the SECOND hero workflow in UI format — the format the operator's entire
	// real library is in, and the only one that renders the batch count segment or
	// posts to the batch endpoint. See heroWorkflowGraphUI's comment.
	uiResources, _ := comfy.ExtractResourcesAny(comfy.FormatUI, []byte(heroWorkflowGraphUI))
	wfUIID, err := st.InsertWorkflow(context.Background(), &store.Workflow{
		Name:      "SDXL Portrait UI (missing models demo)",
		Format:    store.WorkflowFormatUI,
		Graph:     heroWorkflowGraphUI,
		Source:    store.WorkflowSourceImported,
		BaseModel: "SDXL 1.0",
		Resources: uiResources,
	})
	if err != nil {
		_ = st.Close()
		return nil, fmt.Errorf("seed ui workflow: %w", err)
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

	// Seed the 0019 comfy_model_cache with the SAME payload the fake ComfyUI serves,
	// so the run zone's pre-click readiness line renders its NEEDS state.
	//
	// 🔴 WITHOUT THIS THE WALK CERTIFIES THE WRONG BRANCH — the exact failure this
	// lab has already shipped once. The readiness line reads that row and nothing
	// else; the app populates it from a RUN, and the walk captures the workflow page
	// without running first. So the cold-cache branch rendered on every capture, and
	// the state that actually matters — the amber "Needs N …" line, the only one
	// using a colour the walk had never scanned — was never on screen at all. A green
	// "0 violations" would have been a fact about a surface the audit never loaded.
	//
	// It changes NOTHING else, and that was checked rather than assumed: the resource
	// chips' third state reads the same row through ModelFileChoices, but the choices
	// here list installed-sdxl-base / installed-detail-lora while both heroes
	// reference the -MISSING files, so every chip stays ✗ exactly as before.
	if err := st.PutComfyObjectInfo([]byte(fakeObjectInfoJSON)); err != nil {
		_ = st.Close()
		return nil, fmt.Errorf("seed comfy object_info cache: %w", err)
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
		URL:          ts.URL,
		WorkflowID:   wfID,
		WorkflowUIID: wfUIID,
		ScanDir:      scanDir,
		ts:           ts,
		comfy:        comfySrv,
		store:        st,
		cancel:       cancel,
	}, nil
}
