package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
)

// TestKeywordSearchSortsMostDownloaded proves a non-empty keyword search asks
// civitai for the most popular matches first (sort=Most Downloaded), alongside
// the query + limit params.
func TestKeywordSearchSortsMostDownloaded(t *testing.T) {
	reader := &recordingSearchReader{result: &civitai.ModelSearchResult{}}
	srv := newModelServer(t, reader)

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/search?q=anime", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /search?q=anime = %d", rec.Code)
	}
	if reader.callCount() != 1 {
		t.Fatalf("want exactly 1 SearchModels call, got %d", reader.callCount())
	}
	reader.mu.Lock()
	q := reader.calls[0]
	reader.mu.Unlock()
	if q.Get("query") != "anime" {
		t.Errorf("query = %q, want anime", q.Get("query"))
	}
	if q.Get("sort") != "Most Downloaded" {
		t.Errorf("sort = %q, want Most Downloaded", q.Get("sort"))
	}
	if q.Get("limit") != "24" {
		t.Errorf("limit = %q, want 24", q.Get("limit"))
	}
}

// TestSubscribeSearchSortsMostDownloaded proves the dashboard mini-search now sets
// an explicit sort=Most Downloaded (it previously inherited the API default).
func TestSubscribeSearchSortsMostDownloaded(t *testing.T) {
	reader := &recordingSearchReader{result: &civitai.ModelSearchResult{}}
	srv := newModelServer(t, reader)

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/subscribe/search?q=anime", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /subscribe/search?q=anime = %d", rec.Code)
	}
	if reader.callCount() != 1 {
		t.Fatalf("want exactly 1 SearchModels call, got %d", reader.callCount())
	}
	reader.mu.Lock()
	q := reader.calls[0]
	reader.mu.Unlock()
	if q.Get("query") != "anime" {
		t.Errorf("query = %q, want anime", q.Get("query"))
	}
	if q.Get("sort") != "Most Downloaded" {
		t.Errorf("sort = %q, want Most Downloaded", q.Get("sort"))
	}
}

// TestSearchSortPeriodThreaded proves ?sort=&period= on a keyword search are
// validated and forwarded to civitai alongside the query.
func TestSearchSortPeriodThreaded(t *testing.T) {
	reader := &recordingSearchReader{result: &civitai.ModelSearchResult{}}
	srv := newModelServer(t, reader)

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/search?q=x&sort=Newest&period=Week", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /search = %d", rec.Code)
	}
	reader.mu.Lock()
	q := reader.calls[len(reader.calls)-1]
	reader.mu.Unlock()
	if q.Get("query") != "x" {
		t.Errorf("query = %q, want x", q.Get("query"))
	}
	if q.Get("sort") != "Newest" {
		t.Errorf("sort = %q, want Newest", q.Get("sort"))
	}
	if q.Get("period") != "Week" {
		t.Errorf("period = %q, want Week", q.Get("period"))
	}
}

// TestSearchSortRejectsBogusValue proves an out-of-whitelist ?sort= falls back to
// the Most Downloaded default rather than being forwarded verbatim.
func TestSearchSortRejectsBogusValue(t *testing.T) {
	reader := &recordingSearchReader{result: &civitai.ModelSearchResult{}}
	srv := newModelServer(t, reader)

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/search?q=x&sort=DROP+TABLE", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /search = %d", rec.Code)
	}
	reader.mu.Lock()
	q := reader.calls[len(reader.calls)-1]
	reader.mu.Unlock()
	if q.Get("sort") != "Most Downloaded" {
		t.Errorf("bogus sort should default to Most Downloaded, got %q", q.Get("sort"))
	}
}

// TestSearchPageDropdownMarksSelected proves the full-page search form renders the
// sort/period dropdowns with the active option marked, and defaults to Most
// Downloaded / This month with no params.
func TestSearchPageDropdownMarksSelected(t *testing.T) {
	reader := &recordingSearchReader{result: &civitai.ModelSearchResult{}}
	srv := newModelServer(t, reader)

	get := func(target string) string {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d", target, rec.Code)
		}
		return rec.Body.String()
	}

	// Chosen sort → that option carries `selected`.
	body := get("/search?q=x&sort=Newest&period=Week")
	for _, want := range []string{
		`name="sort"`, `name="period"`,
		`<option value="Newest" selected>`,
		`<option value="Week" selected>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("search page missing %q", want)
		}
	}

	// Default (no params) → Most Downloaded / Month selected.
	def := get("/search")
	if !strings.Contains(def, `<option value="Most Downloaded" selected>`) {
		t.Errorf("default sort should be Most Downloaded selected:\n%s", def)
	}
	if !strings.Contains(def, `<option value="Month" selected>`) {
		t.Errorf("default period should be Month selected")
	}
}
