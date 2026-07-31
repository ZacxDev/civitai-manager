package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
	"github.com/ZacxDev/civitai-manager/internal/poller"
	"github.com/ZacxDev/civitai-manager/internal/store"
	g "maragu.dev/gomponents"
)

const searchLimit = "24"

// nsfwSearchFlag is the `/api/v1/models` `nsfw` boolean for the user's current
// maturity range — see maturityRange.modelsNSFWFlag for why that endpoint gets a
// boolean and not a level (it 400s on a level name).
func (s *Server) nsfwSearchFlag() bool { return s.maturity().modelsNSFWFlag() }

// setNSFWParam sets the `nsfw` query param civitai's model search honors:
// nsfw=true includes NSFW models with their images, nsfw=false restricts to SFW.
// Setting it explicitly (rather than omitting) keeps the popular default and the
// keyword search consistent and makes the data-egress behavior obvious.
func setNSFWParam(q url.Values, nsfw bool) {
	if nsfw {
		q.Set("nsfw", "true")
	} else {
		q.Set("nsfw", "false")
	}
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	subs, err := s.store.ListSubscriptions()
	if err != nil {
		s.renderError(w, "load subscriptions", err)
		return
	}
	files, err := s.store.ListLocalFiles()
	if err != nil {
		s.renderError(w, "load local files", err)
		return
	}
	suggestions := librarySubscribeSuggestions(files, subs, subscribeSuggestionLimit)
	// Resolve suggestion titles from the local model_cache only (zero civitai
	// calls); a cache miss leaves Name empty so the card fetches it lazily.
	for i := range suggestions {
		if ent, _ := s.store.GetModelCache(suggestions[i].ModelID); ent != nil && ent.Name != "" {
			suggestions[i].Name = ent.Name
		}
	}
	s.render(w, http.StatusOK, dashboardPage(subs, suggestions, s.csrf, s.currentTheme(), s.maturity(), s.rail(r.Context())))
}

// subscribeSuggestionLimit caps how many library-derived subscribe suggestions
// the dashboard shows.
const subscribeSuggestionLimit = 12

// themeSettingKey persists the UI light/dark choice.
const themeSettingKey = "theme"

// currentTheme returns the persisted UI theme ("light"|"dark"), defaulting to
// dark (civitai-manager's established look). Reflected onto <html data-theme>.
func (s *Server) currentTheme() string {
	v, _ := s.store.GetSettingDefault(themeSettingKey, "dark")
	if v != "light" {
		v = "dark"
	}
	return v
}

// handleSetTheme persists the light/dark choice and asks htmx to refresh so the
// page re-renders under the new <html data-theme> (from which every --civitai-*
// token re-resolves). CSRF-protected like every other state-changing POST.
func (s *Server) handleSetTheme(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if !s.verifyCSRF(w, r) {
		return
	}
	theme := "dark"
	if strings.EqualFold(strings.TrimSpace(r.FormValue("theme")), "light") {
		theme = "light"
	}
	if err := s.store.SetSetting(themeSettingKey, theme); err != nil {
		s.renderError(w, "save theme setting", err)
		return
	}
	w.Header().Set("HX-Refresh", "true")
	w.WriteHeader(http.StatusNoContent)
}

// searchSortOptions / searchPeriodOptions back the search page's sort + period
// filter dropdowns. Each option's Value is the EXACT CivitAI query string sent to
// the models API; Label is the human wording shown in the <select>.
var searchSortOptions = []selectOption{
	{"Most Downloaded", "Most downloaded"},
	{"Highest Rated", "Highest rated"},
	{"Newest", "Newest"},
}

// searchPeriodOptions is ordered narrowest → widest so the list reads as a time
// scale. Every Value is a member of CivitAI's STRICT `period` enum — the API
// answers 400 to anything else (probed live: Day/Week/Month/Year/AllTime → 200;
// ThreeMonths/SixMonths/Quarter/"3 Months" → 400), so there is no 3- or 6-month
// window to offer and faking one client-side would break pagination and make the
// result counts lie.
var searchPeriodOptions = []selectOption{
	{"Day", "Today"},
	{"Week", "This week"},
	{"Month", "This month"},
	{"Year", "This year"},
	{"AllTime", "All time"},
}

// normalizeSearchSort validates a ?sort= value against the whitelist, defaulting
// to "Most Downloaded". Only whitelisted values are ever forwarded to civitai.
func normalizeSearchSort(v string) string {
	switch v {
	case "Most Downloaded", "Highest Rated", "Newest":
		return v
	}
	return "Most Downloaded"
}

// normalizeSearchPeriod validates a ?period= value against the whitelist,
// defaulting to def (the empty-query popular feed defaults to "Month" to preserve
// the cached "Popular this month" behavior; a keyword search defaults to
// "AllTime", the least-restrictive window ≈ the prior no-period behavior).
//
// The whitelist IS searchPeriodOptions, so the dropdown and the set of values
// that may reach civitai.com can never drift apart — an option the UI offers is
// accepted, and nothing else is ever forwarded.
func normalizeSearchPeriod(v, def string) string {
	for _, o := range searchPeriodOptions {
		if o.Value == v {
			return v
		}
	}
	return def
}

// searchHeadingFor labels a non-default result grid (e.g. "Highest rated · this
// week"). The default empty-query feed keeps its own "Popular this month" heading
// (see popularModels), so this is only used for chosen sort/period combinations.
func searchHeadingFor(sort, period string) string {
	return optionLabel(searchSortOptions, sort) + " · " + optionLabel(searchPeriodOptions, period)
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q0 := r.URL.Query()
	query := strings.TrimSpace(q0.Get("q"))
	isHX := r.Header.Get("HX-Request") == "true"
	mr := s.maturity()
	nsfw := s.nsfwSearchFlag()
	sortSel := normalizeSearchSort(q0.Get("sort"))
	// The empty-query feed defaults to Month (the cached popular default); a keyword
	// search defaults to AllTime so it does not silently narrow to a monthly window.
	periodDefault := "AllTime"
	if query == "" {
		periodDefault = "Month"
	}
	periodSel := normalizeSearchPeriod(q0.Get("period"), periodDefault)
	// Per-render model-subscription map (ONE ListSubscriptions query, not per card)
	// so each result card's subscribe control reflects real subscribed state.
	subs := s.modelSubscriptions()

	if query == "" {
		var res *civitai.ModelSearchResult
		var heading string
		if sortSel == "Most Downloaded" && periodSel == "Month" {
			// The default popular feed: cached per NSFW flag with its own heading.
			res, heading = s.popularModels(r.Context(), nsfw)
		} else {
			// A chosen sort/period on the empty-query feed: direct fetch, labeled.
			res = s.searchFeed(r.Context(), nsfw, sortSel, periodSel)
			heading = searchHeadingFor(sortSel, periodSel)
		}
		if isHX {
			s.render(w, http.StatusOK, searchResults(res, subs, mr, s.csrf, heading))
			return
		}
		s.render(w, http.StatusOK, searchPage("", res, subs, s.csrf, s.currentTheme(), mr, heading, sortSel, periodSel, s.rail(r.Context())))
		return
	}

	q := url.Values{}
	q.Set("query", query)
	q.Set("limit", searchLimit)
	// Thread the chosen sort/period through to civitai (defaults: Most Downloaded,
	// AllTime). Period meaningfully applies to Most Downloaded/Highest Rated.
	q.Set("sort", sortSel)
	q.Set("period", periodSel)
	// Tie the nsfw param to the maturity range so NSFW models return WITH their
	// showcase images; the per-image band filter runs at render time (setNSFWParam).
	setNSFWParam(q, nsfw)
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	res, err := s.reader.SearchModels(ctx, q)
	if err != nil {
		if isHX {
			s.render(w, http.StatusOK, errorNote("Search failed: "+err.Error()))
			return
		}
		s.render(w, http.StatusOK, searchPage(query, nil, subs, s.csrf, s.currentTheme(), mr, "", sortSel, periodSel, s.rail(r.Context())))
		return
	}
	if isHX {
		s.render(w, http.StatusOK, searchResults(res, subs, mr, s.csrf, ""))
		return
	}
	s.render(w, http.StatusOK, searchPage(query, res, subs, s.csrf, s.currentTheme(), mr, "", sortSel, periodSel, s.rail(r.Context())))
}

// searchFeed fetches a no-query model feed for a chosen sort/period (the empty-
// query search page with a non-default filter). Unlike popularModels it is NOT
// cached — the cache is reserved for the default Most Downloaded/Month feed. On
// any error it returns nil so searchResults falls back to the empty-state hint.
func (s *Server) searchFeed(parent context.Context, nsfw bool, sort, period string) *civitai.ModelSearchResult {
	q := url.Values{}
	q.Set("sort", sort)
	q.Set("period", period)
	q.Set("limit", searchLimit)
	setNSFWParam(q, nsfw)
	ctx, cancel := context.WithTimeout(parent, 20*time.Second)
	defer cancel()
	res, err := s.reader.SearchModels(ctx, q)
	if err != nil {
		s.log.Warn("search feed fetch failed", "sort", sort, "period", period, "err", err)
		return nil
	}
	return res
}

// handleSubscribeSearch backs the dashboard's integrated civitai search: it
// searches models and renders subscribe-enabled result cards (each with a
// one-click, auto-download Subscribe button) into the dashboard results
// container. GET-only; the Subscribe action itself is a CSRF-protected POST.
func (s *Server) handleSubscribeSearch(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	mr := s.maturity()
	// One ListSubscriptions query per render (not per card) → correct subscribe
	// state on each result card.
	subs := s.modelSubscriptions()
	if query == "" {
		s.render(w, http.StatusOK, subscribeSearchResults(nil, subs, mr, s.csrf))
		return
	}
	q := url.Values{}
	q.Set("query", query)
	q.Set("limit", searchLimit)
	// Return the most popular matches first (the main keyword search does the same);
	// without an explicit sort the dashboard mini-search inherited the API default.
	q.Set("sort", "Most Downloaded")
	// Same NSFW-image behavior as the main search (see setNSFWParam).
	setNSFWParam(q, s.nsfwSearchFlag())
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	res, err := s.reader.SearchModels(ctx, q)
	if err != nil {
		s.render(w, http.StatusOK, errorNote("Search failed: "+err.Error()))
		return
	}
	s.render(w, http.StatusOK, subscribeSearchResults(res, subs, mr, s.csrf))
}

// popularModels returns the "recent popular" model feed (Most Downloaded, this
// Month) used as the empty-query search default, served from a ~10 min in-process
// TTL cache so repeated dashboard/search loads do not hit civitai.com. On success
// it returns the feed plus a heading; on any fetch error it returns (nil, "") so
// the caller falls back to the "Enter a query…" hint.
func (s *Server) popularModels(parent context.Context, nsfw bool) (*civitai.ModelSearchResult, string) {
	s.popularMu.Lock()
	if v := s.popularVal[nsfw]; v != nil && time.Now().Before(s.popularExp[nsfw]) {
		s.popularMu.Unlock()
		return v, "Popular this month"
	}
	s.popularMu.Unlock()

	q := url.Values{}
	q.Set("sort", "Most Downloaded")
	q.Set("period", "Month")
	q.Set("limit", "24")
	// Include NSFW models + their showcase images unless the range tops out at PG
	// (see setNSFWParam) — without this, NSFW models return with images withheld
	// and their cards show "No showcase images".
	setNSFWParam(q, nsfw)
	ctx, cancel := context.WithTimeout(parent, 20*time.Second)
	defer cancel()
	res, err := s.reader.SearchModels(ctx, q)
	if err != nil {
		s.log.Warn("popular models fetch failed", "err", err)
		return nil, ""
	}
	s.popularMu.Lock()
	s.popularVal[nsfw] = res
	s.popularExp[nsfw] = time.Now().Add(popularTTL)
	s.popularMu.Unlock()
	return res, "Popular this month"
}

// handleModelTitle returns just the model's display name as an escaped text
// span, used by the lazy suggestion-title container when the dashboard render
// found no cached name. It resolves cache-first via cachedModelDetail (GetModel
// only on a cache miss/stale), and falls back to "Model #id" on any error so the
// card degrades gracefully rather than showing an error. GET-only; read-only.
func (s *Server) handleModelTitle(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil || id <= 0 {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	name := "Model #" + strconv.Itoa(id)
	m, _, mErr := s.cachedModelDetail(r.Context(), id)
	if mErr == nil && m != nil && m.Name != "" {
		name = m.Name
	}
	s.render(w, http.StatusOK, g.Text(name))
}

// handleModelVersionStatus backs the lazy version-status badge on the dashboard
// subscribe-suggestion cards. It resolves the model detail CACHE-FIRST (one
// GetModel on a miss/stale, matching the model-page posture), cross-references the
// model's local files, and renders the "new version" badge + popover when the
// latest remote version is not in the library — or an empty fragment when up to
// date. GET-only, read-only (no CSRF). It NEVER panics on malformed cache/JSON and
// degrades to an empty fragment on any error, so a card is never broken by it.
func (s *Server) handleModelVersionStatus(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil || id <= 0 {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	// The model's local files (model-kind) for the in-library version cross-ref.
	var files []store.LocalFile
	if fs, ferr := s.store.ListLocalFilesByModel(id); ferr == nil {
		for _, f := range fs {
			if f.Kind == store.LocalKindModel {
				files = append(files, f)
			}
		}
	}
	m, raw, err := s.cachedModelDetail(r.Context(), id)
	if err != nil || m == nil {
		// Offline / not found → render nothing rather than an error chip.
		s.render(w, http.StatusOK, g.Text(""))
		return
	}
	bd := buildVersionBreakdown(m.ModelVersions, files)
	s.render(w, http.StatusOK, versionStatusFragment(id, bd, raw))
}

// handleModelCardImages backs the LAZY showcase carousel on the dashboard
// subscribe-suggestion cards. Like the version-status/title/community lazy
// endpoints it resolves the model detail CACHE-FIRST (cachedModelDetail: one
// GetModel on a miss/stale, served from cache otherwise) — so an already-cached
// model renders its carousel with ZERO network calls. It parses the showcase
// images from the SAME inline-image path the model detail page uses and renders
// modelCardCarousel, honoring the persisted maturity range exactly as
// the search cards do. GET-only, read-only (no CSRF), same outbound-proxy posture
// as its sibling /models/{id}/{version-status,title,community} GETs. On any error
// or a model with no images it renders an EMPTY node — never an error box — so the
// card simply shows no carousel.
func (s *Server) handleModelCardImages(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil || id <= 0 {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	m, raw, err := s.cachedModelDetail(r.Context(), id)
	if err != nil || m == nil {
		s.render(w, http.StatusOK, g.Text("")) // offline/not-found → no carousel
		return
	}
	images := cardCarouselImages(raw)
	if len(images) == 0 {
		s.render(w, http.StatusOK, g.Text("")) // no images → no carousel (not an error)
		return
	}
	s.render(w, http.StatusOK, modelCardCarousel(id, images, s.maturity()))
}

func (s *Server) handleModel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	isHX := r.Header.Get("HX-Request") == "true"
	view, errNode := s.loadModelView(r.Context(), id, r.URL.Query().Get("version"))
	if errNode != nil {
		status := http.StatusBadGateway
		if view.Model == nil && errors.Is(view.loadErr, civitai.ErrNotFound) {
			status = http.StatusNotFound
		}
		// On an HX version swap the target is #version-region, so render just the
		// error node (a full page would inject <html>/navbar into the region).
		if isHX {
			s.render(w, status, errNode)
			return
		}
		// railData{} — an error page skips the outputs rail rather than paying its
		// two extra queries to decorate a "Not found".
		s.render(w, status, page("Not found", s.currentTheme(), s.csrf, s.maturity(), railData{}, errNode))
		return
	}
	// Mark which of this model's versions the user already has locally, so the
	// version list can badge them (mirrors handleModelCard's local-file gather),
	// and resolve the model's subscription (one indexed lookup, no civitai call) so
	// the header renders the correct subscribe/unsubscribe state.
	var sub *store.Subscription
	if mid, cerr := strconv.Atoi(id); cerr == nil {
		view.LocalVersionIDs = s.localVersionIDs(mid)
		sub = s.modelSubscription(mid)
	}
	// A version click is an htmx partial swap of #version-region: render ONLY that
	// region's inner content (not the full page shell), so scroll is preserved and
	// the URL is updated via hx-push-url on the link.
	//
	// The related-workflows section rides along OUT OF BAND. Its ecosystem comes
	// from the SELECTED version (a model's versions can sit on different base
	// models — LUSTIFY!'s newest is Krea 2 while its other 16 are SDXL), so it MUST
	// re-render on a version click; but it lives BELOW #version-region on the page,
	// so an in-band swap would move it. hx-swap-oob replaces it where it stands.
	// Re-rendering is cheap: a same-ecosystem switch yields the same hx-get URL and
	// is answered from facetFeed's TTL cache, costing zero outbound requests.
	if isHX {
		s.render(w, http.StatusOK, g.Group([]g.Node{
			versionRegionInner(view, sub, s.csrf, s.cfg.BaseURL),
			relatedWorkflowsOOB(view),
		}))
		return
	}
	// FULL-PAGE ONLY: the workflow-linkage sections are per-MODEL and live outside
	// #version-region, so a version swap must not pay for them again.
	if mid, cerr := strconv.Atoi(id); cerr == nil {
		view.UsedByWorkflows = s.workflowsUsingModel(r.Context(), mid)
	}
	s.render(w, http.StatusOK, modelDetailPage(view, sub, s.csrf, s.currentTheme(), s.cfg.BaseURL, s.rail(r.Context())))
}

// communityCacheTTL bounds how long a cached community-image feed is served
// before a refresh fetch. A stale entry is still served on a fetch failure
// (fail-open); this only decides when to PREFER a fresh fetch.
const communityCacheTTL = time.Hour

// communityPageSize is how many community tiles the section RENDERS.
const communityPageSize = 12

// communityFetchLimit is how many items the section FETCHES — 4x the page.
//
// WHY OVER-FETCH. /api/v1/images' `nsfw` param is a CEILING that returns a mix at
// and below it (see maturityRange.imagesNSFWCeiling), and the maturity range is a
// BAND inside that mix. Asking for 12 and then filtering would leave a short page
// whenever the band is not the whole response.
//
// WHY 4x, MEASURED. The ceiling always tracks the range MAX, so the band is
// normally the large majority of what comes back — from the 2026-07-31 probe
// (modelVersionId=3112728, limit=100, one request per ceiling):
//
//	range PG..PG13 -> ceiling Soft   -> Soft 63 + None 37            = 100% in band
//	range PG..R    -> ceiling Mature -> Mature 77 + Soft 17 + None 5 = 99%
//	range R..XXX   -> ceiling X      -> 4:15 + 8:41 + 16:40          = 96%
//
// The WORST case is a single-level band at the top, where the ceiling `X` returns
// BOTH X and XXX and the band takes only one of them:
//
//	range X..X     -> ceiling X      -> browsingLevel 8  = 41/100 = 41%
//	range XXX..XXX -> ceiling X      -> browsingLevel 16 = 40/100 = 40%
//
// 4x (48 fetched) yields ~19 in-band items at that worst measured ratio — 1.6x the
// page — in ONE request, well inside the API's limit ceiling of 200 (`limit=201`
// is an HTTP 400, probed the same day). A larger factor buys margin nobody needs
// and makes every cold community feed slower; a smaller one (2x) leaves the two
// top-of-scale bands right at the edge.
//
// 🔴 READ "~19" AS A TYPICAL CASE, NOT A FLOOR. The 40% figure is the worst ratio
// measured on ONE sampled version; the real floor is ZERO. Counter-example found
// by audit: modelVersion 2983680 under ceiling X returns {1:17, 2:8, 4:11, 16:12}
// and NOTHING at level 8, so an x:x band renders an EMPTY feed on a model with 48
// images available. Per-version distributions are arbitrary — a band can be empty
// however large the over-fetch, so no factor makes a full page guaranteed.
//
// It is NOT possible to guarantee a full page: a version may simply not HAVE 12
// images in the band. That case renders short, which is honest, rather than
// paginating a feed whose upstream cursor does not respect the filter.
//
// 🔴 This value shapes the CACHED body but is not part of the cache key. Changing
// it needs a cache invalidation like store migration 0018.
const communityFetchLimit = communityPageSize * 4

// handleModelCommunity backs the LAZY-loaded community feed at the bottom of the
// model page: recent-popular civitai images that use the selected model version.
// It is a GET fragment (no state change, no CSRF) that makes AT MOST ONE bounded
// outbound SearchImages proxy call — the same egress posture as /models — and
// NEVER breaks the page. It is CACHE-FIRST + FAIL-OPEN, keyed on
// (modelID, versionId, CEILING) — where the ceiling is derived from the user's
// maturity range MAX. That key is what stops a body fetched for one range from
// being served to another: two ranges that resolve to the SAME ceiling see the
// same body (correct — the band filter runs at render time), two that resolve to
// DIFFERENT ceilings can never share one.
//
//  1. A FRESH cached entry (within communityCacheTTL) with items is served with
//     NO fetch at all.
//  2. Otherwise it fetches; a SUCCESSFUL non-empty result is cached and rendered.
//  3. On a fetch error/timeout OR an empty result, it FALLS BACK to the last
//     cached entry (even if stale) when that has items — so a civitai outage
//     never blanks a feed the user has seen before.
//  4. Only when there is no usable cache does it render the muted note.
//
// It is fetched out-of-band (not inline during page render) because that
// SearchImages call is slow (20s+, frequently timing out); see loadModelView.
func (s *Server) handleModelCommunity(w http.ResponseWriter, r *http.Request) {
	modelID, merr := strconv.Atoi(r.PathValue("id"))
	if merr != nil || modelID <= 0 {
		s.render(w, http.StatusOK, communityFeedAbsent())
		return
	}
	versionID := strings.TrimSpace(r.URL.Query().Get("versionId"))
	mr := s.maturity()
	ceiling := mr.imagesNSFWCeiling()
	// Validate versionId is a positive integer before spending an upstream round
	// trip on it (a malformed value would only earn a rejection from civitai).
	vid, verr := strconv.Atoi(versionID)
	if verr != nil || vid <= 0 {
		s.render(w, http.StatusOK, communityFeedAbsent())
		return
	}

	// 1. Cache-first: a fresh cached entry with items serves without any fetch.
	cached, _ := s.store.GetCommunityCache(modelID, vid, ceiling)
	if cached != nil && time.Since(cached.FetchedAt) < communityCacheTTL {
		if res, derr := civitai.DecodeLeveledImageSearch(cached.Raw); derr == nil && res != nil && len(res.Items) > 0 {
			s.render(w, http.StatusOK, s.communityFeedFragment(res.Items, mr))
			return
		}
	}

	// 2. Fetch.
	q := url.Values{}
	q.Set("modelVersionId", versionID)
	q.Set("sort", "Most Reactions")
	q.Set("period", "Month")
	q.Set("limit", strconv.Itoa(communityFetchLimit))
	// REQUIRED: omitting `nsfw` is equivalent to asking for SFW only. The value is
	// the CEILING covering the range MAX — never the range itself, which the API
	// cannot express — and imagesNSFWCeiling only ever emits a value the API
	// accepts (an invalid one is an HTTP 400, not a silent no-op).
	//
	// The egress narrows with the range, not just the render: a PG-only range asks
	// for `None`, so no NSFW image URL is ever written to the local cache. Fail-safe
	// for display is not fail-safe at rest.
	q.Set("nsfw", ceiling)
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	res, err := s.reader.SearchImages(ctx, q)
	// The RAW body is the only source of per-item browsingLevel: the SDK's typed
	// ImageItem carries just the string label, which cannot separate X from XXX. A
	// result with no raw body is therefore UNUSABLE here, not merely uncacheable —
	// rendering it would mean guessing every item's maturity.
	if err == nil && res != nil && len(res.Raw) > 0 {
		lev, derr := civitai.DecodeLeveledImageSearch(res.Raw)
		if derr != nil {
			s.log.Warn("community feed decode failed", "versionId", versionID, "err", derr)
		} else if len(lev.Items) > 0 {
			// Only cache a successful, non-empty response (never poison with empty/error).
			if perr := s.store.PutCommunityCache(modelID, vid, ceiling, res.Raw); perr != nil {
				s.log.Warn("cache community feed", "model", modelID, "versionId", vid, "err", perr)
			}
			s.render(w, http.StatusOK, s.communityFeedFragment(lev.Items, mr))
			return
		}
	}
	if err != nil {
		s.log.Warn("community feed fetch failed", "versionId", versionID, "err", err)
	}

	// 3. Fail-open: fall back to the last cached entry (even if stale) with items.
	if cached != nil {
		if sres, derr := civitai.DecodeLeveledImageSearch(cached.Raw); derr == nil && sres != nil && len(sres.Items) > 0 {
			s.render(w, http.StatusOK, s.communityFeedFragment(sres.Items, mr))
			return
		}
	}

	// 4. No usable cache → render NOTHING (the section is omitted entirely rather
	// than leaving a "Community images" heading over a permanent blank). The fetch
	// error, when there was one, is already logged above.
	s.render(w, http.StatusOK, communityFeedAbsent())
}

// handleModelDownload enqueues a single model-version FILE into the app's
// download queue. It is CSRF-protected (a state-changing POST) but NOT
// loopback-gated: the destination path is derived SERVER-SIDE from the model /
// version / file metadata (civitai.DestPath under s.cfg.ModelRoot), never from a
// client-submitted path. On success it returns a "Queued ✓" fragment that
// outerHTML-replaces the file's Download button; a duplicate (an active row
// already exists) returns "Already queued"; any resolution failure returns a
// muted note. The dup-guard and single-active-per-file invariant live in
// store.Enqueue (ux_dlq_active).
func (s *Server) handleModelDownload(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil || id <= 0 {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if !s.verifyCSRF(w, r) {
		return
	}
	// Loopback-gate: download egresses AND writes a file into the model root; CSRF is
	// not an auth boundary, so disable on a non-loopback bind (security audit v0.1.64,
	// 🟡-3). Order: ParseForm → verifyCSRF → gate.
	if !s.gate(w) {
		return
	}
	versionID, _ := strconv.Atoi(r.FormValue("versionId"))
	fileID, _ := strconv.Atoi(r.FormValue("fileId"))
	if versionID <= 0 || fileID <= 0 {
		s.render(w, http.StatusOK, downloadFeedback(id, versionID, fileID, "Invalid request", false))
		return
	}

	// Resolve the model (cache-first) for its type/creator/name, and the version
	// for its files + names. Both are needed to compute the destination path.
	m, _, merr := s.cachedModelDetail(r.Context(), id)
	if merr != nil || m == nil {
		s.render(w, http.StatusOK, downloadFeedback(id, versionID, fileID, "Could not load model", false))
		return
	}
	// A CivitAI Workflows post is a model id like any other and its Archive .zip
	// would pass every check below — but `.zip` is not in library.DefaultExtensions,
	// so the bytes would never be scanned, counted, deduped or quarantined.
	// headerDownloadControl no longer renders a Download button on such a page, and
	// this is the OTHER half: a button disappearing is not the same as the endpoint
	// refusing, and this endpoint is reachable by any hand-crafted loopback+CSRF
	// POST (and by a stale page rendered before an upgrade). Fails OPEN on an empty
	// type, like every other type check in this codebase — refusing a model whose
	// type we simply did not get would be the worse bug.
	if civitai.IsWorkflowPost(m.Type) {
		s.render(w, http.StatusOK, downloadFeedback(id, versionID, fileID,
			"Workflow posts are imported, not downloaded — use Import workflows", false))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	vd, _, verr := s.reader.GetModelVersion(ctx, strconv.Itoa(versionID))
	if verr != nil || vd == nil {
		s.render(w, http.StatusOK, downloadFeedback(id, versionID, fileID, "Could not load version", false))
		return
	}
	var file *civitai.ModelVersionFile
	for i := range vd.Files {
		if vd.Files[i].ID == fileID {
			file = &vd.Files[i]
			break
		}
	}
	if file == nil {
		s.render(w, http.StatusOK, downloadFeedback(id, versionID, fileID, "File not found", false))
		return
	}
	dlURL := strings.TrimSpace(file.DownloadURL)
	if dlURL == "" {
		dlURL = strings.TrimSpace(vd.DownloadURL) // version-level fallback
	}
	if dlURL == "" {
		s.render(w, http.StatusOK, downloadFeedback(id, versionID, fileID, "No download URL available", false))
		return
	}

	creator := ""
	if m.Creator != nil {
		creator = m.Creator.Username
	}
	dest := civitai.DestPath(s.cfg.ModelRoot, m.Type, creator, m.Name, vd.Name, file.Name)
	_, inserted, eerr := s.store.Enqueue(store.QueueItem{
		ModelID:        id,
		VersionID:      versionID,
		FileID:         fileID,
		FileName:       file.Name,
		DownloadURL:    dlURL,
		DestPath:       dest,
		Status:         store.StatusQueued,
		SizeKB:         file.SizeKB,
		SHA256Expected: file.Hashes.SHA256,
	})
	if eerr != nil {
		s.log.Warn("enqueue file download", "model", id, "version", versionID, "file", fileID, "err", eerr)
		s.render(w, http.StatusOK, downloadFeedback(id, versionID, fileID, "Enqueue failed", false))
		return
	}
	if !inserted {
		s.render(w, http.StatusOK, downloadFeedback(id, versionID, fileID, "Already queued", false))
		return
	}
	s.render(w, http.StatusOK, downloadFeedback(id, versionID, fileID, "Queued ✓", true))
}

// maturity returns the persisted app-wide PG..XXX maturity range.
//
// It defaults to the FULL range on an absent row, a store error, or a malformed
// value. That is deliberately fail-OPEN, and it is the one place in this feature
// that is: the range is a user preference, not an access control, and silently
// narrowing it on a bad read would make content the user chose to see vanish with
// no explanation. Fail-CLOSED lives one layer down, on the per-item level
// (maturityUnknown), where an unrated item really is omitted.
func (s *Server) maturity() maturityRange {
	v, err := s.store.GetSettingDefault(maturitySettingKey, "")
	if err != nil {
		return fullMaturityRange()
	}
	if r, ok := parseMaturityRange(v); ok {
		return r
	}
	return fullMaturityRange()
}

// loadModelView fetches and assembles the rich model-detail view: model detail
// (with a description parsed from the raw body), the selected version's detail
// (default: the latest), and the showcase image gallery — the latter sourced
// entirely from the version's INLINE images[] in the model/version JSON, with no
// separate /api/v1/images call in the page path. The version call degrades
// gracefully — a failure there still renders the page. It returns a non-nil error
// node only when the model itself cannot be loaded.
func (s *Server) loadModelView(parent context.Context, id, versionParam string) (modelDetailView, g.Node) {
	ctx, cancel := context.WithTimeout(parent, 20*time.Second)
	defer cancel()

	m, raw, err := s.reader.GetModel(ctx, id)
	if err != nil {
		return modelDetailView{loadErr: err},
			errorNote("Could not load model " + id + ": " + err.Error())
	}

	view := modelDetailView{
		Model:       m,
		Description: parseModelDescription(raw),
		Maturity:    s.maturity(),
		// Newest publishedAt across all versions → header "Updated X ago".
		LastUpdated: newestVersionPublishedAt(raw),
		// Per-version publish times, keyed by version ID → the version tabs' date
		// popovers. Keyed by ID because modelVersions[] is index-ordered, not
		// date-ordered (see the CLAUDE.md data gotcha).
		VersionPublishedAt: versionPublishedTimes(raw),
	}

	// "Already imported?" for the workflow-import section. Only Workflows-type
	// models pay for these queries — and only TWO of them, both keyed on model_id:
	// the total (for the sentence) and at most importedWorkflowsCap rows (for the
	// carousel of cards). Never one query per card. The list is skipped entirely
	// when the count is zero, so the common "not imported yet" page is unchanged.
	if civitai.IsWorkflowPost(m.Type) {
		if mid, aerr := strconv.Atoi(id); aerr == nil {
			if n, cerr := s.store.CountWorkflowsByModel(ctx, mid); cerr == nil {
				view.ImportedWorkflows = n
				if n > 0 {
					if wfs, lerr := s.store.ListWorkflowsByModel(ctx, mid, importedWorkflowsCap); lerr == nil {
						view.ImportedWorkflowList = wfs
					} else {
						// Degrade to the sentence + library link rather than failing the
						// page: the count already proved the workflows are there.
						s.log.Warn("list imported workflows", "model", id, "err", lerr)
					}
				}
			} else {
				s.log.Warn("count imported workflows", "model", id, "err", cerr)
			}
		}
	}

	// Selected version: the ?version= override, else the latest (first listed).
	selVID := 0
	if versionParam != "" {
		selVID, _ = strconv.Atoi(versionParam)
	}
	if selVID == 0 && len(m.ModelVersions) > 0 {
		selVID = m.ModelVersions[0].ID
	}
	view.SelectedVersionID = selVID
	var versionRaw []byte
	if selVID > 0 {
		if vd, vraw, verr := s.reader.GetModelVersion(ctx, strconv.Itoa(selVID)); verr == nil {
			view.Version = vd
			view.PublishedAt = parsePublishedAt(vraw)
			versionRaw = vraw
		}
	}

	// Showcase gallery: sourced from the version's INLINE images[] — already
	// present in the model/version JSON fetched above. This deliberately does NOT
	// make a separate GET /api/v1/images (SearchImages) call: that call was slow
	// (20s+, frequently timing out) and its error was silently swallowed into an
	// empty gallery. The maturity range decides which tiles are emitted at all.
	view.Images = parseVersionImages(versionRaw, raw, selVID)
	return view, nil
}

// handleSetMaturity persists the app-wide PG..XXX maturity range (set from the
// navbar's two-ended control) and asks htmx to refresh so the CURRENT page
// re-renders under the new band — whichever page it is. This mirrors the theme
// toggle's HX-Refresh pattern, so the one control works everywhere rather than
// only on the model page. CSRF-protected like every other state-changing POST.
//
// An INVALID or INVERTED submission (min > max) is REJECTED with 400 and NOTHING
// is persisted. It is deliberately not normalized: silently swapping the ends
// would grant a band the user did not ask for, and clamping to an empty band
// would blank every gallery in a way that reads as a fetch failure. The control
// itself cannot produce one (see maturityControl), so a 400 here means a
// hand-made request, not a user mistake.
func (s *Server) handleSetMaturity(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if !s.verifyCSRF(w, r) {
		return
	}
	lo, okLo := maturityFromSlug(r.FormValue("min"))
	hi, okHi := maturityFromSlug(r.FormValue("max"))
	if !okLo || !okHi {
		http.Error(w, "unknown maturity level", http.StatusBadRequest)
		return
	}
	mr := maturityRange{Min: lo, Max: hi}
	if !mr.valid() {
		http.Error(w, "inverted maturity range", http.StatusBadRequest)
		return
	}
	if err := s.store.SetSetting(maturitySettingKey, mr.String()); err != nil {
		s.renderError(w, "save maturity range", err)
		return
	}
	w.Header().Set("HX-Refresh", "true")
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleCreator(w http.ResponseWriter, r *http.Request) {
	username := r.PathValue("username")
	q := url.Values{}
	q.Set("username", username)
	q.Set("sort", "Newest")
	q.Set("limit", searchLimit)
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	res, err := s.reader.SearchModels(ctx, q)
	if err != nil {
		// railData{} — see handleModel: error pages skip the rail's queries.
		s.render(w, http.StatusBadGateway, page("@"+username, s.currentTheme(), s.csrf, s.maturity(), railData{}, errorNote("Could not load creator: "+err.Error())))
		return
	}
	// One ListSubscriptions query per render → each model card reflects real
	// subscribe state (the creator-subscribe button in the header is separate).
	subs := s.modelSubscriptions()
	s.render(w, http.StatusOK, creatorPage(username, res, subs, s.csrf, s.currentTheme(), s.maturity(), s.rail(r.Context())))
}

func (s *Server) handleSubscribe(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.renderSubsWithError(w, "invalid form: "+err.Error())
		return
	}
	if !s.verifyCSRF(w, r) {
		return
	}
	// Loopback-gate: subscribing is a state change (and enables auto-download egress);
	// CSRF is not an auth boundary, so disable on a non-loopback bind (security audit
	// v0.1.64, 🟡-3). Order: ParseForm → verifyCSRF → gate.
	if !s.gate(w) {
		return
	}
	// `mode` is the radio spelling the creator control (and the model options
	// panel) uses; the dashboard form keeps its checkboxes. Reading BOTH is purely
	// additive — the dashboard form sends no `mode`, so its behaviour is unchanged.
	mode := r.FormValue("mode")
	opts := poller.SubscribeOptions{
		AutoDownload:   checkboxVal(r, "auto_download") || mode == "auto_download",
		NotifyOnly:     checkboxVal(r, "notify_only") || mode == "notify_only",
		BackfillLatest: checkboxVal(r, "backfill_latest"),
		PollInterval:   s.cfg.DefaultPollInterval,
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	creator := strings.TrimSpace(r.FormValue("creator"))
	modelRef := strings.TrimSpace(r.FormValue("model"))

	var subErr error
	switch {
	case creator != "":
		_, subErr = s.sub.SubscribeCreator(ctx, creator, opts)
	case modelRef != "":
		modelID, perr := civitai.ParseModelRef(modelRef)
		if perr != nil {
			s.renderSubsWithError(w, perr.Error())
			return
		}
		_, subErr = s.sub.SubscribeModel(ctx, modelID, opts)
	default:
		s.renderSubsWithError(w, "provide a model id/URL or a creator username")
		return
	}

	if subErr != nil {
		if errors.Is(subErr, poller.ErrAlreadySubscribed) {
			s.renderSubsWithError(w, "already subscribed to that target")
			return
		}
		s.renderSubsWithError(w, "subscribe failed: "+subErr.Error())
		return
	}
	s.renderSubsWithError(w, "")
}

// modelIsWorkflowPost answers "is this model id a CivitAI Workflows post?" for
// the subscribe-control handlers WITHOUT a network round trip: it reads the
// model_cache snapshot and IGNORES its TTL, because a model's type does not
// change — a stale snapshot is authoritative for this one field in a way it is
// not for stats or version lists.
//
// It fails OPEN: a cache miss answers false. That is the safe direction. A
// workflow post whose detail was never cached renders the ordinary Subscribe
// control and the poller's type guard still refuses the download; answering true
// on a miss would silently narrow a REAL model's subscribe options, which is the
// regression this whole change must not cause.
func (s *Server) modelIsWorkflowPost(id int) bool {
	ent, err := s.store.GetModelCache(id)
	if err != nil || ent == nil {
		return false
	}
	m := decodeModelDetail(ent.Raw)
	return m != nil && civitai.IsWorkflowPost(m.Type)
}

// workflowPostFlag ORs the server-side answer with the request's `workflow=1`
// hint (query or form). The rendered control carries that hint across every htmx
// swap so a SEARCH CARD keeps its notify-only shape through the whole flow —
// search results never write model_cache, so the server-side check alone would
// answer false there. The hint can only NARROW the control to notify-only and
// can never grant anything, so a forged one costs nothing.
func (s *Server) workflowPostFlag(r *http.Request, id int) bool {
	return r.FormValue(workflowParamName) == "1" || s.modelIsWorkflowPost(id)
}

// handleModelSubscribeOptions (GET) renders the subscribe options panel (state 2
// of the shared control): the auto-download vs notify-only choice + Confirm/Cancel.
// The heading resolves the model name from the local model_cache only (zero
// civitai calls), falling back to "this model" on a cache miss. Read-only; no CSRF.
func (s *Server) handleModelSubscribeOptions(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil || id <= 0 {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	name := "this model"
	if ent, _ := s.store.GetModelCache(id); ent != nil && ent.Name != "" {
		name = ent.Name
	}
	s.render(w, http.StatusOK, subscribeOptionsPanel(id, name, s.csrf, s.workflowPostFlag(r, id)))
}

// handleModelSubscribeControl (GET) re-renders the shared subscribe control in its
// current persisted state (collapsed when not subscribed, subscribed feedback when
// subscribed). It backs the options panel's Cancel action. Read-only; no CSRF.
func (s *Server) handleModelSubscribeControl(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil || id <= 0 {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	s.render(w, http.StatusOK, subscribeControl(id, s.modelSubscription(id), s.csrf, s.workflowPostFlag(r, id)))
}

// handleModelSubscribe creates a MODEL subscription honoring the options panel's
// choice (auto-download by default, or notify-only when mode=notify_only /
// notify_only=true) and returns the shared control's subscribed-feedback fragment.
// It is SEPARATE from the dashboard's POST /subscribe → subscriptionsTable flow.
// CSRF-protected. An already-subscribed model is idempotent (still returns the
// subscribed feedback).
func (s *Server) handleModelSubscribe(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil || id <= 0 {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if !s.verifyCSRF(w, r) {
		return
	}
	// Loopback-gate: state change (+ auto-download egress); CSRF is not an auth
	// boundary → disable on a non-loopback bind (security audit v0.1.64, 🟡-3).
	if !s.gate(w) {
		return
	}
	// The options panel sends the choice as a radio (mode=auto_download|notify_only);
	// an explicit notify_only=true is also honored.
	notifyOnly := checkboxVal(r, "notify_only") || r.FormValue("mode") == "notify_only"
	// A workflow post can only ever be a notify-only subscription: the poller
	// permanently refuses to download one, so storing auto_download would make the
	// control render "Subscribed ✓ · auto-download" about something that will never
	// happen. Coerce here as well as in the panel — the panel's hidden field only
	// binds a browser.
	workflow := s.workflowPostFlag(r, id)
	if workflow {
		notifyOnly = true
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	_, subErr := s.sub.SubscribeModel(ctx, id, poller.SubscribeOptions{
		AutoDownload: !notifyOnly,
		NotifyOnly:   notifyOnly,
		PollInterval: s.cfg.DefaultPollInterval,
	})
	if subErr != nil && !errors.Is(subErr, poller.ErrAlreadySubscribed) {
		// A genuine failure (e.g. the model didn't resolve): surface it in the
		// control instead of silently collapsing to a bare "Subscribe" button.
		s.log.Warn("model subscribe", "model", id, "err", subErr)
		s.render(w, http.StatusOK, subscribeControlCollapsed(id, s.csrf, "Subscribe failed — please try again.", workflow))
		return
	}
	// Re-render from the persisted state so the control reflects reality
	// (subscribed on success / already-subscribed).
	s.render(w, http.StatusOK, subscribeControl(id, s.modelSubscription(id), s.csrf, workflow))
}

// handleModelUnsubscribe removes the model subscription and returns the collapsed
// subscribe control with an "Unsubscribed" note. It looks up the model's
// subscription id and deletes it. CSRF-protected.
func (s *Server) handleModelUnsubscribe(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil || id <= 0 {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if !s.verifyCSRF(w, r) {
		return
	}
	// Loopback-gate: state change; CSRF is not an auth boundary → disable on a
	// non-loopback bind (security audit v0.1.64, 🟡-3).
	if !s.gate(w) {
		return
	}
	if sub := s.modelSubscription(id); sub != nil {
		if derr := s.store.DeleteSubscription(sub.ID); derr != nil && !errors.Is(derr, store.ErrNotFound) {
			s.log.Warn("model unsubscribe", "model", id, "err", derr)
		}
	}
	s.render(w, http.StatusOK, subscribeControlCollapsed(id, s.csrf, "Unsubscribed", s.workflowPostFlag(r, id)))
}

func (s *Server) handleFlags(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if !s.verifyCSRF(w, r) {
		return
	}
	auto := r.FormValue("auto_download") == "true"
	notify := r.FormValue("notify_only") == "true"
	if err := s.store.SetSubscriptionFlags(id, auto, notify); err != nil {
		s.renderError(w, "update flags", err)
		return
	}
	sub, err := s.store.GetSubscription(id)
	if err != nil {
		s.renderError(w, "reload subscription", err)
		return
	}
	s.render(w, http.StatusOK, subscriptionRow(*sub, s.csrf))
}

func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	if !s.verifyCSRF(w, r) {
		return
	}
	if err := s.store.DeleteSubscription(id); err != nil && !errors.Is(err, store.ErrNotFound) {
		s.renderError(w, "delete subscription", err)
		return
	}
	// Empty body: the htmx outerHTML swap removes the row.
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleEventsFragment(w http.ResponseWriter, r *http.Request) {
	events, err := s.store.RecentEvents(40)
	if err != nil {
		s.render(w, http.StatusOK, errorNote("Could not load activity: "+err.Error()))
		return
	}
	s.render(w, http.StatusOK, eventsFragment(events))
}

func (s *Server) handleQueueFragment(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListQueue()
	if err != nil {
		s.render(w, http.StatusOK, errorNote("Could not load queue: "+err.Error()))
		return
	}
	if len(items) > 25 {
		items = items[:25]
	}
	s.render(w, http.StatusOK, queueFragment(items))
}

// --- helpers ---

func (s *Server) renderSubsWithError(w http.ResponseWriter, errMsg string) {
	subs, err := s.store.ListSubscriptions()
	if err != nil {
		s.renderError(w, "load subscriptions", err)
		return
	}
	s.render(w, http.StatusOK, subscriptionsTable(subs, errMsg, s.csrf))
}

func (s *Server) renderError(w http.ResponseWriter, what string, err error) {
	s.log.Error(what, "err", err)
	s.render(w, http.StatusInternalServerError, errorNote(what+": "+err.Error()))
}

func errorNote(msg string) g.Node {
	return alert("error", "", g.Text(msg))
}

func checkboxVal(r *http.Request, name string) bool {
	return r.FormValue(name) == "true"
}
