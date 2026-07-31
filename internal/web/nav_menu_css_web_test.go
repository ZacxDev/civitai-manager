package web

import (
	"os"
	"strings"
	"testing"
)

// TestNavMenuPanelEscapesTheScrollStrip is the guard on the one non-obvious
// thing about the Library dropdown: WHERE its panel is positioned, at each
// breakpoint, and why.
//
// THE BUG IT GUARDS. .cm-navlinks is `overflow-x: auto`, and per CSS Overflow a
// non-`visible` overflow-x computes overflow-y to `auto` too — so the strip
// clips in BOTH axes and an ordinary absolutely-positioned panel is cut off at
// the bar's bottom edge. The fix has two halves that must BOTH survive:
//
//	< 1024px  the panel is `position: fixed` (a fixed box's containing block is
//	          the viewport, so an ancestor's overflow cannot clip it)
//	>= 1024px .cm-navlinks becomes `overflow: visible` and the panel becomes an
//	          anchored `position: absolute` box
//
// Delete either half and the menu is clipped at exactly one breakpoint —
// invisible to any markup assertion, and invisible to a browser check that only
// ever looks at one viewport width.
//
// 🔴 IT SLICES OUT THE RULE BODIES BEFORE ASSERTING. The first version of this
// coverage lived as a bare `strings.Contains(app, "position: fixed")` in
// class_coverage_web_test.go and was VACUOUS: `.cm-rail` declares
// `position: fixed` ~900 lines earlier, so rewriting the panel to
// `position: absolute` left it green. Mutating the panel's rule is what exposed
// that, and the scoping below is the fix.
func TestNavMenuPanelEscapesTheScrollStrip(t *testing.T) {
	raw, err := os.ReadFile("assets/app.css")
	if err != nil {
		t.Fatalf("read app.css: %v", err)
	}
	css := string(raw)

	// --- the base (mobile / <1024px) rule -----------------------------------
	base := cssRuleIn(t, css, ".cm-navmenu-panel {")
	if !strings.Contains(base, "position: fixed;") {
		t.Errorf("the base .cm-navmenu-panel rule must be `position: fixed` — an absolute box is "+
			"clipped by .cm-navlinks' overflow. Got:\n%s", base)
	}
	// A fixed box needs its offsets, or it lands wherever it was laid out.
	for _, want := range []string{"top: var(--cm-nav-h);", "left: 0;", "right: 0;"} {
		if !strings.Contains(base, want) {
			t.Errorf("the fixed panel is missing %q — it would not sit under the bar. Got:\n%s", want, base)
		}
	}

	// --- the desktop (>=1024px) overrides ------------------------------------
	// Scope to the desktop media block so an `overflow: visible` somewhere else in
	// the file cannot satisfy this. The block is located by its opening line and
	// bounded by the NEXT top-level media query / comment banner.
	desktop := cssMediaBlock(t, css, "@media (min-width: 1024px) {\n  /* Five short links")
	if !strings.Contains(desktop, ".cm-navlinks {") || !strings.Contains(desktop, "overflow: visible;") {
		t.Errorf("at >=1024px .cm-navlinks must stop clipping (`overflow: visible`), or the "+
			"anchored panel below it is cut off. Got:\n%s", desktop)
	}
	if !strings.Contains(desktop, "position: absolute;") {
		t.Errorf("at >=1024px the panel must anchor under its summary (`position: absolute`), "+
			"not stay a full-width fixed sheet. Got:\n%s", desktop)
	}

	// --- the stacking claim --------------------------------------------------
	// The panel's z-index is LOCAL to the nav's stacking context and must stay
	// small. A value that looked global (30/45/50) would misrepresent the budget
	// documented in the STACKING ORDER ledger and buy nothing — a descendant
	// cannot escape its ancestor's stacking context.
	if !strings.Contains(base, "z-index: 1;") {
		t.Errorf("the panel's z-index must be the local 1 (see the STACKING ORDER ledger). Got:\n%s", base)
	}
	for _, global := range []string{"z-index: 25", "z-index: 30", "z-index: 44", "z-index: 45", "z-index: 50"} {
		if strings.Contains(base, global) {
			t.Errorf("the panel must not spend a value from the global stacking budget (%s). Got:\n%s", global, base)
		}
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
