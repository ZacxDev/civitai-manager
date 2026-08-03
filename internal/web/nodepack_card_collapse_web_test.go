package web

import (
	"strings"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/comfy"
)

// detailsExtent returns the balanced [start,end) byte range of the FIRST <details>
// at or after from.
//
// 🔴 It balances <details>/</details> rather than comparing strings.Index positions,
// because a pack card's disclosure can itself sit inside the alternatives disclosure
// — real nesting, not a hypothetical. "marker A appears after <details>" is equally
// true for a marker inside a LATER sibling details, so an index comparison cannot
// tell containment from adjacency. That vacuity mode has shipped in this repo before
// (see divExtent's header).
func detailsExtent(t *testing.T, s string, from int) (start, end int) {
	t.Helper()
	rel := strings.Index(s[from:], "<details")
	if rel < 0 {
		t.Fatalf("no <details> at or after byte %d", from)
	}
	start = from + rel
	depth := 0
	for p := start; p < len(s); {
		open := strings.Index(s[p:], "<details")
		clos := strings.Index(s[p:], "</details>")
		if clos < 0 {
			t.Fatal("unterminated <details>")
		}
		if open >= 0 && open < clos {
			depth++
			p += open + len("<details")
			continue
		}
		depth--
		p += clos + len("</details>")
		if depth == 0 {
			return start, p
		}
	}
	t.Fatal("unbalanced <details>")
	return 0, 0
}

// scopedPack is the measured winning pack from the operator's real failure: it claims
// ONE of the FOUR node types it ships, which is what made it outrank the 93-class
// alternative. The scope line only renders when ClaimedClasses is known and >=
// len(Classes), so a fixture without it silently drops one of the four things this
// test asserts is collapsed.
func scopedPack() comfy.Pack {
	p := installablePack()
	p.ClaimedClasses = 4
	return p
}

// TestWinningPackCardCollapsesItsProvenance is the guard for the second half of the
// "wall of text" report: the pack the user is going to install rendered EIGHT lines
// for one click.
//
// Measured live on workflow 590 before this change — heading, name·version·Best
// match, "Repository: <url>", "Provides: <class>", "This pack provides 4 node types
// in total; 1 of them is what this workflow needs.", the Install button, "Or install
// it by hand:", and the git clone block. #60 collapsed the RUNNER-UP and left the
// winner fully expanded; this applies the same treatment to the one that matters.
//
// The split is: WHICH pack and WHAT ROLE stay visible with the button (that is the
// decision); the evidence for the ranking goes one click away.
func TestWinningPackCardCollapsesItsProvenance(t *testing.T) {
	p := scopedPack()
	card := renderString(t, nodepackCard(rankedPack{Pack: p}, true /* managerPresent */, 7, "tok", "/opt/ComfyUI"))

	// PRECONDITIONS. Without these the assertions below pass on a card that rendered
	// no button, or no scope line, or no disclosure at all — three different ways to
	// be green about nothing.
	if !strings.Contains(card, "Install "+p.Title) {
		t.Fatalf("precondition: want a live Install button on the card:\n%s", card)
	}
	scope := packScopeLine(p)
	if scope == "" {
		t.Fatal("precondition: the fixture must produce a scope line, or one of the " +
			"four collapsed items below is not actually in this card")
	}
	if !strings.Contains(card, "<details") {
		t.Fatalf("precondition: want a disclosure on the card:\n%s", card)
	}

	start, end := detailsExtent(t, card, 0)
	inside := card[start:end]
	outside := card[:start] + card[end:]

	// VISIBLE: which pack this is, and the action.
	for _, want := range []string{p.Title, p.Version, "Install " + p.Title} {
		if !strings.Contains(outside, want) {
			t.Errorf("%q must stay visible — it is what the click decision is made on:\n%s", want, outside)
		}
	}
	// COLLAPSED: the evidence for the ranking, and the by-hand alternative.
	for name, want := range map[string]string{
		"repository line": "Repository: ",
		"provides list":   "Provides: ",
		"scope line":      scope,
		"manual command":  "git clone",
	} {
		if !strings.Contains(inside, want) {
			t.Errorf("the %s must be behind the disclosure, not beside the button:\n%s", name, inside)
		}
		if strings.Contains(outside, want) {
			t.Errorf("the %s is still rendered outside the disclosure:\n%s", name, outside)
		}
	}

	// 🔴 The git clone block keeps its tabindex INSIDE the disclosure. axe reported
	// `scrollable-region-focusable` (serious) on exactly this element twice in #60:
	// overflow-x-auto makes it a scrollable region, and a scrollable region that
	// cannot be focused cannot be scrolled by keyboard at all. Moving it must not
	// have dropped that.
	pre := strings.Index(inside, "<pre")
	if pre < 0 {
		t.Fatalf("no command block inside the disclosure:\n%s", inside)
	}
	if !strings.Contains(inside[pre:], `tabindex="0"`) {
		t.Errorf("the relocated command block lost its tabindex — it is a scrollable "+
			"region and is unreachable by keyboard without it:\n%s", inside)
	}
}

// TestPackCardWithNoInstallButtonKeepsItsCommandVisible is the other half of the same
// rule, and it is the one that would silently destroy a card if the condition were
// written as "is this pack needed" instead of "is there a button".
//
// 🔴 With no Install button the git clone IS the action. Collapsing it leaves a card
// whose entire visible content is a pack name and a disclosure triangle. Both states
// are first-class and common: ComfyUI-Manager absent (no install affordance renders
// at all, by design) and a policy-blocked pack (16% of packs are nightly-only,
// measured — the design's own target workflow needs one).
func TestPackCardWithNoInstallButtonKeepsItsCommandVisible(t *testing.T) {
	cases := []struct {
		name           string
		pack           comfy.Pack
		managerPresent bool
		wantReason     string
	}{
		{"manager absent", scopedPack(), false, ""},
		{"pack not installable", blockedPack(), true, blockedPack().Reason},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			card := renderString(t, nodepackCard(rankedPack{Pack: c.pack}, c.managerPresent, 7, "tok", "/opt/ComfyUI"))

			// PRECONDITION: this really is a no-button card. Otherwise the assertion
			// below is about the collapsed branch and proves nothing.
			if strings.Contains(card, "Install "+c.pack.Title) {
				t.Fatalf("precondition: this state must render NO install button:\n%s", card)
			}
			if !strings.Contains(card, "git clone") {
				t.Fatalf("precondition: want the manual command on the card:\n%s", card)
			}

			// THE ASSERTION: nothing on this card is behind a disclosure.
			if strings.Contains(card, "<details") {
				t.Errorf("a card with no Install button must not collapse anything — the "+
					"manual command is its only action:\n%s", card)
			}
			if c.wantReason != "" && !strings.Contains(card, c.wantReason) {
				t.Errorf("the blocked reason must stay visible; it is what explains the "+
					"absent button:\n%s", card)
			}
		})
	}
}

// TestCollapsingDoesNotReopenTheNeededPredicate: the collapse of a card's PROVENANCE
// is keyed on "is there a button", and the collapse of the CARD ITSELF is keyed on
// rankedPack.needed(). Those are different questions and must not be conflated —
// needed() also drives the button variant and the contest badge, and it was open-coded
// wrongly at all three sites once already (see rankedPack.Sole).
//
// A losing-but-required pack (Contested, not Best, Sole claimant of another class)
// must therefore still get: a FILLED button, the "Also needed" badge, AND the same
// provenance collapse every other installable card gets.
func TestCollapsingDoesNotReopenTheNeededPredicate(t *testing.T) {
	rp := rankedPack{Pack: scopedPack(), Contested: true, Best: false, Sole: true}

	// PRECONDITION: the fixture really is the mixed case the rule exists for.
	if !rp.needed() {
		t.Fatal("precondition: a sole claimant that lost another contest is still needed")
	}
	card := renderString(t, nodepackCard(rp, true, 7, "tok", "/opt/ComfyUI"))

	if !strings.Contains(card, `data-variant="filled"`) {
		t.Errorf("a required pack keeps the loud button — an outline one reads as "+
			"'you probably do not need this':\n%s", card)
	}
	if !strings.Contains(card, "Also needed") {
		t.Errorf("a required losing claimant is badged 'Also needed', not 'Also claims it':\n%s", card)
	}
	// PRECONDITION, same as TestWinningPackCardCollapsesItsProvenance's: without it a
	// mutation that removes the disclosure fails inside detailsExtent with the
	// INSTRUMENT's message ("no <details> at or after byte 0") instead of this guard's
	// own, which reads as a broken helper rather than a broken invariant.
	if !strings.Contains(card, "<details") {
		t.Fatalf("precondition: want a disclosure on the card:\n%s", card)
	}
	start, end := detailsExtent(t, card, 0)
	if !strings.Contains(card[start:end], "Repository: ") {
		t.Errorf("provenance must collapse on a required card too — the collapse is "+
			"keyed on having a button, not on needed():\n%s", card)
	}
}
