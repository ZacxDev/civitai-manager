package web

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
)

// communityReq drives the lazy community-feed endpoint and returns the rendered
// fragment body.
func communityReq(t *testing.T, srv *Server, target string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec.Code, rec.Body.String()
}

// TestNSFWLevelFromString unit-tests the string→numeric NSFW mapping incl. the
// fail-closed behavior on unknown/empty labels.
func TestNSFWLevelFromString(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"None", 1},
		{"none", 1},
		{" Soft ", 2},
		{"Mature", 4},
		{"X", 8},
		{"XX", 16},
		{"XXX", 32},
		{"", nsfwLevelUnknown},        // absent → fail closed
		{"weird", nsfwLevelUnknown},   // unknown → fail closed
		{"MatureX", nsfwLevelUnknown}, // unknown → fail closed
	}
	for _, c := range cases {
		if got := nsfwLevelFromString(c.in); got != c.want {
			t.Errorf("nsfwLevelFromString(%q) = %d, want %d", c.in, got, c.want)
		}
	}
	// Safe only for None; everything else (incl. unknown) is NSFW.
	if isNSFWLevel(nsfwLevelFromString("None")) {
		t.Error("None must be treated safe")
	}
	for _, spicy := range []string{"Soft", "Mature", "X", "", "garbage"} {
		if !isNSFWLevel(nsfwLevelFromString(spicy)) {
			t.Errorf("%q must be treated NSFW (fail closed)", spicy)
		}
	}
}

// communityImage builds a canned civitai.ImageItem for the feed tests.
func communityImage(id int, url, nsfwLevel, username string, likes, hearts int) civitai.ImageItem {
	return civitai.ImageItem{
		ID:        id,
		URL:       url,
		NSFWLevel: nsfwLevel,
		Username:  username,
		Stats:     civitai.ImageStats{LikeCount: likes, HeartCount: hearts},
	}
}

// TestCommunityFeedQueryAndTiles proves the handler calls SearchImages with the
// documented params and renders tiles with a thumbnail src, poster username,
// reaction count, and an external civitai.com/images/{id} link (new tab, noopener).
func TestCommunityFeedQueryAndTiles(t *testing.T) {
	reader := newModelReader(t)
	var captured url.Values
	reader.lastImageQuery = &captured
	reader.communityImages = []civitai.ImageItem{
		// Realistic civitai CDN URL (>=3 path segments) so the thumbnail rewrite
		// inserts a width= transform segment.
		communityImage(555, "https://image.civitai.com/bucket/uuid-abc/real.jpeg", "None", "poster_alice", 40, 2),
	}
	srv := newModelServer(t, reader)

	code, body := communityReq(t, srv, "/models/7/community?versionId=11")
	if code != http.StatusOK {
		t.Fatalf("community endpoint = %d", code)
	}

	// Query params.
	if captured.Get("modelVersionId") != "11" {
		t.Errorf("modelVersionId = %q, want 11", captured.Get("modelVersionId"))
	}
	if captured.Get("sort") != "Most Reactions" {
		t.Errorf("sort = %q, want Most Reactions", captured.Get("sort"))
	}
	if captured.Get("period") != "Month" {
		t.Errorf("period = %q, want Month", captured.Get("period"))
	}
	if captured.Get("limit") != "12" {
		t.Errorf("limit = %q, want 12", captured.Get("limit"))
	}

	// Thumbnail src (downscaled → carries a width= transform).
	if !strings.Contains(body, "width=") {
		t.Errorf("tile should use a downscaled thumbnail (width= param):\n%s", body)
	}
	// Poster username + reaction total (40 likes + 2 hearts = 42).
	if !strings.Contains(body, "poster_alice") {
		t.Error("tile should show the poster username")
	}
	if !strings.Contains(body, "42") {
		t.Error("tile should show the reaction count (40+2=42)")
	}
	// External link out to civitai, new tab, hardened.
	if !strings.Contains(body, `href="https://civitai.com/images/555"`) {
		t.Errorf("tile should link out to the civitai image page:\n%s", body)
	}
	if !strings.Contains(body, `target="_blank"`) || !strings.Contains(body, `rel="noopener noreferrer"`) {
		t.Error("external tile link should be target=_blank rel=noopener noreferrer")
	}
}

// TestCommunityFeedNSFWModes proves the feed honors the NSFW display mode: an
// NSFW image is ABSENT under hide, blurred under blur, and plain under show; a
// safe image always renders.
func TestCommunityFeedNSFWModes(t *testing.T) {
	safeURL := "https://image.civitai.com/safe-community.jpeg" // 1 segment → thumb URL unchanged
	nsfwURL := "https://image.civitai.com/nsfw-community.jpeg" // 1 segment → thumb URL unchanged

	// NB: the test store uses a shared in-memory cache, so one server + explicit
	// per-phase SetSetting is used (multiple servers would share one DB and leak
	// the setting between phases).
	reader := newModelReader(t)
	reader.communityImages = []civitai.ImageItem{
		communityImage(1, safeURL, "None", "alice", 3, 0),
		communityImage(2, nsfwURL, "Mature", "bob", 9, 1),
	}
	srv := newModelServer(t, reader)
	setMode := func(mode string) {
		if err := srv.store.SetSetting(nsfwSettingKey, mode); err != nil {
			t.Fatal(err)
		}
	}

	// hide: the NSFW url must NOT appear in the HTML at all.
	setMode(NSFWHide)
	_, body := communityReq(t, srv, "/models/7/community?versionId=11")
	if strings.Contains(body, nsfwURL) {
		t.Error("hide mode must omit the NSFW image URL from the HTML")
	}
	if !strings.Contains(body, safeURL) {
		t.Error("hide mode should still render the safe image")
	}

	// blur: NSFW present but blurred; safe always rendered.
	setMode(NSFWBlur)
	_, body = communityReq(t, srv, "/models/7/community?versionId=11")
	if !strings.Contains(body, nsfwURL) {
		t.Fatal("blur mode should include the NSFW image (blurred)")
	}
	if !strings.Contains(body, "blur-xl") || !strings.Contains(body, `data-blurred="1"`) {
		t.Error("blur mode should blur the NSFW image (blur-xl + data-blurred)")
	}
	if !strings.Contains(body, "click to reveal") {
		t.Error("blurred NSFW image should offer click-to-reveal")
	}
	if !strings.Contains(body, safeURL) {
		t.Error("blur mode should still render the safe image")
	}

	// show: nothing blurred.
	setMode(NSFWShow)
	_, body = communityReq(t, srv, "/models/7/community?versionId=11")
	if !strings.Contains(body, nsfwURL) {
		t.Error("show mode should include the NSFW image")
	}
	if strings.Contains(body, `data-blurred="1"`) {
		t.Error("show mode must not blur any image")
	}
}

// TestCommunityFeedEmptyAndError proves the lazy fragment degrades gracefully:
// empty items → "No community images yet."; a reader error → a graceful
// "Couldn't load…" note (never a broken page / error alert).
func TestCommunityFeedEmptyAndError(t *testing.T) {
	// Empty items.
	reader := newModelReader(t)
	reader.communityImages = []civitai.ImageItem{} // non-nil empty → no items
	srv := newModelServer(t, reader)
	code, body := communityReq(t, srv, "/models/7/community?versionId=11")
	if code != http.StatusOK {
		t.Fatalf("empty feed = %d, want 200", code)
	}
	if !strings.Contains(body, "No community images yet.") {
		t.Errorf("empty feed should show the empty note:\n%s", body)
	}

	// Reader error.
	reader = newModelReader(t)
	reader.communityErr = errors.New("boom")
	srv = newModelServer(t, reader)
	code, body = communityReq(t, srv, "/models/7/community?versionId=11")
	if code != http.StatusOK {
		t.Fatalf("errored feed = %d, want 200 (graceful)", code)
	}
	// gomponents escapes the apostrophe (Couldn&#39;t), so assert the plain tail.
	if !strings.Contains(body, "load community images") {
		t.Errorf("errored feed should show the graceful note:\n%s", body)
	}

	// Missing versionId → empty note (no fetch attempted).
	reader = newModelReader(t)
	reader.communityErr = errors.New("should-not-be-called")
	srv = newModelServer(t, reader)
	code, body = communityReq(t, srv, "/models/7/community")
	if code != http.StatusOK || !strings.Contains(body, "No community images yet.") {
		t.Errorf("missing versionId should render the empty note, got %d:\n%s", code, body)
	}

	// Non-numeric versionId → empty note, and NO upstream call (the guard rejects
	// it before spending a round trip): communityErr would fire if SearchImages ran.
	reader = newModelReader(t)
	reader.communityErr = errors.New("should-not-be-called")
	srv = newModelServer(t, reader)
	code, body = communityReq(t, srv, "/models/7/community?versionId=abc")
	if code != http.StatusOK || !strings.Contains(body, "No community images yet.") {
		t.Errorf("non-numeric versionId should render the empty note without fetching, got %d:\n%s", code, body)
	}
}
