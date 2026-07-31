package web

import (
	"os"
	"strings"
	"testing"
)

// TestNavMenuPanelEscapesTheScrollStrip is the guard on the one non-obvious
// thing about the Library menu: WHERE its panel is positioned, at each
// breakpoint, and why.
//
// THE BUG IT GUARDS. .cm-navlinks is `overflow-x: auto`, and per CSS Overflow a
// non-`visible` overflow-x computes overflow-y to `auto` too — so the strip
// clips in BOTH axes and an ordinary absolutely-positioned panel is cut off at
// the bar's bottom edge.
//
// 🔴 THE PANEL IS NOW A `popover`, SO THE TOP LAYER SOLVES THE CLIP OUTRIGHT,
// AND THIS TEST MOVED WITH IT. The old shape needed TWO halves — a fixed sheet
// below 1024px and an `overflow: visible` override on .cm-navlinks at >=1024px.
// A top-layer box cannot be clipped by any ancestor's overflow at any width, so
// the override was DELETED and its absence is asserted below: re-adding it would
// be cargo-cult CSS restored against a trap that no longer exists.
//
// ⚠ THE PROMOTION BROKE ANCHORING, WHICH IS THE HALF WORTH GUARDING NOW. A
// top-layer element's containing block is the VIEWPORT, so the old
// `position: absolute; top: calc(100% + 0.625rem)` resolves against the viewport
// height. MEASURED in Brave at 1198x921 by injecting exactly that rule: the
// panel landed at top 931.6px — a full screen below the fold — instead of at
// 50px under its trigger. Hence CSS anchor positioning, behind @supports.
//
// 🔴 IT SLICES OUT THE RULE BODIES BEFORE ASSERTING. The first version of this
// coverage lived as a bare `strings.Contains(app, "position: fixed")` in
// class_coverage_web_test.go and was VACUOUS: `.cm-rail` declares
// `position: fixed` ~900 lines earlier, so rewriting the panel to
// `position: absolute` left it green. Mutating the panel's rule is what exposed
// that, and the scoping below is the fix. Keep it — a flat Contains over this
// sheet proves nothing.
func TestNavMenuPanelEscapesTheScrollStrip(t *testing.T) {
	raw, err := os.ReadFile("assets/app.css")
	if err != nil {
		t.Fatalf("read app.css: %v", err)
	}
	css := string(raw)

	// --- the base (sheet) rule ------------------------------------------------
	base := cssRuleIn(t, css, ".cm-navmenu-panel {")
	if !strings.Contains(base, "position: fixed;") {
		t.Errorf("the base .cm-navmenu-panel rule must be `position: fixed` — the UA [popover] sheet "+
			"would otherwise centre it in the viewport. Got:\n%s", base)
	}
	for _, want := range []string{"top: var(--cm-nav-h);", "left: 0;", "right: 0;"} {
		if !strings.Contains(base, want) {
			t.Errorf("the sheet panel is missing %q — it would not sit under the bar. Got:\n%s", want, base)
		}
	}
	// The [popover] UA sheet sets inset:0 + margin:auto + width/height:fit-content
	// + border:solid. Without these resets the panel is a small bordered box
	// floating in the middle of the screen.
	for _, want := range []string{"margin: 0;", "width: auto;", "border: none;"} {
		if !strings.Contains(base, want) {
			t.Errorf("the base rule must undo the [popover] UA sheet with %q. Got:\n%s", want, base)
		}
	}

	// 🔴 THE UA-DISPLAY TRAP. A popover is CLOSED by the UA rule
	// `[popover]:not(:popover-open) { display: none }`, and an AUTHOR `display`
	// beats a UA one at any specificity — so a `display` in the base rule pins the
	// menu permanently open on every page. Same class as the `hidden` .cm-vgroup
	// that was still `display: flex`. The layout must live on :popover-open only.
	if strings.Contains(base, "display:") {
		t.Errorf("the base .cm-navmenu-panel rule must declare NO `display` — an author display "+
			"beats the UA `[popover]:not(:popover-open){display:none}` rule and the menu would "+
			"render permanently open. Put it on :popover-open. Got:\n%s", base)
	}
	open := cssRuleIn(t, css, ".cm-navmenu-panel:popover-open {")
	if !strings.Contains(open, "display: flex;") {
		t.Errorf("the :popover-open rule must supply the flex layout the base rule may not. Got:\n%s", open)
	}

	// --- the desktop (>=1024px) anchor ---------------------------------------
	desktop := cssMediaBlock(t, css, "@media (min-width: 1024px) {\n  /*\n   * The desktop panel anchors")
	for _, want := range []string{
		"@supports (anchor-name: --cm-navmenu-anchor)", // the guard itself
		"anchor-name: --cm-navmenu-anchor;",            // on the trigger
		"position-anchor: --cm-navmenu-anchor;",        // on the panel
		"top: calc(anchor(bottom) + 0.625rem);",
		"left: anchor(left);",
	} {
		if !strings.Contains(desktop, want) {
			t.Errorf("the desktop anchor is missing %q. Manual CSS cannot anchor a TOP-LAYER box "+
				"under an arbitrary trigger — its containing block is the viewport, measured at "+
				"top 931.6px in a 921px viewport. Got:\n%s", want, desktop)
		}
	}
	// The @supports guard is not decoration: without it, `anchor()` drops whole
	// declarations in Firefox/Safari and the panel half-applies the desktop rules.
	if !strings.Contains(desktop, "@supports") {
		t.Errorf("the anchor rules must sit behind @supports — anchor positioning is Chromium-only "+
			"and an unguarded `anchor()` silently drops in other engines. Got:\n%s", desktop)
	}
	// 🔴 The old workaround must NOT come back.
	if strings.Contains(desktop, "position: absolute;") {
		t.Errorf("the desktop panel must not use `position: absolute` — a top-layer box resolves it "+
			"against the VIEWPORT, which measured a full screen below the fold. Got:\n%s", desktop)
	}
	if strings.Contains(desktop, ".cm-navlinks {") {
		t.Errorf("the `overflow: visible` override on .cm-navlinks was DELETED because the top layer "+
			"cannot be clipped by ancestor overflow (verified live: the open sheet extends 111px "+
			"below the strip and still hit-tests as itself at all four corners). Re-adding it "+
			"restores a workaround for a trap that no longer exists. Got:\n%s", desktop)
	}

	// --- the stacking claim --------------------------------------------------
	// A top-layer box paints above EVERY stacking context, so any z-index here is
	// inert and would misdescribe the STACKING ORDER ledger's budget.
	if strings.Contains(base, "z-index") {
		t.Errorf("the panel must declare NO z-index — the top layer already paints above every "+
			"stacking context, so a value is inert and misrepresents the STACKING ORDER "+
			"ledger. Got:\n%s", base)
	}
}

// TestLightThemeInvisibleSurfacesStayFixed guards the TWO fixes that were found
// by looking at the app in a real browser, and that nothing else in the suite
// can see.
//
// 🔴 THE SHARED ROOT CAUSE, MEASURED LIVE IN BRAVE: on the LIGHT theme
// --civitai-color-surface-2, --civitai-color-surface and --civitai-color-body
// are ALL #FEFEFE. (Dark: #25262B / #1A1B1E / #1A1B1E — three distinct values.)
// So the obvious `background: var(--civitai-color-surface-2)` is correct in dark
// and COMPLETELY INVISIBLE in light, on every surface that reaches for it:
//
//	.cm-navmenu-item:hover  a dropdown item that does not react to the pointer
//	.cm-meter               a capacity bar with no track — the fill floats with
//	                        no scale behind it, so the reader loses "of what"
//
// Both are pure-appearance defects on ONE theme. No markup assertion can see
// them (the class is present either way), contrast_web_test.go cannot (it pins
// the ratio of a token pair, not which token a rule uses), and the dark theme
// hides them from a casual look. Mutating either back to surface-2 left the
// entire suite green, which is why this guard exists.
//
// It asserts the CHOSEN token, not merely "not surface-2": a third value would
// be someone re-litigating a decision that was made against measured numbers.
func TestLightThemeInvisibleSurfacesStayFixed(t *testing.T) {
	raw, err := os.ReadFile("assets/app.css")
	if err != nil {
		t.Fatalf("read app.css: %v", err)
	}
	css := string(raw)

	for _, c := range []struct{ name, selector, want, why string }{
		{
			"nav menu item hover",
			".cm-navmenu-item:hover,",
			"background: color-mix(in srgb, var(--civitai-color-primary) 12%, transparent);",
			"a brand tint is the only wash that is visible on BOTH themes; surface-2 " +
				"equals the surface it sits on in light, so the hover does nothing at all. " +
				"12% is @civitai/components' own light-button tint",
		},
		{
			"capacity meter track",
			".cm-meter {",
			"background: var(--civitai-color-border);",
			"`border` is the one neutral token that differs from the card surface in " +
				"BOTH themes (#CED4DA light / #373A40 dark); a surface-2 track is invisible " +
				"in light and the meter degrades to a floating stub with no scale",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			// cssRuleIn fails the test when the selector is absent, so a renamed rule
			// reports itself rather than passing vacuously.
			rule := cssRuleIn(t, css, c.selector)
			if !strings.Contains(rule, c.want) {
				t.Errorf("%s must declare `%s` — %s. Got:\n%s", c.selector, c.want, c.why, rule)
			}
			// The specific regression: surface-2 is the token both of these were
			// written with, and it is invisible on light.
			if strings.Contains(rule, "--civitai-color-surface-2") {
				t.Errorf("%s reaches for --civitai-color-surface-2, which on the LIGHT theme is "+
					"the same #FEFEFE as the surface it sits on — this is the exact defect the "+
					"rule was changed to fix. Got:\n%s", c.selector, rule)
			}
		})
	}
}

// cssRuleIn returns the text of the rule opened by selector WITHIN the css slice
// it is handed, from the selector through its closing brace. It fails the test when
// the selector is absent — an empty body would make every "must contain" check
// below it fail confusingly and every "must not contain" check pass for free.
//
// Distinct from cssRuleBody (library_status_card_web_test.go), which reads the whole
// shipped sheet itself and matches an EXACT selector list. This one takes the css as
// a parameter precisely so it can be pointed at a slice — e.g. the @media block
// returned by cssMediaBlock — which is the scoping that stopped the panel assertion
// below from matching .cm-rail's `position: fixed` ~900 lines away. Do not merge the
// two: the parameter is the whole point.
func cssRuleIn(t *testing.T, css, selector string) string {
	t.Helper()
	i := strings.Index(css, selector)
	if i < 0 {
		t.Fatalf("app.css has no rule %q", selector)
	}
	rest := css[i:]
	j := strings.Index(rest, "}")
	if j < 0 {
		t.Fatalf("rule %q is not closed", selector)
	}
	return rest[:j+1]
}

// cssMediaBlock returns a whole @media block located by its opening line,
// balancing braces so nested rules are included.
func cssMediaBlock(t *testing.T, css, open string) string {
	t.Helper()
	i := strings.Index(css, open)
	if i < 0 {
		t.Fatalf("app.css has no media block opening with %q", open)
	}
	depth := 0
	for j := i; j < len(css); j++ {
		switch css[j] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return css[i : j+1]
			}
		}
	}
	t.Fatalf("media block opening with %q is not closed", open)
	return ""
}
