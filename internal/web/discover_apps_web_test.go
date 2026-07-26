package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
)

// fakeAppsClient records the params each ListApps call receives and returns a
// canned page (or error), so handler tests can assert the request shape and the
// rendered output without touching civitai.com (the catalog is dark live).
type fakeAppsClient struct {
	mu    sync.Mutex
	calls []civitai.AppsParams
	page  *civitai.AppsPage
	err   error
}

func (f *fakeAppsClient) ListApps(_ context.Context, p civitai.AppsParams) (*civitai.AppsPage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, p)
	return f.page, f.err
}

func (f *fakeAppsClient) last() civitai.AppsParams {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[len(f.calls)-1]
}

func pctPtr(v float64) *float64 { return &v }

// appsServer builds a Server wired to the given fake apps client.
func appsServer(t *testing.T, fake *fakeAppsClient) *Server {
	t.Helper()
	srv := newModelServer(t, fakeReader{})
	srv.appsClientFn = func() appsLister { return fake }
	return srv
}

// sampleAppsPage returns a synthetic page with an offsite item, an onsite item
// with a liveUrl, an onsite item WITHOUT a liveUrl (detail-page fallback), and a
// nextCursor.
func sampleAppsPage() *civitai.AppsPage {
	return &civitai.AppsPage{
		Items: []civitai.App{
			{
				ID: "1", Slug: "cool-offsite", Kind: "offsite",
				Name: "Cool Offsite App", Tagline: "does cool things",
				Category: "utility", ContentRating: "PG",
				CoverURL: "https://cdn/cover1.png", IconURL: "https://cdn/icon1.png",
				Creator:     civitai.AppCreator{Username: "alice"},
				Recommend:   civitai.AppRecommend{RecommendPct: pctPtr(80)},
				ReviewCount: 10,
				KindData:    civitai.AppKindData{ExternalURL: "https://example.com/app"},
			},
			{
				ID: "2", Slug: "neat-onsite", Kind: "onsite",
				Name: "Neat Onsite App", Tagline: "runs on civitai",
				Category: "image", ContentRating: "PG",
				IconURL:  "https://cdn/icon2.png",
				Creator:  civitai.AppCreator{Username: "bob"},
				KindData: civitai.AppKindData{LiveURL: "https://neat.civitai.com"},
			},
			{
				ID: "3", Slug: "no-live", Kind: "onsite",
				Name: "No Live URL App", Tagline: "falls back to detail page",
				Creator:  civitai.AppCreator{Username: "carol"},
				KindData: civitai.AppKindData{},
			},
		},
		Metadata: civitai.AppsMetadata{NextCursor: "CURSOR2"},
	}
}

func getApps(t *testing.T, srv *Server, target string, hx bool) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	if hx {
		req.Header.Set("HX-Request", "true")
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d", target, rec.Code)
	}
	return rec.Body.String()
}

// TestAppsBuildsRequestParams proves the handler forwards the normalized
// kind/category/sort/cursor + limit to ListApps.
func TestAppsBuildsRequestParams(t *testing.T) {
	fake := &fakeAppsClient{page: sampleAppsPage()}
	srv := appsServer(t, fake)

	getApps(t, srv, "/apps/discover?kind=offsite&category=video&sort=newest&cursor=C9", false)

	p := fake.last()
	if p.Kind != "offsite" {
		t.Errorf("kind = %q, want offsite", p.Kind)
	}
	if p.Category != "video" {
		t.Errorf("category = %q, want video", p.Category)
	}
	if p.Sort != "newest" {
		t.Errorf("sort = %q, want newest", p.Sort)
	}
	if p.Cursor != "C9" {
		t.Errorf("cursor = %q, want C9", p.Cursor)
	}
	if p.Limit != appsPageLimit {
		t.Errorf("limit = %d, want %d", p.Limit, appsPageLimit)
	}
}

// TestAppsNormalizesBogusFilters proves an out-of-whitelist kind/sort defaults
// (all / top-rated) and a bogus category drops to "" — never forwarded verbatim.
func TestAppsNormalizesBogusFilters(t *testing.T) {
	fake := &fakeAppsClient{page: sampleAppsPage()}
	srv := appsServer(t, fake)

	getApps(t, srv, "/apps/discover?kind=DROP&sort=DROP+TABLE&category=Robert';--", false)

	p := fake.last()
	if p.Kind != "all" {
		t.Errorf("bogus kind should default to all, got %q", p.Kind)
	}
	if p.Sort != "top-rated" {
		t.Errorf("bogus sort should default to top-rated, got %q", p.Sort)
	}
	if p.Category != "" {
		t.Errorf("bogus category should drop to empty, got %q", p.Category)
	}
}

// TestAppsPageRendersCards proves the full page renders app cards (name, tagline,
// image, creator, contentRating badge) and the correct play links per kind.
func TestAppsPageRendersCards(t *testing.T) {
	fake := &fakeAppsClient{page: sampleAppsPage()}
	srv := appsServer(t, fake)

	body := getApps(t, srv, "/apps/discover", false)

	for _, want := range []string{
		"Apps",                              // page heading + nav
		"Cool Offsite App", "does cool things", // name + tagline
		"https://cdn/cover1.png",            // cover thumbnail
		"@alice", "@bob",                    // creator chips
		"PG",                                // contentRating badge
		"80% recommend",                     // recommend line
		// Play links per kind:
		`href="https://example.com/app"`,          // offsite → externalUrl
		`href="https://neat.civitai.com"`,         // onsite with liveUrl
		`href="https://civitai.com/apps/no-live"`, // onsite without liveUrl → detail fallback
		"Open app ↗",                              // primary action label
		`rel="noopener noreferrer"`,               // hardened new-tab
	} {
		if !strings.Contains(body, want) {
			t.Errorf("apps page missing %q", want)
		}
	}
	// Full-page chrome present (navbar with the Apps link).
	if !strings.Contains(body, `href="/apps/discover"`) {
		t.Error("full page should include the Apps nav link")
	}
}

// TestAppsXSSPlayURLRejected proves a javascript: externalUrl does NOT produce a
// live href — the card renders a safe non-link Unavailable state instead.
func TestAppsXSSPlayURLRejected(t *testing.T) {
	fake := &fakeAppsClient{page: &civitai.AppsPage{Items: []civitai.App{
		{
			ID: "9", Slug: "evil", Kind: "offsite", Name: "Evil App",
			KindData: civitai.AppKindData{ExternalURL: "javascript:alert(1)"},
		},
	}}}
	srv := appsServer(t, fake)

	body := getApps(t, srv, "/apps/discover", false)

	if !strings.Contains(body, "Evil App") {
		t.Fatal("card should still render (guards against an empty false-pass)")
	}
	if strings.Contains(body, "javascript:") {
		t.Errorf("javascript: URL must never appear in the rendered output:\n%s", body)
	}
	if strings.Contains(body, `href="javascript`) {
		t.Error("javascript: must not become a live href")
	}
	if !strings.Contains(body, "Unavailable") {
		t.Error("a rejected play URL should render the Unavailable state")
	}
}

// TestAppsDataURLRejected proves a data: scheme is also rejected.
func TestAppsDataURLRejected(t *testing.T) {
	fake := &fakeAppsClient{page: &civitai.AppsPage{Items: []civitai.App{
		{
			ID: "9", Slug: "d", Kind: "offsite", Name: "Data App",
			KindData: civitai.AppKindData{ExternalURL: "data:text/html,<script>alert(1)</script>"},
		},
	}}}
	srv := appsServer(t, fake)

	body := getApps(t, srv, "/apps/discover", false)
	if strings.Contains(body, "href=\"data:") {
		t.Error("data: must not become a live href")
	}
	if !strings.Contains(body, "Unavailable") {
		t.Error("a rejected data: play URL should render Unavailable")
	}
}

// TestAppsEmptyStateMessage proves an empty catalog renders the honest
// pre-launch message rather than an error.
func TestAppsEmptyStateMessage(t *testing.T) {
	fake := &fakeAppsClient{page: &civitai.AppsPage{Items: nil}}
	srv := appsServer(t, fake)

	body := getApps(t, srv, "/apps/discover", false)
	if !strings.Contains(body, "No published apps yet") {
		t.Errorf("empty catalog should render the honest empty state:\n%s", body)
	}
	if !strings.Contains(body, "populate automatically") {
		t.Error("empty state should explain it fills when the marketplace launches")
	}
}

// TestAppsErrorNote proves a client/API error renders a clean note, not a 500.
func TestAppsErrorNote(t *testing.T) {
	fake := &fakeAppsClient{err: &civitai.AppsError{StatusCode: 429, Status: "429 Too Many Requests"}}
	srv := appsServer(t, fake)

	req := httptest.NewRequest(http.MethodGet, "/apps/discover", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("error should still render 200 OK page, got %d", rec.Code)
	}
	// The apostrophe in "Couldn't" is HTML-escaped, so assert on stable substrings.
	if !strings.Contains(rec.Body.String(), "load apps") ||
		!strings.Contains(rec.Body.String(), "reach the CivitAI apps catalog") {
		t.Errorf("client error should render the clean couldn't-load note:\n%s", rec.Body.String())
	}
}

// TestAppsHXFragmentNoChrome proves the HX results endpoint returns just the
// grid — no full-page chrome (<html>, navbar).
func TestAppsHXFragmentNoChrome(t *testing.T) {
	fake := &fakeAppsClient{page: sampleAppsPage()}
	srv := appsServer(t, fake)

	body := getApps(t, srv, "/apps/discover?kind=all", true)
	if !strings.Contains(body, "Cool Offsite App") {
		t.Error("HX fragment should contain the app cards")
	}
	if strings.Contains(body, "<html") || strings.Contains(body, "<nav") {
		t.Errorf("HX fragment must not contain full-page chrome:\n%s", body)
	}
}

// TestAppsNextControlUsesCursor proves the "next" control carries
// metadata.nextCursor (and the active filters) in its hx-get.
func TestAppsNextControlUsesCursor(t *testing.T) {
	fake := &fakeAppsClient{page: sampleAppsPage()}
	srv := appsServer(t, fake)

	body := getApps(t, srv, "/apps/discover?kind=offsite&sort=newest", true)
	if !strings.Contains(body, "cursor=CURSOR2") {
		t.Errorf("next control should use metadata.nextCursor:\n%s", body)
	}
	if !strings.Contains(body, "Next page") {
		t.Error("next control should be labeled 'Next page'")
	}
	// Active filters preserved in the next request.
	if !strings.Contains(body, "kind=offsite") || !strings.Contains(body, "sort=newest") {
		t.Error("next control should preserve the active filters")
	}
}

// TestAppsNoNextWhenNoCursor proves the "next" control is absent when there is
// no next cursor.
func TestAppsNoNextWhenNoCursor(t *testing.T) {
	page := sampleAppsPage()
	page.Metadata.NextCursor = ""
	fake := &fakeAppsClient{page: page}
	srv := appsServer(t, fake)

	body := getApps(t, srv, "/apps/discover", true)
	if strings.Contains(body, "Next page") {
		t.Error("no next cursor → no Next page control")
	}
}

// TestAppsEscapesUntrustedText proves a <script> in an app name/tagline is
// escaped, not emitted raw.
func TestAppsEscapesUntrustedText(t *testing.T) {
	fake := &fakeAppsClient{page: &civitai.AppsPage{Items: []civitai.App{
		{
			ID: "5", Slug: "x", Kind: "offsite",
			Name:     "<script>alert('xss')</script>",
			Tagline:  "<img src=x onerror=alert(1)>",
			Creator:  civitai.AppCreator{Username: "<b>evil</b>"},
			KindData: civitai.AppKindData{ExternalURL: "https://ok.example.com"},
		},
	}}}
	srv := appsServer(t, fake)

	body := getApps(t, srv, "/apps/discover", false)
	if strings.Contains(body, "<script>alert('xss')</script>") {
		t.Error("app name <script> must be escaped, not emitted raw")
	}
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Error("escaped app name should appear as &lt;script&gt;")
	}
	if strings.Contains(body, "<img src=x onerror") {
		t.Error("tagline <img onerror> must be escaped")
	}
}
