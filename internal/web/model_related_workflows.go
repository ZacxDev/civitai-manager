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

// modelWorkflowFacets derives the WHITELISTED facet selection for a model.
//
//   - Eco: from the model's versions' `baseModel` strings through the curated
//     table. A model spanning several ecosystems (versions on different base
//     models) takes the FIRST in TABLE order — deterministic, and independent of
//     the order CivitAI happens to return versions in, which is the creator's
//     `index` and not anything we should key on.
//   - Use: from the model's own tags, but only insofar as they hit the curated
//     use-case vocabulary; the first match in table order. No match simply means
//     no tag is sent (one broader request), never a guessed one.
//
// Returns the zero facets when no ecosystem resolves — the caller MUST treat that
// as "render nothing" rather than as "no filters".
func modelWorkflowFacets(m *civitai.ModelDetail) workflowFacets {
	if m == nil {
		return workflowFacets{}
	}
	bases := make([]string, 0, len(m.ModelVersions))
	for _, v := range m.ModelVersions {
		if b := strings.TrimSpace(v.BaseModel); b != "" {
			bases = append(bases, b)
		}
	}
	ecos := civitai.EcosystemsForBaseModels(bases)
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

// relatedWorkflowsCard is the model page's placeholder for the section.
//
// It is LAZY (hx-trigger=revealed), exactly like the community feed and for the
// same reason: a cache miss costs up to civitai.MaxUseCaseTagQueries outbound
// requests, and a model page must not block on civitai.com to paint. That also
// makes the fail-soft requirement structural rather than a promise — the worst
// case is a fragment that renders nothing, inside a page that already painted.
//
// Returns nil when the model resolves to no ecosystem, so no heading, no
// placeholder and no request happen at all.
func relatedWorkflowsCard(v modelDetailView) g.Node {
	m := v.Model
	if m == nil {
		return nil
	}
	f := modelWorkflowFacets(m)
	if f.Eco == nil {
		return nil
	}
	return h.Div(
		h.ID("related-workflows"),
		hx("get", relatedWorkflowsPath(m.ID, f)),
		hx("trigger", "revealed"),
		hx("swap", "innerHTML"),
	)
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
