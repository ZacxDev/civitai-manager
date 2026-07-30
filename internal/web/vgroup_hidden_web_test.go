package web

import (
	"regexp"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// The `hidden` base-model panel gate.
// ---------------------------------------------------------------------------
// modelVersionTabs marks every non-active .cm-vgroup panel with the HTML
// `hidden` attribute and relies on it to actually hide the panel. It did not:
// `.cm-version-tabs { display: flex }` and the UA sheet's `[hidden] { display:
// none }` have the SAME specificity (0-1-0), and an AUTHOR declaration beats a
// UA one at equal specificity — so every "hidden" panel rendered.
//
// Symptoms, both live-caught in a real browser on /models/4384:
//   - the base-model pills filtered nothing (all groups' tabs rendered at once);
//   - an un-hidden panel is TRANSPARENT, so it painted nothing yet still won
//     hit-testing over an open version-date popover that overlapped it, and the
//     popover closed under the user's cursor. That reads as a z-index bug and is
//     not one.
//
// HONEST LIMIT: no browser runs in CI. This asserts the shipped RULE and the
// markup that depends on it — not rendered pixels or hit-test order. The
// paint/hit-test claims above were verified interactively.

// cssRuleBodies returns the declaration bodies of every block in the SHIPPED
// app.css whose selector list contains one of the given EXACT selectors.
func cssRuleBodies(t *testing.T, wantSelector string) []string {
	t.Helper()
	css := cssCommentRE.ReplaceAllString(readAppCSS(t), "")
	var out []string
	for _, m := range cssRuleRE.FindAllStringSubmatch(css, -1) {
		sel := strings.TrimSpace(m[1])
		if sel == "" || strings.HasPrefix(sel, "@") {
			continue
		}
		for _, part := range selectorList(sel) {
			if part == wantSelector {
				out = append(out, m[2])
			}
		}
	}
	return out
}

// TestHiddenVersionGroupPanelIsDisplayNone pins the rule that restores `hidden`.
func TestHiddenVersionGroupPanelIsDisplayNone(t *testing.T) {
	bodies := cssRuleBodies(t, ".cm-version-tabs[hidden]")
	if len(bodies) == 0 {
		t.Fatal(".cm-version-tabs[hidden] rule is GONE from app.css — without it the " +
			"`hidden` attribute on a .cm-vgroup panel does nothing (`.cm-version-tabs " +
			"{ display: flex }` outranks the UA `[hidden] { display: none }`), the " +
			"base-model pills stop filtering, and the still-rendered transparent panel " +
			"steals hit-testing from the version-date popover")
	}
	displayRE := regexp.MustCompile(`display\s*:\s*none`)
	found := false
	for _, b := range bodies {
		if displayRE.MatchString(b) {
			found = true
		}
	}
	if !found {
		t.Errorf(".cm-version-tabs[hidden] must set `display: none`; got bodies %q", bodies)
	}
}

// TestHiddenPanelRuleOutranksTheFlexRule guards the SPECIFICITY relationship the
// fix depends on: the base rule must not itself gain an attribute/id or an
// !important that would put it back on top.
func TestHiddenPanelRuleOutranksTheFlexRule(t *testing.T) {
	for _, b := range cssRuleBodies(t, ".cm-version-tabs") {
		if strings.Contains(b, "!important") {
			t.Errorf(".cm-version-tabs must not use !important — it would defeat "+
				".cm-version-tabs[hidden] and un-hide every base-model group again: %q", b)
		}
	}
	// And the fix must not have been "solved" with !important either: that would
	// hide a panel a future rule legitimately wants to show.
	for _, b := range cssRuleBodies(t, ".cm-version-tabs[hidden]") {
		if strings.Contains(b, "!important") {
			t.Errorf(".cm-version-tabs[hidden] needs no !important (0-2-0 already wins); "+
				"adding one hides the panel unconditionally: %q", b)
		}
	}
}

// TestVersionGroupPanelsStillCarryHidden pins the MARKUP half: the CSS rule is
// only useful while the non-active panels actually carry the attribute.
func TestVersionGroupPanelsStillCarryHidden(t *testing.T) {
	vers := manyMultiBaseVersions()
	out := renderString(t, modelVersionTabs(groupedTabsView(1, vers, nil)))
	if !strings.Contains(out, "hidden") {
		t.Fatalf("non-active .cm-vgroup panels must carry the hidden attribute:\n%s", out)
	}
	// Exactly one panel is shown: 3 distinct base models -> 2 hidden panels.
	if got := strings.Count(out, `hidden=""`); got != 2 {
		t.Errorf("expected 2 hidden panels for 3 base-model groups, got %d:\n%s", got, out)
	}
}
