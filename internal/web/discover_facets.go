package web

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
)

// This file holds the browse-by-facet machinery shared by the remote
// Discover-workflows page and (for its vocabulary) the local library workflow
// list. The vocabulary itself lives in internal/civitai/taxonomy.go — the single
// source of truth; nothing here invents a facet value.

// workflowFacets is the normalized, WHITELISTED facet selection for one request.
// A nil field means "not selected". By construction a non-nil field is always a
// row from the curated table, so nothing user-supplied can reach an outbound
// request.
type workflowFacets struct {
	Eco *civitai.Ecosystem
	Use *civitai.UseCase
}

// any reports whether at least one facet is selected.
func (f workflowFacets) any() bool { return f.Eco != nil || f.Use != nil }

// normalizeWorkflowFacets resolves ?eco= and ?use= against the curated table.
//
// UNKNOWN VALUES ARE IGNORED (dropped), not rejected with an error. That is the
// deliberate choice, for two reasons:
//
//   - It matches the idiom already used for ?sort=/?period= (normalizeSearchSort
//     falls back rather than 4xx-ing), so a hand-edited or stale shared link
//     degrades to a usable page instead of an error.
//   - It is SAFE only because "ignore" here means the facet is dropped from BOTH
//     the outbound request and the chip state — the page then honestly shows an
//     unfiltered result with no chip lit. The dangerous alternative would be
//     passing the value through: CivitAI silently ignores an unknown `tag` and
//     returns the UNFILTERED feed (live-verified), which would render as a
//     working filter that is lying.
func normalizeWorkflowFacets(q url.Values) workflowFacets {
	var f workflowFacets
	if e, ok := civitai.EcosystemBySlug(q.Get("eco")); ok {
		f.Eco = &e
	}
	if u, ok := civitai.UseCaseBySlug(q.Get("use")); ok {
		f.Use = &u
	}
	return f
}

// workflowSearchParams builds the query for ONE outbound `types=Workflows`
// request. tag is a single already-whitelisted CivitAI tag, or "" for none.
//
// The three things that make this correct are all live-verified traps:
//
//   - "types" is PLURAL (singular is silently ignored → unfiltered results).
//   - baseModels multi-value MUST be REPEATED params (q.Add). A comma-joined
//     value is parsed as one literal base-model name and returns ZERO items.
//   - "tag" is SINGLE-VALUE: repeated tag= is an HTTP 400 ZodError, and a
//     comma-joined value is silently ignored. Hence one tag per request; the
//     caller fans out over a use case's synonyms and merges.
func workflowSearchParams(query, sort, period string, nsfw bool, eco *civitai.Ecosystem, tag string) url.Values {
	q := url.Values{}
	q.Set("types", "Workflows")
	if s := strings.TrimSpace(query); s != "" {
		q.Set("query", s)
	}
	q.Set("limit", searchLimit)
	q.Set("sort", sort)
	q.Set("period", period)
	// Tie nsfw to the display mode: hide → SFW-only server-side (the NSFW-hide
	// invariant is enforced at render too, but the request must not fetch what we
	// are required to omit).
	setNSFWParam(q, nsfw)
	if eco != nil {
		for _, bm := range eco.BaseModels {
			q.Add("baseModels", bm)
		}
	}
	if tag != "" {
		q.Set("tag", tag)
	}
	return q
}

// mergeWorkflowResults concatenates several result pages into one, DEDUPED BY
// MODEL ID, preserving first-seen order, capped at limit.
//
// It exists because a use case fans out to one request per tag synonym (CivitAI
// does not unify synonyms and `tag` takes only one value), and the same model
// legitimately carries several of them.
//
// It rebuilds Raw as a synthetic {"items":[…]} body carrying the SAME raw item
// objects, because the card renderer reads showcase images and version dates
// straight out of Raw (parseSearchImages / newestVersionInfoByModel). Dropping
// Raw here would silently blank every card's images.
func mergeWorkflowResults(parts []*civitai.ModelSearchResult, limit int) *civitai.ModelSearchResult {
	// Raw item objects by model id, taken from the first page that carries each.
	rawByID := map[int]json.RawMessage{}
	for _, p := range parts {
		if p == nil || len(p.Raw) == 0 {
			continue
		}
		var body struct {
			Items []json.RawMessage `json:"items"`
		}
		if json.Unmarshal(p.Raw, &body) != nil {
			continue
		}
		for _, it := range body.Items {
			var id struct {
				ID int `json:"id"`
			}
			if json.Unmarshal(it, &id) != nil || id.ID == 0 {
				continue
			}
			if _, ok := rawByID[id.ID]; !ok {
				rawByID[id.ID] = it
			}
		}
	}

	out := &civitai.ModelSearchResult{}
	seen := map[int]bool{}
	var rawItems []json.RawMessage
	for _, p := range parts {
		if p == nil {
			continue
		}
		for _, it := range p.Items {
			if seen[it.ID] {
				continue
			}
			seen[it.ID] = true
			out.Items = append(out.Items, it)
			if r, ok := rawByID[it.ID]; ok {
				rawItems = append(rawItems, r)
			}
			if limit > 0 && len(out.Items) >= limit {
				break
			}
		}
		if limit > 0 && len(out.Items) >= limit {
			break
		}
	}
	// Encode the merged raw body. json.Marshal of []json.RawMessage cannot fail
	// for well-formed inputs, and every element came from a successful Unmarshal.
	if b, err := json.Marshal(struct {
		Items []json.RawMessage `json:"items"`
	}{Items: rawItems}); err == nil {
		out.Raw = b
	}
	return out
}

// fetchWorkflowFacetResults performs the outbound fetch(es) for one facet
// selection and returns the merged, deduped result.
//
// Request budget — bounded and predictable:
//   - no use case selected            → exactly 1 request
//   - a use case selected             → up to civitai.MaxUseCaseTagQueries (3)
//
// The ecosystem never adds a request: its whole base-model family rides one
// request as repeated `baseModels=` params.
//
// A partial failure is tolerated: if at least one tag request succeeded the
// merged result is returned. Only an all-failed fan-out returns the error, so a
// single upstream hiccup does not blank an otherwise-good facet page.
func (s *Server) fetchWorkflowFacetResults(parent context.Context, query, sort, period string, nsfw bool, f workflowFacets) (*civitai.ModelSearchResult, error) {
	tags := []string{""}
	if f.Use != nil {
		tags = f.Use.QueryTags()
	}
	ctx, cancel := context.WithTimeout(parent, 20*time.Second)
	defer cancel()

	var (
		parts   []*civitai.ModelSearchResult
		lastErr error
	)
	for _, tag := range tags {
		res, err := s.reader.SearchModels(ctx, workflowSearchParams(query, sort, period, nsfw, f.Eco, tag))
		if err != nil {
			lastErr = err
			s.log.Warn("workflow facet fetch failed", "tag", tag, "err", err)
			continue
		}
		parts = append(parts, res)
	}
	if len(parts) == 0 {
		return nil, lastErr
	}
	limit, _ := strconv.Atoi(searchLimit)
	return mergeWorkflowResults(parts, limit), nil
}

// ---------------------------------------------------------------------------
// Per-facet feed cache
// ---------------------------------------------------------------------------

// facetFeedCache is the TTL cache for the NO-QUERY facet feeds ("Popular this
// month, Flux.1, inpainting"), mirroring the popularModels/popularWorkflows
// pattern but keyed by the whole facet+sort+period+nsfw tuple.
//
// It exists so the browse-by surface cannot hammer civitai.com: a user clicking
// around the ecosystem tiles re-renders the same handful of feeds constantly,
// and a use-case facet costs up to 3 upstream requests per miss.
//
// Only WHITELISTED facet values ever reach a key, so the key space is bounded by
// the curated table (ecosystems+1 × use-cases+1 × 3 sorts × 3 periods × 2 nsfw).
// maxFacetFeedEntries is a belt-and-braces cap on top of that.
//
// The zero value is ready to use (maps are lazily created under the mutex), so
// adding it to Server needs no constructor line.
type facetFeedCache struct {
	mu  sync.Mutex
	val map[string]facetFeedEntry
}

type facetFeedEntry struct {
	res *civitai.ModelSearchResult
	exp time.Time
}

// maxFacetFeedEntries bounds the cache; on overflow it is cleared wholesale
// (simplest correct eviction — the entries are cheap to refetch and the bound is
// far above the realistic working set).
const maxFacetFeedEntries = 256

// facetFeedKey is the cache key. Every component is either a whitelisted table
// slug or an already-normalized sort/period, so the key can never carry
// user-controlled text.
func facetFeedKey(nsfw bool, sort, period string, f workflowFacets) string {
	eco, use := "-", "-"
	if f.Eco != nil {
		eco = f.Eco.Slug
	}
	if f.Use != nil {
		use = f.Use.Slug
	}
	return strconv.FormatBool(nsfw) + "|" + sort + "|" + period + "|" + eco + "|" + use
}

func (c *facetFeedCache) get(key string) (*civitai.ModelSearchResult, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.val[key]
	if !ok || time.Now().After(e.exp) {
		return nil, false
	}
	return e.res, true
}

func (c *facetFeedCache) put(key string, res *civitai.ModelSearchResult, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.val == nil {
		c.val = map[string]facetFeedEntry{}
	}
	if len(c.val) >= maxFacetFeedEntries {
		c.val = map[string]facetFeedEntry{}
	}
	c.val[key] = facetFeedEntry{res: res, exp: time.Now().Add(ttl)}
}

// facetFeed returns the cached no-query feed for a facet selection, fetching on
// a miss. On a fetch error it returns nil (and caches nothing) so the caller
// degrades to the guided empty state rather than showing a stale-looking page.
func (s *Server) facetFeed(parent context.Context, nsfw bool, sort, period string, f workflowFacets) *civitai.ModelSearchResult {
	key := facetFeedKey(nsfw, sort, period, f)
	if res, ok := s.facetFeeds.get(key); ok {
		return res
	}
	res, err := s.fetchWorkflowFacetResults(parent, "", sort, period, nsfw, f)
	if err != nil || res == nil {
		return nil
	}
	s.facetFeeds.put(key, res, popularTTL)
	return res
}

// ---------------------------------------------------------------------------
// URL building
// ---------------------------------------------------------------------------

// discoverDefaultPeriod is the period a /workflows/discover request falls back to
// when ?period= is absent or not whitelisted. It mirrors handleSearch exactly: a
// BROWSE (no keyword) defaults to Month — that is the "Popular this month" feed
// and the window a facet landing page is most useful in — while a KEYWORD search
// defaults to AllTime so it is not silently narrowed to the last 30 days.
//
// It is also the value discoverHref omits from a URL, so a canonical link stays
// short.
func discoverDefaultPeriod(query string) string {
	if strings.TrimSpace(query) == "" {
		return "Month"
	}
	return "AllTime"
}

// discoverSortDefault is the sort discoverHref omits from a URL.
const discoverSortDefault = "Most Downloaded"

// discoverHref builds a canonical /workflows/discover URL from an explicit facet
// selection. Default sort/period are omitted so a shared link is short and two
// equivalent views produce the SAME url (chips compare by href in tests, and a
// user reading the address bar sees only what they actually chose).
func discoverHref(query, sort, period, eco, use string) string {
	q := url.Values{}
	if s := strings.TrimSpace(query); s != "" {
		q.Set("q", s)
	}
	if sort != "" && sort != discoverSortDefault {
		q.Set("sort", sort)
	}
	if period != "" && period != discoverDefaultPeriod(query) {
		q.Set("period", period)
	}
	if eco != "" {
		q.Set("eco", eco)
	}
	if use != "" {
		q.Set("use", use)
	}
	if len(q) == 0 {
		return "/workflows/discover"
	}
	return "/workflows/discover?" + q.Encode()
}

// ecoSlug / useSlug read a selection's slugs ("" when not selected).
func (f workflowFacets) ecoSlug() string {
	if f.Eco == nil {
		return ""
	}
	return f.Eco.Slug
}

func (f workflowFacets) useSlug() string {
	if f.Use == nil {
		return ""
	}
	return f.Use.Slug
}

// discoverFacetHref builds a /workflows/discover URL with ONE facet dimension
// toggled, carrying the current query/sort/period and the OTHER facet so a chip
// click never silently resets the rest of the view. Passing the
// currently-selected value CLEARS that facet (chips toggle); passing "" clears it
// unconditionally (the "All …" chip).
func discoverFacetHref(query, sort, period string, f workflowFacets, dim, slug string) string {
	eco, use := f.ecoSlug(), f.useSlug()
	switch dim {
	case "eco":
		if eco == slug {
			eco = "" // toggle off
		} else {
			eco = slug
		}
	case "use":
		if use == slug {
			use = ""
		} else {
			use = slug
		}
	}
	return discoverHref(query, sort, period, eco, use)
}

// facetSummary is the human phrase describing the current selection, used in
// headings and the empty state ("Flux.1 · Inpainting"). Empty when nothing is
// selected.
func (f workflowFacets) summary() string {
	var parts []string
	if f.Eco != nil {
		parts = append(parts, f.Eco.Label)
	}
	if f.Use != nil {
		parts = append(parts, f.Use.Label)
	}
	return strings.Join(parts, " · ")
}
