package web

import (
	"strings"
	"testing"
	"time"

	"golang.org/x/net/html"

	"github.com/ZacxDev/civitai-manager/internal/comfy"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// Guard for axe-core's `scrollable-region-focusable` (impact: SERIOUS,
// wcag2a/wcag211/wcag213) across every horizontally scrollable region this package
// renders. One instance of this class already shipped as a real violation — the
// `git clone` command block, fixed by commandBlock (nodepack_pages.go).
//
// 🔴 WHY THIS IS NOT "add tabindex to every overflow-x-auto div". Read the rule as
// axe actually defines it (e2e/uxaudit/axe.min.js):
//
//	"scrollable-region-focusable", impact:"serious",
//	any:["focusable-content","focusable-element"], none:[]
//
// `any:` means the region PASSES if EITHER check passes — so a scrollable region is
// allowed to carry no tabindex for exactly as long as it CONTAINS something
// focusable ("focusable-content" pass message: "Element contains focusable
// elements"). Three of this package's six regions pass that way, and giving them a
// tabindex would add a redundant tab stop in front of content that is already
// reachable. The other three have a REACHABLE state containing nothing focusable at
// all, where content past the right edge cannot be reached without a pointer.
//
// So this test pins the MECHANISM, not merely the outcome. Asserting only "the rule
// passes" would be satisfied for the wrong reason the moment a fixture happened to
// contain a button — the over-determined-fixture vacuity mode catalogued in
// CLAUDE.md. Each case declares HOW it passes and the test fails if that changes in
// either direction: delete scrollTable's tabindex and the viaTabindex cases go red;
// delete sortableTh's tabindex and the viaFocusableContent case goes red.
//
// The fixtures are deliberately the MINIMAL-focusable state of each region (an empty
// subscription list, a fully-restored trash batch), because that is the state the
// ux-audit walk's lab data does not produce — which is the whole reason these went
// unflagged.

// scrollMech is how a scrollable region satisfies the rule.
type scrollMech int

const (
	// viaTabindex: the region itself is focusable. Required when it can contain
	// nothing focusable.
	viaTabindex scrollMech = iota
	// viaFocusableContent: the region contains focusable elements, so the rule
	// passes without a tabindex of its own.
	viaFocusableContent
)

func (m scrollMech) String() string {
	if m == viaTabindex {
		return "viaTabindex"
	}
	return "viaFocusableContent"
}

// 🔴 EVERY package-level helper in this file is `a11y`-prefixed, and that is not
// stylistic. This file originally declared a bare `attr(*html.Node, string)` — which
// (a) DUPLICATED the long-standing a11yAttr/a11yHasAttr pair in
// run_preset_a11y_web_test.go, and (b) collided with an `attr` a concurrently-developed
// test file introduced, breaking the MERGED tree while both branches were individually
// green. `go build ./...` still passed (the clash is in _test.go) and `gh` reported the
// PR MERGEABLE/CLEAN, because the two files never touch the same lines — the conflict is
// semantic, and only `go vet ./internal/web/` or `go test` surfaces it.
//
// So: reuse the a11y* helpers rather than adding a generic one, and prefix anything
// genuinely new. A bare `isFocusable` or `findNode` in this package is a collision
// waiting for the next a11y test.

// a11yHasClassToken reports whether n carries the exact class token cls. Token-wise, not
// a substring: "overflow-x-auto" must not be matched by some future
// "overflow-x-auto-md".
func a11yHasClassToken(n *html.Node, cls string) bool {
	for _, a := range n.Attr {
		if a.Key != "class" {
			continue
		}
		for _, f := range strings.Fields(a.Val) {
			if f == cls {
				return true
			}
		}
	}
	return false
}

// a11yIsFocusable mirrors axe's notion of a focusable element closely enough for these
// fragments: an explicit non-negative tabindex, or a natively focusable control that
// is not disabled. A bare <a> with no href is NOT focusable, which is why the href
// check is here rather than matching on tag name alone.
//
// It uses a11yHasAttr wherever ABSENT must be distinguished from EMPTY (tabindex,
// disabled, href) and a11yAttr where "" is a correct answer — an <input> with no type
// is a text input, and so focusable.
func a11yIsFocusable(n *html.Node) bool {
	if n.Type != html.ElementNode {
		return false
	}
	if a11yHasAttr(n, "tabindex") &&
		!strings.HasPrefix(strings.TrimSpace(a11yAttr(n, "tabindex")), "-") {
		return true
	}
	if a11yHasAttr(n, "disabled") {
		return false
	}
	switch n.Data {
	case "button", "select", "textarea":
		return true
	case "a":
		return a11yHasAttr(n, "href")
	case "input":
		return !strings.EqualFold(a11yAttr(n, "type"), "hidden")
	}
	return false
}

// a11yHasFocusableDescendant implements axe's "focusable-content" check.
func a11yHasFocusableDescendant(n *html.Node) bool {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if a11yIsFocusable(c) || a11yHasFocusableDescendant(c) {
			return true
		}
	}
	return false
}

// a11yFindScrollRegions collects every overflow-x-auto element in a parsed fragment.
func a11yFindScrollRegions(n *html.Node) []*html.Node {
	var out []*html.Node
	var walk func(*html.Node)
	walk = func(x *html.Node) {
		if x.Type == html.ElementNode && a11yHasClassToken(x, "overflow-x-auto") {
			out = append(out, x)
		}
		for c := x.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return out
}

func TestEveryScrollableRegionIsKeyboardReachable(t *testing.T) {
	restored := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name string
		html string
		want scrollMech
		why  string
	}{
		{
			name: "subscriptionsTable/empty",
			html: renderString(t, subscriptionsTable(nil, "", "tok")),
			want: viaTabindex,
			why:  "the empty state is six plain <th>s and one text <td> — nothing focusable",
		},
		{
			name: "trashTable/all-restored",
			html: renderString(t, trashTable([]batchView{{
				Batch: store.QuarantineBatch{
					ID: 1, CreatedAt: restored, Reason: "duplicate", RestoredAt: &restored,
				},
				Files: 2,
			}}, "tok")),
			want: viaTabindex,
			why:  "a restored batch renders a badge, not a Restore button",
		},
		{
			name: "cloudResourceTable",
			html: renderString(t, cloudResourceTable([]comfy.ResolvedResource{{
				Filename: "model.safetensors", URN: "urn:air:sd1:checkpoint:civitai:1@2",
				Status: comfy.ResolveResolved,
			}})),
			want: viaTabindex,
			why:  "every cell is text or a badge, in every state",
		},
		{
			name: "libraryModelTable",
			// Needs ≥1 file: libraryModelTable renders an empty-state <p> (no region
			// at all) for an empty slice, which the precondition below catches.
			html: renderString(t, libraryModelTable([]store.LocalFile{{
				ID: 1, Path: "/models/a.safetensors", SizeBytes: 1 << 20,
			}})),
			want: viaFocusableContent,
			why:  "sortableTh headers carry tabindex=0 so the table is always reachable",
		},
		{
			name: "candidatesTable",
			html: renderString(t, candidatesTable([]store.LocalFile{{
				ID: 1, Path: "/models/a.safetensors", SizeBytes: 1 << 20,
				CandidateReason: "duplicate",
			}}, "tok")),
			want: viaFocusableContent,
			why:  "every row carries a checkbox and a Quarantine button",
		},
		{
			name: "navbar/cm-navlinks",
			html: renderString(t, navbar("tok", fullMaturityRange(), railData{})),
			want: viaFocusableContent,
			why:  "the nav links are <a href> elements",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc, err := html.Parse(strings.NewReader(tc.html))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			regions := a11yFindScrollRegions(doc)
			// PRECONDITION: a fixture that renders no scrollable region at all cannot
			// express this invariant, and would pass silently forever.
			if len(regions) != 1 {
				t.Fatalf("precondition: want exactly 1 overflow-x-auto region, got %d "+
					"(fixture cannot express the invariant)", len(regions))
			}
			r := regions[0]

			selfFocusable := a11yIsFocusable(r)
			contentFocusable := a11yHasFocusableDescendant(r)

			// The rule itself: any:["focusable-content","focusable-element"].
			if !selfFocusable && !contentFocusable {
				t.Errorf("axe scrollable-region-focusable (SERIOUS) would FAIL: region is "+
					"not focusable and contains nothing focusable.\n%s", tc.why)
			}

			// And the mechanism, so the guard cannot pass for the wrong reason.
			switch tc.want {
			case viaTabindex:
				// This is the discriminating assertion: the region must have NO
				// focusable content, which is what makes its own tabindex
				// load-bearing. If this stops holding the case has silently become a
				// viaFocusableContent case and the ledger is stale.
				if contentFocusable {
					t.Fatalf("ledger stale: %s is recorded as %s (%s) but now CONTAINS "+
						"focusable content — re-classify it", tc.name, tc.want, tc.why)
				}
				if !selfFocusable {
					t.Errorf("%s must carry tabindex=0: %s", tc.name, tc.why)
				}
			case viaFocusableContent:
				if !contentFocusable {
					t.Errorf("%s is recorded as passing on focusable content (%s), but "+
						"contains nothing focusable — it now needs its own tabindex",
						tc.name, tc.why)
				}
			}
		})
	}
}

// TestScrollTableRegionsAreNamed pins the other half of the fix: a new tab stop that
// announces nothing is a poor trade for a screen-reader user, so scrollTable's
// regions carry role+aria-label (the workflow_graph.go precedent), not commandBlock's
// bare tabindex.
func TestScrollTableRegionsAreNamed(t *testing.T) {
	doc, err := html.Parse(strings.NewReader(renderString(t, subscriptionsTable(nil, "", "tok"))))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	regions := a11yFindScrollRegions(doc)
	if len(regions) != 1 {
		t.Fatalf("precondition: want exactly 1 region, got %d", len(regions))
	}
	role := a11yAttr(regions[0], "role")
	label := a11yAttr(regions[0], "aria-label")
	if role != "region" {
		t.Errorf(`want role="region" on the scroll container, got %q`, role)
	}
	if strings.TrimSpace(label) == "" {
		t.Error("scroll container is a tab stop with no accessible name")
	}
}
