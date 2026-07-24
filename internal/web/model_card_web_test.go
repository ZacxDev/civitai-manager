package web

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// countingModelReader wraps a fakeReader but counts GetModel calls and returns a
// caller-supplied raw model body, so a test can prove the model_cache serves the
// second render without a second API call.
type countingModelReader struct {
	fakeReader
	calls *int32
	raw   []byte
}

func (c countingModelReader) GetModel(context.Context, string) (*civitai.ModelDetail, []byte, error) {
	atomic.AddInt32(c.calls, 1)
	var m civitai.ModelDetail
	if err := json.Unmarshal(c.raw, &m); err != nil {
		return nil, nil, err
	}
	return &m, c.raw, nil
}

// modelCardRawJSON builds a GetModel raw body with a name/type + one version
// carrying inline showcase images (a safe + an NSFW image).
func modelCardRawJSON(t *testing.T) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"id": 7, "name": "Great Model", "type": "LORA",
		"modelVersions": []any{
			map[string]any{"id": 11, "name": "v2", "baseModel": "SDXL",
				"images": inlineImagesJSON([]tImg{
					{url: "https://image.civitai.com/safe.jpeg", level: "1", prompt: "a cat"},
					{url: "https://image.civitai.com/nsfw.jpeg", level: "32", prompt: "spicy"},
				})},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// TestHandleModelCardEnrichesAndCaches proves the lazy card endpoint renders the
// name + carousel + details from GetModel on a miss, and serves the SECOND
// render from the model_cache without a second API call.
func TestHandleModelCardEnrichesAndCaches(t *testing.T) {
	var calls int32
	reader := countingModelReader{calls: &calls, raw: modelCardRawJSON(t)}
	srv := newModelServer(t, reader)
	// Seed a local file for model 7 so the card shows a real file count + size.
	if err := srv.store.UpsertLocalFile(store.LocalFile{
		Path: "/m/great.safetensors", ModelID: intPtr(7), VersionID: intPtr(11),
		SizeBytes: 3 * 1024 * 1024 * 1024, Status: store.LocalStatusMatched, Kind: store.LocalKindModel,
	}); err != nil {
		t.Fatal(err)
	}

	body := getModelPage(t, srv, "/library/model-card/7")
	for _, want := range []string{
		"Great Model",  // name (not "#id")
		"LORA", "SDXL", // type + base model details
		"cm-carousel",                     // the carousel
		"safe.jpeg",                       // showcase images rendered
		"Versions", "Local files", "Size", // key details
		"/models/7", // link to the model page
	} {
		if !strings.Contains(body, want) {
			t.Errorf("model card missing %q", want)
		}
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("GetModel called %d times on first render, want 1", got)
	}

	// The snapshot was cached.
	if ent, _ := srv.store.GetModelCache(7); ent == nil || ent.Name != "Great Model" {
		t.Fatalf("model 7 should be cached with its name, got %+v", ent)
	}

	// Second render → served from cache, NO second API call.
	_ = getModelPage(t, srv, "/library/model-card/7")
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("second render must be served from cache; GetModel called %d times", got)
	}
}

// TestModelCardCarouselRespectsNSFW proves the carousel honors the persisted
// display mode: blur obscures the NSFW image behind click-to-reveal, hide omits
// it, show reveals it — never re-flagging or exposing NSFW.
func TestModelCardCarouselRespectsNSFW(t *testing.T) {
	imgs := []galleryImage{
		{URL: "https://image.civitai.com/safe.jpeg", NSFWLevel: 1},
		{URL: "https://image.civitai.com/nsfw.jpeg", NSFWLevel: 32},
	}

	blur := renderString(t, modelCardCarousel(7, imgs, NSFWBlur))
	if !strings.Contains(blur, "nsfw.jpeg") || !strings.Contains(blur, `data-blurred="1"`) || !strings.Contains(blur, "blur-xl") {
		t.Error("blur mode: NSFW image should be present but blurred")
	}
	if !strings.Contains(blur, "click to reveal") {
		t.Error("blur mode: blurred image should offer click-to-reveal")
	}
	if !strings.Contains(blur, "safe.jpeg") {
		t.Error("blur mode: safe image should be present")
	}

	hide := renderString(t, modelCardCarousel(7, imgs, NSFWHide))
	if strings.Contains(hide, "nsfw.jpeg") {
		t.Error("hide mode: NSFW image must be omitted")
	}
	if !strings.Contains(hide, "safe.jpeg") {
		t.Error("hide mode: safe image should still show")
	}

	show := renderString(t, modelCardCarousel(7, imgs, NSFWShow))
	if !strings.Contains(show, "nsfw.jpeg") {
		t.Error("show mode: NSFW image should be included")
	}
	if strings.Contains(show, `data-blurred="1"`) {
		t.Error("show mode: nothing should be blurred")
	}
}

// TestHandleModelCardCarouselHonorsPersistedNSFW proves the endpoint reads the
// persisted nsfw_display setting (hide) rather than defaulting.
func TestHandleModelCardCarouselHonorsPersistedNSFW(t *testing.T) {
	var calls int32
	reader := countingModelReader{calls: &calls, raw: modelCardRawJSON(t)}
	srv := newModelServer(t, reader)
	if err := srv.store.SetSetting(nsfwSettingKey, NSFWHide); err != nil {
		t.Fatal(err)
	}
	body := getModelPage(t, srv, "/library/model-card/7")
	if strings.Contains(body, "nsfw.jpeg") {
		t.Error("hide mode: the endpoint must omit the NSFW showcase image")
	}
	if !strings.Contains(body, "safe.jpeg") {
		t.Error("hide mode: the safe showcase image should still render")
	}
}

// TestMatchedModelsOrderedBySizeAndLazy proves matched models come first, ordered
// by total local size descending, each as a lazy-loading card.
func TestMatchedModelsOrderedBySizeAndLazy(t *testing.T) {
	files := []store.LocalFile{
		{Path: "/m/small.safetensors", ModelID: intPtr(1), SizeBytes: 1 * 1024 * 1024 * 1024,
			Status: store.LocalStatusMatched, Kind: store.LocalKindModel},
		{Path: "/m/big.safetensors", ModelID: intPtr(2), SizeBytes: 5 * 1024 * 1024 * 1024,
			Status: store.LocalStatusMatched, Kind: store.LocalKindModel},
		{Path: "/m/orphan.safetensors", SizeBytes: 100, Status: store.LocalStatusUnmatched, Kind: store.LocalKindModel},
	}

	matched, unmatched := splitMatchedUnmatched(files)
	if len(matched) != 2 || matched[0].modelID != 2 || matched[1].modelID != 1 {
		t.Fatalf("matched groups should be size-desc [2,1], got %+v", matched)
	}
	if len(unmatched) != 1 {
		t.Fatalf("expected 1 unmatched file, got %d", len(unmatched))
	}

	out := renderString(t, libraryContent(buildLibraryView(files), "csrf"))
	// Lazy-load markup on the model cards.
	for _, want := range []string{
		`hx-get="/library/model-card/2"`,
		`hx-get="/library/model-card/1"`,
		`hx-trigger="load"`,
		"Matched models",
		"Other files",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("results view missing %q", want)
		}
	}
	// The bigger model (2) card must appear BEFORE the smaller (1).
	if strings.Index(out, `model-card/2`) > strings.Index(out, `model-card/1`) {
		t.Error("matched model cards should be ordered biggest-first")
	}
	// The lazy card container carries the outerHTML swap.
	if !strings.Contains(out, `hx-swap="outerHTML"`) {
		t.Error("lazy model card should replace itself via outerHTML")
	}
}

// TestModelCardLazyMarkup is a focused guard on the lazy placeholder's htmx wiring.
func TestModelCardLazyMarkup(t *testing.T) {
	gr := fileGroup{modelID: 42, files: []store.LocalFile{
		{SizeBytes: 2 * 1024 * 1024 * 1024, ModelID: intPtr(42)},
	}}
	out := renderString(t, modelCardLazy(gr))
	for _, want := range []string{
		`hx-get="/library/model-card/42"`,
		`hx-trigger="load"`,
		`hx-swap="outerHTML"`,
		"Loading details",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("lazy card markup missing %q", want)
		}
	}
}
