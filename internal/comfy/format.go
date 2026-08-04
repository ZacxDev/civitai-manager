package comfy

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"
)

// Workflow format identifiers (mirror store.WorkflowFormat*).
const (
	FormatAPI = "api"
	FormatUI  = "ui"
)

// ErrUnknownFormat means the JSON matched neither the api nor the ui shape.
var ErrUnknownFormat = errors.New("unrecognized comfy workflow format")

// modelExtensions are the file suffixes that mark a loader input value as a model
// file (checkpoint / LoRA / VAE / etc.).
var modelExtensions = []string{
	".safetensors", ".ckpt", ".pt", ".pth", ".bin", ".gguf", ".sft",
}

// apiNode is one node of an api-format graph: a class_type and its inputs.
type apiNode struct {
	ClassType string                     `json:"class_type"`
	Inputs    map[string]json.RawMessage `json:"inputs"`
}

// DetectFormat classifies a workflow graph.
//
//   - "api": a flat object mapping node-id → {class_type, inputs}. We require at
//     least one entry that actually carries a non-empty class_type (a bare
//     "{}" or an object of non-node values is not an api graph).
//   - "ui": an object carrying a top-level "nodes" array (the editor save format).
//
// Anything else yields ErrUnknownFormat. The ui check is tried FIRST because a
// ui graph is also a JSON object and could otherwise be mis-probed as api.
func DetectFormat(raw json.RawMessage) (string, error) {
	// UI graphs have a top-level "nodes" array.
	var uiProbe struct {
		Nodes json.RawMessage `json:"nodes"`
	}
	if err := json.Unmarshal(raw, &uiProbe); err == nil && isJSONArray(uiProbe.Nodes) {
		return FormatUI, nil
	}

	// API graphs are a flat map of node-id → {class_type, inputs}.
	var nodes map[string]apiNode
	if err := json.Unmarshal(raw, &nodes); err == nil && usableAPINodes(nodes) > 0 {
		return FormatAPI, nil
	}
	return "", ErrUnknownFormat
}

// usableAPINodes counts the entries of a decoded api graph that actually carry a
// class_type — i.e. the nodes ComfyUI could execute.
//
// 🔴 THIS IS ONE RULE WITH TWO CALLERS ON PURPOSE. DetectFormat's "is this an api
// graph at all" and Preflight's Nodes count are the SAME question, and they used to
// answer it differently: DetectFormat required a non-empty class_type while
// Preflight counted raw map entries. Anything that reached Preflight WITHOUT going
// through DetectFormat could therefore present `{"a":{}}` or a `{"prompt": <graph>}`
// wrapper, score Nodes=1, and be called a graph by the only signal that separates
// "not a graph" from "a clean graph". See comfy.PreflightReport.Nodes.
//
// ⚠ The live example named here used to be handleWorkflowImportPNG, which stored a
// prompt chunk as format=api verbatim. That path now calls DetectFormat, so it is
// history — but the shared rule stays load-bearing: `workflows.format` carries no DB
// constraint, and every row that import wrote before the fix is still out there.
//
// Empty-but-present and whitespace-only both count as absent, matching ComfyUI:
// execution.py's validate_prompt rejects a missing class_type outright, and a blank
// one then fails the NODE_CLASS_MAPPINGS lookup on the next line.
func usableAPINodes(nodes map[string]apiNode) int {
	n := 0
	for _, node := range nodes {
		if strings.TrimSpace(node.ClassType) != "" {
			n++
		}
	}
	return n
}

// ExtractResources scans an api-format graph for referenced model filenames. It
// looks at every node whose class_type is a loader (a known loader class OR any
// class_type containing "Loader") and collects each string input value that ends
// in a model extension. Results are de-duplicated and returned in a DETERMINISTIC
// order: node id per LessNodeKey (client.go) — every NUMERIC id first, ascending by
// value with equal values tie-broken by string ("007" < "07" < "7"), then every
// NON-NUMERIC id lexically — and within a node, ascending input name.
//
// ⚠ That partition is the point, and it is NOT "numeric if both parse, else
// lexical". This comment stated that weaker rule for one round, and it is exactly
// the intransitive comparison the fix replaced: on {"9","10","5abc"} it compares
// "5abc" against "9" lexically and puts 5abc first, where LessNodeKey puts both
// numerics ahead of it. Do not restate the rule here — read LessNodeKey.
//
// 🔴 THE ORDER IS PART OF THE CONTRACT, because this list is PERSISTED and is read
// back as a provenance claim. It reaches `workflows.resources` from the library scan
// (internal/library/workflow_scan.go) and all three import paths, and
// ExtractActiveResources feeds `ResourcesUsed` in internal/web/outputs_capture.go,
// which answers "what did THIS RUN use". A record that changes between runs of the
// same graph is not a record.
//
// ⚠ This comment used to claim "first-seen order preserved". That was FALSE on this
// path and, worse, unachievable in its own terms: an api graph is a JSON OBJECT
// decoded into a Go map, and Go randomises map iteration per process, so there is no
// first-seen order left to preserve. Measured before the fix, on a 5-loader graph:
// FIVE distinct orderings across 200 calls in one process. The ui path
// (extractResourcesUI) really does preserve document order — it ranges a []uiNode
// slice — which is why only this half was affected and why the claim looked true.
//
// Sorting rather than recovering document order is deliberate: recovering it means
// streaming the object with json.Decoder to capture key order, and no consumer wants
// "the order the exporter happened to write" over a stable, explainable one. What
// every consumer needs is that two runs agree.
//
// It is deliberately permissive: unexpected shapes (non-object inputs, non-string
// values, an unparseable graph) yield whatever could be recovered rather than an
// error — the extracted list is an advisory pre-flight aid, not a contract.
func ExtractResources(apiGraph json.RawMessage) ([]string, error) {
	var nodes map[string]apiNode
	if err := json.Unmarshal(apiGraph, &nodes); err != nil {
		return nil, nil
	}
	var (
		out  []string
		seen = map[string]bool{}
	)
	for _, id := range sortedNodeIDs(nodes) {
		n := nodes[id]
		if !isLoaderClass(n.ClassType) {
			continue
		}
		for _, name := range sortedInputNames(n.Inputs) {
			var s string
			if err := json.Unmarshal(n.Inputs[name], &s); err != nil {
				continue // not a string input (e.g. a link array or number)
			}
			if !hasModelExtension(s) {
				continue
			}
			if !seen[s] {
				seen[s] = true
				out = append(out, s)
			}
		}
	}
	return out, nil
}

// sortedNodeIDs orders api-graph node ids ascending, numerically where possible.
//
// 🔴 IT MUST USE LessNodeKey (client.go) — DO NOT hand-roll the comparison here.
// The obvious inline version ("numeric if both parse, else lexical") is NOT a strict
// weak ordering and therefore does not sort at all. Witness {"9","10","5abc"}:
// 9 < 10 numerically, "10" < "5abc" lexically, "5abc" < "9" lexically — a cycle.
// `sort.Slice` on an intransitive comparator returns an ARBITRARY PERMUTATION OF ITS
// INPUT, and the input here comes from a randomised map range — so the naive
// comparator leaves this function exactly as nondeterministic as no sort at all.
// Measured with that version in place: 5 distinct orderings across 500 calls on
// {1,4,9,12,12:3,12:8}.
//
// That is not hypothetical. convert_subgraph.go mints interior node ids as
// "<instance>:<interior>", so any UI workflow with a subgraph containing a loader
// yields plain numeric ids alongside colon ids — convert_test.go pins a converted
// graph keyed {"4","17","100:1"}. LessNodeKey also tie-breaks "07" vs "7" by string,
// which a bare numeric compare leaves to `sort.Slice`'s instability.
//
// This package had already found, fixed and guarded this exact bug in AllImages
// (see TestLessNodeKeyIsAStrictWeakOrdering, which names the same witness). It was
// reintroduced here by a fix written 200 lines away from the solution — the
// "one rule, one place" failure in its purest form.
func sortedNodeIDs(nodes map[string]apiNode) []string {
	ids := make([]string, 0, len(nodes))
	for id := range nodes {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(a, b int) bool { return LessNodeKey(ids[a], ids[b]) })
	return ids
}

// sortedInputNames orders a node's input names lexically. Inputs are a JSON object
// too, so this is the second randomisation source — fixing only the node loop would
// leave a node with two model-valued inputs still nondeterministic.
func sortedInputNames(inputs map[string]json.RawMessage) []string {
	names := make([]string, 0, len(inputs))
	for name := range inputs {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// uiNode is one node of a ui-format (editor "Save") graph: a class type and the
// flat widget-value list the editor persists. widgets_values is heterogeneous
// (strings, numbers, nested arrays), so each entry is kept as a RawMessage and
// only the string ones are inspected.
type uiNode struct {
	Type          string            `json:"type"`
	WidgetsValues []json.RawMessage `json:"widgets_values"`
	// Mode is the litegraph node mode (0 normal, 2 muted, 4 bypassed). It is read
	// ONLY by the active-only extraction path (ExtractActiveResources); the advisory
	// full scan deliberately ignores it — see extractResourcesUI.
	Mode int `json:"mode"`
}

// uiGraph is the ui-format top-level shape we care about: the nodes array.
type uiGraph struct {
	Nodes []uiNode `json:"nodes"`
}

// ExtractResourcesAny extracts referenced model filenames from a graph of either
// format: the api path reuses ExtractResources (loader-node inputs); the ui path
// is a heuristic — it scans EVERY node's widgets_values string entries and keeps
// those ending in a model extension.
//
// Both dedup, but they ORDER DIFFERENTLY and deliberately so: the ui path preserves
// first-seen (document) order because it walks a []uiNode slice, while the api path
// sorts by node id, because its graph is a JSON OBJECT and a map decode leaves no
// document order to preserve. Both are deterministic; do not "unify" them by
// reintroducing a map range on the api side.
// An unrecognized format returns (nil, ErrUnknownFormat).
func ExtractResourcesAny(format string, graph json.RawMessage) ([]string, error) {
	switch format {
	case FormatAPI:
		return ExtractResources(graph)
	case FormatUI:
		return extractResourcesUI(graph, false)
	default:
		return nil, ErrUnknownFormat
	}
}

// ExtractActiveResources is ExtractResourcesAny restricted to the nodes that would
// ACTUALLY RUN: bypassed (mode 4) and muted (mode 2) nodes are skipped.
//
// 🔴 Why this exists as a separate entry point rather than a change to
// ExtractResourcesAny: the two answer DIFFERENT questions and both are wanted.
//
//   - ExtractResourcesAny answers "what does this workflow reference" — the list
//     shown on the workflow page and stored in workflows.resources. A template pack
//     ships N pipelines with all but one bypassed, and a user looking at it wants to
//     know every model the template can need, not just the currently-enabled one.
//   - ExtractActiveResources answers "what did THIS RUN use" — a provenance claim.
//     Including a bypassed pipeline's checkpoint there asserts a model ran that
//     demonstrably did not.
//
// Both share ONE implementation (extractResourcesUI) so the collection rules — which
// widget values count as a model filename, the dedup, the first-seen ordering —
// cannot drift between them.
//
// The API path is identical to ExtractResources: an api graph carries no modes at
// all (conversion has already dropped bypassed nodes), so "active" and "referenced"
// are the same set there by construction.
func ExtractActiveResources(format string, graph json.RawMessage) ([]string, error) {
	switch format {
	case FormatAPI:
		return ExtractResources(graph)
	case FormatUI:
		return extractResourcesUI(graph, true)
	default:
		return nil, ErrUnknownFormat
	}
}

// extractResourcesUI scans nodes' widgets_values for model-filename strings. It is
// deliberately permissive: an unparseable graph or odd widget shapes yield whatever
// could be recovered, never an error (advisory pre-flight aid).
//
// activeOnly skips bypassed/muted nodes. It is the ONLY difference between the two
// exported entry points (ExtractResourcesAny / ExtractActiveResources) — everything
// about WHAT counts as a resource lives here, once.
func extractResourcesUI(graph json.RawMessage, activeOnly bool) ([]string, error) {
	var g uiGraph
	if err := json.Unmarshal(graph, &g); err != nil {
		return nil, nil
	}
	var (
		out  []string
		seen = map[string]bool{}
	)
	for _, n := range g.Nodes {
		if activeOnly && isInactiveMode(n.Mode) {
			continue // bypassed/muted — it will not run, so it referenced nothing
		}
		for _, raw := range n.WidgetsValues {
			var s string
			if err := json.Unmarshal(raw, &s); err != nil {
				continue // not a string widget (number, array, object)
			}
			if !hasModelExtension(s) || seen[s] {
				continue
			}
			seen[s] = true
			out = append(out, s)
		}
	}
	return out, nil
}

// PrimaryCheckpoint returns the primary checkpoint filename a graph loads, used to
// pick the version a scanned workflow auto-links to. For api graphs it is the
// ckpt_name input of the LOWEST-NODE-ID CheckpointLoaderSimple/CheckpointLoader
// node; for ui graphs it is the first model-extension string in the widgets_values
// of a node whose type contains "Checkpoint". ok is false when no checkpoint is
// found.
//
// 🔴 "Lowest node id", not "first", is deliberate and load-bearing on the api path.
// This used to range the node map directly and its doc said "the first" — but a JSON
// object decoded into a Go map has no first, and Go randomises the iteration.
// Measured on a 3-checkpoint graph: 3 distinct answers across 500 calls. That is
// more consequential than the resource-list ordering it sits beside, because
// autoLink (internal/library/workflow_scan.go) prepends this value to its candidate
// list and takes the FIRST hit — so a random answer here randomises the persisted
// model_id/version_id a re-scanned workflow acquires.
func PrimaryCheckpoint(format string, graph json.RawMessage) (string, bool) {
	switch format {
	case FormatAPI:
		return primaryCheckpointAPI(graph)
	case FormatUI:
		return primaryCheckpointUI(graph)
	default:
		return "", false
	}
}

func primaryCheckpointAPI(graph json.RawMessage) (string, bool) {
	var nodes map[string]apiNode
	if err := json.Unmarshal(graph, &nodes); err != nil {
		return "", false
	}
	for _, id := range sortedNodeIDs(nodes) {
		n := nodes[id]
		if n.ClassType != "CheckpointLoaderSimple" && n.ClassType != "CheckpointLoader" {
			continue
		}
		raw, ok := n.Inputs["ckpt_name"]
		if !ok {
			continue
		}
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			continue
		}
		if strings.TrimSpace(s) != "" {
			return s, true
		}
	}
	return "", false
}

func primaryCheckpointUI(graph json.RawMessage) (string, bool) {
	var g uiGraph
	if err := json.Unmarshal(graph, &g); err != nil {
		return "", false
	}
	for _, n := range g.Nodes {
		if !strings.Contains(n.Type, "Checkpoint") {
			continue
		}
		for _, raw := range n.WidgetsValues {
			var s string
			if err := json.Unmarshal(raw, &s); err != nil {
				continue
			}
			if hasModelExtension(s) {
				return s, true
			}
		}
	}
	return "", false
}

// knownLoaders is the explicit loader class set. Any other class_type containing
// "Loader" is also treated as a loader (custom nodes, model-specific loaders).
var knownLoaders = map[string]bool{
	"CheckpointLoaderSimple": true,
	"CheckpointLoader":       true,
	"LoraLoader":             true,
	"LoraLoaderModelOnly":    true,
	"VAELoader":              true,
	"UNETLoader":             true,
	"CLIPLoader":             true,
	"DualCLIPLoader":         true,
	"ControlNetLoader":       true,
	"UpscaleModelLoader":     true,
}

func isLoaderClass(classType string) bool {
	if knownLoaders[classType] {
		return true
	}
	return strings.Contains(classType, "Loader")
}

func hasModelExtension(s string) bool {
	low := strings.ToLower(strings.TrimSpace(s))
	for _, ext := range modelExtensions {
		if strings.HasSuffix(low, ext) {
			return true
		}
	}
	return false
}

// referenceWalkMaxDepth bounds the ReferencesModelFile walk so a pathologically
// nested (or hostile) graph cannot drive unbounded recursion. Real ComfyUI graphs
// nest a handful of levels; 64 is far past anything legitimate.
const referenceWalkMaxDepth = 64

// ReferencesModelFile reports whether a graph references filename as a string value
// ANYWHERE in it. It is a BINDING predicate, not an inventory: the web layer uses it
// to confirm that a (workflow, filename) pair supplied by a request actually belongs
// together before that filename is allowed to drive a network fetch and a filesystem
// write.
//
// It deliberately does NOT reuse ExtractResources/ExtractResourcesAny. Those are
// advisory and narrow — the api path only looks at LOADER classes, so a legitimate
// install target on a non-loader node (e.g. UltralyticsDetectorProvider's model_name)
// would be reported as unreferenced and the user's click refused. This walks every
// string leaf of either format instead, which cannot produce that false negative.
//
// Matching is case-insensitive on the whole reference OR its basename (a reference may
// carry a subfolder prefix like "bbox/face_yolov9c.pt"). An unparseable graph, an empty
// filename, or exceeding the depth bound yields false.
func ReferencesModelFile(format string, graph json.RawMessage, filename string) bool {
	want := strings.ToLower(strings.TrimSpace(filename))
	if want == "" {
		return false
	}
	wantBase := strings.ToLower(pathBase(want))
	var any interface{}
	if err := json.Unmarshal(graph, &any); err != nil {
		return false
	}
	return jsonHasString(any, want, wantBase, 0)
}

// jsonHasString walks decoded JSON for a string leaf matching want (or its basename).
func jsonHasString(v interface{}, want, wantBase string, depth int) bool {
	if depth > referenceWalkMaxDepth {
		return false
	}
	switch t := v.(type) {
	case string:
		low := strings.ToLower(strings.TrimSpace(t))
		return low == want || (wantBase != "" && strings.ToLower(pathBase(low)) == wantBase)
	case []interface{}:
		for _, e := range t {
			if jsonHasString(e, want, wantBase, depth+1) {
				return true
			}
		}
	case map[string]interface{}:
		for _, e := range t {
			if jsonHasString(e, want, wantBase, depth+1) {
				return true
			}
		}
	}
	return false
}

// pathBase is filepath.Base over a forward/back-slash path, without importing
// path/filepath semantics that differ per OS for a graph written on another machine.
func pathBase(s string) string {
	s = strings.ReplaceAll(s, "\\", "/")
	if i := strings.LastIndex(s, "/"); i >= 0 {
		return s[i+1:]
	}
	return s
}

// isJSONArray reports whether raw is a non-null JSON array.
func isJSONArray(raw json.RawMessage) bool {
	for i := 0; i < len(raw); i++ {
		switch raw[i] {
		case ' ', '\t', '\r', '\n':
			continue
		case '[':
			return true
		default:
			return false
		}
	}
	return false
}
