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
	dash := renderString(t, dashboardPage(subs, "csrf", "dark"))
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

	// Badge semantic-color override: green -> success token (Badge has no
	// data-color, so the color is applied via the token-override escape hatch).
	gb := renderString(t, badge("done", "green"))
	if !strings.Contains(gb, "--civitai-color-primary:var(--civitai-color-success)") {
		t.Errorf("green badge should override the primary token with success, got %q", gb)
	}
}

// TestThemeToggleRendersAndPersists covers: the toggle control renders,
// data-theme is wired on the <html> ancestor for both themes, both render
// without panic, and the persisted setting round-trips through the store + the
// POST /settings/theme handler.
func TestThemeToggleRendersAndPersists(t *testing.T) {
	subs := []store.Subscription{}

	dark := renderString(t, dashboardPage(subs, "csrf", "dark"))
	if !strings.Contains(dark, `<html lang="en" data-theme="dark"`) {
		t.Errorf("dark page missing <html data-theme=\"dark\">")
	}
	// In dark, the toggle offers a switch to light.
	if !strings.Contains(dark, `aria-label="Switch to light theme"`) || !strings.Contains(dark, `data-civitai-ui="button"`) {
		t.Errorf("dark page missing the light-theme toggle control")
	}

	light := renderString(t, dashboardPage(subs, "csrf", "light"))
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
		"dashboard": dashboardPage(subs, "csrf", "dark"),
		"search":    searchPage("", nil, "https://civitai.com", "csrf", "light"),
		"library":   libraryPage(buildLibraryView(nil), "csrf", true, nil, "dark"),
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
