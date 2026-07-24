package web

import (
	"strings"
	"testing"
)

// TestNSFWToggleCyclesModes proves the navbar's 3-state NSFW control shows the
// CURRENT mode as its label and POSTs the correct NEXT mode in the cycle
// Hide → Blur → Show → Hide (mirroring the theme-toggle idiom).
func TestNSFWToggleCyclesModes(t *testing.T) {
	cases := []struct {
		mode      string
		wantLabel string
		wantNext  string
	}{
		{NSFWHide, "NSFW: Hide", NSFWBlur},
		{NSFWBlur, "NSFW: Blur", NSFWShow},
		{NSFWShow, "NSFW: Show", NSFWHide},
	}
	for _, tc := range cases {
		t.Run(tc.mode, func(t *testing.T) {
			out := renderString(t, nsfwToggle(tc.mode, "csrf-tok"))
			if !strings.Contains(out, tc.wantLabel) {
				t.Errorf("toggle for %q should show label %q:\n%s", tc.mode, tc.wantLabel, out)
			}
			if !strings.Contains(out, `hx-post="/settings/nsfw"`) {
				t.Errorf("toggle must POST /settings/nsfw:\n%s", out)
			}
			// hx-vals is an HTML attribute, so gomponents escapes the JSON quotes.
			if !strings.Contains(out, `&#34;mode&#34;:&#34;`+tc.wantNext+`&#34;`) {
				t.Errorf("toggle for %q must post NEXT mode %q:\n%s", tc.mode, tc.wantNext, out)
			}
			if !strings.Contains(out, `&#34;csrf_token&#34;:&#34;csrf-tok&#34;`) {
				t.Errorf("toggle must carry the CSRF token:\n%s", out)
			}
		})
	}
}

// TestNavbarRendersNSFWToggle proves every page's navbar carries the NSFW toggle
// alongside the theme toggle.
func TestNavbarRendersNSFWToggle(t *testing.T) {
	out := renderString(t, navbar("dark", "csrf-tok", NSFWBlur))
	if !strings.Contains(out, "NSFW: Blur") {
		t.Errorf("navbar should render the NSFW toggle:\n%s", out)
	}
}

// TestModelHeaderHasNoPerPageNSFWControl proves the old per-model-page NSFW
// control was removed from the header: the model page no longer posts a
// model_id-scoped nsfw form (the navbar toggle replaced it), and there is
// exactly one NSFW control on the page (the navbar one).
func TestModelHeaderHasNoPerPageNSFWControl(t *testing.T) {
	srv := newModelServer(t, newModelReader(t))
	body := getModelPage(t, srv, "/models/7")

	// The old per-page control carried the model_id in its hx-vals; the navbar
	// toggle does not.
	if strings.Contains(body, `"model_id"`) {
		t.Error("model header must not render the old per-page model_id-scoped NSFW control")
	}
	// Exactly one NSFW control (the navbar toggle) remains.
	if n := strings.Count(body, `hx-post="/settings/nsfw"`); n != 1 {
		t.Errorf("expected exactly one NSFW control (navbar toggle), got %d", n)
	}
}
