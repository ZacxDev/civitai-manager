package web

import (
	"fmt"
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
			h.Span(h.Class("truncate"), g.Text(label)),
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
func batchParamsNote(first store.Generation) string {
	const shared = " — every run shares these except the seed."
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

// generationDetailPage renders one generation: full images (lightbox), the params
// panel, and Re-run / Delete actions. wfExists gates the Re-run control (disabled
// when the source workflow was deleted). Render-plain.
func generationDetailPage(gen *store.Generation, images []store.GenerationImage,
	csrf, theme, nsfwMode string, rail ...railData) g.Node {

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
		generationParamsCard(gen),
		generationActionsCard(gen, csrf),
		lightboxOverlay(),
		modelPageScript(),
	}
	return page("Output "+id, theme, csrf, nsfwMode, railOf(rail), body...)
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

// NOTE: the per-workflow "Recent outputs" card that used to live here was
// REMOVED — the global recent-outputs rail (outputs_rail.go), rendered by the app
// shell on every page, supersedes it. Per-workflow browsing is still one click
// away at /outputs?workflow=<id> via the gallery's workflow filter.
