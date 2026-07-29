package web

import (
	g "maragu.dev/gomponents"
	h "maragu.dev/gomponents/html"
)

// browseSurface is the ONE shape every browse surface uses — /search,
// /workflows/discover, and the Library "Workflows" tab.
//
// All three used to render their FILTER CONTROLS in one card() and their RESULTS
// in a separate block below it, so the user read a seam between "the thing I am
// filtering with" and "the thing it filters". They also each rolled their own
// near-copy of that layout, which is why they had drifted apart (a blurb on one, a
// different heading level on another, chips above the card on the third).
//
// browseSurface is a SINGLE panel: heading → blurb → controls → a hairline rule →
// the results, which flow on inside the same surface. The three pages now differ
// only in what they put in each slot.
//
// It is a layout helper and nothing else. The controls, the htmx wiring and the
// results markup are supplied by the caller unchanged, so no behaviour — facet
// chips, sort/period, NSFW handling, swap targets, poll containers — moves here.
type browseSurfaceSpec struct {
	// Title is the page's single <h1>.
	Title string
	// Blurb is the optional explanatory line under it (egress notices live here).
	Blurb string
	// Controls is the filter form. It may be nil for a surface with none.
	Controls g.Node
	// Aside is an optional control rendered on the trailing edge of the head row —
	// an action that is NOT a filter (the Library tab's "Add a workflow").
	Aside g.Node
	// Notice is an optional alert/flash rendered between the controls and the
	// results, INSIDE the surface.
	Notice g.Node
	// ResultsID is the STABLE htmx swap-target id of the results region. It is the
	// element callers already poll into / swap innerHTML on — browseSurface only
	// wraps it, and never replaces it, so the streaming-job invariant is preserved.
	ResultsID string
	// Results is the initial content of that container.
	Results g.Node
	// Foot is optional content BELOW the surface (lightbox overlay, scripts).
	Foot []g.Node
}

// browseSurface renders the spec. The results container keeps the caller's exact
// id so every existing hx-target/poller keeps working.
func browseSurface(s browseSurfaceSpec) g.Node {
	head := []g.Node{h.Class("cm-browse-head")}
	titleRow := []g.Node{h.Class("cm-browse-titlerow")}
	titleCol := []g.Node{h.Class("min-w-0")}
	if s.Title != "" {
		titleCol = append(titleCol, pageTitle(s.Title))
	}
	if s.Blurb != "" {
		titleCol = append(titleCol, h.P(h.Class("text-sm text-slate-400"), g.Text(s.Blurb)))
	}
	titleRow = append(titleRow, h.Div(titleCol...))
	if s.Aside != nil {
		titleRow = append(titleRow, h.Div(h.Class("shrink-0"), s.Aside))
	}
	head = append(head, h.Div(titleRow...))
	if s.Controls != nil {
		head = append(head, h.Div(h.Class("cm-browse-controls"), s.Controls))
	}

	// The results flow on as direct children of the SAME card — that is the whole
	// point of the surface, so there is no wrapper element (and no class) between
	// the rule and the results.
	panel := []g.Node{h.Div(head...)}
	if s.Notice != nil {
		panel = append(panel, h.Div(h.Class("mb-4"), s.Notice))
	}
	panel = append(panel, h.Div(h.ID(s.ResultsID), s.Results))

	nodes := []g.Node{card(panel...)}
	nodes = append(nodes, s.Foot...)
	return g.Group(nodes)
}

// browseFilterForm is the shared shape of a browse surface's filter row: a wide
// query box, the sort/period selects, any hidden state the surface must carry
// through a submit, and the Search button — one flex row that wraps.
//
// The three surfaces used to build this inline, three times, which is how they
// drifted (different label text, different min-widths, one missing the hidden
// facet inputs). idPrefix namespaces the control ids so two surfaces could coexist
// on one page without colliding.
func browseFilterForm(action, target, idPrefix, query, placeholder, sortSel, periodSel string, extra ...g.Node) g.Node {
	nodes := []g.Node{
		h.Class("flex flex-wrap items-end gap-3"),
		hx("get", action),
		hx("target", "#"+target),
		hx("swap", "innerHTML"),
		hx("trigger", "submit"),
		h.Div(
			h.Class("min-w-[12rem] flex-1"),
			textInput("text-input", idPrefix+"-q", "Query",
				h.Type("text"), h.Name("q"), h.Value(query),
				h.Placeholder(placeholder)),
		),
		// Sort + period filter dropdowns (GET params threaded into the civitai
		// query). Their values are the exact civitai query strings.
		labeledSelect(idPrefix+"-sort", "sort", "Sort", searchSortOptions, sortSel),
		labeledSelect(idPrefix+"-period", "period", "Period", searchPeriodOptions, periodSel),
	}
	nodes = append(nodes, extra...)
	nodes = append(nodes, btnPrimary(g.Text("Search")))
	return h.Form(nodes...)
}
