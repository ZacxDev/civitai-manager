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

// generationTile is the SHARED grid tile used by both the global gallery and the
// per-workflow section: a masonry item showing the generation's first image as a
// lazy thumbnail, linking to the detail page, captioned with the (escaped) label +
// relative time + ×N badge. Outputs are the user's OWN local generations with no
// rating signal, so they render PLAIN — no blur/reveal markup (a deliberate
// render-plain surface; see OUTPUT-GALLERY-DESIGN.md).
func generationTile(gen store.Generation) g.Node {
	detailHref := "/outputs/" + strconv.FormatInt(gen.ID, 10)

	var thumb g.Node
	if gen.FirstImageID > 0 {
		thumb = h.Img(
			h.Src(generationImgURL(gen.FirstImageID)),
			h.Alt(generationLabel(gen)),
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

	children := []g.Node{
		h.Class("cm-masonry-item group relative block aspect-square overflow-hidden rounded-md border border-slate-800 bg-slate-900"),
		thumb,
		// Caption overlay: label + meta. g.Text escapes the untrusted-ish name.
		h.Div(
			h.Class("pointer-events-none absolute inset-x-0 bottom-0 z-20 flex items-center justify-between gap-2 bg-slate-950/70 px-2 py-1 text-xs text-slate-200"),
			h.Span(h.Class("truncate"), g.Text(generationLabel(gen))),
			h.Span(h.Class("shrink-0 text-slate-400"), g.Text(strings.Join(meta, " · "))),
		),
	}
	return h.A(append([]g.Node{h.Href(detailHref)}, children...)...)
}

// generationGrid renders a masonry grid of tiles, or an empty-state note.
func generationGrid(gens []store.Generation, emptyMsg string) g.Node {
	if len(gens) == 0 {
		return h.P(h.Class("text-sm text-slate-400"), g.Text(emptyMsg))
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
	selectedWorkflow string, pageNum, total int, csrf, theme, nsfwMode string) g.Node {

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
		h.Div(h.Class("w-80"),
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
		card(generationGrid(gens, "No generations yet — run a workflow and its outputs will appear here.")),
	}

	// Pagination controls (server-side page).
	if total > outputsPageSize {
		body = append(body, outputsPagination(selectedWorkflow, pageNum, total))
	}

	return page("Outputs", theme, csrf, nsfwMode, body...)
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
		controls = append(controls, h.Span(h.Class("text-sm text-slate-600"), g.Text("← Newer")))
	}
	controls = append(controls, h.Span(h.Class("text-sm text-slate-400"),
		g.Text(fmt.Sprintf("Page %d of %d", page+1, lastPage+1))))
	if page < lastPage {
		controls = append(controls, h.A(h.Href(href(page+1)),
			h.Class("text-sm text-indigo-400 hover:text-indigo-300"), g.Text("Older →")))
	} else {
		controls = append(controls, h.Span(h.Class("text-sm text-slate-600"), g.Text("Older →")))
	}
	return h.Div(h.Class("flex items-center justify-center gap-6 pt-2"), g.Group(controls))
}

// generationDetailPage renders one generation: full images (lightbox), the params
// panel, and Re-run / Delete actions. wfExists gates the Re-run control (disabled
// when the source workflow was deleted). Render-plain.
func generationDetailPage(gen *store.Generation, images []store.GenerationImage,
	csrf, theme, nsfwMode string) g.Node {

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
	return page("Output "+id, theme, csrf, nsfwMode, body...)
}

// generationParamsCard renders the stored run params + snapshots, all escaped.
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
			items = append(items, h.Li(h.Class("text-sm text-slate-300 font-mono"), g.Text(from+" → "+to)))
		}
		extra = append(extra, h.Div(
			h.H3(h.Class("text-sm font-semibold text-slate-200 mt-3 mb-2"), g.Text("Model substitutions")),
			h.Ul(h.Class("list-disc pl-5 space-y-1"), g.Group(items))))
	}
	if len(snap.WidgetOverrides) > 0 {
		var items []g.Node
		for _, wo := range snap.WidgetOverrides {
			items = append(items, h.Li(h.Class("text-sm text-slate-300 font-mono"),
				g.Text(fmt.Sprintf("node %s · %s = %s", wo.NodeID, wo.InputName, wo.Value))))
		}
		extra = append(extra, h.Div(
			h.H3(h.Class("text-sm font-semibold text-slate-200 mt-3 mb-2"), g.Text("Parameter edits")),
			h.Ul(h.Class("list-disc pl-5 space-y-1"), g.Group(items))))
	}
	if len(snap.OptionFixes) > 0 {
		var items []g.Node
		for _, of := range snap.OptionFixes {
			items = append(items, h.Li(h.Class("text-sm text-slate-300 font-mono"),
				g.Text(fmt.Sprintf("%s: %s → %s", of.InputName, of.OldValue, of.NewValue))))
		}
		extra = append(extra, h.Div(
			h.H3(h.Class("text-sm font-semibold text-slate-200 mt-3 mb-2"), g.Text("Option fixes")),
			h.Ul(h.Class("list-disc pl-5 space-y-1"), g.Group(items))))
	}
	if len(snap.Resources) > 0 {
		var items []g.Node
		for _, r := range snap.Resources {
			items = append(items, h.Li(h.Class("text-sm text-slate-300 font-mono"), g.Text(r)))
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

// workflowOutputsSection renders the per-workflow "Recent outputs" card on the
// workflow detail page — the shared grid limited to the most recent generations,
// with a "View all →" link when there are more. Returns nil when the workflow has
// no generations (so nothing is emitted).
func workflowOutputsSection(workflowID int64, gens []store.Generation, total int) g.Node {
	if len(gens) == 0 {
		return nil
	}
	wfID := strconv.FormatInt(workflowID, 10)
	head := h.Div(h.Class("flex items-center justify-between"),
		sectionTitle("Recent outputs"),
		h.A(h.Href("/outputs?workflow="+wfID),
			h.Class("text-sm text-indigo-400 hover:text-indigo-300"),
			g.Text(fmt.Sprintf("View all %d →", total))),
	)
	return card(head, generationGrid(gens, ""))
}
