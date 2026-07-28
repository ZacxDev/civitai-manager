package web

import (
	"fmt"

	g "maragu.dev/gomponents"
	h "maragu.dev/gomponents/html"
)

// hx builds an htmx attribute (hx-get, hx-post, hx-target, ...).
func hx(name, value string) g.Node { return g.Attr("hx-"+name, value) }

// dataAttr builds a data-<name>="<value>" attribute.
func dataAttr(name, value string) g.Node { return g.Attr("data-"+name, value) }

// dataFlag builds a valueless data-<name> attribute (the @civitai/components
// contract uses several presence-only markers, e.g. data-civitai-ui-control).
func dataFlag(name string) g.Node { return g.Attr("data-" + name) }

// page wraps body content in the full HTML document shell.
//
// The civitai design system is served from vendored, embedded stylesheets (no
// CDN, fully offline): @civitai/theme's design tokens, @civitai/components'
// attribute-driven component CSS, and app.css which pulls the Tailwind build
// into the `app` cascade layer so the component layer wins where it must (see
// app.css for the cascade rationale). `theme` ("light"|"dark") is reflected onto
// <html data-theme> so every --civitai-* token re-resolves; `csrf` powers the
// persisted theme toggle in the nav.
//
// `rail` is the app shell's global "Recent outputs" sidebar state (see
// outputs_rail.go). Its zero value renders no rail — the shell is then
// byte-identical to the pre-rail one, which is what page builders called without
// shell state (unit tests) produce.
func page(title, theme, csrf, nsfwMode string, rail railData, body ...g.Node) g.Node {
	if theme != "light" {
		theme = "dark"
	}
	// One predicate decides the rail markup, the shell's reserved width and the
	// nav's drawer button, so they can never disagree.
	bodyClass := "min-h-screen bg-slate-950 text-slate-100 antialiased"
	if c := railShellClass(rail, nsfwMode); c != "" {
		bodyClass += " " + c
	}
	return g.El("html",
		h.Lang("en"),
		dataAttr("theme", theme),
		h.Head(
			h.Meta(h.Charset("utf-8")),
			h.Meta(h.Name("viewport"), h.Content("width=device-width, initial-scale=1")),
			h.TitleEl(g.Text(title+" · civitai-manager")),
			// Fix the cascade-layer order FIRST, before any layered stylesheet
			// loads, so civitai.components deterministically wins over the app's
			// Tailwind build regardless of <link> order (see app.css).
			g.El("style", g.Raw("@layer app, civitai.components;")),
			// Vendored civitai design system: tokens, then components.
			h.Link(h.Rel("stylesheet"), h.Href("/assets/civitai-theme.css")),
			h.Link(h.Rel("stylesheet"), h.Href("/assets/civitai-components.css")),
			// app.css @imports the Tailwind build into layer(app).
			h.Link(h.Rel("stylesheet"), h.Href("/assets/app.css")),
			h.Script(h.Src("/assets/htmx.min.js"), h.Defer()),
		),
		h.Body(
			h.Class(bodyClass),
			navbar(theme, csrf, nsfwMode, rail),
			h.Main(
				h.Class("mx-auto "+shellMeasure+" px-4 py-6 space-y-6"),
				g.Group(body),
			),
			// The rail is a SIBLING of <main>, never inside it, so it can never
			// interfere with an htmx poll target in the page body.
			outputsRail(rail, csrf, nsfwMode),
		),
	)
}

// shellMeasure is the shared max-width of the nav bar and <main> — a wide but
// still bounded cap (~1800px), deliberately NOT fully fluid. Nav and main use the
// SAME class so their left/right edges line up at every viewport.
const shellMeasure = "max-w-[1800px]"

// navbar renders the sticky top bar. Sticky positioning + stacking order live in
// .cm-nav (app.css) alongside --cm-nav-h, which the rail's top offset and the
// anchor scroll-margin both derive from — keep those in sync.
//
// MOBILE: the seven destinations stay in ONE horizontally scrollable strip
// (.cm-navlinks) rather than collapsing into a hamburger drawer. At 390px that
// keeps every destination reachable in one gesture with no JS, no focus trap and
// no second overlay competing with the rail drawer for the same corner of the
// screen; only the brand shortens. The controls (rail/NSFW/theme) never scroll.
func navbar(theme, csrf, nsfwMode string, rail railData) g.Node {
	return h.Nav(
		h.Class("cm-nav border-b border-slate-800 bg-slate-900"),
		h.Div(
			h.Class("cm-nav-inner mx-auto "+shellMeasure+" px-4 py-3 flex items-center gap-4"),
			h.A(h.Href("/"), h.Class("shrink-0 font-semibold text-indigo-400"),
				h.Span(h.Class("hidden sm:inline"), g.Text("civitai-manager")),
				h.Span(h.Class("sm:hidden"), g.Text("cm")),
			),
			h.Div(
				h.Class("cm-navlinks flex min-w-0 flex-1 items-center gap-4 overflow-x-auto"),
				navLink("/", "Dashboard"),
				navLink("/search", "Models"),
				navLink("/workflows/discover", "Workflows"),
				navLink("/outputs", "Outputs"),
				navLink("/apps/discover", "Apps"),
				navLink("/library", "Library"),
				navLink("/trash", "Trash"),
			),
			h.Div(h.Class("flex shrink-0 items-center gap-2"),
				g.If(rail.visible(nsfwMode), railNavToggle()),
				nsfwToggle(nsfwMode, csrf),
				themeToggle(theme, csrf),
			),
		),
	)
}

// nsfwToggle renders the global NSFW display control as a 2-state cycling button
// (matching the themeToggle idiom): the label shows the CURRENT mode and one
// click flips between Blur ⇄ Show. It POSTs the NEXT mode (with the CSRF token)
// to /settings/nsfw; the handler persists it and replies HX-Refresh so the
// current page re-renders under the new mode (galleries then blur/show
// accordingly). hx-swap="none" — the refresh does the re-render.
//
// The old "hide" state was removed from the cycle; normalizeNSFWMode migrates any
// stored hide to blur, so the toggle only ever surfaces Blur or Show.
func nsfwToggle(mode, csrf string) g.Node {
	mode = normalizeNSFWMode(mode)
	var next, label string
	switch mode {
	case NSFWShow:
		next, label = NSFWBlur, "NSFW: Show"
	default: // blur (the safe default; migrated hide lands here too)
		next, label = NSFWShow, "NSFW: Blur"
	}
	return civButton("outline", "sm",
		[]g.Node{
			h.Type("button"),
			hx("post", "/settings/nsfw"),
			hx("vals", fmt.Sprintf(`{"mode":%q,"csrf_token":%q}`, next, csrf)),
			hx("swap", "none"),
			g.Attr("aria-label", "Cycle NSFW display (currently "+mode+", click for "+next+")"),
		},
		g.Text(label),
	)
}

func navLink(href, label string) g.Node {
	return h.A(
		h.Href(href),
		// NOT hover:text-white: `white` maps to --civitai-color-primary-fg (#fefefe),
		// which is ALSO the light theme's surface — a 1.00:1 hover that makes the
		// link vanish. indigo-300 (the primary token) reads in both themes.
		h.Class("shrink-0 whitespace-nowrap text-sm text-slate-300 hover:text-indigo-300"),
		g.Text(label),
	)
}

// themeToggle renders the light/dark switch: a civitai outline button that POSTs
// the NEXT theme (with the CSRF token) to /settings/theme; the handler persists
// it in the settings store and replies HX-Refresh so the page re-renders under
// the new <html data-theme>. civitai resolves all tokens from that ancestor
// attribute, so one round-trip re-themes everything.
//
// The control shows a glyph (not text): in dark it shows a SUN "☀" (click →
// light), in light a MOON "☾" (click → dark). A unicode glyph keeps it
// offline-safe. Since the visible label is now an icon, the aria-label carries
// the "Switch to <next> theme" wording for assistive tech.
func themeToggle(theme, csrf string) g.Node {
	// Default (light): show a moon → switching to dark.
	next, glyph := "dark", "☾"
	if theme == "dark" {
		// In dark: show a sun → switching to light.
		next, glyph = "light", "☀"
	}
	return civButton("outline", "sm",
		[]g.Node{
			h.Type("button"),
			hx("post", "/settings/theme"),
			hx("vals", fmt.Sprintf(`{"theme":%q,"csrf_token":%q}`, next, csrf)),
			hx("swap", "none"),
			g.Attr("aria-label", "Switch to "+next+" theme"),
		},
		h.Span(g.Attr("aria-hidden", "true"), g.Text(glyph)),
	)
}

// civButton renders a button per the @civitai/components contract:
//
//	<button data-civitai-ui="button" data-variant=… data-size=…>…</button>
//
// variant is filled|light|outline|subtle, size is sm|md|lg. Extra attributes
// (type, hx-*, aria-*, disabled, …) are supplied via attrs; children are the
// button label/content.
func civButton(variant, size string, attrs []g.Node, children ...g.Node) g.Node {
	all := []g.Node{
		dataAttr("civitai-ui", "button"),
		dataAttr("variant", variant),
		dataAttr("size", size),
	}
	all = append(all, attrs...)
	all = append(all, children...)
	return h.Button(all...)
}

// btnPrimary is the filled primary button used as the submit control in forms.
func btnPrimary(children ...g.Node) g.Node {
	return civButton("filled", "md", []g.Node{h.Type("submit")}, children...)
}

// btnSecondary is a lower-emphasis outline button.
func btnSecondary(children ...g.Node) g.Node {
	return civButton("outline", "md", []g.Node{h.Type("button")}, children...)
}

// card is a padded, bordered panel — a @civitai/components card. data-with-border
// is always set: in the light palette surface==body, so a borderless card would
// be invisible (the design system documents this caveat).
func card(children ...g.Node) g.Node {
	all := []g.Node{
		dataAttr("civitai-ui", "card"),
		dataAttr("with-border", "true"),
		dataAttr("padding", "md"),
	}
	all = append(all, children...)
	return h.Div(all...)
}

// pageTitle is the ONE <h1> of a page; sectionTitle is every heading below it.
//
// sectionTitle hardcodes <h2> and was being used as the page-level title on most
// pages, so six of seven top-level pages shipped with NO <h1> at all: a screen
// reader's "jump to main heading" landed nowhere and the outline started at level
// 2 with no level 1 above it. pageTitle emits the identical classes — this is a
// pure semantics fix with zero visual change — and every page that already had a
// real <h1> (model detail, creator, workflow detail, outputs, generation detail)
// keeps its own.
func pageTitle(text string) g.Node {
	return h.H1(h.Class("text-lg font-semibold text-slate-100 mb-3"), g.Text(text))
}

func sectionTitle(text string) g.Node {
	return h.H2(h.Class("text-lg font-semibold text-slate-100 mb-3"), g.Text(text))
}

// badge renders a @civitai/components badge (light variant, small).
//
// The app uses semantically-colored badges (green/amber/red/blue/indigo/slate).
// As of @civitai/components 0.1.2 the Badge carries a native `data-color`
// intent attribute (info|success|warning|error, mirroring Alert), so semantic
// color is expressed by setting `data-color` — no per-element token override.
// Brand/neutral chips (indigo/slate) set no data-color and render in the
// default primary style.
func badge(text, variant string) g.Node {
	attrs := []g.Node{
		dataAttr("civitai-ui", "badge"),
		dataAttr("variant", "light"),
		dataAttr("size", "sm"),
	}
	if c := badgeColor(variant); c != "" {
		attrs = append(attrs, dataAttr("color", c))
	}
	attrs = append(attrs, g.Text(text))
	return h.Span(attrs...)
}

// badgeColor maps the app's badge color name to a @civitai/components Badge
// `data-color` intent (info|success|warning|error). "" means emit no data-color
// — the badge keeps the default primary style, used for brand (indigo) and
// neutral (slate) chips (the 0.1.2 Badge has no dedicated grey intent).
func badgeColor(variant string) string {
	switch variant {
	case "green":
		return "success"
	case "amber":
		return "warning"
	case "red":
		return "error"
	case "blue":
		return "info"
	default: // indigo (brand) and slate (neutral): no data-color
		return ""
	}
}

// alert renders a @civitai/components alert: role=alert + data-color, with an
// alert-body wrapper and an optional bold title. color is info|success|warning|
// error.
func alert(color, title string, body ...g.Node) g.Node {
	inner := []g.Node{dataFlag("civitai-ui-alert-body")}
	if title != "" {
		inner = append(inner, h.Div(dataFlag("civitai-ui-alert-title"), g.Text(title)))
	}
	inner = append(inner, body...)
	return h.Div(
		dataAttr("civitai-ui", "alert"),
		dataAttr("color", color),
		g.Attr("role", "alert"),
		h.Div(inner...),
	)
}

// textInput renders a @civitai/components text-input: a wrapper carrying the
// role, a bound label, and the control input. `kind` is text-input, textarea or
// number-input; controlAttrs carry name/type/value/placeholder/required/etc. on
// the control element (which already gets data-civitai-ui-control + id).
func textInput(kind, id, label string, controlAttrs ...g.Node) g.Node {
	ctrl := append([]g.Node{dataFlag("civitai-ui-control"), h.ID(id)}, controlAttrs...)
	return h.Div(
		dataAttr("civitai-ui", kind),
		h.Label(dataFlag("civitai-ui-label"), h.For(id), g.Text(label)),
		h.Input(ctrl...),
	)
}

// selectOption is one <option> for a labeled select: Value is the submitted form
// value (and the exact civitai query string, for the search filters); Label is
// the human wording shown to the user.
type selectOption struct {
	Value string
	Label string
}

// optionLabel returns the human label for value among opts, falling back to the
// value itself when it is not a known option.
func optionLabel(opts []selectOption, value string) string {
	for _, o := range opts {
		if o.Value == value {
			return o.Label
		}
	}
	return value
}

// labeledSelect renders a bound label + <select name=…> whose option matching
// selected carries the `selected` attribute. Styled with the civitai text-input
// role so it inherits the theme-aware control surface (both data-theme paths).
func labeledSelect(id, name, label string, opts []selectOption, selected string) g.Node {
	optNodes := make([]g.Node, 0, len(opts))
	for _, o := range opts {
		attrs := []g.Node{h.Value(o.Value)}
		if o.Value == selected {
			attrs = append(attrs, g.Attr("selected"))
		}
		attrs = append(attrs, g.Text(o.Label))
		optNodes = append(optNodes, h.Option(attrs...))
	}
	return h.Div(
		dataAttr("civitai-ui", "text-input"),
		h.Label(dataFlag("civitai-ui-label"), h.For(id), g.Text(label)),
		h.Select(append([]g.Node{
			dataFlag("civitai-ui-control"), h.ID(id), h.Name(name),
		}, optNodes...)...),
	)
}
