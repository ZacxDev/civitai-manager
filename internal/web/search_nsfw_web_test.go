package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// setMaturity persists the range directly (bypassing the CSRF-guarded settings
// POST) so a test can exercise per-band search behaviour.
func setMaturity(t *testing.T, srv *Server, rng string) {
	t.Helper()
	if _, ok := parseMaturityRange(rng); !ok {
		t.Fatalf("fixture range %q is not valid — the test would assert nothing", rng)
	}
	if err := srv.store.SetSetting(maturitySettingKey, rng); err != nil {
		t.Fatalf("set maturity range: %v", err)
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

// TestSearchNSFWParamFollowsTheRangeMax proves the model-search `nsfw` param
// tracks the range's MAX.
//
// 🔴 /api/v1/models takes ONLY a boolean here — `nsfw=Mature` is an HTTP 400
// whose body reads `expected boolean … expected one of "true"|"1"|"yes"|…`
// (live-probed 2026-07-31). So the range degrades to one bit at this endpoint:
// false restricts the feed to SFW models, true lets NSFW models through WITH
// their showcase images (without it they come back image-less and every card
// reads "No showcase images"). Only a band that tops out at PG can be served by
// the SFW-only feed. The per-IMAGE band filter still runs at render time.
func TestSearchNSFWParamFollowsTheRangeMax(t *testing.T) {
	cases := []struct {
		rng  string
		want string
	}{
		{"pg:pg", "false"},
		{"pg:pg13", "true"},
		{"pg:r", "true"},
		{"r:r", "true"},
		{"xxx:xxx", "true"},
		{"pg:xxx", "true"},
	}
	paths := []string{
		"/search?q=anime",           // keyword search
		"/search",                   // popular default (empty query)
		"/subscribe/search?q=anime", // dashboard subscribe search
	}
	for _, tc := range cases {
		for _, p := range paths {
			t.Run(tc.rng+" "+p, func(t *testing.T) {
				reader := &recordingSearchReader{result: popularResult(t)}
				srv := newModelServer(t, reader)
				setMaturity(t, srv, tc.rng)

				rec := httptest.NewRecorder()
				srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
				if rec.Code != http.StatusOK {
					t.Fatalf("GET %s = %d", p, rec.Code)
				}
				got := lastCall(t, reader)["nsfw"]
				if len(got) == 0 || got[0] != tc.want {
					t.Errorf("range %s: nsfw param = %v, want %q", tc.rng, got, tc.want)
				}
				// A level NAME here is a 400 from the real API — never send one.
				for _, name := range []string{"None", "Soft", "Mature", "X", "XXX", "Blocked"} {
					if len(got) > 0 && got[0] == name {
						t.Errorf("range %s sent the level name %q to /api/v1/models, which answers 400",
							tc.rng, name)
					}
				}
			})
		}
	}
}

// TestPopularCacheIsKeyedByTheNSFWFlag proves the popular TTL cache is keyed by
// the boolean the range resolves to: two ranges that agree on the flag share one
// cached list (no extra fetch), and two that disagree do not.
func TestPopularCacheIsKeyedByTheNSFWFlag(t *testing.T) {
	reader := &recordingSearchReader{result: popularResult(t)}
	srv := newModelServer(t, reader)

	get := func() {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/search", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /search = %d", rec.Code)
		}
	}

	// The full range → nsfw=true. First load fetches + caches.
	setMaturity(t, srv, "pg:xxx")
	get()
	if reader.callCount() != 1 {
		t.Fatalf("first load: calls = %d, want 1", reader.callCount())
	}
	if got := lastCall(t, reader)["nsfw"]; len(got) == 0 || got[0] != "true" {
		t.Errorf("full-range popular fetch nsfw = %v, want true", got)
	}

	// A NARROWER band that still needs NSFW models (R..XXX) → same flag → same
	// cache entry, no refetch. The band filter runs at render time.
	setMaturity(t, srv, "r:xxx")
	get()
	if reader.callCount() != 1 {
		t.Fatalf("a same-flag range should hit the cache: calls = %d, want 1", reader.callCount())
	}

	// PG-only flips the flag to false → a DIFFERENT cache key → a real fetch.
	setMaturity(t, srv, "pg:pg")
	get()
	if reader.callCount() != 2 {
		t.Fatalf("flipping the nsfw flag must not reuse the true-flag cache: calls = %d, want 2",
			reader.callCount())
	}
	if got := lastCall(t, reader)["nsfw"]; len(got) == 0 || got[0] != "false" {
		t.Errorf("PG-only popular fetch nsfw = %v, want false", got)
	}
}
