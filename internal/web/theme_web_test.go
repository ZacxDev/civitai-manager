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
	dash := renderString(t, dashboardPage(subs, nil, "csrf", "dark"))
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

// TestThemeToggleRendersAndPersists covers: the toggle control renders,
// data-theme is wired on the <html> ancestor for both themes, both render
// without panic, and the persisted setting round-trips through the store + the
// POST /settings/theme handler.
func TestThemeToggleRendersAndPersists(t *testing.T) {
	subs := []store.Subscription{}

	dark := renderString(t, dashboardPage(subs, nil, "csrf", "dark"))
	if !strings.Contains(dark, `<html lang="en" data-theme="dark"`) {
		t.Errorf("dark page missing <html data-theme=\"dark\">")
	}
	// In dark, the toggle offers a switch to light.
	if !strings.Contains(dark, `aria-label="Switch to light theme"`) || !strings.Contains(dark, `data-civitai-ui="button"`) {
		t.Errorf("dark page missing the light-theme toggle control")
	}

	light := renderString(t, dashboardPage(subs, nil, "csrf", "light"))
	if !strings.Contains(light, `data-theme="light"`) {
		t.Errorf("light page missing data-theme=\"light\"")
	}
	if !strings.Contains(light, `aria-label="Switch to dark theme"`) {
		t.Errorf("light page missing the dark-theme toggle control")
	}

	// Round-trip through the handler + store.
	srv := newTestServer(t)
	if got := srv.currentTheme(); got != "dark" {
		t.Fatalf("default theme = %q, want dark", got)
	}
	rec := httptest.NewRecorder()
	form := strings.NewReader("theme=light&csrf_token=" + srv.csrf)
	req := httptest.NewRequest(http.MethodPost, "/settings/theme", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("POST /settings/theme status = %d, want 204", rec.Code)
	}
	if rec.Header().Get("HX-Refresh") != "true" {
		t.Errorf("POST /settings/theme should reply HX-Refresh: true")
	}
	if got := srv.currentTheme(); got != "light" {
		t.Fatalf("persisted theme = %q, want light", got)
	}

	// A bad value coerces to dark; missing CSRF is rejected.
	recNoCSRF := httptest.NewRecorder()
	reqNoCSRF := httptest.NewRequest(http.MethodPost, "/settings/theme", strings.NewReader("theme=light"))
	reqNoCSRF.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	srv.Handler().ServeHTTP(recNoCSRF, reqNoCSRF)
	if recNoCSRF.Code != http.StatusForbidden {
		t.Errorf("theme POST without CSRF = %d, want 403", recNoCSRF.Code)
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
		"dashboard": dashboardPage(subs, nil, "csrf", "dark"),
		"search":    searchPage("", nil, "csrf", "light", NSFWBlur, ""),
		"library":   libraryPage(buildLibraryView(nil), "csrf", true, nil, "dark", "sources", nil, false, nil),
		"trash":     trashPage(nil, "csrf", "light"),
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
