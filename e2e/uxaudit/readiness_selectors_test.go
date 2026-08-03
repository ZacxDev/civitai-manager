package uxaudit

import (
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

// TestLabRendersTheReadinessNeedsState is the browserless rot-guard for the walk's
// coverage of the pre-click readiness line.
//
// 🔴 IT EXISTS BECAUSE THE OBVIOUS SEEDING IS SILENTLY WRONG. The line reads the 0019
// comfy_model_cache row and NOTHING else; the app populates that row from a RUN, and
// the walk captures the workflow page without running first. So with no explicit seed
// every capture renders the COLD branch — a dimmed "Not checked …" line — and the
// state that actually matters, the amber "Needs N …" one carrying the only colour the
// walk had never scanned, is never on screen. `0 violations` would then be a fact
// about a surface the audit never loaded, which is this lab's documented failure mode
// rather than a hypothetical one.
//
// Boot seeds the row (see boot.go); this pins that it keeps working. It runs in the
// ordinary nested-module `go test` — no browser, no UXAUDIT_WALK — because the browser
// walk is double-gated out of CI and cannot report its own rot.
func TestLabRendersTheReadinessNeedsState(t *testing.T) {
	app, err := Boot(t.TempDir())
	if err != nil {
		t.Fatalf("boot: %v", err)
	}
	t.Cleanup(app.Close)

	// BOTH heroes, because the two take different branches (the UI one converts, the
	// API one does not) and the walk captures a workflow page for each.
	for name, id := range map[string]int64{
		"api": app.WorkflowID,
		"ui":  app.WorkflowUIID,
	} {
		body := fetch(t, app.URL+"/workflows/"+strconv.FormatInt(id, 10)+"/run/readiness")
		// Assert the STATE attribute, never the prose: a reason sentence can contain
		// words a headline also contains, and this package has already shipped a
		// non-vacuity marker that was true on both branches.
		if !strings.Contains(body, `data-readiness="needs"`) {
			t.Errorf("%s hero readiness is not in the NEEDS state — the walk is scanning the "+
				"wrong branch and its axe result says nothing about the amber line:\n%s", name, body)
		}
		if strings.Contains(body, `data-reason="cold"`) {
			t.Errorf("%s hero readiness fell back to the COLD-CACHE branch — Boot's "+
				"PutComfyObjectInfo seed is gone or no longer matches the fake ComfyUI payload:\n%s",
				name, body)
		}
		// Fixture reached the interesting case: both heroes reference exactly the two
		// -MISSING model files, so a count that is not 2 means the fixture drifted and
		// the line is describing something else.
		if !strings.Contains(body, "2 model files") {
			t.Errorf("%s hero readiness does not report the seeded 2 missing model files:\n%s",
				name, body)
		}
	}
}

// fetch GETs a lab URL and returns the body, failing the test on any error.
func fetch(t *testing.T, url string) string {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200", url, resp.StatusCode)
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s: %v", url, err)
	}
	return string(b)
}
