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
// Tab, moved between stops with the arrow keys, and needs no key handling of our
// own. The trigger is a real <button>, activated by Enter/Space, and the panel is
// a `popover` in its default `auto` state — which is where light-dismiss and
// Escape come from. Whether those arrow keys COMMIT is a separate property, and
// they no longer do: see TestMaturityControlCommitsOnlyOnSubmit.
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
	// Submitting the form sends the whole band (both values + the token).
	if !strings.Contains(out, `hx-post="/settings/maturity"`) {
		t.Errorf("the control must POST /settings/maturity:\n%s", out)
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

	// 🔴 THE REACHABLE ONE. The two POSTs above are hand-made; this pair is what a
	// user can actually stage in the shipped panel, and it is why the client gate
	// exists at all. From the SAVED FULL band both tracks render all five stops
	// (nothing is out of range), so min=x can be staged and then max=r — and Apply
	// POSTs exactly this. The server must keep refusing it: with hx-swap="none" and
	// no htmx:responseError handler anywhere in the app, a 400 here is INVISIBLE,
	// which is precisely the dead-Apply-button regression. The client gate stops the
	// POST from being sent; this assertion is what stops the band from landing if the
	// gate is ever bypassed, defeated, or simply switched off with JS.
	if rec := post("min=pg&max=xxx&csrf_token=" + srv.csrf); rec.Code != http.StatusNoContent {
		t.Fatalf("fixture: widening to the full band must persist, got %d", rec.Code)
	}
	if got := srv.maturity(); got.String() != "pg:xxx" {
		t.Fatalf("fixture did not reach the FULL band (where every stop is selectable "+
			"and the inverted pair below is reachable): stored range = %q", got.String())
	}
	if rec := post("min=x&max=r&csrf_token=" + srv.csrf); rec.Code != http.StatusBadRequest {
		t.Errorf("the pair a user can actually stage from the full band (min=x, max=r) "+
			"must be rejected with 400, got %d", rec.Code)
	}
	if got := srv.maturity(); got.String() != "pg:xxx" {
		t.Errorf("staging min=x then max=r and pressing Apply changed the stored range to %q — "+
			"it must persist NOTHING", got.String())
	}
	// Back to the narrow band the remaining assertions are written against.
	if rec := post("min=pg13&max=r&csrf_token=" + srv.csrf); rec.Code != http.StatusNoContent {
		t.Fatalf("fixture: restoring pg13:r must persist, got %d", rec.Code)
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

// matForm slices out the maturity control's <form> — its OPENING TAG and its full
// extent — by the stable id the markup carries.
//
// 🔴 It returns the open tag SEPARATELY on purpose. "Does the control commit on
// change?" is a property of the FORM's own attributes, and a bare
// strings.Contains(out, `hx-trigger="change"`) over the whole control would be
// answerable by any other element that ever grows one — the same shape as the
// vacuous ` popover` substring check documented above, which the trigger's
// `popovertarget` satisfied.
func matForm(t *testing.T, out string) (open, extent string) {
	t.Helper()
	idx := strings.Index(out, `<form id="`+maturityFormID+`"`)
	if idx < 0 {
		t.Fatalf("no <form id=%q> in the maturity control:\n%s", maturityFormID, out)
	}
	rest := out[idx:]
	gt := strings.Index(rest, ">")
	if gt < 0 {
		t.Fatalf("the maturity <form> tag is unterminated:\n%s", out)
	}
	end := strings.Index(rest, "</form>")
	if end < 0 {
		t.Fatalf("the maturity <form> is not closed:\n%s", out)
	}
	return rest[:gt+1], rest[:end]
}

// TestMaturityControlCommitsOnlyOnSubmit is the regression guard for the bug this
// change fixes.
//
// The form used to carry `hx-trigger="change"`, so a radio group — whose arrow
// keys fire `change` on EVERY step — persisted a band and got back `HX-Refresh`
// on every keypress. Lowering the ceiling from XXX to PG was four saves and four
// full page reloads, each one closing the popover and dumping focus to <body>,
// and each intermediate band was written to the DB. The tracks must therefore
// only STAGE a selection; the form commits on `submit` alone (htmx's natural
// trigger for a <form>, which is why no hx-trigger is the correct spelling rather
// than hx-trigger="submit").
//
// Both halves are asserted on the FORM ELEMENT'S OWN OPEN TAG: the absence of a
// change/keyup/input trigger, AND the presence of the submit path that replaces
// it. Absence alone would be satisfied by a form that cannot commit at all.
func TestMaturityControlCommitsOnlyOnSubmit(t *testing.T) {
	out := renderString(t, maturityControl(fullMaturityRange(), "csrf-tok"))
	open, extent := matForm(t, out)

	// Sanity: the slice really is the maturity form, not something that merely
	// contains the id. Without this the assertions below could be true of an empty
	// or wrongly-located string.
	if !strings.Contains(open, `hx-post="/settings/maturity"`) {
		t.Fatalf("the sliced form is not the maturity setter — fixture did not reach the "+
			"element under guard:\n%s", open)
	}
	if !strings.Contains(extent, `id="`+maturityControlMinID+`"`) ||
		!strings.Contains(extent, `id="`+maturityControlMaxID+`"`) {
		t.Fatalf("the sliced form does not contain both range tracks, so it is not the "+
			"element that commits the band:\n%s", extent)
	}

	// 🔴 NO auto-commit trigger. `change` is the one that shipped the bug; keyup and
	// input are the same mistake spelled differently for a radio group.
	if m := regexp.MustCompile(`hx-trigger="([^"]*)"`).FindStringSubmatch(open); m != nil {
		for _, banned := range []string{"change", "keyup", "input"} {
			if strings.Contains(m[1], banned) {
				t.Errorf("the maturity form carries hx-trigger=%q. A radio track fires %q on every "+
					"arrow keypress, so the band is persisted and the page reloaded mid-adjustment — "+
					"the popover closes and focus is dumped to <body>. The tracks must only stage; "+
					"only Apply may commit.", m[1], banned)
			}
		}
	}

	// The submit path that replaces it: a real type=submit INSIDE this form.
	if !strings.Contains(extent, `id="`+maturityApplyID+`" type="submit"`) &&
		!strings.Contains(extent, `type="submit" id="`+maturityApplyID+`"`) {
		t.Errorf("the maturity form has no <button type=submit id=%q> — with the change "+
			"trigger gone and no submit control, the band could never be saved at all:\n%s",
			maturityApplyID, extent)
	}
}

// TestMaturityControlHasAnApplyButton covers the control the user actually
// reaches for: it must be a real, keyboard-reachable button that SUBMITS THIS
// FORM, and it must have a clear accessible name.
//
// The name is asserted as a prefix relationship, not equality: WCAG 2.5.3 (Label
// in Name) wants the visible text to appear at the START of the accessible name,
// so speech input ("click Apply") still matches.
func TestMaturityControlHasAnApplyButton(t *testing.T) {
	out := renderString(t, maturityControl(fullMaturityRange(), "csrf-tok"))
	_, extent := matForm(t, out)

	btnRe := regexp.MustCompile(`<button([^>]*\bid="` + maturityApplyID + `"[^>]*)>([^<]*)</button>`)
	m := btnRe.FindStringSubmatch(extent)
	if m == nil {
		t.Fatalf("no <button id=%q> inside the maturity form:\n%s", maturityApplyID, extent)
	}
	attrs, text := m[1], strings.TrimSpace(m[2])

	if !strings.Contains(attrs, `type="submit"`) {
		t.Errorf("the Apply button must be type=submit — a type=button would render a control "+
			"that looks like the commit and does nothing. Got: <button%s>", attrs)
	}
	if text == "" {
		t.Errorf("the Apply button has no visible label:\n<button%s>%s</button>", attrs, m[2])
	}
	// Keyboard-reachable: not removed from the tab order, and not disabled.
	if strings.Contains(attrs, `tabindex="-1"`) || strings.Contains(attrs, "disabled") {
		t.Errorf("the Apply button must stay keyboard-reachable and enabled. Got: <button%s>", attrs)
	}
	// Accessible name. An aria-label is optional, but if present it must EXTEND the
	// visible text rather than replace it.
	if al := regexp.MustCompile(`aria-label="([^"]*)"`).FindStringSubmatch(attrs); al != nil {
		if !strings.HasPrefix(al[1], text) {
			t.Errorf("the Apply button's accessible name %q does not start with its visible label %q — "+
				"WCAG 2.5.3 (Label in Name): speech input matches what is on screen", al[1], text)
		}
	}
}

// TestMaturityControlDoesNotSaveByClickingRadios is the ONE-SUBMISSION guard, and
// it is deliberately narrow.
//
// Under the old change-trigger, any programmatic preset that set both ends by
// clicking two radios fired TWO POSTs and TWO reloads. Under the submit-only form
// the same preset commits NOTHING. Either way, a click on a radio must not be the
// thing that saves — a preset has to reach the form and call requestSubmit() once.
// This asserts the property structurally: nothing in the panel may carry an inline
// handler that clicks a maturity radio.
//
// ⚠ HONEST LABEL: on this branch the panel contains no preset control at all, so
// this is an INVARIANT guard, not regression coverage — it cannot be made to fail
// by breaking shipped behaviour. It is here because the preset is in flight on
// another branch (`feat/comfy-model-cache`'s "Safe mode", which does exactly the
// forbidden `.click()`), and a clean merge of that branch would silently produce a
// button that saves nothing.
func TestMaturityControlDoesNotSaveByClickingRadios(t *testing.T) {
	out := renderString(t, maturityControl(fullMaturityRange(), "csrf-tok"))
	_, extent := matForm(t, out)

	for _, m := range regexp.MustCompile(`on[a-z]+="([^"]*)"`).FindAllStringSubmatch(extent, -1) {
		if strings.Contains(m[1], ".click()") {
			t.Errorf("an inline handler in the maturity panel calls .click(): %q. Clicking a radio "+
				"no longer saves anything (the form commits on submit only), and under the old "+
				"change-trigger clicking two radios fired two POSTs and two page reloads. A preset "+
				"must set both ends and then call document.getElementById(%q).requestSubmit() ONCE.",
				m[1], maturityFormID)
		}
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
	// ONE control. Counting the panel id is the direct assertion — a second
	// maturityControl anywhere on the page renders a second panel (and a duplicate
	// id, itself a bug). This used to count `hx-post="/settings/maturity"` posters
	// instead, which stopped meaning "one control" the moment the control grew a
	// second poster; the property it was reaching for is pinned here directly.
	if n := strings.Count(body, `id="`+maturityMenuPanelID+`"`); n != 1 {
		t.Errorf("expected exactly one maturity control panel (the navbar one), got %d", n)
	}
	// EXACTLY TWO things POST /settings/maturity, and both belong to that one
	// control: the Apply form (staged band) and the Safe-mode preset (literal band).
	// A per-surface control would push this to 4.
	if n := strings.Count(body, `hx-post="/settings/maturity"`); n != 2 {
		t.Errorf("expected exactly 2 posters of /settings/maturity (the Apply form and "+
			"the Safe-mode preset, both inside the single navbar control), got %d", n)
	}
	if n := strings.Count(body, `id="`+maturityFormID+`"`); n != 1 {
		t.Errorf("expected exactly one maturity Apply form, got %d", n)
	}
	if n := strings.Count(body, `id="`+maturitySafeModeID+`"`); n != 1 {
		t.Errorf("expected exactly one Safe-mode preset button, got %d", n)
	}
	if strings.Contains(body, `hx-post="/settings/nsfw"`) {
		t.Error("the removed /settings/nsfw endpoint is still referenced")
	}
}

// matPanelOpenTag returns the maturity popover PANEL's own open tag.
//
// Scoped deliberately: a bare strings.Contains for `ontoggle` over the whole
// control could be satisfied by any other element that ever grows one, which is the
// documented ` popover`-substring vacuity shape. The panel is the element that has
// to carry it — the toggle event fires on the popover, not on the form.
func matPanelOpenTag(t *testing.T, out string) string {
	t.Helper()
	idx := strings.Index(out, `<div id="`+maturityMenuPanelID+`"`)
	if idx < 0 {
		t.Fatalf("no <div id=%q> panel in the maturity control:\n%s", maturityMenuPanelID, out)
	}
	rest := out[idx:]
	gt := strings.Index(rest, ">")
	if gt < 0 {
		t.Fatalf("the maturity panel tag is unterminated:\n%s", out)
	}
	return rest[:gt+1]
}

// TestMaturityPanelDiscardsAStagedBandOnClose guards the fail-OPEN path that the
// staging change introduced.
//
// Because the tracks now stage instead of committing, a selection the user never
// applied survives in the DOM. Dismiss the panel (Escape / light-dismiss), reopen
// it later, press Apply — and the abandoned selection commits, persisting a band
// nobody chose, and a WIDER one.
//
// MEASURED end-to-end on two builds against a throwaway DB: saved `pg:pg13` →
// stage max=XXX → Escape → reopen → Apply → the pre-fix binary persisted `pg:xxx`
// (the FULL range), the fixed binary persisted `pg:pg13`. Same class as the
// v0.1.98 arrow-key wrap, so it must not regress.
//
// ⚠ An earlier version of this comment ended that sequence with "reopen, lower the
// min → R to XXX". That is unreachable and was never run: at saved `pg:pg13` the
// FROM track emits inputs for `pg`/`pg13` only, so `min=R` cannot be selected in
// that session. The hazard is real from a wider saved band; the example was wrong.
//
// The panel resets its form when the popover closes; form.reset() restores each
// radio to its server-rendered `checked` ATTRIBUTE, i.e. the saved band.
func TestMaturityPanelDiscardsAStagedBandOnClose(t *testing.T) {
	out := renderString(t, maturityControl(fullMaturityRange(), "csrf-tok"))
	panel := matPanelOpenTag(t, out)

	if !strings.Contains(panel, "ontoggle=") {
		t.Fatalf("the maturity panel carries no ontoggle handler, so a staged-but-unapplied "+
			"band survives Escape and can be committed later by an Apply meant for the other "+
			"end of the range (a content gate failing OPEN).\npanel tag: %s", panel)
	}
	// It must react to the CLOSE transition specifically — resetting on open would
	// be harmless but useless, and resetting unconditionally would wipe a selection
	// mid-interaction.
	if !strings.Contains(panel, "newState") || !strings.Contains(panel, "closed") {
		t.Errorf("the panel's ontoggle does not key on the CLOSED transition; it must only "+
			"discard staged state when the panel closes.\npanel tag: %s", panel)
	}
	// It must be a reset, not a click/submit: .click()ing radios saves nothing now,
	// and a submit would COMMIT the staged band — the exact opposite of the intent.
	if !strings.Contains(panel, "reset()") {
		t.Errorf("the panel's ontoggle does not call reset(); only form.reset() restores the "+
			"server-rendered checked attributes (the SAVED band).\npanel tag: %s", panel)
	}
	// …and it must reset the FORM, not `this`. `this` is the panel <div>, which has no
	// reset(): the handler would throw, discard nothing, and leave the staged band in
	// place — the exact bug, with a green test. Asserting only the string "reset()"
	// missed that mutation (it passed the whole suite), so the target is asserted too.
	if !strings.Contains(panel, "querySelector('form')") &&
		!strings.Contains(panel, "querySelector(&#39;form&#39;)") {
		t.Errorf("the panel's ontoggle does not reset the FORM — `this` is the panel <div>, "+
			"which has no reset(), so the handler would throw and discard nothing.\npanel tag: %s", panel)
	}
	if strings.Contains(panel, "submit") || strings.Contains(panel, ".click(") {
		t.Errorf("the panel's ontoggle must not submit or click — closing the panel must never "+
			"COMMIT a band the user did not apply.\npanel tag: %s", panel)
	}
}

// TestMaturityPanelLabelsTheSavedBand guards the copy that makes staging legible.
//
// While the panel is open the radios and this line can legitimately disagree: the
// radios show what Apply WOULD commit, the line shows what is actually in force.
// Unlabelled, that reads as a contradiction; it is also the only on-screen cue that
// a staged change has not been applied.
func TestMaturityPanelLabelsTheSavedBand(t *testing.T) {
	mr := maturityRange{Min: maturityPG, Max: maturityR}
	out := renderString(t, maturityControl(mr, "csrf-tok"))

	idx := strings.Index(out, `class="cm-maturity-current"`)
	if idx < 0 {
		t.Fatalf("no current-band line in the maturity panel:\n%s", out)
	}
	rest := out[idx:]
	end := strings.Index(rest, "</span>")
	if end < 0 {
		t.Fatalf("the current-band span is not closed:\n%s", out)
	}
	line := rest[:end]

	// Fixture reaches the interesting case: this is a real, non-full band.
	if !strings.Contains(line, mr.label()) {
		t.Fatalf("the current-band line does not render the band %q at all: %s", mr.label(), line)
	}
	if !strings.Contains(line, "Saved") {
		t.Errorf("the current-band line does not say the band is SAVED: %s\n"+
			"with staging, an unlabelled band reads as the current SELECTION and contradicts "+
			"the radios whenever a change is staged", line)
	}
}
