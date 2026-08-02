package comfy

import (
	"path"
	"strings"
)

// ModelFileChoices indexes, by BASENAME, every combo choice in an /object_info
// payload that looks like a FILE — i.e. one that carries a filename extension.
// The result answers "can ComfyUI resolve a file called X?" with a map lookup.
//
// 🔴 WHY THE EXTENSION FILTER, AND WHY IT IS NOT "loader nodes only".
// The obvious spelling — walk every schema and reuse choicesContain — scans the
// enum options of EVERY node type, samplers and schedulers included, so a
// resource basename that collided with any enum string ("normal", "karras",
// "euler") would report "ComfyUI has this file". Measured against a live
// ComfyUI, /object_info carries 2462 node types; the overwhelming majority of
// their combos are label enums, not file lists.
//
// Restricting to "loader nodes" is the description people reach for, but there
// is no authoritative loader list in the payload — a class-name heuristic
// ("*Loader") is exactly the kind of suffix rule this repo avoids, and it would
// also miss the real loaders that are not named that way. A FILE EXTENSION is a
// property of the datum itself, needs no table, and cannot go stale when a new
// loader node type appears — which is the same future-proofing the 0019 cache
// design is built around.
//
// The trade is stated rather than hidden: a resource referenced WITHOUT an
// extension (a diffusers-style directory name, say) is not indexed and reads as
// "not found". That fails CLOSED — the app understates what ComfyUI has rather
// than claiming a file it cannot back.
//
// Basenames are normalised exactly like store.ResourceBasename ("\" → "/", then
// path.Base) so a choice carrying a subdirectory prefix — "flux/flux1-dev.safetensors"
// — is comparable with a bare reference, on every platform.
func ModelFileChoices(info ObjectInfo) map[string]struct{} {
	out := make(map[string]struct{})
	add := func(specs map[string]InputSpec) {
		for _, spec := range specs {
			// Choices is populated ALL-OR-NOTHING and only for string option lists
			// (see InputSpec.UnmarshalJSON) — a numeric combo yields nil, never
			// phantom empty strings, so nothing here can index "".
			for _, choice := range spec.Choices {
				base := choiceBasename(choice)
				if base == "" || path.Ext(base) == "" {
					continue
				}
				out[base] = struct{}{}
			}
		}
	}
	for _, sch := range info {
		add(sch.Input.Required)
		add(sch.Input.Optional)
	}
	return out
}

// HasModelFile reports whether idx (from ModelFileChoices) contains a file with
// the given reference's basename. It applies the SAME normalisation and the same
// extension requirement as the index, so the two can never disagree about what a
// name is.
func HasModelFile(idx map[string]struct{}, ref string) bool {
	base := choiceBasename(ref)
	if base == "" || path.Ext(base) == "" {
		return false
	}
	_, ok := idx[base]
	return ok
}

// choiceBasename mirrors store.ResourceBasename: normalise Windows separators,
// then take the last path element. Kept here rather than imported because
// internal/comfy must not depend on internal/store.
func choiceBasename(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, `\`, "/"))
	if s == "" {
		return ""
	}
	return path.Base(s)
}
