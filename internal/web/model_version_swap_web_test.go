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
	// Contains the region content: showcase, version detail, community container.
	for _, want := range []string{
		"Showcase images",
		"Versions",
		"great-model.safetensors", // the selected version's file list
		`id="community-feed"`,
		"versionId=11", // community feed keyed to the swapped version
	} {
		if !strings.Contains(body, want) {
			t.Errorf("region fragment missing %q:\n%s", want, body)
		}
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
