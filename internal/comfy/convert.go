package comfy

import (
	"encoding/json"
	"fmt"
	"strings"
)

// virtualNodeTypes are UI-graph node types that are NOT executed nodes and carry
// no /object_info class_type: they are dropped from the api graph. Reroute is also
// spliced THROUGH during link resolution (its output resolves to its input's
// origin); the others (notes / primitive / rgthree UI-only helpers) simply vanish.
//
// The rgthree "Fast Groups Muter", "Fast Bypasser" and "Bookmark" nodes are
// pure client-side helpers: they have NO backend class in /object_info and carry
// no execution links, so — like Note — they are dropped silently rather than
// warned as "type not available". Only these three UI-only helpers are dropped;
// real rgthree nodes (e.g. "Power Lora Loader (rgthree)") still convert normally.
var virtualNodeTypes = map[string]bool{
	"Reroute":                     true,
	"Note":                        true,
	"MarkdownNote":                true,
	"PrimitiveNode":               true,
	"Primitive":                   true,
	"Fast Groups Muter (rgthree)": true,
	"Fast Bypasser (rgthree)":     true,
	"Bookmark (rgthree)":          true,
	// GetNode/SetNode are virtual "teleport" nodes (KJNodes/rgthree-style): they
	// never become api nodes, but unlike a plain Note they are spliced THROUGH
	// during link resolution — a GetNode re-emits the source a same-named SetNode
	// captured (see resolveOrigin). Listed here so the top loop skips emitting them.
	"SetNode": true,
	"GetNode": true,
}

// UI-graph node modes.
const (
	modeNormal = 0
	modeMuted  = 2
	modeBypass = 4
)

// maxResolveDepth bounds link-splice recursion so a cyclic/adversarial reroute
// chain cannot loop forever.
const maxResolveDepth = 64

// uiConvNode is one node of a UI-format ("Save") graph, as the converter needs it.
type uiConvNode struct {
	ID            json.RawMessage `json:"id"`
	Type          string          `json:"type"`
	Mode          int             `json:"mode"`
	Title         string          `json:"title"`
	WidgetsValues json.RawMessage `json:"widgets_values"`
	Inputs        []uiConvInput   `json:"inputs"`
}

// uiConvInput is one input slot on a UI node: a name, a declared type, and a link
// id (nil when unconnected).
type uiConvInput struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Link *int64 `json:"link"`
}

// uiConvGraph is the UI-format top level the converter parses.
type uiConvGraph struct {
	Nodes       []uiConvNode      `json:"nodes"`
	Links       []json.RawMessage `json:"links"`
	Definitions struct {
		Subgraphs []subgraphDef `json:"subgraphs"`
	} `json:"definitions"`
}

// uiLink is a parsed link: [link_id, origin_node_id, origin_slot, target_node_id,
// target_slot, type].
type uiLink struct {
	ID         int64
	OriginID   string
	OriginSlot int
}

// apiOutNode is one node of the emitted api graph.
type apiOutNode struct {
	ClassType string                     `json:"class_type"`
	Inputs    map[string]json.RawMessage `json:"inputs"`
	Meta      *apiMeta                   `json:"_meta,omitempty"`
}

type apiMeta struct {
	Title string `json:"title"`
}

// ConvertUIToAPI converts a ComfyUI UI-format ("Save") graph into the api-format
// graph that /prompt accepts, mirroring the frontend's graphToPrompt:
//
//   - muted (mode 2) and bypassed (mode 4) nodes are dropped; a bypassed node's
//     inputs are spliced through to its consumers where cleanly possible, and a
//     Reroute is always spliced through.
//   - each kept node's link inputs resolve to ["<origin_id>", slot] refs; its
//     widget values are assigned from widgets_values in input_order, honoring the
//     control_after_generate off-by-one (seed/noise_seed consume an extra slot).
//   - a node type absent from info is OMITTED with a warning (the whole conversion
//     does not fail for one unknown node); all such omissions are collected.
//
// The returned apiGraph is compact JSON that DetectFormat classifies as "api".
// warnings lists every dropped/unresolved condition (an unknown node type, a
// bypass that could not be spliced, a muted origin). err is non-nil only for a
// malformed UI graph or when nothing runnable remains.
func ConvertUIToAPI(uiGraph json.RawMessage, info ObjectInfo) (apiGraph json.RawMessage, warnings []string, err error) {
	var g uiConvGraph
	if err := json.Unmarshal(uiGraph, &g); err != nil {
		return nil, nil, fmt.Errorf("parse ui graph: %w", err)
	}

	// Index links by id.
	linkByID := make(map[int64]uiLink, len(g.Links))
	for _, raw := range g.Links {
		if l, ok := parseLink(raw); ok {
			linkByID[l.ID] = l
		}
	}

	// Expand subgraph group-nodes (definitions.subgraphs[]) into a flat node list
	// BEFORE the rest of conversion. When there are no subgraph definitions this is
	// a no-op and the node list / links are used unchanged (the 41-clean-workflow
	// path is byte-identical). sgRedirect maps a removed instance id -> its output
	// boundary sources, consulted during link resolution.
	nodes := g.Nodes
	var sgRedirect map[string]map[int]origin
	var expandWarnings []string
	if len(g.Definitions.Subgraphs) > 0 {
		nodes, linkByID, sgRedirect, expandWarnings = flattenSubgraphs(&g, linkByID)
	}

	// Index nodes by id.
	byID := make(map[string]*uiConvNode, len(nodes))
	for i := range nodes {
		byID[idToString(nodes[i].ID)] = &nodes[i]
	}

	// Index SetNode captures by constant name so GetNode teleports can be spliced
	// through during link resolution (built from the flattened node list so Set/Get
	// nodes living inside subgraphs are indexed too).
	setByName := buildSetNodeIndex(nodes, linkByID)

	r := &converter{byID: byID, linkByID: linkByID, info: info, setByName: setByName, sgRedirect: sgRedirect}
	r.warnings = append(r.warnings, expandWarnings...)
	result := make(map[string]apiOutNode)

	for i := range nodes {
		n := &nodes[i]
		id := idToString(n.ID)

		// Virtual nodes (reroute/note/primitive) never become api nodes.
		if virtualNodeTypes[n.Type] {
			continue
		}
		// Muted / bypassed nodes are dropped (their passthrough is handled during
		// link resolution for their consumers).
		if n.Mode == modeMuted || n.Mode == modeBypass {
			continue
		}

		sch, known := info[n.Type]
		if !known {
			r.warnf("node %s type %q not available", id, n.Type)
			continue
		}

		inputs, warns := r.buildInputs(n, sch)
		r.warnings = append(r.warnings, warns...)

		out := apiOutNode{ClassType: n.Type, Inputs: inputs}
		if strings.TrimSpace(n.Title) != "" {
			out.Meta = &apiMeta{Title: n.Title}
		}
		result[id] = out
	}

	if len(result) == 0 {
		return nil, r.warnings, fmt.Errorf("conversion produced no runnable nodes")
	}
	out, err := json.Marshal(result)
	if err != nil {
		return nil, r.warnings, fmt.Errorf("marshal api graph: %w", err)
	}
	return out, r.warnings, nil
}

// converter holds the per-conversion indexes + accumulated warnings.
type converter struct {
	byID       map[string]*uiConvNode
	linkByID   map[int64]uiLink
	info       ObjectInfo
	setByName  map[string]teleportSrc
	sgRedirect map[string]map[int]origin
	warnings   []string
}

// teleportSrc is the source (origin node + output slot) a SetNode captured,
// keyed by its constant name. ambiguous is set when more than one SetNode
// declares the same name (an invalid graph we refuse to resolve).
type teleportSrc struct {
	originID   string
	originSlot int
	ok         bool
	ambiguous  bool
}

// buildSetNodeIndex maps each SetNode's constant name to the source feeding its
// input. Duplicate names are marked ambiguous. A SetNode with no connected input
// (or no name) contributes nothing resolvable.
func buildSetNodeIndex(nodes []uiConvNode, linkByID map[int64]uiLink) map[string]teleportSrc {
	idx := make(map[string]teleportSrc)
	for i := range nodes {
		n := &nodes[i]
		if n.Type != "SetNode" {
			continue
		}
		name := getSetNodeName(n)
		if name == "" {
			continue
		}
		src := teleportSrc{}
		if in := firstLinkedInput(n); in >= 0 {
			if l, ok := linkByID[*n.Inputs[in].Link]; ok {
				src = teleportSrc{originID: l.OriginID, originSlot: l.OriginSlot, ok: true}
			}
		}
		if existing, dup := idx[name]; dup {
			existing.ambiguous = true
			idx[name] = existing
			continue
		}
		idx[name] = src
	}
	return idx
}

// getSetNodeName extracts the constant name a Set/GetNode teleports by. The name
// is the first string element of widgets_values; failing that, the node title
// with a leading "Set_/Get_/Set /Get " stripped.
func getSetNodeName(n *uiConvNode) string {
	if arr, ok := asJSONArray(n.WidgetsValues); ok && len(arr) > 0 {
		var s string
		if json.Unmarshal(arr[0], &s) == nil && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	t := strings.TrimSpace(n.Title)
	for _, pfx := range []string{"Set_", "Get_", "Set ", "Get "} {
		if strings.HasPrefix(t, pfx) {
			return strings.TrimSpace(t[len(pfx):])
		}
	}
	return t
}

func (c *converter) warnf(format string, args ...any) {
	c.warnings = append(c.warnings, fmt.Sprintf(format, args...))
}

// buildInputs assembles one node's api inputs: link refs first, then widget values
// walked in input_order with the control_after_generate off-by-one applied.
func (c *converter) buildInputs(n *uiConvNode, sch NodeSchema) (map[string]json.RawMessage, []string) {
	inputs := make(map[string]json.RawMessage)
	var warns []string
	linked := map[string]bool{}

	// 1. Link inputs.
	for _, in := range n.Inputs {
		if in.Link == nil {
			continue
		}
		l, ok := c.linkByID[*in.Link]
		if !ok {
			continue // dangling link id — leave the input unset
		}
		originID, originSlot, ok, warn := c.resolveOrigin(l.OriginID, l.OriginSlot, 0)
		if warn != "" {
			warns = append(warns, warn)
		}
		if !ok {
			continue // unresolved (e.g. muted origin) — leave unset
		}
		ref, _ := json.Marshal([]any{originID, originSlot})
		inputs[in.Name] = ref
		linked[in.Name] = true
	}

	// 2. Widget values.
	wv, isArray := asJSONArray(n.WidgetsValues)
	if !isArray {
		// Some custom nodes serialize widgets_values as an object; we cannot map an
		// unordered object onto ordered inputs. Emit a warning only when there was
		// something to map.
		if isJSONObjectNonEmpty(n.WidgetsValues) {
			warns = append(warns, fmt.Sprintf("node %s uses object-form widgets_values; widget values not mapped", idToString(n.ID)))
		}
		return inputs, warns
	}

	order := append(append([]string{}, sch.InputOrder.Required...), sch.InputOrder.Optional...)
	if len(order) == 0 && len(wv) > 0 && (len(sch.Input.Required)+len(sch.Input.Optional)) > 0 {
		warns = append(warns, fmt.Sprintf("node %s has no input_order; widget values not mapped", idToString(n.ID)))
		return inputs, warns
	}

	cursor := 0
	for _, name := range order {
		spec, ok := lookupSpec(sch, name)
		if !ok || !spec.IsWidget() {
			continue // a link-type input carries no widget value
		}
		if linked[name] {
			// Widget promoted to a link input and connected: its value comes from the
			// link, not widgets_values. (Older ComfyUI omits the value from the array;
			// we do not consume a slot. Widget promotion is an untested edge case.)
			continue
		}
		if cursor >= len(wv) {
			break
		}
		inputs[name] = wv[cursor]
		cursor++
		// control_after_generate quirk: a seed/noise_seed widget consumes an EXTRA
		// widgets_values slot (the fixed/increment/randomize control) with no schema
		// input. Skip it so downstream widgets do not shift.
		if spec.ControlAfterGenerate() || (isIntSpec(spec) && isSeedName(name)) {
			cursor++
		}
	}
	return inputs, warns
}

// resolveOrigin resolves a link's (origin node, output slot) to a concrete
// executed node, splicing through Reroute and bypassed nodes. It returns the
// resolved node id + output slot, ok=false when the origin cannot be resolved
// (e.g. it lands on a muted node), and a non-empty warn describing why.
func (c *converter) resolveOrigin(nodeID string, slot, depth int) (id string, outSlot int, ok bool, warn string) {
	if depth > maxResolveDepth {
		return "", 0, false, fmt.Sprintf("link resolution too deep at node %s", nodeID)
	}
	// A removed subgraph instance: redirect the requested output slot to the
	// internal source that fed that boundary output during expansion.
	if red, ok := c.sgRedirect[nodeID]; ok {
		src, has := red[slot]
		if !has || !src.ok {
			return "", 0, false, "" // boundary output unconnected — leave unset
		}
		return c.resolveOrigin(src.id, src.slot, depth+1)
	}
	n := c.byID[nodeID]
	if n == nil {
		return "", 0, false, ""
	}

	// Reroute: splice through its single input regardless of mode.
	if n.Type == "Reroute" {
		if idx := firstLinkedInput(n); idx >= 0 {
			l := c.linkByID[*n.Inputs[idx].Link]
			return c.resolveOrigin(l.OriginID, l.OriginSlot, depth+1)
		}
		return "", 0, false, fmt.Sprintf("reroute node %s has no input", nodeID)
	}

	// SetNode: splice through the input it captured (its output mirrors its input),
	// regardless of mode — same posture as a Reroute.
	if n.Type == "SetNode" {
		if idx := firstLinkedInput(n); idx >= 0 {
			if l, ok := c.linkByID[*n.Inputs[idx].Link]; ok {
				return c.resolveOrigin(l.OriginID, l.OriginSlot, depth+1)
			}
		}
		return "", 0, false, fmt.Sprintf("SetNode %s has no connected input", nodeID)
	}

	// GetNode: teleport to the source the same-named SetNode captured.
	if n.Type == "GetNode" {
		name := getSetNodeName(n)
		src, found := c.setByName[name]
		if !found || !src.ok {
			return "", 0, false, fmt.Sprintf("GetNode %s references SetNode name %q with no resolvable source", nodeID, name)
		}
		if src.ambiguous {
			return "", 0, false, fmt.Sprintf("GetNode %s name %q is ambiguous (multiple SetNodes)", nodeID, name)
		}
		return c.resolveOrigin(src.originID, src.originSlot, depth+1)
	}

	switch n.Mode {
	case modeBypass:
		// Passthrough: prefer the input at the same index as the requested output
		// slot (1:1 passthrough), else the single connected input.
		idx := -1
		if slot >= 0 && slot < len(n.Inputs) && n.Inputs[slot].Link != nil {
			idx = slot
		} else if only := soleLinkedInput(n); only >= 0 {
			idx = only
		}
		if idx < 0 {
			return "", 0, false, fmt.Sprintf("bypassed node %s could not be spliced (no clean input)", nodeID)
		}
		l := c.linkByID[*n.Inputs[idx].Link]
		return c.resolveOrigin(l.OriginID, l.OriginSlot, depth+1)
	case modeMuted:
		return "", 0, false, fmt.Sprintf("muted node %s output is unavailable", nodeID)
	}
	return nodeID, slot, true, ""
}

// --- small helpers ---

// parseLink parses a UI links[] entry into a uiLink.
func parseLink(raw json.RawMessage) (uiLink, bool) {
	var arr []json.RawMessage
	if json.Unmarshal(raw, &arr) != nil || len(arr) < 5 {
		return uiLink{}, false
	}
	var l uiLink
	if json.Unmarshal(arr[0], &l.ID) != nil {
		return uiLink{}, false
	}
	l.OriginID = idToString(arr[1])
	_ = json.Unmarshal(arr[2], &l.OriginSlot)
	return l, true
}

// idToString renders a node id (JSON int or string) as the string key the api
// graph uses, preserving integers exactly (no float rounding).
func idToString(raw json.RawMessage) string {
	trimmed := strings.TrimSpace(string(raw))
	if strings.HasPrefix(trimmed, `"`) {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return s
		}
	}
	return trimmed
}

// lookupSpec finds an input's spec (required first, then optional).
func lookupSpec(sch NodeSchema, name string) (InputSpec, bool) {
	if s, ok := sch.Input.Required[name]; ok {
		return s, true
	}
	if s, ok := sch.Input.Optional[name]; ok {
		return s, true
	}
	return InputSpec{}, false
}

func isIntSpec(s InputSpec) bool { return strings.EqualFold(strings.TrimSpace(s.TypeName), "INT") }

func isSeedName(name string) bool { return name == "seed" || name == "noise_seed" }

// firstLinkedInput returns the index of the first input with a non-nil link, or -1.
func firstLinkedInput(n *uiConvNode) int {
	for i, in := range n.Inputs {
		if in.Link != nil {
			return i
		}
	}
	return -1
}

// soleLinkedInput returns the index of the ONLY input with a link, or -1 when
// there are zero or more than one.
func soleLinkedInput(n *uiConvNode) int {
	idx := -1
	for i, in := range n.Inputs {
		if in.Link == nil {
			continue
		}
		if idx >= 0 {
			return -1 // more than one
		}
		idx = i
	}
	return idx
}

// asJSONArray unmarshals raw into a slice of raw elements when it is a JSON array.
func asJSONArray(raw json.RawMessage) ([]json.RawMessage, bool) {
	if !isJSONArray(raw) {
		return nil, false
	}
	var arr []json.RawMessage
	if json.Unmarshal(raw, &arr) != nil {
		return nil, false
	}
	return arr, true
}

// isJSONObjectNonEmpty reports whether raw is a JSON object with at least one key.
func isJSONObjectNonEmpty(raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	if !strings.HasPrefix(trimmed, "{") {
		return false
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return false
	}
	return len(m) > 0
}
