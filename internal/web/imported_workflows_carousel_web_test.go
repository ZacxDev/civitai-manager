package web

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/store"
)

// importedWorkflowFixtures builds n plausible imported workflows (ids 1..n, all
// linked to model 7), for the model detail page's imported-workflows carousel.
// Shared with TestWorkflowImportSectionStates.
func importedWorkflowFixtures(n int) []store.Workflow {
	modelID := 7
	out := make([]store.Workflow, 0, n)
	for i := 1; i <= n; i++ {
		out = append(out, store.Workflow{
			ID:        int64(i),
			Name:      "imported workflow " + strconv.Itoa(i),
			Format:    store.WorkflowFormatAPI,
			Source:    store.WorkflowSourceCivitai,
			BaseModel: "SDXL 1.0",
			ModelID:   &modelID,
		})
	}
	return out
}

// TestImportedWorkflowsCarouselRendersOneCardPerWorkflow — N imported workflows
// produce N cards in ONE carousel, each linking to its own workflow.
func TestImportedWorkflowsCarouselRendersOneCardPerWorkflow(t *testing.T) {
	const n = 4
	out := renderString(t, importedWorkflowsCarousel(importedWorkflowFixtures(n)))

	if got := strings.Count(out, `class="cm-carousel-card"`); got != n {
		t.Errorf("want %d carousel items, got %d:\n%s", n, got, out)
	}
	if got := strings.Count(out, "cm-carousel-wrap"); got != 1 {
		t.Errorf("the cards belong in ONE carousel, got %d wrappers:\n%s", got, out)
	}
	for i := 1; i <= n; i++ {
		id := strconv.Itoa(i)
		if !strings.Contains(out, `href="/workflows/`+id+`"`) {
			t.Errorf("card %s must link to its workflow:\n%s", id, out)
		}
		if !strings.Contains(out, "imported workflow "+id) {
			t.Errorf("card %s must print its name:\n%s", id, out)
		}
	}
	// The prev/next controls are the SHARED ones (same handler as every other
	// carousel), so no page needs a second scroller script.
	if !strings.Contains(out, "cmCarouselScroll(this,1)") ||
		!strings.Contains(out, "cmCarouselScroll(this,-1)") {
		t.Errorf("a multi-card carousel must carry the shared prev/next controls:\n%s", out)
	}
}

// TestImportedWorkflowsCarouselRendersNothingWhenEmpty — zero imported
// workflows must produce NOTHING: no heading, no wrapper, no empty strip.
//
// The node must be NIL, not merely empty: it is rendered as a child of the
// section's card, and gomponents skips a nil child entirely. Anything non-nil
// here is a wrapper that would land on the page.
func TestImportedWorkflowsCarouselRendersNothingWhenEmpty(t *testing.T) {
	for _, c := range []struct {
		name string
		wfs  []store.Workflow
	}{
		{"nil", nil},
		{"empty slice", []store.Workflow{}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if node := importedWorkflowsCarousel(c.wfs); node != nil {
				t.Errorf("an empty imported-workflows carousel must render NOTHING, got:\n%s",
					renderString(t, node))
			}
		})
	}

	// ...and the same through the section that hosts it: a zero count renders the
	// import CTA and nothing carousel-shaped.
	section := renderString(t, workflowImportDetailCard(7, "csrf", 0, nil))
	for _, banned := range []string{"cm-carousel", "Already imported", "Showing the"} {
		if strings.Contains(section, banned) {
			t.Errorf("a not-yet-imported model must not render %q:\n%s", banned, section)
		}
	}
}

// TestImportedWorkflowsSectionIsBounded — a library holding far more imported
// workflows than the cap paints EXACTLY the cap, the sentence still reports the
// TRUE total, the withheld remainder is disclosed, and the "see all" affordance
// (the library link) is present.
//
// It drives the REAL handler with 23 rows in the store rather than handing the
// renderer a pre-sliced fixture. That distinction is the whole point: the
// renderer does NOT slice — the bound is store.ListWorkflowsByModel's SQL LIMIT
// and the cap the handler passes it — so a test that slices its own fixture to
// the cap and then asserts the cap proves nothing. (That was this test's first
// version, and raising the cap to 12 left the card count GREEN at 12/12.)
//
// MUTATION-VERIFIED, with the handler driving it:
//   - importedWorkflowsCap 8 → 12 fails with
//     "the page must paint exactly 8 imported-workflow cards, got 12"
//   - dropping the cap argument (passing 0/-1, i.e. no limit) fails the same
//     assertion with 0 cards, since a non-positive limit yields no rows.
//   - removing importedWorkflowsOverflowNote fails with
//     "the section must disclose that 23 workflows exist but only 8 are shown"
func TestImportedWorkflowsSectionIsBounded(t *testing.T) {
	const total = 23
	if importedWorkflowsCap != 8 {
		t.Fatalf("importedWorkflowsCap changed to %d — this test pins the shipped "+
			"bound and its copy; update both deliberately", importedWorkflowsCap)
	}
	if total <= importedWorkflowsCap {
		t.Fatalf("the fixture must EXCEED the cap or nothing is bounded: %d vs %d",
			total, importedWorkflowsCap)
	}

	reader := newModelReader(t)
	m := *reader.model
	m.Type = "Workflows"
	reader.model = &m
	srv := newModelServer(t, reader)

	modelID := 7
	for i := 1; i <= total; i++ {
		if _, err := srv.store.InsertWorkflow(context.Background(), &store.Workflow{
			Name: "bounded wf " + strconv.Itoa(i), Format: store.WorkflowFormatAPI,
			Graph:  `{"1":{"class_type":"KSampler","inputs":{"seed":` + strconv.Itoa(i) + `}}}`,
			Source: store.WorkflowSourceCivitai, ModelID: &modelID,
		}); err != nil {
			t.Fatal(err)
		}
	}

	out := getModelPage(t, srv, "/models/7")

	if got := strings.Count(out, `class="cm-carousel-card"`); got != 8 {
		t.Errorf("the page must paint exactly 8 imported-workflow cards, got %d", got)
	}
	// The count in the copy is the TRUE total, not the number of cards.
	if !strings.Contains(out, "23 workflows from this model are in your workflow library") {
		t.Errorf("the sentence must report the true total (%d)", total)
	}
	// The subset is disclosed, so 8 cards under "23 workflows" is not a bug report.
	if !strings.Contains(out, "Showing the 8 most recent.") {
		t.Errorf("the section must disclose that %d workflows exist but only 8 are shown", total)
	}
	// "See all" — the way to the ones past the cap.
	if !strings.Contains(out, "View in library") ||
		!strings.Contains(out, `href="/library?tab=workflows&amp;model=7"`) {
		t.Error("the see-all affordance (the library link) must be present")
	}
	// Newest first: the last-inserted rows are the ones kept, not the first 8.
	if !strings.Contains(out, "bounded wf 23") {
		t.Error("the cap must keep the NEWEST workflows")
	}
	if strings.Contains(out, "bounded wf 1<") {
		t.Error("the cap must drop the OLDEST workflows")
	}

	// Under the cap, nothing is withheld — so nothing claims otherwise.
	under := renderString(t, workflowImportDetailCard(7, "csrf", 3, importedWorkflowFixtures(3)))
	if strings.Contains(under, "Showing the") {
		t.Errorf("nothing was withheld, so the subset note must be absent:\n%s", under)
	}
	if !strings.Contains(under, "View in library") {
		t.Errorf("the library link must be present even when nothing is withheld:\n%s", under)
	}
}

// TestImportedWorkflowsCarouselUsesTheSharedCardRenderer proves the cards come
// from the SHARED workflowCard renderer (its compact variant), not a fork.
//
// It asserts structure a fork would have to reproduce deliberately AND that the
// compact variant is byte-identical to calling workflowCardCompact directly — so
// a forked copy could only pass by being the same function.
func TestImportedWorkflowsCarouselUsesTheSharedCardRenderer(t *testing.T) {
	wf := importedWorkflowFixtures(1)[0]

	shared := renderString(t, workflowCardCompact(wf))
	inCarousel := renderString(t, importedWorkflowsCarousel([]store.Workflow{wf}))
	if !strings.Contains(inCarousel, shared) {
		t.Errorf("the carousel must embed the SHARED card renderer's exact output.\n"+
			"shared:\n%s\ncarousel:\n%s", shared, inCarousel)
	}

	// Structure the shared card owns: the lift card shell, the shared format badge
	// vocabulary, and the shared Run deep link (workflowRunDeepLink).
	for _, want := range []string{
		"cm-lift",
		"Runnable API",           // workflowFormatBadge
		"SDXL 1.0",               // the base-model badge
		"Discovered",             // the source badge, via optionLabel(workflowSourceFilterOptions…)
		workflowRunDeepLink("1"), // the shared Run target, fragment and all
	} {
		if !strings.Contains(shared, want) {
			t.Errorf("the compact card must keep the shared card's %q:\n%s", want, shared)
		}
	}

	// ...and the full library card still renders the parts compact drops, so
	// "compact" stayed a VARIANT rather than quietly becoming the only card.
	full := renderString(t, workflowCard(wf, "csrf", workflowResolver{nsfwMode: NSFWBlur}))
	for _, want := range []string{"Delete", "View post", workflowRunDeepLink("1")} {
		if !strings.Contains(full, want) {
			t.Errorf("the FULL workflow card must still render %q:\n%s", want, full)
		}
	}
	// The compact variant takes no csrf and must not be able to render a
	// state-changing control.
	for _, banned := range []string{"hx-post", "csrf_token", "Delete"} {
		if strings.Contains(shared, banned) {
			t.Errorf("the compact card must not render %q (it has no csrf token):\n%s",
				banned, shared)
		}
	}
	// It must also not nest an image carousel inside the card carousel — that is
	// the escaping-decoration stacking hazard the variant exists to avoid.
	for _, banned := range []string{"cm-carousel-item", "cm-video-badge", "cm-blur"} {
		if strings.Contains(shared, banned) {
			t.Errorf("the compact card must not carry in-tile image decoration (%q):\n%s",
				banned, shared)
		}
	}
}

// TestWorkflowsModelPageRendersTheImportedCarousel drives the REAL handler: a
// Workflows-type model with rows in the store paints the cards on the page, so
// the loadModelView → view → renderer wiring is covered end to end and not only
// the renderer in isolation.
func TestWorkflowsModelPageRendersTheImportedCarousel(t *testing.T) {
	reader := newModelReader(t)
	m := *reader.model
	m.Type = "Workflows"
	reader.model = &m
	srv := newModelServer(t, reader)

	modelID := 7
	for i := 1; i <= 3; i++ {
		if _, err := srv.store.InsertWorkflow(context.Background(), &store.Workflow{
			Name: "carousel wf " + strconv.Itoa(i), Format: store.WorkflowFormatAPI,
			Graph:  `{"1":{"class_type":"KSampler","inputs":{"seed":` + strconv.Itoa(i) + `}}}`,
			Source: store.WorkflowSourceCivitai, ModelID: &modelID,
		}); err != nil {
			t.Fatal(err)
		}
	}

	body := getModelPage(t, srv, "/models/7")
	if got := strings.Count(body, `class="cm-carousel-card"`); got != 3 {
		t.Errorf("the model page must paint 3 imported-workflow cards, got %d", got)
	}
	for i := 1; i <= 3; i++ {
		if !strings.Contains(body, "carousel wf "+strconv.Itoa(i)) {
			t.Errorf("the model page is missing imported workflow %d:\n%s", i, body)
		}
	}
	// The scroller the carousel's buttons call must be on the page.
	if !strings.Contains(body, "function cmCarouselScroll") {
		t.Error("the model page must emit the shared carousel scroller")
	}
	// The link through to the library survives.
	if !strings.Contains(body, "View in library") {
		t.Error("the model page must still link through to the library")
	}
}
