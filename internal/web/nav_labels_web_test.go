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
// they cannot be satisfied by a heading or an href elsewhere in the fragment.
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
		if !strings.Contains(body, ">"+c.label) {
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
	menu := sliceBetween(t, body, `<details class="cm-navmenu`, "</details>")
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

// TestNavLibraryDropdown pins the Library disclosure: it must be a native
// <details>/<summary> (no JS anywhere in it) carrying both Library surfaces.
//
// WHY "WORKS WITHOUT JS" IS ASSERTED STRUCTURALLY. There is no way to prove
// "JavaScript is not required" from rendered HTML directly, so this asserts the
// two things that would make it FALSE: the element is <details> (which the
// browser opens natively on click and on Enter/Space), and the fragment carries
// no script, no inline handler and no htmx attribute that could be doing the
// opening instead.
func TestNavLibraryDropdown(t *testing.T) {
	body := renderString(t, navbar("dark", "csrf-token", fullMaturityRange(), railData{}))

	menu := sliceBetween(t, body, `<details class="cm-navmenu`, "</details>")

	if !strings.Contains(menu, "<summary") {
		t.Errorf("the Library menu must use <summary> as its trigger:\n%s", menu)
	}
	// Emitted CLOSED. Every page render is a fresh document, so a menu that
	// rendered with `open` would greet the user expanded on every navigation.
	if strings.Contains(body, "<details class=\"cm-navmenu shrink-0\" open") {
		t.Errorf("the Library menu must render closed:\n%s", menu)
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
	// No JS of any kind inside the disclosure: no <script>, no on* handler, no
	// htmx attribute. Any of the three would mean the open/close behaviour is not
	// actually native.
	for _, forbidden := range []string{"<script", "onclick", "onkeydown", "hx-"} {
		if strings.Contains(menu, forbidden) {
			t.Errorf("the Library menu must be JS-free (<details> is native), found %q:\n%s", forbidden, menu)
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
