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
	if err := json.Unmarshal(raw, &nodes); err == nil {
		for _, n := range nodes {
			if strings.TrimSpace(n.ClassType) != "" {
				return FormatAPI, nil
			}
		}
	}
	return "", ErrUnknownFormat
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
