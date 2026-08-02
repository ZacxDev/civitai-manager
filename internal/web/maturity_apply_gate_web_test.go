package web

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// ─────────────────────────────────────────────────────────────────────────────
// THE INVERTED-STAGED-BAND GATE (maturityApplyGateScript).
//
// 🔴 THE REGRESSION. Once the tracks began STAGING (v0.1.99) rather than
// committing on change, an inverted band became reachable AND silent. Measured
// live on the released v0.1.99 dogfood: at the saved FULL band both tracks render
// all five stops, so a user can stage min=X and then max=R. Apply POSTs the
// inverted pair, handleSetMaturity correctly answers 400 and persists nothing —
// but the form is hx-swap="none" and there is no htmx:responseError handler
// anywhere in the app, so NOTHING happens. No reload, no message, the panel stays
// open. A dead Apply button. Before staging this could not happen, because
// committing on change re-rendered the other track's bounds after every change.
//
// 🔴 WHAT THESE TESTS CAN AND CANNOT PROVE — read this before trusting a green run.
// The gate is client-side JavaScript and this package has no JS engine and no
// browser (chromedp lives only in the nested e2e/uxaudit module and needs a
// Chromium binary, so a test there would SKIP in CI, which is worse than no test).
// So every guard below is STRUCTURAL: it asserts the shipped markup and the
// shipped script's own wiring, not runtime behaviour. Concretely:
//
//   - PROVEN: the level table the gate compares against matches the server's
//     scale, over all 25 pairs; the script looks up ids that actually exist; the
//     listeners the gate depends on are registered on the right elements; the
//     button starts enabled and is described by a live region.
//   - NOT PROVEN: that a real browser blocks a real click. That check is
//     outstanding and has to be done in a browser against a throwaway DB.
//
// The three server-side halves stay where they are and stay separate:
// TestMaturityControlCannotEmitAnInvertedRange (markup),
// TestMaturityHandlerRejectsAnInvertedRange (the REAL guard — a client gate binds
// only a browser), and TestMaturityPanelDiscardsAStagedBandOnClose (the reset).
// ─────────────────────────────────────────────────────────────────────────────

// matGateScript slices the maturity control's inline gate script.
//
// Scoped to the control's own render, not a page, so it cannot pick up some other
// surface's <script> — the documented vacuity shape where a bare substring is
// satisfied by a different element entirely.
func matGateScript(t *testing.T, out string) string {
	t.Helper()
	const open = "<script>"
	i := strings.Index(out, open)
	if i < 0 {
		t.Fatalf("the maturity control renders no <script> — the Apply gate is missing "+
			"entirely, so an inverted staged band would POST, 400, and do nothing "+
			"visible:\n%s", out)
	}
	rest := out[i+len(open):]
	end := strings.Index(rest, "</script>")
	if end < 0 {
		t.Fatalf("the maturity control's <script> is not closed:\n%s", out)
	}
	if strings.Contains(rest[end:], open) {
		t.Fatalf("the maturity control renders more than one <script>; this helper "+
			"assumes exactly one and would be asserting about the wrong body:\n%s", out)
	}
	return rest[:end]
}

// matGateOrderRe pulls the gate's level table out of the script. The table is a
// HAND-WRITTEN literal on purpose — see TestMaturityApplyGateTableMatchesTheScale.
var matGateOrderRe = regexp.MustCompile(`ORDER\s*=\s*\{([^}]*)\}`)

// matGateOrder parses that literal into slug -> rank.
func matGateOrder(t *testing.T, script string) map[string]int {
	t.Helper()
	m := matGateOrderRe.FindStringSubmatch(script)
	if m == nil {
		t.Fatalf("the gate script has no ORDER table, so it has no notion of which band "+
			"is inverted:\n%s", script)
	}
	out := map[string]int{}
	for _, pair := range strings.Split(m[1], ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		kv := strings.SplitN(pair, ":", 2)
		if len(kv) != 2 {
			t.Fatalf("unparseable ORDER entry %q in:\n%s", pair, script)
		}
		k := strings.Trim(strings.TrimSpace(kv[0]), `'"`)
		v, err := strconv.Atoi(strings.TrimSpace(kv[1]))
		if err != nil {
			t.Fatalf("ORDER entry %q has a non-numeric rank: %v", pair, err)
		}
		if _, dup := out[k]; dup {
			t.Errorf("ORDER lists %q twice", k)
		}
		out[k] = v
	}
	return out
}

// TestMaturityApplyGateTableMatchesTheScale is the DRIFT guard, and it is the one
// test here that reaches the gate's actual decision.
//
// 🔴 THE TABLE IS DELIBERATELY HAND-WRITTEN IN THE SCRIPT, NOT GENERATED FROM
// maturityScale. Generating it would make this test circular — "calibrated to its
// own constant", the documented vacuous shape: parse a table produced from
// maturityScale, compare it to maturityScale, and it can never fail. Written by
// hand it is a second copy of a rule that lives in Go, which is exactly the thing
// that needs a guard: add a sixth level to maturityScale, or typo a rank, and the
// browser silently gates the wrong pairs while every server-side test stays green.
//
// Having established the table, the test then evaluates the gate's own predicate
// (rank[min] > rank[max]) over ALL 25 ordered pairs and requires it to agree with
// maturityRange.valid() on every one. That covers both halves of the requirement
// in one sweep: every inverted staged pair is blocked, and every valid staged pair
// is left alone.
func TestMaturityApplyGateTableMatchesTheScale(t *testing.T) {
	script := matGateScript(t, renderString(t, maturityControl(fullMaturityRange(), "csrf-tok")))
	order := matGateOrder(t, script)

	if len(order) != len(maturityScale) {
		t.Fatalf("the gate's ORDER table has %d levels but the scale has %d — the client "+
			"and the server disagree about what a band is: %v", len(order), len(maturityScale), order)
	}
	for want, l := range maturityScale {
		got, ok := order[l.slug()]
		if !ok {
			t.Errorf("the gate's ORDER table does not know the level %q, so any band naming "+
				"it is treated as UNRANKED and waved through: %v", l.slug(), order)
			continue
		}
		if got != want {
			t.Errorf("the gate ranks %q at %d, but it is step %d of the scale — the gate would "+
				"block and allow the wrong pairs", l.slug(), got, want)
		}
	}

	// Now the predicate itself, driven off the PARSED table (i.e. off what actually
	// ships), checked against the server's own notion of a valid band.
	var blocked, allowed int
	for _, lo := range maturityScale {
		for _, hi := range maturityScale {
			a, okA := order[lo.slug()]
			b, okB := order[hi.slug()]
			if !okA || !okB {
				continue // already reported above
			}
			gateBlocks := a > b
			serverValid := maturityRange{Min: lo, Max: hi}.valid()
			if gateBlocks == serverValid {
				t.Errorf("staged %s..%s: the gate %s it, but the server considers it %s. "+
					"A pair the gate allows and the server rejects is the dead Apply button "+
					"this change fixes; a pair the gate blocks and the server accepts is a band "+
					"the user is entitled to and cannot reach.",
					lo.slug(), hi.slug(),
					map[bool]string{true: "BLOCKS", false: "allows"}[gateBlocks],
					map[bool]string{true: "valid", false: "inverted"}[serverValid])
			}
			if gateBlocks {
				blocked++
			} else {
				allowed++
			}
		}
	}
	// Fixture-reach: a table that parsed to nothing useful would make the loop above
	// assert over an empty or one-sided set and still pass.
	if blocked == 0 || allowed == 0 {
		t.Fatalf("the sweep did not reach both outcomes (blocked=%d, allowed=%d) — it cannot "+
			"have proved anything about either", blocked, allowed)
	}
}

// matGateVarRe binds a local name in the gate script to the id it resolves.
var matGateVarRe = regexp.MustCompile(`var (\w+) = document\.getElementById\('([^']+)'\)`)

// matGateListenRe finds one addEventListener registration and its target's local
// name.
var matGateListenRe = regexp.MustCompile(`(\w+)\.addEventListener\('([^']+)'`)

// matGateWiring resolves the script's listeners to the ELEMENT IDS they are bound
// to, so the assertions below are about the markup rather than about local
// variable names this test would otherwise be pinning.
func matGateWiring(t *testing.T, script string) (ids map[string]string, listeners map[string][]string) {
	t.Helper()
	ids = map[string]string{}
	for _, m := range matGateVarRe.FindAllStringSubmatch(script, -1) {
		ids[m[1]] = m[2]
	}
	if len(ids) == 0 {
		t.Fatalf("the gate script looks up no element at all:\n%s", script)
	}
	listeners = map[string][]string{}
	for _, m := range matGateListenRe.FindAllStringSubmatch(script, -1) {
		id, ok := ids[m[1]]
		if !ok {
			t.Errorf("the gate script binds %q to the local %q, which is not resolved from an "+
				"element id — this test cannot tell what it is listening to", m[2], m[1])
			continue
		}
		listeners[id] = append(listeners[id], m[2])
	}
	return ids, listeners
}

func matGateHasListener(listeners map[string][]string, id, event string) bool {
	for _, e := range listeners[id] {
		if e == event {
			return true
		}
	}
	return false
}

// TestMaturityApplyGateIsWiredToTheElementsItGates is the id-coupling guard.
//
// The whole script begins `if (!f || !btn || !msg) { return; }`, so ONE stale id
// makes it return immediately and gate nothing — silently, with no console error
// and no visible difference until a user stages an inverted band. That is the same
// failure shape as the trigger's popovertarget and the panel's id: two halves of
// one wire, and a typo in either renders a control that does nothing.
func TestMaturityApplyGateIsWiredToTheElementsItGates(t *testing.T) {
	out := renderString(t, maturityControl(fullMaturityRange(), "csrf-tok"))
	script := matGateScript(t, out)
	ids, _ := matGateWiring(t, script)

	want := map[string]string{
		maturityFormID:      "the form whose staged values it reads",
		maturityApplyID:     "the Apply button whose aria-disabled it sets",
		maturityInvalidID:   "the live region that says why",
		maturityMenuPanelID: "the panel whose close resets the form",
	}
	have := map[string]bool{}
	for _, id := range ids {
		have[id] = true
		// Every id the script reaches for must actually be in the markup.
		if !strings.Contains(out, `id="`+id+`"`) {
			t.Errorf("the gate script looks up id=%q, which the control does not render. The "+
				"script's own guard clause then returns early and gates NOTHING, silently:\n%s", id, out)
		}
	}
	for id, why := range want {
		if !have[id] {
			t.Errorf("the gate script never resolves id=%q (%s) — it cannot be gating on it", id, why)
		}
	}
}

// TestMaturityApplyGateRecomputesOnEveryPathThatChangesTheStagedBand covers the
// three ways the staged band moves, and the second and third are the subtle ones.
//
// 🔴 THE RESET PATH. The panel carries
// ontoggle="…this.querySelector('form').reset()", which discards a staged band on
// close and restores the SAVED one — which is always valid, so Apply must end up
// ENABLED again after a dismiss/reopen cycle. form.reset() fires NO `change`
// event, so a gate bound only to `change` leaves Apply stale-disabled forever
// after one dismiss: the user reopens the panel showing a perfectly valid band
// and the button still refuses. Two independent recomputes cover it — a `reset`
// listener on the form (deferred, because the reset event fires BEFORE the control
// values are restored) and a `toggle` listener on the panel (which runs after the
// inline ontoggle handler has returned, i.e. after reset() completed).
//
// 🔴 THE COMMIT BLOCK. aria-disabled is advisory: unlike the `disabled` attribute
// it stops neither a click nor an implicit Enter submit. Without the
// htmx:beforeRequest cancellation the button would look disabled and POST anyway —
// straight back to the invisible 400.
func TestMaturityApplyGateRecomputesOnEveryPathThatChangesTheStagedBand(t *testing.T) {
	out := renderString(t, maturityControl(fullMaturityRange(), "csrf-tok"))
	script := matGateScript(t, out)
	_, listeners := matGateWiring(t, script)

	for _, c := range []struct{ id, event, why string }{
		{maturityFormID, "change", "a radio track fires `change` on every click and every arrow " +
			"keypress; without this the gate never notices a staged band at all"},
		{maturityFormID, "reset", "the panel's ontoggle calls form.reset() to discard a staged band. " +
			"reset() fires NO change event, so a change-only gate leaves Apply stale-DISABLED " +
			"after one dismiss, refusing a band that is perfectly valid"},
		{maturityMenuPanelID, "toggle", "the second, independent recompute for the same dismiss/reopen " +
			"cycle: it runs after the inline ontoggle reset() has completed, so it sees the " +
			"restored values without needing to defer"},
		{maturityFormID, "htmx:beforeRequest", "aria-disabled does not stop a click or an implicit " +
			"Enter submit, so the request itself must be cancelled — otherwise the button looks " +
			"disabled and POSTs anyway, and the 400 is invisible (hx-swap=none, no responseError handler)"},
	} {
		if !matGateHasListener(listeners, c.id, c.event) {
			t.Errorf("the gate registers no %q listener on id=%q: %s\nregistered: %v",
				c.event, c.id, c.why, listeners)
		}
	}

	// The reset listener must DEFER. The reset event fires before the controls are
	// restored, so a synchronous recompute reads the values being discarded — it
	// would leave Apply disabled on exactly the dismiss it is meant to clear.
	resetIdx := strings.Index(script, `addEventListener('reset'`)
	if resetIdx < 0 {
		t.Fatal("no reset listener to inspect (reported above)")
	}
	// Bound the window to this one registration so a setTimeout elsewhere in the
	// script cannot satisfy the assertion.
	tail := script[resetIdx:]
	if nxt := strings.Index(tail[1:], "addEventListener("); nxt >= 0 {
		tail = tail[:nxt+1]
	}
	if !strings.Contains(tail, "setTimeout") {
		t.Errorf("the gate's `reset` handler does not defer. The reset event fires BEFORE the "+
			"control values are restored, so a synchronous recompute reads the band being "+
			"DISCARDED and leaves Apply disabled on the very dismiss that should re-enable "+
			"it:\n%s", tail)
	}
}

// matApplyButtonTag returns the Apply button's own open tag, scoped to the
// maturity form. Never a body-wide substring: this control renders in the navbar
// of every page, and `disabled`/`aria-disabled` are attributes any other button
// may legitimately carry.
func matApplyButtonTag(t *testing.T, out string) string {
	t.Helper()
	_, extent := matForm(t, out)
	m := regexp.MustCompile(`<button[^>]*\bid="` + maturityApplyID + `"[^>]*>`).FindString(extent)
	if m == "" {
		t.Fatalf("no <button id=%q> inside the maturity form:\n%s", maturityApplyID, extent)
	}
	return m
}

// TestMaturityApplyStartsEnabledAtEverySavedBand pins the initial state.
//
// The SAVED band is always valid — handleSetMaturity refuses to persist anything
// else, and maturityControl normalizes a corrupt stored value — so at render time
// staged == saved == valid and Apply must be live. A gate that shipped its
// disabled state in the MARKUP would present a control that is dead on arrival,
// and dead with JS off, where nothing would ever re-enable it.
func TestMaturityApplyStartsEnabledAtEverySavedBand(t *testing.T) {
	for _, lo := range maturityScale {
		for _, hi := range maturityScale {
			if lo > hi {
				continue
			}
			mr := maturityRange{Min: lo, Max: hi}
			tag := matApplyButtonTag(t, renderString(t, maturityControl(mr, "csrf-tok")))
			if strings.Contains(tag, `aria-disabled="true"`) {
				t.Errorf("%s: Apply is server-rendered aria-disabled — the saved band is always "+
					"valid, so the button must start live: %s", mr.String(), tag)
			}
			if regexp.MustCompile(`(^|\s)disabled(\s|=|>)`).MatchString(tag) {
				t.Errorf("%s: Apply carries the real `disabled` attribute. It leaves the tab order "+
					"and announces nothing, so a keyboard user tabs past the control with no way to "+
					"learn why it will not commit — the gate uses aria-disabled + a live region "+
					"instead: %s", mr.String(), tag)
			}
		}
	}
	// Fixture reach: the loop must actually have found a button to inspect.
	if matApplyButtonTag(t, renderString(t, maturityControl(fullMaturityRange(), "csrf-tok"))) == "" {
		t.Fatal("no Apply button was inspected")
	}
}

// TestMaturityApplyGateExplainsItselfToAssistiveTech is the accessibility half.
//
// A disabled-looking button with no reachable reason is the same dead end the
// silent 400 was. The reason therefore has to be BOTH announced when it appears
// (the user is focused in the radio group at that moment, not on the button) and
// discoverable later by anyone who tabs to Apply — hence a role="status" live
// region AND aria-describedby, not one or the other.
//
// It must also stay RENDERED. A live region that is `hidden` (or display:none) and
// then un-hidden and populated in the same tick is announced unreliably; an empty
// block box has no line boxes and so costs no layout, which is what makes a
// permanently-present region free.
func TestMaturityApplyGateExplainsItselfToAssistiveTech(t *testing.T) {
	out := renderString(t, maturityControl(fullMaturityRange(), "csrf-tok"))
	_, extent := matForm(t, out)

	// The reason element, sliced by its own id — inside the FORM, so it travels with
	// the control rather than sitting somewhere a fragment swap could drop.
	idx := strings.Index(extent, `id="`+maturityInvalidID+`"`)
	if idx < 0 {
		t.Fatalf("no element with id=%q inside the maturity form — the gate has nowhere to say "+
			"why Apply will not commit:\n%s", maturityInvalidID, extent)
	}
	start := strings.LastIndex(extent[:idx], "<")
	gt := strings.Index(extent[start:], ">")
	if start < 0 || gt < 0 {
		t.Fatalf("could not slice the open tag for id=%q:\n%s", maturityInvalidID, extent)
	}
	reason := extent[start : start+gt+1]

	if !strings.Contains(reason, `role="status"`) {
		t.Errorf("the reason element is not a live region (role=\"status\"). The user is focused "+
			"in the radio group when the band goes inverted, not on the button, so without it "+
			"nothing is announced at the moment the gate closes: %s", reason)
	}
	for _, banned := range []string{"hidden", "aria-hidden"} {
		if strings.Contains(reason, banned) {
			t.Errorf("the reason element carries %q. A live region that is un-hidden and populated "+
				"in the same tick is announced unreliably — it must stay rendered and simply be "+
				"empty when the band is valid: %s", banned, reason)
		}
	}
	// Empty at render: the saved band is valid, so there is nothing to say yet.
	body := extent[start+gt+1:]
	if end := strings.Index(body, "<"); end > 0 {
		if txt := strings.TrimSpace(body[:end]); txt != "" {
			t.Errorf("the reason element is server-rendered with text (%q). The saved band is "+
				"always valid, so a reason on arrival is a lie — and with JS off it would never "+
				"be cleared", txt)
		}
	}

	// The programmatic association, asserted on the BUTTON's own tag.
	tag := matApplyButtonTag(t, out)
	if !strings.Contains(tag, `aria-describedby="`+maturityInvalidID+`"`) {
		t.Errorf("the Apply button is not described by the reason element. An announcement that "+
			"has already passed is unrecoverable: a user who tabs to Apply afterwards would be "+
			"told only \"Apply\", with no hint that it will refuse: %s", tag)
	}
}
