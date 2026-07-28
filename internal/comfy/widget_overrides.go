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

var integerLiteralRe = regexp.MustCompile(`^-?\d+$`)

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
