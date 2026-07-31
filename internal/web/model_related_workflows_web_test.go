package web

import (
	"strings"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
)

// ===========================================================================
// "Workflows for this model" — the OUTBOUND REQUEST is what is under test.
// ===========================================================================
//
// Every assertion here corresponds to a REAL bug this repo has shipped or a real
// upstream behaviour that fails SILENTLY. Each was re-probed against the live
// https://civitai.com/api/v1/models on 2026-07-30 before these tests were written
// (a fake-reader test cannot discover any of them — it can only pin them):
//
//	types=Workflows&baseModels=Flux.1 D&…&limit=5&sort=Most Downloaded&period=AllTime
//	  → HTTP 200, 5 items, all type=Workflows (618578, 912123, 617060, …)
//	baseModels=Flux.1 D,Flux.1 S  (comma-joined)
//	  → HTTP 200, items: []            ← SILENTLY EMPTY, not an error
//	type=Workflows                (singular)
//	  → HTTP 200, types: [Checkpoint × 5]  ← SILENTLY UNFILTERED
//	tag=inpaint&tag=upscaler      (repeated)
//	  → HTTP 400
//	tag=inpaint / tag=inpainting / tag="inpaint fill"  (one request each)
//	  → 5 / 5 / 1 items with DIFFERENT ids — synonyms are NOT unified, which is
//	    exactly why the fan-out + merge exists.

// fluxModel is a model whose versions put it in the Flux.1 ecosystem and whose
// tags include one that maps to a curated use case ("inpainting" → Inpainting)
// alongside noise that must never be forwarded.
func fluxModel() *civitai.ModelDetail {
	return &civitai.ModelDetail{
		ID: 42, Name: "A Flux LoRA", Type: "LORA",
		// Raw CivitAI tags: a stopword, a use-case hit, and pure noise. Only the
		// curated mapping of the middle one may ever reach the wire.
		Tags: []string{"tool", "inpainting", "anime girl", "zzz-not-a-real-tag-xyz"},
		ModelVersions: []civitai.ModelVersionSummary{
			{ID: 421, Name: "v1", BaseModel: "Flux.1 D"},
			{ID: 422, Name: "v2", BaseModel: "Flux.1 Kontext"},
		},
	}
}

// relatedServer wires a server whose SearchModels calls are recorded.
func relatedServer(t *testing.T, reader *tagSearchReader) *Server {
	t.Helper()
	// GetModel must answer with the Flux model, so the PAGE-level derivation
	// (modelWorkflowFacets) is exercised too, not only the fragment handler.
	reader.fakeReader = fakeReader{model: fluxModel()}
	srv := newTestServer(t)
	srv.reader = reader
	return srv
}

// TestRelatedWorkflowsRequestUsesPluralTypes is mutation (a): flipping `types` to
// the singular `type` must fail here. Live-probed: singular returns CHECKPOINTS.
func TestRelatedWorkflowsRequestUsesPluralTypes(t *testing.T) {
	r := &tagSearchReader{byTag: map[string][]int{}, noTag: []int{1}}
	srv := relatedServer(t, r)

	rec := get(t, srv, "/models/42/related-workflows?eco=flux1")
	if rec.Code != 200 {
		t.Fatalf("fragment status = %d", rec.Code)
	}
	if len(r.calls) == 0 {
		t.Fatal("no outbound search was issued")
	}
	for i, q := range r.calls {
		if got := q.Get("types"); got != "Workflows" {
			t.Errorf("call %d: types = %q, want %q — the models API filters by the PLURAL "+
				"param; singular `type=` is SILENTLY IGNORED and returns unfiltered results "+
				"(live-probed: it returned five Checkpoints)", i, got, "Workflows")
		}
		if _, bad := q["type"]; bad {
			t.Errorf("call %d: the request carries a SINGULAR `type=%q` param — it is silently "+
				"ignored by CivitAI and its presence means the filter is not doing what it looks "+
				"like it is doing", i, q.Get("type"))
		}
		// `period` is the ONE filter that fails LOUDLY: anything outside the enum is
		// an HTTP 400, so a typo here would break the section outright.
		validPeriods := map[string]bool{"Day": true, "Week": true, "Month": true, "Year": true, "AllTime": true}
		if p := q.Get("period"); !validPeriods[p] {
			t.Errorf("call %d: period = %q is not in the strict enum "+
				"Day|Week|Month|Year|AllTime — CivitAI answers anything else with HTTP 400", i, p)
		}
		// No free-text query: the model NAME is a weak signal AND `period` is applied
		// as a post-filter over an already-paged keyword result set, so query+period
		// returns under-filled pages while nextCursor keeps advancing.
		if got := q.Get("query"); got != "" {
			t.Errorf("call %d: the request carries query=%q. The model name is not used as a "+
				"keyword — it matches workflow titles essentially never, and it interacts badly "+
				"with the period post-filter.", i, got)
		}
	}
}

// TestRelatedWorkflowsSendsBaseModelsAsRepeatedParams is mutation (b): joining
// the ecosystem's base models with commas must fail here. Live-probed: the
// comma-joined form returns HTTP 200 with an EMPTY item list — a silently blank
// section, not an error anyone would notice.
func TestRelatedWorkflowsSendsBaseModelsAsRepeatedParams(t *testing.T) {
	r := &tagSearchReader{byTag: map[string][]int{}, noTag: []int{1}}
	srv := relatedServer(t, r)

	get(t, srv, "/models/42/related-workflows?eco=flux1")
	if len(r.calls) == 0 {
		t.Fatal("no outbound search was issued")
	}
	eco, ok := civitai.EcosystemBySlug("flux1")
	if !ok {
		t.Fatal("the flux1 ecosystem vanished from the curated table")
	}
	for i, q := range r.calls {
		got := q["baseModels"]
		if len(got) != len(eco.BaseModels) {
			t.Errorf("call %d: got %d baseModels params %v, want %d REPEATED params (one per "+
				"base model in the ecosystem)", i, len(got), got, len(eco.BaseModels))
		}
		for _, v := range got {
			if strings.Contains(v, ",") {
				t.Errorf("call %d: baseModels value %q is COMMA-JOINED. CivitAI parses that as "+
					"ONE literal base-model name and returns HTTP 200 with items: [] — the page "+
					"goes silently empty. Use url.Values.Add (repeated params).", i, v)
			}
		}
		// The union must actually be the ecosystem's, not a truncation.
		want := map[string]bool{}
		for _, b := range eco.BaseModels {
			want[b] = true
		}
		for _, v := range got {
			if !want[v] {
				t.Errorf("call %d: baseModels carries %q, which is not in the flux1 ecosystem", i, v)
			}
		}
	}
}

// TestRelatedWorkflowsForwardsOnlyWhitelistedTags is mutation (c): forwarding the
// model's RAW tags must fail here.
//
// This is the most dangerous of the three, because it fails UPWARDS: an unknown
// tag is not rejected, the filter is silently DROPPED and the UNFILTERED feed
// comes back — so the section would render a filter that is lying rather than an
// obvious error.
func TestRelatedWorkflowsForwardsOnlyWhitelistedTags(t *testing.T) {
	r := &tagSearchReader{byTag: map[string][]int{}, noTag: []int{1}}
	srv := relatedServer(t, r)

	// ?use=inpaint is the whitelisted slug the model page derives from the model's
	// "inpainting" tag.
	get(t, srv, "/models/42/related-workflows?eco=flux1&use=inpaint")
	if len(r.calls) == 0 {
		t.Fatal("no outbound search was issued")
	}

	uc, ok := civitai.UseCaseBySlug("inpaint")
	if !ok {
		t.Fatal("the inpaint use case vanished from the curated table")
	}
	allowed := map[string]bool{}
	for _, tg := range uc.QueryTags() {
		allowed[tg] = true
	}

	for i, q := range r.calls {
		// SINGLE-VALUE. Repeated tag= is an HTTP 400 ZodError (live-probed).
		if n := len(q["tag"]); n > 1 {
			t.Errorf("call %d: %d `tag` params in ONE request — CivitAI answers that with "+
				"HTTP 400 (\"expected string, received array\")", i, n)
		}
		tg := q.Get("tag")
		if tg == "" {
			t.Errorf("call %d: a use-case fan-out request carries no tag at all", i)
			continue
		}
		if strings.Contains(tg, ",") {
			t.Errorf("call %d: tag %q is COMMA-JOINED — CivitAI returns results byte-identical "+
				"to sending no tag at all, i.e. the filter is silently dropped", i, tg)
		}
		if !allowed[tg] {
			t.Errorf("call %d: tag %q is NOT in the curated table (civitai.UseCase.QueryTags). "+
				"An unknown tag is SILENTLY DROPPED by CivitAI and the UNFILTERED feed comes "+
				"back, so the UI would render a filter that is lying. Only whitelisted tags "+
				"may be forwarded.", i, tg)
		}
		// The model's own raw tags must never appear on the wire.
		for _, raw := range fluxModel().Tags {
			if tg == raw && !allowed[raw] {
				t.Errorf("call %d: the model's RAW tag %q was forwarded verbatim", i, raw)
			}
		}
	}

	// --- the injection path: a HAND-EDITED ?use= ------------------------------
	// The handler re-validates against the curated table, so an unknown use case
	// must be DROPPED (one broader, tag-less request) — never forwarded as a tag.
	// Forwarding it would be the worst outcome available: CivitAI silently ignores
	// an unknown tag and returns the UNFILTERED feed, so the section would render
	// under a filter label that does not describe its contents.
	r2 := &tagSearchReader{byTag: map[string][]int{}, noTag: []int{1}}
	srv2 := relatedServer(t, r2)
	get(t, srv2, "/models/42/related-workflows?eco=flux1&use=zzz-not-a-real-tag-xyz")

	if len(r2.calls) != 1 {
		t.Fatalf("an unknown ?use= must collapse to ONE tag-less request, got %d: %v",
			len(r2.calls), r2.tags())
	}
	if tg := r2.calls[0].Get("tag"); tg != "" {
		t.Errorf("a hand-edited ?use= reached the wire as tag=%q. Only values resolved "+
			"through the curated table (civitai.UseCaseBySlug) may be forwarded — an "+
			"unknown tag is SILENTLY DROPPED by CivitAI and the UNFILTERED feed comes "+
			"back, which would render a filter that is lying.", tg)
	}
}

// TestRelatedWorkflowsFansOutOneRequestPerTagAndDedupes pins the whole fan-out
// contract: one request per whitelisted synonym, capped, merged and deduped by
// MODEL ID (live-probed: the three inpaint synonyms return overlapping but
// different sets).
func TestRelatedWorkflowsFansOutOneRequestPerTagAndDedupes(t *testing.T) {
	uc, _ := civitai.UseCaseBySlug("inpaint")
	tags := uc.QueryTags()
	if len(tags) < 2 {
		t.Fatalf("the inpaint use case needs >=2 synonyms to exercise the fan-out, got %v", tags)
	}
	// Overlapping sets, exactly like the live API: id 100 is returned by every
	// synonym and must appear ONCE.
	byTag := map[string][]int{}
	for i, tg := range tags {
		byTag[tg] = []int{100, 200 + i}
	}
	r := &tagSearchReader{byTag: byTag}
	srv := relatedServer(t, r)

	rec := get(t, srv, "/models/42/related-workflows?eco=flux1&use=inpaint")
	if rec.Code != 200 {
		t.Fatalf("fragment status = %d", rec.Code)
	}

	if len(r.calls) != len(tags) {
		t.Errorf("issued %d requests for %d tag synonyms (%v) — `tag` is single-value, so a "+
			"use case needs ONE REQUEST PER TAG", len(r.calls), len(tags), r.tags())
	}
	if len(r.calls) > civitai.MaxUseCaseTagQueries {
		t.Errorf("fan-out of %d exceeds the MaxUseCaseTagQueries bound of %d",
			len(r.calls), civitai.MaxUseCaseTagQueries)
	}
	seen := map[string]bool{}
	for _, tg := range r.tags() {
		if seen[tg] {
			t.Errorf("tag %q was queried twice", tg)
		}
		seen[tg] = true
	}

	body := rec.Body.String()
	// Deduped by model id: the shared model appears once.
	if n := strings.Count(body, `/models/100`); n == 0 {
		t.Errorf("the model returned by every synonym is missing from the merged grid")
	}
	if n := strings.Count(body, `href="/models/100"`); n > 1 {
		t.Errorf("model 100 appears %d times — the merge must dedupe by model id", n)
	}
}

// TestRelatedWorkflowsDegradesOnApiError: an upstream failure must render an
// EMPTY section and must not 500 the fragment (and therefore never the page).
func TestRelatedWorkflowsDegradesOnApiError(t *testing.T) {
	// Every call errors.
	r := &tagSearchReader{byTag: map[string][]int{}, errored: map[string]bool{"": true}}
	srv := relatedServer(t, r)

	rec := get(t, srv, "/models/42/related-workflows?eco=flux1")
	if rec.Code != 200 {
		t.Fatalf("an API error must still answer 200 with an empty fragment; got %d", rec.Code)
	}
	if b := strings.TrimSpace(rec.Body.String()); b != "" {
		t.Errorf("an API error must render NOTHING, got %q", firstN(b, 300))
	}

	// …and the model PAGE itself is unaffected: it only carries the lazy
	// placeholder, so it cannot depend on the fetch at all.
	page := get(t, srv, "/models/42")
	if page.Code != 200 {
		t.Errorf("the model page status = %d — the related-workflows fetch must not be on the "+
			"page's critical path at all", page.Code)
	}
}

// TestRelatedWorkflowsEmptyResultIsQuiet: an empty result renders nothing rather
// than "no results" — an empty first page is NOT proof there is nothing (the
// period post-filter can under-fill a page).
func TestRelatedWorkflowsEmptyResultIsQuiet(t *testing.T) {
	r := &tagSearchReader{byTag: map[string][]int{}, noTag: nil}
	srv := relatedServer(t, r)

	rec := get(t, srv, "/models/42/related-workflows?eco=flux1")
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	if b := strings.TrimSpace(rec.Body.String()); b != "" {
		t.Errorf("an empty result must render nothing, got %q", firstN(b, 300))
	}
}

// TestRelatedWorkflowsRefusesAnUnwhitelistedEcosystem: a hand-edited ?eco= must
// not become an unfiltered feed dressed up as "workflows for this model".
func TestRelatedWorkflowsRefusesAnUnwhitelistedEcosystem(t *testing.T) {
	r := &tagSearchReader{byTag: map[string][]int{}, noTag: []int{1}}
	srv := relatedServer(t, r)

	for _, path := range []string{
		"/models/42/related-workflows",                    // no eco at all
		"/models/42/related-workflows?eco=not-a-real-eco", // unknown slug
		"/models/42/related-workflows?eco=",               // blank
		"/models/42/related-workflows?use=inpaint",        // use without eco
		"/models/0/related-workflows?eco=flux1",           // bad model id
	} {
		rec := get(t, srv, path)
		if rec.Code != 200 {
			t.Errorf("%s: status = %d, want a fail-soft 200", path, rec.Code)
		}
		if b := strings.TrimSpace(rec.Body.String()); b != "" {
			t.Errorf("%s: must render nothing, got %q", path, firstN(b, 200))
		}
	}
	if len(r.calls) != 0 {
		t.Errorf("an unresolvable facet must issue NO outbound request (it would be the "+
			"UNFILTERED feed); got %d calls: %v", len(r.calls), r.calls)
	}
}

// TestModelWorkflowFacetsAreDerivedFromTheWhitelist pins the derivation itself:
// ecosystem from base models, use case from tags — through the curated table in
// both cases, and never a raw tag.
func TestModelWorkflowFacetsAreDerivedFromTheWhitelist(t *testing.T) {
	f := modelWorkflowFacets(fluxModel(), 421)
	if f.Eco == nil || f.Eco.Slug != "flux1" {
		t.Fatalf("ecosystem = %v, want flux1 (derived from the versions' baseModels)", f.Eco)
	}
	if f.Use == nil || f.Use.Slug != "inpaint" {
		t.Fatalf("use case = %v, want inpaint (the only model tag in the curated vocabulary)", f.Use)
	}

	// A model on an unrecognized base model resolves to NO ecosystem, and the
	// section must then render nothing at all.
	unknown := &civitai.ModelDetail{
		ID: 7, Name: "x", Type: "LORA",
		ModelVersions: []civitai.ModelVersionSummary{{ID: 71, BaseModel: "TotallyMadeUp 9000"}},
	}
	if got := modelWorkflowFacets(unknown, 71); got.Eco != nil {
		t.Errorf("an unrecognized baseModel must resolve to no ecosystem, got %v", got.Eco)
	}
	// The container is still emitted (it is the OOB target a later version switch
	// needs), but it is EMPTY, `hidden`, and carries NO hx-get — so nothing renders
	// and nothing is fetched.
	if html := renderString(t, relatedWorkflowsCard(modelDetailView{Model: unknown, SelectedVersionID: 71})); strings.Contains(html, "hx-get") {
		t.Errorf("a version with no ecosystem must issue no request — an unfiltered "+
			"types=Workflows feed is the POPULAR feed, not 'workflows for this model'; got %q", html)
	} else if !strings.Contains(html, "hidden") {
		t.Errorf("the empty container must carry the `hidden` ATTRIBUTE (space-y-6 skips "+
			"[hidden] children, so it costs no section spacing); got %q", html)
	}

	// Tags that are pure noise / stopwords yield NO use case (one broader request),
	// never a guessed one.
	noisy := &civitai.ModelDetail{
		ID: 8, Name: "x", Type: "LORA",
		Tags:          []string{"tool", "comfyui", "anime girl"},
		ModelVersions: []civitai.ModelVersionSummary{{ID: 81, BaseModel: "Flux.1 D"}},
	}
	if got := modelWorkflowFacets(noisy, 81); got.Use != nil {
		t.Errorf("stopword/noise tags must yield NO use case, got %v", got.Use)
	}
}

// TestRelatedWorkflowsSectionIsLazyAndSwappedOutOfBand: the section must not be
// on the page's critical path, must keep its PLACE in the DOM (it renders below
// #version-region), and must still re-resolve on a version tab click — which is
// what hx-swap-oob buys.
func TestRelatedWorkflowsSectionIsLazyAndSwappedOutOfBand(t *testing.T) {
	r := &tagSearchReader{byTag: map[string][]int{}, noTag: []int{1}}
	srv := relatedServer(t, r)

	body := get(t, srv, "/models/42").Body.String()
	if !strings.Contains(body, `id="related-workflows"`) {
		t.Fatalf("the model page is missing the related-workflows container; body = %q",
			firstN(body, 1200))
	}
	if !strings.Contains(body, `hx-trigger="revealed"`) {
		t.Error("the section must be lazily fetched, not rendered inline — a cache miss costs " +
			"up to MaxUseCaseTagQueries outbound requests")
	}
	if !strings.Contains(body, "/models/42/related-workflows?eco=flux1") {
		t.Errorf("the container must carry the derived facet slugs; body = %q", firstN(body, 1200))
	}
	// The PAGE render itself issued no SearchModels call at all.
	if len(r.calls) != 0 {
		t.Errorf("the model page render issued %d outbound searches; it must issue none", len(r.calls))
	}

	// The htmx version-swap fragment carries it as an OUT-OF-BAND element. The
	// section's ecosystem comes from the SELECTED version, so a version click MUST
	// re-render it; it sits below #version-region on the page, so an in-band swap
	// would move it. hx-swap-oob replaces it where it stands.
	_, hxBody := hxGet(t, srv, "/models/42")
	if !strings.Contains(hxBody, `id="related-workflows"`) {
		t.Fatalf("a version swap must re-render the related-workflows container (its "+
			"ecosystem is the SELECTED version's); fragment = %q", firstN(hxBody, 1200))
	}
	if !strings.Contains(hxBody, `hx-swap-oob="true"`) {
		t.Errorf("the container must be swapped OUT OF BAND — an in-band copy would move "+
			"the section inside #version-region and reorder the page; fragment = %q",
			firstN(hxBody, 1200))
	}
	// EXACTLY ONE. htmx lifts an oob element out of the fragment and removes it
	// before swapping the rest, so a second copy would be swapped IN BAND into
	// #version-region and the page would carry two — one of them inside the region
	// that re-renders, i.e. duplicating on every subsequent version click. This is
	// the last property the older …OutsideTheVersionRegion test used to cover
	// (it asserted the id was ABSENT from the fragment, which was the per-MODEL
	// contract this fix deliberately reversed), so assert the count, not absence.
	if n := strings.Count(hxBody, `id="related-workflows"`); n != 1 {
		t.Errorf("the swap fragment carries %d related-workflows containers, want exactly 1 — "+
			"a second copy is swapped in band and duplicates the section; fragment = %q",
			n, firstN(hxBody, 1200))
	}
	// The swap fragment is still lazy: it re-issues the hx-get, it does not embed
	// a grid, so the version click itself costs no outbound request either.
	if len(r.calls) != 0 {
		t.Errorf("the version swap issued %d outbound searches; it must issue none", len(r.calls))
	}
}

// TestRelatedWorkflowsBrowseLinkCarriesTheFacets: the "Browse more" hand-off must
// land on /workflows/discover with the SAME facets, so the section and the full
// grid can never disagree about what is being shown.
func TestRelatedWorkflowsBrowseLinkCarriesTheFacets(t *testing.T) {
	byTag := map[string][]int{}
	uc, _ := civitai.UseCaseBySlug("inpaint")
	for _, tg := range uc.QueryTags() {
		byTag[tg] = []int{101}
	}
	r := &tagSearchReader{byTag: byTag}
	srv := relatedServer(t, r)

	body := get(t, srv, "/models/42/related-workflows?eco=flux1&use=inpaint").Body.String()
	if !strings.Contains(body, "Workflows for Flux.1 · Inpainting") {
		t.Errorf("the heading must name the ACTUAL filter that produced the grid; body = %q",
			firstN(body, 500))
	}
	// gomponents attribute-escapes the &, so compare against the escaped form.
	want := strings.ReplaceAll(
		discoverHref("", relatedWorkflowsSort, relatedWorkflowsPeriod, "flux1", "inpaint"),
		"&", "&amp;")
	if !strings.Contains(body, want) {
		t.Errorf("the browse link must be %q; body = %q", want, firstN(body, 800))
	}
	// The import action is the EXISTING audited endpoint, with a CSRF token.
	if !strings.Contains(body, "/workflows/discover/101/import") {
		t.Error("cards must reuse the existing import endpoint, not a parallel one")
	}
	if !strings.Contains(body, srv.csrf) {
		t.Error("the import control must carry the CSRF token")
	}
}
