package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/store"
)

// libraryBody GETs an arbitrary library URL and returns its HTML.
func libraryBody(t *testing.T, srv *Server, target string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d", target, rec.Code)
	}
	return rec.Body.String()
}

// seedSourcePostLibrary builds the shape the deep-link exists for: one CivitAI
// Workflows post that unpacked into SEVERAL workflows, a second post with exactly
// ONE, and a workflow with no civitai linkage at all.
func seedSourcePostLibrary(t *testing.T) *Server {
	t.Helper()
	srv := newWorkflowServer(t)
	ctx := context.Background()

	if err := srv.store.PutModelCache(1847730, "WAN 2.2 Smooth Workflow",
		[]byte(`{"id":1847730,"name":"WAN 2.2 Smooth Workflow","modelVersions":[{"id":3142605,"name":"v6.0"}]}`)); err != nil {
		t.Fatalf("cache model: %v", err)
	}

	seed := func(name string, modelID, versionID *int) {
		if _, err := srv.store.InsertWorkflow(ctx, &store.Workflow{
			Name: name, Format: store.WorkflowFormatAPI, Graph: `{"` + name + `":{"class_type":"X","inputs":{}}}`,
			Source: store.WorkflowSourceCivitai, ModelID: modelID, VersionID: versionID,
		}); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}
	seed("smooth-t2v", intp(1847730), intp(3142605))
	seed("smooth-i2v", intp(1847730), intp(3142605))
	seed("smooth-f2lf", intp(1847730), intp(3142605))
	seed("lonely", intp(2184844), intp(3168420))
	seed("pasted-by-hand", nil, nil)
	return srv
}

// TestLibraryWorkflowsFilteredToSourcePost is the core deep-link claim: only that
// post's workflows are listed, and the header names it and links back to CivitAI.
func TestLibraryWorkflowsFilteredToSourcePost(t *testing.T) {
	srv := seedSourcePostLibrary(t)
	body := libraryBody(t, srv, "/library?tab=workflows&model=1847730")

	for _, want := range []string{"smooth-t2v", "smooth-i2v", "smooth-f2lf"} {
		if !strings.Contains(body, want) {
			t.Errorf("filtered view missing %q:\n%s", want, body)
		}
	}
	for _, absent := range []string{"lonely", "pasted-by-hand"} {
		if strings.Contains(body, absent) {
			t.Errorf("filtered view leaked %q from outside the post", absent)
		}
	}
	// The header names the source post (from the LOCAL cache) …
	if !strings.Contains(body, "WAN 2.2 Smooth Workflow") {
		t.Errorf("header does not name the source model:\n%s", body)
	}
	if !strings.Contains(body, "3 workflows imported from this CivitAI post") {
		t.Errorf("header does not state the count:\n%s", body)
	}
	// … links back to it on civitai.com …
	if !strings.Contains(body, `href="https://civitai.com/models/1847730"`) {
		t.Errorf("header missing the link back to CivitAI:\n%s", body)
	}
	// … and offers the way out.
	if !strings.Contains(body, `href="/library?tab=workflows"`) {
		t.Errorf("header missing the escape back to the full library:\n%s", body)
	}
}

// TestLibraryWorkflowsUnfilteredByDefault is the regression guard: without ?model=
// nothing is hidden and no source-post header appears.
func TestLibraryWorkflowsUnfilteredByDefault(t *testing.T) {
	srv := seedSourcePostLibrary(t)
	for _, target := range []string{
		"/library?tab=workflows",
		"/library?tab=workflows&model=", // empty
		"/library?tab=workflows&model=0",
		"/library?tab=workflows&model=-5",
		"/library?tab=workflows&model=notanumber",
		"/library?tab=workflows&model=1'%20OR%201=1",
	} {
		t.Run(target, func(t *testing.T) {
			body := libraryBody(t, srv, target)
			for _, want := range []string{"smooth-t2v", "lonely", "pasted-by-hand"} {
				if !strings.Contains(body, want) {
					t.Errorf("%s hid %q — an unparseable filter must be DROPPED, not applied", target, want)
				}
			}
			if strings.Contains(body, "imported from this CivitAI post") {
				t.Errorf("%s rendered a source-post header with no valid filter", target)
			}
		})
	}
}

// TestLibraryWorkflowsSourcePostWithExactlyOne covers the singular copy and proves
// the single-workflow post is a normal, working case.
func TestLibraryWorkflowsSourcePostWithExactlyOne(t *testing.T) {
	srv := seedSourcePostLibrary(t)
	body := libraryBody(t, srv, "/library?tab=workflows&model=2184844")

	if !strings.Contains(body, "lonely") {
		t.Errorf("the post's single workflow is missing:\n%s", body)
	}
	if strings.Contains(body, "smooth-t2v") {
		t.Errorf("filtered view leaked another post's workflow")
	}
	if !strings.Contains(body, "1 workflow imported from this CivitAI post") {
		t.Errorf("singular count copy missing:\n%s", body)
	}
	// The model was never cached — the header names the id rather than inventing a title.
	if !strings.Contains(body, "Workflows from CivitAI model 2184844") {
		t.Errorf("uncached post should be named by id:\n%s", body)
	}
}

// TestLibraryWorkflowsSourcePostMatchingNothing: a valid-but-unknown model id gets
// the guided empty state naming the filter, never a bare "no workflows" that reads
// as data loss.
func TestLibraryWorkflowsSourcePostMatchingNothing(t *testing.T) {
	srv := seedSourcePostLibrary(t)
	body := libraryBody(t, srv, "/library?tab=workflows&model=999999")

	if strings.Contains(body, "smooth-t2v") || strings.Contains(body, "lonely") {
		t.Errorf("an unmatched post filter must show nothing from the library:\n%s", body)
	}
	if !strings.Contains(body, "Workflows from CivitAI model 999999") {
		t.Errorf("header should still name the requested post:\n%s", body)
	}
	if !strings.Contains(body, "CivitAI model 999999") || !strings.Contains(body, "workflows in your library") {
		t.Errorf("expected the guided empty state naming the filter:\n%s", body)
	}
	if !strings.Contains(body, `href="/library?tab=workflows"`) {
		t.Errorf("empty state must offer a way back:\n%s", body)
	}
}

// TestSourcePostFilterSurvivesFacetToggles pins that the post is a SCOPE: clicking
// an ecosystem/use-case chip narrows within the post instead of escaping it.
func TestSourcePostFilterSurvivesFacetToggles(t *testing.T) {
	f := libraryWorkflowFacets{Model: 1847730}
	for _, tc := range []struct{ dim, value, want string }{
		{"eco", "flux1", "/library?model=1847730&tab=workflows"},
		{"use", "inpaint", "/library?model=1847730&tab=workflows"},
		{"eco", "", "/library?model=1847730&tab=workflows"},
	} {
		got := libraryWorkflowHref(f, tc.dim, tc.value)
		if !strings.Contains(got, "model=1847730") {
			t.Errorf("toggling %s=%s dropped the source-post scope: %s", tc.dim, tc.value, got)
		}
	}
	// And a plain facet URL (no post) must not grow a model= parameter.
	if got := libraryWorkflowHref(libraryWorkflowFacets{}, "eco", "flux1"); strings.Contains(got, "model=") {
		t.Errorf("a facet href with no post scope must not carry model=: %s", got)
	}
}

// TestSourcePostFilterEscapesUntrustedModelName: the post title comes from the
// CivitAI cache and is untrusted.
func TestSourcePostFilterEscapesUntrustedModelName(t *testing.T) {
	got := renderString(t, sourcePostHeader(7, `<script>alert(1)</script>`, 3))
	if strings.Contains(got, "<script>alert(1)</script>") {
		t.Errorf("untrusted model name must be escaped:\n%s", got)
	}
	if !strings.Contains(got, `href="https://civitai.com/models/7"`) {
		t.Errorf("link back to CivitAI must be built from the numeric id:\n%s", got)
	}
	if !strings.Contains(got, `rel="noopener noreferrer"`) {
		t.Errorf("external link must carry rel=noopener noreferrer:\n%s", got)
	}
}
