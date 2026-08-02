package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ─────────────────────────────────────────────────────────────────────────────
// SAFE MODE — the one-click PG..PG-13 preset.
// ─────────────────────────────────────────────────────────────────────────────
// Two shipped-and-wrong versions are what these tests exist to keep out:
//
//  1. A `javascript:void(function(){…})()` onclick. The prefix is a functional
//     no-op, but it is a literal `javascript:` on every page (the control is in
//     the nav), and it tripped the two site-wide XSS canaries.
//  2. Driving the radios and submitting the form. That fails OPEN: maturityTrack
//     emits NO <input> for a stop outside the SAVED band, so from a saved band of
//     R..XXX the min "pg" radio exists but the max "pg13" radio does not, and the
//     POST carries min=pg&max=xxx — a Safe-mode button that persists the FULL
//     range.
//
// The guards below pin the fix from three independent angles: the PAYLOAD is the
// literal band at every saved band, the button carries no script, and it lives
// outside the form so htmx cannot serialise the staged radios with it.

// matSafeTag returns the Safe-mode button's OPENING TAG — its attributes only,
// none of its children's. Naming the element matters: a bare Contains over the
// whole control would be satisfied by the Apply button or the gate script.
func matSafeTag(t *testing.T, out string) string {
	t.Helper()
	i := strings.Index(out, `id="`+maturitySafeModeID+`"`)
	if i < 0 {
		t.Fatalf("the maturity control renders no Safe-mode button (id=%q):\n%s", maturitySafeModeID, out)
	}
	start := strings.LastIndex(out[:i], "<")
	end := strings.Index(out[i:], ">")
	if start < 0 || end < 0 {
		t.Fatalf("could not slice the Safe-mode button's opening tag out of:\n%s", out)
	}
	return out[start : i+end+1]
}

// TestSafeModeSendsTheLiteralBandFromEverySavedBand is the direct answer to
// failure mode 2. It walks ALL 15 valid saved bands, because the bug only appears
// at bands where the tracks omit one of the two stops the preset needs — a test
// run only at the default PG..XXX band would have passed against the broken code.
func TestSafeModeSendsTheLiteralBandFromEverySavedBand(t *testing.T) {
	// The exact attribute the button must carry, with gomponents' entity escaping.
	// A LITERAL expected value, not one derived from the code under test.
	const wantVals = `hx-vals="{&#34;min&#34;:&#34;pg&#34;,&#34;max&#34;:&#34;pg13&#34;,&#34;csrf_token&#34;:&#34;tok-123&#34;}"`

	bands := 0
	for _, lo := range maturityScale {
		for _, hi := range maturityScale {
			if lo > hi {
				continue
			}
			bands++
			mr := maturityRange{Min: lo, Max: hi}
			out := renderString(t, maturityControl(mr, "tok-123"))
			tag := matSafeTag(t, out)

			if !strings.Contains(tag, wantVals) {
				t.Errorf("saved band %s: Safe mode must POST the LITERAL pg..pg13 band.\n"+
					"want attribute: %s\ngot tag: %s", mr.String(), wantVals, tag)
			}
			if !strings.Contains(tag, `hx-post="/settings/maturity"`) {
				t.Errorf("saved band %s: Safe mode must POST the existing maturity endpoint:\n%s",
					mr.String(), tag)
			}
			// hx-swap=none: the handler answers 204 + HX-Refresh, so a swap would
			// blank whatever it targeted.
			if !strings.Contains(tag, `hx-swap="none"`) {
				t.Errorf("saved band %s: Safe mode must not swap a response:\n%s", mr.String(), tag)
			}
			// type=button — it sits in a nav full of forms, and the HTML default is
			// submit.
			if !strings.Contains(tag, `type="button"`) {
				t.Errorf("saved band %s: Safe mode must be type=button:\n%s", mr.String(), tag)
			}
		}
	}
	if bands != 15 {
		t.Fatalf("fixture did not cover the whole scale: walked %d bands, want 15", bands)
	}
}

// TestSafeModeCarriesNoScriptAndNoJavascriptURL is the direct answer to failure
// mode 1. It is scoped to the maturity control's own render rather than a page,
// so it cannot be satisfied — or broken — by some other surface's markup.
func TestSafeModeCarriesNoScriptAndNoJavascriptURL(t *testing.T) {
	out := renderString(t, maturityControl(fullMaturityRange(), "tok"))

	// The control DOES ship one <script> (the Apply gate). The banned thing is the
	// literal scheme, anywhere in the control.
	if strings.Contains(out, "javascript:") {
		t.Errorf("the maturity control must contain no `javascript:` anywhere — it is in the "+
			"nav, so this lands on EVERY page and trips the site-wide XSS canaries "+
			"(TestAppsXSSPlayURLRejected, TestModelDescriptionSanitized):\n%s", out)
	}

	tag := matSafeTag(t, out)
	for _, banned := range []string{"onclick=", "onmousedown=", "onpointerdown=", "href="} {
		if strings.Contains(tag, banned) {
			t.Errorf("the Safe-mode button must be a plain htmx button — no %q:\n%s", banned, tag)
		}
	}
}

// TestSafeModeButtonIsOutsideTheApplyForm pins the placement that makes the
// payload deterministic.
//
// htmx serialises the CLOSEST ENCLOSING FORM for a non-GET request issued by a
// button. Inside the form, the staged radios would ride along with hx-vals and the
// result would depend on merge precedence; outside it, closest('form') is null and
// the body is exactly the three hx-vals keys. This package has no JS engine, so
// the guard is STRUCTURAL: it asserts the button sits after the form closes.
func TestSafeModeButtonIsOutsideTheApplyForm(t *testing.T) {
	out := renderString(t, maturityControl(fullMaturityRange(), "tok"))

	formStart := strings.Index(out, `<form id="`+maturityFormID+`"`)
	if formStart < 0 {
		t.Fatalf("no maturity form in:\n%s", out)
	}
	formEnd := strings.Index(out[formStart:], "</form>")
	if formEnd < 0 {
		t.Fatalf("the maturity form is never closed:\n%s", out)
	}
	formEnd += formStart + len("</form>")

	safeAt := strings.Index(out, `id="`+maturitySafeModeID+`"`)
	if safeAt < 0 {
		t.Fatalf("no Safe-mode button in:\n%s", out)
	}
	if safeAt < formEnd {
		t.Errorf("the Safe-mode button is INSIDE the Apply form (at %d, form closes at %d). "+
			"htmx would then serialise the staged radios into its POST alongside "+
			"hx-vals, so the band it sends would depend on the saved band — the exact "+
			"fail-OPEN bug this placement removes:\n%s", safeAt, formEnd, out)
	}
	// …and still inside the popover panel, or it would not be reachable at all.
	panelAt := strings.Index(out, `id="`+maturityMenuPanelID+`"`)
	if panelAt < 0 || safeAt < panelAt {
		t.Errorf("the Safe-mode button must live inside the maturity panel:\n%s", out)
	}
}

// TestSafeModePayloadNarrowsAWideSavedBand closes the loop on the SERVER. The
// markup guards above prove what the button sends; this proves that what it sends
// does the right thing, from the band the broken version failed on.
//
// R..XXX is chosen deliberately: it is the measured case where "set the radios and
// submit" persisted PG..XXX.
func TestSafeModePayloadNarrowsAWideSavedBand(t *testing.T) {
	// A file-backed store — store.Open(":memory:") is cache=shared across every
	// in-memory server in this package, so "did this write land" would be answering
	// about another test's rows.
	srv := newFileBackedServer(t)

	post := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/settings/maturity", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		srv.Handler().ServeHTTP(rec, req)
		return rec
	}

	// Reach the interesting state through the legitimate path, and ASSERT it — a
	// preset that narrows from the default band would prove nothing.
	if rec := post("min=r&max=xxx&csrf_token=" + srv.csrf); rec.Code != http.StatusNoContent {
		t.Fatalf("seeding the wide band failed: status = %d, want 204", rec.Code)
	}
	if got := srv.maturity(); got.String() != "r:xxx" {
		t.Fatalf("fixture did not reach the interesting state: stored range = %q, want r:xxx", got.String())
	}
	// From THIS saved band, maturityTrack emits no max=pg13 input at all — which is
	// why the radio-driving approach could not express the preset. Assert that,
	// so the fixture is provably at the case the bug needed.
	rendered := renderString(t, maturityControl(srv.maturity(), srv.csrf))
	if strings.Contains(rendered, `id="`+maturityControlMaxID+`-pg13"`) {
		t.Fatalf("precondition broken: at a saved band of r:xxx the max track must NOT emit a "+
			"pg13 radio — the whole point of the preset bypassing the radios:\n%s", rendered)
	}

	// Exactly the body the button's hx-vals encodes.
	if rec := post("min=pg&max=pg13&csrf_token=" + srv.csrf); rec.Code != http.StatusNoContent {
		t.Fatalf("the Safe-mode payload must persist: status = %d, want 204", rec.Code)
	}
	if got := srv.maturity(); got.String() != "pg:pg13" {
		t.Errorf("Safe mode persisted %q, want \"pg:pg13\". A preset that widens the band is "+
			"the fail-OPEN bug this whole control was reworked for.", got.String())
	}
}
