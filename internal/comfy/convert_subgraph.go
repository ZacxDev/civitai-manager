package comfy

import (
	"encoding/json"
	"fmt"
	"strconv"
)

// maxSubgraphDepth bounds recursive subgraph expansion so a cyclic or adversarial
// definitions.subgraphs[] (a subgraph that instantiates itself, directly or via a
// cycle) cannot expand forever. It reuses the same bounded-recursion posture as
// maxResolveDepth.
const maxSubgraphDepth = maxResolveDepth

// maxSubgraphNodes caps the TOTAL expansion work (every expand() call plus every
// emitted interior node — see sgExpander.count). The depth bound alone does NOT
// prevent exponential blow-up: a self-referential (or short-cycle) subgraph where
// each instance holds ≥2 child instances fans out to ~2^depth recursive expand()
// calls before the per-leaf depth guard trips (CPU/OOM DoS on running a hostile
// imported workflow — no context threads through this pure-CPU recursion to
// interrupt it). Counting WORK (not just emitted nodes) is required because a
// purely-nested self-referential subgraph emits zero clones yet still recurses
// exponentially. Once the work count crosses this bound, expansion aborts
// IMMEDIATELY (via sgExpander.aborted) and ConvertUIToAPI returns an error.
//
// Chosen empirically: the max flattened node count across the 68 dogfood workflows
// is 235; this cap (10000, ~42×) sits comfortably above any real workflow yet far
// below the exponential fan-out an adversarial graph would attempt.
const maxSubgraphNodes = 10000

// subgraphDef is one entry of the workflow's definitions.subgraphs[]: a reusable
// group-node "blueprint" identified by a UUID (used as a node instance's type).
// Its interior is an ordinary UI graph (nodes+links) bracketed by two virtual
// boundary I/O nodes; inputs/outputs are the ordered boundary slots that map the
// instance's external links onto interior nodes.
type subgraphDef struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	InputNode  sgIONode          `json:"inputNode"`
	OutputNode sgIONode          `json:"outputNode"`
	Inputs     []sgIOSlot        `json:"inputs"`
	Outputs    []sgIOSlot        `json:"outputs"`
	Nodes      []uiConvNode      `json:"nodes"`
	Links      []json.RawMessage `json:"links"`
}

// sgIONode is a subgraph's boundary I/O node reference; only its id matters for
// lowering (interior links whose origin is inputNode.id are boundary inputs, and
// interior links whose target is outputNode.id are boundary outputs).
type sgIONode struct {
	ID json.RawMessage `json:"id"`
}

// sgIOSlot is one ordered boundary slot (an input or an output) of a subgraph.
// Only its position (index in Inputs/Outputs) is used for wiring; name/type are
// advisory. Type is json.RawMessage (never read) for the same reason as
// uiConvInput.Type: a COMBO/enum boundary slot can carry an ARRAY type instead of
// a string, which would break the graph-level Unmarshal if typed `string`.
type sgIOSlot struct {
	ID   json.RawMessage `json:"id"`
	Name string          `json:"name"`
	Type json.RawMessage `json:"type"`
}

// origin is a resolved source: an executable node id and its output slot. ok is
// false when a boundary slot is unconnected (the consumer input is left unset).
type origin struct {
	id   string
	slot int
	ok   bool
}

// sgInternalLink is a parsed interior subgraph link. Interior links serialize as
// either the top-level array form [id, origin_id, origin_slot, target_id,
// target_slot, type] or the newer object form; both are supported.
type sgInternalLink struct {
	id         int64
	originID   string
	originSlot int
	targetID   string
	targetSlot int
}

// sgExpander flattens subgraph instances into a single-level node list. It emits
// executable interior nodes with instance-unique prefixed ids, synthesizes the
// links wiring them (fresh negative ids that never collide with real link ids),
// and records, for each removed instance, the interior source feeding each of its
// output boundary slots (redirect) so link resolution can splice through.
type sgExpander struct {
	defs     map[string]*subgraphDef
	outNodes []uiConvNode
	linkByID map[int64]uiLink
	redirect map[string]map[int]origin
	nextLink int64
	warnings []string
	// count is the total expansion WORK: every expand() entry plus every emitted
	// interior node. Bounding work (not just emitted nodes) is essential — a
	// self-referential subgraph whose interior is only ≥2 nested instances (and no
	// ordinary nodes) emits ZERO clones yet triggers ~2^depth recursive expand()
	// calls, so an emitted-node cap alone would not stop it.
	count int
	// aborted trips when count crosses maxSubgraphNodes; once set, expand() unwinds
	// immediately at every recursion boundary so an exponential fan-out bails after
	// ~cap units of work instead of finishing the blow-up.
	aborted bool
}

// charge accounts one unit of expansion work and returns false (setting aborted)
// the moment the total crosses the cap, so the caller stops immediately.
func (e *sgExpander) charge() bool {
	if e.aborted {
		return false
	}
	e.count++
	if e.count > maxSubgraphNodes {
		e.aborted = true
		return false
	}
	return true
}

// emit appends a flattened interior node, charging one unit of work. It returns
// false (and sets aborted) the moment the cap is crossed so the caller stops.
func (e *sgExpander) emit(n uiConvNode) bool {
	if !e.charge() {
		return false
	}
	e.outNodes = append(e.outNodes, n)
	return true
}

// flattenSubgraphs expands every subgraph group-node instance in g into a flat
// node list. It returns the flattened nodes, the link table extended with the
// synthesized interior links, the instance-output redirect map, and any warnings.
// Nodes whose type is not a subgraph id are carried through unchanged.
func flattenSubgraphs(g *uiConvGraph, linkByID map[int64]uiLink) ([]uiConvNode, map[int64]uiLink, map[string]map[int]origin, []string, error) {
	defs := make(map[string]*subgraphDef, len(g.Definitions.Subgraphs))
	for i := range g.Definitions.Subgraphs {
		d := &g.Definitions.Subgraphs[i]
		if d.ID != "" {
			defs[d.ID] = d
		}
	}

	e := &sgExpander{
		defs:     defs,
		linkByID: linkByID,
		redirect: map[string]map[int]origin{},
		nextLink: -1,
	}

	for i := range g.Nodes {
		if e.aborted {
			break
		}
		n := &g.Nodes[i]
		def, isInstance := defs[n.Type]
		if !isInstance {
			if !e.emit(*n) { // ordinary top-level node, unchanged
				break
			}
			continue
		}
		instID := idToString(n.ID)
		inputSrc := e.instanceInputSources(n, def, linkByID)
		outSrc := e.expand(def, instID, inputSrc, 0)
		e.registerRedirect(instID, outSrc)
	}

	if e.aborted {
		return nil, nil, nil, nil, fmt.Errorf("workflow too large: subgraph expansion exceeded %d nodes", maxSubgraphNodes)
	}
	return e.outNodes, e.linkByID, e.redirect, e.warnings, nil
}

// instanceInputSources reads the external source feeding each of the instance's
// boundary input slots (by index), in the instance's own namespace. An
// unconnected slot yields origin{ok:false}.
func (e *sgExpander) instanceInputSources(inst *uiConvNode, def *subgraphDef, linkByID map[int64]uiLink) []origin {
	src := make([]origin, len(def.Inputs))
	for k := range def.Inputs {
		src[k] = origin{}
		if k < len(inst.Inputs) && inst.Inputs[k].Link != nil {
			if l, ok := linkByID[*inst.Inputs[k].Link]; ok {
				src[k] = origin{id: l.OriginID, slot: l.OriginSlot, ok: true}
			}
		}
	}
	return src
}

// expand inlines one subgraph instance's interior. prefix is the instance's
// unique id path (nested instances extend it), inputSrc are the resolved external
// sources for the boundary input slots. It returns the resolved sources for the
// boundary output slots (in the caller's namespace), for the caller's redirect.
func (e *sgExpander) expand(def *subgraphDef, prefix string, inputSrc []origin, depth int) []origin {
	if !e.charge() {
		return nil
	}
	if depth > maxSubgraphDepth {
		e.warnf("subgraph %q nested deeper than %d; not expanded", def.Name, maxSubgraphDepth)
		return nil
	}

	inputNodeID := idToString(def.InputNode.ID)
	outputNodeID := idToString(def.OutputNode.ID)
	intLinks := parseSubgraphLinks(def.Links)

	// resolveInterior maps an interior (origin_id, origin_slot) to a final source:
	// a boundary input redirects to the external inputSrc; any other interior node
	// id becomes prefixed.
	resolveInterior := func(originID string, originSlot int) origin {
		if originID == inputNodeID {
			if originSlot >= 0 && originSlot < len(inputSrc) {
				return inputSrc[originSlot]
			}
			return origin{}
		}
		return origin{id: prefix + ":" + originID, slot: originSlot, ok: true}
	}

	for i := range def.Nodes {
		if e.aborted {
			return nil
		}
		m := &def.Nodes[i]
		mID := idToString(m.ID)
		if mID == inputNodeID || mID == outputNodeID {
			continue // boundary I/O nodes are not executable
		}
		finalID := prefix + ":" + mID

		if nestedDef, isNested := e.defs[m.Type]; isNested {
			// A nested subgraph instance: resolve ITS boundary input sources in this
			// interior's namespace, recurse, and redirect its outputs.
			nestedInputSrc := make([]origin, len(nestedDef.Inputs))
			for k := range nestedDef.Inputs {
				nestedInputSrc[k] = origin{}
				if k < len(m.Inputs) && m.Inputs[k].Link != nil {
					if il, ok := intLinks[*m.Inputs[k].Link]; ok {
						nestedInputSrc[k] = resolveInterior(il.originID, il.originSlot)
					}
				}
			}
			outSrc := e.expand(nestedDef, finalID, nestedInputSrc, depth+1)
			if e.aborted {
				return nil
			}
			e.registerRedirect(finalID, outSrc)
			continue
		}

		// An ordinary interior node: clone it with a prefixed id and inputs rewired
		// to synthesized links pointing at their resolved sources.
		clone := *m
		clone.ID = json.RawMessage(strconv.Quote(finalID))
		clone.WidgetsValues = e.scopeTeleportName(m, prefix)
		clone.Inputs = make([]uiConvInput, len(m.Inputs))
		for j, in := range m.Inputs {
			// Carry the widget-promotion marker into the clone: a promoted widget slot
			// (widget field present) KEEPS its widgets_values slot even when link-connected,
			// and buildInputs relies on that marker (hasWidgetMarker → widgetBacked) to
			// consume the slot without shifting later widget values. Dropping it here would
			// misalign every subsequent widget value on interior nodes (Link is synthesized
			// separately just below).
			ni := uiConvInput{Name: in.Name, Type: in.Type, Widget: in.Widget}
			if in.Link != nil {
				if il, ok := intLinks[*in.Link]; ok {
					if s := resolveInterior(il.originID, il.originSlot); s.ok {
						lid := e.newLink(s.id, s.slot)
						ni.Link = &lid
					}
				}
			}
			clone.Inputs[j] = ni
		}
		if !e.emit(clone) {
			return nil
		}
	}

	// Resolve each output boundary slot to its interior source.
	outSrc := make([]origin, len(def.Outputs))
	for j := range def.Outputs {
		outSrc[j] = origin{}
		if il, ok := outputBoundaryLink(intLinks, outputNodeID, j); ok {
			outSrc[j] = resolveInterior(il.originID, il.originSlot)
		}
	}
	return outSrc
}

// scopeTeleportName instance-scopes a Set/GetNode's constant name so teleports
// inside a subgraph stay local to each instance (two instances of the same
// subgraph do not collide). Non-teleport nodes' widgets_values are returned
// unchanged.
func (e *sgExpander) scopeTeleportName(m *uiConvNode, prefix string) json.RawMessage {
	if m.Type != "SetNode" && m.Type != "GetNode" {
		return m.WidgetsValues
	}
	arr, ok := asJSONArray(m.WidgetsValues)
	if !ok || len(arr) == 0 {
		return m.WidgetsValues
	}
	var name string
	if json.Unmarshal(arr[0], &name) != nil || name == "" {
		return m.WidgetsValues
	}
	scoped, _ := json.Marshal(prefix + ":" + name)
	out := append([]json.RawMessage{scoped}, arr[1:]...)
	b, err := json.Marshal(out)
	if err != nil {
		return m.WidgetsValues
	}
	return b
}

// registerRedirect records, for a removed instance id, the interior source of
// each of its output boundary slots.
func (e *sgExpander) registerRedirect(instID string, outSrc []origin) {
	m := make(map[int]origin, len(outSrc))
	for slot, src := range outSrc {
		m[slot] = src
	}
	e.redirect[instID] = m
}

// newLink synthesizes a fresh link (a decreasing negative id, never colliding
// with real positive link ids) from the given source and returns its id.
func (e *sgExpander) newLink(originID string, originSlot int) int64 {
	id := e.nextLink
	e.nextLink--
	e.linkByID[id] = uiLink{ID: id, OriginID: originID, OriginSlot: originSlot}
	return id
}

func (e *sgExpander) warnf(format string, args ...any) {
	e.warnings = append(e.warnings, fmt.Sprintf(format, args...))
}

// outputBoundaryLink finds the interior link feeding output boundary slot j (the
// link whose target is the subgraph's output node at target_slot j).
func outputBoundaryLink(intLinks map[int64]sgInternalLink, outputNodeID string, j int) (sgInternalLink, bool) {
	for _, il := range intLinks {
		if il.targetID == outputNodeID && il.targetSlot == j {
			return il, true
		}
	}
	return sgInternalLink{}, false
}

// parseSubgraphLinks parses a subgraph's interior links (array or object form)
// into a map keyed by link id.
func parseSubgraphLinks(raw []json.RawMessage) map[int64]sgInternalLink {
	out := make(map[int64]sgInternalLink, len(raw))
	for _, r := range raw {
		if il, ok := parseSubgraphLink(r); ok {
			out[il.id] = il
		}
	}
	return out
}

// parseSubgraphLink parses one interior link entry in either the array form
// [id, origin_id, origin_slot, target_id, target_slot, type] or the object form
// {"id":…,"origin_id":…,"origin_slot":…,"target_id":…,"target_slot":…}.
func parseSubgraphLink(raw json.RawMessage) (sgInternalLink, bool) {
	if isJSONArray(raw) {
		var arr []json.RawMessage
		if json.Unmarshal(raw, &arr) != nil || len(arr) < 5 {
			return sgInternalLink{}, false
		}
		var il sgInternalLink
		if json.Unmarshal(arr[0], &il.id) != nil {
			return sgInternalLink{}, false
		}
		il.originID = idToString(arr[1])
		_ = json.Unmarshal(arr[2], &il.originSlot)
		il.targetID = idToString(arr[3])
		_ = json.Unmarshal(arr[4], &il.targetSlot)
		return il, true
	}
	var obj struct {
		ID         int64           `json:"id"`
		OriginID   json.RawMessage `json:"origin_id"`
		OriginSlot int             `json:"origin_slot"`
		TargetID   json.RawMessage `json:"target_id"`
		TargetSlot int             `json:"target_slot"`
	}
	if json.Unmarshal(raw, &obj) != nil || len(obj.OriginID) == 0 {
		return sgInternalLink{}, false
	}
	return sgInternalLink{
		id:         obj.ID,
		originID:   idToString(obj.OriginID),
		originSlot: obj.OriginSlot,
		targetID:   idToString(obj.TargetID),
		targetSlot: obj.TargetSlot,
	}, true
}
