package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// fakeReader is a configurable civitai.Reader for the model-page tests: no
// network, deterministic model/version/image payloads.
type fakeReader struct {
	model    *civitai.ModelDetail
	modelRaw []byte
	version  *civitai.ModelVersionDetail
	verRaw   []byte
	images   []civitai.ImageItem
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
func (f fakeReader) SearchModels(context.Context, url.Values) (*civitai.ModelSearchResult, error) {
	return &civitai.ModelSearchResult{}, nil
}
func (f fakeReader) SearchCreators(context.Context, url.Values) (*civitai.CreatorSearchResult, error) {
	return &civitai.CreatorSearchResult{}, nil
}
func (f fakeReader) SearchImages(context.Context, url.Values) (*civitai.ImageSearchResult, error) {
	return &civitai.ImageSearchResult{Items: f.images}, nil
}

func rawMeta(t *testing.T, prompt string) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(map[string]any{"prompt": prompt, "sampler": "Euler a", "seed": 12345, "steps": 20})
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
		Files: []civitai.ModelVersionFile{
			{ID: 1, Name: "great-model.safetensors", Type: "Model", SizeKB: 2 * 1024 * 1024},
		},
	}
	verRaw := []byte(`{"publishedAt":"2026-01-15T00:00:00Z"}`)
	images := []civitai.ImageItem{
		{ID: 100, URL: "https://image.civitai.com/safe.jpeg", Width: 512, Height: 512,
			NSFWLevel: "None", Meta: rawMeta(t, "a fluffy cat")},
		{ID: 200, URL: "https://image.civitai.com/nsfw.jpeg", Width: 512, Height: 512,
			NSFWLevel: "X", Meta: rawMeta(t, "spicy prompt")},
	}
	return fakeReader{model: model, modelRaw: modelRaw, version: version, verRaw: verRaw, images: images}
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

func TestModelNSFWBlurByDefault(t *testing.T) {
	srv := newModelServer(t, newModelReader(t))
	body := getModelPage(t, srv, "/models/7")

	if !strings.Contains(body, "safe.jpeg") || !strings.Contains(body, "nsfw.jpeg") {
		t.Fatal("both images should be present by default (blur mode)")
	}
	if !strings.Contains(body, `data-blurred="1"`) || !strings.Contains(body, "blur-xl") {
		t.Error("NSFW image should be blurred by default (blur-xl + data-blurred)")
	}
	if !strings.Contains(body, "click to reveal") {
		t.Error("blurred NSFW image should offer click-to-reveal")
	}
}

func TestModelNSFWShowUnblurs(t *testing.T) {
	srv := newModelServer(t, newModelReader(t))
	if err := srv.store.SetSetting(nsfwSettingKey, NSFWShow); err != nil {
		t.Fatal(err)
	}
	body := getModelPage(t, srv, "/models/7")
	if !strings.Contains(body, "nsfw.jpeg") {
		t.Error("show mode should include the NSFW image")
	}
	if strings.Contains(body, `data-blurred="1"`) {
		t.Error("show mode must not blur any image")
	}
}

func TestModelNSFWHideOmits(t *testing.T) {
	srv := newModelServer(t, newModelReader(t))
	if err := srv.store.SetSetting(nsfwSettingKey, NSFWHide); err != nil {
		t.Fatal(err)
	}
	body := getModelPage(t, srv, "/models/7")
	if strings.Contains(body, "nsfw.jpeg") {
		t.Error("hide mode should omit the NSFW image entirely")
	}
	if !strings.Contains(body, "safe.jpeg") {
		t.Error("hide mode should still show safe images")
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

func TestNSFWSettingPersistsViaEndpoint(t *testing.T) {
	srv := newModelServer(t, newModelReader(t))
	// Toggle via the endpoint (CSRF required).
	rec := post(t, srv, "/settings/nsfw", url.Values{"mode": {"show"}, "model_id": {"7"}}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("set nsfw = %d", rec.Code)
	}
	// The setting persisted and the re-rendered page reflects show mode.
	if v, _ := srv.store.GetSettingDefault(nsfwSettingKey, NSFWBlur); v != NSFWShow {
		t.Fatalf("nsfw setting = %q, want show", v)
	}
	if strings.Contains(rec.Body.String(), `data-blurred="1"`) {
		t.Error("after switching to show, the re-rendered page must not blur")
	}

	// Without CSRF → 403.
	rec = post(t, srv, "/settings/nsfw", url.Values{"mode": {"hide"}}, false)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("set nsfw without CSRF = %d, want 403", rec.Code)
	}
}
