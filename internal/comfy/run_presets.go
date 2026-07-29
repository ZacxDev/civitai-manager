package comfy

import (
	"encoding/json"
	"sort"
	"strconv"
)

// ── Run-preset reconciliation ────────────────────────────────────────────────
//
// A saved run preset stores values keyed POSITIONALLY: (node id, widgets_values
// index) for each editable parameter, and "<selector node id>:<group index>" for
// each multi-mode pick. The graph underneath can change — a rescan replaces a
// workflow row's graph in place, a re-import lands a different graph_hash, an
// author republishes with a node reordered. Silently applying a stale positional
// set to a changed graph is the failure mode this file exists to prevent: a saved
// SEED type-coerced into what is now a prompt slot is a plausible-looking,
// silent corruption.
//
// The rule, in two steps:
//
//	EXACT       preset.graph_hash == workflow.graph_hash, both non-blank.
//	            The hash IS the proof that every position still means what it
//	            meant. Apply everything; no per-entry check.
//	DRIFTED     hashes differ, or EITHER is blank (blank cannot be proven equal —
//	            the same call runOptionsFromParams already makes). Every stored
//	            entry is re-checked against the live RunInput at the same key by
//	            its (kind, class_type, input_name) tuple; anything that does not
//	            match, is absent, or carries no tuple at all is DROPPED, defaulted
//	            to the graph's current value, and NAMED to the user.
//
// This is INFORMATIVE only. It decides what the form shows and what the user is
// told. The submitted run is still filtered by the unchanged submit-time
// allow-list (parseWidgetOverridesForModes / parseModeChoices), which re-derives
// the accepted key set from the mode-applied graph. A bug here can therefore
// produce a wrong-LOOKING form; it cannot produce a submission outside the
// curated editable set.
//
// KNOWN LIMIT, stated honestly: if node 3 stays CLIPTextEncode.text but the graph
// is rewired so node 3 now feeds the NEGATIVE conditioning, the tuple still
// matches and no widget-key check can detect it. The mitigation is that the live
// Label is rendered beside every field, so the user sees "Prompt (NEGATIVE)"
// pre-filled with their positive text. That is visibility, not a guarantee;
// detecting it needs link-graph analysis DetectRunInputs does not do.

// PresetEntry is ONE stored parameter value in a saved run preset.
//
// NodeID/Widget are the positional key — the holder's (node id, widgets_values
// index), exactly what RunInput emits and what ApplyUIWidgetOverrides consumes.
//
// Kind/ClassType/InputName are the DRIFT TUPLE, snapshotted from the RunInput at
// save time so a drifted graph can be re-checked structurally instead of only by
// hash equality. All three are optional: an entry written before presets existed
// carries none, which the reconciler treats as "unverifiable" (dropped and named)
// rather than as a match.
//
// Label is DISPLAY ONLY and explicitly NOT part of the match key — it is derived
// from untrusted author node titles and can change harmlessly.
type PresetEntry struct {
	NodeID    string
	Widget    int
	Value     string
	Kind      RunInputKind
	ClassType string
	InputName string
	Label     string
}

// PresetEntryFor snapshots one live RunInput plus a value into a storable entry,
// capturing the drift tuple from the SAME RunInput the value was edited on. Every
// save path goes through this so a stored tuple can never disagree with the field
// it came from.
func PresetEntryFor(ri RunInput, value string) PresetEntry {
	return PresetEntry{
		NodeID:    ri.NodeID,
		Widget:    ri.WidgetIndex,
		Value:     value,
		Kind:      ri.Kind,
		ClassType: ri.SourceClassType,
		InputName: ri.InputName,
		Label:     ri.Label,
	}
}

// tuple reports the entry's drift tuple and whether it carries one at all.
func (e PresetEntry) tuple() (RunInputKind, string, string, bool) {
	has := e.Kind != "" || e.ClassType != "" || e.InputName != ""
	return e.Kind, e.ClassType, e.InputName, has
}

// displayName is what the user is told a dropped entry was. It prefers the stored
// (untrusted, escaped-at-render) label and falls back to the structural
// "class.input" name, then to the raw positional key — so a drop is never
// reported as an empty string.
func (e PresetEntry) displayName() string {
	if e.Label != "" {
		return e.Label
	}
	if e.ClassType != "" && e.InputName != "" {
		return e.ClassType + "." + e.InputName
	}
	if e.InputName != "" {
		return e.InputName
	}
	return "#" + e.NodeID + " widget " + strconv.Itoa(e.Widget)
}

// PresetDropReason classifies why a stored value was not applied. The UI turns it
// into a sentence; the constant is what tests pin.
type PresetDropReason string

const (
	// PresetDropRetargeted — the key still exists but now drives a DIFFERENT input
	// (kind/class/input-name changed). This is the headline hazard: applying the
	// stored value here would type-coerce e.g. a seed into a prompt.
	PresetDropRetargeted PresetDropReason = "retargeted"
	// PresetDropGone — the stored key is not among the live editable inputs at all.
	PresetDropGone PresetDropReason = "gone"
	// PresetDropUnverifiable — the key exists but the entry carries no drift tuple
	// (written before the tuple was recorded), so it cannot be checked.
	PresetDropUnverifiable PresetDropReason = "unverifiable"
	// PresetDropModeUnknown — a stored mode key the graph no longer surfaces.
	PresetDropModeUnknown PresetDropReason = "mode-unknown"
	// PresetDropModeDrifted — a stored mode key withheld because the graph hash
	// moved. Mode keys are "<selector node id>:<group index>" — positional in the
	// group array, so exactly as position-sensitive as a widget index.
	PresetDropModeDrifted PresetDropReason = "mode-drifted"
)

// PresetDrop is one value the reconciler refused to apply, named for the user.
// Name and Detail are UNTRUSTED (author titles) and must be rendered via g.Text.
type PresetDrop struct {
	Name   string
	Reason PresetDropReason
	// Detail names the live input the key now drives (retargeted drops only).
	Detail string
}

// PresetField is one rendered run-parameter control: the LIVE input (label,
// kind, choices, origin — always the graph's truth) plus the value to pre-fill.
type PresetField struct {
	Input RunInput
	Value string
	// FromPreset is true when Value came from the preset; false when it was
	// defaulted to the graph's current value.
	FromPreset bool
}

// PresetReconciliation is the whole render-time verdict for ONE preset tab.
type PresetReconciliation struct {
	// Exact is true when the hash proved the stored positions still hold.
	Exact bool
	// Fields is one entry per LIVE editable input, in DetectRunInputs order.
	Fields []PresetField
	// Dropped names every stored value that was not applied.
	Dropped []PresetDrop
	// NewInputs names live inputs the preset had no stored value for (drift only).
	NewInputs []string
	// DroppedModes names every stored mode pick that was not applied.
	DroppedModes []PresetDrop
	// Modes is the mode selection actually applied to the graph before detection.
	Modes map[string]string
}

// NeedsBanner reports whether anything was dropped or newly appeared — i.e.
// whether the drift banner must be shown. An EXACT, unchanged preset renders no
// banner at all, which is what keeps the banner worth reading.
func (r PresetReconciliation) NeedsBanner() bool {
	return len(r.Dropped) > 0 || len(r.NewInputs) > 0 || len(r.DroppedModes) > 0
}

// Applied counts the stored values that survived reconciliation.
func (r PresetReconciliation) Applied() int {
	n := 0
	for _, f := range r.Fields {
		if f.FromPreset {
			n++
		}
	}
	return n
}

// ResolvePresetModes validates a preset's stored mode selection against a graph.
//
// It returns the picks that may be applied plus a named drop for every one that
// may not. Two independent gates:
//
//   - HASH: a mode key is positional ("<selector node id>:<group index>"), so it
//     is withheld wholesale when the hash cannot prove the graph is unchanged.
//   - STRUCTURE: the key must still be surfaced by DetectModeSelectors under the
//     SAME selector it was stored against.
//
// A withheld mode is NAMED, never silently ignored: with the mode not applied a
// multi-mode template converts to an all-bypassed graph and the run aborts as
// "nothing to run" — a baffling error for what is really "your saved mode no
// longer exists".
func ResolvePresetModes(graph json.RawMessage, stored map[string]string, hashMatch bool) (map[string]string, []PresetDrop) {
	if len(stored) == 0 {
		return nil, nil
	}
	// modeKey → (owning selector key, group title) for the CURRENT graph.
	type modeInfo struct{ selector, title string }
	known := map[string]modeInfo{}
	for _, sel := range DetectModeSelectors(graph) {
		for _, m := range sel.Modes {
			known[m.Key] = modeInfo{selector: sel.Key, title: m.Title}
		}
	}

	selKeys := make([]string, 0, len(stored))
	for k := range stored {
		selKeys = append(selKeys, k)
	}
	sort.Strings(selKeys) // deterministic drop order

	var out map[string]string
	var dropped []PresetDrop
	for _, selKey := range selKeys {
		modeKey := stored[selKey]
		info, ok := known[modeKey]
		name := modeKey
		if ok && info.title != "" {
			name = info.title
		}
		switch {
		case !ok || info.selector != selKey:
			dropped = append(dropped, PresetDrop{Name: name, Reason: PresetDropModeUnknown})
		case !hashMatch:
			dropped = append(dropped, PresetDrop{Name: name, Reason: PresetDropModeDrifted})
		default:
			if out == nil {
				out = map[string]string{}
			}
			out[selKey] = modeKey
		}
	}
	return out, dropped
}

// ReconcileRunPreset is the pure render-time reconciler. It applies effModes to a
// COPY of graph (the stored workflow is never mutated), detects the live editable
// inputs from that mode-applied graph — the same graph the run would convert —
// and matches the stored entries against them per the rule at the top of this
// file.
//
// effModes is the EFFECTIVE mode selection (what the run will use), resolved by
// the caller; modeDrops carries anything ResolvePresetModes already refused, so
// the whole verdict arrives in one value.
//
// hashMatch is the caller's EXACT/DRIFTED verdict: preset.graph_hash ==
// workflow.graph_hash with both non-blank.
func ReconcileRunPreset(graph json.RawMessage, effModes map[string]string, modeDrops []PresetDrop, entries []PresetEntry, hashMatch bool) PresetReconciliation {
	rec := PresetReconciliation{Exact: hashMatch, Modes: effModes, DroppedModes: modeDrops}

	modeGraph := graph
	if len(effModes) > 0 {
		modeGraph = ApplyModeSelection(graph, effModes)
	}
	live := DetectRunInputs(modeGraph, nil)

	stored := make(map[UIWidgetKey]PresetEntry, len(entries))
	for _, e := range entries {
		if e.NodeID == "" {
			continue // malformed — never default it onto some node's slot 0
		}
		stored[UIWidgetKey{NodeID: e.NodeID, Widget: e.Widget}] = e
	}
	matched := make(map[UIWidgetKey]bool, len(stored))

	rec.Fields = make([]PresetField, 0, len(live))
	for _, ri := range live {
		key := UIWidgetKey{NodeID: ri.NodeID, Widget: ri.WidgetIndex}
		e, ok := stored[key]
		if !ok {
			// A live input the preset has no value for. Only reported on DRIFT: on an
			// EXACT hash the input set is identical by construction, and a brand-new
			// preset (no entries at all) is not "12 new parameters".
			rec.Fields = append(rec.Fields, PresetField{Input: ri, Value: ri.Current})
			if !hashMatch && len(entries) > 0 {
				rec.NewInputs = append(rec.NewInputs, ri.Label)
			}
			continue
		}
		matched[key] = true

		if hashMatch {
			// The hash IS the proof. No per-entry check.
			rec.Fields = append(rec.Fields, PresetField{Input: ri, Value: e.Value, FromPreset: true})
			continue
		}
		kind, class, input, has := e.tuple()
		switch {
		case !has:
			rec.Fields = append(rec.Fields, PresetField{Input: ri, Value: ri.Current})
			rec.Dropped = append(rec.Dropped, PresetDrop{
				Name: e.displayName(), Reason: PresetDropUnverifiable, Detail: ri.Label,
			})
		case kind != ri.Kind || class != ri.SourceClassType || input != ri.InputName:
			rec.Fields = append(rec.Fields, PresetField{Input: ri, Value: ri.Current})
			rec.Dropped = append(rec.Dropped, PresetDrop{
				Name: e.displayName(), Reason: PresetDropRetargeted, Detail: ri.Label,
			})
		default:
			rec.Fields = append(rec.Fields, PresetField{Input: ri, Value: e.Value, FromPreset: true})
		}
	}

	// Stored keys with no live field. Reported in BOTH the exact and drifted cases:
	// even on an equal hash a value with nowhere to go is a value the user believes
	// is applied and is not. (On an equal hash with the same modes this is empty;
	// it fires when the mode picker selected a different pipeline.)
	var gone []PresetEntry
	for key, e := range stored {
		if !matched[key] {
			gone = append(gone, e)
		}
	}
	sort.Slice(gone, func(i, j int) bool {
		if gone[i].NodeID != gone[j].NodeID {
			return gone[i].NodeID < gone[j].NodeID
		}
		return gone[i].Widget < gone[j].Widget
	})
	for _, e := range gone {
		rec.Dropped = append(rec.Dropped, PresetDrop{Name: e.displayName(), Reason: PresetDropGone})
	}
	return rec
}
