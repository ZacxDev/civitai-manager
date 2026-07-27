package comfy

import (
	"encoding/json"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// MissingModelRef maps one missing model FILENAME (as reported by Preflight) to
// the loader node(s) that reference it, the input name + class_type of the
// reference (used to infer the CivitAI resolution type), the inferred CivitAI
// `types` value, and the installed substitute candidates (the loader input's
// live /object_info combo Choices — the authoritative "what ComfyUI has").
//
// A single filename may be referenced by multiple nodes (e.g. the same LoRA used
// twice); NodeIDs then lists all of them so a substitute swap can replace every
// occurrence. InputName/ClassType record the FIRST referencing (deterministic,
// node-id sorted) input; Candidates is the union of choices across all
// referencing (class,input) pairs, first-seen order preserved.
type MissingModelRef struct {
	Filename    string
	NodeIDs     []string
	InputName   string
	ClassType   string
	CivitaiType string
	Candidates  []string
}

// MissingModel is the render-ready form of a MissingModelRef: the filename, a
// cleaned CivitAI search Query, the inferred CivitaiType, and the substitute
// candidates split into SameBase (candidates whose name shares the workflow's
// base-model token, shown first) and OtherCandidates (the rest — safe to
// collapse, since a loader can enumerate dozens of installed files).
type MissingModel struct {
	Filename        string
	Query           string
	CivitaiType     string
	SameBase        []string
	OtherCandidates []string
}

// versionSuffixRe matches a trailing version token like `_v70`, `-v1.5`, or a
// bare `_1.0` at the END of a base filename (extension already stripped). It is
// deliberately anchored so an interior number (e.g. `840000` in a VAE name) is
// preserved.
var versionSuffixRe = regexp.MustCompile(`(?i)[_-]v?\d[\d.]*$`)

// CleanModelQuery turns a referenced model FILENAME into a CivitAI search query:
// it drops any subfolder prefix, the model extension, and a trailing version
// token, replaces separators with spaces, and splits camelCase runs. Original
// letter case is preserved (so "fabricatedXL" → "fabricated XL", not lowercased).
//
//	seg-a/fabricatedXL_v70.safetensors → "fabricated XL"
//	sd_xl_base_1.0.safetensors         → "sd xl base"
func CleanModelQuery(filename string) string {
	s := filepath.Base(strings.TrimSpace(filename))
	// Strip a known model extension (case-insensitive).
	low := strings.ToLower(s)
	for _, ext := range modelExtensions {
		if strings.HasSuffix(low, ext) {
			s = s[:len(s)-len(ext)]
			break
		}
	}
	// Strip trailing version token(s) (`_v70`, `_1.0`, …), possibly stacked.
	for {
		n := versionSuffixRe.ReplaceAllString(s, "")
		if n == s {
			break
		}
		s = n
	}
	// Separators → spaces, then split camelCase, then collapse whitespace.
	s = strings.NewReplacer("_", " ", "-", " ", ".", " ").Replace(s)
	s = splitCamel(s)
	return strings.Join(strings.Fields(s), " ")
}

// splitCamel inserts a space at camelCase boundaries: a lower/digit followed by
// an upper ("fabricatedXL" → "fabricated XL"), and an upper-run ending before an
// upper+lower ("XLModel" → "XL Model"). Consecutive uppercase (an acronym) is
// kept together.
func splitCamel(s string) string {
	runes := []rune(s)
	var b strings.Builder
	for i, r := range runes {
		if i > 0 && unicode.IsUpper(r) {
			prev := runes[i-1]
			switch {
			case unicode.IsLower(prev) || unicode.IsDigit(prev):
				b.WriteRune(' ')
			case unicode.IsUpper(prev) && i+1 < len(runes) && unicode.IsLower(runes[i+1]):
				b.WriteRune(' ')
			}
		}
		b.WriteRune(r)
	}
	return b.String()
}

// InferCivitaiType maps a loader input name (and, as a fallback, its class_type)
// to the CivitAI models-list `types` value used to filter a resolution search.
// An unknown input/class yields "" — the caller then omits the type filter
// rather than guessing. (The models list API filters by `types` PLURAL.)
func InferCivitaiType(inputName, classType string) string {
	switch strings.ToLower(strings.TrimSpace(inputName)) {
	case "ckpt_name", "unet_name":
		return "Checkpoint"
	case "lora_name":
		return "LORA"
	case "vae_name":
		return "VAE"
	case "control_net_name":
		return "Controlnet"
	}
	if in := strings.ToLower(inputName); strings.Contains(in, "embedding") || strings.Contains(in, "embed") {
		return "TextualInversion"
	}
	switch {
	case strings.Contains(classType, "Checkpoint"),
		strings.Contains(classType, "UNET"), strings.Contains(classType, "Unet"):
		return "Checkpoint"
	case strings.Contains(classType, "Lora"), strings.Contains(classType, "LoRA"):
		return "LORA"
	case strings.Contains(classType, "VAE"):
		return "VAE"
	case strings.Contains(classType, "ControlNet"), strings.Contains(classType, "Controlnet"):
		return "Controlnet"
	}
	return ""
}

// MapMissingModels resolves each missing FILENAME (in the given order) to its
// loader references + installed substitute candidates against a live
// /object_info schema. It scans every loader node's STRING inputs for an exact
// value match; unmatched shapes are ignored (advisory aid, never an error).
func MapMissingModels(apiGraph json.RawMessage, info ObjectInfo, missing []string) []MissingModelRef {
	var nodes map[string]apiNode
	_ = json.Unmarshal(apiGraph, &nodes) // parse failure → empty map → no refs

	ids := make([]string, 0, len(nodes))
	for id := range nodes {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	out := make([]MissingModelRef, 0, len(missing))
	for _, fn := range missing {
		ref := MissingModelRef{Filename: fn}
		nodeSeen := map[string]bool{}
		candSeen := map[string]bool{}
		for _, id := range ids {
			n := nodes[id]
			if !isLoaderClass(n.ClassType) {
				continue
			}
			inNames := make([]string, 0, len(n.Inputs))
			for k := range n.Inputs {
				inNames = append(inNames, k)
			}
			sort.Strings(inNames)
			for _, inName := range inNames {
				var sv string
				if err := json.Unmarshal(n.Inputs[inName], &sv); err != nil {
					continue
				}
				if sv != fn {
					continue
				}
				if !nodeSeen[id] {
					nodeSeen[id] = true
					ref.NodeIDs = append(ref.NodeIDs, id)
				}
				if ref.InputName == "" {
					ref.InputName, ref.ClassType = inName, n.ClassType
				}
				for _, c := range loaderInputChoices(info, n.ClassType, inName) {
					if !candSeen[c] {
						candSeen[c] = true
						ref.Candidates = append(ref.Candidates, c)
					}
				}
			}
		}
		ref.CivitaiType = InferCivitaiType(ref.InputName, ref.ClassType)
		out = append(out, ref)
	}
	return out
}

// loaderInputChoices returns the /object_info combo choices for one node input
// (required first, then optional). These are exactly the files ComfyUI already
// has installed for that slot — the authoritative substitute candidate list.
func loaderInputChoices(info ObjectInfo, classType, inputName string) []string {
	sch, ok := info[classType]
	if !ok {
		return nil
	}
	if spec, ok := sch.Input.Required[inputName]; ok && len(spec.Choices) > 0 {
		return spec.Choices
	}
	if spec, ok := sch.Input.Optional[inputName]; ok {
		return spec.Choices
	}
	return nil
}

// AnalyzeMissingModels builds the render-ready MissingModel list: the mapped
// refs, each with a cleaned search Query and its substitute candidates split by
// whether they share the workflow's base-model token (baseModel may be empty →
// everything lands in OtherCandidates).
func AnalyzeMissingModels(apiGraph json.RawMessage, info ObjectInfo, missing []string, baseModel string) []MissingModel {
	refs := MapMissingModels(apiGraph, info, missing)
	out := make([]MissingModel, 0, len(refs))
	for _, r := range refs {
		same, other := splitCandidatesByBase(r.Candidates, baseModel)
		out = append(out, MissingModel{
			Filename:        r.Filename,
			Query:           CleanModelQuery(r.Filename),
			CivitaiType:     r.CivitaiType,
			SameBase:        same,
			OtherCandidates: other,
		})
	}
	return out
}

// splitCandidatesByBase partitions candidates by whether their (normalized) name
// contains a token of the workflow's base model. An empty baseModel (or one we
// cannot tokenize) leaves everything in `other`, preserving order.
func splitCandidatesByBase(candidates []string, baseModel string) (same, other []string) {
	toks := baseModelTokens(baseModel)
	if len(toks) == 0 {
		return nil, append([]string(nil), candidates...)
	}
	for _, c := range candidates {
		nc := normToken(c)
		matched := false
		for _, t := range toks {
			if strings.Contains(nc, t) {
				matched = true
				break
			}
		}
		if matched {
			same = append(same, c)
		} else {
			other = append(other, c)
		}
	}
	return same, other
}

// baseModelTokens returns the lowercase, separator-free tokens that identify a
// base-model family in a candidate filename (heuristic). "" for an empty or
// unrecognized base model.
func baseModelTokens(baseModel string) []string {
	n := normToken(baseModel)
	if n == "" {
		return nil
	}
	switch {
	case strings.Contains(n, "illustrious"):
		return []string{"illustrious", "ilxl"}
	case strings.Contains(n, "pony"):
		return []string{"pony"}
	case strings.Contains(n, "flux"):
		return []string{"flux"}
	case strings.Contains(n, "sdxl"), strings.Contains(n, "xl"):
		return []string{"sdxl", "xl"}
	case strings.Contains(n, "sd35"), strings.Contains(n, "sd3"):
		return []string{"sd35", "sd3"}
	case strings.Contains(n, "sd15"), strings.Contains(n, "sd1"):
		return []string{"sd15", "sd1"}
	case strings.Contains(n, "sd2"):
		return []string{"sd2"}
	}
	return nil
}

// normToken lowercases s and drops every non-alphanumeric rune, so "SD 1.5" and
// "sd_1.5" both normalize to "sd15".
func normToken(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// ApplySubstitutions returns a COPY of the api-format graph with every loader
// input string value that equals a key of `sub` replaced by the mapped value —
// every occurrence across every loader node. All other node fields (class_type,
// _meta, non-matching inputs) are preserved. The input graph is never mutated. A
// parse failure or empty sub returns apiGraph unchanged.
func ApplySubstitutions(apiGraph json.RawMessage, sub map[string]string) json.RawMessage {
	if len(sub) == 0 {
		return apiGraph
	}
	var nodes map[string]json.RawMessage
	if err := json.Unmarshal(apiGraph, &nodes); err != nil {
		return apiGraph
	}
	changed := false
	for id, raw := range nodes {
		var node map[string]json.RawMessage
		if err := json.Unmarshal(raw, &node); err != nil {
			continue
		}
		ct := ""
		if ctRaw, ok := node["class_type"]; ok {
			_ = json.Unmarshal(ctRaw, &ct)
		}
		if !isLoaderClass(ct) {
			continue
		}
		inputsRaw, ok := node["inputs"]
		if !ok {
			continue
		}
		var inputs map[string]json.RawMessage
		if err := json.Unmarshal(inputsRaw, &inputs); err != nil {
			continue
		}
		nodeChanged := false
		for k, v := range inputs {
			var sv string
			if err := json.Unmarshal(v, &sv); err != nil {
				continue
			}
			repl, ok := sub[sv]
			if !ok {
				continue
			}
			nb, err := json.Marshal(repl)
			if err != nil {
				continue
			}
			inputs[k] = nb
			nodeChanged = true
		}
		if !nodeChanged {
			continue
		}
		ib, err := json.Marshal(inputs)
		if err != nil {
			continue
		}
		node["inputs"] = ib
		nb, err := json.Marshal(node)
		if err != nil {
			continue
		}
		nodes[id] = nb
		changed = true
	}
	if !changed {
		return apiGraph
	}
	out, err := json.Marshal(nodes)
	if err != nil {
		return apiGraph
	}
	return out
}
