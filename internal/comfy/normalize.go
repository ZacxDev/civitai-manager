package comfy

import "encoding/json"

// inertPickerNodes are the ImpactPack node class_types that carry a frontend-only
// "action picker" combo (a combo you pick from to APPEND text to another field,
// whose own value is never read by the Python node). ComfyUI's frontend overrides
// each such widget's serializeValue() to ALWAYS emit the placeholder choice
// ("Preventing validation errors from occurring in any situation") — so a drifted
// saved value never reaches the backend. Our converter reimplements graphToPrompt
// without running that JS, so it must mirror the normalization here.
//
// See claudedocs/INERT-OPTION-DRIFT-DESIGN.md §2 and js/impact-pack.js:712-801.
var inertPickerNodes = []string{
	"ImpactWildcardEncode",
	"ImpactWildcardProcessor",
	"ToDetailerPipe",
	"ToDetailerPipeSDXL",
	"EditDetailerPipe",
	"EditDetailerPipeSDXL",
	"BasicPipeToDetailerPipe",
	"BasicPipeToDetailerPipeSDXL",
}

// inertPickerInputs are the two frontend-only picker input names on the node family
// above. Both are force-normalized by serializeValue and inert at execution.
var inertPickerInputs = []string{"Select to add Wildcard", "Select to add LoRA"}

// inertActionPickers is the curated (classType, inputName) set of ImpactPack
// frontend-only inert pickers safe to normalize even when they are multi-choice
// (e.g. "Select to add LoRA" carries the full installed-LoRA list as choices, but
// its value is inert and always serialized to the placeholder Choices[0]).
var inertActionPickers = func() map[[2]string]bool {
	m := make(map[[2]string]bool, len(inertPickerNodes)*len(inertPickerInputs))
	for _, class := range inertPickerNodes {
		for _, input := range inertPickerInputs {
			m[[2]string{class, input}] = true
		}
	}
	return m
}()

// normalizeComboWidget mirrors ComfyUI's frontend serializeValue normalization for
// combo (enum) widget values that have DRIFTED (the saved value is no longer among
// the installed node's /object_info choices). It returns a VALID choice in the two
// cases where doing so cannot change the workflow's output — otherwise it returns
// the raw value unchanged (a genuine, output-affecting multi-choice drift stays a
// BadOption so the existing install/substitute UI handles it).
//
//	Tier 1 (structural): the combo has exactly ONE valid choice → there is no
//	  decision to make; emit that choice.
//	Tier 2 (curated):     the (classType, inputName) is a known ImpactPack inert
//	  action picker → emit the placeholder choice (Choices[0]), mirroring
//	  serializeValue.
//
// CRITICAL guard: a combo whose current value is a MODEL FILE is NEVER normalized,
// even when single-choice — silently swapping a model to a different installed one
// changes the output. Those stay BadOptions for the model-install/substitute flow.
//
// Normalization is FULLY SILENT — it must NOT emit a conversion warning, because the
// run path aborts on any warning (`len(warnings) > 0`), which would turn a
// successfully-normalized graph into a "could not be converted" failure. ComfyUI's
// own frontend shows nothing here, so neither do we. The value is only changed on the
// EMITTED api graph; the stored graph is never mutated.
func (c *converter) normalizeComboWidget(classType, inputName string, spec InputSpec, raw json.RawMessage) json.RawMessage {
	if !spec.IsCombo || len(spec.Choices) == 0 {
		return raw // not a combo, or non-string (uncomparable) choices
	}
	val, ok := scalarComboValue(raw)
	if !ok {
		return raw // link array / object / non-scalar — not a combo widget value
	}
	if choicesContainValue(spec.Choices, val) {
		return raw // already a valid choice — nothing to normalize
	}
	// CRITICAL guard — never silently swap a model file to a different installed one.
	// A drifted model combo must remain a BadOption for the install/substitute UI.
	if hasModelExtension(val) {
		return raw
	}

	switch {
	case len(spec.Choices) == 1:
		// Tier 1: single valid choice — provably loss-free, no decision. Silent.
		return marshalString(spec.Choices[0])
	case inertActionPickers[[2]string{classType, inputName}]:
		// Tier 2: curated inert action picker — emit the placeholder (Choices[0]),
		// mirroring ComfyUI's serializeValue override. Silent.
		return marshalString(spec.Choices[0])
	}
	return raw // genuine multi-choice, non-inert drift — leave as a BadOption
}

// marshalString encodes s as a JSON string. Marshaling a plain string never fails,
// so the error is dropped.
func marshalString(s string) json.RawMessage {
	b, _ := json.Marshal(s)
	return b
}
