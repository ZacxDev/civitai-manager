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
	Creator    string // civitai creator username ("" when unknown), linked to /creators/{username}
	Type       string
	BaseModel  string
	Versions   int
	FileCount  int
	TotalBytes int64
	Images     []galleryImage
	NSFWMode   string

	// Version breakdown (Item C). Local is the versions the user actually has
	// (grouped from local files); Available is the model's version list (civitai
	// newest-first) marked with in-library / newer flags. UpdateAvailable is true
	// when the latest available version is NOT in the library, in which case
	// Latest{ID,Name} name it.
	Local           []localVersionGroup
	Available       []availableVersion
	UpdateAvailable bool
	LatestID        int
	LatestName      string

	// Subscription state for this model (Subscribed=false, SubscriptionID=0 when
	// not subscribed).
	Subscribed     bool
	SubscriptionID int64
}

// localVersionGroup is one version the user has locally: its resolved name, the
// total size of its local files, and how many files it comprises.
type localVersionGroup struct {
	VersionID int
	Name      string
	Bytes     int64
	FileCount int
}

// availableVersion is one of the model's civitai versions, tagged with whether
// the user already has it locally (InLibrary) and whether it is newer than the
// user's newest local version in list order (Newer).
type availableVersion struct {
	ID        int
	Name      string
	InLibrary bool
	Newer     bool
}

// versionBreakdown is the pure result of cross-referencing a model's available
// versions against the user's local files. It is computed by buildVersionBreakdown
// and folded into matchedModelCardView.
type versionBreakdown struct {
	Local           []localVersionGroup
	TotalBytes      int64
	Available       []availableVersion
	UpdateAvailable bool
	LatestID        int
	LatestName      string
}

// buildVersionBreakdown groups the model's local files by version and cross-references
// them against the model's available versions.
//
// Newness assumption: the civitai model-detail version summary carries NO timestamp,
// so we rely on civitai's list order, which is NEWEST-FIRST — i.e. ModelVersions[0]
// is the latest. A version is "newer" than the user's holdings if it sits ABOVE
// (lower index than) the user's newest local version in that list. When the user has
// no version that appears in the list, every listed version is treated as newer.
func buildVersionBreakdown(versions []civitai.ModelVersionSummary, files []store.LocalFile) versionBreakdown {
	var b versionBreakdown

	nameByID := make(map[int]string, len(versions))
	indexByID := make(map[int]int, len(versions))
	for i, ver := range versions {
		nameByID[ver.ID] = ver.Name
		indexByID[ver.ID] = i
	}

	// Group local files by version id (nil version id → 0, an "unknown version").
	type agg struct {
		bytes int64
		count int
	}
	byVer := map[int]*agg{}
	var seen []int // first-seen order of local version ids
	for _, f := range files {
		vid := 0
		if f.VersionID != nil {
			vid = *f.VersionID
		}
		a := byVer[vid]
		if a == nil {
			a = &agg{}
			byVer[vid] = a
			seen = append(seen, vid)
		}
		a.bytes += f.SizeBytes
		a.count++
		b.TotalBytes += f.SizeBytes
	}
	localSet := make(map[int]bool, len(byVer))
	for vid := range byVer {
		localSet[vid] = true
	}

	// Emit local groups: known versions in list (newest-first) order first, then any
	// unknown local version ids in first-seen order, for a deterministic display.
	emitted := map[int]bool{}
	add := func(vid int) {
		if emitted[vid] {
			return
		}
		emitted[vid] = true
		name := nameByID[vid]
		if name == "" {
			if vid == 0 {
				name = "Unknown version"
			} else {
				name = fmt.Sprintf("Version #%d", vid)
			}
		}
		a := byVer[vid]
		b.Local = append(b.Local, localVersionGroup{
			VersionID: vid, Name: name, Bytes: a.bytes, FileCount: a.count,
		})
	}
	for _, ver := range versions {
		if localSet[ver.ID] {
			add(ver.ID)
		}
	}
	for _, vid := range seen {
		add(vid)
	}

	// The user's newest local version index (versions are newest-first, so the FIRST
	// in-library one is the newest). len(versions) when they have none from the list.
	newestLocalIndex := len(versions)
	for i, ver := range versions {
		if localSet[ver.ID] {
			newestLocalIndex = i
			break
		}
	}

	b.Available = make([]availableVersion, 0, len(versions))
	for i, ver := range versions {
		av := availableVersion{ID: ver.ID, Name: ver.Name, InLibrary: localSet[ver.ID]}
		av.Newer = !av.InLibrary && i < newestLocalIndex
		b.Available = append(b.Available, av)
	}

	if len(versions) > 0 {
		b.LatestID = versions[0].ID
		b.LatestName = versions[0].Name
		b.UpdateAvailable = !localSet[versions[0].ID]
	}
	return b
}

// buildMatchedModelCardView assembles a matchedModelCardView from the model detail (typed +
// raw), the model's local files, and the persisted NSFW display mode. Showcase
// images are sourced from the model raw JSON's inline images[] via
// parseVersionImages — the SAME inline-image path the model page uses, never a
// separate /api/v1/images call.
func buildMatchedModelCardView(id int, m *civitai.ModelDetail, raw []byte, files []store.LocalFile, nsfwMode string, sub *store.Subscription) matchedModelCardView {
	v := matchedModelCardView{ModelID: id, NSFWMode: nsfwMode}
	for _, f := range files {
		v.TotalBytes += f.SizeBytes
	}
	v.FileCount = len(files)
	var versions []civitai.ModelVersionSummary
	if m != nil {
		v.Name = m.Name
		v.Type = m.Type
		if m.Creator != nil {
			v.Creator = m.Creator.Username
		}
		versions = m.ModelVersions
		v.Versions = len(versions)
		if len(versions) > 0 {
			v.BaseModel = versions[0].BaseModel
		}
	}
	// Version breakdown: which versions you have (grouped), which are available, and
	// whether the latest is newer than your newest local version.
	bd := buildVersionBreakdown(versions, files)
	v.Local = bd.Local
	v.Available = bd.Available
	v.UpdateAvailable = bd.UpdateAvailable
	v.LatestID = bd.LatestID
	v.LatestName = bd.LatestName
	// Subscription state.
	if sub != nil {
		v.Subscribed = true
		v.SubscriptionID = sub.ID
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
func matchedModelCard(v matchedModelCardView, csrf string) g.Node {
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
				g.If(v.Creator != "", h.A(
					h.Href("/creators/"+v.Creator),
					h.Class("mt-0.5 block text-xs text-slate-400 hover:text-slate-200"),
					g.Text("@"+v.Creator),
				)),
				h.Div(
					h.Class("mt-1 flex flex-wrap items-center gap-1.5"),
					g.If(v.Type != "", badge(v.Type, "indigo")),
					g.If(v.BaseModel != "", badge(v.BaseModel, "blue")),
				),
			),
			sizeText(v.TotalBytes),
		),
		modelCardCarousel(v.ModelID, v.Images, v.NSFWMode),
		versionBreakdownSection(v, csrf),
	)
}

// versionBreakdownSection renders the matched card's version story: an
// update-available banner when the latest version is not local, an expandable
// list of the versions the user has (with sizes), an expandable list of the
// model's available versions marked in-library/newer, and the subscribe toggle.
func versionBreakdownSection(v matchedModelCardView, csrf string) g.Node {
	var parts []g.Node

	// 1. Prominent "update available" banner → the in-app model page at that version.
	if v.UpdateAvailable && v.LatestID > 0 {
		label := v.LatestName
		if label == "" {
			label = fmt.Sprintf("Version #%d", v.LatestID)
		}
		parts = append(parts, h.A(
			h.Href(fmt.Sprintf("/models/%d?version=%d", v.ModelID, v.LatestID)),
			h.Class("block rounded-md border border-amber-600/60 bg-amber-950/30 px-3 py-2 text-sm font-medium text-amber-300 hover:bg-amber-950/50"),
			g.Text("Update available: "+label+" →"),
		))
	}

	// 2. Versions you have (expandable), with a total-size line and size-by-version.
	parts = append(parts, h.Details(
		h.Class("rounded-md border border-slate-800 px-3 py-2 text-xs text-slate-400"),
		h.Summary(
			h.Class("cursor-pointer select-none font-medium text-slate-300"),
			g.Text(fmt.Sprintf("Versions in your library (%d)", len(v.Local))),
		),
		h.Div(
			h.Class("mt-2 space-y-1"),
			statInline("Total size", humanBytes(v.TotalBytes)+" · "+strconv.Itoa(v.FileCount)+" file(s)"),
			g.Group(localVersionRows(v.Local)),
		),
	))

	// 3. Available versions (expandable), each marked in-library / newer.
	if len(v.Available) > 0 {
		parts = append(parts, h.Details(
			h.Class("rounded-md border border-slate-800 px-3 py-2 text-xs text-slate-400"),
			h.Summary(
				h.Class("cursor-pointer select-none font-medium text-slate-300"),
				g.Text(fmt.Sprintf("Available versions (%d)", len(v.Available))),
			),
			h.Div(h.Class("mt-2 space-y-1"), g.Group(availableVersionRows(v.ModelID, v.Available))),
		))
	}

	// 4. Subscribe / unsubscribe toggle.
	parts = append(parts, h.Div(h.Class("pt-1"), subscribeToggle(v.ModelID, subFromView(v), csrf)))

	return h.Div(h.Class("space-y-2"), g.Group(parts))
}

// subFromView reconstructs the minimal *store.Subscription the toggle needs from
// the view's subscription state (nil when not subscribed).
func subFromView(v matchedModelCardView) *store.Subscription {
	if !v.Subscribed {
		return nil
	}
	return &store.Subscription{ID: v.SubscriptionID, Kind: store.KindModel, ModelID: &v.ModelID}
}

// localVersionRows renders one row per local version: name, size, file count.
func localVersionRows(groups []localVersionGroup) []g.Node {
	if len(groups) == 0 {
		return []g.Node{h.Div(h.Class("text-slate-500"), g.Text("No local versions."))}
	}
	rows := make([]g.Node, 0, len(groups))
	for _, gr := range groups {
		rows = append(rows, h.Div(
			h.Class("flex items-center justify-between gap-2"),
			h.Span(h.Class("truncate text-slate-300"), g.Text(gr.Name)),
			h.Span(h.Class("shrink-0 text-slate-500"),
				g.Text(humanBytes(gr.Bytes)+" · "+strconv.Itoa(gr.FileCount)+" file(s)")),
		))
	}
	return rows
}

// availableVersionRows renders one row per available version, marking each as
// "in library" or "newer" (the latter linked to the in-app model page at that
// version). Version names are escaped via g.Text (untrusted civitai strings).
func availableVersionRows(modelID int, avs []availableVersion) []g.Node {
	rows := make([]g.Node, 0, len(avs))
	for _, av := range avs {
		var mark g.Node
		switch {
		case av.InLibrary:
			mark = badge("in library", "green")
		case av.Newer:
			mark = h.A(
				h.Href(fmt.Sprintf("/models/%d?version=%d", modelID, av.ID)),
				h.Class("text-indigo-300 hover:text-indigo-200"),
				g.Text("newer →"),
			)
		default:
			mark = h.Span(h.Class("text-slate-600"), g.Text("—"))
		}
		rows = append(rows, h.Div(
			h.Class("flex items-center justify-between gap-2"),
			h.Span(h.Class("truncate text-slate-300"), g.Text(av.Name)),
			h.Span(h.Class("shrink-0"), mark),
		))
	}
	return rows
}

// subscribeToggle renders the model's subscribe/unsubscribe control. When not
// subscribed it POSTs /models/{id}/subscribe (one-click, auto-download ON); when
// subscribed it POSTs /models/{id}/unsubscribe. The button targets ITSELF and
// swaps outerHTML, so the two handlers return an updated subscribeToggle and the
// card reflects the new state without a full reload. Both carry the CSRF token.
// The wrapping span with a stable id is the swap target.
func subscribeToggle(modelID int, sub *store.Subscription, csrf string) g.Node {
	id := fmt.Sprintf("subscribe-toggle-%d", modelID)
	if sub != nil {
		return h.Span(h.ID(id),
			civButton("subtle", "sm",
				[]g.Node{
					h.Type("button"),
					hx("post", fmt.Sprintf("/models/%d/unsubscribe", modelID)),
					hx("vals", fmt.Sprintf(`{"csrf_token":%q}`, csrf)),
					hx("target", "#"+id),
					hx("swap", "outerHTML"),
					g.Attr("aria-label", "Unsubscribe from this model"),
				},
				g.Text("Subscribed ✓ · Unsubscribe"),
			),
		)
	}
	return h.Span(h.ID(id),
		civButton("outline", "sm",
			[]g.Node{
				h.Type("button"),
				hx("post", fmt.Sprintf("/models/%d/subscribe", modelID)),
				hx("vals", fmt.Sprintf(`{"csrf_token":%q}`, csrf)),
				hx("target", "#"+id),
				hx("swap", "outerHTML"),
				g.Attr("aria-label", "Subscribe to this model"),
			},
			g.Text("Subscribe"),
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
