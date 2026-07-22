package web

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
	g "maragu.dev/gomponents"
	h "maragu.dev/gomponents/html"
)

// NSFW display modes (persisted under nsfwSettingKey). blur is the default.
const (
	NSFWHide       = "hide"
	NSFWBlur       = "blur"
	NSFWShow       = "show"
	nsfwSettingKey = "nsfw_display"
)

// normalizeNSFWMode coerces a stored/submitted value to a known mode, defaulting
// to blur (the safe default: NSFW images are obscured until the user reveals one).
func normalizeNSFWMode(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case NSFWHide:
		return NSFWHide
	case NSFWShow:
		return NSFWShow
	default:
		return NSFWBlur
	}
}

// nsfwRankUnknown is the severity assigned to any nsfwLevel label the tool does
// not explicitly recognize as safe. It is high (above every known level) so the
// blur/hide gate FAILS CLOSED: an unknown/new label is blurred (blur mode) and
// omitted (hide mode) rather than rendered in the clear.
const nsfwRankUnknown = 99

// nsfwRank maps a CivitAI image nsfwLevel label to an ordered severity used by
// the blur/hide gate. It FAILS CLOSED: only an explicitly-recognized SAFE value
// ("" / "none") ranks 0; "Soft" and above are treated as NSFW (blurred/hidden),
// and any unrecognized/unparseable label ranks nsfwRankUnknown so a level the
// tool doesn't know is never shown un-obscured. A genuinely-absent ("") level on
// a safe image still ranks 0, so this does not blur everything.
func nsfwRank(level string) int {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "", "none":
		return 0
	case "soft":
		return 1
	case "mature":
		return 2
	case "x", "xxx":
		return 3
	default:
		return nsfwRankUnknown // unknown/unparseable → fail closed (treat as NSFW)
	}
}

// isNSFWImage reports whether an image should be treated as NSFW (rank above the
// safe threshold — Soft and higher, plus any unrecognized level).
func isNSFWImage(im civitai.ImageItem) bool { return nsfwRank(im.NSFWLevel) > 0 }

// modelDetailView bundles everything the rich model page renders. Any of the
// optional pieces (Version, Images) may be zero if the corresponding API call
// failed — the page degrades gracefully rather than erroring.
type modelDetailView struct {
	Model             *civitai.ModelDetail
	Description       string // raw author HTML; sanitized at render time
	SelectedVersionID int
	Version           *civitai.ModelVersionDetail
	PublishedAt       string
	Images            []civitai.ImageItem
	NSFWMode          string
	// loadErr carries the model-load failure (used only to classify the HTTP
	// status: a not-found model → 404, anything else → 502).
	loadErr error
}

// parseModelDescription extracts the `description` field from a raw model-detail
// JSON body. ModelDetail does not carry it as a typed field, so it is read from
// the raw bytes GetModel returns. Returns "" when absent.
func parseModelDescription(raw []byte) string {
	var body struct {
		Description string `json:"description"`
	}
	_ = json.Unmarshal(raw, &body)
	return body.Description
}

// parsePublishedAt best-effort reads a version's `publishedAt` timestamp from its
// raw JSON body (ModelVersionDetail does not type it). Returns "" when absent.
func parsePublishedAt(raw []byte) string {
	var body struct {
		PublishedAt string `json:"publishedAt"`
	}
	_ = json.Unmarshal(raw, &body)
	return strings.TrimSpace(body.PublishedAt)
}

// modelDetailPage renders the rich model detail page: header + stats, sanitized
// description, tags, a version selector with per-version detail, and a showcase
// image gallery with NSFW handling + a lightbox.
func modelDetailPage(v modelDetailView, csrf string) g.Node {
	m := v.Model
	creator := ""
	if m.Creator != nil {
		creator = m.Creator.Username
	}
	mode := normalizeNSFWMode(v.NSFWMode)

	return page(m.Name,
		modelHeaderCard(m, creator, csrf),
		g.If(strings.TrimSpace(v.Description) != "", modelDescriptionCard(v.Description)),
		g.If(len(m.Tags) > 0, modelTagsCard(m.Tags)),
		modelVersionsCard(v),
		modelGalleryCard(v.Images, mode, m.ID, csrf),
		lightboxOverlay(),
		modelPageScript(),
	)
}

func modelHeaderCard(m *civitai.ModelDetail, creator, csrf string) g.Node {
	return card(
		h.Div(
			h.Class("flex flex-wrap items-start justify-between gap-4"),
			h.Div(
				h.H1(h.Class("text-xl font-semibold"), g.Text(m.Name)),
				h.Div(
					h.Class("mt-1 flex flex-wrap items-center gap-2 text-sm text-slate-400"),
					badge(m.Type, "indigo"),
					g.If(m.NSFW, badge("NSFW", "red")),
					g.If(creator != "", h.A(h.Href("/creators/"+creator),
						h.Class("hover:underline"), g.Text("@"+creator))),
				),
				h.Div(
					h.Class("mt-2 flex flex-wrap gap-4 text-xs text-slate-400"),
					statInline("Downloads", strconv.Itoa(m.Stats.DownloadCount)),
					statInline("Likes", strconv.Itoa(m.Stats.ThumbsUpCount)),
					statInline("Comments", strconv.Itoa(m.Stats.CommentCount)),
				),
			),
			subscribeInline("model", strconv.Itoa(m.ID), "Subscribe", csrf),
		),
	)
}

func statInline(label, value string) g.Node {
	return h.Div(
		h.Span(h.Class("text-slate-500"), g.Text(label+": ")),
		h.Span(h.Class("font-medium text-slate-200"), g.Text(value)),
	)
}

// modelDescriptionCard renders the SANITIZED description HTML. The raw author
// HTML is routed through bluemonday's UGCPolicy (see sanitize.go) before g.Raw,
// so a <script>/onerror=/javascript: in a description cannot execute.
func modelDescriptionCard(rawHTML string) g.Node {
	return card(
		sectionTitle("Description"),
		h.Div(
			h.Class("prose-invert max-w-none text-sm text-slate-300 space-y-2 [&_a]:text-indigo-400 [&_a]:underline"),
			g.Raw(sanitizeDescription(rawHTML)),
		),
	)
}

func modelTagsCard(tags []string) g.Node {
	return card(
		sectionTitle("Tags"),
		h.Div(
			h.Class("flex flex-wrap gap-1.5"),
			g.Map(tags, func(t string) g.Node { return badge(t, "slate") }),
		),
	)
}

// modelVersionsCard renders the version list (each a link that reloads the page
// with that version selected) and the selected version's detail block.
func modelVersionsCard(v modelDetailView) g.Node {
	m := v.Model
	var items []g.Node
	for _, ver := range m.ModelVersions {
		selected := ver.ID == v.SelectedVersionID
		cls := "block rounded-md border border-slate-800 px-3 py-1.5 text-sm text-slate-300 hover:bg-slate-800"
		if selected {
			cls = "block rounded-md border border-indigo-600 bg-indigo-950/40 px-3 py-1.5 text-sm text-indigo-200"
		}
		items = append(items, h.A(
			h.Href(fmt.Sprintf("/models/%d?version=%d", m.ID, ver.ID)),
			h.Class(cls),
			h.Div(h.Class("flex items-center justify-between gap-2"),
				h.Span(g.Text(ver.Name)),
				g.If(ver.BaseModel != "", badge(ver.BaseModel, "blue")),
			),
		))
	}

	return card(
		sectionTitle("Versions"),
		h.Div(
			h.Class("grid gap-4 md:grid-cols-3"),
			h.Div(h.Class("space-y-1.5 md:col-span-1"), g.Group(items)),
			h.Div(h.Class("md:col-span-2"), versionDetail(v)),
		),
	)
}

// versionDetail renders the selected version's key facts: base model, trigger
// words as copy-able chips, published date, and the file list.
func versionDetail(v modelDetailView) g.Node {
	ver := v.Version
	if ver == nil {
		return h.P(h.Class("text-sm text-slate-500"), g.Text("Select a version to see its details."))
	}
	var rows []g.Node
	if ver.BaseModel != "" {
		rows = append(rows, detailRow("Base model", badge(ver.BaseModel, "blue")))
	}
	if v.PublishedAt != "" {
		rows = append(rows, detailRow("Published", h.Span(h.Class("text-sm text-slate-300"), g.Text(v.PublishedAt))))
	}
	if len(ver.TrainedWords) > 0 {
		rows = append(rows, detailRow("Trigger words", triggerWordChips(ver.TrainedWords)))
	}
	rows = append(rows, detailRow("Files", fileList(ver.Files)))

	return h.Div(h.Class("space-y-3"), g.Group(rows))
}

func detailRow(label string, value g.Node) g.Node {
	return h.Div(
		h.Div(h.Class("text-xs uppercase tracking-wide text-slate-500"), g.Text(label)),
		h.Div(h.Class("mt-1"), value),
	)
}

// triggerWordChips renders each trained/trigger word as a click-to-copy chip.
func triggerWordChips(words []string) g.Node {
	return h.Div(
		h.Class("flex flex-wrap gap-1.5"),
		g.Map(words, func(word string) g.Node {
			return h.Button(
				h.Type("button"),
				g.Attr("data-copy", word),
				g.Attr("onclick", "cmCopy(this)"),
				h.Class("cm-chip inline-flex items-center gap-1 rounded-md border border-slate-700 bg-slate-800 px-2 py-0.5 text-xs text-slate-200 hover:bg-slate-700"),
				h.Title("Click to copy"),
				g.Text(word),
				h.Span(h.Class("text-slate-500"), g.Text("⧉")),
			)
		}),
	)
}

func fileList(files []civitai.ModelVersionFile) g.Node {
	if len(files) == 0 {
		return h.P(h.Class("text-sm text-slate-500"), g.Text("No files."))
	}
	var rows []g.Node
	for _, f := range files {
		rows = append(rows, h.Li(
			h.Class("flex items-center justify-between gap-2 rounded border border-slate-800 px-2 py-1 text-xs"),
			h.Span(h.Class("truncate text-slate-300"), g.Text(f.Name)),
			h.Span(h.Class("flex shrink-0 items-center gap-2 text-slate-500"),
				g.If(f.Type != "", badge(f.Type, "slate")),
				g.Text(humanBytes(int64(f.SizeKB*1024))),
			),
		))
	}
	return h.Ul(h.Class("space-y-1"), g.Group(rows))
}

// modelGalleryCard renders the showcase image gallery with NSFW handling + the
// global display-mode control.
func modelGalleryCard(images []civitai.ImageItem, mode string, modelID int, csrf string) g.Node {
	var tiles []g.Node
	shown := 0
	for _, im := range images {
		nsfw := isNSFWImage(im)
		if nsfw && mode == NSFWHide {
			continue // hide mode omits NSFW images entirely
		}
		blur := nsfw && mode == NSFWBlur
		tiles = append(tiles, galleryTile(im, blur))
		shown++
	}

	body := g.Node(h.P(h.Class("text-sm text-slate-500"), g.Text("No showcase images available.")))
	if shown > 0 {
		body = h.Div(
			h.Class("grid grid-cols-2 gap-2 sm:grid-cols-3 lg:grid-cols-4"),
			g.Group(tiles),
		)
	}

	return card(
		h.Div(
			h.Class("mb-3 flex flex-wrap items-center justify-between gap-2"),
			sectionTitleInline("Showcase images"),
			nsfwControl(mode, modelID, csrf),
		),
		body,
	)
}

func sectionTitleInline(text string) g.Node {
	return h.H2(h.Class("text-lg font-semibold text-slate-100"), g.Text(text))
}

// nsfwControl renders the persisted global NSFW display toggle (hide/blur/show).
// Each option POSTs the new mode (persisting it) and reloads the page so the
// gallery re-renders under the new mode.
func nsfwControl(mode string, modelID int, csrf string) g.Node {
	opt := func(value, label string) g.Node {
		cls := "cursor-pointer rounded-md px-2 py-1 text-xs bg-slate-800 text-slate-400 hover:bg-slate-700"
		if mode == value {
			cls = "cursor-pointer rounded-md px-2 py-1 text-xs bg-indigo-700 text-indigo-100"
		}
		return h.Button(
			h.Type("button"),
			hx("post", "/settings/nsfw"),
			hx("vals", fmt.Sprintf(`{"mode":%q,"model_id":"%d","csrf_token":%q}`, value, modelID, csrf)),
			hx("target", "body"),
			hx("swap", "outerHTML"),
			h.Class(cls),
			g.Text(label),
		)
	}
	return h.Div(
		h.Class("flex items-center gap-1"),
		h.Span(h.Class("text-xs text-slate-500"), g.Text("NSFW:")),
		opt(NSFWHide, "Hide"),
		opt(NSFWBlur, "Blur"),
		opt(NSFWShow, "Show"),
	)
}

// galleryTile renders one showcase image. When blur is true the image is shown
// blurred behind a click-to-reveal overlay; otherwise clicking opens the
// lightbox. Generation metadata is stashed in a hidden node the lightbox shows.
func galleryTile(im civitai.ImageItem, blur bool) g.Node {
	metaID := fmt.Sprintf("cm-meta-%d", im.ID)
	imgClass := "h-full w-full cursor-zoom-in object-cover transition"
	if blur {
		imgClass += " blur-xl"
	}

	img := h.Img(
		h.Src(im.URL),
		h.Alt("showcase image"),
		h.Loading("lazy"),
		g.Attr("data-full", im.URL),
		g.Attr("data-meta", metaID),
		g.If(blur, g.Attr("data-blurred", "1")),
		g.Attr("onclick", "cmTileClick(this)"),
		h.Class(imgClass),
	)

	children := []g.Node{
		h.Class("group relative aspect-square overflow-hidden rounded-md border border-slate-800 bg-slate-900"),
		img,
	}
	if blur {
		children = append(children, h.Button(
			h.Type("button"),
			g.Attr("onclick", "cmReveal(this)"),
			h.Class("cm-reveal absolute inset-0 z-10 flex items-center justify-center bg-slate-950/40 text-xs font-medium text-slate-100"),
			g.Text("NSFW · click to reveal"),
		))
	}
	children = append(children, imageMetaHidden(metaID, im))
	return h.Div(children...)
}

// imageMetaHidden renders the (hidden) generation-metadata block the lightbox
// reveals when the image is expanded.
func imageMetaHidden(metaID string, im civitai.ImageItem) g.Node {
	meta, state := im.ParseMeta()
	var rows []g.Node
	if state == civitai.MetaOK {
		add := func(label, val string) {
			if strings.TrimSpace(val) == "" {
				return
			}
			rows = append(rows, h.Div(
				h.Class("text-xs"),
				h.Span(h.Class("text-slate-500"), g.Text(label+": ")),
				h.Span(h.Class("text-slate-200 break-words"), g.Text(val)),
			))
		}
		add("Prompt", meta.Prompt)
		add("Negative", meta.NegativePrompt)
		add("Sampler", meta.Sampler)
		add("Steps", meta.StepsString())
		add("CFG", meta.CfgScaleString())
		add("Seed", meta.SeedString())
		add("Model", meta.Model)
	}
	if len(rows) == 0 {
		rows = append(rows, h.Div(h.Class("text-xs text-slate-500"),
			g.Text("No generation metadata for this image.")))
	}
	return h.Template(
		h.ID(metaID),
		h.Div(h.Class("space-y-1"), g.Group(rows)),
	)
}

// lightboxOverlay is the single shared full-size viewer (hidden until opened by
// cmTileClick). It shows the full image and the selected image's metadata.
func lightboxOverlay() g.Node {
	return h.Div(
		h.ID("cm-lightbox"),
		g.Attr("onclick", "cmCloseLightbox(event)"),
		h.Class("fixed inset-0 z-50 hidden items-center justify-center bg-black/80 p-4"),
		h.Div(
			h.Class("flex max-h-full w-full max-w-5xl flex-col gap-3 overflow-hidden md:flex-row"),
			h.Img(h.ID("cm-lightbox-img"), h.Alt("full image"),
				h.Class("max-h-[85vh] max-w-full rounded-md object-contain")),
			h.Div(
				h.ID("cm-lightbox-meta"),
				h.Class("max-h-[85vh] w-full overflow-y-auto rounded-md bg-slate-900 p-3 md:w-80"),
			),
		),
		h.Button(
			h.Type("button"),
			g.Attr("onclick", "cmCloseLightbox()"),
			h.Class("absolute right-4 top-4 rounded-md bg-slate-800 px-3 py-1 text-sm text-slate-200 hover:bg-slate-700"),
			g.Text("Close ✕"),
		),
	)
}

// modelPageScript is the small, self-contained interaction script for the model
// page: click-to-copy chips, NSFW reveal, and the lightbox. No external JS.
func modelPageScript() g.Node {
	const js = `
function cmCopy(btn){
  var t = btn.getAttribute('data-copy') || '';
  if (navigator.clipboard) { navigator.clipboard.writeText(t); }
  var prev = btn.innerHTML;
  btn.innerHTML = 'copied ✓';
  setTimeout(function(){ btn.innerHTML = prev; }, 1200);
}
function cmReveal(btn){
  var img = btn.parentElement.querySelector('img');
  if (img){ img.classList.remove('blur-xl'); img.removeAttribute('data-blurred'); }
  btn.remove();
}
function cmTileClick(img){
  if (img.getAttribute('data-blurred')){ return; }
  cmOpenLightbox(img.getAttribute('data-full'), img.getAttribute('data-meta'));
}
function cmOpenLightbox(url, metaId){
  var box = document.getElementById('cm-lightbox');
  document.getElementById('cm-lightbox-img').src = url;
  var meta = document.getElementById('cm-lightbox-meta');
  var tpl = document.getElementById(metaId);
  meta.innerHTML = tpl ? tpl.innerHTML : '';
  box.classList.remove('hidden');
  box.classList.add('flex');
}
function cmCloseLightbox(ev){
  if (ev && ev.target && ev.target.id !== 'cm-lightbox' && ev.type === 'click') { return; }
  var box = document.getElementById('cm-lightbox');
  box.classList.add('hidden');
  box.classList.remove('flex');
  document.getElementById('cm-lightbox-img').src = '';
}
document.addEventListener('keydown', function(e){ if (e.key === 'Escape'){ cmCloseLightbox(); } });
`
	return h.Script(g.Raw(js))
}
