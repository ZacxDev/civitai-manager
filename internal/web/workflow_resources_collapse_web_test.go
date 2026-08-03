package web

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/store"
)

// resourcesCardSlice returns the page bytes belonging to the "Referenced resources"
// card: from its heading up to the start of the NEXT <h2>.
//
// 🔴 It exists because searching the whole page for a <details> silently finds the
// WRONG one. The workflow detail page renders further disclosures below this card
// (the Graph and Details sections), so a mutation that removes this card's disclosure
// entirely leaves detailsExtent measuring an unrelated element several kilobytes
// later — which still fails, but for a reason that has nothing to do with the
// invariant, and which would just as happily PASS if that later disclosure happened
// to contain a matching string. Bounding the search to the card is what makes the
// containment assertions mean what they say. Measured while mutation-testing this
// guard: the unbounded form reported "resource here.safetensors vanished from the
// card" when the resource was on screen the whole time.
func resourcesCardSlice(t *testing.T, page string) string {
	t.Helper()
	i := strings.Index(page, "Referenced resources")
	if i < 0 {
		t.Fatal("no Referenced resources heading")
	}
	rest := page[i:]
	if j := strings.Index(rest, "<h2"); j > 0 {
		return rest[:j]
	}
	return rest
}

// comfyOnlyResource is a filename the seeded /object_info fixture lists but the local
// library does NOT hold — i.e. a ◎ chip: usable by a run, absent from the library.
//
// 🔴 Every fixture below must contain one. Without it `have(base)` and
// resourceMarkState(...) != missing agree on every input, so a summary that counted
// through the wrong one is INDISTINGUISHABLE from a correct one. Measured: mutating
// countMissingResources to a bare `!resolver.have(...)` SURVIVED both tests in this
// file until this resource was added.
const comfyOnlyResource = "cached_a.safetensors"

// seedResourcesWorkflow builds the shape the operator's workflow 590 has: several
// referenced resources, only some of which are on this machine.
//
// It returns the workflow id and the page HTML. The seeded local file is what makes
// the ✓/✗ split real — a fixture where everything is missing could not tell a count
// of TOTAL from a count of MISSING, and the summary asserts both — and the seeded
// /object_info is what makes the ◎ tier reachable (see comfyOnlyResource).
func seedResourcesWorkflow(t *testing.T, srv *Server, resources []string, present ...string) (string, string) {
	t.Helper()
	if err := srv.store.PutComfyObjectInfo([]byte(objectInfoFixture)); err != nil {
		t.Fatalf("seed comfy model cache: %v", err)
	}
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
	resources := []string{"here.safetensors", comfyOnlyResource, "gone.safetensors", "also-gone.safetensors"}
	wfID, page := seedResourcesWorkflow(t, srv, resources, "here.safetensors")

	// PRECONDITIONS. Without these every assertion below could pass on a page whose
	// card never rendered, or whose fixture makes total == missing so the summary
	// cannot discriminate between the two numbers.
	if !strings.Contains(page, "Referenced resources") {
		t.Fatalf("precondition: the resources card did not render:\n%s", page)
	}
	// 🔴 ALL THREE tiers must be present. ✓/✗ alone leaves `have()` and
	// resourceMarkState agreeing on every input, so a summary counting through the
	// wrong one passes — measured, that mutation survived until ◎ was seeded.
	for _, state := range []string{`data-have="yes"`, `data-have="comfy"`, `data-have="no"`} {
		if !strings.Contains(page, state) {
			t.Fatalf("precondition: the fixture must produce a %s chip, or the summary's "+
				"counts cannot discriminate a correct rule from a wrong one:\n%s", state, page)
		}
	}

	// THE SUMMARY: 4 mentioned, 2 not here — and 2 is NOT 4-minus-the-one-local-file,
	// so a count that ignored the ComfyUI tier would report 3 and fail.
	want := "4 files this workflow mentions · 2 not on this machine"
	if !strings.Contains(page, want) {
		t.Errorf("want the counting summary %q:\n%s", want, page)
	}

	// THE COLLAPSE: the chips and the scope sentence are inside the disclosure —
	// searched WITHIN this card, never across the whole page (see resourcesCardSlice).
	cardHTML := resourcesCardSlice(t, page)
	if !strings.Contains(cardHTML, "<details") {
		// Asserted HERE, before the extent helper, so a card that collapsed nothing
		// fails with this guard's own sentence rather than with detailsExtent's
		// "no <details>" — the invariant, not the instrument.
		t.Fatalf("the resources card renders no disclosure: its chip row restates the "+
			"run-failure panel above it and must be one click away, not inline:\n%s", cardHTML)
	}
	start, end := detailsExtent(t, cardHTML, 0)
	inside := cardHTML[start:end]
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
	for _, res := range resources {
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
	// The disclosure is CLOSED by default — an `open` attribute would restore the
	// wall of chips this change exists to fold away.
	if strings.Contains(cardHTML[start:start+40], " open") {
		t.Errorf("the resources disclosure must render closed:\n%s", cardHTML[start:start+40])
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
		[]string{"a.safetensors", "b.safetensors", comfyOnlyResource, "c.safetensors", "d.safetensors"},
		"a.safetensors", "b.safetensors")

	card := resourcesCardSlice(t, page)

	// ⚠ Count the WRAPPER, one per resource. `cm-res-chip` appears on the chip AND on
	// its detail popover, so counting that class returns 2× the real number — which is
	// exactly what the precondition below caught while this test was being written.
	gotMissing := strings.Count(card, `data-have="no"`)
	gotTotal := strings.Count(card, "cm-res-chip-wrap")

	// PRECONDITION: the fixture really rendered five chips — two present, one
	// ComfyUI-only, two missing. All three tiers, and total != missing != present, so
	// no two of the three plausible counting rules produce the same number here.
	gotComfy := strings.Count(card, `data-have="comfy"`)
	if gotTotal != 5 || gotMissing != 2 || gotComfy != 1 {
		t.Fatalf("precondition: want 5 chips (2 present, 1 comfy-only, 2 missing), got "+
			"total=%d missing=%d comfy=%d\n%s", gotTotal, gotMissing, gotComfy, card)
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
