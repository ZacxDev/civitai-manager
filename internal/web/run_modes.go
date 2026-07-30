package web

import (
	"encoding/json"
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

// runModesContainerID is the STABLE container holding the multi-mode picker. It is
// rendered once with the page and never swapped, so every run control can
// hx-include its selects from anywhere in the document (including from inside
// #run-status, which IS swapped).
const runModesContainerID = "run-modes"

// runModesInclude is the hx-include selector every run control uses to carry the
// current mode picks along with its own request. A page with no picker simply
// matches nothing.
const runModesInclude = "#" + runModesContainerID + " select"

// runParamsContainerID is the STABLE container holding the "Parameters" panel. The
// mode picker re-fetches it on change (a bypassed node exposes no editable inputs,
// so the panel is empty until a mode is chosen); its innerHTML is swapped, never
// the node itself.
const runParamsContainerID = "run-params"

// modeChoiceField is the single form field the picker submits. Its VALUE carries
// both halves of the choice ("<selector key>:<group index>"), so there is no
// parallel-array alignment to get wrong (see the warning in run_params.go).
const modeChoiceField = "mode_key"

// parseModeChoices reads the mode_key form values into the selector→mode map
// runOptions.ModeSelection wants. It keeps ONLY keys that DetectModeSelectors
// currently surfaces for THIS workflow, so a caller can never name a group the
// author did not wire into an exclusive switch. comfy.ApplyModeSelection re-derives
// the accepted set again on its own, so this is a lenient parse backed by a
// structural guarantee downstream.
func parseModeChoices(form url.Values, wf *store.Workflow) map[string]string {
	vals := form[modeChoiceField]
	if len(vals) == 0 || wf == nil || wf.Format != store.WorkflowFormatUI {
		return nil
	}
	allowed := map[string]string{} // mode key → owning selector key
	for _, sel := range comfy.DetectModeSelectors(json.RawMessage(wf.Graph)) {
		for _, m := range sel.Modes {
			allowed[m.Key] = sel.Key
		}
	}
	if len(allowed) == 0 {
		return nil
	}
	out := map[string]string{}
	for _, v := range vals {
		v = strings.TrimSpace(v)
		selKey, ok := allowed[v]
		if !ok {
			continue // "" (keep as saved), unknown, or hostile
		}
		if _, dup := out[selKey]; dup {
			continue // one pick per selector; a repeat is a malformed request
		}
		out[selKey] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// runModesPanel renders the multi-mode picker for a template workflow that carries
// one or more mutually-exclusive rgthree group switches. It returns an EMPTY stable
// container for an ordinary workflow — the container still exists so the
// hx-include selector is uniform, but nothing is shown and nothing is submitted.
//
// The picker is deliberately NOT a submit: the run controls (Run, Run again, Run
// with these parameters, Run with selected options, Install…) all hx-include these
// selects, so a pick applies to whichever run the user starts next. Changing a pick
// re-fetches the Parameters panel, because which parameters exist depends on which
// pipeline is enabled.
func runModesPanel(wf *store.Workflow, csrf string) g.Node {
	return runModesPanelWith(wf, csrf, nil, false)
}

// runModesPanelSelected is runModesPanel with the pre-selected pick OVERRIDDEN,
// used when opening a run-preset tab: the preset's stored mode pre-selects the
// picker, after which the picker is authoritative again.
func runModesPanelSelected(wf *store.Workflow, csrf string, selected map[string]string) g.Node {
	return runModesPanelWith(wf, csrf, selected, false)
}

// runModesPanelWith renders the picker with an optional pre-selection override and
// an optional hx-swap-oob marker.
//
// oob=true makes this an OUT-OF-BAND swap of the whole #run-modes container so a
// tab-open response can pre-select the preset's mode in the same round trip; it
// returns nil for a workflow with no selectors, because swapping an empty
// container over a picker that never existed is pure noise. In-band (oob=false)
// it still returns the EMPTY stable container for an ordinary workflow, so the
// hx-include selector stays uniform.
func runModesPanelWith(wf *store.Workflow, csrf string, selected map[string]string, oob bool) g.Node {
	sels := detectWorkflowModes(wf)
	if len(sels) == 0 {
		if oob {
			return nil
		}
		return h.Div(h.ID(runModesContainerID))
	}
	id := strconv.FormatInt(wf.ID, 10)
	blocks := make([]g.Node, 0, len(sels))
	for i, sel := range sels {
		blocks = append(blocks, runModeSelect(id, csrf, i, sel, selected[sel.Key]))
	}
	attrs := []g.Node{
		h.ID(runModesContainerID),
		h.Class("mt-3 rounded border border-slate-800 p-3 space-y-3"),
	}
	if oob {
		attrs = append(attrs, hx("swap-oob", "true"))
	}
	return h.Div(append(attrs,
		h.Div(h.Class("text-xs font-semibold text-slate-200"), g.Text("Workflow mode")),
		h.P(h.Class("text-xs text-slate-400"),
			g.Text(runModesBlurb(sels)),
		),
		g.Group(blocks),
	)...)
}

// runModesBlurb explains the picker WITHOUT overclaiming. The 581 shape ships
// every pipeline switched off (so the run genuinely cannot work until the user
// picks); the 588 shape ships one already live (so the picker is a swap, not a
// prerequisite). Saying "ships with all of them switched off" on the latter would
// be plainly false, and the user is looking at a pre-selected dropdown that
// contradicts it.
func runModesBlurb(sels []comfy.ModeSelector) string {
	allOff := true
	for _, s := range sels {
		if s.Selected() != "" {
			allOff = false
		}
	}
	if allOff {
		return "This workflow packs several pipelines into one file and ships with all of them " +
			"switched off. Pick the one to run — the choice applies to this run only, the saved " +
			"workflow is unchanged."
	}
	return "This workflow packs several mutually-exclusive pipelines into one file. " +
		"The one its author left enabled is selected; changing it applies to this run only, " +
		"the saved workflow is unchanged."
}

// runModeSelect renders one selector's <select>. The currently-live mode (if any) is
// pre-selected; when the author shipped every mode off, the placeholder is selected
// and the run proceeds exactly as it does today until the user picks.
// override, when non-empty, pre-selects that mode instead of the one the STORED
// graph has enabled — the run-preset tab-open case. It is only ever a key the
// caller already validated against DetectModeSelectors.
func runModeSelect(wfID, csrf string, idx int, sel comfy.ModeSelector, override string) g.Node {
	fid := "cm-mode-" + strconv.Itoa(idx)
	current := sel.Selected()
	if override != "" {
		current = override
	}
	opts := make([]g.Node, 0, len(sel.Modes)+1)
	if current == "" {
		opts = append(opts, h.Option(h.Value(""), g.Attr("selected"),
			g.Text("Choose a mode…")))
	}
	for _, m := range sel.Modes {
		label := m.Title
		if strings.TrimSpace(label) == "" {
			label = "(untitled group)"
		}
		label += " · " + strconv.Itoa(m.NodeCount) + " nodes"
		attrs := []g.Node{h.Value(m.Key)}
		if m.Key == current {
			attrs = append(attrs, g.Attr("selected"))
		}
		attrs = append(attrs, g.Text(label))
		opts = append(opts, h.Option(attrs...))
	}
	selAttrs := []g.Node{
		dataFlag("civitai-ui-control"),
		h.ID(fid),
		h.Name(modeChoiceField),
		// Re-render the Parameters panel for the chosen pipeline: the editable inputs of
		// a bypassed node are (correctly) not surfaced, so they only appear once a mode
		// is selected. Swaps the innerHTML of the stable #run-params container.
		hx("get", "/workflows/"+wfID+"/run/params"),
		hx("trigger", "change"),
		hx("target", "#"+runParamsContainerID),
		hx("swap", "innerHTML"),
		// Carry the ACTIVE preset id (and only that — the field values would make this
		// GET's query string unbounded) so changing the mode re-renders the tab the
		// user is on instead of snapping back to the first one.
		hx("include", runModesInclude+", #"+presetIDInputID),
	}
	selAttrs = append(selAttrs, opts...)
	_ = csrf // the picker itself changes no state; the GET carries no token
	return h.Div(
		dataAttr("civitai-ui", "text-input"),
		h.Label(dataFlag("civitai-ui-label"), h.For(fid), g.Text(sel.Label)),
		h.Select(selAttrs...),
	)
}

// detectWorkflowModes is the single place the run surfaces ask "is this a multi-mode
// template?". An api-format graph is never one (modes are a UI-graph group concept).
func detectWorkflowModes(wf *store.Workflow) []comfy.ModeSelector {
	if wf == nil || wf.Format != store.WorkflowFormatUI {
		return nil
	}
	return comfy.DetectModeSelectors(json.RawMessage(wf.Graph))
}

// handleWorkflowRunParams re-renders the "Parameters" panel for the currently picked
// mode. GET (no state change, no CSRF), loopback-gated like the other run endpoints.
// The mode choice arrives as ordinary query values and is filtered through
// parseModeChoices, so it can only ever name a real mode of a real selector.
//
// This is the PICKER-CHANGED entry point, so the picker is authoritative and a
// preset's stored mode is NOT preferred here (preferPresetModes=false) — otherwise
// the preset would immediately undo the change the user just made. It carries the
// active preset id so the tab survives the swap. Being a GET it changes nothing:
// values typed since the last save are re-rendered from the stored preset, which
// is unchanged from today (the panel was always re-fetched on a mode change).
func (s *Server) handleWorkflowRunParams(w http.ResponseWriter, r *http.Request) {
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
	q := r.URL.Query()
	activeID, _ := strconv.ParseInt(strings.TrimSpace(q.Get(presetIDField)), 10, 64)
	view := s.buildPresetView(r.Context(), wf, activeID, parseModeChoices(q, wf), false)
	s.render(w, http.StatusOK, runParamsBody(wf, s.csrf, view))
}

// runParamsBody is the #run-params response body. It must be a REAL node — an
// empty div clears the container honestly when the graph exposes nothing editable
// (an api graph, or a template before a mode is picked).
func runParamsBody(wf *store.Workflow, csrf string, v presetTabView) g.Node {
	if panel := runPresetPanel(wf, csrf, v); panel != nil {
		return panel
	}
	return h.Div()
}

// runParametersPanelForModes renders the Parameters panel against the graph the run
// would actually convert: the stored graph with the chosen mode applied. An empty
// choice set is the plain stored graph, so ordinary workflows are unaffected.
//
// It is the PRESET-FREE rendering path, kept for callers (and tests) that have no
// preset context.
func runParametersPanelForModes(wf *store.Workflow, csrf string, choices map[string]string) g.Node {
	view := wf
	if len(choices) > 0 && wf.Format == store.WorkflowFormatUI {
		cp := *wf
		cp.Graph = string(comfy.ApplyModeSelection(json.RawMessage(wf.Graph), choices))
		view = &cp
	}
	// runParametersPanel yields nil when the graph exposes nothing editable (an
	// api graph, or a template before a mode is picked). This is a RESPONSE body, so
	// it must be a real node — an empty div clears the container honestly.
	if panel := runParametersPanel(view, csrf); panel != nil {
		return panel
	}
	return h.Div()
}
