package web

import (
	"strconv"
	"strings"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
	g "maragu.dev/gomponents"
	h "maragu.dev/gomponents/html"
)

// This file renders the browse-by-facet UI on /workflows/discover: the curated
// landing grid shown as the entry point, the URL-addressable facet chips, and the
// guided empty state. The vocabulary always comes from internal/civitai's curated
// table — nothing here hard-codes a base model or a tag.

// ecosystemKindLabel names the landing-grid section a Kind renders under. Video
// families stay SEPARATE ecosystems (a Wan 2.2 graph and an LTXV graph share
// nothing operationally) but read as one group here.
func ecosystemKindLabel(kind string) string {
	switch kind {
	case civitai.EcosystemKindImage:
		return "Image models"
	case civitai.EcosystemKindVideo:
		return "Video models"
	case civitai.EcosystemKindAudio:
		return "Audio models"
	default:
		return "Other"
	}
}

// ecosystemKindOrder is the landing-grid section order.
var ecosystemKindOrder = []string{
	civitai.EcosystemKindImage,
	civitai.EcosystemKindVideo,
	civitai.EcosystemKindAudio,
	civitai.EcosystemKindOther,
}

// workflowBrowseLanding is the entry point shown for an empty query with no facet
// selected: the curated ecosystem grid (grouped by kind) and the use-case grid.
//
// It issues ZERO upstream requests — every tile is a static table row linking to
// its own facet URL. That is the whole point: a landing page that fetched a
// preview feed per tile would fire ~30 requests at civitai.com on every render.
func workflowBrowseLanding(v workflowDiscoverView) g.Node {
	var sections []g.Node
	sections = append(sections, h.H2(h.Class("text-sm font-semibold text-slate-200 mb-2"),
		g.Text("Browse by ecosystem")))

	byKind := map[string][]civitai.Ecosystem{}
	for _, e := range civitai.Ecosystems() {
		byKind[e.Kind] = append(byKind[e.Kind], e)
	}
	for _, kind := range ecosystemKindOrder {
		es := byKind[kind]
		if len(es) == 0 {
			continue
		}
		tiles := make([]g.Node, 0, len(es))
		for _, e := range es {
			tiles = append(tiles, facetTile(
				discoverFacetHref(v.Query, v.Sort, v.Period, v.Facets, "eco", e.Slug),
				e.Label, baseModelHint(e)))
		}
		sections = append(sections,
			h.H3(h.Class("text-xs uppercase tracking-wide text-slate-500 mt-3 mb-1"),
				g.Text(ecosystemKindLabel(kind))),
			h.Div(h.Class("cm-facet-grid"), g.Group(tiles)),
		)
	}

	ucs := civitai.UseCases()
	tiles := make([]g.Node, 0, len(ucs))
	for _, u := range ucs {
		tiles = append(tiles, facetTile(
			discoverFacetHref(v.Query, v.Sort, v.Period, v.Facets, "use", u.Slug),
			u.Label, strings.Join(u.QueryTags(), ", ")))
	}
	sections = append(sections,
		h.H2(h.Class("text-sm font-semibold text-slate-200 mt-5 mb-2"), g.Text("Browse by use case")),
		h.Div(h.Class("cm-facet-grid"), g.Group(tiles)),
	)

	return card(
		g.Group(sections),
		h.P(h.Class("text-xs text-slate-500 mt-3"),
			g.Text("Use cases come from the workflow's CivitAI tags. Tags like \"tool\" and \"comfyui\" are on nearly every workflow, so they are ignored.")),
	)
}

// baseModelHint is the tile's subtitle: the base models the family covers, so the
// grouping is legible rather than a mystery label. Truncated for the wide families.
func baseModelHint(e civitai.Ecosystem) string {
	const max = 3
	if len(e.BaseModels) <= max {
		return strings.Join(e.BaseModels, ", ")
	}
	return strings.Join(e.BaseModels[:max], ", ") + ", +" +
		strconv.Itoa(len(e.BaseModels)-max) + " more"
}

// facetTile is one clickable landing tile. It is a plain <a> (full navigation),
// so the URL — and therefore the shareability of the resulting view — is always
// correct without any htmx history juggling.
func facetTile(href, label, hint string) g.Node {
	return h.A(
		h.Href(href),
		h.Class("cm-facet-tile block rounded-md border p-2"),
		h.Div(h.Class("truncate text-sm font-medium text-slate-200"), g.Text(label)),
		g.If(hint != "", h.Div(h.Class("truncate text-xs text-slate-500"), h.Title(hint), g.Text(hint))),
	)
}

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
	case "Month":
		return "this month"
	case "Week":
		return "this week"
	}
	return ""
}
