package web

import (
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/store"
)

// ---------------------------------------------------------------------------
// The .cm-lift popover-stacking gate.
//
// WHY THIS EXISTS
// ---------------
// `.cm-lift:hover/:focus-within` sets a `transform`, which creates a STACKING
// CONTEXT. Every popover in this app (.cm-updated-pop / .cm-vstatus-pop) is a
// DESCENDANT of its trigger and carries `z-index: 50`, so inside a lift card
// that 50 is scoped to the card and is worth nothing against anything outside
// it. The FOLLOWING card's in-tile decoration (reveal overlay z-10, caption bar
// z-20) sits in `.cm-carousel-wrap`, which is `position: relative; z-index:
// auto` and therefore NOT a stacking context, so it escapes into the shared
// parent context and outranks the transformed card's effective z-index of 0 —
// painting over the open "N resources" popover on /library?tab=workflows. That
// was caught in a real browser against v0.1.81 by hit-testing the popover's own
// pixels, which returned the NEXT card's reveal <button>.
//
// The fix raises the CARD (the popover cannot raise itself out of the trap),
// and ONLY while a popover inside it is open. These tests pin both halves and
// the value's placement in the app's z-index budget.
//
// HONEST LIMIT: no browser runs in CI, so this asserts the shipped RULE and the
// markup nesting it depends on — not rendered pixels. The paint-order claim
// above was verified interactively, not here.
// ---------------------------------------------------------------------------

var (
	cssCommentRE  = regexp.MustCompile(`(?s)/\*.*?\*/`)
	cssRuleRE     = regexp.MustCompile(`(?s)([^{}]*)\{([^{}]*)\}`)
	cssZIndexRE   = regexp.MustCompile(`z-index\s*:\s*(-?\d+)`)
	cssPositionRE = regexp.MustCompile(`(?m)^\s*position\s*:\s*([a-z-]+)`)
)

// cssRule is one flattened declaration block from app.css. Rules nested in an
// @media block surface as themselves (the at-rule prelude is not a selector),
// which is what these assertions want: a `z-index` on `.cm-lift` is equally
// wrong whether or not it is wrapped in a media query.
type cssRule struct {
	selector string
	body     string
}

// cmLiftRules returns every declaration block in the SHIPPED app.css whose
// selector list mentions .cm-lift, comments stripped.
func cmLiftRules(t *testing.T) []cssRule {
	t.Helper()
	css := cssCommentRE.ReplaceAllString(readAppCSS(t), "")
	var out []cssRule
	for _, m := range cssRuleRE.FindAllStringSubmatch(css, -1) {
		sel := strings.TrimSpace(m[1])
		if sel == "" || strings.HasPrefix(sel, "@") || !strings.Contains(sel, ".cm-lift") {
			continue
		}
		out = append(out, cssRule{selector: sel, body: m[2]})
	}
	if len(out) == 0 {
		t.Fatal("no .cm-lift rules found in app.css — the extractor or the stylesheet changed shape")
	}
	return out
}

// selectorList splits a comma-separated selector list into its parts.
func selectorList(sel string) []string {
	parts := strings.Split(sel, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

// scopedToAnOpenPopover reports whether ONE selector only matches a .cm-lift that
// currently CONTAINS an open popover. All three open states are accepted because
// all three genuinely display a popover: :hover and :focus-within are the CSS-only
// path (and what a click or Tab reaches, the triggers carrying tabindex=0), and
// .cm-pop-open is what the shared hover controller in modelPageScript holds for
// its ~200ms grace period after the pointer leaves.
func scopedToAnOpenPopover(sel string) bool {
	if !strings.Contains(sel, ":has(") {
		return false
	}
	return strings.Contains(sel, ":hover") ||
		strings.Contains(sel, ":focus-within") ||
		strings.Contains(sel, ".cm-pop-open")
}

// TestLiftCardIsRaisedOnlyWhileAPopoverIsOpen is the core regression gate: the
// lift card may be raised, but ONLY in the open-popover state. A permanent raise
// would let a card paint over its neighbours during ordinary scrolling, which is
// a worse bug than the one being fixed.
func TestLiftCardIsRaisedOnlyWhileAPopoverIsOpen(t *testing.T) {
	raised := 0
	for _, r := range cmLiftRules(t) {
		m := cssZIndexRE.FindStringSubmatch(r.body)
		if m == nil {
			continue
		}
		raised++
		for _, sel := range selectorList(r.selector) {
			if !scopedToAnOpenPopover(sel) {
				t.Errorf("selector %q raises .cm-lift (z-index: %s) OUTSIDE the open-popover "+
					"state — a card raised at rest overlaps its neighbours while scrolling. "+
					"Scope it with :has() on an open .cm-updated/.cm-vstatus.", sel, m[1])
			}
		}
		z, err := strconv.Atoi(m[1])
		if err != nil {
			t.Fatalf("unparseable z-index %q on %q", m[1], r.selector)
		}
		// The budget (see the STACKING ORDER ledger in app.css): the raise must
		// clear the NEXT card's escaping in-tile decoration, whose highest layer is
		// the z-20 caption bar, and must stay BELOW the sticky nav at 30 so a raised
		// card can never cover the app chrome (nor the 44/45 rail and the 50
		// popover/lightbox tier above it).
		if z <= 20 {
			t.Errorf("%q raises .cm-lift to z-index %d, which does NOT clear the next card's "+
				"escaping in-tile decoration (max 20) — the popover stays covered", r.selector, z)
		}
		if z >= 30 {
			t.Errorf("%q raises .cm-lift to z-index %d, at or above the sticky nav (30) — a "+
				"raised card would paint over the app chrome", r.selector, z)
		}
	}
	if raised == 0 {
		t.Error("no rule raises .cm-lift while a popover is open — a popover nested in a lift " +
			"card is trapped by the card's transform-induced stacking context and will be " +
			"painted over by the following card")
	}
}

// TestLiftCardIsPositionedSoTheRaiseApplies pins the half that is easy to delete
// as "unused": `z-index` has NO effect on a `position: static` box. The card's
// transform creates a stacking context but does not make z-index apply, so
// without this the raise silently does nothing.
func TestLiftCardIsPositionedSoTheRaiseApplies(t *testing.T) {
	positioned := false
	for _, r := range cmLiftRules(t) {
		m := cssPositionRE.FindStringSubmatch(r.body)
		if m == nil {
			continue
		}
		if selectorList(r.selector)[0] == ".cm-lift" && len(selectorList(r.selector)) == 1 {
			if m[1] == "relative" {
				positioned = true
			}
		}
		if m[1] == "static" {
			t.Errorf("%q sets position: static on .cm-lift — the open-popover z-index would "+
				"stop applying", r.selector)
		}
	}
	if !positioned {
		t.Error("the base `.cm-lift` rule must set `position: relative` (with no z-index, so it " +
			"creates no stacking context on its own) — otherwise the open-popover z-index is " +
			"inert and the popover is still painted over")
	}
}

// TestLiftRaiseCoversEveryPopoverWrapperAndOpenState proves the rule is keyed on
// the SHARED wrapper classes rather than on .cm-res-trigger alone, so every
// popover reusing the mechanism (referenced-resources, version-status, "Updated X
// ago", ComfyUI reachability) is covered inside any lift card — the bug is
// structural, not specific to the surface that exposed it.
func TestLiftRaiseCoversEveryPopoverWrapperAndOpenState(t *testing.T) {
	var raisedSelectors []string
	for _, r := range cmLiftRules(t) {
		if cssZIndexRE.MatchString(r.body) {
			raisedSelectors = append(raisedSelectors, selectorList(r.selector)...)
		}
	}
	all := strings.Join(raisedSelectors, " | ")
	for _, wrapper := range []string{".cm-updated", ".cm-vstatus"} {
		for _, state := range []string{":hover", ":focus-within", ".cm-pop-open"} {
			want := wrapper + state
			found := false
			for _, sel := range raisedSelectors {
				if strings.Contains(sel, want) {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("no open-popover raise covers %q — that popover state stays trapped "+
					"under the following card. Have: %s", want, all)
			}
		}
	}
}

// TestLiftRaiseDegradesGracefullyWithoutHas pins the failure mode. An engine
// without :has() invalidates the WHOLE selector list of a rule that uses it and
// drops that rule, so the raise must live in a block containing NOTHING else:
// then the only consequence is the popover being clipped exactly as it was
// before the fix. Merging the raise into a rule that must keep applying (the
// base .cm-lift rule, or the hover lift itself) would take that rule down with
// it on an old engine.
func TestLiftRaiseDegradesGracefullyWithoutHas(t *testing.T) {
	for _, r := range cmLiftRules(t) {
		hasSel := false
		for _, sel := range selectorList(r.selector) {
			if strings.Contains(sel, ":has(") {
				hasSel = true
				break
			}
		}
		if !hasSel {
			// A rule that must survive on a :has()-less engine may not depend on it.
			continue
		}
		// Every selector in a :has() rule's list must itself use :has(), or the
		// non-:has() selectors are dropped along with it on an old engine.
		for _, sel := range selectorList(r.selector) {
			if !strings.Contains(sel, ":has(") {
				t.Errorf("selector %q shares a rule with a :has() selector — an engine without "+
					":has() drops the whole list, silently taking this selector's declarations "+
					"with it. Give it its own block.", sel)
			}
		}
		// The block must carry only the raise. Anything else here is lost on an old
		// engine rather than degrading to the pre-fix behaviour.
		body := strings.TrimSpace(r.body)
		decls := 0
		for _, d := range strings.Split(body, ";") {
			if strings.TrimSpace(d) != "" {
				decls++
			}
		}
		if decls != 1 || !cssZIndexRE.MatchString(body) {
			t.Errorf("the :has() rule %q must declare ONLY the z-index raise so that dropping "+
				"it on a :has()-less engine costs nothing but the raise; got: %s", r.selector, body)
		}
	}
}

// TestWorkflowCardNestsThePopoverInsideTheLiftCard pins the markup nesting the CSS
// fix is written against: the "N resources" popover is a DESCENDANT of the
// transformed .cm-lift card, which is precisely why its own z-index cannot save
// it. renderString here returns exactly ONE card element, so anything in the
// output is by construction inside it.
//
// If this ever fails because the popover was deliberately moved OUT of the card
// (the structural alternative to the :has() raise), the raise rule in app.css has
// become dead code — delete it together with the tests above rather than
// weakening them.
func TestWorkflowCardNestsThePopoverInsideTheLiftCard(t *testing.T) {
	wf := store.Workflow{
		ID: 7, Name: "stacking", Format: store.WorkflowFormatAPI,
		Source:    store.WorkflowSourceImported,
		Resources: []string{"a.safetensors", "b.safetensors"},
	}
	got := renderString(t, workflowCard(wf, "csrf", workflowResolver{mr: fullMaturityRange()}))

	// The rendered node IS the lift card: its opening tag carries .cm-lift.
	open := got
	if i := strings.Index(got, ">"); i >= 0 {
		open = got[:i]
	}
	if !strings.Contains(open, "cm-lift") {
		t.Fatalf("the workflow card's own element must carry .cm-lift (the transform that "+
			"creates the trapping stacking context); opening tag was:\n%s", open)
	}
	// ...and the popover trigger + body are inside it.
	for _, want := range []string{"cm-updated cm-res-trigger", "cm-updated-pop cm-res-pop"} {
		if !strings.Contains(got, want) {
			t.Errorf("the resources popover (%q) must render inside the .cm-lift card:\n%s", want, got)
		}
	}
}
