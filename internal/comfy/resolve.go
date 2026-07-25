package comfy

import (
	"encoding/json"
	"path/filepath"
	"sort"
	"strings"
)

// ResourceStatus classifies how well a referenced resource was resolved to an AIR
// URN for a cloud run.
const (
	// ResolveResolved: filename matched a local file with a cached model whose
	// baseModel is a KNOWN ecosystem — a confident URN.
	ResolveResolved = "resolved"
	// ResolveGuessed: matched + cached, but the baseModel is not in the ecosystem
	// map, so the URN uses a best-effort ecosystem key and should be reviewed.
	ResolveGuessed = "guessed"
	// ResolveUnresolved: filename not in the library, ambiguous, or no cache —
	// the user must supply the URN.
	ResolveUnresolved = "unresolved"
	// ResolveCustomNode: a referenced class_type outside the core-node set — the
	// user must supply a nodepack URN (urn:air:comfy:nodepack:...).
	ResolveCustomNode = "custom-node"
)

// ResolvedResource is one referenced resource and its derived AIR URN + status.
// Filename is the referenced model filename (for model resources) or the custom
// node's class_type (for custom-node rows). URN is blank for unresolved/custom.
type ResolvedResource struct {
	Filename string
	URN      string
	Status   string
}

// LocalMatch is the store linkage a basename lookup yields: the civitai model +
// version ids a local file resolved to.
type LocalMatch struct {
	ModelID   int
	VersionID int
}

// ResourceLookup is the store surface ResolveResources needs, abstracted so the
// resolver is unit-testable with a fake (no real store/DB).
type ResourceLookup interface {
	// LocalFileByBasename returns the model/version linkage for a referenced
	// filename, or (nil, nil) when there is no (unambiguous, linked) match.
	LocalFileByBasename(basename string) (*LocalMatch, error)
	// ModelTypeBaseModel returns the model's ModelType and the given version's
	// baseModel from the local model cache; ok is false when the model/version is
	// not cached.
	ModelTypeBaseModel(modelID, versionID int) (modelType, baseModel string, ok bool)
}

// ResolveResources derives the AIR URN list a cloud run needs from an API-format
// graph. For each referenced model filename it walks the resolution chain
// (filename → local file → cached model type + version baseModel → AIR URN); each
// result is flagged resolved/guessed/unresolved. It then appends a custom-node row
// (empty URN) for every referenced class_type outside the core-node set, so the
// user can fill in the nodepack URN.
//
// It is permissive: an unparseable graph yields whatever could be recovered.
func ResolveResources(apiGraph json.RawMessage, lookup ResourceLookup) ([]ResolvedResource, error) {
	var out []ResolvedResource

	// 1. Model-file resources (loader inputs), in first-seen order.
	refs, _ := ExtractResources(apiGraph)
	for _, ref := range refs {
		out = append(out, resolveModelRef(ref, lookup))
	}

	// 2. Custom nodes: class_types outside the core-node set.
	for _, ct := range customNodeClasses(apiGraph) {
		out = append(out, ResolvedResource{Filename: ct, URN: "", Status: ResolveCustomNode})
	}

	return out, nil
}

// resolveModelRef resolves a single referenced model filename to a ResolvedResource.
func resolveModelRef(ref string, lookup ResourceLookup) ResolvedResource {
	res := ResolvedResource{Filename: ref, Status: ResolveUnresolved}
	if lookup == nil {
		return res
	}
	base := filepath.Base(ref)
	match, err := lookup.LocalFileByBasename(base)
	if err != nil || match == nil || match.ModelID == 0 || match.VersionID == 0 {
		return res // not in library / ambiguous / not linked
	}
	modelType, baseModel, ok := lookup.ModelTypeBaseModel(match.ModelID, match.VersionID)
	if !ok {
		return res // no cache to derive type/ecosystem from
	}
	eco, ecoKnown := EcosystemKey(baseModel)
	urnType := URNType(modelType)
	res.URN = BuildCivitaiAIR(eco, urnType, match.ModelID, match.VersionID)
	// Only a fully-mapped URN (known ecosystem AND known type) is "resolved". An
	// unmapped type yields a ":unknown:" segment the orchestrator rejects, so it
	// must degrade to "guessed" for the user to review — a green have-✓ badge on an
	// unsubmittable URN would be a lie.
	if ecoKnown && urnType != "unknown" {
		res.Status = ResolveResolved
	} else {
		res.Status = ResolveGuessed
	}
	return res
}

// customNodeClasses returns the sorted, de-duplicated set of graph class_types not
// in coreNodeClasses (best-effort custom-node detection). An unparseable graph
// yields nil.
func customNodeClasses(apiGraph json.RawMessage) []string {
	var nodes map[string]apiNode
	if err := json.Unmarshal(apiGraph, &nodes); err != nil {
		return nil
	}
	seen := map[string]bool{}
	for _, n := range nodes {
		ct := strings.TrimSpace(n.ClassType)
		if ct == "" || coreNodeClasses[ct] || seen[ct] {
			continue
		}
		seen[ct] = true
	}
	if len(seen) == 0 {
		return nil
	}
	out := make([]string, 0, len(seen))
	for ct := range seen {
		out = append(out, ct)
	}
	sort.Strings(out)
	return out
}

// coreNodeClasses is a pragmatic set of built-in ComfyUI node class_types. A
// class_type absent from this set is treated as a custom node whose nodepack URN
// the user must supply (we deliberately do NOT try to auto-map class_types to
// comfyregistry nodepacks — that is out of scope for C1). The set is intentionally
// small + documented; false-positives are acceptable (the user reviews the list).
var coreNodeClasses = map[string]bool{
	"KSampler":                 true,
	"KSamplerAdvanced":         true,
	"CheckpointLoaderSimple":   true,
	"CheckpointLoader":         true,
	"CLIPTextEncode":           true,
	"CLIPSetLastLayer":         true,
	"CLIPLoader":               true,
	"DualCLIPLoader":           true,
	"VAEDecode":                true,
	"VAEEncode":                true,
	"VAEEncodeForInpaint":      true,
	"VAELoader":                true,
	"EmptyLatentImage":         true,
	"EmptySD3LatentImage":      true,
	"LatentUpscale":            true,
	"LatentUpscaleBy":          true,
	"LatentFromBatch":          true,
	"RepeatLatentBatch":        true,
	"SaveImage":                true,
	"PreviewImage":             true,
	"LoadImage":                true,
	"LoadImageMask":            true,
	"ImageScale":               true,
	"ImageScaleBy":             true,
	"ImageUpscaleWithModel":    true,
	"UpscaleModelLoader":       true,
	"LoraLoader":               true,
	"LoraLoaderModelOnly":      true,
	"ControlNetLoader":         true,
	"ControlNetApply":          true,
	"ControlNetApplyAdvanced":  true,
	"UNETLoader":               true,
	"ConditioningCombine":      true,
	"ConditioningConcat":       true,
	"ConditioningSetArea":      true,
	"ConditioningZeroOut":      true,
	"FluxGuidance":             true,
	"ModelSamplingFlux":        true,
	"ModelSamplingSD3":         true,
	"BasicScheduler":           true,
	"BasicGuider":              true,
	"SamplerCustom":            true,
	"SamplerCustomAdvanced":    true,
	"KSamplerSelect":           true,
	"RandomNoise":              true,
	"VAEDecodeTiled":           true,
	"InpaintModelConditioning": true,
	"PrimitiveNode":            true,
	"Note":                     true,
	"Reroute":                  true,
}
