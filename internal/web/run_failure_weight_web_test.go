package web

import (
	"strings"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/comfy"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// contestedNodesSnapshot reproduces the operator's measured workflow-590 state: a
// preflight failure with missing model files AND one missing node type claimed by
// TWO packs — the tight, correct one and the sprawling alternative.
//
// The two packs' signals are deliberately OPPOSED in the way that matters here:
// the winner is the one with the SMALLER surface (4 claimed classes vs 93), so a
// collapse keyed on anything but the ranking would pick the wrong one.
func contestedNodesSnapshot() runSnapshot {
	return runSnapshot{
		Started: true, WorkflowID: 7, Phase: runPhaseFailed,
		Message:         "Preflight failed.",
		GraphIncomplete: true,
		Preflight: &comfy.PreflightReport{
			MissingModels: []string{"upscaler.pth"},
			MissingNodes:  []string{"UltimateSDUpscale"},
		},
		MissingModels: []comfy.MissingModel{
			{Filename: "upscaler.pth", Query: "upscaler", CivitaiType: "Upscaler"},
		},
		MissingResolved: map[string]missingResolution{},
		LibMeta:         map[string]store.LocalModelMeta{},
		NodeAttr: nodeAttribution{
			ManagerPresent: true,
			RemoteLookup:   true,
			Packs: []comfy.Pack{
				{ID: "ultimate", Title: "ComfyUI_UltimateSDUpscale",
					Repository: "https://github.com/ssitu/ComfyUI_UltimateSDUpscale",
					Classes:    []string{"UltimateSDUpscale"}, ClaimedClasses: 4,
					Source: comfy.SourceMap, Installable: true},
				{ID: "promptchain", Title: "ComfyUI-PromptChain",
					Repository: "https://github.com/mobcat40/ComfyUI-PromptChain",
					Classes:    []string{"UltimateSDUpscale"}, ClaimedClasses: 93,
					Source: comfy.SourceMap, Installable: true},
			},
		},
	}
}

// TestAlternativePackIsCollapsedButStillReachable is the guard for the runner-up
// collapse. Both halves are load-bearing and are asserted separately:
//
//	COLLAPSED  — it must not render as a peer of the best match;
//	REACHABLE  — it must still be present, named, and installable.
//
// The ranking is a heuristic over a third-party index, so hiding the alternative
// outright would make a guess authoritative.
func TestAlternativePackIsCollapsedButStillReachable(t *testing.T) {
	body := renderString(t, runStatusFragment(contestedNodesSnapshot(), 7, "tok", true, fullMaturityRange()))

	// PRECONDITION: the fixture really did produce a contest, and the app ranked the
	// tight pack first. Without this the rest is green on a panel with no contest.
	if !strings.Contains(body, "More than one pack claims UltimateSDUpscale") {
		t.Fatalf("precondition: the fixture must produce a contested class:\n%s", body)
	}
	// 🔴 The precondition checks only the WINNER's badge. It deliberately does NOT
	// check "Also claims it": that badge renders on the alternative's own card, so
	// including it here would make DELETING the alternative fail at the precondition
	// instead of at the reachability assertion below — red for the wrong reason, and
	// indistinguishable from a broken fixture. Verified: with the alternatives
	// dropped, the reachability block is what reports it.
	if !strings.Contains(body, "Best match") {
		t.Fatalf("precondition: the fixture must produce a ranked winner:\n%s", body)
	}

	// REACHABLE — present, named, badged, and its Install button intact.
	for _, want := range []string{
		"ComfyUI-PromptChain",
		"https://github.com/mobcat40/ComfyUI-PromptChain",
		"Install ComfyUI-PromptChain",
		"Also claims it",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the alternative pack must stay reachable, missing %q:\n%s", want, body)
		}
	}

	// COLLAPSED — inside a <details>, and the summary names it so the disclosure is
	// not a mystery box.
	summary := "Other pack claiming the same node: ComfyUI-PromptChain"
	if !strings.Contains(body, summary) {
		t.Errorf("the alternative must sit behind a summary naming it (%q):\n%s", summary, body)
	}
	alt := strings.Index(body, "Install ComfyUI-PromptChain")
	det := strings.Index(body, summary)
	if det < 0 || alt < 0 || det > alt {
		t.Fatalf("the alternative must render AFTER its summary (summary=%d alt=%d)", det, alt)
	}
	// The BEST match must NOT be behind that disclosure — it is the answer.
	best := strings.Index(body, "Install ComfyUI_UltimateSDUpscale")
	if best < 0 {
		t.Fatalf("the best match must stay expanded:\n%s", body)
	}
	if best > det {
		t.Errorf("the best match must render BEFORE the alternatives disclosure (best=%d summary=%d)", best, det)
	}
}

// mixedClaimPacks is the fixture for the case that shipped broken: pack Y LOSES the
// contest for UltimateSDUpscale to the tighter pack X, and is simultaneously the
// ONLY claimant of SoloNode. Y is therefore REQUIRED, not an alternative.
//
// 🔴 The two classes are load-bearing and so is the order. A one-class-each fixture
// cannot express this at all — the bug needs a single pack to hold both a losing
// claim and a sole claim, which is precisely what TestUncontestedPacksAreNeverCollapsed
// (three packs, one disjoint class each, zero contests) structurally cannot reach.
func mixedClaimPacks() []comfy.Pack {
	return []comfy.Pack{
		{ID: "ultimate", Title: "ComfyUI_UltimateSDUpscale",
			Repository: "https://github.com/ssitu/ComfyUI_UltimateSDUpscale",
			Classes:    []string{"UltimateSDUpscale"}, ClaimedClasses: 4,
			Source: comfy.SourceMap, Installable: true},
		{ID: "promptchain", Title: "ComfyUI-PromptChain",
			Repository: "https://github.com/mobcat40/ComfyUI-PromptChain",
			Classes:    []string{"UltimateSDUpscale", "SoloNode"}, ClaimedClasses: 93,
			Source: comfy.SourceMap, Installable: true},
	}
}

// TestSoleClaimantIsNeverCollapsedAsAnAlternative is the guard for the mixed case:
// a pack that loses one contest while being the sole provider of another missing
// node type must render as a REQUIRED pack, not behind the alternatives disclosure.
//
// Measured before the fix: needed=1 alternatives=1, ComfyUI-PromptChain collapsed,
// "SoloNode" reachable only inside the closed <details>, and the summary line
// reading "Other pack claiming the same node: ComfyUI-PromptChain" — false, since
// it also claims a node nothing else does.
func TestSoleClaimantIsNeverCollapsedAsAnAlternative(t *testing.T) {
	snap := contestedNodesSnapshot()
	snap.Preflight.MissingNodes = []string{"UltimateSDUpscale", "SoloNode"}
	snap.NodeAttr.Packs = mixedClaimPacks()

	// PRECONDITIONS on the RANKING, asserted before any rendering. A fixture that
	// cannot express the contest must fail loudly here rather than pass quietly
	// downstream — the repo's documented "the fixture never populated the signal"
	// failure. These pin the exact shape the bug needs: ONE contested class with two
	// claimants, and a second class claimed by exactly one of them.
	ranked := rankPacks(snap.NodeAttr.Packs)
	if len(ranked) != 2 {
		t.Fatalf("precondition: want 2 ranked packs, got %d", len(ranked))
	}
	if got := contestedClasses(ranked); len(got) != 1 || got[0] != "UltimateSDUpscale" {
		t.Fatalf("precondition: want exactly UltimateSDUpscale contested, got %v", got)
	}
	if !ranked[0].Contested || !ranked[0].Best {
		t.Fatalf("precondition: pack X must WIN the contest, got Contested=%v Best=%v",
			ranked[0].Contested, ranked[0].Best)
	}
	if !ranked[1].Contested || ranked[1].Best {
		t.Fatalf("precondition: pack Y must LOSE the contest, got Contested=%v Best=%v",
			ranked[1].Contested, ranked[1].Best)
	}

	// THE ASSERTION. Y is the only claimant of SoloNode, so it is needed.
	if _, alternatives := splitNeededFromAlternatives(ranked); len(alternatives) != 0 {
		t.Errorf("a pack that is the SOLE claimant of a missing node type was collapsed as an "+
			"alternative: %s claims %v, and nothing else claims SoloNode",
			alternatives[0].Pack.Title, alternatives[0].Pack.Classes)
	}

	body := renderString(t, runStatusFragment(snap, 7, "tok", true, fullMaturityRange()))

	// SoloNode must be reachable in the open panel, not only inside a closed
	// <details>. Position is what proves it: anything after the alternatives summary
	// is inside that disclosure.
	if !strings.Contains(body, "SoloNode") {
		t.Fatalf("SoloNode is missing from the panel entirely:\n%s", body)
	}
	if det := strings.Index(body, "claiming the same node"); det >= 0 {
		t.Errorf("nothing may be collapsed here — the alternatives disclosure rendered, and "+
			"the only candidate for it is the sole claimant of SoloNode:\n%s", body)
	}
	// The required pack keeps a LOUD install button and is not badged as optional.
	if !strings.Contains(body, "Install ComfyUI-PromptChain") {
		t.Errorf("the required pack lost its Install button:\n%s", body)
	}
	if strings.Contains(body, "Also claims it") {
		t.Errorf("a REQUIRED pack must not be badged as a mere alternative claimant:\n%s", body)
	}
}

// TestUncontestedPacksAreNeverCollapsed covers a DIFFERENT mutation: three missing
// node types from three different packs means three REQUIRED packs, and a
// position-based implementation (collapse everything after the first) would hide
// the second and third.
//
// ⚠ It is deliberately NOT the guard for the sole-claimant bug and cannot be: with
// one disjoint class per pack NOTHING is contested, so a pack that both loses a
// contest and solely claims another class is structurally unreachable from this
// fixture. That case is TestSoleClaimantIsNeverCollapsedAsAnAlternative, above.
// Mutation-verified as covering the position-based collapse and NOT the
// `Contested && !Best` one — see the PR's mutation matrix.
func TestUncontestedPacksAreNeverCollapsed(t *testing.T) {
	snap := contestedNodesSnapshot()
	snap.Preflight.MissingNodes = []string{"NodeA", "NodeB", "NodeC"}
	snap.NodeAttr.Packs = []comfy.Pack{
		{ID: "a", Title: "PackA", Repository: "https://github.com/x/a",
			Classes: []string{"NodeA"}, ClaimedClasses: 3, Source: comfy.SourceMap, Installable: true},
		{ID: "b", Title: "PackB", Repository: "https://github.com/x/b",
			Classes: []string{"NodeB"}, ClaimedClasses: 3, Source: comfy.SourceMap, Installable: true},
		{ID: "c", Title: "PackC", Repository: "https://github.com/x/c",
			Classes: []string{"NodeC"}, ClaimedClasses: 3, Source: comfy.SourceMap, Installable: true},
	}
	body := renderString(t, runStatusFragment(snap, 7, "tok", true, fullMaturityRange()))

	// PRECONDITION: nothing is contested here, so nothing may collapse.
	if strings.Contains(body, "Also claims it") {
		t.Fatalf("precondition: this fixture must produce NO contest:\n%s", body)
	}
	if strings.Contains(body, "claiming the same node") {
		t.Errorf("three separately-needed packs must not be collapsed as alternatives:\n%s", body)
	}
	for _, want := range []string{"Install PackA", "Install PackB", "Install PackC"} {
		if !strings.Contains(body, want) {
			t.Errorf("every REQUIRED pack must stay expanded, missing %q:\n%s", want, body)
		}
	}
}

// TestScrollableCommandBlocksAreKeyboardReachable pins the axe fix.
//
// 🔴 `overflow-x-auto` makes a box a scrollable region, and a scrollable region with
// no tabindex cannot be scrolled by keyboard — everything past the right edge is
// unreachable without a pointer. These commands overflow routinely (a git URL plus a
// custom_nodes path), so at the mobile viewport the text ends mid-URL.
//
// axe rated it `scrollable-region-focusable`, impact SERIOUS, on two mobile captures
// the very first walk that could render this panel. It went unseen for so long
// because the ux-audit lab had no ComfyUI-Manager fake and no missing node type, so
// no pack card — and therefore no command block — ever reached a capture.
func TestScrollableCommandBlocksAreKeyboardReachable(t *testing.T) {
	body := renderString(t, runStatusFragment(contestedNodesSnapshot(), 7, "tok", true, fullMaturityRange()))

	// PRECONDITION: the scrollable blocks are actually in this render, or "they are
	// all focusable" is vacuously true.
	const scrollable = `<pre class="mt-1 overflow-x-auto rounded border border-slate-800 p-2"`
	n := strings.Count(body, scrollable)
	if n != 2 {
		t.Fatalf("precondition: want 2 scrollable command blocks (one per pack), got %d:\n%s", n, body)
	}
	// Assert on the <pre> ELEMENT, not a bare substring: a tabindex anywhere else in
	// the panel would satisfy a naive Contains while these boxes stayed unreachable.
	if got := strings.Count(body, scrollable+` tabindex="0"`); got != n {
		t.Errorf("%d of %d scrollable command blocks are keyboard-reachable — a scrollable "+
			"region without tabindex cannot be scrolled without a pointer:\n%s", got, n, body)
	}
}

// TestLowerBoundNoticeStaysAboveTheInstallCTA is the ordering guard the caveat's
// own comment demands: the CTA is a promise ("install these and it should run")
// and the reader has to know the list is bounded BEFORE acting on it.
//
// It pins position, presence, and the two things the wording must never do — quote
// a count, or read as a complete list.
func TestLowerBoundNoticeStaysAboveTheInstallCTA(t *testing.T) {
	body := renderString(t, runStatusFragment(contestedNodesSnapshot(), 7, "tok", true, fullMaturityRange()))

	notice := strings.Index(body, lowerBoundNotice190Marker)
	if notice < 0 {
		t.Fatalf("the lower-bound caveat is missing entirely:\n%s", body)
	}
	// PRECONDITION: the CTA is actually present in this render, or "above" is vacuous.
	cta := strings.Index(body, "install-missing-and-run")
	if cta < 0 {
		t.Fatalf("precondition: this fixture must render the install CTA:\n%s", body)
	}
	if notice > cta {
		t.Errorf("the caveat must render ABOVE the install CTA (caveat=%d cta=%d):\n%s", notice, cta, body)
	}
	// It must also stay above the missing-nodes panel it refers to.
	if nodes := strings.Index(body, "Missing custom nodes"); nodes >= 0 && notice > nodes {
		t.Errorf("the caveat must render above the panels it qualifies (caveat=%d nodes=%d)", notice, nodes)
	}
}

// TestFailureReportDoesNotRestateItsOwnHeadline guards item 4: the counts belong to
// the headline, and the lead repeating them was 194 characters of duplication.
func TestFailureReportDoesNotRestateItsOwnHeadline(t *testing.T) {
	body := renderString(t, runStatusFragment(contestedNodesSnapshot(), 7, "tok", true, fullMaturityRange()))

	if !strings.Contains(body, "Run failed — 1 model file and 1 custom node are missing") {
		t.Fatalf("precondition: want the counted headline:\n%s", body)
	}
	// The old lead's shape, in both numbers, must not come back.
	for _, banned := range []string{
		"This workflow needs 1 model file",
		"1 model file that is not installed",
		"1 custom node that is not installed",
	} {
		if strings.Contains(body, banned) {
			t.Errorf("the lead restates the headline (%q):\n%s", banned, body)
		}
	}
}

// TestManualInstallNoteRendersOnceForTheWholePanel guards the de-duplication: the
// per-pack COMMANDS stay (each names a different repository), the identical
// after-install advice does not repeat per pack.
func TestManualInstallNoteRendersOnceForTheWholePanel(t *testing.T) {
	body := renderString(t, runStatusFragment(contestedNodesSnapshot(), 7, "tok", true, fullMaturityRange()))

	// PRECONDITION: two packs really are rendered, so a per-pack duplicate WOULD
	// appear twice if it existed.
	if n := strings.Count(body, "git clone"); n != 2 {
		t.Fatalf("precondition: want 2 manual commands (one per pack), got %d:\n%s", n, body)
	}
	if n := strings.Count(body, "requirements.txt"); n != 1 {
		t.Errorf("the after-install note must render once, got %d occurrences:\n%s", n, body)
	}
}

// TestNodePackMethodologyLivesInTechnicalDetails guards item 3 — relocation, NOT
// deletion. Every relocated sentence must still be reachable, and must sit inside
// the Technical details disclosure rather than above the recovery actions.
func TestNodePackMethodologyLivesInTechnicalDetails(t *testing.T) {
	body := renderString(t, runStatusFragment(contestedNodesSnapshot(), 7, "tok", true, fullMaturityRange()))

	tech := strings.Index(body, "Technical details")
	if tech < 0 {
		t.Fatalf("precondition: want the Technical details disclosure:\n%s", body)
	}
	cta := strings.Index(body, "install-missing-and-run")
	if cta < 0 {
		t.Fatalf("precondition: want the install CTA:\n%s", body)
	}

	for _, moved := range []string{
		"belongs to a custom-node pack",      // what a pack is
		"api.comfy.org",                      // where the names come from
		"ranked by how much of each pack is", // how the ranking works
	} {
		at := strings.Index(body, moved)
		if at < 0 {
			t.Errorf("relocated methodology was DELETED, not moved: %q missing", moved)
			continue
		}
		if at < tech {
			t.Errorf("%q still renders above Technical details (at=%d tech=%d)", moved, at, tech)
		}
		if at < cta {
			t.Errorf("%q still renders before the primary action (at=%d cta=%d)", moved, at, cta)
		}
	}
}
