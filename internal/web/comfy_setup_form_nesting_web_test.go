package web

import (
	"context"
	"strings"
	"testing"

	"golang.org/x/net/html"

	"github.com/ZacxDev/civitai-manager/internal/comfy"
)

// The setup slot swaps in a <form>. If the slot sits inside the incompatible-options
// <form>, the first CTA click nests one form in another — measured in a real browser
// with the app's vendored htmx 2.0.4:
//
//	                                     before click   after click
//	document.querySelector("form form")  null           NON-NULL
//	outer form's first button[type=submit]  "Run with…"  "Save folder"
//
// and mid-flight during a delayed POST /run-with-options, "Save folder" was disabled
// while "Run with selected options" stayed LIVE, because hx-disabled-elt's `find` is
// a single querySelector.
//
// 🔴 THESE GUARDS PARSE THE DOM. An index comparison cannot tell "sibling before"
// from "child of" — the exact trap divExtent's own comment records — and that is the
// whole distinction here, since the slot was ALREADY rendered above the groups when
// it was still inside the form.

// parseFragment parses a rendered fragment into a body-context node tree.
func parseFragment(t *testing.T, frag string) *html.Node {
	t.Helper()
	doc, err := html.Parse(strings.NewReader("<!doctype html><html><body>" + frag + "</body></html>"))
	if err != nil {
		t.Fatalf("parse fragment: %v", err)
	}
	return doc
}

func hasAttr(n *html.Node, key, want string) bool {
	for _, a := range n.Attr {
		if a.Key == key && a.Val == want {
			return true
		}
	}
	return false
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

// findNode returns the first node in document order satisfying pred.
func findNode(n *html.Node, pred func(*html.Node) bool) *html.Node {
	if pred(n) {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if got := findNode(c, pred); got != nil {
			return got
		}
	}
	return nil
}

// countNodes counts every node in the tree satisfying pred.
func countNodes(n *html.Node, pred func(*html.Node) bool) int {
	c := 0
	if pred(n) {
		c++
	}
	for k := n.FirstChild; k != nil; k = k.NextSibling {
		c += countNodes(k, pred)
	}
	return c
}

func isElem(n *html.Node, tag string) bool {
	return n.Type == html.ElementNode && n.Data == tag
}

// hasAncestor reports whether n has an ancestor satisfying pred.
func hasAncestor(n *html.Node, pred func(*html.Node) bool) bool {
	for p := n.Parent; p != nil; p = p.Parent {
		if pred(p) {
			return true
		}
	}
	return false
}

// nodeText is an element's concatenated text, whitespace-collapsed.
func nodeText(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(k *html.Node) {
		if k.Type == html.TextNode {
			b.WriteString(k.Data)
		}
		for c := k.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return strings.Join(strings.Fields(b.String()), " ")
}

func isSetupContainer(n *html.Node) bool { return hasAttr(n, "id", comfySetupContainerID) }

// TestSetupSlotIsNotInsideTheOptionsForm is the structural half: as RENDERED, the
// container must have no <form> ancestor.
func TestSetupSlotIsNotInsideTheOptionsForm(t *testing.T) {
	section := renderString(t, incompatibleOptionsSection(
		[]comfy.BadOption{routableFileBadOption}, 7, "tok", false, false, true))
	doc := parseFragment(t, section)

	// PRECONDITIONS: both parties to the relationship are really present.
	cont := findNode(doc, isSetupContainer)
	if cont == nil {
		t.Fatalf("precondition: no #%s in the section:\n%s", comfySetupContainerID, section)
	}
	optForm := findNode(doc, func(n *html.Node) bool {
		return isElem(n, "form") && strings.Contains(attr(n, "hx-post"), "run-with-options")
	})
	if optForm == nil {
		t.Fatalf("precondition: no run-with-options form in the section:\n%s", section)
	}

	if hasAncestor(cont, func(n *html.Node) bool { return isElem(n, "form") }) {
		t.Errorf("the setup slot must be a SIBLING of the options form, not a descendant — "+
			"the fragment it swaps in is itself a <form>, so this nests one form in another "+
			"on the first click:\n%s", section)
	}
	// It still precedes the groups, which is the placement the slot exists to have.
	ci := strings.Index(section, `id="`+comfySetupContainerID+`"`)
	gi := strings.Index(section, `name="opt_input"`)
	if ci < 0 || gi < 0 || ci > gi {
		t.Errorf("the slot must still render above the groups (slot %d, first group %d):\n%s",
			ci, gi, section)
	}
}

// TestClickedSetupFormDoesNotNestInsideTheOptionsForm is the BEHAVIOURAL half, and it
// is the one that reproduces what the browser measured: it splices the REAL fragment
// the CTA loads into the container (what hx-swap="innerHTML" does) and then asserts
// the two properties that failed.
//
// 🔴 The structural guard above cannot see this on its own — it says where the
// container is, not what happens once the container is filled. Pinning only one of
// the two leaves the seam open, which is the lesson this repo keeps re-teaching.
func TestClickedSetupFormDoesNotNestInsideTheOptionsForm(t *testing.T) {
	srv := setupTestServer(t)
	section := renderString(t, incompatibleOptionsSection(
		[]comfy.BadOption{routableFileBadOption}, 7, "tok", false, false, true))
	loaded := renderString(t, srv.comfySetupFragment(context.Background(), 7, "", ""))

	// PRECONDITION: the thing being swapped in really is a <form>. If it ever stops
	// being one this guard would pass for a reason unrelated to the hazard.
	if lf := findNode(parseFragment(t, loaded), func(n *html.Node) bool { return isElem(n, "form") }); lf == nil {
		t.Fatalf("precondition: the setup fragment is no longer a <form>:\n%s", loaded)
	}

	// hx-swap="innerHTML" on #comfy-setup: replace the container's contents.
	start, end := divExtent(t, section, `id="`+comfySetupContainerID+`"`)
	open := strings.Index(section[start:end], ">") + start + 1
	clicked := section[:open] + loaded + section[end-len("</div>"):]

	doc := parseFragment(t, clicked)

	// PRECONDITION: the splice produced the state under test, not a mangled string.
	if n := countNodes(doc, isSetupContainer); n != 1 {
		t.Fatalf("precondition: want exactly one container after the splice, got %d:\n%s", n, clicked)
	}
	if !strings.Contains(clicked, `name="model_path"`) {
		t.Fatalf("precondition: the swapped-in form is not in the spliced markup:\n%s", clicked)
	}

	// 1. The setup form's FIELDS must not land in the options form.
	//
	// 🔴 The obvious assertion here — `findNode(form with a form ancestor)`, i.e.
	// `document.querySelector("form form")` — IS VACUOUS AGAINST A PARSER and was
	// caught being so: html.Parse implements the HTML5 form-pointer rule, so it DROPS
	// a nested <form> start tag outright. There is then no nested form node to find and
	// the check passes on the broken markup too. Measured: with the slot put back
	// inside the form, that assertion stayed silent while the two below fired.
	//
	// The parser dropping the tag is not a reprieve, it is the hazard itself: the
	// inner form's controls are re-parented into the OUTER form, so model_path, a
	// second csrf_token and a type=submit "Save folder" become fields of the run
	// request — at which point Save folder runs the workflow. Asserting the fields is
	// therefore both the failure-capable check AND the one that names the real cost.
	optFormEarly := findNode(doc, func(n *html.Node) bool {
		return isElem(n, "form") && strings.Contains(attr(n, "hx-post"), "run-with-options")
	})
	if optFormEarly == nil {
		t.Fatalf("precondition: the options form vanished from the spliced markup:\n%s", clicked)
	}
	if stray := findNode(optFormEarly, func(n *html.Node) bool {
		return isElem(n, "input") && attr(n, "name") == "model_path"
	}); stray != nil {
		t.Errorf("the setup form's model_path field is inside the options form — the parser "+
			"re-parented it, so a run POST now carries it:\n%s", clicked)
	}
	if n := countNodes(optFormEarly, func(k *html.Node) bool {
		return isElem(k, "input") && attr(k, "name") == "csrf_token"
	}); n != 1 {
		t.Errorf("the options form carries %d csrf_token inputs, want exactly 1 — a second one "+
			"means the setup form's fields were re-parented into it:\n%s", n, clicked)
	}
	if n := countNodes(optFormEarly, func(k *html.Node) bool {
		return isElem(k, "button") && strings.EqualFold(attr(k, "type"), "submit")
	}); n != 1 {
		t.Errorf("the options form holds %d submit buttons, want exactly 1 — a second one is "+
			"'Save folder', which would then run the workflow:\n%s", n, clicked)
	}

	// 2. hx-disabled-elt="find button[type='submit']" — htmx's `find` is ONE
	// querySelector, so this must resolve to the section's OWN submit. Reproduced by
	// taking the options form's first descendant submit button in document order.
	optForm := findNode(doc, func(n *html.Node) bool {
		return isElem(n, "form") && strings.Contains(attr(n, "hx-post"), "run-with-options")
	})
	if optForm == nil {
		t.Fatalf("precondition: the options form vanished from the spliced markup:\n%s", clicked)
	}
	if want := "find button[type='submit']"; attr(optForm, "hx-disabled-elt") != want {
		t.Fatalf("precondition: this guard reproduces %q, but the form now carries %q",
			want, attr(optForm, "hx-disabled-elt"))
	}
	first := findNode(optForm, func(n *html.Node) bool {
		return isElem(n, "button") && strings.EqualFold(attr(n, "type"), "submit")
	})
	if first == nil {
		t.Fatalf("the options form has no submit button at all:\n%s", clicked)
	}
	if got := nodeText(first); got != "Run with selected options" {
		t.Errorf("the section's in-flight guard resolves to %q — it must disable the "+
			"section's OWN submit, not a control the setup form swapped in:\n%s", got, clicked)
	}
}

// TestSetupCTAIsNotASubmitButton pins h.Type("button") on the CTA.
//
// The structural fix above is the primary defence — the CTA is no longer inside any
// form, so nothing can submit. This pins the attribute anyway because it costs one
// assertion and the alternative is relying on htmx's own click cancellation, which
// is a property of a vendored dependency rather than of this markup.
func TestSetupCTAIsNotASubmitButton(t *testing.T) {
	doc := parseFragment(t, renderString(t, comfySetupCTA(7, "Set up automatic install for 1 model file")))
	btn := findNode(doc, func(n *html.Node) bool { return isElem(n, "button") })
	if btn == nil {
		t.Fatalf("precondition: the CTA rendered no button")
	}
	if got := attr(btn, "type"); got != "button" {
		t.Errorf(`the setup CTA must carry type="button", got %q — a <button> with no type `+
			`defaults to SUBMIT`, got)
	}
	// The trigger is on the SAME element as the type, not merely somewhere on the page.
	if got := attr(btn, "hx-get"); !strings.HasSuffix(got, "/comfy-setup") {
		t.Errorf("the typed button must be the one carrying the setup trigger, got hx-get=%q", got)
	}
}
