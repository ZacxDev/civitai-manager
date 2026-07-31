package web

import (
	"strings"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/store"
	g "maragu.dev/gomponents"
)

// ---------------------------------------------------------------------------
// Item 1 — the rescan BUTTON + modal, and the first-run scan CARD.
//
// The Model-files tab used to spend a full titled card on the scan control
// forever. It now spends one only while the library holds NO MODELS (the guided
// first-run state); once there are models the control collapses to a "Rescan
// library" button that opens a native <dialog>.
// ---------------------------------------------------------------------------

// TestFirstRunRendersTheFullScanCard is the first-run half of the gate: with no
// models, the guided scan CARD renders inline and the rescan button/modal does not.
//
// MUTATION-VERIFIED: making filesTabBody render scanRescanControl unconditionally
// fails this with "first run must render the guided scan CARD inline, not a rescan
// trigger".
func TestFirstRunRendersTheFullScanCard(t *testing.T) {
	body := g.Text("results-placeholder")
	out := renderString(t, filesTabBody(body, "csrf-x", true, libraryHasModels(libraryView{})))

	if !strings.Contains(out, "Scan for model files") {
		t.Errorf("first run must render the scan form inline:\n%s", out)
	}
	// The guided state is a titled CARD, not a bare button.
	if !strings.Contains(out, `<h2 class="text-lg font-semibold text-slate-100 mb-3">Model files</h2>`) {
		t.Errorf("first run must render the titled scan card:\n%s", out)
	}
	if strings.Contains(out, "<dialog") || strings.Contains(out, "Rescan library") {
		t.Errorf("first run must render the guided scan CARD inline, not a rescan trigger:\n%s", out)
	}
}

// TestPopulatedLibraryRendersTheRescanButtonNotTheCard is the other half: once the
// library holds models the full card is GONE and only the compact rescan button
// (plus its dialog) remains.
func TestPopulatedLibraryRendersTheRescanButtonNotTheCard(t *testing.T) {
	v := libraryView{Files: []store.LocalFile{matchedFile(1, 1<<20)}}
	out := renderString(t, filesTabBody(g.Text("results"), "csrf-tok", true, libraryHasModels(v)))

	if !strings.Contains(out, "Rescan library") {
		t.Errorf("a populated library must render the rescan button:\n%s", out)
	}
	// The old always-on card is gone: no "Model files" scan <h2> wrapping the form.
	if strings.Contains(out, `<h2 class="text-lg font-semibold text-slate-100 mb-3">Model files</h2>`) {
		t.Errorf("the full scan card must NOT render once the library holds models:\n%s", out)
	}
	// The scan form must appear EXACTLY once, and only inside the dialog.
	if n := strings.Count(out, `hx-post="/library/scan"`); n != 1 {
		t.Errorf("expected exactly one scan form, got %d:\n%s", n, out)
	}
	iDialog, iForm := strings.Index(out, "<dialog"), strings.Index(out, `hx-post="/library/scan"`)
	if iDialog < 0 || iForm < iDialog {
		t.Errorf("the scan form must live INSIDE the dialog:\n%s", out)
	}
}

// TestLibraryHasModelsIsRowCountNotCandidates pins the "never scanned" signal
// itself: it is the persisted local_files MODEL row count. A library holding only
// flagged candidates and no model rows is still the first-run state ("0 models"),
// which its predecessor (hasResults, which OR-ed in v.Candidates) got wrong.
func TestLibraryHasModelsIsRowCountNotCandidates(t *testing.T) {
	if libraryHasModels(libraryView{}) {
		t.Error("an empty library must read as not-yet-scanned")
	}
	onlyCandidates := libraryView{Candidates: []store.LocalFile{candidate(store.CandidateBroken, 10)}}
	if libraryHasModels(onlyCandidates) {
		t.Error("candidates without model files must still read as 0 models (the first-run state)")
	}
	if !libraryHasModels(libraryView{Files: []store.LocalFile{matchedFile(1, 1)}}) {
		t.Error("one model file must read as a populated library")
	}
}

// TestRescanModalIsKeyboardOperableAndCarriesCSRF pins the modal contract.
//
// 🔴 showModal() IS THE LOAD-BEARING PART. Escape dismissal, focus moving INTO the
// dialog, focus being TRAPPED there (the rest of the document goes inert) and focus
// being RESTORED to the trigger on close are all behaviour the BROWSER provides —
// but ONLY for showModal(). The `open` attribute and `.show()` render the same box
// as a NON-modal: no top layer, no inertness, no Escape, no focus containment. A Go
// markup test cannot observe focus, so it asserts the one thing that DECIDES
// whether the browser provides it; the real focus/Escape behaviour is verified in a
// browser.
func TestRescanModalIsKeyboardOperableAndCarriesCSRF(t *testing.T) {
	out := renderString(t, scanRescanControl("csrf-tok", true))

	for _, want := range []string{
		"Rescan library",        // the trigger
		"<dialog",               // the native element
		`id="scan-form-dialog"`, // its stable id
		".showModal()",          // modality: Escape, focus trap + restore
		`aria-labelledby="scan-form-dialog-title"`,
		`id="scan-form-dialog-title"`, // the heading it is labelled by
		`<form method="dialog"`,       // the built-in dismiss control
		`aria-label="Close"`,          // …with an accessible name
		"Scan for model files",        // the relocated form's submit
		`name="csrf_token"`,           // 🔴 CSRF on the state-changing POST
		`value="csrf-tok"`,
		`hx-post="/library/scan"`,   // unchanged scan endpoint
		`hx-target="#scan-results"`, // unchanged streaming target
		`hx-swap="innerHTML"`,       // never outerHTML — that breaks the poll loop
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rescan modal missing %q:\n%s", want, out)
		}
	}
	// A NON-modal dialog would silently lose every keyboard guarantee above.
	if strings.Contains(out, ".show()") {
		t.Errorf("the dialog must be opened with showModal(), never show() — show() is "+
			"non-modal: no Escape, no focus trap, no inert background:\n%s", out)
	}
	if strings.Contains(out, "<dialog open") {
		t.Errorf("the dialog must not ship in the `open` state (also non-modal):\n%s", out)
	}
}
