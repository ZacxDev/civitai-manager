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
					{url: "https://image.civitai.com/nsfw.jpeg", level: "16", prompt: "spicy"},
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
		"cm-carousel",                            // the carousel
		"safe.jpeg",                              // showcase images rendered
		"Versions in your library", "Total size", // version breakdown
		"Subscribe", // subscribe toggle
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

// TestMatchedCardAuthorLink proves the matched card renders an @username link to
// the creator page when the model has a creator, and renders no author link (no
// panic) when the creator is nil.
func TestMatchedCardAuthorLink(t *testing.T) {
	withCreator := matchedModelCardView{ModelID: 7, Name: "Great Model", Creator: "carol"}
	out := renderString(t, matchedModelCard(withCreator, "csrf"))
	if !strings.Contains(out, `href="/creators/carol"`) || !strings.Contains(out, "@carol") {
		t.Errorf("card with a creator should link @carol to /creators/carol:\n%s", out)
	}

	noCreator := matchedModelCardView{ModelID: 8, Name: "Anon Model"}
	out = renderString(t, matchedModelCard(noCreator, "csrf"))
	if strings.Contains(out, "/creators/") {
		t.Errorf("card without a creator should render no author link:\n%s", out)
	}
}

// TestBuildMatchedCardViewResolvesCreator proves buildMatchedModelCardView pulls
// the creator username from a non-nil Creator and leaves it empty otherwise.
func TestBuildMatchedCardViewResolvesCreator(t *testing.T) {
	m := &civitai.ModelDetail{ID: 7, Name: "M", Creator: &civitai.Creator{Username: "carol"}}
	if v := buildMatchedModelCardView(7, m, nil, nil, fullMaturityRange(), nil); v.Creator != "carol" {
		t.Errorf("Creator = %q, want carol", v.Creator)
	}
	m2 := &civitai.ModelDetail{ID: 8, Name: "M"}
	if v := buildMatchedModelCardView(8, m2, nil, nil, fullMaturityRange(), nil); v.Creator != "" {
		t.Errorf("Creator = %q, want empty for nil creator", v.Creator)
	}
}

// TestModelCardCarouselRespectsTheRange proves the carousel OMITS out-of-band
// images server-side and renders in-band ones plain — no blur, no reveal overlay.
//
// The fixture deliberately includes a level-32 image: 32 is CivitAI's Blocked
// bucket, not a step on the user-selectable scale, so it must be omitted at EVERY
// range including the full one.
func TestModelCardCarouselRespectsTheRange(t *testing.T) {
	imgs := []galleryImage{
		{URL: "https://image.civitai.com/pg.jpeg", NSFWLevel: 1},
		{URL: "https://image.civitai.com/x.jpeg", NSFWLevel: 8},
		{URL: "https://image.civitai.com/xxx.jpeg", NSFWLevel: 16},
		{URL: "https://image.civitai.com/blocked.jpeg", NSFWLevel: 32},
	}

	full := renderString(t, modelCardCarousel(7, imgs, fullMaturityRange()))
	for _, want := range []string{"pg.jpeg", "x.jpeg", "xxx.jpeg"} {
		if !strings.Contains(full, want) {
			t.Errorf("the full range should render %s", want)
		}
	}
	if strings.Contains(full, "blocked.jpeg") {
		t.Error("a level-32 (Blocked) image must be omitted even at the full range")
	}
	for _, dead := range []string{"cm-blur", `data-blurred="1"`, ">reveal<"} {
		if strings.Contains(full, dead) {
			t.Errorf("the carousel still emits the dead blur marker %q", dead)
		}
	}

	// X only — and the XXX image must NOT come along, even though CivitAI labels
	// both "X" on the images API.
	xOnly := renderString(t, modelCardCarousel(7, imgs, maturityRange{maturityX, maturityX}))
	if !strings.Contains(xOnly, "x.jpeg") {
		t.Error("an X-only range should render the level-8 image")
	}
	if strings.Contains(xOnly, "xxx.jpeg") {
		t.Error("an X-only range LEAKED the level-16 (XXX) image")
	}
	if strings.Contains(xOnly, "pg.jpeg") {
		t.Error("an X-only range LEAKED the PG image")
	}

	// Nothing in band → the honest empty line, not an empty strip.
	none := renderString(t, modelCardCarousel(7,
		[]galleryImage{{URL: "https://image.civitai.com/xxx.jpeg", NSFWLevel: 16}},
		maturityRange{maturityPG, maturityPG}))
	if !strings.Contains(none, "No showcase images.") {
		t.Errorf("a carousel with nothing in band should say so:\n%s", none)
	}
	if strings.Contains(none, "xxx.jpeg") {
		t.Error("the empty-state carousel LEAKED the omitted URL")
	}
}

// TestHandleModelCardCarouselHonorsPersistedRange proves the endpoint reads the
// persisted maturity_range setting rather than defaulting.
func TestHandleModelCardCarouselHonorsPersistedRange(t *testing.T) {
	var calls int32
	reader := countingModelReader{calls: &calls, raw: modelCardRawJSON(t)}
	srv := newModelServer(t, reader)
	if err := srv.store.SetSetting(maturitySettingKey, "pg:pg"); err != nil {
		t.Fatal(err)
	}
	body := getModelPage(t, srv, "/library/model-card/7")
	if strings.Contains(body, "nsfw.jpeg") {
		t.Errorf("a PG-only range LEAKED the NSFW showcase image:\n%s", body)
	}
	if !strings.Contains(body, "safe.jpeg") {
		t.Error("the PG showcase image should still render")
	}
	if strings.Contains(body, `data-blurred="1"`) {
		t.Error("the endpoint must not blur any showcase image — blur is gone")
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
		"Matched models (",
		"Unmatched (",
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
	out := renderString(t, modelCardLazy(gr, ""))
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
