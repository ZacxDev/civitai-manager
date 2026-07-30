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

	// A stored hide migrates to blur: the NSFW url IS present (blurred), not omitted.
	setMode(NSFWHide)
	_, body := communityReq(t, srv, "/models/7/community?versionId=11")
	if !strings.Contains(body, nsfwURL) {
		t.Error("migrated hide (→blur) should include the NSFW image URL")
	}
	if !strings.Contains(body, `data-blurred="1"`) {
		t.Error("migrated hide (→blur) should blur the NSFW image")
	}
	if !strings.Contains(body, safeURL) {
		t.Error("migrated hide (→blur) should still render the safe image")
	}

	// blur: NSFW present but blurred; safe always rendered.
	setMode(NSFWBlur)
	_, body = communityReq(t, srv, "/models/7/community?versionId=11")
	if !strings.Contains(body, nsfwURL) {
		t.Fatal("blur mode should include the NSFW image (blurred)")
	}
	if !strings.Contains(body, "cm-blur") || !strings.Contains(body, `data-blurred="1"`) {
		t.Error("blur mode should blur the NSFW image (cm-blur + data-blurred)")
	}
	if !strings.Contains(body, "reveal") {
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

// TestCommunityFeedEmptyAndError proves the lazy fragment degrades to NOTHING —
// no "Community images" heading, no empty state, no error note — in every path
// that yields zero renderable tiles, while still answering 200 so htmx swaps an
// empty container rather than erroring. The section is only ever rendered when
// there is at least one image to show.
func TestCommunityFeedEmptyAndError(t *testing.T) {
	cases := []struct {
		name   string
		target string
		setup  func(r *fakeReader)
	}{
		{
			name:   "empty items",
			target: "/models/7/community?versionId=11",
			setup:  func(r *fakeReader) { r.communityImages = []civitai.ImageItem{} }, // non-nil empty
		},
		{
			name:   "reader error",
			target: "/models/7/community?versionId=11",
			setup:  func(r *fakeReader) { r.communityErr = errors.New("boom") },
		},
		{
			// No versionId → no fetch attempted; communityErr would fire if it were.
			name:   "missing versionId",
			target: "/models/7/community",
			setup:  func(r *fakeReader) { r.communityErr = errors.New("should-not-be-called") },
		},
		{
			// Non-numeric versionId is rejected before spending an upstream round trip.
			name:   "non-numeric versionId",
			target: "/models/7/community?versionId=abc",
			setup:  func(r *fakeReader) { r.communityErr = errors.New("should-not-be-called") },
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			reader := newModelReader(t)
			c.setup(&reader)
			srv := newModelServer(t, reader)

			code, body := communityReq(t, srv, c.target)
			if code != http.StatusOK {
				t.Fatalf("feed = %d, want 200 (graceful)", code)
			}
			if strings.TrimSpace(body) != "" {
				t.Errorf("a feed with no images must render NOTHING, got:\n%s", body)
			}
			// Belt and braces: neither the heading nor either old note may appear.
			for _, banned := range []string{
				"Community images", "No community images yet.", "load community images",
			} {
				if strings.Contains(body, banned) {
					t.Errorf("empty feed must not render %q:\n%s", banned, body)
				}
			}
		})
	}
}

// TestCommunityFeedRendersWhenNonEmpty is the other half of the rule above: with
// at least one image the section IS present, heading and all.
func TestCommunityFeedRendersWhenNonEmpty(t *testing.T) {
	reader := newModelReader(t)
	reader.communityImages = []civitai.ImageItem{
		communityImage(9, "https://image.civitai.com/bucket/uuid/one.jpeg", "None", "dana", 1, 0),
	}
	srv := newModelServer(t, reader)

	code, body := communityReq(t, srv, "/models/7/community?versionId=11")
	if code != http.StatusOK {
		t.Fatalf("feed = %d, want 200", code)
	}
	if !strings.Contains(body, "Community images") {
		t.Errorf("a non-empty feed should render the section heading:\n%s", body)
	}
	if !strings.Contains(body, "dana") {
		t.Errorf("a non-empty feed should render its tiles:\n%s", body)
	}
}
