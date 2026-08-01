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
	// Any other ComfyUI call would mean the run got past preflight unexpectedly;
	// answer 404 loudly rather than silently succeeding.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "uxaudit fake ComfyUI: unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
	})
	return httptest.NewServer(mux)
}

// fakeObjectInfoJSON is the ComfyUI /object_info payload. Its loader combos list
// only INSTALLED files, none of which match the seeded workflow's references, so
// preflight reports every referenced model as missing. Every class_type the
// seeded graph uses is present here so none is flagged as a missing NODE (we want
// missing MODELS, the hero). The raw shape mirrors ComfyUI: each input is
// [choicesOrType, opts]; a string first element (e.g. "MODEL") is a typed slot,
// an array first element is a combo whose entries are the choices.
const fakeObjectInfoJSON = `{
  "CheckpointLoaderSimple": {"input": {"required": {
    "ckpt_name": [["installed-sdxl-base.safetensors"], {}]
  }}},
  "LoraLoader": {"input": {"required": {
    "lora_name": [["installed-detail-lora.safetensors"], {}],
    "model": ["MODEL", {}], "clip": ["CLIP", {}],
    "strength_model": ["FLOAT", {}], "strength_clip": ["FLOAT", {}]
  }}},
  "CLIPTextEncode": {"input": {"required": {"text": ["STRING", {}], "clip": ["CLIP", {}]}}},
  "EmptyLatentImage": {"input": {"required": {"width": ["INT", {}], "height": ["INT", {}], "batch_size": ["INT", {}]}}},
  "KSampler": {"input": {"required": {"model": ["MODEL", {}], "positive": ["CONDITIONING", {}], "negative": ["CONDITIONING", {}], "latent_image": ["LATENT", {}]}}},
  "VAEDecode": {"input": {"required": {"samples": ["LATENT", {}], "vae": ["VAE", {}]}}},
  "SaveImage": {"input": {"required": {"images": ["IMAGE", {}], "filename_prefix": ["STRING", {}]}}}
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
