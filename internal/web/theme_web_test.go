package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/store"
	g "maragu.dev/gomponents"
)

// TestCivitaiContractMarkup asserts the @civitai/components attribute contract
// (data-civitai-ui + data-variant/data-size + the input/alert sub-parts) is
// present on each of the five converted component types.
func TestCivitaiContractMarkup(t *testing.T) {
	mid := 42
	subs := []store.Subscription{
		{ID: 1, Kind: store.KindModel, ModelID: &mid, AutoDownload: true, Layout: "default", PollIntervalSecs: 3600},
	}

	// Dashboard exercises button (Subscribe), card, badge (flags), text-input.
	dash := renderString(t, dashboardPage(subs, nil, "csrf", fullMaturityRange()))
	for name, want := range map[string]string{
		"button ui":          `data-civitai-ui="button"`,
		"button variant":     `data-variant="filled"`,
		"button size":        `data-size="md"`,
		"card ui":            `data-civitai-ui="card"`,
		"card border":        `data-with-border="true"`,
		"card padding":       `data-padding="md"`,
		"text-input ui":      `data-civitai-ui="text-input"`,
		"text-input label":   `data-civitai-ui-label`,
		"text-input control": `data-civitai-ui-control`,
	} {
		if !strings.Contains(dash, want) {
			t.Errorf("dashboard missing %s (%q)", name, want)
		}
	}

	// Badge appears in the activity/queue fragments (htmx-loaded).
	ev := renderString(t, eventsFragment([]store.Event{{ID: 1, TS: time.Now(), Level: store.LevelError, Kind: "x", Message: "boom"}}))
	for _, want := range []string{`data-civitai-ui="badge"`, `data-variant="light"`, `data-size="sm"`} {
		if !strings.Contains(ev, want) {
			t.Errorf("events fragment badge missing %q", want)
		}
	}

	// Alert (role + data-color + alert body) via the error banner path.
	al := renderString(t, subscriptionsTable(subs, "boom went the API", "csrf"))
	for _, want := range []string{
		`data-civitai-ui="alert"`, `data-color="error"`, `role="alert"`,
		`data-civitai-ui-alert-body`, "boom went the API",
	} {
		if !strings.Contains(al, want) {
			t.Errorf("alert markup missing %q", want)
		}
	}

	// Badge semantic color via the 0.1.2 native data-color contract (replaces
	// the removed per-element --civitai-color-primary token-override hack).
	gb := renderString(t, badge("done", "green"))
	if !strings.Contains(gb, `data-color="success"`) {
		t.Errorf("green badge should carry data-color=success, got %q", gb)
	}
	if strings.Contains(gb, "--civitai-color-primary:") {
		t.Errorf("green badge must not emit the removed --civitai-color-primary override hack, got %q", gb)
	}
}

// TestBadgeDataColorMapping asserts the app's status badges emit the correct
// @civitai/components 0.1.2 `data-color` intent (info|success|warning|error),
// neutral/brand chips emit NO data-color, and NO badge uses the removed
// per-element --civitai-color-primary token-override hack.
func TestBadgeDataColorMapping(t *testing.T) {
	// Download-queue statuses.
	queue := map[store.QueueStatus]string{
		store.StatusDone:        `data-color="success"`,
		store.StatusFailed:      `data-color="error"`,
		store.StatusDownloading: `data-color="info"`,
		store.StatusQueued:      `data-color="info"`,
		store.StatusSkipped:     `data-color="warning"`,
	}
	for st, want := range queue {
		out := renderString(t, queueStatusBadge(st))
		if !strings.Contains(out, want) {
			t.Errorf("queueStatusBadge(%q) missing %q, got %q", st, want, out)
		}
		if strings.Contains(out, "--civitai-color-primary:") {
			t.Errorf("queueStatusBadge(%q) uses removed token-override hack, got %q", st, out)
		}
	}

	// Event levels.
	levels := map[string]string{
		store.LevelError: `data-color="error"`,
		store.LevelWarn:  `data-color="warning"`,
		store.LevelInfo:  `data-color="info"`,
	}
	for lv, want := range levels {
		out := renderString(t, levelBadge(lv))
		if !strings.Contains(out, want) {
			t.Errorf("levelBadge(%q) missing %q, got %q", lv, want, out)
		}
	}

	// Library candidate reasons.
	cands := map[string]string{
		store.CandidateDuplicate:  `data-color="info"`,
		store.CandidateBroken:     `data-color="warning"`,
		store.CandidateSuperseded: `data-color="warning"`,
	}
	for reason, want := range cands {
		out := renderString(t, candidateBadge(reason))
		if !strings.Contains(out, want) {
			t.Errorf("candidateBadge(%q) missing %q, got %q", reason, want, out)
		}
	}

	// Library file statuses: matched -> success; unmatched -> neutral (no
	// data-color at all).
	matched := renderString(t, statusBadge(store.LocalFile{Status: store.LocalStatusMatched}))
	if !strings.Contains(matched, `data-color="success"`) {
		t.Errorf("matched statusBadge missing data-color=success, got %q", matched)
	}
	unmatched := renderString(t, statusBadge(store.LocalFile{Status: store.LocalStatusUnmatched}))
	if strings.Contains(unmatched, "data-color=") {
		t.Errorf("unmatched statusBadge should be neutral (no data-color), got %q", unmatched)
	}
	if strings.Contains(unmatched, "--civitai-color-primary:") {
		t.Errorf("unmatched statusBadge should not use the removed token-override hack, got %q", unmatched)
	}
}

// TestVendored012DesignSystemFixes guards that the embedded design-system CSS is
// the 0.1.2 vintage: the Badge `data-color` block (F2) and the dark-palette
// `--civitai-color-primary-fg` token (F8) are both present in the bytes shipped
// in the binary.
func TestVendored012DesignSystemFixes(t *testing.T) {
	comp, err := assetsFS.ReadFile("assets/civitai-components.css")
	if err != nil {
		t.Fatalf("read embedded civitai-components.css: %v", err)
	}
	// This comment is unique to the 0.1.2 Badge data-color block.
	if !strings.Contains(string(comp), "mirroring Alert's `data-color` contract") {
		t.Errorf("civitai-components.css is not 0.1.2: missing the Badge data-color block (F2)")
	}
	if !strings.Contains(string(comp), "&[data-color='success']") {
		t.Errorf("civitai-components.css missing data-color intent rules (F2)")
	}

	theme, err := assetsFS.ReadFile("assets/civitai-theme.css")
	if err != nil {
		t.Fatalf("read embedded civitai-theme.css: %v", err)
	}
	// The dark block specifically must ship --civitai-color-primary-fg (F8).
	darkIdx := strings.Index(string(theme), "[data-theme='dark']")
	if darkIdx < 0 {
		t.Fatalf("civitai-theme.css missing [data-theme='dark'] block")
	}
	if !strings.Contains(string(theme)[darkIdx:], "--civitai-color-primary-fg") {
		t.Errorf("civitai-theme.css dark block missing --civitai-color-primary-fg (F8 not present)")
	}
}

// themeRetiredRoutes are the SERVED, network-free GET routes the light-mode
// retirement guard sweeps. Real routes, not page builders: the builders take no
// theme argument any more, so calling one could not observe a handler that had
// somehow reintroduced a light path. Only the served bytes can.
var themeRetiredRoutes = []string{
	librarySubscriptionsHref,
	libraryModelFilesHref,
	libraryWorkflowsHref,
	"/search",
	"/disks",
	"/outputs",
}

// TestLightThemeRetiredFromTheUI is the Task-1 guard. It asserts the THREE things
// the retirement means, each independently:
//
//  1. every served page pins <html data-theme="dark"> and NO served page ever
//     emits data-theme="light";
//  2. the toggle control is gone — no "Switch to <x> theme" accessible name, no
//     sun/moon glyph, and no hx-post to /settings/theme anywhere in the markup;
//  3. POST /settings/theme is no longer routed at all (405), rather than kept as
//     a no-op that would answer 204 and change nothing.
//
// It deliberately does NOT assert anything about the light CSS — that is the
// point of the retirement, and contrast_web_test.go remains its (untouched)
// gate. See TestLightThemeCSSIsRetainedNotDeleted below for the dormancy half.
func TestLightThemeRetiredFromTheUI(t *testing.T) {
	srv := newTestServer(t)

	for _, route := range themeRetiredRoutes {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, route, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d, want 200 (fixture must actually reach a rendered page)", route, rec.Code)
		}
		body := rec.Body.String()

		// (1) the pinned attribute, and the absence of its alternative.
		if !strings.Contains(body, `<html lang="en" data-theme="dark"`) {
			t.Errorf("GET %s: missing the pinned <html lang=\"en\" data-theme=\"dark\">", route)
		}
		if strings.Contains(body, `data-theme="light"`) {
			t.Errorf("GET %s: emitted data-theme=\"light\" — the light path is retired from the UI", route)
		}

		// (2) no toggle, in any of the three shapes it could come back as.
		for _, gone := range []string{
			`aria-label="Switch to light theme"`,
			`aria-label="Switch to dark theme"`,
			"/settings/theme",
			"☀",
			"☾",
		} {
			if strings.Contains(body, gone) {
				t.Errorf("GET %s: still renders theme-toggle artifact %q", route, gone)
			}
		}
	}

	// (3) the route is REMOVED, not a no-op. A no-op would answer 204 here and
	// read as working plumbing forever. Sent WITH a valid CSRF token on purpose —
	// a 403 would prove nothing about routing, only about the CSRF middleware.
	//
	// The expected code is 404, NOT 405: net/http's ServeMux answers 405 only when
	// the PATH matches a registered pattern under a different method, and
	// /settings/theme is now registered under no method at all. (Measured — this
	// assertion was written as 405 first and the test caught it.)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/settings/theme",
		strings.NewReader("theme=light&csrf_token="+srv.csrf))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code == http.StatusNoContent {
		t.Errorf("POST /settings/theme answered 204 — the route was kept as a no-op; it must be removed")
	}
	if rec.Code != http.StatusNotFound {
		t.Errorf("POST /settings/theme status = %d, want 404 (route removed entirely)", rec.Code)
	}
}

// TestLightThemeCSSIsRetainedNotDeleted is the other half of the decision: the
// light path left the UI, and the CSS that implements it stayed. Deleting those
// blocks would silently gut contrast_web_test.go — its 25 light-theme debt
// entries resolve tokens out of these very bytes, and a token that no longer
// exists cannot have a ratio that "moves".
//
// It reads the EMBEDDED assets (what actually ships in the binary), not the
// files on disk.
func TestLightThemeCSSIsRetainedNotDeleted(t *testing.T) {
	for _, asset := range []string{"assets/civitai-theme.css", "assets/app.css"} {
		b, err := assetsFS.ReadFile(asset)
		if err != nil {
			t.Fatalf("read embedded %s: %v", asset, err)
		}
		if !strings.Contains(string(b), "data-theme='light'") &&
			!strings.Contains(string(b), `data-theme="light"`) {
			t.Errorf("%s carries no [data-theme=light] rules — the dormant light path was deleted; "+
				"it is deliberately retained so contrast_web_test.go keeps gating it", asset)
		}
	}
}

// TestVendoredAssetsServed asserts the vendored design-system assets are
// embedded and served with the right content-type — the offline/self-contained
// invariant (no CDN).
func TestVendoredAssetsServed(t *testing.T) {
	srv := newTestServer(t)
	cases := map[string]string{
		"/assets/civitai-theme.css":      "--civitai-color-body",
		"/assets/civitai-components.css": "@layer civitai.components",
		"/assets/app.css":                "layer(app)",
		"/assets/output.css":             "--civitai-color-surface",
	}
	for path, needle := range cases {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("asset %s status = %d, want 200", path, rec.Code)
			continue
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/css") {
			t.Errorf("asset %s content-type = %q, want text/css*", path, ct)
		}
		if !strings.Contains(rec.Body.String(), needle) {
			t.Errorf("asset %s missing expected content %q", path, needle)
		}
	}
}

// TestNoExternalCDNInShippedHTML grep-asserts the offline property: no rendered
// page references an external CDN for the design system — everything is served
// from /assets/.
func TestNoExternalCDNInShippedHTML(t *testing.T) {
	mid := 7
	subs := []store.Subscription{{ID: 1, Kind: store.KindModel, ModelID: &mid, Layout: "default", PollIntervalSecs: 3600}}
	items := []store.QueueItem{{ID: 1, FileName: "a.safetensors", Status: store.StatusDone, SizeKB: 1}}
	evs := []store.Event{{ID: 1, TS: time.Now(), Level: store.LevelInfo, Kind: "x", Message: "hi"}}

	pages := map[string]g.Node{
		"dashboard": dashboardPage(subs, nil, "csrf", fullMaturityRange()),
		"search":    searchPage("", nil, nil, "csrf", fullMaturityRange(), "", "Most Downloaded", "Month"),
		"library":   libraryPage(buildLibraryView(nil), "csrf", true, nil, "sources", nil, false, nil, fullMaturityRange(), libraryWorkflowsView{}),
		"trash":     trashPage(nil, "csrf", fullMaturityRange()),
		"queue":     queueFragment(items),
		"events":    eventsFragment(evs),
	}
	banned := []string{"jsdelivr.net", "unpkg.com", "cdn.", "//fonts.", "esm.sh"}
	for name, node := range pages {
		out := renderString(t, node)
		for _, bad := range banned {
			if strings.Contains(out, bad) {
				t.Errorf("%s page references external resource %q (offline invariant broken)", name, bad)
			}
		}
	}
}
