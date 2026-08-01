package web

import (
	"strconv"

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
// app.css for the cascade rationale).
//
// 🔴 <html data-theme> IS PINNED TO shellTheme — see that constant for why the
// light path was retired from the UI, and for what re-enabling it would take.
//
// `rail` is the app shell's global "Recent outputs" sidebar state (see
// outputs_rail.go). Its zero value renders no rail — the shell is then
// byte-identical to the pre-rail one, which is what page builders called without
// shell state (unit tests) produce.
func page(title, csrf string, mr maturityRange, rail railData, body ...g.Node) g.Node {
	// One predicate decides the rail markup, the shell's reserved width and the
	// nav's drawer button, so they can never disagree.
	bodyClass := "min-h-screen bg-slate-950 text-slate-100 antialiased"
	if c := railShellClass(rail); c != "" {
		bodyClass += " " + c
	}
	return g.El("html",
		h.Lang("en"),
		dataAttr("theme", shellTheme),
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
			navbar(csrf, mr, rail),
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

// shellTheme is the ONE value <html data-theme> ever carries. Every --civitai-*
// token resolves from that attribute, so this constant alone decides the whole
// app's palette.
//
// 🔴 THE LIGHT PATH IS RETIRED FROM THE UI, NOT DELETED FROM THE CSS. There is no
// theme toggle, no `theme` setting read, and no POST /settings/theme route any
// more — the app is dark, always, for everyone. What was deliberately KEPT:
//
//   - Every `[data-theme='light']` block in the vendored civitai-theme.css and in
//     app.css stays exactly as shipped. Nothing was stripped.
//   - contrast_web_test.go still parses that REAL CSS and still resolves BOTH
//     themes, including its 25 accepted light-theme debt entries. It is unchanged
//     and stays the gate: a light pair that silently changes ratio still fails the
//     build even though no user can currently see it. That is the point — the
//     dormant path cannot rot unnoticed.
//
// RE-ENABLING is therefore a UI change, not a CSS one. It needs, in order: a
// persisted setting + reader (the old `theme` settings key and currentTheme), a
// CSRF-protected POST route that writes it and replies HX-Refresh, a control in
// navbar, and threading the chosen value back down to this attribute. The stored
// `theme` row from before the retirement is left UNTOUCHED in the settings table
// — no migration deletes it — so an existing user's old preference is still there
// to be read if that day comes.
const shellTheme = "dark"

// navbar renders the sticky top bar. Sticky positioning + stacking order live in
// .cm-nav (app.css) alongside --cm-nav-h, which the rail's top offset and the
// anchor scroll-margin both derive from — keep those in sync.
//
// MOBILE: the destinations stay in ONE horizontally scrollable strip
// (.cm-navlinks) rather than collapsing into a hamburger drawer. At 390px that
// keeps every destination reachable in one gesture with no JS, no focus trap and
// no second overlay competing with the rail drawer for the same corner of the
// screen; only the brand shortens. The controls (rail/maturity) never scroll.
// (The theme toggle used to sit beside them — see shellTheme for why it is gone.)
//
// THE STRIP HOLDS FIVE ENTRIES, NOT SEVEN, and the difference is the point:
//
//   - The BRAND is the home link, and "/" is the SEARCH experience (handleHome).
//     "Dashboard" was a second control going to the same place as the wordmark
//     beside it, so it is gone and the wordmark grew a mark (brandLink). The page
//     it used to open now lives at /subscriptions, in the Library menu.
//   - "Models"/"Workflows" read as "my models"/"my workflows" next to a Library
//     entry that is also about models and workflows. They are RENAMED
//     "Find models"/"Find workflows" — the routes (/search,
//     /workflows/discover) are unchanged.
//   - "Library" became a DROPDOWN (libraryMenu), first over /library's two real
//     surfaces — because /library has always been a two-tab page and the tab was
//     invisible from the nav — and now over three, since /subscriptions moved in
//     when "/" stopped being it. It shipped as a <details> disclosure and is now
//     a `popover` — see libraryMenu for why (light-dismiss and Escape, which
//     <details> has neither of).
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
func navbar(csrf string, mr maturityRange, rail railData) g.Node {
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
			),
		),
	)
}

// maturityControlMinID / maturityControlMaxID are the stable ids the two ends of
// the range control carry — now the two TRACK groups rather than two <select>s.
// They are constants because both the markup and the tests that assert the
// accessible names key off them.
//
// maturityMenuPanelID is the popover's id. As with libraryMenuPanelID, the
// trigger's popovertarget and the panel's id are the ENTIRE wiring of this
// control, so they come from ONE constant — a typo in either renders a button
// that does nothing at all, silently, with no console error.
const (
	maturityControlMinID = "cm-maturity-min"
	maturityControlMaxID = "cm-maturity-max"
	maturityMenuPanelID  = "cm-maturity-menu"
)

// maturityControl renders the app-wide PG..XXX maturity RANGE as an ICON BUTTON
// opening a POPOVER that holds a two-sided slider. It replaced the old 2-state
// NSFW blur⇄show button outright: one concept, one stored setting. It previously
// shipped as two bare <select>s sitting permanently in the nav bar.
//
// 🔴 WHAT THE SLIDER IS, AND WHY IT IS NOT TWO <input type="range">. HTML has no
// two-thumb range input. The obvious dual-slider — two overlapping range inputs —
// was considered and REJECTED for two concrete reasons, not on taste:
//
//   - To keep the MARKUP incapable of inverting (see below), each input's own
//     min/max must be clamped to the other end's current value. Two range inputs
//     with DIFFERENT min/max spans have different value-space-to-pixel scales, so
//     their tracks do not line up; making them line up means sizing each by a
//     percentage of the span and fighting the UA's thumb inset at both ends.
//   - Dropping that clamp so the two tracks DO align means JavaScript becomes the
//     only thing keeping the thumbs apart — i.e. the safety property would depend
//     on script, which is exactly what must not happen here.
//
// So each end is a 5-STOP SEGMENTED TRACK built from native radio inputs, and the
// two tracks share ONE 5-column grid — they align BY CONSTRUCTION rather than by
// arithmetic. A radio track is a slider in every way that matters here: Tab
// reaches the group, the arrow keys move between stops and commit as they go
// (native radiogroup behaviour, no key handling of our own), Home/End jump to the
// ends, and each stop carries a real accessible name. It also submits the level
// SLUG directly, so the wire format is UNCHANGED and handleSetMaturity did not
// have to be touched at all.
//
// 🔴 THE CONTROL STILL CANNOT EMIT AN INVERTED RANGE — the same rule as the
// selects it replaces, expressed differently: a stop that would invert the band
// is rendered `disabled`. A disabled radio is not submittable, not selectable and
// is skipped by arrow-key navigation, so every reachable change from a valid
// state lands on another valid state. (Widening past the other end therefore
// takes two steps — the same deliberate trade as before.) It is RENDERED rather
// than omitted so the unreachable region is visible, which is a strict
// improvement on the selects: they silently offered a shorter list, giving the
// user no idea the other end was in the way.
//
// The handler ALSO rejects an inverted submission with 400, and that is the REAL
// guard — the markup constraint only binds a browser. Both halves are tested
// independently; never collapse them into one test.
//
// Changing any stop submits the WHOLE form (both ends + the CSRF token) to
// POST /settings/maturity, which persists and replies HX-Refresh so the current
// page re-renders under the new band — so the one control works on every page.
//
// STACKING: the panel is a `popover`, so it renders in the TOP LAYER and declares
// NO z-index — see libraryMenu and the STACKING ORDER ledger in app.css. It
// spends nothing from the budget.
func maturityControl(mr maturityRange, csrf string) g.Node {
	if !mr.valid() {
		mr = fullMaturityRange()
	}
	return h.Div(
		h.Class("cm-maturity"),
		h.Button(
			// A real <button type=button>: focusable, activated by click AND by
			// Enter/Space with no key handling of our own. type=button is load-bearing
			// — the default is `submit`, and this button sits in a nav that carries
			// forms.
			h.Type("button"),
			h.Class("cm-maturity-trigger"),
			g.Attr("popovertarget", maturityMenuPanelID),
			// The accessible name CARRIES THE CURRENT STATE. The old control spelled
			// the band out in two visible selects; an icon button announcing only
			// "Maturity" would lose that, so the band goes into the name.
			g.Attr("aria-label", "Maturity: "+mr.label()),
			maturityGlyph(),
			// The compact band, shown beside the icon at wider viewports and hidden on
			// narrow ones (CSS). aria-hidden because the button's aria-label already
			// says it — announcing it twice would be noise.
			h.Span(h.Class("cm-maturity-band"), g.Attr("aria-hidden", "true"),
				g.Text(mr.Min.label()+"–"+mr.Max.label())),
		),
		h.Div(
			h.ID(maturityMenuPanelID),
			// VALUELESS = `auto` = light-dismiss + Escape. Do NOT give it a value:
			// popover="manual" has neither, and would silently return this control to a
			// panel that can only be closed by finding the trigger again.
			g.Attr("popover"),
			h.Class("cm-maturity-panel"),
			h.Form(
				hx("post", "/settings/maturity"),
				hx("trigger", "change"),
				hx("swap", "none"),
				// The CSRF token rides in the form, not in hx-vals, so the control is one
				// self-contained POST body — hx-include is not needed and cannot go stale.
				h.Input(h.Type("hidden"), h.Name("csrf_token"), h.Value(csrf)),
				h.Div(h.Class("cm-maturity-head"),
					h.Span(h.Class("cm-maturity-title"), g.Text("Maturity")),
					h.Span(h.Class("cm-maturity-current"), g.Text(mr.label())),
				),
				maturityTrack(maturityControlMinID, "min", "Maturity from", mr.Min, maturityPG, mr.Max),
				maturityTrack(maturityControlMaxID, "max", "Maturity to", mr.Max, mr.Min, maturityXXX),
				h.Div(h.Class("mt-3 pt-3 border-t border-slate-800"),
					civButton("outline", "sm", []g.Node{
						h.Type("button"),
						h.ID("cm-maturity-safe"),
						g.Attr("onclick", `javascript:void(function(){
							var min=document.getElementById('cm-maturity-min-pg');
							var max=document.getElementById('cm-maturity-max-pg13');
							if(min)min.click();
							if(max)max.click();
						})()`),
					}, g.Text("Safe mode")),
				),
			),
		),
	)
}

// maturityGlyph is the trigger's icon: a shield outline stroked in currentColor,
// so it re-themes with the surrounding text for free and adds NO coloured pair to
// contrast_web_test.go's table. aria-hidden + focusable=false keeps it out of the
// accessibility tree — the button's aria-label is the name (the same contract the
// brand mark uses; see brand.go).
func maturityGlyph() g.Node {
	return g.El("svg",
		g.Attr("viewBox", "0 0 16 16"),
		g.Attr("width", "14"), g.Attr("height", "14"),
		g.Attr("fill", "none"),
		g.Attr("stroke", "currentColor"),
		g.Attr("stroke-width", "1.5"),
		g.Attr("stroke-linejoin", "round"),
		g.Attr("aria-hidden", "true"),
		g.Attr("focusable", "false"),
		g.El("path", g.Attr("d", "M8 1.75 2.75 3.5v4c0 3 2.1 5.6 5.25 6.75 3.15-1.15 5.25-3.75 5.25-6.75v-4L8 1.75Z")),
	)
}

// maturityTrack renders ONE end of the range as a 5-stop segmented slider: a
// <fieldset> (so the group has a real, announced name from its <legend>) holding
// one radio per level on a shared 5-column grid.
//
// 🔴 lo/hi are the INCLUSIVE bounds that keep the band valid. A level outside them
// renders as an INERT stop — same grid cell, same dot and tick, so the geometry
// stays uniform and the user can see where the track ends — but it emits **NO
// <input> at all**. It is therefore not a member of the radio group: unsubmittable,
// unselectable, and *unreachable by the keyboard*.
//
// 🔴 IT USED TO BE A `disabled` RADIO, AND THAT SHIPPED A CONTENT-GATING CONTROL
// THAT FAILED OPEN. A disabled radio is skipped by arrow-key navigation — but a
// native radio group also WRAPS AROUND, so "skipped" at a boundary means the focus
// lands on the far end of the scale. Proved live in Brave: with the band at "X
// only", one **ArrowLeft** on the max track — the *reducing* direction — moved to
// `xxx`, committed, and persisted **"X to XXX"**. One keypress intended to lower
// the ceiling silently admitted XXX content, with the page reloading immediately
// and focus dumped to <body>, so nothing announced what had happened.
// The two <select>s this control replaced could not do that: they OMITTED
// out-of-range options, and a <select> does not wrap at its first option. Emitting
// no input restores exactly that property.
//
// Note the inverted-range guard alone could never catch this — `x:xxx` IS a valid
// band. It asks "does every reachable stop yield a valid range?", never "can a
// keypress reach a stop the user did not intend?". That is why the guard below
// asserts the ABSENCE of an input, not the presence of `disabled`.
//
// The selected level is ALWAYS inside [lo,hi] for any valid range, so the checked
// stop can never be an inert one. If it ever were, that end would submit NOTHING
// and the handler would 400 on an empty slug — a real failure mode, which is why
// maturityControl normalizes an invalid range before calling this.
func maturityTrack(id, name, label string, selected, lo, hi maturityLevel) g.Node {
	stops := make([]g.Node, 0, len(maturityScale))
	for i, l := range maturityScale {
		kids := []g.Node{
			// The dot is the thumb; the tick is the level's name. For a real stop the
			// tick is the label's TEXT, so it is the radio's accessible name — no
			// aria-label needed and none should be added.
			h.Span(h.Class("cm-mat-dot"), g.Attr("aria-hidden", "true")),
			h.Span(h.Class("cm-mat-tick"), g.Text(l.label())),
		}
		if l < lo || l > hi {
			// Out of band: no input, so the keyboard cannot reach it and a forged
			// submit cannot name it. A <span>, not a <label> — a label with no control
			// is meaningless to AT.
			stops = append(stops, h.Span(
				h.Class("cm-mat-stop cm-mat-stop-out"),
				dataAttr("step", strconv.Itoa(i)),
				g.Group(kids),
			))
			continue
		}
		attrs := []g.Node{
			h.Type("radio"),
			h.Name(name),
			h.Value(l.slug()),
			h.Class("cm-mat-radio"),
			h.ID(id + "-" + l.slug()),
		}
		if l == selected {
			attrs = append(attrs, g.Attr("checked"))
		}
		stops = append(stops, h.Label(
			h.Class("cm-mat-stop"),
			dataAttr("step", strconv.Itoa(i)),
			g.Group(append([]g.Node{h.Input(attrs...)}, kids...)),
		))
	}
	return h.FieldSet(
		h.Class("cm-mat-track"),
		h.ID(id),
		// A real <legend>, not an aria-label: it is what assistive tech prefers, and
		// it names WHICH end the user is on ("Maturity from" vs "Maturity to").
		// Ambiguity there was the original reason each end got its own label.
		h.Legend(h.Class("cm-mat-legend"), g.Text(label)),
		h.Div(h.Class("cm-mat-stops"), g.Group(stops)),
	)
}

// libraryModelFilesHref / libraryWorkflowsHref are the two real Library
// surfaces. /library is ONE page with a tab query param (see handleLibrary,
// which reads ?tab=), so these are the canonical deep links into each half —
// the same hrefs the app's own empty-state CTAs already use.
//
// librarySubscriptionsHref is the THIRD entry, and it is the page that used to
// be "/". When "/" became the search experience (handleHome) this page lost its
// only entry point — the brand wordmark — and would have been reachable by URL
// alone. It belongs in THIS menu rather than the top-level strip because the
// strip's entries are all "find something on civitai" and this one is "what I
// have asked this app to watch, and what it is doing about it", which is the
// same possessive sense as the two Library tabs beside it.
const (
	libraryModelFilesHref    = "/library?tab=files"
	libraryWorkflowsHref     = "/library?tab=workflows"
	librarySubscriptionsHref = "/subscriptions"
)

// libraryMenuPanelID is the popover's id. The trigger's popovertarget and the
// panel's id are the ENTIRE wiring of this control, so they come from one
// constant — a typo in either would render a button that does nothing at all,
// silently, with no console error.
const libraryMenuPanelID = "cm-library-menu"

// libraryMenu renders the nav's "Library" dropdown as a `popover`, still with
// NO JAVASCRIPT.
//
// 🔴 IT USED TO BE <details>/<summary>, AND THE SWAP BOUGHT TWO THINGS <details>
// CANNOT DO AT ALL:
//
//	LIGHT-DISMISS  a click anywhere outside closes it. <details> has no such
//	               behaviour, so below 1024px — where the panel is a full-width
//	               sheet under the bar — the only way to dismiss it without
//	               navigating was to find the summary again.
//	ESCAPE         <details> does no key handling whatsoever.
//
// Both come from the `popover` attribute in its default `auto` state, which is
// why the state matters: 🔴 `popover="manual"` HAS NEITHER, and swapping the
// value would silently return the control to exactly the behaviour this change
// removed. Everything <details> did give — activation on click and on
// Enter/Space, an expanded state exposed to assistive tech (the invoker gets
// implicit aria-expanded/aria-details from popovertarget), a closed initial
// state on every render, and no JS — the popover gives too.
//
// 🔴 A POPOVER RENDERS IN THE TOP LAYER, WHICH ANCESTOR `overflow` CANNOT CLIP.
// That is a direct fix for the trap the old CSS worked around rather than a
// bonus: .cm-navlinks is `overflow-x: auto`, and per CSS Overflow a non-visible
// overflow-x forces overflow-y to `auto` too, so a plain absolute panel hanging
// below the bar was cut off at the bar's bottom edge. The old fix needed BOTH a
// `position: fixed` sheet below 1024px AND an `overflow: visible` override on
// .cm-navlinks at >=1024px. The top layer removes the need for the override
// entirely — see the NAV MENU block of app.css, where it has been deleted.
//
// ⚠ THE SAME PROMOTION BREAKS ANCHORING, WHICH IS THE NON-OBVIOUS COST. A
// top-layer element's containing block is the VIEWPORT, not its nearest
// positioned ancestor — so the old desktop rule (`position: absolute; top:
// calc(100% + 0.625rem)`) resolves 100% against the viewport height and drops
// the panel far below the fold. Measured live, not deduced: see the CSS. The
// replacement is CSS anchor positioning behind an @supports guard; browsers
// without it keep the full-width sheet, which is what every narrow viewport
// already gets.
//
// STACKING: the panel now spends NOTHING from the app's z-index budget and
// carries no z-index at all — the top layer paints above every stacking context
// in the document, including the sticky nav's 30. Its old local `z-index: 1`
// would be inert; keeping it would misdescribe the budget. See the STACKING
// ORDER ledger in app.css.
func libraryMenu() g.Node {
	return h.Div(
		h.Class("cm-navmenu shrink-0"),
		h.Button(
			// A real <button type=button>: focusable, and activated by click AND by
			// Enter/Space with no key handling of our own. type=button is load-bearing
			// — the default is `submit`, and this control does sit inside a nav that
			// carries forms.
			h.Type("button"),
			h.Class("cm-navmenu-summary"),
			g.Attr("popovertarget", libraryMenuPanelID),
			g.Text("Library"),
			// aria-hidden: the caret is pure affordance. The invoker already exposes
			// its expanded state, so announcing "down triangle" would be noise.
			h.Span(h.Class("cm-navmenu-caret"), g.Attr("aria-hidden", "true"), g.Text("▾")),
		),
		h.Div(
			h.ID(libraryMenuPanelID),
			// VALUELESS = `auto` = light-dismiss + Escape. Do not give it a value.
			g.Attr("popover"),
			h.Class("cm-navmenu-panel"),
			h.A(h.Href(libraryModelFilesHref), h.Class("cm-navmenu-item"), g.Text("Model files")),
			h.A(h.Href(libraryWorkflowsHref), h.Class("cm-navmenu-item"), g.Text("Workflows")),
			// The label comes from the PAGE's own title constant, so the menu entry and
			// the <h1> it opens can never say different things.
			h.A(h.Href(librarySubscriptionsHref), h.Class("cm-navmenu-item"), g.Text(subscriptionsPageTitle)),
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

// (The light/dark themeToggle that used to live here is GONE — see shellTheme.)

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
