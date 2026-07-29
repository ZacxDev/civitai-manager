package comfy

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Attribution of a MISSING ComfyUI class_type to the custom-node PACK that
// provides it. Pure and I/O-free: BuildIndex takes the two ComfyUI-Manager JSON
// documents verbatim and Attribute answers from them, so the whole engine is
// unit-testable against captured real responses.
//
// Every rule below is measured against the LIVE Manager V3.41 index
// (2026-07-29, 5573 mapping keys / 7358 packs) — see
// claudedocs/CUSTOM-NODE-RESOLUTION-DESIGN.md. The three that cost real bugs:
//
//   - The join is TWO-WAY. 2389 of 5573 getmappings keys (43%) are raw GitHub
//     URLs, not pack ids. A pack-id-only join silently under-reports as a "we
//     don't know this node" false negative, never as an error.
//   - 38 mapping entries carry a `nodename_pattern` REGEX instead of an
//     enumerated class list, and one of them is `.*` — a catch-all that would
//     confidently attribute every unresolvable node in every workflow to one
//     pack. It is discarded EMPIRICALLY (probe-matched), not by blacklisting the
//     literal string, so the next catch-all is caught too.
//   - 1178 of 7358 packs are nightly-only (`cnr_latest == "nightly"`) and
//     ComfyUI-Manager REFUSES them at its default security policy, so
//     Installable is a real, common false — with a user-facing Reason.

// Attribution sources, recorded on each Pack so the UI can label confidence.
// A pack is only as trustworthy as the weakest rung that produced it.
const (
	// SourceMap is an EXACT class-name hit in Manager's enumerated class list —
	// the highest-confidence rung.
	SourceMap = "map"
	// SourceRegistry is a hit from the Comfy Registry API
	// (GET https://api.comfy.org/comfy-nodes/{class}/node). Also exact, but
	// remote and first-party rather than Manager's own index.
	SourceRegistry = "registry"
	// SourcePattern is a `nodename_pattern` regex hit — the LOWEST-confidence
	// rung. Render it as "likely provided by", never as certain.
	SourcePattern = "pattern"
)

// sourceRank orders the rungs by confidence for deterministic, useful output.
var sourceRank = map[string]int{SourceMap: 0, SourceRegistry: 1, SourcePattern: 2}

// Pack is a custom-node pack that provides one or more of the missing class_types.
type Pack struct {
	// ID is the ComfyUI-Manager pack id, empty when only a URL key resolved and
	// that URL is absent from the node-pack list.
	ID string
	// Title is the human label (Manager's title/title_aux, else the id/URL).
	Title string
	// Repository is the canonical repo URL. It is shown to the user BEFORE any
	// install and is the manual-install answer when Installable is false.
	Repository string
	// Version is the pack's `cnr_latest` (the Comfy Registry release), or
	// "nightly"/"" when there is no registry release.
	Version string
	// Installable is true only when the pack is CNR-released, i.e. ComfyUI-Manager
	// can install it at its DEFAULT security policy without a git-url install.
	Installable bool
	// Reason is a plain, user-facing explanation of why Installable is false.
	// Empty when Installable is true.
	Reason string
	// Classes are the missing class_types this pack provides, sorted.
	Classes []string
	// Source is the ladder rung that produced this attribution (SourceMap /
	// SourceRegistry / SourcePattern).
	Source string
}

// NodePackIndex is the joined class -> pack index built from ComfyUI-Manager's
// getmappings + getlist documents. The zero value is not usable; build it with
// BuildIndex, which ALWAYS returns a non-nil index (an empty one on malformed
// input) so no caller can nil-panic on hostile data.
type NodePackIndex struct {
	// byClass maps an exact class name to the mapping keys that claim it. A class
	// can be claimed by MANY packs (PreViewVideo by 16 live), so this is a slice
	// and attribution never silently collapses to one.
	byClass map[string][]string
	// patterns are the surviving (non-catch-all, compilable) nodename_pattern
	// entries, in mapping-key sorted order for determinism.
	patterns []patternEntry
	// meta is per-mapping-key display metadata (title_aux and friends).
	meta map[string]mappingMeta
	// packs is getlist's node_packs, keyed by its map key (which is the pack id
	// for most, but 3784 of 7358 entries carry no explicit `id` field at all).
	packs map[string]nodePackEntry
	// byRepo maps a NORMALIZED repo/file URL to a packs key. This is the second
	// leg of the two-way join: it is the ONLY way a URL mapping key resolves.
	byRepo map[string]string
}

// patternEntry is one surviving nodename_pattern rule.
type patternEntry struct {
	key string
	re  *regexp.Regexp
}

// mappingMeta is the second element of a getmappings value: the display metadata
// object. Only the fields we surface are decoded; the rest is ignored.
type mappingMeta struct {
	TitleAux string `json:"title_aux"`
	Title    string `json:"title"`
	Pattern  string `json:"nodename_pattern"`
}

// nodePackEntry is one getlist `node_packs` value. Field presence varies wildly in
// the real document — 3784 of 7358 entries have no `id` and 3967 no `cnr_latest`
// — so every consumer must treat the empty string as "absent", never as a value.
type nodePackEntry struct {
	ID         string   `json:"id"`
	Title      string   `json:"title"`
	Repository string   `json:"repository"`
	Reference  string   `json:"reference"`
	Files      []string `json:"files"`
	CNRLatest  string   `json:"cnr_latest"`
	Version    string   `json:"version"`
}

// catchAllProbes are synthetic class names that no real ComfyUI node pack can
// legitimately claim. A nodename_pattern is treated as a CATCH-ALL — and
// discarded — when it matches EVERY one of them.
//
// Requiring ALL probes to match (rather than any single one) is deliberate: a
// genuine catch-all like `.*` matches anything, so it matches all three, while a
// narrow-but-unlucky real pattern (say `^Z`) would match at most one of these
// structurally different strings and is therefore kept. This is the empirical
// guard the design mandates — blacklisting the literal string `.*` would break
// the moment upstream adds `.+`, `^`, or `(?s).*`.
var catchAllProbes = []string{
	"cmprobe9f3nonexistentclass",
	"ZZZ Probe Class 42",
	"__cm.probe/unlikely__",
}

// BuildIndex joins Manager's getmappings and getlist documents into an
// attribution index.
//
// Both arguments are UNTRUSTED (they originate from a network fetch of a
// third-party index). Malformed input degrades to an EMPTY-but-usable index plus
// an error — never a nil index and never a panic — so a caller that ignores the
// error still gets "everything is unattributed" rather than a crash.
//
// getlist may be nil/empty: it is GONE in ComfyUI-Manager V4's default mode, and
// attribution still works from getmappings alone (with Installable false for
// every pack, since only getlist carries `cnr_latest`).
func BuildIndex(mappings, getlist json.RawMessage) (*NodePackIndex, error) {
	ix := &NodePackIndex{
		byClass: map[string][]string{},
		meta:    map[string]mappingMeta{},
		packs:   map[string]nodePackEntry{},
		byRepo:  map[string]string{},
	}

	if err := ix.loadPacks(getlist); err != nil {
		return ix, err
	}
	if err := ix.loadMappings(mappings); err != nil {
		return ix, err
	}
	return ix, nil
}

// loadPacks decodes getlist's node_packs and builds the URL->pack leg of the join.
func (ix *NodePackIndex) loadPacks(getlist json.RawMessage) error {
	if len(strings.TrimSpace(string(getlist))) == 0 {
		return nil // absent getlist (V4 default mode) is a supported state
	}
	var doc struct {
		NodePacks map[string]nodePackEntry `json:"node_packs"`
	}
	if err := json.Unmarshal(getlist, &doc); err != nil {
		return fmt.Errorf("parse node-pack list: %w", err)
	}
	// Sort the keys so a URL claimed by two packs resolves to the SAME pack on
	// every run (Go map iteration is randomized).
	keys := make([]string, 0, len(doc.NodePacks))
	for k := range doc.NodePacks {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		p := doc.NodePacks[k]
		ix.packs[k] = p
		for _, u := range append([]string{p.Repository, p.Reference}, p.Files...) {
			if n := normalizeRepoURL(u); n != "" {
				if _, taken := ix.byRepo[n]; !taken {
					ix.byRepo[n] = k
				}
			}
		}
	}
	return nil
}

// loadMappings decodes getmappings ({key: [[classes...], {meta}]}) into the
// class index plus the surviving pattern rules.
func (ix *NodePackIndex) loadMappings(mappings json.RawMessage) error {
	if len(strings.TrimSpace(string(mappings))) == 0 {
		return nil
	}
	var doc map[string][]json.RawMessage
	if err := json.Unmarshal(mappings, &doc); err != nil {
		return fmt.Errorf("parse node mappings: %w", err)
	}
	keys := make([]string, 0, len(doc))
	for k := range doc {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, key := range keys {
		entry := doc[key]
		var classes []string
		if len(entry) > 0 {
			// Best-effort: a malformed element is skipped, never fatal — one bad
			// entry in a 5573-key third-party document must not lose the other 5572.
			_ = json.Unmarshal(entry[0], &classes)
		}
		var meta mappingMeta
		if len(entry) > 1 {
			_ = json.Unmarshal(entry[1], &meta)
		}
		ix.meta[key] = meta

		for _, cls := range classes {
			if cls = strings.TrimSpace(cls); cls != "" {
				ix.byClass[cls] = append(ix.byClass[cls], key)
			}
		}
		if pat := strings.TrimSpace(meta.Pattern); pat != "" {
			if re := compileSafePattern(pat); re != nil {
				ix.patterns = append(ix.patterns, patternEntry{key: key, re: re})
			}
		}
	}
	// Dedupe + sort each class's claimants so Attribute is deterministic.
	for cls, ks := range ix.byClass {
		ix.byClass[cls] = sortedUnique(ks)
	}
	return nil
}

// compileSafePattern compiles a nodename_pattern and returns nil when the rule
// must not be used: an uncompilable regex (Go's RE2 rejects the backreference /
// lookaround syntax Python's `re` accepts) or a CATCH-ALL that matches the
// synthetic probes.
//
// Matching is UNANCHORED (regexp.MatchString == Python's re.search), which is
// what ComfyUI-Manager's own attribution does in manager_core.py — and is
// required for the real `\(mtb\)$` rule to match "Note Plus (mtb)". Note that
// Manager's getmappings-augmentation path in manager_server.py uses the stricter
// re.match instead; that upstream inconsistency is why this is spelled out.
func compileSafePattern(pat string) *regexp.Regexp {
	re, err := regexp.Compile(pat)
	if err != nil {
		return nil
	}
	for _, probe := range catchAllProbes {
		if !re.MatchString(probe) {
			return re // failed to match a probe => not a catch-all => keep
		}
	}
	return nil // matched every probe => catch-all => discard
}

// Attribute maps missing class_types to the packs that provide them, applying the
// enumerated-class rung first and the lower-confidence nodename_pattern rung only
// for classes the first rung could not place.
//
// It returns BOTH results. A class may resolve to MULTIPLE candidate packs (16
// packs claim PreViewVideo live), so nothing is silently collapsed to one answer,
// and unattributed is a first-class UI state rather than an error — 4 of wf581's
// 7 real missing classes land there.
//
// Ordering is deterministic: packs sort by confidence rung, then title, then id,
// then repository; each pack's Classes and unattributed are sorted.
//
// Results are grouped by (pack, RUNG), so one pack CAN appear twice when it was
// reached two ways — live, comfy-mtb comes back once at map confidence for
// "Pick From Batch (mtb)" and again at pattern confidence for "Note Plus (mtb)".
// That is deliberate: those two classes are not equally certain and collapsing
// them would launder the weaker one into the stronger label. A caller that wants
// exactly one entry per pack must fold them with MergePacks and accept that the
// strongest rung then labels the whole group.
func (ix *NodePackIndex) Attribute(missing []string) (packs []Pack, unattributed []string) {
	if ix == nil {
		return nil, sortedUnique(missing)
	}
	// keyed accumulates classes per (mapping key, source) so one pack hit by two
	// classes is ONE Pack listing both.
	type acc struct {
		key     string
		source  string
		classes []string
	}
	order := []string{}
	keyed := map[string]*acc{}

	add := func(mapKey, source, class string) {
		id := source + "\x00" + mapKey
		a, ok := keyed[id]
		if !ok {
			a = &acc{key: mapKey, source: source}
			keyed[id] = a
			order = append(order, id)
		}
		a.classes = append(a.classes, class)
	}

	for _, cls := range sortedUnique(missing) {
		if claimants := ix.byClass[cls]; len(claimants) > 0 {
			for _, k := range claimants {
				add(k, SourceMap, cls)
			}
			continue
		}
		matched := false
		for _, p := range ix.patterns {
			if p.re.MatchString(cls) {
				add(p.key, SourcePattern, cls)
				matched = true
			}
		}
		if !matched {
			unattributed = append(unattributed, cls)
		}
	}

	for _, id := range order {
		a := keyed[id]
		p := ix.packFor(a.key)
		p.Source = a.source
		p.Classes = sortedUnique(a.classes)
		packs = append(packs, p)
	}
	sortPacks(packs)
	return packs, unattributed
}

// packFor resolves a getmappings key to a Pack via the TWO-WAY join: pack id
// first, then the key-as-URL matched against every pack's repository/reference/
// files. The URL leg is not optional — 43% of live mapping keys are URLs, and
// e.g. `https://github.com/GACLove/ComfyUI-VFI` resolves ONLY through it.
//
// A key that resolves through neither leg still yields a usable Pack (title from
// the mapping metadata, repository from the key when it is a URL) so the
// manual-install answer is always available; it is simply never Installable.
func (ix *NodePackIndex) packFor(key string) Pack {
	meta := ix.meta[key]

	packKey, entry, found := "", nodePackEntry{}, false
	if e, ok := ix.packs[key]; ok { // leg 1: pack id
		packKey, entry, found = key, e, true
	} else if n := normalizeRepoURL(key); n != "" { // leg 2: URL
		if k, ok := ix.byRepo[n]; ok {
			packKey, entry, found = k, ix.packs[k], true
		}
	}

	p := Pack{Title: firstNonEmpty(meta.TitleAux, meta.Title, key)}
	if !found {
		if isHTTPURL(key) {
			p.Repository = key
		}
		p.Reason = "ComfyUI-Manager's node-pack list does not list this pack, so it cannot be installed automatically. Install it by hand."
		return p
	}

	p.ID = firstNonEmpty(entry.ID, packKey)
	p.Title = firstNonEmpty(entry.Title, meta.TitleAux, meta.Title, p.ID)
	p.Repository = firstNonEmpty(entry.Repository, entry.Reference, firstFile(entry.Files))
	if p.Repository == "" && isHTTPURL(key) {
		p.Repository = key
	}
	p.Version = firstNonEmpty(entry.CNRLatest, entry.Version)

	switch {
	case entry.CNRLatest == "":
		// No Comfy Registry release at all: Manager can only reach it as a raw
		// git url, which its default policy refuses.
		p.Reason = "This pack has no Comfy Registry release, so ComfyUI-Manager can only install it as a raw git clone — which it refuses at its default security settings. Install it by hand from the repository URL."
	case entry.CNRLatest == "nightly":
		// 1178 of 7358 live packs. This is the COMMON blocked case, not an edge.
		p.Reason = "This pack ships only a nightly (git) build, not a Comfy Registry release. ComfyUI-Manager refuses git-url installs at its default security settings, so install it by hand from the repository URL (or enable allow_git_url_install in ComfyUI-Manager)."
	default:
		p.Installable = true
	}
	return p
}

// MergePacks folds attribution results from several rungs (map/pattern from
// Attribute, registry from a Registry lookup) into one deterministic list,
// unioning the Classes of packs that are the same pack. The strongest
// (lowest-rank) Source wins for the merged entry.
//
// Identity is a SET of keys — the pack id AND the normalized repository URL —
// and two entries merge when they share ANY key. Matching on one key alone is
// not enough, and that was a live-caught bug: the static extension-node-map has
// no pack ids at all, so its comfy-mtb entry is identified only by
// github.com/melmass/comfy_mtb while the Registry's is identified only by the id
// `comfy-mtb`. An id-first-else-url rule put them in different buckets and
// double-listed the pack. Title is a LAST-RESORT key only (titles collide across
// unrelated packs).
//
// Because one entry can carry keys belonging to two existing groups, matching
// groups are collapsed together rather than merged into the first hit.
func MergePacks(sets ...[]Pack) []Pack {
	var groups []*Pack
	index := map[string]int{} // identity key -> group index

	for _, set := range sets {
		for _, p := range set {
			keys := packIdentityKeys(p)

			// Every distinct group this entry belongs to, in ascending order.
			var hits []int
			seen := map[int]bool{}
			for _, k := range keys {
				if gi, ok := index[k]; ok && !seen[gi] {
					seen[gi] = true
					hits = append(hits, gi)
				}
			}
			sort.Ints(hits)

			if len(hits) == 0 {
				cp := p
				cp.Classes = sortedUnique(p.Classes)
				groups = append(groups, &cp)
				gi := len(groups) - 1
				for _, k := range keys {
					index[k] = gi
				}
				continue
			}

			target := hits[0]
			// Collapse any further groups this entry bridges into the first.
			for _, gi := range hits[1:] {
				mergePackInto(groups[target], *groups[gi])
				groups[gi] = nil
				for k, v := range index {
					if v == gi {
						index[k] = target
					}
				}
			}
			mergePackInto(groups[target], p)
			for _, k := range keys {
				index[k] = target
			}
			// A merged group may have gained new keys (an id or URL it lacked).
			for _, k := range packIdentityKeys(*groups[target]) {
				if _, ok := index[k]; !ok {
					index[k] = target
				}
			}
		}
	}

	out := make([]Pack, 0, len(groups))
	for _, g := range groups {
		if g != nil {
			out = append(out, *g)
		}
	}
	sortPacks(out)
	return out
}

// mergePackInto folds src into dst: classes union, the strongest Source wins,
// empty fields are filled, and a proven-installable stays installable.
func mergePackInto(dst *Pack, src Pack) {
	dst.Classes = sortedUnique(append(dst.Classes, src.Classes...))
	if sourceRank[src.Source] < sourceRank[dst.Source] {
		dst.Source = src.Source
	}
	dst.ID = firstNonEmpty(dst.ID, src.ID)
	dst.Repository = firstNonEmpty(dst.Repository, src.Repository)
	dst.Version = firstNonEmpty(dst.Version, src.Version)
	dst.Title = firstNonEmpty(dst.Title, src.Title)
	// Installable is proven by ComfyUI-Manager's getlist; a rung that could not
	// prove it (the Registry, or a Manager-less static index) must never downgrade
	// a rung that did.
	if src.Installable {
		dst.Installable = true
		dst.Reason = ""
	} else if !dst.Installable {
		dst.Reason = firstNonEmpty(dst.Reason, src.Reason)
	}
}

// packIdentityKeys returns every key by which a pack may be recognised: its id
// and its normalized repository URL. Title is used ONLY when the pack has
// neither, because titles are not unique across packs.
func packIdentityKeys(p Pack) []string {
	var keys []string
	if id := strings.TrimSpace(p.ID); id != "" {
		keys = append(keys, "id:"+strings.ToLower(id))
	}
	if n := normalizeRepoURL(p.Repository); n != "" {
		keys = append(keys, "url:"+n)
	}
	if len(keys) == 0 {
		if t := strings.TrimSpace(p.Title); t != "" {
			keys = append(keys, "title:"+strings.ToLower(t))
		}
	}
	return keys
}

// sortPacks orders packs by confidence rung, then title/id/repository, so the UI
// renders the same order every time.
func sortPacks(packs []Pack) {
	sort.SliceStable(packs, func(i, j int) bool {
		a, b := packs[i], packs[j]
		if ra, rb := sourceRank[a.Source], sourceRank[b.Source]; ra != rb {
			return ra < rb
		}
		if a.Title != b.Title {
			return a.Title < b.Title
		}
		if a.ID != b.ID {
			return a.ID < b.ID
		}
		return a.Repository < b.Repository
	})
}

// normalizeRepoURL canonicalizes a repo/file URL for the URL leg of the join:
// lower-cased, scheme/`www.`/`.git`/trailing-slash stripped. It returns "" for a
// non-http(s) value so a bare pack id never collides with a URL.
//
// Matching on the normalized form is what makes the join survive the real
// document's http/https, trailing-slash and `.git` inconsistencies.
func normalizeRepoURL(raw string) string {
	s := strings.TrimSpace(raw)
	if !isHTTPURL(s) {
		return ""
	}
	s = strings.ToLower(s)
	s = strings.TrimPrefix(strings.TrimPrefix(s, "https://"), "http://")
	s = strings.TrimPrefix(s, "www.")
	s = strings.TrimRight(s, "/")
	s = strings.TrimSuffix(s, ".git")
	return s
}

// isHTTPURL reports whether raw looks like an http(s) URL (the discriminator
// between a pack-id mapping key and a URL mapping key).
func isHTTPURL(raw string) bool {
	s := strings.ToLower(strings.TrimSpace(raw))
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

// firstFile returns the first non-empty entry of files, or "".
func firstFile(files []string) string {
	for _, f := range files {
		if f = strings.TrimSpace(f); f != "" {
			return f
		}
	}
	return ""
}

// firstNonEmpty returns the first non-blank string, or "".
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if t := strings.TrimSpace(v); t != "" {
			return t
		}
	}
	return ""
}

// sortedUnique returns the deduped, sorted, blank-stripped form of in.
func sortedUnique(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, v := range in {
		if v = strings.TrimSpace(v); v == "" {
			continue
		}
		if _, dup := seen[v]; dup {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	if len(out) == 0 {
		return nil
	}
	sort.Strings(out)
	return out
}
