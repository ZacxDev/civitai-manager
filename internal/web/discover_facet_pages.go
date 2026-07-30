package web

import (
	"github.com/ZacxDev/civitai-manager/internal/civitai"
	g "maragu.dev/gomponents"
	h "maragu.dev/gomponents/html"
)

// This file renders the browse-by-facet UI on /workflows/discover: the
// URL-addressable facet chips and the guided empty state. The vocabulary always
// comes from internal/civitai's curated table — nothing here hard-codes a base
// model or a tag.
//
// There used to be a SECOND rendering of the same two facet dimensions above the
// chips: a card of ~30 clickable tiles ("Browse by ecosystem" / "Browse by use
// case"), shown on the entry view only. It offered no navigation the chip row does
// not, so the entry page listed every ecosystem and use case twice and pushed the
// actual results below the fold. The chip row is the one that stayed: it is
// present on EVERY view (not just the entry one), it shows the current selection,
// and it re-renders inside the htmx results container so its hrefs cannot go
// stale.

// workflowFacetChips renders both facet dimensions as URL-addressable chips
// alongside the existing sort/period controls. Each chip is a link to the same
// page with that one facet TOGGLED, so selecting, swapping and clearing a facet
// are all the same interaction and every state is a shareable URL.
//
// The chip row is rendered INSIDE the htmx results container on purpose: an htmx
// search re-renders it, so a chip's href can never go stale against the query
// currently in the box.
func workflowFacetChips(v workflowDiscoverView) g.Node {
	ecoChips := []g.Node{facetChip(
		discoverFacetHref(v.Query, v.Sort, v.Period, v.Facets, "eco", ""),
		"All ecosystems", v.Facets.Eco == nil)}
	for _, e := range civitai.Ecosystems() {
		on := v.Facets.Eco != nil && v.Facets.Eco.Slug == e.Slug
		ecoChips = append(ecoChips, facetChip(
			discoverFacetHref(v.Query, v.Sort, v.Period, v.Facets, "eco", e.Slug), e.Label, on))
	}
	useChips := []g.Node{facetChip(
		discoverFacetHref(v.Query, v.Sort, v.Period, v.Facets, "use", ""),
		"All use cases", v.Facets.Use == nil)}
	for _, u := range civitai.UseCases() {
		on := v.Facets.Use != nil && v.Facets.Use.Slug == u.Slug
		useChips = append(useChips, facetChip(
			discoverFacetHref(v.Query, v.Sort, v.Period, v.Facets, "use", u.Slug), u.Label, on))
	}
	return h.Div(
		h.Class("space-y-2 mb-4"),
		facetChipRow("Ecosystem", ecoChips),
		facetChipRow("Use case", useChips),
	)
}

func facetChipRow(label string, chips []g.Node) g.Node {
	return h.Div(
		h.Class("flex flex-wrap items-center gap-1"),
		h.Span(h.Class("mr-1 text-xs uppercase tracking-wide text-slate-500"), g.Text(label)),
		g.Group(chips),
	)
}

// facetChip is one toggle chip. Selected state is carried BOTH by the
// .cm-facet-chip-on class (visual, theme-aware in app.css) and by
// aria-pressed/aria-current, so it is not colour-only.
func facetChip(href, label string, selected bool) g.Node {
	class := "cm-chip cm-facet-chip inline-flex items-center gap-1 rounded-md border px-2 py-0.5 text-xs text-slate-200"
	attrs := []g.Node{h.Href(href)}
	if selected {
		class += " cm-facet-chip-on"
		attrs = append(attrs, g.Attr("aria-current", "true"))
	}
	attrs = append(attrs, h.Class(class), g.Text(label))
	return h.A(attrs...)
}

// workflowFacetEmptyState is the guided empty state for a facet view that
// returned nothing — the case a bare "No workflows found." leaves the user stuck
// in, with no idea which of their two filters is responsible.
//
// It names the exact selection, and offers the two real escapes: widen the time
// window (the usual cause — a narrow ecosystem × use case pair genuinely has no
// workflows THIS MONTH but plenty all-time) and clear the facets.
//
// It deliberately mirrors emptyState()'s markup and classes (layout.go) rather
// than calling it, because that helper takes exactly one CTA and the whole point
// here is offering the choice between widening and clearing.
func workflowFacetEmptyState(v workflowDiscoverView) g.Node {
	what := v.Facets.summary()
	heading := "No " + what + " workflows"
	if v.Query != "" {
		heading = "No " + what + " workflows matching that search"
	}
	if lbl := periodPhrase(v.Period); lbl != "" {
		heading += " " + lbl
	}

	var ctas []g.Node
	if v.Period != "AllTime" {
		// Widen the window, keeping the query, sort AND both facets — the escape
		// must not silently discard what the user was browsing.
		ctas = append(ctas, facetCTA(
			discoverHref(v.Query, v.Sort, "AllTime", v.Facets.ecoSlug(), v.Facets.useSlug()),
			"Search all time"))
	}
	if v.Facets.Use != nil && v.Facets.Eco != nil {
		// Two facets stacked is the usual over-narrowing; offer dropping each.
		ctas = append(ctas,
			facetCTA(discoverFacetHref(v.Query, v.Sort, v.Period, v.Facets, "use", v.Facets.Use.Slug),
				"Any use case"),
			facetCTA(discoverFacetHref(v.Query, v.Sort, v.Period, v.Facets, "eco", v.Facets.Eco.Slug),
				"Any ecosystem"))
	}
	ctas = append(ctas, facetCTA("/workflows/discover", "Clear all filters"))

	return h.Div(
		h.Class("py-6 text-center"),
		h.H3(h.Class("text-base font-semibold text-slate-200"), g.Text(heading)),
		h.P(h.Class("mx-auto mt-1 mb-3 max-w-md text-sm text-slate-400"),
			g.Text("Nothing on CivitAI matches this combination. Widen the time window or drop a filter.")),
		h.Div(h.Class("flex flex-wrap items-center justify-center gap-2"), g.Group(ctas)),
	)
}

// facetCTA is one empty-state action, styled as a civitai-ui button like the
// shared emptyState CTA.
func facetCTA(href, label string) g.Node {
	return h.A(
		h.Href(href),
		dataAttr("civitai-ui", "button"), dataAttr("variant", "filled"), dataAttr("size", "sm"),
		h.Span(h.Class("cm-cta-icon"), g.Attr("aria-hidden", "true"), g.Text("→ ")),
		g.Text(label),
	)
}

// periodPhrase renders a period as the trailing phrase in the empty-state
// heading ("this month"). AllTime gets no phrase — "No X workflows all time"
// reads badly and there is nothing to widen to.
func periodPhrase(period string) string {
	switch period {
	case "Day":
		return "today"
	case "Week":
		return "this week"
	case "Month":
		return "this month"
	case "Year":
		return "this year"
	}
	return ""
}
