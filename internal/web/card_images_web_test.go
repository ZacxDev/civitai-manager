package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// cardImagesRawNoImages builds a GetModel raw body with a version that carries NO
// inline images, so the card-images endpoint has nothing to render.
func cardImagesRawNoImages(t *testing.T) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"id": 7, "name": "Great Model", "type": "LORA",
		"modelVersions": []any{
			map[string]any{"id": 11, "name": "v2", "baseModel": "SDXL"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// TestCardImagesCacheHitNoFetch proves the card-images endpoint renders the
// carousel from the model_cache WITHOUT any GetModel call when the model is already
// cached (the whole point of cache-first: an already-seen model costs no network).
func TestCardImagesCacheHitNoFetch(t *testing.T) {
	var calls int32
	reader := countingModelReader{calls: &calls, raw: modelCardRawJSON(t)}
	srv := newModelServer(t, reader)
	// Seed the model_cache so the endpoint is served entirely from cache.
	if err := srv.store.PutModelCache(7, "Great Model", modelCardRawJSON(t)); err != nil {
		t.Fatal(err)
	}

	body := getModelPage(t, srv, "/models/7/card-images")
	if atomic.LoadInt32(&calls) != 0 {
		t.Fatalf("cache hit must not call the reader, got %d calls", atomic.LoadInt32(&calls))
	}
	if !strings.Contains(body, "cm-carousel") {
		t.Errorf("cache-hit card-images should render a carousel:\n%s", body)
	}
}

// TestCardImagesCacheMissFetchesOnceAndCaches proves a cache MISS fetches exactly
// once, caches the result, and renders the carousel — and a SECOND request is then
// served from the cache without a further fetch.
func TestCardImagesCacheMissFetchesOnceAndCaches(t *testing.T) {
	var calls int32
	reader := countingModelReader{calls: &calls, raw: modelCardRawJSON(t)}
	srv := newModelServer(t, reader) // NOT seeded → cold cache

	body := getModelPage(t, srv, "/models/7/card-images")
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("cache miss should fetch exactly once, got %d", atomic.LoadInt32(&calls))
	}
	if !strings.Contains(body, "cm-carousel") {
		t.Errorf("cache-miss card-images should render a carousel:\n%s", body)
	}
	// Second render: served from the cache the first fetch populated (no new call).
	_ = getModelPage(t, srv, "/models/7/card-images")
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("second render should be cache-served, got %d calls", atomic.LoadInt32(&calls))
	}
}

// TestCardImagesNoImagesEmptyNode proves a model with no showcase images renders an
// EMPTY node (200, no carousel, no error box) so the card simply shows no carousel.
func TestCardImagesNoImagesEmptyNode(t *testing.T) {
	srv := newModelServer(t, errModelReader{})
	if err := srv.store.PutModelCache(7, "Great Model", cardImagesRawNoImages(t)); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/models/7/card-images", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("card-images (no images) = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "cm-carousel") {
		t.Errorf("a model with no images must render no carousel:\n%s", body)
	}
	if strings.TrimSpace(body) != "" {
		t.Errorf("no-images card-images should be an empty node, got %q", body)
	}
}

// TestCardImagesUnknownModelEmptyNode proves an offline/not-found model degrades to
// an empty node (no error chip), never breaking the card.
func TestCardImagesUnknownModelEmptyNode(t *testing.T) {
	srv := newModelServer(t, errModelReader{}) // GetModel errors, no cache
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/models/999/card-images", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("unknown model card-images = %d, want 200", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "cm-carousel") {
		t.Errorf("unknown model must render no carousel:\n%s", rec.Body.String())
	}
}

// TestCardImagesNSFWModeHonored proves the persisted NSFW mode is threaded into the
// carousel: blur mode blurs the NSFW tile (cm-blur affordance), show renders it
// plain (no cm-blur). modelCardRawJSON carries one safe + one NSFW image.
func TestCardImagesNSFWModeHonored(t *testing.T) {
	// Blur mode: the NSFW tile carries the cm-blur affordance.
	blurSrv := newModelServer(t, errModelReader{})
	if err := blurSrv.store.PutModelCache(7, "Great Model", modelCardRawJSON(t)); err != nil {
		t.Fatal(err)
	}
	if err := blurSrv.store.SetSetting(nsfwSettingKey, NSFWBlur); err != nil {
		t.Fatal(err)
	}
	blurBody := getModelPage(t, blurSrv, "/models/7/card-images")
	if !strings.Contains(blurBody, "cm-blur") {
		t.Errorf("blur mode should blur the NSFW tile (cm-blur):\n%s", blurBody)
	}

	// Show mode: no blur affordance — the NSFW tile renders plain.
	showSrv := newModelServer(t, errModelReader{})
	if err := showSrv.store.PutModelCache(7, "Great Model", modelCardRawJSON(t)); err != nil {
		t.Fatal(err)
	}
	if err := showSrv.store.SetSetting(nsfwSettingKey, NSFWShow); err != nil {
		t.Fatal(err)
	}
	showBody := getModelPage(t, showSrv, "/models/7/card-images")
	if strings.Contains(showBody, "cm-blur") {
		t.Errorf("show mode should render the NSFW tile plain (no cm-blur):\n%s", showBody)
	}
	if !strings.Contains(showBody, "cm-carousel") {
		t.Errorf("show mode should still render the carousel:\n%s", showBody)
	}
}

// TestSuggestionCardLazyCardImages proves each dashboard suggestion card wires the
// lazy card-images container (the STABLE #card-images-{id} div with the right
// hx-get), at the top of the card.
func TestSuggestionCardLazyCardImages(t *testing.T) {
	out := renderString(t, suggestionsList([]suggestion{
		{ModelID: 7, FileCount: 2, TotalBytes: 1 << 20, Name: "Great Model"},
	}, "csrf"))

	if !strings.Contains(out, `id="card-images-7"`) {
		t.Errorf("suggestion card should carry the lazy card-images container:\n%s", out)
	}
	if !strings.Contains(out, `hx-get="/models/7/card-images"`) {
		t.Errorf("card-images container should hx-get the card-images endpoint:\n%s", out)
	}
	if !strings.Contains(out, `class="cm-card-images-lazy"`) {
		t.Errorf("card-images container should be a collapsible cm-card-images-lazy block:\n%s", out)
	}
	// Placement: the carousel container renders BEFORE the title (top of the card).
	imgIdx := strings.Index(out, `id="card-images-7"`)
	titleIdx := strings.Index(out, "Great Model")
	if imgIdx < 0 || titleIdx < 0 || imgIdx > titleIdx {
		t.Errorf("card-images should render above the title (img=%d title=%d):\n%s", imgIdx, titleIdx, out)
	}
}
