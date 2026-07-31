package web

import (
	"context"
	"os"
	"strings"
	"testing"

	"golang.org/x/net/html"

	"github.com/ZacxDev/civitai-manager/internal/comfy"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// This file is the regression guard for issue #28: an hx-include selector that
// resolves to ZERO elements.
//
// htmx does not fail such a request, it LOGS — `we()` in the vendored bundle:
//
//	const r = p(e, n);
//	if (r.length === 0) { O('The selector "' + n + '" on ' + t + " returned no matches!"); return [ve] }
//
// …which the ux-audit harness picks up as a first-party console error on every
// walk. The offending selector was "#run-modes select": #run-modes is a STABLE
// container rendered on every workflow, but the <select> inside it exists only for
// a multi-mode template, so every ordinary workflow logged the error.
//
// The tests below pin the two halves of the fix:
//
//   - selectorsAlwaysMatch: on the run panel, every hx-include selector resolves to
//     at least one element — in the single-mode / missing-models state that produced
//     the report, and in the multi-mode state.
//   - the container include is EQUIVALENT to the old descendant one: a multi-mode
//     page still delivers the mode_key <select> through it.

// ---------------------------------------------------------------------------
// A minimal, honest resolver for the selector shapes this codebase actually uses
// ---------------------------------------------------------------------------

// htmxExtendedToken reports whether tok is one of htmx's EXTENDED (non-CSS) tokens,
// which `p()` resolves through its own token table rather than querySelectorAll.
// They are relative to the triggering element, which a static render cannot model,
// so they are skipped rather than guessed at.
func htmxExtendedToken(tok string) bool {
	for _, p := range []string{"closest ", "find ", "next ", "previous ", "global "} {
		if strings.HasPrefix(tok, p) {
			return true
		}
	}
	switch tok {
	case "this", "next", "previous", "nextElementSibling", "previousElementSibling",
		"document", "window", "body", "root", "host":
		return true
	}
	return false
}

// simpleMatch matches one compound selector of the form `tag`, `#id` or `tag#id`.
// That is the whole grammar used by this package's hx-include attributes; anything
// richer makes the test fail loudly (see resolveSelector) rather than silently pass.
func simpleMatch(n *html.Node, sel string) (ok, understood bool) {
	if n.Type != html.ElementNode {
		return false, true
	}
	tag, id := sel, ""
	if i := strings.Index(sel, "#"); i >= 0 {
		tag, id = sel[:i], sel[i+1:]
	}
	if strings.ContainsAny(sel, ".[]:>+~*,") || (id == "" && tag == "") {
		return false, false
	}
	if tag != "" && n.Data != tag {
		return false, true
	}
	if id != "" {
		got := ""
		for _, a := range n.Attr {
			if a.Key == "id" {
				got = a.Val
			}
		}
		if got != id {
			return false, true
		}
	}
	return true, true
}

// descendants returns every element node strictly below root.
func descendants(root *html.Node) []*html.Node {
	var out []*html.Node
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if c.Type == html.ElementNode {
				out = append(out, c)
			}
			walk(c)
		}
	}
	walk(root)
	return out
}

// resolveSelector applies a descendant-combinator CSS selector (space-separated
// compound selectors) to the document, returning the matched elements. understood
// is false when the selector uses grammar this resolver does not model — the caller
// treats that as a test bug, never as a pass.
func resolveSelector(t *testing.T, doc *html.Node, sel string) (matches []*html.Node, understood bool) {
	t.Helper()
	cur := []*html.Node{doc}
	for _, part := range strings.Fields(sel) {
		var next []*html.Node
		for _, base := range cur {
			for _, d := range descendants(base) {
				ok, und := simpleMatch(d, part)
				if !und {
					return nil, false
				}
				if ok {
					next = append(next, d)
				}
			}
		}
		cur = next
		if len(cur) == 0 {
			break
		}
	}
	return cur, true
}

// hxIncludeAttrs returns every hx-include value in the document, paired with a short
// description of the element carrying it (for a legible failure).
func hxIncludeAttrs(doc *html.Node) map[string][]string {
	out := map[string][]string{}
	for _, n := range descendants(doc) {
		var inc, id, hxPost, txt string
		for _, a := range n.Attr {
			switch a.Key {
			case "hx-include":
				inc = a.Val
			case "id":
				id = a.Val
			case "hx-post", "hx-get":
				hxPost = a.Val
			}
		}
		if inc == "" {
			continue
		}
		txt = "<" + n.Data + ">"
		if id != "" {
			txt += " id=" + id
		}
		if hxPost != "" {
			txt += " → " + hxPost
		}
		out[inc] = append(out[inc], txt)
	}
	return out
}

// assertIncludesMatch is the core assertion: every hx-include selector in page
// resolves to at least one element, so htmx never logs
// "The selector … returned no matches!".
//
// It deliberately mirrors htmx's OWN error condition, which is per-ATTRIBUTE, not
// per-token: `p()` splits the value on top-level commas, resolves each token and
// UNIONS the results, and `we()` only logs when that union is empty. So a single
// dead token in a comma list is not a defect to assert against here — and one such
// token legitimately exists: the mode picker includes
// "#run-modes, #cm-preset-id-field", and the preset-id field is absent exactly when
// a multi-mode template has no mode picked yet (a bypassed graph exposes no editable
// inputs, so the Parameters panel — and the preset form holding that field — is
// empty). There is no active preset to carry in that state, so the field being
// missing is correct, and #run-modes keeps the attribute non-empty.
//
// A list containing an EXTENDED token ("closest form", …) is skipped: those resolve
// relative to the triggering element at runtime, so a static render cannot prove the
// union is empty and a claim either way would be guesswork.
func assertIncludesMatch(t *testing.T, label, page string) {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(page))
	if err != nil {
		t.Fatalf("%s: parse rendered page: %v", label, err)
	}
	found := hxIncludeAttrs(doc)
	if len(found) == 0 {
		t.Fatalf("%s: no hx-include attributes in the rendered page — the test lost its subject", label)
	}
	checked := 0
	for sel, owners := range found {
		tokens := strings.Split(sel, ",")
		skip, union := false, 0
		for _, tok := range tokens {
			tok = strings.TrimSpace(tok)
			if tok == "" {
				continue
			}
			if htmxExtendedToken(tok) {
				skip = true
				break
			}
			m, understood := resolveSelector(t, doc, tok)
			if !understood {
				t.Fatalf("%s: hx-include token %q uses selector grammar this test cannot model; "+
					"extend simpleMatch rather than dropping the assertion", label, tok)
			}
			union += len(m)
		}
		if skip {
			continue
		}
		checked++
		if union == 0 {
			t.Errorf("%s: hx-include %q (on %s) matches NOTHING in the rendered page — htmx logs "+
				"'The selector %q on hx-include returned no matches!' and carries no value for it (issue #28)",
				label, sel, strings.Join(owners, ", "), sel)
		}
	}
	if checked == 0 {
		t.Fatalf("%s: every hx-include was skipped — the assertion proved nothing", label)
	}
}

// renderMissingModelsRunPanel renders the run panel in the state the ux-audit harness
// captures as `run-missing-models`: a settled preflight failure naming a model file
// that is not installed. wf drives whether the page is single- or multi-mode.
func renderMissingModelsRunPanel(t *testing.T, srv *Server, wf *store.Workflow) string {
	t.Helper()
	v := srv.buildPresetView(context.Background(), wf, 0, nil, true)
	snap := runSnapshot{
		Started: true, WorkflowID: wf.ID, Seq: 4, Phase: runPhaseFailed,
		Message:   "Preflight failed — this workflow references nodes or models that are not installed.",
		Preflight: &comfy.PreflightReport{MissingModels: []string{"alpha-MISSING.safetensors"}},
		MissingModels: []comfy.MissingModel{
			{Filename: "alpha-MISSING.safetensors", Query: "alpha", CivitaiType: "Checkpoint"}},
		MissingResolved: map[string]missingResolution{},
		LibMeta:         map[string]store.LocalModelMeta{},
	}
	return renderString(t, generateSection(wf, snap, "tok", true, true, fullMaturityRange(), v, true, comfyHelperView{}))
}

// TestRunPanelHxIncludesAlwaysMatch is the issue-#28 regression test.
//
// NON-VACUOUS BY CONSTRUCTION: revert runModesInclude to "#run-modes select" and the
// single-mode subtests fail — #run-modes is rendered as an EMPTY container for any
// workflow with no mode selectors, so the descendant selector resolves to zero
// elements, which is exactly the console error the harness reported.
func TestRunPanelHxIncludesAlwaysMatch(t *testing.T) {
	srv := newTestServer(t)

	multi, err := os.ReadFile("../comfy/testdata/wf581_modes_multimode.json")
	if err != nil {
		t.Fatalf("read multi-mode fixture: %v", err)
	}

	for _, tc := range []struct {
		name  string
		graph string
		multi bool
	}{
		// The reported state: the harness seeds a workflow referencing missing models
		// and no mode selectors, so #run-modes is the stable EMPTY container.
		{"single-mode (missing models)", presetUIGraph, false},
		// The state the include exists FOR: the picker must still be reachable.
		{"multi-mode (missing models)", string(multi), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wf := seedPresetWorkflow(t, srv, tc.name, tc.graph)
			page := renderMissingModelsRunPanel(t, srv, wf)

			// Guard the fixture itself: a graph that silently stopped being
			// single-/multi-mode would make this test assert nothing.
			if got := len(detectWorkflowModes(wf)) > 0; got != tc.multi {
				t.Fatalf("fixture arity changed: multi-mode=%v, want %v", got, tc.multi)
			}
			assertIncludesMatch(t, tc.name, page)
		})
	}
}

// TestRunModesIncludeStillDeliversTheModePicker pins the EQUIVALENCE claim behind
// the fix: including the stable container rather than "#run-modes select" must not
// change what is submitted. htmx walks a non-form included element's descendants
// (`querySelectorAll("input, textarea, select")`), so the mode_key <select> is still
// collected — this asserts it really is inside the container the run controls name.
func TestRunModesIncludeStillDeliversTheModePicker(t *testing.T) {
	b, err := os.ReadFile("../comfy/testdata/wf581_modes_multimode.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	wf := &store.Workflow{ID: 42, Format: store.WorkflowFormatUI, Graph: string(b)}
	page := renderString(t, runModesPanel(wf, "tok"))

	doc, err := html.Parse(strings.NewReader(page))
	if err != nil {
		t.Fatalf("parse picker: %v", err)
	}
	// runModesInclude must name the container...
	got, understood := resolveSelector(t, doc, runModesInclude)
	if !understood {
		t.Fatalf("runModesInclude %q is not a plain descendant selector any more", runModesInclude)
	}
	if len(got) != 1 {
		t.Fatalf("runModesInclude %q matched %d elements, want exactly the stable container",
			runModesInclude, len(got))
	}
	// ...and the mode field must live INSIDE it, or the include stops carrying the pick.
	var named []string
	for _, d := range descendants(got[0]) {
		if d.Data != "select" {
			continue
		}
		for _, a := range d.Attr {
			if a.Key == "name" {
				named = append(named, a.Val)
			}
		}
	}
	if len(named) == 0 {
		t.Fatalf("no <select> under %q — the run controls would submit no mode:\n%s",
			runModesInclude, page)
	}
	for _, n := range named {
		if n != modeChoiceField {
			t.Errorf("unexpected field %q under the mode container; the include would now "+
				"carry it into every run request", n)
		}
	}
}

// TestSingleModeRunCarriesNoModeKeyByDesign is the EVIDENCE that issue #28 was a
// console-only defect, not a behavioural one.
//
// The issue text supposed the absent parameter made the server "fall back to
// whatever default it uses". It does not: for a workflow with no mode selectors
// there is no mode to submit, parseModeChoices refuses to produce one even when a
// mode_key IS present, and comfy.ApplyModeSelection returns the graph UNCHANGED for
// an empty selection. So the request the server sees is byte-identical before and
// after the fix — what changed is only that htmx stops logging.
func TestSingleModeRunCarriesNoModeKeyByDesign(t *testing.T) {
	apiWF := &store.Workflow{ID: 1, Format: store.WorkflowFormatAPI,
		Graph: `{"3":{"class_type":"KSampler","inputs":{}}}`}
	uiWF := &store.Workflow{ID: 2, Format: store.WorkflowFormatUI, Graph: plainUIGraph}

	for _, tc := range []struct {
		name string
		wf   *store.Workflow
	}{
		// The exact shape the ux-audit harness seeds (heroWorkflowGraph is API format).
		{"api format", apiWF},
		{"ui single-mode", uiWF},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if n := len(detectWorkflowModes(tc.wf)); n != 0 {
				t.Fatalf("fixture is not single-mode (%d selectors)", n)
			}
			// Absent mode_key — what the run request actually carries today.
			if got := parseModeChoices(nil, tc.wf); got != nil {
				t.Errorf("absent mode_key should parse to nil, got %v", got)
			}
			// Present mode_key — refused all the same, so nothing was being "lost".
			forged := map[string][]string{modeChoiceField: {"4:0", "anything"}}
			if got := parseModeChoices(forged, tc.wf); got != nil {
				t.Errorf("a forged mode_key must not be honoured on a single-mode workflow, got %v", got)
			}
			// And an empty selection leaves the graph untouched — no silent default.
			out := comfy.ApplyModeSelection([]byte(tc.wf.Graph), nil)
			if string(out) != tc.wf.Graph {
				t.Errorf("empty mode selection must return the graph unchanged:\n got %s\nwant %s",
					out, tc.wf.Graph)
			}
		})
	}
}
