package web

import (
	"strings"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/comfy"
)

// The rendered half of the pack-ranking fix. comfy.sortPacks decides the ORDER
// (guarded in internal/comfy/nodepack_rank_test.go); this file guards that the
// panel makes that order legible instead of presenting several claimants as
// equals — which is what put an equally prominent Install button on a 93-class
// grab-bag.

// contestedAttribution is the measured ground case as the panel receives it:
// two packs claiming UltimateSDUpscale, already in comfy-ranked order (the
// tightly-scoped one first).
func contestedAttribution() nodeAttribution {
	return nodeAttribution{
		ManagerPresent: true,
		Packs: []comfy.Pack{
			{
				ID: "comfyui_ultimatesdupscale", Title: "ComfyUI_UltimateSDUpscale",
				Repository:  "https://github.com/ssitu/ComfyUI_UltimateSDUpscale",
				Version:     "1.7.2",
				Installable: true,
				Classes:     []string{"UltimateSDUpscale"}, ClaimedClasses: 4,
				Source: comfy.SourceMap,
			},
			{
				ID: "comfyui-promptchain", Title: "ComfyUI-PromptChain",
				Repository:  "https://github.com/mobcat40/ComfyUI-PromptChain",
				Version:     "0.1.13",
				Installable: true,
				Classes:     []string{"UltimateSDUpscale"}, ClaimedClasses: 93,
				Source: comfy.SourceMap,
			},
		},
	}
}

func renderMissingNodesPanel(t *testing.T, attr nodeAttribution) string {
	t.Helper()
	return renderString(t, missingNodesPanel(attr, []string{"UltimateSDUpscale"}, 9, "csrf-tok", "/opt/ComfyUI"))
}

// TestContestedNodeTypeIsLabelledNotSilent is the core guard: with two claimants
// the panel must SAY so, badge the favoured one, and keep the other visible.
func TestContestedNodeTypeIsLabelledNotSilent(t *testing.T) {
	body := renderMissingNodesPanel(t, contestedAttribution())

	// 1. The ambiguity is stated at all.
	if !strings.Contains(body, "More than one pack claims UltimateSDUpscale") {
		t.Errorf("the panel does not tell the user the node type has several claimants:\n%s", body)
	}
	// 2. The favoured claimant is named as such, and the loser is labelled too — an
	//    unlabelled list reads as a ranking nobody asserted.
	if !strings.Contains(body, "Best match") {
		t.Errorf("the favoured claimant carries no label:\n%s", body)
	}
	if !strings.Contains(body, "Also claims it") {
		t.Errorf("the alternative claimant carries no label:\n%s", body)
	}
	// 3. 🔴 NOTHING IS DROPPED. The ranking is a heuristic over a third-party index;
	//    a wrong single answer is worse than an ordered list.
	for _, want := range []string{
		"ComfyUI_UltimateSDUpscale",
		"ComfyUI-PromptChain",
		"https://github.com/ssitu/ComfyUI_UltimateSDUpscale",
		"https://github.com/mobcat40/ComfyUI-PromptChain",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("candidate %q was dropped from the panel:\n%s", want, body)
		}
	}
}

// TestBestMatchIsRenderedBeforeTheAlternative pins the ORDER as it actually
// reaches the DOM. The comfy-level guard proves sortPacks ranks correctly; this
// proves the panel does not re-order or re-group it away.
func TestBestMatchIsRenderedBeforeTheAlternative(t *testing.T) {
	body := renderMissingNodesPanel(t, contestedAttribution())

	best := strings.Index(body, "https://github.com/ssitu/ComfyUI_UltimateSDUpscale")
	alt := strings.Index(body, "https://github.com/mobcat40/ComfyUI-PromptChain")
	if best < 0 || alt < 0 {
		t.Fatalf("both packs must render (best=%d alt=%d):\n%s", best, alt, body)
	}
	if best > alt {
		t.Errorf("the 4-class pack must render BEFORE the 93-class pack; got best=%d alt=%d", best, alt)
	}
}

// TestLosingClaimantGetsAQuieterInstallButton pins the prominence decision: the
// alternative keeps a WORKING install (the ranking is not authoritative) but must
// not shout as loudly as the best match. Equal prominence is precisely what made
// installing 93 unrelated node types a coin-flip.
func TestLosingClaimantGetsAQuieterInstallButton(t *testing.T) {
	body := renderMissingNodesPanel(t, contestedAttribution())

	best := installButtonVariant(t, body, "ComfyUI_UltimateSDUpscale")
	alt := installButtonVariant(t, body, "ComfyUI-PromptChain")

	if best != "filled" {
		t.Errorf("best match install variant = %q, want filled", best)
	}
	if alt != "outline" {
		t.Errorf("alternative install variant = %q, want outline (present but quieter)", alt)
	}
	// It must still be a REAL install control — demoted, not disabled.
	if !strings.Contains(body, "Install ComfyUI-PromptChain") {
		t.Errorf("the alternative lost its install affordance entirely:\n%s", body)
	}
}

// installButtonVariant finds the "Install <title>" button and returns the
// data-variant of the element it belongs to. It walks BACK from the label to the
// nearest preceding `data-variant="…"`, which is that button's own attribute —
// a forward search would find the NEXT card's button.
func installButtonVariant(t *testing.T, body, title string) string {
	t.Helper()
	label := "Install " + title + "<"
	at := strings.Index(body, label)
	if at < 0 {
		t.Fatalf("no install button for %q in:\n%s", title, body)
	}
	const attr = `data-variant="`
	open := strings.LastIndex(body[:at], attr)
	if open < 0 {
		t.Fatalf("install button for %q carries no data-variant", title)
	}
	rest := body[open+len(attr):]
	end := strings.Index(rest, `"`)
	if end < 0 {
		t.Fatalf("unterminated data-variant for %q", title)
	}
	return rest[:end]
}

// TestPackScopeIsShownAsTheReason pins that the EVIDENCE for the ranking is on
// screen. A verdict with no reasoning cannot be checked or overruled by the user,
// and the numbers are what make "best match" mean something.
func TestPackScopeIsShownAsTheReason(t *testing.T) {
	body := renderMissingNodesPanel(t, contestedAttribution())

	for _, want := range []string{
		"This pack provides 4 node types in total; 1 of them is what this workflow needs.",
		"This pack provides 93 node types in total; 1 of them is what this workflow needs.",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing the scope evidence %q:\n%s", want, body)
		}
	}
}

// TestUncontestedPanelStaysQuiet is the REGRESSION guard for the ordinary case:
// one claimant, one class. No ambiguity notice, no badges — a panel that cried
// "more than one pack claims this" on a single pack would be noise, and worse,
// would train the user to ignore the real warning.
func TestUncontestedPanelStaysQuiet(t *testing.T) {
	attr := contestedAttribution()
	attr.Packs = attr.Packs[:1] // just the ssitu pack

	body := renderMissingNodesPanel(t, attr)

	if !strings.Contains(body, "ComfyUI_UltimateSDUpscale") {
		t.Fatalf("the single pack did not render at all:\n%s", body)
	}
	for _, forbidden := range []string{"More than one pack claims", "Best match", "Also claims it"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("an UNCONTESTED panel must not say %q:\n%s", forbidden, body)
		}
	}
	if v := installButtonVariant(t, body, "ComfyUI_UltimateSDUpscale"); v != "filled" {
		t.Errorf("an uncontested pack's install variant = %q, want filled", v)
	}
}

// TestUnknownScopeShowsNoFabricatedReason pins that a pack whose surface could not
// be measured (the Comfy Registry rung publishes no pack-wide class list) gets NO
// scope line rather than an invented one — "provides 0 node types" would be a
// false statement about a real pack.
func TestUnknownScopeShowsNoFabricatedReason(t *testing.T) {
	attr := nodeAttribution{
		ManagerPresent: true,
		Packs: []comfy.Pack{{
			ID: "mystery", Title: "Mystery Pack",
			Repository:  "https://github.com/someone/mystery",
			Installable: true,
			Classes:     []string{"UltimateSDUpscale"}, ClaimedClasses: 0,
			Source: comfy.SourceRegistry,
		}},
	}
	body := renderMissingNodesPanel(t, attr)

	if !strings.Contains(body, "Mystery Pack") {
		t.Fatalf("the pack did not render:\n%s", body)
	}
	if strings.Contains(body, "This pack provides 0 node types") {
		t.Errorf("an unmeasured pack was given a fabricated scope line:\n%s", body)
	}
	if strings.Contains(body, "This pack provides") {
		t.Errorf("an unmeasured pack must carry NO scope line at all:\n%s", body)
	}
}

// TestAmbiguityNoticeNamesEveryContestedClass pins the copy for the multi-class
// case, so the notice cannot silently report only the first one.
func TestAmbiguityNoticeNamesEveryContestedClass(t *testing.T) {
	attr := nodeAttribution{
		ManagerPresent: true,
		Packs: []comfy.Pack{
			{ID: "a", Title: "Pack A", Repository: "https://github.com/x/a",
				Classes: []string{"Alpha", "Beta"}, ClaimedClasses: 2, Source: comfy.SourceMap},
			{ID: "b", Title: "Pack B", Repository: "https://github.com/x/b",
				Classes: []string{"Alpha", "Beta"}, ClaimedClasses: 40, Source: comfy.SourceMap},
		},
	}
	body := renderMissingNodesPanel(t, attr)

	if !strings.Contains(body, "More than one pack claims Alpha and Beta") {
		t.Errorf("the notice does not name every contested class:\n%s", body)
	}
}
