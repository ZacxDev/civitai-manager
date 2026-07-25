package web

import (
	"strings"
	"testing"

	g "maragu.dev/gomponents"
)

// TestScanControlsInlineBeforeFirstScan proves the Model-files tab keeps the scan
// form INLINE (no dialog, no rescan trigger) before any scan has produced results,
// so the first scan needs no hunting.
func TestScanControlsInlineBeforeFirstScan(t *testing.T) {
	body := g.Text("results-placeholder")
	out := renderString(t, filesTabBody(body, "csrf-x", true, false))
	if !strings.Contains(out, "Scan for model files") {
		t.Errorf("first-scan tab must render the scan form inline:\n%s", out)
	}
	if strings.Contains(out, "<dialog") {
		t.Errorf("first-scan tab must NOT wrap the form in a dialog:\n%s", out)
	}
	if strings.Contains(out, "Scan / Rescan") {
		t.Errorf("first-scan tab must NOT show the Scan / Rescan trigger:\n%s", out)
	}
}

// TestScanControlsMoveToDialogWithResults proves that once results exist the scan
// form is relocated behind a native <dialog> opened by a "Scan / Rescan" trigger,
// carrying its CSRF token and the unchanged POST /library/scan wiring.
func TestScanControlsMoveToDialogWithResults(t *testing.T) {
	body := g.Text("results-placeholder")
	out := renderString(t, filesTabBody(body, "csrf-tok", true, true))
	for _, want := range []string{
		"Scan / Rescan",             // the trigger
		`<dialog`,                   // the native dialog element
		`id="scan-form-dialog"`,     // its stable id
		`.showModal()`,              // inline (non-external) opener
		"Scan for model files",      // the relocated form's submit
		`value="csrf-tok"`,          // CSRF token still present inside the dialog
		`hx-post="/library/scan"`,   // unchanged scan endpoint
		`hx-target="#scan-results"`, // unchanged streaming target
	} {
		if !strings.Contains(out, want) {
			t.Errorf("results tab dialog missing %q:\n%s", want, out)
		}
	}
	// The form still targets the STABLE #scan-results container via innerHTML, never
	// self-replacing it (the race-safe streaming invariant).
	if strings.Contains(out, `hx-target="#scan-results"`) && strings.Contains(out, `hx-swap="outerHTML"`) {
		if strings.Contains(out, `hx-target="#scan-results" hx-swap="outerHTML"`) {
			t.Errorf("scan form must swap #scan-results innerHTML, not outerHTML:\n%s", out)
		}
	}
}
