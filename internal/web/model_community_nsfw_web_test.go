package web

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
)

// ===========================================================================
// "Community images" showed SFW posts ONLY — the OUTBOUND `nsfw` param.
// ===========================================================================
//
// handleModelCommunity used to build its /api/v1/images query with
// modelVersionId + sort + period + limit and NO `nsfw` param, and CivitAI reads
// an absent `nsfw` as "SFW only". Live-probed 2026-07-30 against the real API
// (modelVersionId=3112728, limit=20), counting the returned nsfwLevel labels:
//
//	(absent)     None 20        <- the shipped behaviour: nothing but SFW
//	nsfw=None    None 20
//	nsfw=Soft    Soft 16, None 4
//	nsfw=Mature  Mature 15, Soft 3, X 1, None 1
//	nsfw=X       X 17, Mature 3     <- NO SFW items at all
//	nsfw=true    X 17, Mature 3     <- identical to X
//	nsfw=false   None 20
//	nsfw=bogus   HTTP 400           <- unlike `tag`, this one fails LOUDLY
//
// `nsfw` is a BROWSING LEVEL, not a ceiling. `Mature` is the widest mix that
// STILL returns level-None items, which is why it — and not X/true — is safe for
// a SFW model. Re-probed across 17 model versions: `Mature` never returned FEWER
// items than the no-param request.
//
// A fake reader cannot DISCOVER any of that; these tests only PIN it.

// TestCommunityFeedRequestCarriesTheNSFWLevel is mutation (b): deleting the
// `q.Set("nsfw", …)` line from handleModelCommunity must fail here.
func TestCommunityFeedRequestCarriesTheNSFWLevel(t *testing.T) {
	reader := newModelReader(t)
	var captured url.Values
	reader.lastImageQuery = &captured
	reader.communityImages = []civitai.ImageItem{
		communityImage(1, "https://image.civitai.com/bucket/uuid/a.jpeg", "Mature", "alice", 3, 0),
	}
	srv := newModelServer(t, reader)

	if code, _ := communityReq(t, srv, "/models/7/community?versionId=11"); code != http.StatusOK {
		t.Fatalf("community endpoint = %d", code)
	}

	got, present := captured["nsfw"]
	if !present {
		t.Fatalf("the request carries NO `nsfw` param — CivitAI then returns SFW ONLY "+
			"(live-probed: absent => nsfwLevel None x20), so the section can never show an "+
			"NSFW post. query = %v", captured)
	}
	if len(got) != 1 || got[0] != "Mature" {
		t.Fatalf("nsfw = %v, want exactly [Mature]", got)
	}

	// The VALUE matters as much as its presence, so pin the ones that were rejected
	// and why. X/true drop level-None entirely and would blank a SFW model's feed;
	// None/false are what the bug already did; anything off-enum is an HTTP 400.
	for _, rejected := range []string{"X", "true", "None", "false", "Soft"} {
		if captured.Get("nsfw") == rejected {
			t.Fatalf("nsfw = %q — see the live probe above; only Mature is both "+
				"NSFW-inclusive and SFW-safe", rejected)
		}
	}

	// The rest of the audited query is unchanged by the fix.
	if captured.Get("modelVersionId") != "11" || captured.Get("sort") != "Most Reactions" ||
		captured.Get("period") != "Month" || captured.Get("limit") != "12" {
		t.Errorf("the nsfw fix must not disturb the rest of the query: %v", captured)
	}
}

// TestCommunityFeedNSFWLevelIsTheConstantOnTheWire pins that the wire value is
// the documented constant rather than something derived per-request — the
// browsing level deliberately does NOT follow the app's NSFW toggle (see
// communityImagesNSFWLevel: the toggle is two-state blur⇄show, blur is a
// browser-side filter, and deriving a level from it would invent a third NSFW
// concept and make `blur` act as an access control it is not).
func TestCommunityFeedNSFWLevelIsTheConstantOnTheWire(t *testing.T) {
	if communityImagesNSFWLevel != "Mature" {
		t.Fatalf("communityImagesNSFWLevel = %q; if this is a deliberate change, re-probe "+
			"the live API first — X/true return NO SFW items", communityImagesNSFWLevel)
	}
	for _, mode := range []string{NSFWBlur, NSFWShow} {
		reader := newModelReader(t)
		var captured url.Values
		reader.lastImageQuery = &captured
		reader.communityImages = []civitai.ImageItem{
			communityImage(1, "https://image.civitai.com/bucket/uuid/a.jpeg", "None", "alice", 3, 0),
		}
		srv := newModelServer(t, reader)
		if err := srv.store.SetSetting(nsfwSettingKey, mode); err != nil {
			t.Fatal(err)
		}
		if code, _ := communityReq(t, srv, "/models/7/community?versionId=11"); code != http.StatusOK {
			t.Fatalf("[%s] community endpoint = %d", mode, code)
		}
		if got := captured.Get("nsfw"); got != communityImagesNSFWLevel {
			t.Errorf("[%s] nsfw = %q, want the constant %q — the display toggle must not "+
				"change what is FETCHED", mode, got, communityImagesNSFWLevel)
		}
	}
}

// TestCommunityFeedIgnoresAPreFixCacheEntry is mutation (c): the poisoned-cache
// half of the fix.
//
// Every row written before this change is a body captured with NO `nsfw` param —
// i.e. SFW-only JSON — and the handler serves the cache FIRST and, on any fetch
// failure, serves it stale forever. Without the re-key (store migration 0017) a
// previously-visited model would keep rendering its old SFW-only feed and the fix
// would look like it had not worked.
//
// This seeds exactly that row (the old shape, carried forward with an empty
// level) and proves it is unreachable: the handler refetches and renders the NEW
// body. Reverting GetCommunityCache to a (model_id, version_id) lookup — i.e.
// serving the old-shape entry — must fail here.
func TestCommunityFeedIgnoresAPreFixCacheEntry(t *testing.T) {
	// The FRESH upstream body carries an NSFW post from a distinguishable poster.
	fresh := []civitai.ImageItem{
		communityImage(2, "https://image.civitai.com/bucket/uuid/new.jpeg", "Mature", "fresh_nsfw_poster", 9, 1),
	}
	reader := newModelReader(t)
	reader.communityImages = fresh
	reader.communityRaw = communityRawBody(t, fresh)
	srv := newModelServer(t, reader)

	// Seed a PRE-FIX row: same (model, version), SFW-only body, no browsing level.
	// It is deliberately FRESH (fetched_at = now) so the cache-first shortcut would
	// take it if the level were not part of the key.
	poisoned := communityRawBody(t, []civitai.ImageItem{
		communityImage(1, "https://image.civitai.com/bucket/uuid/old.jpeg", "None", "stale_sfw_poster", 5, 0),
	})
	if _, err := srv.store.DB().Exec(
		`INSERT INTO community_cache (model_id, version_id, nsfw, raw, fetched_at)
		 VALUES (?, ?, '', ?, ?)`,
		7, 11, poisoned, time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}

	code, body := communityReq(t, srv, "/models/7/community?versionId=11")
	if code != http.StatusOK {
		t.Fatalf("community endpoint = %d", code)
	}
	if strings.Contains(body, "stale_sfw_poster") {
		t.Fatalf("the pre-fix (SFW-only) cache entry was served — the nsfw level must be "+
			"part of the cache key or the fix silently does nothing until the TTL expires:\n%s", body)
	}
	if !strings.Contains(body, "fresh_nsfw_poster") {
		t.Fatalf("expected the freshly fetched NSFW-inclusive feed:\n%s", body)
	}

	// …and the refetched body is stored under the CURRENT level, so the next call is
	// a hit at the right key rather than another refetch.
	ent, err := srv.store.GetCommunityCache(7, 11, communityImagesNSFWLevel)
	if err != nil || ent == nil {
		t.Fatalf("the refetched feed should be cached at the current level, got (%v,%v)", ent, err)
	}
	if !strings.Contains(string(ent.Raw), "fresh_nsfw_poster") {
		t.Errorf("cached body is not the fresh one: %s", ent.Raw)
	}
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
