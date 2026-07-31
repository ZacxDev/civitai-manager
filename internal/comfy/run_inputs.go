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

// RunInput is one detected, editable per-run input. It is pre-filled with the
// graph's current value (Current) and applied as an ephemeral override keyed by
// (NodeID, WidgetIndex) — see UIWidgetKey / ApplyUIWidgetOverrides, which rewrite the
// UI graph BEFORE conversion so the converter maps the edited widgets_values slot to
// the right api input name.
//
// NodeID/WidgetIndex point at the node that actually HOLDS the value. For a plain
// widget that is the curated node itself; for a widget that has been converted to an
// input and wired from upstream (litegraph "widget → input"), it is the resolved
// SOURCE node further up the chain (see resolveUpstreamWidget) — the curated node's
// own widgets_values slot is ignored by ComfyUI in that case, so editing it would
// silently no-op. ClassType/InputName always name the CONSUMER's semantics (what the
// value MEANS: "the KSampler's steps"), which is what the label and the enum Choices
// are derived from.
type RunInput struct {
	NodeID    string
	ClassType string
	InputName string
	Label     string
	Kind      RunInputKind
	Current   string
	// WidgetIndex is the slot in NodeID's widgets_values array holding Current.
	WidgetIndex int
	// Resolved is true when the value was found on an UPSTREAM source node rather
	// than on the curated consumer node itself.
	Resolved bool
	// SourceClassType is the class of the node that holds the value (== ClassType
	// when !Resolved). Informational — surfaced so the UI can explain the indirection.
	SourceClassType string
	// SourceVia lists each PASS-THROUGH hop the upstream walk took, as
	// "#<node id> <class>.<input>". It is surfaced verbatim in the UI: which upstream
	// input was followed is a judgement chooseSourceWidget makes from graph structure
	// alone (no /object_info at render time), so showing the path is what makes a
	// wrong pick visible to the user instead of silently editing the wrong thing.
	SourceVia []string
	// Consumers is how many curated inputs this ONE widget drives. Two consumers wired
	// to the same upstream widget are the same editable value, so DetectRunInputs emits
	// a single RunInput for them (see the dedupe in DetectRunInputs) and records the
	// count here — rendering one field per consumer would show N controls that are
	// secretly one, and collapse to last-wins on submit.
	Consumers int
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

// maxRunInputHops bounds how far a converted-widget chain is followed upstream
// (consumer → … → the node holding the value). A malformed or hostile graph can wire
// a very long — or cyclic — chain; the hop cap plus the per-walk visited set make the
// walk terminate in every case.
const maxRunInputHops = 8

// DetectRunInputs scans a UI-format ("Save") graph and returns the curated, editable
// run inputs (prompts, KSampler settings, empty-latent dimensions) for its TOP-LEVEL
// nodes AND for the interiors of the subgraph definitions those nodes instantiate —
// see subgraphRunTargets for which interiors qualify and why the rest are refused.
//
// An interior input is addressed as "<instance id>:<interior id>" (subgraphKeySep),
// the same id flattening gives the node in the submitted graph; ApplyUIWidgetOverrides
// resolves that form back into definitions.subgraphs[].nodes[] so the edit lands
// BEFORE conversion clones the interior.
//
// An upstream walk still STOPS at a subgraph INSTANCE node: conversion deletes that
// node, and a value flowing OUT of a subgraph would have to be chased across the
// namespace boundary. The interior is reached by scanning it directly (below), not by
// walking through the instance.
//
// A curated input whose slot is link-connected is handled by its slot KIND:
//
//   - a TRUE link input (MODEL/LATENT/… — no `widget` marker) carries no widget value
//     and is skipped;
//   - a CONVERTED widget (litegraph "widget → input": a `widget` object AND a link) is
//     followed UPSTREAM to the node that actually holds the value, which is then
//     surfaced under the CONSUMER's semantics (the positive CLIPTextEncode's `text`
//     chain shows as a prompt textarea even though the text lives on an upstream
//     wildcard/regex node). Real-world graphs route almost every parameter this way,
//     so without this the panel surfaces nearly nothing.
//
// Bypassed/muted nodes (mode 2/4) are never surfaced — neither as a consumer nor as a
// resolved source. Pre-fill values are read from the holding node's widgets_values.
// info may be nil; when present it supplies the enum Choices for
// sampler_name/scheduler. An api-format graph (no widgets_values) yields no inputs.
func DetectRunInputs(graph json.RawMessage, info ObjectInfo) []RunInput {
	var g uiConvGraph
	if err := json.Unmarshal(graph, &g); err != nil {
		return nil
	}
	idx := newRunGraphIndex(&g)
	var out []RunInput
	// DEDUPE by the override key (node id + widgets_values index). Several curated
	// consumers can resolve to the SAME upstream widget — one `easy int` feeding a
	// base and a refiner KSampler's steps, or one string node feeding an SDXL
	// encoder's text_g and text_l. They are one editable value; emitting one RunInput
	// per consumer would render N fields that all post the same key, and the override
	// map would silently keep only the last one (dropping the field the user edited).
	at := make(map[UIWidgetKey]int, len(g.Nodes))
	add := func(ri RunInput) {
		key := UIWidgetKey{NodeID: ri.NodeID, Widget: ri.WidgetIndex}
		if pos, dup := at[key]; dup {
			out[pos].Consumers++
			return // first consumer in nodes[] order supplies the label — deterministic
		}
		ri.Consumers = 1
		at[key] = len(out)
		out = append(out, ri)
	}

	// Subgraph interiors are surfaced in the position of the INSTANCE node, so the
	// panel's field order still follows nodes[] order.
	byInstance := make(map[string]*subgraphDef)
	for _, t := range subgraphRunTargets(&g) {
		byInstance[t.instanceID] = t.def
	}

	for i := range g.Nodes {
		n := &g.Nodes[i]
		if isInactiveMode(n.Mode) {
			continue // bypassed/muted consumer — it will not run, so it is not editable
		}
		if layout, ok := runInputLayouts[n.Type]; ok {
			for _, ri := range detectNodeRunInputs(idx, n, layout, info) {
				add(ri)
			}
			continue
		}
		instID := idToString(n.ID)
		def, ok := byInstance[instID]
		if !ok || def.ID != n.Type {
			continue // not a subgraph instance we are willing to reach into
		}
		sub := newSubgraphRunIndex(def, idx.subgraphTypes)
		for j := range def.Nodes {
			m := &def.Nodes[j]
			layout, ok := runInputLayouts[m.Type]
			if !ok {
				// NOT recursed into. A NESTED subgraph instance's type is a definition
				// uuid, which is never a curated class name, so the scan stops here by
				// construction — exactly one level deep, no recursion, and therefore no
				// depth bound or cycle guard to get wrong. Parameters two levels down stay
				// uneditable; that is the same honest limit the whole interior used to have.
				continue
			}
			if isInactiveMode(m.Mode) {
				continue // bypassed/muted interior node
			}
			for _, ri := range detectNodeRunInputs(sub, m, layout, info) {
				add(scopeToInstance(ri, instID, def.Name))
			}
		}
	}
	return out
}

// subgraphKeySep separates the SUBGRAPH INSTANCE id from the INTERIOR node id in a
// RunInput.NodeID / UIWidgetKey.NodeID that addresses a node living inside a subgraph
// definition — "93:10" is interior node 10 of the subgraph instantiated by top-level
// node 93.
//
// Why this spelling:
//
//   - It is EXACTLY the id the converter's flattener already emits for that node
//     (sgExpander.expand builds `prefix + ":" + interiorID`), so the address the run
//     panel shows is the node id that appears in the submitted api graph.
//   - Top-level node ids are plain integers, so a bare id can never contain the
//     separator. A key WITHOUT it is a top-level key and resolves exactly as it always
//     has — which is what makes every run preset and generations.params row written
//     before this existed keep working untouched, with no migration.
//   - It is a plain string, so it round-trips through the stored params JSON (where
//     node_id is a string), the wp_node form field, and PresetEntry.NodeID with no
//     schema change anywhere.
const subgraphKeySep = ":"

// subgraphRunTarget is one subgraph instance whose INTERIOR may be surfaced as
// editable run inputs.
type subgraphRunTarget struct {
	instanceID string
	def        *subgraphDef
}

// subgraphRunTargets returns, in top-level nodes[] order, the subgraph instances whose
// interior may be exposed as editable run inputs AND written to by
// ApplyUIWidgetOverrides. Both sides call THIS function, so the exposure rule and the
// write rule cannot drift apart.
//
// Two refusals, both deliberate:
//
//  1. A definition instantiated MORE THAN ONCE anywhere — top level or inside another
//     definition — is skipped entirely. There is no per-instance widgets_values: an
//     override is written into the DEFINITION and flattening then clones the edited
//     interior into EVERY instance, so a "seed" edit would move N independent
//     pipelines together and a batch could never give them different values. Exposing
//     nothing is the honest answer; a control that silently drives more than it names
//     is the failure this refuses (same posture as the install-and-run substitution
//     offer). No workflow in the dogfood library instantiates a definition twice, so
//     live data cannot catch a regression here — TestSubgraphMultiInstanceNotExposed
//     is the only guard.
//
//  2. A BYPASSED/MUTED instance is skipped, because its interior state is AMBIGUOUS —
//     NOT because the interior does not execute.
//
//     🔴 Do not restate this as "the pipeline is not running": measured, it IS.
//     `flattenSubgraphs` expands every instance with no reference to the instance's
//     own `mode` (convert_subgraph.go's loop over g.Nodes), and it runs BEFORE
//     ConvertUIToAPI drops inactive nodes — and each clone carries the INTERIOR
//     node's mode, not the instance's. Verified on the wf557 fixture: with top-level
//     node 93 set to mode 4, flattening still emits all SEVEN interior clones at
//     mode=0. So bypassing a subgraph instance does not currently bypass it in the
//     submitted graph at all.
//
//     That is a PRE-EXISTING converter bug, out of scope here (it changes what runs,
//     not what is editable) and it is not a regression from this code — nothing was
//     exposed from a subgraph interior before either. Whoever fixes the converter's
//     flatten/mode ordering should revisit this refusal: once a bypassed instance
//     really is dropped, "not running" becomes the correct reason and this stays
//     right for a better reason. Until then the honest justification is that the user
//     has marked the instance off in the UI while its interior still executes, so
//     surfacing its widgets as live, batch-randomised controls would be asserting a
//     coherence the graph does not have.
func subgraphRunTargets(g *uiConvGraph) []subgraphRunTarget {
	if len(g.Definitions.Subgraphs) == 0 {
		return nil
	}
	defs := make(map[string]*subgraphDef, len(g.Definitions.Subgraphs))
	for i := range g.Definitions.Subgraphs {
		if d := &g.Definitions.Subgraphs[i]; d.ID != "" {
			defs[d.ID] = d
		}
	}
	// Count instantiations ANYWHERE: a definition used once at the top level and once
	// inside another definition is still cloned twice by flattening.
	count := make(map[string]int, len(defs))
	for i := range g.Nodes {
		if _, ok := defs[g.Nodes[i].Type]; ok {
			count[g.Nodes[i].Type]++
		}
	}
	for _, d := range defs {
		for j := range d.Nodes {
			if _, ok := defs[d.Nodes[j].Type]; ok {
				count[d.Nodes[j].Type]++
			}
		}
	}

	var out []subgraphRunTarget
	for i := range g.Nodes {
		n := &g.Nodes[i]
		def, ok := defs[n.Type]
		if !ok || isInactiveMode(n.Mode) || count[n.Type] != 1 {
			continue
		}
		instID := idToString(n.ID)
		if instID == "" || strings.Contains(instID, subgraphKeySep) {
			continue // an id already carrying the separator would make the key ambiguous
		}
		out = append(out, subgraphRunTarget{instanceID: instID, def: def})
	}
	return out
}

// scopeToInstance rewrites an interior RunInput's addresses into the instance
// namespace ("<instance id>:<interior id>"). SourceVia is rewritten too, so the
// resolution path the UI prints names ids that exist in the submitted graph rather
// than bare interior ids that collide with unrelated top-level nodes.
//
// It also DISAMBIGUATES THE LABEL, which is a correctness-of-intent requirement, not
// decoration. These graphs routinely hold two KSamplerAdvanced in one subgraph (a
// high-noise/low-noise WAN pair), so without this the panel renders "CFG, Sampler,
// Scheduler" twice with nothing to tell the stages apart and the user can silently
// edit the wrong pass. Measured across the 70-workflow dogfood library: 60 label
// collisions involve an interior input without the suffix, 0 with it.
//
// The suffix is the DEFINITION NAME plus the INTERIOR id ("· Sample #12"), not the
// full scoped id ("· Sample #93:12"): both scored identically (0 collisions) on the
// real library, so the shorter, more readable one wins. The interior id is the half
// that actually varies between the samplers — the instance id is constant for every
// field of one subgraph. An unnamed definition degrades to "· #12". Top-level labels
// are left BYTE-IDENTICAL, so nothing that exists today moves.
func scopeToInstance(ri RunInput, instID, defName string) RunInput {
	ri.Label = subgraphInputLabel(ri.Label, defName, ri.NodeID)
	ri.NodeID = instID + subgraphKeySep + ri.NodeID
	for i, v := range ri.SourceVia {
		if strings.HasPrefix(v, "#") {
			ri.SourceVia[i] = "#" + instID + subgraphKeySep + v[1:]
		}
	}
	return ri
}

// subgraphInputLabel appends the subgraph origin to an interior input's label:
// "CFG · Sample #12", or "CFG · #12" when the definition carries no name.
//
// defName is AUTHOR TEXT and therefore untrusted — it is escaped at render like every
// other label (gomponents' g.Text), the same treatment RunInput.Label already gets for
// node titles. It is whitespace-trimmed here so a name of spaces degrades to the
// nameless form instead of rendering "CFG ·  #12".
func subgraphInputLabel(label, defName, interiorID string) string {
	if name := strings.TrimSpace(defName); name != "" {
		return label + " · " + name + " #" + interiorID
	}
	return label + " · #" + interiorID
}

// newSubgraphRunIndex builds the by-id / by-link index for ONE subgraph definition's
// interior, in that definition's own id namespace. Two things differ from the
// top-level index:
//
//   - the boundary I/O nodes are EXCLUDED, exactly as sgExpander.expand excludes them:
//     they are virtual, get no clone, and an override aimed at one would vanish. An
//     upstream walk that reaches the boundary therefore dangles and surfaces nothing —
//     the right answer, since such a value comes from OUTSIDE the subgraph and chasing
//     it across the namespace boundary is not attempted here.
//   - subgraphTypes carries every definition id (shared with the top-level index), so
//     the walk still stops at a nested subgraph instance.
func newSubgraphRunIndex(def *subgraphDef, subgraphTypes map[string]bool) *runGraphIndex {
	idx := &runGraphIndex{
		byID:          make(map[string]*uiConvNode, len(def.Nodes)),
		linkByID:      make(map[int64]uiLink, len(def.Links)),
		subgraphTypes: subgraphTypes,
	}
	inID, outID := idToString(def.InputNode.ID), idToString(def.OutputNode.ID)
	for i := range def.Nodes {
		id := idToString(def.Nodes[i].ID)
		if id == "" || id == inID || id == outID {
			continue
		}
		idx.byID[id] = &def.Nodes[i]
	}
	for _, raw := range def.Links {
		if il, ok := parseSubgraphLink(raw); ok {
			idx.linkByID[il.id] = uiLink{ID: il.id, OriginID: il.originID, OriginSlot: il.originSlot}
		}
	}
	return idx
}

// runGraphIndex is the by-id node / by-id link lookup the upstream walk needs, built
// once per DetectRunInputs call.
type runGraphIndex struct {
	byID     map[string]*uiConvNode
	linkByID map[int64]uiLink
	// subgraphTypes are the definitions.subgraphs[] ids. A top-level node whose TYPE
	// is one of these is a subgraph INSTANCE: conversion deletes it and replaces it
	// with prefixed clones of the subgraph's interior nodes, so an override written to
	// the instance's own widgets_values would silently vanish. Resolution therefore
	// stops at such a node (see resolveUpstreamWidget). The interior itself IS reached
	// — by scanning definitions.subgraphs[].nodes[] directly (see subgraphRunTargets),
	// which writes to the definition, the only place an edit survives flattening.
	subgraphTypes map[string]bool
}

func newRunGraphIndex(g *uiConvGraph) *runGraphIndex {
	idx := &runGraphIndex{
		byID:          make(map[string]*uiConvNode, len(g.Nodes)),
		linkByID:      make(map[int64]uiLink, len(g.Links)),
		subgraphTypes: make(map[string]bool, len(g.Definitions.Subgraphs)),
	}
	for _, sg := range g.Definitions.Subgraphs {
		if sg.ID != "" {
			idx.subgraphTypes[sg.ID] = true
		}
	}
	for i := range g.Nodes {
		id := idToString(g.Nodes[i].ID)
		if id == "" {
			continue
		}
		idx.byID[id] = &g.Nodes[i]
	}
	for _, raw := range g.Links {
		if l, ok := parseLink(raw); ok {
			idx.linkByID[l.ID] = l
		}
	}
	return idx
}

// input finds a node's input slot by name (nil when absent).
func (idx *runGraphIndex) input(n *uiConvNode, name string) *uiConvInput {
	for i := range n.Inputs {
		if n.Inputs[i].Name == name {
			return &n.Inputs[i]
		}
	}
	return nil
}

// isInactiveMode reports whether a UI node's mode marks it muted or bypassed — such a
// node does not execute, so its widgets must never be surfaced as editable.
func isInactiveMode(mode int) bool { return mode == modeMuted || mode == modeBypass }

// detectNodeRunInputs walks ONE node's widgets_values by its curated layout,
// emitting the exposed inputs pre-filled with their current value. It advances the
// cursor for every widget slot (exposed or not, including the seed
// control_after_generate slot) so the positions of later widgets are correct.
func detectNodeRunInputs(idx *runGraphIndex, n *uiConvNode, layout []runWidget, info ObjectInfo) []RunInput {
	var wv []json.RawMessage
	if json.Unmarshal(n.WidgetsValues, &wv) != nil {
		return nil // object-form or absent widgets_values — curated nodes use array form
	}
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
		if !wdg.expose {
			continue
		}
		ri := RunInput{ClassType: n.Type, InputName: wdg.name, Kind: wdg.kind}
		slot := idx.input(n, wdg.name)
		switch {
		case slot == nil || slot.Link == nil:
			// Plain (unconverted, unwired) widget: the value lives right here.
			ri.NodeID, ri.WidgetIndex, ri.SourceClassType = nodeID, valIdx, n.Type
			if valIdx < len(wv) {
				ri.Current = scalarWidgetString(wv[valIdx])
			}
			ri.Label = runInputLabel(wdg, title, false)
		case !hasWidgetMarker(slot.Widget):
			continue // a TRUE link input — there is no widget value to edit
		default:
			src, ok := idx.resolveUpstreamWidget(n, wdg.name, wdg.kind)
			if !ok {
				continue // unresolvable chain (cycle / hop cap / bypassed / no candidate)
			}
			ri.NodeID, ri.WidgetIndex = src.nodeID, src.widgetIndex
			ri.Current, ri.SourceClassType, ri.Resolved = src.current, src.classType, true
			ri.SourceVia = src.via
			// The SOURCE node's title wins: in real graphs it is the human label of the
			// value ("POSITIVE"/"NEGATIVE"/"Steps"), while the consumer's title names the
			// consumer. Fall back to the consumer's title when the source has none.
			ri.Label = runInputLabel(wdg, firstNonEmptyString(src.title, title), true)
		}
		if wdg.kind == RunInputSelect {
			ri.Choices = comboInputChoices(info, n.Type, wdg.name)
		}
		out = append(out, ri)
	}
	return out
}

// resolvedWidget is where an upstream walk landed: the node that HOLDS the value and
// the widgets_values slot inside it.
type resolvedWidget struct {
	nodeID      string
	classType   string
	title       string
	widgetIndex int
	current     string
	via         []string // pass-through hops, as "#<id> <class>.<input>"
}

// resolveUpstreamWidget follows a converted widget input upstream to the node that
// actually holds its value. At each hop it takes the input's link to the origin node
// and asks chooseSourceWidget which of that node's widgets carries the value; when
// that widget is ITSELF a linked converted widget the walk recurses (this is how an
// ImpactWildcardProcessor → RegexReplace → CLIPTextEncode.text chain resolves back to
// the wildcard node's prompt).
//
// It gives up — surfacing nothing rather than something wrong — when the chain is
// cyclic, exceeds maxRunInputHops, dangles (unknown link id / missing node), reaches a
// bypassed/muted node, or offers no type-compatible widget.
func (idx *runGraphIndex) resolveUpstreamWidget(start *uiConvNode, inputName string, kind RunInputKind) (resolvedWidget, bool) {
	cur, name := start, inputName
	visited := map[string]bool{idToString(start.ID): true}
	var via []string
	for hop := 0; hop < maxRunInputHops; hop++ {
		slot := idx.input(cur, name)
		if slot == nil || slot.Link == nil {
			return resolvedWidget{}, false
		}
		l, ok := idx.linkByID[*slot.Link]
		if !ok {
			return resolvedWidget{}, false // dangling link id
		}
		src, ok := idx.byID[l.OriginID]
		if !ok || src == nil {
			return resolvedWidget{}, false // missing upstream node
		}
		if isInactiveMode(src.Mode) {
			return resolvedWidget{}, false // bypassed/muted source — never editable
		}
		if idx.subgraphTypes[src.Type] {
			// A subgraph INSTANCE: flattening deletes this node and emits prefixed
			// clones of the subgraph interior, so an override written to the instance's
			// widgets_values would never reach the submitted graph. Surface nothing
			// rather than a control that silently does not apply. (The interior is
			// scanned directly instead — see subgraphRunTargets. Chasing a value that
			// flows OUT of a subgraph would additionally have to cross the instance's
			// output boundary into another id namespace; that is not attempted.)
			return resolvedWidget{}, false
		}
		if visited[l.OriginID] {
			return resolvedWidget{}, false // cycle
		}
		visited[l.OriginID] = true

		next, widgetIdx, val, ok := chooseSourceWidget(src, kind, name)
		if !ok {
			return resolvedWidget{}, false
		}
		if next != "" {
			via = append(via, "#"+l.OriginID+" "+src.Type+"."+next)
			cur, name = src, next
			continue // the value passes THROUGH this node — keep walking
		}
		return resolvedWidget{
			nodeID:      l.OriginID,
			classType:   src.Type,
			title:       strings.TrimSpace(src.Title),
			widgetIndex: widgetIdx,
			current:     val,
			via:         via,
		}, true
	}
	return resolvedWidget{}, false // hop cap
}

// chooseSourceWidget picks which widget on an upstream node carries a value of the
// wanted kind. It is GENERIC over node types — nothing here keys off a class name; the
// choice comes from the graph's own structure. Two deterministic rules, in order:
//
//  1. If the node has a type-compatible CONVERTED widget that is itself LINKED, the
//     value merely passes through this node (a RegexReplace's `string`, a passthrough
//     wrapper's input): return its name so the caller recurses upstream. Ties are
//     broken by NAME first — a slot named like the consumer's input (`text` → `text`)
//     is the same semantic value — then by inputs[] order.
//  2. Otherwise the value lives in this node's own widgets_values: return the FIRST
//     slot whose JSON type matches the wanted kind (string for text/select, number for
//     int/float/seed). Ties are broken by widget order — the first slot wins, which is
//     the primary/leading widget in every ComfyUI node layout (an ImpactWildcardProcessor's
//     `wildcard_text` ahead of its `populated_text`, an `easy int`'s `value`).
//
// A control_after_generate slot (a "fixed"/"randomize"/… string right after a numeric
// slot) is skipped: it is a serialization artifact, not a value.
//
// KNOWN LIMIT: rule 2 is positional. Without /object_info (which the render path does
// not fetch) a widgets_values slot carries no name, so on a node with several
// same-typed widgets the leading one is a convention, not a proof — e.g. a node
// serializing a pattern widget ahead of its subject widget would surface the pattern.
// The resolution path is therefore reported to the user (RunInput.SourceVia + the
// widget index) so a wrong pick is visible rather than silent.
//
// Returns (recurseInputName, widgetIndex, currentValue, ok).
func chooseSourceWidget(n *uiConvNode, kind RunInputKind, preferName string) (string, int, string, bool) {
	best := ""
	for _, in := range n.Inputs {
		if in.Link == nil || !hasWidgetMarker(in.Widget) {
			continue
		}
		if !slotTypeMatchesKind(in.Type, kind) {
			continue
		}
		if in.Name == preferName {
			return in.Name, 0, "", true // same-named slot: the same semantic value
		}
		if best == "" {
			best = in.Name
		}
	}
	if best != "" {
		return best, 0, "", true
	}
	wv, ok := asJSONArray(n.WidgetsValues)
	if !ok {
		return "", 0, "", false // object-form / absent widgets_values
	}
	for i, raw := range wv {
		if !widgetValueMatchesKind(raw, kind) {
			continue
		}
		if kindWantsString(kind) && isSeedControlSlot(raw) && i > 0 && isJSONNumberValue(wv[i-1]) {
			continue // control_after_generate artifact, not an editable value
		}
		return "", i, scalarWidgetString(raw), true
	}
	return "", 0, "", false
}

// kindWantsString reports whether a run-input kind is carried by a STRING value.
func kindWantsString(kind RunInputKind) bool {
	return kind == RunInputText || kind == RunInputSelect
}

// slotTypeMatchesKind reports whether an input slot's DECLARED litegraph type is
// compatible with the wanted kind. The declared type is usually a string but is an
// ARRAY for a COMBO slot (the option list) — an array/object/absent type yields false,
// so an untyped slot is never followed blindly.
func slotTypeMatchesKind(raw json.RawMessage, kind RunInputKind) bool {
	var t string
	if json.Unmarshal(raw, &t) != nil {
		return false
	}
	t = strings.ToUpper(strings.TrimSpace(t))
	if kindWantsString(kind) {
		return t == "STRING"
	}
	return t == "INT" || t == "FLOAT" || t == "NUMBER"
}

// widgetValueMatchesKind reports whether a widgets_values slot's JSON type can carry
// the wanted kind (string ⇄ text/select, number ⇄ int/float/seed). Booleans, nulls,
// arrays and objects never match.
func widgetValueMatchesKind(raw json.RawMessage, kind RunInputKind) bool {
	t := bytes.TrimSpace(raw)
	if len(t) == 0 {
		return false
	}
	if kindWantsString(kind) {
		return t[0] == '"'
	}
	return isJSONNumberValue(raw)
}

// isJSONNumberValue reports whether a raw JSON value is a number literal.
func isJSONNumberValue(raw json.RawMessage) bool {
	t := bytes.TrimSpace(raw)
	if len(t) == 0 {
		return false
	}
	return t[0] == '-' || (t[0] >= '0' && t[0] <= '9')
}

func firstNonEmptyString(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// runInputLabel builds an input's display label. For a prompt (text) input the node
// title — when the graph carries one — is appended so a positive/negative pair is
// distinguishable ("Prompt (POSITIVE)"). For a numeric/enum input the title is
// appended ONLY when the value was resolved upstream (the holding node's title is then
// the user's own name for it) and it does not merely repeat the label.
func runInputLabel(wdg runWidget, title string, resolved bool) string {
	if title == "" {
		return wdg.label
	}
	if wdg.kind == RunInputText {
		return wdg.label + " (" + title + ")"
	}
	if resolved && !strings.EqualFold(title, wdg.label) {
		return wdg.label + " (" + title + ")"
	}
	return wdg.label
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
