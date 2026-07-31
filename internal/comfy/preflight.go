package comfy

import (
	"encoding/json"
	"path/filepath"
	"sort"
	"strings"
)

// PreflightReport is the result of validating an api-format graph against a live
// ComfyUI's /object_info AND the local library, BEFORE submitting. This is the
// library manager's differentiator: ComfyUI does not auto-download models and
// rejects a graph whose referenced files are not on disk, so we tell the user what
// is missing first.
type PreflightReport struct {
	// MissingNodes are class_types referenced by the graph that are absent from
	// /object_info (a custom node the local ComfyUI does not have installed).
	MissingNodes []string
	// MissingModels are model filenames referenced by loader inputs that are neither
	// present in the loader's /object_info choices list NOR satisfied by localHave
	// (the local library). These would fail ComfyUI's own validation.
	MissingModels []string
	// BadOptions are combo (enum) inputs whose SAVED value is not among the installed
	// node's current Choices — e.g. a node-version label drift on an inert picker, or
	// a model-file combo on a non-loader node whose file is not installed. ComfyUI
	// rejects these with an opaque `value_not_in_list`; surfacing them lets the user
	// pick a valid choice and re-run. Model FILES already covered by MissingModels are
	// NOT duplicated here.
	BadOptions []BadOption
	// OK is true when there are no missing nodes, no missing models, and no bad
	// combo-option values.
	OK bool
}

// BadOption is one combo (enum) input whose current value is not a valid choice on
// the installed node. It is GROUPED by distinct (InputName, Current value) across
// every node that shares it — so the same drifted value on four nodes is ONE
// BadOption listing all four NodeIDs. ClassType records the first (node-id sorted)
// referencing node's class; Choices are that input's valid combo choices from the
// live /object_info.
type BadOption struct {
	// NodeIDs are all nodes sharing this (InputName, Current) combo value.
	NodeIDs []string
	// ClassType is the class of the first referencing node (may differ across the
	// grouped nodes when the same input name appears on multiple node types).
	ClassType string
	// InputName is the combo input's name (e.g. "model_name").
	InputName string
	// Current is the saved (invalid) value.
	Current string
	// Choices are the input's valid combo choices from object_info.
	Choices []string
}

// Preflight validates an api-format graph against the node schema (info) and the
// local library (localHave reports whether a model FILENAME, by basename, exists
// locally). It reports referenced node types absent from info and referenced model
// filenames satisfied by neither the node's object_info choices nor localHave.
//
// localHave may be nil (treated as "have nothing locally"): then a model is
// considered present only if it appears in the node's object_info choices list
// (i.e. ComfyUI itself already sees the file).
func Preflight(apiGraph json.RawMessage, info ObjectInfo, localHave func(filename string) bool) PreflightReport {
	if localHave == nil {
		localHave = func(string) bool { return false }
	}

	var nodes map[string]apiNode
	if err := json.Unmarshal(apiGraph, &nodes); err != nil {
		// A graph we cannot parse cannot be pre-flighted; report not-OK with no
		// specifics rather than panicking on untrusted input.
		return PreflightReport{OK: false}
	}

	// 1. Missing node types.
	missingNodesSet := map[string]bool{}
	for _, n := range nodes {
		ct := strings.TrimSpace(n.ClassType)
		if ct == "" {
			continue
		}
		if _, ok := info[ct]; !ok {
			missingNodesSet[ct] = true
		}
	}

	// 2. Missing models. Reuse ExtractResources to find referenced model filenames,
	// then for each, check the loader node's object_info choices and the local
	// library.
	refs, _ := ExtractResources(apiGraph)
	// modelRefSet is the set of ALL loader model-file values (satisfied OR missing).
	// The model-file flow OWNS these values, so bad-option detection must skip them:
	// a missing one is already reported in MissingModels, and a locally-present one
	// (in localHave but absent from the combo choices) is intentionally treated as
	// satisfied by the model path — not double-flagged as an incompatible option.
	modelRefSet := make(map[string]bool, len(refs))
	missingModelsSet := map[string]bool{}
	for _, ref := range refs {
		modelRefSet[ref] = true
		if modelSatisfied(ref, nodes, info, localHave) {
			continue
		}
		missingModelsSet[ref] = true
	}

	report := PreflightReport{
		MissingNodes:  sortedKeys(missingNodesSet),
		MissingModels: sortedKeys(missingModelsSet),
		BadOptions:    detectBadOptions(nodes, info, modelRefSet),
	}
	report.OK = len(report.MissingNodes) == 0 &&
		len(report.MissingModels) == 0 &&
		len(report.BadOptions) == 0
	return report
}

// detectBadOptions finds every combo (enum) input whose scalar value is not among
// the installed node's Choices, grouped by distinct (InputName, Current) across all
// nodes. Values owned by the model-file flow (modelRefSet) are skipped so a missing
// or locally-satisfied model file is never double-reported. Iteration is
// deterministic (node ids sorted, then input names sorted) so grouping order and
// NodeIDs are stable.
func detectBadOptions(nodes map[string]apiNode, info ObjectInfo, modelRefSet map[string]bool) []BadOption {
	ids := make([]string, 0, len(nodes))
	for id := range nodes {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	type groupKey struct{ input, current string }
	byKey := map[groupKey]*BadOption{}
	var order []groupKey

	for _, id := range ids {
		n := nodes[id]
		sch, ok := info[n.ClassType]
		if !ok {
			continue // missing node type — handled by MissingNodes, not here
		}
		inNames := make([]string, 0, len(n.Inputs))
		for k := range n.Inputs {
			inNames = append(inNames, k)
		}
		sort.Strings(inNames)
		for _, inName := range inNames {
			spec, ok := inputSpec(sch, inName)
			if !ok || !spec.IsCombo || len(spec.Choices) == 0 {
				// Not a combo, or a combo whose options are NOT strings (InputSpec
				// leaves Choices nil for a numeric option list like RIFE VFI's
				// scale_factor [0.25,0.5,1.0,2.0,4.0] — see stringChoices). Numeric
				// combos are deliberately NOT validated here: comparing a saved number
				// against formatted option text is a false-positive generator ("1" vs
				// "1.0"), and the repair the UI would offer is string-typed, so it
				// could not produce a graph ComfyUI accepts anyway. Under-validating
				// leaves ComfyUI's submit-time check as the authority.
				continue
			}
			val, ok := scalarComboValue(n.Inputs[inName])
			if !ok {
				continue // link array, object, or otherwise not a scalar value
			}
			if modelRefSet[val] {
				continue // owned by the model-file flow (MissingModels) — do not double-report
			}
			if choicesContainValue(spec.Choices, val) {
				continue // value is a valid choice
			}
			k := groupKey{input: inName, current: val}
			bo, seen := byKey[k]
			if !seen {
				bo = &BadOption{
					ClassType: n.ClassType,
					InputName: inName,
					Current:   val,
					Choices:   spec.Choices,
				}
				byKey[k] = bo
				order = append(order, k)
			}
			bo.NodeIDs = append(bo.NodeIDs, id)
		}
	}

	if len(order) == 0 {
		return nil
	}
	out := make([]BadOption, 0, len(order))
	for _, k := range order {
		out = append(out, *byKey[k])
	}
	return out
}

// inputSpec returns the schema spec for one input name, checking Required then
// Optional.
func inputSpec(sch NodeSchema, name string) (InputSpec, bool) {
	if spec, ok := sch.Input.Required[name]; ok {
		return spec, true
	}
	spec, ok := sch.Input.Optional[name]
	return spec, ok
}

// choicesContainValue reports whether value matches one of a combo input's choices,
// exact or by basename (so a subfolder-prefixed file combo like
// "bbox/face_yolov8m.pt" is compared consistently with the same tolerance as
// choicesContain). For a plain label value (no path separator) basename == the
// value, so this reduces to an exact match.
func choicesContainValue(choices []string, value string) bool {
	base := filepath.Base(value)
	for _, c := range choices {
		if c == value || filepath.Base(c) == base {
			return true
		}
	}
	return false
}

// scalarString decodes a node input value that is a plain scalar (string or number)
// into its string form. A link input (a JSON array like ["6",0]) or an object value
// yields ok=false, so only widget-entered scalar values are considered.
func scalarComboValue(raw json.RawMessage) (string, bool) {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s, true
	}
	var num json.Number
	if err := json.Unmarshal(raw, &num); err == nil {
		return num.String(), true
	}
	return "", false
}

// modelSatisfied reports whether a referenced model filename is available: either
// it appears in some loader node's object_info choices list (ComfyUI sees the
// file), or localHave reports it present in the local library by basename.
func modelSatisfied(ref string, nodes map[string]apiNode, info ObjectInfo, localHave func(string) bool) bool {
	if localHave(filepath.Base(ref)) {
		return true
	}
	// Check every kept node's schema choices for this filename. A loader's choices
	// list enumerates the files ComfyUI already has installed for that input.
	for _, n := range nodes {
		sch, ok := info[n.ClassType]
		if !ok {
			continue
		}
		if choicesContain(sch, ref) {
			return true
		}
	}
	return false
}

// choicesContain reports whether any input's combo choices in a node schema include
// filename (exact match, or by basename to tolerate a choices entry that carries a
// subdirectory prefix like "flux/foo.safetensors").
func choicesContain(sch NodeSchema, filename string) bool {
	base := filepath.Base(filename)
	check := func(specs map[string]InputSpec) bool {
		for _, spec := range specs {
			for _, choice := range spec.Choices {
				if choice == filename || filepath.Base(choice) == base {
					return true
				}
			}
		}
		return false
	}
	return check(sch.Input.Required) || check(sch.Input.Optional)
}

func sortedKeys(m map[string]bool) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
