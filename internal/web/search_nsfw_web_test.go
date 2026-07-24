package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// setNSFWMode persists the display mode directly (bypassing the CSRF-guarded
// settings POST) so a test can exercise hide/blur/show search behavior.
func setNSFWMode(t *testing.T, srv *Server, mode string) {
	t.Helper()
	if err := srv.store.SetSetting(nsfwSettingKey, mode); err != nil {
		t.Fatalf("set nsfw mode: %v", err)
	}
}

// lastCall returns the url.Values of the most recent SearchModels call.
func lastCall(t *testing.T, r *recordingSearchReader) map[string][]string {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.calls) == 0 {
		t.Fatal("no SearchModels call recorded")
	}
	return r.calls[len(r.calls)-1]
}

// TestSearchNSFWParamByMode proves the civitai model-search nsfw param is tied to
// the display mode: blur/show send nsfw=true (so NSFW models return WITH their
// showcase images), hide sends nsfw=false (SFW-only) — across the keyword search,
// the popular default, and the dashboard subscribe search.
func TestSearchNSFWParamByMode(t *testing.T) {
	cases := []struct {
		mode string
		want string
	}{
		{NSFWBlur, "true"},
		{NSFWShow, "true"},
		{NSFWHide, "false"},
	}
	paths := []string{
		"/search?q=anime",     // keyword search
		"/search",             // popular default (empty query)
		"/subscribe/search?q=anime", // dashboard subscribe search
	}
	for _, tc := range cases {
		for _, p := range paths {
			t.Run(tc.mode+" "+p, func(t *testing.T) {
				reader := &recordingSearchReader{result: popularResult(t)}
				srv := newModelServer(t, reader)
				setNSFWMode(t, srv, tc.mode)

				rec := httptest.NewRecorder()
				srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
				if rec.Code != http.StatusOK {
					t.Fatalf("GET %s = %d", p, rec.Code)
				}
				if got := lastCall(t, reader)["nsfw"]; len(got) == 0 || got[0] != tc.want {
					t.Errorf("nsfw param = %v, want %q", got, tc.want)
				}
			})
		}
	}
}

// TestPopularCacheKeyedByNSFW proves the popular TTL cache holds a separate entry
// per NSFW flag: a mode flip must NOT serve the other flag's cached list, and
// each flag caches independently (no extra fetch within the TTL for a repeat).
func TestPopularCacheKeyedByNSFW(t *testing.T) {
	reader := &recordingSearchReader{result: popularResult(t)}
	srv := newModelServer(t, reader)

	get := func() {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/search", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /search = %d", rec.Code)
		}
	}

	// Default mode is blur → nsfw=true. First load fetches + caches.
	setNSFWMode(t, srv, NSFWBlur)
	get()
	if reader.callCount() != 1 {
		t.Fatalf("first blur load: calls = %d, want 1", reader.callCount())
	}
	// Repeat within TTL is served from the true-flag cache — no extra fetch.
	get()
	if reader.callCount() != 1 {
		t.Fatalf("cached blur load: calls = %d, want 1", reader.callCount())
	}

	// Flip to hide → nsfw=false. This is a DIFFERENT cache key, so it must fetch
	// (not serve the true-flag entry) with nsfw=false.
	setNSFWMode(t, srv, NSFWHide)
	get()
	if reader.callCount() != 2 {
		t.Fatalf("hide load must not serve the blur cache: calls = %d, want 2", reader.callCount())
	}
	if got := lastCall(t, reader)["nsfw"]; len(got) == 0 || got[0] != "false" {
		t.Errorf("hide popular fetch nsfw = %v, want false", got)
	}
	// Repeat hide within TTL is cached.
	get()
	if reader.callCount() != 2 {
		t.Fatalf("cached hide load: calls = %d, want 2", reader.callCount())
	}

	// Flip back to blur → served from the still-valid true-flag cache (no fetch).
	setNSFWMode(t, srv, NSFWBlur)
	get()
	if reader.callCount() != 2 {
		t.Fatalf("blur reload should hit the true-flag cache: calls = %d, want 2", reader.callCount())
	}
}
