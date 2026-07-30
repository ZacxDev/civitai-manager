package web

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/ZacxDev/civitai-manager/internal/comfy"
	"github.com/ZacxDev/civitai-manager/internal/store"
	g "maragu.dev/gomponents"
	h "maragu.dev/gomponents/html"
)

// handleWorkflowRunWithParams starts an EPHEMERAL run applying the "Parameters"
// panel's per-(node,input) overrides. Same prologue/gating as the other run
// endpoints (CSRF + loopback), same one-run-at-a-time invariant, and the stored
// workflow is never mutated (the overrides apply to the converted api-graph copy).
func (s *Server) handleWorkflowRunWithParams(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if !s.verifyCSRF(w, r) {
		return
	}
	if !s.gate(w) {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad workflow id", http.StatusBadRequest)
		return
	}
	wf, err := s.store.GetWorkflow(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.renderError(w, "load workflow", err)
		return
	}
	// The mode picks ride along via hx-include, and they are applied FIRST: the
	// Parameters panel for a multi-mode template is rendered against the graph with
	// that mode enabled, so its widget keys only make sense in that same graph.
	modes := parseModeChoices(r.Form, wf)
	opts := runOptions{
		ModeSelection:     modes,
		UIWidgetOverrides: parseWidgetOverridesForModes(r.Form, wf, modes),
	}
	// Attribute the run to the preset tab it was started from (pure labeling —
	// nothing about the run behaves differently). A preset id naming ANOTHER
	// workflow's row is a 404, the same rule the preset CRUD endpoints follow;
	// the implicit tab posts 0 and is simply unattributed.
	if pid := formPresetID(r); pid > 0 {
		p := s.presetOfWorkflow(w, r, wf, pid)
		if p == nil {
			return
		}
		opts.PresetID, opts.PresetName = p.ID, p.Name
	}
	s.startRun(wf, opts)
	s.render(w, http.StatusOK, runStatusFragment(s.runJobState(), id, s.csrf, s.comfyDownloadEligible(), s.nsfwMode()))
}

// parseWidgetOverrides reads the parallel wp_node / wp_widget / wp_value form arrays
// (one triple per Parameters field, index-aligned in DOM order) into an override
// map keyed by (node id, widgets_values index). It keeps ONLY keys that
// DetectRunInputs surfaces for THIS workflow, so a caller can never target a widget
// outside the curated, editable set. The values are applied by
// comfy.ApplyUIWidgetOverrides, which additionally refuses to add slots or rewrite a
// non-scalar one — so this is a lenient parse backed by a structural guarantee
// downstream.
func parseWidgetOverrides(form url.Values, wf *store.Workflow) map[comfy.UIWidgetKey]string {
	return parseWidgetOverridesForModes(form, wf, nil)
}

// parseWidgetOverridesForModes is parseWidgetOverrides against the graph the run
// will ACTUALLY convert. For a multi-mode template the stored graph has every
// pipeline bypassed, and DetectRunInputs (correctly) surfaces nothing from a
// bypassed node — so the allow-list has to be derived from the mode-applied copy,
// exactly the graph the panel was rendered from. An empty choice set is the stored
// graph, so ordinary workflows are byte-for-byte unaffected.
func parseWidgetOverridesForModes(form url.Values, wf *store.Workflow, modes map[string]string) map[comfy.UIWidgetKey]string {
	graph := []byte(wf.Graph)
	if len(modes) > 0 && wf.Format == store.WorkflowFormatUI {
		graph = comfy.ApplyModeSelection(graph, modes)
	}
	return parseWidgetOverridesAgainst(form, graph)
}

func parseWidgetOverridesAgainst(form url.Values, graph []byte) map[comfy.UIWidgetKey]string {
	nodes, widgets, values := form["wp_node"], form["wp_widget"], form["wp_value"]
	n := len(nodes)
	if len(widgets) < n {
		n = len(widgets)
	}
	if len(values) < n {
		n = len(values)
	}
	if n == 0 {
		return nil
	}
	allowed := make(map[comfy.UIWidgetKey]bool)
	for _, ri := range comfy.DetectRunInputs(graph, nil) {
		allowed[comfy.UIWidgetKey{NodeID: ri.NodeID, Widget: ri.WidgetIndex}] = true
	}
	out := make(map[comfy.UIWidgetKey]string, n)
	conflicted := map[comfy.UIWidgetKey]bool{}
	for i := 0; i < n; i++ {
		widx, err := strconv.Atoi(widgets[i])
		if err != nil {
			continue
		}
		key := comfy.UIWidgetKey{NodeID: nodes[i], Widget: widx}
		if !allowed[key] {
			continue // not a curated, editable widget for this workflow
		}
		// The panel renders ONE field per key (DetectRunInputs dedupes), so a repeated
		// key is a malformed/hand-built request. If the repeats disagree there is no
		// non-arbitrary winner — assigning one would silently discard the other, which
		// is exactly the last-wins bug the dedupe exists to prevent. Drop the key.
		if prev, dup := out[key]; dup {
			if prev != values[i] {
				conflicted[key] = true
			}
			continue
		}
		out[key] = values[i]
	}
	for key := range conflicted {
		delete(out, key)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// runParametersPanel renders the run "Parameters" panel for a workflow with NO
// preset context: the IMPLICIT tab, seeded from the graph's current values.
//
// It is a thin wrapper over runPresetPanel — the SAME renderer production uses —
// deliberately, so a test asserting this markup is asserting live markup. A second,
// parallel renderer would be exactly the "green tests over a dead production path"
// trap this repo keeps paying for.
//
// It returns nil when the graph exposes no editable inputs (an api-format graph, or
// a UI graph with none of the curated nodes) so the panel is simply absent.
//
// Sampler/scheduler render as free-text inputs here: choices come from /object_info,
// which the render path does not fetch (offline/no slow network in render), so they
// degrade to text — an invalid enum is caught by preflight's existing
// incompatible-options flow. DetectRunInputs still accepts object_info elsewhere, so
// object_info-backed selects can be added without changing the wiring.
func runParametersPanel(wf *store.Workflow, csrf string) g.Node {
	return runPresetPanel(wf, csrf, implicitPresetView(wf, nil))
}

// runParamField renders one Parameters control, preceded by the hidden wp_node /
// wp_widget fields that pair (index-aligned in DOM order) with the wp_value control —
// the same parallel-array shape parseWidgetOverrides reads. Every pre-filled value is
// escaped (g.Text for textareas, attribute escaping for value=).
//
// The three parallel arrays are paired BY POSITION, which holds only because every
// control here always submits exactly one wp_value (all five kinds are a single
// input/textarea/select). A future control that can submit zero values (an unchecked
// checkbox) or several would shift the alignment — such a control must carry its key
// in the value itself (or use indexed field names) rather than rely on this pairing.
func runParamField(idx int, ri comfy.RunInput) g.Node {
	return runParamFieldValue(idx, ri, ri.Current)
}

// runParamFieldValue is runParamField with the pre-filled value supplied, so a run
// PRESET can show its saved value while every other property of the control (label,
// kind, choices, origin note) still comes from the LIVE graph. Keeping the live
// RunInput authoritative for everything except the value is what makes a
// retargeted slot visible: the user sees the graph's current label beside the
// value, not the label the preset remembered.
func runParamFieldValue(idx int, ri comfy.RunInput, value string) g.Node {
	fid := "cm-param-" + strconv.Itoa(idx)
	hidden := []g.Node{
		h.Input(h.Type("hidden"), h.Name("wp_node"), h.Value(ri.NodeID)),
		h.Input(h.Type("hidden"), h.Name("wp_widget"), h.Value(strconv.Itoa(ri.WidgetIndex))),
	}

	var control g.Node
	switch ri.Kind {
	case comfy.RunInputText:
		control = h.Textarea(
			dataFlag("civitai-ui-control"), h.ID(fid), h.Name("wp_value"), h.Rows("3"),
			g.Text(value),
		)
	case comfy.RunInputSelect:
		if len(ri.Choices) > 0 {
			control = paramSelect(fid, ri.Choices, value)
		} else {
			control = h.Input(dataFlag("civitai-ui-control"), h.ID(fid),
				h.Type("text"), h.Name("wp_value"), h.Value(value))
		}
	case comfy.RunInputSeed:
		control = h.Div(h.Class("flex items-center gap-2"),
			h.Input(dataFlag("civitai-ui-control"), h.ID(fid), h.Type("number"),
				h.Name("wp_value"), g.Attr("step", "1"), h.Value(value)),
			civButton("outline", "sm", []g.Node{
				h.Type("button"),
				g.Attr("onclick", "cmRandomSeed('"+fid+"')"),
				g.Attr("aria-label", "Randomize seed"),
			}, g.Text("🎲")),
		)
	case comfy.RunInputInt:
		control = h.Input(dataFlag("civitai-ui-control"), h.ID(fid), h.Type("number"),
			h.Name("wp_value"), g.Attr("step", "1"), h.Value(value))
	case comfy.RunInputFloat:
		control = h.Input(dataFlag("civitai-ui-control"), h.ID(fid), h.Type("number"),
			h.Name("wp_value"), g.Attr("step", "any"), h.Value(value))
	default:
		control = h.Input(dataFlag("civitai-ui-control"), h.ID(fid), h.Type("text"),
			h.Name("wp_value"), h.Value(value))
	}

	// Size/shape the field by its KIND — never by its label, which comes from the
	// graph author's node titles and says nothing about how long the value is. A
	// seed, a step count and a multi-line prompt are three different-sized values and
	// must not be three identical boxes; the sizing itself lives in .cm-param-* in
	// app.css.
	//
	// 🔴 PRESENTATION ONLY. parseWidgetOverrides pairs the parallel
	// wp_node/wp_widget/wp_value arrays BY DOM POSITION, so a class may change which
	// grid track a field OCCUPIES but must never change the order fields are emitted
	// in. CSS grid auto-placement follows source order, so the submitted form is
	// byte-identical to before.
	//
	// The classes are assembled from LITERALS into a local (rather than returned by a
	// helper) so the class-coverage guard in class_coverage_web_test.go can still
	// resolve them — a helper call is a blind spot it cannot see through.
	kindClass := "cm-param"
	switch ri.Kind {
	case comfy.RunInputText:
		kindClass += " cm-param-text"
	case comfy.RunInputSelect:
		kindClass += " cm-param-select"
	case comfy.RunInputSeed:
		kindClass += " cm-param-seed"
	case comfy.RunInputInt:
		kindClass += " cm-param-int"
	case comfy.RunInputFloat:
		kindClass += " cm-param-float"
	default:
		kindClass += " cm-param-other"
	}

	return h.Div(
		dataAttr("civitai-ui", "text-input"),
		h.Class(kindClass),
		h.Label(dataFlag("civitai-ui-label"), h.For(fid), g.Text(ri.Label)),
		g.Group(hidden),
		control,
		// When the value lives on an upstream node (a widget converted to an input and
		// wired from a primitive/custom node), say so — the edit lands there, not on the
		// node the label names, and that indirection is otherwise invisible.
		g.If(ri.Resolved, h.P(h.Class("text-xs text-slate-500"), g.Text(runParamOrigin(ri)))),
	)
}

// runParamOrigin describes where an edit will actually land: the holding node, the
// exact widget slot, every pass-through hop the resolver followed, and how many
// curated inputs this one widget drives (a shared primitive is ONE field, so say so
// rather than letting the user think the other consumer is unedited).
func runParamOrigin(ri comfy.RunInput) string {
	s := "from #" + ri.NodeID + " " + ri.SourceClassType + " widget " + strconv.Itoa(ri.WidgetIndex)
	if len(ri.SourceVia) > 0 {
		s += " (via " + strings.Join(ri.SourceVia, " → ") + ")"
	}
	if ri.Consumers > 1 {
		s += " · drives " + strconv.Itoa(ri.Consumers) + " inputs"
	}
	return s
}

// paramSelect renders an enum <select name="wp_value"> for a sampler/scheduler input.
// The current value is pre-selected; if it is not among the live choices it is
// prepended as a "(current)" option so the run defaults to the SAVED value rather than
// silently switching to the first choice.
func paramSelect(id string, choices []string, selected string) g.Node {
	opts := make([]g.Node, 0, len(choices)+1)
	found := false
	for _, c := range choices {
		attrs := []g.Node{h.Value(c)}
		if c == selected {
			attrs = append(attrs, g.Attr("selected"))
			found = true
		}
		attrs = append(attrs, g.Text(c))
		opts = append(opts, h.Option(attrs...))
	}
	if !found && selected != "" {
		opts = append([]g.Node{h.Option(h.Value(selected), g.Attr("selected"),
			g.Text(selected+" (current)"))}, opts...)
	}
	sel := append([]g.Node{dataFlag("civitai-ui-control"), h.ID(id), h.Name("wp_value")}, opts...)
	return h.Select(sel...)
}

// runParamsScript is the tiny, self-contained (offline/no-CDN) randomize-seed helper.
// Defined idempotently (a plain function (re)definition, no listeners) so re-emitting
// it after an htmx swap never stacks handlers. The seed range stays within 2^53 so
// the number field carries it exactly.
func runParamsScript() g.Node {
	const js = `
function cmRandomSeed(fid){
  var el = document.getElementById(fid);
  if(!el){ return; }
  el.value = String(Math.floor(Math.random() * 1000000000000000));
}
`
	return h.Script(g.Raw(js))
}
