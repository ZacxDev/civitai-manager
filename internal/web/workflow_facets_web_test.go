package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// seedFacetLibrary builds a library exercising every classification path:
//
//	1 linked+tagged, Flux.1 D              → flux1 ecosystem, inpaint+upscale use cases
//	2 linked+tagged, Illustrious           → sdxl ecosystem, detailer use case
//	3 stored SD 1.5, linked version Flux   → TWO ecosystems (multi-membership)
//	4 authored, no base model, no link     → Unclassified in BOTH dimensions
//	5 scanned, base model Wan, no link     → wan ecosystem, Unclassified use case
//	6 linked but model has ONLY stopwords  → Unclassified use case
func seedFacetLibrary(t *testing.T, srv *Server) {
	t.Helper()
	ctx := context.Background()

	put := func(id int, name, raw string) {
		if err := srv.store.PutModelCache(id, name, []byte(raw)); err != nil {
			t.Fatalf("cache model %d: %v", id, err)
		}
	}
	put(10, "Flux Megapack",
		`{"id":10,"name":"Flux Megapack","tags":["tool","inpaint","upscaler","comfyui"],
		  "modelVersions":[{"id":100,"name":"v1","baseModel":"Flux.1 D"}]}`)
	put(20, "Illu Detailer",
		`{"id":20,"name":"Illu Detailer","tags":["adetailer","illustrious","workflow"],
		  "modelVersions":[{"id":200,"name":"v1","baseModel":"Illustrious"}]}`)
	put(30, "Cross Family",
		`{"id":30,"name":"Cross Family","tags":["controlnet"],
		  "modelVersions":[{"id":300,"name":"v1","baseModel":"Flux.1 D"}]}`)
	put(60, "Only Noise",
		`{"id":60,"name":"Only Noise","tags":["tool","comfyui","workflow","workflows","comfy"],
		  "modelVersions":[{"id":600,"name":"v1","baseModel":"Qwen"}]}`)

	seed := []store.Workflow{
		{Name: "wf-flux", Format: store.WorkflowFormatAPI, Graph: "{}",
			Source: store.WorkflowSourceCivitai, ModelID: intp(10), VersionID: intp(100)},
		{Name: "wf-illu", Format: store.WorkflowFormatAPI, Graph: "{}",
			Source: store.WorkflowSourceCivitai, ModelID: intp(20), VersionID: intp(200)},
		{Name: "wf-cross", Format: store.WorkflowFormatAPI, Graph: "{}", BaseModel: "SD 1.5",
			Source: store.WorkflowSourceCivitai, ModelID: intp(30), VersionID: intp(300)},
		{Name: "wf-authored", Format: store.WorkflowFormatAPI, Graph: "{}",
			Source: store.WorkflowSourceAuthored},
		{Name: "wf-scanned", Format: store.WorkflowFormatUI, Graph: "{}", BaseModel: "Wan Video 2.2 I2V-A14B",
			Source: store.WorkflowSourceScanned},
		{Name: "wf-noisy", Format: store.WorkflowFormatAPI, Graph: "{}",
			Source: store.WorkflowSourceCivitai, ModelID: intp(60), VersionID: intp(600)},
	}
	for i := range seed {
		if _, err := srv.store.InsertWorkflow(ctx, &seed[i]); err != nil {
			t.Fatalf("seed workflow %d: %v", i, err)
		}
	}
}

func libraryWorkflowsBody(t *testing.T, srv *Server, query string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	target := "/library?tab=workflows"
	if query != "" {
		target += "&" + query
	}
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d", target, rec.Code)
	}
	return rec.Body.String()
}

// ---------------------------------------------------------------------------
// Classification
// ---------------------------------------------------------------------------

// TestClassifyLocalWorkflows pins the classification rules directly, including
// the two that are easy to get silently wrong: multi-membership across a
// workflow's own base model AND its linked version's, and stopword-only tags
// producing NO use case rather than a junk one.
func TestClassifyLocalWorkflows(t *testing.T) {
	srv := newWorkflowServer(t)
	seedFacetLibrary(t, srv)

	wfs, err := srv.store.ListWorkflows(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	byName := map[string]workflowClassification{}
	cls := classifyWorkflows(wfs, srv.workflowResolver())
	for i, wf := range wfs {
		byName[wf.Name] = cls[i]
	}

	check := func(name string, wantEco, wantUse []string) {
		t.Helper()
		cl, ok := byName[name]
		if !ok {
			t.Fatalf("%s not in library", name)
		}
		var gotEco, gotUse []string
		for _, e := range cl.Ecosystems {
			gotEco = append(gotEco, e.Slug)
		}
		for _, u := range cl.UseCases {
			gotUse = append(gotUse, u.Slug)
		}
		if strings.Join(gotEco, ",") != strings.Join(wantEco, ",") {
			t.Errorf("%s ecosystems = %v, want %v", name, gotEco, wantEco)
		}
		if strings.Join(gotUse, ",") != strings.Join(wantUse, ",") {
			t.Errorf("%s use cases = %v, want %v", name, gotUse, wantUse)
		}
	}

	check("wf-flux", []string{"flux1"}, []string{"inpaint", "upscale"})
	check("wf-illu", []string{"sdxl"}, []string{"detailer"})
	// MULTI-MEMBERSHIP: stored SD 1.5 + linked version Flux.1 D → BOTH families,
	// in table order. Picking one arbitrarily would be the bug.
	check("wf-cross", []string{"flux1", "sd15"}, []string{"controlnet"})
	// Authored: nothing at all. NOT guessed at.
	check("wf-authored", nil, nil)
	// Scanned with a base model but no CivitAI link → ecosystem yes, use case no.
	check("wf-scanned", []string{"wan"}, nil)
	// Linked to a model whose tags are ALL stopwords → still no use case.
	check("wf-noisy", []string{"qwen"}, nil)
}

// TestClassifierMemoizesModelLookups — a library of N workflows linked to M
// models must read the model cache M times, not N. Without the memo a large
// library re-reads the same row on every card.
func TestClassifierMemoizesModelLookups(t *testing.T) {
	var lookups int
	res := workflowResolver{cachedModel: func(id int) (string, []byte, bool) {
		lookups++
		return "M", []byte(`{"id":1,"tags":["inpaint"],"modelVersions":[{"id":1,"baseModel":"Flux.1 D"}]}`), true
	}}
	wfs := make([]store.Workflow, 0, 20)
	for i := 0; i < 20; i++ {
		wfs = append(wfs, store.Workflow{Name: "w", ModelID: intp(7), VersionID: intp(1)})
	}
	classifyWorkflows(wfs, res)
	if lookups != 1 {
		t.Errorf("model cache read %d times for 20 workflows on ONE model, want 1", lookups)
	}
}

// TestClassifierDegradesOnMalformedCache — a corrupt cache row must yield
// Unclassified, never a panic or a page error.
func TestClassifierDegradesOnMalformedCache(t *testing.T) {
	res := workflowResolver{cachedModel: func(int) (string, []byte, bool) {
		return "M", []byte(`{"tags": "not-an-array"`), true
	}}
	cls := classifyWorkflows([]store.Workflow{{Name: "w", ModelID: intp(1), VersionID: intp(1)}}, res)
	if len(cls[0].Ecosystems) != 0 || len(cls[0].UseCases) != 0 {
		t.Errorf("malformed cache must degrade to Unclassified, got %+v", cls[0])
	}
}

// ---------------------------------------------------------------------------
// Counts + the Unclassified bucket
// ---------------------------------------------------------------------------

// TestUnclassifiedBucketIsVisibleWithACorrectCount is the honesty requirement:
// tagless workflows must be surfaced as a first-class bucket with an obvious
// count, never silently dropped.
func TestUnclassifiedBucketIsVisibleWithACorrectCount(t *testing.T) {
	srv := newWorkflowServer(t)
	seedFacetLibrary(t, srv)
	body := libraryWorkflowsBody(t, srv, "")

	if !strings.Contains(body, "Browse your workflows") {
		t.Fatalf("the local browse-by bar is missing entirely:\n%s", body[:min(len(body), 600)])
	}
	// Ecosystem Unclassified = 1 (wf-authored only — the others all have a base model).
	if !strings.Contains(body, `href="/library?eco=none&amp;tab=workflows"`) {
		t.Error("the Unclassified ECOSYSTEM bucket must be browsable")
	}
	// Use-case Unclassified = 3 (wf-authored, wf-scanned, wf-noisy).
	if !strings.Contains(body, `href="/library?tab=workflows&amp;use=none"`) {
		t.Error("the Unclassified USE CASE bucket must be browsable")
	}
	if !strings.Contains(body, ">Unclassified<") {
		t.Error("the Unclassified chip must be labelled")
	}
	// The count must be rendered and correct: 3 workflows have no use case.
	if !strings.Contains(body, "(3)") {
		t.Errorf("the Unclassified use-case count (3) is not visible:\n%s", body[:min(len(body), 2500)])
	}
	// And the explanation of WHY, so the bucket does not look like a bug. Asserted
	// as the note's STATE (its per-reason counts), not as prose: this assertion
	// used to grep for the phrase "cannot have one", which stayed green for the
	// whole time that sentence was telling most users something false.
	// TestUnclassifiedNoteSplitsTheCountByReason pins the numbers.
	if _, _, ok := unclassifiedNoteCounts(body); !ok {
		t.Error("the panel must explain why unclassified workflows have no use case")
	}
	// With NO facet selected every workflow is listed — including the unclassified.
	for _, name := range []string{"wf-flux", "wf-illu", "wf-cross", "wf-authored", "wf-scanned", "wf-noisy"} {
		if !strings.Contains(body, name) {
			t.Errorf("unfiltered library must list %s — filtering is opt-in", name)
		}
	}
}

// unclassifiedNoteCounts extracts the Unclassified note's two per-reason counts
// from rendered markup.
//
// It requires BOTH attributes inside ONE <p …> open tag. That is deliberate:
// checking the two substrings independently would pass when they landed on
// DIFFERENT elements — this repo has already shipped exactly that bug, where a
// test for " popover" was satisfied by another element's popovertarget.
var unclassifiedNoteRe = regexp.MustCompile(
	`<p[^>]*\sdata-unclassified-unlinked="(\d+)"[^>]*\sdata-unclassified-linked="(\d+)"[^>]*>`)

func unclassifiedNoteCounts(body string) (unlinked, linked int, ok bool) {
	m := unclassifiedNoteRe.FindStringSubmatch(body)
	if m == nil {
		return 0, 0, false
	}
	unlinked, _ = strconv.Atoi(m[1])
	linked, _ = strconv.Atoi(m[2])
	return unlinked, linked, true
}

// TestUnclassifiedNoteSplitsTheCountByReason guards the note's HONESTY.
//
// The note used to attribute the ENTIRE Unclassified use-case bucket to "no
// CivitAI link". That is one of two reasons and — measured on a real
// 71-workflow library — the MINORITY one: 58 unclassified, of which 39 were
// linked, their model's tags simply matching no curated use case. The sentence
// was false about two thirds of the number printed beside it.
//
// It asserts STATE (the two counts the note renders), never prose. The previous
// assertion here grepped for the phrase "cannot have one" and stayed green for
// the entire time that sentence was wrong — a wording check cannot see a lie
// told in the right words.
func TestUnclassifiedNoteSplitsTheCountByReason(t *testing.T) {
	srv := newWorkflowServer(t)
	seedFacetLibrary(t, srv)

	// PRECONDITION — the fixture must genuinely contain BOTH reasons, or it
	// cannot express the bug. A library whose unclassified workflows are ALL
	// unlinked is precisely the case the old false sentence was TRUE for: such a
	// fixture would pass forever, before and after the fix.
	wfs, err := srv.store.ListWorkflows(context.Background())
	if err != nil {
		t.Fatalf("list workflows: %v", err)
	}
	c := countWorkflowFacets(classifyWorkflows(wfs, srv.workflowResolver()))
	if c.UseNoneLinked == 0 {
		t.Fatalf("fixture cannot express the bug: no LINKED workflow is unclassified "+
			"(UseNone=%d, UseNoneLinked=%d) — wf-noisy must stay linked to stopword-only model 60",
			c.UseNone, c.UseNoneLinked)
	}
	if c.useNoneUnlinked() == 0 {
		t.Fatalf("fixture cannot discriminate the two reasons: no UNLINKED workflow is unclassified "+
			"(UseNone=%d, UseNoneLinked=%d)", c.UseNone, c.UseNoneLinked)
	}

	// LITERALS, never derived from countWorkflowFacets: an expectation computed
	// from the same source as its subject moves with it, and no mutation can
	// separate the two. The two values are also deliberately DISTINCT (2 vs 1),
	// so swapping the labels is detectable — equal values could not tell the
	// attributes apart.
	const (
		wantUnlinked = 2 // wf-authored, wf-scanned — no CivitAI link at all
		wantLinked   = 1 // wf-noisy — linked to model 60, whose tags are all stopwords
		wantTotal    = 3
	)

	body := libraryWorkflowsBody(t, srv, "")
	unlinked, linked, ok := unclassifiedNoteCounts(body)
	if !ok {
		t.Fatalf("the Unclassified note rendered without its per-reason counts")
	}
	if unlinked != wantUnlinked || linked != wantLinked {
		t.Errorf("unclassified note = %d unlinked / %d linked, want %d / %d — a workflow that IS "+
			"linked but matched no use case must not be reported as having no CivitAI link",
			unlinked, linked, wantUnlinked, wantLinked)
	}
	if unlinked+linked != wantTotal {
		t.Errorf("the note's two reasons sum to %d but the Unclassified bucket holds %d — every "+
			"unclassified workflow must be accounted for by exactly one reason", unlinked+linked, wantTotal)
	}
}

// TestUnclassifiedNoteOmitsAReasonWithNoMembers — when every unclassified
// workflow shares ONE reason, the other reason must not be rendered as a "0"
// clause, and an empty bucket must render no note at all.
func TestUnclassifiedNoteOmitsAReasonWithNoMembers(t *testing.T) {
	if unclassifiedUseCaseNote(workflowFacetCounts{}) != nil {
		t.Error("an empty Unclassified bucket must render no note at all")
	}
	for _, tc := range []struct {
		name                     string
		counts                   workflowFacetCounts
		wantUnlinked, wantLinked int
	}{
		{"every unclassified workflow is unlinked", workflowFacetCounts{UseNone: 4}, 4, 0},
		{"every unclassified workflow is linked", workflowFacetCounts{UseNone: 4, UseNoneLinked: 4}, 0, 4},
		{"a single unclassified workflow", workflowFacetCounts{UseNone: 1}, 1, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := renderString(t, unclassifiedUseCaseNote(tc.counts))
			unlinked, linked, ok := unclassifiedNoteCounts(out)
			if !ok {
				t.Fatalf("note rendered without its counts: %s", out)
			}
			if unlinked != tc.wantUnlinked || linked != tc.wantLinked {
				t.Errorf("counts = %d unlinked / %d linked, want %d / %d",
					unlinked, linked, tc.wantUnlinked, tc.wantLinked)
			}
			// The visible sentence must carry no zero-count clause. Attribute values
			// render as ="0", so this can only match prose.
			if strings.Contains(out, " 0 ") {
				t.Errorf("a reason with no members must not be rendered as a 0 clause: %s", out)
			}
		})
	}
}

func TestCountWorkflowFacetsCountsMultiMembershipInEveryBucket(t *testing.T) {
	srv := newWorkflowServer(t)
	seedFacetLibrary(t, srv)
	wfs, _ := srv.store.ListWorkflows(context.Background())
	c := countWorkflowFacets(classifyWorkflows(wfs, srv.workflowResolver()))

	if c.Total != 6 {
		t.Fatalf("total = %d, want 6", c.Total)
	}
	// wf-flux AND wf-cross are both flux1 → the multi-membership workflow counts
	// in flux1 as well as sd15. Bucket counts deliberately do NOT sum to Total.
	for slug, want := range map[string]int{"flux1": 2, "sd15": 1, "sdxl": 1, "wan": 1, "qwen": 1} {
		if c.Eco[slug] != want {
			t.Errorf("ecosystem %s count = %d, want %d", slug, c.Eco[slug], want)
		}
	}
	if c.EcoNone != 1 {
		t.Errorf("EcoNone = %d, want 1 (only the authored workflow)", c.EcoNone)
	}
	if c.UseNone != 3 {
		t.Errorf("UseNone = %d, want 3 (authored + scanned + stopword-only)", c.UseNone)
	}
	for slug, want := range map[string]int{"inpaint": 1, "upscale": 1, "detailer": 1, "controlnet": 1} {
		if c.Use[slug] != want {
			t.Errorf("use case %s count = %d, want %d", slug, c.Use[slug], want)
		}
	}
}

// ---------------------------------------------------------------------------
// Filtering
// ---------------------------------------------------------------------------

// TestLibraryFacetFiltering — every bucket actually narrows the list, and the
// multi-membership workflow shows up under BOTH of its ecosystems.
func TestLibraryFacetFiltering(t *testing.T) {
	srv := newWorkflowServer(t)
	seedFacetLibrary(t, srv)

	cases := []struct {
		query   string
		present []string
		absent  []string
	}{
		// Multi-membership: wf-cross is reachable under flux1 AND sd15.
		{"eco=flux1", []string{"wf-flux", "wf-cross"}, []string{"wf-illu", "wf-authored", "wf-scanned"}},
		{"eco=sd15", []string{"wf-cross"}, []string{"wf-flux", "wf-illu"}},
		{"eco=sdxl", []string{"wf-illu"}, []string{"wf-flux", "wf-cross"}},
		{"use=inpaint", []string{"wf-flux"}, []string{"wf-illu", "wf-cross", "wf-authored"}},
		{"use=detailer", []string{"wf-illu"}, []string{"wf-flux"}},
		// The Unclassified buckets.
		{"eco=none", []string{"wf-authored"}, []string{"wf-flux", "wf-scanned"}},
		{"use=none", []string{"wf-authored", "wf-scanned", "wf-noisy"}, []string{"wf-flux", "wf-illu", "wf-cross"}},
		// Both dimensions AND together.
		{"eco=flux1&use=controlnet", []string{"wf-cross"}, []string{"wf-flux"}},
		{"eco=wan&use=none", []string{"wf-scanned"}, []string{"wf-authored", "wf-flux"}},
	}
	for _, tc := range cases {
		t.Run(tc.query, func(t *testing.T) {
			body := libraryWorkflowsBody(t, srv, tc.query)
			// Look only at the LIST region: the chip bar names buckets, not workflows,
			// but the "Browse your workflows" card sits above the list.
			for _, name := range tc.present {
				if !strings.Contains(body, `data-name="`+name+`"`) {
					t.Errorf("%s should be listed under %s", name, tc.query)
				}
			}
			for _, name := range tc.absent {
				if strings.Contains(body, `data-name="`+name+`"`) {
					t.Errorf("%s must NOT be listed under %s", name, tc.query)
				}
			}
		})
	}
}

// TestLibraryFacetChipsShowSelectedStateAndClear
func TestLibraryFacetChipsShowSelectedStateAndClear(t *testing.T) {
	srv := newWorkflowServer(t)
	seedFacetLibrary(t, srv)
	body := libraryWorkflowsBody(t, srv, "eco=flux1&use=inpaint")

	if n := strings.Count(body, "cm-facet-chip-on"); n != 2 {
		t.Errorf("want 2 selected chips, got %d", n)
	}
	if !strings.Contains(body, `aria-current="true"`) {
		t.Error("a selected chip must carry aria-current, not colour alone")
	}
	// Clicking the selected ecosystem chip clears only it (keeps ?use=).
	if !strings.Contains(body, `href="/library?tab=workflows&amp;use=inpaint"`) {
		t.Error("the selected ecosystem chip must toggle itself off, keeping the use case")
	}
	// Clicking the selected use-case chip clears only it (keeps ?eco=).
	if !strings.Contains(body, `href="/library?eco=flux1&amp;tab=workflows"`) {
		t.Error("the selected use-case chip must toggle itself off, keeping the ecosystem")
	}
	// The "All" chips clear their dimension entirely.
	if !strings.Contains(body, `href="/library?tab=workflows&amp;use=inpaint"`) {
		t.Error("the All-ecosystems chip must clear only the ecosystem")
	}
}

// TestLibraryFacetEmptyStateIsGuided — a filter matching nothing must not look
// like an empty library.
func TestLibraryFacetEmptyStateIsGuided(t *testing.T) {
	srv := newWorkflowServer(t)
	seedFacetLibrary(t, srv)
	// wf-illu is the only sdxl workflow and its use case is detailer, so
	// sdxl × inpaint is a real, reachable empty intersection.
	body := libraryWorkflowsBody(t, srv, "eco=sdxl&use=inpaint")

	if !strings.Contains(body, "No SDXL family · Inpainting workflows in your library") {
		t.Errorf("empty state must name the selection:\n%s", body[:min(len(body), 2000)])
	}
	for _, want := range []string{
		"Clear filters",
		`href="/library?tab=workflows"`,
		"Find these on CivitAI",
		// …and that jump carries the SAME facets to the remote Discover page.
		"/workflows/discover?eco=sdxl&amp;use=inpaint",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("guided local empty state missing %q", want)
		}
	}
	// The generic "no workflows at all" state must NOT be what a filtered view shows.
	if strings.Contains(body, "No workflows yet") {
		t.Error("a filtered empty view must not claim the library is empty")
	}
}

// TestLibraryEmptyLibraryHasNoFacetBar — an empty library gets its existing
// onboarding empty state, not a bar of zero-count chips.
func TestLibraryEmptyLibraryHasNoFacetBar(t *testing.T) {
	srv := newWorkflowServer(t)
	body := libraryWorkflowsBody(t, srv, "")
	if strings.Contains(body, "Browse your workflows") {
		t.Error("an empty library must not render a facet bar")
	}
	if !strings.Contains(body, "No workflows yet") {
		t.Error("an empty library must keep its onboarding empty state")
	}
}

// TestLibraryFacetChipsOnlyForPopulatedBuckets — 20 dead filters would be worse
// than none. Unclassified is the exception, always shown when populated.
func TestLibraryFacetChipsOnlyForPopulatedBuckets(t *testing.T) {
	srv := newWorkflowServer(t)
	seedFacetLibrary(t, srv)
	body := libraryWorkflowsBody(t, srv, "")

	if !strings.Contains(body, ">Flux.1<") || !strings.Contains(body, ">SDXL family<") {
		t.Error("populated ecosystem buckets must have chips")
	}
	for _, absent := range []string{">Stable Cascade<", ">HiDream<", ">Mochi<", ">Face swap & identity<"} {
		if strings.Contains(body, absent) {
			t.Errorf("empty bucket %s must not render a chip", absent)
		}
	}
}

// ---------------------------------------------------------------------------
// Normalization
// ---------------------------------------------------------------------------

// TestLibraryFacetNormalizationRejectsUnknownValues — same whitelist-only rule as
// the Discover page. An unknown value is IGNORED, so the view degrades to
// unfiltered rather than silently matching nothing.
func TestLibraryFacetNormalizationRejectsUnknownValues(t *testing.T) {
	for _, bad := range []string{
		"nope", "Illustrious", "inpaint,upscale", "NONE ", strings.Repeat("q", 4096),
		"none'; DROP TABLE--", "../../etc/passwd",
	} {
		f := normalizeLibraryWorkflowFacets(url.Values{"eco": {bad}, "use": {bad}})
		if bad == "NONE " {
			// Trimmed + lowercased, this IS the reserved Unclassified value.
			if !f.EcoNone || !f.UseNone {
				t.Errorf("%q should normalize to the Unclassified bucket", bad)
			}
			continue
		}
		if f.any() {
			t.Errorf("unknown facet %q was accepted: %+v", bad, f)
		}
	}
	// The good path still works.
	f := normalizeLibraryWorkflowFacets(url.Values{"eco": {"FLUX1"}, "use": {" inpaint "}})
	if f.Eco == nil || f.Eco.Slug != "flux1" || f.Use == nil || f.Use.Slug != "inpaint" {
		t.Errorf("valid facets did not resolve: %+v", f)
	}
}

// TestUnknownLibraryFacetShowsEverything — the consequence of "ignore": a bad
// link shows the whole library with no chip lit, never a mysteriously empty page.
func TestUnknownLibraryFacetShowsEverything(t *testing.T) {
	srv := newWorkflowServer(t)
	seedFacetLibrary(t, srv)
	body := libraryWorkflowsBody(t, srv, "eco=bogus&use=bogus")
	for _, name := range []string{"wf-flux", "wf-authored", "wf-scanned"} {
		if !strings.Contains(body, `data-name="`+name+`"`) {
			t.Errorf("an unknown facet must degrade to unfiltered; %s missing", name)
		}
	}
	// Only the two "All" chips are lit.
	if n := strings.Count(body, "cm-facet-chip-on"); n != 2 {
		t.Errorf("an ignored facet must leave only the two All chips selected, got %d", n)
	}
}

// TestLibraryAndDiscoverShareOneVocabulary — the whole point of the single table.
// Every ecosystem/use-case label the library can render must be a label the
// Discover page can render, and vice versa.
func TestLibraryAndDiscoverShareOneVocabulary(t *testing.T) {
	for _, e := range civitai.Ecosystems() {
		if _, ok := civitai.EcosystemBySlug(e.Slug); !ok {
			t.Errorf("ecosystem %q is not resolvable by its own slug", e.Slug)
		}
		if libraryWorkflowHref(libraryWorkflowFacets{}, "eco", e.Slug) == "/library?tab=workflows" {
			t.Errorf("ecosystem %q produced a no-op library href", e.Slug)
		}
		if discoverHref("", "Most Downloaded", "Month", e.Slug, "") == "/workflows/discover" {
			t.Errorf("ecosystem %q produced a no-op discover href", e.Slug)
		}
	}
	for _, u := range civitai.UseCases() {
		if _, ok := civitai.UseCaseBySlug(u.Slug); !ok {
			t.Errorf("use case %q is not resolvable by its own slug", u.Slug)
		}
	}
	// The reserved local Unclassified value must NOT be a taxonomy slug — if it
	// ever became one it could be forwarded to civitai.com as a baseModel/tag.
	if _, ok := civitai.EcosystemBySlug(facetUnclassified); ok {
		t.Error("the reserved Unclassified value collides with an ecosystem slug")
	}
	if _, ok := civitai.UseCaseBySlug(facetUnclassified); ok {
		t.Error("the reserved Unclassified value collides with a use-case slug")
	}
}
