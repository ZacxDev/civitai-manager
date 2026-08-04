package web

import (
	"encoding/json"
	"net/url"
	"strconv"
	"strings"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
	"github.com/ZacxDev/civitai-manager/internal/store"
	g "maragu.dev/gomponents"
	h "maragu.dev/gomponents/html"
)

// Local-library browse-by-facet: the SAME curated ecosystem / use-case
// vocabulary the remote Discover page uses (internal/civitai/taxonomy.go),
// applied to the workflows already in the library. Sharing one table is the
// point — a user who browsed "Flux.1 · Inpainting" on Discover must find the
// same two words meaning the same two things in their own library.
//
// ==========================================================================
// THE HONEST LIMIT OF TAG-ONLY CLASSIFICATION
// ==========================================================================
// A local workflow's USE CASE can only come from its LINKED CivitAI model's
// tags, because the product decision is a curated tag mapping with NO
// graph-based classification. A workflow that was pasted, extracted from a PNG,
// authored locally, or found by a disk scan has NO CivitAI link and therefore NO
// tags — so it CANNOT have a use case. That is a real and common case, not an
// edge case.
//
// Those workflows are NOT hidden and NOT guessed at. They go into an explicit,
// first-class "Unclassified" bucket with a visible count, browsable like any
// other facet. Nothing filters them out unless the user asks for a facet.

// facetUnclassified is the reserved facet value for the Unclassified bucket. It
// is not a taxonomy slug (the table would reject it), so it is normalized
// separately — which also keeps it impossible for "none" to ever be forwarded to
// civitai.com as a baseModel or tag.
const facetUnclassified = "none"

// workflowClassification is one local workflow's facet membership. Both lists are
// empty for a workflow that has neither a recognizable base model nor a linked,
// cached, tagged CivitAI model.
type workflowClassification struct {
	Ecosystems []civitai.Ecosystem
	UseCases   []civitai.UseCase
	// BaseModels is what the ecosystem lookup was fed (for the "why is this here"
	// hint on the card and for tests).
	BaseModels []string
	// Tags is the linked model's tags with stopwords removed.
	Tags []string
	// Linked reports whether the workflow carries a CivitAI model id at all — i.e.
	// whether a use case was ever POSSIBLE for it. It is the difference between
	// "we had nothing to classify" and "we looked and nothing matched", which is
	// the only honest way to explain the Unclassified bucket (see
	// unclassifiedUseCaseNote). It is NOT "we have the model's tags": a linked
	// workflow whose model is not in the local cache is Linked with no tags, and
	// is deliberately counted in the same bucket — from the user's side both mean
	// "no known use case matched", and splitting a third way would put a number on
	// screen that was 0 on every library measured.
	Linked bool
}

func (c workflowClassification) inEcosystem(slug string) bool {
	for _, e := range c.Ecosystems {
		if e.Slug == slug {
			return true
		}
	}
	return false
}

func (c workflowClassification) inUseCase(slug string) bool {
	for _, u := range c.UseCases {
		if u.Slug == slug {
			return true
		}
	}
	return false
}

// workflowClassifier classifies local workflows against the curated table,
// MEMOIZING the per-model cache lookup. Without the memo a 500-workflow library
// linked to 40 models would issue 500 model_cache reads per page render.
type workflowClassifier struct {
	res   workflowResolver
	cache map[int]modelFacetSource
}

// modelFacetSource is what one cached CivitAI model contributes: its tags (for
// use cases) and its per-version base models (for ecosystems).
type modelFacetSource struct {
	tags           []string
	baseModelByVer map[int]string
	ok             bool
}

func newWorkflowClassifier(r workflowResolver) *workflowClassifier {
	return &workflowClassifier{res: r, cache: map[int]modelFacetSource{}}
}

// modelSource returns the cached CivitAI model's facet inputs. A cache MISS is a
// normal outcome (the model was never fetched) and yields ok=false — the caller
// then falls back to whatever the workflow row itself carries.
func (c *workflowClassifier) modelSource(modelID int) modelFacetSource {
	if src, ok := c.cache[modelID]; ok {
		return src
	}
	var src modelFacetSource
	if c.res.cachedModel != nil {
		if _, raw, ok := c.res.cachedModel(modelID); ok && len(raw) > 0 {
			src = parseModelFacetSource(raw)
		}
	}
	c.cache[modelID] = src
	return src
}

// parseModelFacetSource pulls tags[] and modelVersions[].baseModel out of a
// cached GetModel body. Defensive: any decode failure yields an empty source, so
// a malformed cache row degrades to Unclassified rather than breaking the page.
func parseModelFacetSource(raw []byte) modelFacetSource {
	var body struct {
		Tags          []string `json:"tags"`
		ModelVersions []struct {
			ID        int    `json:"id"`
			BaseModel string `json:"baseModel"`
		} `json:"modelVersions"`
	}
	if json.Unmarshal(raw, &body) != nil {
		return modelFacetSource{}
	}
	src := modelFacetSource{ok: true, baseModelByVer: map[int]string{}}
	for _, t := range body.Tags {
		if t = strings.TrimSpace(t); t != "" && !civitai.IsStopwordTag(t) {
			src.tags = append(src.tags, t)
		}
	}
	for _, v := range body.ModelVersions {
		if strings.TrimSpace(v.BaseModel) != "" {
			src.baseModelByVer[v.ID] = v.BaseModel
		}
	}
	return src
}

// classify computes one workflow's facet membership.
//
// ECOSYSTEM comes from the workflow's own stored BaseModel AND — when it is
// linked to a cached CivitAI version — that version's baseModel. Both are used
// because they disagree in practice: a scanned workflow often records the base
// model its graph loads, while the linked CivitAI version records what the
// creator published it under. Feeding both to EcosystemsForBaseModels is the
// multi-membership rule, and it is deliberate: a workflow that legitimately spans
// two families is browsable under both.
//
// USE CASE comes ONLY from the linked model's tags (see the file header).
func (c *workflowClassifier) classify(wf store.Workflow) workflowClassification {
	var cl workflowClassification
	if bm := strings.TrimSpace(wf.BaseModel); bm != "" {
		cl.BaseModels = append(cl.BaseModels, bm)
	}
	if wf.ModelID != nil {
		cl.Linked = true
		src := c.modelSource(*wf.ModelID)
		cl.Tags = src.tags
		if wf.VersionID != nil {
			if bm := src.baseModelByVer[*wf.VersionID]; bm != "" && !containsFold(cl.BaseModels, bm) {
				cl.BaseModels = append(cl.BaseModels, bm)
			}
		}
	}
	cl.Ecosystems = civitai.EcosystemsForBaseModels(cl.BaseModels)
	cl.UseCases = civitai.UseCasesForTags(cl.Tags)
	return cl
}

func containsFold(hay []string, needle string) bool {
	for _, s := range hay {
		if strings.EqualFold(s, needle) {
			return true
		}
	}
	return false
}

// classifyWorkflows classifies a whole list, index-aligned with wfs.
func classifyWorkflows(wfs []store.Workflow, r workflowResolver) []workflowClassification {
	c := newWorkflowClassifier(r)
	out := make([]workflowClassification, len(wfs))
	for i, wf := range wfs {
		out[i] = c.classify(wf)
	}
	return out
}

// workflowFacetCounts is the per-bucket population of the local library. The two
// None counts are the Unclassified buckets and are rendered with the same
// prominence as any other chip.
type workflowFacetCounts struct {
	Eco     map[string]int
	Use     map[string]int
	EcoNone int
	UseNone int
	// UseNoneLinked is the part of UseNone that IS linked to a CivitAI model —
	// i.e. the workflows for which a use case was possible and none matched. The
	// remainder (UseNone - UseNoneLinked) never had a link at all.
	//
	// The split exists because the note used to attribute ALL of UseNone to "no
	// CivitAI link". Measured on a real 71-workflow library: 58 unclassified, of
	// which 39 WERE linked — the stated reason was wrong for two thirds of the
	// number printed beside it.
	UseNoneLinked int
	Total         int
}

// useNoneUnlinked is the complement of UseNoneLinked. It is DERIVED rather than
// counted separately so the two halves can never disagree with UseNone.
func (c workflowFacetCounts) useNoneUnlinked() int { return c.UseNone - c.UseNoneLinked }

// countWorkflowFacets tallies every bucket. A workflow in two ecosystems counts
// in BOTH — that is the correct answer to "how many Flux workflows do I have",
// not a double-count bug, and it is why the bucket counts do not sum to Total.
func countWorkflowFacets(cls []workflowClassification) workflowFacetCounts {
	c := workflowFacetCounts{Eco: map[string]int{}, Use: map[string]int{}, Total: len(cls)}
	for _, cl := range cls {
		if len(cl.Ecosystems) == 0 {
			c.EcoNone++
		}
		for _, e := range cl.Ecosystems {
			c.Eco[e.Slug]++
		}
		if len(cl.UseCases) == 0 {
			c.UseNone++
			if cl.Linked {
				c.UseNoneLinked++
			}
		}
		for _, u := range cl.UseCases {
			c.Use[u.Slug]++
		}
	}
	return c
}

// libraryWorkflowFacets is the normalized facet selection for the library
// workflows tab. Same whitelist-only contract as the Discover page, plus the
// reserved Unclassified value.
type libraryWorkflowFacets struct {
	Eco *civitai.Ecosystem
	Use *civitai.UseCase
	// EcoNone / UseNone select the Unclassified bucket of that dimension.
	EcoNone bool
	UseNone bool
	// Model narrows to the workflows imported from ONE CivitAI model — the "source
	// post" filter. Importing a Workflows model routinely yields many workflows (22
	// for one post in a real library), so the post-import "View in library" link
	// lands here rather than dumping the user into an undifferentiated list.
	//
	// It is NOT a taxonomy facet: the value is a plain civitai model id, matched
	// against the workflow row's own model_id (no cache lookup, so it works even for
	// an uncached model). 0 means "not filtering".
	Model int
}

func (f libraryWorkflowFacets) any() bool {
	return f.Eco != nil || f.Use != nil || f.EcoNone || f.UseNone || f.Model > 0
}

// normalizeLibraryWorkflowFacets resolves ?eco=/?use= for the library tab.
// Unknown values are IGNORED (dropped), matching the Discover page — an unknown
// value renders as "no filter", never as a filter that silently matches nothing.
func normalizeLibraryWorkflowFacets(q url.Values) libraryWorkflowFacets {
	var f libraryWorkflowFacets
	eco := strings.ToLower(strings.TrimSpace(q.Get("eco")))
	use := strings.ToLower(strings.TrimSpace(q.Get("use")))
	if eco == facetUnclassified {
		f.EcoNone = true
	} else if e, ok := civitai.EcosystemBySlug(eco); ok {
		f.Eco = &e
	}
	if use == facetUnclassified {
		f.UseNone = true
	} else if u, ok := civitai.UseCaseBySlug(use); ok {
		f.Use = &u
	}
	// ?model= narrows to one source post. A non-numeric or non-positive value is
	// IGNORED (dropped) exactly like an unknown eco/use slug — it must never become a
	// filter that silently matches nothing.
	if n, err := strconv.Atoi(strings.TrimSpace(q.Get("model"))); err == nil && n > 0 {
		f.Model = n
	}
	return f
}

func (f libraryWorkflowFacets) ecoValue() string {
	switch {
	case f.EcoNone:
		return facetUnclassified
	case f.Eco != nil:
		return f.Eco.Slug
	}
	return ""
}

func (f libraryWorkflowFacets) useValue() string {
	switch {
	case f.UseNone:
		return facetUnclassified
	case f.Use != nil:
		return f.Use.Slug
	}
	return ""
}

// summary is the human phrase for the current selection, used in the filtered
// heading and the empty state.
func (f libraryWorkflowFacets) summary() string {
	var parts []string
	switch {
	case f.EcoNone:
		parts = append(parts, "Unclassified ecosystem")
	case f.Eco != nil:
		parts = append(parts, f.Eco.Label)
	}
	switch {
	case f.UseNone:
		parts = append(parts, "Unclassified use case")
	case f.Use != nil:
		parts = append(parts, f.Use.Label)
	}
	if f.Model > 0 {
		parts = append(parts, "CivitAI model "+strconv.Itoa(f.Model))
	}
	return strings.Join(parts, " · ")
}

// matches reports whether a classification satisfies the selection. Both
// dimensions are ANDed; an unselected dimension matches everything.
func (f libraryWorkflowFacets) matches(cl workflowClassification) bool {
	switch {
	case f.EcoNone:
		if len(cl.Ecosystems) != 0 {
			return false
		}
	case f.Eco != nil:
		if !cl.inEcosystem(f.Eco.Slug) {
			return false
		}
	}
	switch {
	case f.UseNone:
		if len(cl.UseCases) != 0 {
			return false
		}
	case f.Use != nil:
		if !cl.inUseCase(f.Use.Slug) {
			return false
		}
	}
	return true
}

// scopeWorkflowsToSourceModel narrows a list (and its index-aligned
// classifications) to ONE CivitAI source post. It is applied BEFORE the browse-by
// counts are taken, so within a post the chips describe the post. With no ?model=
// filter it returns both slices untouched.
func scopeWorkflowsToSourceModel(wfs []store.Workflow, cls []workflowClassification, f libraryWorkflowFacets) ([]store.Workflow, []workflowClassification) {
	if f.Model <= 0 {
		return wfs, cls
	}
	outW := make([]store.Workflow, 0, len(wfs))
	outC := make([]workflowClassification, 0, len(wfs))
	for i, wf := range wfs {
		if !f.matchesWorkflow(wf) {
			continue
		}
		outW = append(outW, wf)
		if i < len(cls) {
			outC = append(outC, cls[i])
		} else {
			outC = append(outC, workflowClassification{})
		}
	}
	return outW, outC
}

// matchesWorkflow is the row-level half of the selection (the source-post filter),
// ANDed with the classification half. A workflow with no civitai linkage can never
// match a ?model= filter — which is correct: it did not come from that post.
func (f libraryWorkflowFacets) matchesWorkflow(wf store.Workflow) bool {
	return f.Model <= 0 || (wf.ModelID != nil && *wf.ModelID == f.Model)
}

// filterWorkflows keeps the workflows matching the selection, preserving order.
// With NO facet selected it returns everything — including the unclassified ones.
// Filtering is strictly opt-in; nothing is ever hidden by default.
func filterWorkflows(wfs []store.Workflow, cls []workflowClassification, f libraryWorkflowFacets) []store.Workflow {
	if !f.any() {
		return wfs
	}
	out := make([]store.Workflow, 0, len(wfs))
	for i, wf := range wfs {
		if i < len(cls) && f.matches(cls[i]) && f.matchesWorkflow(wf) {
			out = append(out, wf)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Rendering
// ---------------------------------------------------------------------------

// libraryWorkflowHref builds a /library?tab=workflows URL with one facet
// dimension toggled. Plain full navigation, so every filtered view of the local
// library is a shareable, bookmarkable URL exactly like the Discover page.
func libraryWorkflowHref(f libraryWorkflowFacets, dim, value string) string {
	eco, use := f.ecoValue(), f.useValue()
	switch dim {
	case "eco":
		if eco == value {
			eco = ""
		} else {
			eco = value
		}
	case "use":
		if use == value {
			use = ""
		} else {
			use = value
		}
	}
	q := url.Values{}
	q.Set("tab", "workflows")
	if eco != "" {
		q.Set("eco", eco)
	}
	if use != "" {
		q.Set("use", use)
	}
	// The source-post filter is a SCOPE, not a chip: toggling an ecosystem/use-case
	// chip must narrow WITHIN the post the user came from, not silently escape it.
	if f.Model > 0 {
		q.Set("model", strconv.Itoa(f.Model))
	}
	return "/library?" + q.Encode()
}

// workflowFacetBar renders the local browse-by chips: one per NON-EMPTY bucket,
// each with its count, plus the Unclassified bucket.
//
// Only populated buckets get a chip — an empty library should not present 20
// dead filters — but Unclassified is shown whenever it has members, precisely so
// tagless workflows are impossible to miss.
func workflowFacetBar(counts workflowFacetCounts, f libraryWorkflowFacets) g.Node {
	ecoChips := []g.Node{libFacetChip(libraryWorkflowHref(f, "eco", ""), "All", counts.Total, f.ecoValue() == "")}
	for _, e := range civitai.Ecosystems() {
		n := counts.Eco[e.Slug]
		if n == 0 {
			continue
		}
		ecoChips = append(ecoChips, libFacetChip(
			libraryWorkflowHref(f, "eco", e.Slug), e.Label, n, f.Eco != nil && f.Eco.Slug == e.Slug))
	}
	if counts.EcoNone > 0 {
		ecoChips = append(ecoChips, libFacetChip(
			libraryWorkflowHref(f, "eco", facetUnclassified), "Unclassified", counts.EcoNone, f.EcoNone))
	}

	useChips := []g.Node{libFacetChip(libraryWorkflowHref(f, "use", ""), "All", counts.Total, f.useValue() == "")}
	for _, u := range civitai.UseCases() {
		n := counts.Use[u.Slug]
		if n == 0 {
			continue
		}
		useChips = append(useChips, libFacetChip(
			libraryWorkflowHref(f, "use", u.Slug), u.Label, n, f.Use != nil && f.Use.Slug == u.Slug))
	}
	if counts.UseNone > 0 {
		useChips = append(useChips, libFacetChip(
			libraryWorkflowHref(f, "use", facetUnclassified), "Unclassified", counts.UseNone, f.UseNone))
	}

	// The honest note about what Unclassified means, shown only when it applies.
	note := unclassifiedUseCaseNote(counts)

	// A plain block, NOT a card: the bar now lives in the browse surface's controls
	// slot, and a card here would paint a second border inside that one surface.
	return h.Div(
		h.Class("space-y-2"),
		sectionTitle("Browse your workflows"),
		facetChipRow("Ecosystem", ecoChips),
		facetChipRow("Use case", useChips),
		note,
	)
}

// unclassifiedUseCaseNote explains WHY the Unclassified use-case bucket has the
// size it does, SPLIT BY REASON. Returns nil when the bucket is empty.
//
// 🔴 WHY THIS IS SPLIT. The single-sentence version attributed the whole bucket
// to "no CivitAI link". That is one of two reasons, and on a real 71-workflow
// library it was the MINORITY one: 58 unclassified, only 19 unlinked — the note
// stated a cause that was false for 39 of the 58 it counted. A user with a
// linked, tagged workflow was told it had no link.
//
// 🔴 EVERY NUMBER IS COMPUTED AND NO TAG IS NAMED. The copy must survive an edit
// to internal/civitai/taxonomy.go — adding a use case or a stopword moves these
// counts, and a sentence that spelled a tag, a vocabulary word, or a fixed count
// would silently start lying. "No known use case matched" is true whatever the
// table says, which is the property being protected here. It also holds for a
// linked workflow whose model is not in the local cache: nothing matched because
// there was nothing to match (see workflowClassification.Linked).
//
// The two counts are also exposed as data attributes. They are the assertable
// STATE of this note — prose is not, because any wording a guard greps for is a
// word some other feature is free to emit.
func unclassifiedUseCaseNote(counts workflowFacetCounts) g.Node {
	if counts.UseNone == 0 {
		return nil
	}
	unlinked, linked := counts.useNoneUnlinked(), counts.UseNoneLinked

	// A clause is emitted only when its count is non-zero: a library where every
	// unclassified workflow shares one reason must not read "… ; 0 are linked".
	// Subject-verb agreement: plural() covers the noun, not the verb, and a
	// one-workflow library rendering "1 workflow have no use case" is the kind of
	// small wrongness that makes a user distrust the numbers beside it.
	have, they, listed := "have", "they are", "They are"
	if counts.UseNone == 1 {
		have, they, listed = "has", "it is", "It is"
	}

	var why string
	switch {
	case linked == 0:
		why = " — " + they + " not linked to a CivitAI model."
	case unlinked == 0:
		why = " — " + they + " linked to a CivitAI model, but no known use case matched."
	default:
		why = ": " + strconv.Itoa(unlinked) + " not linked to a CivitAI model, " +
			strconv.Itoa(linked) + " linked but no known use case matched."
	}

	return h.P(
		h.Class("text-xs text-slate-500"),
		g.Attr("data-unclassified-unlinked", strconv.Itoa(unlinked)),
		g.Attr("data-unclassified-linked", strconv.Itoa(linked)),
		g.Text("Use cases come from a linked CivitAI model's tags. "+
			strconv.Itoa(counts.UseNone)+" workflow"+plural(counts.UseNone)+
			" "+have+" no use case"+why+
			" "+listed+" listed under Unclassified, never hidden."),
	)
}

// libFacetChip is one local chip: label + count. The count is what makes the
// Unclassified bucket's size obvious at a glance.
func libFacetChip(href, label string, count int, selected bool) g.Node {
	class := "cm-chip cm-facet-chip inline-flex items-center gap-1 rounded-md border px-2 py-0.5 text-xs text-slate-200"
	attrs := []g.Node{h.Href(href)}
	if selected {
		class += " cm-facet-chip-on"
		attrs = append(attrs, g.Attr("aria-current", "true"))
	}
	attrs = append(attrs, h.Class(class), g.Text(label),
		h.Span(h.Class("text-xs text-slate-400"), g.Text("("+strconv.Itoa(count)+")")))
	return h.A(attrs...)
}

// workflowFacetEmptyLocal is the guided empty state for a local filter that
// matched nothing: it names the selection and offers a way back, rather than
// looking like an empty library.
func workflowFacetEmptyLocal(f libraryWorkflowFacets) g.Node {
	what := f.summary()
	if what != "" {
		what = " " + what
	}
	return card(
		h.Class("py-6 text-center"),
		h.H3(h.Class("text-base font-semibold text-slate-200"),
			g.Text("No"+what+" workflows in your library")),
		h.P(h.Class("mx-auto mt-1 mb-3 max-w-md text-sm text-slate-400"),
			g.Text("Your library has workflows, just none matching this filter. Clear it, or find more on CivitAI.")),
		h.Div(h.Class("flex flex-wrap items-center justify-center gap-2"),
			facetCTA("/library?tab=workflows", "Clear filters"),
			facetCTA(discoverHref("", "Most Downloaded", "Month", discoverEcoSlug(f), discoverUseSlug(f)),
				"Find these on CivitAI"),
		),
	)
}

// discoverEcoSlug / discoverUseSlug translate a LOCAL selection into a REMOTE
// Discover facet — the "I have none of these locally, show me some" jump. The
// Unclassified buckets have no remote equivalent (they mean "no data"), so they
// translate to no facet rather than to a nonsense one.
func discoverEcoSlug(f libraryWorkflowFacets) string {
	if f.Eco == nil {
		return ""
	}
	return f.Eco.Slug
}

func discoverUseSlug(f libraryWorkflowFacets) string {
	if f.Use == nil {
		return ""
	}
	return f.Use.Slug
}
