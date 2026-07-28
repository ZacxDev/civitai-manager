package civitai

import (
	"regexp"
	"strings"
	"testing"
)

var slugRe = regexp.MustCompile(`^[a-z0-9]+$`)

// TestEveryEcosystemMapsToAtLeastOneBaseModel — an ecosystem with no baseModel
// values would render a browsable tile whose request carries no filter at all,
// i.e. the unfiltered feed dressed up as a filter.
func TestEveryEcosystemMapsToAtLeastOneBaseModel(t *testing.T) {
	for _, e := range Ecosystems() {
		if len(e.BaseModels) == 0 {
			t.Errorf("ecosystem %q (%s) maps to no baseModel values", e.Slug, e.Label)
		}
		for _, bm := range e.BaseModels {
			if strings.TrimSpace(bm) == "" {
				t.Errorf("ecosystem %q has an empty baseModel value", e.Slug)
			}
		}
	}
}

// TestEveryUseCaseMapsToAtLeastOneTag — same failure mode as above: a use case
// with no tags would send no `tag=` param and return everything.
func TestEveryUseCaseMapsToAtLeastOneTag(t *testing.T) {
	for _, u := range UseCases() {
		if len(u.Tags) == 0 {
			t.Errorf("use case %q (%s) maps to no tags", u.Slug, u.Label)
		}
		for _, tag := range u.Tags {
			if strings.TrimSpace(tag) == "" {
				t.Errorf("use case %q has an empty tag", u.Slug)
			}
		}
	}
}

// TestUseCaseTagsAreLowercase pins the invariant the lookup relies on:
// UseCasesForTags lowercases its INPUT and compares against the table verbatim,
// so an uppercase table entry would silently never match a local workflow's tags.
func TestUseCaseTagsAreLowercase(t *testing.T) {
	for _, u := range UseCases() {
		for _, tag := range u.Tags {
			if tag != strings.ToLower(tag) {
				t.Errorf("use case %q tag %q is not lowercase — UseCasesForTags would never match it", u.Slug, tag)
			}
		}
	}
}

// TestUseCaseTagsAreNotStopwords is the load-bearing one: a stopword tag
// (`tool`, `comfyui`, …) is on nearly every Workflows model, so filtering by it
// returns the whole feed. Live-verified: CivitAI silently IGNORES an unmatched
// tag and an over-broad tag filters nothing, so this would be an invisible lie.
func TestUseCaseTagsAreNotStopwords(t *testing.T) {
	for _, u := range UseCases() {
		for _, tag := range u.Tags {
			if IsStopwordTag(tag) {
				t.Errorf("use case %q maps to stopword tag %q — that tag is on nearly every Workflows model and filters nothing", u.Slug, tag)
			}
		}
	}
}

// TestStopwordListCoversTheKnownNoise pins the tags the product decision named
// explicitly, so nobody quietly drops one.
func TestStopwordListCoversTheKnownNoise(t *testing.T) {
	for _, tag := range []string{"tool", "comfyui", "comfy", "workflow", "workflows"} {
		if !IsStopwordTag(tag) {
			t.Errorf("%q must be a stopword", tag)
		}
	}
	// Case-insensitivity: CivitAI tags arrive in mixed case.
	if !IsStopwordTag("  ComfyUI  ") {
		t.Error("stopword matching must be case-insensitive and trim")
	}
	// And a real signal tag must NOT be a stopword.
	for _, tag := range []string{"inpaint", "upscaler", "controlnet"} {
		if IsStopwordTag(tag) {
			t.Errorf("%q must NOT be a stopword", tag)
		}
	}
}

// TestNoDuplicateSlugsOrLabels — slugs are URL-addressable facet values and
// labels are the user-visible names; a duplicate of either makes one row
// unreachable or ambiguous.
func TestNoDuplicateSlugsOrLabels(t *testing.T) {
	seenSlug, seenLabel := map[string]string{}, map[string]string{}
	for _, e := range Ecosystems() {
		if prev, ok := seenSlug[e.Slug]; ok {
			t.Errorf("duplicate ecosystem slug %q (also %q)", e.Slug, prev)
		}
		seenSlug[e.Slug] = e.Label
		if prev, ok := seenLabel[strings.ToLower(e.Label)]; ok {
			t.Errorf("duplicate ecosystem label %q (also slug %q)", e.Label, prev)
		}
		seenLabel[strings.ToLower(e.Label)] = e.Slug
		if !slugRe.MatchString(e.Slug) {
			t.Errorf("ecosystem slug %q is not URL-safe lowercase-alphanumeric", e.Slug)
		}
	}
	seenSlug, seenLabel = map[string]string{}, map[string]string{}
	for _, u := range UseCases() {
		if prev, ok := seenSlug[u.Slug]; ok {
			t.Errorf("duplicate use-case slug %q (also %q)", u.Slug, prev)
		}
		seenSlug[u.Slug] = u.Label
		if prev, ok := seenLabel[strings.ToLower(u.Label)]; ok {
			t.Errorf("duplicate use-case label %q (also slug %q)", u.Label, prev)
		}
		seenLabel[strings.ToLower(u.Label)] = u.Slug
		if !slugRe.MatchString(u.Slug) {
			t.Errorf("use-case slug %q is not URL-safe lowercase-alphanumeric", u.Slug)
		}
	}
}

// TestEcosystemBaseModelsArePartitioned — the same CivitAI baseModel value may
// belong to only ONE ecosystem. Overlapping rows would make a single model show
// up under two families for a reason that has nothing to do with the model, and
// would double-count it in the local library grouping. Legitimate
// multi-membership comes from a MODEL having several versions with different
// baseModels (see TestModelWithSeveralBaseModelsAppearsUnderEachEcosystem).
func TestEcosystemBaseModelsArePartitioned(t *testing.T) {
	owner := map[string]string{}
	for _, e := range Ecosystems() {
		for _, bm := range e.BaseModels {
			k := strings.ToLower(bm)
			if prev, ok := owner[k]; ok {
				t.Errorf("baseModel %q is claimed by both %q and %q — the table must be a partition", bm, prev, e.Slug)
			}
			owner[k] = e.Slug
		}
	}
}

// TestEcosystemKindsAreKnown keeps the landing-page grouping total: an unknown
// Kind would render a tile under no heading at all.
func TestEcosystemKindsAreKnown(t *testing.T) {
	ok := map[string]bool{
		EcosystemKindImage: true, EcosystemKindVideo: true,
		EcosystemKindAudio: true, EcosystemKindOther: true,
	}
	for _, e := range Ecosystems() {
		if !ok[e.Kind] {
			t.Errorf("ecosystem %q has unknown kind %q", e.Slug, e.Kind)
		}
	}
}

// TestEcosystemTableCoversTheLiveHarvestVocabulary asserts the curated table
// actually covers the baseModel values a live 600-model `types=Workflows`
// harvest returned on 2026-07-28. Without this, the table drifts into covering
// only what someone remembered, and real workflows fall into Unclassified.
func TestEcosystemTableCoversTheLiveHarvestVocabulary(t *testing.T) {
	// Verbatim from the live harvest (see the comment block in taxonomy.go).
	observed := []string{
		"Flux.1 D", "Other", "ZImageTurbo", "SDXL 1.0", "SD 1.5", "Illustrious",
		"Wan Video 2.2 I2V-A14B", "Flux.2 Klein 9B", "Qwen", "Wan Video",
		"Hunyuan Video", "Wan Video 14B t2v", "LTXV 2.3", "Anima", "Krea 2",
		"Wan Video 14B i2v 720p", "LTXV", "LTXV2", "Pony", "Stable Cascade",
		"Wan Video 2.2 T2V-A14B", "Flux.1 S", "Wan Video 14B i2v 480p",
		"Flux.1 Kontext", "ZImageBase", "Flux.2 D", "Flux.2 Klein 9B-base",
		"Flux.2 Klein 4B", "SDXL Lightning", "Wan Video 2.2 TI2V-5B",
		"Wan Video 1.3B t2v", "ACE Audio", "SD 1.5 LCM", "HiDream", "SDXL 0.9",
		"SVD XT", "PixArt E", "SDXL Hyper", "Pony V7", "SDXL 1.0 LCM", "Chroma",
		"Ideogram 4.0", "Mochi", "Flux.2 Klein 4B-base", "Ernie", "Flux.1 Krea",
	}
	for _, bm := range observed {
		if got := EcosystemsForBaseModel(bm); len(got) == 0 {
			t.Errorf("live-observed baseModel %q maps to no ecosystem — add it to the table", bm)
		}
	}
}

func TestEcosystemsForBaseModelIsCaseAndSpaceInsensitive(t *testing.T) {
	for _, in := range []string{"illustrious", "  Illustrious  ", "ILLUSTRIOUS"} {
		got := EcosystemsForBaseModel(in)
		if len(got) != 1 || got[0].Slug != "sdxl" {
			t.Errorf("EcosystemsForBaseModel(%q) = %v, want the sdxl family", in, got)
		}
	}
	if got := EcosystemsForBaseModel(""); got != nil {
		t.Errorf("empty baseModel must map to nothing, got %v", got)
	}
	if got := EcosystemsForBaseModel("NotARealBaseModel"); got != nil {
		t.Errorf("unknown baseModel must map to nothing, got %v", got)
	}
}

// TestModelWithSeveralBaseModelsAppearsUnderEachEcosystem is the multi-membership
// requirement stated as a test. The input is a REAL live-observed collection
// (model 128556 "ComfyUI inpaint workflow" spans Anima/Other/SD 1.5/SDXL 1.0).
func TestModelWithSeveralBaseModelsAppearsUnderEachEcosystem(t *testing.T) {
	got := EcosystemsForBaseModels([]string{"Anima", "Other", "SD 1.5", "SDXL 1.0"})
	want := map[string]bool{"anima": true, "other": true, "sd15": true, "sdxl": true}
	if len(got) != len(want) {
		t.Fatalf("got %d ecosystems %v, want %d", len(got), slugsOf(got), len(want))
	}
	for _, e := range got {
		if !want[e.Slug] {
			t.Errorf("unexpected ecosystem %q", e.Slug)
		}
	}
	// Two versions in the SAME family must NOT double-count.
	if got := EcosystemsForBaseModels([]string{"Illustrious", "Pony", "SDXL 1.0"}); len(got) != 1 || got[0].Slug != "sdxl" {
		t.Errorf("same-family baseModels must dedupe to one ecosystem, got %v", slugsOf(got))
	}
	// Order is TABLE order, not input order — a shared link must render stably.
	a := slugsOf(EcosystemsForBaseModels([]string{"SD 1.5", "Flux.1 D", "ZImageTurbo"}))
	b := slugsOf(EcosystemsForBaseModels([]string{"ZImageTurbo", "SD 1.5", "Flux.1 D"}))
	if strings.Join(a, ",") != strings.Join(b, ",") {
		t.Errorf("ecosystem order depends on input order: %v vs %v", a, b)
	}
	if strings.Join(a, ",") != "flux1,zimage,sd15" {
		t.Errorf("ecosystem order = %v, want table order flux1,zimage,sd15", a)
	}
}

func slugsOf(es []Ecosystem) []string {
	out := make([]string, 0, len(es))
	for _, e := range es {
		out = append(out, e.Slug)
	}
	return out
}

func TestEcosystemBySlugRejectsAnythingNotWhitelisted(t *testing.T) {
	if e, ok := EcosystemBySlug("SDXL"); !ok || e.Slug != "sdxl" {
		t.Errorf("EcosystemBySlug(SDXL) = %v,%v — must resolve case-insensitively", e.Slug, ok)
	}
	for _, bad := range []string{
		"", "   ", "nope", "sdxl'; DROP TABLE--", "../../etc/passwd",
		"Illustrious",                // a baseModel VALUE is not a slug
		strings.Repeat("a", 4096),    // absurd length
		"sdxl\nX-Injected: 1",        // header injection shape
		"sdxl&baseModels=Everything", // param-smuggling shape
		"%73dxl",                     // pre-encoded
	} {
		if _, ok := EcosystemBySlug(bad); ok {
			t.Errorf("EcosystemBySlug(%q) resolved — hostile/unknown values must be rejected", bad)
		}
	}
}

func TestUseCaseBySlugRejectsAnythingNotWhitelisted(t *testing.T) {
	if u, ok := UseCaseBySlug("  Inpaint  "); !ok || u.Slug != "inpaint" {
		t.Errorf("UseCaseBySlug(Inpaint) = %v,%v", u.Slug, ok)
	}
	for _, bad := range []string{
		"", "nope", "tool", "comfyui", // a stopword is not a use case
		"inpaint,upscale",         // the comma form CivitAI silently ignores
		strings.Repeat("z", 4096), //
		"inpaint&tag=anything",
	} {
		if _, ok := UseCaseBySlug(bad); ok {
			t.Errorf("UseCaseBySlug(%q) resolved — must be rejected", bad)
		}
	}
}

// TestQueryTagsIsBounded — `tag` is single-valued upstream so each synonym costs
// one outbound request; this cap is what keeps a facet page from bursting.
func TestQueryTagsIsBounded(t *testing.T) {
	for _, u := range UseCases() {
		got := u.QueryTags()
		if len(got) > MaxUseCaseTagQueries {
			t.Errorf("use case %q would issue %d requests, cap is %d", u.Slug, len(got), MaxUseCaseTagQueries)
		}
		if len(got) == 0 {
			t.Errorf("use case %q would issue no tag query", u.Slug)
		}
		// The PRIMARY tag must come first — it is the single-request fallback.
		if got[0] != u.Tags[0] {
			t.Errorf("use case %q primary tag = %q, want %q", u.Slug, got[0], u.Tags[0])
		}
	}
	// The video use case has 9 synonyms; it must be truncated, not expanded.
	v, ok := UseCaseBySlug("video")
	if !ok {
		t.Fatal("video use case missing")
	}
	if len(v.Tags) <= MaxUseCaseTagQueries {
		t.Skip("video no longer has more tags than the cap; the truncation path is untested here")
	}
	if len(v.QueryTags()) != MaxUseCaseTagQueries {
		t.Errorf("video QueryTags = %d, want exactly the cap %d", len(v.QueryTags()), MaxUseCaseTagQueries)
	}
}

func TestUseCasesForTags(t *testing.T) {
	// Real tag list from live model 790080 「FLUX】INPAINT.
	got := UseCasesForTags([]string{"flux1.d", "inpaint", "comfyui", "lora", "flux1.s", "upscaler", "flux.1", "tool"})
	want := []string{"inpaint", "upscale", "lora"}
	if strings.Join(slugsOfUse(got), ",") != strings.Join(want, ",") {
		t.Errorf("UseCasesForTags = %v, want %v (table order)", slugsOfUse(got), want)
	}
	// Mixed case + padding.
	if got := UseCasesForTags([]string{" InPaint "}); len(got) != 1 || got[0].Slug != "inpaint" {
		t.Errorf("case/space-insensitive match failed: %v", slugsOfUse(got))
	}
	// A tagless workflow — the Unclassified case. MUST be nil, never a guess.
	if got := UseCasesForTags(nil); got != nil {
		t.Errorf("no tags must yield no use case, got %v", slugsOfUse(got))
	}
	// Stopwords ONLY — still Unclassified, not a bucket.
	if got := UseCasesForTags([]string{"tool", "comfyui", "workflow", "workflows", "comfy"}); got != nil {
		t.Errorf("stopwords-only must yield no use case, got %v", slugsOfUse(got))
	}
	// Order is TABLE order regardless of input order.
	a := slugsOfUse(UseCasesForTags([]string{"upscaler", "inpaint"}))
	b := slugsOfUse(UseCasesForTags([]string{"inpaint", "upscaler"}))
	if strings.Join(a, ",") != strings.Join(b, ",") || strings.Join(a, ",") != "inpaint,upscale" {
		t.Errorf("use-case order unstable: %v vs %v", a, b)
	}
}

func slugsOfUse(us []UseCase) []string {
	out := make([]string, 0, len(us))
	for _, u := range us {
		out = append(out, u.Slug)
	}
	return out
}

// TestTablesReturnCopies — the tables are package-level slices; handing out the
// backing array would let one request's render corrupt the vocabulary process-wide.
func TestTablesReturnCopies(t *testing.T) {
	a := Ecosystems()
	a[0].Label = "MUTATED"
	if Ecosystems()[0].Label == "MUTATED" {
		t.Error("Ecosystems() leaks the shared table")
	}
	u := UseCases()
	u[0].Label = "MUTATED"
	if UseCases()[0].Label == "MUTATED" {
		t.Error("UseCases() leaks the shared table")
	}
	v, _ := UseCaseBySlug("video")
	q := v.QueryTags()
	q[0] = "MUTATED"
	if v2, _ := UseCaseBySlug("video"); v2.Tags[0] == "MUTATED" {
		t.Error("QueryTags() leaks the shared tag slice")
	}
}
