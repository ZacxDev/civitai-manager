package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

// communityItem is one fixture image for the community-feed tests.
//
// 🔴 Label and Level are SEPARATE fields on purpose. On the real /api/v1/images
// payload they are separate keys that DISAGREE about the top of the scale:
// `nsfwLevel` is a string that says "X" for both browsingLevel 8 and 16, while
// `browsingLevel` is the number that tells them apart (measured 2026-07-31: 41
// items at 8 and 40 at 16 on one nsfw=X response, all labelled "X").
//
// Keeping both in the fixture is what lets a test prove the render path reads the
// NUMBER: set Label to something that would give the wrong answer and watch the
// assertion hold.
type communityItem struct {
	ID       int
	URL      string
	Label    string // the string nsfwLevel, as CivitAI writes it
	Level    int    // the numeric browsingLevel — authoritative
	Username string
	Likes    int
	Hearts   int
	Width    int
	Height   int
}

// communityBody marshals fixture items into an /api/v1/images-shaped body — the
// exact bytes SearchImages returns on .Raw and the community cache stores.
//
// It is hand-built rather than marshalled from civitai.ImageItem because that
// type has NO browsingLevel field: a body produced from it could never carry the
// one signal the maturity filter reads.
func communityBody(t *testing.T, items ...communityItem) []byte {
	t.Helper()
	parts := make([]string, 0, len(items))
	for _, it := range items {
		parts = append(parts, fmt.Sprintf(
			`{"id":%d,"url":%s,"nsfwLevel":%s,"browsingLevel":%d,"username":%s,"width":%d,"height":%d,`+
				`"stats":{"likeCount":%d,"heartCount":%d}}`,
			it.ID, mustJSON(t, it.URL), mustJSON(t, it.Label), it.Level, mustJSON(t, it.Username),
			it.Width, it.Height, it.Likes, it.Hearts))
	}
	return []byte(`{"items":[` + strings.Join(parts, ",") + `],"metadata":{}}`)
}

func mustJSON(t *testing.T, v string) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// pgItem is the common "a plain SFW community post" fixture.
func pgItem(id int, url, username string, likes, hearts int) communityItem {
	return communityItem{ID: id, URL: url, Label: "None", Level: 1,
		Username: username, Likes: likes, Hearts: hearts}
}

// TestCommunityFeedQueryAndTiles proves the handler calls SearchImages with the
// documented params and renders tiles with a thumbnail src, poster username,
// reaction count, and an external civitai.com/images/{id} link (new tab, noopener).
func TestCommunityFeedQueryAndTiles(t *testing.T) {
	reader := newModelReader(t)
	var captured url.Values
	reader.lastImageQuery = &captured
	// Realistic civitai CDN URL (>=3 path segments) so the thumbnail rewrite
	// inserts a width= transform segment.
	reader.communityRaw = communityBody(t,
		pgItem(555, "https://image.civitai.com/bucket/uuid-abc/real.jpeg", "poster_alice", 40, 2))
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
	// The request over-fetches: the `nsfw` ceiling returns a MIX, so the band the
	// user selected is only a share of it (see communityFetchLimit).
	if got, want := captured.Get("limit"), "48"; got != want {
		t.Errorf("limit = %q, want %q (communityPageSize x4)", got, want)
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

// TestCommunityFeedOmitsOutOfRangeServerSide is the 🔴 omission guard for this
// surface: an out-of-band image's URL must be ABSENT FROM THE BYTES, not merely
// styled differently. A CSS-only implementation passes every "is it blurred"
// assertion and still ships the pixels.
func TestCommunityFeedOmitsOutOfRangeServerSide(t *testing.T) {
	const (
		pgURL  = "https://image.civitai.com/bucket/uuid/lvl-pg.jpeg"
		rURL   = "https://image.civitai.com/bucket/uuid/lvl-r.jpeg"
		xURL   = "https://image.civitai.com/bucket/uuid/lvl-x.jpeg"
		xxxURL = "https://image.civitai.com/bucket/uuid/lvl-xxx.jpeg"
	)
	body := communityBody(t,
		communityItem{ID: 1, URL: pgURL, Label: "None", Level: 1, Username: "pg_poster"},
		communityItem{ID: 2, URL: rURL, Label: "Mature", Level: 4, Username: "r_poster"},
		// The two below carry the SAME string label and different numbers.
		communityItem{ID: 3, URL: xURL, Label: "X", Level: 8, Username: "x_poster"},
		communityItem{ID: 4, URL: xxxURL, Label: "X", Level: 16, Username: "xxx_poster"},
	)

	cases := []struct {
		rng     string
		present []string
		absent  []string
	}{
		{"pg:xxx", []string{pgURL, rURL, xURL, xxxURL}, nil},
		{"pg:pg", []string{pgURL}, []string{rURL, xURL, xxxURL}},
		{"r:r", []string{rURL}, []string{pgURL, xURL, xxxURL}},
		// X only: the XXX item is labelled "X" too, so a string implementation keeps it.
		{"x:x", []string{xURL}, []string{pgURL, rURL, xxxURL}},
		{"xxx:xxx", []string{xxxURL}, []string{pgURL, rURL, xURL}},
		{"pg:x", []string{pgURL, rURL, xURL}, []string{xxxURL}},
		{"r:xxx", []string{rURL, xURL, xxxURL}, []string{pgURL}},
	}
	for _, c := range cases {
		t.Run(c.rng, func(t *testing.T) {
			reader := newModelReader(t)
			reader.communityRaw = body
			srv := newModelServer(t, reader)
			if err := srv.store.SetSetting(maturitySettingKey, c.rng); err != nil {
				t.Fatal(err)
			}
			_, out := communityReq(t, srv, "/models/7/community?versionId=11")
			for _, u := range c.present {
				if !strings.Contains(out, thumbFragment(u)) {
					t.Errorf("range %s should render %s:\n%s", c.rng, u, out)
				}
			}
			for _, u := range c.absent {
				if strings.Contains(out, thumbFragment(u)) {
					t.Errorf("range %s LEAKED %s — an out-of-range URL must not reach the DOM "+
						"at all, not merely be styled differently:\n%s", c.rng, u, out)
				}
			}
			// Blur is gone: nothing may be obscured client-side any more.
			for _, dead := range []string{"cm-blur", "data-blurred", "cmReveal", ">reveal<"} {
				if strings.Contains(out, dead) {
					t.Errorf("range %s emitted the dead blur marker %q", c.rng, dead)
				}
			}
		})
	}
}

// thumbFragment is the distinctive part of a rendered thumbnail src: the tile
// rewrites the URL with a width= transform, so the raw URL never appears
// verbatim. Matching on the trailing path keeps the assertion honest.
//
// ⚠ The fixture filenames are deliberately "lvl-pg / lvl-r / lvl-x / lvl-xxx":
// with the obvious "x.jpeg" / "xxx.jpeg" the fragment for X is a SUBSTRING of the
// one for XXX, so an "is it absent" assertion silently matches the wrong tile and
// reports a leak that is not there.
func thumbFragment(u string) string {
	i := strings.LastIndex(u, "/")
	return u[i+1:]
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
		setup  func(t *testing.T, r *fakeReader)
	}{
		{
			name:   "empty items",
			target: "/models/7/community?versionId=11",
			setup:  func(t *testing.T, r *fakeReader) { r.communityRaw = communityBody(t) },
		},
		{
			name:   "reader error",
			target: "/models/7/community?versionId=11",
			setup:  func(t *testing.T, r *fakeReader) { r.communityErr = errors.New("boom") },
		},
		{
			// Every item is outside the band → nothing renders, and the section is
			// omitted rather than left as a heading over a blank.
			name:   "everything filtered out",
			target: "/models/7/community?versionId=11",
			setup: func(t *testing.T, r *fakeReader) {
				r.communityRaw = communityBody(t,
					communityItem{ID: 1, URL: "https://image.civitai.com/b/u/x.jpeg", Label: "X", Level: 16, Username: "z"})
			},
		},
		{
			// No versionId → no fetch attempted; communityErr would fire if it were.
			name:   "missing versionId",
			target: "/models/7/community",
			setup:  func(t *testing.T, r *fakeReader) { r.communityErr = errors.New("should-not-be-called") },
		},
		{
			// Non-numeric versionId is rejected before spending an upstream round trip.
			name:   "non-numeric versionId",
			target: "/models/7/community?versionId=abc",
			setup:  func(t *testing.T, r *fakeReader) { r.communityErr = errors.New("should-not-be-called") },
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			reader := newModelReader(t)
			c.setup(t, &reader)
			srv := newModelServer(t, reader)
			if c.name == "everything filtered out" {
				if err := srv.store.SetSetting(maturitySettingKey, "pg:pg"); err != nil {
					t.Fatal(err)
				}
			}

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
	reader.communityRaw = communityBody(t,
		pgItem(9, "https://image.civitai.com/bucket/uuid/one.jpeg", "dana", 1, 0))
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

// TestCommunityFeedRawlessResultIsUnusable: a reader that returns items but NO
// raw body cannot be rendered, because the per-item browsingLevel lives only in
// the raw bytes. The handler must degrade quietly rather than guess.
func TestCommunityFeedRawlessResultIsUnusable(t *testing.T) {
	reader := newModelReader(t)
	srv := newModelServer(t, reader)
	// rawlessReader returns one item with no Raw at all.
	srv.reader = rawlessReader{inner: reader}

	code, body := communityReq(t, srv, "/models/7/community?versionId=11")
	if code != http.StatusOK {
		t.Fatalf("feed = %d, want 200", code)
	}
	if strings.TrimSpace(body) != "" {
		t.Errorf("a result with no raw body must render nothing (its maturity is unknowable), got:\n%s", body)
	}
}

type rawlessReader struct{ inner fakeReader }

func (r rawlessReader) GetModel(ctx context.Context, id string) (*civitai.ModelDetail, []byte, error) {
	return r.inner.GetModel(ctx, id)
}
func (r rawlessReader) GetModelVersion(ctx context.Context, id string) (*civitai.ModelVersionDetail, []byte, error) {
	return r.inner.GetModelVersion(ctx, id)
}
func (r rawlessReader) GetModelVersionByHash(ctx context.Context, h string) (*civitai.ModelVersionDetail, []byte, error) {
	return r.inner.GetModelVersionByHash(ctx, h)
}
func (r rawlessReader) GetModelVersionsByHashes(ctx context.Context, h []string) ([]civitai.HashMatch, error) {
	return r.inner.GetModelVersionsByHashes(ctx, h)
}
func (r rawlessReader) SearchModels(ctx context.Context, q url.Values) (*civitai.ModelSearchResult, error) {
	return r.inner.SearchModels(ctx, q)
}
func (r rawlessReader) SearchCreators(ctx context.Context, q url.Values) (*civitai.CreatorSearchResult, error) {
	return r.inner.SearchCreators(ctx, q)
}
func (r rawlessReader) SearchImages(context.Context, url.Values) (*civitai.ImageSearchResult, error) {
	return &civitai.ImageSearchResult{Items: []civitai.ImageItem{
		{ID: 1, URL: "https://image.civitai.com/b/u/leak.jpeg", NSFWLevel: "None", Username: "nobody"},
	}}, nil
}
