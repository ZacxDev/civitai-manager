package web

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/store"
)

// ===========================================================================
// An ALREADY-IMPORTED discover card offers View, not the Import CTA.
// ===========================================================================
//
// Re-importing a model whose workflows are already in the library can only ever
// report "0 imported, N already present" — a primary action that is a dead end.
// The card therefore flips to a View link into the library FILTERED TO THAT MODEL,
// which is the same destination the import RESULT already lands on.
//
// The lookup behind it is the one thing that can go quietly wrong at scale: the
// grid renders up to `searchLimit` cards, so the question must be answered for the
// whole page in ONE batched query, never once per card.

// importedProbe wraps a real store lookup while COUNTING invocations and
// recording the id batches it was handed. It is installed on
// Server.importedWorkflowsFn, which is exactly the seam production uses.
type importedProbe struct {
	mu      sync.Mutex
	calls   int
	batches [][]int
	counts  map[int]int
}

func (p *importedProbe) fn(_ context.Context, ids []int) map[int]int {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	p.batches = append(p.batches, append([]int(nil), ids...))
	out := map[int]int{}
	for _, id := range ids {
		if n := p.counts[id]; n > 0 {
			out[id] = n
		}
	}
	return out
}

func (p *importedProbe) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

// discoverServerWithCards wires a discover feed of the given model ids.
func discoverServerWithCards(t *testing.T, ids []int) (*Server, *tagSearchReader) {
	t.Helper()
	r := &tagSearchReader{byTag: map[string][]int{}, noTag: ids}
	srv := newTestServer(t)
	srv.reader = r
	return srv, r
}

// TestDiscoverCardShowsViewWhenAlreadyImported is the core flip, and it asserts
// BOTH directions from ONE render so the fixture cannot be green by accident: the
// same grid carries an imported model and a not-imported one.
func TestDiscoverCardShowsViewWhenAlreadyImported(t *testing.T) {
	const importedID, freshID = 101, 202
	srv, _ := discoverServerWithCards(t, []int{importedID, freshID})
	probe := &importedProbe{counts: map[int]int{importedID: 3}}
	srv.importedWorkflowsFn = probe.fn

	body := get(t, srv, "/workflows/discover").Body.String()

	// --- the IMPORTED card -----------------------------------------------------
	imported := cardFragment(t, body, importedID)
	wantHref := fmt.Sprintf("/library?tab=workflows&amp;model=%d", importedID)
	if !strings.Contains(imported, wantHref) {
		t.Errorf("the imported card must link into the library filtered to that model (%s); "+
			"card = %q", wantHref, imported)
	}
	if !strings.Contains(imported, "View 3 in library") {
		t.Errorf("the imported card must offer View with the count it actually holds; card = %q",
			imported)
	}
	if strings.Contains(imported, "Import workflows") {
		t.Errorf("the imported card still shows the Import CTA — re-importing it can only "+
			"report \"0 imported, N already present\"; card = %q", imported)
	}

	// --- the NOT-imported card -------------------------------------------------
	fresh := cardFragment(t, body, freshID)
	if !strings.Contains(fresh, "Import workflows") {
		t.Errorf("a model with nothing in the library must still offer Import; card = %q", fresh)
	}
	if strings.Contains(fresh, "in library") {
		t.Errorf("a model with nothing in the library must NOT claim to be there; card = %q", fresh)
	}
}

// TestImportedViewHrefMatchesTheImportResultDestination pins the two together by
// CONSTRUCTION rather than by two copies of a format string: if the import result
// ever moves, this fails.
func TestImportedViewHrefMatchesTheImportResultDestination(t *testing.T) {
	const modelID = 555
	view := renderString(t, workflowImportOrView(modelID, "csrf", 2))
	// workflowImportResult with no single-workflow anchor is the plain
	// "View in library" destination the import lands on.
	result := renderString(t, workflowImportResult(modelID, "Imported 2 workflow(s).", true, 0))

	want := workflowsLibraryHref(modelID)
	if want != "/library?tab=workflows&model=555" {
		t.Fatalf("workflowsLibraryHref = %q — the library deep link changed shape; the "+
			"assertions below describe the OLD one", want)
	}
	esc := strings.ReplaceAll(want, "&", "&amp;")
	if !strings.Contains(view, esc) {
		t.Errorf("the imported card's View href is not %q; got %q", esc, view)
	}
	if !strings.Contains(result, esc) {
		t.Errorf("the import RESULT link is not %q; got %q", esc, result)
	}
}

// TestPartiallyImportedCardKeepsAWayToImportTheRest.
//
// Nothing at render time can distinguish "all 22 imported" from "3 of 22" — that
// needs the remote archive's contents, which a browse page must not download. So
// the imported state does not HIDE importing: View is the primary action, and a
// de-emphasised "Import again" reaches the same audited, CSRF-protected,
// idempotent endpoint. Without it, "import the rest" would be unreachable from
// every surface in the app.
func TestPartiallyImportedCardKeepsAWayToImportTheRest(t *testing.T) {
	const modelID = 777
	html := renderString(t, workflowImportOrView(modelID, "tok-abc", 3))

	if !strings.Contains(html, "Import again") {
		t.Errorf("an imported card must keep a way to fetch workflows that are not in the "+
			"library yet — a partially-imported model would otherwise be stuck; got %q", html)
	}
	post := fmt.Sprintf(`hx-post="/workflows/discover/%d/import"`, modelID)
	if !strings.Contains(html, post) {
		t.Errorf("the re-import control must reuse the EXISTING audited endpoint (%s); got %q",
			post, html)
	}
	if !strings.Contains(html, "tok-abc") {
		t.Errorf("the re-import control must carry the CSRF token; got %q", html)
	}
	// It must NOT be the primary action — View is.
	if strings.Contains(html, `data-variant="filled"`) &&
		strings.Index(html, "Import again") < strings.Index(html, "View 3 in library") {
		t.Errorf("Import again must not precede/outrank the View action; got %q", html)
	}
}

// TestImportedLookupIsOneBoundedQueryPerPage is the scaling guard.
//
// It renders a grid of 20 cards and requires EXACTLY ONE lookup carrying ALL of
// them. A per-card implementation makes this 20.
func TestImportedLookupIsOneBoundedQueryPerPage(t *testing.T) {
	ids := make([]int, 0, 20)
	for i := 0; i < 20; i++ {
		ids = append(ids, 1000+i)
	}
	srv, _ := discoverServerWithCards(t, ids)
	probe := &importedProbe{counts: map[int]int{ids[0]: 1, ids[7]: 4}}
	srv.importedWorkflowsFn = probe.fn

	body := get(t, srv, "/workflows/discover").Body.String()

	// Fixture sanity: the grid really did render all 20 cards, so "1 call" is a
	// statement about batching and not about an empty page.
	for _, id := range ids {
		if !strings.Contains(body, fmt.Sprintf(`href="/models/%d"`, id)) {
			t.Fatalf("fixture is miscalibrated: model %d never rendered, so this test cannot "+
				"detect a per-card lookup", id)
		}
	}

	if n := probe.callCount(); n != 1 {
		t.Fatalf("the imported-workflows lookup ran %d times for a %d-card page — it must be "+
			"ONE batched query for the whole page, not one per card", n, len(ids))
	}
	if got := len(probe.batches[0]); got != len(ids) {
		t.Errorf("the single lookup was handed %d ids for a %d-card page — it must cover the "+
			"whole page in one go", got, len(ids))
	}
	// And the answer actually reached the cards, in both directions.
	if !strings.Contains(cardFragment(t, body, ids[7]), "View 4 in library") {
		t.Errorf("the batched result did not reach card %d", ids[7])
	}
	if !strings.Contains(cardFragment(t, body, ids[3]), "Import workflows") {
		t.Errorf("card %d has nothing in the library and must still offer Import", ids[3])
	}
}

// TestImportedLookupFailsSoftTowardImport: a lookup error must not claim
// "already in your library" (which would hide the import behind a link to an
// empty list). Offering the import is safe — it is idempotent.
func TestImportedLookupFailsSoftTowardImport(t *testing.T) {
	srv, _ := discoverServerWithCards(t, []int{404})
	srv.importedWorkflowsFn = func(context.Context, []int) map[int]int { return nil }

	card := cardFragment(t, get(t, srv, "/workflows/discover").Body.String(), 404)
	if !strings.Contains(card, "Import workflows") {
		t.Errorf("an unavailable lookup must degrade to the Import CTA; card = %q", card)
	}
}

// TestImportedLookupUsesTheRealStore exercises the PRODUCTION path (no seam) end
// to end: a workflow row inserted with model_id = N must flip that card.
func TestImportedLookupUsesTheRealStore(t *testing.T) {
	const modelID = 8080
	srv, _ := discoverServerWithCards(t, []int{modelID, 9090})
	mid := modelID
	if _, err := srv.store.InsertWorkflow(context.Background(), &store.Workflow{
		Name: "imported", Format: store.WorkflowFormatAPI,
		Graph:  `{"1":{"class_type":"KSampler","inputs":{"seed":1}}}`,
		Source: store.WorkflowSourceCivitai, ModelID: &mid,
	}); err != nil {
		t.Fatal(err)
	}

	body := get(t, srv, "/workflows/discover").Body.String()
	if !strings.Contains(cardFragment(t, body, modelID), "View 1 in library") {
		t.Errorf("a row stored with model_id=%d must flip that card without any test seam; "+
			"card = %q", modelID, cardFragment(t, body, modelID))
	}
	if !strings.Contains(cardFragment(t, body, 9090), "Import workflows") {
		t.Errorf("the unrelated model must still offer Import")
	}
}

// cardFragment slices out the markup of ONE result card, from its own model link
// to the next card's (or the end). Asserting against the whole page body would
// let another card's markup satisfy the assertion.
func cardFragment(t *testing.T, body string, modelID int) string {
	t.Helper()
	anchor := fmt.Sprintf(`href="/models/%d"`, modelID)
	i := strings.Index(body, anchor)
	if i < 0 {
		t.Fatalf("model %d does not appear in the rendered grid at all", modelID)
	}
	rest := body[i:]
	// The next card starts at the next `href="/models/` that is not this one.
	if j := strings.Index(rest[len(anchor):], `href="/models/`); j >= 0 {
		return rest[:len(anchor)+j]
	}
	return rest
}
