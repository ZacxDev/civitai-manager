package web

import (
	"net/http"
	"net/http/httptest"
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
