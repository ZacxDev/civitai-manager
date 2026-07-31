package civitai

import "strings"

// This file is the SINGLE SOURCE OF TRUTH for the two browse-by vocabularies the
// app exposes — ECOSYSTEM (base-model family) and USE CASE (what a workflow is
// for). Both the remote Discover-workflows page and the local library workflow
// list read this table, so the vocabulary can never drift between them, and
// adding an ecosystem or use case is a one-line table edit.
//
// The table is CURATED and DETERMINISTIC. It is deliberately NOT derived from the
// contents of a result page: a dynamically-derived facet list changes under the
// user between renders, cannot be URL-addressed stably, and cannot be tested.
//
//
// ==========================================================================
// LIVE-VERIFIED CivitAI API SEMANTICS these facets depend on (2026-07-28)
// ==========================================================================
// Every line below was checked against the REAL https://civitai.com/api/v1/models
// with types=Workflows. This repo has shipped three bugs that were green in
// fake-reader tests and broken against reality, so these are recorded here rather
// than assumed.
//
//  1. `types` is PLURAL. Singular `type=` is silently ignored. (Pre-existing
//     gotcha, repeated here because every facet request carries it.)
//
//  2. `baseModels` accepts MULTIPLE values ONLY as a REPEATED param:
//     `baseModels=Illustrious&baseModels=Pony` → the UNION (OR) of both.
//     Verified with 8 repeated values in one request — no error, no truncation.
//     *** `baseModels=Illustrious,Pony` (comma-joined) returns ZERO items. ***
//     The comma form is not a list — it is parsed as one literal base-model name
//     that matches nothing, so it fails SILENTLY as an empty result page rather
//     than an error. Use url.Values.Add (repeated), never a comma join.
//
//  3. An UNKNOWN `baseModels` value returns HTTP 200 with `items: []` — a silent
//     empty page, not an error. So an un-whitelisted ecosystem value looks like
//     "nothing matched" to the user. (See 4 for the opposite, worse, failure.)
//
//  4. `tag` is SINGLE-VALUE ONLY, and it fails in TWO different silent ways:
//     - REPEATED `tag=inpaint&tag=upscaler` → HTTP 400 ZodError
//     ("expected string, received array"). Hard failure, at least it is loud.
//     - COMMA `tag=inpaint,upscaler` → HTTP 200 with results IDENTICAL to
//     sending no tag at all. The filter is silently DROPPED.
//     - An UNKNOWN tag (`tag=zzz-not-a-real-tag-xyz`) → also byte-identical to
//     the unfiltered baseline. The filter is silently DROPPED.
//     *** This is the dangerous one: a bad tag does not return "no results", it
//     returns EVERYTHING, so the page looks like a working filter that is
//     quietly lying. ***  Hence: only whitelisted tags from this table are ever
//     forwarded, and a multi-tag use case is fanned out into ONE REQUEST PER TAG
//     that are merged + deduped by model id client-side.
//
//  5. `tag` matching is CASE-INSENSITIVE: tag=inpaint, tag=Inpaint and
//     tag=INPAINT return byte-identical result sets.
//
//  6. `tag` synonyms are NOT unified server-side. tag=detailer, tag=adetailer,
//     tag=facedetailer and tag="face detailer" each return a DIFFERENT set of
//     models. That is precisely why a use case carries a synonym LIST rather than
//     a single tag: covering "detail fix" needs several requests merged.
//
//  7. Facets combine correctly with query / sort / period / nsfw: a
//     types+tag+baseModels+query+sort+period request returns a properly
//     intersected (AND across param KINDS) result set.
//
// ==========================================================================
// Where the curated values come from
// ==========================================================================
// The ecosystem BaseModels lists are the values actually observed across a live
// harvest of 600 `types=Workflows` models (Most Downloaded / AllTime, 6 pages of
// 100 via the cursor). Observed frequencies, highest first: Flux.1 D 423, Other
// 189, ZImageTurbo 179, SDXL 1.0 155, SD 1.5 154, Illustrious 147, Wan Video 2.2
// I2V-A14B 115, Flux.2 Klein 9B 79, Qwen 76, Wan Video 73, Hunyuan Video 72, Wan
// Video 14B t2v 53, LTXV 2.3 44, Anima 44, Krea 2 40, Wan Video 14B i2v 720p 38,
// LTXV 36, LTXV2 33, Pony 33, Stable Cascade 29, Wan Video 2.2 T2V-A14B 27,
// Flux.1 S 27, Wan Video 14B i2v 480p 22, Flux.1 Kontext 21, ZImageBase 16,
// Flux.2 D 9, Flux.2 Klein 9B-base 8, Flux.2 Klein 4B 8, SDXL Lightning 7, Wan
// Video 2.2 TI2V-5B 7, Wan Video 1.3B t2v 6, ACE Audio 6, SD 1.5 LCM 5, HiDream
// 5, SDXL 0.9 4, SVD XT 3, PixArt E 3, SDXL Hyper 3, Pony V7 3, SDXL 1.0 LCM 2,
// Chroma 2, Ideogram 4.0 2, Mochi 2, Flux.2 Klein 4B-base 1, Ernie 1, Flux.1
// Krea 1.
//
// The use-case tag lists come from the same harvest's tag histogram, minus the
// near-ubiquitous noise (see tagStopwords).

// EcosystemKind groups ecosystems on the browse-by landing page. Video models
// stay SEPARATE ecosystems (a Wan 2.2 workflow and an LTXV workflow share
// nothing operationally) but render under one "Video models" heading, which is
// what makes the landing grid readable without collapsing the filters.
const (
	EcosystemKindImage = "image"
	EcosystemKindVideo = "video"
	EcosystemKindAudio = "audio"
	EcosystemKindOther = "other"
)

// Ecosystem is one browsable base-model family.
//
// BaseModels are the EXACT CivitAI `baseModel` strings this family covers; they
// are sent as REPEATED `baseModels=` params (see semantics note 2 above).
type Ecosystem struct {
	// Slug is the stable, URL-safe facet value (?eco=<slug>). Never change one:
	// it is what a shared link carries.
	Slug string
	// Label is the user-facing display name.
	Label string
	// Kind groups the tile on the landing page (image / video / audio / other).
	Kind string
	// BaseModels are the exact CivitAI baseModel values in this family. Each
	// value belongs to EXACTLY ONE ecosystem — the table is a partition, which
	// TestEcosystemBaseModelsArePartitioned enforces. Multi-membership of a
	// MODEL across ecosystems is legitimate and comes from its versions carrying
	// different baseModels; it is never created by overlapping table rows.
	BaseModels []string
}

// ecosystems is the ordered, curated ecosystem table. ORDER IS USER-VISIBLE (it
// is the landing-grid order within each Kind), so it is ranked roughly by how
// much real workflow content each family has, not alphabetically.
var ecosystems = []Ecosystem{
	{
		Slug: "flux1", Label: "Flux.1", Kind: EcosystemKindImage,
		BaseModels: []string{"Flux.1 D", "Flux.1 S", "Flux.1 Kontext", "Flux.1 Krea"},
	},
	{
		Slug: "flux2", Label: "Flux.2", Kind: EcosystemKindImage,
		BaseModels: []string{"Flux.2 D", "Flux.2 Klein 9B", "Flux.2 Klein 9B-base", "Flux.2 Klein 4B", "Flux.2 Klein 4B-base"},
	},
	{
		// The SDXL FAMILY: the community fine-tune lineages (Illustrious, Pony,
		// NoobAI) are architecturally SDXL and share workflow structure, so they
		// belong to one browsable family even though CivitAI lists them as
		// distinct baseModel values. This is exactly the case the repeated-param
		// `baseModels=` union exists for.
		Slug: "sdxl", Label: "SDXL family", Kind: EcosystemKindImage,
		BaseModels: []string{
			"SDXL 1.0", "SDXL 0.9", "SDXL 1.0 LCM", "SDXL Lightning", "SDXL Hyper", "SDXL Turbo",
			"Illustrious", "Pony", "Pony V7", "NoobAI",
		},
	},
	{
		Slug: "zimage", Label: "Z-Image", Kind: EcosystemKindImage,
		BaseModels: []string{"ZImageTurbo", "ZImageBase"},
	},
	{
		Slug: "sd15", Label: "SD 1.5", Kind: EcosystemKindImage,
		BaseModels: []string{"SD 1.5", "SD 1.5 LCM", "SD 1.5 Hyper", "SD 1.4"},
	},
	{
		Slug: "qwen", Label: "Qwen", Kind: EcosystemKindImage,
		BaseModels: []string{"Qwen"},
	},
	{
		// Anima is its own anime-oriented base model on CivitAI, not an SDXL
		// fine-tune label, so it gets its own family rather than being folded in.
		Slug: "anima", Label: "Anima", Kind: EcosystemKindImage,
		BaseModels: []string{"Anima"},
	},
	{
		Slug: "krea", Label: "Krea 2", Kind: EcosystemKindImage,
		BaseModels: []string{"Krea 2"},
	},
	{
		Slug: "cascade", Label: "Stable Cascade", Kind: EcosystemKindImage,
		BaseModels: []string{"Stable Cascade"},
	},
	{
		Slug: "hidream", Label: "HiDream", Kind: EcosystemKindImage,
		BaseModels: []string{"HiDream"},
	},
	{
		Slug: "chroma", Label: "Chroma", Kind: EcosystemKindImage,
		BaseModels: []string{"Chroma"},
	},
	{
		Slug: "pixart", Label: "PixArt", Kind: EcosystemKindImage,
		BaseModels: []string{"PixArt a", "PixArt E"},
	},
	{
		Slug: "sd3", Label: "SD 3.x", Kind: EcosystemKindImage,
		BaseModels: []string{"SD 3", "SD 3.5", "SD 3.5 Medium", "SD 3.5 Large", "SD 3.5 Large Turbo"},
	},
	{
		Slug: "wan", Label: "Wan Video", Kind: EcosystemKindVideo,
		BaseModels: []string{
			"Wan Video", "Wan Video 1.3B t2v", "Wan Video 14B t2v",
			"Wan Video 14B i2v 480p", "Wan Video 14B i2v 720p",
			"Wan Video 2.2 T2V-A14B", "Wan Video 2.2 I2V-A14B", "Wan Video 2.2 TI2V-5B",
		},
	},
	{
		Slug: "ltxv", Label: "LTXV", Kind: EcosystemKindVideo,
		BaseModels: []string{"LTXV", "LTXV2", "LTXV 2.3"},
	},
	{
		Slug: "hunyuan", Label: "Hunyuan Video", Kind: EcosystemKindVideo,
		BaseModels: []string{"Hunyuan Video"},
	},
	{
		Slug: "svd", Label: "Stable Video Diffusion", Kind: EcosystemKindVideo,
		BaseModels: []string{"SVD", "SVD XT"},
	},
	{
		Slug: "mochi", Label: "Mochi", Kind: EcosystemKindVideo,
		BaseModels: []string{"Mochi"},
	},
	{
		Slug: "audio", Label: "Audio", Kind: EcosystemKindAudio,
		BaseModels: []string{"ACE Audio", "Stable Audio"},
	},
	{
		// "Other" IS a real, filterable CivitAI baseModel value (189/600 in the
		// harvest — the second most common), so it is browsable rather than
		// dropped. The hosted/API base models are folded in here because they
		// have single-digit workflow counts and no shared local tooling.
		Slug: "other", Label: "Other / unlisted", Kind: EcosystemKindOther,
		BaseModels: []string{"Other", "Ideogram 4.0", "Ernie"},
	},
}

// UseCase is one browsable "what is this workflow for" facet.
//
// Tags are the CivitAI tag synonyms that indicate this use case. Tags[0] is the
// PRIMARY tag — the one used when only a single outbound request is affordable.
// The rest exist because CivitAI does NOT unify tag synonyms (semantics note 6):
// covering a use case genuinely needs several requests merged.
//
// Per the product decision this mapping is TAG-ONLY. There is deliberately no
// graph-inspection classifier — not for remote results and not for local ones —
// so a workflow with no tags has NO use case and is surfaced as Unclassified
// rather than guessed at.
type UseCase struct {
	// Slug is the stable, URL-safe facet value (?use=<slug>).
	Slug string
	// Label is the user-facing display name.
	Label string
	// Tags are the CivitAI tags for this use case, most-signal first. Every tag
	// here is lowercase (CivitAI tag matching is case-insensitive, note 5) and
	// none may be a stopword (TestUseCaseTagsAreNotStopwords enforces that).
	Tags []string
}

// useCases is the ordered, curated use-case table. ORDER IS USER-VISIBLE.
var useCases = []UseCase{
	{Slug: "txt2img", Label: "Text to image", Tags: []string{"txt2img", "t2i", "text to image"}},
	{Slug: "img2img", Label: "Image to image", Tags: []string{"img2img", "i2i", "image to image"}},
	{Slug: "inpaint", Label: "Inpainting", Tags: []string{"inpaint", "inpainting", "inpaint fill", "outpaint"}},
	{Slug: "upscale", Label: "Upscaling", Tags: []string{"upscaler", "upscale", "highres"}},
	{Slug: "detailer", Label: "Face & detail fix", Tags: []string{"detailer", "adetailer", "facedetailer", "face detailer"}},
	{Slug: "controlnet", Label: "ControlNet & guidance", Tags: []string{"controlnet", "openpose", "canny", "depth"}},
	{Slug: "faceswap", Label: "Face swap & identity", Tags: []string{"face swap", "faceswap", "faceid", "pulid", "reactor"}},
	{Slug: "video", Label: "Video generation", Tags: []string{"video", "i2v", "t2v", "image to video", "img2video", "vid2vid", "v2v", "animatediff", "animation"}},
	{Slug: "style", Label: "Style & reference transfer", Tags: []string{"ipadapter", "style transfer", "style"}},
	{Slug: "regional", Label: "Regional & multi-subject", Tags: []string{"regional prompt", "regional", "multi character", "forge couple"}},
	{Slug: "lora", Label: "LoRA pipelines", Tags: []string{"lora", "lycoris"}},
	{Slug: "lowvram", Label: "Low VRAM", Tags: []string{"low vram", "lowram", "gguf"}},
}

// tagStopwords are tags that carry NO use-case signal on Workflows models because
// they are near-ubiquitous. Measured over the 600-model harvest: `tool` appeared
// on 379, `comfyui` on 179, `workflow` on 163, `base model` on 76 — i.e. they
// describe "this is a ComfyUI workflow", which is already the page's premise.
//
// Two jobs: (a) no use case may map to one (a stopword tag would return the
// unfiltered feed dressed up as a filter), and (b) they are stripped when a
// local workflow's linked-model tags are shown or classified.
var tagStopwords = map[string]bool{
	"tool":       true,
	"tools":      true,
	"comfy":      true,
	"comfyui":    true,
	"workflow":   true,
	"workflows":  true,
	"base model": true,
	"assets":     true,
	"civitai":    true,
}

// MaxUseCaseTagQueries bounds the outbound request fan-out for ONE use-case
// facet. `tag` is single-valued (semantics note 4), so a use case with N tag
// synonyms would need N requests; this caps it so a facet page can never issue an
// unbounded burst at civitai.com. The first MaxUseCaseTagQueries tags of a use
// case are the ones queried — hence "most-signal first" ordering in the table.
const MaxUseCaseTagQueries = 3

// Ecosystems returns the curated ecosystem table in display order. The slice is a
// copy, so a caller cannot mutate the shared table.
func Ecosystems() []Ecosystem {
	out := make([]Ecosystem, len(ecosystems))
	copy(out, ecosystems)
	return out
}

// UseCases returns the curated use-case table in display order (copy).
func UseCases() []UseCase {
	out := make([]UseCase, len(useCases))
	copy(out, useCases)
	return out
}

// EcosystemBySlug resolves a ?eco= facet value. It is the ONLY way an ecosystem
// value may enter an outbound request: an unrecognized slug returns ok=false and
// the caller drops the facet entirely, so an un-whitelisted string can never be
// forwarded to civitai.com (where it would silently return an empty page — see
// semantics note 3).
//
// Matching is case-insensitive on a trimmed value; everything else is rejected.
func EcosystemBySlug(slug string) (Ecosystem, bool) {
	s := strings.ToLower(strings.TrimSpace(slug))
	if s == "" {
		return Ecosystem{}, false
	}
	for _, e := range ecosystems {
		if e.Slug == s {
			return e, true
		}
	}
	return Ecosystem{}, false
}

// UseCaseBySlug resolves a ?use= facet value, same whitelist-only contract as
// EcosystemBySlug. Critical here: an unrecognized tag forwarded to CivitAI is
// SILENTLY IGNORED and returns the unfiltered feed (semantics note 4), so a
// non-whitelisted use case would produce a filter UI that lies. Never forward one.
func UseCaseBySlug(slug string) (UseCase, bool) {
	s := strings.ToLower(strings.TrimSpace(slug))
	if s == "" {
		return UseCase{}, false
	}
	for _, u := range useCases {
		if u.Slug == s {
			return u, true
		}
	}
	return UseCase{}, false
}

// QueryTags returns the tags this use case will actually be queried with — its
// first MaxUseCaseTagQueries synonyms, one outbound request each, merged and
// deduped by model id by the caller.
func (u UseCase) QueryTags() []string {
	if len(u.Tags) <= MaxUseCaseTagQueries {
		out := make([]string, len(u.Tags))
		copy(out, u.Tags)
		return out
	}
	out := make([]string, MaxUseCaseTagQueries)
	copy(out, u.Tags[:MaxUseCaseTagQueries])
	return out
}

// EcosystemsForBaseModel returns every ecosystem covering one CivitAI baseModel
// value. The table is a partition so this returns at most one entry today; it
// returns a SLICE anyway because the caller's real question — "which ecosystems
// does this MODEL belong to" — is genuinely multi-valued (a model's versions
// carry different baseModels), and collapsing to a single answer there would be
// wrong. Comparison is case-insensitive on a trimmed value.
func EcosystemsForBaseModel(baseModel string) []Ecosystem {
	bm := strings.ToLower(strings.TrimSpace(baseModel))
	if bm == "" {
		return nil
	}
	var out []Ecosystem
	for _, e := range ecosystems {
		for _, v := range e.BaseModels {
			if strings.ToLower(v) == bm {
				out = append(out, e)
				break
			}
		}
	}
	return out
}

// CanonicalBaseModel resolves one CivitAI `baseModel` string against THIS
// ecosystem's member list and returns the TABLE's spelling of it.
//
// It exists so a base-model name can be DISPLAYED without ever echoing caller
// text. The "Workflows for Illustrious · SDXL family" heading names two things —
// the selected version's own base model and the family actually being searched —
// and the first half reaches the fragment handler as a URL parameter. Echoing
// that parameter would reflect arbitrary text into the page; returning the table
// value instead means the heading can only say something the curated table
// contains.
//
// It is scoped to ONE ecosystem on purpose: a base model that resolves to a
// DIFFERENT family than the `eco` being queried is a mismatched (hand-edited)
// pair, and ok=false makes the caller fall back to the family label alone rather
// than print a base model the outgoing query does not cover.
//
// Comparison is case-insensitive on a trimmed value; the returned string is
// always the table's own casing.
func (e Ecosystem) CanonicalBaseModel(baseModel string) (string, bool) {
	bm := strings.ToLower(strings.TrimSpace(baseModel))
	if bm == "" {
		return "", false
	}
	for _, v := range e.BaseModels {
		if strings.ToLower(v) == bm {
			return v, true
		}
	}
	return "", false
}

// EcosystemsForBaseModels is the multi-membership answer for a whole model: the
// union of the ecosystems of each of its versions' baseModels, in TABLE order
// (stable and independent of the input order), deduped.
//
// Multi-membership is CORRECT, not a bug — a collection spanning "SD 1.5",
// "Flux.1 D" and "ZImageTurbo" legitimately belongs to three ecosystems and is
// browsable under all three. Nothing here picks an arbitrary primary.
func EcosystemsForBaseModels(baseModels []string) []Ecosystem {
	hit := map[string]bool{}
	for _, bm := range baseModels {
		for _, e := range EcosystemsForBaseModel(bm) {
			hit[e.Slug] = true
		}
	}
	if len(hit) == 0 {
		return nil
	}
	var out []Ecosystem
	for _, e := range ecosystems {
		if hit[e.Slug] {
			out = append(out, e)
		}
	}
	return out
}

// UseCasesForTags maps a set of CivitAI tags to use cases, in TABLE order,
// deduped. Stopwords are dropped before matching, and matching is
// case-insensitive (CivitAI tags arrive in mixed case).
//
// Returns nil when nothing matches — the caller MUST surface that as
// "Unclassified" rather than inventing a bucket. Under the tag-only rule a
// locally-authored / PNG-extracted / scanned workflow has no tags at all, so nil
// here is the normal case for those, not an error.
func UseCasesForTags(tags []string) []UseCase {
	if len(tags) == 0 {
		return nil
	}
	have := make(map[string]bool, len(tags))
	for _, t := range tags {
		t = strings.ToLower(strings.TrimSpace(t))
		if t == "" || tagStopwords[t] {
			continue
		}
		have[t] = true
	}
	if len(have) == 0 {
		return nil
	}
	var out []UseCase
	for _, u := range useCases {
		for _, t := range u.Tags {
			if have[t] {
				out = append(out, u)
				break
			}
		}
	}
	return out
}

// IsStopwordTag reports whether a tag is pure noise on Workflows models (see
// tagStopwords). Case-insensitive.
func IsStopwordTag(tag string) bool {
	return tagStopwords[strings.ToLower(strings.TrimSpace(tag))]
}

// StopwordTags returns the stopword list (sorted-stable via the table below is
// unnecessary — callers only membership-test), exposed for tests and for the
// "why is this tag not shown" explanation in the UI.
func StopwordTags() []string {
	out := make([]string, 0, len(tagStopwords))
	for t := range tagStopwords {
		out = append(out, t)
	}
	return out
}
