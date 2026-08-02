package comfy

import (
	"encoding/json"
	"strings"
)

// NodeOrigin says where a node class_type came from: ComfyUI itself, a pack
// installed into custom_nodes/, or "we do not know".
type NodeOrigin int

const (
	// NodeOriginUnknown: the class_type is not in the /object_info we have (or we
	// have none at all). It is NOT a synonym for "custom" — see NodeOrigins.
	NodeOriginUnknown NodeOrigin = iota
	// NodeOriginBuiltin: ComfyUI ships this node type.
	NodeOriginBuiltin
	// NodeOriginCustom: this node type came from custom_nodes/.
	NodeOriginCustom
)

// customNodesModuleRoot is the ONE module namespace that means "not shipped with
// ComfyUI". Everything else is core.
const customNodesModuleRoot = "custom_nodes"

// NodeOrigins indexes an /object_info payload by class_type into its origin.
//
// 🔴 THE RULE IS A DENY-LIST ON `custom_nodes.*`, NOT AN ALLOW-LIST OF CORE
// NAMESPACES — and that distinction is measured, not stylistic. Against the live
// ComfyUI v0.27 at 127.0.0.1:8188 (2462 node types, /object_info = 4,661,988
// bytes), the four `python_module` roots are:
//
//	custom_nodes     1672
//	comfy_extras      501
//	comfy_api_nodes   224
//	nodes              65
//
// so `custom_nodes` excluded leaves exactly **790 built-ins** — the same 790 this
// repo measured independently. The obvious allow-list spelling (`comfy_extras.*`
// or `nodes`, which is how the fix was originally described) yields only **566**
// and would silently reclassify all **224 `comfy_api_nodes.*`** types — ComfyUI's
// bundled API nodes — as custom. A deny-list also ages in the safe direction: a
// core namespace ComfyUI adds tomorrow reads as built-in, where an allow-list
// would call every one of its node types custom.
//
// Matching is on the FIRST DOT-SEGMENT, never a string prefix. `strings.HasPrefix`
// would also match a hypothetical `custom_nodesomething`, and `nodes` is a bare
// value with no dot at all (verified: it is the only dot-free module in the live
// payload, and nothing else starts with "nodes").
//
// 🔴 ABSENT IS `NodeOriginUnknown`, NOT `NodeOriginCustom`. A class_type missing
// from /object_info is genuinely unclassifiable here, and conflating the two would
// re-introduce this bug's mirror image: `PrimitiveNode`, `Note` and `Reroute` are
// LiteGraph FRONTEND-only nodes that appear in no /object_info at all (verified
// live — they are the only 3 of coreNodeClasses' 50 entries that are absent) and
// are emphatically not custom nodes. The caller decides what to do with unknown;
// see customNodeClasses, which falls back to coreNodeClasses.
//
// A nil/empty payload yields a nil index, which reports NodeOriginUnknown for
// everything — the correct answer when we have no observation.
//
// 🔴 IT DECODES ONLY `python_module`, AND THAT IS DELIBERATE — the obvious
// spelling, taking an already-decoded ObjectInfo, is what this replaced.
// Unmarshalling into ObjectInfo materialises every node's required/optional
// InputSpec maps through InputSpec's custom UnmarshalJSON; comfy_model_cache.go
// records that as ~88 ms against the real 4,661,988-byte payload, and this runs
// on a page render. The shape below allocates one string per node type instead
// (measured: ~30 ms for the same live payload). NodeSchema therefore does NOT
// carry a PythonModule field — adding one back would strand this function's
// reason to exist and the deadcode gate would catch the orphan.
//
// A decode failure yields a nil index, so every class reads NodeOriginUnknown and
// detection falls back to coreNodeClasses — a corrupt cache row degrades to the
// pre-existing behaviour rather than breaking the page.
func NodeOrigins(raw []byte) map[string]NodeOrigin {
	if len(raw) == 0 {
		return nil
	}
	var modules map[string]struct {
		PythonModule string `json:"python_module"`
	}
	if err := json.Unmarshal(raw, &modules); err != nil || len(modules) == 0 {
		return nil
	}
	out := make(map[string]NodeOrigin, len(modules))
	for classType, m := range modules {
		out[classType] = originOfModule(m.PythonModule)
	}
	return out
}

// originOfModule classifies one `python_module` value.
//
// An EMPTY module is Unknown, not Builtin: a payload that omitted the field tells
// us nothing, and answering "built-in" there would assert on absent evidence.
func originOfModule(pythonModule string) NodeOrigin {
	m := strings.TrimSpace(pythonModule)
	if m == "" {
		return NodeOriginUnknown
	}
	root := m
	if i := strings.IndexByte(root, '.'); i >= 0 {
		root = root[:i]
	}
	if root == customNodesModuleRoot {
		return NodeOriginCustom
	}
	return NodeOriginBuiltin
}

// OriginOf reports the origin an index holds for a class_type. A nil index (no
// cached /object_info) reports NodeOriginUnknown for every class.
func OriginOf(idx map[string]NodeOrigin, classType string) NodeOrigin {
	if idx == nil {
		return NodeOriginUnknown
	}
	o, ok := idx[strings.TrimSpace(classType)]
	if !ok {
		return NodeOriginUnknown
	}
	return o
}
