package comfy

import (
	"encoding/json"
	"errors"
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
// through DetectFormat — handleWorkflowImportPNG stores a prompt chunk as format=api
// verbatim — could therefore present `{"a":{}}` or a `{"prompt": <graph>}` wrapper,
// score Nodes=1, and be called a graph by the only signal that separates "not a
// graph" from "a clean graph". See comfy.PreflightReport.Nodes.
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
// in a model extension. Results are de-duplicated with first-seen order
// preserved.
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
	for _, n := range nodes {
		if !isLoaderClass(n.ClassType) {
			continue
		}
		for _, rawVal := range n.Inputs {
			var s string
			if err := json.Unmarshal(rawVal, &s); err != nil {
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
// those ending in a model extension. Both dedup with first-seen order preserved.
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
// ckpt_name input of the first CheckpointLoaderSimple/CheckpointLoader node; for
// ui graphs it is the first model-extension string in the widgets_values of a node
// whose type contains "Checkpoint". ok is false when no checkpoint is found.
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
	for _, n := range nodes {
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
