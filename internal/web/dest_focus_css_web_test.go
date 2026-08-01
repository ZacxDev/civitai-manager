package web

import (
	"os"
	"strings"
	"testing"
)

// TestDestinationFocusRingIsPerRadio guards a copy-paste slip that no markup
// assertion and no contrast check can see.
//
// THE BUG. The destination control is two native radios (#cm-dest-local /
// #cm-dest-cloud) with sibling <label> tabs. The :checked rules correctly key each
// arm on its OWN radio id — but the focus-visible rule below them keyed BOTH arms on
// the shared .cm-dest-radio class, so focusing EITHER radio outlined BOTH tabs.
// Measured live in Brave with the local radio focused:
//
//	{"active":"cm-dest-local","radioMatchesFocus":true,"cloudTabOutlinedByLocalFocus":true}
//
// Why it matters enough to guard: the outline is the ONLY signal a keyboard user
// gets about which destination they are on — one of which spends money. A ring on
// both options is worse than no ring, because it looks authoritative and says
// nothing. The rule is invisible to every other checker: the classes are present
// either way, the tokens are unchanged, and both themes render "an outline".
//
// It asserts the SHAPE (each arm scoped to one id), not the exact declarations, so
// restyling the ring is free and re-broadening its selector is not.
func TestDestinationFocusRingIsPerRadio(t *testing.T) {
	raw, err := os.ReadFile("assets/app.css")
	if err != nil {
		t.Fatalf("read app.css: %v", err)
	}
	css := string(raw)

	// cssRuleIn fails the test when the selector is absent, so a renamed rule reports
	// itself rather than passing vacuously.
	rule := cssRuleIn(t, css, "#cm-dest-local:focus-visible")

	for _, want := range []string{
		`#cm-dest-local:focus-visible ~ .cm-dest-tabs .cm-dest-tab[for="cm-dest-local"]`,
		`#cm-dest-cloud:focus-visible ~ .cm-dest-tabs .cm-dest-tab[for="cm-dest-cloud"]`,
	} {
		if !strings.Contains(rule, want) {
			t.Errorf("the destination focus ring must scope each arm to its OWN radio id.\n"+
				"missing: %s\ngot:\n%s", want, rule)
		}
	}
	// The specific regression: the shared class outlines both tabs from either radio.
	if strings.Contains(rule, ".cm-dest-radio:focus-visible") {
		t.Errorf("the focus rule keys on the shared .cm-dest-radio class, so focusing ONE "+
			"radio outlines BOTH tabs — the ring then cannot say which destination is "+
			"selected, and one of them spends Buzz. Got:\n%s", rule)
	}
}
