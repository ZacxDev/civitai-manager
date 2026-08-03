package uxaudit

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
	"github.com/ZacxDev/civitai-manager/internal/poller"
)

// fakeReader is a deterministic, offline civitai.Reader. It NEVER touches
// civitai.com: every read returns canned data so the Discover-workflows browse
// page and the missing-model "Find on CivitAI" resolution render real cards
// without network egress. It mirrors the shape of the repo's test-only stubReader
// (internal/web/web_test.go) but is a normal (non-test) type so a different module
// can use it.
type fakeReader struct{}

var _ civitai.Reader = fakeReader{}

func (fakeReader) GetModel(context.Context, string) (*civitai.ModelDetail, []byte, error) {
	return &civitai.ModelDetail{
		ID:   4384,
		Name: "DreamShaper XL",
		Type: "Checkpoint",
		Creator: &civitai.Creator{
			Username: "lab-author",
		},
		Stats: civitai.ModelStats{
			DownloadCount: 1200,
			ThumbsUpCount: 340,
			CommentCount:  85,
		},
		ModelVersions: []civitai.ModelVersionSummary{
			{
				ID:        1,
				Name:      "v1.0",
				BaseModel: "SDXL 1.0",
				Files: []civitai.ModelVersionFile{
					{
						ID:      1001,
						Name:    "dreamshaperXL_v10.safetensors",
						Type:    "Model",
						SizeKB:  6500000,
						Primary: true,
					},
				},
			},
		},
	}, nil, nil
}

func (fakeReader) GetModelVersion(context.Context, string) (*civitai.ModelVersionDetail, []byte, error) {
	return &civitai.ModelVersionDetail{}, nil, nil
}

func (fakeReader) GetModelVersionByHash(context.Context, string) (*civitai.ModelVersionDetail, []byte, error) {
	return nil, nil, civitai.ErrNotFound
}

func (fakeReader) GetModelVersionsByHashes(context.Context, []string) ([]civitai.HashMatch, error) {
	return nil, nil
}

// SearchModels returns a small, deterministic result set. The `.Raw` body carries
// matching item ids (the Discover facet feed parses it) but DELIBERATELY omits any
// image URLs so the browser never fetches a real civitai CDN asset during the
// hermetic walk — the cards render as offline text cards.
func (fakeReader) SearchModels(_ context.Context, q url.Values) (*civitai.ModelSearchResult, error) {
	items := cannedWorkflowModels()
	raw := rawItemsBody(items)
	return &civitai.ModelSearchResult{Items: items, Raw: raw}, nil
}

func (fakeReader) SearchCreators(context.Context, url.Values) (*civitai.CreatorSearchResult, error) {
	return &civitai.CreatorSearchResult{}, nil
}

func (fakeReader) SearchImages(context.Context, url.Values) (*civitai.ImageSearchResult, error) {
	return &civitai.ImageSearchResult{}, nil
}

func cannedWorkflowModels() []civitai.ModelListItem {
	return []civitai.ModelListItem{
		{ID: 900001, Name: "SDXL Portrait Workflow", Type: "Workflows", Creator: &civitai.Creator{Username: "lab-author"}},
		{ID: 900002, Name: "Inpaint Cleanup Workflow", Type: "Workflows", Creator: &civitai.Creator{Username: "lab-author"}},
		{ID: 900003, Name: "Upscale x4 Workflow", Type: "Workflows", Creator: &civitai.Creator{Username: "lab-author"}},
	}
}

// rawItemsBody builds the `{"items":[...]}` raw response body the Discover facet
// feed (internal/web/discover_facets.go) parses to re-key items by id. Each item
// carries only id/name/type — no images — to keep the walk fully offline.
func rawItemsBody(items []civitai.ModelListItem) []byte {
	type rawItem struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
		Type string `json:"type"`
	}
	body := struct {
		Items []rawItem `json:"items"`
	}{}
	for _, it := range items {
		body.Items = append(body.Items, rawItem{ID: it.ID, Name: it.Name, Type: it.Type})
	}
	b, _ := json.Marshal(body)
	return b
}

// fakeSubscriber satisfies web.Subscriber. The walk never POSTs a real
// subscription, so these are inert; they exist only to construct the server.
type fakeSubscriber struct{}

func (fakeSubscriber) SubscribeModel(context.Context, int, poller.SubscribeOptions) (int64, error) {
	return 1, nil
}

func (fakeSubscriber) SubscribeCreator(context.Context, string, poller.SubscribeOptions) (int64, error) {
	return 1, nil
}

// newFakeComfyUI returns an httptest.Server that answers the minimal ComfyUI HTTP
// surface the run path exercises BEFORE it would submit a prompt:
//
//   - GET /system_stats  → a reachability/version probe.
//   - GET /object_info   → a node schema whose loader combos list only INSTALLED
//     files, so a workflow referencing an un-installed checkpoint/LoRA fails
//     comfy.Preflight and the "missing models" hero panel renders.
//   - the ComfyUI-Manager V3 subset → so the missing-CUSTOM-NODE half of that same
//     panel renders too. Manager is a custom node, so its web API is served by
//     ComfyUI itself and belongs on this mux rather than behind a new seam.
//
// No /prompt is ever hit: preflight stops the run before submission. The web
// server (server-side) is the only client — the browser never talks to it.
func newFakeComfyUI() *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/system_stats", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"system":{"comfyui_version":"0.3.30-uxaudit-fake","os":"linux","python_version":"3.12.0"}}`))
	})
	mux.HandleFunc("/object_info", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(fakeObjectInfoJSON))
	})
	// --- ComfyUI-Manager V3.41 subset ---
	//
	// 🔴 Without these the walk certified a panel it never fully rendered.
	// ManagerProbe requires one of the queue/status routes to answer before
	// ManagerPresent is true, and with it false the attribution pass produced no
	// packs at all — so the pack cards, the "Best match"/"Also claims it" badges,
	// the Install buttons, the ambiguity notice and the alternatives disclosure were
	// ALL absent from every capture. The axe result was therefore true and scoped to
	// a panel missing its entire node-pack half.
	mux.HandleFunc("/api/manager/queue/status", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"total_count":0,"done_count":0,"in_progress_count":0,"is_processing":false}`))
	})
	mux.HandleFunc("/api/manager/version", func(w http.ResponseWriter, _ *http.Request) {
		// V3 answers PLAIN TEXT, not JSON — matching the real Manager.
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("V3.41-uxaudit-fake"))
	})
	mux.HandleFunc("/api/customnode/getmappings", func(w http.ResponseWriter, r *http.Request) {
		// The real handler bracket-accesses query["mode"] and 500s without it.
		if r.URL.Query().Get("mode") == "" {
			http.Error(w, "KeyError: 'mode'", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(fakeManagerMappingsJSON))
	})
	mux.HandleFunc("/api/customnode/getlist", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("mode") == "" {
			http.Error(w, "KeyError: 'mode'", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(fakeManagerGetlistJSON))
	})
	mux.HandleFunc("/api/customnode/installed", func(w http.ResponseWriter, _ *http.Request) {
		// Nothing is installed, so both claimants stay installable and the contest is
		// a real choice rather than a settled one.
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	})
	mux.HandleFunc("/queue", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"queue_running":[],"queue_pending":[]}`))
	})
	// Any other ComfyUI call would mean the run got past preflight unexpectedly;
	// answer 404 loudly rather than silently succeeding.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "uxaudit fake ComfyUI: unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
	})
	return httptest.NewServer(mux)
}

// fakeObjectInfoJSON is the ComfyUI /object_info payload. Its loader combos list
// only INSTALLED files, none of which match the seeded workflows' references, so
// preflight reports every referenced model as missing. Every class_type the
// seeded graphs use is present here so none is flagged as a missing NODE (we want
// missing MODELS, the hero). The raw shape mirrors ComfyUI: each input is
// [choicesOrType, opts]; a string first element (e.g. "MODEL") is a typed slot,
// an array first element is a combo whose entries are the choices.
//
// 🔴 `input_order` is LOAD-BEARING and must be present on every entry, even though
// the API-format hero never reads it — but NOT for the reason this comment used to
// give. It claimed "realRun returns EARLY on any conversion warning — before
// comfy.Preflight — so a UI-format hero would render the conversion-warnings panel
// INSTEAD of the missing-models one". Both halves are dead. The early return was
// replaced by the never-submit gate (`internal/web/run_handlers.go`, search
// NEVER-SUBMIT GATE), which runs AFTER comfy.Preflight; and on a preflight failure
// the conversion warnings render WITH the missing-models panel — subordinated into
// its "Technical details" disclosure — never instead of it.
//
// The requirement survives by the OPPOSITE mechanism, measured rather than reasoned.
// comfy.ConvertUIToAPI walks input_order to assign a UI node's widgets_values; an
// entry that HAS inputs but no input_order makes it emit `node N has no input_order;
// widget values not mapped` and map NOTHING for that node. On a LOADER that drops the
// model filename out of the converted graph, so comfy.Preflight never sees a reference
// to it and cannot report it missing. Strip both loaders and `report.OK` stays TRUE —
// the gate then takes its `if report.OK` branch and returns warnings only, so the walk
// never exercises preflight on the format real users actually have. (Measured: all 71
// workflows in the operator's library are `ui`; zero are `api`.)
//
// 🔴 The OBVIOUS one-entry probe is UNDER-POWERED — do not re-derive this with it.
// Stripping CheckpointLoaderSimple ALONE leaves the missing-models panel rendering,
// because LoraLoader independently keeps preflight red. It reads as "input_order does
// not matter here" and it is wrong. Measured against this fixture + heroWorkflowGraphUI:
//
//	stripped                warnings  report.OK  panel               walk
//	none (as shipped)          0        false     missing-models (2)  PASS 24 caps / 0 viol
//	CheckpointLoaderSimple     1        false     missing-models (1)  PASS 24 caps / 0 viol
//	the 5 NON-loader entries   5        false     missing-models (2)  (not walked)
//	BOTH loaders               2        TRUE      warnings-only       FAIL — 60s timeout
//	all 7 entries              7        TRUE      warnings-only       (not walked)
//
// The walk's failure mode is loud, which is what makes the green rows mean something:
// heroRunPrep waits on HeroMarker ("Missing model files") for 60s, so a warnings-only
// panel times out `capture run-missing-models-ui` rather than quietly capturing the
// wrong page. Only run-missing-models-ui fails — the API hero never converts at all.
//
// So the panel flip is driven by the LOADER entries. The other five still need theirs,
// for the separate widget-alignment reason below and because
// TestUIHeroGraphReachesPreflight requires ZERO conversion warnings. Keep it on every
// entry; just do not justify it with the deleted early return.
//
// KSampler carries the full real widget set (seed / steps / cfg / sampler_name /
// scheduler / denoise) rather than links only, for two reasons: the UI graph's
// widgets_values has to line up with a REAL KSampler serialization — including the
// control_after_generate slot that follows `seed` — and DetectRunInputs needs a
// seed to expose for the Parameters panel the UI-format run surface is audited
// with. The API-format hero graph simply omits those inputs; preflight does not
// require an input to be present, so it is unaffected.
// fakeManagerMappingsJSON is ComfyUI-Manager's node-class → pack index, shaped
// exactly like the real /api/customnode/getmappings body: pack id → [class list,
// metadata].
//
// 🔴 TWO packs deliberately claim UltimateSDUpscale, and that contest is the whole
// point of this fixture. It reproduces the measured live case
// (comfyui_ultimatesdupscale enumerates 4 classes, comfyui-promptchain 93), which is
// what makes the run panel render its ambiguity notice, the "Best match" /
// "Also claims it" badges, and — the state the audit could not reach before — the
// COLLAPSED alternatives disclosure. A single-claimant fixture renders none of them.
//
// ⚠ Keep PromptChain's list long. The ranking is by SCOPE (matched classes over
// claimed classes), so if the two lists were the same length the winner would be
// decided by the weaker name-affinity signal instead, and the fixture would stop
// exercising the comparator it exists to exercise.
const fakeManagerMappingsJSON = `{
 "https://github.com/ssitu/ComfyUI_UltimateSDUpscale": [
  ["UltimateSDUpscale", "UltimateSDUpscaleNoUpscale", "UltimateSDUpscaleCustomSample", "UltimateSDUpscalePipe"],
  {"title_aux": "UltimateSDUpscale", "author": "ssitu"}
 ],
 "https://github.com/mobcat40/ComfyUI-PromptChain": [
  ["UltimateSDUpscale", "PromptChain", "PromptChainLoad", "PromptChainSave", "PromptChainMerge",
   "PromptChainSplit", "PromptChainRandom", "PromptChainWeight", "PromptChainStyle",
   "PromptChainPreview", "PromptChainBatch", "PromptChainFilter"],
  {"title_aux": "ComfyUI-PromptChain", "author": "mobcat40"}
 ]
}`

// fakeManagerGetlistJSON is Manager's pack catalogue — where the repository URL, the
// title and the installability of each claimant come from. Both packs are
// "not-installed" so both keep a working Install button: the alternatives disclosure
// must stay REACHABLE and actionable, not merely present.
const fakeManagerGetlistJSON = `{
 "channel": "default",
 "node_packs": {
  "https://github.com/ssitu/ComfyUI_UltimateSDUpscale": {
   "id": "https://github.com/ssitu/ComfyUI_UltimateSDUpscale",
   "title": "UltimateSDUpscale",
   "author": "ssitu",
   "reference": "https://github.com/ssitu/ComfyUI_UltimateSDUpscale",
   "repository": "https://github.com/ssitu/ComfyUI_UltimateSDUpscale",
   "files": ["https://github.com/ssitu/ComfyUI_UltimateSDUpscale"],
   "install_type": "git-clone",
   "state": "not-installed",
   "version": "1.0.0",
   "cnr_latest": "1.0.0",
   "trust": true
  },
  "https://github.com/mobcat40/ComfyUI-PromptChain": {
   "id": "https://github.com/mobcat40/ComfyUI-PromptChain",
   "title": "ComfyUI-PromptChain",
   "author": "mobcat40",
   "reference": "https://github.com/mobcat40/ComfyUI-PromptChain",
   "repository": "https://github.com/mobcat40/ComfyUI-PromptChain",
   "files": ["https://github.com/mobcat40/ComfyUI-PromptChain"],
   "install_type": "git-clone",
   "state": "not-installed",
   "version": "nightly",
   "cnr_latest": "nightly",
   "trust": true
  }
 }
}`

const fakeObjectInfoJSON = `{
  "CheckpointLoaderSimple": {"input": {"required": {
    "ckpt_name": [["installed-sdxl-base.safetensors"], {}]
  }}, "input_order": {"required": ["ckpt_name"]}},
  "LoraLoader": {"input": {"required": {
    "lora_name": [["installed-detail-lora.safetensors"], {}],
    "model": ["MODEL", {}], "clip": ["CLIP", {}],
    "strength_model": ["FLOAT", {}], "strength_clip": ["FLOAT", {}]
  }}, "input_order": {"required": ["model", "clip", "lora_name", "strength_model", "strength_clip"]}},
  "CLIPTextEncode": {"input": {"required": {"text": ["STRING", {"multiline": true}], "clip": ["CLIP", {}]}},
    "input_order": {"required": ["text", "clip"]}},
  "EmptyLatentImage": {"input": {"required": {"width": ["INT", {}], "height": ["INT", {}], "batch_size": ["INT", {}]}},
    "input_order": {"required": ["width", "height", "batch_size"]}},
  "KSampler": {"input": {"required": {
    "model": ["MODEL", {}],
    "seed": ["INT", {"default": 0, "control_after_generate": true}],
    "steps": ["INT", {"default": 20}],
    "cfg": ["FLOAT", {"default": 8.0}],
    "sampler_name": [["euler", "dpmpp_2m", "ddim"], {}],
    "scheduler": [["normal", "karras", "simple"], {}],
    "positive": ["CONDITIONING", {}], "negative": ["CONDITIONING", {}],
    "latent_image": ["LATENT", {}],
    "denoise": ["FLOAT", {"default": 1.0}]
  }}, "input_order": {"required": [
    "model", "seed", "steps", "cfg", "sampler_name", "scheduler",
    "positive", "negative", "latent_image", "denoise"]}},
  "VAEDecode": {"input": {"required": {"samples": ["LATENT", {}], "vae": ["VAE", {}]}},
    "input_order": {"required": ["samples", "vae"]}},
  "SaveImage": {"input": {"required": {"images": ["IMAGE", {}], "filename_prefix": ["STRING", {}]}},
    "input_order": {"required": ["images", "filename_prefix"]}}
}`

// sameOrigin reports whether two URLs share scheme+host+port. Used to classify
// console/network events as first- vs third-party (civitai-manager is
// single-origin loopback, so nearly everything is first-party).
func sameOrigin(a, b string) bool {
	ua, err := url.Parse(a)
	if err != nil {
		return false
	}
	ub, err := url.Parse(b)
	if err != nil {
		return false
	}
	if ua.Scheme == "" || ub.Scheme == "" {
		// A relative/opaque event URL (no scheme) is treated as same-origin: it was
		// resolved against the page, so it belongs to the page's own origin.
		return true
	}
	return strings.EqualFold(ua.Scheme, ub.Scheme) && strings.EqualFold(ua.Host, ub.Host)
}
