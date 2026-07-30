package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
)

// communityRawBody marshals a set of images into an /api/v1/images-shaped body
// (the bytes SearchImages would return on .Raw and the community cache stores).
func communityRawBody(t *testing.T, imgs []civitai.ImageItem) []byte {
	t.Helper()
	b, err := json.Marshal(civitai.ImageSearchResult{Items: imgs})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// backdateCommunityCache rewrites a cache row's fetched_at to now-age so tests
// can exercise the stale (fail-open) branch. White-box (same package).
func backdateCommunityCache(t *testing.T, srv *Server, modelID, versionID int, age time.Duration) {
	t.Helper()
	stamp := time.Now().Add(-age).UTC().Format(time.RFC3339)
	if _, err := srv.store.DB().Exec(
		`UPDATE community_cache SET fetched_at = ? WHERE model_id = ? AND version_id = ?`,
		stamp, modelID, versionID); err != nil {
		t.Fatal(err)
	}
}

// TestCommunityCacheFirstCallFetchesAndCaches proves the first call fetches
// upstream and persists the response to the community cache.
func TestCommunityCacheFirstCallFetchesAndCaches(t *testing.T) {
	imgs := []civitai.ImageItem{
		communityImage(1, "https://image.civitai.com/bucket/uuid/a.jpeg", "None", "alice", 5, 0),
	}
	reader := newModelReader(t)
	reader.communityImages = imgs
	reader.communityRaw = communityRawBody(t, imgs)
	srv := newModelServer(t, reader)

	code, body := communityReq(t, srv, "/models/7/community?versionId=11")
	if code != http.StatusOK || !strings.Contains(body, "alice") {
		t.Fatalf("first call should render the fetched feed, got %d:\n%s", code, body)
	}
	if got := atomic.LoadInt32(reader.searchHits); got != 1 {
		t.Fatalf("first call should fetch exactly once, got %d", got)
	}
	// The response was cached under (7, 11).
	ent, err := srv.store.GetCommunityCache(7, 11)
	if err != nil || ent == nil {
		t.Fatalf("first call should have cached the feed, got (%v,%v)", ent, err)
	}
}

// TestCommunityCacheSecondCallServesFromCache proves a second call within TTL is
// served from the cache WITHOUT a second upstream fetch (searchHits stays 1).
func TestCommunityCacheSecondCallServesFromCache(t *testing.T) {
	imgs := []civitai.ImageItem{
		communityImage(1, "https://image.civitai.com/bucket/uuid/a.jpeg", "None", "alice", 5, 0),
	}
	reader := newModelReader(t)
	reader.communityImages = imgs
	reader.communityRaw = communityRawBody(t, imgs)
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

// TestCommunityCacheFailOpenServesStale proves a fetch ERROR falls back to the
// last cached entry (even when stale) rather than the error note.
func TestCommunityCacheFailOpenServesStale(t *testing.T) {
	imgs := []civitai.ImageItem{
		communityImage(2, "https://image.civitai.com/bucket/uuid/cached.jpeg", "None", "cached_bob", 9, 0),
	}
	reader := newModelReader(t)
	reader.communityImages = imgs
	reader.communityRaw = communityRawBody(t, imgs)
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
	imgs := []civitai.ImageItem{
		communityImage(3, "https://image.civitai.com/bucket/uuid/prev.jpeg", "None", "prev_carol", 4, 0),
	}
	reader := newModelReader(t)
	reader.communityImages = imgs
	reader.communityRaw = communityRawBody(t, imgs)
	srv := newModelServer(t, reader)

	// Prime + backdate so the next call re-fetches.
	_, _ = communityReq(t, srv, "/models/7/community?versionId=11")
	backdateCommunityCache(t, srv, 7, 11, 2*communityCacheTTL)

	// Now upstream returns an EMPTY (non-nil) result. The prior non-empty cache
	// must be served rather than an empty (omitted) section.
	empty := newModelReader(t)
	empty.communityImages = []civitai.ImageItem{}
	srv.reader = empty

	_, body := communityReq(t, srv, "/models/7/community?versionId=11")
	if !strings.Contains(body, "prev_carol") {
		t.Errorf("empty fresh result should fall back to the non-empty cache:\n%s", body)
	}
}
