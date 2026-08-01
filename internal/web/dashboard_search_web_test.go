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
	// A representative response:
	//   item 1: first version [image, VIDEO, image] — videos are now INCLUDED
	//           (poster thumbnail), so all 3 are kept; the 2nd version must not leak.
	//   item 2: single version with an empty images array → absent.
	//   item 3: no versions → absent.
	//   item 4: 10 images (cap check).
	//   item 5: first version has NO images, a LATER version does → scan to it.
	//   item 6: only a video (video-only model) → still yields a card.
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
		map[string]any{"id": 5, "modelVersions": []any{
			map[string]any{"id": 51, "images": []any{}}, // first version: no images
			map[string]any{"id": 52, "images": []any{
				map[string]any{"url": "https://image.civitai.com/m5-later.jpeg", "type": "image"},
			}},
		}},
		map[string]any{"id": 6, "modelVersions": []any{
			map[string]any{"id": 61, "images": []any{
				map[string]any{"url": "https://image.civitai.com/m6-only.mp4", "type": "video"},
			}},
		}},
	})

	got := parseSearchImages(raw)

	// Item 1: first version wins, VIDEO now included → 3 images in order.
	m1 := got[1]
	if len(m1) != 3 {
		t.Fatalf("model 1: want 3 images (video included), got %d", len(m1))
	}
	if m1[0].URL != "https://image.civitai.com/m1-a.jpeg" ||
		m1[1].URL != "https://image.civitai.com/m1-vid.mp4" ||
		m1[2].URL != "https://image.civitai.com/m1-b.jpeg" {
		t.Errorf("model 1 urls/order wrong: %+v", m1)
	}
	if !isVideoType(m1[1].Type) {
		t.Errorf("model 1 middle item should carry video Type, got %q", m1[1].Type)
	}
	if m1[2].NSFWLevel != 4 {
		t.Errorf("model 1 third image nsfwLevel: want 4, got %d", m1[2].NSFWLevel)
	}
	// The second version's image must NOT leak in (first version already yielded).
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
	// Item 5: first version imageless → scans to the later version's image.
	m5 := got[5]
	if len(m5) != 1 || m5[0].URL != "https://image.civitai.com/m5-later.jpeg" {
		t.Errorf("model 5: want the later version's single image, got %+v", m5)
	}
	// Item 6: video-only model still yields a (video-typed) tile.
	m6 := got[6]
	if len(m6) != 1 || m6[0].URL != "https://image.civitai.com/m6-only.mp4" || !isVideoType(m6[0].Type) {
		t.Errorf("model 6 (video-only): want one video tile, got %+v", m6)
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

// TestParseSearchImagesNewestHealthyVersion covers the version-selection policy:
// pick the NEWEST version (by publishedAt) with a healthy showcase
// (>= minShowcaseImages), skipping a more-recent-but-sparse variant; fall back to
// the richest version when none clears the bar. modelVersions[] order is
// default-first (NOT date-sorted), so selection must sort by date itself.
// TestParseSearchImagesPrimaryVersion: the card shows the creator's PRIMARY
// version (modelVersions[0], the version the detail page defaults to), NOT the
// newest by date. It scans to a later version only when the primary has no images.
func TestParseSearchImagesPrimaryVersion(t *testing.T) {
	imgs := func(n int, prefix string) []any {
		out := make([]any, 0, n)
		for i := 0; i < n; i++ {
			out = append(out, map[string]any{
				"url": "https://image.civitai.com/" + prefix + string(rune('a'+i)) + ".jpeg", "type": "image",
			})
		}
		return out
	}
	raw := searchRawJSON(t, []any{
		// Model 100: primary version [0] is OLDER than a later version, but the PRIMARY
		// (default) version's images must win — even though a newer version exists.
		map[string]any{"id": 100, "modelVersions": []any{
			map[string]any{"id": 1, "publishedAt": "2023-07-29T00:00:00.000Z", "images": imgs(5, "primary-")},
			map[string]any{"id": 2, "publishedAt": "2023-12-06T00:00:00.000Z", "images": imgs(5, "newer-")},
		}},
		// Model 200: primary version has NO images → scan to the next version so the
		// card is not empty.
		map[string]any{"id": 200, "modelVersions": []any{
			map[string]any{"id": 3, "images": []any{}},
			map[string]any{"id": 4, "images": imgs(3, "fallback-")},
		}},
	})

	got := parseSearchImages(raw)

	if m := got[100]; len(m) != 5 || !strings.Contains(m[0].URL, "primary-") {
		t.Errorf("model 100: want the PRIMARY version's images (not the newer one), got %+v", m)
	}
	if m := got[200]; len(m) != 3 || !strings.Contains(m[0].URL, "fallback-") {
		t.Errorf("model 200: primary has no images → want the next version's, got %+v", m)
	}
}

// --- B. NSFW modes on cards ---

// TestModelCardMaturityRange covers the shared search/dashboard card: an image
// outside the band is OMITTED (URL absent), one inside renders plain.
func TestModelCardMaturityRange(t *testing.T) {
	const pgURL = "https://image.civitai.com/lvl-pg.jpeg"
	const xxxURL = "https://image.civitai.com/lvl-xxx.jpeg"
	images := []galleryImage{
		{URL: pgURL, NSFWLevel: 1},
		{URL: xxxURL, NSFWLevel: 16},
	}
	it := civitai.ModelListItem{ID: 5, Name: "Card Model", Type: "LORA"}

	t.Run("full range renders both, plain", func(t *testing.T) {
		out := renderString(t, modelCard(it, images, nil, fullMaturityRange(), "test-csrf", modelUpdateInfo{}))
		if !strings.Contains(out, xxxURL) || !strings.Contains(out, pgURL) {
			t.Errorf("the full range should render both images:\n%s", out)
		}
		for _, dead := range []string{">reveal<", "cm-blur", `data-blurred="1"`} {
			if strings.Contains(out, dead) {
				t.Errorf("the card still emits the dead blur marker %q", dead)
			}
		}
	})

	t.Run("PG-only omits the XXX image", func(t *testing.T) {
		out := renderString(t, modelCard(it, images, nil,
			maturityRange{maturityPG, maturityPG}, "test-csrf", modelUpdateInfo{}))
		if strings.Contains(out, xxxURL) {
			t.Errorf("a PG-only band LEAKED the XXX image URL:\n%s", out)
		}
		if !strings.Contains(out, pgURL) {
			t.Error("the PG image must still render")
		}
	})

	t.Run("XXX-only omits the PG image", func(t *testing.T) {
		out := renderString(t, modelCard(it, images, nil,
			maturityRange{maturityXXX, maturityXXX}, "test-csrf", modelUpdateInfo{}))
		if strings.Contains(out, pgURL) {
			t.Errorf("an XXX-only band LEAKED the PG image URL:\n%s", out)
		}
		if !strings.Contains(out, xxxURL) {
			t.Error("the XXX image must render")
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

// TestHomeIsTheSearchPage pins the front door: GET "/" is the SEARCH experience.
//
// The brand wordmark links to "/" (brandLink), so whatever "/" resolves to is
// what the logo means. This asserts the redirect AND that its target actually
// serves a page — a redirect into a 404 would satisfy a Location check alone.
//
// 🔴 THE STATUS CODE IS PINNED, not just "some 3xx". A 301 is cached by the
// browser indefinitely: shipping one and changing our mind later would strand
// every user who had ever visited, with no server-side fix available. Same
// reasoning that keeps handleTrashRedirect on 302.
func TestHomeIsTheSearchPage(t *testing.T) {
	srv := newTestServer(t)

	rec := get(t, srv, "/")
	if rec.Code != http.StatusFound {
		t.Fatalf("GET / = %d, want 302 Found (301 would be cached indefinitely — see handleHome)", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/search" {
		t.Fatalf("GET / Location = %q, want /search", loc)
	}
	// The destination must really be the search page, not merely routed.
	dest := get(t, srv, "/search")
	if dest.Code != http.StatusOK {
		t.Fatalf("the redirect target /search returned %d", dest.Code)
	}
	if body := dest.Body.String(); !strings.Contains(body, `name="q"`) {
		t.Errorf("/search does not render a query box — the front door leads somewhere that is not search:\n%s",
			firstN(body, 800))
	}

	// 🔴 THE ABSENCE HALF. "/" must not ALSO render the subscriptions page: two
	// urls for one surface is the state this replaced, and a handler that renders
	// the old page while emitting a Location header would pass every check above.
	if strings.Contains(rec.Body.String(), ">"+subscriptionsPageTitle+"</h1>") {
		t.Errorf("GET / still renders the subscriptions page body:\n%s", firstN(rec.Body.String(), 800))
	}
}

// TestSubscriptionsPageTitle pins the page that used to be "/" — its url, its
// name, and the ABSENCE of the two names it has already outgrown.
//
// WHY THE NAMES KEEP CHANGING. "Dashboard" was a nav entry pointing at the same
// place as the brand wordmark beside it; the nav rework deleted the entry, which
// left the word naming a control the user could no longer see. "Overview"
// replaced it and described the page while it WAS the landing page. It is not the
// landing page any more (handleHome), so it is now named for its subject.
//
// 🔴 THE ABSENCE HALF IS WHAT MAKES THIS A GUARD. Asserting only that
// "Subscriptions" appears would stay green if a later edit put "Dashboard" back
// in the tab title or left an "Overview" heading on a card — both of which
// reintroduce exactly the split vocabulary this removes. It scans the WHOLE
// rendered page, and it deliberately does NOT ban the substring "dashboard": the
// Go identifiers (handleDashboard, dashboardPage) keep that name and are not
// user-visible.
func TestSubscriptionsPageTitle(t *testing.T) {
	srv := newTestServer(t)
	rec := get(t, srv, librarySubscriptionsHref)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200", librarySubscriptionsHref, rec.Code)
	}
	body := rec.Body.String()

	// The browser tab and the page heading, both from subscriptionsPageTitle.
	if !strings.Contains(body, "<title>"+subscriptionsPageTitle+" · civitai-manager</title>") {
		t.Errorf("the page's <title> must read %q:\n%s", subscriptionsPageTitle, firstN(body, 600))
	}
	if !strings.Contains(body, ">"+subscriptionsPageTitle+"</h1>") {
		t.Errorf("the page's <h1> must read %q:\n%s", subscriptionsPageTitle, firstN(body, 3000))
	}
	// FIXTURE REACH: exactly one <h1>, which is also what
	// TestEveryFullPageHasExactlyOneH1 requires — deleting the heading is not an
	// available way to satisfy the absence check below.
	if n := strings.Count(body, "<h1 "); n != 1 {
		t.Errorf("the page must have exactly one <h1>, got %d", n)
	}
	// FIXTURE REACH: it really is the old dashboard, not some stub that happens to
	// carry the right title.
	for _, want := range []string{"Add a subscription", "Download queue", "Activity"} {
		if !strings.Contains(body, want) {
			t.Fatalf("the fixture is not the subscriptions page (missing %q) — the checks above prove nothing", want)
		}
	}

	// GONE: no user-visible occurrence of either retired name anywhere on the page.
	for _, gone := range []string{">Dashboard<", ">Dashboard</h1>", "<title>Dashboard", ">Overview<", "<title>Overview"} {
		if strings.Contains(body, gone) {
			t.Errorf("the page still shows the retired name via %q:\n%s", gone, firstN(body, 3000))
		}
	}
}

func TestDashboardManualFormDemotedAndSearchBox(t *testing.T) {
	out := renderString(t, dashboardPage(nil, nil, "test-csrf", fullMaturityRange()))
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
	// The card now renders the shared 3-step subscribe control: a Subscribe button
	// that opens the options panel for that model (id 77) via GET.
	if !strings.Contains(out, `hx-get="/models/77/subscribe-options"`) {
		t.Error("subscribe card should use the shared subscribe control (options panel)")
	}
	if !strings.Contains(out, `id="subscribe-control-77"`) {
		t.Error("subscribe card should carry the stable subscribe-control container")
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

// errModelReader fails GetModel (and starts with no cache) so the lazy title
// handler must fall back to "Model #id".
type errModelReader struct{ fakeReader }

func (errModelReader) GetModel(context.Context, string) (*civitai.ModelDetail, []byte, error) {
	return nil, nil, civitai.ErrNotFound
}

// --- F. Suggestions rendering ---

func TestDashboardRendersSuggestions(t *testing.T) {
	suggestions := []suggestion{
		{ModelID: 42, FileCount: 2, TotalBytes: 1500, Name: "Resolved Model"}, // cached name
		{ModelID: 7, FileCount: 1, TotalBytes: 500},                           // cache miss -> lazy
	}
	out := renderString(t, dashboardPage(nil, suggestions, "test-csrf", fullMaturityRange()))
	if !strings.Contains(out, "Subscribe suggestions from your library") {
		t.Error("suggestions section heading missing")
	}
	for _, want := range []string{"Resolved Model", "/models/42", "/models/7"} {
		if !strings.Contains(out, want) {
			t.Errorf("suggestions missing %q", want)
		}
	}
	// A resolved name renders directly (no lazy fetch for that card).
	if strings.Contains(out, `hx-get="/models/42/title"`) {
		t.Error("a cache-resolved suggestion should NOT lazily fetch its title")
	}
	// A cache-miss suggestion renders a lazy title container fetched on load.
	if !strings.Contains(out, `hx-get="/models/7/title"`) {
		t.Error("a cache-miss suggestion should lazily fetch its title")
	}
	if !strings.Contains(out, "Loading…") {
		t.Error("lazy suggestion title should show a Loading placeholder")
	}
	// One-click Subscribe with auto_download=true.
	if !strings.Contains(out, `name="auto_download"`) || !strings.Contains(out, `value="true"`) {
		t.Error("suggestion cards must offer one-click auto-download Subscribe")
	}
}

// TestModelTitleHandler proves the lazy title endpoint returns the resolved
// model name (cache-first) and falls back gracefully.
func TestModelTitleHandler(t *testing.T) {
	// Cache hit: seed model_cache; the handler must return the cached name without
	// depending on the reader (stubReader would return "M" anyway, so seed a
	// distinct name to prove the cache path).
	t.Run("cache hit", func(t *testing.T) {
		srv := newTestServer(t)
		if err := srv.store.PutModelCache(55, "Cached Name", []byte(`{"name":"Cached Name"}`)); err != nil {
			t.Fatalf("seed cache: %v", err)
		}
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/models/55/title", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "Cached Name") {
			t.Errorf("title handler should return the cached name, got %q", rec.Body.String())
		}
	})

	// Error fallback: a reader that fails GetModel and no cache entry -> "Model #id".
	t.Run("error fallback", func(t *testing.T) {
		srv := newModelServer(t, errModelReader{})
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/models/999/title", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "Model #999") {
			t.Errorf("title handler should fall back to Model #id, got %q", rec.Body.String())
		}
	})
}

func TestDashboardHidesEmptySuggestions(t *testing.T) {
	out := renderString(t, dashboardPage(nil, nil, "test-csrf", fullMaturityRange()))
	if strings.Contains(out, "Subscribe suggestions from your library") {
		t.Error("suggestions section should be hidden when there are none")
	}
}
