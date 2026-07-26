package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
	g "maragu.dev/gomponents"
	h "maragu.dev/gomponents/html"
)

// handleDiscoverWorkflows backs the browse-only "Discover workflows" page
// (Slice D1). It is the existing model search re-skinned and pinned to
// type=Workflows: it reuses handleSearch's query/limit/sort/period/NSFW param
// assembly verbatim, then calls the SAME s.reader.SearchModels the model search
// uses. It is GET-only and read-only — there are no state-changing controls on
// this page (no subscribe/download/import), so no CSRF is involved.
//
// Egress posture is identical to /search: a non-empty query is sent to
// civitai.com (with the nsfw flag tied to the display mode). An empty query does
// NOT fetch — it renders a plain "search for workflows" prompt (D1 keeps the
// empty state simple; the optional cached-popular feed is deferred).
//
// Like the model search it mirrors, results are capped at searchLimit; it does
// not paginate (the existing /search page does not either).
func (s *Server) handleDiscoverWorkflows(w http.ResponseWriter, r *http.Request) {
	q0 := r.URL.Query()
	query := strings.TrimSpace(q0.Get("q"))
	isHX := r.Header.Get("HX-Request") == "true"
	mode := s.nsfwMode()
	nsfw := s.nsfwSearchFlag()
	sortSel := normalizeSearchSort(q0.Get("sort"))
	// A keyword search defaults to AllTime (the least-restrictive window), matching
	// the keyword branch of handleSearch — so results are not silently narrowed.
	periodSel := normalizeSearchPeriod(q0.Get("period"), "AllTime")

	if query == "" {
		// Empty state: no fetch, no egress — just the prompt.
		if isHX {
			s.render(w, http.StatusOK, workflowDiscoverResults(nil, mode, ""))
			return
		}
		s.render(w, http.StatusOK, workflowDiscoverPage("", nil, s.currentTheme(), mode, sortSel, periodSel))
		return
	}

	// Reuse the model-search param assembly verbatim, then pin type=Workflows.
	q := url.Values{}
	q.Set("query", query)
	q.Set("limit", searchLimit)
	q.Set("sort", sortSel)
	q.Set("period", periodSel)
	// Tie nsfw to the display mode so NSFW models return WITH their showcase images
	// (blur/show) or are excluded (hide) — same posture as the model search.
	setNSFWParam(q, nsfw)
	q.Set("type", "Workflows")

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	res, err := s.reader.SearchModels(ctx, q)
	if err != nil {
		if isHX {
			s.render(w, http.StatusOK, errorNote("Search failed: "+err.Error()))
			return
		}
		s.render(w, http.StatusOK, workflowDiscoverPage(query, nil, s.currentTheme(), mode, sortSel, periodSel))
		return
	}
	if isHX {
		s.render(w, http.StatusOK, workflowDiscoverResults(res, mode, ""))
		return
	}
	s.render(w, http.StatusOK, workflowDiscoverPage(query, res, s.currentTheme(), mode, sortSel, periodSel))
}

// discoverPage renders the full "Discover workflows" page: the search form (query
// + sort/period dropdowns, wired to GET /workflows/discover) and the results
// container. It reuses the same lightbox + carousel scripts the search page uses.
func workflowDiscoverPage(query string, res *civitai.ModelSearchResult, theme, mode, sortSel, periodSel string) g.Node {
	// No CSRF token is needed (the page has no state-changing controls); pass "".
	return page("Discover workflows", theme, "", mode,
		card(
			sectionTitle("Discover workflows"),
			h.P(h.Class("text-sm text-slate-400 mb-3"),
				g.Text("Browse ComfyUI workflows on CivitAI. Your search query is sent to civitai.com. Each result links to its model page.")),
			h.Form(
				h.Class("flex flex-wrap items-end gap-3"),
				hx("get", "/workflows/discover"),
				hx("target", "#discover-results"),
				hx("swap", "innerHTML"),
				hx("trigger", "submit"),
				h.Div(
					h.Class("min-w-[12rem] flex-1"),
					textInput("text-input", "discover-q", "Query",
						h.Type("text"), h.Name("q"), h.Value(query),
						h.Placeholder("Search workflows by name, tag, …")),
				),
				labeledSelect("discover-sort", "sort", "Sort", searchSortOptions, sortSel),
				labeledSelect("discover-period", "period", "Period", searchPeriodOptions, periodSel),
				btnPrimary(g.Text("Search")),
			),
		),
		h.Div(h.ID("discover-results"), workflowDiscoverResults(res, mode, "")),
		lightboxOverlay(),
		modelPageScript(),
		libraryCarouselScript(),
	)
}

// workflowDiscoverResults renders the browse-only result grid fragment (used by htmx swaps
// too). It reuses the SAME image parse (parseSearchImages), per-model "Updated X
// ago" info (newestVersionInfoByModel), carousel, and card body the model search
// uses — via modelCardCore with a nil action, so each card is link-only (name →
// /models/{id}) with NO subscribe/download/import control. mode is the NSFW
// display mode threaded to the carousels.
func workflowDiscoverResults(res *civitai.ModelSearchResult, mode, heading string) g.Node {
	if res == nil {
		return h.P(h.Class("text-sm text-slate-500"), g.Text("Search for workflows to discover on CivitAI."))
	}
	if len(res.Items) == 0 {
		return h.P(h.Class("text-sm text-slate-500"), g.Text("No workflows found."))
	}
	images := parseSearchImages(res.Raw)
	updated := newestVersionInfoByModel(res.Raw)
	grid := h.Div(
		h.Class("grid gap-4 sm:grid-cols-2 lg:grid-cols-3"),
		g.Map(res.Items, func(it civitai.ModelListItem) g.Node {
			// nil action → browse-only card (no state-changing controls).
			return modelCardCore(it, images[it.ID], mode, updated[it.ID], nil)
		}),
	)
	if heading == "" {
		return grid
	}
	return h.Div(sectionTitle(heading), grid)
}
