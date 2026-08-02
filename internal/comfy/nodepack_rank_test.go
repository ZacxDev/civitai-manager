package comfy

import (
	"encoding/json"
	"testing"
)

// The measured ground case, reduced to a fixture.
//
// `UltimateSDUpscale` is claimed by TWO packs in ComfyUI-Manager's live index
// (V3.41, getmappings?mode=cache, 5573 repos):
//
//	comfyui-promptchain        93 classes — a grab-bag that happens to list it
//	comfyui_ultimatesdupscale   4 classes — its whole surface is UltimateSDUpscale*
//
// Alphabetical title order put the 93-class pack FIRST, next to an equally
// prominent Install button. Installing it writes 93 unrelated node types into the
// user's ComfyUI — a real side effect, reachable for the first time because the
// conversion-abort fix made this panel render at all.
//
// The fixture keeps the real class COUNTS (93 vs 4) and the real titles, because
// both the scope ratio and the alphabetical-order trap depend on them: "ComfyUI-
// PromptChain" < "ComfyUI_UltimateSDUpscale" byte-wise, so a regression to
// title-order sorting is caught rather than accidentally passing.
const rankMappingsFixture = `{
  "comfyui-promptchain": [
    ["UltimateSDUpscale","ApplyPulid","ApplyFirstBlockCachePatch","ApplyPuLIDFlux2",
     "N05","N06","N07","N08","N09","N10","N11","N12","N13","N14","N15","N16","N17","N18","N19","N20",
     "N21","N22","N23","N24","N25","N26","N27","N28","N29","N30","N31","N32","N33","N34","N35","N36",
     "N37","N38","N39","N40","N41","N42","N43","N44","N45","N46","N47","N48","N49","N50","N51","N52",
     "N53","N54","N55","N56","N57","N58","N59","N60","N61","N62","N63","N64","N65","N66","N67","N68",
     "N69","N70","N71","N72","N73","N74","N75","N76","N77","N78","N79","N80","N81","N82","N83","N84",
     "N85","N86","N87","N88","N89","N90","N91","N92","N93"],
    {"title_aux":"ComfyUI-PromptChain"}
  ],
  "comfyui_ultimatesdupscale": [
    ["UltimateSDUpscale","UltimateSDUpscaleCustomSample","UltimateSDUpscaleGuider","UltimateSDUpscaleNoUpscale"],
    {"title_aux":"ComfyUI_UltimateSDUpscale"}
  ]
}`

const rankGetlistFixture = `{"node_packs":{
  "comfyui-promptchain":{"id":"comfyui-promptchain","title":"ComfyUI-PromptChain",
    "repository":"https://github.com/mobcat40/ComfyUI-PromptChain","cnr_latest":"0.1.13"},
  "comfyui_ultimatesdupscale":{"id":"comfyui_ultimatesdupscale","title":"ComfyUI_UltimateSDUpscale",
    "repository":"https://github.com/ssitu/ComfyUI_UltimateSDUpscale","cnr_latest":"1.7.2"}
}}`

func rankIndex(t *testing.T) *NodePackIndex {
	t.Helper()
	ix, err := BuildIndex(json.RawMessage(rankMappingsFixture), json.RawMessage(rankGetlistFixture))
	if err != nil {
		t.Fatalf("BuildIndex: %v", err)
	}
	return ix
}

// TestTightlyScopedPackOutranksABroadOne is the ground-case guard: the 4-class
// pack must come FIRST, and the 93-class pack must still be present.
func TestTightlyScopedPackOutranksABroadOne(t *testing.T) {
	packs, unattributed := rankIndex(t).Attribute([]string{"UltimateSDUpscale"})

	if len(unattributed) != 0 {
		t.Fatalf("unattributed = %v, want none", unattributed)
	}
	// INTERMEDIATE STATE: the fixture really does produce a CONTEST. Without two
	// claimants the ordering assertion below would be vacuously true.
	if len(packs) != 2 {
		t.Fatalf("want 2 claimants (the contest this test exists for), got %d: %+v", len(packs), packs)
	}
	// And the scope signal really was measured — a 0 here means "unknown" and would
	// make compareScope fall through to the old alphabetical behaviour.
	byID := map[string]Pack{}
	for _, p := range packs {
		byID[p.ID] = p
	}
	if got := byID["comfyui_ultimatesdupscale"].ClaimedClasses; got != 4 {
		t.Fatalf("ssitu pack ClaimedClasses = %d, want 4 — the scope signal was not measured", got)
	}
	if got := byID["comfyui-promptchain"].ClaimedClasses; got != 93 {
		t.Fatalf("promptchain pack ClaimedClasses = %d, want 93 — the scope signal was not measured", got)
	}

	if packs[0].ID != "comfyui_ultimatesdupscale" {
		t.Errorf("the tightly-scoped pack must rank FIRST; got order [%s, %s]", packs[0].ID, packs[1].ID)
	}
	// 🔴 Ranking must never DROP a candidate — a wrong single answer is worse than
	// an ordered list.
	if packs[1].ID != "comfyui-promptchain" {
		t.Errorf("the broad pack must still be present as an alternative; got %q", packs[1].ID)
	}
}

// TestRankingBeatsAlphabeticalOrder states the regression explicitly: byte-wise
// "ComfyUI-PromptChain" sorts BEFORE "ComfyUI_UltimateSDUpscale" ('-' is 0x2D,
// '_' is 0x5F), so the pre-change title-ordered code put the wrong pack first.
// If this fixture ever stops encoding that trap the guard above proves nothing.
func TestRankingBeatsAlphabeticalOrder(t *testing.T) {
	if !("ComfyUI-PromptChain" < "ComfyUI_UltimateSDUpscale") {
		t.Fatal("fixture no longer encodes the alphabetical trap — the ranking guard would pass for the wrong reason")
	}
	packs, _ := rankIndex(t).Attribute([]string{"UltimateSDUpscale"})
	if len(packs) < 2 {
		t.Fatalf("want 2 claimants, got %d", len(packs))
	}
	if packs[0].Title != "ComfyUI_UltimateSDUpscale" {
		t.Errorf("ranking fell back to alphabetical title order; first = %q", packs[0].Title)
	}
}

// TestScopeIsAShareNotACount pins that the ranking compares the SHARE of each
// pack's own surface, not the raw number of matched classes. A broad pack that
// matches MORE of the requested classes in absolute terms must still lose to a
// pack that is almost entirely what you asked for.
func TestScopeIsAShareNotACount(t *testing.T) {
	// broad claims 100 classes and matches 2 of them (2%).
	// narrow claims 2 classes and matches 1 (50%).
	broad := Pack{ID: "broad", Title: "AAA Broad", Source: SourceMap,
		Classes: []string{"Alpha", "Beta"}, ClaimedClasses: 100}
	narrow := Pack{ID: "narrow", Title: "ZZZ Narrow", Source: SourceMap,
		Classes: []string{"Gamma"}, ClaimedClasses: 2}

	packs := []Pack{broad, narrow}
	sortPacks(packs)
	if packs[0].ID != "narrow" {
		t.Errorf("a 1-of-2 pack must outrank a 2-of-100 pack; got %q first", packs[0].ID)
	}
}

// TestUnknownScopeNeverOutranksAMeasuredPack pins the ClaimedClasses == 0 rule.
// The Comfy Registry answers per class and publishes no pack-wide class list, so
// its packs carry no surface. Scoring that absence as a ratio would make the
// LEAST-known pack look perfectly scoped and float it to the top.
func TestUnknownScopeNeverOutranksAMeasuredPack(t *testing.T) {
	unknown := Pack{ID: "unknown", Title: "AAA Unknown", Source: SourceMap,
		Classes: []string{"Alpha"}, ClaimedClasses: 0}
	measured := Pack{ID: "measured", Title: "ZZZ Measured", Source: SourceMap,
		Classes: []string{"Alpha"}, ClaimedClasses: 50}

	packs := []Pack{unknown, measured}
	sortPacks(packs)
	if packs[0].ID != "measured" {
		t.Errorf("a pack with an UNKNOWN surface must not outrank a measured one; got %q first", packs[0].ID)
	}
}

// TestConfidenceRungStillOutranksScope pins that the new signal did not displace
// the old one: a nodename_pattern GUESS must never beat an exact class-list hit,
// however tightly scoped the guess looks.
func TestConfidenceRungStillOutranksScope(t *testing.T) {
	perfectGuess := Pack{ID: "guess", Title: "AAA Guess", Source: SourcePattern,
		Classes: []string{"Alpha"}, ClaimedClasses: 1}
	broadExact := Pack{ID: "exact", Title: "ZZZ Exact", Source: SourceMap,
		Classes: []string{"Alpha"}, ClaimedClasses: 500}

	packs := []Pack{perfectGuess, broadExact}
	sortPacks(packs)
	if packs[0].ID != "exact" {
		t.Errorf("an exact class-list hit must outrank a pattern guess regardless of scope; got %q first", packs[0].ID)
	}
}

// TestNameAffinityBreaksAScopeTie pins the secondary signal — and pins that it is
// only a tie-break. Both packs here have an identical scope share, so affinity is
// the only thing left to decide it.
func TestNameAffinityBreaksAScopeTie(t *testing.T) {
	unrelated := Pack{ID: "aaa-unrelated", Title: "AAA Unrelated Pack", Source: SourceMap,
		Repository: "https://github.com/someone/AAA-Unrelated-Pack",
		Classes:    []string{"UltimateSDUpscale"}, ClaimedClasses: 4}
	named := Pack{ID: "zzz-named", Title: "ComfyUI_UltimateSDUpscale", Source: SourceMap,
		Repository: "https://github.com/ssitu/ComfyUI_UltimateSDUpscale",
		Classes:    []string{"UltimateSDUpscale"}, ClaimedClasses: 4}

	// INTERMEDIATE STATE: the scope key must genuinely TIE, or this would be a
	// scope test wearing an affinity label.
	if compareScope(unrelated, named) != 0 {
		t.Fatal("fixture does not tie on scope — affinity is not what is being measured")
	}
	if !hasNameAffinity(named) {
		t.Fatal("fixture pack is not name-affine; the folded comparison is not reaching it")
	}
	if hasNameAffinity(unrelated) {
		t.Fatal("control pack is unexpectedly name-affine")
	}

	packs := []Pack{unrelated, named}
	sortPacks(packs)
	if packs[0].ID != "zzz-named" {
		t.Errorf("the pack NAMED after the class must win a scope tie; got %q first", packs[0].ID)
	}
}

// TestScopeOutranksNameAffinity pins the PRECEDENCE between the two new signals,
// which the tie-break test above cannot see (there, scope ties by construction).
//
// 🔴 This is the guard that keeps the fix a fix. A pack name is the author's choice
// of words and is trivially gamed — "ComfyUI-UltimateSDUpscale-And-92-Other-Things"
// is a legal repo name. If affinity were allowed to outrank scope, a broad grab-bag
// that merely NAMES itself after the class would be promoted back to first place
// with a filled Install button, which is exactly the failure this work removed.
// Scope measures what the pack IS; affinity only breaks ties.
func TestScopeOutranksNameAffinity(t *testing.T) {
	// Named after the class, but a 1-of-93 grab-bag.
	broadButNamed := Pack{ID: "broad-named", Title: "ComfyUI-UltimateSDUpscale-Extras",
		Repository: "https://github.com/someone/ComfyUI-UltimateSDUpscale-Extras",
		Source:     SourceMap,
		Classes:    []string{"UltimateSDUpscale"}, ClaimedClasses: 93}
	// Unrelated name, but almost entirely what was asked for.
	tightButUnnamed := Pack{ID: "tight-unnamed", Title: "Zebra Tools",
		Repository: "https://github.com/someone/zebra-tools",
		Source:     SourceMap,
		Classes:    []string{"UltimateSDUpscale"}, ClaimedClasses: 2}

	// INTERMEDIATE STATE: the two signals must genuinely DISAGREE here, or the
	// assertion proves nothing about precedence.
	if !hasNameAffinity(broadButNamed) {
		t.Fatal("fixture broken: the broad pack is meant to be name-affine")
	}
	if hasNameAffinity(tightButUnnamed) {
		t.Fatal("fixture broken: the tight pack is meant NOT to be name-affine")
	}
	if compareScope(broadButNamed, tightButUnnamed) <= 0 {
		t.Fatal("fixture broken: the tight pack is meant to win on scope")
	}

	packs := []Pack{broadButNamed, tightButUnnamed}
	sortPacks(packs)
	if packs[0].ID != "tight-unnamed" {
		t.Errorf("SCOPE must outrank name affinity — a grab-bag named after the class "+
			"must not be promoted; got %q first", packs[0].ID)
	}
}

// TestClaimedClassesCountsDistinctClasses pins that a duplicated entry in the
// third-party list cannot inflate a pack's apparent breadth — which would wrongly
// DEMOTE it.
func TestClaimedClassesCountsDistinctClasses(t *testing.T) {
	mappings := `{"dupes":[["Alpha","Alpha","Beta"],{"title_aux":"Dupes"}]}`
	ix, err := BuildIndex(json.RawMessage(mappings), nil)
	if err != nil {
		t.Fatalf("BuildIndex: %v", err)
	}
	packs, _ := ix.Attribute([]string{"Alpha"})
	if len(packs) != 1 {
		t.Fatalf("want 1 pack, got %d", len(packs))
	}
	if packs[0].ClaimedClasses != 2 {
		t.Errorf("ClaimedClasses = %d, want 2 distinct (Alpha, Beta)", packs[0].ClaimedClasses)
	}
}

// TestMergePacksKeepsTheKnownScope pins that folding a Registry-rung entry (which
// knows no surface) into a Manager-rung one does not erase the measured count.
func TestMergePacksKeepsTheKnownScope(t *testing.T) {
	fromMap := []Pack{{ID: "p", Title: "P", Repository: "https://github.com/a/p",
		Source: SourceMap, Classes: []string{"Alpha"}, ClaimedClasses: 4}}
	fromRegistry := []Pack{{ID: "p", Title: "P", Repository: "https://github.com/a/p",
		Source: SourceRegistry, Classes: []string{"Beta"}, ClaimedClasses: 0}}

	merged := MergePacks(fromMap, fromRegistry)
	if len(merged) != 1 {
		t.Fatalf("want 1 merged pack, got %d", len(merged))
	}
	if merged[0].ClaimedClasses != 4 {
		t.Errorf("merged ClaimedClasses = %d, want 4 — the measured surface was lost", merged[0].ClaimedClasses)
	}
}

// TestScopeDecidesWhenAffinityFavoursTheWrongPack is the ground case the two
// tests above CANNOT carry, and the reason this file needed a third.
//
// 🔴 In the real fixture the tightly-scoped winner (`comfyui_ultimatesdupscale`)
// also wins on NAME AFFINITY, so its ordering is over-determined: an audit
// measured that deleting `compareScope` from `sortPacks` entirely leaves both
// ground-case tests GREEN. They assert the 4-vs-93 counts, but the counts are not
// what the ordering rests on — affinity alone reproduces it. That is CLAUDE.md's
// "true for an incidental reason" mode: the assertion is correct, and it is
// correct for a reason the test does not name.
//
// Here the signals are deliberately OPPOSED. The broad pack is the one NAMED
// after the class, and the tight pack is not — so only scope can produce the
// right order, and removing scope flips it. This is what makes "scope is primary,
// affinity is secondary" a tested property rather than a comment.
//
// It matters because affinity is the gameable signal: `ComfyUI-FooNode-And-92-
// Other-Things` is a legal repository name, and the whole point of ranking is to
// resist exactly that.
func TestScopeDecidesWhenAffinityFavoursTheWrongPack(t *testing.T) {
	const mappings = `{
      "comfyui-foonode": [
        ["FooNode","A1","A2","A3","A4","A5","A6","A7","A8","A9"],
        {"title_aux":"ComfyUI-FooNode"}
      ],
      "tight-pack": [
        ["FooNode","B1"],
        {"title_aux":"tight-pack"}
      ]
    }`
	// The getlist half is what populates Pack.ID/Title/Repository — without it the
	// packs come back unidentified and the byID lookup below silently reads a zero
	// value (which is how the first draft of this test "failed" its own precondition).
	const getlist = `{"node_packs":{
      "comfyui-foonode":{"id":"comfyui-foonode","title":"ComfyUI-FooNode",
        "repository":"https://github.com/someone/ComfyUI-FooNode","cnr_latest":"1.0.0"},
      "tight-pack":{"id":"tight-pack","title":"tight-pack",
        "repository":"https://github.com/other/tight-pack","cnr_latest":"1.0.0"}
    }}`
	ix, err := BuildIndex(json.RawMessage(mappings), json.RawMessage(getlist))
	if err != nil {
		t.Fatalf("BuildIndex: %v", err)
	}
	packs, unattributed := ix.Attribute([]string{"FooNode"})
	if len(unattributed) != 0 {
		t.Fatalf("unattributed = %v, want none", unattributed)
	}

	// INTERMEDIATE STATE — without all three of these the ordering assertion below
	// would not be testing what its name says.
	if len(packs) != 2 {
		t.Fatalf("want 2 claimants (the contest), got %d: %+v", len(packs), packs)
	}
	byID := map[string]Pack{}
	for _, p := range packs {
		byID[p.ID] = p
	}
	broad, tight := byID["comfyui-foonode"], byID["tight-pack"]
	if broad.ClaimedClasses != 10 || tight.ClaimedClasses != 2 {
		t.Fatalf("scope signal not measured: broad=%d tight=%d, want 10 and 2",
			broad.ClaimedClasses, tight.ClaimedClasses)
	}
	// The affinity signal must genuinely point at the WRONG pack, or this test
	// degenerates into the over-determined ones above.
	if !hasNameAffinity(broad) {
		t.Fatal("fixture precondition: the BROAD pack must be the name-matching one")
	}
	if hasNameAffinity(tight) {
		t.Fatal("fixture precondition: the TIGHT pack must NOT match by name")
	}

	if packs[0].ID != "tight-pack" {
		t.Errorf("scope must decide when affinity points the other way: got order [%s, %s]. "+
			"Name affinity is the gameable signal — a pack can call itself anything — so it "+
			"must never outrank how much of the pack is actually what you need.",
			packs[0].ID, packs[1].ID)
	}
}
