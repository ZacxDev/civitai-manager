package web

import (
	"html"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
)

// ---------------------------------------------------------------------------
// The duplicate browse-by-facet element
// ---------------------------------------------------------------------------
//
// /workflows/discover rendered the SAME two facet dimensions twice on its entry
// view: a compact chip row (kept) and, below it, a card of ~30 clickable tiles
// under "Browse by ecosystem" / "Browse by use case" (removed). These tests pin
// that exactly one of the two survived — the chip row — and that the tile card
// cannot come back unnoticed.

// tileCardMarkers are the strings unique to the REMOVED card-based element. None
// of them belongs to the chip row.
var tileCardMarkers = []string{
	"Browse by ecosystem",
	"Browse by use case",
	"cm-facet-grid",
	"cm-facet-tile",
	// The tile card's section headings and its trailing note.
	"Image models",
	"Video models",
	"Audio models",
	"Use cases come from the workflow's CivitAI tags",
}

// chipRowMarkers are the strings that prove the COMPACT chip row is still there.
var chipRowMarkers = []string{
	"cm-facet-chip",
	">Ecosystem<",
	">Use case<",
	">All ecosystems<",
	">All use cases<",
}

func TestOnlyTheChipRowRendersTheFacetVocabulary(t *testing.T) {
	for _, tc := range []struct {
		name   string
		target string
		hx     bool
	}{
		{"entry view (full page)", "/workflows/discover", false},
		{"entry view (hx fragment)", "/workflows/discover", true},
		{"faceted browse", "/workflows/discover?eco=flux1", false},
		{"keyword search", "/workflows/discover?q=wan", false},
		{"faceted keyword search", "/workflows/discover?q=wan&eco=flux1&use=inpaint", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader := &recordingSearchReader{result: workflowResult(t)}
			srv := newModelServer(t, reader)
			req := httptest.NewRequest(http.MethodGet, tc.target, nil)
			if tc.hx {
				req.Header.Set("HX-Request", "true")
			}
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("GET %s = %d", tc.target, rec.Code)
			}
			body := rec.Body.String()
			// Guard against an empty false-pass: the page really did render.
			if !strings.Contains(body, "discover-results") && !tc.hx {
				t.Fatal("results container missing — the absence assertions below would false-pass")
			}
			for _, gone := range tileCardMarkers {
				if strings.Contains(body, gone) {
					t.Errorf("the removed card-based browse element is back: found %q", gone)
				}
			}
			for _, want := range chipRowMarkers {
				if !strings.Contains(body, want) {
					t.Errorf("the compact chip row must survive: missing %q", want)
				}
			}
		})
	}
}

// TestChipRowStillCoversEveryCuratedFacet — the tile card was the only other place
// the full vocabulary appeared, so removing it must not cost the user access to
// any ecosystem or use case.
func TestChipRowStillCoversEveryCuratedFacet(t *testing.T) {
	reader := &recordingSearchReader{result: workflowResult(t)}
	srv := newModelServer(t, reader)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/workflows/discover", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET = %d", rec.Code)
	}
	body := rec.Body.String()

	// Labels reach the DOM HTML-escaped ("Face & detail fix" → "Face &amp; detail
	// fix"), so compare against the escaped form the renderer actually emits.
	for _, e := range civitai.Ecosystems() {
		if !strings.Contains(body, ">"+html.EscapeString(e.Label)+"<") {
			t.Errorf("ecosystem %q (%s) is no longer reachable from the discover page", e.Label, e.Slug)
		}
		if !strings.Contains(body, "eco="+e.Slug) {
			t.Errorf("ecosystem %q has no chip href (eco=%s)", e.Label, e.Slug)
		}
	}
	for _, u := range civitai.UseCases() {
		if !strings.Contains(body, ">"+html.EscapeString(u.Label)+"<") {
			t.Errorf("use case %q (%s) is no longer reachable from the discover page", u.Label, u.Slug)
		}
		if !strings.Contains(body, "use="+u.Slug) {
			t.Errorf("use case %q has no chip href (use=%s)", u.Label, u.Slug)
		}
	}
}

// TestRemovedFacetTileCSSIsGone pins the other half of the removal: the tile card's
// hand-written rules are deleted, not left behind as CSS nothing selects. The chip
// rules must NOT be caught by the same sweep.
func TestRemovedFacetTileCSSIsGone(t *testing.T) {
	b, err := os.ReadFile("assets/app.css")
	if err != nil {
		t.Fatalf("read app.css: %v", err)
	}
	css := string(b)
	for _, gone := range []string{".cm-facet-grid {", ".cm-facet-tile {", ".cm-facet-tile:hover"} {
		if strings.Contains(css, gone) {
			t.Errorf("dead rule %q is still in app.css — the element it styled was removed", gone)
		}
	}
	for _, want := range []string{".cm-facet-chip", ".cm-facet-chip-on"} {
		if !strings.Contains(css, want) {
			t.Errorf("chip rule %q must survive — the chip row is the element we kept", want)
		}
	}
}
