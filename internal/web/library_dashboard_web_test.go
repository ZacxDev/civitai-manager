package web

import (
	"regexp"
	"strings"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// ---------------------------------------------------------------------------
// Item 4 — matched / unmatched TABS on one card.
// ---------------------------------------------------------------------------

// unmatchedFile is one scanned file CivitAI could not identify.
func unmatchedFile(path string, size int64) store.LocalFile {
	return store.LocalFile{Path: path, SizeBytes: size,
		Status: store.LocalStatusUnmatched, Kind: store.LocalKindModel}
}

// TestMatchedFilesCardHasBothTabsReachable proves BOTH populations live on ONE
// card and both are reachable: two role=tab controls with correct aria-selected,
// each wired to a role=tabpanel that is actually in the DOM (the unmatched one
// merely `hidden`, so switching costs no request).
func TestMatchedFilesCardHasBothTabsReachable(t *testing.T) {
	matched := []fileGroup{{modelID: 7, files: []store.LocalFile{matchedFile(7, 2048)}}}
	unmatched := []store.LocalFile{unmatchedFile("/m/mystery.safetensors", 4096)}
	out := renderString(t, matchedFilesCard(matched, unmatched, nil))

	// ONE tablist, TWO tabs, TWO panels. The tab count is matched on
	// `type="button" role="tab"` and NOT on a bare `role="tab"`: the inline toggle
	// script contains the selector `[role="tab"]` twice, so a bare count reads 4 and
	// the assertion would be measuring the script, not the markup.
	if n := strings.Count(out, `role="tablist" aria-label="Model files"`); n != 1 {
		t.Errorf("expected exactly 1 tablist, got %d:\n%s", n, out)
	}
	if n := strings.Count(out, `type="button" role="tab"`); n != 2 {
		t.Errorf("expected 2 tabs, got %d:\n%s", n, out)
	}
	if n := strings.Count(out, `role="tabpanel"`); n != 2 {
		t.Errorf("expected 2 tabpanels, got %d:\n%s", n, out)
	}
	// ONE card holds them. Counted on a render with NO matched groups, because each
	// lazy model card is itself a card — a populated render legitimately has more.
	if n := strings.Count(renderString(t, matchedFilesCard(nil, unmatched, nil)),
		`data-civitai-ui="card"`); n != 1 {
		t.Errorf("matched/unmatched must live on ONE card, got %d", n)
	}

	// Exactly one selected tab, and it is the matched one.
	if n := strings.Count(out, `aria-selected="true"`); n != 1 {
		t.Errorf("exactly one tab may be aria-selected, got %d:\n%s", n, out)
	}
	if !strings.Contains(out, `class="lib-tab lib-tab-active" aria-selected="true" aria-controls="lib-files-panel-matched"`) {
		t.Errorf("the matched tab must be the selected one:\n%s", out)
	}
	if !strings.Contains(out, `class="lib-tab" aria-selected="false" aria-controls="lib-files-panel-unmatched"`) {
		t.Errorf("the unmatched tab must render unselected:\n%s", out)
	}

	// Both populations are in the DOM — the unmatched one is reachable, not dropped.
	if !strings.Contains(out, `id="model-card-7"`) {
		t.Errorf("the matched panel must hold the model cards:\n%s", out)
	}
	if !strings.Contains(out, "/m/mystery.safetensors") {
		t.Errorf("the unmatched panel must hold the unmatched files:\n%s", out)
	}
	// Counts live in the tab labels (they used to be section headings).
	for _, want := range []string{"Matched models (1)", "Unmatched (1)"} {
		if !strings.Contains(out, want) {
			t.Errorf("tab label %q missing:\n%s", want, out)
		}
	}
	// The unmatched panel starts hidden, and each panel is labelled by its tab.
	if !strings.Contains(out, `id="lib-files-panel-unmatched" role="tabpanel" aria-labelledby="lib-files-tab-unmatched" tabindex="0" hidden`) {
		t.Errorf("the unmatched panel must start hidden and be labelled by its tab:\n%s", out)
	}
}

// TestMatchedFilesTabsAreKeyboardOperable pins the keyboard contract: real
// <button>s (so Enter/Space activate with no handler at all) plus explicit
// ArrowLeft/ArrowRight roving per the ARIA tabs pattern.
func TestMatchedFilesTabsAreKeyboardOperable(t *testing.T) {
	out := renderString(t, matchedFilesCard(nil, nil, nil))

	if n := strings.Count(out, `<button id="lib-files-tab-`); n != 2 {
		t.Errorf("the tabs must be real <button>s (Enter/Space for free), got %d:\n%s", n, out)
	}
	for _, want := range []string{
		`onclick="cmLibFilesTab(this)"`,
		`onkeydown="cmLibFilesTabKey(event,this)"`,
		"function cmLibFilesTab(btn)",
		"function cmLibFilesTabKey(e, btn)",
		"ArrowLeft", "ArrowRight",
		// The script must drive the a11y state, not only the class.
		"setAttribute('aria-selected'",
		"panel.hidden = !on",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("tab keyboard wiring missing %q:\n%s", want, out)
		}
	}
}

// TestBothTabStripsShareTheLibTabVocabulary is the anti-fork guard. The in-card
// strip and the page strip must paint from the SAME class vocabulary; if someone
// forks one into its own classes this fails, because the assertion is on the exact
// shared tokens rather than on "some tab-ish class".
func TestBothTabStripsShareTheLibTabVocabulary(t *testing.T) {
	page := renderString(t, libraryTabStrip("files"))
	card := renderString(t, matchedFilesCard(nil, nil, nil))

	for _, want := range []string{libTabsClass, libTabClass, libTabActiveClass} {
		if !strings.Contains(page, want) {
			t.Errorf("the PAGE tab strip does not use %q:\n%s", want, page)
		}
		if !strings.Contains(card, want) {
			t.Errorf("the IN-CARD tab strip does not use %q — do not fork the tab idiom:\n%s",
				want, card)
		}
	}
	// The JS that re-applies the classes on click must use the same literals, or the
	// strip forks the moment the user clicks it.
	for _, want := range []string{`'lib-tab lib-tab-active'`, `'lib-tab'`} {
		if !strings.Contains(card, want) {
			t.Errorf("the tab script must re-apply the shared %s class:\n%s", want, card)
		}
	}
	// Neither strip may fall back to the civitai button component (that is the
	// button chrome the underline strip deliberately replaced).
	if strings.Contains(card, `data-civitai-ui="button"`) {
		t.Errorf("in-card tabs must not render the civitai button component:\n%s", card)
	}
}

// ---------------------------------------------------------------------------
// Item 5 — the update-available control and the subscribe control.
// ---------------------------------------------------------------------------

// updatableCardView is a matched-model card whose latest version is NOT local.
func updatableCardView(t *testing.T) matchedModelCardView {
	t.Helper()
	m := &civitai.ModelDetail{ID: 7, Name: "Great Model", ModelVersions: threeVersions()}
	return buildMatchedModelCardView(7, m, nil, []store.LocalFile{localFile(10, 50)},
		fullMaturityRange(), nil)
}

// TestUpdateAvailableCTAHasIconAndPopover pins item 5's first half: a custom SVG
// glyph, and the update DETAILS in a hover/focus popover rather than inline.
func TestUpdateAvailableCTAHasIconAndPopover(t *testing.T) {
	out := renderString(t, updateAvailableCTA(updatableCardView(t)))

	for _, want := range []string{
		"cm-upd-cta",         // the success-toned CTA
		"<svg", "cm-upd-ico", // its custom inline glyph
		`href="/models/7?version=30"`, // still the deeplink to the latest version
		"Update available: v3",
		// The popover, on the SHARED mechanism.
		"cm-updated", "cm-updated-pop", `role="tooltip"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("update-available CTA missing %q:\n%s", want, out)
		}
	}

	// The DETAILS must live in the popover, not on the CTA face.
	pop := out[strings.Index(out, "cm-updated-pop"):]
	for _, want := range []string{"You have: v1", "Latest: v3", "3 version(s) published"} {
		if !strings.Contains(pop, want) {
			t.Errorf("update details must live in the popover, missing %q:\n%s", want, pop)
		}
	}
	// It must no longer borrow the WARNING-toned run-failure CTA.
	if strings.Contains(out, "cm-fix-cta") {
		t.Errorf("the update CTA must not reuse the amber .cm-fix-cta — an available "+
			"update is not a fault:\n%s", out)
	}
	// One hover affordance per element.
	if strings.Contains(out, "title=") {
		t.Errorf("the CTA owns a popover, so it must not also carry title=:\n%s", out)
	}
}

// TestUpdateCTAUsesTheTextToken is the split-token gate for the new coloured pair,
// and it reads the SHIPPED rule rather than a copy of it.
//
// 🔴 TestTokenContrast cannot catch this on its own: it resolves TOKEN pairs from
// the theme blocks and knows nothing about which rule paints which token. So the
// contrast table pins the RATIO and this pins the MAPPING.
//
// MUTATION-VERIFIED: changing .cm-upd-cta's `color` to var(--civitai-color-success)
// fails this with ".cm-upd-cta paints text with the FILL token".
func TestUpdateCTAUsesTheTextToken(t *testing.T) {
	body := cssRuleBody(t, ".cm-upd-cta")

	colorRE := regexp.MustCompile(`(?m)^\s*color\s*:\s*([^;]+);`)
	m := colorRE.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf(".cm-upd-cta declares no color:\n%s", body)
	}
	got := strings.TrimSpace(m[1])
	if got != "var(--civitai-color-success-text)" {
		t.Errorf(".cm-upd-cta paints text with %s. Every intent token is SPLIT: the base "+
			"token is the FILL (what the color-mix tint is made of) and `-text` is the "+
			"AA-contrast FOREGROUND. Using the FILL token for text reintroduces exactly the "+
			"WCAG failures v0.1.79 fixed — on dark the two roles are mathematically "+
			"unsatisfiable by one value. Want var(--civitai-color-success-text).", got)
	}
	// The tint the `-text` token sits on is the one pinned in uiPairs().
	if !strings.Contains(body, "var(--civitai-color-success) 14%") {
		t.Errorf(".cm-upd-cta's background tint must stay success@14%%, which is the pair "+
			"pinned in contrast_web_test.go's uiPairs():\n%s", body)
	}
}

// TestSubscribeControlIsLargerAndExplainsItself pins item 5's second half: the
// button grew, and an info popover states the AUTO-DOWNLOAD consequence before the
// click.
func TestSubscribeControlIsLargerAndExplainsItself(t *testing.T) {
	// workflow=false: this pins the ORDINARY model control. The Workflows-post
	// shape is a separate control with different copy — see
	// workflow_subscribe_web_test.go.
	out := renderString(t, subscribeControl(7, nil, "csrf", false))

	// The size class actually changed (it was data-size="sm").
	if !strings.Contains(out, `data-size="md"`) {
		t.Errorf("the collapsed Subscribe button must render at the larger md size:\n%s", out)
	}
	if strings.Contains(out, `data-size="sm"`) {
		t.Errorf("no part of the collapsed subscribe control may still be sm:\n%s", out)
	}

	// The info affordance, on the shared popover mechanism, beside the button.
	for _, want := range []string{
		"cm-subinfo", "cm-updated", "cm-updated-pop", `role="tooltip"`,
		"<svg", "cm-info-ico",
		`role="img"`, `aria-label="What subscribing does"`, // an icon-only trigger needs a name
	} {
		if !strings.Contains(out, want) {
			t.Errorf("subscribe info affordance missing %q:\n%s", want, out)
		}
	}

	// 🔴 The CONSEQUENCE must be stated plainly: subscribing DOWNLOADS FILES by
	// default. "Subscribe" otherwise reads like a notification opt-in.
	pop := out[strings.Index(out, "cm-updated-pop"):]
	for _, want := range []string{
		"Auto-download", "default", "downloaded", "automatically",
		"model folder", "disk space", "Notify only",
	} {
		if !strings.Contains(pop, want) {
			t.Errorf("the subscribe explanation must name the auto-download consequence, "+
				"missing %q:\n%s", want, pop)
		}
	}
	// One hover affordance per element.
	if strings.Contains(out, "title=") {
		t.Errorf("the info trigger owns a popover, so it must not also carry title=:\n%s", out)
	}
}

// TestMatchedCardCarriesBothItem5Controls proves the two controls above actually
// reach the matched-model card — the surface the brief is about — rather than only
// rendering in isolation.
func TestMatchedCardCarriesBothItem5Controls(t *testing.T) {
	out := renderString(t, matchedModelCard(updatableCardView(t), "csrf"))
	for _, want := range []string{"cm-upd-cta", "cm-upd-ico", "cm-subinfo", `data-size="md"`} {
		if !strings.Contains(out, want) {
			t.Errorf("the matched model card is missing %q:\n%s", want, out)
		}
	}
}
