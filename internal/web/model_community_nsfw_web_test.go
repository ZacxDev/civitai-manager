package web

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// ===========================================================================
// The OUTBOUND `nsfw` param: a CEILING, derived from the maturity range MAX.
// ===========================================================================
//
// /api/v1/images' `nsfw` is a browsing CEILING that returns a MIX at and below
// it, and OMITTING it is equivalent to asking for SFW only. Live-probed
// 2026-07-31 against the real API (modelVersionId=3112728, limit=100), counting
// (nsfwLevel | browsingLevel):
//
//	nsfw=None    -> None|1 39
//	nsfw=Soft    -> Soft|2 63, None|1 37
//	nsfw=Mature  -> Mature|4 77, Soft|2 17, X|8 1, None|1 5
//	nsfw=X       -> X|16 40, X|8 41, Mature|4 15, Soft|2 3, None|1 1
//	nsfw=XXX     -> HTTP 400   (the enum is None|Soft|Mature|X|Blocked)
//
// So the API cannot express a range: we ask for the ceiling that COVERS the
// range's Max and filter the response by browsingLevel. A fake reader cannot
// DISCOVER any of that; these tests only PIN it.

// TestCommunityRequestCeilingFollowsTheRangeMax proves the outbound `nsfw` value
// tracks the range MAX — and never emits a value the API rejects.
func TestCommunityRequestCeilingFollowsTheRangeMax(t *testing.T) {
	cases := []struct {
		rng     string
		ceiling string
	}{
		{"pg:pg", "None"},
		{"pg:pg13", "Soft"},
		{"pg13:pg13", "Soft"},
		{"pg:r", "Mature"},
		{"r:r", "Mature"},
		{"pg:x", "X"},
		{"x:x", "X"},
		// The API has NO "XXX" ceiling — `nsfw=XXX` is a 400 — so a band at the top
		// of the scale must still ask for X and filter down.
		{"xxx:xxx", "X"},
		{"pg:xxx", "X"},
	}
	for _, c := range cases {
		t.Run(c.rng, func(t *testing.T) {
			reader := newModelReader(t)
			var captured url.Values
			reader.lastImageQuery = &captured
			reader.communityRaw = communityBody(t,
				pgItem(1, "https://image.civitai.com/bucket/uuid/a.jpeg", "alice", 3, 0))
			srv := newModelServer(t, reader)
			if err := srv.store.SetSetting(maturitySettingKey, c.rng); err != nil {
				t.Fatal(err)
			}

			if code, _ := communityReq(t, srv, "/models/7/community?versionId=11"); code != http.StatusOK {
				t.Fatalf("community endpoint = %d", code)
			}

			got, present := captured["nsfw"]
			if !present {
				t.Fatalf("the request carries NO `nsfw` param — CivitAI then returns SFW ONLY "+
					"(live-probed: absent => nsfwLevel None only), so the section can never show "+
					"an NSFW post. query = %v", captured)
			}
			if len(got) != 1 || got[0] != c.ceiling {
				t.Fatalf("range %s sent nsfw = %v, want exactly [%s]", c.rng, got, c.ceiling)
			}
			// The rest of the audited query is unchanged.
			if captured.Get("modelVersionId") != "11" || captured.Get("sort") != "Most Reactions" ||
				captured.Get("period") != "Month" {
				t.Errorf("the ceiling must not disturb the rest of the query: %v", captured)
			}
		})
	}
}

// TestCommunityRequestNeverSendsAnInvalidCeiling is the loud-failure guard. Every
// value outside the API's enum is an HTTP 400, so a bug here breaks the feed
// outright rather than degrading — and "XXX" is exactly the value a naive
// implementation would send for a top-of-scale band.
func TestCommunityRequestNeverSendsAnInvalidCeiling(t *testing.T) {
	valid := map[string]bool{"None": true, "Soft": true, "Mature": true, "X": true}
	// ⚠ The in-memory test store is `file::memory:?cache=shared`, so every
	// store.Open in this process is the SAME database and the community cache
	// SURVIVES across servers. Two ranges that resolve to the same ceiling would
	// then hit the cache and make no request at all — the capture would be empty
	// and this test would assert nothing. A distinct version id per iteration
	// keeps every case on its own cache key.
	version := 1000
	for _, lo := range maturityScale {
		for _, hi := range maturityScale {
			if lo > hi {
				continue
			}
			version++
			rng := maturityRange{Min: lo, Max: hi}
			reader := newModelReader(t)
			var captured url.Values
			reader.lastImageQuery = &captured
			reader.communityRaw = communityBody(t,
				pgItem(1, "https://image.civitai.com/bucket/uuid/a.jpeg", "alice", 3, 0))
			srv := newModelServer(t, reader)
			if err := srv.store.SetSetting(maturitySettingKey, rng.String()); err != nil {
				t.Fatal(err)
			}
			_, _ = communityReq(t, srv, "/models/7/community?versionId="+itoa(version))

			got := captured.Get("nsfw")
			if got == "" {
				t.Fatalf("range %s made no request at all — the case is vacuous", rng.String())
			}
			if !valid[got] {
				t.Errorf("range %s sent nsfw=%q — the API answers HTTP 400 to anything outside "+
					"None|Soft|Mature|X|Blocked", rng.String(), got)
			}
			if got == "XXX" {
				t.Errorf("range %s sent the non-existent XXX ceiling", rng.String())
			}
		}
	}
}

// TestCommunityOverFetchesAndClampsToThePage is the under-fill guard.
//
// The upstream body models the measured worst case: a ceiling-X response where
// the top two levels split roughly half and half (41/40 out of 100 in the live
// probe). A single-level band therefore gets ~40% of the response, so fetching
// only communityPageSize items would render ~5 tiles for a page of 12.
func TestCommunityOverFetchesAndClampsToThePage(t *testing.T) {
	// 48 items alternating X (8) and XXX (16) — what the ceiling returns.
	items := make([]communityItem, 0, communityFetchLimit)
	for i := 0; i < communityFetchLimit; i++ {
		lvl := 8
		if i%2 == 1 {
			lvl = 16
		}
		items = append(items, communityItem{
			ID: 1000 + i, Label: "X", Level: lvl, Username: "p",
			URL: "https://image.civitai.com/bucket/uuid/img" + itoa(i) + ".jpeg",
		})
	}

	reader := newModelReader(t)
	var captured url.Values
	reader.lastImageQuery = &captured
	reader.communityRaw = communityBody(t, items...)
	srv := newModelServer(t, reader)
	if err := srv.store.SetSetting(maturitySettingKey, "x:x"); err != nil {
		t.Fatal(err)
	}

	_, body := communityReq(t, srv, "/models/7/community?versionId=11")

	// The request asked for MORE than a page.
	if got := captured.Get("limit"); got != itoa(communityFetchLimit) {
		t.Fatalf("limit = %q, want %d — without over-fetching a narrow band cannot fill a page",
			got, communityFetchLimit)
	}
	// 24 of the 48 are in band; the page renders exactly communityPageSize.
	if n := strings.Count(body, "cm-masonry-item"); n != communityPageSize {
		t.Errorf("rendered %d tiles, want exactly %d (a full page, clamped)", n, communityPageSize)
	}
	// The clamp must not leak the surplus: tile 12 onwards of the in-band set is
	// simply not rendered, and nothing out of band is rendered at all.
	if strings.Contains(body, "img1.jpeg") {
		t.Error("an XXX item leaked into an X-only band")
	}
}

// TestCommunityShortPageWhenTheBandIsThin is the honest other half: when the
// upstream genuinely has fewer than a page in band, the section renders SHORT
// rather than padding with out-of-band tiles or rendering nothing.
func TestCommunityShortPageWhenTheBandIsThin(t *testing.T) {
	items := []communityItem{
		{ID: 1, URL: "https://image.civitai.com/bucket/uuid/r1.jpeg", Label: "Mature", Level: 4, Username: "a"},
		{ID: 2, URL: "https://image.civitai.com/bucket/uuid/r2.jpeg", Label: "Mature", Level: 4, Username: "b"},
	}
	for i := 0; i < 20; i++ {
		items = append(items, communityItem{
			ID: 100 + i, URL: "https://image.civitai.com/bucket/uuid/x" + itoa(i) + ".jpeg",
			Label: "X", Level: 8, Username: "x",
		})
	}
	reader := newModelReader(t)
	reader.communityRaw = communityBody(t, items...)
	srv := newModelServer(t, reader)
	if err := srv.store.SetSetting(maturitySettingKey, "r:r"); err != nil {
		t.Fatal(err)
	}

	_, body := communityReq(t, srv, "/models/7/community?versionId=11")
	if n := strings.Count(body, "cm-masonry-item"); n != 2 {
		t.Errorf("rendered %d tiles, want 2 — a thin band renders SHORT, it does not pad", n)
	}
	if strings.Contains(body, "/x0.jpeg") {
		t.Error("an out-of-band tile was used to pad a short page")
	}
	if !strings.Contains(body, "Community images") {
		t.Error("a short but non-empty band should still render the section")
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

// TestCommunityFeedApiErrorNeverBreaksTheModelPage: an upstream failure degrades
// to a QUIET empty fragment (200, no heading, no error note) and the model page
// itself still renders — the feed is lazy and off the page's critical path, so a
// civitai outage must never turn into a 500 on /models/{id}.
func TestCommunityFeedApiErrorNeverBreaksTheModelPage(t *testing.T) {
	reader := newModelReader(t)
	reader.communityErr = errors.New("civitai down")
	srv := newModelServer(t, reader)

	code, body := communityReq(t, srv, "/models/7/community?versionId=11")
	if code != http.StatusOK {
		t.Fatalf("an API error must still answer 200; got %d", code)
	}
	if strings.TrimSpace(body) != "" {
		t.Errorf("an API error must render NOTHING, got:\n%s", body)
	}

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/models/7", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("the model page = %d — the community fetch must not be on its critical path", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `id="community-feed"`) {
		t.Error("the model page should still carry the lazy community container")
	}
}
