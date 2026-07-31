package web

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

// pageMain returns just the <main>…</main> region of a rendered page — i.e. the
// PAGE BODY without the app shell (nav + the global recent-outputs rail). Tests
// that count page content must scope to it, because the rail is chrome present on
// every page and is deliberately NOT filtered by the page's own query.
func pageMain(body string) string {
	i := strings.Index(body, "<main")
	if i < 0 {
		return body
	}
	j := strings.Index(body[i:], "</main>")
	if j < 0 {
		return body[i:]
	}
	return body[i : i+j]
}

// pageShell returns everything OUTSIDE <main> — the nav and the rail.
func pageShell(body string) string {
	i := strings.Index(body, "<main")
	if i < 0 {
		return body
	}
	j := strings.Index(body[i:], "</main>")
	if j < 0 {
		return body[:i]
	}
	return body[:i] + body[i+j:]
}

func TestRailRendersOnEveryPage(t *testing.T) {
	srv, root := newOutputsServer(t, "127.0.0.1:8787")
	wf := seedWF(t, srv, "alpha")
	genID, _ := seedGen(t, srv, root, &wf, "alpha", []byte("X"))

	paths := []string{
		"/",
		"/search?q=x",
		"/library",
		"/trash",
		"/outputs",
		"/outputs/" + strconv.FormatInt(genID, 10),
		"/workflows/" + strconv.FormatInt(wf, 10),
		"/apps/discover",
	}
	for _, p := range paths {
		t.Run(p, func(t *testing.T) {
			rec := get(t, srv, p)
			if rec.Code != http.StatusOK {
				t.Fatalf("GET %s status = %d", p, rec.Code)
			}
			body := rec.Body.String()
			shell := pageShell(body)
			for _, want := range []string{
				`id="cm-rail"`,         // the sidebar itself
				`class="cm-rail-item"`, // at least one entry
				`href="/outputs/` + strconv.FormatInt(genID, 10) + `"`, // links to the detail page
				`href="/outputs"`,    // "view all"
				`id="cm-rail-scrim"`, // mobile drawer scrim
				`id="cm-rail-open"`,  // nav drawer control
				"cm-shell-rail",      // <body> reserves the rail's width
			} {
				if !strings.Contains(shell, want) {
					t.Errorf("GET %s: shell missing %q", p, want)
				}
			}
		})
	}
}

// TestRailDrawerHasModalSemantics pins the pieces the drawer's a11y depends on.
// The dialog role/aria-modal are applied by the script (only while open — on
// desktop the same element is a static complementary column, where role="dialog"
// would be wrong), so this asserts the script contains them plus the focus
// move/restore and the inert marking of the rest of the page.
func TestRailDrawerHasModalSemantics(t *testing.T) {
	srv, root := newOutputsServer(t, "127.0.0.1:8787")
	wf := seedWF(t, srv, "wf")
	seedGen(t, srv, root, &wf, "wf", []byte("X"))
	shell := pageShell(get(t, srv, "/").Body.String())

	for _, want := range []string{
		`aria-controls="cm-rail"`,     // the opener names its target
		`aria-expanded="false"`,       // …and reports state
		`id="cm-rail-close"`,          // focus lands here on open
		`aria-label="Recent outputs"`, // the rail is a labelled landmark
		"cmRailPrevFocus",             // focus is restored on close
		`setAttribute('role', 'dialog')`,
		`setAttribute('aria-modal', 'true')`,
		"inert",              // the rest of the page is removed from the tab order
		"e.key === 'Escape'", // Escape closes
	} {
		if !strings.Contains(shell, want) {
			t.Errorf("rail drawer missing %q", want)
		}
	}
}

func TestRailShowsGenerationsFromEveryWorkflow(t *testing.T) {
	srv, root := newOutputsServer(t, "127.0.0.1:8787")
	wfA := seedWF(t, srv, "alpha")
	wfB := seedWF(t, srv, "beta")
	seedGen(t, srv, root, &wfA, "alpha", []byte("A"))
	seedGen(t, srv, root, &wfB, "beta", []byte("B"))

	// The rail is GLOBAL: even on workflow alpha's detail page it lists beta's
	// generation too (that is exactly what replaced the per-workflow card).
	shell := pageShell(get(t, srv, "/workflows/"+strconv.FormatInt(wfA, 10)).Body.String())
	for _, want := range []string{"alpha", "beta"} {
		if !strings.Contains(shell, want) {
			t.Errorf("global rail missing generation from %q", want)
		}
	}
}

func TestRailEmptyStateRendersNothing(t *testing.T) {
	srv, _ := newOutputsServer(t, "127.0.0.1:8787")
	seedWF(t, srv, "wf") // a workflow but no generations — a fresh install

	for _, p := range []string{"/", "/outputs", "/library"} {
		body := get(t, srv, p).Body.String()
		for _, bad := range []string{
			`id="cm-rail"`, `id="cm-rail-scrim"`, `id="cm-rail-open"`,
			"cm-shell-rail", "cmRailDrawer",
		} {
			if strings.Contains(body, bad) {
				t.Errorf("GET %s on a fresh install must not render rail markup, found %q", p, bad)
			}
		}
	}
}

func TestRailHonorsNSFWModes(t *testing.T) {
	srv, root := newOutputsServer(t, "127.0.0.1:8787")
	wf := seedWF(t, srv, "wf")
	genID, _ := seedGen(t, srv, root, &wf, "wf", []byte("X"))
	imgURL := "/outputs/img/"

	rd := srv.rail(context.Background())
	if len(rd.Groups) != 1 {
		t.Fatalf("fixture: rail has %d entries, want 1", len(rd.Groups))
	}

	t.Run("hide OMITS the rail server-side", func(t *testing.T) {
		// Rendered through a real page builder, so the shell class and the nav
		// control are covered too — not only the aside.
		out := renderString(t, dashboardPage(nil, nil, "csrf", "dark", NSFWHide, rd))
		for _, bad := range []string{
			`id="cm-rail"`, "cm-rail-item", "cm-shell-rail", `id="cm-rail-open"`,
			"cmRailDrawer", // the drawer script ships with the rail — it must go too
			imgURL, "/outputs/" + strconv.FormatInt(genID, 10),
		} {
			if strings.Contains(out, bad) {
				t.Errorf("NSFW hide must OMIT the rail markup entirely, found %q", bad)
			}
		}
		// And the component itself renders nothing at all.
		if n := outputsRail(rd, "csrf", NSFWHide); n != nil {
			t.Error("outputsRail must return nil under NSFW hide")
		}
		if rd.visible(NSFWHide) {
			t.Error("railData.visible must be false under NSFW hide")
		}
	})

	t.Run("blur renders blurred", func(t *testing.T) {
		out := renderString(t, dashboardPage(nil, nil, "csrf", "dark", NSFWBlur, rd))
		if !strings.Contains(out, `id="cm-rail"`) {
			t.Fatal("blur mode must still render the rail")
		}
		if !strings.Contains(out, `data-nsfw="blur"`) {
			t.Error("blur mode must mark the rail so .cm-out-thumb is blurred")
		}
		if !strings.Contains(out, imgURL) {
			t.Error("blur mode blurs the pixels but still serves the thumbnail")
		}
	})

	t.Run("show renders plain", func(t *testing.T) {
		out := renderString(t, dashboardPage(nil, nil, "csrf", "dark", NSFWShow, rd))
		if !strings.Contains(out, `id="cm-rail"`) {
			t.Fatal("show mode must render the rail")
		}
		if strings.Contains(out, `data-nsfw="blur"`) {
			t.Error("show mode must render plain — no blur marker")
		}
	})
}

func TestRailIsBoundedToLimit(t *testing.T) {
	srv, root := newOutputsServer(t, "127.0.0.1:8787")
	wf := seedWF(t, srv, "wf")
	const seeded = outputsRailLimit + 9
	for i := 0; i < seeded; i++ {
		seedGen(t, srv, root, &wf, "wf"+strconv.Itoa(i), []byte("X"))
	}
	rd := srv.rail(context.Background())
	if len(rd.Groups) != outputsRailLimit {
		t.Fatalf("rail loaded %d entries, want the bounded %d", len(rd.Groups), outputsRailLimit)
	}
	shell := pageShell(get(t, srv, "/").Body.String())
	if n := strings.Count(shell, `class="cm-rail-item"`); n != outputsRailLimit {
		t.Errorf("rail rendered %d tiles, want at most %d", n, outputsRailLimit)
	}
}

// ── batch collapsing ────────────────────────────────────────────────────────
//
// The rail shows outputsRailLimit ENTRIES, and a "Queue ×N" batch is ONE entry.
// Before this, an N-item batch ate N of the 12 slots (seen live with a 3-item
// batch), which is what these tests pin.

// railItemHrefs returns the href of every rail entry, in render order. Scoped to
// the shell so nav/page links can never be mistaken for rail entries.
func railItemHrefs(t *testing.T, shell string) []string {
	t.Helper()
	var out []string
	rest := shell
	for {
		i := strings.Index(rest, `class="cm-rail-item"`)
		if i < 0 {
			return out
		}
		// The anchor opens before the class attribute: href="…" class="cm-rail-item".
		open := strings.LastIndex(rest[:i], "<a ")
		if open < 0 {
			t.Fatalf("a cm-rail-item that is not inside an <a>: %s", firstN(rest[:i], 200))
		}
		tag := rest[open:i]
		j := strings.Index(tag, `href="`)
		if j < 0 {
			t.Fatalf("rail entry with no href: %q", tag)
		}
		k := strings.Index(tag[j+6:], `"`)
		out = append(out, tag[j+6:j+6+k])
		rest = rest[i+1:]
	}
}

// TestRailCollapsesABatchToOneEntry is the headline behaviour: N runs of one
// batch become ONE tile with a ×N badge linking to the batch view, while ordinary
// runs beside it keep their exact previous markup.
//
// The batch is seeded STOPPED — 3 captured of 8 requested — so the badge's N is
// pinned to what the rail actually collapsed. A ×8 badge over three images would
// claim five outputs that were never made.
func TestRailCollapsesABatchToOneEntry(t *testing.T) {
	srv, root := newOutputsServer(t, "127.0.0.1:8787")
	wf := seedWF(t, srv, "solo-wf")
	soloID, _ := seedGen(t, srv, root, &wf, "solo", []byte("S")) // oldest
	batchIDs := seedBatch(t, srv, root, "railbatch", "Hi-res 8-step", 3, 8)

	rd := srv.rail(context.Background())
	if len(rd.Groups) != 2 {
		t.Fatalf("rail has %d entries, want 2 (one collapsed batch + one solo run)", len(rd.Groups))
	}
	if rd.Groups[0].Count != 3 {
		t.Errorf("batch entry collapsed %d rows, want 3", rd.Groups[0].Count)
	}
	if rd.Groups[1].Count != 1 {
		t.Errorf("solo entry collapsed %d rows, want 1", rd.Groups[1].Count)
	}

	shell := pageShell(get(t, srv, "/").Body.String())
	if n := strings.Count(shell, `class="cm-rail-item"`); n != 2 {
		t.Fatalf("rail rendered %d tiles, want 2 — a 3-item batch must not take 3 slots", n)
	}
	// ONE tile, linking to the batch view, badged ×3.
	if !strings.Contains(shell, `href="/outputs/batch/railbatch"`) {
		t.Error("the collapsed batch tile must link to /outputs/batch/railbatch")
	}
	if !strings.Contains(shell, `class="cm-rail-badge"`) || !strings.Contains(shell, "×3") {
		t.Errorf("the collapsed batch tile must carry a ×3 badge; shell = %q", firstN(shell, 1200))
	}
	if strings.Contains(shell, "×8") {
		t.Error("the badge must report the CAPTURED count (3), not batch_total (8) — " +
			"a batch stopped at 3 of 8 must not claim eight outputs")
	}
	if n := strings.Count(shell, "cm-rail-badge"); n != 1 {
		t.Errorf("%d badges rendered, want exactly 1 (the solo run must not get one)", n)
	}
	// No member of the batch appears as its own rail entry any more.
	for _, id := range batchIDs {
		if strings.Contains(shell, `href="/outputs/`+strconv.FormatInt(id, 10)+`"`) {
			t.Errorf("generation %d is a batch member and must not have its own rail entry", id)
		}
	}
	// …and a non-batch run is untouched: same detail link, no badge.
	if !strings.Contains(shell, `href="/outputs/`+strconv.FormatInt(soloID, 10)+`"`) {
		t.Error("a non-batch run must keep its own /outputs/{id} rail entry")
	}
}

// TestRailOverFetchesSoBatchesCannotUnderFillIt is the test that JUSTIFIES the
// design: the rail reads railFetchLimit rows and clamps to outputsRailLimit
// GROUPS, rather than reading outputsRailLimit rows and grouping those.
//
// The fixture is 12 two-item batches = 24 rows. A naive "fetch 12, then merge"
// reads only the newest 12 rows — six whole batches — and renders SIX entries,
// leaving half the rail empty. Over-fetching keeps all 12.
func TestRailOverFetchesSoBatchesCannotUnderFillIt(t *testing.T) {
	srv, root := newOutputsServer(t, "127.0.0.1:8787")
	const batches = outputsRailLimit
	for i := 0; i < batches; i++ {
		seedBatch(t, srv, root, "ubatch"+strconv.Itoa(i), "preset", 2, 2)
	}

	rd := srv.rail(context.Background())
	if len(rd.Groups) != outputsRailLimit {
		t.Fatalf("rail has %d entries, want %d — a naive 'fetch %d rows then merge' "+
			"yields only %d here (12 batches × 2 rows), under-filling the rail",
			len(rd.Groups), outputsRailLimit, outputsRailLimit, outputsRailLimit/2)
	}
	for i, gr := range rd.Groups {
		if gr.Count != 2 {
			t.Errorf("entry %d collapsed %d rows, want 2", i, gr.Count)
		}
	}
	shell := pageShell(get(t, srv, "/").Body.String())
	if n := strings.Count(shell, `class="cm-rail-item"`); n != outputsRailLimit {
		t.Errorf("rail rendered %d tiles, want a full %d", n, outputsRailLimit)
	}
	if n := strings.Count(shell, "cm-rail-badge"); n != outputsRailLimit {
		t.Errorf("%d ×N badges, want %d (every entry here is a collapsed batch)", n, outputsRailLimit)
	}
}

// TestRailStaysFullAfterOneMaxSizeBatch is the case the ×2 over-fetch got wrong.
// railFetchLimit is tied to maxBatchCount, so the LARGEST batch the UI can queue
// must still leave a FULL rail: one collapsed entry plus outputsRailLimit-1 solo
// runs. Every number is derived from the constants, so raising maxBatchCount moves
// the fixture with it instead of leaving a test that silently stops covering the
// worst case.
//
// At railFetchLimit = outputsRailLimit*2 = 24 this rendered ONE entry badged ×24
// — a badge contradicting the batch page it links to — and the shipped "16" quick
// pick left 9 tiles. Both are regressions on EVERY page in the app, since the rail
// is chrome.
func TestRailStaysFullAfterOneMaxSizeBatch(t *testing.T) {
	srv, root := newOutputsServer(t, "127.0.0.1:8787")
	wf := seedWF(t, srv, "wf")
	// Enough solo runs to fill the rail on their own, seeded FIRST so they are the
	// older half and the batch sits newest (what the user sees right after clicking).
	for i := 0; i < outputsRailLimit; i++ {
		seedGen(t, srv, root, &wf, "solo"+strconv.Itoa(i), []byte("S"))
	}
	seedBatch(t, srv, root, "maxbatch", "preset", maxBatchCount, maxBatchCount)

	rd := srv.rail(context.Background())
	if len(rd.Groups) != outputsRailLimit {
		t.Fatalf("after ONE batch of %d the rail shows %d entries, want a full %d — "+
			"railFetchLimit (%d) must hold a max-size batch PLUS a full rail",
			maxBatchCount, len(rd.Groups), outputsRailLimit, railFetchLimit)
	}
	if got := rd.Groups[0].Count; got != maxBatchCount {
		t.Errorf("the batch entry reports ×%d but the batch has %d runs — the badge "+
			"contradicts the batch page it links to", got, maxBatchCount)
	}

	shell := pageShell(get(t, srv, "/").Body.String())
	if n := strings.Count(shell, `class="cm-rail-item"`); n != outputsRailLimit {
		t.Errorf("rail rendered %d tiles, want a full %d", n, outputsRailLimit)
	}
	if !strings.Contains(shell, "×"+strconv.Itoa(maxBatchCount)) {
		t.Errorf("missing the ×%d badge; a truncated count would under-report the batch", maxBatchCount)
	}
	// The shipped quick picks are the realistic way to reach this — 16 is one click.
	for _, n := range batchQuickPicks {
		if n > maxBatchCount {
			t.Errorf("quick pick ×%d exceeds maxBatchCount %d", n, maxBatchCount)
		}
	}
}

// TestRailGroupingPreservesNewestFirstOrder — a batch takes the position of its
// NEWEST member, so collapsing can never silently reorder the rail.
func TestRailGroupingPreservesNewestFirstOrder(t *testing.T) {
	srv, root := newOutputsServer(t, "127.0.0.1:8787")
	wf := seedWF(t, srv, "wf")
	oldID, _ := seedGen(t, srv, root, &wf, "older", []byte("O"))
	seedBatch(t, srv, root, "midbatch", "preset", 3, 3)
	newID, _ := seedGen(t, srv, root, &wf, "newer", []byte("N"))

	shell := pageShell(get(t, srv, "/").Body.String())
	got := railItemHrefs(t, shell)
	want := []string{
		"/outputs/" + strconv.FormatInt(newID, 10),
		"/outputs/batch/midbatch",
		"/outputs/" + strconv.FormatInt(oldID, 10),
	}
	if len(got) != len(want) {
		t.Fatalf("rail entries = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("rail entry %d = %q, want %q (order = %v)", i, got[i], want[i], got)
		}
	}
}

// TestRailSingleItemBatchRendersAsOrdinaryTile — a ×1 badge is noise, and a
// one-run batch has nothing to browse side by side, so a group of one renders
// exactly like a plain run. Seeded as 1-of-8: a STOPPED batch is the realistic
// way to get here, and it must not claim ×8 either.
func TestRailSingleItemBatchRendersAsOrdinaryTile(t *testing.T) {
	srv, root := newOutputsServer(t, "127.0.0.1:8787")
	ids := seedBatch(t, srv, root, "stopped", "preset", 1, 8)

	rd := srv.rail(context.Background())
	if len(rd.Groups) != 1 || rd.Groups[0].Count != 1 {
		t.Fatalf("rail groups = %+v, want one entry collapsing one row", rd.Groups)
	}

	shell := pageShell(get(t, srv, "/").Body.String())
	if !strings.Contains(shell, `href="/outputs/`+strconv.FormatInt(ids[0], 10)+`"`) {
		t.Error("a one-run batch must render as an ordinary tile linking to the run")
	}
	for _, bad := range []string{"cm-rail-badge", "×1", "×8", `href="/outputs/batch/stopped"`} {
		if strings.Contains(shell, bad) {
			t.Errorf("a one-run batch must render as an ordinary tile; found %q", bad)
		}
	}
}

// TestRailStoreReadStaysBounded pins the 🔴 constraint: rail() runs on EVERY page
// render, so its query is a fixed bounded read. The over-fetch must stay under
// store.recentGenerationsCap (50) — otherwise the store would silently clamp it
// and the group clamp would start under-filling again for a reason invisible here.
func TestRailStoreReadStaysBounded(t *testing.T) {
	// A real bound, not a design-lock: the window must be able to fill the rail at
	// all, and must stay under the store's cap. Pinning it to one exact expression
	// would just fail with "want outputsRailLimit*2" the next time the value is
	// tuned, telling the reader nothing about boundedness.
	if railFetchLimit < outputsRailLimit || railFetchLimit > 50 {
		t.Fatalf("railFetchLimit = %d, want between outputsRailLimit (%d) and the "+
			"store's recentGenerationsCap (50)", railFetchLimit, outputsRailLimit)
	}

	srv, root := newOutputsServer(t, "127.0.0.1:8787")
	wf := seedWF(t, srv, "wf")
	const seeded = railFetchLimit + 8
	for i := 0; i < seeded; i++ {
		seedGen(t, srv, root, &wf, "wf"+strconv.Itoa(i), []byte("X"))
	}

	// EXACTLY railFetchLimit rows come back: not more (the read is bounded and does
	// not grow with history) and not fewer (the store's own cap does not bite).
	rows, err := srv.store.ListRecentGenerations(context.Background(), railFetchLimit)
	if err != nil {
		t.Fatalf("ListRecentGenerations: %v", err)
	}
	if len(rows) != railFetchLimit {
		t.Fatalf("the rail's read returned %d rows for a limit of %d — if it is short, "+
			"railFetchLimit has grown past the store's recentGenerationsCap",
			len(rows), railFetchLimit)
	}
	// …and the renderer still shows only outputsRailLimit of them.
	if rd := srv.rail(context.Background()); len(rd.Groups) != outputsRailLimit {
		t.Errorf("rail rendered %d entries from %d seeded rows, want %d",
			len(rd.Groups), seeded, outputsRailLimit)
	}
}

func TestRailCollapseTogglePersistsAndRerenders(t *testing.T) {
	srv, root := newOutputsServer(t, "127.0.0.1:8787")
	wf := seedWF(t, srv, "wf")
	seedGen(t, srv, root, &wf, "wf", []byte("X"))

	// Default: expanded.
	shell := pageShell(get(t, srv, "/").Body.String())
	if !strings.Contains(shell, `data-collapsed="false"`) ||
		!strings.Contains(shell, "cm-shell-rail") ||
		strings.Contains(shell, "cm-shell-rail-collapsed") {
		t.Fatalf("rail should start expanded; shell = %q", firstN(shell, 600))
	}
	// The control POSTs the NEXT state, like the theme/NSFW toggles.
	if !strings.Contains(shell, `hx-post="/settings/outputs-rail"`) {
		t.Error("collapse control must POST /settings/outputs-rail")
	}
	if !strings.Contains(shell, `&#34;collapsed&#34;:&#34;true&#34;`) {
		t.Error("expanded rail's control must POST collapsed=true")
	}

	rec := post(t, srv, "/settings/outputs-rail", url.Values{"collapsed": {"true"}}, true)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("collapse POST status = %d, want 204", rec.Code)
	}
	if rec.Header().Get("HX-Refresh") != "true" {
		t.Error("collapse POST must reply HX-Refresh so the shell re-renders")
	}
	if v, _ := srv.store.GetSettingDefault(outputsRailSettingKey, ""); v != "true" {
		t.Errorf("collapsed setting = %q, want \"true\" (persisted to the settings store)", v)
	}

	// Re-render: collapsed markup + the narrow shell reservation.
	shell = pageShell(get(t, srv, "/").Body.String())
	if !strings.Contains(shell, `data-collapsed="true"`) {
		t.Error("re-rendered rail missing data-collapsed=true")
	}
	if !strings.Contains(shell, "cm-shell-rail-collapsed") {
		t.Error("collapsed shell must reserve the narrow width")
	}
	if !strings.Contains(shell, `&#34;collapsed&#34;:&#34;false&#34;`) {
		t.Error("collapsed rail's control must POST collapsed=false (expand)")
	}
	// The entries are still in the markup (CSS hides them) so expanding is instant,
	// but the vertical label identifies the collapsed edge.
	if !strings.Contains(shell, "cm-rail-vlabel") {
		t.Error("collapsed rail missing its vertical label")
	}

	// Expand again.
	if rec := post(t, srv, "/settings/outputs-rail", url.Values{"collapsed": {"false"}}, true); rec.Code != http.StatusNoContent {
		t.Fatalf("expand POST status = %d, want 204", rec.Code)
	}
	shell = pageShell(get(t, srv, "/").Body.String())
	if !strings.Contains(shell, `data-collapsed="false"`) || strings.Contains(shell, "cm-shell-rail-collapsed") {
		t.Error("rail did not return to the expanded state")
	}
}

func TestRailCollapseRejectsWithoutCSRF(t *testing.T) {
	srv, root := newOutputsServer(t, "127.0.0.1:8787")
	wf := seedWF(t, srv, "wf")
	seedGen(t, srv, root, &wf, "wf", []byte("X"))

	rec := post(t, srv, "/settings/outputs-rail", url.Values{"collapsed": {"true"}}, false)
	if rec.Code != http.StatusForbidden {
		t.Errorf("collapse without CSRF status = %d, want 403", rec.Code)
	}
	if v, _ := srv.store.GetSettingDefault(outputsRailSettingKey, "unset"); v != "unset" {
		t.Errorf("a CSRF-rejected POST must not persist anything; setting = %q", v)
	}
}

// TestWorkflowDetailStripAndGlobalRailCoexist replaces the former
// TestWorkflowDetailNoLongerHasPerWorkflowOutputsCard.
//
// That test pinned a removal which has since been REVERSED on purpose: the global
// rail is CROSS-workflow chrome ("what did I make recently") and never answered
// "what has THIS workflow made", so it did not actually supersede the per-workflow
// card. Both surfaces now render, and this pins that they coexist rather than one
// having eaten the other.
func TestWorkflowDetailStripAndGlobalRailCoexist(t *testing.T) {
	srv, root := newOutputsServer(t, "127.0.0.1:8787")
	wf := seedWF(t, srv, "wf")
	seedGen(t, srv, root, &wf, "wf", []byte("X"))

	body := get(t, srv, "/workflows/"+strconv.FormatInt(wf, 10)).Body.String()
	main := pageMain(body)
	if !strings.Contains(main, "Recent outputs") {
		t.Error("the workflow detail body must carry its own per-workflow Recent outputs strip")
	}
	if !strings.Contains(main, "/outputs?workflow="+strconv.FormatInt(wf, 10)) {
		t.Error("the strip must link to the workflow-filtered gallery")
	}
	if !strings.Contains(main, "/outputs/img/") {
		t.Error("the strip must render output thumbnails")
	}
	// …and the GLOBAL rail is still there, in the shell, unchanged.
	if !strings.Contains(pageShell(body), `id="cm-rail"`) {
		t.Error("the global rail must still render alongside the per-workflow strip")
	}
}

func TestShellIsStickyAndWide(t *testing.T) {
	srv, _ := newOutputsServer(t, "127.0.0.1:8787")
	body := get(t, srv, "/").Body.String()

	if strings.Contains(body, "max-w-6xl") {
		t.Error("the shell must no longer use the 1152px measure")
	}
	// Nav and main must share the SAME measure, or the bar is indented against the
	// content at wide viewports.
	if n := strings.Count(body, "max-w-[1800px]"); n != 2 {
		t.Errorf("max-w-[1800px] appears %d times, want 2 (navbar inner + <main>)", n)
	}
	for _, want := range []string{`class="cm-nav `, "cm-nav-inner", "cm-navlinks"} {
		if !strings.Contains(body, want) {
			t.Errorf("shell missing %q (sticky nav / mobile link strip)", want)
		}
	}
	// The narrow-viewport link strip must not force the bar itself to overflow.
	if !strings.Contains(body, "overflow-x-auto") {
		t.Error("nav link strip must be horizontally scrollable at narrow widths")
	}
	// The vanishing-on-hover link colour must be gone (white == light surface).
	if strings.Contains(body, "hover:text-white") {
		t.Error("nav links must not use hover:text-white (1.00:1 in the light theme)")
	}
}

// firstN truncates a rendered page for a readable failure message.
func firstN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// TestRailNeverBreaksAPageOnStoreError proves the rail degrades instead of
// failing the render when its query cannot run.
func TestRailNeverBreaksAPageOnStoreError(t *testing.T) {
	srv, root := newOutputsServer(t, "127.0.0.1:8787")
	wf := seedWF(t, srv, "wf")
	seedGen(t, srv, root, &wf, "wf", []byte("X"))
	if err := srv.store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	rd := srv.rail(context.Background())
	if len(rd.Groups) != 0 {
		t.Errorf("a failed rail query must degrade to no rail, got %d entries", len(rd.Groups))
	}
	if rd.visible(NSFWBlur) {
		t.Error("a degraded rail must not be visible")
	}
}
