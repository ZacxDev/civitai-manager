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

// TestSearchNSFWParamByMode proves the civitai model-search nsfw param follows the
// display mode. Since the toggle dropped the hide state (nsfwSearchFlag is now
// always true — a stored hide migrates to blur), blur/show/hide all send
// nsfw=true across the keyword search, the popular default, and the dashboard
// subscribe search.
func TestSearchNSFWParamByMode(t *testing.T) {
	cases := []struct {
		mode string
		want string
	}{
		{NSFWBlur, "true"},
		{NSFWShow, "true"},
		{NSFWHide, "true"}, // migrated to blur → still nsfw=true
	}
	paths := []string{
		"/search?q=anime",           // keyword search
		"/search",                   // popular default (empty query)
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

// TestPopularCacheSharedAcrossModes proves that after the hide→blur migration, the
// popular TTL cache is keyed by an nsfw flag that is now always true for every
// UI-selectable mode (blur/show/hide-migrated-to-blur). So flipping the display
// mode serves the SAME cached list — no extra fetch within the TTL. (The cache
// key is still keyed by the flag; there is simply no longer a mode that produces
// nsfw=false.)
func TestPopularCacheSharedAcrossModes(t *testing.T) {
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
	if got := lastCall(t, reader)["nsfw"]; len(got) == 0 || got[0] != "true" {
		t.Errorf("blur popular fetch nsfw = %v, want true", got)
	}

	// Flip to show → still nsfw=true → served from the SAME cache (no fetch).
	setNSFWMode(t, srv, NSFWShow)
	get()
	if reader.callCount() != 1 {
		t.Fatalf("show load should hit the true-flag cache: calls = %d, want 1", reader.callCount())
	}

	// Flip to a stored hide → migrates to blur → nsfw=true → same cache (no fetch).
	setNSFWMode(t, srv, NSFWHide)
	get()
	if reader.callCount() != 1 {
		t.Fatalf("migrated-hide load should hit the true-flag cache: calls = %d, want 1", reader.callCount())
	}
}
