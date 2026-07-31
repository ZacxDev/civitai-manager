package web

import (
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/comfy"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// splitAtFirstDetails returns the markup BEFORE the first <details> and the
// markup from that <details> onward. The Generate card's only disclosure is the
// advanced-parameter one, so "before" is exactly the always-visible region.
func splitAtFirstDetails(t *testing.T, html string) (visible, collapsed string) {
	t.Helper()
	i := strings.Index(html, "<details")
	if i < 0 {
		t.Fatalf("no <details> in the panel at all — the advanced split did not render:\n%s", html)
	}
	return html[:i], html[i:]
}

// TestPromptInputsAreVisibleOnLoad is the whole point of the change: the page you
// land on to RUN a workflow must show the prompt without a click.
//
// It used to be one <details> holding the summary, the tab strip, EVERY field and
// the Run button, closed unless the workflow had saved presets — so the default
// state of the generate card was "the prompt you want to type in is hidden, and so
// is the button that runs it".
//
// The assertion is positional, not merely a substring: a `Contains` check on the
// prompt would have passed the whole time, because the prompt was always in the
// markup — just inside a closed disclosure. So the prompt textarea must appear
// BEFORE the first <details>, and an advanced control must appear after it.
func TestPromptInputsAreVisibleOnLoad(t *testing.T) {
	wf := &store.Workflow{ID: 7, Name: "t2i", Format: store.WorkflowFormatUI, Graph: uiTxt2imgGraph}
	got := renderString(t, runParametersPanel(wf, "tok"))

	visible, collapsed := splitAtFirstDetails(t, got)

	// The prompt: its pre-filled text and its <textarea> are outside the disclosure.
	if !strings.Contains(visible, "a scenic mountain") {
		t.Errorf("the prompt value is not in the always-visible region:\n%s", visible)
	}
	if !strings.Contains(visible, "<textarea") {
		t.Errorf("the prompt textarea is not in the always-visible region:\n%s", visible)
	}
	// The label, so it is a usable field and not a naked box.
	if !strings.Contains(visible, "Prompt (Positive)") {
		t.Errorf("the prompt label is not in the always-visible region:\n%s", visible)
	}
	// The Run CTA rode inside the same old disclosure. It must be reachable too.
	if !strings.Contains(got, "Run with these parameters") {
		t.Fatalf("the run CTA vanished:\n%s", got)
	}

	// The advanced knobs are the ones that collapse. The seed is the clearest case:
	// nobody types a seed to get started.
	if !strings.Contains(collapsed, `value="156680208700286"`) {
		t.Errorf("the seed should be inside the advanced disclosure:\n%s", collapsed)
	}
	if strings.Contains(visible, `value="156680208700286"`) {
		t.Errorf("the seed is still in the always-visible region:\n%s", visible)
	}
	if !strings.Contains(collapsed, "Advanced parameters") {
		t.Errorf("the disclosure must name itself:\n%s", collapsed)
	}
	// Closed by default for a workflow with no saved presets — the whole point is
	// that only the advanced half is behind a click.
	if strings.Contains(collapsed[:strings.Index(collapsed, ">")+1], " open") {
		t.Errorf("the advanced disclosure should start closed with no saved presets:\n%s", collapsed)
	}
}

// TestAdvancedSplitIsByKindNotByLabel pins WHAT the split keys on.
//
// A label-based split ("does the label contain 'prompt'?") would be exactly the
// prose/keyword heuristic this repo keeps paying for: labels are the graph
// author's node titles, so a node titled "Positive" or "描述" or nothing at all
// would silently fall on the wrong side. The kind is assigned by the detector.
func TestAdvancedSplitIsByKindNotByLabel(t *testing.T) {
	fields := []comfy.PresetField{
		// Deliberately mislabelled in both directions: a text input whose label says
		// nothing about prompting, and an int whose label says "prompt".
		{Input: comfy.RunInput{Kind: comfy.RunInputText, Label: "描述"}},
		{Input: comfy.RunInput{Kind: comfy.RunInputSeed, Label: "prompt seed"}},
		{Input: comfy.RunInput{Kind: comfy.RunInputText, Label: ""}},
		{Input: comfy.RunInput{Kind: comfy.RunInputInt, Label: "Prompt steps"}},
		{Input: comfy.RunInput{Kind: comfy.RunInputSelect, Label: "sampler"}},
		{Input: comfy.RunInput{Kind: comfy.RunInputFloat, Label: "cfg"}},
	}
	prompt, advanced := runParamFieldSplit(fields)

	if want := []int{0, 2}; !equalInts(prompt, want) {
		t.Errorf("prompt indices = %v, want %v (RunInputText only, regardless of label)", prompt, want)
	}
	if want := []int{1, 3, 4, 5}; !equalInts(advanced, want) {
		t.Errorf("advanced indices = %v, want %v", advanced, want)
	}
	// Relative order inside each half is preserved and every field lands in exactly
	// one half — the two properties the wp_* pairing depends on.
	if len(prompt)+len(advanced) != len(fields) {
		t.Errorf("%d fields split into %d + %d — a field was lost or duplicated",
			len(fields), len(prompt), len(advanced))
	}
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// wpTriples extracts the wp_node / wp_widget / wp_value form fields from rendered
// markup IN DOCUMENT ORDER — i.e. exactly what a browser would submit — so the
// pairing can be checked against the real parser rather than against an assumption.
//
// wp_value appears in three control shapes (textarea / number input / text input),
// so both the textarea body and the value= attribute are handled.
var wpNameRe = regexp.MustCompile(`name="wp_(node|widget|value)"`)

func wpTriples(t *testing.T, html string) url.Values {
	t.Helper()
	out := url.Values{}
	for _, m := range wpNameRe.FindAllStringSubmatchIndex(html, -1) {
		which := html[m[2]:m[3]]
		// Find the tag this attribute belongs to.
		open := strings.LastIndex(html[:m[0]], "<")
		if open < 0 {
			t.Fatalf("malformed markup near offset %d", m[0])
		}
		end := strings.Index(html[m[1]:], ">")
		if end < 0 {
			t.Fatalf("unterminated tag near offset %d", m[1])
		}
		tagRest := html[m[1] : m[1]+end]

		if strings.HasPrefix(html[open:], "<textarea") {
			body := html[m[1]+end+1:]
			close := strings.Index(body, "</textarea>")
			if close < 0 {
				t.Fatalf("unterminated textarea near offset %d", m[1])
			}
			out.Add("wp_"+which, body[:close])
			continue
		}
		vi := strings.Index(tagRest, `value="`)
		if vi < 0 {
			// A <select> carries its value on the selected <option>; the fixtures used
			// here never produce one, so this is a real defect if it fires.
			t.Fatalf("no value= for wp_%s in tag %q", which, tagRest)
		}
		rest := tagRest[vi+len(`value="`):]
		q := strings.Index(rest, `"`)
		if q < 0 {
			t.Fatalf("unterminated value= in tag %q", tagRest)
		}
		out.Add("wp_"+which, rest[:q])
	}
	return out
}

// TestAdvancedSplitKeepsWidgetPairing is the guard the split's doc comment points
// at, and the reason it is safe to partition the fields at all.
//
// parseWidgetOverrides pairs the three parallel arrays BY POSITION. That survives a
// partition only because each control emits exactly ONE entry in each array, so a
// permutation permutes all three identically. This test does not take that on
// faith: it reads the arrays out of the rendered DOM in document order — what a
// browser would actually submit — and runs them through the PRODUCTION parser,
// asserting each widget still receives its own value.
func TestAdvancedSplitKeepsWidgetPairing(t *testing.T) {
	wf := &store.Workflow{ID: 7, Format: store.WorkflowFormatUI, Graph: uiTxt2imgGraph}
	got := renderString(t, runParametersPanel(wf, "tok"))

	form := wpTriples(t, got)
	nodes, widgets, values := form["wp_node"], form["wp_widget"], form["wp_value"]

	// The fixture must reach the interesting case: fields on BOTH sides of the
	// split, or the partition is not being exercised at all.
	visible, collapsed := splitAtFirstDetails(t, got)
	if !strings.Contains(visible, `name="wp_value"`) || !strings.Contains(collapsed, `name="wp_value"`) {
		t.Fatalf("the fixture does not populate both halves of the split — this test proves nothing")
	}

	if len(nodes) != len(widgets) || len(nodes) != len(values) {
		t.Fatalf("the three parallel arrays are not the same length: %d node / %d widget / %d value —"+
			" a field emitted an unbalanced number of entries:\n%v", len(nodes), len(widgets), len(values), form)
	}
	if len(nodes) < 4 {
		t.Fatalf("only %d fields rendered — the fixture is too thin to detect a mispairing", len(nodes))
	}

	// The production parser, against the same graph the panel was rendered from.
	overrides := parseWidgetOverridesAgainst(form, []byte(uiTxt2imgGraph))
	if len(overrides) != len(nodes) {
		t.Fatalf("parsed %d overrides from %d rendered fields — the arrays no longer line up:\n%v\n%v",
			len(overrides), len(nodes), form, overrides)
	}
	for i := range nodes {
		key := comfy.UIWidgetKey{NodeID: nodes[i], Widget: atoiOrFatal(t, widgets[i])}
		if got, ok := overrides[key]; !ok || got != values[i] {
			t.Errorf("field %d (%s widget %s) parsed as %q (present=%v), want %q",
				i, nodes[i], widgets[i], got, ok, values[i])
		}
	}

	// Spot-check two values that came from opposite halves of the split, so a
	// wholesale mispairing cannot hide behind a self-consistent extraction.
	if overrides[comfy.UIWidgetKey{NodeID: "6", Widget: 0}] != "a scenic mountain" {
		t.Errorf("the prompt (visible half) did not survive the round trip: %v", overrides)
	}
	if overrides[comfy.UIWidgetKey{NodeID: "3", Widget: 0}] != "156680208700286" {
		t.Errorf("the seed (collapsed half) did not survive the round trip: %v", overrides)
	}
}

func atoiOrFatal(t *testing.T, s string) int {
	t.Helper()
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			t.Fatalf("non-numeric widget index %q", s)
		}
		n = n*10 + int(r-'0')
	}
	return n
}
