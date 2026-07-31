package web

import (
	g "maragu.dev/gomponents"
	h "maragu.dev/gomponents/html"
)

// brandMarkSVG is the app's logo: a diamond outline with a solid diamond core.
//
// WHY AN INLINE <svg> AND NOT AN IMAGE OR AN ICON FONT. The offline/no-CDN
// invariant forbids fetching a font or a remote image, and a separate asset file
// would be one more request on every page. Inline markup is zero requests, and —
// the reason the geometry is drawn with `currentColor` rather than a literal —
// it inherits the link's color, so the mark re-themes with `data-theme` for free
// and can never drift from the wordmark beside it. There is no hex value
// anywhere in this file for the same reason: a literal here would be exactly the
// hardcoded colour the theme system exists to eliminate.
//
// GEOMETRY. Two concentric diamonds on a 24×24 grid, rendered at 22px in the
// nav. The outer one is a 2-unit stroke (≈1.8px at nav size) whose vertices land
// on integer coordinates, and the core is a filled diamond of exactly half its
// radius — so at 1x both shapes' edges fall on or very near device pixels
// instead of smearing across two. It reads as a mark, not as an accidental
// rotated square, because of the hole between the two.
//
// `aria-hidden="true"` + `focusable="false"` are load-bearing: the mark sits
// INSIDE the brand link next to visible wordmark text, so the link already has
// an accessible name. An exposed <title> here (or an aria-label on the link)
// would make a screen reader announce the app name twice — see brandLink.
const brandMarkSVG = `<svg class="cm-brand-mark" viewBox="0 0 24 24" width="22" height="22" fill="none" ` +
	`stroke="currentColor" stroke-width="2" stroke-linejoin="round" aria-hidden="true" focusable="false">` +
	`<path d="M12 2 L22 12 L12 22 L2 12 Z"/>` +
	`<path d="M12 7.5 L16.5 12 L12 16.5 L7.5 12 Z" fill="currentColor" stroke="none"/>` +
	`</svg>`

// faviconHref is the app's tab icon: the SAME mark, as a standalone document at
// assets/favicon.svg. It is EMBEDDED and served by the existing /assets/
// FileServer (see assets.go's go:embed list) — no external fetch, so the
// offline invariant holds. There was no favicon at all before this; browsers
// were requesting /favicon.ico and 404ing on every page load.
//
// IT CANNOT BE brandMarkSVG. A favicon renders OUTSIDE the page, in browser
// chrome with no cascade, so `currentColor` resolves to the initial black and
// the mark would vanish on a dark tab strip. The file therefore pins the design
// system's brand blue (--civitai-color-primary, #228BE6) as a literal — the one
// place in this app where a hardcoded colour is correct, precisely because there
// is no token to resolve. #228BE6 is mid-tone enough to read against both light
// and dark tab strips, so it needs no prefers-color-scheme branch (and favicon
// renderers vary in whether they honour an embedded <style> at all;
// presentation attributes are the portable choice).
const faviconHref = "/assets/favicon.svg"

// brandName / brandNameShort are the wordmark's two forms. The full one keeps
// the binary's own name so the nav, the <title> suffix and `civitai-manager
// --version` all say the same thing; the short one is what survives at 390px,
// where the full wordmark would eat the link strip.
const (
	brandName      = "civitai-manager"
	brandNameShort = "cm"
)

// brandLink is the nav's home link: the mark plus the wordmark, as ONE anchor to
// "/". It replaced both the old text-only brand and the separate "Dashboard" nav
// link — two controls that went to the same place.
//
// EXACTLY ONE ACCESSIBLE NAME, and it comes from the visible text. The link
// carries no aria-label and the <svg> is aria-hidden, so the name is whichever
// wordmark span is visible. The two spans are `hidden sm:inline` / `sm:hidden`,
// i.e. `display: none` at the other breakpoint — and content in a `display: none`
// subtree is excluded from the accessible-name computation, so exactly one of
// them contributes at any viewport. Adding an aria-label "civitai-manager" here
// would override the visible text and announce the name twice over.
func brandLink() g.Node {
	return h.A(
		h.Href("/"),
		h.Class("cm-brand shrink-0 flex items-center gap-2 font-semibold text-indigo-400"),
		g.Raw(brandMarkSVG),
		h.Span(h.Class("cm-brand-name hidden sm:inline"), g.Text(brandName)),
		h.Span(h.Class("cm-brand-name sm:hidden"), g.Text(brandNameShort)),
	)
}
