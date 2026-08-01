package web

import (
	g "maragu.dev/gomponents"
	h "maragu.dev/gomponents/html"
)

// crumb is a single breadcrumb item. An empty Href marks the current page.
type crumb struct {
	Label string // untrusted — always rendered via g.Text
	Href  string // "" == current page (last crumb, rendered as plain text with aria-current="page")
}

// breadcrumbs renders a <nav aria-label="Breadcrumb"> trail. Separators are
// CSS (li+li::before), not text nodes. Labels go through g.Text for HTML
// escaping. Truncated last crumbs carry a title attribute.
func breadcrumbs(items ...crumb) g.Node {
	var lis []g.Node
	for i, c := range items {
		isLast := i == len(items)-1
		if isLast {
			lis = append(lis, h.Li(
				h.Class("cm-crumb"),
				h.Span(h.Class("truncate min-w-0"), g.Attr("aria-current", "page"), h.Title(c.Label), g.Text(c.Label)),
			))
			continue
		}
		lis = append(lis, h.Li(
			h.Class("cm-crumb"),
			h.A(h.Href(c.Href), h.Class("text-indigo-400 hover:text-indigo-300"), g.Text(c.Label)),
		))
	}
	return h.Nav(g.Attr("aria-label", "Breadcrumb"),
		h.Ol(h.Class("cm-crumbs"), g.Group(lis)),
	)
}
