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
	Mode    maturityRange // the app-wide PG..XXX maturity band
	CSRF    string
	Heading string // section heading above the grid ("" = none)
	Res     *civitai.ModelSearchResult
	// Imported maps a card's civitai model id → how many workflows the local
	// library already holds from it (>0 flips that card's action from Import to
	// View). Built by ONE batched query for the whole page — never per card, see
	// Server.importedWorkflowsFn.
	Imported map[int]int
}

// handleDiscoverWorkflows backs the "Discover workflows" page: the model search
// pinned to types=Workflows, plus URL-addressable ECOSYSTEM (base-model family)
// and USE CASE (curated tag) facets.
//
// It is GET-only and read-only apart from the per-card import control (which is
// its own CSRF-protected POST endpoint).
//
// Egress: a keyword search and a facet browse both send a request to civitai.com.
// The ENTRY view (no query, no facet) issues NOTHING per facet — the chip row is
// built from the curated static table — and its popular feed comes from the TTL
// cache, so idle loads of the entry page do not hit the network.
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
		Mode:   s.maturity(),
		CSRF:   s.csrf,
		Facets: normalizeWorkflowFacets(q0),
	}
	v.Period = normalizeSearchPeriod(q0.Get("period"), discoverDefaultPeriod(query))

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

	// "Already imported?" for the whole page in ONE batched query, before either
	// render path — the fragment and the full page draw the same grid.
	if v.Res != nil {
		v.Imported = s.importedWorkflowModels(r.Context(), modelIDsOf(v.Res.Items))
	}

	if isHX {
		s.render(w, http.StatusOK, workflowDiscoverResults(v))
		return
	}
	s.render(w, http.StatusOK, workflowDiscoverPage(v, s.rail(r.Context())))
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
func workflowDiscoverPage(v workflowDiscoverView, rail ...railData) g.Node {
	return page("Discover workflows", v.CSRF, v.Mode, railOf(rail),
		browseSurface(browseSurfaceSpec{
			Title: "Discover workflows",
			// ⚠ EGRESS DISCLOSURE — READ BEFORE EDITING EITHER HALF.
			// "Your search is sent to civitai.com" is now the ONLY egress statement
			// on this surface. The import half — "Importing downloads the workflow
			// zip with your token and stores each workflow locally." — was removed
			// here by explicit request, and workflowImportAction deliberately
			// carries no note of its own (per-card it was noise on a grid of cards,
			// see its comment block). Consequence, accepted knowingly:
			// /workflows/discover no longer discloses the IMPORT egress anywhere.
			// The model DETAIL page's related-workflows section still carries the
			// sentence, and it is that surface's only egress statement — do not
			// remove it as "duplicate copy".
			Blurb: "Browse ComfyUI workflows on CivitAI by ecosystem, use case, or keyword. Your search is sent to civitai.com.",
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
// the result grid or the appropriate empty state. Cards reuse modelCardCore
// exactly as before, with the import action and no subscribe/download control.
//
// The chip row is the ONLY browse-by-facet control — the duplicate card of facet
// tiles that used to sit between it and the grid on the entry view is gone.
func workflowDiscoverResults(v workflowDiscoverView) g.Node {
	return g.Group([]g.Node{
		workflowFacetChips(v),
		workflowDiscoverGrid(v),
	})
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
			return modelCardCore(it, images[it.ID], v.Mode, updated[it.ID],
				workflowImportOrView(it.ID, v.CSRF, v.Imported[it.ID]))
		}),
	)
	if v.Heading == "" {
		return grid
	}
	return h.Div(sectionTitle(v.Heading), grid)
}
