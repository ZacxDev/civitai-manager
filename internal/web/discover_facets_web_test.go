package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
)

// ---------------------------------------------------------------------------
// Outbound param building
// ---------------------------------------------------------------------------

// TestFacetParamsEncodeExactly asserts the ACTUAL ENCODED QUERY STRING for each
// facet combination, not just individual Get()s. This is deliberately the
// strictest possible assertion because this repo's whole class of shipped bugs
// (types-singular, comma-joined baseModels, comma-joined tag) is invisible to a
// per-key check: url.Values.Get returns the first value, so a comma-joined
// "Illustrious,Pony" and a correct repeated pair both "pass" a Get-based test
// while only one of them works against the real API.
func TestFacetParamsEncodeExactly(t *testing.T) {
	flux, _ := civitai.EcosystemBySlug("flux1")
	sdxl, _ := civitai.EcosystemBySlug("sdxl")

	cases := []struct {
		name                     string
		query, sort, period, tag string
		nsfw                     bool
		eco                      *civitai.Ecosystem
		want                     string
	}{
		{
			name: "no facet, browse default",
			sort: "Most Downloaded", period: "Month", nsfw: true,
			want: "limit=24&nsfw=true&period=Month&sort=Most+Downloaded&types=Workflows",
		},
		{
			name: "ecosystem only — REPEATED baseModels, never comma-joined",
			sort: "Most Downloaded", period: "Month", nsfw: true, eco: &flux,
			want: "baseModels=Flux.1+D&baseModels=Flux.1+S&baseModels=Flux.1+Kontext&baseModels=Flux.1+Krea&" +
				"limit=24&nsfw=true&period=Month&sort=Most+Downloaded&types=Workflows",
		},
		{
			name: "use case only — exactly ONE tag value",
			sort: "Most Downloaded", period: "Month", nsfw: true, tag: "inpaint",
			want: "limit=24&nsfw=true&period=Month&sort=Most+Downloaded&tag=inpaint&types=Workflows",
		},
		{
			name:  "ecosystem + use case + query + sort + period",
			query: "portrait", sort: "Newest", period: "Week", nsfw: true, eco: &sdxl, tag: "upscaler",
			want: "baseModels=SDXL+1.0&baseModels=SDXL+0.9&baseModels=SDXL+1.0+LCM&baseModels=SDXL+Lightning&" +
				"baseModels=SDXL+Hyper&baseModels=SDXL+Turbo&baseModels=Illustrious&baseModels=Pony&" +
				"baseModels=Pony+V7&baseModels=NoobAI&" +
				"limit=24&nsfw=true&period=Week&query=portrait&sort=Newest&tag=upscaler&types=Workflows",
		},
		{
			name: "nsfw hide posture — nsfw=false reaches the wire",
			sort: "Most Downloaded", period: "AllTime", nsfw: false, eco: &flux,
			want: "baseModels=Flux.1+D&baseModels=Flux.1+S&baseModels=Flux.1+Kontext&baseModels=Flux.1+Krea&" +
				"limit=24&nsfw=false&period=AllTime&sort=Most+Downloaded&types=Workflows",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := workflowSearchParams(tc.query, tc.sort, tc.period, tc.nsfw, tc.eco, tc.tag).Encode()
			if got != tc.want {
				t.Errorf("encoded query mismatch\n got: %s\nwant: %s", got, tc.want)
			}
		})
	}
}

// TestFacetParamsNeverCommaJoin is the direct guard on the two live-verified
// silent failures: a comma-joined baseModels returns ZERO items, and a
// comma-joined tag is silently IGNORED (returns the unfiltered feed).
func TestFacetParamsNeverCommaJoin(t *testing.T) {
	for _, e := range civitai.Ecosystems() {
		eco := e
		q := workflowSearchParams("", "Most Downloaded", "Month", true, &eco, "")
		if len(q["baseModels"]) != len(e.BaseModels) {
			t.Errorf("%s: sent %d baseModels params for %d values — must be one REPEATED param each",
				e.Slug, len(q["baseModels"]), len(e.BaseModels))
		}
		for _, v := range q["baseModels"] {
			if strings.Contains(v, ",") {
				t.Errorf("%s: baseModels value %q is comma-joined — upstream returns ZERO items for that", e.Slug, v)
			}
		}
	}
	for _, u := range civitai.UseCases() {
		for _, tag := range u.QueryTags() {
			q := workflowSearchParams("", "Most Downloaded", "Month", true, nil, tag)
			if len(q["tag"]) != 1 {
				t.Errorf("%s: sent %d tag params — upstream 400s on a repeated tag", u.Slug, len(q["tag"]))
			}
			if strings.Contains(q.Get("tag"), ",") {
				t.Errorf("%s: tag %q is comma-joined — upstream silently ignores it", u.Slug, q.Get("tag"))
			}
		}
	}
}

// TestFacetParamsAlwaysPinTypesPlural — every facet request must still carry the
// PLURAL types param; the singular form is silently ignored upstream and would
// return mixed Checkpoints/LoRAs on a "workflows" page.
func TestFacetParamsAlwaysPinTypesPlural(t *testing.T) {
	eco, _ := civitai.EcosystemBySlug("wan")
	q := workflowSearchParams("x", "Newest", "Week", true, &eco, "video")
	if q.Get("types") != "Workflows" {
		t.Errorf("types = %q, want Workflows", q.Get("types"))
	}
	if q.Get("type") != "" {
		t.Errorf("singular type must never be set, got %q", q.Get("type"))
	}
}

// ---------------------------------------------------------------------------
// Facet normalization — hostile input must never reach the wire
// ---------------------------------------------------------------------------

// TestUnknownFacetValuesNeverReachOutboundRequest is the security-shaped test.
// The chosen rule is IGNORE (drop the facet), and the reason it must be enforced
// server-side is that CivitAI does NOT reject a bad facet: an unknown `tag` is
// silently dropped upstream and the UNFILTERED feed comes back, so a passed-
// through value would render as a working filter that is lying to the user.
func TestUnknownFacetValuesNeverReachOutboundRequest(t *testing.T) {
	hostile := []string{
		"nope",
		"Illustrious",                    // a baseModel VALUE, not a slug
		"inpaint,upscale",                // the comma form upstream ignores
		"sdxl%26baseModels%3DEverything", // param smuggling
		"sdxl'%20OR%201%3D1--",           // injection shape
		strings.Repeat("a", 4096),        // absurd length
		"..%2F..%2Fetc%2Fpasswd",
		"%00sdxl",
	}
	for _, bad := range hostile {
		t.Run(bad[:min(len(bad), 24)], func(t *testing.T) {
			reader := &recordingSearchReader{result: workflowResult(t)}
			srv := newModelServer(t, reader)

			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
				"/workflows/discover?eco="+bad+"&use="+bad, nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("GET = %d, want 200 (unknown facets are ignored, not an error)", rec.Code)
			}
			if reader.callCount() != 1 {
				t.Fatalf("want exactly 1 upstream call (facets dropped → no fan-out), got %d", reader.callCount())
			}
			reader.mu.Lock()
			q := reader.calls[0]
			reader.mu.Unlock()
			if len(q["baseModels"]) != 0 {
				t.Errorf("an unknown ?eco= leaked baseModels=%v to the outbound request", q["baseModels"])
			}
			if len(q["tag"]) != 0 {
				t.Errorf("an unknown ?use= leaked tag=%v to the outbound request", q["tag"])
			}
			// And the whole encoded query must contain no trace of the input.
			if strings.Contains(q.Encode(), url.QueryEscape(bad)) {
				t.Errorf("hostile facet value reached the wire: %s", q.Encode())
			}
			// The UI must not light a chip for a facet that was dropped.
			body := rec.Body.String()
			if strings.Contains(body, "cm-facet-chip-on") {
				n := strings.Count(body, "cm-facet-chip-on")
				// The two "All …" chips are legitimately selected.
				if n != 2 {
					t.Errorf("dropped facet still renders a selected chip (%d on-chips)", n)
				}
			}
		})
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ---------------------------------------------------------------------------
// Fan-out, merge, dedupe
// ---------------------------------------------------------------------------

// tagSearchReader returns a DIFFERENT result set per `tag` param, mirroring the
// live-verified reality that CivitAI does not unify tag synonyms (tag=detailer,
// tag=adetailer and tag=facedetailer each return different models).
type tagSearchReader struct {
	fakeReader
	mu      sync.Mutex
	calls   []url.Values
	byTag   map[string][]int // tag → model ids returned
	noTag   []int
	errored map[string]bool
}

func (r *tagSearchReader) SearchModels(_ context.Context, q url.Values) (*civitai.ModelSearchResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, q)
	tag := q.Get("tag")
	if r.errored[tag] {
		return nil, context.DeadlineExceeded
	}
	ids := r.noTag
	if tag != "" {
		ids = r.byTag[tag]
	}
	items := make([]any, 0, len(ids))
	list := make([]civitai.ModelListItem, 0, len(ids))
	for _, id := range ids {
		items = append(items, map[string]any{
			"id": id, "name": "wf" + itoaTest(id), "type": "Workflows",
			"modelVersions": []any{map[string]any{
				"id": id * 10, "name": "v1", "publishedAt": "2026-01-02T00:00:00.000Z",
				"images": []any{map[string]any{
					"url": "https://image.civitai.com/m" + itoaTest(id) + ".jpeg", "nsfwLevel": 1, "type": "image",
				}},
			}},
		})
		list = append(list, civitai.ModelListItem{ID: id, Name: "wf" + itoaTest(id), Type: "Workflows"})
	}
	raw, err := json.Marshal(map[string]any{"items": items})
	if err != nil {
		return nil, err
	}
	return &civitai.ModelSearchResult{Items: list, Raw: raw}, nil
}

func (r *tagSearchReader) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

func (r *tagSearchReader) tags() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.calls))
	for _, c := range r.calls {
		out = append(out, c.Get("tag"))
	}
	return out
}

func itoaTest(i int) string {
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

// TestUseCaseFansOutPerTagAndMergesDeduped: `tag` is single-valued upstream, so a
// use case must issue one request per synonym and merge them. The same model
// carrying two synonyms must appear ONCE.
func TestUseCaseFansOutPerTagAndMergesDeduped(t *testing.T) {
	reader := &tagSearchReader{byTag: map[string][]int{
		"detailer":     {1, 2},
		"adetailer":    {2, 3}, // 2 overlaps with detailer
		"facedetailer": {3, 4}, // 3 overlaps with adetailer
	}}
	srv := newModelServer(t, reader)

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/workflows/discover?use=detailer", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET = %d", rec.Code)
	}
	if got := reader.callCount(); got != civitai.MaxUseCaseTagQueries {
		t.Fatalf("upstream calls = %d, want the capped fan-out %d", got, civitai.MaxUseCaseTagQueries)
	}
	wantTags := []string{"detailer", "adetailer", "facedetailer"}
	if strings.Join(reader.tags(), ",") != strings.Join(wantTags, ",") {
		t.Errorf("fan-out tags = %v, want %v (table order, primary first)", reader.tags(), wantTags)
	}
	body := rec.Body.String()
	for _, id := range []string{"wf1", "wf2", "wf3", "wf4"} {
		if !strings.Contains(body, id) {
			t.Errorf("merged result missing %s", id)
		}
	}
	// Dedup: model 2 appears in two fan-out pages but must render one card.
	if n := strings.Count(body, `/models/2"`); n > 1 {
		t.Errorf("model 2 rendered %d times — merge must dedupe by model id", n)
	}
}

// TestFacetFanOutToleratesPartialFailure — one flaky upstream request must not
// blank an otherwise-good facet page.
func TestFacetFanOutToleratesPartialFailure(t *testing.T) {
	reader := &tagSearchReader{
		byTag:   map[string][]int{"detailer": {1}, "adetailer": {2}, "facedetailer": {3}},
		errored: map[string]bool{"adetailer": true},
	}
	srv := newModelServer(t, reader)

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/workflows/discover?use=detailer", nil))
	body := rec.Body.String()
	if !strings.Contains(body, "wf1") || !strings.Contains(body, "wf3") {
		t.Errorf("a single failed fan-out request blanked the page:\n%s", body[:min(len(body), 800)])
	}
}

// TestMergeWorkflowResultsRebuildsRaw — the card renderer reads showcase images
// and version dates out of Raw, not out of Items. A merge that dropped Raw would
// silently render every merged card with no images and no "Updated" line.
func TestMergeWorkflowResultsRebuildsRaw(t *testing.T) {
	mk := func(ids ...int) *civitai.ModelSearchResult {
		items := make([]any, 0, len(ids))
		list := make([]civitai.ModelListItem, 0, len(ids))
		for _, id := range ids {
			items = append(items, map[string]any{"id": id, "modelVersions": []any{
				map[string]any{"id": id, "images": []any{
					map[string]any{"url": "https://image.civitai.com/x" + itoaTest(id) + ".jpeg", "nsfwLevel": 1, "type": "image"},
				}},
			}})
			list = append(list, civitai.ModelListItem{ID: id})
		}
		raw, _ := json.Marshal(map[string]any{"items": items})
		return &civitai.ModelSearchResult{Items: list, Raw: raw}
	}
	got := mergeWorkflowResults([]*civitai.ModelSearchResult{mk(1, 2), mk(2, 3), nil}, 24)
	if len(got.Items) != 3 {
		t.Fatalf("merged items = %d, want 3 (deduped)", len(got.Items))
	}
	if got.Items[0].ID != 1 || got.Items[1].ID != 2 || got.Items[2].ID != 3 {
		t.Errorf("first-seen order not preserved: %+v", got.Items)
	}
	imgs := parseSearchImages(got.Raw)
	for _, id := range []int{1, 2, 3} {
		if len(imgs[id]) != 1 {
			t.Errorf("merged Raw lost model %d's showcase images (got %d)", id, len(imgs[id]))
		}
	}
	// The cap is honoured.
	if capped := mergeWorkflowResults([]*civitai.ModelSearchResult{mk(1, 2, 3, 4)}, 2); len(capped.Items) != 2 {
		t.Errorf("limit not applied: got %d items", len(capped.Items))
	}
}

// ---------------------------------------------------------------------------
// Landing page + caching
// ---------------------------------------------------------------------------

// TestLandingIssuesNoPerTileRequests is the caching requirement stated bluntly:
// the browse-by entry page renders ~30 tiles and must cost exactly ONE upstream
// request (the popular feed), not one per tile.
func TestLandingIssuesNoPerTileRequests(t *testing.T) {
	reader := &recordingSearchReader{result: workflowResult(t)}
	srv := newModelServer(t, reader)

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/workflows/discover", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET = %d", rec.Code)
	}
	if got := reader.callCount(); got != 1 {
		t.Fatalf("landing page issued %d upstream requests, want exactly 1", got)
	}
	body := rec.Body.String()
	// Every curated tile is present without any of them having been fetched.
	for _, want := range []string{
		"Browse by ecosystem", "Browse by use case",
		"Image models", "Video models",
		">SDXL family<", ">Flux.1<", ">Wan Video<", ">Z-Image<",
		">Inpainting<", ">Upscaling<", ">Video generation<",
		"/workflows/discover?eco=sdxl", "/workflows/discover?use=inpaint",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("landing page missing %q", want)
		}
	}
}

// TestFacetFeedsAreCachedPerFacet — a facet feed is TTL-cached under its own key,
// so re-browsing a facet is free, and two DIFFERENT facets do not share an entry
// (a shared key would serve Flux results under the Wan chip).
func TestFacetFeedsAreCachedPerFacet(t *testing.T) {
	reader := &recordingSearchReader{result: workflowResult(t)}
	srv := newModelServer(t, reader)

	get := func(target string) {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d", target, rec.Code)
		}
	}
	get("/workflows/discover?eco=flux1")
	get("/workflows/discover?eco=flux1") // cached
	if got := reader.callCount(); got != 1 {
		t.Fatalf("second load of the same facet should be cached; calls = %d, want 1", got)
	}
	get("/workflows/discover?eco=wan") // different key → one more fetch
	if got := reader.callCount(); got != 2 {
		t.Fatalf("a different facet must not reuse another's cache entry; calls = %d, want 2", got)
	}
	get("/workflows/discover?eco=flux1&period=Week") // period is part of the key
	if got := reader.callCount(); got != 3 {
		t.Fatalf("period must be part of the cache key; calls = %d, want 3", got)
	}
	get("/workflows/discover?eco=flux1") // still cached from the first pair
	if got := reader.callCount(); got != 3 {
		t.Fatalf("original facet entry was evicted; calls = %d, want 3", got)
	}
}

// TestFacetCacheKeySeparatesEveryDimension pins the key composition directly, so
// a future refactor cannot collapse two dimensions and start cross-serving.
func TestFacetCacheKeySeparatesEveryDimension(t *testing.T) {
	flux, _ := civitai.EcosystemBySlug("flux1")
	wan, _ := civitai.EcosystemBySlug("wan")
	inpaint, _ := civitai.UseCaseBySlug("inpaint")

	keys := map[string]string{
		"none":        facetFeedKey(true, "Most Downloaded", "Month", workflowFacets{}),
		"flux":        facetFeedKey(true, "Most Downloaded", "Month", workflowFacets{Eco: &flux}),
		"wan":         facetFeedKey(true, "Most Downloaded", "Month", workflowFacets{Eco: &wan}),
		"inpaint":     facetFeedKey(true, "Most Downloaded", "Month", workflowFacets{Use: &inpaint}),
		"both":        facetFeedKey(true, "Most Downloaded", "Month", workflowFacets{Eco: &flux, Use: &inpaint}),
		"othersort":   facetFeedKey(true, "Newest", "Month", workflowFacets{Eco: &flux}),
		"otherperiod": facetFeedKey(true, "Most Downloaded", "Week", workflowFacets{Eco: &flux}),
		"othernsfw":   facetFeedKey(false, "Most Downloaded", "Month", workflowFacets{Eco: &flux}),
	}
	seen := map[string]string{}
	for name, k := range keys {
		if prev, ok := seen[k]; ok {
			t.Errorf("cache key collision: %s and %s both produce %q", prev, name, k)
		}
		seen[k] = name
	}
}

// ---------------------------------------------------------------------------
// Chips, URL-addressability, empty state
// ---------------------------------------------------------------------------

// TestFacetChipsRenderSelectedAndClearState — a chip must show which facet is
// live, and clicking the live chip must CLEAR it (toggle), not re-apply it.
func TestFacetChipsRenderSelectedAndClearState(t *testing.T) {
	reader := &recordingSearchReader{result: workflowResult(t)}
	srv := newModelServer(t, reader)

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/workflows/discover?eco=flux1&use=inpaint", nil))
	body := rec.Body.String()

	// The selected chips carry the on-class AND aria-current (not colour-only).
	if !strings.Contains(body, `aria-current="true"`) {
		t.Error("a selected chip must carry aria-current, not colour alone")
	}
	if n := strings.Count(body, "cm-facet-chip-on"); n != 2 {
		t.Errorf("want exactly 2 selected chips (one per dimension), got %d", n)
	}
	// The SELECTED ecosystem chip's href toggles it OFF (keeps ?use=).
	if !strings.Contains(body, `href="/workflows/discover?use=inpaint"`) {
		t.Error("clicking the selected ecosystem chip must clear only that facet")
	}
	// The SELECTED use-case chip's href toggles it OFF (keeps ?eco=).
	if !strings.Contains(body, `href="/workflows/discover?eco=flux1"`) {
		t.Error("clicking the selected use-case chip must clear only that facet")
	}
	// An UNSELECTED chip swaps the ecosystem while keeping the use case.
	if !strings.Contains(body, `href="/workflows/discover?eco=wan&amp;use=inpaint"`) {
		t.Errorf("an unselected ecosystem chip must preserve the other facet:\n%s", body[:min(len(body), 400)])
	}
	// The heading names the selection so the grid is never ambiguous.
	if !strings.Contains(body, "Flux.1 · Inpainting") {
		t.Error("the results heading must name the active facets")
	}
}

// TestFacetsSurviveSortPeriodAndQueryChanges — a filtered view must stay filtered
// when the user changes sort/period or types a query, both through the form
// (hidden inputs) and through the chip links (which carry the current state).
func TestFacetsSurviveSortPeriodAndQueryChanges(t *testing.T) {
	reader := &recordingSearchReader{result: workflowResult(t)}
	srv := newModelServer(t, reader)

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/workflows/discover?q=portrait&eco=sdxl&use=upscale&sort=Newest&period=Week", nil))
	body := rec.Body.String()

	// The search form carries the facets, so submitting it does not clear them.
	for _, want := range []string{
		`<input type="hidden" name="eco" value="sdxl">`,
		`<input type="hidden" name="use" value="upscale">`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("search form missing %s — a sort/period change would silently clear the facet", want)
		}
	}
	// Chip links carry query+sort+period, so a chip click does not reset them.
	if !strings.Contains(body, "q=portrait") || !strings.Contains(body, "sort=Newest") || !strings.Contains(body, "period=Week") {
		t.Error("chip hrefs must carry the current query/sort/period")
	}
	// And EVERY outbound request in the use-case fan-out used all of it. Checking
	// only the last call would miss a fan-out that dropped the ecosystem on the
	// synonym requests (the "upscale" use case issues one request per synonym, so
	// the last call carries tag=highres, not tag=upscaler).
	reader.mu.Lock()
	calls := reader.calls
	reader.mu.Unlock()
	if len(calls) != civitai.MaxUseCaseTagQueries {
		t.Fatalf("fan-out calls = %d, want %d", len(calls), civitai.MaxUseCaseTagQueries)
	}
	var tags []string
	for i, q := range calls {
		if q.Get("query") != "portrait" || q.Get("sort") != "Newest" || q.Get("period") != "Week" {
			t.Errorf("fan-out call %d lost query/sort/period: %v", i, q)
		}
		if len(q["baseModels"]) != 10 {
			t.Errorf("fan-out call %d lost the ecosystem facet: %v", i, q["baseModels"])
		}
		tags = append(tags, q.Get("tag"))
	}
	if strings.Join(tags, ",") != "upscaler,upscale,highres" {
		t.Errorf("fan-out tags = %v, want the upscale use case's synonyms", tags)
	}
}

// TestDiscoverHrefIsCanonical — default sort/period are omitted so two equivalent
// views produce ONE url. A link that varied by irrelevant defaults would make
// "shareable" a lie and would fragment the feed cache in the eye of the user.
func TestDiscoverHrefIsCanonical(t *testing.T) {
	cases := []struct{ q, sort, period, eco, use, want string }{
		{"", "Most Downloaded", "Month", "", "", "/workflows/discover"},
		{"", "Most Downloaded", "Month", "sdxl", "", "/workflows/discover?eco=sdxl"},
		{"", "Newest", "Month", "sdxl", "", "/workflows/discover?eco=sdxl&sort=Newest"},
		{"", "Most Downloaded", "AllTime", "sdxl", "", "/workflows/discover?eco=sdxl&period=AllTime"},
		// With a keyword the DEFAULT period is AllTime, so AllTime is omitted and
		// Month is explicit — the mirror image of the browse case.
		{"wan", "Most Downloaded", "AllTime", "", "", "/workflows/discover?q=wan"},
		{"wan", "Most Downloaded", "Month", "", "", "/workflows/discover?period=Month&q=wan"},
	}
	for _, tc := range cases {
		if got := discoverHref(tc.q, tc.sort, tc.period, tc.eco, tc.use); got != tc.want {
			t.Errorf("discoverHref(%q,%q,%q,%q,%q) = %q, want %q", tc.q, tc.sort, tc.period, tc.eco, tc.use, got, tc.want)
		}
	}
}

// TestFacetEmptyStateIsGuided — the user requirement verbatim: a zero-result
// facet view must NAME what it searched and offer a way out, not dead-end on
// "No workflows found.".
func TestFacetEmptyStateIsGuided(t *testing.T) {
	reader := &recordingSearchReader{result: &civitai.ModelSearchResult{}}
	srv := newModelServer(t, reader)

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/workflows/discover?eco=krea&use=inpaint", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "No Krea 2 · Inpainting workflows this month") {
		t.Errorf("empty state must name the exact selection and window:\n%s", body[:min(len(body), 1200)])
	}
	for _, want := range []string{
		"Search all time",            // widen
		"period=AllTime",             // …and it actually widens
		"Any use case",               // drop one facet
		"Any ecosystem",              // drop the other
		"Clear all filters",          // clear everything
		`href="/workflows/discover"`, // …to the bare page
	} {
		if !strings.Contains(body, want) {
			t.Errorf("guided empty state missing %q", want)
		}
	}
	// The bare dead-end wording must NOT be what a faceted view shows.
	if strings.Contains(body, "No workflows found.") {
		t.Error("a faceted empty view must not fall back to the unguided 'No workflows found.'")
	}
}

// TestFacetEmptyStateOmitsWidenWhenAlreadyAllTime — offering "Search all time"
// while already on AllTime is a dead button.
func TestFacetEmptyStateOmitsWidenWhenAlreadyAllTime(t *testing.T) {
	reader := &recordingSearchReader{result: &civitai.ModelSearchResult{}}
	srv := newModelServer(t, reader)

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/workflows/discover?eco=krea&period=AllTime", nil))
	body := rec.Body.String()
	if strings.Contains(body, "Search all time") {
		t.Error("must not offer to widen to AllTime when already on AllTime")
	}
	if !strings.Contains(body, "Clear all filters") {
		t.Error("must still offer a way out")
	}
	if !strings.Contains(body, "No Krea 2 workflows") {
		t.Errorf("heading should name the selection without a period phrase:\n%s", body[:min(len(body), 600)])
	}
}

// TestUnfacetedEmptyStatesAreUnchanged — the two pre-existing empty states must
// not be swallowed by the new one.
func TestUnfacetedEmptyStatesAreUnchanged(t *testing.T) {
	reader := &recordingSearchReader{result: &civitai.ModelSearchResult{}}
	srv := newModelServer(t, reader)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/workflows/discover?q=nomatch", nil))
	if !strings.Contains(rec.Body.String(), "No workflows found.") {
		t.Error("an unfaceted zero-result search should keep the plain 'No workflows found.'")
	}

	srv2 := newModelServer(t, erroringSearchReader{})
	rec2 := httptest.NewRecorder()
	srv2.Handler().ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/workflows/discover?eco=flux1", nil))
	body := rec2.Body.String()
	if !strings.Contains(body, "Search for workflows") {
		t.Error("a FETCH ERROR must degrade to the neutral prompt")
	}
	if strings.Contains(body, "Search all time") {
		t.Error("a fetch error must NOT be dressed up as 'nothing matched, widen your search'")
	}
}

// ---------------------------------------------------------------------------
// NSFW
// ---------------------------------------------------------------------------

// nsfwProbeResult is a one-item result whose version carries one NSFW (level 8)
// and one safe (level 1) showcase image.
func nsfwProbeResult(t *testing.T, typ string) *civitai.ModelSearchResult {
	t.Helper()
	raw := searchRawJSON(t, []any{
		map[string]any{
			"id": 4242, "name": "Spicy WF", "type": typ,
			"modelVersions": []any{map[string]any{
				"id": 1, "name": "v1", "publishedAt": "2026-01-02T00:00:00.000Z",
				"images": []any{
					map[string]any{"url": "https://image.civitai.com/NSFW-SECRET.jpeg", "nsfwLevel": 8, "type": "image"},
					map[string]any{"url": "https://image.civitai.com/safe.jpeg", "nsfwLevel": 1, "type": "image"},
				},
			}},
		},
	})
	return &civitai.ModelSearchResult{
		Items: []civitai.ModelListItem{{ID: 4242, Name: "Spicy WF", Type: typ}},
		Raw:   raw,
	}
}

func facetViewFor(t *testing.T, res *civitai.ModelSearchResult, mode string) workflowDiscoverView {
	t.Helper()
	eco, _ := civitai.EcosystemBySlug("flux1")
	use, _ := civitai.UseCaseBySlug("inpaint")
	return workflowDiscoverView{
		Sort: "Most Downloaded", Period: "Month", CSRF: "csrf", Res: res, Mode: mode,
		Facets: workflowFacets{Eco: &eco, Use: &use},
	}
}

// TestFacetPageNSFWHandlingMatchesTheModelSearch pins the NSFW posture of the new
// facet surface against the ALREADY-AUDITED /search renderer, for every mode.
//
// It is written as an equivalence test rather than "hide omits the URL" because
// of a REPO-WIDE PRE-EXISTING condition, verified here rather than assumed:
// normalizeNSFWMode MIGRATES a stored "hide" to blur (the navbar toggle dropped
// the hide state — see the const block in model_pages.go), and
// modelCardCarouselW normalizes BEFORE testing mode == NSFWHide. So the
// server-side omit branch is inert on the model search too, not just here — this
// change neither introduced nor widened that. Asserting "hide omits" would have
// been a test that documents a capability the app does not currently have.
//
// What this DOES guarantee is the thing a new surface can actually get wrong:
// the facet page must never render NSFW content more permissively than the
// existing search page does.
func TestFacetPageNSFWHandlingMatchesTheModelSearch(t *testing.T) {
	for _, mode := range []string{NSFWHide, NSFWBlur, NSFWShow} {
		t.Run(mode, func(t *testing.T) {
			facet := renderString(t, workflowDiscoverResults(facetViewFor(t, nsfwProbeResult(t, "Workflows"), mode)))
			search := renderString(t, searchResults(nsfwProbeResult(t, "LORA"), nil, mode, "csrf", ""))

			// Non-vacuity: both renderers actually produced the images.
			if !strings.Contains(facet, "safe.jpeg") || !strings.Contains(search, "safe.jpeg") {
				t.Fatal("neither renderer produced a showcase image — the comparison would be vacuous")
			}
			gotFacet := strings.Contains(facet, "NSFW-SECRET")
			gotSearch := strings.Contains(search, "NSFW-SECRET")
			if gotFacet != gotSearch {
				t.Errorf("facet page renders the NSFW image=%v but /search renders it=%v — the new surface must not be more permissive",
					gotFacet, gotSearch)
			}
			blurFacet := strings.Contains(facet, "cm-blur")
			blurSearch := strings.Contains(search, "cm-blur")
			if blurFacet != blurSearch {
				t.Errorf("facet page blurs=%v but /search blurs=%v", blurFacet, blurSearch)
			}
			// Show mode must NOT blur; every other mode must (the effective behaviour
			// after the hide→blur migration).
			if mode == NSFWShow && blurFacet {
				t.Error("show mode must render NSFW plainly")
			}
			if mode != NSFWShow && !blurFacet {
				t.Errorf("%s mode must obscure the NSFW image", mode)
			}
		})
	}
}

// TestFacetSFWRequestOmitsNSFWAtTheSource — the LIVE server-side gate on this
// surface is the outbound nsfw param: nsfw=false makes CivitAI return SFW-only,
// so the content is omitted at the source and never reaches the renderer at all.
// Every request in a use-case fan-out must carry it.
func TestFacetSFWRequestOmitsNSFWAtTheSource(t *testing.T) {
	eco, _ := civitai.EcosystemBySlug("flux1")
	for _, tag := range []string{"", "inpaint"} {
		q := workflowSearchParams("", "Most Downloaded", "Month", false, &eco, tag)
		if q.Get("nsfw") != "false" {
			t.Errorf("tag=%q: nsfw = %q, want false — an SFW request must exclude NSFW models upstream", tag, q.Get("nsfw"))
		}
	}
}

// TestFacetRequestHonoursNSFWFlag — the outbound facet request must carry the
// nsfw param, so hide mode does not even FETCH what it is required to omit.
func TestFacetRequestHonoursNSFWFlag(t *testing.T) {
	reader := &recordingSearchReader{result: workflowResult(t)}
	srv := newModelServer(t, reader)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/workflows/discover?eco=flux1&use=inpaint", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET = %d", rec.Code)
	}
	reader.mu.Lock()
	defer reader.mu.Unlock()
	for i, c := range reader.calls {
		if c.Get("nsfw") == "" {
			t.Errorf("fan-out call %d omitted the nsfw param: %v", i, c)
		}
	}
}

// TestHXFacetFragmentHasChipsButNoChrome — the htmx swap must re-render the chips
// (so their hrefs track the query in the box) while still being a fragment.
func TestHXFacetFragmentHasChipsButNoChrome(t *testing.T) {
	reader := &recordingSearchReader{result: workflowResult(t)}
	srv := newModelServer(t, reader)

	req := httptest.NewRequest(http.MethodGet, "/workflows/discover?q=wan&eco=flux1", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	body := rec.Body.String()
	if strings.Contains(body, "<html") || strings.Contains(body, "<nav") {
		t.Error("HX fragment must not contain full-page chrome")
	}
	if !strings.Contains(body, "cm-facet-chip") {
		t.Error("HX fragment must re-render the chips, or their hrefs go stale against the new query")
	}
	if !strings.Contains(body, "q=wan") {
		t.Error("re-rendered chips must carry the query that was just searched")
	}
	// The landing grid belongs to the entry view only.
	if strings.Contains(body, "Browse by ecosystem") {
		t.Error("the landing grid must not render once a query/facet is active")
	}
}
