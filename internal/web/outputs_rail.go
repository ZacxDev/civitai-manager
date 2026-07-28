package web

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/ZacxDev/civitai-manager/internal/store"
	g "maragu.dev/gomponents"
	h "maragu.dev/gomponents/html"
)

// outputsRailLimit is how many recent generations the GLOBAL outputs rail shows.
// It is deliberately small: the rail is app chrome rendered on every page, so its
// store query must stay a fixed, bounded read (see store.ListRecentGenerations,
// which additionally clamps to its own hard cap).
const outputsRailLimit = 12

// outputsRailSettingKey persists the rail's collapsed state, mirroring the
// theme/NSFW settings idiom (a CSRF-protected POST → settings store → HX-Refresh).
const outputsRailSettingKey = "outputs_rail_collapsed"

// railData is the per-request state of the global "Recent outputs" rail. Its ZERO
// VALUE renders no rail at all, which is what unit tests that call a page builder
// without shell state get.
type railData struct {
	// Gens are the most recent generations across ALL workflows (never more than
	// outputsRailLimit). Empty means a fresh install / nothing captured yet — the
	// rail is then omitted entirely rather than rendered as a dead empty column.
	Gens []store.Generation
	// Collapsed is the persisted collapse state (desktop rail width; the mobile
	// drawer's open/closed state is ephemeral and lives in the DOM).
	Collapsed bool
}

// visible reports whether the rail renders for this request. It is the SINGLE
// predicate behind the rail markup, the shell's reserved width, and the nav's
// drawer button, so those three can never disagree.
//
// NSFW: a captured generation carries no per-image rating signal, so the whole
// rail is treated as ONE surface — mode "hide" OMITS it server-side (no markup at
// all), "blur" renders it blurred (hover/focus reveals), "show" renders it plain.
//
// The hide test deliberately reads the RAW stored/passed mode rather than
// normalizeNSFWMode's output: that helper MIGRATES a stored "hide" to blur (the
// navbar toggle dropped the hide state), so normalizing first would make the omit
// branch unreachable. Reading the raw value keeps the "hide omits server-side"
// capability real and testable, per the CLAUDE.md invariant.
func (rd railData) visible(nsfwMode string) bool {
	if len(rd.Gens) == 0 {
		return false
	}
	return !strings.EqualFold(strings.TrimSpace(nsfwMode), NSFWHide)
}

// railShellClass is the <body> class that reserves the rail's width on desktop.
// Empty when no rail renders, so the shell is byte-identical to the pre-rail one.
func railShellClass(rd railData, nsfwMode string) string {
	if !rd.visible(nsfwMode) {
		return ""
	}
	if rd.Collapsed {
		return "cm-shell-rail-collapsed"
	}
	return "cm-shell-rail"
}

// railOf picks the optional shell state a page builder was handed. Page builders
// take it as a trailing variadic so the (many) unit tests that render a page
// without any shell state keep compiling and keep rendering a rail-free shell.
func railOf(rd []railData) railData {
	if len(rd) > 0 {
		return rd[0]
	}
	return railData{}
}

// outputsRail renders the global right-hand "Recent outputs" sidebar: the most
// recent generations across ALL workflows, each linking to its detail page exactly
// like a gallery tile. It returns nil when the rail is not visible.
//
// Layout is CSS-only (.cm-rail in app.css): a fixed right column on desktop that
// collapses to a thin labelled edge, and an off-canvas drawer below 1024px opened
// from the nav. It is a sibling of <main>, never inside it, so it cannot interfere
// with any htmx poll target in the page body.
func outputsRail(rd railData, csrf, nsfwMode string) g.Node {
	// visible() reads the RAW mode (see its doc) — normalize only afterwards, for
	// the blur/show distinction.
	if !rd.visible(nsfwMode) {
		return nil
	}
	mode := normalizeNSFWMode(nsfwMode)

	tiles := make([]g.Node, 0, len(rd.Gens))
	for _, gen := range rd.Gens {
		tiles = append(tiles, railTile(gen))
	}

	// Collapse control: POSTs the NEXT state with the CSRF token and replies
	// HX-Refresh, exactly like the theme and NSFW toggles.
	next, glyph, label := "true", "›", "Collapse recent outputs"
	if rd.Collapsed {
		next, glyph, label = "false", "‹", "Expand recent outputs"
	}
	collapse := civButton("subtle", "sm",
		[]g.Node{
			h.Type("button"),
			h.Class("cm-rail-collapse"),
			hx("post", "/settings/outputs-rail"),
			hx("vals", fmt.Sprintf(`{"collapsed":%q,"csrf_token":%q}`, next, csrf)),
			hx("swap", "none"),
			g.Attr("aria-label", label),
		},
		h.Span(g.Attr("aria-hidden", "true"), g.Text(glyph)),
	)

	// Mobile-only close control for the drawer.
	closeBtn := civButton("subtle", "sm",
		[]g.Node{
			h.Type("button"),
			h.Class("cm-rail-close"),
			g.Attr("onclick", "cmRailDrawer(false)"),
			g.Attr("aria-label", "Close recent outputs"),
		},
		h.Span(g.Attr("aria-hidden", "true"), g.Text("✕")),
	)

	aside := h.Aside(
		h.ID("cm-rail"),
		h.Class("cm-rail"),
		dataAttr("collapsed", strconv.FormatBool(rd.Collapsed)),
		dataAttr("open", "false"),
		g.If(mode == NSFWBlur, dataAttr("nsfw", "blur")),
		g.Attr("aria-label", "Recent outputs"),
		h.Div(h.Class("cm-rail-head"),
			h.Span(h.Class("cm-rail-title"), g.Text("Recent outputs")),
			collapse,
			closeBtn,
		),
		// Vertical label shown only while the desktop rail is collapsed, so the thin
		// edge still says what it is.
		h.Span(h.Class("cm-rail-vlabel"), g.Text("Recent outputs")),
		h.Div(h.Class("cm-rail-body"), g.Group(tiles)),
		h.Div(h.Class("cm-rail-foot"),
			h.A(h.Href("/outputs"), h.Class("cm-rail-all"), g.Text("View all outputs →")),
		),
	)

	return g.Group([]g.Node{
		h.Div(h.ID("cm-rail-scrim"), h.Class("cm-rail-scrim"),
			dataAttr("open", "false"),
			g.Attr("onclick", "cmRailDrawer(false)"),
			g.Attr("aria-hidden", "true")),
		aside,
		railDrawerScript(),
	})
}

// railTile is one rail entry: the generation's first image as a lazy thumbnail
// linking to /outputs/{id} (the same destination the gallery tiles use), captioned
// with the escaped workflow label and a relative time.
func railTile(gen store.Generation) g.Node {
	label := generationLabel(gen)

	var thumb g.Node
	if gen.FirstImageID > 0 {
		thumb = h.Img(
			h.Src(generationImgURL(gen.FirstImageID)),
			h.Alt(label),
			g.Attr("loading", "lazy"),
			h.Class("cm-rail-thumb"),
		)
	} else {
		thumb = h.Span(h.Class("cm-rail-nothumb"), g.Text("no image"))
	}

	return h.A(
		h.Href("/outputs/"+strconv.FormatInt(gen.ID, 10)),
		h.Class("cm-rail-item"),
		h.Title(label+" · "+humanSince(gen.CreatedAt)),
		thumb,
		h.Span(h.Class("cm-rail-cap"), g.Text(label)),
	)
}

// railNavToggle is the nav control that opens the rail drawer on narrow screens
// (hidden by CSS at the desktop breakpoint, where the rail is always on screen).
func railNavToggle() g.Node {
	return civButton("outline", "sm",
		[]g.Node{
			h.Type("button"),
			h.ID("cm-rail-open"),
			h.Class("cm-rail-open-btn"),
			g.Attr("aria-controls", "cm-rail"),
			g.Attr("aria-expanded", "false"),
			g.Attr("onclick", "cmRailDrawer(true)"),
			g.Attr("aria-label", "Open recent outputs"),
		},
		h.Span(g.Attr("aria-hidden", "true"), g.Text("▤")),
	)
}

// railDrawerScript toggles the EPHEMERAL mobile drawer state (data-open on the
// rail + scrim, aria-expanded on the nav button). Vendored inline — no CDN, no
// framework. The persisted desktop collapse state is server state and does NOT go
// through here.
func railDrawerScript() g.Node {
	return h.Script(g.Raw(`
function cmRailDrawer(open){
  var v = open ? 'true' : 'false';
  var r = document.getElementById('cm-rail');
  var s = document.getElementById('cm-rail-scrim');
  var b = document.getElementById('cm-rail-open');
  if (r) { r.setAttribute('data-open', v); }
  if (s) { s.setAttribute('data-open', v); }
  if (b) { b.setAttribute('aria-expanded', v); }
}
document.addEventListener('keydown', function(e){
  if (e.key === 'Escape') { cmRailDrawer(false); }
});
`))
}
