package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// fakeReader is a configurable civitai.Reader for the model-page tests: no
// network, deterministic model/version payloads. Showcase images are carried in
// the version raw JSON (verRaw) as an inline images[] array — NOT via
// SearchImages, which the page path must never call.
type fakeReader struct {
	model    *civitai.ModelDetail
	modelRaw []byte
	version  *civitai.ModelVersionDetail
	verRaw   []byte
	// searchHits counts SearchImages calls. The model page RENDER must never touch
	// the slow /api/v1/images endpoint, so it must stay 0 on the page path
	// (regression guard for the perf bug). By default SearchImages returns an error
	// to prove the page does not depend on it; the lazy community-feed handler is
	// exercised separately by setting communityRaw/communityErr below.
	searchHits *int32
	// communityRaw, when non-nil, is the RAW /api/v1/images body SearchImages
	// returns — and it is the ONLY thing the community handler reads.
	//
	// 🔴 That is deliberate, not laziness. The typed civitai.ImageItem carries the
	// STRING nsfwLevel, which labels BOTH of the top two levels "X"; only the raw
	// body's numeric `browsingLevel` separates X from XXX. A fixture built from
	// typed items literally CANNOT express the case the maturity range exists to
	// handle, so the fake refuses to offer one. Build bodies with communityBody.
	//
	// An empty-but-present body (`{"items":[]}`) models the "no community images"
	// outcome. communityErr, when set, is returned instead.
	communityRaw []byte
	communityErr error
	// lastImageQuery, when non-nil, captures the url.Values of the most recent
	// SearchImages call so tests can assert the community query params.
	lastImageQuery *url.Values
}

func (f fakeReader) GetModel(context.Context, string) (*civitai.ModelDetail, []byte, error) {
	return f.model, f.modelRaw, nil
}
func (f fakeReader) GetModelVersion(context.Context, string) (*civitai.ModelVersionDetail, []byte, error) {
	return f.version, f.verRaw, nil
}
func (f fakeReader) GetModelVersionByHash(context.Context, string) (*civitai.ModelVersionDetail, []byte, error) {
	return nil, nil, civitai.ErrNotFound
}
func (f fakeReader) GetModelVersionsByHashes(context.Context, []string) ([]civitai.HashMatch, error) {
	return nil, nil
}
func (f fakeReader) SearchModels(context.Context, url.Values) (*civitai.ModelSearchResult, error) {
	return &civitai.ModelSearchResult{}, nil
}
func (f fakeReader) SearchCreators(context.Context, url.Values) (*civitai.CreatorSearchResult, error) {
	return &civitai.CreatorSearchResult{}, nil
}
func (f fakeReader) SearchImages(_ context.Context, q url.Values) (*civitai.ImageSearchResult, error) {
	if f.searchHits != nil {
		atomic.AddInt32(f.searchHits, 1)
	}
	if f.lastImageQuery != nil {
		*f.lastImageQuery = q
	}
	if f.communityErr != nil {
		return nil, f.communityErr
	}
	if f.communityRaw != nil {
		res, err := civitai.DecodeImageSearch(f.communityRaw)
		if err != nil {
			// A malformed fixture body is a test bug, not a scenario — surface it as
			// the reader error rather than silently rendering nothing.
			return nil, err
		}
		return res, nil
	}
	return nil, errors.New("SearchImages must not be called from the model page path")
}

// tImg is a test showcase image. level is the RAW JSON token for nsfwLevel (e.g.
// "1", "4", "32", `"garbage"`) so tests can exercise numeric, non-integer, and
// missing (level == "") levels. prompt seeds the inline generation meta.
type tImg struct {
	url    string
	level  string
	prompt string
}

// inlineImagesJSON builds the []any for an inline images[] array with numeric
// nsfwLevel + flat generation meta.
func inlineImagesJSON(imgs []tImg) []any {
	out := make([]any, 0, len(imgs))
	for _, im := range imgs {
		obj := map[string]any{
			"url":    im.url,
			"width":  512,
			"height": 512,
			"meta":   map[string]any{"prompt": im.prompt, "sampler": "Euler a", "seed": 12345, "steps": 20},
		}
		if im.level != "" {
			obj["nsfwLevel"] = json.RawMessage(im.level)
		}
		out = append(out, obj)
	}
	return out
}

// versionRawJSON builds a version-detail raw body carrying publishedAt + an
// inline images[] array (the shape GetModelVersion returns).
func versionRawJSON(t *testing.T, publishedAt string, imgs []tImg) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"publishedAt": publishedAt,
		"images":      inlineImagesJSON(imgs),
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func newModelReader(t *testing.T) fakeReader {
	t.Helper()
	model := &civitai.ModelDetail{
		ID: 7, Name: "Great Model", Type: "Checkpoint",
		Creator: &civitai.Creator{Username: "carol"},
		Tags:    []string{"anime", "portrait"},
		Stats:   civitai.ModelStats{DownloadCount: 1234, ThumbsUpCount: 56, CommentCount: 7},
		ModelVersions: []civitai.ModelVersionSummary{
			{ID: 11, Name: "v2", BaseModel: "SDXL"},
			{ID: 10, Name: "v1", BaseModel: "SD 1.5"},
		},
	}
	// The malicious description exercises the sanitizer end-to-end.
	modelRaw := []byte(`{"description":"<p>Nice model</p><script>alert(1)</script><img src=x onerror=alert(2)><a href=\"https://example.com\">link</a>"}`)
	version := &civitai.ModelVersionDetail{
		ID: 11, ModelID: 7, BaseModel: "SDXL",
		TrainedWords: []string{"mytoken", "secondword"},
		// TWO files, each with a real downloadUrl. The header download control only
		// PRINTS names/sizes/types in its multi-file MENU shape — one file renders a
		// bare "Download" button by design (headerDownloadControl) — so a one-file
		// fixture would make every "the page lists the version's files" assertion in
		// this package vacuous. Two files is also the realistic shape (a checkpoint
		// plus its VAE).
		Files: []civitai.ModelVersionFile{
			{ID: 1, Name: "great-model.safetensors", Type: "Model", SizeKB: 2 * 1024 * 1024,
				DownloadURL: "https://civitai.com/api/download/models/11"},
			{ID: 2, Name: "great-model.vae.pt", Type: "VAE", SizeKB: 300 * 1024,
				DownloadURL: "https://civitai.com/api/download/models/11?type=VAE"},
		},
	}
	// Inline showcase images with NUMERIC nsfwLevel: 1 = None/PG (safe),
	// 32 = XXX (NSFW).
	verRaw := versionRawJSON(t, "2026-01-15T00:00:00Z", []tImg{
		{url: "https://image.civitai.com/safe.jpeg", level: "1", prompt: "a fluffy cat"},
		{url: "https://image.civitai.com/nsfw.jpeg", level: "16", prompt: "spicy prompt"},
	})
	return fakeReader{
		model: model, modelRaw: modelRaw, version: version, verRaw: verRaw,
		searchHits: new(int32),
	}
}

func newModelServer(t *testing.T, reader civitai.Reader) *Server {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return NewServer(st, reader, stubSubscriber{}, Config{
		BaseURL: "https://civitai.com", DefaultPollInterval: time.Hour, Addr: "127.0.0.1:8787",
	}, nil)
}

func getModelPage(t *testing.T, srv *Server, target string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d", target, rec.Code)
	}
	return rec.Body.String()
}

func TestModelPageRendersRichDetail(t *testing.T) {
	srv := newModelServer(t, newModelReader(t))
	body := getModelPage(t, srv, "/models/7")

	for _, want := range []string{
		"Great Model", "Checkpoint", "@carol", // header + creator
		"1234", "56", "7", // stats
		"anime", "portrait", // tags
		"v2", "v1", "SDXL", // versions
		"mytoken", "secondword", // trigger words
		"great-model.safetensors", "2.0 GB", // file list + size
		"2026-01-15", // published date
		"Subscribe",  // subscribe affordance preserved
	} {
		if !strings.Contains(body, want) {
			t.Errorf("model page missing %q", want)
		}
	}
	// Trigger words are copy-able chips.
	if !strings.Contains(body, "cmCopy") || !strings.Contains(body, `data-copy="mytoken"`) {
		t.Errorf("trigger words should be copy-able chips")
	}
}

func TestModelDescriptionSanitized(t *testing.T) {
	srv := newModelServer(t, newModelReader(t))
	body := getModelPage(t, srv, "/models/7")

	if !strings.Contains(body, "Nice model") {
		t.Error("safe description text should survive")
	}
	if !strings.Contains(body, "https://example.com") {
		t.Error("safe link should survive")
	}
	// NB: the page legitimately contains <script> tags (htmx + the model-page
	// interaction script), so we assert on the description's specific injected
	// tokens — none of which appear elsewhere in the page. The "<script" element
	// stripping itself is covered by the sanitizer unit test.
	for _, bad := range []string{"alert(1)", "alert(2)", "onerror", "javascript:"} {
		if strings.Contains(body, bad) {
			t.Errorf("unsafe content %q survived sanitization:\n%s", bad, body)
		}
	}
}

// TestModelDescriptionCollapsible proves the description is wrapped in the
// collapsible container with a Read more toggle (item 3), the sanitized content
// still renders, and sanitization is unchanged (injected script/handler stripped).
func TestModelDescriptionCollapsible(t *testing.T) {
	srv := newModelServer(t, newModelReader(t))
	body := getModelPage(t, srv, "/models/7")

	for _, want := range []string{
		"cm-desc-collapsible", `data-collapsed="true"`,
		"cm-desc-content", "cm-desc-toggle", "cmToggleDesc",
		"Read more",
		"Nice model", // sanitized content survives inside the wrapper
	} {
		if !strings.Contains(body, want) {
			t.Errorf("collapsible description missing %q", want)
		}
	}
	// Sanitization must be unchanged: the injected <script>/onerror are stripped.
	for _, bad := range []string{"alert(1)", "alert(2)", "onerror"} {
		if strings.Contains(body, bad) {
			t.Errorf("unsafe content %q survived the collapsible wrapper", bad)
		}
	}
}

// TestModelTagsChipRow proves tags render as a compact inline chip row (item 4),
// not a standalone "Tags" card.
func TestModelTagsChipRow(t *testing.T) {
	srv := newModelServer(t, newModelReader(t))
	body := getModelPage(t, srv, "/models/7")

	if !strings.Contains(body, "cm-tag-chip") {
		t.Error("tags should render as inline cm-tag-chip chips")
	}
	if !strings.Contains(body, "anime") || !strings.Contains(body, "portrait") {
		t.Error("tag text should render")
	}
	if strings.Contains(body, ">Tags<") {
		t.Errorf("tags should not be a standalone 'Tags' card:\n%s", body)
	}
}

// TestModelNoTagsRendersNothing proves a model with no tags renders no chip row.
func TestModelNoTagsRendersNothing(t *testing.T) {
	reader := newModelReader(t)
	reader.model.Tags = nil
	srv := newModelServer(t, reader)
	body := getModelPage(t, srv, "/models/7")

	if strings.Contains(body, "cm-tag-chip") {
		t.Error("a model with no tags should render no chip row")
	}
}

// TestModelHeaderViewOnCivitai proves the header carries a hardened "View on
// CivitAI" link to {BaseURL}/models/{id} (item 5).
func TestModelHeaderViewOnCivitai(t *testing.T) {
	srv := newModelServer(t, newModelReader(t))
	body := getModelPage(t, srv, "/models/7")

	if !strings.Contains(body, `href="https://civitai.com/models/7"`) {
		t.Errorf("header should link to the civitai model page:\n%s", body)
	}
	if !strings.Contains(body, "View on CivitAI") {
		t.Error("header should show a 'View on CivitAI' affordance")
	}
	if !strings.Contains(body, `target="_blank"`) || !strings.Contains(body, `rel="noopener noreferrer"`) {
		t.Error("the external link must be target=_blank rel=noopener noreferrer")
	}
}

// TestModelShowcaseDefaultRangeShowsEverythingRatable proves the default (full)
// range renders every image whose level is on the scale, PLAIN — there is no blur
// left anywhere on the page.
func TestModelShowcaseDefaultRangeShowsEverythingRatable(t *testing.T) {
	srv := newModelServer(t, newModelReader(t))
	body := getModelPage(t, srv, "/models/7")

	if !strings.Contains(body, "safe.jpeg") {
		t.Fatal("the PG showcase image should render under the default range")
	}
	for _, dead := range []string{"cm-blur", `data-blurred="1"`, "cmReveal", ">reveal<"} {
		t.Run(dead, func(t *testing.T) {
			if strings.Contains(body, dead) {
				t.Errorf("the model page still emits the dead blur marker %q — blur was "+
					"replaced by server-side omission:\n%s", dead, body)
			}
		})
	}
}

// TestModelShowcaseOmitsOutOfRange proves the showcase carousel OMITS an image
// outside the band server-side: its URL must not be in the response at all.
func TestModelShowcaseOmitsOutOfRange(t *testing.T) {
	reader := newModelReader(t)
	reader.verRaw = versionRawJSON(t, "2026-01-15T00:00:00Z", []tImg{
		{url: "https://image.civitai.com/lvl-pg.jpeg", level: "1", prompt: "pg"},
		{url: "https://image.civitai.com/lvl-r.jpeg", level: "4", prompt: "r"},
		{url: "https://image.civitai.com/lvl-x.jpeg", level: "8", prompt: "x"},
		{url: "https://image.civitai.com/lvl-xxx.jpeg", level: "16", prompt: "xxx"},
	})
	srv := newModelServer(t, reader)

	cases := []struct {
		rng     string
		present []string
		absent  []string
	}{
		{"pg:xxx", []string{"lvl-pg.jpeg", "lvl-r.jpeg", "lvl-x.jpeg", "lvl-xxx.jpeg"}, nil},
		{"pg:pg", []string{"lvl-pg.jpeg"}, []string{"lvl-r.jpeg", "lvl-x.jpeg", "lvl-xxx.jpeg"}},
		{"pg:x", []string{"lvl-pg.jpeg", "lvl-r.jpeg", "lvl-x.jpeg"}, []string{"lvl-xxx.jpeg"}},
		{"xxx:xxx", []string{"lvl-xxx.jpeg"}, []string{"lvl-pg.jpeg", "lvl-r.jpeg", "lvl-x.jpeg"}},
	}
	for _, c := range cases {
		t.Run(c.rng, func(t *testing.T) {
			if err := srv.store.SetSetting(maturitySettingKey, c.rng); err != nil {
				t.Fatal(err)
			}
			body := getModelPage(t, srv, "/models/7")
			for _, u := range c.present {
				if !strings.Contains(body, u) {
					t.Errorf("range %s should render %s", c.rng, u)
				}
			}
			for _, u := range c.absent {
				if strings.Contains(body, u) {
					t.Errorf("range %s LEAKED %s — an out-of-range image URL must never reach "+
						"the DOM", c.rng, u)
				}
			}
		})
	}
}

// TestModelMaturityDefaultsToTheFullRange proves an absent / malformed stored
// value reads back as PG..XXX rather than silently narrowing.
func TestModelMaturityDefaultsToTheFullRange(t *testing.T) {
	srv := newModelServer(t, newModelReader(t))
	if got := srv.maturity(); got != fullMaturityRange() {
		t.Fatalf("with no stored setting, maturity() = %s, want the full range", got.String())
	}
	for _, junk := range []string{"", "blur", "show", "hide", "xxx:pg", "nope"} {
		if err := srv.store.SetSetting(maturitySettingKey, junk); err != nil {
			t.Fatal(err)
		}
		if got := srv.maturity(); got != fullMaturityRange() {
			t.Errorf("stored %q read back as %s, want the full range (fail-open)", junk, got.String())
		}
	}
}

// TestModelUnknownLevelFailsClosed proves an image whose numeric level is absent,
// garbage, or off the five-step scale (notably 32 = Blocked) is OMITTED at every
// range, including the full one. Fail-closed: an unrated image is never rendered
// on the assumption that it is tame.
func TestModelUnknownLevelFailsClosed(t *testing.T) {
	reader := newModelReader(t)
	reader.verRaw = versionRawJSON(t, "2026-01-15T00:00:00Z", []tImg{
		{url: "https://image.civitai.com/safe.jpeg", level: "1", prompt: "safe"},
		{url: "https://image.civitai.com/garbage.jpeg", level: `"SuperSpicy9000"`, prompt: "garbage level"},
		{url: "https://image.civitai.com/blocked.jpeg", level: "32", prompt: "blocked bucket"},
	})
	srv := newModelServer(t, reader)

	for _, rng := range []string{"pg:xxx", "pg:pg", "xxx:xxx"} {
		if err := srv.store.SetSetting(maturitySettingKey, rng); err != nil {
			t.Fatal(err)
		}
		body := getModelPage(t, srv, "/models/7")
		if strings.Contains(body, "garbage.jpeg") {
			t.Errorf("range %s rendered an image with an unparseable level — must fail closed", rng)
		}
		if strings.Contains(body, "blocked.jpeg") {
			t.Errorf("range %s rendered a level-32 (Blocked) image — Blocked is not a scale step", rng)
		}
	}

	// …and the genuinely-rated image is unaffected: we did not omit everything.
	if err := srv.store.SetSetting(maturitySettingKey, "pg:xxx"); err != nil {
		t.Fatal(err)
	}
	if body := getModelPage(t, srv, "/models/7"); !strings.Contains(body, "safe.jpeg") {
		t.Error("the PG image should still render — fail-closed must not mean fail-empty")
	}
}
func TestModelGalleryLightboxAndMetadata(t *testing.T) {
	srv := newModelServer(t, newModelReader(t))
	body := getModelPage(t, srv, "/models/7")

	for _, want := range []string{
		"cm-lightbox", "cmOpenLightbox", "cmTileClick", // lightbox wiring
		"cursor-zoom-in",                            // click-to-expand affordance
		"a fluffy cat", "Prompt", "Sampler", "Seed", // generation metadata
	} {
		if !strings.Contains(body, want) {
			t.Errorf("gallery/lightbox markup missing %q", want)
		}
	}
}

// TestMaturitySettingPersistsViaEndpoint covers the setter: a valid range
// persists and replies HX-Refresh, a missing CSRF token is 403, and an invalid or
// INVERTED range is 400 with nothing written.
func TestMaturitySettingPersistsViaEndpoint(t *testing.T) {
	srv := newModelServer(t, newModelReader(t))

	rec := post(t, srv, "/settings/maturity", url.Values{"min": {"pg"}, "max": {"r"}}, true)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("set maturity = %d, want 204", rec.Code)
	}
	if rec.Header().Get("HX-Refresh") != "true" {
		t.Errorf("set maturity must reply HX-Refresh: true, got %q", rec.Header().Get("HX-Refresh"))
	}
	if v, _ := srv.store.GetSettingDefault(maturitySettingKey, ""); v != "pg:r" {
		t.Fatalf("maturity setting = %q, want pg:r", v)
	}

	// Without CSRF → 403, and the stored value is untouched.
	rec = post(t, srv, "/settings/maturity", url.Values{"min": {"pg"}, "max": {"xxx"}}, false)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("set maturity without CSRF = %d, want 403", rec.Code)
	}
	if v, _ := srv.store.GetSettingDefault(maturitySettingKey, ""); v != "pg:r" {
		t.Fatalf("a CSRF-less POST changed the setting to %q", v)
	}

	// Inverted / unknown submissions are REJECTED, never swapped or clamped.
	for _, bad := range []url.Values{
		{"min": {"xxx"}, "max": {"pg"}},  // inverted
		{"min": {"r"}, "max": {"pg13"}},  // inverted
		{"min": {"pg"}, "max": {"pg14"}}, // unknown level
		{"min": {"blur"}, "max": {"xxx"}},
		{"max": {"xxx"}}, // missing end
	} {
		rec = post(t, srv, "/settings/maturity", bad, true)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("POST %v = %d, want 400", bad, rec.Code)
		}
		if v, _ := srv.store.GetSettingDefault(maturitySettingKey, ""); v != "pg:r" {
			t.Fatalf("a rejected POST %v changed the setting to %q", bad, v)
		}
	}
}

// TestModelPageBadgesOwnedVersions proves the version list marks the versions the
// user has locally with a green ✓ indicator (item 6) — accessible-labeled, not a
// text badge — and only those.
func TestModelPageBadgesOwnedVersions(t *testing.T) {
	srv := newModelServer(t, newModelReader(t))
	// The model has versions 11 (v2) and 10 (v1); the user owns only version 11.
	if err := srv.store.UpsertLocalFile(store.LocalFile{
		Path: "/m/great-v2.safetensors", ModelID: intPtr(7), VersionID: intPtr(11),
		SizeBytes: 1024, Status: store.LocalStatusMatched, Kind: store.LocalKindModel,
	}); err != nil {
		t.Fatal(err)
	}
	body := getModelPage(t, srv, "/models/7")

	if !strings.Contains(body, `aria-label="In your library"`) {
		t.Error("owned version should carry the accessible-labeled ✓ indicator")
	}
	// The old text badge must be gone.
	if strings.Contains(body, "in your library") {
		t.Error("the text badge should be replaced by the ✓ indicator")
	}
	// Exactly one version is owned, so exactly one indicator.
	if n := strings.Count(body, `aria-label="In your library"`); n != 1 {
		t.Errorf("expected exactly one owned-version indicator, got %d", n)
	}
}

// TestModelPageNoBadgeWhenNoLocalVersions proves versions the user does not own
// carry no library indicator.
func TestModelPageNoBadgeWhenNoLocalVersions(t *testing.T) {
	srv := newModelServer(t, newModelReader(t))
	body := getModelPage(t, srv, "/models/7")
	if strings.Contains(body, `aria-label="In your library"`) {
		t.Error("no local files → no version should carry the ✓ indicator")
	}
}

// TestModelPageNeverCallsSearchImages is the regression guard for the perf bug:
// the default model page must source its gallery from inline images and NEVER
// hit the slow /api/v1/images (SearchImages) endpoint. The fake's SearchImages
// both records the call and returns an error, so if the page path called it the
// gallery would be empty AND the counter would be non-zero.
func TestModelPageNeverCallsSearchImages(t *testing.T) {
	reader := newModelReader(t)
	srv := newModelServer(t, reader)
	body := getModelPage(t, srv, "/models/7")

	if got := atomic.LoadInt32(reader.searchHits); got != 0 {
		t.Fatalf("SearchImages was called %d times; the model page must never call it", got)
	}
	// ...and the gallery still rendered from the inline images.
	if !strings.Contains(body, "safe.jpeg") || !strings.Contains(body, "nsfw.jpeg") {
		t.Error("gallery should render from inline images without SearchImages")
	}
}

// TestModelGalleryEmptyStateWhenNoInlineImages proves a version that genuinely
// carries no inline images renders the truthful empty state (not a swallowed
// error), and still without any SearchImages call.
func TestModelGalleryEmptyStateWhenNoInlineImages(t *testing.T) {
	reader := newModelReader(t)
	reader.verRaw = versionRawJSON(t, "2026-01-15T00:00:00Z", nil) // no images[]
	srv := newModelServer(t, reader)
	body := getModelPage(t, srv, "/models/7")

	if !strings.Contains(body, "No showcase images") {
		t.Error("a version with no inline images should show the truthful empty state")
	}
	if strings.Contains(body, "safe.jpeg") {
		t.Error("no images should be rendered when the version carries none")
	}
	if got := atomic.LoadInt32(reader.searchHits); got != 0 {
		t.Fatalf("SearchImages called %d times on the empty-gallery path; must be 0", got)
	}
}

// TestModelGalleryFallsBackToModelRawImages proves the parser falls back to the
// matching version object inside the model raw JSON when the version raw carries
// no top-level images[].
func TestModelGalleryFallsBackToModelRawImages(t *testing.T) {
	reader := newModelReader(t)
	reader.verRaw = versionRawJSON(t, "2026-01-15T00:00:00Z", nil) // version raw: no images
	// Model raw carries the images inline under modelVersions[].images (id 11 is
	// the selected/latest version).
	modelRaw, err := json.Marshal(map[string]any{
		"description": "desc",
		"modelVersions": []any{
			map[string]any{"id": 11, "images": inlineImagesJSON([]tImg{
				{url: "https://image.civitai.com/fallback.jpeg", level: "1", prompt: "from model raw"},
			})},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	reader.modelRaw = modelRaw
	srv := newModelServer(t, reader)
	body := getModelPage(t, srv, "/models/7")

	if !strings.Contains(body, "fallback.jpeg") {
		t.Error("gallery should fall back to inline images in the model raw JSON")
	}
	if !strings.Contains(body, "from model raw") {
		t.Error("fallback image meta should render")
	}
}

// TestParseVersionImages unit-tests the inline-image parser directly: mixed
// numeric levels (1, 4, 32), a missing level, and a garbage level; URLs,
// dimensions, and meta are preserved; a body with no images → nil (not error).
func TestParseVersionImages(t *testing.T) {
	verRaw := versionRawJSON(t, "", []tImg{
		{url: "https://image.civitai.com/a.jpeg", level: "1", prompt: "safe"},
		{url: "https://image.civitai.com/b.jpeg", level: "4", prompt: "mature"},
		{url: "https://image.civitai.com/c.jpeg", level: "32", prompt: "xxx"},
		{url: "https://image.civitai.com/d.jpeg", level: "", prompt: "missing level"},
		{url: "https://image.civitai.com/e.jpeg", level: `"garbage"`, prompt: "garbage level"},
	})

	imgs := parseVersionImages(verRaw, nil, 11)
	if len(imgs) != 5 {
		t.Fatalf("parsed %d images, want 5", len(imgs))
	}
	wantLevels := []int{1, 4, 32, browsingLevelUnknown, browsingLevelUnknown}
	for i, want := range wantLevels {
		if imgs[i].NSFWLevel != want {
			t.Errorf("image %d level = %d, want %d", i, imgs[i].NSFWLevel, want)
		}
	}
	if imgs[0].URL != "https://image.civitai.com/a.jpeg" || imgs[0].Width != 512 || imgs[0].Height != 512 {
		t.Errorf("URL/dimensions not preserved: %+v", imgs[0])
	}
	meta, state := civitai.ImageItem{Meta: imgs[0].Meta}.ParseMeta()
	if state != civitai.MetaOK || meta.Prompt != "safe" {
		t.Errorf("meta not preserved: state=%v prompt=%q", state, meta.Prompt)
	}

	// A raw body with no images[] → nil, not an error.
	if got := parseVersionImages([]byte(`{"publishedAt":"x"}`), nil, 11); got != nil {
		t.Errorf("no-images body should yield nil, got %d", len(got))
	}
	// Empty/garbage raw → nil.
	if got := parseVersionImages(nil, nil, 11); got != nil {
		t.Errorf("nil raw should yield nil, got %d", len(got))
	}
}
