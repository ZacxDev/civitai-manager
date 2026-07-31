package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// importButtonNote is the paragraph that used to render directly beneath the
// "Import workflows" button, once per card.
const importButtonNote = "Downloads the workflow zip from civitai.com using your token"

// TestImportButtonHasNoTrailingCopy — the note under the button is gone from every
// surface that renders the control, while the button itself (and its CSRF-bearing
// POST) is untouched.
func TestImportButtonHasNoTrailingCopy(t *testing.T) {
	// The control, rendered directly.
	action := renderString(t, workflowImportAction(1818841, "csrf"))
	if strings.Contains(action, importButtonNote) {
		t.Errorf("the copy under the import button should be gone:\n%s", action)
	}
	for _, want := range []string{
		"Import workflows",
		"/workflows/discover/1818841/import",
		"csrf_token",
		`id="wf-import-1818841"`,
	} {
		if !strings.Contains(action, want) {
			t.Errorf("the import control itself must still render %q:\n%s", want, action)
		}
	}

	// The model detail page's "Import workflows" card keeps its heading + button.
	// The third argument is the already-imported count (PR B); 0 = not yet imported,
	// which is the state that still renders the import CTA.
	detail := renderString(t, workflowImportDetailCard(1818841, "csrf", 0, nil))
	if strings.Contains(detail, importButtonNote) {
		t.Errorf("the copy under the import button should be gone from the detail card:\n%s", detail)
	}
	for _, want := range []string{workflowImportCardHeading, "Import workflows"} {
		if !strings.Contains(detail, want) {
			t.Errorf("import detail card missing %q:\n%s", want, detail)
		}
	}
}

// TestDiscoverCardsDropTheImportNoteButKeepThePageBlurb — the per-card note is
// gone, but the page's own description (which is what states the civitai.com
// egress) is a DIFFERENT element and stays.
func TestDiscoverCardsDropTheImportNoteButKeepThePageBlurb(t *testing.T) {
	reader := &recordingSearchReader{result: workflowResult(t)}
	srv := newModelServer(t, reader)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/workflows/discover?q=wan", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Import workflows") {
		t.Fatal("no import button on the page — the assertion below would false-pass")
	}
	if strings.Contains(body, importButtonNote) {
		t.Error("the per-card note under the import button should be gone")
	}
	if !strings.Contains(body, "Browse ComfyUI workflows on CivitAI") {
		t.Error("the page's own blurb is a different element and must stay")
	}
	if !strings.Contains(body, "Importing downloads the workflow zip with your token") {
		t.Error("the blurb still has to state the civitai.com egress")
	}
}
