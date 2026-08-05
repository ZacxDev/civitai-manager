package comfy

import (
	"encoding/json"
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
// (empty URN) for every referenced class_type that is not a ComfyUI built-in, so
// the user can fill in the nodepack URN.
//
// origins is a NodeOrigins index over a cached /object_info and is the
// AUTHORITATIVE answer for built-in-vs-custom; pass nil when none is available
// and detection falls back to coreNodeClasses. See customNodeClasses.
//
// It is permissive: an unparseable graph yields whatever could be recovered.
func ResolveResources(apiGraph json.RawMessage, lookup ResourceLookup, origins map[string]NodeOrigin) ([]ResolvedResource, error) {
	var out []ResolvedResource

	// 1. Model-file resources (loader inputs), ordered by node id then input name.
	// (NOT "first-seen": an api graph is a JSON object, so decoding it leaves no
	// document order to preserve — see ExtractResources.) This order reaches the
	// cloud panel's resolved-resource table and the submitted input.resources, so it
	// has to be stable across runs of the same graph.
	refs, _ := ExtractResources(apiGraph)
	for _, ref := range refs {
		out = append(out, resolveModelRef(ref, lookup))
	}

	// 2. Custom nodes: class_types ComfyUI does not ship.
	for _, ct := range customNodeClasses(apiGraph, origins) {
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
	// PathBase, not filepath.Base: ref is a graph reference and LocalFileByBasename is
	// keyed by store.ResourceBasename, which folds "\" to "/" — so a Windows-authored
	// reference has to be folded the same way or the lookup can never hit.
	base := PathBase(ref)
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

// customNodeClasses returns the sorted, de-duplicated set of graph class_types
// that are not ComfyUI built-ins. An unparseable graph yields nil.
//
// 🔴 TWO TIERS, AND THE ORDER IS THE WHOLE POINT.
//
//  1. OBSERVED (origins, from a cached /object_info): ComfyUI itself reported the
//     registering python_module, so built-in-vs-custom is a fact. This tier
//     answers for the 2462 node types a live ComfyUI knows about.
//  2. UNKNOWN (class absent from the payload, or no payload at all): fall back to
//     coreNodeClasses, i.e. EXACTLY the pre-existing behaviour.
//
// Tier 2 is why coreNodeClasses is kept rather than deleted, and it earns its
// place twice over. It is not merely the cold-cache path: `PrimitiveNode`, `Note`
// and `Reroute` are frontend-only LiteGraph nodes that a live ComfyUI reports in
// NO /object_info (verified against the running instance), so tier 1 can never
// classify them and only the table keeps them from being called custom.
//
// 🔴 THE FAILURE DIRECTIONS ARE NOT SYMMETRIC, WHICH IS WHY THE FALLBACK IS THE
// OLD TABLE AND NOT "assume built-in". A false CUSTOM is the bug this fixes: the
// banner tells the user their workflow probably cannot run on cloud, and they
// stop — silently, with no way to discover they were wrong. A false BUILT-IN
// merely omits the warning; the user runs a FREE whatif estimate, and CivitAI's
// own CustomComfy step rejects a genuine nodepack at submit, loudly, from the
// authority on the question. Cheap and recoverable beats silent and terminal.
// But "assume built-in when we know nothing" would delete the signal entirely on
// every install with a cold cache — a much larger behaviour change than this bug
// warrants — so the unknown case holds the status quo instead.
func customNodeClasses(apiGraph json.RawMessage, origins map[string]NodeOrigin) []string {
	var nodes map[string]apiNode
	if err := json.Unmarshal(apiGraph, &nodes); err != nil {
		return nil
	}
	seen := map[string]bool{}
	for _, n := range nodes {
		ct := strings.TrimSpace(n.ClassType)
		if ct == "" || seen[ct] {
			continue
		}
		switch OriginOf(origins, ct) {
		case NodeOriginBuiltin:
			continue // observed: ComfyUI ships it
		case NodeOriginCustom:
			seen[ct] = true // observed: it came from custom_nodes/
			continue
		}
		// Unknown: no observation for this class — hold the pre-existing behaviour.
		if coreNodeClasses[ct] {
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

// coreNodeClasses is the FALLBACK tier of custom-node detection, used only for a
// class_type that a cached /object_info could not classify (see
// customNodeClasses). It is no longer the primary signal, and it must not be
// treated as an inventory of ComfyUI's built-ins: measured against the live
// instance, these 50 entries cover 47 of the 790 node types ComfyUI actually
// ships, and the other 743 are exactly why a table-only detector called a real
// built-in custom in 44 of the operator's 70 workflows.
//
// 🔴 DO NOT DELETE IT, and do not "complete" it either. Deleting it would call
// `PrimitiveNode`, `Note` and `Reroute` custom nodes — the 3 entries here that
// appear in NO /object_info, because they are frontend-only LiteGraph nodes — and
// would drop every install with a cold cache to zero detection. Completing it
// would mean hand-maintaining 790 names that change every ComfyUI release, which
// is the maintenance burden NodeOrigins exists to retire.
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
