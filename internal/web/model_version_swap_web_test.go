package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// hxGet issues a GET carrying the HX-Request header (an htmx-driven request).
func hxGet(t *testing.T, srv *Server, target string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.Header.Set("HX-Request", "true")
	srv.Handler().ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// TestModelVersionSwapHXFragment proves an HX-Request to /models/{id}?version={vid}
// returns ONLY the #version-region fragment (no full-page <html>/navbar),
// containing the selected version's detail + the community-feed container keyed to
// that version (item 7).
func TestModelVersionSwapHXFragment(t *testing.T) {
	srv := newModelServer(t, newModelReader(t))

	code, body := hxGet(t, srv, "/models/7?version=11")
	if code != http.StatusOK {
		t.Fatalf("HX version swap = %d, want 200", code)
	}
	// Fragment only — no full-page shell / navbar.
	if strings.Contains(body, "<html") {
		t.Errorf("HX swap must not return the full page shell:\n%s", body)
	}
	if strings.Contains(body, ">civitai-manager<") {
		t.Errorf("HX swap must not include the navbar:\n%s", body)
	}
	// Contains the region content: version tabs, showcase, version detail (files/
	// metadata), community container.
	for _, want := range []string{
		"cm-version-tabs",       // the version tab bar re-renders inside the region
		"cm-version-tab-active", // an active tab is present after the swap
		"Showcase images",
		"cm-showcase-lg", // the enlarged detail showcase
		// The standalone download CARD is retired; the action lives in the header's
		// action group (headerDownloadControl) and must re-render with the region,
		// since its files are version-dependent.
		"cm-dl-menu",              // the multi-file download trigger, in the header
		"great-model.safetensors", // the selected version's file list
		`id="community-feed"`,
		"versionId=11", // community feed keyed to the swapped version
	} {
		if !strings.Contains(body, want) {
			t.Errorf("region fragment missing %q:\n%s", want, body)
		}
	}
	// The active tab moved to the swapped-in version (11), and only one tab is
	// active — proving the tab-bar highlight re-renders with the region.
	if strings.Count(body, "cm-version-tab-active") != 1 {
		t.Errorf("exactly one tab should be active after the swap:\n%s", body)
	}
	if !strings.Contains(body, `hx-get="/models/7?version=11"`) || !strings.Contains(body, `aria-current="true"`) {
		t.Errorf("the swapped-in version's tab should be the active one:\n%s", body)
	}
}

// TestModelPageFullWithoutHX proves a normal (non-HX) request returns the full
// page with the stable #version-region container.
func TestModelPageFullWithoutHX(t *testing.T) {
	srv := newModelServer(t, newModelReader(t))
	body := getModelPage(t, srv, "/models/7")

	if !strings.Contains(body, "<html") {
		t.Error("a non-HX request should return the full page")
	}
	if !strings.Contains(body, `id="version-region"`) {
		t.Error("the full page should carry the stable #version-region container")
	}
}

// TestModelVersionLinkMarkup proves the version links carry BOTH the htmx
// partial-swap wiring (hx-get / hx-target=#version-region / hx-push-url) AND the
// no-JS href fallback.
func TestModelVersionLinkMarkup(t *testing.T) {
	srv := newModelServer(t, newModelReader(t))
	body := getModelPage(t, srv, "/models/7")

	for _, want := range []string{
		`hx-get="/models/7?version=10"`,
		`hx-target="#version-region"`,
		`hx-swap="innerHTML"`,
		`hx-push-url="true"`,
		`href="/models/7?version=10"`, // no-JS fallback
	} {
		if !strings.Contains(body, want) {
			t.Errorf("version link missing %q:\n%s", want, body)
		}
	}
}
