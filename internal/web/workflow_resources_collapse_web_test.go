package web

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/store"
)

// seedResourcesWorkflow builds the shape the operator's workflow 590 has: several
// referenced resources, only some of which are on this machine.
//
// It returns the workflow id and the page HTML. The seeded local file is what makes
// the ✓/✗ split real — a fixture where everything is missing could not tell a count
// of TOTAL from a count of MISSING, and the summary asserts both.
func seedResourcesWorkflow(t *testing.T, srv *Server, resources []string, present ...string) (string, string) {
	t.Helper()
	for _, p := range present {
		if err := srv.store.UpsertLocalFile(store.LocalFile{
			Path: "/mnt/models/checkpoints/" + p, SizeBytes: 10,
			Status: store.LocalStatusMatched, Kind: store.LocalKindModel,
		}); err != nil {
			t.Fatalf("seed local file %s: %v", p, err)
		}
	}
	id, err := srv.store.InsertWorkflow(context.Background(), &store.Workflow{
		Name: "res-collapse", Format: store.WorkflowFormatAPI, Graph: "{}",
		Source: store.WorkflowSourceImported, Resources: resources,
	})
	if err != nil {
		t.Fatalf("seed workflow: %v", err)
	}
	wfID := strconv.FormatInt(id, 10)
	return wfID, get(t, srv, "/workflows/"+wfID).Body.String()
}

// TestReferencedResourcesCardCollapsesItsChips is the guard for the third finding in
// the "wall of text" report: the page states the failure twice.
//
// 🔴 MEASURED on workflow 590: the run-failure panel is 804px tall in a 669px
// viewport and names 3 missing model files; 77px below its bottom edge this card
// listed 9 chips, 6 of them ✗, including all 3 the panel had just named. #60 labelled
// both surfaces ("what a run would load" vs "including pipelines that are switched
// off"), which made them honest but left them redundant.
//
// The chips are now behind a disclosure whose summary states the COUNTS — which the
// chips only implied — so the collapsed card cross-links the two surfaces (this is the
// superset, and here is how big it is) instead of re-enumerating the failure.
func TestReferencedResourcesCardCollapsesItsChips(t *testing.T) {
	srv := newWorkflowServer(t)
	wfID, page := seedResourcesWorkflow(t,
		srv,
		[]string{"here.safetensors", "gone.safetensors", "also-gone.safetensors"},
		"here.safetensors")

	// PRECONDITIONS. Without these every assertion below could pass on a page whose
	// card never rendered, or whose fixture makes total == missing so the summary
	// cannot discriminate between the two numbers.
	if !strings.Contains(page, "Referenced resources") {
		t.Fatalf("precondition: the resources card did not render:\n%s", page)
	}
	if !strings.Contains(page, `data-have="yes"`) || !strings.Contains(page, `data-have="no"`) {
		t.Fatalf("precondition: the fixture must produce BOTH a present and a missing " +
			"chip, or the summary's two counts are indistinguishable")
	}

	// THE SUMMARY: 3 mentioned, 2 not here. Two DIFFERENT numbers, so a summary that
	// reported the same value twice would fail.
	want := "3 files this workflow mentions · 2 not on this machine"
	if !strings.Contains(page, want) {
		t.Errorf("want the counting summary %q:\n%s", want, page)
	}

	// THE COLLAPSE: the chips and the scope sentence are inside the disclosure.
	i := strings.Index(page, "Referenced resources")
	start, end := detailsExtent(t, page, i)
	inside := page[start:end]
	if !strings.Contains(inside, "cm-res-chips") {
		t.Errorf("the chip row must be behind the disclosure — it is what restates the "+
			"failure panel above it:\n%s", inside)
	}
	if !strings.Contains(inside, "including pipelines that are switched off") {
		t.Errorf("the scope sentence must travel WITH the chips it describes:\n%s", inside)
	}

	// 🔴 NOTHING DELETED. This card is the only surface listing resources from
	// switched-off pipelines, so every chip must still be in the document — one click
	// away, not gone.
	for _, res := range []string{"here.safetensors", "gone.safetensors", "also-gone.safetensors"} {
		if !strings.Contains(inside, res) {
			t.Errorf("resource %q vanished from the card; collapsing must not delete "+
				"the switched-off-pipeline listing:\n%s", res, inside)
		}
	}
	// The heading stays OUTSIDE the disclosure: it is the section's landmark, and
	// burying it would leave the card unnavigable by heading.
	if strings.Contains(inside, "Referenced resources") {
		t.Errorf("the section heading must stay outside the disclosure:\n%s", inside)
	}
	_ = wfID
}

// TestResourcesSummaryCountsAgreeWithTheChips is the anti-drift half. The summary is a
// COUNT of the marks the chips render, so the two must be computed by one rule.
//
// 🔴 A summary reading "2 not on this machine" beside three ✗ chips is worse than no
// summary: it is the app contradicting itself about the one thing the collapsed card
// asserts. resourceMarkState is the single definition; this proves the card actually
// counts through it, by checking the summary against the rendered marks rather than
// against a second computation of its own.
func TestResourcesSummaryCountsAgreeWithTheChips(t *testing.T) {
	srv := newWorkflowServer(t)
	_, page := seedResourcesWorkflow(t,
		srv,
		[]string{"a.safetensors", "b.safetensors", "c.safetensors", "d.safetensors"},
		"a.safetensors", "b.safetensors")

	i := strings.Index(page, "Referenced resources")
	start, end := detailsExtent(t, page, i)
	card := page[:end]

	// ⚠ Count the WRAPPER, one per resource. `cm-res-chip` appears on the chip AND on
	// its detail popover, so counting that class returns 2× the real number — which is
	// exactly what the precondition below caught while this test was being written.
	gotMissing := strings.Count(card, `data-have="no"`)
	gotTotal := strings.Count(card, "cm-res-chip-wrap")

	// PRECONDITION: the fixture really rendered four chips, two of them missing —
	// otherwise the assertion is comparing a summary against nothing.
	if gotTotal != 4 || gotMissing != 2 {
		t.Fatalf("precondition: want 4 chips with 2 missing, got total=%d missing=%d\n%s",
			gotTotal, gotMissing, card[start:end])
	}

	want := resourcesSummaryLine(gotTotal, gotMissing)
	if !strings.Contains(page, want) {
		t.Errorf("the summary must agree with the marks it counts — want %q derived from "+
			"the rendered chips:\n%s", want, page)
	}
	// And it must not be the OTHER plausible sentence: "all on this machine" while
	// two chips say otherwise.
	if strings.Contains(page, "all on this machine") {
		t.Errorf("the summary claims everything is present while 2 chips are ✗:\n%s", page)
	}
}

// TestResourcesSummaryLineCopy pins the two branches and the singular, which are pure
// string assembly and therefore cheap to get subtly wrong.
func TestResourcesSummaryLineCopy(t *testing.T) {
	cases := []struct {
		total, missing int
		want           string
	}{
		{1, 0, "1 file this workflow mentions · all on this machine"},
		{1, 1, "1 file this workflow mentions · 1 not on this machine"},
		{9, 6, "9 files this workflow mentions · 6 not on this machine"},
		{9, 0, "9 files this workflow mentions · all on this machine"},
	}
	for _, c := range cases {
		if got := resourcesSummaryLine(c.total, c.missing); got != c.want {
			t.Errorf("resourcesSummaryLine(%d, %d) = %q, want %q", c.total, c.missing, got, c.want)
		}
	}
}
