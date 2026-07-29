package web

import (
	"net/http"
	"strings"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
	g "maragu.dev/gomponents"
	h "maragu.dev/gomponents/html"
)

// workflowDiscoverView is everything one /workflows/discover render needs. It is
// threaded whole (instead of a growing positional argument list) because the
// facet chips, the landing grid and the empty state all need the SAME
// query/sort/period/facet tuple to build their links — a chip that dropped the
// current sort would silently reset the view.
type workflowDiscoverView struct {
	Query   string
	Sort    string
	Period  string
	Facets  workflowFacets
	Mode    string // NSFW display mode
	CSRF    string
	Heading string // section heading above the grid ("" = none)
	// Landing is true for the entry-point view (no query, no facet): the curated
	// browse-by grid renders above the popular feed.
	Landing bool
	Res     *civitai.ModelSearchResult
}

// handleDiscoverWorkflows backs the "Discover workflows" page: the model search
// pinned to types=Workflows, plus URL-addressable ECOSYSTEM (base-model family)
// and USE CASE (curated tag) facets.
//
// It is GET-only and read-only apart from the per-card import control (which is
// its own CSRF-protected POST endpoint).
//
// Egress: a keyword search and a facet browse both send a request to civitai.com.
// The LANDING view (no query, no facet) issues NO per-tile requests at all — the
// browse grid is the curated static table — and its popular feed comes from the
// TTL cache, so idle loads of the entry page do not hit the network.
//
// Request budget per render: 1 (no use case) or up to
// civitai.MaxUseCaseTagQueries (a use case fans out over its tag synonyms
// because CivitAI's `tag` param takes exactly one value — see taxonomy.go).
func (s *Server) handleDiscoverWorkflows(w http.ResponseWriter, r *http.Request) {
	q0 := r.URL.Query()
	query := strings.TrimSpace(q0.Get("q"))
	isHX := r.Header.Get("HX-Request") == "true"
	nsfw := s.nsfwSearchFlag()

	v := workflowDiscoverView{
		Query:  query,
		Sort:   normalizeSearchSort(q0.Get("sort")),
		Mode:   s.nsfwMode(),
		CSRF:   s.csrf,
		Facets: normalizeWorkflowFacets(q0),
	}
	v.Period = normalizeSearchPeriod(q0.Get("period"), discoverDefaultPeriod(query))
	v.Landing = query == "" && !v.Facets.any()

	if query == "" {
		// A BROWSE: the landing feed or a facet feed. Both go through the TTL
		// cache, so clicking around the facets does not hammer civitai.com.
		v.Res = s.facetFeed(r.Context(), nsfw, v.Sort, v.Period, v.Facets)
		if v.Res != nil {
			v.Heading = discoverFeedHeading(v.Sort, v.Period, v.Facets)
		}
	} else {
		res, err := s.fetchWorkflowFacetResults(r.Context(), query, v.Sort, v.Period, nsfw, v.Facets)
		if err != nil {
			if isHX {
				s.render(w, http.StatusOK, errorNote("Search failed: "+err.Error()))
				return
			}
			// Full-page degrade: render the page with no results (the prompt), as
			// before — an error is not an empty result and must not be dressed up
			// as "widen your search".
			v.Res = nil
		} else {
			v.Res = res
		}
	}

	if isHX {
		s.render(w, http.StatusOK, workflowDiscoverResults(v))
		return
	}
	s.render(w, http.StatusOK, workflowDiscoverPage(v, s.currentTheme(), s.rail(r.Context())))
}

// discoverFeedHeading labels a no-query browse feed. The unfaceted default keeps
// its historical "Popular this month" wording; a facet view is prefixed with what
// is selected ("Flux.1 · Inpainting · Popular this month") so the grid is never
// ambiguous about which filter produced it.
func discoverFeedHeading(sort, period string, f workflowFacets) string {
	base := "Popular this month"
	if !(sort == discoverSortDefault && period == "Month") {
		base = searchHeadingFor(sort, period)
	}
	if s := f.summary(); s != "" {
		return s + " · " + base
	}
	return base
}

// workflowDiscoverPage renders the full page: the search form (query +
// sort/period, wired to GET /workflows/discover) and the results container. The
// facet chips live INSIDE the results container so an htmx search re-renders them
// and a chip href can never go stale against the query in the box.
func workflowDiscoverPage(v workflowDiscoverView, theme string, rail ...railData) g.Node {
	return page("Discover workflows", theme, v.CSRF, v.Mode, railOf(rail),
		browseSurface(browseSurfaceSpec{
			Title: "Discover workflows",
			Blurb: "Browse ComfyUI workflows on CivitAI by ecosystem, use case, or keyword. Your search is sent to civitai.com. Importing downloads the workflow zip with your token and stores each workflow locally.",
			Controls: browseFilterForm("/workflows/discover", "discover-results", "discover",
				v.Query, "Search workflows by name, tag, …", v.Sort, v.Period,
				// Carry the selected facets through a sort/period/query change, so
				// changing the sort of a faceted view does not silently clear it.
				facetHiddenInputs(v.Facets)),
			ResultsID: "discover-results",
			Results:   workflowDiscoverResults(v),
			Foot:      []g.Node{lightboxOverlay(), modelPageScript(), libraryCarouselScript()},
		}),
	)
}

// facetHiddenInputs emits the current facet selection as hidden form fields, so
// the search form's htmx GET preserves the facets. Values are table slugs, never
// user text.
func facetHiddenInputs(f workflowFacets) g.Node {
	var nodes []g.Node
	if s := f.ecoSlug(); s != "" {
		nodes = append(nodes, h.Input(h.Type("hidden"), h.Name("eco"), h.Value(s)))
	}
	if s := f.useSlug(); s != "" {
		nodes = append(nodes, h.Input(h.Type("hidden"), h.Name("use"), h.Value(s)))
	}
	return g.Group(nodes)
}

// workflowDiscoverResults renders the swappable fragment: the facet chips, then
// the landing browse-grid (entry point only), then the result grid or the
// appropriate empty state. Cards reuse modelCardCore exactly as before, with the
// import action and no subscribe/download control.
func workflowDiscoverResults(v workflowDiscoverView) g.Node {
	body := []g.Node{workflowFacetChips(v)}
	if v.Landing {
		body = append(body, workflowBrowseLanding(v))
	}
	body = append(body, workflowDiscoverGrid(v))
	return g.Group(body)
}

// workflowDiscoverGrid is the result region alone: the card grid, or one of three
// distinct empty states — an unfaceted prompt, the bare "no match", and the
// guided facet empty state that names the selection and offers a way out.
func workflowDiscoverGrid(v workflowDiscoverView) g.Node {
	if v.Res == nil {
		// No fetch happened, or it failed. Never claim "nothing matched".
		return h.P(h.Class("text-sm text-slate-500"), g.Text("Search for workflows to discover on CivitAI."))
	}
	if len(v.Res.Items) == 0 {
		if v.Facets.any() {
			return workflowFacetEmptyState(v)
		}
		return h.P(h.Class("text-sm text-slate-500"), g.Text("No workflows found."))
	}
	images := parseSearchImages(v.Res.Raw)
	updated := newestVersionInfoByModel(v.Res.Raw)
	grid := h.Div(
		h.Class("cm-cardgrid"),
		g.Map(v.Res.Items, func(it civitai.ModelListItem) g.Node {
			return modelCardCore(it, images[it.ID], v.Mode, updated[it.ID], workflowImportAction(it.ID, v.CSRF))
		}),
	)
	if v.Heading == "" {
		return grid
	}
	return h.Div(sectionTitle(v.Heading), grid)
}
