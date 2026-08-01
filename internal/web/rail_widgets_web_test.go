package web

import (
	"strings"
	"testing"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/store"
)

// railFixture builds a rail carrying BOTH widgets' data, so a test asserting one
// widget cannot pass because the other one happened to render.
func railFixture(t *testing.T) railData {
	t.Helper()
	now := time.Now()
	groups := []railGroup{
		{Rep: genFixture(11, "alpha", now.Add(-1*time.Minute)), Count: 1},
		{Rep: genFixture(12, "beta", now.Add(-9*time.Minute)), Count: 1},
	}
	acts := []activityEntry{
		{Kind: activityRun, Subject: "wf", Text: "wf", Href: "/outputs/11", TS: now, Count: 3},
	}
	return railData{Groups: groups, Activity: acts}
}

// genFixture is a generation with a real first image, so generationThumb renders a
// thumbnail rather than the "no output" placeholder — without it the preview tests
// would assert against an empty box and prove nothing.
func genFixture(id int64, workflow string, at time.Time) store.Generation {
	wid := id
	return store.Generation{
		ID: id, WorkflowID: &wid, WorkflowName: workflow, CreatedAt: at,
		FirstImageID: id * 10, FirstImageContentType: "image/png",
	}
}

// videoGenFixture is genFixture whose first output is a VIDEO, which is the only
// shape that exercises the lazy data-src path.
func videoGenFixture(id int64, workflow string, at time.Time) store.Generation {
	gen := genFixture(id, workflow, at)
	gen.FirstImageContentType = "video/mp4"
	return gen
}

// TestRailIsALeftHandColumn pins the SIDE, which lives entirely in CSS.
//
// 🔴 IT CHECKS ALL THREE COUPLED DECLARATIONS, not just one. The rail's side is
// `left`/`border-right`/negative `translateX` on .cm-rail PLUS `padding-left` on
// the two shell classes; asserting only one of them would stay green for a
// half-moved rail that reserves space on one side and paints on the other — which
// is a real, visible bug and the one this test exists to prevent.
func TestRailIsALeftHandColumn(t *testing.T) {
	b, err := assetsFS.ReadFile("assets/app.css")
	if err != nil {
		t.Fatalf("read app.css: %v", err)
	}
	css := string(b)

	rail := cssRuleIn(t, css, ".cm-rail {")
	for _, want := range []string{"left: 0;", "border-right: 1px solid"} {
		if !strings.Contains(rail, want) {
			t.Errorf(".cm-rail must be a LEFT column — missing %q. Got:\n%s", want, rail)
		}
	}
	// The old right-hand declarations must be GONE, not merely joined by new ones:
	// `left: 0` alongside a surviving `right: 0` stretches the rail across the
	// whole viewport.
	for _, gone := range []string{"right: 0;", "border-left: 1px solid"} {
		if strings.Contains(rail, gone) {
			t.Errorf(".cm-rail still carries the right-hand declaration %q — a rail with BOTH "+
				"left and right pinned spans the entire viewport. Got:\n%s", gone, rail)
		}
	}
	// The off-canvas drawer must slide in from the LEFT.
	if !strings.Contains(rail, "translateX(-100%)") {
		t.Errorf(".cm-rail's closed drawer must be translateX(-100%%) so it sits off-canvas to "+
			"the LEFT; a positive value parks it off the right edge and it slides in from the "+
			"wrong side. Got:\n%s", rail)
	}

	// The shell reservation must name the SAME side.
	shell := cssRuleIn(t, css, ".cm-shell-rail {")
	shellCol := cssRuleIn(t, css, ".cm-shell-rail-collapsed {")
	for name, rule := range map[string]string{".cm-shell-rail": shell, ".cm-shell-rail-collapsed": shellCol} {
		if !strings.Contains(rule, "padding-left:") {
			t.Errorf("%s must reserve the rail's width with padding-LEFT, matching .cm-rail's "+
				"`left: 0`. Got:\n%s", name, rule)
		}
		if strings.Contains(rule, "padding-right:") {
			t.Errorf("%s still reserves space on the RIGHT while the rail paints on the LEFT — "+
				"the content would be inset away from the rail and overlapped by it. Got:\n%s", name, rule)
		}
	}
}

// TestRailRendersBothWidgetsAndKeepsTheOutputsLink covers the container shape and
// the one link the rail must never lose.
func TestRailRendersBothWidgetsAndKeepsTheOutputsLink(t *testing.T) {
	out := renderString(t, outputsRail(railFixture(t), "csrf"))

	if n := strings.Count(out, `class="cm-rail-widget"`); n != 2 {
		t.Fatalf("expected 2 widgets in the rail container, got %d:\n%s", n, out)
	}
	// 🔴 The rail heading links to /outputs and that is LOAD-BEARING: "Outputs"
	// left the nav, so this is the app's only in-app entry point to the gallery.
	if !strings.Contains(out, `<a href="/outputs" class="cm-rail-title cm-rail-title-link"`) {
		t.Errorf("the outputs widget's HEADING must be a link to /outputs — it is the app's only "+
			"in-app entry point to the gallery:\n%s", out)
	}
	if !strings.Contains(out, "Recent activity") {
		t.Errorf("the activity widget is missing:\n%s", out)
	}
}

// TestRailOutputsLinkSurvivesAnEmptyOutputsWidget is the case the container shape
// created and the old rail could not reach: a user with ACTIVITY but no
// generations. The rail now renders for them, and the heading link must render
// with it — otherwise /outputs would again have no in-app route.
func TestRailOutputsLinkSurvivesAnEmptyOutputsWidget(t *testing.T) {
	rd := railData{Activity: []activityEntry{
		{Kind: activityDownload, Subject: "m:1", Text: "thing.safetensors", TS: time.Now(), Count: 1},
	}}
	if !rd.visible() {
		t.Fatal("a rail with activity but no generations must still be visible — otherwise the " +
			"activity widget can never render and /outputs has no in-app link")
	}
	out := renderString(t, outputsRail(rd, "csrf"))
	if !strings.Contains(out, `href="/outputs"`) {
		t.Errorf("the outputs widget must render its heading link even with no tiles:\n%s", out)
	}
	if !strings.Contains(out, "No generations yet.") {
		t.Errorf("an empty outputs widget should say so rather than render a bare heading:\n%s", out)
	}
}

// TestRailCollapsedShowsExactlyOnePreview: the collapsed edge is 2.25rem wide, so
// it shows ONE thumbnail — not a column of unreadable slivers, and not zero.
func TestRailCollapsedShowsExactlyOnePreview(t *testing.T) {
	rd := railFixture(t)
	rd.Collapsed = true
	out := renderString(t, outputsRail(rd, "csrf"))

	if n := strings.Count(out, `class="cm-rail-preview"`); n != 1 {
		t.Fatalf("the collapsed rail must render EXACTLY ONE preview, got %d:\n%s", n, out)
	}
	// Bound the probe to the preview element itself, not the whole rail: the widget
	// stack is still in the DOM (CSS hides it), so counting thumbnails across the
	// document would measure the outputs widget's tiles instead.
	prev := divExtentAt(t, out, strings.Index(out, `class="cm-rail-preview"`))
	if n := strings.Count(prev, "cm-out-thumb") + strings.Count(prev, "cm-out-nothumb"); n != 1 {
		t.Errorf("the preview must hold exactly one thumbnail, got %d:\n%s", n, prev)
	}
	// It must be the NEWEST group — the collapsed edge answers "did my last run
	// produce something?", so showing an older one would be a wrong answer.
	//
	// ⚠ Asserted on the newest group's IMAGE URL, and paired with a NEGATIVE on the
	// older one's. A bare Contains(prev, "11") was the first draft and it was
	// vacuous: FirstImageID is 110 for group 11 and 120 for group 12, so "11"
	// matches inside "/outputs/img/110" no matter which group was rendered — and
	// would have matched even if the preview had picked the wrong one.
	newest, oldest := generationImgURL(110), generationImgURL(120)
	if !strings.Contains(prev, newest) {
		t.Errorf("the preview must be built from the NEWEST group (image %s):\n%s", newest, prev)
	}
	if strings.Contains(prev, oldest) {
		t.Errorf("the preview rendered the OLDER group (image %s) — the collapsed edge answers "+
			"\"did my last run produce something?\":\n%s", oldest, prev)
	}

	// Expanded renders NO preview — it would duplicate the first tile.
	rd.Collapsed = false
	if strings.Contains(renderString(t, outputsRail(rd, "csrf")), `class="cm-rail-preview"`) {
		t.Error("the EXPANDED rail must not render the collapsed preview — it would duplicate " +
			"the outputs widget's first tile")
	}
}

// TestRailCollapsedPreviewStaysLazy: the preview is a thumbnail like any other, so
// it must not regress the deferred-video rule. `preload="metadata"` does NOT bound
// a fetch — measured 472,055 bytes transferred for a 471,755-byte clip — so a
// video renders data-src and an IntersectionObserver swaps it in.
func TestRailCollapsedPreviewStaysLazy(t *testing.T) {
	rd := railData{
		Collapsed: true,
		Groups:    []railGroup{{Rep: videoGenFixture(5, "clip", time.Now()), Count: 1}},
	}
	out := renderString(t, outputsRail(rd, "csrf"))
	prev := divExtentAt(t, out, strings.Index(out, `class="cm-rail-preview"`))
	if !strings.Contains(prev, "<video") {
		t.Fatalf("fixture did not reach the video path — the lazy rule is untested:\n%s", prev)
	}
	if !strings.Contains(prev, "data-src") {
		t.Errorf("the preview's video must carry data-src (lazy), not an eager src:\n%s", prev)
	}
	if strings.Contains(prev, "<video src=") || strings.Contains(prev, "<source src=") {
		t.Errorf("the preview regressed to an EAGER video src — preload=metadata does not bound "+
			"the fetch (measured: the whole file transfers):\n%s", prev)
	}
}

// TestRailActivityPollerTargetsAStableContainer is the streaming-job invariant
// applied to the rail: the polling node must never replace ITSELF.
//
// A self-replacing poller stops after exactly ONE tick, and it does so silently —
// the symptom is "the feed just never updates", which reads as a server problem.
func TestRailActivityPollerTargetsAStableContainer(t *testing.T) {
	out := renderString(t, railActivityWidget(railFixture(t).Activity))

	if !strings.Contains(out, `id="`+railActivityBodyID+`"`) {
		t.Fatalf("the activity widget must carry the stable container id %q:\n%s", railActivityBodyID, out)
	}
	if !strings.Contains(out, `hx-get="/fragments/rail-activity"`) {
		t.Errorf("the activity widget must poll /fragments/rail-activity:\n%s", out)
	}
	// 🔴 innerHTML, never outerHTML.
	if !strings.Contains(out, `hx-swap="innerHTML"`) {
		t.Errorf("the poller must swap innerHTML — the polling node carries the hx-trigger, so "+
			"replacing it ends the loop after one tick:\n%s", out)
	}
	if strings.Contains(out, `hx-swap="outerHTML"`) {
		t.Errorf("the poller must NOT swap outerHTML — that deletes the node carrying its own "+
			"trigger and the poll loop stops silently:\n%s", out)
	}

	// And the fragment the server returns must be the INNER content only — if it
	// re-emitted the container, an innerHTML swap would NEST a second polling node
	// on every tick, doubling the poll rate each time.
	frag := renderString(t, railActivityList(railFixture(t).Activity))
	if strings.Contains(frag, `id="`+railActivityBodyID+`"`) {
		t.Errorf("the poll fragment must NOT re-emit the container id — each innerHTML swap would "+
			"nest another polling node and the poll rate would double every tick:\n%s", frag)
	}
	if strings.Contains(frag, "hx-trigger") {
		t.Errorf("the poll fragment must carry no hx-trigger of its own:\n%s", frag)
	}
}

// TestRailActivityCoalescesRuns is the widget's headline behaviour: eight runs of
// one workflow read as ONE line, not eight, so they cannot push every other kind
// of activity out of a column that holds railActivityLimit rows.
func TestRailActivityCoalescesRuns(t *testing.T) {
	now := time.Now()
	// Eight separate runs of "upscale" (each its own group, i.e. NOT already
	// collapsed by the batch grouper) interleaved with one run of "inpaint".
	var groups []railGroup
	for i := 0; i < 8; i++ {
		groups = append(groups, railGroup{
			Rep:   genFixture(int64(100+i), "upscale", now.Add(-time.Duration(i)*time.Minute)),
			Count: 1,
		})
	}
	groups = append(groups, railGroup{
		Rep:   genFixture(200, "inpaint", now.Add(-30*time.Minute)),
		Count: 1,
	})

	got := buildRailActivity(groups, nil, railActivityLimit)

	// Confirm the fixture REACHED the interesting case: nine source rows, and the
	// eight "upscale" ones really do share a subject.
	if len(got) != 2 {
		t.Fatalf("9 runs over 2 workflows must coalesce to 2 entries, got %d: %+v", len(got), got)
	}
	var upscale *activityEntry
	for i := range got {
		if got[i].Count == 8 {
			upscale = &got[i]
		}
	}
	if upscale == nil {
		t.Fatalf("no entry collapsed the 8 same-workflow runs: %+v", got)
	}
	line := activityLine(*upscale)
	if !strings.HasPrefix(line, "8 runs of ") {
		t.Errorf("a coalesced run entry must read \"8 runs of <workflow>\", got %q", line)
	}
	// Newest-first, and the coalesced entry sits at its NEWEST member's position.
	if !got[0].TS.After(got[1].TS) && !got[0].TS.Equal(got[1].TS) {
		t.Errorf("the feed must be newest-first, got %v then %v", got[0].TS, got[1].TS)
	}

	// A SINGLE occurrence must NOT be dressed up as a count — "×1" is pure noise.
	for _, e := range got {
		if e.Count == 1 && strings.Contains(activityLine(e), "1 runs") {
			t.Errorf("a single run must render its plain label, got %q", activityLine(e))
		}
	}
}

// TestRailActivityDoesNotMergeAcrossKinds: a failure must never be counted
// alongside a success for the same model — "3 downloads" while one of them
// errored is a lie the user acts on.
func TestRailActivityDoesNotMergeAcrossKinds(t *testing.T) {
	now := time.Now()
	mid := 7
	evs := []store.Event{
		{ID: 3, TS: now, Level: store.LevelInfo, Kind: "enqueued", ModelID: &mid, Message: "queued a.safetensors"},
		{ID: 2, TS: now.Add(-time.Minute), Level: store.LevelInfo, Kind: "enqueued", ModelID: &mid, Message: "queued b.safetensors"},
		{ID: 1, TS: now.Add(-2 * time.Minute), Level: store.LevelError, Kind: "enqueue_error", ModelID: &mid, Message: "enqueue failed"},
	}
	got := buildRailActivity(nil, evs, railActivityLimit)

	if len(got) != 2 {
		t.Fatalf("two successes for model 7 must coalesce but the FAILURE must stay separate; "+
			"want 2 entries, got %d: %+v", len(got), got)
	}
	var dl, prob *activityEntry
	for i := range got {
		switch got[i].Kind {
		case activityDownload:
			dl = &got[i]
		case activityProblem:
			prob = &got[i]
		}
	}
	if dl == nil || dl.Count != 2 {
		t.Errorf("the two enqueued events for one model must coalesce to a single count-2 entry: %+v", got)
	}
	if prob == nil || prob.Count != 1 {
		t.Errorf("the failure must remain its own count-1 entry — folding it into the successes "+
			"would report a download count that includes an error: %+v", got)
	}
}

// TestRailActivityLinksStaySameOrigin: the feed's TEXT comes from stored,
// externally-influenced messages, so its hrefs are whitelisted to app paths.
func TestRailActivityLinksStaySameOrigin(t *testing.T) {
	for _, bad := range []string{"javascript:alert(1)", "//evil.example/x", "https://evil.example", "relative"} {
		if safeRailHref(bad) {
			t.Errorf("safeRailHref(%q) = true, want false", bad)
		}
	}
	for _, ok := range []string{"/outputs/1", "/models/7", "/outputs?batch=x"} {
		if !safeRailHref(ok) {
			t.Errorf("safeRailHref(%q) = false, want true", ok)
		}
	}
	// An entry whose href is rejected must render as TEXT, not as a dead <a>.
	out := renderString(t, railActivityList([]activityEntry{
		{Kind: activityNote, Subject: "s", Text: "hi", Href: "javascript:alert(1)", TS: time.Now(), Count: 1},
	}))
	if strings.Contains(out, "javascript:") {
		t.Errorf("a rejected href must not reach the DOM:\n%s", out)
	}
}

// TestRailVisibilityTakesNoMaturityRange re-pins the invariant the container
// rework could plausibly have broken: the rail shows the user's OWN generations,
// which nobody rated, so no maturity range reaches it and it renders in full at
// every band — PG-only included.
func TestRailVisibilityTakesNoMaturityRange(t *testing.T) {
	rd := railFixture(t)
	// The signature itself is the guard: visible() takes no argument, so there is
	// nowhere for a range to enter. Assert the rendered rail is byte-identical
	// under the narrowest and widest bands by rendering the whole page shell.
	narrow := renderString(t, page("t", "csrf", maturityRange{Min: maturityPG, Max: maturityPG}, rd))
	wide := renderString(t, page("t", "csrf", fullMaturityRange(), rd))

	nRail := railExtent(t, narrow)
	wRail := railExtent(t, wide)
	if nRail != wRail {
		t.Errorf("the rail differs between a PG-only band and the full band — the user's own " +
			"generations are unrated and OUT OF SCOPE of a CivitAI content scale; feeding a " +
			"range in here silently blanks their own work")
	}
	if !strings.Contains(nRail, "cm-rail-widget") {
		t.Fatalf("fixture did not reach a rendered rail — the comparison above is vacuous:\n%s", nRail)
	}
}

// railExtent slices the <aside id="cm-rail"> element out of a rendered page using
// brace-free tag balancing.
func railExtent(t *testing.T, page string) string {
	t.Helper()
	i := strings.Index(page, `<aside id="cm-rail"`)
	if i < 0 {
		t.Fatalf("no rail in the rendered page")
	}
	j := strings.Index(page[i:], "</aside>")
	if j < 0 {
		t.Fatalf("the rail element is not closed")
	}
	return page[i : i+j]
}

// divExtentAt returns the balanced extent of the element whose opening tag
// contains the byte at idx.
//
// 🔴 It is BRACE-BALANCED, not a pair of strings.Index positions. "between marker
// A and marker B" stays true for an element NESTED inside the first one, so a
// position comparison cannot tell containment from adjacency — the exact vacuity
// mode that has bitten this repo.
func divExtentAt(t *testing.T, s string, idx int) string {
	t.Helper()
	if idx < 0 {
		t.Fatalf("marker not found")
	}
	start := strings.LastIndex(s[:idx], "<")
	if start < 0 {
		t.Fatalf("no opening tag before the marker")
	}
	depth := 0
	for i := start; i < len(s); i++ {
		if strings.HasPrefix(s[i:], "<div") {
			depth++
		} else if strings.HasPrefix(s[i:], "</div>") {
			depth--
			if depth == 0 {
				return s[start : i+6]
			}
		}
	}
	t.Fatalf("element starting at %d is not closed", start)
	return ""
}
