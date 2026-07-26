package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
)

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
// request with type=Workflows plus the query/limit/sort/period params.
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
	if q.Get("type") != "Workflows" {
		t.Errorf("type = %q, want Workflows", q.Get("type"))
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

// TestDiscoverEmptyQueryDoesNotFetch proves the empty-query state renders a prompt
// and makes NO SearchModels call (no egress on an empty query).
func TestDiscoverEmptyQueryDoesNotFetch(t *testing.T) {
	reader := &recordingSearchReader{result: workflowResult(t)}
	srv := newModelServer(t, reader)

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/workflows/discover", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /workflows/discover = %d", rec.Code)
	}
	if reader.callCount() != 0 {
		t.Fatalf("empty query must not fetch; SearchModels calls = %d, want 0", reader.callCount())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Search for workflows") {
		t.Errorf("empty-query page should show the search prompt:\n%s", body)
	}
	// Full page chrome present (navbar with the Discover link).
	if !strings.Contains(body, `href="/workflows/discover"`) {
		t.Error("full page should include the Discover nav link")
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
		"WAN 2.2 Workflow T2V-I2V-T2I",        // model name
		"https://image.civitai.com/wf.jpeg",   // carousel showcase image
		`href="/models/1818841"`,              // card links to the in-app detail page
		"Updated",                             // "Updated X ago" popover line
		"Discover workflows",                  // page heading
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

// TestDiscoverCardsHaveNoStateChangingControls proves discover cards are browse-
// only: no Subscribe/Download/Import controls and no CSRF-bearing controls. It
// asserts against the HX results FRAGMENT (pure cards) so navbar chrome — whose
// theme/NSFW toggles legitimately carry a csrf_token — cannot mask a card-level
// control leaking in.
func TestDiscoverCardsHaveNoStateChangingControls(t *testing.T) {
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
	// The subscribe control renders a "Subscribe" button that GETs the options panel;
	// none of those markers may appear on a browse-only card.
	for _, forbidden := range []string{
		"subscribe-options", // subscribe control's GET target
		">Subscribe<",       // subscribe button label
		"/download",         // download action
		"csrf_token",        // no state-changing controls → no CSRF anywhere on the page
	} {
		if strings.Contains(body, forbidden) {
			t.Errorf("browse-only discover card should NOT contain %q:\n%s", forbidden, body)
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
