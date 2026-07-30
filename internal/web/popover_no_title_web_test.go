package web

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// ---------------------------------------------------------------------------
// DOUBLE HOVER GUARD — a custom popover trigger must not ALSO carry title=.
// ---------------------------------------------------------------------------
// A `title=` attribute makes the browser draw its NATIVE tooltip after the OS
// hover delay. On an element that also owns a `.cm-updated-pop` /
// `.cm-vstatus-pop`, both fire on the same hover and the user sees two
// overlapping tooltips saying the same thing.
//
// The rule is about the COLLISION, not about title= being bad: a `title=` on an
// element with no custom popover (a truncated path cell, a rail tile, an
// icon-only button) is the correct affordance and is deliberately untouched —
// TestRailTileKeepsItsTitle below pins one such case so this guard can never be
// "fixed" by stripping title= everywhere.
//
// Removing an accessible NAME is never acceptable, so each case here also
// asserts the trigger still has one (visible text, or aria-label for an icon
// trigger rendered as role=img/role=button).

// openTagOf returns the OPENING tag of the element carrying wantClass — i.e. the
// attribute list of the trigger itself, with none of its children's attributes.
// Matching the whole rendered fragment would be useless: a title= on a nested
// chip would make the test pass or fail for the wrong element.
func openTagOf(t *testing.T, html, wantClass string) string {
	t.Helper()
	re := regexp.MustCompile(`<[a-zA-Z]+[^>]*class="` + regexp.QuoteMeta(wantClass) + `"[^>]*>`)
	m := re.FindString(html)
	if m == "" {
		t.Fatalf("no element with class=%q in:\n%s", wantClass, html)
	}
	return m
}

// assertPopoverTriggerNoTitle pins the whole contract for one trigger: it opens a
// custom popover, it carries no title=, and it still has an accessible name.
func assertPopoverTriggerNoTitle(t *testing.T, html, triggerClass, popClass, wantName string) {
	t.Helper()
	if !strings.Contains(html, popClass) {
		t.Fatalf("trigger %q must still render its custom popover %q:\n%s", triggerClass, popClass, html)
	}
	tag := openTagOf(t, html, triggerClass)
	if strings.Contains(tag, "title=") {
		t.Errorf("trigger %q owns a custom popover, so it must NOT also carry title= "+
			"(double hover: the native tooltip paints over the popover). Opening tag:\n%s", triggerClass, tag)
	}
	if wantName == "" {
		return
	}
	// The accessible name must survive the title removal — either as aria-label
	// on the trigger, or as the trigger's own visible text.
	if !strings.Contains(tag, `aria-label="`+wantName+`"`) && !strings.Contains(html, wantName) {
		t.Errorf("trigger %q lost its accessible name %q after dropping title=:\n%s", triggerClass, wantName, html)
	}
}

func TestUpdatedHeaderStatHasNoTitle(t *testing.T) {
	out := renderString(t, updatedHeaderStat(7, 11, "2 years ago", "2023-01-26 10:00", "v1", "2023-01-26"))
	assertPopoverTriggerNoTitle(t, out, "cm-updated", "cm-updated-pop", "")
	if !strings.Contains(out, "Updated: ") || !strings.Contains(out, "2 years ago") {
		t.Errorf("header stat must keep its visible text as the accessible name:\n%s", out)
	}
}

func TestUpdatedCardLineHasNoTitle(t *testing.T) {
	out := renderString(t, updatedCardLine(7, 11, "2 years ago", "2023-01-26 10:00", "v1", "2023-01-26"))
	assertPopoverTriggerNoTitle(t, out, "cm-updated text-xs text-slate-500", "cm-updated-pop", "")
	if !strings.Contains(out, "Updated 2 years ago") {
		t.Errorf("card line must keep its visible text as the accessible name:\n%s", out)
	}
}

func TestVersionDatePopoverHasNoTitle(t *testing.T) {
	pub := time.Date(2023, 1, 26, 10, 0, 0, 0, time.UTC)
	out := renderString(t, versionDatePopover(pub))
	// The clock glyph carries no words, so aria-label IS the accessible name here.
	assertPopoverTriggerNoTitle(t, out, "cm-updated cm-vdate", "cm-updated-pop", "Published "+humanSince(pub))
}

func TestComfyStatusIconHasNoTitle(t *testing.T) {
	out := renderString(t, comfyStatusIcon(comfyStatusView{}))
	assertPopoverTriggerNoTitle(t, out, "cm-updated", "cm-updated-pop", "")
	if !strings.Contains(out, "aria-label=") {
		t.Errorf("comfy status icon is icon-only (role=img) — it MUST keep its aria-label:\n%s", out)
	}
}

func TestWorkflowResourcesPopoverHasNoTitle(t *testing.T) {
	out := renderString(t, workflowResourcesPopover([]string{"a.safetensors"}, workflowResolver{}))
	assertPopoverTriggerNoTitle(t, out, "cm-updated cm-res-trigger", "cm-updated-pop cm-res-pop",
		"1 resource referenced by this workflow")
}

// TestVersionTabPopoverTriggerHasNoTitle exercises the reported surface end to
// end: the version tab as rendered by versionTab, not the popover helper alone.
func TestVersionTabPopoverTriggerHasNoTitle(t *testing.T) {
	pub := time.Date(2023, 1, 26, 10, 0, 0, 0, time.UTC)
	v := modelDetailView{
		Model: &civitai.ModelDetail{ID: 7, Name: "M", ModelVersions: []civitai.ModelVersionSummary{
			{ID: 11, Name: "v1", BaseModel: "SD 1.5"},
		}},
		SelectedVersionID:  11,
		LocalVersionIDs:    map[int]bool{11: true},
		VersionPublishedAt: map[int]time.Time{11: pub},
	}
	out := renderString(t, versionTab(v, v.Model.ModelVersions[0]))
	assertPopoverTriggerNoTitle(t, out, "cm-updated cm-vdate", "cm-updated-pop", "Published "+humanSince(pub))
	// The in-library ✓ is a SIBLING of the popover trigger, not the same element,
	// so its title= is a legitimate lone affordance and must survive.
	if !strings.Contains(out, `title="In your library"`) {
		t.Errorf("the in-library ✓ has no custom popover — its title= must be kept:\n%s", out)
	}
}

// TestRailTileKeepsItsTitle is the counterweight: railTile deliberately uses
// title= and owns NO custom popover, so it must NOT be swept up by the rule
// above. Without this, "delete every title=" would pass the guard.
func TestRailTileKeepsItsTitle(t *testing.T) {
	out := renderString(t, railTile(railGroup{
		Count: 1,
		Rep:   store.Generation{ID: 1, CreatedAt: time.Now().Add(-time.Hour)},
	}))
	if !strings.Contains(out, "title=") {
		t.Errorf("railTile has no custom popover — its title= is the affordance and must stay:\n%s", out)
	}
	if strings.Contains(out, "cm-updated-pop") || strings.Contains(out, "cm-vstatus-pop") {
		t.Errorf("railTile is not supposed to own a custom popover:\n%s", out)
	}
}
