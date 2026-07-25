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

// TestVersionStatusBothPublishDates proves the popover shows BOTH the local and
// the latest version's publish date (item 2), sourced from the same cached raw.
func TestVersionStatusBothPublishDates(t *testing.T) {
	raw := versionStatusRaw(t, []map[string]any{
		{"id": 20, "name": "v2", "publishedAt": "2024-03-10T12:00:00.000Z"},
		{"id": 10, "name": "v1", "publishedAt": "2023-01-05T12:00:00.000Z"},
	})
	srv := seedVersionStatus(t, raw, 10) // has v1 (id 10); latest is v2 (id 20)

	body := getModelPage(t, srv, "/models/7/version-status")
	for _, want := range []string{
		"You have: v1", "Published 2023-01-05", // local version's date
		"Latest: v2", "Published 2024-03-10", // latest version's date
	} {
		if !strings.Contains(body, want) {
			t.Errorf("popover missing %q:\n%s", want, body)
		}
	}
}

// TestVersionStatusOmitsMissingLocalDate proves the popover degrades gracefully
// when the user's local version is NOT in the model's version list (its date is
// unknown): it renders the local name without a date and does not crash.
func TestVersionStatusOmitsMissingLocalDate(t *testing.T) {
	raw := versionStatusRaw(t, []map[string]any{
		{"id": 20, "name": "v2", "publishedAt": "2024-03-10T12:00:00.000Z"},
		{"id": 10, "name": "v1", "publishedAt": "2023-01-05T12:00:00.000Z"},
	})
	// The user owns version 99, which is not in the list → no resolvable date.
	srv := seedVersionStatus(t, raw, 99)

	body := getModelPage(t, srv, "/models/7/version-status")
	if !strings.Contains(body, "You have: Version #99") {
		t.Errorf("should name the unknown local version:\n%s", body)
	}
	// The local line carries no date; the latest line still does.
	if strings.Contains(body, "You have: Version #99 · Published") {
		t.Error("an unknown local version must not fabricate a publish date")
	}
	if !strings.Contains(body, "Latest: v2 · Published 2024-03-10") {
		t.Errorf("the latest line should still carry its date:\n%s", body)
	}
}

// TestVersionStatusEscapesNames proves untrusted version names in the popover are
// HTML-escaped (never injected raw).
func TestVersionStatusEscapesNames(t *testing.T) {
	raw := versionStatusRaw(t, []map[string]any{
		{"id": 20, "name": "<b>evil</b>", "publishedAt": "2024-03-10T12:00:00.000Z"},
		{"id": 10, "name": "v1"},
	})
	srv := seedVersionStatus(t, raw, 10)

	body := getModelPage(t, srv, "/models/7/version-status")
	if strings.Contains(body, "<b>evil</b>") {
		t.Errorf("version name must be escaped, not injected raw:\n%s", body)
	}
	if !strings.Contains(body, "&lt;b&gt;evil&lt;/b&gt;") {
		t.Errorf("escaped version name should appear:\n%s", body)
	}
}

// TestSuggestionVersionStatusOnOwnRow proves the lazy version-status container is
// placed on its OWN ROW below the title/footprint (item 1), as a collapsible
// cm-vstatus-lazy block — not inline beside the title.
func TestSuggestionVersionStatusOnOwnRow(t *testing.T) {
	out := renderString(t, suggestionsList([]suggestion{
		{ModelID: 7, FileCount: 2, TotalBytes: 1 << 20, Name: "Great Model"},
	}, "csrf"))

	if !strings.Contains(out, `class="cm-vstatus-lazy"`) {
		t.Errorf("version-status should be an own-row cm-vstatus-lazy container:\n%s", out)
	}
	// Placement: the container renders AFTER the file-count footprint (below the
	// title/footprint, not inline beside the title).
	footprint := strings.Index(out, "file(s)")
	status := strings.Index(out, `id="version-status-7"`)
	if footprint < 0 || status < 0 || status < footprint {
		t.Errorf("version-status should render below the footprint (footprint=%d status=%d):\n%s",
			footprint, status, out)
	}
}

// TestSuggestionUpToDateNoStrayBadge proves an up-to-date model's version-status
// fragment renders nothing (no stray pill), keeping the own-row container empty
// (and thus collapsed by CSS).
func TestSuggestionUpToDateNoStrayBadge(t *testing.T) {
	out := renderString(t, versionStatusFragment(versionBreakdown{UpdateAvailable: false}, nil))
	if strings.TrimSpace(out) != "" || strings.Contains(out, "cm-vstatus") {
		t.Errorf("up-to-date fragment should render nothing, got %q", out)
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
