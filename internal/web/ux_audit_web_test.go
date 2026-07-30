package web

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
	"github.com/ZacxDev/civitai-manager/internal/library"
	"github.com/ZacxDev/civitai-manager/internal/store"
	g "maragu.dev/gomponents"
)

// This file guards the fixes from the responsive/design audit (F3, F5, F6, F8,
// F9, F10, F11, F12, F13, F16, F17, F18). Every assertion below pins a SPECIFIC
// failure that shipped, not "the page renders" — the classic way these
// regressions come back is a class silently dropped during an unrelated edit.
//
// HONEST SCOPE: these are markup/CSS-text assertions. They prove the fix is in
// the served bytes; they cannot prove the rendered result at a real breakpoint or
// the perceived contrast. No browser exists on this host (see CLAUDE.md).

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func appCSS(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("assets/app.css")
	if err != nil {
		t.Fatalf("read app.css: %v", err)
	}
	return string(b)
}

// headingTags returns every heading tag in document order, e.g. ["h1","h2","h2"].
var headingRe = regexp.MustCompile(`<(h[1-6])[\s>]`)

func headingTags(html string) []string {
	var out []string
	for _, m := range headingRe.FindAllStringSubmatch(html, -1) {
		out = append(out, m[1])
	}
	return out
}

// fullPages renders every top-level page the server can serve, keyed by a name.
// Anything that calls page() belongs here — that is the set F9 is about.
func fullPages(t *testing.T) map[string]string {
	t.Helper()

	mid := 42
	subs := []store.Subscription{
		{ID: 1, Kind: store.KindModel, ModelID: &mid, AutoDownload: true, Layout: "default"},
	}
	searchRes := &civitai.ModelSearchResult{Items: []civitai.ModelListItem{
		{ID: 1, Name: "Cool LoRA", Type: "LORA", Creator: &civitai.Creator{Username: "bob"}},
	}}
	model := &civitai.ModelDetail{ID: 7, Name: "Great Model", Type: "Checkpoint",
		Creator:       &civitai.Creator{Username: "carol"},
		ModelVersions: []civitai.ModelVersionSummary{{ID: 1, Name: "v1", BaseModel: "SDXL"}}}
	detail := modelDetailView{Model: model, SelectedVersionID: 1,
		Version: &civitai.ModelVersionDetail{ID: 1, BaseModel: "SDXL"}}
	wfID := int64(3)
	gen := &store.Generation{ID: 5, WorkflowID: &wfID, WorkflowName: "wf", PromptID: "p1", ImageCount: 1}
	wf := &store.Workflow{ID: 3, Name: "My workflow", Format: store.WorkflowFormatAPI, Graph: "{}"}

	return map[string]string{
		"dashboard":  renderString(t, dashboardPage(subs, nil, "csrf", "dark", NSFWBlur)),
		"search":     renderString(t, searchPage("q", searchRes, nil, "csrf", "dark", NSFWBlur, "", "", "")),
		"creator":    renderString(t, creatorPage("dave", searchRes, nil, "csrf", "dark", NSFWBlur)),
		"model":      renderString(t, modelDetailPage(detail, nil, "csrf", "dark", "https://civitai.com")),
		"library":    renderString(t, libraryPage(libraryView{}, "csrf", true, []string{"/m"}, "dark", "files", nil, true, nil, NSFWBlur, libraryWorkflowsView{})),
		"trash":      renderString(t, trashPage(nil, "csrf", "dark", NSFWBlur)),
		"outputs":    renderString(t, outputsGalleryPage(nil, nil, "", 0, 0, "csrf", "dark", NSFWBlur)),
		"generation": renderString(t, generationDetailPage(gen, nil, "csrf", "dark", NSFWBlur)),
		"workflow":   renderString(t, detailPageNode(wf, "csrf", "dark", NSFWBlur, false, comfyHelperView{}, workflowResolver{})),
		"discover-workflow": renderString(t, workflowDiscoverPage(workflowDiscoverView{
			Res: searchRes, Mode: NSFWBlur, CSRF: "csrf",
			Sort: "Most Downloaded", Period: "Month",
		}, "dark")),
		"discover-apps": renderString(t, appsDiscoverPage(nil, "dark", NSFWBlur, "", "", "", "csrf")),
	}
}

// ---------------------------------------------------------------------------
// F9 — exactly one <h1> per page, and it precedes every <h2>
// ---------------------------------------------------------------------------

// TestEveryFullPageHasExactlyOneH1 pins F9: sectionTitle() hardcodes <h2> and was
// used as the PAGE title on most pages, so six of eleven full pages had no <h1> at
// all and their outline started at level 2 with no level 1 above it.
func TestEveryFullPageHasExactlyOneH1(t *testing.T) {
	for name, html := range fullPages(t) {
		tags := headingTags(html)
		var h1s int
		for _, tag := range tags {
			if tag == "h1" {
				h1s++
			}
		}
		if h1s != 1 {
			t.Errorf("page %q has %d <h1> (want exactly 1); heading order = %v", name, h1s, tags)
			continue
		}
		// The <h1> must come FIRST among headings — an <h2> above it leaves the
		// document outline starting at level 2, which is the same defect in reverse.
		if tags[0] != "h1" {
			t.Errorf("page %q starts its outline with <%s>, not <h1>; heading order = %v",
				name, tags[0], tags)
		}
	}
}

// TestPageTitleAndSectionTitleAgreeVisually pins that the F9 fix is SEMANTIC only:
// pageTitle must emit an <h1> with byte-identical classes to sectionTitle's <h2>,
// so no page changed appearance.
func TestPageTitleAndSectionTitleAgreeVisually(t *testing.T) {
	h1 := renderString(t, pageTitle("X"))
	h2 := renderString(t, sectionTitle("X"))
	if !strings.HasPrefix(h1, "<h1 ") {
		t.Errorf("pageTitle must emit an <h1>, got %s", h1)
	}
	if !strings.HasPrefix(h2, "<h2 ") {
		t.Errorf("sectionTitle must emit an <h2>, got %s", h2)
	}
	classOf := func(tag string) string {
		i := strings.Index(tag, `class="`)
		if i < 0 {
			return ""
		}
		rest := tag[i+len(`class="`):]
		return rest[:strings.Index(rest, `"`)]
	}
	if classOf(h1) == "" || classOf(h1) != classOf(h2) {
		t.Errorf("pageTitle and sectionTitle must be visually identical:\n h1 = %s\n h2 = %s", h1, h2)
	}
}

// ---------------------------------------------------------------------------
// F5 — a disabled control must be VISIBLE
// ---------------------------------------------------------------------------

// TestDisabledPaginationIsVisible pins F5: the inert pagination arrows used
// text-slate-600, which maps to --civitai-color-border — 1.48:1 on light and
// 1.52:1 on dark, i.e. invisible in both themes rather than merely de-emphasized.
func TestDisabledPaginationIsVisible(t *testing.T) {
	// page 0 of 3 → "← Newer" is inert; last page → "Older →" is inert.
	first := renderString(t, outputsPagination("", 0, outputsPageSize*3))
	last := renderString(t, outputsPagination("", 2, outputsPageSize*3))

	for name, html := range map[string]string{"first page": first, "last page": last} {
		if strings.Contains(html, "text-slate-600") {
			t.Errorf("%s still uses text-slate-600 (1.48:1 light / 1.52:1 dark — invisible)", name)
		}
		if !strings.Contains(html, "cm-disabled text-sm text-slate-500") {
			t.Errorf("%s: the inert arrow must carry `cm-disabled text-sm text-slate-500`:\n%s", name, html)
		}
	}
	// Exactly one arrow is inert on each end, and the other is a real link.
	if strings.Count(first, "cm-disabled") != 1 || !strings.Contains(first, `href="/outputs?page=1"`) {
		t.Errorf("first page should have ONE inert arrow and a live Older link:\n%s", first)
	}
	if strings.Count(last, "cm-disabled") != 1 || !strings.Contains(last, `href="/outputs?page=1"`) {
		t.Errorf("last page should have ONE inert arrow and a live Newer link:\n%s", last)
	}
	if !strings.Contains(appCSS(t), ".cm-disabled {") {
		t.Error("app.css must define .cm-disabled — otherwise the class-coverage guard is the only thing holding it up")
	}
}

// TestNoInvisibleSlate600Text sweeps the whole package: text-slate-600 resolves to
// the BORDER token and must never be used as a text color anywhere.
func TestNoInvisibleSlate600Text(t *testing.T) {
	scan := scanClassCalls(t)
	if where, ok := scan.where["text-slate-600"]; ok {
		t.Errorf("text-slate-600 is back at %s — it maps to --civitai-color-border "+
			"(1.48:1 light / 1.52:1 dark). Use text-slate-500 + .cm-disabled.", where)
	}
}

// ---------------------------------------------------------------------------
// F6 / F16 — light-theme contrast
// ---------------------------------------------------------------------------

// TestLightThemeDimmedTextOverride pins F6. --civitai-color-text-dimmed is the
// app's most-used text color (text-slate-400 AND text-slate-500 both map to it)
// and the vendored light value #868e96 on #fefefe is 3.29:1, failing WCAG AA.
// The override must live in app.css — civitai-theme.css is AUTOGENERATED and
// parity-tested upstream, so editing it would be silently reverted.
//
// The value is #6b7280 — 4.79:1 on the body, which clears AA there. It does NOT
// clear AA (4.02:1) on the 12%/14% color-mix tint the `light` button variant
// paints under it; a darker #5f6672 would, but darkening light-theme colours was
// rejected in favour of brand fidelity (see the ACCEPTED AA DEBT block in
// app.css). That tint pair is pinned as accepted debt in contrast_web_test.go.
func TestLightThemeDimmedTextOverride(t *testing.T) {
	css := appCSS(t)
	if !strings.Contains(css, `[data-theme="light"] {`) ||
		!strings.Contains(css, "--civitai-color-text-dimmed: #6b7280;") {
		t.Error("app.css must override --civitai-color-text-dimmed to #6b7280 under [data-theme=\"light\"] (3.29:1 -> 4.79:1)")
	}
	// The ratios are the whole point of the deviation — keep them recorded.
	for _, want := range []string{"3.29:1", "4.79:1"} {
		if !strings.Contains(css, want) {
			t.Errorf("app.css must record the %s contrast ratio next to the override", want)
		}
	}
	// A :root override would (at equal specificity, from a later file) also beat
	// civitai-theme.css's [data-theme='dark'] block and wreck the dark palette.
	if strings.Contains(css, ":root {\n  --civitai-color-text-dimmed") {
		t.Error("the dimmed-text override must be scoped to [data-theme=\"light\"], never :root")
	}

	theme, err := os.ReadFile("assets/civitai-theme.css")
	if err != nil {
		t.Fatalf("read civitai-theme.css: %v", err)
	}
	if !strings.Contains(string(theme), "--civitai-color-text-dimmed: #868e96;") {
		t.Error("the vendored AUTOGENERATED theme must NOT be edited — it should still hold #868e96")
	}
}

// TestNoUndefinedThemeTokens pins F16: app.css referenced
// var(--civitai-color-text-muted, #94a3b8), a token @civitai/theme does not
// define, so both rules ALWAYS used the hardcoded #94a3b8 (2.53:1 on light) and
// never re-themed.
func TestNoUndefinedThemeTokens(t *testing.T) {
	css := appCSS(t)
	if strings.Contains(css, "var(--civitai-color-text-muted") {
		t.Error("app.css references --civitai-color-text-muted, which @civitai/theme does not define — use --civitai-color-text-dimmed")
	}
	theme, err := os.ReadFile("assets/civitai-theme.css")
	if err != nil {
		t.Fatalf("read civitai-theme.css: %v", err)
	}
	// Every --civitai-* token app.css consumes must actually be DEFINED somewhere
	// that ships: either upstream in civitai-theme.css, or locally in app.css (the
	// `-text` contrast-split tokens and the `on-*` fill inks are defined locally,
	// per-theme, because the vendored theme has no concept of them). A token
	// defined in NEITHER place silently falls through to its literal fallback and
	// stops re-theming — which is the bug F16 pinned.
	tokenRe := regexp.MustCompile(`var\((--civitai-color-[a-z0-9-]+)`)
	seen := map[string]bool{}
	for _, m := range tokenRe.FindAllStringSubmatch(css, -1) {
		if seen[m[1]] {
			continue
		}
		seen[m[1]] = true
		if strings.Contains(string(theme), m[1]+":") {
			continue
		}
		// Defined locally? Require it under BOTH themes, so neither path regresses.
		if strings.Count(css, m[1]+":") >= 2 {
			continue
		}
		t.Errorf("app.css uses %s, which neither civitai-theme.css nor app.css defines "+
			"for both themes — it will fall through to its literal fallback", m[1])
	}
}

// ---------------------------------------------------------------------------
// F17 — focus visibility
// ---------------------------------------------------------------------------

// TestFocusVisibleRingExists pins F17: the purged Tailwind build carries ZERO
// focus:/focus-visible: selectors, so without this rule keyboard focus is
// untracked across the entire app.
func TestFocusVisibleRingExists(t *testing.T) {
	css := appCSS(t)
	for _, want := range []string{
		":focus-visible {",
		"outline: 2px solid var(--civitai-color-primary)",
		"outline-offset: 2px",
		`[data-civitai-ui="button"]:focus-visible`,
		`[data-civitai-ui-control]:focus-visible`,
	} {
		if !strings.Contains(css, want) {
			t.Errorf("app.css missing focus-ring piece %q", want)
		}
	}
	// The ring must cover the plain elements too, not only the DS roles.
	if !strings.Contains(css, ":where(a, button, summary, input, select, textarea, [tabindex]):focus-visible") {
		t.Error("app.css must ring plain links/buttons/controls, not only data-civitai-ui roles")
	}
}

// TestTileFocusRingIsDrawnInward pins the SIGN of .cm-tile-link's outline-offset.
//
// The gallery tile's detail link is `absolute inset-0` inside a tile carrying
// overflow-hidden, so its border box IS the clip boundary: the shared ring above
// (outline-offset: +2px plus an outward box-shadow) is drawn entirely inside the
// clipped region and NOTHING paints. Only a NEGATIVE offset puts it back on
// screen. Verified A/B in headless Brave — with +2px the anchor is focused,
// :focus-visible matches, the outline computes, and the screenshot shows no ring.
//
// This test exists because that failure mode is INVISIBLE to the normal gate: CSS
// is not compiled, a positive value merely restates the inherited offset, and
// nothing errors. The rule shipped once as an inert `outline-offset: 2px` sitting
// under a comment that argued at length for a negative one — build, vet, test and
// gofmt were all green. Assert the sign, not the exact value, so the ring can be
// re-tuned without churning this test.
func TestTileFocusRingIsDrawnInward(t *testing.T) {
	css := appCSS(t)

	const sel = ".cm-tile-link:focus-visible"
	i := strings.Index(css, sel)
	if i < 0 {
		t.Fatalf("app.css has no %s rule — the gallery tile's overlay anchor would "+
			"inherit the app-wide OUTWARD ring, which its overflow-hidden clips away "+
			"entirely (WCAG 2.4.7)", sel)
	}
	block := css[i:]
	if j := strings.Index(block, "}"); j >= 0 {
		block = block[:j]
	}

	const decl = "outline-offset:"
	k := strings.Index(block, decl)
	if k < 0 {
		t.Fatalf("%s must set outline-offset; block was:\n%s", sel, block)
	}
	value := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(block[k+len(decl):]), ";"))
	if !strings.HasPrefix(value, "-") {
		t.Errorf("%s has outline-offset: %s — a non-negative offset is INERT here. The "+
			"anchor is absolute inset-0 inside an overflow-hidden tile, so an outward "+
			"ring lands in the clipped region and the tile shows NO keyboard focus at "+
			"all. It must be negative.", sel, value)
	}
}

// ---------------------------------------------------------------------------
// F3 / F11 — 390px overflow
// ---------------------------------------------------------------------------

// TestMetaRowIsPhoneSafe pins F3. The label was a flat w-40 (160px of a ~326px
// content box) and the value had neither min-w-0 nor a break opportunity, so a
// flex item's default min-width:auto forced the row far wider than the viewport.
func TestMetaRowIsPhoneSafe(t *testing.T) {
	out := renderString(t, metaRow("Graph hash",
		"6b86b273ff34fce19d6b804eff5a3f5747ada4eaa22f1d49c01e52ddb7875b4b"))
	if !strings.Contains(out, `class="text-slate-500 w-28 sm:w-40 shrink-0"`) {
		t.Errorf("metaRow label must be w-28 sm:w-40 (a flat w-40 is 49%% of a 390px card):\n%s", out)
	}
	for _, want := range []string{"min-w-0", "break-all"} {
		if !strings.Contains(out, want) {
			t.Errorf("metaRow value must carry %q — a 64-char sha256 has no break opportunity:\n%s", want, out)
		}
	}
}

// TestLongUntrustedStringsCanBreak pins that every list printing an arbitrary
// filename/path/URN carries break-all. Each of these renders content the user
// never chose, with no guaranteed break opportunity.
func TestLongUntrustedStringsCanBreak(t *testing.T) {
	const long = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.safetensors"

	wfID := int64(3)
	gen := &store.Generation{
		ID: 5, WorkflowID: &wfID, PromptID: "p", GraphHash: strings.Repeat("a", 64),
		Params: `{"substitute":{"` + long + `":"` + long + `"},"resources":["` + long + `"],` +
			`"widget_overrides":[{"node_id":"1","input_name":"ckpt","value":"` + long + `"}],` +
			`"option_fixes":[{"input_name":"sampler","old_value":"x","new_value":"` + long + `"}]}`,
	}

	cases := map[string]string{
		"generation run params": renderString(t, generationParamsCard(gen)),
		"workflow resources": renderString(t, detailPageNode(
			&store.Workflow{ID: 1, Name: "w", Format: store.WorkflowFormatAPI, Graph: "{}",
				Resources: []string{long}},
			"csrf", "dark", NSFWBlur, false, comfyHelperView{}, workflowResolver{})),
		"run preflight missing list": renderString(t, missingList("Missing", []string{long})),
		// The batch page's h1 prints an untrusted label (a preset name is clamped to
		// 80 bytes; a WORKFLOW name is not) at text-2xl in a flex row.
		// pageMain scopes to <main>: the document <title> also carries the label but
		// lays out nothing.
		"batch page header": pageMain(renderString(t, batchGalleryPage(
			[]store.Generation{{ID: 1, WorkflowID: &wfID, WorkflowName: long, PromptID: "p",
				BatchID: "b", BatchIndex: 1, BatchTotal: 2}},
			"csrf", "dark", NSFWBlur))),
		"structured graph listing": renderString(t, workflowGraphSection(
			[]byte(`{"nodes":[{"id":1,"type":"`+long+`","inputs":[{"name":"`+long+`"}]}]}`),
			store.WorkflowFormatAPI)),
	}
	for name, html := range cases {
		// Every element that prints the long string must be able to break it.
		for _, chunk := range strings.Split(html, "<") {
			// Only TEXT CONTENT can widen a box. The same string inside an ATTRIBUTE
			// (aria-label on the tile's overlay anchor, a title tooltip) lays out
			// nothing, and flagging it is a false positive. A chunk is
			// "tag attrs>text", so the text starts after the first '>'.
			gt := strings.Index(chunk, ">")
			if gt < 0 || !strings.Contains(chunk[gt:], long) {
				continue
			}
			// `truncate` is the third valid mechanism, but ONLY paired with min-w-0,
			// and the pairing is what keeps this checker honest rather than being
			// redundant. truncate sets overflow:hidden, and a flex ITEM's
			// min-width:auto only applies while overflow is VISIBLE — so a direct
			// flex item does collapse to 0 and clip. But on a DESCENDANT of a flex
			// item that lacks min-w-0, truncate's white-space:nowrap makes the
			// ancestor's min-content width the whole string and the row blows out —
			// strictly worse than not truncating. This scan sees one element's class
			// string and cannot tell those two apart, so accepting bare `truncate`
			// would silently exempt the broken nesting (a delta audit demonstrated
			// exactly that: `<div class=flex><div class=flex-1><span class=truncate>`
			// went unflagged here while the pre-truncate checker caught it).
			// Requiring min-w-0 alongside costs one redundant class on the direct-item
			// case and closes the hole. Do NOT relax this back to bare `truncate`.
			truncates := strings.Contains(chunk, "truncate") && strings.Contains(chunk, "min-w-0")
			if !strings.Contains(chunk, "break-all") && !strings.Contains(chunk, "title=") && !truncates {
				t.Errorf("%s: an element printing an unbreakable %d-char string can neither "+
					"break nor truncate:\n  <%s", name, len(long), chunk)
			}
		}
	}
}

// TestFixedWidthControlsCanShrink pins F11: a bare w-80 is a FIXED 320px, which
// overflows the ~326px box a card leaves on a 360/375px device.
func TestFixedWidthControlsCanShrink(t *testing.T) {
	pages := map[string]string{
		"labeledInput":   renderString(t, labeledInput("model", "Model id", "12345", true)),
		"outputs filter": renderString(t, outputsGalleryPage(nil, nil, "", 0, 0, "csrf", "dark", NSFWBlur)),
	}
	for name, html := range pages {
		for _, chunk := range strings.Split(html, "<") {
			if !strings.Contains(chunk, "w-80") {
				continue
			}
			if !strings.Contains(chunk, "max-w-full") {
				t.Errorf("%s: a fixed w-80 (320px) without max-w-full overflows a 360px phone:\n  <%s", name, chunk)
			}
		}
	}
	// And the guard is worthless if neither site rendered a w-80 at all.
	if !strings.Contains(pages["labeledInput"], "w-80") {
		t.Fatal("labeledInput no longer renders w-80 — this test is vacuous, re-point it")
	}
}

// ---------------------------------------------------------------------------
// F12 — the graph SVG must actually be pannable
// ---------------------------------------------------------------------------

// TestGraphSVGCanOverflowItsScroller pins F12. width:100%;max-width:100% inside an
// overflow-auto container makes the SVG mathematically incapable of exceeding the
// container, so overflow-x never engages: a 4000px graph shrank to ~8% with
// sub-pixel text and nothing to pan.
func TestGraphSVGCanOverflowItsScroller(t *testing.T) {
	// A wide graph: two nodes ~3000px apart.
	wide := []byte(`{"nodes":[
		{"id":1,"type":"A","pos":[0,0],"size":[200,90]},
		{"id":2,"type":"B","pos":[3000,0],"size":[200,90]}]}`)
	out := renderString(t, workflowGraphSection(wide, store.WorkflowFormatUI))
	if !strings.Contains(out, "overflow-auto") {
		t.Fatalf("the graph container must be a scroller:\n%s", out)
	}
	if !strings.Contains(out, "min-width:900px") {
		t.Errorf("a >900px graph must get the full min-width floor so its container can scroll:\n%s", out)
	}

	// A NARROW graph must NOT be stretched — the floor is a cap, not a fixed width.
	narrow := []byte(`{"nodes":[{"id":1,"type":"A","pos":[0,0],"size":[200,90]}]}`)
	out = renderString(t, workflowGraphSection(narrow, store.WorkflowFormatUI))
	if strings.Contains(out, "min-width:900px") {
		t.Errorf("a small graph must keep its natural width, not be stretched to 900px:\n%s", out)
	}
	if !strings.Contains(out, "min-width:") {
		t.Errorf("the small graph should still carry its own (natural) min-width floor:\n%s", out)
	}
}

// ---------------------------------------------------------------------------
// F13 / F8 — vertical budget and tap targets
// ---------------------------------------------------------------------------

// TestShowcaseHeightStepsUpAtSm pins F13: 22rem (352px) at EVERY viewport spent
// 42% of a 390x844 phone on one showcase strip.
func TestShowcaseHeightStepsUpAtSm(t *testing.T) {
	css := appCSS(t)
	i := strings.Index(css, ".cm-showcase-lg .cm-carousel-item {")
	if i < 0 {
		t.Fatal("app.css lost the .cm-showcase-lg rule")
	}
	block := css[i:]
	if end := strings.Index(block, "\n/*"); end > 0 {
		block = block[:end]
	}
	if !strings.Contains(block, "height: 14rem;") {
		t.Errorf("the phone default must be 14rem, not 22rem:\n%s", block)
	}
	if !strings.Contains(block, "@media (min-width: 640px)") || !strings.Contains(block, "height: 22rem;") {
		t.Errorf("22rem must be gated behind the sm breakpoint:\n%s", block)
	}
}

// TestCarouselButtonTapTarget pins F8's floor: the carousel arrows were 28x28,
// the smallest control in the app, on a strip whose purpose is being poked.
func TestCarouselButtonTapTarget(t *testing.T) {
	css := appCSS(t)
	// The BASE rule — identified by the position:absolute only it declares. The
	// coarse-pointer block overrides it separately and is asserted below.
	i := strings.Index(css, ".cm-carousel-btn {\n  position: absolute;")
	if i < 0 {
		t.Fatal("app.css lost the base .cm-carousel-btn rule")
	}
	block := css[i : i+400]
	if strings.Contains(block, "width: 1.75rem") {
		t.Error(".cm-carousel-btn is back to 28x28 — below every touch-target guideline")
	}
	if !strings.Contains(block, "width: 2.25rem") || !strings.Contains(block, "height: 2.25rem") {
		t.Errorf(".cm-carousel-btn must be >= 2.25rem (36px):\n%s", block)
	}
	if !strings.Contains(css, "@media (pointer: coarse)") {
		t.Error("app.css must raise the sm/md control floor for coarse pointers (F8)")
	}
}

// ---------------------------------------------------------------------------
// F10 — a truncated path must stay recoverable
// ---------------------------------------------------------------------------

// TestTruncatedPathsCarryATitle pins F10. Right-truncating a model path discards
// the filename at the end — the only part that identifies it — and there was no
// title, no wrap and no way to see the rest.
func TestTruncatedPathsCarryATitle(t *testing.T) {
	const p = "/mnt/models/checkpoints/some_very_long_directory_name/sd_xl_base_1.0.safetensors"
	cell := renderString(t, pathCell(g.Attr("class", "cm-path-ellipsis truncate max-w-lg"), p))
	if !strings.Contains(cell, `title="`+p+`"`) {
		t.Errorf("a truncated path cell must carry the FULL path as a title:\n%s", cell)
	}
	if !strings.Contains(cell, "cm-path-ellipsis") {
		t.Errorf("a truncated path cell must flip the ellipsis to the start:\n%s", cell)
	}
	if !strings.Contains(cell, "<bdi>") {
		t.Errorf("the path text must be bidi-isolated or an RTL cell reorders its leading slash:\n%s", cell)
	}
	css := appCSS(t)
	for _, want := range []string{".cm-path-ellipsis {", "direction: rtl;", "text-align: left;"} {
		if !strings.Contains(css, want) {
			t.Errorf("app.css missing .cm-path-ellipsis piece %q", want)
		}
	}
}

// TestEveryTruncatedTextHasATitle sweeps the package: any element carrying the
// `truncate` utility hides content, so it must expose the full value some other
// way (a title tooltip). The exceptions are elements whose text is already short
// and fully repeated elsewhere on the same row.
func TestEveryTruncatedTextHasATitle(t *testing.T) {
	src := goSourceFiles(t)
	// Sites deliberately without a title: their content is a display name already
	// shown in full nearby, not a path.
	allowed := map[string]bool{
		"model_card_pages.go":      true, // version names, repeated in the version list
		"model_pages.go":           true, // file names, with the size + action on the row
		"model_community_pages.go": true, // @username caption over its own tile
		"outputs_pages.go":         true, // generation label, repeated on the detail page
		"pages.go":                 true, // queue item filename, with a progress bar
	}
	for name, body := range src {
		if allowed[name] {
			continue
		}
		lines := strings.Split(body, "\n")
		for i, line := range lines {
			if !strings.Contains(line, `h.Class("`) || !strings.Contains(line, "truncate") {
				continue
			}
			// gofmt splits a long element across lines, so the title may sit just
			// above or below the class — look at a small window, not one line.
			window := strings.Join(lines[max(0, i-1):min(len(lines), i+3)], "\n")
			if !strings.Contains(window, "h.Title(") && !strings.Contains(window, "pathCell(") {
				t.Errorf("%s: a truncated element hides its content with no title=:\n  %s",
					name, strings.TrimSpace(line))
			}
		}
	}
}

func goSourceFiles(t *testing.T) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	out := map[string]string{}
	for _, e := range entries {
		n := e.Name()
		if !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		b, err := os.ReadFile(n)
		if err != nil {
			t.Fatalf("read %s: %v", n, err)
		}
		out[n] = string(b)
	}
	return out
}

// ---------------------------------------------------------------------------
// F18 — bounded progress, guided empty states, scrollable tabs
// ---------------------------------------------------------------------------

// TestScanProgressListIsBounded pins F18a: the poller appended a card per matched
// file into an UNBOUNDED list for as long as the scan ran, walking the Stop button
// off the bottom of the screen.
func TestScanProgressListIsBounded(t *testing.T) {
	var results []library.FileResult
	for i := 0; i < 40; i++ {
		results = append(results, library.FileResult{Name: "f.safetensors", Status: "matched"})
	}
	out := renderString(t, scanScanning(scanSnapshot{Started: true, Running: true, Results: results}, "csrf"))
	if !strings.Contains(out, "max-h-56 space-y-2 overflow-y-auto") {
		t.Errorf("the live scan result list must be height-capped and scroll on its own:\n%s", out)
	}
	// The Stop control must still be in the fragment (it is what the cap protects).
	if !strings.Contains(out, "/library/scan/stop") {
		t.Errorf("the scanning fragment must still carry the Stop control:\n%s", out)
	}
}

// TestEmptyStatesGuideTheUser pins F18b. /trash was one <p> ("Trash is empty."),
// and the outputs gallery and a no-result search were the same — a bare sentence
// that tells a first-time user nothing about what the feature does or what to do.
func TestEmptyStatesGuideTheUser(t *testing.T) {
	cases := map[string]struct {
		html    string
		heading string
		cta     string
	}{
		"trash": {
			renderString(t, trashPage(nil, "csrf", "dark", NSFWBlur)),
			"Nothing in the trash", "/library?tab=files",
		},
		"outputs": {
			renderString(t, outputsGalleryPage(nil, nil, "", 0, 0, "csrf", "dark", NSFWBlur)),
			"No generations yet", "/library?tab=workflows",
		},
		"search no results": {
			renderString(t, searchResults(&civitai.ModelSearchResult{}, nil, NSFWBlur, "csrf", "")),
			"No models matched that search", "/search",
		},
	}
	for name, c := range cases {
		if !strings.Contains(c.html, c.heading) {
			t.Errorf("%s empty state has no heading %q", name, c.heading)
		}
		if !strings.Contains(c.html, `href="`+c.cta+`"`) {
			t.Errorf("%s empty state has no primary CTA to %s", name, c.cta)
		}
		// A CTA that is not actually a button is just another sentence.
		if !strings.Contains(c.html, `data-civitai-ui="button"`) {
			t.Errorf("%s empty state's CTA is not rendered as a button", name)
		}
		// It must explain, not just label: the old versions were one short sentence.
		if !strings.Contains(c.html, "mx-auto mt-1 mb-3 max-w-md text-sm text-slate-400") {
			t.Errorf("%s empty state has no explanation paragraph", name)
		}
	}
	// The old bare strings must be gone.
	if strings.Contains(cases["trash"].html, "Trash is empty.") {
		t.Error("the bare `Trash is empty.` paragraph is back")
	}
	if strings.Contains(cases["search no results"].html, ">No results.<") {
		t.Error("the bare `No results.` paragraph is back")
	}
}

// TestLibraryTabStripScrolls pins F18c: three tab labels plus gap-6 do not fit
// 390px, and the strip had no overflow handling at all.
func TestLibraryTabStripScrolls(t *testing.T) {
	css := appCSS(t)
	i := strings.Index(css, ".lib-tabs {")
	if i < 0 {
		t.Fatal("app.css lost the .lib-tabs rule")
	}
	block := css[i : i+400]
	for _, want := range []string{"flex-wrap: nowrap;", "overflow-x: auto;"} {
		if !strings.Contains(block, want) {
			t.Errorf(".lib-tabs must scroll rather than wrap, missing %q:\n%s", want, block)
		}
	}
	if !strings.Contains(css, "white-space: nowrap;") {
		t.Error(".lib-tab must not wrap inside the scrolling strip")
	}
}

// ---------------------------------------------------------------------------
// F15 — an author's node color still wins over the theme
// ---------------------------------------------------------------------------

// TestGraphThemeDefaultsNeverOverrideAuthorColors pins the invariant the F15 fix
// had to preserve: the theme-aware palette applies ONLY where the graph specified
// no color of its own.
func TestGraphThemeDefaultsNeverOverrideAuthorColors(t *testing.T) {
	authored := []byte(`{"nodes":[{"id":1,"type":"A","pos":[0,0],"size":[200,90],
		"color":"#323","bgcolor":"#535","widgets_values":["v"]}]}`)
	out := renderString(t, workflowGraphSection(authored, store.WorkflowFormatUI))
	for _, want := range []string{`fill="#323"`, `fill="#535"`} {
		if !strings.Contains(out, want) {
			t.Errorf("an author-specified node color must survive: missing %s\n%s", want, out)
		}
	}
	// The author-colored surfaces must NOT be re-painted by the theme rules.
	for _, banned := range []string{"cm-g-body", "cm-g-title", "cm-g-text", "cm-g-widget"} {
		if strings.Contains(out, banned) {
			t.Errorf("an author-colored node must not carry the theme class %q — "+
				"a light-theme flip would repaint the user's own color:\n%s", banned, out)
		}
	}

	// A node with NO colors of its own gets the full theme-aware treatment.
	plain := []byte(`{"nodes":[{"id":1,"type":"A","pos":[0,0],"size":[200,90],
		"inputs":[{"name":"in"}],"outputs":[{"name":"out"}],"widgets_values":["v"]}]}`)
	out = renderString(t, workflowGraphSection(plain, store.WorkflowFormatUI))
	for _, want := range []string{"cm-graph", "cm-g-body", "cm-g-title", "cm-g-text", "cm-g-widget", "cm-g-slot", "cm-g-stroke"} {
		if !strings.Contains(out, want) {
			t.Errorf("an uncolored node must be theme-aware: missing %q\n%s", want, out)
		}
	}
	// The dark literals stay as presentation attributes so the SVG is legible with
	// no stylesheet at all.
	if !strings.Contains(out, `fill="#334155"`) {
		t.Errorf("the dark fallback must remain a presentation attribute:\n%s", out)
	}
	// And both themes must actually be defined.
	css := appCSS(t)
	if !strings.Contains(css, ".cm-graph {") || !strings.Contains(css, `[data-theme="light"] .cm-graph {`) {
		t.Error("app.css must define the graph palette for BOTH themes")
	}
}
