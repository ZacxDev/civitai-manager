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
func page(title, theme, csrf string, mr maturityRange, rail railData, body ...g.Node) g.Node {
	if theme != "light" {
		theme = "dark"
	}
	// One predicate decides the rail markup, the shell's reserved width and the
	// nav's drawer button, so they can never disagree.
	bodyClass := "min-h-screen bg-slate-950 text-slate-100 antialiased"
	if c := railShellClass(rail); c != "" {
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
			// The tab icon — the SAME geometric mark the nav's brand link renders,
			// embedded and served from /assets (see brand.go). Declaring it stops the
			// browser probing /favicon.ico and 404ing on every page load.
			h.Link(h.Rel("icon"), h.Type("image/svg+xml"), h.Href(faviconHref)),
			h.Script(h.Src("/assets/htmx.min.js"), h.Defer()),
		),
		h.Body(
			h.Class(bodyClass),
			navbar(theme, csrf, mr, rail),
			h.Main(
				h.Class("mx-auto "+shellMeasure+" px-4 py-6 space-y-6"),
				g.Group(body),
			),
			// The rail is a SIBLING of <main>, never inside it, so it can never
			// interfere with an htmx poll target in the page body.
			outputsRail(rail, csrf),
			// Lazy-attach thumbnail video sources. It lives in the SHARED layout
			// because the recent-outputs rail renders on EVERY page, so a video
			// thumbnail can appear on any of them — not only /outputs. It is a
			// no-op on a page with no video tile.
			lazyVideoScript(),
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
// MOBILE: the destinations stay in ONE horizontally scrollable strip
// (.cm-navlinks) rather than collapsing into a hamburger drawer. At 390px that
// keeps every destination reachable in one gesture with no JS, no focus trap and
// no second overlay competing with the rail drawer for the same corner of the
// screen; only the brand shortens. The controls (rail/maturity/theme) never scroll.
//
// THE STRIP HOLDS FIVE ENTRIES, NOT SEVEN, and the difference is the point:
//
//   - The BRAND is the home link. "Dashboard" was a second control going to the
//     same place as the wordmark beside it, so it is gone and the wordmark grew a
//     mark (brandLink).
//   - "Models"/"Workflows" read as "my models"/"my workflows" next to a Library
//     entry that is also about models and workflows. They are RENAMED
//     "Find models"/"Find workflows" — the routes (/search,
//     /workflows/discover) are unchanged.
//   - "Library" became a <details> DISCLOSURE over its two real surfaces
//     (libraryMenu), because /library has always been a two-tab page and the tab
//     was invisible from the nav.
//   - "Outputs" left the strip. /outputs is STILL ROUTED and still linked — from
//     the recent-outputs rail's heading and its foot link (outputs_rail.go). The
//     rail is app chrome on every page, so the destination did not lose its
//     entry point. HONEST LIMIT, recorded because it is a real gap: the rail
//     renders only when at least one generation exists (railData.visible()), so
//     on a fresh install /outputs is reachable by URL but has no in-app link.
//     That is the state in which the page has nothing to show anyway, and its own
//     empty state points at the Library — but it IS a reachability edge, not a
//     clean win.
//   - "Trash" became "Disks", which absorbs the quarantine table it used to show
//     and adds per-disk capacity. /trash redirects there (handleTrashRedirect).
func navbar(theme, csrf string, mr maturityRange, rail railData) g.Node {
	return h.Nav(
		h.Class("cm-nav border-b border-slate-800 bg-slate-900"),
		h.Div(
			h.Class("cm-nav-inner mx-auto "+shellMeasure+" px-4 py-3 flex items-center gap-4"),
			brandLink(),
			h.Div(
				h.Class("cm-navlinks flex min-w-0 flex-1 items-center gap-4 overflow-x-auto"),
				navLink("/search", "Find models"),
				navLink("/workflows/discover", "Find workflows"),
				navLink("/apps/discover", "Apps"),
				libraryMenu(),
				navLink("/disks", "Disks"),
			),
			h.Div(h.Class("flex shrink-0 items-center gap-2"),
				g.If(rail.visible(), railNavToggle()),
				maturityControl(mr, csrf),
				themeToggle(theme, csrf),
			),
		),
	)
}

// maturityControlID / maturityControlMaxID are the stable ids the two ends of the
// range control carry. Exported as constants because both the markup and the
// tests that assert the accessible names key off them.
const (
	maturityControlMinID = "cm-maturity-min"
	maturityControlMaxID = "cm-maturity-max"
)

// maturityControl renders the app-wide PG..XXX maturity RANGE as TWO NATIVE
// <select>s inside one <form> — a "from" end and a "to" end. It replaced the old
// 2-state NSFW blur⇄show button outright: one concept, one stored setting.
//
// WHY TWO <select>s AND NOT A SLIDER. HTML has no two-thumb range input, so a
// dual slider means two overlapping <input type=range> plus JavaScript to keep
// them apart — and this app ships htmx and nothing else. Two selects are:
//
//   - keyboard-operable for free (Tab to reach, arrows/Home/End to change,
//     type-ahead to jump) with no key handling of our own;
//   - individually named for assistive tech — each end carries its OWN <label>,
//     so a screen reader announces "Maturity from" / "Maturity to" rather than one
//     ambiguous "slider";
//   - offline and framework-free — native elements, no CDN, no polyfill;
//   - and readable at a glance: the current band is literally spelled out.
//
// 🔴 THE CONTROL CANNOT EMIT AN INVERTED RANGE. Each end only offers the levels
// that keep the band valid — the "from" select stops at the current Max, the "to"
// select starts at the current Min — so every single change from a valid state
// lands on another valid state. (Widening past the other end therefore takes two
// steps, which is the deliberate trade for never being able to submit nonsense.)
// The handler ALSO rejects an inverted submission with 400; the two are belt and
// braces, because the markup constraint only binds a browser.
//
// Changing either end submits the WHOLE form (both values + the CSRF token) to
// POST /settings/maturity, which persists and replies HX-Refresh so the current
// page re-renders under the new band — the same idiom as the theme toggle, so the
// one control works on every page.
func maturityControl(mr maturityRange, csrf string) g.Node {
	if !mr.valid() {
		mr = fullMaturityRange()
	}
	return h.Form(
		h.Class("cm-maturity"),
		hx("post", "/settings/maturity"),
		hx("trigger", "change"),
		hx("swap", "none"),
		// The CSRF token rides in the form, not in hx-vals, so the control is one
		// self-contained POST body — hx-include is not needed and cannot go stale.
		h.Input(h.Type("hidden"), h.Name("csrf_token"), h.Value(csrf)),
		h.Span(h.Class("cm-maturity-legend"), g.Attr("aria-hidden", "true"), g.Text("Maturity")),
		maturityEnd(maturityControlMinID, "min", "Maturity from", mr.Min, maturityPG, mr.Max),
		h.Span(h.Class("cm-maturity-dash"), g.Attr("aria-hidden", "true"), g.Text("–")),
		maturityEnd(maturityControlMaxID, "max", "Maturity to", mr.Max, mr.Min, maturityXXX),
	)
}

// maturityEnd renders one end of the range control: a visually-hidden but REAL
// <label> bound to the <select> (so the end has an accessible name), offering
// only the levels between lo and hi inclusive — which is what makes an inverted
// range unreachable from the UI.
func maturityEnd(id, name, label string, selected, lo, hi maturityLevel) g.Node {
	opts := make([]g.Node, 0, len(maturityScale))
	for _, l := range maturityScale {
		if l < lo || l > hi {
			continue
		}
		attrs := []g.Node{h.Value(l.slug())}
		if l == selected {
			attrs = append(attrs, g.Attr("selected"))
		}
		attrs = append(attrs, g.Text(l.label()))
		opts = append(opts, h.Option(attrs...))
	}
	return g.Group([]g.Node{
		// A real <label for=…>, not an aria-label: it is the element assistive tech
		// prefers, and .cm-sr-only keeps it out of the visual nav strip without
		// removing it from the accessibility tree (display:none would).
		h.Label(h.Class("cm-sr-only"), h.For(id), g.Text(label)),
		h.Select(append([]g.Node{
			h.ID(id), h.Name(name), h.Class("cm-maturity-select"),
		}, opts...)...),
	})
}

// libraryModelFilesHref / libraryWorkflowsHref are the two real Library
// surfaces. /library is ONE page with a tab query param (see handleLibrary,
// which reads ?tab=), so these are the canonical deep links into each half —
// the same hrefs the app's own empty-state CTAs already use.
const (
	libraryModelFilesHref = "/library?tab=files"
	libraryWorkflowsHref  = "/library?tab=workflows"
)

// libraryMenu renders the nav's "Library" dropdown as a native <details>
// disclosure over the two Library tabs.
//
// 🔴 IT IS <details>/<summary>, NOT A JS CONTROLLER, AND THAT IS DELIBERATE.
// The app ships htmx and a couple of tiny page scripts, nothing more, and a
// bespoke dropdown would need a click-outside handler, Escape handling, focus
// management, aria-expanded bookkeeping and a re-open guard after every htmx
// swap.
//
// WHAT <details> ACTUALLY GIVES, AND WHAT IT DOES NOT — the earlier version of
// this comment claimed it provides "all of that natively", which is wrong and
// worth stating precisely because it is the reason someone might reach for JS
// later:
//
//	GIVES     opens on click AND on Enter/Space; exposes its expanded state to
//	          assistive tech without any aria-expanded bookkeeping; fully
//	          operable with JavaScript disabled; closes itself on navigation for
//	          free, since every page render emits it WITHOUT `open`, so
//	          following an item leaves a closed menu with no state to reset.
//	DOES NOT  light-dismiss (a click anywhere else leaves it open) and Escape
//	          (no key handling at all). Nor does it move focus into the panel.
//
// The missing light-dismiss is felt most BELOW 1024px, where the panel is a
// full-width `position: fixed` sheet under the bar: the only way to dismiss it
// without navigating is to find the summary again. Accepted for now — it is one
// extra click on a menu with two items, in a single-user local app.
//
// THE DETERMINISTIC UPGRADE, IF THAT BECOMES ANNOYING, IS `popover`, NOT JS. A
// popover attribute plus popovertarget gives light-dismiss and Escape natively
// and keeps the no-JS property, so it is a strictly better version of the same
// trade. It is not done here because it is a real interaction change to the app's
// primary navigation and would need browser verification at both breakpoints,
// which is out of scope for an audit-fix pass.
//
// 🔴 THE PANEL WOULD BE CLIPPED IF IT WERE A PLAIN ABSOLUTE BOX. Its parent
// .cm-navlinks is `overflow-x: auto`, and per CSS Overflow a non-visible
// overflow-x forces overflow-y to `auto` as well — the same rule that made the
// version tab strip clip its popover (see the .cm-version-tabs note in
// app.css). So a panel hanging below the bar would be cut off at the bar's
// bottom edge at narrow widths. The CSS answers that WITHOUT touching the
// scroll strip: below 1024px the panel is `position: fixed` (fixed boxes are
// laid out against the viewport and are not clipped by an ancestor's overflow),
// rendering as a full-width sheet under the bar; at >=1024px, where five short
// links cannot overflow, .cm-navlinks switches to `overflow: visible` and the
// panel anchors under its summary. Both live in the NAV MENU block of app.css.
//
// STACKING: the panel spends NOTHING from the app's global z-index budget. The
// whole nav is one stacking context at z-30 (.cm-nav is `position: sticky` with
// a z-index), so every descendant paints inside it — above page content and
// below the rail drawer (44/45) and the popover tier (50), which is exactly
// where a nav menu belongs. The panel's own z-index is a local 1, meaningful
// only against its nav siblings. See the STACKING ORDER ledger in app.css.
func libraryMenu() g.Node {
	return h.Details(
		h.Class("cm-navmenu shrink-0"),
		h.Summary(
			h.Class("cm-navmenu-summary"),
			g.Text("Library"),
			// aria-hidden: the caret is pure affordance. <summary> already exposes its
			// open/closed state, so announcing "down triangle" would be noise.
			h.Span(h.Class("cm-navmenu-caret"), g.Attr("aria-hidden", "true"), g.Text("▾")),
		),
		h.Div(
			h.Class("cm-navmenu-panel"),
			h.A(h.Href(libraryModelFilesHref), h.Class("cm-navmenu-item"), g.Text("Model files")),
			h.A(h.Href(libraryWorkflowsHref), h.Class("cm-navmenu-item"), g.Text("Workflows")),
		),
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

// tokenVars builds the inline custom-property override that recolors a civitai
// component to a different semantic intent (the documented per-element hack).
//
// It emits BOTH halves of the contrast split described in the WCAG block of
// assets/app.css: `--civitai-color-primary` drives the component's FILL/tint,
// and `--civitai-color-primary-text` drives its FOREGROUND. Setting only the
// first would leave a recolored `light`/`subtle` button painting the (correct)
// error/success tint under the *primary* text color, and would also drop it back
// to the low-contrast base token — which is precisely what axe flagged on the
// dashboard's Unsubscribe and auto/notify buttons.
//
// tok is a civitai token stem ("error", "success", "warning", "info",
// "text-dimmed"); each has a matching `--civitai-color-<tok>-text`.
func tokenVars(tok string) string {
	return "--civitai-color-primary:var(--civitai-color-" + tok + ");" +
		"--civitai-color-primary-text:var(--civitai-color-" + tok + "-text)"
}

// tokenVarsFilled is tokenVars for a FILLED component, where the token is the
// BACKGROUND sitting under --civitai-color-primary-fg text rather than the
// foreground. White-on-warning is 3.55:1 (dark) / 2.55:1 (light) — both fail AA —
// so the filled case additionally pins the ink to `--civitai-color-on-<tok>`,
// a dark foreground chosen per theme in app.css (4.81:1 dark / 6.70:1 light).
func tokenVarsFilled(tok string) string {
	return tokenVars(tok) + ";--civitai-color-primary-fg:var(--civitai-color-on-" + tok + ")"
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

// emptyState renders the app's ONE empty-state shape: a heading naming what is
// missing, a sentence explaining why the surface is empty and what fills it, and a
// primary action that actually does that.
//
// Several surfaces shipped a bare sentence instead — /trash was literally one <p>
// reading "Trash is empty.", and the outputs gallery and a no-result search were
// the same — which tells a first-time user nothing about how the feature works or
// what to do next. This mirrors the guided empty state the Model-files tab already
// had (scanForModelsCTA in library_pages.go).
//
// It returns the CONTENT, not a card, so a caller that is already inside a card
// (trash, outputs) does not end up with a card inside a card. ctaHref/ctaLabel may
// be empty for a surface with no meaningful next action.
func emptyState(heading, explanation, ctaHref, ctaLabel string) g.Node {
	var cta g.Node
	if ctaHref != "" && ctaLabel != "" {
		cta = h.A(
			h.Href(ctaHref),
			dataAttr("civitai-ui", "button"), dataAttr("variant", "filled"), dataAttr("size", "md"),
			h.Span(h.Class("cm-cta-icon"), g.Attr("aria-hidden", "true"), g.Text("→ ")),
			g.Text(ctaLabel),
		)
	}
	return h.Div(
		h.Class("py-6 text-center"),
		h.H3(h.Class("text-base font-semibold text-slate-200"), g.Text(heading)),
		h.P(h.Class("mx-auto mt-1 mb-3 max-w-md text-sm text-slate-400"), g.Text(explanation)),
		cta,
	)
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

// configDocsURL is this project's configuration reference. A state whose only
// remedy is "edit your config" needs somewhere to send a user who does not know
// where that file is or what the key does — the docs page answers both.
const configDocsURL = "https://github.com/ZacxDev/civitai-manager/blob/main/docs/configuration.md"

// configDocsLink is the small outline link to the configuration docs, used by the
// config-gated empty states.
func configDocsLink(label string) g.Node {
	return h.A(
		h.Href(configDocsURL),
		h.Target("_blank"),
		g.Attr("rel", "noopener noreferrer"),
		dataAttr("civitai-ui", "button"),
		dataAttr("variant", "outline"),
		dataAttr("size", "sm"),
		g.Text(label+" ↗"),
	)
}

// alert renders a @civitai/components alert: role=alert + data-color, with an
// alert-body wrapper and an optional bold title. color is info|success|warning|
// error.
func alert(color, title string, body ...g.Node) g.Node {
	return alertIcon(color, "", title, body...)
}

// alertIcon is alert() with a leading GLYPH in the component's icon slot:
// [data-civitai-ui='alert'] is a flex row (gap 10px, align flex-start) whose first
// child sits beside [data-civitai-ui-alert-body], which is exactly that slot — so
// this needs no new CSS.
//
// It exists so a failure state can be distinguished by SHAPE, not by color alone.
// An alert's only other signal is its tint + border, which a reader with a
// color-vision deficiency (or a monochrome / forced-colors rendering) cannot tell
// apart from the info/warning variants. The glyph is aria-hidden: the alert already
// carries role="alert" plus a text title that names the problem, so announcing
// "warning sign" as well would be redundant noise for a screen-reader user.
//
// glyph "" is the plain alert (no icon slot emitted at all).
func alertIcon(color, glyph, title string, body ...g.Node) g.Node {
	inner := []g.Node{dataFlag("civitai-ui-alert-body")}
	if title != "" {
		inner = append(inner, h.Div(dataFlag("civitai-ui-alert-title"), g.Text(title)))
	}
	inner = append(inner, body...)
	attrs := []g.Node{
		dataAttr("civitai-ui", "alert"),
		dataAttr("color", color),
		g.Attr("role", "alert"),
	}
	if glyph != "" {
		attrs = append(attrs, h.Span(g.Attr("aria-hidden", "true"), g.Text(glyph)))
	}
	attrs = append(attrs, h.Div(inner...))
	return h.Div(attrs...)
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
