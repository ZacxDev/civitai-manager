package web

import (
	"net/url"
	"strings"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
)

// ===========================================================================
// /models/1386234 — the two things the user reported, on ONE fixture.
// ===========================================================================
//
//  1. The section read "Workflows for SDXL family" while every version of the
//     model is baseModel "Illustrious". That is not a bug in the QUERY — the
//     curated table groups Illustrious/Pony/NoobAI into the SDXL family because
//     CivitAI's `baseModels` filter unions them and returns far more genuinely
//     compatible workflows — it is a bug in the HEADING, which named the family
//     and not the version. It now names both: "Workflows for Illustrious · SDXL
//     family", with the outgoing request UNCHANGED.
//
//  2. The section silently filtered to "Upscaling". The model carries 25 CivitAI
//     tags; three of them resolve to curated use cases, and `upscale` won purely
//     because it sits above `controlnet` and `lora` in the table. The user could
//     see neither why nor how to change it. The resolved use cases are now a
//     visible chip row with "All" selected by default.
//
// The fixture below is the REAL model, fetched live 2026-07-31
// (GET https://civitai.com/api/v1/models/1386234):
//
//	type: Workflows
//	versions: 3107550 V37 … 2575178 V28 — ALL baseModel "Illustrious"
//	tags: comfyui, noobai, controlnet, workflow, tool, refiner, clip, eps,
//	      regional prompting, comfy, ip adapter, upscale, illustrious, sdxl,
//	      lora, detail, nai, vpred, tipo, qwenvl, vae, xl, workflows, pony,
//	      clip vision
//
// Of those 25, EXACTLY THREE hit the curated vocabulary: `upscale` → Upscaling,
// `controlnet` → ControlNet & guidance, `lora` → LoRA pipelines. (`detail`,
// `ip adapter` and `regional prompting` look like hits but are NOT — the table's
// entries are `detailer`, `ipadapter` and `regional prompt`, and CivitAI does not
// unify tag synonyms. That is what makes this fixture a real calibration rather
// than a convenient one.)

const (
	illustriousModelID   = 1386234
	illustriousVersionID = 3107550
)

func illustriousWorkflowModel() *civitai.ModelDetail {
	vs := []civitai.ModelVersionSummary{
		{ID: illustriousVersionID, Name: "V37", BaseModel: "Illustrious"},
		{ID: 3036553, Name: "V36", BaseModel: "Illustrious"},
		{ID: 2939840, Name: "V35", BaseModel: "Illustrious"},
		{ID: 2879502, Name: "V34", BaseModel: "Illustrious"},
	}
	return &civitai.ModelDetail{
		ID: illustriousModelID, Name: "Illustrious workflow pack", Type: "Workflows",
		Tags: []string{
			"comfyui", "noobai", "controlnet", "workflow", "tool", "refiner", "clip",
			"eps", "regional prompting", "comfy", "ip adapter", "upscale", "illustrious",
			"sdxl", "lora", "detail", "nai", "vpred", "tipo", "qwenvl", "vae", "xl",
			"workflows", "pony", "clip vision",
		},
		ModelVersions: vs,
	}
}

func illustriousServer(t *testing.T, r *tagSearchReader) *Server {
	t.Helper()
	r.fakeReader = fakeReader{model: illustriousWorkflowModel()}
	srv := newTestServer(t)
	srv.reader = r
	return srv
}

// ---------------------------------------------------------------------------
// ITEM 1 — the heading names the SELECTED VERSION's own base model
// ---------------------------------------------------------------------------

// TestHeadingNamesTheVersionsBaseModelAndTheFamily is the user's exact report.
//
// Fixture sanity first: the version's base model and the ecosystem label must
// actually DIFFER here, or "names both" would be indistinguishable from "names
// the family" and this test could not detect the regression.
func TestHeadingNamesTheVersionsBaseModelAndTheFamily(t *testing.T) {
	m := illustriousWorkflowModel()
	f := modelWorkflowFacets(m, illustriousVersionID)
	if f.Eco == nil || f.Eco.Slug != "sdxl" {
		t.Fatalf("ecosystem = %v, want sdxl — an Illustrious version belongs to the SDXL "+
			"family in the curated table", f.Eco)
	}
	bm := selectedVersionBaseModel(m, illustriousVersionID)
	if bm != "Illustrious" {
		t.Fatalf("selected version base model = %q, want Illustrious", bm)
	}
	if strings.EqualFold(bm, f.Eco.Label) {
		t.Fatalf("fixture is miscalibrated: the base model %q IS the ecosystem label %q, so "+
			"the collapse branch would answer this test and the two-part heading would go "+
			"untested", bm, f.Eco.Label)
	}

	const want = "Workflows for Illustrious · SDXL family"
	if got := relatedWorkflowsHeading(f, bm); got != want {
		t.Errorf("heading = %q, want %q — the section must name the version the user is "+
			"looking at AND the family actually being searched", got, want)
	}

	// --- end to end, at the HTTP level -----------------------------------------
	// The PAGE must put the SELECTED VERSION's base model in the fragment URL. This
	// half is what catches "read the base model off the ECOSYSTEM instead": the sdxl
	// family's first member is "SDXL 1.0", not "Illustrious", so that mutation lands
	// here even though relatedWorkflowsHeading itself would still look correct.
	if bm == f.Eco.BaseModels[0] {
		t.Fatalf("fixture is miscalibrated: the version's base model %q is also the sdxl "+
			"family's FIRST member, so reading the base model off the ecosystem would be "+
			"indistinguishable from reading it off the version", bm)
	}
	r := &tagSearchReader{byTag: map[string][]int{}, noTag: []int{9001}}
	srv := illustriousServer(t, r)

	page := get(t, srv, "/models/1386234").Body.String()
	if !strings.Contains(page, "bm=Illustrious") {
		t.Errorf("the model page must carry the SELECTED VERSION's own base model into the "+
			"fragment URL (bm=Illustrious); page = %q", firstN(page, 2000))
	}
	if strings.Contains(page, "bm=SDXL") {
		t.Errorf("the fragment URL names %q — that is the ecosystem's first member, not the "+
			"version the user is looking at", "SDXL 1.0")
	}
	frag := get(t, srv, "/models/1386234/related-workflows?eco=sdxl&bm=Illustrious").Body.String()
	if !strings.Contains(frag, want) {
		t.Errorf("the rendered section heading must be %q; body = %q", want, firstN(frag, 600))
	}
}

// TestHeadingCollapsesWhenTheBaseModelIsTheFamily: a Krea 2 version sits in the
// "Krea 2" ecosystem, so both halves are the same word. "Krea 2 · Krea 2" is
// noise.
func TestHeadingCollapsesWhenTheBaseModelIsTheFamily(t *testing.T) {
	eco, ok := civitai.EcosystemBySlug("krea")
	if !ok {
		t.Fatal("the krea ecosystem vanished from the curated table")
	}
	f := workflowFacets{Eco: &eco}
	for _, bm := range []string{"Krea 2", "krea 2", "  KREA 2  "} {
		if got := relatedWorkflowsHeading(f, bm); got != "Workflows for Krea 2" {
			t.Errorf("heading for base model %q = %q, want %q — the collapse must be "+
				"case- and whitespace-insensitive", bm, got, "Workflows for Krea 2")
		}
	}
}

// TestHeadingFallsBackToTheFamilyAlone: nothing resolvable → the family label,
// never a dangling separator and never caller text.
func TestHeadingFallsBackToTheFamilyAlone(t *testing.T) {
	eco, _ := civitai.EcosystemBySlug("sdxl")
	f := workflowFacets{Eco: &eco}
	for _, bm := range []string{"", "   "} {
		if got := relatedWorkflowsHeading(f, bm); got != "Workflows for SDXL family" {
			t.Errorf("heading for base model %q = %q, want %q", bm, got, "Workflows for SDXL family")
		}
	}
}

// TestHeadingBaseModelIsWhitelistedNotEchoed: the base model reaches the fragment
// handler as a URL parameter, so it must be resolved through the curated table
// and rendered in the TABLE's casing — never echoed.
//
// The mismatch case matters as much as the junk case: `bm` naming a base model
// from a DIFFERENT family would describe a query that is not being made.
func TestHeadingBaseModelIsWhitelistedNotEchoed(t *testing.T) {
	r := &tagSearchReader{byTag: map[string][]int{}, noTag: []int{9001}}
	srv := illustriousServer(t, r)

	cases := []struct {
		name string
		bm   string
		want string
	}{
		{"canonical", "Illustrious", "Workflows for Illustrious · SDXL family"},
		{"lowercased input is canonicalised", "illustrious", "Workflows for Illustrious · SDXL family"},
		{"a base model from ANOTHER family is refused", "Flux.1 D", "Workflows for SDXL family"},
		{"junk is refused", "<script>alert(1)</script>", "Workflows for SDXL family"},
		{"absent", "", "Workflows for SDXL family"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			q := url.Values{"eco": {"sdxl"}}
			if c.bm != "" {
				q.Set("bm", c.bm)
			}
			body := get(t, srv, "/models/1386234/related-workflows?"+q.Encode()).Body.String()
			if !strings.Contains(body, c.want) {
				t.Errorf("heading for bm=%q must be %q; body = %q", c.bm, c.want, firstN(body, 600))
			}
			if c.bm == "<script>alert(1)</script>" && strings.Contains(body, "alert(1)") {
				t.Errorf("the bm parameter was ECHOED into the page: %q", firstN(body, 600))
			}
		})
	}
}

// TestHeadingChangeLeavesTheOutgoingQueryUNCHANGED is the load-bearing half of
// item 1: naming the version must not narrow the search. The request is still the
// whole family's REPEATED-param union.
//
// The comma-joined form is asserted against explicitly because it is the silent
// failure: CivitAI parses `baseModels=A,B` as one literal base-model name and
// answers HTTP 200 with items: [].
func TestHeadingChangeLeavesTheOutgoingQueryUNCHANGED(t *testing.T) {
	eco, _ := civitai.EcosystemBySlug("sdxl")

	// With bm, and without: the outbound params must be byte-identical.
	withBM := &tagSearchReader{byTag: map[string][]int{}, noTag: []int{9001}}
	get(t, illustriousServer(t, withBM), "/models/1386234/related-workflows?eco=sdxl&bm=Illustrious")
	withoutBM := &tagSearchReader{byTag: map[string][]int{}, noTag: []int{9001}}
	get(t, illustriousServer(t, withoutBM), "/models/1386234/related-workflows?eco=sdxl")

	if len(withBM.calls) != 1 || len(withoutBM.calls) != 1 {
		t.Fatalf("expected exactly one outbound request each, got %d and %d",
			len(withBM.calls), len(withoutBM.calls))
	}
	if withBM.calls[0].Encode() != withoutBM.calls[0].Encode() {
		t.Fatalf("adding bm= changed the OUTBOUND query.\n with bm: %s\n without: %s\n"+
			"The base model is a HEADING label; the search stays the family union.",
			withBM.calls[0].Encode(), withoutBM.calls[0].Encode())
	}

	q := withBM.calls[0]
	// The version's own base model must NOT have replaced the family union.
	got := q["baseModels"]
	if len(got) != len(eco.BaseModels) {
		t.Errorf("baseModels carries %d values %v, want the whole SDXL family (%d REPEATED "+
			"params) — naming the version in the heading must not narrow the query",
			len(got), got, len(eco.BaseModels))
	}
	for _, v := range got {
		if strings.Contains(v, ",") {
			t.Errorf("baseModels value %q is COMMA-JOINED. CivitAI parses that as ONE literal "+
				"base-model name and returns HTTP 200 with items: [] — a silently empty "+
				"section. Use url.Values.Add (repeated params).", v)
		}
	}
	if _, ok := q["bm"]; ok {
		t.Errorf("the display-only `bm` parameter reached the outbound request: %s", q.Encode())
	}
	if got := q.Get("types"); got != "Workflows" {
		t.Errorf("types = %q, want the PLURAL Workflows", got)
	}
}

// ---------------------------------------------------------------------------
// ITEM 2 — the use case is a VISIBLE PICKER, defaulting to All
// ---------------------------------------------------------------------------

// TestUseCaseChipsOfferOnlyThisModelsResolvedUseCases.
//
// Fixture calibration is asserted, not assumed: exactly three of the model's 25
// tags may resolve, and `upscale` must be FIRST in table order — that ordering is
// the whole reason the old auto-pick showed "Upscaling".
func TestUseCaseChipsOfferOnlyThisModelsResolvedUseCases(t *testing.T) {
	m := illustriousWorkflowModel()
	offered := modelUseCaseChoices(m)

	var slugs []string
	for _, u := range offered {
		slugs = append(slugs, u.Slug)
	}
	want := []string{"upscale", "controlnet", "lora"}
	if strings.Join(slugs, ",") != strings.Join(want, ",") {
		t.Fatalf("offered use cases = %v, want %v (table order). If this changed because the "+
			"curated table grew a synonym, recalibrate — but the FIRST entry must stay the "+
			"one the old auto-pick would have applied, or the 'All is default' test below "+
			"cannot detect the regression.", slugs, want)
	}
	if len(offered) >= len(civitai.UseCases()) {
		t.Fatalf("the chip row offers %d of %d table entries — it must offer only the ones "+
			"THIS model's tags resolve to, not the whole vocabulary",
			len(offered), len(civitai.UseCases()))
	}

	r := &tagSearchReader{byTag: map[string][]int{}, noTag: []int{9001}}
	srv := illustriousServer(t, r)
	body := get(t, srv,
		"/models/1386234/related-workflows?eco=sdxl&bm=Illustrious&uses=upscale,controlnet,lora").Body.String()

	for _, u := range offered {
		// gomponents HTML-escapes text, so "ControlNet & guidance" renders with &amp;.
		if !strings.Contains(body, ">"+escText(u.Label)+"<") {
			t.Errorf("the chip row is missing %q; body = %q", u.Label, firstN(body, 1500))
		}
	}
	if !strings.Contains(body, ">All<") {
		t.Errorf("the chip row must offer All; body = %q", firstN(body, 1500))
	}
	// Use cases this model does NOT resolve to must not be offered.
	for _, absent := range []string{"Inpainting", "Face swap & identity", "Low VRAM"} {
		if strings.Contains(body, ">"+escText(absent)+"<") {
			t.Errorf("the chip row offers %q, which this model's tags do not resolve to", absent)
		}
	}
	// Keyboard-operable + accessible: real buttons, an accessible group name, and
	// pressed state carried by ARIA rather than by colour alone.
	if !strings.Contains(body, `aria-pressed="true"`) || !strings.Contains(body, `aria-pressed="false"`) {
		t.Errorf("chips must carry aria-pressed in BOTH states (selection must not be "+
			"colour-only); body = %q", firstN(body, 1500))
	}
	if !strings.Contains(body, `aria-label="Filter these workflows by use case"`) {
		t.Errorf("the chip row needs an accessible name; body = %q", firstN(body, 1500))
	}
}

// TestAllIsTheDefaultAndAppliesNoTag is mutation (b): restoring the auto-pick in
// modelWorkflowFacets must fail here.
//
// Two independent halves, because the auto-pick could be restored in either
// place: the PAGE must not put a ?use= in the fragment URL, and the FRAGMENT must
// not put a tag on the wire.
func TestAllIsTheDefaultAndAppliesNoTag(t *testing.T) {
	r := &tagSearchReader{byTag: map[string][]int{}, noTag: []int{9001}}
	srv := illustriousServer(t, r)

	// --- the PAGE ---------------------------------------------------------------
	page := get(t, srv, "/models/1386234").Body.String()
	if strings.Contains(page, "use=upscale") {
		t.Errorf("the model page pre-selected the use case `upscale` in the fragment URL. "+
			"It is the FIRST tag-resolved use case in TABLE ORDER, not the most relevant "+
			"one, and the user never picked it; page = %q", firstN(page, 2000))
	}
	if !strings.Contains(page, "uses=upscale%2Ccontrolnet%2Clora") {
		t.Errorf("the model page must OFFER the resolved use cases as the chip vocabulary; "+
			"page = %q", firstN(page, 2000))
	}

	// --- the FRAGMENT -----------------------------------------------------------
	body := get(t, srv,
		"/models/1386234/related-workflows?eco=sdxl&bm=Illustrious&uses=upscale,controlnet,lora").Body.String()

	if len(r.calls) != 1 {
		t.Fatalf("the default view must issue exactly ONE (tag-less) request, got %d: %v",
			len(r.calls), r.tags())
	}
	if tg := r.calls[0].Get("tag"); tg != "" {
		t.Fatalf("the default view sent tag=%q. \"All\" must apply NO tag — an auto-applied "+
			"filter the user did not pick is exactly the reported bug.", tg)
	}
	if strings.Contains(body, "Workflows for Illustrious · SDXL family · Upscaling") {
		t.Errorf("the heading still names an auto-applied use case; body = %q", firstN(body, 600))
	}
	// "All" is the pressed chip.
	all := strings.Index(body, ">All<")
	if all < 0 {
		t.Fatalf("no All chip; body = %q", firstN(body, 1500))
	}
	if !strings.Contains(body[:all], `aria-pressed="true"`) {
		t.Errorf("the All chip must be the pressed one by default; body = %q", firstN(body, 1500))
	}
	if strings.Count(body, `aria-pressed="true"`) != 1 {
		t.Errorf("exactly one chip may be pressed, got %d", strings.Count(body, `aria-pressed="true"`))
	}
}

// TestSelectingAChipSendsExactlyThatWhitelistedSlug: the chip's own hx-get IS the
// request it issues, so following it proves the round trip.
func TestSelectingAChipSendsExactlyThatWhitelistedSlug(t *testing.T) {
	uc, ok := civitai.UseCaseBySlug("controlnet")
	if !ok {
		t.Fatal("the controlnet use case vanished from the curated table")
	}
	byTag := map[string][]int{}
	for _, tg := range uc.QueryTags() {
		byTag[tg] = []int{9001}
	}
	// noTag is populated too: the chip row is only rendered when the DEFAULT ("All")
	// view has results, so an empty no-tag feed would make this test assert against
	// an empty body and pass for the wrong reason.
	r := &tagSearchReader{byTag: byTag, noTag: []int{9001}}
	srv := illustriousServer(t, r)

	base := "/models/1386234/related-workflows?eco=sdxl&bm=Illustrious&uses=upscale,controlnet,lora"
	body := get(t, srv, base).Body.String()

	// The ControlNet chip's hx-get must be the same endpoint plus use=controlnet.
	want := "/models/1386234/related-workflows?bm=Illustrious&amp;eco=sdxl&amp;use=controlnet&amp;uses=upscale%2Ccontrolnet%2Clora"
	if !strings.Contains(body, `hx-get="`+want+`"`) {
		t.Fatalf("the ControlNet chip must request %q — same read-only endpoint, carrying the "+
			"ecosystem, the base model and the chip vocabulary through; body = %q",
			want, firstN(body, 2500))
	}
	if !strings.Contains(body, `hx-target="#`+relatedWorkflowsID+`"`) {
		t.Errorf("a chip must re-swap the section container, not the page; body = %q",
			firstN(body, 2500))
	}

	// Follow it.
	r.calls = nil
	sel := get(t, srv, strings.ReplaceAll(want, "&amp;", "&")).Body.String()

	if len(r.calls) != len(uc.QueryTags()) {
		t.Fatalf("selecting ControlNet issued %d requests for %d whitelisted synonyms (%v) — "+
			"`tag` is single-value, so a use case needs ONE REQUEST PER TAG",
			len(r.calls), len(uc.QueryTags()), r.tags())
	}
	allowed := map[string]bool{}
	for _, tg := range uc.QueryTags() {
		allowed[tg] = true
	}
	for i, q := range r.calls {
		if n := len(q["tag"]); n > 1 {
			t.Errorf("call %d: %d `tag` params in ONE request — CivitAI answers that with "+
				"HTTP 400 (\"expected string, received array\")", i, n)
		}
		tg := q.Get("tag")
		if !allowed[tg] {
			t.Errorf("call %d: tag %q is not in the curated table. An unknown tag is SILENTLY "+
				"DROPPED by CivitAI and the UNFILTERED feed comes back, so the chip would "+
				"render a filter that is lying.", i, tg)
		}
		if strings.Contains(tg, ",") {
			t.Errorf("call %d: tag %q is COMMA-JOINED — the filter is silently dropped", i, tg)
		}
	}
	if !strings.Contains(sel, escText("Workflows for Illustrious · SDXL family · ControlNet & guidance")) {
		t.Errorf("the heading must name the selected use case; body = %q", firstN(sel, 600))
	}
	// And it is now the pressed chip, exactly one.
	if strings.Count(sel, `aria-pressed="true"`) != 1 {
		t.Errorf("exactly one chip may be pressed, got %d", strings.Count(sel, `aria-pressed="true"`))
	}
}

// TestChipFanOutStillMergesAndDedupesByModelID: a multi-tag use case picked from
// the chip row must still merge its per-synonym pages and dedupe by MODEL ID.
func TestChipFanOutStillMergesAndDedupesByModelID(t *testing.T) {
	uc, _ := civitai.UseCaseBySlug("controlnet")
	tags := uc.QueryTags()
	if len(tags) < 2 {
		t.Fatalf("the controlnet use case needs >=2 synonyms to exercise the fan-out, got %v", tags)
	}
	// Overlapping sets, like the live API: id 700 comes back from every synonym.
	byTag := map[string][]int{}
	for i, tg := range tags {
		byTag[tg] = []int{700, 800 + i}
	}
	r := &tagSearchReader{byTag: byTag}
	srv := illustriousServer(t, r)

	body := get(t, srv,
		"/models/1386234/related-workflows?eco=sdxl&bm=Illustrious&use=controlnet&uses=upscale,controlnet,lora").Body.String()

	if n := strings.Count(body, `href="/models/700"`); n != 1 {
		t.Errorf("model 700 is returned by every synonym and appears %d times — the merge "+
			"must dedupe by model id", n)
	}
	for i := range tags {
		if !strings.Contains(body, `href="/models/`+itoaTest(800+i)+`"`) {
			t.Errorf("model %d (unique to one synonym) is missing — the merge dropped a page",
				800+i)
		}
	}
}

// TestHandEditedUseIsStillRefused: the existing ?use= whitelist is unchanged, and
// the NEW ?uses= chip vocabulary is whitelisted too — an unknown entry must not
// become a chip whose label is caller text.
func TestHandEditedUseIsStillRefused(t *testing.T) {
	r := &tagSearchReader{byTag: map[string][]int{}, noTag: []int{9001}}
	srv := illustriousServer(t, r)

	body := get(t, srv, "/models/1386234/related-workflows?eco=sdxl&bm=Illustrious"+
		"&use=zzz-not-a-real-tag-xyz&uses=upscale,zzz-not-a-real-tag-xyz,%3Cb%3Ex%3C/b%3E").Body.String()

	if len(r.calls) != 1 {
		t.Fatalf("an unknown ?use= must collapse to ONE tag-less request, got %d: %v",
			len(r.calls), r.tags())
	}
	if tg := r.calls[0].Get("tag"); tg != "" {
		t.Errorf("a hand-edited ?use= reached the wire as tag=%q — an unknown tag is SILENTLY "+
			"DROPPED by CivitAI and the UNFILTERED feed comes back", tg)
	}
	if strings.Contains(body, "zzz-not-a-real-tag-xyz") || strings.Contains(body, "<b>x</b>") {
		t.Errorf("an unknown ?uses= entry was rendered as a chip label — the chip vocabulary "+
			"must be whitelisted too, or clicking it would hand the same text to ?use=; "+
			"body = %q", firstN(body, 1500))
	}
	// The one VALID entry still renders, so this is not green because the row vanished.
	if !strings.Contains(body, ">Upscaling<") {
		t.Errorf("the valid ?uses= entry must still render; body = %q", firstN(body, 1500))
	}
}

// TestChipRowIsAbsentWhenTheModelResolvesToNoUseCase: a control with only one
// option ("All") is a control that does nothing.
func TestChipRowIsAbsentWhenTheModelResolvesToNoUseCase(t *testing.T) {
	r := &tagSearchReader{byTag: map[string][]int{}, noTag: []int{9001}}
	r.fakeReader = fakeReader{model: lustifyShapedModel()}
	srv := newTestServer(t)
	srv.reader = r

	body := get(t, srv, "/models/573152/related-workflows?eco=sdxl&bm=SDXL+1.0").Body.String()
	if strings.Contains(body, ">All<") {
		t.Errorf("a model whose tags resolve to no use case must render no chip row at all; "+
			"body = %q", firstN(body, 1200))
	}
	if !strings.Contains(body, "Workflows for SDXL 1.0 · SDXL family") {
		t.Errorf("the section must still render its heading; body = %q", firstN(body, 600))
	}
}

// TestSelectedUseCaseWithNoResultsKeepsTheChipRow: the escape hatch. With a use
// case selected, an empty result must NOT vanish the section — the chip row is
// the only way back to All, and disappearing would strand the user on a filter
// they can neither see nor clear.
//
// With NO use case selected an empty result still renders nothing, exactly as
// before: an empty first page is not proof of an empty ecosystem.
func TestSelectedUseCaseWithNoResultsKeepsTheChipRow(t *testing.T) {
	r := &tagSearchReader{byTag: map[string][]int{}, noTag: nil}
	srv := illustriousServer(t, r)

	withUse := get(t, srv,
		"/models/1386234/related-workflows?eco=sdxl&bm=Illustrious&use=controlnet&uses=upscale,controlnet,lora").Body.String()
	if !strings.Contains(withUse, ">All<") {
		t.Errorf("an empty FILTERED result must keep the chip row so the user can get back "+
			"to All; body = %q", firstN(withUse, 1200))
	}

	r2 := &tagSearchReader{byTag: map[string][]int{}, noTag: nil}
	srv2 := illustriousServer(t, r2)
	noUse := get(t, srv2,
		"/models/1386234/related-workflows?eco=sdxl&bm=Illustrious&uses=upscale,controlnet,lora").Body.String()
	if b := strings.TrimSpace(noUse); b != "" {
		t.Errorf("an empty UNFILTERED result must still render nothing (an empty first page "+
			"is not proof there is nothing); got %q", firstN(b, 400))
	}
}

// TestChipEndpointStaysGETOnly: the picker must not turn a read-only endpoint
// into a state-changing one.
func TestChipEndpointStaysGETOnly(t *testing.T) {
	r := &tagSearchReader{byTag: map[string][]int{}, noTag: []int{9001}}
	srv := illustriousServer(t, r)
	body := get(t, srv,
		"/models/1386234/related-workflows?eco=sdxl&bm=Illustrious&uses=upscale,controlnet,lora").Body.String()

	chips := body
	if i := strings.Index(chips, "cm-cardgrid"); i > 0 {
		chips = chips[:i] // everything above the grid: heading + chip row
	}
	if strings.Contains(chips, "hx-post") {
		t.Errorf("a use-case chip POSTs — the section endpoint is GET/read-only; chips = %q",
			firstN(chips, 1500))
	}
}

// escText mirrors gomponents' text escaping for the few labels that contain an
// ampersand ("ControlNet & guidance", "Face swap & identity"). Asserting the raw
// label would silently never match.
func escText(s string) string { return strings.ReplaceAll(s, "&", "&amp;") }
