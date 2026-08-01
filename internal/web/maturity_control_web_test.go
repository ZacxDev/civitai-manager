package web

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/store"
)

// newFileBackedServer is newTestServer with a FILE-backed store.
//
// 🔴 store.Open(":memory:") resolves to `file::memory:?cache=shared`, so every
// in-memory test server in this package shares ONE database. A test that writes a
// setting and then reads it back would be asserting about rows another test's
// server may have written — and, worse, could pass for that reason. Any test that
// checks "did this write land / did it NOT land" needs its own file.
func newFileBackedServer(t *testing.T) *Server {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return NewServer(st, stubReader{}, stubSubscriber{}, Config{
		BaseURL: "https://civitai.com", DefaultPollInterval: time.Hour, Addr: "127.0.0.1:8787",
	}, nil)
}

// The nav's maturity RANGE control replaced the old 2-state NSFW blur⇄show
// button, and has since become an ICON BUTTON opening a POPOVER that holds a
// two-sided segmented slider (two 5-stop radio tracks). These tests cover the
// things a hand-rolled range control can get wrong: it must be operable without a
// mouse, both ends must be NAMED for assistive tech, and it must not be able to
// submit an inverted band.
//
// 🔴 THE INVERSION GUARD HAS TWO INDEPENDENT HALVES AND THEY MUST STAY SEPARATE:
// TestMaturityControlCannotEmitAnInvertedRange checks the MARKUP (which only ever
// binds a browser), and TestMaturityHandlerRejectsAnInvertedRange checks the
// SERVER (which is the real guard). Collapsing them into one test would let a
// change satisfy the pair while defeating either.

// matStopRe extracts one radio stop's attributes from a rendered track: the
// value slug, plus whatever else the tag carries (checked / disabled).
// matStopOpenRe matches the OPENING tag of one stop — a <label> for a selectable
// level, a <span class="… cm-mat-stop-out"> for a level outside the valid band.
// Captures: 1 = tag name, 2 = the extra classes, 3 = the step index.
//
// It deliberately matches only the OPEN tag, and callers slice between successive
// matches. A non-greedy `.*?</span>` would terminate at the first NESTED </span>
// (the dot), silently truncating every out-of-band stop to nothing.
var matStopOpenRe = regexp.MustCompile(`<(label|span) class="cm-mat-stop([^"]*)" data-step="(\d+)">`)

// matRadioRe finds a stop's radio input, if it has one.
var matRadioRe = regexp.MustCompile(`<input type="radio"[^>]*value="([a-z0-9]+)"([^>]*)>`)

// matStop is one rendered stop: its level slug (empty when it has no input at all),
// whether it is out-of-band, and whether it carries a radio.
type matStop struct {
	slug      string
	outOfBand bool
	hasInput  bool
}

// matStops slices a track into its stops. Callers assert on ALL of them, because
// the property under guard is about which stops carry a control at all.
func matStops(track string) []matStop {
	locs := matStopOpenRe.FindAllStringSubmatchIndex(track, -1)
	out := make([]matStop, 0, len(locs))
	for i, loc := range locs {
		end := len(track)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		seg := track[loc[1]:end]
		st := matStop{outOfBand: strings.Contains(track[loc[4]:loc[5]], "cm-mat-stop-out")}
		if m := matRadioRe.FindStringSubmatch(seg); m != nil {
			st.hasInput, st.slug = true, m[1]
		}
		out = append(out, st)
	}
	return out
}

// matTrack slices out ONE end's <fieldset> by its id, using brace-free tag
// balancing on the fieldset element so the two ends can never bleed into each
// other. Comparing strings.Index positions would not do: "between the two ids" is
// still true for an element NESTED inside the first.
func matTrack(t *testing.T, out, id string) string {
	t.Helper()
	open := strings.Index(out, `id="`+id+`"`)
	if open < 0 {
		t.Fatalf("no element with id=%q in:\n%s", id, out)
	}
	start := strings.LastIndex(out[:open], "<fieldset")
	if start < 0 {
		t.Fatalf("id=%q is not inside a <fieldset>:\n%s", id, out)
	}
	end := strings.Index(out[start:], "</fieldset>")
	if end < 0 {
		t.Fatalf("the <fieldset> for id=%q is not closed:\n%s", id, out)
	}
	return out[start : start+end]
}

// TestMaturityControlIsKeyboardOperable proves the control is built from NATIVE
// form elements, which is the whole keyboard story: a radio group is reached with
// Tab, moved between stops with the arrow keys (which also COMMIT, firing
// `change`), and needs no key handling of our own. The trigger is a real
// <button>, activated by Enter/Space, and the panel is a `popover` in its default
// `auto` state — which is where light-dismiss and Escape come from.
//
// The negative half matters as much: a <div role=slider> or a click-only widget
// would pass a "does the markup contain the levels" test and be unusable.
func TestMaturityControlIsKeyboardOperable(t *testing.T) {
	out := renderString(t, maturityControl(fullMaturityRange(), "csrf-tok"))

	// Ten stops: five levels x two ends, all native radios.
	if n := strings.Count(out, `<input type="radio"`); n != 2*len(maturityScale) {
		t.Fatalf("expected %d native radio stops (5 levels x 2 ends), got %d:\n%s",
			2*len(maturityScale), n, out)
	}
	// A real button trigger wired to the panel by popovertarget.
	if !strings.Contains(out, `<button type="button"`) {
		t.Errorf("the trigger must be a real <button type=button>:\n%s", out)
	}
	if !strings.Contains(out, `popovertarget="`+maturityMenuPanelID+`"`) ||
		!strings.Contains(out, `id="`+maturityMenuPanelID+`"`) {
		t.Errorf("the trigger's popovertarget and the panel's id must both be %q — "+
			"a mismatch renders a button that does nothing, silently:\n%s", maturityMenuPanelID, out)
	}
	// 🔴 VALUELESS popover = `auto` = light-dismiss + Escape. popover="manual" has
	// NEITHER, so a value here would silently remove both behaviours.
	// 🔴 ASSERT THE PANEL ELEMENT, NOT A BARE " popover" SUBSTRING. The loose form
	// was VACUOUS: it is satisfied by the TRIGGER's ` popovertarget="…"`, so deleting
	// the panel's own `popover` attribute passed the entire web suite.
	//
	// What that hole hid is the control's worst failure mode. The base
	// .cm-maturity-panel rule deliberately declares no `display` (the documented
	// UA-display trap — an author `display` would beat `[popover]:not(:popover-open)`
	// and pin the panel open). Without the attribute the panel is therefore a plain
	// block at `position: fixed; top: var(--cm-nav-h); left: 0; right: 0` — a
	// permanently-open sheet across the top of EVERY page in the app.
	if !strings.Contains(out, `id="`+maturityMenuPanelID+`" popover`) {
		t.Errorf("the panel element itself must carry the valueless popover attribute — "+
			"a bare \" popover\" check passes on the trigger's popovertarget and would "+
			"miss a panel that renders as a permanent full-width sheet:\n%s", out)
	}
	if strings.Contains(out, `popover="manual"`) || strings.Contains(out, `popover="hint"`) {
		t.Errorf("the panel's popover must stay VALUELESS (auto) — manual/hint have no "+
			"light-dismiss and no Escape:\n%s", out)
	}
	// No custom key handling, no non-native widget standing in for a control.
	for _, banned := range []string{
		"onkeydown", "onkeyup", "onkeypress", `role="slider"`, "tabindex=", "<div onclick",
	} {
		if strings.Contains(out, banned) {
			t.Errorf("the control uses %q — native elements must carry the keyboard story:\n%s", banned, out)
		}
	}
	if strings.Contains(out, `tabindex="-1"`) {
		t.Error("an end of the range must not be removed from the tab order")
	}
	// Changing any stop submits the whole form (both values + the token).
	if !strings.Contains(out, `hx-post="/settings/maturity"`) {
		t.Errorf("the control must POST /settings/maturity:\n%s", out)
	}
	if !strings.Contains(out, `hx-trigger="change"`) {
		t.Errorf("the control must submit on change (arrow-key changes fire it too):\n%s", out)
	}
}

// TestMaturityControlBothEndsHaveAccessibleNames proves EACH end is a <fieldset>
// with its own <legend>. One ambiguous "Maturity" for two tracks would leave a
// screen reader user unable to tell which end they are on — the same requirement
// the two <select>s met with per-end <label>s.
//
// It also proves the trigger's accessible name carries the CURRENT BAND. An icon
// button announcing only "Maturity" would lose the at-a-glance state the two
// visible selects used to provide.
func TestMaturityControlBothEndsHaveAccessibleNames(t *testing.T) {
	mr := maturityRange{Min: maturityPG13, Max: maturityX}
	out := renderString(t, maturityControl(mr, "csrf-tok"))

	for _, want := range []struct{ id, legend string }{
		{maturityControlMinID, "Maturity from"},
		{maturityControlMaxID, "Maturity to"},
	} {
		track := matTrack(t, out, want.id)
		lg := regexp.MustCompile(`<legend[^>]*>([^<]*)</legend>`).FindStringSubmatch(track)
		if lg == nil {
			t.Errorf("the %q track has no <legend> — that end has no accessible name:\n%s", want.id, track)
			continue
		}
		if strings.TrimSpace(lg[1]) != want.legend {
			t.Errorf("legend for %q = %q, want %q", want.id, lg[1], want.legend)
		}
	}

	// The trigger names the current band.
	if !strings.Contains(out, `aria-label="Maturity: `+mr.label()+`"`) {
		t.Errorf("the trigger's accessible name must carry the current band (%q):\n%s", mr.label(), out)
	}

	// Every stop's LABEL TEXT is the radio's accessible name — so no stop may be
	// left nameless, and none needs an aria-label.
	for _, l := range maturityScale {
		if !strings.Contains(out, ">"+l.label()+"</span>") {
			t.Errorf("no visible tick names level %q, so that stop has no accessible name:\n%s", l.label(), out)
		}
	}

	// 🔴 The radio must be CLIPPED, not removed. display:none / visibility:hidden /
	// hidden would take it out of the tab order AND the accessibility tree, which
	// is the entire keyboard and screen-reader story for this control.
	if strings.Contains(out, "display:none") || strings.Contains(out, "<input hidden") {
		t.Error("a stop must not be removed from the accessibility tree")
	}
}

// TestMaturityControlCannotEmitAnInvertedRange is the STRUCTURAL half of the
// inversion guard, over every valid band: a stop that would invert the range must
// be `disabled` (unsubmittable, unselectable, skipped by arrow keys), and every
// ENABLED stop must lead to a still-valid band.
//
// The handler rejects an inverted submission anyway — see
// TestMaturityHandlerRejectsAnInvertedRange, which is deliberately a SEPARATE
// test — because markup only constrains a browser.
func TestMaturityControlCannotEmitAnInvertedRange(t *testing.T) {
	for _, lo := range maturityScale {
		for _, hi := range maturityScale {
			if lo > hi {
				continue
			}
			mr := maturityRange{Min: lo, Max: hi}
			out := renderString(t, maturityControl(mr, "csrf-tok"))

			minTrack := matTrack(t, out, maturityControlMinID)
			maxTrack := matTrack(t, out, maturityControlMaxID)

			check := func(end, track string, band func(maturityLevel) maturityRange) {
				stops := matStops(track)
				if len(stops) != len(maturityScale) {
					t.Fatalf("%s: the %s track rendered %d stops, want %d — every level must be "+
						"DRAWN so the geometry stays uniform and the blocked region is visible:\n%s",
						mr.String(), end, len(stops), len(maturityScale), track)
				}
				enabled := 0
				for i, st := range stops {
					// 🔴 THE LOAD-BEARING ASSERTION. An out-of-band stop must emit NO input,
					// so it is not a member of the radio group. It used to be a `disabled`
					// radio, which arrow keys skip — but a native radio group WRAPS, so at a
					// boundary the skip landed on the far end: from "X only", one ArrowLeft
					// (the REDUCING direction) committed and persisted "X to XXX". A
					// content-gating control failing open on one keypress.
					if st.outOfBand && st.hasInput {
						t.Errorf("%s: the %s end renders step %d as an out-of-band stop that STILL "+
							"carries a radio (%q). A disabled radio is still a group member, and "+
							"arrow-key wrap-around reaches it — that is how one keypress raised the "+
							"ceiling to XXX. Out-of-band stops must emit no input at all.",
							mr.String(), end, i, st.slug)
						continue
					}
					if !st.hasInput {
						if !st.outOfBand {
							t.Errorf("%s: the %s end renders step %d with no input and no "+
								"out-of-band marker — it is neither selectable nor legibly blocked",
								mr.String(), end, i)
						}
						continue
					}
					l, ok := maturityFromSlug(st.slug)
					if !ok {
						t.Fatalf("%s: the %s end offered an unknown level %q", mr.String(), end, st.slug)
					}
					enabled++
					if !band(l).valid() {
						t.Errorf("%s: the %s end leaves %s SELECTABLE, and choosing it would invert the range",
							mr.String(), end, st.slug)
					}
				}
				// The converse: a level that WOULD be a valid band must stay reachable, or
				// the control blocks a state the user is entitled to.
				for _, l := range maturityScale {
					if !band(l).valid() {
						continue
					}
					found := false
					for _, st := range stops {
						if st.hasInput && st.slug == l.slug() {
							found = true
						}
					}
					if !found {
						t.Errorf("%s: the %s end has no selectable stop for %s, which would have "+
							"been a valid band — the control must not block a reachable state",
							mr.String(), end, l.slug())
					}
				}
				if enabled == 0 {
					t.Fatalf("%s: the %s end has no selectable stop at all", mr.String(), end)
				}
			}
			check("FROM", minTrack, func(l maturityLevel) maturityRange {
				return maturityRange{Min: l, Max: mr.Max}
			})
			check("TO", maxTrack, func(l maturityLevel) maturityRange {
				return maturityRange{Min: mr.Min, Max: l}
			})

			// The current value of each end must be CHECKED, or a change to the other
			// end would silently submit a different band. It must also never be the
			// disabled stop — that end would then submit nothing and the handler would
			// 400 on an empty slug.
			for _, want := range []struct {
				end, track, slug string
			}{
				{"FROM", minTrack, lo.slug()},
				{"TO", maxTrack, hi.slug()},
			} {
				var found bool
				for _, s := range matRadioRe.FindAllStringSubmatch(want.track, -1) {
					if s[1] != want.slug {
						continue
					}
					found = true
					if !strings.Contains(s[2], "checked") {
						t.Errorf("%s: the %s end does not check %s:\n%s", mr.String(), want.end, want.slug, want.track)
					}
					// The selected level is always inside the band, so its stop must be a
					// real radio — never an out-of-band span. If it were, that end would
					// submit nothing and the handler would 400 on an empty slug.
				}
				if !found {
					t.Errorf("%s: the %s end has no stop for %s", mr.String(), want.end, want.slug)
				}
			}
		}
	}
}

// TestMaturityHandlerRejectsAnInvertedRange is the SERVER half of the inversion
// guard, and it is the real one — the markup constraint above only binds a
// browser, so a hand-made POST bypasses it entirely.
//
// It asserts the rejection is a REJECTION: 400, and NOTHING persisted. Silently
// swapping the ends would grant a band the user never asked for; clamping to an
// empty band would blank every gallery in a way that reads as a fetch failure.
func TestMaturityHandlerRejectsAnInvertedRange(t *testing.T) {
	// A file-backed store: store.Open(":memory:") resolves to
	// file::memory:?cache=shared, so every in-memory test server in this package
	// shares ONE database and "did this write land" would be answering about
	// someone else's rows.
	srv := newFileBackedServer(t)

	// Establish a known, narrow starting point through the legitimate path.
	post := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/settings/maturity", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		srv.Handler().ServeHTTP(rec, req)
		return rec
	}
	if rec := post("min=pg13&max=r&csrf_token=" + srv.csrf); rec.Code != http.StatusNoContent {
		t.Fatalf("a VALID range must persist: status = %d, want 204", rec.Code)
	}
	if got := srv.maturity(); got.String() != "pg13:r" {
		t.Fatalf("fixture did not reach the interesting state: stored range = %q, want pg13:r", got.String())
	}

	// Now the inverted submission. Every field is otherwise well-formed and the
	// CSRF token is VALID on purpose: a 403 would prove nothing about the range
	// check.
	rec := post("min=xxx&max=pg&csrf_token=" + srv.csrf)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("an INVERTED range must be rejected with 400, got %d", rec.Code)
	}
	if got := srv.maturity(); got.String() != "pg13:r" {
		t.Errorf("an inverted submission changed the stored range to %q — it must persist NOTHING. "+
			"Swapping the ends would grant an unasked-for band; clamping to empty reads as a fetch failure",
			got.String())
	}

	// An unknown level is likewise a 400 that persists nothing.
	if rec := post("min=pg&max=banana&csrf_token=" + srv.csrf); rec.Code != http.StatusBadRequest {
		t.Errorf("an unknown level must be rejected with 400, got %d", rec.Code)
	}
	if got := srv.maturity(); got.String() != "pg13:r" {
		t.Errorf("an unknown-level submission changed the stored range to %q", got.String())
	}

	// And CSRF is still enforced on this endpoint.
	if rec := post("min=pg&max=xxx"); rec.Code != http.StatusForbidden {
		t.Errorf("maturity POST without CSRF = %d, want 403", rec.Code)
	}
}

// TestMaturityControlCarriesCSRF: the setter is a state-changing POST, so the
// token must ride in the form body.
func TestMaturityControlCarriesCSRF(t *testing.T) {
	out := renderString(t, maturityControl(fullMaturityRange(), "csrf-tok"))
	if !strings.Contains(out, `name="csrf_token"`) || !strings.Contains(out, `value="csrf-tok"`) {
		t.Errorf("the control must carry the CSRF token in the form:\n%s", out)
	}
}

// TestMaturityControlNormalizesAnInvalidStoredRange: a corrupt stored value must
// render the full range rather than an empty or inverted control. This is the
// fail-OPEN read (a preference, not an access control).
func TestMaturityControlNormalizesAnInvalidStoredRange(t *testing.T) {
	out := renderString(t, maturityControl(maturityRange{Min: maturityXXX, Max: maturityPG}, "csrf-tok"))
	minTrack := matTrack(t, out, maturityControlMinID)
	maxTrack := matTrack(t, out, maturityControlMaxID)
	if !strings.Contains(minTrack, `value="pg" class="cm-mat-radio" id="cm-maturity-min-pg" checked`) {
		t.Errorf("an invalid stored range should check PG at the FROM end:\n%s", minTrack)
	}
	if !strings.Contains(maxTrack, `value="xxx" class="cm-mat-radio" id="cm-maturity-max-xxx" checked`) {
		t.Errorf("an invalid stored range should check XXX at the TO end:\n%s", maxTrack)
	}
	// At the full range NOTHING is out of band, so nothing may be disabled.
	if strings.Contains(minTrack, "disabled") || strings.Contains(maxTrack, "disabled") {
		t.Errorf("the full range blocks no level, so no stop may be disabled:\n%s\n%s", minTrack, maxTrack)
	}
}

// TestMaturityPopoverPositionsAndKeepsItsRadiosReachable guards the CSS that the
// presence-only class list in class_coverage_web_test.go cannot: the two
// positioning modes, the UA-display trap, and the clipped-not-hidden radio.
func TestMaturityPopoverPositionsAndKeepsItsRadiosReachable(t *testing.T) {
	b, err := assetsFS.ReadFile("assets/app.css")
	if err != nil {
		t.Fatalf("read app.css: %v", err)
	}
	css := string(b)

	// 🔴 THE UA-DISPLAY TRAP. A popover is closed by the UA rule
	// `[popover]:not(:popover-open){display:none}`, and an AUTHOR `display` beats a
	// UA one at any specificity — so a `display` in the BASE rule pins the panel
	// permanently open, on every page. The layout must live on :popover-open.
	base := cssRuleIn(t, css, ".cm-maturity-panel {")
	if strings.Contains(base, "display:") {
		t.Errorf("the base .cm-maturity-panel rule must declare NO `display` — an author display "+
			"beats the UA `[popover]:not(:popover-open){display:none}` rule and the panel would "+
			"render permanently open. Put it on :popover-open. Got:\n%s", base)
	}
	open := cssRuleIn(t, css, ".cm-maturity-panel:popover-open {")
	if !strings.Contains(open, "display: block;") {
		t.Errorf("the :popover-open rule must supply the layout the base rule may not. Got:\n%s", open)
	}

	// 🔴 THE RADIO IS CLIPPED, NOT HIDDEN. display:none / visibility:hidden would
	// remove it from the tab order and the accessibility tree — i.e. delete the
	// keyboard story of a control whose entire interaction model is arrow keys.
	radio := cssRuleIn(t, css, ".cm-mat-radio {")
	if !strings.Contains(radio, "clip-path: inset(50%);") {
		t.Errorf("the radio must be CLIPPED so it stays focusable and announced. Got:\n%s", radio)
	}
	for _, banned := range []string{"display: none", "visibility: hidden"} {
		if strings.Contains(radio, banned) {
			t.Errorf("the radio must not use %q — that removes it from the tab order AND the "+
				"accessibility tree. Got:\n%s", banned, radio)
		}
	}
	// The focus ring has to land on the visible dot, since the input is a 1px box.
	if !strings.Contains(css, ".cm-mat-radio:focus-visible ~ .cm-mat-dot {") {
		t.Error("focus must be shown on .cm-mat-dot — the input itself is clipped to 1px, " +
			"so without this the control is keyboard-operable but gives no focus indication")
	}

	// The desktop anchor, same mechanism and same reasoning as the nav menu's.
	desktop := cssMediaBlock(t, css, "@media (min-width: 1024px) {\n  /*\n   * The desktop MATURITY panel anchors")
	for _, want := range []string{
		"@supports (anchor-name: --cm-maturity-anchor)",
		"anchor-name: --cm-maturity-anchor;",
		"position-anchor: --cm-maturity-anchor;",
		"top: calc(anchor(bottom) + 0.625rem);",
		// Anchored RIGHT: this control lives in the nav's right-hand group, so a
		// left anchor would push a 20rem panel off the edge of the viewport.
		"right: anchor(right);",
	} {
		if !strings.Contains(desktop, want) {
			t.Errorf("the desktop anchor is missing %q. A top-layer box's containing block is the "+
				"VIEWPORT, so `top: calc(100%% + …)` resolves against the viewport height and drops "+
				"the panel below the fold — anchor positioning is the only way to say "+
				"\"under that button\". Got:\n%s", want, desktop)
		}
	}
	if strings.Contains(desktop, "position: absolute;") {
		t.Errorf("the desktop panel must not use `position: absolute` — a top-layer box resolves "+
			"it against the VIEWPORT. Got:\n%s", desktop)
	}

	// 🔴 The panel spends NOTHING from the z-index budget: the top layer paints
	// above every stacking context, so a z-index here would be inert AND would
	// misdescribe the STACKING ORDER ledger.
	if strings.Contains(base, "z-index") || strings.Contains(open, "z-index") || strings.Contains(desktop, "z-index") {
		t.Error("the maturity panel must declare NO z-index — it is in the top layer, so a value " +
			"would be inert and would misdescribe the STACKING ORDER ledger in app.css")
	}
}

// TestNavbarRendersTheMaturityControl proves every page's navbar carries it —
// and that the dead NSFW toggle is gone.
func TestNavbarRendersTheMaturityControl(t *testing.T) {
	out := renderString(t, navbar("csrf-tok", fullMaturityRange(), railData{}))
	if !strings.Contains(out, `hx-post="/settings/maturity"`) {
		t.Errorf("navbar should render the maturity control:\n%s", out)
	}
	if !strings.Contains(out, `popovertarget="`+maturityMenuPanelID+`"`) {
		t.Errorf("navbar should render the maturity control's popover trigger:\n%s", out)
	}
	for _, dead := range []string{"NSFW: Blur", "NSFW: Show", `hx-post="/settings/nsfw"`} {
		if strings.Contains(out, dead) {
			t.Errorf("navbar still carries the removed NSFW toggle (%q):\n%s", dead, out)
		}
	}
}

// TestModelPageHasExactlyOneMaturityControl proves the setting lives in ONE place
// (the navbar) — the old per-model-page NSFW control is gone and was not
// reintroduced per surface.
func TestModelPageHasExactlyOneMaturityControl(t *testing.T) {
	srv := newModelServer(t, newModelReader(t))
	body := getModelPage(t, srv, "/models/7")

	if strings.Contains(body, `"model_id"`) {
		t.Error("model header must not render a model_id-scoped maturity control")
	}
	if n := strings.Count(body, `hx-post="/settings/maturity"`); n != 1 {
		t.Errorf("expected exactly one maturity control (the navbar one), got %d", n)
	}
	if strings.Contains(body, `hx-post="/settings/nsfw"`) {
		t.Error("the removed /settings/nsfw endpoint is still referenced")
	}
}
