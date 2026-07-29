package comfy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The fixtures are TRIMMED CAPTURES of the real ComfyUI-Manager V3.41 endpoints
// on 127.0.0.1:8188 (2026-07-29): entries are real, only the class lists and the
// long prose descriptions were cut down. They deliberately carry every shape that
// broke a naive implementation — URL keys, nodename_pattern entries, the `.*`
// catch-all, a nightly-only pack, a pack with neither `id` nor `cnr_latest`, and
// a class claimed by three packs.
func loadFixture(t *testing.T, name string) json.RawMessage {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return json.RawMessage(b)
}

func realIndex(t *testing.T) *NodePackIndex {
	t.Helper()
	ix, err := BuildIndex(
		loadFixture(t, "nodepack_getmappings.json"),
		loadFixture(t, "nodepack_getlist.json"),
	)
	if err != nil {
		t.Fatalf("BuildIndex: %v", err)
	}
	return ix
}

// packByID finds an attributed pack by its id (or, when the pack has no id, by
// its repository URL).
func packByID(packs []Pack, id string) (Pack, bool) {
	for _, p := range packs {
		if p.ID == id || p.Repository == id {
			return p, true
		}
	}
	return Pack{}, false
}

// TestAttributeRealClasses pins the ladder's rungs 1 and 3 against the real
// missing classes from wf581, including the two-way join and the pattern rung.
func TestAttributeRealClasses(t *testing.T) {
	ix := realIndex(t)

	tests := []struct {
		name       string
		class      string
		wantPackID string // pack ID, or repository URL for an id-less pack
		wantSource string
	}{
		{
			// Rung 1 + pack-id join. Comfyroll is the wf581 headline case.
			name:       "enumerated class via pack-id join",
			class:      "CR Float To Integer",
			wantPackID: "ComfyUI_Comfyroll_CustomNodes",
			wantSource: SourceMap,
		},
		{
			// 🔴 The two-way join. The mapping key here is the raw URL
			// https://github.com/GACLove/ComfyUI-VFI; the pack key is
			// "ComfyUI-VFI". A pack-id-only join returns NOTHING for this.
			name:       "enumerated class via URL-key join",
			class:      "RIFEInterpolation",
			wantPackID: "ComfyUI-VFI",
			wantSource: SourceMap,
		},
		{
			name:       "enumerated class on a CNR-released pack",
			class:      "Pick From Batch (mtb)",
			wantPackID: "comfy-mtb",
			wantSource: SourceMap,
		},
		{
			// Rung 3. "Note Plus (mtb)" is in NEITHER the enumerated list nor the
			// Registry; only the real `\(mtb\)$` pattern places it, at lower
			// confidence.
			name:       "unenumerated class via nodename_pattern",
			class:      "Note Plus (mtb)",
			wantPackID: "comfy-mtb",
			wantSource: SourcePattern,
		},
		{
			name:       "unenumerated class via a prefix pattern",
			class:      "DF_Float_to_integer",
			wantPackID: "derfuu_comfyui_moddednodes",
			wantSource: SourcePattern,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			packs, unattributed := ix.Attribute([]string{tc.class})
			if len(unattributed) != 0 {
				t.Fatalf("class %q came back unattributed: %v", tc.class, unattributed)
			}
			got, ok := packByID(packs, tc.wantPackID)
			if !ok {
				t.Fatalf("class %q did not resolve to %q; got %+v", tc.class, tc.wantPackID, packs)
			}
			if got.Source != tc.wantSource {
				t.Errorf("Source = %q, want %q", got.Source, tc.wantSource)
			}
			if len(got.Classes) != 1 || got.Classes[0] != tc.class {
				t.Errorf("Classes = %v, want [%q]", got.Classes, tc.class)
			}
			if got.Repository == "" {
				t.Error("Repository is empty — it is the manual-install answer and must always be shown")
			}
		})
	}
}

// TestURLKeyJoinIsRequired proves the URL leg is load-bearing rather than
// incidental: the mapping key is a URL, the pack key is a bare id, and they only
// meet through the pack's repository/files entries.
func TestURLKeyJoinIsRequired(t *testing.T) {
	mappings := loadFixture(t, "nodepack_getmappings.json")

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(mappings, &raw); err != nil {
		t.Fatal(err)
	}
	const urlKey = "https://github.com/GACLove/ComfyUI-VFI"
	if _, ok := raw[urlKey]; !ok {
		t.Fatalf("fixture lost its URL mapping key %q — the two-way join is no longer covered", urlKey)
	}

	ix := realIndex(t)
	if _, ok := ix.packs[urlKey]; ok {
		t.Fatal("the URL key must NOT be a node_packs key; otherwise this test proves nothing")
	}

	packs, unattributed := ix.Attribute([]string{"RIFEInterpolation"})
	if len(unattributed) != 0 || len(packs) != 1 {
		t.Fatalf("URL-key join failed: packs=%+v unattributed=%v", packs, unattributed)
	}
	if packs[0].ID != "ComfyUI-VFI" {
		t.Errorf("ID = %q, want ComfyUI-VFI", packs[0].ID)
	}
	if !strings.Contains(packs[0].Repository, "GACLove/ComfyUI-VFI") {
		t.Errorf("Repository = %q", packs[0].Repository)
	}
}

// TestInstallableRules pins the CNR-release gate. 1178 of 7358 live packs are
// nightly-only and ComfyUI-Manager REFUSES them at its default policy, so a
// false Installable must always carry a plain user-facing Reason.
func TestInstallableRules(t *testing.T) {
	ix := realIndex(t)

	tests := []struct {
		name            string
		class           string
		wantPackID      string
		wantInstallable bool
		reasonContains  string
	}{
		{
			name:            "CNR-released pack is installable with no reason",
			class:           "Pick From Batch (mtb)",
			wantPackID:      "comfy-mtb",
			wantInstallable: true,
		},
		{
			name:            "nightly-only pack is NOT installable and says why",
			class:           "CR Float To Integer",
			wantPackID:      "ComfyUI_Comfyroll_CustomNodes",
			wantInstallable: false,
			reasonContains:  "nightly",
		},
		{
			name:            "pack with no cnr_latest at all is NOT installable",
			class:           "RIFEInterpolation",
			wantPackID:      "ComfyUI-VFI",
			wantInstallable: false,
			reasonContains:  "Comfy Registry release",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			packs, _ := ix.Attribute([]string{tc.class})
			got, ok := packByID(packs, tc.wantPackID)
			if !ok {
				t.Fatalf("no pack %q in %+v", tc.wantPackID, packs)
			}
			if got.Installable != tc.wantInstallable {
				t.Fatalf("Installable = %v, want %v (version %q)", got.Installable, tc.wantInstallable, got.Version)
			}
			if tc.wantInstallable {
				if got.Reason != "" {
					t.Errorf("Reason must be empty when installable, got %q", got.Reason)
				}
				return
			}
			if !strings.Contains(got.Reason, tc.reasonContains) {
				t.Errorf("Reason = %q, want it to mention %q", got.Reason, tc.reasonContains)
			}
			if !strings.Contains(strings.ToLower(got.Reason), "hand") {
				t.Errorf("Reason must point at the manual path, got %q", got.Reason)
			}
		})
	}
}

// TestCatchAllPatternDiscarded is the 🔴 guard. The real index carries a `.*`
// nodename_pattern (Anomalous_Model_Browser) which matches EVERY class name;
// applied naively it attributes every unresolvable node in every workflow to that
// one pack — strictly worse than reporting "unattributed".
func TestCatchAllPatternDiscarded(t *testing.T) {
	// The fixture must still contain the catch-all, else the guard is untested.
	var raw map[string][]json.RawMessage
	if err := json.Unmarshal(loadFixture(t, "nodepack_getmappings.json"), &raw); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range raw {
		if len(entry) < 2 {
			continue
		}
		var meta mappingMeta
		_ = json.Unmarshal(entry[1], &meta)
		if meta.Pattern == ".*" {
			found = true
		}
	}
	if !found {
		t.Fatal("fixture no longer carries the real `.*` catch-all pattern — the guard is untested")
	}

	ix := realIndex(t)
	for _, p := range ix.patterns {
		if p.re.String() == ".*" {
			t.Fatalf("catch-all pattern survived compilation for key %q", p.key)
		}
	}

	// A nonsense class must land in unattributed, NOT be attributed to anything.
	packs, unattributed := ix.Attribute([]string{"ZZZ_unrelated_class_9f3"})
	if len(packs) != 0 {
		t.Errorf("nonsense class was attributed to %+v — the catch-all leaked", packs)
	}
	if len(unattributed) != 1 || unattributed[0] != "ZZZ_unrelated_class_9f3" {
		t.Errorf("unattributed = %v, want the nonsense class", unattributed)
	}
}

// TestCatchAllGuardIsEmpirical proves the guard is a probe, not a `.*` string
// blacklist: other spellings of "matches everything" are discarded too, while a
// narrow real pattern survives.
func TestCatchAllGuardIsEmpirical(t *testing.T) {
	tests := []struct {
		pattern string
		keep    bool
	}{
		{`.*`, false},
		{`.+`, false},
		{`(?s).*`, false},
		{``, false},  // empty compiles and matches everything
		{`^`, false}, // matches at the start of every string
		{`[\s\S]*`, false},
		{`\(mtb\)$`, true}, // the real MTB rule
		{`^DF_`, true},     // the real Derfuu rule
		{`^Z`, true},       // narrow, but shares a prefix with one probe
		{`(?P<x>`, false},  // uncompilable: skipped, never fatal
		{`a(?!b)`, false},  // RE2 rejects lookahead: skipped, never fatal
	}
	for _, tc := range tests {
		t.Run(tc.pattern, func(t *testing.T) {
			got := compileSafePattern(tc.pattern)
			if tc.keep && got == nil {
				t.Fatalf("pattern %q was discarded but should be kept", tc.pattern)
			}
			if !tc.keep && got != nil {
				t.Fatalf("pattern %q was kept but should be discarded", tc.pattern)
			}
		})
	}
}

// TestInvalidPatternIsSkippedNotFatal: an uncompilable regex in a hostile/broken
// index must cost only that rule.
func TestInvalidPatternIsSkippedNotFatal(t *testing.T) {
	mappings := json.RawMessage(`{
	  "good-pack": [["RealClass"], {"title_aux":"Good"}],
	  "broken-pack": [[], {"title_aux":"Broken","nodename_pattern":"a(?!b)"}],
	  "mtb-like":   [[], {"title_aux":"MTBish","nodename_pattern":"\\(zz\\)$"}]
	}`)
	ix, err := BuildIndex(mappings, nil)
	if err != nil {
		t.Fatalf("BuildIndex must not fail on an uncompilable pattern: %v", err)
	}
	if len(ix.patterns) != 1 || ix.patterns[0].key != "mtb-like" {
		t.Fatalf("patterns = %+v, want only the compilable rule", ix.patterns)
	}
	packs, unattributed := ix.Attribute([]string{"RealClass", "Thing (zz)"})
	if len(unattributed) != 0 {
		t.Fatalf("unattributed = %v", unattributed)
	}
	if len(packs) != 2 {
		t.Fatalf("packs = %+v, want 2", packs)
	}
}

// TestMultiPackAmbiguity: a class claimed by several packs must return them ALL.
// PreViewVideo is claimed by 16 packs in the live index; the fixture keeps three.
func TestMultiPackAmbiguity(t *testing.T) {
	ix := realIndex(t)
	packs, unattributed := ix.Attribute([]string{"PreViewVideo"})
	if len(unattributed) != 0 {
		t.Fatalf("unattributed = %v", unattributed)
	}
	if len(packs) < 3 {
		t.Fatalf("PreViewVideo resolved to %d pack(s); ambiguity was collapsed: %+v", len(packs), packs)
	}
	for _, p := range packs {
		if len(p.Classes) != 1 || p.Classes[0] != "PreViewVideo" {
			t.Errorf("pack %q Classes = %v", p.ID, p.Classes)
		}
	}
}

// TestAttributeIsDeterministic: repeated runs over shuffled input give byte-equal
// output (Go map iteration is randomized, so an unsorted implementation flaps).
func TestAttributeIsDeterministic(t *testing.T) {
	ix := realIndex(t)
	inputs := [][]string{
		{"PreViewVideo", "CR Float To Integer", "Note Plus (mtb)", "MMAudioSampler", "RIFEInterpolation"},
		{"MMAudioSampler", "RIFEInterpolation", "Note Plus (mtb)", "PreViewVideo", "CR Float To Integer"},
		{"CR Float To Integer", "PreViewVideo", "RIFEInterpolation", "MMAudioSampler", "Note Plus (mtb)"},
	}
	var want string
	for i, in := range inputs {
		packs, unattributed := ix.Attribute(in)
		b, err := json.Marshal(struct {
			P []Pack
			U []string
		}{packs, unattributed})
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			want = string(b)
			continue
		}
		if string(b) != want {
			t.Fatalf("ordering is input-dependent:\n%s\nvs\n%s", b, want)
		}
	}
	// Duplicates collapse and the order the caller passed does not leak through.
	packs, _ := ix.Attribute([]string{"Note Plus (mtb)", "Note Plus (mtb)"})
	for _, p := range packs {
		if len(p.Classes) != 1 {
			t.Errorf("duplicate class was not deduped: %v", p.Classes)
		}
	}
}

// TestMapRungWinsOverPatternRung: the pattern rung only runs for classes the
// enumerated rung could not place. "Pick From Batch (mtb)" is enumerated AND
// matches `\(mtb\)$` — it must be reported once, at map confidence.
func TestMapRungWinsOverPatternRung(t *testing.T) {
	ix := realIndex(t)
	packs, _ := ix.Attribute([]string{"Pick From Batch (mtb)"})
	if len(packs) != 1 {
		t.Fatalf("packs = %+v, want exactly one", packs)
	}
	if packs[0].Source != SourceMap {
		t.Errorf("Source = %q, want %q (the pattern rung must not shadow an exact hit)", packs[0].Source, SourceMap)
	}
}

// TestMalformedJSONDegradesSafely: this data is UNTRUSTED. Every hostile shape
// must yield a usable empty index and an error, never a panic and never a nil
// index a caller could dereference.
func TestMalformedJSONDegradesSafely(t *testing.T) {
	tests := []struct {
		name     string
		mappings string
		getlist  string
		wantErr  bool
	}{
		{"both empty", ``, ``, false},
		{"both null", `null`, `null`, false},
		{"mappings truncated", `{"a": [["X"],`, `{}`, true},
		{"getlist truncated", `{}`, `{"node_packs":`, true},
		{"mappings is an array", `[1,2,3]`, `{}`, true},
		{"getlist is a string", `{}`, `"nope"`, true},
		{"mapping value is a scalar", `{"a": 7}`, `{}`, true},
		{"mapping classes are objects", `{"a": [{"not":"a list"}, {"title_aux":"T"}]}`, `{}`, false},
		{"pack entry is a scalar", `{}`, `{"node_packs":{"p": 5}}`, true},
		{"deeply nested garbage", `{"a":[[["nested"]],{"title_aux":1}]}`, `{}`, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ix, err := BuildIndex(json.RawMessage(tc.mappings), json.RawMessage(tc.getlist))
			if ix == nil {
				t.Fatal("BuildIndex returned a nil index — callers must never nil-panic on hostile data")
			}
			if tc.wantErr && err == nil {
				t.Errorf("expected an error for %s", tc.name)
			}
			// Whatever happened, the index must still answer without panicking.
			packs, unattributed := ix.Attribute([]string{"AnyClass", ""})
			_ = packs
			if len(unattributed) != 1 || unattributed[0] != "AnyClass" {
				t.Errorf("unattributed = %v, want [AnyClass]", unattributed)
			}
		})
	}

	// A nil index must behave too (a caller may hold one after a failed fetch).
	var nilIX *NodePackIndex
	packs, unattributed := nilIX.Attribute([]string{"B", "A"})
	if len(packs) != 0 || len(unattributed) != 2 || unattributed[0] != "A" {
		t.Fatalf("nil index: packs=%v unattributed=%v", packs, unattributed)
	}
}

// TestBuildIndexWithoutGetlist: getlist is GONE in ComfyUI-Manager V4's default
// mode. Attribution must still work from getmappings alone, with Installable
// false everywhere (only getlist carries cnr_latest).
func TestBuildIndexWithoutGetlist(t *testing.T) {
	ix, err := BuildIndex(loadFixture(t, "nodepack_getmappings.json"), nil)
	if err != nil {
		t.Fatalf("BuildIndex without getlist: %v", err)
	}
	packs, unattributed := ix.Attribute([]string{"Pick From Batch (mtb)"})
	if len(unattributed) != 0 || len(packs) != 1 {
		t.Fatalf("packs=%+v unattributed=%v", packs, unattributed)
	}
	if packs[0].Installable {
		t.Error("Installable must be false without getlist — cnr_latest is unknowable")
	}
	if packs[0].Reason == "" {
		t.Error("a non-installable pack must always carry a Reason")
	}
	if packs[0].Title == "" {
		t.Error("Title must fall back to the mapping metadata")
	}
}

// TestNormalizeRepoURL pins the URL-join canonicalization, which is what lets the
// real document's http/https, www, .git and trailing-slash variants meet.
func TestNormalizeRepoURL(t *testing.T) {
	tests := []struct{ in, want string }{
		{"https://github.com/GACLove/ComfyUI-VFI", "github.com/gaclove/comfyui-vfi"},
		{"http://github.com/GACLove/ComfyUI-VFI/", "github.com/gaclove/comfyui-vfi"},
		{"https://www.github.com/GACLove/ComfyUI-VFI.git", "github.com/gaclove/comfyui-vfi"},
		{"  https://github.com/GACLove/ComfyUI-VFI  ", "github.com/gaclove/comfyui-vfi"},
		{"comfy-mtb", ""},              // a bare pack id must never look like a URL
		{"git@github.com:a/b.git", ""}, // non-http scheme
		{"", ""},
	}
	for _, tc := range tests {
		if got := normalizeRepoURL(tc.in); got != tc.want {
			t.Errorf("normalizeRepoURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestMergePacksUnionsRungs: the static map and the Registry are COMPLEMENTARY
// (live: map 3/7, registry 4/7, union 6/7), so merging must union rather than
// rank — and must not double-list a pack both rungs found.
func TestMergePacksUnionsRungs(t *testing.T) {
	fromMap := []Pack{
		{ID: "comfy-mtb", Title: "comfy-mtb", Repository: "https://github.com/melMass/comfy_mtb", Installable: true, Classes: []string{"Pick From Batch (mtb)"}, Source: SourceMap},
		{ID: "ComfyUI_Comfyroll_CustomNodes", Title: "Comfyroll Studio", Repository: "https://github.com/Suzie1/ComfyUI_Comfyroll_CustomNodes", Reason: "nightly only", Classes: []string{"CR Float To Integer"}, Source: SourceMap},
	}
	fromRegistry := []Pack{
		// Same pack, different rung + a different class: must merge into one.
		{ID: "comfy-mtb", Title: "comfy-mtb", Classes: []string{"Batch Float (mtb)"}, Source: SourceRegistry},
		{ID: "comfyui-mmaudio", Title: "comfyui-mmaudio", Classes: []string{"MMAudioSampler", "MMAudioModelLoader"}, Source: SourceRegistry},
	}
	fromPattern := []Pack{
		{ID: "comfy-mtb", Title: "comfy-mtb", Classes: []string{"Note Plus (mtb)"}, Source: SourcePattern},
	}

	got := MergePacks(fromMap, fromRegistry, fromPattern)
	if len(got) != 3 {
		t.Fatalf("got %d packs, want 3: %+v", len(got), got)
	}
	mtb, ok := packByID(got, "comfy-mtb")
	if !ok {
		t.Fatal("comfy-mtb missing")
	}
	if len(mtb.Classes) != 3 {
		t.Errorf("comfy-mtb Classes = %v, want all three unioned", mtb.Classes)
	}
	if mtb.Source != SourceMap {
		t.Errorf("Source = %q, want the strongest rung %q", mtb.Source, SourceMap)
	}
	if !mtb.Installable || mtb.Reason != "" {
		t.Errorf("Installable=%v Reason=%q — a proven-installable pack must stay installable", mtb.Installable, mtb.Reason)
	}
	// Confidence ordering: map rungs first, pattern rungs last.
	if got[0].Source != SourceMap {
		t.Errorf("first pack Source = %q, want map first", got[0].Source)
	}
	if last := got[len(got)-1]; sourceRank[last.Source] < sourceRank[got[0].Source] {
		t.Errorf("packs are not confidence-ordered: %+v", got)
	}
	// Merging is deterministic regardless of set order.
	again := MergePacks(fromPattern, fromRegistry, fromMap)
	a, _ := json.Marshal(got)
	b, _ := json.Marshal(again)
	if string(a) != string(b) {
		t.Errorf("MergePacks is order-dependent:\n%s\nvs\n%s", a, b)
	}
}

// TestMergePacksIdentityFallsBackToURL: a pack with no id (3784 of 7358 live
// entries have none) still merges by its repository URL rather than duplicating.
func TestMergePacksIdentityFallsBackToURL(t *testing.T) {
	a := []Pack{{Title: "ComfyUI-VFI", Repository: "https://github.com/GACLove/ComfyUI-VFI", Classes: []string{"RIFEInterpolation"}, Source: SourceMap}}
	b := []Pack{{Title: "ComfyUI-VFI", Repository: "http://github.com/GACLove/ComfyUI-VFI.git", Classes: []string{"CalculateLoadedFPS"}, Source: SourceRegistry}}
	got := MergePacks(a, b)
	if len(got) != 1 {
		t.Fatalf("got %d packs, want 1 (URL identity failed): %+v", len(got), got)
	}
	if len(got[0].Classes) != 2 {
		t.Errorf("Classes = %v, want both", got[0].Classes)
	}
}

// TestMergePacksBridgesIDAndURLIdentity is a LIVE-CAUGHT regression. Merging the
// real static extension-node-map (whose entries have NO pack id — the file is
// keyed entirely by URL) with the real Registry result (which has an id) produced
// comfy-mtb TWICE, because an id-first-else-url identity put them in separate
// buckets. Two entries must merge when they share ANY identity key.
func TestMergePacksBridgesIDAndURLIdentity(t *testing.T) {
	// Exactly the live shapes: static index -> URL only; Registry -> id + URL.
	static := []Pack{{
		Title:      "MTB Nodes",
		Repository: "https://github.com/melMass/comfy_mtb",
		Classes:    []string{"Pick From Batch (mtb)"},
		Source:     SourceMap,
	}}
	registry := []Pack{{
		ID:         "comfy-mtb",
		Title:      "comfy-mtb",
		Repository: "https://github.com/melMass/comfy_mtb",
		Version:    "0.5.4",
		Classes:    []string{"Note Plus (mtb)"},
		Source:     SourceRegistry,
	}}
	got := MergePacks(static, registry)
	if len(got) != 1 {
		t.Fatalf("got %d packs, want 1 — the same pack was double-listed: %+v", len(got), got)
	}
	if got[0].ID != "comfy-mtb" {
		t.Errorf("ID = %q, want the id contributed by the Registry rung", got[0].ID)
	}
	if len(got[0].Classes) != 2 {
		t.Errorf("Classes = %v, want both unioned", got[0].Classes)
	}
	if got[0].Source != SourceMap {
		t.Errorf("Source = %q, want the strongest rung", got[0].Source)
	}
}

// TestMergePacksCollapsesBridgedGroups: an entry carrying BOTH an id and a URL
// that already belong to two separate groups must collapse them, not join one and
// orphan the other.
func TestMergePacksCollapsesBridgedGroups(t *testing.T) {
	idOnly := []Pack{{ID: "comfy-mtb", Title: "comfy-mtb", Classes: []string{"A"}, Source: SourceRegistry}}
	urlOnly := []Pack{{Title: "MTB", Repository: "https://github.com/melMass/comfy_mtb", Classes: []string{"B"}, Source: SourcePattern}}
	bridge := []Pack{{ID: "comfy-mtb", Repository: "https://github.com/melMass/comfy_mtb", Classes: []string{"C"}, Installable: true, Source: SourceMap}}

	got := MergePacks(idOnly, urlOnly, bridge)
	if len(got) != 1 {
		t.Fatalf("got %d packs, want 1: %+v", len(got), got)
	}
	if len(got[0].Classes) != 3 {
		t.Errorf("Classes = %v, want A, B and C", got[0].Classes)
	}
	if !got[0].Installable {
		t.Error("a proven-installable contribution must win")
	}
	if got[0].Source != SourceMap {
		t.Errorf("Source = %q, want the strongest rung", got[0].Source)
	}
}

// TestPatternMatchingIsUnanchored pins the semantics the real `\(mtb\)$` rule
// needs. ComfyUI-Manager's own attribution (manager_core.py) uses re.search;
// Go's regexp.MatchString matches that. An anchored implementation silently loses
// every suffix rule.
func TestPatternMatchingIsUnanchored(t *testing.T) {
	re := regexp.MustCompile(`\(mtb\)$`)
	if !re.MatchString("Note Plus (mtb)") {
		t.Fatal("suffix pattern must match a class it does not prefix")
	}
	ix := realIndex(t)
	packs, unattributed := ix.Attribute([]string{"Some Brand New Node (mtb)"})
	if len(unattributed) != 0 {
		t.Fatalf("suffix pattern rung did not fire: %v", unattributed)
	}
	if len(packs) != 1 || packs[0].ID != "comfy-mtb" || packs[0].Source != SourcePattern {
		t.Fatalf("packs = %+v", packs)
	}
}
