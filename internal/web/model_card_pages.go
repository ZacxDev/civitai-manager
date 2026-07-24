package web

import (
	"fmt"
	"strconv"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
	"github.com/ZacxDev/civitai-manager/internal/store"
	g "maragu.dev/gomponents"
	h "maragu.dev/gomponents/html"
)

// matchedModelCardView bundles everything the enriched (lazy-loaded) matched-model card
// renders. It is assembled by buildMatchedModelCardView from a cached/fetched
// ModelDetail plus the model's local files.
type matchedModelCardView struct {
	ModelID    int
	Name       string
	Type       string
	BaseModel  string
	Versions   int
	FileCount  int
	TotalBytes int64
	Images     []galleryImage
	NSFWMode   string
}

// buildMatchedModelCardView assembles a matchedModelCardView from the model detail (typed +
// raw), the model's local files, and the persisted NSFW display mode. Showcase
// images are sourced from the model raw JSON's inline images[] via
// parseVersionImages — the SAME inline-image path the model page uses, never a
// separate /api/v1/images call.
func buildMatchedModelCardView(id int, m *civitai.ModelDetail, raw []byte, files []store.LocalFile, nsfwMode string) matchedModelCardView {
	v := matchedModelCardView{ModelID: id, NSFWMode: nsfwMode}
	for _, f := range files {
		v.TotalBytes += f.SizeBytes
	}
	v.FileCount = len(files)
	if m != nil {
		v.Name = m.Name
		v.Type = m.Type
		v.Versions = len(m.ModelVersions)
		if len(m.ModelVersions) > 0 {
			v.BaseModel = m.ModelVersions[0].BaseModel
		}
	}
	// versionID 0 → the first listed version's inline images.
	v.Images = parseVersionImages(nil, raw, 0)
	return v
}

// modelCardLazy is the placeholder container rendered IMMEDIATELY in the results
// view for one matched model: it shows what is already known (model id, file
// count, total size) and lazy-loads the enriched card (name + carousel +
// details) via htmx (hx-get load), replacing itself (outerHTML) with the
// server-rendered modelCard. The browser naturally throttles the concurrent
// lazy loads.
func modelCardLazy(gr fileGroup) g.Node {
	id := gr.modelID
	var total int64
	for _, f := range gr.files {
		total += f.SizeBytes
	}
	return card(
		h.ID(fmt.Sprintf("model-card-%d", id)),
		hx("get", fmt.Sprintf("/library/model-card/%d", id)),
		hx("trigger", "load"),
		hx("swap", "outerHTML"),
		h.Class("space-y-3"),
		h.Div(
			h.Class("flex items-center justify-between gap-3"),
			h.A(
				h.Href("/models/"+strconv.Itoa(id)),
				h.Class("text-base font-semibold text-indigo-300 hover:text-indigo-200"),
				g.Text("Model #"+strconv.Itoa(id)),
			),
			sizeText(total),
		),
		h.Div(
			h.Class("flex items-center gap-2 text-xs text-slate-500"),
			spinnerGlyph(),
			g.Text(fmt.Sprintf("Loading details… %d file(s)", len(gr.files))),
		),
	)
}

// modelCard is the enriched matched-model card served by handleModelCard: the
// model name (linked to its page), type + base-model badges, a showcase-image
// carousel (NSFW-respecting), and key details (versions, local file count,
// total size).
func matchedModelCard(v matchedModelCardView) g.Node {
	name := v.Name
	if name == "" {
		name = "Model #" + strconv.Itoa(v.ModelID)
	}
	return card(
		h.ID(fmt.Sprintf("model-card-%d", v.ModelID)),
		h.Class("space-y-3"),
		h.Div(
			h.Class("flex items-start justify-between gap-3"),
			h.Div(
				h.A(
					h.Href("/models/"+strconv.Itoa(v.ModelID)),
					h.Class("text-base font-semibold text-indigo-300 hover:text-indigo-200"),
					g.Text(name),
				),
				h.Div(
					h.Class("mt-1 flex flex-wrap items-center gap-1.5"),
					g.If(v.Type != "", badge(v.Type, "indigo")),
					g.If(v.BaseModel != "", badge(v.BaseModel, "blue")),
				),
			),
			sizeText(v.TotalBytes),
		),
		modelCardCarousel(v.ModelID, v.Images, v.NSFWMode),
		h.Div(
			h.Class("flex flex-wrap gap-x-4 gap-y-1 text-xs text-slate-400"),
			statInline("Versions", strconv.Itoa(v.Versions)),
			statInline("Local files", strconv.Itoa(v.FileCount)),
			statInline("Size", humanBytes(v.TotalBytes)),
		),
	)
}

// modelCardError renders a graceful fallback card when the model detail could
// not be fetched (and no cache entry exists): the file count/size still show,
// with a muted note, so the results view degrades rather than erroring.
func modelCardError(id, fileCount int, total int64, msg string) g.Node {
	return card(
		h.ID(fmt.Sprintf("model-card-%d", id)),
		h.Class("space-y-2"),
		h.Div(
			h.Class("flex items-center justify-between gap-3"),
			h.A(
				h.Href("/models/"+strconv.Itoa(id)),
				h.Class("text-base font-semibold text-indigo-300 hover:text-indigo-200"),
				g.Text("Model #"+strconv.Itoa(id)),
			),
			sizeText(total),
		),
		h.P(h.Class("text-xs text-amber-400"), g.Text(msg)),
		h.Div(
			h.Class("flex flex-wrap gap-x-4 text-xs text-slate-400"),
			statInline("Local files", strconv.Itoa(fileCount)),
			statInline("Size", humanBytes(total)),
		),
	)
}

// modelCardCarousel renders the model's showcase images as a horizontal
// scroll-snap carousel, honoring the persisted NSFW display mode exactly as the
// model page does (hide omits, blur obscures behind click-to-reveal, show
// reveals) — it never re-flags or exposes NSFW. Each tile reuses galleryTile
// (and thus the shared lightbox on the results page) with a per-model-namespaced
// meta id so multiple carousels don't collide.
func modelCardCarousel(modelID int, images []galleryImage, mode string) g.Node {
	mode = normalizeNSFWMode(mode)
	var tiles []g.Node
	shown := 0
	for i, im := range images {
		nsfw := isNSFWLevel(im.NSFWLevel)
		if nsfw && mode == NSFWHide {
			continue // hide mode omits NSFW images entirely
		}
		blur := nsfw && mode == NSFWBlur
		tiles = append(tiles, h.Div(
			h.Class("cm-carousel-item"),
			galleryTile(im, fmt.Sprintf("cm-meta-m%d-%d", modelID, i), blur),
		))
		shown++
	}
	if shown == 0 {
		return h.P(h.Class("text-xs text-slate-500"), g.Text("No showcase images."))
	}
	strip := h.Div(h.Class("cm-carousel"), g.Group(tiles))
	if shown <= 1 {
		return h.Div(h.Class("cm-carousel-wrap"), strip)
	}
	return h.Div(
		h.Class("cm-carousel-wrap"),
		strip,
		carouselButton("prev", "‹"),
		carouselButton("next", "›"),
	)
}

// carouselButton renders a prev/next scroll control for the carousel; the tiny
// cmCarouselScroll helper (libraryCarouselScript) scrolls the sibling strip.
func carouselButton(dir, glyph string) g.Node {
	delta := "-1"
	cls := "cm-carousel-btn cm-carousel-btn-prev"
	aria := "Scroll to previous images"
	if dir == "next" {
		delta = "1"
		cls = "cm-carousel-btn cm-carousel-btn-next"
		aria = "Scroll to next images"
	}
	return h.Button(
		h.Type("button"),
		h.Class(cls),
		g.Attr("aria-label", aria),
		g.Attr("onclick", "cmCarouselScroll(this,"+delta+")"),
		g.Text(glyph),
	)
}

// libraryCarouselScript is the tiny, self-contained (no CDN) prev/next scroller
// for the model-card carousels. Scrolling/snapping itself is CSS-only
// (.cm-carousel); this only wires the optional buttons. Defined idempotently so
// it survives every htmx swap of the results fragment.
func libraryCarouselScript() g.Node {
	const js = `
function cmCarouselScroll(btn, dir){
  var wrap = btn.closest('.cm-carousel-wrap');
  if(!wrap){ return; }
  var strip = wrap.querySelector('.cm-carousel');
  if(!strip){ return; }
  strip.scrollBy({ left: dir * strip.clientWidth * 0.8, behavior: 'smooth' });
}
`
	return h.Script(g.Raw(js))
}
