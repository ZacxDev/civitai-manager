package web

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ZacxDev/civitai-manager/internal/store"
	g "maragu.dev/gomponents"
	h "maragu.dev/gomponents/html"
)

// outputsPageSize is the number of generations rendered per gallery page.
const outputsPageSize = 48

// generationLabel is the human caption for a generation: the snapshot workflow
// name, or "workflow #id" when the name is blank, or "(deleted workflow)" when the
// source workflow has been deleted (workflow_id NULL). All snapshot text is
// untrusted-ish and rendered via g.Text by callers.
func generationLabel(gen store.Generation) string {
	if gen.WorkflowID == nil {
		if gen.WorkflowName != "" {
			return gen.WorkflowName + " (deleted)"
		}
		return "(deleted workflow)"
	}
	if gen.WorkflowName != "" {
		return gen.WorkflowName
	}
	return "workflow #" + strconv.FormatInt(*gen.WorkflowID, 10)
}

// generationImgURL is the app-owned byte-serving URL for one stored image.
func generationImgURL(imageID int64) string {
	return "/outputs/img/" + strconv.FormatInt(imageID, 10)
}

// batchHref is the browse URL for one batch. It returns "" for a generation that
// belongs to no batch, and ALSO for a batch id that would not survive
// store.ValidBatchID — the same predicate the handler validates the incoming path
// segment with, so a corrupted row can never emit a link the server would 404.
func batchHref(batchID string) string {
	if batchID == "" || !store.ValidBatchID(batchID) {
		return ""
	}
	return "/outputs/batch/" + batchID
}

// generationBatchSegment renders the tile caption's "Batch i/N" segment, or nil
// for a run that belongs to no batch.
//
// The link carries pointer-events-auto because the caption bar as a whole is
// pointer-events-none (clicks fall through it to the tile's full-bleed overlay
// anchor). This segment is the ONE part of the caption that must stay clickable,
// and it wins because the caption is an absolutely positioned z-20 box — a
// stacking context ABOVE the z-10 overlay anchor.
//
// Defensive: a half-populated row (batch_id set but index/total zero — all three
// are written together, so this should be impossible) renders as PLAIN,
// UNLINKED text rather than an honest-looking "Batch 0/0" pointing somewhere.
//
// NOTE: generationTile is shared by /outputs and the batch page itself, so on
// /outputs/batch/{id} this link points at the page the user is already on. That
// is deliberate and harmless — a self-link costs nothing and is cheaper than
// threading a "current batch" flag through every tile caller. Do not "fix" it.
func generationBatchSegment(gen store.Generation) g.Node {
	if gen.BatchID == "" {
		return nil
	}
	href := batchHref(gen.BatchID)
	if href == "" || gen.BatchIndex <= 0 || gen.BatchTotal <= 0 {
		return h.Span(h.Class("shrink-0 text-slate-400"), g.Text("Batch"))
	}
	text := "Batch " + strconv.Itoa(gen.BatchIndex) + "/" + strconv.Itoa(gen.BatchTotal)
	return h.Span(h.Class("shrink-0"),
		h.A(h.Href(href),
			h.Class("pointer-events-auto text-indigo-400 hover:text-indigo-300"),
			g.Text(text)))
}

// generationTile is the SHARED grid tile used by both the global gallery and the
// per-batch page: a masonry item showing the generation's first image as a lazy
// thumbnail, linking to the detail page, captioned with the (escaped) label +
// optional "Batch i/N" + relative time + ×N badge. Outputs are the user's OWN
// local generations with no rating signal, so they render PLAIN — no blur/reveal
// markup (a deliberate render-plain surface; see OUTPUT-GALLERY-DESIGN.md).
//
// The tile is a <div> whose detail link is a full-bleed OVERLAY anchor rather
// than an <a> wrapping the whole tile. That restructure is load-bearing: the
// caption now carries its own batch link, and a nested <a> inside an <a> is
// invalid HTML that browsers unnest — the inner link would never be clickable.
// The overlay keeps the entire tile clickable exactly as before, so it needs an
// explicit accessible name (it has no text content of its own).
func generationTile(gen store.Generation) g.Node {
	detailHref := "/outputs/" + strconv.FormatInt(gen.ID, 10)
	label := generationLabel(gen)

	var thumb g.Node
	if gen.FirstImageID > 0 {
		thumb = h.Img(
			h.Src(generationImgURL(gen.FirstImageID)),
			h.Alt(label),
			g.Attr("loading", "lazy"),
			h.Class("absolute inset-0 h-full w-full object-cover"),
		)
	} else {
		thumb = h.Div(
			h.Class("absolute inset-0 flex items-center justify-center text-xs text-slate-500"),
			g.Text("no image"),
		)
	}

	// Right-side caption: [partial · ][×N · ]relative-time. Kept in the bottom bar
	// (rather than corner badges) so only already-purged utility classes are used.
	var meta []string
	if gen.Status == store.GenerationStatusPartial {
		meta = append(meta, "partial")
	}
	if gen.ImageCount > 1 {
		meta = append(meta, "×"+strconv.Itoa(gen.ImageCount))
	}
	meta = append(meta, humanSince(gen.CreatedAt))

	return h.Div(
		h.Class("cm-masonry-item group relative block aspect-square overflow-hidden rounded-md border border-slate-800 bg-slate-900"),
		thumb,
		// Full-bleed detail link, UNDER the caption bar (z-10 vs z-20) so the
		// caption's own batch link stays reachable. aria-label because it has no
		// text content. .cm-tile-link draws its focus ring INWARD — the app-wide
		// ring is drawn outside the border box, which this tile's overflow-hidden
		// clips away entirely (see app.css); without it the tile has no visible
		// keyboard focus at all.
		h.A(h.Href(detailHref), g.Attr("aria-label", label),
			h.Class("cm-tile-link absolute inset-0 z-10")),
		// Caption overlay: label + optional batch + meta. g.Text escapes the
		// untrusted-ish name. The bar is pointer-events-none so it never steals a
		// click from the overlay anchor above.
		h.Div(
			h.Class("pointer-events-none absolute inset-x-0 bottom-0 z-20 flex items-center justify-between gap-2 bg-slate-950/70 px-2 py-1 text-xs text-slate-200"),
			// min-w-0 is redundant HERE (truncate's overflow:hidden already collapses
			// this direct flex item's automatic minimum size) but it is required by
			// TestLongUntrustedStringsCanBreak — see the pairing rationale there.
			h.Span(h.Class("min-w-0 truncate"), g.Text(label)),
			generationBatchSegment(gen),
			h.Span(h.Class("shrink-0 text-slate-400"), g.Text(strings.Join(meta, " · "))),
		),
	)
}

// generationGrid renders a masonry grid of tiles, or the guided empty state.
func generationGrid(gens []store.Generation, empty g.Node) g.Node {
	if len(gens) == 0 {
		return empty
	}
	tiles := make([]g.Node, 0, len(gens))
	for _, gen := range gens {
		tiles = append(tiles, generationTile(gen))
	}
	return h.Div(h.Class("cm-masonry"), g.Group(tiles))
}

// outputsGalleryPage is the global /outputs page: a paginated masonry grid,
// newest-first, with an optional workflow filter. Render-plain (no NSFW blur).
func outputsGalleryPage(gens []store.Generation, wfRefs []store.GenerationWorkflowRef,
	selectedWorkflow string, pageNum, total int, csrf, theme, nsfwMode string, rail ...railData) g.Node {

	// Workflow filter select. An empty value = all.
	opts := []selectOption{{Value: "", Label: "All workflows"}}
	for _, ref := range wfRefs {
		label := ref.Name
		if label == "" {
			label = "workflow #" + strconv.FormatInt(ref.WorkflowID, 10)
		}
		opts = append(opts, selectOption{Value: strconv.FormatInt(ref.WorkflowID, 10), Label: label})
	}
	filter := h.Form(
		h.Method("get"), h.Action("/outputs"),
		h.Class("flex items-end gap-3"),
		// max-w-full — see labeledInput: a fixed 320px overflows a phone card.
		h.Div(h.Class("w-80 max-w-full"),
			// onchange submits the GET form — no JS framework, offline-safe.
			g.El("span", g.Attr("onchange", "this.closest('form').submit()"),
				labeledSelect("outputs-wf", "workflow", "Filter by workflow", opts, selectedWorkflow)),
		),
	)

	header := h.Div(h.Class("flex items-center justify-between gap-4"),
		h.H1(h.Class("text-2xl font-semibold text-slate-100"), g.Text("Outputs")),
		filter,
	)

	body := []g.Node{
		header,
		h.P(h.Class("text-sm text-slate-400"),
			g.Text("Images captured from your successful workflow runs. These are your own local generations and render plain.")),
		card(generationGrid(gens, emptyState(
			"No generations yet",
			"Every image a workflow run produces is captured here automatically, with the "+
				"parameters it was made with, so you can re-run or delete it later. Run a "+
				"workflow from your library and its outputs will appear on this page.",
			"/library?tab=workflows", "Go to your workflows"))),
	}

	// Pagination controls (server-side page).
	if total > outputsPageSize {
		body = append(body, outputsPagination(selectedWorkflow, pageNum, total))
	}

	return page("Outputs", theme, csrf, nsfwMode, railOf(rail), body...)
}

// batchLabel is the human name of a batch: the run preset's snapshotted label
// when the batch came from one, else the workflow label. Both are untrusted-ish
// snapshot text — every caller renders it through g.Text.
func batchLabel(first store.Generation) string {
	if l := strings.TrimSpace(first.PresetName); l != "" {
		return l
	}
	return generationLabel(first)
}

// batchCountLine states how many of the batch's requested runs were actually
// captured.
//
// BatchTotal is N AS REQUESTED at batch start, but Stop cancels the remainder and
// a run can fail without producing images — so len(gens) is routinely LESS than
// BatchTotal. Printing a bare "8 runs" over five tiles would be a lie, so a short
// batch says so explicitly. (A row written before batch_total existed, or a
// corrupted one, reports 0 — fall back to the captured count rather than claiming
// "5 of 0".)
func batchCountLine(captured, total int) string {
	if total > captured {
		return fmt.Sprintf("%d of %d runs captured — the batch was stopped or some runs failed.", captured, total)
	}
	if captured == 1 {
		return "1 run."
	}
	return fmt.Sprintf("%d runs.", captured)
}

// batchParamsNote labels the hoisted params card HONESTLY.
//
// The card is `gens[0]` — ONE run's row — and it prints per-run fields
// (Prompt id, Captured, Images, Status) plus, under "Parameter edits", that run's
// own widget overrides. The batch's per-item SEED lives right there:
// withFreshSeeds writes a fresh comfy.NewSeed() per item and buildRunParamsSnapshot
// serializes it into each row's params. So an earlier draft reading "Every run in
// this batch shares the parameters below — only the seed differs" was FALSE: it sat
// directly above run #1's seed while claiming all N shared it.
//
// The fix is copy, not filtering: naming the card as one run's parameters is
// accurate and costs no new card variant. It degrades for a one-run batch (nothing
// to contrast against) and for a row missing its batch index/total.
//
// The trailing clause must not shrink to "…except the seed" — a delta audit caught
// that phrasing still being FALSE. The seed is not the only per-run field on show:
// Prompt id is one ComfyUI submission, and Captured/Status/Images describe this run
// alone, so at least two displayed values ALWAYS differ between runs. Name them.
func batchParamsNote(first store.Generation) string {
	const shared = " — the other runs used the same settings with different seeds. " +
		"Prompt id, capture time, status and image count are this run's alone."
	if first.BatchIndex > 0 && first.BatchTotal > 1 {
		return fmt.Sprintf("Parameters of run %d of %d%s", first.BatchIndex, first.BatchTotal, shared)
	}
	if first.BatchTotal == 1 {
		return "Parameters of this run."
	}
	return "Parameters of the first captured run" + shared
}

// batchGalleryPage is GET /outputs/batch/{id}: the N generations of ONE batch in
// run order, with the parameters they SHARE rendered once at the top instead of
// once per tile. That is the whole point of the surface — "here are my 8 seeds of
// the same prompt, side by side" — and it is why the params card is hoisted out
// of the grid rather than repeated.
//
// Read-only (no CSRF, no actions): per-generation Re-run/Delete stay on the
// detail page, one click away through any tile. Render-plain like every other
// outputs surface — these are the user's own local generations.
//
// gens is never empty: the handler 404s a batch with zero rows, so gens[0] is
// safe. The grid's empty branch is therefore unreachable and only exists so a
// direct unit-test call cannot panic.
func batchGalleryPage(gens []store.Generation, csrf, theme, nsfwMode string, rail ...railData) g.Node {
	first := gens[0]
	label := batchLabel(first)

	// The h1 prints an UNTRUSTED label (a preset name is clamped to 80 bytes, but a
	// workflow name is unbounded) at text-2xl inside a flex row. A flex item's
	// default min-width:auto is its min-content width, so without min-w-0 an
	// unbreakable 80-char name is ~1150px and forces the whole PAGE into a
	// horizontal scroll on a 390px phone; break-all gives it somewhere to wrap.
	// Same class of bug as metaRow's — see TestLongUntrustedStringsCanBreak.
	header := h.Div(h.Class("flex items-center justify-between gap-4"),
		h.H1(h.Class("min-w-0 break-all text-2xl font-semibold text-slate-100"),
			g.Text("Batch «"+label+"»")),
		h.A(h.Href("/outputs"), h.Class("shrink-0 text-sm text-indigo-400 hover:text-indigo-300"),
			g.Text("← All outputs")),
	)

	body := []g.Node{
		header,
		h.P(h.Class("text-sm text-slate-400"), g.Text(batchCountLine(len(gens), first.BatchTotal))),
		h.P(h.Class("text-sm text-slate-400"), g.Text(batchParamsNote(first))),
		generationParamsCard(&gens[0]),
		card(generationGrid(gens, emptyState(
			"This batch captured no images",
			"A batch records one generation per run that produced output. If every run was "+
				"stopped or failed there is nothing to show here.",
			"/outputs", "Back to all outputs"))),
	}

	return page("Batch "+label, theme, csrf, nsfwMode, railOf(rail), body...)
}

// outputsPagination renders Prev/Next controls preserving the workflow filter.
func outputsPagination(selectedWorkflow string, page, total int) g.Node {
	lastPage := (total - 1) / outputsPageSize
	href := func(p int) string {
		u := "/outputs?page=" + strconv.Itoa(p)
		if selectedWorkflow != "" {
			u += "&workflow=" + selectedWorkflow
		}
		return u
	}
	var controls []g.Node
	if page > 0 {
		controls = append(controls, h.A(h.Href(href(page-1)),
			h.Class("text-sm text-indigo-400 hover:text-indigo-300"), g.Text("← Newer")))
	} else {
		controls = append(controls, h.Span(h.Class("cm-disabled text-sm text-slate-500"), g.Text("← Newer")))
	}
	controls = append(controls, h.Span(h.Class("text-sm text-slate-400"),
		g.Text(fmt.Sprintf("Page %d of %d", page+1, lastPage+1))))
	if page < lastPage {
		controls = append(controls, h.A(h.Href(href(page+1)),
			h.Class("text-sm text-indigo-400 hover:text-indigo-300"), g.Text("Older →")))
	} else {
		controls = append(controls, h.Span(h.Class("cm-disabled text-sm text-slate-500"), g.Text("Older →")))
	}
	return h.Div(h.Class("flex items-center justify-center gap-6 pt-2"), g.Group(controls))
}

// generationDetailPage renders one generation: full images (lightbox), the
// provenance card (source workflow + prompt + resources), the params panel, and
// Re-run / Delete actions. Render-plain.
//
// resolver is the SAME workflowResolver the workflow detail page uses — it is what
// lets the resource chips resolve a basename to a local file and a CivitAI/HF source
// link. Its zero value is safe (nothing about a file is then claimed), which is what
// a unit test calling this without a server gets.
func generationDetailPage(gen *store.Generation, images []store.GenerationImage,
	csrf, theme, nsfwMode string, resolver workflowResolver, rail ...railData) g.Node {

	id := strconv.FormatInt(gen.ID, 10)

	// Full images, each opening the shared lightbox.
	var imgNodes []g.Node
	for _, img := range images {
		url := generationImgURL(img.ID)
		imgNodes = append(imgNodes, h.Img(
			h.Src(url),
			h.Alt(img.Filename),
			g.Attr("loading", "lazy"),
			dataAttr("full", url),
			g.Attr("onclick", "cmOpenLightbox(this.getAttribute('data-full'), '', false)"),
			h.Class("w-full h-auto cursor-zoom-in rounded border border-slate-800 bg-slate-900"),
		))
	}
	imagesCard := card(
		sectionTitle("Images"),
		h.Div(h.Class("grid grid-cols-2 sm:grid-cols-3 gap-3"), g.Group(imgNodes)),
	)

	// Header + back link.
	header := h.Div(h.Class("flex items-center justify-between"),
		h.H1(h.Class("text-2xl font-semibold text-slate-100"), g.Text(generationLabel(*gen))),
		h.A(h.Href("/outputs"), h.Class("text-sm text-indigo-400 hover:text-indigo-300"),
			g.Text("← Back to Outputs")),
	)

	body := []g.Node{
		header,
		imagesCard,
		// Provenance sits directly BELOW the media and ABOVE the raw params dump: it
		// answers "what made this picture" (workflow, prompt, models), which is what
		// someone scrolling off the image is actually asking.
		generationProvenanceCard(gen, resolver),
		generationParamsCard(gen),
		generationActionsCard(gen, csrf),
		lightboxOverlay(),
		modelPageScript(),
	}
	return page("Output "+id, theme, csrf, nsfwMode, railOf(rail), body...)
}

// promptNotRecordedNote is what a generation captured BEFORE prompt capture existed
// says in place of a prompt.
//
// It is deliberately an explicit statement rather than a blank or a best guess. The
// prompt could be re-read from the linked workflow's graph today — and that is
// exactly the wrong thing to do: a rescan replaces a workflow's graph in place, so
// the text shown would be the CURRENT prompt attributed to a PAST image, with nothing
// on screen admitting the difference.
//
// Both this and promptNoneDetectedNote are deliberately written WITHOUT an
// apostrophe, so a test can assert the shipped constant directly: g.Text escapes
// `'` to `&#39;`, and a test comparing against the Go string would silently never
// match — a guard that cannot fail.
const promptNotRecordedNote = "Not recorded for this run. Prompts are captured with " +
	"the run, and this generation predates that. Reading it from the workflow now " +
	"would show that workflow as it is TODAY, which is not necessarily what made " +
	"this image."

// promptNoneDetectedNote is the OTHER empty case: capture ran and found no prompt
// input at all (an api-format workflow has no widgets_values to read, and a UI graph
// may simply carry no CLIPTextEncode-family node). Distinct wording matters — "we
// looked and there is none" is a different fact from "we never looked".
const promptNoneDetectedNote = "No prompt input was detected in the graph this run " +
	"submitted, so there is no prompt text to show."

// generationProvenanceCard answers "what made this image": the source workflow, the
// prompt that ran, and the models it referenced.
//
// It sits below the media and is built ENTIRELY from the generation row + its params
// snapshot — never from the live workflow — so an orphaned generation (workflow_id
// NULL) renders identically to a linked one apart from the link itself.
func generationProvenanceCard(gen *store.Generation, resolver workflowResolver) g.Node {
	snap := parseRunParams(gen.Params)
	return card(
		sectionTitle("Provenance"),
		generationWorkflowRow(gen),
		generationPromptBlock(snap),
		generationResourcesBlock(snap, resolver),
	)
}

// generationWorkflowRow renders the source-workflow row: a LINK when the workflow
// still exists, and plain text when it does not.
//
// workflow_id is nullable (ON DELETE SET NULL) while workflow_name is a snapshot, so
// a deleted workflow still has a name to print — but printing it as a link would send
// the user to a 404. generationLabel already encodes that convention (it appends
// "(deleted)" / returns "(deleted workflow)"), so the two renderings can never
// disagree about the text.
func generationWorkflowRow(gen *store.Generation) g.Node {
	label := generationLabel(*gen)
	// Same shape as metaRow (which takes a plain string and so cannot carry a link).
	var value g.Node
	if gen.WorkflowID != nil {
		// break-all lives on the element that PRINTS the name, not only on its <dd>
		// parent — that is what TestLongUntrustedStringsCanBreak checks, and it is right
		// to: an inline <a> is its own box and wraps by its own rules.
		value = h.A(
			h.Href("/workflows/"+strconv.FormatInt(*gen.WorkflowID, 10)),
			h.Class("break-all text-indigo-400 hover:text-indigo-300"),
			g.Text(label))
	} else {
		value = h.Span(
			h.Class("break-all text-slate-400"),
			h.Title("The source workflow was deleted, so there is nothing to link to. "+
				"The name is the snapshot taken when this generation was captured."),
			g.Text(label))
	}
	return h.Dl(h.Class("space-y-1"),
		h.Div(h.Class("flex gap-2 text-sm"),
			h.Dt(h.Class("text-slate-500 w-28 sm:w-40 shrink-0"), g.Text("From workflow")),
			// min-w-0 + break-all for metaRow's reason: a workflow name is untrusted,
			// unbounded and has no guaranteed break opportunity.
			h.Dd(h.Class("text-slate-200 min-w-0 break-all"), value),
		))
}

// generationPromptBlock renders the captured prompt(s), or states plainly why there
// is none. It NEVER falls back to reading the workflow — see promptNotRecordedNote.
//
// Each prompt keeps its own label ("Prompt (POSITIVE)", "Prompt (G)"), so a
// positive/negative pair stays distinguishable; collapsing them into one blob would
// make the negative prompt read as part of the positive one.
func generationPromptBlock(snap runParamsSnapshot) g.Node {
	head := h.H3(h.Class("text-sm font-semibold text-slate-200 mt-3 mb-2"), g.Text("Prompt"))
	switch {
	case !snap.PromptsCaptured:
		return h.Div(head, h.P(h.Class("text-sm text-slate-400"), g.Text(promptNotRecordedNote)))
	case len(snap.Prompts) == 0:
		return h.Div(head, h.P(h.Class("text-sm text-slate-400"), g.Text(promptNoneDetectedNote)))
	}
	items := make([]g.Node, 0, len(snap.Prompts))
	for _, p := range snap.Prompts {
		items = append(items, h.Div(h.Class("mb-2"),
			h.Div(h.Class("text-xs text-slate-500 min-w-0 break-all"), g.Text(promptEntryLabel(p))),
			// .cm-prompt keeps the author's own line breaks (white-space: pre-wrap);
			// break-all is what stops one unbroken 5000-char prompt from widening the
			// card past the viewport (TestLongUntrustedStringsCanBreak).
			h.P(h.Class("cm-prompt min-w-0 break-all text-sm text-slate-300"), g.Text(p.Text)),
		))
	}
	return h.Div(head, g.Group(items))
}

// promptEntryLabel is the heading above one captured prompt. It prefers the label the
// graph's own node title produced, degrades to the input name (text / text_g / text_l)
// and finally to "Prompt". The node id is appended whenever the label alone would not
// distinguish two prompts — an untitled positive/negative pair both label "Prompt",
// and captioning both identically is exactly the collapse this block exists to avoid.
func promptEntryLabel(p promptEntry) string {
	label := strings.TrimSpace(p.Label)
	if label == "" {
		label = strings.TrimSpace(p.InputName)
	}
	if label == "" {
		label = "Prompt"
	}
	if p.NodeID != "" {
		label += " · node " + p.NodeID
	}
	return label
}

// generationResourcesBlock renders the models this run referenced, through the SHARED
// .cm-res-chip component (workflow_resources.go) — the same one the workflow detail
// page uses, so a checkpoint reads identically on both surfaces and a second renderer
// can never drift from it.
func generationResourcesBlock(snap runParamsSnapshot, resolver workflowResolver) g.Node {
	resources, substituted := effectiveResources(snap)
	if len(resources) == 0 {
		return nil
	}
	kids := []g.Node{
		h.H3(h.Class("text-sm font-semibold text-slate-200 mt-3 mb-2"), g.Text("Resources used")),
		workflowResourceChips(resources, resolver),
	}
	if substituted {
		kids = append(kids, h.P(h.Class("text-xs text-slate-400 mt-2"),
			g.Text("One or more models were substituted for this run — the chips show what "+
				"actually ran; the swap is listed under Model substitutions below.")))
	}
	// Same statement the workflow detail page makes: the folder control execs a file
	// manager on the SERVER, which is meaningless when the UI is driven from another
	// device. It is gated on a chip ACTUALLY carrying the control, not merely on the
	// loopback bind — a generation whose resources are all missing from the library
	// gets no folder button at all, and explaining a control that is not on screen
	// reads as a bug (caught in a real browser, where the note sat under two chips
	// that had no button).
	if resolver.openFolder && anyResourceRevealable(resources, resolver) {
		kids = append(kids, h.P(h.Class("text-xs text-slate-400 mt-2"),
			g.Text("The folder button opens a file-manager window on the computer running civitai-manager.")))
	}
	return h.Div(kids...)
}

// anyResourceRevealable reports whether at least ONE of these resources resolves to
// a concrete, contained local file — i.e. whether any chip will actually render the
// "open containing folder" control. It asks the SAME predicate the chip does
// (resourceInfo.revealable), so the explanation and the button can never disagree.
func anyResourceRevealable(resources []string, resolver workflowResolver) bool {
	for _, res := range resources {
		info, _ := resolver.resource(filepath.Base(strings.ReplaceAll(res, "\\", "/")))
		if info.revealable() {
			return true
		}
	}
	return false
}

// effectiveResources maps the workflow's referenced-resource snapshot through the
// run's recorded substitutions, so the chips name the files that ACTUALLY ran.
//
// A substitution is a real swap of one model file for another at run time
// (comfy.ApplySubstitutions rewrites every loader input equal to the missing
// filename), and snap.Resources is the workflow's PRE-substitution list. Rendering it
// unmapped would confidently name a file this run did not use — and would mark it
// "not in your library", since the reason it was substituted is that it is missing.
//
// The keys match exactly: both the resource list and the substitution map hold the
// verbatim loader-input value from the same graph.
func effectiveResources(snap runParamsSnapshot) (resources []string, substituted bool) {
	if len(snap.Resources) == 0 {
		return nil, false
	}
	out := make([]string, 0, len(snap.Resources))
	for _, res := range snap.Resources {
		if to, ok := snap.Substitute[res]; ok && to != "" {
			out = append(out, to)
			substituted = true
			continue
		}
		out = append(out, res)
	}
	return out, substituted
}

// generationParamsCard renders the stored run params + snapshots, all escaped.
//
// Every list below prints UNTRUSTED, often unbreakable strings (model filenames,
// substitution pairs, node/widget values, resource URNs), so each carries
// break-all — without it a single long token widens the card past a phone
// viewport and puts the whole page into a horizontal scroll. metaRow does the
// same for the <dl> rows (the "Graph hash" sha256 is the worst case there).
func generationParamsCard(gen *store.Generation) g.Node {
	snap := parseRunParams(gen.Params)
	rows := []g.Node{
		metaRow("Prompt id", gen.PromptID),
		metaRow("Captured", gen.CreatedAt.Local().Format("2006-01-02 15:04")),
		metaRow("Images", strconv.Itoa(gen.ImageCount)),
		metaRow("Status", gen.Status),
	}
	if gen.BaseModel != "" {
		rows = append(rows, metaRow("Base model", gen.BaseModel))
	}
	if snap.Format != "" {
		rows = append(rows, metaRow("Format", snap.Format))
	}
	if gen.GraphHash != "" {
		rows = append(rows, metaRow("Graph hash", gen.GraphHash))
	}

	extra := []g.Node{}
	if len(snap.Substitute) > 0 {
		var items []g.Node
		for from, to := range snap.Substitute {
			items = append(items, h.Li(h.Class("text-sm text-slate-300 font-mono break-all"), g.Text(from+" → "+to)))
		}
		extra = append(extra, h.Div(
			h.H3(h.Class("text-sm font-semibold text-slate-200 mt-3 mb-2"), g.Text("Model substitutions")),
			h.Ul(h.Class("list-disc pl-5 space-y-1"), g.Group(items))))
	}
	// Parameter edits come in TWO shapes: the current UI-graph form (node + widget
	// slot index) and the legacy api-graph form (node + input name) kept for
	// generations recorded before the Parameters panel moved to widget indices. Render
	// both — showing only one silently blanks the block for every run of the other era.
	if len(snap.UIWidgetOverrides) > 0 || len(snap.WidgetOverrides) > 0 {
		var items []g.Node
		for _, wo := range snap.UIWidgetOverrides {
			items = append(items, h.Li(h.Class("text-sm text-slate-300 font-mono break-all"),
				g.Text(fmt.Sprintf("node %s · widget %s = %s", wo.NodeID, wo.widgetDisplay(), wo.Value))))
		}
		for _, wo := range snap.WidgetOverrides {
			items = append(items, h.Li(h.Class("text-sm text-slate-300 font-mono break-all"),
				g.Text(fmt.Sprintf("node %s · %s = %s", wo.NodeID, wo.InputName, wo.Value))))
		}
		extra = append(extra, h.Div(
			h.H3(h.Class("text-sm font-semibold text-slate-200 mt-3 mb-2"), g.Text("Parameter edits")),
			h.Ul(h.Class("list-disc pl-5 space-y-1"), g.Group(items))))
	}
	if len(snap.OptionFixes) > 0 {
		var items []g.Node
		for _, of := range snap.OptionFixes {
			items = append(items, h.Li(h.Class("text-sm text-slate-300 font-mono break-all"),
				g.Text(fmt.Sprintf("%s: %s → %s", of.InputName, of.OldValue, of.NewValue))))
		}
		extra = append(extra, h.Div(
			h.H3(h.Class("text-sm font-semibold text-slate-200 mt-3 mb-2"), g.Text("Option fixes")),
			h.Ul(h.Class("list-disc pl-5 space-y-1"), g.Group(items))))
	}
	if len(snap.Resources) > 0 {
		var items []g.Node
		for _, r := range snap.Resources {
			items = append(items, h.Li(h.Class("text-sm text-slate-300 font-mono break-all"), g.Text(r)))
		}
		extra = append(extra, h.Div(
			h.H3(h.Class("text-sm font-semibold text-slate-200 mt-3 mb-2"), g.Text("Referenced resources")),
			h.Ul(h.Class("list-disc pl-5 space-y-1"), g.Group(items))))
	}

	inner := append([]g.Node{sectionTitle("Run parameters"), h.Dl(h.Class("space-y-1"), g.Group(rows))}, extra...)
	return card(inner...)
}

// generationActionsCard renders the Re-run + Delete controls. Re-run is disabled
// when the source workflow was deleted (workflow_id NULL) — the image stays
// viewable forever, but there is no workflow to re-run.
func generationActionsCard(gen *store.Generation, csrf string) g.Node {
	id := strconv.FormatInt(gen.ID, 10)

	var rerun g.Node
	if gen.WorkflowID != nil {
		rerun = h.Form(
			h.Method("post"), h.Action("/outputs/"+id+"/rerun"),
			h.Class("inline"),
			csrfInput(csrf),
			btnPrimary(g.Text("Re-run this")),
		)
	} else {
		rerun = civButton("filled", "md",
			[]g.Node{h.Type("button"), g.Attr("disabled", ""),
				g.Attr("title", "The source workflow was deleted — this generation can no longer be re-run.")},
			g.Text("Re-run this"))
	}

	del := h.Form(
		h.Method("post"), h.Action("/outputs/"+id+"/delete"),
		h.Class("inline"),
		g.Attr("onsubmit", "return confirm('Delete this generation and its image files? This cannot be undone.')"),
		csrfInput(csrf),
		civButton("outline", "md", []g.Node{h.Type("submit")}, g.Text("Delete")),
	)

	return card(
		sectionTitle("Actions"),
		h.Div(h.Class("flex flex-wrap items-center gap-3"), rerun, del),
	)
}

// workflowOutputsStripLimit is how many tiles the per-workflow "Recent outputs"
// strip shows. Like the rail's limit it is deliberately small and FIXED: the strip
// renders on every workflow detail view, so its store read must be a bounded query
// (store.ListGenerations with a Limit, via the existing per-workflow filter) and
// never a scan whose cost grows with the gallery.
//
// 8 fills two rows of the 4-column desktop grid; there is no pagination here on
// purpose — "view all →" is the surface for that.
const workflowOutputsStripLimit = 8

// workflowOutputsStrip renders ONE workflow's most recent outputs as a compact
// thumbnail strip, with a "view all" link into the filtered gallery. It returns nil
// for a workflow with no captured outputs — a dead empty strip on every never-run
// workflow would be pure noise.
//
// HISTORY (this reverses a removal, deliberately): a per-workflow "Recent outputs"
// card used to live in this file and was deleted when the GLOBAL rail shipped. The
// rail did not actually supersede it: the rail is CROSS-workflow chrome answering
// "what did I make recently", while this answers "what has THIS workflow made" —
// per-workflow provenance, right beside the controls that would make more. Both now
// exist and they are not redundant.
//
// TILE RENDERER: it reuses generationTile (the gallery/batch tile), NOT railTile.
// generationTile is self-contained — aspect-square, its own overlay anchor, its own
// caption — and inherits nothing from its parent, so it drops into any grid.
// railTile's .cm-rail-item/.cm-rail-thumb/.cm-rail-cap rules are defined only under
// the .cm-rail column (and vary with its collapsed/drawer states), so lifting it into
// the page body would import the rail's geometry along with it. Its markup contract
// is pinned byte-for-byte by batch_gallery_web_test.go and is NOT touched here.
//
// Render-plain (no NSFW blur) like every outputs surface — the user's own local
// generations carry no rating signal.
func workflowOutputsStrip(workflowID int64, gens []store.Generation) g.Node {
	if len(gens) == 0 {
		return nil
	}
	href := "/outputs?workflow=" + strconv.FormatInt(workflowID, 10)
	return card(
		h.Div(h.Class("flex items-center justify-between gap-4"),
			sectionTitle("Recent outputs"),
			h.A(h.Href(href), h.Class("shrink-0 text-sm text-indigo-400 hover:text-indigo-300 mb-3"),
				g.Text("View all →")),
		),
		// generationGrid, not a hand-rolled grid: .cm-masonry is the container
		// .cm-masonry-item was written for (its margin-bottom + break-inside are the
		// column layout's gutter), so reusing the pair keeps spacing identical to the
		// gallery and introduces no new utility class into the purged build. The nil
		// empty-state is unreachable — the len==0 early return above owns that case.
		generationGrid(gens, nil),
	)
}
