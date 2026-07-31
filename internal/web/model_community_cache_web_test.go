package web

import (
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fullRangeCeiling is the CivitAI `nsfw` ceiling the DEFAULT (full) maturity
// range asks for, and therefore the community cache key every test below uses.
// Spelled out rather than recomputed so a change to imagesNSFWCeiling shows up
// here as a failing cache lookup instead of silently agreeing with itself.
const fullRangeCeiling = "X"

// backdateCommunityCache rewrites a cache row's fetched_at to now-age so tests
// can exercise the stale (fail-open) branch. White-box (same package).
func backdateCommunityCache(t *testing.T, srv *Server, modelID, versionID int, age time.Duration) {
	t.Helper()
	stamp := time.Now().Add(-age).UTC().Format(time.RFC3339)
	if _, err := srv.store.DB().Exec(
		`UPDATE community_cache SET fetched_at = ? WHERE model_id = ? AND version_id = ? AND nsfw = ?`,
		stamp, modelID, versionID, fullRangeCeiling); err != nil {
		t.Fatal(err)
	}
}

// TestCommunityCacheFirstCallFetchesAndCaches proves the first call fetches
// upstream and persists the response to the community cache, keyed by the
// CEILING the current range asks for.
func TestCommunityCacheFirstCallFetchesAndCaches(t *testing.T) {
	reader := newModelReader(t)
	reader.communityRaw = communityBody(t,
		pgItem(1, "https://image.civitai.com/bucket/uuid/a.jpeg", "alice", 5, 0))
	srv := newModelServer(t, reader)

	code, body := communityReq(t, srv, "/models/7/community?versionId=11")
	if code != http.StatusOK || !strings.Contains(body, "alice") {
		t.Fatalf("first call should render the fetched feed, got %d:\n%s", code, body)
	}
	if got := atomic.LoadInt32(reader.searchHits); got != 1 {
		t.Fatalf("first call should fetch exactly once, got %d", got)
	}
	ent, err := srv.store.GetCommunityCache(7, 11, fullRangeCeiling)
	if err != nil || ent == nil {
		t.Fatalf("first call should have cached the feed under the ceiling, got (%v,%v)", ent, err)
	}
}

// TestCommunityCacheSecondCallServesFromCache proves a second call within TTL is
// served from the cache WITHOUT a second upstream fetch (searchHits stays 1).
func TestCommunityCacheSecondCallServesFromCache(t *testing.T) {
	reader := newModelReader(t)
	reader.communityRaw = communityBody(t,
		pgItem(1, "https://image.civitai.com/bucket/uuid/a.jpeg", "alice", 5, 0))
	srv := newModelServer(t, reader)

	_, _ = communityReq(t, srv, "/models/7/community?versionId=11")
	_, body := communityReq(t, srv, "/models/7/community?versionId=11")

	if !strings.Contains(body, "alice") {
		t.Errorf("second (cached) call should still render the feed:\n%s", body)
	}
	if got := atomic.LoadInt32(reader.searchHits); got != 1 {
		t.Fatalf("second call must be served from cache (no re-fetch); searchHits = %d, want 1", got)
	}
}

// TestCommunityCacheKeyIsTheRequestedCeiling is the 🔴 cross-range guard: a body
// fetched for one range must never be served to a range whose ceiling differs.
//
// It primes the cache at the full range (ceiling X) with a distinctive poster,
// then narrows the range to PG-only (ceiling None) and proves the handler goes
// back to the network rather than reusing the wider body — which would hand a
// PG-only user a page built from an X-ceiling response.
func TestCommunityCacheKeyIsTheRequestedCeiling(t *testing.T) {
	reader := newModelReader(t)
	reader.communityRaw = communityBody(t,
		pgItem(1, "https://image.civitai.com/bucket/uuid/wide.jpeg", "wide_poster", 5, 0))
	srv := newModelServer(t, reader)

	if _, _ = communityReq(t, srv, "/models/7/community?versionId=11"); atomic.LoadInt32(reader.searchHits) != 1 {
		t.Fatalf("priming call should have fetched once, got %d", atomic.LoadInt32(reader.searchHits))
	}

	if err := srv.store.SetSetting(maturitySettingKey, "pg:pg"); err != nil {
		t.Fatal(err)
	}
	_, _ = communityReq(t, srv, "/models/7/community?versionId=11")
	if got := atomic.LoadInt32(reader.searchHits); got != 2 {
		t.Fatalf("narrowing the range changes the CEILING, so the cached body must not be "+
			"reused; searchHits = %d, want 2", got)
	}
	// …and the second body was stored under the NEW ceiling, beside the old one.
	if ent, _ := srv.store.GetCommunityCache(7, 11, "None"); ent == nil {
		t.Error("the refetch should be cached under the PG ceiling \"None\"")
	}
	if ent, _ := srv.store.GetCommunityCache(7, 11, fullRangeCeiling); ent == nil {
		t.Error("the original X-ceiling entry should still exist (keys are independent)")
	}
}

// TestCommunityCacheFailOpenServesStale proves a fetch ERROR falls back to the
// last cached entry (even when stale) rather than the error note.
func TestCommunityCacheFailOpenServesStale(t *testing.T) {
	reader := newModelReader(t)
	reader.communityRaw = communityBody(t,
		pgItem(2, "https://image.civitai.com/bucket/uuid/cached.jpeg", "cached_bob", 9, 0))
	srv := newModelServer(t, reader)

	// Prime the cache with a real fetch, then backdate it past the TTL so the next
	// call skips the fresh-cache shortcut and actually attempts a fetch.
	_, _ = communityReq(t, srv, "/models/7/community?versionId=11")
	backdateCommunityCache(t, srv, 7, 11, 2*communityCacheTTL)

	// Now make the upstream fetch fail. The stale cache must be served (fail-open).
	failing := newModelReader(t)
	failing.communityErr = errors.New("civitai down")
	// Reuse the SAME store (so the cached row is visible) by swapping the reader.
	srv.reader = failing

	code, body := communityReq(t, srv, "/models/7/community?versionId=11")
	if code != http.StatusOK {
		t.Fatalf("fail-open call = %d, want 200", code)
	}
	if !strings.Contains(body, "cached_bob") {
		t.Errorf("fetch error with a prior cache should serve the stale feed (fail-open):\n%s", body)
	}
	if strings.Contains(body, "load community images") {
		t.Error("fail-open must NOT show the error note when a cached feed exists")
	}
}

// TestCommunityCacheStaleEntryIsStillRangeFiltered: serving a stale body must not
// bypass the band. The cache is a fetch optimisation, never a filter bypass.
func TestCommunityCacheStaleEntryIsStillRangeFiltered(t *testing.T) {
	reader := newModelReader(t)
	reader.communityRaw = communityBody(t,
		pgItem(1, "https://image.civitai.com/bucket/uuid/pg.jpeg", "pg_poster", 1, 0),
		communityItem{ID: 2, URL: "https://image.civitai.com/bucket/uuid/xxx.jpeg",
			Label: "X", Level: 16, Username: "xxx_poster"},
	)
	srv := newModelServer(t, reader)

	_, _ = communityReq(t, srv, "/models/7/community?versionId=11")
	backdateCommunityCache(t, srv, 7, 11, 2*communityCacheTTL)

	failing := newModelReader(t)
	failing.communityErr = errors.New("civitai down")
	srv.reader = failing

	// A range that still resolves to the SAME ceiling (X) so the stale entry is the
	// one served — but excludes XXX.
	if err := srv.store.SetSetting(maturitySettingKey, "pg:x"); err != nil {
		t.Fatal(err)
	}
	_, body := communityReq(t, srv, "/models/7/community?versionId=11")
	if !strings.Contains(body, "pg_poster") {
		t.Fatalf("the stale entry should still be served (fail-open):\n%s", body)
	}
	if strings.Contains(body, "xxx.jpeg") || strings.Contains(body, "xxx_poster") {
		t.Errorf("a STALE cached body was served past the maturity band:\n%s", body)
	}
}

// TestCommunityCacheErrorNoCacheRendersNothing proves a fetch error with NO usable
// cache degrades to an EMPTY fragment — 200, but no heading and no error note (the
// failure is logged server-side instead of scarring the page).
func TestCommunityCacheErrorNoCacheRendersNothing(t *testing.T) {
	reader := newModelReader(t)
	reader.communityErr = errors.New("boom")
	srv := newModelServer(t, reader)

	code, body := communityReq(t, srv, "/models/7/community?versionId=11")
	if code != http.StatusOK {
		t.Fatalf("errored feed = %d, want 200", code)
	}
	if strings.TrimSpace(body) != "" {
		t.Errorf("fetch error with no cache should render nothing, got:\n%s", body)
	}
}

// TestCommunityCacheEmptyResultServesStale proves an EMPTY fresh result falls
// back to a prior non-empty cache (never blanks a feed the user has seen).
func TestCommunityCacheEmptyResultServesStale(t *testing.T) {
	reader := newModelReader(t)
	reader.communityRaw = communityBody(t,
		pgItem(3, "https://image.civitai.com/bucket/uuid/prev.jpeg", "prev_carol", 4, 0))
	srv := newModelServer(t, reader)

	// Prime + backdate so the next call re-fetches.
	_, _ = communityReq(t, srv, "/models/7/community?versionId=11")
	backdateCommunityCache(t, srv, 7, 11, 2*communityCacheTTL)

	// Now upstream returns an EMPTY (but well-formed) body. The prior non-empty
	// cache must be served rather than an empty (omitted) section.
	empty := newModelReader(t)
	empty.communityRaw = communityBody(t)
	srv.reader = empty

	_, body := communityReq(t, srv, "/models/7/community?versionId=11")
	if !strings.Contains(body, "prev_carol") {
		t.Errorf("empty fresh result should fall back to the non-empty cache:\n%s", body)
	}
}
