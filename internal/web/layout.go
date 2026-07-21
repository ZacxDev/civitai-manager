package web

import (
	g "maragu.dev/gomponents"
	h "maragu.dev/gomponents/html"
)

// hx builds an htmx attribute (hx-get, hx-post, hx-target, ...).
func hx(name, value string) g.Node { return g.Attr("hx-"+name, value) }

// page wraps body content in the full HTML document shell: embedded Tailwind
// CSS, embedded htmx, and a top nav. Self-contained -- no external requests.
func page(title string, body ...g.Node) g.Node {
	return g.El("html",
		h.Lang("en"),
		h.Head(
			h.Meta(h.Charset("utf-8")),
			h.Meta(h.Name("viewport"), h.Content("width=device-width, initial-scale=1")),
			h.TitleEl(g.Text(title+" · civitai-manager")),
			h.Link(h.Rel("stylesheet"), h.Href("/assets/output.css")),
			h.Script(h.Src("/assets/htmx.min.js"), h.Defer()),
		),
		h.Body(
			h.Class("min-h-screen bg-slate-950 text-slate-100 antialiased"),
			navbar(),
			h.Main(
				h.Class("mx-auto max-w-6xl px-4 py-6 space-y-6"),
				g.Group(body),
			),
		),
	)
}

func navbar() g.Node {
	return h.Nav(
		h.Class("border-b border-slate-800 bg-slate-900"),
		h.Div(
			h.Class("mx-auto max-w-6xl px-4 py-3 flex items-center gap-6"),
			h.A(h.Href("/"), h.Class("font-semibold text-indigo-400"), g.Text("civitai-manager")),
			navLink("/", "Dashboard"),
			navLink("/search", "Search"),
			navLink("/library", "Library"),
			navLink("/trash", "Trash"),
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

// card is a padded panel container.
func card(children ...g.Node) g.Node {
	return h.Div(
		h.Class("rounded-lg border border-slate-800 bg-slate-900 p-4"),
		g.Group(children),
	)
}

func sectionTitle(text string) g.Node {
	return h.H2(h.Class("text-lg font-semibold text-slate-100 mb-3"), g.Text(text))
}

// badge renders a small pill with a color variant.
func badge(text, variant string) g.Node {
	base := "inline-flex items-center rounded-full px-2 py-0.5 text-xs font-medium "
	color := "bg-slate-700 text-slate-200"
	switch variant {
	case "green":
		color = "bg-emerald-900 text-emerald-200"
	case "amber":
		color = "bg-amber-900 text-amber-200"
	case "red":
		color = "bg-rose-900 text-rose-200"
	case "indigo":
		color = "bg-indigo-900 text-indigo-200"
	case "blue":
		color = "bg-sky-900 text-sky-200"
	}
	return h.Span(h.Class(base+color), g.Text(text))
}

// btn renders a primary or secondary button-styled element.
func btnPrimary(children ...g.Node) g.Node {
	return h.Button(
		h.Class("rounded-md bg-indigo-600 px-3 py-1.5 text-sm font-medium text-white hover:bg-indigo-500"),
		g.Group(children),
	)
}

func btnSecondary(children ...g.Node) g.Node {
	return h.Button(
		h.Class("rounded-md border border-slate-700 bg-slate-800 px-3 py-1.5 text-sm text-slate-200 hover:bg-slate-700"),
		g.Group(children),
	)
}
