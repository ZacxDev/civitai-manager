package uxaudit

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

// This file is the BROWSERLESS rot-guard for the walk's coupling to the app.
//
// The browser walk (TestUXAuditWalk) is double-gated out of `go test ./...` — it needs
// UXAUDIT_WALK plus a resolvable Chromium — so when a selector goes stale nothing
// reports it until someone runs `make ux-audit` by hand. That is exactly how the hero
// run-control selector sat broken for two releases (see RunPostPath's comment).
//
// These tests boot the real lab App and fetch the real served HTML over HTTP. No
// browser is involved, so they run in the ordinary `go test ./...` for this module and
// fail the moment the app stops rendering something the walk depends on.

// fetchPage GETs an app-relative path and returns the response body + status.
func fetchPage(t *testing.T, app *App, path string) (string, int) {
	t.Helper()
	resp, err := http.Get(app.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(body), resp.StatusCode
}

// bootLabApp boots the hermetic lab app for a test and closes it on cleanup.
func bootLabApp(t *testing.T) *App {
	t.Helper()
	app, err := Boot(t.TempDir())
	if err != nil {
		t.Fatalf("boot lab app: %v", err)
	}
	t.Cleanup(app.Close)
	return app
}

// TestWalkSelectorsMatchTheServedApp is the guard that would have caught the stale
// hero selector. It asserts the ATTRIBUTES the walk's selectors key on are actually
// present in the HTML the app serves — the run control's hx-post and the import
// trigger's title=.
//
// Non-vacuity: each case first asserts a marker proving the fixture reached the real
// page (not an error page or an empty body), so a 500 cannot masquerade as a matched
// selector, and the selector assertion is reached only on a genuinely rendered page.
func TestWalkSelectorsMatchTheServedApp(t *testing.T) {
	app := bootLabApp(t)

	t.Run("hero run control", func(t *testing.T) {
		// The run control is NOT in the page's initial HTML — it is delivered by the
		// comfy-status htmx fragment once the ComfyUI probe succeeds, so that is what
		// the guard must fetch.
		path := RunControlFragmentPath(app.WorkflowID)
		body, status := fetchPage(t, app, path)
		if status != http.StatusOK {
			t.Fatalf("GET %s: status %d, want 200", path, status)
		}
		// Fixture reached the interesting case: the probe against the lab's fake
		// ComfyUI succeeded, so this is the ENABLED branch that carries a run button.
		// Without this the unreachable branch (no button at all) would render a 200 and
		// the selector assertion below would report a stale selector instead of a
		// broken fixture.
		if !strings.Contains(body, "ComfyUI reachable") {
			t.Fatalf("GET %s did not render the reachable branch — the lab's fake ComfyUI "+
				"is not being probed successfully, so this test cannot see the run button at all.\nbody:\n%s",
				path, truncate(body, 600))
		}
		// The exact attribute the walk's RunButtonSelector matches on.
		want := fmt.Sprintf(`hx-post="/workflows/%d/%s"`, app.WorkflowID, RunPostPath)
		if !strings.Contains(body, want) {
			t.Errorf("the run-control fragment does not carry the walk's run control %s\n"+
				"the hero prep would hang on WaitVisible until the capture context expires "+
				"(a bare \"context deadline exceeded\"); update RunPostPath to the app's current run control",
				want)
		}
	})

	t.Run("import trigger", func(t *testing.T) {
		const path = "/library?tab=workflows"
		body, status := fetchPage(t, app, path)
		if status != http.StatusOK {
			t.Fatalf("GET %s: status %d, want 200", path, status)
		}
		// Fixture reached the interesting case: the Workflows tab actually rendered.
		if !strings.Contains(body, "SDXL Portrait") {
			t.Fatalf("GET %s did not render the workflows tab (no seeded workflow); got %d bytes", path, len(body))
		}
		want := fmt.Sprintf(`title="%s`, ImportTriggerTitlePrefix)
		if !strings.Contains(body, want) {
			t.Errorf("workflows tab does not carry the walk's import trigger prefix %s\n"+
				"the workflow-import view's prep would hang on WaitVisible; "+
				"update ImportTriggerTitlePrefix to the app's current trigger", want)
		}
	})
}

// TestWalkViewPathsAreServable asserts every view the walk navigates to answers 200.
// A view path that 404s still produces a screenshot and an axe scan — of an ERROR
// page — which is silently pushed as if it were the real surface.
//
// ⚠ Honest limit, measured: this catches a path the app no longer ROUTES (mutating a
// view to /no-such-route-at-all fails it with a 404). It does NOT catch a routed path
// whose subject does not exist — the lab's fakeReader answers ANY creator username or
// model id with seeded data, so /creators/does-not-exist returns a fully-rendered 200.
// Do not read a pass here as "every view shows the intended content".
func TestWalkViewPathsAreServable(t *testing.T) {
	app := bootLabApp(t)

	seen := map[string]bool{}
	for _, v := range Views(app) {
		if seen[v.Path] {
			continue // several views share a path and differ only by prep
		}
		seen[v.Path] = true
		t.Run(v.Name, func(t *testing.T) {
			body, status := fetchPage(t, app, v.Path)
			if status != http.StatusOK {
				t.Errorf("view %q: GET %s returned %d, want 200 — the walk would audit an error page",
					v.Name, v.Path, status)
			}
			if len(body) == 0 {
				t.Errorf("view %q: GET %s returned an empty body", v.Name, v.Path)
			}
		})
	}
}

// TestHeroRunStatusContainerExists guards the OTHER half of the hero chain: the walk
// re-pins to the run it triggered by reading data-run-seq inside #run-status. If that
// container id changes, readRunSeq silently reads 0 and waitForNewRunPanel can never
// be satisfied — the same opaque timeout, from a different cause.
func TestHeroRunStatusContainerExists(t *testing.T) {
	app := bootLabApp(t)
	path := fmt.Sprintf("/workflows/%d", app.WorkflowID)
	body, status := fetchPage(t, app, path)
	if status != http.StatusOK {
		t.Fatalf("GET %s: status %d, want 200", path, status)
	}
	if !strings.Contains(body, "SDXL Portrait") {
		t.Fatalf("GET %s did not render the seeded hero workflow", path)
	}
	if !strings.Contains(body, `id="run-status"`) {
		t.Error(`workflow detail page has no id="run-status" container — ` +
			`readRunSeq/waitForNewRunPanel would never see a run and the hero capture would time out`)
	}
}
