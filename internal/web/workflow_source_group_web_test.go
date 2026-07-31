package web

import (
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/store"
)

// srcWF builds a library workflow attributed to a CivitAI source post (a
// Workflows-TYPE model id — CivitAI serves workflow posts from /models/<id> URLs,
// so the id is a model id and the thing is not a checkpoint). srcID 0 means NO
// source: pasted, PNG-extracted, authored locally or disk-scanned.
func srcWF(id int64, name string, srcID int) store.Workflow {
	wf := store.Workflow{
		ID:        id,
		Name:      name,
		Format:    store.WorkflowFormatAPI,
		Source:    store.WorkflowSourceCivitai,
		CreatedAt: time.Unix(1700000000+id, 0),
	}
	if srcID > 0 {
		s := srcID
		wf.ModelID = &s
	} else {
		wf.Source = store.WorkflowSourceScanned
	}
	return wf
}

func countItems(html string) int {
	return strings.Count(html, `class="cm-wf-item"`)
}

// renderWFList renders the library workflow list the way the Library tab does.
func renderWFList(t *testing.T, wfs []store.Workflow, res workflowResolver) string {
	t.Helper()
	return renderString(t, workflowList(wfs, "csrf-tok", true, res))
}

// TestOneSourceCollapsesToOneItem is the reported bug: several workflows imported
// from ONE CivitAI post rendered as several near-identical cards (same showcase,
// same "from …" line), so the list read as duplicates.
func TestOneSourceCollapsesToOneItem(t *testing.T) {
	wfs := []store.Workflow{
		srcWF(1, "Flux basic", 1386234),
		srcWF(2, "Flux upscale", 1386234),
		srcWF(3, "Flux inpaint", 1386234),
		srcWF(4, "Other pack", 999),
	}
	out := renderWFList(t, wfs, workflowResolver{mr: fullMaturityRange()})

	if got := countItems(out); got != 2 {
		t.Fatalf("%d list items for 4 workflows from 2 sources, want 2:\n%s", got, out)
	}
	// The collapsed item shows its FIRST member and offers the rest.
	if !strings.Contains(out, `id="wf-1"`) {
		t.Errorf("the group should show its first member (wf-1):\n%s", out)
	}
	if !strings.Contains(out, "cm-wf-pick") {
		t.Errorf("a 3-workflow source must offer a picker:\n%s", out)
	}
	// NOTHING IS HIDDEN: every member is an option in that picker.
	for _, want := range []string{"Flux basic", "Flux upscale", "Flux inpaint"} {
		if !strings.Contains(out, want) {
			t.Errorf("member %q is unreachable — it is in no card and no option:\n%s", want, out)
		}
	}
}

// TestSingleWorkflowSourceRendersNoSelect: a one-option dropdown is a control that
// cannot do anything, and offering it implies there is something else to choose.
func TestSingleWorkflowSourceRendersNoSelect(t *testing.T) {
	wfs := []store.Workflow{srcWF(1, "Only one", 4242)}
	out := renderWFList(t, wfs, workflowResolver{mr: fullMaturityRange()})

	if got := countItems(out); got != 1 {
		t.Fatalf("%d items for 1 workflow, want 1", got)
	}
	if strings.Contains(out, "cm-wf-pick") {
		t.Errorf("a source with one workflow must render no picker:\n%s", out)
	}
	// Scoped to the picker's own select — the list's sort/filter controls bar above
	// legitimately has <select>s of its own.
	if strings.Contains(out, "cm-wf-pick-select") {
		t.Errorf("a source with one workflow must render no picker <select>:\n%s", out)
	}
	// And no orphan anchors either — there is nothing behind a select.
	if strings.Contains(out, "cm-wf-alt-anchor") {
		t.Errorf("a group of one needs no alternate anchors:\n%s", out)
	}
}

// TestUnlinkedWorkflowsAreNotBundled: a workflow with ModelID == nil has no
// source. Grouping them together would invent a "no source" bucket that is not a
// source at all and would hide unrelated workflows behind one representative.
func TestUnlinkedWorkflowsAreNotBundled(t *testing.T) {
	wfs := []store.Workflow{
		srcWF(1, "scanned a", 0),
		srcWF(2, "scanned b", 0),
		srcWF(3, "scanned c", 0),
	}
	out := renderWFList(t, wfs, workflowResolver{mr: fullMaturityRange()})

	if got := countItems(out); got != 3 {
		t.Fatalf("%d items for 3 source-less workflows, want 3 (each stands alone):\n%s", got, out)
	}
	if strings.Contains(out, "cm-wf-pick") {
		t.Errorf("source-less workflows must not be collapsed behind a picker:\n%s", out)
	}
	for _, id := range []string{`id="wf-1"`, `id="wf-2"`, `id="wf-3"`} {
		if !strings.Contains(out, id) {
			t.Errorf("missing %s — a source-less workflow lost its own item:\n%s", id, out)
		}
	}
}

// TestGroupingUnit exercises the partitioner directly, including the ordering and
// pick rules the renderer depends on.
func TestGroupingUnit(t *testing.T) {
	wfs := []store.Workflow{
		srcWF(10, "a", 100),
		srcWF(11, "loose", 0),
		srcWF(12, "b", 100),
		srcWF(13, "c", 200),
		srcWF(14, "loose2", 0),
	}

	groups := groupWorkflowsBySource(wfs, nil)
	if len(groups) != 4 {
		t.Fatalf("got %d groups, want 4", len(groups))
	}
	// A group takes the position of its FIRST member, so the server's
	// newest-imported-first order still means something.
	if groups[0].SourceID != 100 || len(groups[0].Members) != 2 {
		t.Errorf("group 0 = %+v, want source 100 with 2 members", groups[0])
	}
	if groups[1].SourceID != 0 || groups[1].Members[0].ID != 11 {
		t.Errorf("group 1 = %+v, want the loose workflow 11 standing alone", groups[1])
	}
	if groups[2].SourceID != 200 || groups[3].Members[0].ID != 14 {
		t.Errorf("groups 2/3 = %+v / %+v, want source 200 then loose 14", groups[2], groups[3])
	}
	for _, grp := range groups {
		if grp.Selected != 0 {
			t.Errorf("with no picks every group must show its first member, got %d", grp.Selected)
		}
	}

	// A pick selects that member.
	picked := groupWorkflowsBySource(wfs, map[int64]bool{12: true})
	if picked[0].shown().ID != 12 {
		t.Errorf("?pick=12 should show workflow 12, got %d", picked[0].shown().ID)
	}
	if got := activeWorkflowPicks(picked); len(got) != 1 || got[0] != 12 {
		t.Errorf("activeWorkflowPicks = %v, want [12]", got)
	}

	// A pick naming a workflow that is not in the list — stale bookmark, deleted
	// workflow, hand-edited URL — degrades to the default rather than to an empty
	// item.
	stale := groupWorkflowsBySource(wfs, map[int64]bool{99999: true})
	for i, grp := range stale {
		if grp.Selected != 0 {
			t.Errorf("group %d honoured a pick for a workflow it does not hold", i)
		}
	}
	if got := activeWorkflowPicks(stale); len(got) != 0 {
		t.Errorf("a stale pick must not become an active pick, got %v", got)
	}
}

// TestPickerShowsThePickedMemberAndKeepsTheRestReachable is the round trip: a
// ?pick= names a member, that member's card is what renders, and the others stay
// reachable both as options and as deep-link anchors.
func TestPickerShowsThePickedMemberAndKeepsTheRestReachable(t *testing.T) {
	wfs := []store.Workflow{
		srcWF(1, "Flux basic", 1386234),
		srcWF(2, "Flux upscale", 1386234),
		srcWF(3, "Flux inpaint", 1386234),
	}
	res := workflowResolver{
		mr:       fullMaturityRange(),
		picks:    map[int64]bool{2: true},
		pickBase: url.Values{"tab": []string{"workflows"}},
	}
	out := renderWFList(t, wfs, res)

	if !strings.Contains(out, `class="cm-wf-item" data-name="Flux upscale"`) {
		t.Errorf("the picked member should be the one shown:\n%s", out)
	}
	if !strings.Contains(out, `<option value="2" selected>Flux upscale</option>`) {
		t.Errorf("the select must reflect the current pick:\n%s", out)
	}
	// The two it is not showing keep working #wf-<id> anchors, so every existing
	// deep link into the library still resolves.
	for _, id := range []string{`id="wf-1"`, `id="wf-3"`} {
		if !strings.Contains(out, id) {
			t.Errorf("deep-link anchor %s was lost when the group collapsed:\n%s", id, out)
		}
	}
	if !strings.Contains(out, `class="cm-wf-alt-anchor"`) {
		t.Errorf("the non-shown members must render alternate anchors:\n%s", out)
	}
	// And the text filter can still find them.
	if !strings.Contains(out, `data-alt-names="Flux basic Flux inpaint"`) {
		t.Errorf("data-alt-names must list the members behind the select:\n%s", out)
	}
}

// TestPickerIsARealGetFormThatWorksWithoutJS. The <select> plus its submit button
// navigate to /library with the picks in the query — no htmx, no swap contract to
// break inside the stable #workflow-scan-results container the workflow-scan
// poller owns. The onchange handler is an enhancement layered on top; nothing is
// reachable only through it.
func TestPickerIsARealGetFormThatWorksWithoutJS(t *testing.T) {
	wfs := []store.Workflow{srcWF(1, "a", 77), srcWF(2, "b", 77)}
	out := renderWFList(t, wfs, workflowResolver{
		mr:       fullMaturityRange(),
		pickBase: url.Values{"tab": []string{"workflows"}, "eco": []string{"flux-1"}},
	})

	if !strings.Contains(out, `method="get"`) {
		t.Errorf("the picker must be a real GET form:\n%s", out)
	}
	// The fragment survives GET submission, so the reload lands back on this item.
	if !strings.Contains(out, `action="/library#wf-1"`) {
		t.Errorf("the form must point back at the group's stable anchor:\n%s", out)
	}
	if !strings.Contains(out, `<button type="submit" class="cm-wf-pick-go">Show</button>`) {
		t.Errorf("without the submit button the control needs JavaScript to do anything:\n%s", out)
	}
	// Scoped to the form itself: the card elsewhere legitimately uses htmx (the
	// lazy source-name load).
	fi := strings.Index(out, `<form class="cm-wf-pick"`)
	if fi < 0 {
		t.Fatalf("no picker form:\n%s", out)
	}
	form := out[fi : fi+strings.Index(out[fi:], "</form>")]
	if strings.Contains(form, "hx-") {
		t.Errorf("the picker must not be htmx-driven — a full GET cannot orphan the "+
			"workflow-scan poller that owns this container:\n%s", form)
	}
	// The tab and the active facets ride along, or applying a pick would silently
	// dump the user back on an unfiltered default tab.
	for _, want := range []string{
		`<input type="hidden" name="tab" value="workflows">`,
		`<input type="hidden" name="eco" value="flux-1">`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the picker form drops %s:\n%s", want, out)
		}
	}
	// The select has an accessible name of its own.
	if !strings.Contains(out, `<label class="cm-sr-only" for="cm-wf-pick-77">`) {
		t.Errorf("the select needs a real <label for=…>:\n%s", out)
	}
}

// TestPickBaseIsAWhitelist: echoing the request's whole query back into hidden
// inputs would reflect arbitrary attacker-supplied parameter NAMES into the page.
func TestPickBaseIsAWhitelist(t *testing.T) {
	base := libraryPickBase(url.Values{
		"tab":     []string{"workflows"},
		"eco":     []string{"flux-1"},
		"use":     []string{"inpaint"},
		"model":   []string{"1386234"},
		"evil":    []string{`"><script>alert(1)</script>`},
		"flash":   []string{"imported"},
		"pick":    []string{"9"},
		"blank":   []string{"   "},
		"tabtrap": []string{"x"},
	})
	for _, k := range []string{"tab", "eco", "use", "model"} {
		if base.Get(k) == "" {
			t.Errorf("libraryPickBase dropped the whitelisted key %q", k)
		}
	}
	for _, k := range []string{"evil", "flash", "pick", "blank", "tabtrap"} {
		if _, ok := base[k]; ok {
			t.Errorf("libraryPickBase reflected the non-whitelisted key %q", k)
		}
	}
}

// TestPickerCarriesOtherGroupsPicks: applying one group's selection must not reset
// every other group on the page back to its default.
func TestPickerCarriesOtherGroupsPicks(t *testing.T) {
	wfs := []store.Workflow{
		srcWF(1, "A one", 100), srcWF(2, "A two", 100),
		srcWF(3, "B one", 200), srcWF(4, "B two", 200),
	}
	groups := groupWorkflowsBySource(wfs, map[int64]bool{4: true})
	active := activeWorkflowPicks(groups)
	if len(active) != 1 || active[0] != 4 {
		t.Fatalf("active picks = %v, want [4] — the fixture is not exercising the case", active)
	}

	// Group A's form must carry B's pick.
	a := renderString(t, workflowSourcePicker(groups[0], url.Values{}, otherPicks(active, groups[0])))
	if !strings.Contains(a, `<input type="hidden" name="pick" value="4">`) {
		t.Errorf("group A's form drops group B's selection:\n%s", a)
	}
	// Group B's own form must NOT carry its own pick as a hidden input — its select
	// already submits that name, and a duplicate would make the URL ambiguous.
	b := renderString(t, workflowSourcePicker(groups[1], url.Values{}, otherPicks(active, groups[1])))
	if strings.Contains(b, `<input type="hidden" name="pick" value="4">`) {
		t.Errorf("group B's form duplicates its own selection as a hidden input:\n%s", b)
	}
	if !strings.Contains(b, `<option value="4" selected>`) {
		t.Errorf("group B's select should carry the selection instead:\n%s", b)
	}
}

// TestParseWorkflowPicksIsLenient — a pick is a display preference. A malformed
// one must fall back to the default item, never to an error page or an empty list.
func TestParseWorkflowPicksIsLenient(t *testing.T) {
	got := parseWorkflowPicks(url.Values{"pick": []string{"12", " 34 ", "abc", "-1", "0", ""}})
	if len(got) != 2 || !got[12] || !got[34] {
		t.Fatalf("parseWorkflowPicks = %v, want {12,34}", got)
	}
	if parseWorkflowPicks(url.Values{}) != nil {
		t.Errorf("no picks should yield a nil map, not an empty one")
	}
}

// TestBadgesSitBesideTheTitle pins the layout change STRUCTURALLY rather than by
// substring: a `Contains` check on the badge would have passed before and after.
// The title anchor and the first badge must be in the SAME element, so there can be
// no closing </div> between them.
func TestBadgesSitBesideTheTitle(t *testing.T) {
	wf := srcWF(1, "Flux basic", 1386234)
	wf.BaseModel = "Flux.1 D"
	out := renderWFList(t, []store.Workflow{wf}, workflowResolver{mr: fullMaturityRange()})

	anchor := `<a href="/workflows/1"`
	ai := strings.Index(out, anchor)
	if ai < 0 {
		t.Fatalf("no title anchor:\n%s", out)
	}
	// The title's own text is inside the anchor; measure from where it CLOSES.
	rel := strings.Index(out[ai:], "</a>")
	if rel < 0 {
		t.Fatalf("unterminated title anchor:\n%s", out[ai:])
	}
	if !strings.Contains(out[ai:ai+rel], "Flux basic") {
		t.Fatalf("the title anchor does not hold the workflow name:\n%s", out[ai:ai+rel])
	}
	after := out[ai+rel+len("</a>"):]

	// The first badge after the title. The base-model badge is the distinctive one.
	bi := strings.Index(after, "Flux.1 D")
	if bi < 0 {
		t.Fatalf("no base-model badge after the title:\n%s", after)
	}
	between := after[:bi]
	// 🔴 THE PRECISE CLAIM: no ELEMENT BOUNDARY of any kind separates the title from
	// its badges — they are siblings in one row. The badges are <span>s, so an
	// earlier version of this check ("no </div> between them") was VACUOUS: with the
	// badges back on their own <div> row the slice held `</a><div …><span …>` and
	// closed no div at all, so the pre-change structure passed. Both the opening and
	// the closing tag have to be excluded.
	for _, boundary := range []string{"<div", "</div>"} {
		if strings.Contains(between, boundary) {
			t.Errorf("the badges are on a row of their own — %q separates the title from "+
				"the first badge:\n%s", boundary, between)
		}
	}
}

// TestFromLinkIsAnIdBuiltExternalLink covers Task 2's other half.
//
// The href is built from the numeric source id — never interpolated from a stored
// URL string — and re-validated through the package's ONE external-link guard
// (isSafeHTTPURL), the same one the Apps cards use.
func TestFromLinkIsAnIdBuiltExternalLink(t *testing.T) {
	out := renderWFList(t, []store.Workflow{srcWF(1, "Flux basic", 1386234)},
		workflowResolver{mr: fullMaturityRange()})

	want := `<a href="https://civitai.com/models/1386234"`
	if !strings.Contains(out, want) {
		t.Fatalf("the \"from\" link must point at the source post on civitai.com (%s):\n%s", want, out)
	}
	i := strings.Index(out, want)
	tag := out[i : i+strings.Index(out[i:], ">")+1]
	for _, attr := range []string{`target="_blank"`, `rel="noopener noreferrer"`} {
		if !strings.Contains(tag, attr) {
			t.Errorf("external link is missing %s: %s", attr, tag)
		}
	}
	// It is a LINK now, not the plain text it used to be.
	if !strings.Contains(out, "from </span>") && !strings.Contains(out, ">from <") {
		t.Errorf("the \"from\" label disappeared:\n%s", out)
	}

	// The href must be derived from the id alone, so a stored URL cannot reach it.
	// A different id must produce a different, matching URL.
	other := renderWFList(t, []store.Workflow{srcWF(1, "x", 42)},
		workflowResolver{mr: fullMaturityRange()})
	if !strings.Contains(other, `href="https://civitai.com/models/42"`) {
		t.Errorf("the href does not track the source id:\n%s", other)
	}
}

// TestSourceCopyDoesNotCallItAModel. CivitAI serves workflow posts from URLs that
// look like MODEL pages, so the id being grouped on names a Workflows-TYPE post,
// not a checkpoint. The picker's own copy must not imply otherwise.
func TestSourceCopyDoesNotCallItAModel(t *testing.T) {
	groups := groupWorkflowsBySource([]store.Workflow{
		srcWF(1, "a", 100), srcWF(2, "b", 100),
	}, nil)
	out := renderString(t, workflowSourcePicker(groups[0], url.Values{}, nil))

	// Strip the hidden inputs, whose NAMES legitimately include the `model` facet
	// parameter — that is a query key, not user-facing copy.
	visible := out
	if i := strings.Index(visible, "<label"); i >= 0 {
		visible = visible[i:]
	}
	if strings.Contains(strings.ToLower(visible), "model") {
		t.Errorf("the picker's user-facing copy calls the source a model:\n%s", visible)
	}
	if !strings.Contains(visible, "source") {
		t.Errorf("the picker should name what it groups by — the source:\n%s", visible)
	}
}

// TestGroupItemDataAttributesDescribeTheShownMember: the sort/filter data-* must
// describe what the user is looking at, or "Name A→Z" would order the list by
// workflows that are not on screen.
func TestGroupItemDataAttributesDescribeTheShownMember(t *testing.T) {
	a := srcWF(1, "Alpha", 100)
	b := srcWF(2, "Beta", 100)
	b.Format = store.WorkflowFormatUI
	b.BaseModel = "SDXL"

	out := renderWFList(t, []store.Workflow{a, b}, workflowResolver{
		mr:    fullMaturityRange(),
		picks: map[int64]bool{2: true},
	})
	for _, want := range []string{
		`data-name="Beta"`,
		`data-format="` + store.WorkflowFormatUI + `"`,
		`data-base="SDXL"`,
		`data-created="` + strconv.FormatInt(b.CreatedAt.Unix(), 10) + `"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the item does not describe the SHOWN member (%s):\n%s", want, out)
		}
	}
}
