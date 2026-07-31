package web

import (
	"strings"
	"testing"
)

// TestNavbarLabels pins the nav's destination set after the rework.
//
// THE ABSENCE HALF IS THE POINT. A partial edit — renaming "Models" but leaving
// "Dashboard", or adding "Disks" beside a surviving "Trash" — still satisfies
// every presence assertion, so the removed labels are asserted GONE with the same
// weight as the new ones being present.
//
// The label checks use the exact anchor text (">X<"), not a bare substring, so
// they cannot be satisfied by a heading or an href elsewhere in the fragment —
// and unlike the absence list below, which was always written that way, the
// presence loop USED to check only `">"+label`, with no closing angle bracket.
// That left ">Apps" satisfiable by an ">Apps discovery" heading and made the
// comment describe a strictness the code did not have. Both ends are ">X<" now.
// ("Library" is a <summary> whose text is followed by the caret <span>, so its
// closing "<" is that tag rather than a </a>; the assertion holds either way.)
func TestNavbarLabels(t *testing.T) {
	body := renderString(t, navbar("dark", "csrf-token", fullMaturityRange(), railData{}))

	// New / kept labels, with the route each must point at. The routes are pinned
	// beside the labels because a rename that also moved the href would otherwise
	// pass the label half and silently break the destination.
	for _, c := range []struct{ label, href string }{
		{"Find models", "/search"},
		{"Find workflows", "/workflows/discover"},
		{"Apps", "/apps/discover"},
		{"Library", ""}, // a <summary>, not an <a> — its two items are checked below
		{"Disks", "/disks"},
	} {
		if !strings.Contains(body, ">"+c.label+"<") {
			t.Errorf("nav is missing the %q entry:\n%s", c.label, body)
		}
		if c.href != "" && !strings.Contains(body, `href="`+c.href+`"`) {
			t.Errorf("the %q entry must point at %s", c.label, c.href)
		}
	}

	// The absence checks run against the nav WITHOUT the Library disclosure's
	// panel. That panel legitimately contains an item labelled "Workflows" (the
	// library's workflow tab), which is a different destination from the removed
	// top-level "Workflows" link — searching the whole fragment would confuse the
	// two and make the check unsatisfiable. Removing the panel is what keeps
	// ">Workflows<" a meaningful assertion about the top-level strip.
	menu := navMenuFragment(t, body)
	topLevel := strings.Replace(body, menu, "", 1)
	if strings.Contains(topLevel, "cm-navmenu-item") {
		t.Fatalf("the menu panel was not removed from the fixture; the absence checks below would be meaningless")
	}

	// GONE. Each of these was a real nav entry before the rework:
	//   Dashboard — folded into the brand link (same destination, one control)
	//   Models / Workflows — renamed to disambiguate them from the Library
	//   Outputs — moved to the recent-outputs rail heading
	//   Trash — replaced by Disks, which absorbed its content
	//   Search / Discover — the older names those two carried before that
	for _, gone := range []string{
		">Dashboard<", ">Models<", ">Workflows<", ">Outputs<", ">Trash<",
		">Search<", ">Discover<",
	} {
		if strings.Contains(topLevel, gone) {
			t.Errorf("nav still renders the removed label %q:\n%s", gone, topLevel)
		}
	}

	// The old flat /library and /trash hrefs must be gone too — a label rename
	// that left the anchor behind would pass every check above.
	for _, gone := range []string{`href="/library"`, `href="/trash"`} {
		if strings.Contains(topLevel, gone) {
			t.Errorf("nav still links %s", gone)
		}
	}
}

// navMenuFragment slices the Library menu out of a rendered navbar.
//
// The wrapper is a plain <div>, so it cannot be bounded by "</div>" — the panel
// inside it closes one first. `</div></div>` is the panel's close followed by the
// wrapper's, which is unambiguous here because the panel is the wrapper's LAST
// child. sliceBetween fails the test when either marker is absent, so a
// restructure reports itself instead of silently returning an empty fragment
// that would make every "must not contain" check below pass for free.
func navMenuFragment(t *testing.T, body string) string {
	t.Helper()
	return sliceBetween(t, body, `<div class="cm-navmenu `, "</div></div>")
}

// TestNavLibraryDropdown pins the Library menu: a `popover` (no JS anywhere in
// it) carrying both Library surfaces.
//
// 🔴 THE `auto` STATE IS THE WHOLE REASON THIS STOPPED BEING <details>, SO IT IS
// ASSERTED DIRECTLY. <details> gives activation and keyboard operation but
// neither light-dismiss nor Escape; `popover` in its default `auto` state gives
// both. `popover="manual"` gives NEITHER — it is a one-word edit that silently
// reverts the entire change while every other assertion here stays green, which
// is exactly why the value is pinned rather than merely the attribute's
// presence.
//
// WHY "WORKS WITHOUT JS" IS ASSERTED STRUCTURALLY. There is no way to prove
// "JavaScript is not required" from rendered HTML, so this asserts the things
// that would make it FALSE: the trigger is wired by popovertarget (which the
// browser honours natively on click and on Enter/Space), and the fragment
// carries no script, no inline handler and no htmx attribute that could be doing
// the opening instead.
//
// Live-verified in Brave at 1198px: click opens, click-outside closes, Escape
// closes and returns focus to the trigger. No markup assertion can see any of
// that — this guards the WIRING those behaviours depend on.
func TestNavLibraryDropdown(t *testing.T) {
	body := renderString(t, navbar("dark", "csrf-token", fullMaturityRange(), railData{}))

	menu := navMenuFragment(t, body)

	// The trigger: a real focusable <button>, explicitly type=button (the default
	// is submit), pointing at the panel.
	if !strings.Contains(menu, `<button type="button" class="cm-navmenu-summary"`) {
		t.Errorf("the trigger must be a <button type=\"button\"> — a <div> would not be focusable and "+
			"would not activate on Enter/Space:\n%s", menu)
	}
	if !strings.Contains(menu, `popovertarget="`+libraryMenuPanelID+`"`) {
		t.Errorf("the trigger must carry popovertarget=%q; without it the button does nothing at all, "+
			"silently:\n%s", libraryMenuPanelID, menu)
	}
	// The panel: the SAME id, and the popover attribute in its auto state.
	if !strings.Contains(menu, `<div id="`+libraryMenuPanelID+`" popover class="cm-navmenu-panel">`) {
		t.Errorf("the panel must be <div id=%q popover class=\"cm-navmenu-panel\"> — a valueless "+
			"`popover` IS the auto state:\n%s", libraryMenuPanelID, menu)
	}
	// 🔴 The states that would silently remove light-dismiss and Escape.
	for _, dead := range []string{`popover="manual"`, `popover="hint"`} {
		if strings.Contains(menu, dead) {
			t.Errorf("the panel declares %s — that state has NO light-dismiss and NO Escape, which "+
				"is the entire reason this stopped being a <details>:\n%s", dead, menu)
		}
	}
	// The old disclosure must be gone, not merely joined by a popover.
	for _, gone := range []string{"<details", "<summary"} {
		if strings.Contains(menu, gone) {
			t.Errorf("the Library menu still renders %s — <details> cannot light-dismiss or "+
				"Escape:\n%s", gone, menu)
		}
	}

	for _, c := range []struct{ label, href string }{
		{"Model files", "/library?tab=files"},
		{"Workflows", "/library?tab=workflows"},
	} {
		want := `href="` + c.href + `" class="cm-navmenu-item">` + c.label
		if !strings.Contains(menu, want) {
			t.Errorf("the Library menu is missing %q -> %s:\n%s", c.label, c.href, menu)
		}
	}
	// No JS of any kind: no <script>, no on* handler, no htmx attribute. Any of
	// the three would mean the open/close behaviour is not actually native.
	for _, forbidden := range []string{"<script", "onclick", "onkeydown", "hx-"} {
		if strings.Contains(menu, forbidden) {
			t.Errorf("the Library menu must be JS-free (popover is native), found %q:\n%s", forbidden, menu)
		}
	}
}

// sliceBetween returns the substring of s from the first occurrence of start
// through the first end that follows it, failing the test when either is absent
// (an empty slice would make every "must contain" check below it vacuously fail
// in a confusing way, and every "must not contain" check vacuously PASS).
func sliceBetween(t *testing.T, s, start, end string) string {
	t.Helper()
	i := strings.Index(s, start)
	if i < 0 {
		t.Fatalf("fixture does not contain %q:\n%s", start, s)
	}
	j := strings.Index(s[i:], end)
	if j < 0 {
		t.Fatalf("fixture has %q with no closing %q:\n%s", start, end, s)
	}
	return s[i : i+j+len(end)]
}
