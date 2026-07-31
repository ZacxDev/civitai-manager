package web

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
	g "maragu.dev/gomponents"
	h "maragu.dev/gomponents/html"
)

// ===========================================================================
// "Workflows for this model" — the REMOTE half of the model<->workflow linkage.
// ===========================================================================
//
// It answers "what ComfyUI workflows on CivitAI could I run this model in?" by
// reusing the Discover-workflows machinery whole: the same audited param builder
// (workflowSearchParams), the same tag fan-out + merge (fetchWorkflowFacetResults),
// the same TTL cache (facetFeed) and the same card renderer + import action
// (modelCardCore / workflowImportAction). There is deliberately no second
// implementation of any of that — every documented CivitAI trap is handled in
// exactly one place.
//
// ---------------------------------------------------------------------------
// THE QUERY, AND WHY IT IS THIS QUERY
// ---------------------------------------------------------------------------
// Live-verified against the real API on 2026-07-30 (see the test file for the
// exact curls and their results):
//
//	types=Workflows            PLURAL. Singular `type=` is silently ignored —
//	                           re-probed: `type=Workflows` returned five
//	                           CHECKPOINTS, not workflows.
//	baseModels=<A>&baseModels=<B>…
//	                           REPEATED params, one per base model in the
//	                           model's ecosystem — their UNION. Re-probed: the
//	                           comma-joined form returned HTTP 200 with items:[],
//	                           i.e. a silently EMPTY page.
//	tag=<one whitelisted tag>  ONE per request, fanned out over the use case's
//	                           synonyms and merged/deduped by model id. Re-probed:
//	                           repeated `tag=` is HTTP 400, and the three
//	                           `inpaint` synonyms returned three DIFFERENT sets —
//	                           which is exactly why the fan-out exists.
//	sort=Most Downloaded, period=AllTime, nsfw=<display mode>
//
// THE SIGNAL IS THE ECOSYSTEM, and it is REQUIRED. A model's versions carry
// CivitAI `baseModel` strings; those map through the curated table
// (civitai.EcosystemsForBaseModels) to a family, and a workflow built for that
// family is genuinely runnable with this model. If NO ecosystem resolves, this
// section renders NOTHING — an unfiltered `types=Workflows` feed is the popular
// feed, and presenting that as "workflows for this model" would be a section
// that lies.
//
// THE MODEL NAME IS DELIBERATELY NOT USED as a free-text `query=`. Two reasons,
// both concrete: (a) a model name is arbitrary untrusted text ("wai NSFW
// illustrious v14") and matches workflow titles essentially never; (b) CivitAI
// applies `period` as a POST-FILTER over an already-paged keyword result set, so
// `query=` + a period returns under-filled or empty pages while nextCursor keeps
// advancing (documented in CLAUDE.md). Sending no query sidesteps that entirely.
//
// TAGS ARE NEVER INVENTED AND NEVER FORWARDED RAW. The model's own tags are
// untrusted CivitAI text; they are mapped through civitai.UseCasesForTags, which
// only matches the curated vocabulary, and what goes on the wire is
// UseCase.QueryTags() — table values. This matters more than it looks: an
// UNKNOWN tag is not rejected by CivitAI, it is SILENTLY DROPPED and the
// UNFILTERED feed comes back, so a raw tag would render a filter that is lying.

// relatedWorkflowsSort / relatedWorkflowsPeriod are the fixed sort/period of this
// section.
//
// "Most Downloaded" over "AllTime" is chosen for RECALL, and both parts matter:
// the section browses a possibly-niche ecosystem with no keyword, so a narrow
// period (Day/Week) would routinely empty it for no reason, and AllTime is a
// valid enum member (`period` is the one filter that fails LOUDLY — anything
// outside Day|Week|Month|Year|AllTime is an HTTP 400).
//
// They are CONSTANTS rather than user controls on purpose: this is a fixed
// cross-reference on someone else's page, and the full sort/period/facet controls
// already exist one click away on /workflows/discover.
const (
	relatedWorkflowsSort   = discoverSortDefault
	relatedWorkflowsPeriod = "AllTime"
)

// relatedWorkflowsCap is how many cards the section shows. It is a SECTION on a
// model page, not a search results page — the "Browse more" link hands off to
// /workflows/discover with the same facets for the full grid.
const relatedWorkflowsCap = 6

// selectedVersionBaseModel returns the `baseModel` string of the version the page
// is CURRENTLY SHOWING — one version, never a union.
//
// The fallback is the PRIMARY version, m.ModelVersions[0]. That is not "the
// newest": modelVersions[] is ordered by the creator's `index`, so [0] is the
// featured/primary version and is exactly what the detail page renders when no
// ?version= is given (see loadModelView). Reaching it means the selection could
// not be matched at all (id 0, or a ?version= belonging to another model), and
// falling back to what the page would otherwise be showing keeps the heading and
// the grid describing the same version.
func selectedVersionBaseModel(m *civitai.ModelDetail, selectedVersionID int) string {
	for _, v := range m.ModelVersions {
		if v.ID == selectedVersionID {
			return strings.TrimSpace(v.BaseModel)
		}
	}
	if len(m.ModelVersions) > 0 {
		return strings.TrimSpace(m.ModelVersions[0].BaseModel)
	}
	return ""
}

// modelWorkflowFacets derives the WHITELISTED facet selection for the SELECTED
// VERSION of a model.
//
//   - Eco: from the SELECTED version's `baseModel` string through the curated
//     table. Exactly one base model is consulted, so the answer is the ecosystem
//     of the thing the user is looking at.
//   - Use: from the model's own tags, but only insofar as they hit the curated
//     use-case vocabulary; the first match in table order. No match simply means
//     no tag is sent (one broader request), never a guessed one. Tags are a
//     MODEL-level fact on CivitAI (there is no per-version tag list), so this half
//     is genuinely version-independent.
//
// WHY NOT THE UNION OF ALL VERSIONS. It used to be
// EcosystemsForBaseModels(every version's baseModel) with ecos[0] taken in table
// order, which is a MAJORITY/table-order vote that ignores the selection
// entirely. Live case that broke (v0.1.87, /models/573152): LUSTIFY! has 17
// versions — the newest, 3112728, is "Krea 2" and the other 16 are SDXL 1.0 /
// SDXL Lightning. With 3112728 selected the section still read "Workflows for
// SDXL family" and fetched SDXL workflows, i.e. it described a version the user
// was not looking at.
//
// Returns the zero facets when no ecosystem resolves — the caller MUST treat that
// as "render nothing" rather than as "no filters". This is the case that matters
// most: an unfiltered types=Workflows feed IS the popular feed, so a heading over
// it would lie.
func modelWorkflowFacets(m *civitai.ModelDetail, selectedVersionID int) workflowFacets {
	if m == nil {
		return workflowFacets{}
	}
	// EcosystemsForBaseModel is per-BASE-MODEL and the table is a partition
	// (TestEcosystemBaseModelsArePartitioned), so this is at most one entry.
	ecos := civitai.EcosystemsForBaseModel(selectedVersionBaseModel(m, selectedVersionID))
	if len(ecos) == 0 {
		return workflowFacets{}
	}
	f := workflowFacets{Eco: &ecos[0]}
	if uses := civitai.UseCasesForTags(m.Tags); len(uses) > 0 {
		f.Use = &uses[0]
	}
	return f
}

// relatedWorkflowsPath builds the lazy fragment's URL. It carries only the
// resolved facet SLUGS — curated table values, never model text — and the handler
// re-validates them through the same whitelist anyway, so a hand-edited URL can
// reach the outbound request with nothing the table does not contain.
func relatedWorkflowsPath(modelID int, f workflowFacets) string {
	q := url.Values{}
	if s := f.ecoSlug(); s != "" {
		q.Set("eco", s)
	}
	if s := f.useSlug(); s != "" {
		q.Set("use", s)
	}
	return fmt.Sprintf("/models/%d/related-workflows?%s", modelID, q.Encode())
}

// relatedWorkflowsID is the stable container id. It is stable because a version
// tab click re-targets it OUT OF BAND (see relatedWorkflowsOOB).
const relatedWorkflowsID = "related-workflows"

// relatedWorkflowsCard is the model page's placeholder for the section.
//
// It is LAZY (hx-trigger=revealed), exactly like the community feed and for the
// same reason: a cache miss costs up to civitai.MaxUseCaseTagQueries outbound
// requests, and a model page must not block on civitai.com to paint. That also
// makes the fail-soft requirement structural rather than a promise — the worst
// case is a fragment that renders nothing, inside a page that already painted.
//
// WHEN THE SELECTED VERSION RESOLVES TO NO ECOSYSTEM the container is rendered
// EMPTY and `hidden`, so the section shows nothing — no heading, no placeholder
// and, because there is no hx-get, no request either. It is still emitted for two
// reasons: it is the OOB target a later version switch needs to be able to find,
// and the `hidden` ATTRIBUTE (not a class) is what keeps it free: <main> is
// `space-y-6`, whose generated rule is
// `.space-y-6 > :not([hidden]) ~ :not([hidden])`, so a `[hidden]` child is
// excluded from BOTH sides of the sibling combinator and contributes no margin.
// The surrounding section spacing is therefore byte-identical to rendering
// nothing at all.
func relatedWorkflowsCard(v modelDetailView) g.Node {
	return relatedWorkflowsContainer(v, false)
}

// relatedWorkflowsOOB is the same container marked for an OUT-OF-BAND swap. It
// rides along with the #version-region fragment on a version tab click so the
// section re-resolves to the newly selected version WITHOUT moving in the DOM.
//
// Why OOB and not "put it inside #version-region": the section sits below the
// version region on the page, and moving it into the swapped container would
// reorder the page's sections. hx-swap-oob replaces the element in place.
//
// A same-ecosystem switch (e.g. between two of LUSTIFY!'s 16 SDXL versions)
// produces the SAME hx-get URL, so the refetched fragment is answered from
// facetFeed's TTL cache — ZERO outbound civitai requests. Only a switch that
// actually changes the ecosystem misses that cache.
func relatedWorkflowsOOB(v modelDetailView) g.Node {
	return relatedWorkflowsContainer(v, true)
}

func relatedWorkflowsContainer(v modelDetailView, oob bool) g.Node {
	attrs := []g.Node{h.ID(relatedWorkflowsID)}
	if oob {
		attrs = append(attrs, hx("swap-oob", "true"))
	}
	m := v.Model
	var f workflowFacets
	if m != nil {
		f = modelWorkflowFacets(m, v.SelectedVersionID)
	}
	if f.Eco == nil {
		// No ecosystem for the SELECTED version → render nothing, fetch nothing.
		return h.Div(append(attrs, g.Attr("hidden"))...)
	}
	return h.Div(append(attrs,
		hx("get", relatedWorkflowsPath(m.ID, f)),
		hx("trigger", "revealed"),
		hx("swap", "innerHTML"),
	)...)
}

// relatedWorkflowsHeading names the ACTUAL filter that produced the grid, so the
// section can never be a filter the user cannot see ("Workflows for Flux.1 ·
// Inpainting"). Both parts are curated table labels.
func relatedWorkflowsHeading(f workflowFacets) string {
	head := "Workflows for " + f.Eco.Label
	if f.Use != nil {
		head += " · " + f.Use.Label
	}
	return head
}

// relatedWorkflowsAbsent is what EVERY fail-soft path renders: nothing at all.
// It mirrors communityFeedAbsent — s.render calls Render on the node it is
// handed, so a bare nil g.Node would panic instead of degrading.
func relatedWorkflowsAbsent() g.Node { return g.Text("") }

// handleModelRelatedWorkflows serves the lazy fragment.
//
// GET, read-only, no CSRF (the only state-changing control inside is the import
// button, which carries its own token to its own audited endpoint).
//
// FAIL-SOFT at every step: a bad id, a facet that is not in the curated table, a
// fetch error and an empty result all render EMPTY (nil) rather than an error
// node. This is a cross-reference decorating someone else's page — it degrades to
// absence, never to a broken model page and never to a misleading grid.
func (s *Server) handleModelRelatedWorkflows(w http.ResponseWriter, r *http.Request) {
	modelID, err := strconv.Atoi(r.PathValue("id"))
	if err != nil || modelID <= 0 {
		s.render(w, http.StatusOK, relatedWorkflowsAbsent())
		return
	}
	// Re-validate the facets against the curated table. normalizeWorkflowFacets
	// DROPS anything unknown, so this is the whitelist gate: nothing from the URL
	// can reach civitai.com except a table value.
	f := normalizeWorkflowFacets(r.URL.Query())
	if f.Eco == nil {
		// No ecosystem → the request would be the unfiltered popular feed. Render
		// nothing rather than a section pretending to be about this model.
		s.render(w, http.StatusOK, relatedWorkflowsAbsent())
		return
	}

	// facetFeed is the SHARED TTL cache the Discover page uses (keyed by
	// nsfw|sort|period|eco|use), so browsing several models of the same ecosystem
	// costs one fetch, and it returns nil on any error.
	res := s.facetFeed(r.Context(), s.nsfwSearchFlag(), relatedWorkflowsSort, relatedWorkflowsPeriod, f)
	if res == nil {
		s.render(w, http.StatusOK, relatedWorkflowsAbsent())
		return
	}
	s.render(w, http.StatusOK, relatedWorkflowsResults(modelID, f, res, s.nsfwMode(), s.csrf))
}

// relatedWorkflowsResults renders the fragment body: the heading, the capped card
// grid and the hand-off link. Nil when nothing is left to show.
//
// The model's OWN id is filtered out. It can only collide when the page itself is
// a Workflows-type model, and a "workflows for this model" grid whose first card
// is the page you are on is pure noise.
//
// Card contents are UNTRUSTED CivitAI metadata (names, descriptions, image URLs)
// and go through modelCardCore, which is the same renderer the audited
// /workflows/discover grid uses — including its sanitizer and NSFW handling. No
// field is interpolated here.
func relatedWorkflowsResults(modelID int, f workflowFacets, res *civitai.ModelSearchResult, mode, csrf string) g.Node {
	items := make([]civitai.ModelListItem, 0, len(res.Items))
	for _, it := range res.Items {
		if it.ID == modelID {
			continue
		}
		items = append(items, it)
		if len(items) >= relatedWorkflowsCap {
			break
		}
	}
	if len(items) == 0 {
		// Quiet empty state. An empty first page is NOT proof there is nothing —
		// CivitAI's period post-filter can under-fill a page — so this says nothing
		// about the ecosystem, it just shows nothing.
		return relatedWorkflowsAbsent()
	}

	// Both maps are keyed by model id and read from the SAME raw body the cards
	// come from; extra ids left over from the self-filter are simply never looked up.
	images := parseSearchImages(res.Raw)
	updated := newestVersionInfoByModel(res.Raw)

	return card(
		h.Div(h.Class("mb-2 flex flex-wrap items-center justify-between gap-2"),
			h.H2(h.Class("text-sm font-semibold text-slate-300"),
				g.Text(relatedWorkflowsHeading(f))),
			h.A(
				h.Href(discoverHref("", relatedWorkflowsSort, relatedWorkflowsPeriod, f.ecoSlug(), f.useSlug())),
				h.Class("text-xs text-indigo-400 hover:text-indigo-300"),
				g.Text("Browse more →"),
			),
		),
		h.P(h.Class("mb-3 text-xs text-slate-400"),
			g.Text("ComfyUI workflows on CivitAI built for this model's base-model family. "+
				"Importing downloads the workflow zip with your token and stores each workflow locally.")),
		h.Div(h.Class("cm-cardgrid"),
			g.Map(items, func(it civitai.ModelListItem) g.Node {
				return modelCardCore(it, images[it.ID], mode, updated[it.ID], workflowImportAction(it.ID, csrf))
			}),
		),
	)
}
