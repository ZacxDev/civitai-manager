package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// browseReader is stubReader with a scripted SearchModels, so /search and
// /workflows/discover render real result cards with no network.
type browseReader struct {
	stubReader
	res *civitai.ModelSearchResult
}

func (b browseReader) SearchModels(context.Context, url.Values) (*civitai.ModelSearchResult, error) {
	return b.res, nil
}

// The three browse surfaces the unification covers, and the STABLE htmx container
// each one swaps its results into.
var browseSurfaces = []struct {
	name      string
	path      string
	resultsID string
	action    string // the filter form's hx-get, "" when the surface has no query form
}{
	{"models search", "/search", "search-results", "/search"},
	{"discover workflows", "/workflows/discover", "discover-results", "/workflows/discover"},
	{"library workflows tab", "/library?tab=workflows", "workflow-scan-results", ""},
}

// cardOpen matches the opening tag of a design-system card.
var cardOpen = regexp.MustCompile(`<div data-civitai-ui="card"`)

func browseBody(t *testing.T, srv *Server, path string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d", path, rec.Code)
	}
	return rec.Body.String()
}

// newBrowseServer is a server that can render all three surfaces offline: a fake
// civitai search backing /search and /workflows/discover, and a seeded workflow so
// the library tab has content.
func newBrowseServer(t *testing.T, res *civitai.ModelSearchResult) *Server {
	t.Helper()
	if res == nil {
		res = &civitai.ModelSearchResult{
			Items: []civitai.ModelListItem{{ID: 5, Name: "A Model", Type: "Checkpoint"}},
			Raw:   []byte(`{"items":[{"id":5,"name":"A Model"}]}`),
		}
	}
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	srv := NewServer(st, browseReader{res: res}, stubSubscriber{},
		Config{BaseURL: "https://civitai.com", DefaultPollInterval: time.Hour, Addr: "127.0.0.1:8972"}, nil)
	if _, err := srv.store.InsertWorkflow(context.Background(), &store.Workflow{
		Name: "seeded", Format: store.WorkflowFormatAPI, Graph: "{}",
		Source: store.WorkflowSourceImported,
	}); err != nil {
		t.Fatalf("seed workflow: %v", err)
	}
	return srv
}

// TestBrowseSurfacesAreOneContinuousSurface is the structural claim of the
// unification: on each page the controls and the results live in the SAME card,
// separated by the head rule — not in two stacked cards with a seam between them.
func TestBrowseSurfacesAreOneContinuousSurface(t *testing.T) {
	srv := newBrowseServer(t, nil)
	for _, s := range browseSurfaces {
		t.Run(s.name, func(t *testing.T) {
			body := browseBody(t, srv, s.path)

			head := strings.Index(body, `class="cm-browse-head"`)
			if head < 0 {
				t.Fatalf("%s does not use the shared browse surface:\n%s", s.path, body)
			}
			results := strings.Index(body, `id="`+s.resultsID+`"`)
			if results < 0 {
				t.Fatalf("%s lost its stable results container %q", s.path, s.resultsID)
			}
			if results < head {
				t.Errorf("%s renders its results BEFORE the controls", s.path)
			}
			// The card that opens the surface must be the one still open when the
			// results container starts: no card CLOSES and reopens between them, which
			// is exactly the seam this work removed.
			between := body[head:results]
			if n := len(cardOpen.FindAllString(between, -1)); n != 0 {
				t.Errorf("%s opens %d new card(s) between its controls and its results — "+
					"they must share one surface:\n%s", s.path, n, between)
			}
		})
	}
}

// TestBrowseSurfacesShareOneComponent pins that the three pages are built from the
// SAME helper rather than three near-copies: identical head markup, identical
// filter-row shape, identical control ids modulo the per-surface prefix.
func TestBrowseSurfacesShareOneComponent(t *testing.T) {
	srv := newBrowseServer(t, nil)
	for _, s := range browseSurfaces {
		t.Run(s.name, func(t *testing.T) {
			body := browseBody(t, srv, s.path)
			for _, want := range []string{`class="cm-browse-head"`, `class="cm-browse-titlerow"`} {
				if !strings.Contains(body, want) {
					t.Errorf("%s missing shared surface markup %q", s.path, want)
				}
			}
			if s.action == "" {
				return // the library tab filters with chips, not a query form
			}
			if !strings.Contains(body, `class="cm-browse-controls"`) {
				t.Errorf("%s does not render its filter row in the shared controls slot", s.path)
			}
			// The filter row itself: query box + both selects + submit, all inside ONE
			// form pointed at the surface's own endpoint and results container.
			for _, want := range []string{
				`hx-get="` + s.action + `"`,
				`hx-target="#` + s.resultsID + `"`,
				`hx-swap="innerHTML"`,
				`name="q"`, `name="sort"`, `name="period"`,
			} {
				if !strings.Contains(body, want) {
					t.Errorf("%s filter row missing %q", s.path, want)
				}
			}
		})
	}
}

// TestSearchSurfaceKeepsSortPeriodAndSelection: the sort/period selects still
// reflect the request and still round-trip.
func TestSearchSurfaceKeepsSortPeriodAndSelection(t *testing.T) {
	srv := newBrowseServer(t, nil)
	for _, path := range []string{
		"/search?q=dog&sort=Newest&period=Week",
		"/workflows/discover?q=dog&sort=Newest&period=Week",
	} {
		t.Run(path, func(t *testing.T) {
			body := browseBody(t, srv, path)
			if !strings.Contains(body, `value="dog"`) {
				t.Errorf("%s did not echo the query into the box:\n%s", path, body)
			}
			if !strings.Contains(body, `<option value="Newest" selected`) {
				t.Errorf("%s did not preserve the sort selection", path)
			}
			if !strings.Contains(body, `<option value="Week" selected`) {
				t.Errorf("%s did not preserve the period selection", path)
			}
		})
	}
}

// TestDiscoverSurfaceKeepsFacetsThroughTheForm pins the behaviour that made the
// discover form different from the search form: its hidden facet inputs, which
// stop a sort/period change from silently clearing the browsed facets.
func TestDiscoverSurfaceKeepsFacetsThroughTheForm(t *testing.T) {
	srv := newBrowseServer(t, nil)
	body := browseBody(t, srv, "/workflows/discover?eco=flux1&use=inpaint")
	for _, want := range []string{
		`<input type="hidden" name="eco" value="flux1">`,
		`<input type="hidden" name="use" value="inpaint">`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("discover form lost its facet carry-through %q:\n%s", want, body)
		}
	}
	// And the chips themselves still render inside the swapped results container.
	if !strings.Contains(body, "cm-facet-chip") {
		t.Errorf("discover surface lost its facet chips:\n%s", body)
	}
}

// TestLibraryWorkflowsSurfaceKeepsItsControls: the tab's chips + "Add a workflow"
// action + the import dialog all survive the move onto the shared surface, and the
// scan poll container is still the swap target.
func TestLibraryWorkflowsSurfaceKeepsItsControls(t *testing.T) {
	srv := newBrowseServer(t, nil)
	body := browseBody(t, srv, "/library?tab=workflows")

	for _, want := range []string{
		"Add a workflow",                             // the (now head-row) trigger
		`id="` + workflowImportDialogID + `"`,        // its dialog still exists
		`action="/workflows/import"`,                 // the paste-JSON form
		`action="/workflows/import-png"`,             // the PNG form
		`id="` + workflowScanResultsID + `"`,         // the STABLE scan container
		`hx-target="#` + workflowScanResultsID + `"`, // the scan form still targets it
	} {
		if !strings.Contains(body, want) {
			t.Errorf("library workflows surface lost %q:\n%s", want, body)
		}
	}
	// The Library page keeps exactly ONE <h1> — the surface must not add a second.
	if n := strings.Count(body, "<h1"); n != 1 {
		t.Errorf("library page should have exactly 1 <h1>, got %d", n)
	}
}

// TestSearchAndDiscoverPagesKeepExactlyOneH1 guards the heading semantics the
// surface owns on the two pages that DO carry their own title.
func TestSearchAndDiscoverPagesKeepExactlyOneH1(t *testing.T) {
	srv := newBrowseServer(t, nil)
	for _, path := range []string{"/search", "/workflows/discover"} {
		body := browseBody(t, srv, path)
		if n := strings.Count(body, "<h1"); n != 1 {
			t.Errorf("%s should have exactly 1 <h1>, got %d", path, n)
		}
	}
}

// TestSearchResultsFragmentIsUnchangedByTheSurface: the htmx swap payload is the
// results ALONE (no surface chrome), so a search that swaps #search-results cannot
// smuggle a second head or a nested card into the surface.
func TestSearchResultsFragmentIsUnchangedByTheSurface(t *testing.T) {
	srv := newBrowseServer(t, nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/search?q=dog", nil)
	req.Header.Set("HX-Request", "true")
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("htmx search = %d", rec.Code)
	}
	frag := rec.Body.String()
	if strings.Contains(frag, "cm-browse-head") || strings.Contains(frag, "<h1") {
		t.Errorf("the results fragment must carry no surface chrome:\n%s", frag)
	}
	if !strings.Contains(frag, "A Model") {
		t.Errorf("results fragment missing the result:\n%s", frag)
	}
}

// TestSearchEmptyStateIsNotACardInsideTheSurface: the no-match state used to be
// wrapped in its own card, which now sits INSIDE the surface card.
func TestSearchEmptyStateIsNotACardInsideTheSurface(t *testing.T) {
	got := renderString(t, searchResults(&civitai.ModelSearchResult{}, nil, "blur", "tok", ""))
	if cardOpen.MatchString(got) {
		t.Errorf("the empty state must not open a card inside the browse surface:\n%s", got)
	}
	if !strings.Contains(got, "No models matched that search") {
		t.Errorf("empty state content lost:\n%s", got)
	}
	if !strings.Contains(got, "Browse popular models") {
		t.Errorf("empty state CTA lost:\n%s", got)
	}
}

// TestBrowseSurfacesHonourNSFWMode: the surfaces render cards that respect the
// stored NSFW display mode, unchanged by the layout work.
func TestBrowseSurfacesHonourNSFWMode(t *testing.T) {
	srv := newBrowseServer(t, &civitai.ModelSearchResult{
		Items: []civitai.ModelListItem{{ID: 5, Name: "A Model", NSFW: true}},
		Raw: []byte(`{"items":[{"id":5,"name":"A Model","nsfw":true,` +
			`"modelVersions":[{"id":1,"images":[{"url":"https://example.invalid/a.jpg","nsfwLevel":8}]}]}]}`),
	})
	if err := srv.store.SetSetting("nsfw_mode", "blur"); err != nil {
		t.Fatalf("set nsfw mode: %v", err)
	}
	body := browseBody(t, srv, "/search?q=x")
	if !strings.Contains(body, "cm-blur") {
		t.Errorf("blur mode not applied on the search surface:\n%s", body)
	}
}
