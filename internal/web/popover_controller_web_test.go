package web

import (
	"strings"
	"testing"
)

// TestPopoverControllerScript proves modelPageScript emits the shared, dependency-
// free popover hover controller the two deeplinked popovers rely on: document-level
// event delegation (mouseover/mouseout — required because the popovers are inserted
// LAZILY via htmx), the .cm-pop-open toggle, the .cm-vstatus/.cm-updated wrapper
// selector, and the ~200ms grace delay.
//
// NOTE: the JS hover/click behavior itself is MARKUP-VERIFIED ONLY here — no browser
// is available in this environment, so the actual DOM hover/timer/navigation is not
// exercised. These assertions prove the controller is shipped and wired to the
// right selectors, not that it behaves at runtime.
func TestPopoverControllerScript(t *testing.T) {
	js := renderString(t, modelPageScript())
	for _, want := range []string{
		"cm-pop-open",                  // the class the controller toggles
		".cm-vstatus, .cm-updated",     // both popover wrappers via delegation
		"addEventListener('mouseover'", // enter (delegated on document)
		"addEventListener('mouseout'",  // leave
		"200",                          // the grace-delay ms
		"relatedTarget",                // treats the popover (child) as part of the region
	} {
		if !strings.Contains(js, want) {
			t.Errorf("popover controller missing %q:\n%s", want, js)
		}
	}
}

// TestPopoverControllerLoadsOnBothPages proves the controller ships on BOTH the
// dashboard and the search page (both host the lazy popovers), via modelPageScript.
func TestPopoverControllerLoadsOnBothPages(t *testing.T) {
	dash := renderString(t, dashboardPage(nil, nil, "csrf", fullMaturityRange()))
	if !strings.Contains(dash, "cm-pop-open") {
		t.Errorf("dashboard should ship the popover controller")
	}
	search := renderString(t, searchPage("", nil, nil, "csrf", fullMaturityRange(), "", "", ""))
	if !strings.Contains(search, "cm-pop-open") {
		t.Errorf("search page should ship the popover controller")
	}
}

// TestPopoverControllerCSSPresent proves the .cm-pop-open class shows both popovers
// (alongside the retained :hover/:focus-within fallbacks) in the served app.css —
// the custom CSS the JS toggle depends on survives the Tailwind purge (.cm-* rules
// are served as-is).
func TestPopoverControllerCSSPresent(t *testing.T) {
	b, err := assetsFS.ReadFile("assets/app.css")
	if err != nil {
		t.Fatal(err)
	}
	css := string(b)
	for _, want := range []string{
		".cm-vstatus.cm-pop-open .cm-vstatus-pop",
		".cm-updated.cm-pop-open .cm-updated-pop",
		".cm-card-images-lazy:empty",
	} {
		if !strings.Contains(css, want) {
			t.Errorf("app.css missing %q", want)
		}
	}
}
