package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/ZacxDev/civitai-manager/internal/comfy"
	"github.com/ZacxDev/civitai-manager/internal/store"
	g "maragu.dev/gomponents"
)

// ── Run presets (the run panel's tabs) ───────────────────────────────────────
//
// A preset is a saved, editable RUN-PARAMETER SET for one workflow. Tabs switch
// between them, Fork duplicates one, and unrun drafts survive because switching
// tabs is a POST that persists the outgoing tab's values.
//
// Three rules hold this together and each of them is a decision, not an accident:
//
//  1. UI-FORMAT ONLY. DetectRunInputs returns nothing for an api graph, so an api
//     workflow has no editable parameters and no meaningful preset. It keeps
//     today's single Run button, untouched.
//
//  2. THE MODE PICKER IS THE SOURCE OF TRUTH. #run-modes sits OUTSIDE the preset
//     form and every run control hx-includes it. A preset's stored mode only
//     PRE-SELECTS the picker when the tab is opened (an out-of-band swap of
//     #run-modes), after which the picker wins. That keeps the graph the panel is
//     rendered against identical to the graph the run will convert — rendering
//     against one and running the other is how a template silently converts to an
//     all-bypassed graph and aborts as "nothing to run".
//
//  3. SAVE NEVER SILENTLY ADOPTS. Persisting values is "save my text"; re-stamping
//     graph_hash is "I certify this param set against the current graph". The
//     second needs its own click (adopt_graph=1), mirroring the repo's
//     never-substitute-a-file-silently invariant.

// runPresetFormID is the STABLE id of the preset form. Tab buttons, Fork, Save and
// Delete all sit outside it (or are type=button inside it) and hx-include it by
// this id, so every request carries the current field values.
const runPresetFormID = "run-preset-form"

// runPresetInclude is the hx-include selector every preset control uses: the
// form's fields AND the page-level mode picker (runModesInclude), the same pattern
// every existing run control follows.
const runPresetInclude = "#" + runPresetFormID + ", " + runModesInclude

// presetIDField carries the ACTIVE tab's id on every preset request, so a handler
// knows which preset the posted values belong to. "0" is the implicit unsaved tab.
const presetIDField = "preset_id"

// presetIDInputID is the DOM id of that hidden field. The mode picker hx-includes
// it (and only it) so changing the mode re-renders the tab the user is on without
// dragging every field value into a GET query string.
const presetIDInputID = "cm-preset-id-field"

// presetNameField is the tab-label input; presetAdoptField is the explicit
// "adopt the current graph" confirmation (see rule 3).
const (
	presetNameField  = "preset_name"
	presetAdoptField = "adopt_graph"
	presetFromField  = "from"
)

// maxPresetNameLen bounds the stored tab label. The strip is a single scrolling
// row; an unbounded label is a layout weapon, and the name is untrusted user text.
const maxPresetNameLen = 80

// presetTabView is everything the panel renderer needs for ONE render.
type presetTabView struct {
	// Presets is the workflow's saved presets in tab order.
	Presets []store.RunPreset
	// Active is the preset whose values are shown, or nil for the IMPLICIT tab —
	// the unsaved "Preset 1" a workflow gets before anything is stored, so a page
	// render never has to write to the database.
	Active *store.RunPreset
	// Rec is the reconciled field set + drift report for the active tab.
	Rec comfy.PresetReconciliation
	// Drifted is true when the active preset's stored graph_hash could not be
	// proven equal to the workflow's — the state "Adopt current graph" clears.
	Drifted bool
	// AtCap disables Fork with a reason instead of evicting a preset.
	AtCap bool
	// Notice is a server-authored line rendered above the fields (a cap refusal, a
	// "saved but not adopted" confirmation). Never reflected request input.
	Notice string
	// ModesOOB, when non-nil, is an out-of-band re-render of #run-modes that
	// pre-selects the active preset's stored mode (rule 2).
	ModesOOB map[string]string
	// UIFormat gates the whole tab surface (rule 1).
	UIFormat bool
}

// ActiveID is the active tab's preset id, or 0 for the implicit tab.
func (v presetTabView) ActiveID() int64 {
	if v.Active == nil {
		return 0
	}
	return v.Active.ID
}

// presetLabel is a tab's display label: the user's (untrusted, escaped at render)
// name, or a positional fallback so a blank name is never a blank tab.
func presetLabel(p store.RunPreset, idx int) string {
	if n := strings.TrimSpace(p.Name); n != "" {
		return n
	}
	return "Preset " + strconv.Itoa(idx+1)
}

// ── params codec ─────────────────────────────────────────────────────────────

// presetEntries decodes a preset's stored params blob into reconciler entries.
// Malformed entries (no node id, non-integer widget index) are dropped rather
// than defaulted onto slot 0 — the same rule runOptionsFromParams follows.
func presetEntries(params string) []comfy.PresetEntry {
	snap := parseRunParams(params)
	out := make([]comfy.PresetEntry, 0, len(snap.UIWidgetOverrides))
	for _, e := range snap.UIWidgetOverrides {
		widx, ok := e.widgetIndex()
		if !ok || e.NodeID == "" {
			continue
		}
		out = append(out, comfy.PresetEntry{
			NodeID:    e.NodeID,
			Widget:    widx,
			Value:     e.Value,
			Kind:      comfy.RunInputKind(e.Kind),
			ClassType: e.ClassType,
			InputName: e.InputName,
			Label:     e.Label,
		})
	}
	return out
}

// presetModes decodes a preset's stored mode selection.
func presetModes(params string) map[string]string {
	return parseRunParams(params).ModeSelection
}

// presetParamsWith rewrites a params blob's POSITIONAL families (the widget
// entries and the mode selection) while preserving everything else verbatim.
//
// substitute / option_fixes are NAME-keyed and degrade safely downstream (an
// unknown filename or input name is a no-op, and option fixes are re-validated
// against live object_info inside realRun), so they are carried across a save
// untouched and are never part of the drift report.
func presetParamsWith(prev string, entries []comfy.PresetEntry, modes map[string]string) string {
	snap := parseRunParams(prev)
	snap.UIWidgetOverrides = make([]uiWidgetOverrideEntry, 0, len(entries))
	for _, e := range entries {
		snap.UIWidgetOverrides = append(snap.UIWidgetOverrides, uiWidgetOverrideEntry{
			NodeID:    e.NodeID,
			Widget:    json.RawMessage(strconv.Itoa(e.Widget)),
			Value:     e.Value,
			Kind:      string(e.Kind),
			ClassType: e.ClassType,
			InputName: e.InputName,
			Label:     e.Label,
		})
	}
	// Stable order so the stored blob is comparable between saves.
	sort.Slice(snap.UIWidgetOverrides, func(i, j int) bool {
		a, b := snap.UIWidgetOverrides[i], snap.UIWidgetOverrides[j]
		if a.NodeID != b.NodeID {
			return a.NodeID < b.NodeID
		}
		ai, _ := a.widgetIndex()
		bi, _ := b.widgetIndex()
		return ai < bi
	})
	if len(snap.UIWidgetOverrides) == 0 {
		snap.UIWidgetOverrides = nil
	}
	snap.ModeSelection = nil
	if len(modes) > 0 {
		snap.ModeSelection = make(map[string]string, len(modes))
		for k, v := range modes {
			snap.ModeSelection[k] = v
		}
	}
	if b := marshalRunParams(snap); b != "" {
		return b
	}
	return "{}"
}

// presetEntriesFromForm turns a posted preset form into storable entries.
//
// The values go through the UNCHANGED parseWidgetOverridesForModes allow-list, so
// a hand-built request can never store a key outside the curated editable set for
// the mode-applied graph. The drift tuple is then snapshotted from the LIVE
// RunInput each surviving key belongs to, which is why a stored tuple can never
// disagree with the field it came from.
func presetEntriesFromForm(form url.Values, wf *store.Workflow, modes map[string]string) []comfy.PresetEntry {
	overrides := parseWidgetOverridesForModes(form, wf, modes)
	if len(overrides) == 0 {
		return nil
	}
	var out []comfy.PresetEntry
	for _, ri := range comfy.DetectRunInputs(modeAppliedGraph(wf, modes), nil) {
		if v, ok := overrides[comfy.UIWidgetKey{NodeID: ri.NodeID, Widget: ri.WidgetIndex}]; ok {
			out = append(out, comfy.PresetEntryFor(ri, v))
		}
	}
	return out
}

// presetEntriesFromGraph seeds a brand-new preset from the graph's CURRENT values,
// so a fresh tab starts as "exactly what the workflow says" rather than empty.
func presetEntriesFromGraph(wf *store.Workflow, modes map[string]string) []comfy.PresetEntry {
	var out []comfy.PresetEntry
	for _, ri := range comfy.DetectRunInputs(modeAppliedGraph(wf, modes), nil) {
		out = append(out, comfy.PresetEntryFor(ri, ri.Current))
	}
	return out
}

// modeAppliedGraph returns the graph a run with these mode picks would convert: an
// ephemeral copy with the chosen pipeline un-bypassed. An empty pick set (or an
// api workflow) is the stored graph, byte for byte.
func modeAppliedGraph(wf *store.Workflow, modes map[string]string) json.RawMessage {
	graph := json.RawMessage(wf.Graph)
	if len(modes) > 0 && wf.Format == store.WorkflowFormatUI {
		return comfy.ApplyModeSelection(graph, modes)
	}
	return graph
}

// presetHashMatch is the EXACT/DRIFTED verdict: equal AND both non-blank. A blank
// hash on either side cannot be proven equal — the same call runOptionsFromParams
// makes, and a reachable one because graph_hash is nullable for pre-0011 rows.
func presetHashMatch(p *store.RunPreset, wf *store.Workflow) bool {
	if p == nil {
		return true // the implicit tab is seeded from the live graph by construction
	}
	return p.GraphHash != "" && wf.GraphHash != "" && p.GraphHash == wf.GraphHash
}

// ── view assembly ────────────────────────────────────────────────────────────

// buildPresetView loads a workflow's presets, picks the active tab, and reconciles
// it against the graph the run would convert.
//
// preferPresetModes distinguishes the two entry points. On a TAB OPEN (page
// render, activate, fork, save, delete) the active preset's stored mode
// pre-selects the picker, so the view is built for that mode and #run-modes is
// re-rendered out of band to match. When the user changes the PICKER, the picker
// is authoritative and the preset's stored mode is ignored for this render.
//
// Exactly ONE reconciliation (and therefore one DetectRunInputs) runs per render,
// for the active tab only: inactive tabs are label-only. Reconciling all twelve
// would be a 12× regression on the run page for no benefit.
func (s *Server) buildPresetView(ctx context.Context, wf *store.Workflow, activeID int64, pickerModes map[string]string, preferPresetModes bool) presetTabView {
	v := presetTabView{UIFormat: wf.Format == store.WorkflowFormatUI}
	if !v.UIFormat {
		return v
	}
	presets, err := s.store.ListRunPresets(ctx, wf.ID)
	if err != nil {
		// A preset read failing must never take the run panel down: degrade to the
		// implicit tab, which is exactly today's behaviour.
		s.log.Warn("run presets: list failed", "workflow", wf.ID, "err", err)
		presets = nil
	}
	v.Presets = presets
	v.AtCap = len(presets) >= store.MaxRunPresetsPerWorkflow

	for i := range presets {
		if presets[i].ID == activeID {
			v.Active = &presets[i]
			break
		}
	}
	if v.Active == nil && len(presets) > 0 {
		v.Active = &presets[0]
	}

	hashMatch := presetHashMatch(v.Active, wf)
	v.Drifted = v.Active != nil && !hashMatch

	var entries []comfy.PresetEntry
	var modeDrops []comfy.PresetDrop
	effModes := pickerModes
	if v.Active != nil {
		entries = presetEntries(v.Active.Params)
		applicable, drops := comfy.ResolvePresetModes(
			json.RawMessage(wf.Graph), presetModes(v.Active.Params), hashMatch)
		modeDrops = drops
		if preferPresetModes && len(applicable) > 0 {
			// The preset pre-selects the picker: reconcile against ITS mode and swap
			// #run-modes out of band so the next run uses the same graph.
			effModes = applicable
			v.ModesOOB = applicable
		}
	}
	v.Rec = comfy.ReconcileRunPreset(json.RawMessage(wf.Graph), effModes, modeDrops, entries, hashMatch)
	return v
}

// implicitPresetView is the preset-free view: the IMPLICIT tab, every field
// seeded from the graph's current values under the given mode selection. It is
// what a workflow with nothing saved renders, and what the preset-free callers
// (and their tests) go through, so there is only ever ONE parameter renderer.
func implicitPresetView(wf *store.Workflow, modes map[string]string) presetTabView {
	v := presetTabView{UIFormat: wf.Format == store.WorkflowFormatUI}
	if !v.UIFormat {
		return v
	}
	// No stored entries and hashMatch=true: nothing to reconcile, nothing to warn
	// about — every field is simply the graph's own value.
	v.Rec = comfy.ReconcileRunPreset(json.RawMessage(wf.Graph), modes, nil, nil, true)
	return v
}

// ── HTTP surface ─────────────────────────────────────────────────────────────
//
// Every preset POST uses the run endpoints' prologue verbatim: ParseForm →
// verifyCSRF → gate, in that order. The CRUD endpoints are loopback-gated too even
// though they touch no filesystem path: they are the INPUT to a run, and the run
// panel itself renders nothing but a note off-loopback — an ungated preset editor
// would be an editor for a surface the caller cannot use.

// presetRequest runs the shared prologue and loads the workflow. It returns nil
// when it has already written the response.
func (s *Server) presetRequest(w http.ResponseWriter, r *http.Request) *store.Workflow {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return nil
	}
	if !s.verifyCSRF(w, r) {
		return nil
	}
	if !s.gate(w) {
		return nil
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad workflow id", http.StatusBadRequest)
		return nil
	}
	wf, err := s.store.GetWorkflow(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return nil
	}
	if err != nil {
		s.renderError(w, "load workflow", err)
		return nil
	}
	return wf
}

// presetOfWorkflow loads {pid} and proves it belongs to wf. A preset id that names
// another workflow's row is a 404, never a cross-workflow read or write — the id
// space is global and guessable.
func (s *Server) presetOfWorkflow(w http.ResponseWriter, r *http.Request, wf *store.Workflow, pid int64) *store.RunPreset {
	p, err := s.store.GetRunPreset(r.Context(), pid)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return nil
	}
	if err != nil {
		s.renderError(w, "load run preset", err)
		return nil
	}
	if p.WorkflowID != wf.ID {
		http.NotFound(w, r)
		return nil
	}
	return p
}

// pathPresetID parses the {pid} path value.
func pathPresetID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("pid"), 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

// persistOutgoing saves the POSTED field values into the tab they were typed in
// (the form's preset_id), so switching tabs, forking, or deleting never discards
// an unrun draft. It NEVER re-stamps graph_hash — that is adopt's job alone.
//
// It returns false only when it has already written a response (a cross-workflow
// preset id). A blank/0 preset_id is the implicit tab: nothing to save.
func (s *Server) persistOutgoing(w http.ResponseWriter, r *http.Request, wf *store.Workflow, skipID int64) bool {
	pid, err := strconv.ParseInt(strings.TrimSpace(r.FormValue(presetIDField)), 10, 64)
	if err != nil || pid <= 0 || pid == skipID {
		return true
	}
	p := s.presetOfWorkflow(w, r, wf, pid)
	if p == nil {
		return false
	}
	modes := parseModeChoices(r.Form, wf)
	entries := presetEntriesFromForm(r.Form, wf, modes)
	if entries == nil {
		// Nothing editable was posted (a collapsed panel, an api graph). Do not blank
		// out a stored value set on the strength of an empty form.
		return true
	}
	params := presetParamsWith(p.Params, entries, modes)
	if err := s.store.UpdateRunPreset(r.Context(), p.ID, presetNameOr(r, p.Name), params); err != nil {
		s.log.Warn("run presets: persist outgoing failed", "preset", p.ID, "err", err)
	}
	return true
}

// presetNameOr reads the posted tab label, bounded and trimmed, falling back to
// the stored one when the field was not submitted at all.
func presetNameOr(r *http.Request, fallback string) string {
	if _, ok := r.Form[presetNameField]; !ok {
		return fallback
	}
	name := strings.TrimSpace(r.FormValue(presetNameField))
	if len(name) > maxPresetNameLen {
		name = name[:maxPresetNameLen]
	}
	return name
}

// renderPresetPanel writes the #run-params body for wf with activeID selected,
// plus (when the active preset carries a mode) the out-of-band #run-modes swap.
func (s *Server) renderPresetPanel(w http.ResponseWriter, r *http.Request, wf *store.Workflow, activeID int64, notice string) {
	view := s.buildPresetView(r.Context(), wf, activeID, parseModeChoices(r.Form, wf), true)
	view.Notice = notice
	s.render(w, http.StatusOK, g.Group([]g.Node{
		runPresetPanel(wf, s.csrf, view),
		runModesOOB(wf, s.csrf, view),
	}))
}

// handleWorkflowRunPresetCreate creates a tab. With from=<pid> it is FORK: a deep
// copy of that preset's stored params AND its graph_hash, so a forked drift stays
// visible instead of being laundered into a fresh-looking preset. Without it, the
// new tab takes the posted field values (falling back to the graph's current
// values), stamped with the workflow's current hash.
func (s *Server) handleWorkflowRunPresetCreate(w http.ResponseWriter, r *http.Request) {
	wf := s.presetRequest(w, r)
	if wf == nil {
		return
	}
	if wf.Format != store.WorkflowFormatUI {
		http.NotFound(w, r) // presets are UI-format only
		return
	}
	// Persist whatever the user typed in the outgoing tab FIRST, so a fork copies
	// the text on screen rather than the last-saved text.
	if !s.persistOutgoing(w, r, wf, 0) {
		return
	}

	modes := parseModeChoices(r.Form, wf)
	newPreset := store.RunPreset{WorkflowID: wf.ID, Position: -1, GraphHash: wf.GraphHash}

	if raw := strings.TrimSpace(r.FormValue(presetFromField)); raw != "" {
		src, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			http.Error(w, "bad preset id", http.StatusBadRequest)
			return
		}
		p := s.presetOfWorkflow(w, r, wf, src)
		if p == nil {
			return
		}
		// Deep copy: params is an immutable string here, so the fork can never alias
		// the source's stored values.
		newPreset.Params = p.Params
		newPreset.GraphHash = p.GraphHash
		newPreset.Name = forkName(p.Name)
	} else {
		entries := presetEntriesFromForm(r.Form, wf, modes)
		if entries == nil {
			entries = presetEntriesFromGraph(wf, modes)
		}
		newPreset.Params = presetParamsWith("", entries, modes)
		newPreset.Name = presetNameOr(r, "")
	}

	id, err := s.store.CreateRunPreset(r.Context(), newPreset)
	if errors.Is(err, store.ErrPresetCapReached) {
		// No silent eviction — presets are user data. Re-render with the reason and
		// keep the current tab active.
		s.renderPresetPanel(w, r, wf, formPresetID(r), presetCapNotice())
		return
	}
	if err != nil {
		s.renderError(w, "create run preset", err)
		return
	}
	s.renderPresetPanel(w, r, wf, id, "")
}

// forkName labels a fork without ever growing without bound.
func forkName(src string) string {
	name := strings.TrimSpace(src)
	if name == "" {
		return "Copy"
	}
	name += " copy"
	if len(name) > maxPresetNameLen {
		name = name[:maxPresetNameLen]
	}
	return name
}

// presetCapNotice is server-authored text (never reflected input).
func presetCapNotice() string {
	return "This workflow already has " + strconv.Itoa(store.MaxRunPresetsPerWorkflow) +
		" presets — the maximum. Delete one before adding another."
}

// formPresetID reads the active tab id the request carried (0 = implicit).
func formPresetID(r *http.Request) int64 {
	id, err := strconv.ParseInt(strings.TrimSpace(r.FormValue(presetIDField)), 10, 64)
	if err != nil || id < 0 {
		return 0
	}
	return id
}

// handleWorkflowRunPresetActivate is the tab switch. It is a POST, not a GET,
// precisely so the OUTGOING tab's typed values are persisted in the same round
// trip: a GET switch would silently discard them, and requirement "unrun drafts
// survive" is the whole point of storing presets.
func (s *Server) handleWorkflowRunPresetActivate(w http.ResponseWriter, r *http.Request) {
	wf := s.presetRequest(w, r)
	if wf == nil {
		return
	}
	pid, ok := pathPresetID(r)
	if !ok {
		http.Error(w, "bad preset id", http.StatusBadRequest)
		return
	}
	target := s.presetOfWorkflow(w, r, wf, pid)
	if target == nil {
		return
	}
	if !s.persistOutgoing(w, r, wf, pid) {
		return
	}
	s.renderPresetPanel(w, r, wf, pid, "")
}

// handleWorkflowRunPresetSave persists the active tab's name + values.
//
// It re-stamps graph_hash ONLY with an explicit adopt_graph=1. Clicking Save means
// "save my text"; it does not mean "I certify this whole parameter set against a
// graph I have not inspected". Same shape as the repo's never-substitute-a-file-
// silently rule: the first click saves and OFFERS the adoption, a second click
// carrying the flag performs it.
func (s *Server) handleWorkflowRunPresetSave(w http.ResponseWriter, r *http.Request) {
	wf := s.presetRequest(w, r)
	if wf == nil {
		return
	}
	pid, ok := pathPresetID(r)
	if !ok {
		http.Error(w, "bad preset id", http.StatusBadRequest)
		return
	}
	p := s.presetOfWorkflow(w, r, wf, pid)
	if p == nil {
		return
	}

	modes := parseModeChoices(r.Form, wf)
	entries := presetEntriesFromForm(r.Form, wf, modes)
	params := presetParamsWith(p.Params, entries, modes)
	name := presetNameOr(r, p.Name)

	drifted := !presetHashMatch(p, wf)
	adopt := drifted && strings.TrimSpace(r.FormValue(presetAdoptField)) == "1"

	var err error
	notice := "Saved."
	switch {
	case adopt:
		err = s.store.AdoptRunPresetGraph(r.Context(), p.ID, name, params, wf.GraphHash)
		notice = "Saved and adopted the current graph."
	default:
		err = s.store.UpdateRunPreset(r.Context(), p.ID, name, params)
		if drifted {
			notice = "Saved. This preset still targets an earlier version of this " +
				"workflow — use \"Adopt current graph\" to clear that."
		}
	}
	if err != nil {
		s.renderError(w, "save run preset", err)
		return
	}
	s.renderPresetPanel(w, r, wf, p.ID, notice)
}

// handleWorkflowRunPresetDelete removes a tab and activates its neighbour.
func (s *Server) handleWorkflowRunPresetDelete(w http.ResponseWriter, r *http.Request) {
	wf := s.presetRequest(w, r)
	if wf == nil {
		return
	}
	pid, ok := pathPresetID(r)
	if !ok {
		http.Error(w, "bad preset id", http.StatusBadRequest)
		return
	}
	p := s.presetOfWorkflow(w, r, wf, pid)
	if p == nil {
		return
	}
	// Saving values into the tab being deleted would be a wasted write; every OTHER
	// tab's draft is still preserved.
	if !s.persistOutgoing(w, r, wf, pid) {
		return
	}

	next := s.neighbourPreset(r.Context(), wf.ID, pid)
	if err := s.store.DeleteRunPreset(r.Context(), pid); err != nil && !errors.Is(err, store.ErrNotFound) {
		s.renderError(w, "delete run preset", err)
		return
	}
	s.renderPresetPanel(w, r, wf, next, "Preset deleted.")
}

// neighbourPreset picks which tab to activate after deleting pid: the one before
// it, else the one after, else the implicit tab (0).
func (s *Server) neighbourPreset(ctx context.Context, wfID, pid int64) int64 {
	list, err := s.store.ListRunPresets(ctx, wfID)
	if err != nil {
		return 0
	}
	for i := range list {
		if list[i].ID != pid {
			continue
		}
		if i > 0 {
			return list[i-1].ID
		}
		if i+1 < len(list) {
			return list[i+1].ID
		}
	}
	return 0
}
