package comfy

import "encoding/json"

// OptionFixKey identifies one combo-input fix by the input's name and its current
// (invalid) value. A single key rewrites EVERY node whose input of that name holds
// that value — matching how Preflight GROUPS bad options by (InputName, Current).
type OptionFixKey struct {
	InputName string
	OldValue  string
}

// ValidateOptionFixes filters requested option fixes down to the SAFE, applicable
// ones: a fix is kept only when (a) it matches a detected BadOption group by
// (InputName, OldValue) AND (b) its chosen new value is actually one of that group's
// live combo Choices. An off-list value, or a fix for an input that is not a bad
// option, is dropped — so the caller can never inject an arbitrary value into the
// graph. Returns a fresh map (never nil-panics on an empty input).
func ValidateOptionFixes(requested map[OptionFixKey]string, bad []BadOption) map[OptionFixKey]string {
	out := map[OptionFixKey]string{}
	if len(requested) == 0 {
		return out
	}
	byKey := make(map[OptionFixKey][]string, len(bad))
	for _, bo := range bad {
		byKey[OptionFixKey{InputName: bo.InputName, OldValue: bo.Current}] = bo.Choices
	}
	for key, newVal := range requested {
		choices, ok := byKey[key]
		if !ok {
			continue // not a detected bad option
		}
		if !choicesContainValue(choices, newVal) {
			continue // off-list value — refuse to inject it
		}
		out[key] = newVal
	}
	return out
}

// ApplyOptionFixes returns a COPY of the api-format graph with every combo input
// matching a fix key (InputName + current value == OldValue) rewritten to the fix's
// new value — on ANY node class (unlike ApplySubstitutions, which is loader-scoped).
// Link inputs (array values) and non-matching inputs are left untouched, and the
// input graph is NEVER mutated. A parse failure or empty fix set returns apiGraph
// unchanged.
func ApplyOptionFixes(apiGraph json.RawMessage, fixes map[OptionFixKey]string) json.RawMessage {
	if len(fixes) == 0 {
		return apiGraph
	}
	var nodes map[string]json.RawMessage
	if err := json.Unmarshal(apiGraph, &nodes); err != nil {
		return apiGraph
	}
	changed := false
	for id, raw := range nodes {
		var node map[string]json.RawMessage
		if err := json.Unmarshal(raw, &node); err != nil {
			continue
		}
		inputsRaw, ok := node["inputs"]
		if !ok {
			continue
		}
		var inputs map[string]json.RawMessage
		if err := json.Unmarshal(inputsRaw, &inputs); err != nil {
			continue
		}
		nodeChanged := false
		for k, v := range inputs {
			var sv string
			if err := json.Unmarshal(v, &sv); err != nil {
				continue // link array, number, or object — never a targeted combo string
			}
			repl, ok := fixes[OptionFixKey{InputName: k, OldValue: sv}]
			if !ok {
				continue
			}
			nb, err := json.Marshal(repl)
			if err != nil {
				continue
			}
			inputs[k] = nb
			nodeChanged = true
		}
		if !nodeChanged {
			continue
		}
		ib, err := json.Marshal(inputs)
		if err != nil {
			continue
		}
		node["inputs"] = ib
		nb, err := json.Marshal(node)
		if err != nil {
			continue
		}
		nodes[id] = nb
		changed = true
	}
	if !changed {
		return apiGraph
	}
	out, err := json.Marshal(nodes)
	if err != nil {
		return apiGraph
	}
	return out
}
