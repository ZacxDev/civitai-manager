package web

import (
	"context"
	"strings"
	"testing"

	"golang.org/x/net/html"

	"github.com/ZacxDev/civitai-manager/internal/comfy"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// Regression guards for the two axe violations the ux-audit walk reported against
// the UI-format workflow views (`workflow-detail-ui` and `run-missing-models-ui`, at
// both viewports — 4 captures each, 8 of 8 violations in the run):
//
//   - aria-required-children (CRITICAL): runPresetTabStrip put the "+ Fork" button
//     inside role="tablist", which may contain only role="tab" children.
//   - svg-img-alt (SERIOUS): the generated graph <svg> carried role="img" with no
//     accessible name, which announces nothing at all.
//
// 🔴 WHY THESE PARSE THE DOM INSTEAD OF CALLING strings.Contains. Both bugs are
// about WHICH ELEMENT carries an attribute, and a bare substring search cannot see
// that — this repo has already shipped a permanently-open sheet across every page
// because `strings.Contains(out, " popover")` was satisfied by a different
// element's ` popovertarget=`. Index-ordering comparisons are equally useless here:
// `Index(tablist) < Index(fork)` is true whether Fork is a CHILD of the tablist (the
// bug) or its SIBLING (the fix), which is the entire distinction. So these walk the
// real parsed tree and assert containment and id-resolution.
//
// Each test asserts its PRECONDITIONS with t.Fatal before asserting the outcome, so
// a fixture that cannot express the bug fails loudly instead of passing quietly.

// ── parsed-DOM helpers ───────────────────────────────────────────────────────

// a11yParse parses a rendered fragment into a document tree.
func a11yParse(t *testing.T, s string) *html.Node {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(s))
	if err != nil {
		t.Fatalf("parse rendered HTML: %v", err)
	}
	return doc
}

// a11yAttr returns an element's attribute value, or "" when it is absent. Callers
// that must distinguish absent from empty use a11yHasAttr.
func a11yAttr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func a11yHasAttr(n *html.Node, key string) bool {
	for _, a := range n.Attr {
		if a.Key == key {
			return true
		}
	}
	return false
}

// a11yWithAttr returns every element in the document carrying key=val.
func a11yWithAttr(doc *html.Node, key, val string) []*html.Node {
	var out []*html.Node
	for _, n := range descendants(doc) {
		if a11yAttr(n, key) == val {
			out = append(out, n)
		}
	}
	return out
}

// a11yByID returns the single element with the given id, or nil. It fails the test
// when the id is DUPLICATED — an ambiguous id would make every aria-controls /
// aria-labelledby assertion below meaningless.
func a11yByID(t *testing.T, doc *html.Node, id string) *html.Node {
	t.Helper()
	found := a11yWithAttr(doc, "id", id)
	if len(found) > 1 {
		t.Fatalf("id %q appears %d times; ARIA id references would be ambiguous", id, len(found))
	}
	if len(found) == 0 {
		return nil
	}
	return found[0]
}

// a11yElementChildren returns an element's DIRECT element children (skipping text
// and comment nodes). Direct children are exactly what aria-required-children
// judges, so this must not be flattened to descendants.
func a11yElementChildren(n *html.Node) []*html.Node {
	var out []*html.Node
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode {
			out = append(out, c)
		}
	}
	return out
}

// a11yContains reports whether want is n itself or anywhere below it.
func a11yContains(n, want *html.Node) bool {
	if n == nil || want == nil {
		return false
	}
	if n == want {
		return true
	}
	for _, d := range descendants(n) {
		if d == want {
			return true
		}
	}
	return false
}

// a11yText is an element's concatenated, space-collapsed text content.
func a11yText(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(x *html.Node) {
		if x.Type == html.TextNode {
			b.WriteString(x.Data)
		}
		for c := x.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return strings.Join(strings.Fields(b.String()), " ")
}

// a11yDescribe renders an element as `<tag id=… role=… class=…>` for error text, so
// a failure names the offending element instead of dumping the whole page.
func a11yDescribe(n *html.Node) string {
	var b strings.Builder
	b.WriteString("<" + n.Data)
	for _, k := range []string{"id", "role", "type", "class", "aria-controls", "aria-labelledby"} {
		if v := a11yAttr(n, k); v != "" {
			b.WriteString(" " + k + "=" + `"` + v + `"`)
		}
	}
	b.WriteString(">")
	if txt := a11yText(n); txt != "" {
		if len(txt) > 40 {
			txt = txt[:40] + "…"
		}
		b.WriteString(" text=" + `"` + txt + `"`)
	}
	return b.String()
}

// a11yFindForkButton locates the "+ Fork" control by its visible text — the same
// thing a user reads. It is deliberately NOT found by class: Fork shares
// .cm-version-tab with the tabs, so a class lookup could not tell them apart.
func a11yFindForkButton(doc *html.Node) *html.Node {
	for _, n := range descendants(doc) {
		if n.Data == "button" && strings.Contains(a11yText(n), "Fork") {
			return n
		}
	}
	return nil
}

// a11yRunPanelPage renders the run section for a UI-format workflow carrying two
// saved presets — the state that puts real tabs AND Fork on the page at once.
func a11yRunPanelPage(t *testing.T, srv *Server, wf *store.Workflow) string {
	t.Helper()
	v := srv.buildPresetView(context.Background(), wf, 0, nil, true)
	return renderString(t, generateSection(wf, runSnapshot{}, "tok", true, false,
		fullMaturityRange(), v, true, comfyHelperView{}))
}

// a11ySeedTwoPresetWorkflow seeds a UI-format workflow with two presets.
func a11ySeedTwoPresetWorkflow(t *testing.T, srv *Server) *store.Workflow {
	t.Helper()
	wf := seedPresetWorkflow(t, srv, "t2i", presetUIGraph)
	cur := func(ri comfy.RunInput) string { return ri.Current }
	seedPreset(t, srv, wf, "Base", wf.GraphHash, cur)
	seedPreset(t, srv, wf, "Hi-res", wf.GraphHash, cur)
	return wf
}

// ── guard 1: the tablist may contain only tabs ───────────────────────────────

// TestRunPresetTablistContainsOnlyTabs is the aria-required-children guard.
//
// Mutation-verified: putting runPresetForkButton back inside the tablist div (the
// shipped bug) fails this with "run-preset tablist has a NON-TAB direct child".
func TestRunPresetTablistContainsOnlyTabs(t *testing.T) {
	srv := newTestServer(t)
	wf := a11ySeedTwoPresetWorkflow(t, srv)
	doc := a11yParse(t, a11yRunPanelPage(t, srv, wf))

	// PRECONDITIONS — prove the fixture can actually express the bug.
	lists := a11yWithAttr(doc, "role", "tablist")
	if len(lists) != 1 {
		t.Fatalf("fixture: want exactly 1 role=tablist on the run panel, got %d", len(lists))
	}
	list := lists[0]
	fork := a11yFindForkButton(doc)
	if fork == nil {
		t.Fatal("fixture: no \"+ Fork\" button on the page — this guard could never " +
			"observe the violation it exists for")
	}
	// 🔴 THIS PRECONDITION MUST DESCRIBE THE FIXTURE, NOT THE FIX. It first read
	// `len(kids) != 2` — the child count of the CORRECT strip — which made the guard
	// fail on the real bug with "fixture: tablist has 3 direct element children"
	// instead of its own message, because the broken strip holds 3 (two tabs + Fork)
	// and the Fatalf fired before the role check ever ran. That is a test going red
	// for a DIFFERENT guard's reason, and it would have hidden the day the role check
	// itself stopped working. Assert the lower bound the seeding guarantees and let
	// the assertion below judge the extras.
	kids := a11yElementChildren(list)
	if len(kids) < 2 {
		t.Fatalf("fixture: tablist has %d direct element children, want at least 2 (the two "+
			"seeded presets); a strip with no tabs cannot demonstrate anything", len(kids))
	}
	if n := len(a11yWithAttr(doc, "role", "tab")); n != 2 {
		t.Fatalf("fixture: want 2 role=tab elements (the two seeded presets), got %d", n)
	}

	// THE ASSERTION.
	for _, c := range kids {
		if role := a11yAttr(c, "role"); role != "tab" {
			t.Errorf("run-preset tablist has a NON-TAB direct child (role=%q): %s\n"+
				"a role=tablist may contain ONLY role=tab children "+
				"(axe aria-required-children, CRITICAL)", role, a11yDescribe(c))
		}
	}
	if a11yContains(list, fork) {
		t.Errorf("run-preset tablist has a NON-TAB direct child: \"+ Fork\" is INSIDE the tablist.\n"+
			"Fork creates a preset, it selects nothing — it must be a SIBLING of the tablist, "+
			"not a member of it, and must not be given role=tab to silence the rule.\n"+
			"fork: %s", a11yDescribe(fork))
	}
	// Fork must stay operable wherever it moved to.
	if a11yHasAttr(fork, "hidden") {
		t.Errorf("\"+ Fork\" must not be hidden: %s", a11yDescribe(fork))
	}
}

// ── guard 2: the tab↔panel wiring resolves, and the panel is not a <form> ─────

// TestRunPresetTabsAreWiredToTheirPanel pins the association completed alongside
// the Fork fix, including the SECOND violation that fix introduced and this guard
// would have caught: role="tabpanel" on the <form> is aria-allowed-role.
//
// Mutation-verified three ways — see the test body's comments.
func TestRunPresetTabsAreWiredToTheirPanel(t *testing.T) {
	srv := newTestServer(t)
	wf := a11ySeedTwoPresetWorkflow(t, srv)
	doc := a11yParse(t, a11yRunPanelPage(t, srv, wf))

	// PRECONDITIONS.
	panels := a11yWithAttr(doc, "role", "tabpanel")
	if len(panels) != 1 {
		t.Fatalf("fixture: want exactly 1 role=tabpanel on the run panel, got %d", len(panels))
	}
	panel := panels[0]
	tabs := a11yWithAttr(doc, "role", "tab")
	if len(tabs) != 2 {
		t.Fatalf("fixture: want 2 role=tab (the two seeded presets), got %d", len(tabs))
	}

	// (a) The panel may not BE the form. A <form> maps to the form role and permits
	// only search/none/presentation as an override, so role=tabpanel on it is an axe
	// aria-allowed-role violation — which is exactly what shipping the Fork fix
	// without this wrapper produced.
	if panel.Data == "form" {
		t.Errorf("the tabpanel role is on a <form>: %s\n"+
			"a <form> may not be given role=tabpanel (axe aria-allowed-role) — "+
			"keep the role on a wrapper element", a11yDescribe(panel))
	}

	// (b) The form must still exist, keep its own id, and live INSIDE the panel.
	// runPresetInclude is "#run-preset-form, #run-modes", so a form that lost this id
	// would stop every preset control posting the user's typed values.
	form := a11yByID(t, doc, runPresetFormID)
	if form == nil {
		t.Fatalf("#%s is gone — hx-include %q would resolve to nothing and every "+
			"preset control would post empty values", runPresetFormID, runPresetInclude)
	}
	if form.Data != "form" {
		t.Errorf("#%s must remain a <form>, got <%s>", runPresetFormID, form.Data)
	}
	if !a11yContains(panel, form) {
		t.Errorf("the preset form is not inside the tabpanel; the panel would announce "+
			"an empty region.\npanel: %s\nform:  %s", a11yDescribe(panel), a11yDescribe(form))
	}

	// (c) Every tab's aria-controls must RESOLVE to that panel — not merely be
	// present, and not point somewhere else.
	panelID := a11yAttr(panel, "id")
	if panelID == "" {
		t.Fatalf("the tabpanel has no id, so no tab can reference it: %s", a11yDescribe(panel))
	}
	for _, tab := range tabs {
		ctl := a11yAttr(tab, "aria-controls")
		if ctl == "" {
			t.Errorf("tab has no aria-controls: %s", a11yDescribe(tab))
			continue
		}
		target := a11yByID(t, doc, ctl)
		if target == nil {
			t.Errorf("tab aria-controls=%q resolves to NOTHING on the page: %s", ctl, a11yDescribe(tab))
			continue
		}
		if target != panel {
			t.Errorf("tab aria-controls=%q resolves to %s, want the tabpanel %s",
				ctl, a11yDescribe(target), a11yDescribe(panel))
		}
	}

	// (d) The panel's aria-labelledby must resolve to the SELECTED tab — the whole
	// point of the label, and the thing a dangling id reference would silently break.
	lb := a11yAttr(panel, "aria-labelledby")
	if lb == "" {
		t.Fatalf("the tabpanel has no aria-labelledby: %s", a11yDescribe(panel))
	}
	labeller := a11yByID(t, doc, lb)
	if labeller == nil {
		t.Fatalf("tabpanel aria-labelledby=%q resolves to NOTHING on the page — a dangling "+
			"ARIA reference leaves the panel unnamed", lb)
	}
	if role := a11yAttr(labeller, "role"); role != "tab" {
		t.Errorf("tabpanel aria-labelledby=%q resolves to a role=%q element, want a tab: %s",
			lb, role, a11yDescribe(labeller))
	}
	if sel := a11yAttr(labeller, "aria-selected"); sel != "true" {
		t.Errorf("tabpanel aria-labelledby=%q names a tab with aria-selected=%q, want the "+
			"SELECTED tab: %s", lb, sel, a11yDescribe(labeller))
	}

	// (e) Exactly one tab may be selected.
	if n := len(a11yWithAttr(doc, "aria-selected", "true")); n != 1 {
		t.Errorf("aria-selected=\"true\" count = %d, want exactly 1", n)
	}

	// (f) Every tab must stay an ordinary tab stop. The strip deliberately ships NO
	// arrow-key handler, so a roving tabindex (tabindex="-1" on the inactive tabs)
	// would make them keyboard-UNREACHABLE rather than more conformant.
	for _, tab := range tabs {
		if ti := a11yAttr(tab, "tabindex"); ti == "-1" {
			t.Errorf("tab carries tabindex=\"-1\" but the strip has no arrow-key handler, "+
				"so this tab is now unreachable by keyboard: %s", a11yDescribe(tab))
		}
	}
}

// ── guard 3: the graph SVG must have an accessible name ──────────────────────

// TestWorkflowGraphSVGHasAnAccessibleName is the svg-img-alt guard.
//
// It renders through workflowGraphSection — the PRODUCTION entry point that the
// workflow detail page calls — rather than the bare SVG builder, so the guard
// measures the same node tree the harness scanned.
//
// Mutation-verified: deleting the svg's aria-label fails this with
// "role=\"img\" with NO accessible name".
func TestWorkflowGraphSVGHasAnAccessibleName(t *testing.T) {
	// PRECONDITION: the fixture must actually reach the SVG branch AND draw
	// something. A graph that produced an empty or absent SVG would make every
	// assertion below vacuously true.
	_, st, ok := buildWorkflowGraphSVG([]byte(twoNodeUIGraph))
	if !ok {
		t.Fatal("fixture: the graph did not render as an SVG at all")
	}
	if st.DrawnNodes == 0 || st.DrawnLinks == 0 {
		t.Fatalf("fixture: the renderer drew nodes=%d links=%d; it must draw both for the "+
			"accessible name to have anything to report", st.DrawnNodes, st.DrawnLinks)
	}

	out := renderGraphNode(t, workflowGraphSection([]byte(twoNodeUIGraph), store.WorkflowFormatUI))
	doc := a11yParse(t, out)

	var svgs []*html.Node
	for _, n := range descendants(doc) {
		if n.Data == "svg" {
			svgs = append(svgs, n)
		}
	}
	if len(svgs) != 1 {
		t.Fatalf("fixture: want exactly 1 <svg> in the graph section, got %d", len(svgs))
	}
	svg := svgs[0]
	if role := a11yAttr(svg, "role"); role != "img" {
		t.Fatalf("fixture: the <svg> carries role=%q, not \"img\" — svg-img-alt would not "+
			"apply and this guard would prove nothing", role)
	}

	// THE ASSERTION: an accessible name from any of the three sources axe accepts.
	name := a11yAttr(svg, "aria-label")
	source := "aria-label"
	if strings.TrimSpace(name) == "" {
		if ref := a11yAttr(svg, "aria-labelledby"); ref != "" {
			if target := a11yByID(t, doc, ref); target != nil {
				name, source = a11yText(target), "aria-labelledby"
			}
		}
	}
	if strings.TrimSpace(name) == "" {
		// An SVG <title> names the graphic only when it is the FIRST child.
		if kids := a11yElementChildren(svg); len(kids) > 0 && kids[0].Data == "title" {
			name, source = a11yText(kids[0]), "<title>"
		}
	}
	if strings.TrimSpace(name) == "" {
		t.Fatalf("the workflow graph <svg> has role=\"img\" with NO accessible name.\n"+
			"role=img hides the SVG's contents from assistive tech, so an unnamed one "+
			"announces nothing at all (axe svg-img-alt, SERIOUS). Give it an aria-label, "+
			"an aria-labelledby, or a <title> first child.\nsvg: %s", a11yDescribe(svg))
	}

	// The name must SAY something — a name that is merely the word "image", or that
	// only repeats the scroll container's operating instructions, is not a name.
	region := (*html.Node)(nil)
	for _, n := range descendants(doc) {
		if a11yAttr(n, "role") == "region" {
			region = n
			break
		}
	}
	if region == nil {
		t.Fatal("fixture: the scrollable graph container (role=region) is gone; this guard " +
			"also pins that the two names stay distinct")
	}
	regionName := a11yAttr(region, "aria-label")
	if regionName == "" {
		t.Fatal("fixture: the graph container has no aria-label to compare against")
	}
	if strings.EqualFold(strings.TrimSpace(name), strings.TrimSpace(regionName)) {
		t.Errorf("the <svg> (%s) and its scroll container announce the IDENTICAL name %q — "+
			"that is a duplicated announcement, not two pieces of information. The container "+
			"should say how to OPERATE the region; the image should say what it DEPICTS.",
			source, name)
	}
}
