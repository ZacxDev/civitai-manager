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
func page(title, theme, csrf string, body ...g.Node) g.Node {
	if theme != "light" {
		theme = "dark"
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
			h.Class("min-h-screen bg-slate-950 text-slate-100 antialiased"),
			navbar(theme, csrf),
			h.Main(
				h.Class("mx-auto max-w-6xl px-4 py-6 space-y-6"),
				g.Group(body),
			),
		),
	)
}

func navbar(theme, csrf string) g.Node {
	return h.Nav(
		h.Class("border-b border-slate-800 bg-slate-900"),
		h.Div(
			h.Class("mx-auto max-w-6xl px-4 py-3 flex items-center gap-6"),
			h.A(h.Href("/"), h.Class("font-semibold text-indigo-400"), g.Text("civitai-manager")),
			navLink("/", "Dashboard"),
			navLink("/search", "Search"),
			navLink("/library", "Library"),
			navLink("/trash", "Trash"),
			h.Div(h.Class("ml-auto"), themeToggle(theme, csrf)),
		),
	)
}

func navLink(href, label string) g.Node {
	return h.A(
		h.Href(href),
		h.Class("text-sm text-slate-300 hover:text-white"),
		g.Text(label),
	)
}

// themeToggle renders the light/dark switch: a civitai outline button that POSTs
// the NEXT theme (with the CSRF token) to /settings/theme; the handler persists
// it in the settings store and replies HX-Refresh so the page re-renders under
// the new <html data-theme>. civitai resolves all tokens from that ancestor
// attribute, so one round-trip re-themes everything.
func themeToggle(theme, csrf string) g.Node {
	next, label := "dark", "Dark"
	if theme == "dark" {
		next, label = "light", "Light"
	}
	return civButton("outline", "sm",
		[]g.Node{
			h.Type("button"),
			hx("post", "/settings/theme"),
			hx("vals", fmt.Sprintf(`{"theme":%q,"csrf_token":%q}`, next, csrf)),
			hx("swap", "none"),
			g.Attr("aria-label", "Switch to "+next+" theme"),
		},
		g.Text(label),
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

func sectionTitle(text string) g.Node {
	return h.H2(h.Class("text-lg font-semibold text-slate-100 mb-3"), g.Text(text))
}

// badge renders a @civitai/components badge (light variant, small).
//
// The app uses semantically-colored badges (green/amber/red/blue/indigo/slate),
// but @civitai/components 0.1.1 Badge has NO data-color attribute — it is
// monochrome (primary only), unlike Alert which does carry data-color. So each
// semantic color is expressed with the DOCUMENTED token-override escape hatch:
// locally redeclare --civitai-color-primary to the matching status token. See
// REPORT.md (friction: "Badge lacks semantic color").
func badge(text, variant string) g.Node {
	attrs := []g.Node{
		dataAttr("civitai-ui", "badge"),
		dataAttr("variant", "light"),
		dataAttr("size", "sm"),
	}
	if tok := badgeToken(variant); tok != "" {
		attrs = append(attrs, h.StyleAttr("--civitai-color-primary:var(--civitai-color-"+tok+")"))
	}
	attrs = append(attrs, g.Text(text))
	return h.Span(attrs...)
}

// badgeToken maps the app's badge color name to the civitai status token used to
// re-tint the badge. "" means keep the default primary color (no override).
func badgeToken(variant string) string {
	switch variant {
	case "green":
		return "success"
	case "amber":
		return "warning"
	case "red":
		return "error"
	case "blue":
		return "info"
	case "indigo":
		return "" // already primary
	default: // slate / unknown -> neutral grey
		return "text-dimmed"
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
