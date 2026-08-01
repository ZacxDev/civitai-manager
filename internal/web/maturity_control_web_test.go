package web

import (
	"regexp"
	"strings"
	"testing"
)

// The nav's maturity RANGE control replaced the old 2-state NSFW blur⇄show
// button. These tests cover the three things a hand-rolled range control can get
// wrong: it must be operable without a mouse, both ends must be NAMED for
// assistive tech, and it must not be able to submit an inverted band.

// TestMaturityControlIsKeyboardOperable proves the control is built from NATIVE
// form elements, which is the whole keyboard story: a <select> is reachable with
// Tab, changed with the arrow keys / Home / End / type-ahead, and committed with
// Enter — none of which we implement or can break.
//
// The negative half matters as much: a <div role=slider> or a click-only widget
// would pass a "does the markup contain the levels" test and be unusable.
func TestMaturityControlIsKeyboardOperable(t *testing.T) {
	out := renderString(t, maturityControl(fullMaturityRange(), "csrf-tok"))

	if n := strings.Count(out, "<select"); n != 2 {
		t.Fatalf("the control should be exactly two native <select>s (one per end), got %d:\n%s", n, out)
	}
	// No custom key handling, no non-native widget standing in for a control.
	for _, banned := range []string{
		"onkeydown", "onkeyup", "onkeypress", `role="slider"`, "tabindex=", "<div onclick",
	} {
		if strings.Contains(out, banned) {
			t.Errorf("the control uses %q — native elements must carry the keyboard story:\n%s", banned, out)
		}
	}
	// Nothing may be focus-trapped out of the tab order.
	if strings.Contains(out, `tabindex="-1"`) {
		t.Error("an end of the range must not be removed from the tab order")
	}
	// Changing either end submits the whole form (both values + the token).
	if !strings.Contains(out, `hx-post="/settings/maturity"`) {
		t.Errorf("the control must POST /settings/maturity:\n%s", out)
	}
	if !strings.Contains(out, `hx-trigger="change"`) {
		t.Errorf("the control must submit on change (keyboard changes fire it too):\n%s", out)
	}
}

// TestMaturityControlBothEndsHaveAccessibleNames proves EACH end carries its own
// bound <label>. One ambiguous "Maturity" for two selects would leave a screen
// reader user unable to tell which end they are on.
func TestMaturityControlBothEndsHaveAccessibleNames(t *testing.T) {
	out := renderString(t, maturityControl(fullMaturityRange(), "csrf-tok"))

	for _, want := range []struct{ id, label string }{
		{maturityControlMinID, "Maturity from"},
		{maturityControlMaxID, "Maturity to"},
	} {
		lbl := regexp.MustCompile(`<label[^>]*for="` + regexp.QuoteMeta(want.id) + `"[^>]*>([^<]*)</label>`)
		m := lbl.FindStringSubmatch(out)
		if m == nil {
			t.Errorf("no <label for=%q> — that end has no accessible name:\n%s", want.id, out)
			continue
		}
		if strings.TrimSpace(m[1]) != want.label {
			t.Errorf("label for %q = %q, want %q", want.id, m[1], want.label)
		}
		if !strings.Contains(out, `id="`+want.id+`"`) {
			t.Errorf("no element carries id=%q, so the label binds to nothing", want.id)
		}
	}
	// The visible legend is decoration only — it must not be announced twice.
	if !strings.Contains(out, `aria-hidden="true"`) {
		t.Error("the visible \"Maturity\" legend should be aria-hidden (the labels name the ends)")
	}
	// .cm-sr-only hides the labels visually while KEEPING them in the a11y tree;
	// display:none / hidden would remove them.
	if !strings.Contains(out, `class="cm-sr-only"`) {
		t.Error("the labels should be visually hidden via .cm-sr-only, not removed")
	}
	if strings.Contains(out, `<label hidden`) || strings.Contains(out, "display:none") {
		t.Error("a label must not be removed from the accessibility tree")
	}
}

// TestMaturityControlCannotEmitAnInvertedRange is the structural half of the
// inversion guard: each end offers ONLY the levels that keep the band valid, so
// no single change from a valid state can produce min > max. (The handler
// rejects one anyway — see TestMaturitySettingPersistsViaEndpoint — because
// markup only constrains a browser.)
func TestMaturityControlCannotEmitAnInvertedRange(t *testing.T) {
	optRe := regexp.MustCompile(`<option value="([a-z0-9]+)"`)

	for _, lo := range maturityScale {
		for _, hi := range maturityScale {
			if lo > hi {
				continue
			}
			mr := maturityRange{Min: lo, Max: hi}
			out := renderString(t, maturityControl(mr, "csrf-tok"))

			// Split the markup at the second <select> so each end's options are read
			// separately.
			parts := strings.SplitN(out, "<select", 3)
			if len(parts) != 3 {
				t.Fatalf("%s: expected two selects:\n%s", mr.String(), out)
			}
			minOpts := collect(optRe, parts[1])
			maxOpts := collect(optRe, parts[2])

			if len(minOpts) == 0 || len(maxOpts) == 0 {
				t.Fatalf("%s: an end offered no options at all", mr.String())
			}
			for _, o := range minOpts {
				l, ok := maturityFromSlug(o)
				if !ok {
					t.Fatalf("%s: the FROM end offered an unknown level %q", mr.String(), o)
				}
				if !(maturityRange{Min: l, Max: mr.Max}).valid() {
					t.Errorf("%s: choosing FROM=%s would invert the range", mr.String(), o)
				}
			}
			for _, o := range maxOpts {
				l, ok := maturityFromSlug(o)
				if !ok {
					t.Fatalf("%s: the TO end offered an unknown level %q", mr.String(), o)
				}
				if !(maturityRange{Min: mr.Min, Max: l}).valid() {
					t.Errorf("%s: choosing TO=%s would invert the range", mr.String(), o)
				}
			}
			// The current value of each end must be selected, or a change to the OTHER
			// end would silently submit a different band.
			if !strings.Contains(parts[1], `<option value="`+lo.slug()+`" selected`) {
				t.Errorf("%s: the FROM end does not preselect %s:\n%s", mr.String(), lo.slug(), parts[1])
			}
			if !strings.Contains(parts[2], `<option value="`+hi.slug()+`" selected`) {
				t.Errorf("%s: the TO end does not preselect %s:\n%s", mr.String(), hi.slug(), parts[2])
			}
		}
	}
}

func collect(re *regexp.Regexp, s string) []string {
	var out []string
	for _, m := range re.FindAllStringSubmatch(s, -1) {
		out = append(out, m[1])
	}
	return out
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
// render the full range rather than an empty or inverted control.
func TestMaturityControlNormalizesAnInvalidStoredRange(t *testing.T) {
	out := renderString(t, maturityControl(maturityRange{Min: maturityXXX, Max: maturityPG}, "csrf-tok"))
	if !strings.Contains(out, `<option value="pg" selected`) || !strings.Contains(out, `<option value="xxx" selected`) {
		t.Errorf("an invalid stored range should render as PG..XXX:\n%s", out)
	}
}

// TestNavbarRendersTheMaturityControl proves every page's navbar carries it,
// alongside the theme toggle — and that the dead NSFW toggle is gone.
func TestNavbarRendersTheMaturityControl(t *testing.T) {
	out := renderString(t, navbar("csrf-tok", fullMaturityRange(), railData{}))
	if !strings.Contains(out, `hx-post="/settings/maturity"`) {
		t.Errorf("navbar should render the maturity control:\n%s", out)
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
