package web

import (
	"strconv"
	"strings"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
)

// ===========================================================================
// "Workflows for <ecosystem>" must name the SELECTED VERSION's ecosystem.
// ===========================================================================
//
// THE SHIPPED BUG (v0.1.87), reported against /models/573152:
// modelWorkflowFacets unioned the base models of ALL m.ModelVersions and took
// ecos[0] in TABLE order. That is a majority/table-order vote that ignores the
// selection entirely, so on LUSTIFY! — whose newest version 3112728 is "Krea 2"
// while its other 16 are "SDXL 1.0" / "SDXL Lightning" — selecting the Krea 2
// version still read "Workflows for SDXL family" and fetched SDXL workflows.
//
// The real shape, fetched live 2026-07-30 (GET /api/v1/models/573152):
//
//	3112728  Krea 2          v10 (Krea 2)            2026-07-25
//	3045803  SDXL 1.0        ZENITH (V9)             2026-07-04
//	2808677  SDXL 1.0        APEX (V8)               2026-04-13
//	…13 more SDXL 1.0…
//	938628   SDXL Lightning  v4.0 DMD2               2024-10-09
//
// lustifyShapedModel reproduces exactly that: ONE minority-ecosystem version
// among many of another, with the minority one NOT first in the list (so a test
// that merely read m.ModelVersions[0] would also be wrong).

const (
	lustifyKreaVersionID = 3112728
	lustifySDXLVersionID = 3045803
)

func lustifyShapedModel() *civitai.ModelDetail {
	vs := []civitai.ModelVersionSummary{
		// Order as CivitAI returns it: the creator's `index`, primary first. Here the
		// primary IS the Krea 2 one, so the test also proves the resolution is by ID
		// and not by position — see lustifySDXLSelected below.
		{ID: lustifyKreaVersionID, Name: "v10 (Krea 2)", BaseModel: "Krea 2"},
		{ID: lustifySDXLVersionID, Name: "ZENITH (V9)", BaseModel: "SDXL 1.0"},
		{ID: 2808677, Name: "APEX (V8)", BaseModel: "SDXL 1.0"},
		{ID: 2875936, Name: "APEX INPAINTING", BaseModel: "SDXL 1.0"},
		{ID: 2155386, Name: "GGWP (V7)", BaseModel: "SDXL 1.0"},
		{ID: 1569593, Name: "OLT (FIXED TEXTURES)", BaseModel: "SDXL 1.0"},
		{ID: 1094291, Name: "ENDGAME", BaseModel: "SDXL 1.0"},
		{ID: 938628, Name: "v4.0 DMD2", BaseModel: "SDXL Lightning"},
	}
	return &civitai.ModelDetail{
		ID: 573152, Name: "LUSTIFY! [NSFW checkpoint]", Type: "Checkpoint",
		// The real model's tags; none of them is in the curated use-case vocabulary,
		// so the facet is ecosystem-only — exactly like the live page.
		Tags:          []string{"base model", "checkpoint", "girls", "photorealisic", "woman"},
		ModelVersions: vs,
	}
}

// TestFacetsResolveFromTheSelectedVersionNotTheMajority is mutation (a): reverting
// modelWorkflowFacets to the union-and-take-first must fail here.
//
// The fixture is calibrated so the union answer and the selected-version answer
// DISAGREE: 7 of 8 versions are SDXL, and "sdxl" precedes "krea" in the curated
// table, so the old code returned SDXL for EVERY version of this model.
func TestFacetsResolveFromTheSelectedVersionNotTheMajority(t *testing.T) {
	m := lustifyShapedModel()

	// Sanity-check the fixture actually reaches the interesting case: the old
	// union-and-take-first answer must be SDXL, i.e. NOT the Krea version's answer.
	// Without this a "green" test below could be green for the wrong reason.
	bases := make([]string, 0, len(m.ModelVersions))
	for _, v := range m.ModelVersions {
		bases = append(bases, v.BaseModel)
	}
	union := civitai.EcosystemsForBaseModels(bases)
	if len(union) == 0 || union[0].Slug != "sdxl" {
		t.Fatalf("fixture is miscalibrated: the OLD union-and-take-first answer is %v, "+
			"but the bug only exists when it is sdxl — this test would not be able to "+
			"detect the regression", union)
	}

	got := modelWorkflowFacets(m, lustifyKreaVersionID)
	if got.Eco == nil || got.Eco.Slug != "krea" {
		t.Fatalf("with version %d (Krea 2) selected the ecosystem = %v, want krea. "+
			"This is the v0.1.87 bug: the majority of versions are SDXL, so a union over "+
			"ALL versions labels the section 'Workflows for SDXL family' while the user is "+
			"looking at a Krea 2 version.", lustifyKreaVersionID, got.Eco)
	}
	// The Krea version's OWN base model IS the ecosystem label, so the two halves
	// of the heading collapse to one: "Workflows for Krea 2", never "… Krea 2 ·
	// Krea 2".
	if h := relatedWorkflowsHeading(got, "Krea 2"); h != "Workflows for Krea 2" {
		t.Errorf("heading = %q, want %q — the heading must name the SELECTED version's "+
			"ecosystem, and must COLLAPSE when the version's base model IS the ecosystem "+
			"label", h, "Workflows for Krea 2")
	}

	// …and the SDXL versions still resolve to SDXL. The Krea version is FIRST in the
	// list, so this half also proves the resolution keys on the id rather than on
	// m.ModelVersions[0].
	sdxl := modelWorkflowFacets(m, lustifySDXLVersionID)
	if sdxl.Eco == nil || sdxl.Eco.Slug != "sdxl" {
		t.Fatalf("with version %d (SDXL 1.0) selected the ecosystem = %v, want sdxl",
			lustifySDXLVersionID, sdxl.Eco)
	}
	// Here the two halves DIFFER — the version is SDXL 1.0, the family being
	// searched is the whole SDXL union — so both are named.
	if h := relatedWorkflowsHeading(sdxl, "SDXL 1.0"); h != "Workflows for SDXL 1.0 · SDXL family" {
		t.Errorf("heading = %q, want %q", h, "Workflows for SDXL 1.0 · SDXL family")
	}
	// No resolvable base model → the family label alone, never a dangling separator.
	if h := relatedWorkflowsHeading(sdxl, ""); h != "Workflows for SDXL family" {
		t.Errorf("heading with no base model = %q, want %q", h, "Workflows for SDXL family")
	}
}

// TestFacetsAreStableAcrossSameEcosystemVersions: switching between versions that
// share an ecosystem must produce the IDENTICAL facetFeed CACHE KEY — which is
// what makes the re-render free (zero outbound requests). Only an ecosystem
// CHANGE may cost a fetch.
//
// It asserts the CACHE KEY, not the fragment URL. The URL now also carries `bm=`
// (the selected version's own base model, for the heading), which legitimately
// differs between two SDXL versions — "SDXL 1.0" vs "SDXL Lightning" — while the
// outbound request and its cache key are byte-identical. Asserting URL equality
// here would forbid the heading from naming the version at all.
func TestFacetsAreStableAcrossSameEcosystemVersions(t *testing.T) {
	m := lustifyShapedModel()

	var want, wantKey string
	for _, vid := range []int{lustifySDXLVersionID, 2808677, 2875936, 2155386, 1569593, 1094291, 938628} {
		f := modelWorkflowFacets(m, vid)
		if f.Eco == nil || f.Eco.Slug != "sdxl" {
			t.Fatalf("version %d resolved to %v, want the sdxl ecosystem (SDXL Lightning is "+
				"in the SDXL family too — the community fine-tune lineages are architecturally "+
				"SDXL, see the curated table)", vid, f.Eco)
		}
		key := facetFeedKey(false, relatedWorkflowsSort, relatedWorkflowsPeriod, f)
		if wantKey == "" {
			wantKey = key
			want = relatedWorkflowsPath(m.ID, f, "", nil)
			continue
		}
		if key != wantKey {
			t.Fatalf("version %d produced the facet cache key %q, want %q — a same-ecosystem "+
				"switch must hit the facetFeed cache without an outbound request", vid, key, wantKey)
		}
		// The request-determining half of the URL (everything but the display-only
		// `bm`) must also be identical.
		if got := relatedWorkflowsPath(m.ID, f, "", nil); got != want {
			t.Fatalf("version %d produced the fragment URL %q, want %q", vid, got, want)
		}
	}

	// The Krea version must NOT share that key, or an ecosystem change would be
	// served the previous ecosystem's cached feed.
	kf := modelWorkflowFacets(m, lustifyKreaVersionID)
	if key := facetFeedKey(false, relatedWorkflowsSort, relatedWorkflowsPeriod, kf); key == wantKey {
		t.Fatalf("the Krea 2 version produced the same facet cache key as the SDXL versions "+
			"(%q) — a DIFFERENT ecosystem must miss the cache and refetch", key)
	}
	if krea := relatedWorkflowsPath(m.ID, kf, "", nil); krea == want {
		t.Fatalf("the Krea 2 version produced the same fragment URL as the SDXL versions "+
			"(%q) — a DIFFERENT ecosystem must miss the cache and refetch", krea)
	}
}

// TestSelectedVersionWithNoEcosystemRendersNothing: the "render nothing" rule is
// per SELECTED VERSION now, and it must survive. An unfiltered types=Workflows
// request IS the popular feed, so a heading over it would lie.
func TestSelectedVersionWithNoEcosystemRendersNothing(t *testing.T) {
	m := lustifyShapedModel()
	// One version on a base model the curated table does not know. Its siblings are
	// all resolvable, so a union would happily label the section "SDXL family".
	const orphanID = 999001
	m.ModelVersions = append(m.ModelVersions,
		civitai.ModelVersionSummary{ID: orphanID, Name: "experimental", BaseModel: "TotallyMadeUp 9000"})

	if f := modelWorkflowFacets(m, orphanID); f.Eco != nil {
		t.Fatalf("a selected version on an unrecognized baseModel resolved to %v; it must "+
			"resolve to NO ecosystem, because an unfiltered types=Workflows feed is the "+
			"POPULAR feed and a heading over it would lie", f.Eco)
	}

	html := renderString(t, relatedWorkflowsCard(modelDetailView{Model: m, SelectedVersionID: orphanID}))
	if strings.Contains(html, "hx-get") {
		t.Errorf("the container must issue no request for a version with no ecosystem; got %q", html)
	}
	if !strings.Contains(html, "hidden") {
		t.Errorf("the empty container must carry the `hidden` ATTRIBUTE so <main>'s space-y-6 "+
			"(.space-y-6 > :not([hidden]) ~ :not([hidden])) skips it and the section spacing is "+
			"unchanged; got %q", html)
	}
	// It IS still emitted, because a later version switch needs an OOB target to
	// find — without it htmx would drop the swap and the section could never come
	// back.
	if !strings.Contains(html, `id="related-workflows"`) {
		t.Errorf("the container must still be emitted as the OOB target; got %q", html)
	}
	oob := renderString(t, relatedWorkflowsOOB(modelDetailView{Model: m, SelectedVersionID: orphanID}))
	if !strings.Contains(oob, `hx-swap-oob="true"`) {
		t.Errorf("switching TO a no-ecosystem version must OOB-clear the section, not leave "+
			"the previous ecosystem's grid on screen; got %q", oob)
	}
}

// TestVersionSwapRerendersTheSectionWithTheSelectedEcosystem is the end-to-end
// reproduction of the user's report at the HTTP level: /models/573152 with
// version 3112728 selected must say Krea, and with an SDXL version selected must
// say SDXL — on BOTH the full page and the htmx version-swap fragment.
func TestVersionSwapRerendersTheSectionWithTheSelectedEcosystem(t *testing.T) {
	r := &tagSearchReader{byTag: map[string][]int{}, noTag: []int{1}}
	r.fakeReader = fakeReader{model: lustifyShapedModel()}
	srv := newTestServer(t)
	srv.reader = r

	// The fragment URL now also carries `bm=` — the SELECTED version's own base
	// model, for the heading — so the assertions name the WHOLE query string. That
	// is the point of this pair: the Krea version must ask for bm=Krea 2 + eco=krea
	// and the SDXL one for bm=SDXL 1.0 + eco=sdxl. url.Values.Encode sorts keys, so
	// `bm` precedes `eco`. This model's tags resolve to no use case, hence no
	// `uses=` chip vocabulary.
	cases := []struct {
		version int
		wantEco string
		badEco  string
	}{
		{lustifyKreaVersionID, "bm=Krea+2&amp;eco=krea", "eco=sdxl"},
		{lustifySDXLVersionID, "bm=SDXL+1.0&amp;eco=sdxl", "eco=krea"},
	}
	for _, c := range cases {
		full := get(t, srv, pathWithVersion(c.version)).Body.String()
		if !strings.Contains(full, "/models/573152/related-workflows?"+c.wantEco) {
			t.Errorf("full page with version %d selected: fragment URL should carry %q; body = %q",
				c.version, c.wantEco, firstN(full, 1500))
		}
		if strings.Contains(full, "/models/573152/related-workflows?"+c.badEco) {
			t.Errorf("full page with version %d selected: fragment URL carries %q — that is the "+
				"OTHER version's ecosystem (the v0.1.87 bug)", c.version, c.badEco)
		}

		_, hxBody := hxGet(t, srv, pathWithVersion(c.version))
		if !strings.Contains(hxBody, "/models/573152/related-workflows?"+c.wantEco) {
			t.Errorf("version-swap fragment for version %d should carry %q; fragment = %q",
				c.version, c.wantEco, firstN(hxBody, 1500))
		}
		if strings.Contains(hxBody, "/models/573152/related-workflows?"+c.badEco) {
			t.Errorf("version-swap fragment for version %d carries %q — a version click must "+
				"re-resolve the ecosystem, not keep the previous one", c.version, c.badEco)
		}
	}

	// And the rendered heading actually names it. The fragment handler re-validates
	// ?eco= through the same whitelist, so this also covers the round trip.
	body := get(t, srv, "/models/573152/related-workflows?eco=krea&bm=Krea+2").Body.String()
	if !strings.Contains(body, "Workflows for Krea 2") {
		t.Errorf("the fragment heading must name the selected version's ecosystem; body = %q",
			firstN(body, 600))
	}
	if strings.Contains(body, "SDXL family") {
		t.Errorf("the fragment still mentions SDXL family; body = %q", firstN(body, 600))
	}
}

func pathWithVersion(v int) string {
	return "/models/573152?version=" + strconv.Itoa(v)
}
