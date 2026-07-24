package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// --- A. parseSearchImages ---

// searchRawJSON builds a SearchModels raw response body from a nested map.
func searchRawJSON(t *testing.T, items []any) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"items": items})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestParseSearchImages(t *testing.T) {
	// A representative response: item 1 has two versions (only the FIRST is used)
	// with an image, a video (excluded), and a second image; item 2 has an empty
	// images array; item 3 has no versions; item 4 has 10 images (cap check).
	tenImages := make([]any, 0, 10)
	for i := 0; i < 10; i++ {
		tenImages = append(tenImages, map[string]any{
			"url": "https://image.civitai.com/m4-" + string(rune('a'+i)) + ".jpeg", "nsfwLevel": 1, "type": "image",
		})
	}
	raw := searchRawJSON(t, []any{
		map[string]any{"id": 1, "modelVersions": []any{
			map[string]any{"id": 11, "images": []any{
				map[string]any{"url": "https://image.civitai.com/m1-a.jpeg", "nsfwLevel": 1, "type": "image"},
				map[string]any{"url": "https://image.civitai.com/m1-vid.mp4", "nsfwLevel": 1, "type": "video"},
				map[string]any{"url": "https://image.civitai.com/m1-b.jpeg", "nsfwLevel": 4, "type": "image"},
			}},
			map[string]any{"id": 10, "images": []any{
				map[string]any{"url": "https://image.civitai.com/other-version.jpeg", "type": "image"},
			}},
		}},
		map[string]any{"id": 2, "modelVersions": []any{
			map[string]any{"id": 20, "images": []any{}},
		}},
		map[string]any{"id": 3, "modelVersions": []any{}},
		map[string]any{"id": 4, "modelVersions": []any{
			map[string]any{"id": 40, "images": tenImages},
		}},
	})

	got := parseSearchImages(raw)

	// Item 1: first version only, video excluded → 2 images.
	m1 := got[1]
	if len(m1) != 2 {
		t.Fatalf("model 1: want 2 images (video excluded), got %d", len(m1))
	}
	if m1[0].URL != "https://image.civitai.com/m1-a.jpeg" || m1[1].URL != "https://image.civitai.com/m1-b.jpeg" {
		t.Errorf("model 1 urls wrong: %+v", m1)
	}
	if m1[1].NSFWLevel != 4 {
		t.Errorf("model 1 second image nsfwLevel: want 4, got %d", m1[1].NSFWLevel)
	}
	// The second version's image must NOT leak in.
	for _, im := range m1 {
		if strings.Contains(im.URL, "other-version") {
			t.Errorf("second version's image leaked into model 1: %s", im.URL)
		}
	}
	// Item 2 (empty images) and item 3 (no versions) → absent from the map.
	if _, ok := got[2]; ok {
		t.Error("model 2 has no images and should be absent from the map")
	}
	if _, ok := got[3]; ok {
		t.Error("model 3 has no versions and should be absent from the map")
	}
	// Item 4: capped at searchImageCap.
	if len(got[4]) != searchImageCap {
		t.Errorf("model 4: want cap %d images, got %d", searchImageCap, len(got[4]))
	}
}

func TestParseSearchImagesEmptyRaw(t *testing.T) {
	if got := parseSearchImages(nil); got == nil || len(got) != 0 {
		t.Errorf("nil raw should give a non-nil empty map, got %v", got)
	}
	if got := parseSearchImages([]byte("not json")); len(got) != 0 {
		t.Errorf("garbage raw should give an empty map, got %v", got)
	}
}

// --- B. NSFW modes on cards ---

func TestModelCardNSFWModes(t *testing.T) {
	const safeURL = "https://image.civitai.com/safe.jpeg"
	const nsfwURL = "https://image.civitai.com/nsfw.jpeg"
	images := []galleryImage{
		{URL: safeURL, NSFWLevel: 1},  // safe (None/PG)
		{URL: nsfwURL, NSFWLevel: 32}, // NSFW (XXX)
	}
	it := civitai.ModelListItem{ID: 5, Name: "Card Model", Type: "LORA"}

	t.Run("show renders nsfw plain", func(t *testing.T) {
		out := renderString(t, modelCard(it, images, NSFWShow))
		if !strings.Contains(out, nsfwURL) {
			t.Error("show mode should render the NSFW image url")
		}
		if strings.Contains(out, "click to reveal") {
			t.Error("show mode should not blur/gate the NSFW image")
		}
		if !strings.Contains(out, safeURL) {
			t.Error("safe image must always render")
		}
	})

	t.Run("blur renders nsfw blurred", func(t *testing.T) {
		out := renderString(t, modelCard(it, images, NSFWBlur))
		if !strings.Contains(out, nsfwURL) {
			t.Error("blur mode still renders the url (behind a reveal overlay)")
		}
		if !strings.Contains(out, "click to reveal") {
			t.Error("blur mode should gate the NSFW image behind a reveal overlay")
		}
		if !strings.Contains(out, "blur-xl") {
			t.Error("blur mode should blur the NSFW image")
		}
		if !strings.Contains(out, safeURL) {
			t.Error("safe image must always render")
		}
	})

	t.Run("hide omits nsfw url server-side", func(t *testing.T) {
		out := renderString(t, modelCard(it, images, NSFWHide))
		if strings.Contains(out, nsfwURL) {
			t.Error("hide mode MUST omit the NSFW image url server-side")
		}
		if !strings.Contains(out, safeURL) {
			t.Error("safe image must still render under hide")
		}
	})
}

// --- C. Popular default + cache ---

// recordingSearchReader records the url.Values passed to SearchModels and counts
// calls, returning a fixed result. It reuses fakeReader for the other methods.
type recordingSearchReader struct {
	fakeReader
	mu     sync.Mutex
	calls  []url.Values
	result *civitai.ModelSearchResult
}

func (r *recordingSearchReader) SearchModels(_ context.Context, q url.Values) (*civitai.ModelSearchResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, q)
	return r.result, nil
}

func (r *recordingSearchReader) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

// popularResult builds a one-item search result with a safe showcase image.
func popularResult(t *testing.T) *civitai.ModelSearchResult {
	t.Helper()
	raw := searchRawJSON(t, []any{
		map[string]any{"id": 77, "name": "Popular Model", "type": "LORA", "modelVersions": []any{
			map[string]any{"id": 770, "images": []any{
				map[string]any{"url": "https://image.civitai.com/pop.jpeg", "nsfwLevel": 1, "type": "image"},
			}},
		}},
	})
	return &civitai.ModelSearchResult{
		Items: []civitai.ModelListItem{{ID: 77, Name: "Popular Model", Type: "LORA"}},
		Raw:   raw,
	}
}

func TestPopularDefaultAndCache(t *testing.T) {
	reader := &recordingSearchReader{result: popularResult(t)}
	srv := newModelServer(t, reader)

	getSearch := func() string {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/search", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /search = %d", rec.Code)
		}
		return rec.Body.String()
	}

	body := getSearch()
	if !strings.Contains(body, "Popular this month") {
		t.Error("empty-query search should show the popular-this-month heading")
	}
	if !strings.Contains(body, "Popular Model") {
		t.Error("empty-query search should render the popular cards")
	}
	if !strings.Contains(body, "https://image.civitai.com/pop.jpeg") {
		t.Error("popular card should render its showcase image")
	}

	// The first fetch used the documented popular query params.
	if reader.callCount() != 1 {
		t.Fatalf("want exactly 1 SearchModels call on first load, got %d", reader.callCount())
	}
	reader.mu.Lock()
	q := reader.calls[0]
	reader.mu.Unlock()
	if q.Get("sort") != "Most Downloaded" || q.Get("period") != "Month" || q.Get("limit") != "24" {
		t.Errorf("popular query params wrong: %v", q)
	}
	if q.Get("query") != "" {
		t.Errorf("popular fetch must not carry a query, got %q", q.Get("query"))
	}

	// Second load within the TTL is served from cache — no extra API call.
	_ = getSearch()
	if reader.callCount() != 1 {
		t.Fatalf("second load within TTL should be cached; SearchModels calls = %d, want 1", reader.callCount())
	}
}

// --- D. Dashboard structure + subscribe search ---

func TestDashboardManualFormDemotedAndSearchBox(t *testing.T) {
	out := renderString(t, dashboardPage(nil, nil, "test-csrf", "dark"))
	for _, want := range []string{
		"<details",                   // manual form is demoted into a details
		"Add by model id / URL",      // the summary label
		`id="subscribe-results"`,     // integrated search results container
		`name="q"`,                   // the search box
		`hx-get="/subscribe/search"`, // wired to the subscribe-search route
		`hx-post="/subscribe"`,       // the manual form still posts /subscribe
	} {
		if !strings.Contains(out, want) {
			t.Errorf("dashboard missing %q", want)
		}
	}
}

func TestSubscribeSearchRendersSubscribeCards(t *testing.T) {
	reader := &recordingSearchReader{result: popularResult(t)}
	srv := newModelServer(t, reader)

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/subscribe/search?q=popular", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /subscribe/search = %d", rec.Code)
	}
	out := rec.Body.String()
	if !strings.Contains(out, "Popular Model") {
		t.Error("subscribe search should render the result card")
	}
	// A one-click Subscribe control with auto_download=true + CSRF posting /subscribe.
	if !strings.Contains(out, `hx-post="/subscribe"`) {
		t.Error("subscribe card missing the /subscribe POST")
	}
	if !strings.Contains(out, `name="auto_download"`) || !strings.Contains(out, `value="true"`) {
		t.Error("subscribe card must set auto_download=true")
	}
	if !strings.Contains(out, srv.csrf) {
		t.Error("subscribe card must carry the CSRF token")
	}
	// It carried the typed query (not the popular params).
	reader.mu.Lock()
	q := reader.calls[len(reader.calls)-1]
	reader.mu.Unlock()
	if q.Get("query") != "popular" {
		t.Errorf("subscribe search query wrong: %v", q)
	}
}

// --- E. librarySubscribeSuggestions (pure) ---

func TestLibrarySubscribeSuggestions(t *testing.T) {
	files := []store.LocalFile{
		{ModelID: intPtr(1), SizeBytes: 100},
		{ModelID: intPtr(1), SizeBytes: 50},  // model 1 total 150, 2 files
		{ModelID: intPtr(2), SizeBytes: 500}, // model 2 total 500
		{ModelID: intPtr(3), SizeBytes: 999}, // model 3 — already subscribed, excluded
		{ModelID: nil, SizeBytes: 10000},     // unmatched — ignored
	}
	subbed := 3
	subs := []store.Subscription{
		{Kind: store.KindModel, ModelID: &subbed},
		{Kind: store.KindCreator, Username: "x"}, // creator sub — irrelevant
	}

	got := librarySubscribeSuggestions(files, subs, 12)

	if len(got) != 2 {
		t.Fatalf("want 2 suggestions (3 is subscribed, nil is unmatched), got %d: %+v", len(got), got)
	}
	// Ordered by total bytes desc: model 2 (500) before model 1 (150).
	if got[0].ModelID != 2 || got[0].TotalBytes != 500 {
		t.Errorf("first suggestion should be model 2 (500 bytes), got %+v", got[0])
	}
	if got[1].ModelID != 1 || got[1].TotalBytes != 150 || got[1].FileCount != 2 {
		t.Errorf("second suggestion should be model 1 (150 bytes, 2 files), got %+v", got[1])
	}
	for _, sg := range got {
		if sg.ModelID == 3 {
			t.Error("model 3 is already subscribed and must be excluded")
		}
	}
}

func TestLibrarySubscribeSuggestionsCap(t *testing.T) {
	var files []store.LocalFile
	for i := 1; i <= 20; i++ {
		files = append(files, store.LocalFile{ModelID: intPtr(i), SizeBytes: int64(i)})
	}
	got := librarySubscribeSuggestions(files, nil, 5)
	if len(got) != 5 {
		t.Fatalf("cap not applied: want 5, got %d", len(got))
	}
	// Largest bytes first: model 20 then 19...
	if got[0].ModelID != 20 || got[4].ModelID != 16 {
		t.Errorf("cap should keep the top-5 by bytes desc, got %d..%d", got[0].ModelID, got[4].ModelID)
	}
}

// --- F. Suggestions rendering ---

func TestDashboardRendersSuggestions(t *testing.T) {
	suggestions := []suggestion{
		{ModelID: 42, FileCount: 2, TotalBytes: 1500},
		{ModelID: 7, FileCount: 1, TotalBytes: 500},
	}
	out := renderString(t, dashboardPage(nil, suggestions, "test-csrf", "dark"))
	if !strings.Contains(out, "Subscribe suggestions from your library") {
		t.Error("suggestions section heading missing")
	}
	for _, want := range []string{"Model #42", "Model #7", "/models/42", "/models/7"} {
		if !strings.Contains(out, want) {
			t.Errorf("suggestions missing %q", want)
		}
	}
	// One-click Subscribe with auto_download=true.
	if !strings.Contains(out, `name="auto_download"`) || !strings.Contains(out, `value="true"`) {
		t.Error("suggestion cards must offer one-click auto-download Subscribe")
	}
}

func TestDashboardHidesEmptySuggestions(t *testing.T) {
	out := renderString(t, dashboardPage(nil, nil, "test-csrf", "dark"))
	if strings.Contains(out, "Subscribe suggestions from your library") {
		t.Error("suggestions section should be hidden when there are none")
	}
}
