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
	if len(rd.Gens) != 1 {
		t.Fatalf("fixture: rail has %d generations, want 1", len(rd.Gens))
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
			t.Error("blur mode must mark the rail so .cm-rail-thumb is blurred")
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
	if len(rd.Gens) != outputsRailLimit {
		t.Fatalf("rail loaded %d generations, want the bounded %d", len(rd.Gens), outputsRailLimit)
	}
	shell := pageShell(get(t, srv, "/").Body.String())
	if n := strings.Count(shell, `class="cm-rail-item"`); n != outputsRailLimit {
		t.Errorf("rail rendered %d tiles, want at most %d", n, outputsRailLimit)
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

func TestWorkflowDetailNoLongerHasPerWorkflowOutputsCard(t *testing.T) {
	srv, root := newOutputsServer(t, "127.0.0.1:8787")
	wf := seedWF(t, srv, "wf")
	seedGen(t, srv, root, &wf, "wf", []byte("X"))

	body := get(t, srv, "/workflows/"+strconv.FormatInt(wf, 10)).Body.String()
	main := pageMain(body)
	if strings.Contains(main, "Recent outputs") {
		t.Error("the per-workflow Recent outputs card must be GONE from the workflow detail body")
	}
	if strings.Contains(main, "View all 1 →") {
		t.Error("the per-workflow View-all link must be gone")
	}
	if strings.Contains(main, "/outputs/img/") {
		t.Error("no output thumbnails belong in the workflow detail body any more")
	}
	// …and the global rail took over.
	if !strings.Contains(pageShell(body), `id="cm-rail"`) {
		t.Error("the global rail must supersede the removed card")
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
	if len(rd.Gens) != 0 {
		t.Errorf("a failed rail query must degrade to no rail, got %d rows", len(rd.Gens))
	}
	if rd.visible(NSFWBlur) {
		t.Error("a degraded rail must not be visible")
	}
}
