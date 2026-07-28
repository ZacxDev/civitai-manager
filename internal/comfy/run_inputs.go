package comfy

import (
	"bytes"
	"encoding/json"
	"strings"
)

// RunInputKind classifies one editable run input so the UI can pick the right
// control (a textarea for prompts, a number field for steps/cfg, a number+dice for
// seeds, a select for sampler/scheduler).
type RunInputKind string

const (
	RunInputText   RunInputKind = "text"   // multiline prompt → <textarea>
	RunInputInt    RunInputKind = "int"    // integer number field
	RunInputFloat  RunInputKind = "float"  // float number field
	RunInputSeed   RunInputKind = "seed"   // integer + a randomize control
	RunInputSelect RunInputKind = "select" // enum (sampler/scheduler) → <select> (or text fallback)
)

// RunInput is one detected, editable per-run input on a TOP-LEVEL graph node. It is
// pre-filled with the graph's current value (Current) and applied as an ephemeral
// override keyed by (NodeID, InputName) — the api-graph node key is preserved from
// this UI node id through ConvertUIToAPI, so the same key targets the converted node.
type RunInput struct {
	NodeID    string
	ClassType string
	InputName string
	Label     string
	Kind      RunInputKind
	Current   string
	// Choices are the enum options for a select input, populated from /object_info
	// when it is supplied to DetectRunInputs (nil → the UI degrades to a text field).
	Choices []string
}

// runWidget describes one widget slot in a curated node's widgets_values layout, in
// widget order (link inputs excluded — they carry no widget value). expose marks the
// slots surfaced as editable; the non-exposed ones are walked only to keep the
// widgets_values cursor aligned. isSeed marks a seed/noise_seed widget, which is
// followed in a UI graph by an extra control_after_generate slot the walk must skip.
type runWidget struct {
	name   string
	label  string
	kind   RunInputKind
	expose bool
	isSeed bool
}

// runInputLayouts is the CURATED set: the widget-value layout of the handful of core
// ComfyUI nodes whose key inputs are worth editing per-run. Only these exact class
// names match — a custom node with a different class is never touched. The layouts
// mirror each node's stable INPUT_TYPES widget order (modern ComfyUI serialization),
// which is what the converter walks via input_order; here it is hardcoded so
// detection needs no live /object_info.
var runInputLayouts = map[string][]runWidget{
	"CLIPTextEncode": {
		{name: "text", label: "Prompt", kind: RunInputText, expose: true},
	},
	// SDXL: six sizing ints precede the two prompt boxes (text_g / text_l).
	"CLIPTextEncodeSDXL": {
		{name: "width", kind: RunInputInt},
		{name: "height", kind: RunInputInt},
		{name: "crop_w", kind: RunInputInt},
		{name: "crop_h", kind: RunInputInt},
		{name: "target_width", kind: RunInputInt},
		{name: "target_height", kind: RunInputInt},
		{name: "text_g", label: "Prompt (G)", kind: RunInputText, expose: true},
		{name: "text_l", label: "Prompt (L)", kind: RunInputText, expose: true},
	},
	"KSampler": {
		{name: "seed", label: "Seed", kind: RunInputSeed, expose: true, isSeed: true},
		{name: "steps", label: "Steps", kind: RunInputInt, expose: true},
		{name: "cfg", label: "CFG", kind: RunInputFloat, expose: true},
		{name: "sampler_name", label: "Sampler", kind: RunInputSelect, expose: true},
		{name: "scheduler", label: "Scheduler", kind: RunInputSelect, expose: true},
		{name: "denoise", label: "Denoise", kind: RunInputFloat, expose: true},
	},
	"KSamplerAdvanced": {
		{name: "add_noise", kind: RunInputSelect},
		{name: "noise_seed", label: "Noise seed", kind: RunInputSeed, expose: true, isSeed: true},
		{name: "steps", label: "Steps", kind: RunInputInt, expose: true},
		{name: "cfg", label: "CFG", kind: RunInputFloat, expose: true},
		{name: "sampler_name", label: "Sampler", kind: RunInputSelect, expose: true},
		{name: "scheduler", label: "Scheduler", kind: RunInputSelect, expose: true},
		{name: "start_at_step", kind: RunInputInt},
		{name: "end_at_step", kind: RunInputInt},
		{name: "return_with_leftover_noise", kind: RunInputSelect},
	},
	"EmptyLatentImage": {
		{name: "width", label: "Width", kind: RunInputInt, expose: true},
		{name: "height", label: "Height", kind: RunInputInt, expose: true},
		{name: "batch_size", label: "Batch size", kind: RunInputInt, expose: true},
	},
	"EmptySD3LatentImage": {
		{name: "width", label: "Width", kind: RunInputInt, expose: true},
		{name: "height", label: "Height", kind: RunInputInt, expose: true},
		{name: "batch_size", label: "Batch size", kind: RunInputInt, expose: true},
	},
}

// seedControlValues are the control_after_generate strings a UI graph stores in the
// extra widgets_values slot right after a seed/noise_seed value; the walk skips such
// a slot so later widget values stay aligned.
var seedControlValues = map[string]bool{
	"fixed": true, "increment": true, "decrement": true, "randomize": true,
}

// DetectRunInputs scans a UI-format ("Save") graph and returns the curated, editable
// run inputs for its TOP-LEVEL nodes (prompts, KSampler settings, empty-latent
// dimensions). Subgraph-interior nodes are NOT scanned — they live under
// definitions.subgraphs[] (not the top-level nodes[]) and their ids are rewritten by
// flattening, so they are out of scope. An input whose slot is link-connected is
// skipped (its value comes from a link, not a widget). Pre-fill values are read from
// each node's widgets_values by the curated layout. info may be nil; when present it
// supplies the enum Choices for sampler_name/scheduler. An api-format graph (no
// widgets_values) yields no inputs.
func DetectRunInputs(graph json.RawMessage, info ObjectInfo) []RunInput {
	var g uiConvGraph
	if err := json.Unmarshal(graph, &g); err != nil {
		return nil
	}
	var out []RunInput
	for i := range g.Nodes {
		n := &g.Nodes[i]
		layout, ok := runInputLayouts[n.Type]
		if !ok {
			continue
		}
		out = append(out, detectNodeRunInputs(n, layout, info)...)
	}
	return out
}

// detectNodeRunInputs walks ONE node's widgets_values by its curated layout,
// emitting the exposed, non-link-connected inputs pre-filled with their current
// value. It advances the cursor for every widget slot (exposed or not, including the
// seed control_after_generate slot) so the positions of later widgets are correct.
func detectNodeRunInputs(n *uiConvNode, layout []runWidget, info ObjectInfo) []RunInput {
	var wv []json.RawMessage
	if json.Unmarshal(n.WidgetsValues, &wv) != nil {
		return nil // object-form or absent widgets_values — curated nodes use array form
	}
	linked := linkedInputNames(n)
	nodeID := idToString(n.ID)
	title := strings.TrimSpace(n.Title)

	var out []RunInput
	cursor := 0
	for _, wdg := range layout {
		valIdx := cursor
		cursor++
		if wdg.isSeed && cursor < len(wv) && isSeedControlSlot(wv[cursor]) {
			cursor++ // consume the control_after_generate slot
		}
		if !wdg.expose || linked[wdg.name] {
			continue
		}
		cur := ""
		if valIdx < len(wv) {
			cur = scalarWidgetString(wv[valIdx])
		}
		ri := RunInput{
			NodeID:    nodeID,
			ClassType: n.Type,
			InputName: wdg.name,
			Label:     runInputLabel(wdg, title),
			Kind:      wdg.kind,
			Current:   cur,
		}
		if wdg.kind == RunInputSelect {
			ri.Choices = comboInputChoices(info, n.Type, wdg.name)
		}
		out = append(out, ri)
	}
	return out
}

// runInputLabel builds an input's display label. For a prompt (text) input the node
// title — when the graph carries one — is appended so a positive/negative pair is
// distinguishable ("Prompt (Positive)"); numeric/enum labels are left as-is.
func runInputLabel(wdg runWidget, title string) string {
	if wdg.kind == RunInputText && title != "" {
		return wdg.label + " (" + title + ")"
	}
	return wdg.label
}

// linkedInputNames is the set of a node's input slot names that are wired to a link
// (value comes from an upstream node, not a widget).
func linkedInputNames(n *uiConvNode) map[string]bool {
	m := make(map[string]bool, len(n.Inputs))
	for _, in := range n.Inputs {
		if in.Link != nil {
			m[in.Name] = true
		}
	}
	return m
}

// isSeedControlSlot reports whether a widgets_values slot holds a
// control_after_generate string ("fixed"/"increment"/"decrement"/"randomize").
func isSeedControlSlot(raw json.RawMessage) bool {
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return false
	}
	return seedControlValues[strings.ToLower(strings.TrimSpace(s))]
}

// scalarWidgetString renders a scalar widget value (string, number, or bool) as a
// plain string for pre-filling a form control. A link array / object yields "".
func scalarWidgetString(raw json.RawMessage) string {
	t := bytes.TrimSpace(raw)
	if len(t) == 0 {
		return ""
	}
	switch t[0] {
	case '"':
		var s string
		if json.Unmarshal(t, &s) == nil {
			return s
		}
		return ""
	case '[', '{':
		return ""
	default:
		return string(t) // number / bool / null literal
	}
}

// comboInputChoices returns the enum choices for a node input from /object_info
// (required then optional), or nil when info is absent or the input is not a
// string-choice combo.
func comboInputChoices(info ObjectInfo, classType, inputName string) []string {
	sch, ok := info[classType]
	if !ok {
		return nil
	}
	if spec, ok := sch.Input.Required[inputName]; ok && spec.IsCombo && len(spec.Choices) > 0 {
		return spec.Choices
	}
	if spec, ok := sch.Input.Optional[inputName]; ok && spec.IsCombo && len(spec.Choices) > 0 {
		return spec.Choices
	}
	return nil
}
