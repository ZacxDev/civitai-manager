package comfy

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

// WidgetOverrideKey identifies ONE editable widget input to override, by its api-graph
// node id (preserved from the UI node id through ConvertUIToAPI) and input name. A
// prompt is node-specific, so the key is node+input — distinct from OptionFixKey
// (which rewrites a combo by value across all nodes).
type WidgetOverrideKey struct {
	NodeID    string
	InputName string
}

// UIWidgetKey identifies ONE editable widget slot in a UI-format ("Save") graph, by
// node id and the slot's INDEX in that node's widgets_values array. This is the key
// DetectRunInputs emits (RunInput.NodeID + RunInput.WidgetIndex).
//
// Why an index and not an input name: the value a curated node shows may live on a
// DIFFERENT, upstream node (a widget converted to an input, driven by a primitive or a
// custom node), and mapping that node's widgets_values slots to schema input NAMES
// needs /object_info — which the page-render path deliberately does not fetch. The
// index needs nothing but the graph. ConvertUIToAPI then performs the index → name
// mapping exactly once, with the live schema, so an override written here lands on the
// correct api input.
type UIWidgetKey struct {
	NodeID string
	Widget int
}

var integerLiteralRe = regexp.MustCompile(`^-?\d+$`)

// ApplyUIWidgetOverrides returns a COPY of a UI-format graph with each targeted
// node's targeted widgets_values slot rewritten to the override value. It must run
// BEFORE ConvertUIToAPI (the converter maps the edited slot onto the right api input
// name). Like ApplyWidgetOverrides it is deliberately narrow:
//
//   - it rewrites ONLY an EXISTING slot of an EXISTING node — never appends a slot,
//     never creates a node;
//   - the replacement PRESERVES the slot's JSON type (a string stays a string, an
//     integer keeps full precision, a numeric slot given a non-numeric override is left
//     unchanged), and an array/object/bool/null slot is never touched;
//   - the input graph is never mutated. A parse failure, a non-array widgets_values, or
//     an empty override set returns uiGraph unchanged.
func ApplyUIWidgetOverrides(uiGraph json.RawMessage, overrides map[UIWidgetKey]string) json.RawMessage {
	if len(overrides) == 0 {
		return uiGraph
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(uiGraph, &doc); err != nil {
		return uiGraph
	}
	var nodes []json.RawMessage
	if err := json.Unmarshal(doc["nodes"], &nodes); err != nil {
		return uiGraph
	}
	changed := false
	for i, raw := range nodes {
		var node map[string]json.RawMessage
		if err := json.Unmarshal(raw, &node); err != nil {
			continue
		}
		id := idToString(node["id"])
		if id == "" {
			continue
		}
		wv, ok := asJSONArray(node["widgets_values"])
		if !ok {
			continue // object-form or absent widgets_values — no indexed slot to rewrite
		}
		nodeChanged := false
		for idx := range wv {
			val, ok := overrides[UIWidgetKey{NodeID: id, Widget: idx}]
			if !ok {
				continue
			}
			repl, ok := typedOverrideValue(wv[idx], val)
			if !ok {
				continue // array/object/bool slot, or a numeric slot with an unparseable value
			}
			wv[idx] = repl
			nodeChanged = true
		}
		if !nodeChanged {
			continue
		}
		wb, err := json.Marshal(wv)
		if err != nil {
			continue
		}
		node["widgets_values"] = wb
		nb, err := json.Marshal(node)
		if err != nil {
			continue
		}
		nodes[i] = nb
		changed = true
	}
	if !changed {
		return uiGraph
	}
	nb, err := json.Marshal(nodes)
	if err != nil {
		return uiGraph
	}
	doc["nodes"] = nb
	out, err := json.Marshal(doc)
	if err != nil {
		return uiGraph
	}
	return out
}

// ApplyWidgetOverrides returns a COPY of the api-format graph with each targeted
// node's targeted input rewritten to the override value. It is deliberately narrow:
//
//   - it rewrites ONLY an input that ALREADY EXISTS on the targeted node — it never
//     adds a key and never creates a node;
//   - it NEVER touches a link input (an array value like ["4",0]) or an object value —
//     only a scalar string/number widget value is replaced;
//   - the replacement PRESERVES the existing value's JSON type: a string stays a
//     string, a number stays a number (integers keep full precision), and a numeric
//     input that receives a non-numeric override is left unchanged.
//
// The input graph is never mutated (it is parsed into fresh maps and a new document
// is marshaled), so a caller passing the stored workflow's bytes gets them back
// byte-identical. A parse failure or empty override set returns apiGraph unchanged.
func ApplyWidgetOverrides(apiGraph json.RawMessage, overrides map[WidgetOverrideKey]string) json.RawMessage {
	if len(overrides) == 0 {
		return apiGraph
	}
	var nodes map[string]json.RawMessage
	if err := json.Unmarshal(apiGraph, &nodes); err != nil {
		return apiGraph
	}
	changed := false
	for key, val := range overrides {
		raw, ok := nodes[key.NodeID]
		if !ok {
			continue // unknown node — ignore
		}
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
		existing, ok := inputs[key.InputName]
		if !ok {
			continue // never ADD an input the node does not already have
		}
		repl, ok := typedOverrideValue(existing, val)
		if !ok {
			continue // link/object value, or a number field with an unparseable override
		}
		inputs[key.InputName] = repl
		ib, err := json.Marshal(inputs)
		if err != nil {
			continue
		}
		node["inputs"] = ib
		nb, err := json.Marshal(node)
		if err != nil {
			continue
		}
		nodes[key.NodeID] = nb
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

// typedOverrideValue produces the JSON replacement for an existing input value,
// preserving its JSON type. It returns ok=false for a link array / object / bool /
// null existing value (never rewritten), or when a numeric field's override does not
// parse as a number.
func typedOverrideValue(existing json.RawMessage, val string) (json.RawMessage, bool) {
	t := bytes.TrimSpace(existing)
	if len(t) == 0 {
		return nil, false
	}
	switch t[0] {
	case '[', '{':
		return nil, false // link ref or object — never touch
	case '"':
		b, err := json.Marshal(val)
		if err != nil {
			return nil, false
		}
		return b, true
	default:
		if t[0] == '-' || (t[0] >= '0' && t[0] <= '9') {
			return numberJSONFromString(val)
		}
		return nil, false // bool / null — outside the curated (string/number) set
	}
}

// numberJSONFromString turns a form string into a JSON number token. An integer is
// emitted verbatim (preserving arbitrary precision, e.g. a 19-digit seed that would
// lose precision through float64); a decimal parses through float64. A non-numeric
// string yields ok=false.
func numberJSONFromString(s string) (json.RawMessage, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, false
	}
	if integerLiteralRe.MatchString(s) {
		return json.RawMessage(s), true
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil, false
	}
	b, err := json.Marshal(f)
	if err != nil {
		return nil, false
	}
	return b, true
}
