package web

import (
	"strings"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/store"
)

// TestImportCardHeadingIsStableAcrossBothStates is the whole point of the change.
//
// The card used to be titled "Import workflows" with nothing imported and
// "Workflows" once something was, so the section appeared to RENAME ITSELF the
// moment you used it. The verb now lives on the button and the heading is one
// constant, so this asserts the SAME string in both states — and that the old
// verb-as-heading is gone from the empty state, which is the half a "contains the
// new heading" check alone would miss.
func TestImportCardHeadingIsStableAcrossBothStates(t *testing.T) {
	empty := renderString(t, workflowImportDetailCard(7, "csrf", 0, nil))
	filled := renderString(t, workflowImportDetailCard(7, "csrf", 2, []store.Workflow{
		{ID: 1, Name: "a", Format: store.WorkflowFormatAPI, Source: store.WorkflowSourceCivitai},
		{ID: 2, Name: "b", Format: store.WorkflowFormatAPI, Source: store.WorkflowSourceCivitai},
	}))

	for name, body := range map[string]string{"empty": empty, "imported": filled} {
		if !strings.Contains(body, ">"+workflowImportCardHeading+"<") {
			t.Errorf("%s state: heading %q is missing — the two states must share one "+
				"heading or the card renames itself on use:\n%s",
				name, workflowImportCardHeading, firstN(body, 600))
		}
	}
	// The empty state must no longer carry the verb as a HEADING. Scoped to an <h2>
	// so the button's own "Import workflows" text cannot satisfy it.
	if strings.Contains(empty, `<h2 class="text-sm font-semibold text-slate-300 mb-2">Import workflows</h2>`) {
		t.Errorf("the empty state still titles the card with the verb; the button carries "+
			"it now:\n%s", firstN(empty, 600))
	}
}

// TestImportCardHeadingIsUnambiguousAmongItsSiblings pins the reason the heading is
// not the shorter "Workflows".
//
// A model page carries THREE sections whose headings begin with "Workflows":
// "Workflows that use this model" (local library, matched by file) and "Workflows
// for <ecosystem>" (remote, by base model) both landed alongside this card. A bare
// "Workflows" would be the ambiguous one of three siblings. If someone shortens it,
// this fails and names the neighbours.
func TestImportCardHeadingIsUnambiguousAmongItsSiblings(t *testing.T) {
	if workflowImportCardHeading == "Workflows" {
		t.Fatal(`the heading was shortened to a bare "Workflows", which collides with the ` +
			`sibling sections "Workflows that use this model" and "Workflows for <ecosystem>" ` +
			`on the same page — keep it distinguishable`)
	}
	if !strings.HasPrefix(workflowImportCardHeading, "Workflows") {
		t.Errorf("heading %q no longer reads as a Workflows section", workflowImportCardHeading)
	}
}

// TestImportButtonCarriesTheAppsAddGlyph — the icon is ＋ (the same glyph as "Add a
// workflow"), not a download arrow.
//
// The cm-cta-icon vocabulary is → ＋ ↗ ▶ and nothing else; a ⤓ would be the only one
// of its kind and would read as "download a file" rather than "put this in my
// library". The glyph is aria-hidden, so the accessible name stays the visible text
// — asserted here because an icon-only variant of this control would need an
// aria-label and this one deliberately does not have one.
func TestImportButtonCarriesTheAppsAddGlyph(t *testing.T) {
	action := renderString(t, workflowImportAction(7, "csrf"))
	if !strings.Contains(action, `<span class="cm-cta-icon" aria-hidden="true">＋ </span>`) {
		t.Errorf("the import button is missing the aria-hidden ＋ add glyph:\n%s", action)
	}
	if strings.Contains(action, "⤓") {
		t.Error("⤓ is not in this app's cm-cta-icon vocabulary (→ ＋ ↗ ▶) — it would be " +
			"the only download-arrow in the UI and reads as 'fetch a file', not 'add to library'")
	}
	if !strings.Contains(action, "Import workflows") {
		t.Errorf("the button lost its visible text label, which IS its accessible name:\n%s", action)
	}
	// A visible text label means no aria-label is needed; one here would override the
	// text and is a smell that someone shrank the control to an icon.
	if strings.Contains(action, "aria-label") {
		t.Errorf("the import button should be named by its visible text, not an aria-label:\n%s", action)
	}
}

// TestImportActionSurvivesTheHeaderRow — the action container is the hx-swap TARGET
// and now sits inside the card's header row. Moving a swap target is exactly how a
// swap silently starts replacing the wrong thing, so pin the contract: the id, the
// POST, the CSRF and the self-targeting swap all still render.
func TestImportActionSurvivesTheHeaderRow(t *testing.T) {
	detail := renderString(t, workflowImportDetailCard(1818841, "csrf-tok", 0, nil))
	for _, want := range []string{
		`id="wf-import-1818841"`,
		`hx-post="/workflows/discover/1818841/import"`,
		"csrf-tok",
		`hx-target="#wf-import-1818841"`,
		`hx-swap="innerHTML"`,
	} {
		if !strings.Contains(detail, want) {
			t.Errorf("the import contract lost %q after moving into the header row:\n%s",
				want, firstN(detail, 900))
		}
	}
}
