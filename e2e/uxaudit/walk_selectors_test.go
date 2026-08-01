package uxaudit

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
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
//
// It uses its own client rather than http.DefaultClient for two reasons, both of
// which would otherwise weaken the guards:
//   - a timeout, so a hung handler fails this test in 10s instead of stalling to the
//     package timeout with no attribution;
//   - redirects are NOT followed. The walk's chromedp Navigate does follow them, but
//     for a guard "this path serves the view" is the property under test — silently
//     reporting 200 for a path that now 302s elsewhere is exactly the kind of pass
//     that hides a moved surface.
var labClient = &http.Client{
	Timeout: 10 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

func fetchPage(t *testing.T, app *App, path string) (string, int) {
	t.Helper()
	resp, err := labClient.Get(app.URL + path)
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

// hasButtonWith reports whether some `<button …>` OPEN TAG in html contains attr.
//
// The walk's selectors are `button[hx-post=…]` / `button[title^=…]` — they match a
// BUTTON, so the guard should too.
//
// ⚠ Be honest about what this buys TODAY: **nothing**. Measured on the two bodies
// actually guarded — the comfy-status fragment carries exactly one `<button`, zero
// `<form` and one occurrence of the hx-post; `/workflows/1` carries none at all — so
// swapping this for a bare strings.Contains leaves both real guards green (only the
// table test below reddens). An earlier version of this comment claimed the preset
// FORM (internal/web/run_preset_pages.go) emits the same hx-post INTO this fragment
// and that a body-wide search would therefore keep passing; that is false for these
// bodies, and citing CLAUDE.md's "assertion matched a DIFFERENT element's attribute"
// precedent for it was wrong. This is defence against such a carrier APPEARING —
// which is cheap and strictly safer — not a bug it currently prevents.
//
// ⚠ Two measured limits, neither reachable in the guarded bodies:
//   - it splits on the first `>` after a button tag, so an attribute VALUE containing
//     a literal `>` would mis-parse (none asserted here can);
//   - the tag name must be exactly `button`; the delimiter check below is what stops
//     `<button-group attr>` from counting (it did, before that check).
func hasButtonWith(html, attr string) bool {
	rest := html
	for {
		i := strings.Index(rest, "<button")
		if i < 0 {
			return false
		}
		rest = rest[i:]
		// Tag-name boundary: the byte after "<button" must end the name, otherwise
		// this is <button-group>/<buttonbar>/… and not a <button> at all.
		if len(rest) > len("<button") {
			switch rest[len("<button")] {
			case ' ', '\t', '\n', '\r', '>', '/':
			default:
				rest = rest[len("<button"):]
				continue
			}
		}
		j := strings.Index(rest, ">")
		if j < 0 {
			return false
		}
		if strings.Contains(rest[:j+1], attr) {
			return true
		}
		rest = rest[j+1:]
	}
}

// TestHasButtonWithDiscriminatesTheElement proves the helper does the one thing a
// bare strings.Contains does not: reject a match that is NOT on a <button>. Without
// this, "asserted on a button" is a claim in a comment rather than a property.
func TestHasButtonWithDiscriminatesTheElement(t *testing.T) {
	const attr = `hx-post="/workflows/1/run-with-params"`
	cases := []struct {
		name string
		html string
		want bool
	}{
		{"on a button", `<div><button class="x" ` + attr + `>Generate</button></div>`, true},
		// The live shape this exists for: the preset FORM carries the same hx-post.
		{"on a form only", `<form ` + attr + `><input name="seed"></form>`, false},
		{"on a div only", `<div ` + attr + `></div>`, false},
		{"absent entirely", `<button hx-post="/workflows/1/run">Old</button>`, false},
		// A non-matching button must not shadow a later matching one.
		{"second button matches", `<button id="a">A</button><button ` + attr + `>B</button>`, true},
		// A form carrying it must not make a non-matching button look like a hit.
		// ⚠ This case is WEAKER than its name suggests: the scan jumps straight to the
		// first `<button`, so the form's attribute is never in scope at all. Kept
		// because it still discriminates against a bare Contains, but the row below is
		// the one that exercises the hard shape.
		{"form matches, button does not", `<form ` + attr + `><button id="b">B</button></form>`, false},
		// The hard shape: a non-button carrier appearing AFTER a button open tag, so
		// the scan has to actually reject it rather than never reach it.
		{"non-button carrier after a button", `<button id="a">A</button><form ` + attr + `></form>`, false},
		// Tag-name boundary — `<button-group` starts with "<button" but is not a button.
		{"button-prefixed tag name", `<button-group ` + attr + `></button-group>`, false},
		{"button-prefixed tag then a real one", `<button-group x></button-group><button ` + attr + `>B</button>`, true},
		{"no buttons at all", `<p>nothing here</p>`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasButtonWith(tc.html, attr); got != tc.want {
				t.Errorf("hasButtonWith(%q) = %v, want %v", tc.html, got, tc.want)
			}
		})
	}
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
		//
		// 🔴 Assert data-state="ok", NOT the headline text. The reachable headline is
		// "ComfyUI reachable" and the UNREACHABLE one is "No ComfyUI reachable at <url>"
		// (internal/web/run_pages.go comfyStatusLines) — so a Contains check for
		// "ComfyUI reachable" is TRUE ON BOTH BRANCHES and this non-vacuity guard was
		// itself vacuous. Proven by pointing the lab's ComfyURL at a dead port: the
		// marker check passed and the test then reported "stale selector" when the truth
		// was "broken fixture" — the exact confusion this check exists to prevent.
		// data-state is emitted only by the reachable arm.
		if !strings.Contains(body, `data-state="ok"`) {
			t.Fatalf("GET %s did not render the REACHABLE branch (no data-state=\"ok\") — the lab's "+
				"fake ComfyUI is not being probed successfully, so this test cannot see the run "+
				"button at all. This is a broken FIXTURE, not a stale selector.\nbody:\n%s",
				path, truncate(body, 600))
		}
		// The exact attribute the walk's RunButtonSelector matches on — asserted on a
		// BUTTON, because that is what the selector matches.
		want := fmt.Sprintf(`hx-post="/workflows/%d/%s"`, app.WorkflowID, RunPostPath)
		if !hasButtonWith(body, want) {
			t.Errorf("no <button> in the run-control fragment carries the walk's run control %s\n"+
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
		if !hasButtonWith(body, want) {
			t.Errorf("no <button> on the workflows tab carries the walk's import trigger prefix %s\n"+
				"the workflow-import view's prep would hang on WaitVisible; "+
				"update ImportTriggerTitlePrefix to the app's current trigger", want)
		}
	})

	// The import view's prep does not stop at clicking the trigger — it then waits for
	// `#workflow-import-dialog[open]`. That id is a SECOND copy of a string owned by
	// internal/web (workflowImportDialogID), and it drifts silently: a rename reddens
	// internal/web's own tests while this module keeps compiling and dies later, in a
	// browser, as the same opaque WaitVisible timeout.
	//
	// ⚠ Honest limit: this asserts the ID EXISTS, not that it is a <dialog> nor that
	// the trigger opens it. Re-homing the id onto a <div popover> would keep this green
	// and still hang the walk, because `[open]` is a <dialog> attribute. Guarding that
	// properly needs the browser; this covers the rename, which is the likely drift.
	t.Run("import dialog container", func(t *testing.T) {
		const path = "/library?tab=workflows"
		body, status := fetchPage(t, app, path)
		if status != http.StatusOK {
			t.Fatalf("GET %s: status %d, want 200", path, status)
		}
		if !strings.Contains(body, `id="`+ImportDialogID+`"`) {
			t.Errorf("workflows tab has no id=%q dialog — the workflow-import view's prep "+
				"would click the trigger and then hang waiting for the dialog to open", ImportDialogID)
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

	// A floor on coverage. Without it, deleting entries from Views() shrinks the audit
	// silently and every remaining assertion still passes — the walk would report a
	// clean run over a fraction of the app. Raise this when views are added; it is a
	// ratchet, not an exact count.
	const minViews = 10
	if n := len(Views(app)); n < minViews {
		t.Errorf("Views() returned %d views, want at least %d — the walk's coverage shrank; "+
			"if a view was deliberately removed, lower this floor in the same commit", n, minViews)
	}

	seen := map[string]bool{}
	for _, v := range Views(app) {
		if seen[v.Path] {
			continue // several views share a path and differ only by prep
		}
		seen[v.Path] = true
		t.Run(v.Name, func(t *testing.T) {
			body, status := fetchPage(t, app, v.Path)
			if status != http.StatusOK {
				hint := "the walk would audit an error page"
				// fetchPage does not follow redirects on purpose, so a 3xx here means the
				// view MOVED — the browser walk would silently audit wherever it landed.
				// Say so, otherwise this reads as a broken guard rather than moved content.
				if status >= 300 && status < 400 {
					hint = "this view now REDIRECTS (the guard deliberately does not follow it) — " +
						"the walk would follow it and audit a different surface under this view's name; " +
						"point the view at its new path"
				}
				t.Errorf("view %q: GET %s returned %d, want 200 — %s", v.Name, v.Path, status, hint)
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
