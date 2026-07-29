package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
)

// erroringSearchReader is a civitai.Reader whose SearchModels always errors, used
// to exercise the popular-feed graceful-degrade path.
type erroringSearchReader struct{ fakeReader }

func (erroringSearchReader) SearchModels(context.Context, url.Values) (*civitai.ModelSearchResult, error) {
	return nil, errors.New("boom")
}

// workflowResult builds a one-item Workflows-type search result with a safe
// showcase image and a version, so the discover cards render a name, a carousel
// (via parseSearchImages), and an "Updated X ago" popover.
func workflowResult(t *testing.T) *civitai.ModelSearchResult {
	t.Helper()
	raw := searchRawJSON(t, []any{
		map[string]any{
			"id": 1818841, "name": "WAN 2.2 Workflow T2V-I2V-T2I", "type": "Workflows",
			"creator": map[string]any{"username": "pgc"},
			"modelVersions": []any{
				map[string]any{
					"id": 991, "name": "v1.8.5", "publishedAt": "2026-01-02T00:00:00.000Z",
					"images": []any{
						map[string]any{"url": "https://image.civitai.com/wf.jpeg", "nsfwLevel": 1, "type": "image"},
					},
				},
			},
		},
	})
	return &civitai.ModelSearchResult{
		Items: []civitai.ModelListItem{{ID: 1818841, Name: "WAN 2.2 Workflow T2V-I2V-T2I", Type: "Workflows"}},
		Raw:   raw,
	}
}

// TestDiscoverPinsTypeWorkflows proves the discover handler builds the SearchModels
// request with types=Workflows (PLURAL — the CivitAI API ignores singular "type")
// plus the query/limit/sort/period params.
func TestDiscoverPinsTypeWorkflows(t *testing.T) {
	reader := &recordingSearchReader{result: workflowResult(t)}
	srv := newModelServer(t, reader)

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/workflows/discover?q=wan&sort=Newest&period=Week", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /workflows/discover = %d", rec.Code)
	}
	if reader.callCount() != 1 {
		t.Fatalf("want exactly 1 SearchModels call, got %d", reader.callCount())
	}
	reader.mu.Lock()
	q := reader.calls[0]
	reader.mu.Unlock()
	if q.Get("types") != "Workflows" {
		t.Errorf("types = %q, want Workflows", q.Get("types"))
	}
	if q.Get("type") != "" {
		t.Errorf("singular type must NOT be set (API ignores it); got %q", q.Get("type"))
	}
	if q.Get("query") != "wan" {
		t.Errorf("query = %q, want wan", q.Get("query"))
	}
	if q.Get("limit") != "24" {
		t.Errorf("limit = %q, want 24", q.Get("limit"))
	}
	if q.Get("sort") != "Newest" {
		t.Errorf("sort = %q, want Newest", q.Get("sort"))
	}
	if q.Get("period") != "Week" {
		t.Errorf("period = %q, want Week", q.Get("period"))
	}
	// NSFW default mode is blur → nsfw=true param sent (models return WITH images).
	if q.Get("nsfw") != "true" {
		t.Errorf("nsfw = %q, want true (blur default sends nsfw=true)", q.Get("nsfw"))
	}
}

// TestDiscoverSortRejectsBogusValue proves an out-of-whitelist ?sort= defaults to
// Most Downloaded rather than being forwarded verbatim (shared normalize helpers).
func TestDiscoverSortRejectsBogusValue(t *testing.T) {
	reader := &recordingSearchReader{result: workflowResult(t)}
	srv := newModelServer(t, reader)

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/workflows/discover?q=x&sort=DROP+TABLE", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /workflows/discover = %d", rec.Code)
	}
	reader.mu.Lock()
	q := reader.calls[len(reader.calls)-1]
	reader.mu.Unlock()
	if q.Get("sort") != "Most Downloaded" {
		t.Errorf("bogus sort should default to Most Downloaded, got %q", q.Get("sort"))
	}
	// A keyword search defaults period to AllTime (not narrowed to a month).
	if q.Get("period") != "AllTime" {
		t.Errorf("default period = %q, want AllTime", q.Get("period"))
	}
}

// TestDiscoverEmptyQueryShowsPopular proves the empty-query state now renders the
// cached "Popular this month" WORKFLOWS feed: one SearchModels call pinned to
// types=Workflows + Most Downloaded / Month, the heading, and the result cards.
func TestDiscoverEmptyQueryShowsPopular(t *testing.T) {
	reader := &recordingSearchReader{result: workflowResult(t)}
	srv := newModelServer(t, reader)

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/workflows/discover", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /workflows/discover = %d", rec.Code)
	}
	if reader.callCount() != 1 {
		t.Fatalf("empty query should fetch the popular feed once; SearchModels calls = %d, want 1", reader.callCount())
	}
	reader.mu.Lock()
	q := reader.calls[0]
	reader.mu.Unlock()
	if q.Get("types") != "Workflows" {
		t.Errorf("popular feed must pin types=Workflows, got %q", q.Get("types"))
	}
	if q.Get("type") != "" {
		t.Errorf("singular type must NOT be set (API ignores it); got %q", q.Get("type"))
	}
	if q.Get("sort") != "Most Downloaded" || q.Get("period") != "Month" || q.Get("limit") != "24" {
		t.Errorf("popular workflow query params wrong: %v", q)
	}
	if q.Get("query") != "" {
		t.Errorf("popular fetch must not carry a query, got %q", q.Get("query"))
	}
	body := rec.Body.String()
	for _, want := range []string{
		"Popular this month",           // the feed heading
		"WAN 2.2 Workflow T2V-I2V-T2I", // a result card
		`href="/workflows/discover"`,   // full page chrome (Discover nav link)
	} {
		if !strings.Contains(body, want) {
			t.Errorf("empty-query popular page missing %q:\n%s", want, body)
		}
	}
}

// TestDiscoverPopularCached proves the popular workflows feed is TTL-cached: a
// second empty-query request does NOT re-fetch (call count stays 1).
func TestDiscoverPopularCached(t *testing.T) {
	reader := &recordingSearchReader{result: workflowResult(t)}
	srv := newModelServer(t, reader)

	get := func() {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/workflows/discover", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /workflows/discover = %d", rec.Code)
		}
	}
	get()
	get()
	if reader.callCount() != 1 {
		t.Fatalf("second empty-query load within TTL should be cached; SearchModels calls = %d, want 1", reader.callCount())
	}
}

// TestDiscoverPopularFetchErrorFallsBack proves a popular-feed fetch error
// degrades to the plain "Search for workflows…" prompt (HTTP 200, no crash).
func TestDiscoverPopularFetchErrorFallsBack(t *testing.T) {
	srv := newModelServer(t, erroringSearchReader{})

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/workflows/discover", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /workflows/discover = %d, want 200 (graceful degrade)", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Search for workflows") {
		t.Errorf("on a popular-feed fetch error the page should show the plain prompt:\n%s", body)
	}
	if strings.Contains(body, "Popular this month") {
		t.Errorf("no heading should render when the popular fetch failed:\n%s", body)
	}
}

// TestDiscoverPageRendersCards proves the full page renders workflow result cards:
// the name, the showcase carousel image (via parseSearchImages), the "Updated X
// ago" popover, and a link to the in-app model detail page.
func TestDiscoverPageRendersCards(t *testing.T) {
	reader := &recordingSearchReader{result: workflowResult(t)}
	srv := newModelServer(t, reader)

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/workflows/discover?q=wan", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /workflows/discover = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"WAN 2.2 Workflow T2V-I2V-T2I",      // model name
		"https://image.civitai.com/wf.jpeg", // carousel showcase image
		`href="/models/1818841"`,            // card links to the in-app detail page
		"Updated",                           // "Updated X ago" popover line
		"Discover workflows",                // page heading
	} {
		if !strings.Contains(body, want) {
			t.Errorf("discover page missing %q", want)
		}
	}
}

// TestDiscoverHXPartialReturnsFragmentOnly proves the HX-partial results endpoint
// returns just the results grid — no full-page chrome (<html>, navbar).
func TestDiscoverHXPartialReturnsFragmentOnly(t *testing.T) {
	reader := &recordingSearchReader{result: workflowResult(t)}
	srv := newModelServer(t, reader)

	req := httptest.NewRequest(http.MethodGet, "/workflows/discover?q=wan", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("HX GET /workflows/discover = %d", rec.Code)
	}
	body := rec.Body.String()
	// The card content is present...
	if !strings.Contains(body, "WAN 2.2 Workflow T2V-I2V-T2I") {
		t.Error("HX fragment should contain the result card")
	}
	// ...but NOT the full-page chrome.
	if strings.Contains(body, "<html") || strings.Contains(body, "<nav") {
		t.Errorf("HX fragment must not contain full-page chrome:\n%s", body)
	}
}

// TestDiscoverCardsHaveImportControlOnly proves the D2 discover card carries the
// "Import workflow(s)" control (a CSRF-bearing POST to the import endpoint) but
// still NOT the model-search Subscribe/Download controls — import is the only
// state-changing affordance on a discover card. It asserts against the HX results
// FRAGMENT (pure cards) so navbar chrome cannot mask what leaks in.
func TestDiscoverCardsHaveImportControlOnly(t *testing.T) {
	reader := &recordingSearchReader{result: workflowResult(t)}
	srv := newModelServer(t, reader)

	req := httptest.NewRequest(http.MethodGet, "/workflows/discover?q=wan", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("HX GET /workflows/discover = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "WAN 2.2 Workflow T2V-I2V-T2I") {
		t.Fatal("fragment should contain the result card (guards against an empty false-pass)")
	}
	// The import control's POST target + CSRF must be present.
	for _, want := range []string{
		"/workflows/discover/1818841/import", // import POST target
		"Import workflow(s)",                 // import button label
		"csrf_token",                         // import is CSRF-protected
	} {
		if !strings.Contains(body, want) {
			t.Errorf("D2 discover card should contain %q:\n%s", want, body)
		}
	}
	// The model-search subscribe/download controls do NOT belong on a discover card.
	for _, forbidden := range []string{
		"subscribe-options", // subscribe control's GET target
		">Subscribe<",       // subscribe button label
		"/download",         // download action
	} {
		if strings.Contains(body, forbidden) {
			t.Errorf("discover card should NOT contain %q:\n%s", forbidden, body)
		}
	}
}

// TestDiscoverEmptyResults proves a zero-item result renders the "No workflows
// found." empty state rather than an error.
func TestDiscoverEmptyResults(t *testing.T) {
	reader := &recordingSearchReader{result: &civitai.ModelSearchResult{}}
	srv := newModelServer(t, reader)

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/workflows/discover?q=nomatch", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /workflows/discover = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "No workflows found.") {
		t.Error("empty result should render the 'No workflows found.' state")
	}
}
