package comfy

import (
	"encoding/json"
	"path/filepath"
	"sort"
	"strings"
)

// PreflightReport is the result of validating an api-format graph against a live
// ComfyUI's /object_info AND the local library, BEFORE submitting. This is the
// library manager's differentiator: ComfyUI does not auto-download models and
// rejects a graph whose referenced files are not on disk, so we tell the user what
// is missing first.
type PreflightReport struct {
	// MissingNodes are class_types referenced by the graph that are absent from
	// /object_info (a custom node the local ComfyUI does not have installed).
	MissingNodes []string
	// MissingModels are model filenames referenced by loader inputs that are neither
	// present in the loader's /object_info choices list NOR satisfied by localHave
	// (the local library). These would fail ComfyUI's own validation.
	MissingModels []string
	// OK is true when there are no missing nodes and no missing models.
	OK bool
}

// Preflight validates an api-format graph against the node schema (info) and the
// local library (localHave reports whether a model FILENAME, by basename, exists
// locally). It reports referenced node types absent from info and referenced model
// filenames satisfied by neither the node's object_info choices nor localHave.
//
// localHave may be nil (treated as "have nothing locally"): then a model is
// considered present only if it appears in the node's object_info choices list
// (i.e. ComfyUI itself already sees the file).
func Preflight(apiGraph json.RawMessage, info ObjectInfo, localHave func(filename string) bool) PreflightReport {
	if localHave == nil {
		localHave = func(string) bool { return false }
	}

	var nodes map[string]apiNode
	if err := json.Unmarshal(apiGraph, &nodes); err != nil {
		// A graph we cannot parse cannot be pre-flighted; report not-OK with no
		// specifics rather than panicking on untrusted input.
		return PreflightReport{OK: false}
	}

	// 1. Missing node types.
	missingNodesSet := map[string]bool{}
	for _, n := range nodes {
		ct := strings.TrimSpace(n.ClassType)
		if ct == "" {
			continue
		}
		if _, ok := info[ct]; !ok {
			missingNodesSet[ct] = true
		}
	}

	// 2. Missing models. Reuse ExtractResources to find referenced model filenames,
	// then for each, check the loader node's object_info choices and the local
	// library.
	refs, _ := ExtractResources(apiGraph)
	missingModelsSet := map[string]bool{}
	for _, ref := range refs {
		if modelSatisfied(ref, nodes, info, localHave) {
			continue
		}
		missingModelsSet[ref] = true
	}

	report := PreflightReport{
		MissingNodes:  sortedKeys(missingNodesSet),
		MissingModels: sortedKeys(missingModelsSet),
	}
	report.OK = len(report.MissingNodes) == 0 && len(report.MissingModels) == 0
	return report
}

// modelSatisfied reports whether a referenced model filename is available: either
// it appears in some loader node's object_info choices list (ComfyUI sees the
// file), or localHave reports it present in the local library by basename.
func modelSatisfied(ref string, nodes map[string]apiNode, info ObjectInfo, localHave func(string) bool) bool {
	if localHave(filepath.Base(ref)) {
		return true
	}
	// Check every kept node's schema choices for this filename. A loader's choices
	// list enumerates the files ComfyUI already has installed for that input.
	for _, n := range nodes {
		sch, ok := info[n.ClassType]
		if !ok {
			continue
		}
		if choicesContain(sch, ref) {
			return true
		}
	}
	return false
}

// choicesContain reports whether any input's combo choices in a node schema include
// filename (exact match, or by basename to tolerate a choices entry that carries a
// subdirectory prefix like "flux/foo.safetensors").
func choicesContain(sch NodeSchema, filename string) bool {
	base := filepath.Base(filename)
	check := func(specs map[string]InputSpec) bool {
		for _, spec := range specs {
			for _, choice := range spec.Choices {
				if choice == filename || filepath.Base(choice) == base {
					return true
				}
			}
		}
		return false
	}
	return check(sch.Input.Required) || check(sch.Input.Optional)
}

func sortedKeys(m map[string]bool) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
