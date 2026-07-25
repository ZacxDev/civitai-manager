package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/store"
)

// versionStatusRaw builds a GetModel raw body with the given versions (newest
// first). Each entry is {id, name, publishedAt}.
func versionStatusRaw(t *testing.T, versions []map[string]any) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"id": 7, "name": "Great Model", "type": "LORA",
		"modelVersions": versions,
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// seedVersionStatus seeds the model_cache for model 7 and (optionally) a local file
// at localVer, then returns the running server. The reader errors on GetModel so a
// served fragment PROVES the cache-first path (no network).
func seedVersionStatus(t *testing.T, raw []byte, localVer int) *Server {
	t.Helper()
	srv := newModelServer(t, errModelReader{})
	if err := srv.store.PutModelCache(7, "Great Model", raw); err != nil {
		t.Fatal(err)
	}
	if localVer > 0 {
		if err := srv.store.UpsertLocalFile(store.LocalFile{
			Path: "/m/great.safetensors", ModelID: intPtr(7), VersionID: intPtr(localVer),
			SizeBytes: 1 << 20, Status: store.LocalStatusMatched, Kind: store.LocalKindModel,
		}); err != nil {
			t.Fatal(err)
		}
	}
	return srv
}

// TestVersionStatusUpdateAvailable proves the endpoint renders the "new version"
// badge + popover (with the local→latest names + publish date) when the latest
// remote version is not in the library, served CACHE-FIRST (reader errors).
func TestVersionStatusUpdateAvailable(t *testing.T) {
	raw := versionStatusRaw(t, []map[string]any{
		{"id": 20, "name": "v2", "publishedAt": "2024-01-15T12:00:00.000Z"},
		{"id": 10, "name": "v1"},
	})
	srv := seedVersionStatus(t, raw, 10) // user has v1 (id 10); latest is v2 (id 20)

	body := getModelPage(t, srv, "/models/7/version-status")
	for _, want := range []string{
		"cm-vstatus", "⟳ new version",
		"You have: v1", "Latest: v2",
		"Published 2024-01-15",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("version-status fragment missing %q:\n%s", want, body)
		}
	}
}

// TestVersionStatusUpToDate proves a model whose latest version is already local
// renders NOTHING (an empty fragment) — keeping the suggestion grid clean.
func TestVersionStatusUpToDate(t *testing.T) {
	raw := versionStatusRaw(t, []map[string]any{
		{"id": 20, "name": "v2"},
		{"id": 10, "name": "v1"},
	})
	srv := seedVersionStatus(t, raw, 20) // user already has the latest (id 20)

	body := getModelPage(t, srv, "/models/7/version-status")
	if strings.Contains(body, "new version") || strings.Contains(body, "cm-vstatus") {
		t.Errorf("up-to-date model must render no update badge:\n%q", body)
	}
}

// TestVersionStatusEscapesVersionName proves an untrusted (malicious) version name
// is HTML-escaped in the popover, never emitted as live markup.
func TestVersionStatusEscapesVersionName(t *testing.T) {
	raw := versionStatusRaw(t, []map[string]any{
		{"id": 20, "name": "<script>alert(1)</script>"},
		{"id": 10, "name": "v1"},
	})
	srv := seedVersionStatus(t, raw, 10)

	body := getModelPage(t, srv, "/models/7/version-status")
	if strings.Contains(body, "<script>alert(1)</script>") {
		t.Errorf("malicious version name must be escaped, not emitted raw:\n%s", body)
	}
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Errorf("expected the version name to be HTML-escaped:\n%s", body)
	}
}

// TestVersionStatusFetchPathNoPanic proves the endpoint handles an UNCACHED model
// via the fetch path without panicking, and renders the badge when the fetched
// detail shows an update available (no local files → latest is not in library).
func TestVersionStatusFetchPathNoPanic(t *testing.T) {
	var calls int32
	raw := versionStatusRaw(t, []map[string]any{
		{"id": 20, "name": "v2"},
		{"id": 10, "name": "v1"},
	})
	reader := countingModelReader{calls: &calls, raw: raw}
	srv := newModelServer(t, reader) // NOT seeded → cache miss → fetch

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/models/7/version-status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("version-status (fetch path) = %d", rec.Code)
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("cache miss should fetch exactly once, got %d", atomic.LoadInt32(&calls))
	}
	// No local files → the latest remote version isn't in library → update badge.
	if !strings.Contains(rec.Body.String(), "new version") {
		t.Errorf("fetch path should show an update badge for an unheld model:\n%s", rec.Body.String())
	}
}

// TestVersionStatusUnknownModelDegrades proves an offline/not-found model degrades
// to an empty fragment (200, no error chip) rather than failing the card.
func TestVersionStatusUnknownModelDegrades(t *testing.T) {
	srv := newModelServer(t, errModelReader{}) // GetModel → ErrNotFound, no cache
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/models/999/version-status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("unknown model status = %d, want 200", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "new version") {
		t.Errorf("unknown model must not render an update badge:\n%s", rec.Body.String())
	}
}

// TestSuggestionCardLazyVersionStatus proves each dashboard suggestion card wires
// the lazy version-status trigger.
func TestSuggestionCardLazyVersionStatus(t *testing.T) {
	out := renderString(t, suggestionsList([]suggestion{
		{ModelID: 7, FileCount: 1, TotalBytes: 1 << 20, Name: "Great Model"},
	}, "csrf"))
	for _, want := range []string{
		`hx-get="/models/7/version-status"`,
		`hx-trigger="load"`,
		`id="version-status-7"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("suggestion card missing lazy version-status wiring %q:\n%s", want, out)
		}
	}
}
