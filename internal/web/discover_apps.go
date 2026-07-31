package web

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
	g "maragu.dev/gomponents"
	h "maragu.dev/gomponents/html"
)

// appsLister is the narrow read surface the apps-discover page depends on.
// *civitai.AppsClient satisfies it; tests supply a fake serving a synthetic
// catalog without touching civitai.com.
type appsLister interface {
	ListApps(ctx context.Context, p civitai.AppsParams) (*civitai.AppsPage, error)
}

// appsPageLimit is how many apps to request per page (well under the API's max
// of 50 and its lenient rate limit).
const appsPageLimit = 24

// appsKindOptions / appsSortOptions back the browse filters. Each option's Value
// is the EXACT query string sent to /api/v1/apps; Label is the human wording.
var appsKindOptions = []selectOption{
	{"all", "All"},
	{"onsite", "On-site"},
	{"offsite", "Off-site"},
}

var appsSortOptions = []selectOption{
	{"top-rated", "Top rated"},
	{"popular", "Popular"},
	{"newest", "Newest"},
	{"name", "Name"},
}

// appsCategoryOptions is intentionally minimal: only "All". The real category
// list (MARKETPLACE_CATEGORIES) is server-side and the catalog is dark today, so
// we do NOT fabricate category names. A deep-linked ?category= is still honored
// (validated by normalizeAppsCategory) so the axis works the moment the
// marketplace publishes categories.
var appsCategoryOptions = []selectOption{
	{"", "All categories"},
}

// normalizeAppsKind whitelists the kind filter, defaulting to "all".
func normalizeAppsKind(v string) string {
	switch v {
	case "all", "onsite", "offsite":
		return v
	}
	return "all"
}

// normalizeAppsSort whitelists the sort, defaulting to "top-rated" (the API
// default). Only whitelisted values are ever forwarded to civitai.
func normalizeAppsSort(v string) string {
	switch v {
	case "top-rated", "popular", "newest", "name":
		return v
	}
	return "top-rated"
}

// appsCategoryRe bounds an accepted category to a safe slug (lowercase alnum +
// hyphen). Anything else normalizes to "" (All) rather than being forwarded
// verbatim.
var appsCategoryRe = regexp.MustCompile(`^[a-z0-9-]{1,40}$`)

// normalizeAppsCategory returns v when it is a safe category slug, else ""
// (All). We don't enumerate the real category set (unknown pre-launch), but we
// never forward an unbounded/bogus value.
func normalizeAppsCategory(v string) string {
	v = strings.TrimSpace(v)
	if appsCategoryRe.MatchString(v) {
		return v
	}
	return ""
}

// handleDiscoverApps backs the Apps browse page (Slice A1). It is GET-only and
// read-only: no CSRF (browsing + external links are not state-changing on our
// side) and NOT loopback-gated (it is an outbound proxy GET like the model
// search, not an arbitrary-path primitive). The SAME handler serves the full
// page and, on HX-Request, the results fragment.
//
// Egress: the chosen filters (kind/category/sort/cursor) are sent to
// civitai.com. No file hashes are sent (unlike the scan match_remote path).
//
// Reality: the catalog is dark for a normal user, so a normal load returns
// {items:[]} → an honest empty state (not an error). A client/API error renders
// a clean "couldn't load apps" note (never a 500).
func (s *Server) handleDiscoverApps(w http.ResponseWriter, r *http.Request) {
	q0 := r.URL.Query()
	isHX := r.Header.Get("HX-Request") == "true"
	mr := s.maturity()

	kindSel := normalizeAppsKind(q0.Get("kind"))
	catSel := normalizeAppsCategory(q0.Get("category"))
	sortSel := normalizeAppsSort(q0.Get("sort"))
	cursor := strings.TrimSpace(q0.Get("cursor"))

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	page, err := s.appsClient().ListApps(ctx, civitai.AppsParams{
		Kind:     kindSel,
		Category: catSel,
		Sort:     sortSel,
		Cursor:   cursor,
		Limit:    appsPageLimit,
	})

	var results g.Node
	if err != nil {
		s.log.Error("apps list", "err", err)
		results = appsErrorNote()
	} else {
		results = appsDiscoverResults(page, kindSel, catSel, sortSel)
	}

	if isHX {
		s.render(w, http.StatusOK, results)
		return
	}
	s.render(w, http.StatusOK, appsDiscoverPage(results, s.currentTheme(), mr, kindSel, catSel, sortSel, s.csrf, s.rail(r.Context())))
}

// appsDiscoverPage renders the full Apps browse page: the filter form (kind /
// category / sort dropdowns wired to GET /apps/discover) and the results
// container. The container id is stable — the form and the cursor "next" control
// both swap its innerHTML, never the container itself (streaming-job invariant).
func appsDiscoverPage(results g.Node, theme string, mr maturityRange, kindSel, catSel, sortSel, csrf string, rail ...railData) g.Node {
	return page("Apps", theme, csrf, mr, railOf(rail),
		card(
			pageTitle("Apps"), // the page's single <h1>
			h.P(h.Class("text-sm text-slate-400 mb-3"),
				g.Text("Browse published CivitAI Apps. Your chosen filters are sent to civitai.com; opening an app launches it in your browser (on civitai.com or the app's own site).")),
			h.Form(
				h.Class("flex flex-wrap items-end gap-3"),
				hx("get", "/apps/discover"),
				hx("target", "#apps-discover-results"),
				hx("swap", "innerHTML"),
				// Reload when any filter changes (no cursor carried → resets to page 1).
				hx("trigger", "change"),
				labeledSelect("apps-kind", "kind", "Kind", appsKindOptions, kindSel),
				labeledSelect("apps-category", "category", "Category", appsCategoryOptions, catSel),
				labeledSelect("apps-sort", "sort", "Sort", appsSortOptions, sortSel),
				btnPrimary(g.Text("Apply")),
			),
		),
		h.Div(h.ID("apps-discover-results"), results),
	)
}

// appsDiscoverResults renders the results fragment (used by HX swaps too): the
// app-card grid plus a cursor "next" control. A nil page or empty items list
// renders the honest pre-launch empty state, NOT an error. kindSel/catSel/sortSel
// are baked into the "next" control so pagination preserves the active filters.
//
// 🔴 APPS ARE OUT OF SCOPE OF THE MATURITY RANGE, deliberately — this used to take
// a maturityRange it never read, a signature that promised filtering which did not
// happen. An app carries `contentRating`, a STRING, not the numeric browsingLevel
// the whole scale is keyed on (see maturity.go), so there is nothing here to
// compare a band against. Same standing as the outputs rail: not "level 0", not
// maturityUnknown — outside the scale.
//
// This is currently unobservable: /api/v1/apps is launch-gated and returns
// {"items":[]} for a normal user (re-verified live). When CivitAI flips that flag
// third-party cover art WILL render at every band, so if apps must respect the
// range, map contentRating -> a level HERE first — do not re-add an unread param.
func appsDiscoverResults(page *civitai.AppsPage, kindSel, catSel, sortSel string) g.Node {
	if page == nil || len(page.Items) == 0 {
		return appsEmptyState()
	}
	grid := h.Div(
		h.Class("cm-cardgrid"),
		g.Map(page.Items, func(a civitai.App) g.Node { return appCard(a) }),
	)
	next := appsNextControl(page.Metadata.NextCursor, kindSel, catSel, sortSel)
	return h.Div(grid, next)
}

// appsEmptyState is the load-bearing pre-launch message. The catalog returns no
// apps to a normal user until CivitAI opens the marketplace; this must read as a
// correct, pending state — not a broken page.
func appsEmptyState() g.Node {
	return h.Div(
		h.Class("rounded-md border border-slate-800 bg-slate-900 p-4"),
		h.P(h.Class("text-sm text-slate-400"),
			g.Text("No published apps yet — CivitAI's App marketplace hasn't launched publicly. This page will populate automatically when it does.")),
	)
}

// appsErrorNote is the clean, non-fatal failure state for a client/API error.
func appsErrorNote() g.Node {
	return alert("warning", "Couldn't load apps",
		g.Text("Couldn't reach the CivitAI apps catalog right now. Try again in a moment."))
}

// appsNextControl renders the cursor "next page" affordance. It is empty when
// there is no next cursor. It issues a GET carrying the active filters + the
// opaque cursor, swapping the stable results container's innerHTML.
func appsNextControl(nextCursor, kindSel, catSel, sortSel string) g.Node {
	if strings.TrimSpace(nextCursor) == "" {
		return g.Text("")
	}
	q := url.Values{}
	q.Set("kind", kindSel)
	if catSel != "" {
		q.Set("category", catSel)
	}
	q.Set("sort", sortSel)
	q.Set("cursor", nextCursor)
	target := "/apps/discover?" + q.Encode()
	return h.Div(
		h.Class("mt-4 flex justify-center"),
		civButton("outline", "md",
			[]g.Node{
				h.Type("button"),
				hx("get", target),
				hx("target", "#apps-discover-results"),
				hx("swap", "innerHTML"),
			},
			g.Text("Next page"),
		),
	)
}

// appCard renders one app: a cover/icon thumbnail, name, tagline, content-rating
// + category badges, creator chip, recommend %/review count, and ONE primary
// action — the external click-to-play anchor (see appPlayAction). All untrusted
// text is emitted via g.Text (escaped); the play URL's scheme is validated
// before it becomes an href (see appPlayURL).
func appCard(a civitai.App) g.Node {
	thumb := strings.TrimSpace(a.CoverURL)
	if thumb == "" {
		thumb = strings.TrimSpace(a.IconURL)
	}

	var badges []g.Node
	if a.ContentRating != "" {
		badges = append(badges, badge(a.ContentRating, "slate"))
	}
	if a.Category != "" {
		badges = append(badges, badge(a.Category, "indigo"))
	}
	if k := appsKindLabel(a.Kind); k != "" {
		badges = append(badges, badge(k, "blue"))
	}

	children := []g.Node{
		h.Class("flex flex-col gap-2"),
	}
	if thumb != "" {
		// App icon/cover loads from the civitai CDN (same class as model showcase
		// images). The manager's own assets remain vendored.
		children = append(children, h.Img(
			h.Src(thumb),
			h.Alt(a.Name),
			h.Loading("lazy"),
			h.Class("w-full h-40 object-cover rounded border border-slate-800 bg-slate-900"),
		))
	}
	children = append(children,
		h.H3(h.Class("text-base font-semibold text-slate-100"), g.Text(a.Name)),
	)
	if a.Tagline != "" {
		children = append(children, h.P(h.Class("text-sm text-slate-400"), g.Text(a.Tagline)))
	}
	if len(badges) > 0 {
		children = append(children, h.Div(append([]g.Node{h.Class("flex flex-wrap items-center gap-2")}, badges...)...))
	}
	if a.Creator.Username != "" {
		children = append(children, h.Div(
			h.Class("text-xs text-slate-400"),
			g.Text("@"+a.Creator.Username),
		))
	}
	children = append(children, appRecommendLine(a))
	children = append(children, appPlayAction(a))

	return card(children...)
}

// appsKindLabel maps the API kind to a short human badge label ("" for unknown).
func appsKindLabel(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "onsite":
		return "On-site"
	case "offsite":
		return "Off-site"
	}
	return ""
}

// appRecommendLine renders the recommend %/review-count summary. It shows the
// percentage only when the API provides one (recommendPct is nullable).
func appRecommendLine(a civitai.App) g.Node {
	if a.Recommend.RecommendPct != nil {
		return h.Div(
			h.Class("text-xs text-slate-400"),
			g.Text(fmt.Sprintf("%d%% recommend · %d reviews", int(*a.Recommend.RecommendPct), a.ReviewCount)),
		)
	}
	return h.Div(
		h.Class("text-xs text-slate-500"),
		g.Text(fmt.Sprintf("%d reviews", a.ReviewCount)),
	)
}

// appPlayURL returns the click-to-play target for an app and whether it is a
// valid, SAFE (http/https) URL.
//
//   - offsite → kindData.externalUrl (fully external, from the API — UNTRUSTED).
//   - onsite  → kindData.liveUrl if present, else the civitai detail page
//     https://civitai.com/apps/{slug} (we construct that fallback; we do NOT
//     link straight to /apps/run/{slug}, which can SSR-404 for a viewer without
//     the page flag).
//
// SECURITY: the API-supplied URL flows into an href. A non-http(s) scheme
// (javascript:, data:, …) is REJECTED — returns ("", false) so NO live href is
// rendered — to prevent an href-injection XSS from a malicious app listing.
func appPlayURL(a civitai.App) (string, bool) {
	var raw string
	switch strings.ToLower(strings.TrimSpace(a.Kind)) {
	case "offsite":
		raw = a.KindData.ExternalURL
	default: // onsite (and unknown kinds): prefer liveUrl, else the detail page.
		raw = a.KindData.LiveURL
		if strings.TrimSpace(raw) == "" && strings.TrimSpace(a.Slug) != "" {
			raw = "https://civitai.com/apps/" + a.Slug
		}
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false
	}
	if !isSafeHTTPURL(raw) {
		return "", false
	}
	return raw, true
}

// isSafeHTTPURL reports whether raw parses to an http/https URL with a host. Any
// other scheme (javascript:, data:, mailto:, …) or a parse failure is unsafe.
func isSafeHTTPURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		return u.Host != ""
	}
	return false
}

// appPlayAction renders the primary click-to-play control: an external anchor
// opened in a new tab, hardened with rel=noopener noreferrer (mirroring
// viewOnCivitaiLink). For a missing/unsafe play URL it renders a non-link
// "Unavailable" state — never an href — so a malicious URL cannot inject.
func appPlayAction(a civitai.App) g.Node {
	playURL, ok := appPlayURL(a)
	if !ok {
		return h.Span(h.Class("mt-1 text-xs text-slate-500"), g.Text("Unavailable"))
	}
	return h.A(
		h.Href(playURL),
		h.Target("_blank"),
		g.Attr("rel", "noopener noreferrer"),
		dataAttr("civitai-ui", "button"),
		dataAttr("variant", "filled"),
		dataAttr("size", "sm"),
		g.Text("Open app ↗"),
	)
}
