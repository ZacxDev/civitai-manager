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

// installButtonOpenTag returns the OPEN TAG of ONE pack's Install button, so an
// assertion can be about THAT button's attributes and nothing else.
//
// 🔴 It exists because the thing under guard is an attribute VALUE that every other
// button on the panel also carries: `strings.Contains(body, "data-variant=\"filled\"")`
// is satisfied by the other pack's button and by the panel's own primary CTA, so it
// stays green with the pack under test demoted to `outline`. That is the repo's
// documented "the assertion matched a DIFFERENT element's attribute" failure.
//
// The label marker is anchored on BOTH sides (">Install <title><"), so one pack title
// being a prefix of another cannot make this return the wrong element — the other
// documented trap ("one fixture name was a substring of another").
func installButtonOpenTag(t *testing.T, body, packTitle string) string {
	t.Helper()
	marker := ">Install " + packTitle + "<"
	at := strings.Index(body, marker)
	if at < 0 {
		t.Fatalf("no Install button for %q in the panel:\n%s", packTitle, body)
	}
	open := strings.LastIndex(body[:at], "<button")
	if open < 0 {
		t.Fatalf("the Install label for %q is not inside a <button>:\n%s", packTitle, body)
	}
	tag := body[open : at+1]
	// INTEGRITY: the slice must be exactly one open tag. If any other element opened
	// between that "<button" and the label, LastIndex found the wrong button and every
	// assertion below would be about a different element's attributes.
	if strings.Contains(tag[len("<button"):], "<") {
		t.Fatalf("the %q Install label is not a direct child of the button found: %q", packTitle, tag)
	}
	return tag
}

// TestRequiredPackKeepsTheLoudInstallButton is the guard for the THIRD consumer of
// needed() — the Install button's prominence (nodepackCard's `variant`). The collapse
// and the badge each had one; this one did not, and re-open-coding it as
// `Contested && !Best` passed the entire suite.
//
// What that mutation ships: a pack the user MUST install renders with an `outline`
// button beside the best match's `filled` one, which is the panel's own "you probably
// do not need this" signal — the exact message nodepackCard's comment forbids. The
// pack stays present and installable, so nothing about presence, naming or ordering
// can see it; only the variant can.
//
// Both directions are asserted, because a rule that demotes NOTHING is equally wrong:
// the pure alternative in contestedNodesSnapshot() must still be quiet.
func TestRequiredPackKeepsTheLoudInstallButton(t *testing.T) {
	snap := contestedNodesSnapshot()
	snap.Preflight.MissingNodes = []string{"UltimateSDUpscale", "SoloNode"}
	snap.NodeAttr.Packs = mixedClaimPacks()

	// PRECONDITIONS on the RANKING. The guard is only meaningful for a pack that LOST
	// a contest and is nonetheless required — that is the single state where the two
	// predicates disagree. Without these, the assertion could be green about a pack
	// neither predicate would ever have demoted.
	ranked := rankPacks(snap.NodeAttr.Packs)
	if len(ranked) != 2 {
		t.Fatalf("precondition: want 2 ranked packs, got %d", len(ranked))
	}
	y := ranked[1]
	if !y.Contested || y.Best {
		t.Fatalf("precondition: pack Y must LOSE its contest, got Contested=%v Best=%v", y.Contested, y.Best)
	}
	if !y.Sole || !y.needed() {
		t.Fatalf("precondition: pack Y must still be REQUIRED (sole claimant of SoloNode), "+
			"got Sole=%v needed=%v", y.Sole, y.needed())
	}

	body := renderString(t, runStatusFragment(snap, 7, "tok", true, fullMaturityRange()))

	// THE ASSERTION. A required pack gets the loud button, whatever it lost.
	if tag := installButtonOpenTag(t, body, "ComfyUI-PromptChain"); !strings.Contains(tag, `data-variant="filled"`) {
		t.Errorf("a REQUIRED pack was demoted to a quiet Install button — it is the sole "+
			"claimant of SoloNode, so an outline button beside the best match's filled one "+
			"tells the reader to skip a pack they must install; button was %q", tag)
	}
	// The winner is the control: it must be loud too, so the assertion above cannot be
	// satisfied by a build that simply made every button filled... which is what the
	// negative half below rules out.
	if tag := installButtonOpenTag(t, body, "ComfyUI_UltimateSDUpscale"); !strings.Contains(tag, `data-variant="filled"`) {
		t.Errorf("the best match lost its loud Install button; button was %q", tag)
	}

	// NEGATIVE HALF — a pack that is genuinely OPTIONAL must still be demoted, or the
	// rule collapses into "everything is filled" and the prominence carries no signal.
	altSnap := contestedNodesSnapshot()
	altRanked := rankPacks(altSnap.NodeAttr.Packs)
	if len(altRanked) != 2 || altRanked[1].needed() {
		t.Fatalf("precondition: contestedNodesSnapshot's runner-up must be a PURE alternative, "+
			"got %d packs, needed=%v", len(altRanked), altRanked[len(altRanked)-1].needed())
	}
	altBody := renderString(t, runStatusFragment(altSnap, 7, "tok", true, fullMaturityRange()))
	if tag := installButtonOpenTag(t, altBody, "ComfyUI-PromptChain"); !strings.Contains(tag, `data-variant="outline"`) {
		t.Errorf("an OPTIONAL alternative kept the loud Install button — the prominence is the "+
			"only thing distinguishing it from the best match; button was %q", tag)
	}
}

// TestRequiredPackIsBadgedAsAlsoNeeded is the POSITIVE half of the contest badge.
//
// 🔴 The existing coverage only forbids the WRONG label ("Also claims it"), so
// deleting the "Also needed" case — returning "" from it — survived the whole suite:
// "one was absent" is not "the correct one is present". A pack that must be installed
// then renders with no badge at all, beside a pack badged "Best match", which reads as
// the unlabelled one being the also-ran.
func TestRequiredPackIsBadgedAsAlsoNeeded(t *testing.T) {
	snap := contestedNodesSnapshot()
	snap.Preflight.MissingNodes = []string{"UltimateSDUpscale", "SoloNode"}
	snap.NodeAttr.Packs = mixedClaimPacks()

	ranked := rankPacks(snap.NodeAttr.Packs)
	if len(ranked) != 2 {
		t.Fatalf("precondition: want 2 ranked packs, got %d", len(ranked))
	}
	// PRECONDITION: this is the mixed state the third case of contestLabel exists for.
	// An uncontested or a winning pack never reaches it.
	if y := ranked[1]; !y.Contested || y.Best || !y.needed() {
		t.Fatalf("precondition: pack Y must be contested, losing and still required, "+
			"got Contested=%v Best=%v needed=%v", y.Contested, y.Best, y.needed())
	}

	// The function itself, pinned to a literal. This is what a `return ""` mutation
	// trips, and it cannot be satisfied by any other string in the panel.
	if got := contestLabel(ranked[1]); got != "Also needed" {
		t.Errorf("contestLabel for a required-but-losing pack = %q, want %q — an unbadged "+
			"card beside a \"Best match\" reads as the one to skip", got, "Also needed")
	}

	// And it must actually REACH the panel, once, on the card it describes.
	body := renderString(t, runStatusFragment(snap, 7, "tok", true, fullMaturityRange()))
	if n := strings.Count(body, ">Also needed<"); n != 1 {
		t.Errorf("want exactly 1 \"Also needed\" badge rendered, got %d:\n%s", n, body)
	}
	if n := strings.Count(body, ">Best match<"); n != 1 {
		t.Errorf("precondition: want exactly 1 \"Best match\" badge to contrast with, got %d:\n%s", n, body)
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
