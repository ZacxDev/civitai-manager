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

// runPresetPanelID is the STABLE id of the element wrapping that form — the tab
// strip's `tabpanel` (see runPresetTabPanel). It is what the tabs' aria-controls
// points at.
//
// 🔴 It is deliberately a SEPARATE id from runPresetFormID rather than the role
// being moved onto the form: a <form> may not carry role="tabpanel" (axe
// aria-allowed-role), and runPresetInclude names the FORM's id, so collapsing the
// two would stop every preset control from posting the user's typed values.
const runPresetPanelID = "run-preset-panel"

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

// presetNoFieldsNotice is what a write that captured NOTHING reports. It is
// server-authored text; nothing from the request is reflected into it.
//
// Saying "Saved." there would be the lie the whole carry-through rule exists to
// prevent: the values on the user's screen were NOT stored (they name parameters
// this workflow no longer has), and the ones that ARE stored are not the ones being
// displayed. The user needs to know both, and what to do about it.
const presetNoFieldsNotice = "None of the values this page sent are parameters of " +
	"this workflow as it is now, so your previously saved values were kept unchanged. " +
	"This page is out of date — the workflow changed underneath it, or the workflow-mode " +
	"picker had not finished re-rendering. Reopen this tab to edit the current parameters."

// presetNoAdoptHashNotice explains a refused adoption. "Adopt current graph" stamps
// workflows.graph_hash into the preset; a workflow row that predates content hashes
// (migration 0011) and has never been re-scanned carries none, so there is literally
// nothing to stamp. Reporting "adopted" there left the preset permanently drifted
// with a button the user could click forever.
//
// The ADVICE has to match what the row can actually do. Only a row with a
// source_path is re-scannable — that is the sole key UpsertWorkflowByPath (the one
// path that refreshes graph_hash in place) accepts. A pre-0011 workflow that arrived
// by paste, by PNG, or from civitai has none: telling its owner to "re-scan the
// workflow" names an action that cannot be performed on it, so the button stays inert
// and the advice explains nothing. Every INSERT path populates graph_hash today
// (store.InsertWorkflow), so importing the file again does produce a hash-carrying
// row — it is just a NEW row, which is the honest thing to say.
func presetNoAdoptHashNotice(rescannable bool) string {
	const head = "Saved. This workflow has no content hash yet — it has not been " +
		"scanned since this app started recording them — so there is nothing to adopt. "
	if rescannable {
		return head + "Re-scan the workflow, then use \"Adopt current graph\" again."
	}
	return head + "This workflow was not scanned from a file on disk, so there is " +
		"nothing to re-scan and this preset cannot be adopted. Import the workflow " +
		"again to get a copy that records one."
}

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
//
// A malformed entry (no node id, non-integer widget index) is NEVER defaulted onto
// slot 0 — but it is not silently swallowed here either. It is marked Malformed and
// handed to the reconciler, which drops it AND NAMES it, because "dropped,
// defaulted, and named" is the rule for every other drop and a value the user
// believes is saved must not vanish without a word. Dropping it in this decoder is
// what made it the one unnamed drop in the whole surface.
func presetEntries(params string) []comfy.PresetEntry {
	snap := parseRunParams(params)
	out := make([]comfy.PresetEntry, 0, len(snap.UIWidgetOverrides))
	for _, e := range snap.UIWidgetOverrides {
		entry := comfy.PresetEntry{
			NodeID:    e.NodeID,
			Value:     e.Value,
			Kind:      comfy.RunInputKind(e.Kind),
			ClassType: e.ClassType,
			InputName: e.InputName,
			Label:     e.Label,
		}
		widx, ok := e.widgetIndex()
		if !ok || e.NodeID == "" {
			entry.Malformed = true
		} else {
			entry.Widget = widx
		}
		out = append(out, entry)
	}
	return out
}

// presetModes decodes a preset's stored mode selection.
func presetModes(params string) map[string]string {
	return parseRunParams(params).ModeSelection
}

// presetParamsFrom rewrites a params blob's POSITIONAL families (the widget
// entries and the mode selection) while preserving everything else verbatim.
//
// substitute / option_fixes are NAME-keyed and degrade safely downstream (an
// unknown filename or input name is a no-op, and option fixes are re-validated
// against live object_info inside realRun), so they are carried across a save
// untouched and are never part of the drift report.
func presetParamsFrom(prev string, overrides []uiWidgetOverrideEntry, modes map[string]string) string {
	snap := parseRunParams(prev)
	snap.UIWidgetOverrides = overrides
	// Stable order so the stored blob is comparable between saves. SliceStable, not
	// Slice: two entries on the same node whose slot index is undecodable compare
	// equal (widgetIndex reports 0 for both), and an unstable sort would reorder
	// them arbitrarily between otherwise identical writes.
	sort.SliceStable(snap.UIWidgetOverrides, func(i, j int) bool {
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

// storableOverrides serializes captured entries into the stored blob's shape.
func storableOverrides(entries []comfy.PresetEntry) []uiWidgetOverrideEntry {
	out := make([]uiWidgetOverrideEntry, 0, len(entries))
	for _, e := range entries {
		out = append(out, uiWidgetOverrideEntry{
			NodeID:    e.NodeID,
			Widget:    json.RawMessage(strconv.Itoa(e.Widget)),
			Value:     e.Value,
			Kind:      string(e.Kind),
			ClassType: e.ClassType,
			InputName: e.InputName,
			Label:     e.Label,
		})
	}
	return out
}

// presetParamsWith REPLACES the stored entries AND the stored mode selection with
// what it is given. It is the from-scratch writer: creating a preset (prev is "") and
// the one write that may not carry anything forward (see presetEntryWrite's
// certifying branch) — for the mode selection just as much as for the entries.
func presetParamsWith(prev string, entries []comfy.PresetEntry, modes map[string]string) string {
	return presetParamsFrom(prev, storableOverrides(entries), modes)
}

// presetParamsMerged MERGES entries over the stored ones: a captured key wins for
// itself, every other stored key is carried forward untouched. The mode selection
// merges by the same rule (presetModesMerged), so the two positional families
// cannot drift apart in behaviour.
//
// This is the shape the storage model forces. A preset holds the FULL run-parameter
// set (design decision 4), but a save can only ever capture WHAT IS ON SCREEN, and
// which parameters are on screen depends on the selected workflow mode. Full-set
// storage + partial-set capture + wholesale replacement is structural data loss:
// saving while mode B is selected deleted every value belonging to mode A, and
// because the preset was not drifted the write was stamped with the workflow's
// CURRENT hash, so the next open took the EXACT fast path — no banner, no drop
// list, nothing on screen saying the values were gone.
//
// Two rules make the merge honest:
//
//   - The capture WINS for its own key, including when its value is empty. Clearing
//     a field posts that key with "" (a text input submits whether or not it holds
//     text), so a clear is a capture and must not be undone by the carry-forward.
//   - Carried-forward entries are NOT filtered against the live key set. Whether a
//     stored key still resolves is reconciliation's job on READ
//     (comfy.ReconcileRunPreset drops, retargets and NAMES), and filtering here
//     would delete exactly the keys that are off-screen because of the current
//     mode — the loss this function exists to stop.
func presetParamsMerged(prev string, entries []comfy.PresetEntry, modes map[string]string) string {
	captured := make(map[comfy.UIWidgetKey]bool, len(entries))
	for _, e := range entries {
		captured[comfy.UIWidgetKey{NodeID: e.NodeID, Widget: e.Widget}] = true
	}
	carried := make([]uiWidgetOverrideEntry, 0, len(parseRunParams(prev).UIWidgetOverrides))
	for _, e := range parseRunParams(prev).UIWidgetOverrides {
		if widx, ok := e.widgetIndex(); ok && e.NodeID != "" &&
			captured[comfy.UIWidgetKey{NodeID: e.NodeID, Widget: widx}] {
			continue // the capture is the newer truth for this key
		}
		// Kept VERBATIM — the undecodable slot value included. Re-serializing a
		// malformed entry through an int would aim it at slot 0, which is the silent
		// retarget the whole reconciler exists to prevent. It stays malformed here and
		// stays dropped-and-NAMED on the next open.
		carried = append(carried, e)
	}
	return presetParamsFrom(prev, append(carried, storableOverrides(entries)...),
		presetModesMerged(prev, modes))
}

// presetModesMerged MERGES the captured mode picks over the stored ones: a selector
// this request picked for wins for itself, a selector it said nothing about keeps its
// stored pick.
//
// Same failure as the widget wipe, one field over. A preset holds the FULL run
// parameter set including its mode, but a request only carries a mode_key when the
// picker actually submitted one — and presetParamsFrom REPLACED the stored selection
// wholesale, so a save that captured widget values while posting no mode_key stored
// an empty selection over the user's pick. parseModeChoices' own comment already
// reads `"" (keep as saved)`; the write then did not keep it.
//
// WHAT "CAPTURED" MEANS FOR A MODE is narrower than for a widget, and the difference
// is structural rather than a judgement call:
//
//   - A parameter field ALWAYS submits — a text input posts whether or not it holds
//     text — so an absent widget key means the field was not on screen, and an empty
//     posted value is a deliberate CLEAR that must win.
//   - A mode <select> submits one value per selector, and parseModeChoices keeps only
//     values naming a real mode of a real selector. So no mode_key for a selector
//     (absent, blank placeholder, unknown, hostile) is the ABSENCE of a capture and
//     carries the stored pick forward; an explicit live mode key IS the capture and
//     wins for its selector.
//
// ⚠ KNOWN LIMIT — a stored pick can be REPLACED by a save, never REMOVED by one.
// "The user deselected the mode" is not representable in the posted form at all:
// runModeSelect renders the blank "Choose a mode…" option ONLY while nothing is
// selected, so a picker showing a mode offers no way back to blank, and a blank value
// parses identically to no field at all. A sentinel meaning "clear it" is
// deliberately NOT invented here — that is new UI, not a merge fix. Deleting the tab
// is today's way to clear a stored pick. Carrying forward is still the strictly safer
// half of that trade: the alternative was deleting the pick on a write that never
// asked to.
//
// This does NOT let a stored pick outlive a picker change (RESOLVED decision 1(c):
// #run-modes is the source of truth, a preset's mode only pre-selects it). Every run
// control hx-includes "#run-modes select", so a user who changed the picker posts the
// NEW key — which is a capture, and wins.
func presetModesMerged(prev string, modes map[string]string) map[string]string {
	stored := parseRunParams(prev).ModeSelection
	if len(stored) == 0 {
		return modes
	}
	out := make(map[string]string, len(stored)+len(modes))
	for k, v := range stored {
		out[k] = v
	}
	for k, v := range modes {
		out[k] = v
	}
	return out
}

// presetUncapturedCount counts the stored entries this capture did NOT cover — the
// values a REPLACING write would discard.
func presetUncapturedCount(prev string, entries []comfy.PresetEntry) int {
	captured := make(map[comfy.UIWidgetKey]bool, len(entries))
	for _, e := range entries {
		captured[comfy.UIWidgetKey{NodeID: e.NodeID, Widget: e.Widget}] = true
	}
	n := 0
	for _, e := range parseRunParams(prev).UIWidgetOverrides {
		widx, ok := e.widgetIndex()
		if ok && e.NodeID != "" && captured[comfy.UIWidgetKey{NodeID: e.NodeID, Widget: widx}] {
			continue
		}
		n++
	}
	return n
}

// presetUncapturedModes counts the stored mode picks this request did not capture —
// the picks a REPLACING (certifying) write discards, counted so the caller can SAY
// so rather than dropping them in silence.
func presetUncapturedModes(prev string, modes map[string]string) int {
	n := 0
	for selKey := range parseRunParams(prev).ModeSelection {
		if _, ok := modes[selKey]; !ok {
			n++
		}
	}
	return n
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

// presetWriteHash is the graph_hash every entry-REPLACING write must store.
//
// Entries always come from the CURRENT graph (parseWidgetOverridesForModes allows
// only live keys and PresetEntryFor snapshots the tuple off the live RunInput), so
// there are exactly two truthful answers — and leaving the old hash in place is
// neither of them:
//
//   - The hash already matches, or this is an explicit ADOPT → store the current
//     hash. Truthful, and it keeps the ordinary edit loop on the exact fast path
//     instead of manufacturing permanent drift out of every save.
//   - Otherwise (the preset is drifted and the user did NOT adopt) → store "".
//     Blank can never be proven equal, so reconciliation always takes the per-entry
//     tuple-checked drift path. Fail-safe: matching entries still apply, mismatched
//     ones are dropped and named, and a false EXACT is impossible.
//
// Blanking rather than stamping is what keeps RESOLVED decision 7 intact: plain
// Save must not silently adopt the current graph. The banner, the "Adopt current
// graph" button and the un-certified state all survive a save; only the false
// certificate goes away.
func presetWriteHash(p *store.RunPreset, wf *store.Workflow, adopt bool) string {
	if !presetAdoptable(wf) {
		// Nothing to stamp. Falling through would "store the current hash" and store
		// a blank — the same value, but arrived at by accident. See presetAdoptable:
		// the caller owes the user a different SENTENCE in this case, not a different
		// hash.
		return ""
	}
	if adopt || presetHashMatch(p, wf) {
		return wf.GraphHash
	}
	return ""
}

// presetAdoptable reports whether "Adopt current graph" can do anything at all.
//
// Adoption stamps workflows.graph_hash into the preset. graph_hash arrived in
// migration 0011 and is written by the import/scan path, so a workflow row that has
// never been re-scanned since carries a BLANK one — and blank can never be proven
// equal (presetHashMatch), so stamping it leaves the preset permanently drifted with
// an "Adopt current graph" button that does nothing, forever, while the response
// says it adopted. The honest answer is to refuse and say what would fix it.
func presetAdoptable(wf *store.Workflow) bool {
	return wf.GraphHash != ""
}

// presetEntryWrite decides the (params, graph_hash) pair ONE entry-replacing write
// stores. Every such write goes through it, so save and persistOutgoing cannot
// drift apart on the rule that matters most here.
//
// EMPTY ENTRIES ARE NOT AN EMPTY PARAMETER SET. presetEntriesFromForm returns nil
// when NO posted triple survived the allow-list, and that is a statement about the
// REQUEST, never about the user's intent: no control in this panel can produce it
// deliberately (a text input submits whether or not it holds text). It happens when
// a page posts against a graph it was not rendered from —
//
//   - a STALE TAB: the workflow was re-imported or re-scanned underneath it, so every
//     key the page holds names a node that no longer exists;
//   - the MODE RACE: the picker's asynchronous re-render of #run-params has not
//     landed, so Save posts the NEW mode_key with the PREVIOUS mode's fields, and the
//     allow-list (derived from the mode-applied graph) rejects all of them.
//
// Replacing the stored entries there destroyed every saved value — and because
// presetWriteHash returns the workflow's hash whenever the preset is NOT drifted, the
// empty result was stamped with a VALID CURRENT hash: the next open took the exact
// fast path and reported no drift, no drops, nothing. Silent total data loss.
//
// So an empty capture carries the stored positional families through UNTOUCHED,
// hash included: the entries did not change, so the hash that described them still
// does. The write itself still happens — the tab label is not positional and a
// rename must not be silently dropped — and the caller tells the user plainly
// (presetNoFieldsNotice) rather than reporting a save that did not happen.
//
// A PARTIAL capture is the same failure one notch quieter, and on a real multi-mode
// template it is the LIKELIER one: the capture is the INTERSECTION of the posted
// keys and the live keys, so saving while mode B is selected captures mode B's
// fields and, under wholesale replacement, deleted mode A's. That write was not
// drifted, so it was stamped with the workflow's CURRENT hash and the next open took
// the EXACT fast path — Applied=1, Dropped=0, no banner at all. Hence
// presetParamsMerged: the capture wins for its own keys, everything else is carried.
//
// THE ONE WRITE THAT MAY NOT MERGE is the CERTIFYING one. The returned hash is a
// claim about the stored entries — on the EXACT read path every entry it covers is
// applied with NO per-entry tuple check — so it may only be stamped over entries
// that were captured against the graph it names. Two cases, and only two:
//
//   - hash == "" → no claim is made; reconciliation re-checks every entry's tuple.
//     Merging is free.
//   - hash != "" AND presetHashMatch(p, wf) → the preset ALREADY carried this exact
//     hash, so by induction the carried entries were captured against this very
//     graph. Merging keeps the claim true.
//   - hash != "" AND NOT presetHashMatch(p, wf) → an ADOPTION. The stamp is new, and
//     it would newly certify carried entries captured against an OLDER graph.
//     Carrying them here is how a stale positional key gets applied without a check
//     — the retarget hazard. So an adoption certifies ONLY what this request
//     captured, i.e. only what the user was looking at when they clicked (decision 7
//     is exactly "I do not certify a param set against a graph I did not inspect").
//     The discarded count is returned so the caller can SAY so; those same values
//     were already named as drops in the banner the user adopted from.
//
// THE MODE SELECTION IS SUBJECT TO EXACTLY THE SAME ARGUMENT, because a
// comfy.ModeGroup.Key is "<selector node id>:<group index>" — as positional as a
// widget index, and re-derived from the CURRENT graph on every read:
//
//   - hash == "" → ResolvePresetModes' hash gate withholds every stored pick and
//     NAMES it, so carrying is free.
//   - hash != "" AND presetHashMatch → the pick was captured under this very hash by
//     induction; carrying keeps the claim true.
//   - hash != "" AND NOT presetHashMatch (an ADOPTION) → the hash gate stops firing,
//     leaving only the STRUCTURE check, which passes as long as the key still names
//     SOME group of that selector. An author who inserted or reordered a group keeps
//     the key valid while it now names a DIFFERENT pipeline — the retarget hazard, at
//     pipeline scale. So an adoption certifies only the picks this request made, and
//     an uncaptured stored pick counts toward discarded like any other saved value it
//     did not keep. (Adoption only happens on the drifted path, where the pick was
//     already named as a PresetDropModeDrifted in the banner the user adopted from.)
func presetEntryWrite(p *store.RunPreset, wf *store.Workflow, entries []comfy.PresetEntry, modes map[string]string, adopt bool) (params, hash string, discarded int) {
	if len(entries) == 0 {
		return p.Params, p.GraphHash, 0
	}
	hash = presetWriteHash(p, wf, adopt)
	if hash != "" && !presetHashMatch(p, wf) {
		return presetParamsWith(p.Params, entries, modes), hash,
			presetUncapturedCount(p.Params, entries) + presetUncapturedModes(p.Params, modes)
	}
	return presetParamsMerged(p.Params, entries, modes), hash, 0
}

// presetFormPostedFields distinguishes the two ways a capture can come back empty:
// a form that carried parameter controls whose keys no longer resolve (the
// out-of-date page) from one that carried none at all (a rename, or a panel with
// nothing editable). They deserve different sentences — telling a user who just
// renamed a tab that their page is out of date is its own small lie.
func presetFormPostedFields(form url.Values) bool {
	return len(form["wp_node"]) > 0
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
// an unrun draft.
//
// It never ADOPTS: on a drifted preset it stores a BLANK graph_hash, because the
// entries it just wrote were captured against the current graph and the stored hash
// named an older one. Leaving that hash would be a false certificate (see
// presetWriteHash); re-stamping it would be a silent adoption behind a tab click.
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
	// A partial or empty capture carries the stored values through (presetEntryWrite
	// merges); the label is still written, so renaming a tab and then switching away
	// from it does not lose the rename. This path never adopts, so it never discards.
	params, hash, _ := presetEntryWrite(p, wf, entries, modes, false)
	if err := s.store.UpdateRunPreset(r.Context(), p.ID, presetNameOr(r, p.Name), params,
		hash); err != nil {
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
	name := presetNameOr(r, p.Name)

	out := presetSaveOutcome{
		Captured:     len(entries) > 0,
		PostedFields: presetFormPostedFields(r.Form),
		Drifted:      !presetHashMatch(p, wf),
		Rescannable:  wf.SourcePath != "",
	}
	out.AdoptAsked = out.Drifted && strings.TrimSpace(r.FormValue(presetAdoptField)) == "1"
	// An adoption certifies the STORED entries against the CURRENT graph, so it needs
	// both halves to be real: entries this request actually captured from that graph
	// (an out-of-date page is showing a different one — certifying from it is exactly
	// the "certify a param set against a graph I did not inspect" hazard decision 7
	// exists to prevent), and a hash to stamp.
	out.Adopted = out.AdoptAsked && out.Captured && presetAdoptable(wf)

	// The values just captured belong to the CURRENT graph, so the hash written with
	// them is either the current one (adopt, or nothing had drifted) or blank. It is
	// never the pre-edit hash: that would certify these values against a graph they
	// were not captured from. A write that captured nothing changes neither.
	params, hash, discarded := presetEntryWrite(p, wf, entries, modes, out.Adopted)
	out.Discarded = discarded
	if err := s.store.UpdateRunPreset(r.Context(), p.ID, name, params, hash); err != nil {
		s.renderError(w, "save run preset", err)
		return
	}
	s.renderPresetPanel(w, r, wf, p.ID, out.notice())
}

// presetSaveOutcome is what a save actually DID, which is the only thing its notice
// is allowed to describe. Keeping the four facts apart is deliberate: every honesty
// defect this file has shipped was a notice that described the click instead of the
// write ("Saved." over a wipe, "adopted the current graph" over a blank hash).
type presetSaveOutcome struct {
	// Captured is true when at least one posted value survived the allow-list and was
	// therefore written into the preset.
	Captured bool
	// PostedFields is true when the request carried parameter controls at all.
	PostedFields bool
	// AdoptAsked / Adopted: what the user clicked, and what happened.
	AdoptAsked bool
	Adopted    bool
	// Drifted is the pre-write verdict.
	Drifted bool
	// Discarded counts stored values an ADOPTION could not certify and therefore did
	// not keep (presetEntryWrite's certifying branch) — uncaptured widget entries AND
	// an uncaptured mode pick, both positional. Zero on every other write.
	Discarded int
	// Rescannable is true when this workflow row came from a disk scan, so "re-scan
	// it" is advice the user can actually act on.
	Rescannable bool
}

// notice is the server-authored line the save response renders. Never reflected
// request input.
func (o presetSaveOutcome) notice() string {
	switch {
	case !o.Captured && o.PostedFields:
		// The out-of-date page. Says the values were kept, which is what the write did.
		return presetNoFieldsNotice
	case o.Adopted && o.Discarded > 0:
		// An adoption certifies only what this request captured, so stored values with
		// no field on screen were NOT carried into the new certificate. They were named
		// as drops in the banner the user adopted from; saying it again here is the
		// difference between "offered" and "performed".
		return "Saved and adopted the current graph. " + presetDiscardedPhrase(o.Discarded) +
			" no parameter of this workflow now matches could not be certified against " +
			"it and were not kept."
	case o.Adopted:
		return "Saved and adopted the current graph."
	case o.AdoptAsked && !o.Captured:
		// Refused for the same reason as above; that sentence already covers it.
		return presetNoFieldsNotice
	case o.AdoptAsked:
		// Captured, but the workflow has no hash to stamp.
		return presetNoAdoptHashNotice(o.Rescannable)
	case o.Drifted:
		return "Saved. This preset still targets an earlier version of this " +
			"workflow — use \"Adopt current graph\" to clear that."
	default:
		return "Saved."
	}
}

// presetDiscardedPhrase is the count half of the adoption notice, server-authored.
func presetDiscardedPhrase(n int) string {
	if n == 1 {
		return "1 saved value that"
	}
	return strconv.Itoa(n) + " saved values that"
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
