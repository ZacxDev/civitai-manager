package uxaudit

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// TestUXAuditWalk is the runnable ux-audit walk AND the hermetic assertion suite. It
// is double-gated so `go test ./...` skips it cleanly:
//
//   - UXAUDIT_WALK must be set (so the slow browser walk never runs in a plain
//     `go test ./...`), and
//   - a Chromium binary must be resolvable (AUDITLOOP_CHROMIUM / PATH; never
//     downloaded) — absent → skip with a clear message, no silent gap.
//
// `make ux-audit` sets UXAUDIT_WALK=1 (and UXAUDIT_OUT for a persistent, gitignored
// output dir) to run it. When AUDITLOOP_PUSH_URL + a token are set it ALSO pushes to
// auditloop — non-fatally (a push failure is logged, the walk still passes).
func TestUXAuditWalk(t *testing.T) {
	if os.Getenv("UXAUDIT_WALK") == "" {
		t.Skip("set UXAUDIT_WALK=1 to run the browser ux-audit walk (make ux-audit)")
	}
	execPath := ResolveChromium()
	if execPath == "" {
		t.Skip("no chromium found: set AUDITLOOP_CHROMIUM or put chromium on PATH (never `playwright install`)")
	}
	t.Logf("using chromium: %s", execPath)

	// Persist artifacts to UXAUDIT_OUT (make ux-audit points this at a gitignored
	// dir); otherwise a temp dir that is cleaned up with the test.
	outDir := os.Getenv("UXAUDIT_OUT")
	if outDir == "" {
		outDir = t.TempDir()
	}
	t.Logf("writing artifacts to: %s", outDir)

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	res, err := Walk(ctx, execPath, outDir, "civitai-manager funnel (lab)")
	if err != nil {
		t.Fatalf("walk failed: %v", err)
	}

	// Every intended (view × viewport) must have produced a PNG + axe JSON + a
	// network capture, and a stable url. Assertions are non-vacuous: an empty
	// screenshot or missing axe fails.
	// View NAMES don't depend on the (now-closed) App's workflow id, so a throwaway
	// App suffices to enumerate the intended coverage set.
	wantViews := map[string]bool{}
	for _, v := range Views(&App{}) {
		wantViews[v.Name] = true
	}
	gotView := map[string]int{}
	// EVERY Hero view is asserted, not just one. This used to be a single
	// `heroCap *CapturedView` that each Hero desktop capture OVERWROTE, so with more
	// than one hero only the last survived and the others were audited but never
	// content-checked — a silent hole the moment a second hero was added.
	var heroCaps []*CapturedView
	for i := range res.Captures {
		cv := &res.Captures[i]
		gotView[cv.View.Name]++
		if len(cv.Capture.ScreenshotPNG) == 0 {
			t.Errorf("%s/%s: empty screenshot", cv.View.Name, cv.Viewport.Name)
		}
		if !hasPNGMagic(cv.Capture.ScreenshotPNG) {
			t.Errorf("%s/%s: screenshot is not a PNG", cv.View.Name, cv.Viewport.Name)
		}
		if len(cv.Capture.AxeJSON) == 0 {
			// FATAL, not Errorf: an empty axe capture is the ONE path where a page with
			// no a11y data reaches the payload as a 0-violation page (A11yFindings
			// deliberately treats empty input as "nothing to interpret" rather than a
			// scan failure). Pushing it would report every rule as resolved and flip the
			// next run's regression delta — exactly what fail-loud exists to prevent. An
			// Errorf here would record the failure and STILL fall through to the push
			// below, so this must abort.
			t.Fatalf("%s/%s: no axe JSON — refusing to continue (a 0-violation page would corrupt the a11y baseline)", cv.View.Name, cv.Viewport.Name)
		} else {
			var probe struct {
				Violations []json.RawMessage `json:"violations"`
			}
			if err := json.Unmarshal(cv.Capture.AxeJSON, &probe); err != nil {
				t.Errorf("%s/%s: axe JSON did not parse: %v", cv.View.Name, cv.Viewport.Name, err)
			}
		}
		if cv.Capture.NetworkJSON == nil {
			t.Errorf("%s/%s: nil network capture", cv.View.Name, cv.Viewport.Name)
		}
		if cv.View.Hero && cv.Viewport.Name == "desktop" {
			heroCaps = append(heroCaps, cv)
		}
	}
	for name := range wantViews {
		if gotView[name] != len(Viewports) {
			t.Errorf("view %q: captured %d times, want %d (one per viewport)", name, gotView[name], len(Viewports))
		}
	}

	// The HERO: the missing-models resolution panel must actually have rendered —
	// assert on its CONTENT (the panel heading AND a specific missing filename the
	// fixture guarantees), not merely a 200 / non-empty shot. The RE-PIN guarantee (the
	// captured panel belongs to the run this walk triggered, not a stale prior run left
	// in the server-global #run-status) is enforced upstream by the hero Prep's
	// waitForNewRunPanel: it only lets the capture proceed once #run-status shows a
	// data-run-seq strictly greater than the pre-click value AND the marker text, so a
	// stale panel can never satisfy it and the walk fails loudly if this run's panel
	// never appears.
	// The hero count is a LITERAL on purpose. It used to be derived from
	// `Views(&App{})` — the same source `heroCaps` is built from — so deleting a Hero
	// view shrank both sides equally and the assertion could not fail. Verified: with
	// the derived form, deleting the run-missing-models-ui view left this test GREEN.
	// (That deletion is caught, but by the browserless `minViews` ratchet in
	// walk_selectors_test.go — not here, which is what the old comment claimed.)
	//
	// Both formats are heroes: the API branch and the UI branch of realRun reach the
	// missing-models panel differently, and only a UI-format graph exercises the
	// early-return-before-Preflight path. Raise this when a hero is added.
	const wantHeroes = 2
	if len(heroCaps) != wantHeroes {
		t.Fatalf("captured %d hero desktop views, want %d — a hero view was not captured", len(heroCaps), wantHeroes)
	}
	if wantHeroes == 0 {
		t.Fatal("no hero views declared — the missing-models centrepiece is not being audited at all")
	}
	for _, hc := range heroCaps {
		body := hc.Capture.BodyText
		if !strings.Contains(body, HeroMarker) {
			t.Errorf("hero %q body missing %q panel; got:\n%s", hc.View.Name, HeroMarker, truncate(body, 800))
		}
		if !strings.Contains(body, "MISSING") {
			t.Errorf("hero %q body does not name a missing model file; got:\n%s", hc.View.Name, truncate(body, 800))
		}
	}

	// 🔴 THE THREE NEW VIEWS MUST DIFFER FROM THE HERO, and until this existed nothing
	// committed proved they do. The evidence for them was a one-off injected-<img>
	// control run by hand — decisive at the time, and guaranteed to rot.
	//
	// Capture.BodyText is document.body.innerText, and innerText reflects RENDERED text:
	// it excludes a closed <dialog> and the body of a closed <details>. That makes it a
	// STATE, not a spelling — exactly the discriminator needed. If a prep silently stops
	// opening what it opens (a stale selector, a click that lands on nothing), the copy
	// below disappears from the view that must have it, and this fails.
	//
	// Each row also names copy the HERO must NOT have, so "the app started rendering this
	// everywhere" cannot satisfy the guard.
	assertOpenedContent(t, res, []openedContentCase{{
		view: "run-fix-model",
		// Section headings that exist only inside fixModelDialog.
		want: []string{"Use matched model from CivitAI", "Replace with a model from my library"},
	}, {
		view: "run-fix-model-blocked",
		want: []string{"Use matched model from CivitAI", "Replace with a model from my library"},
	}, {
		view: "run-missing-models-expanded",
		// The BODY of a collapsed disclosure — never its <summary>, which renders either
		// way and would pass with the expansion deleted.
		want: []string{"Or install it by hand", "Preflight failed"},
	}})

	// The generated metadata must be schema-valid against the file set.
	if err := res.Payload.Validate(setOf(res.Files)); err != nil {
		t.Errorf("generated metadata invalid: %v", err)
	}
	// metadata.json must have been written to the output dir.
	if _, err := os.Stat(outDir + "/metadata.json"); err != nil {
		t.Errorf("metadata.json not written: %v", err)
	}

	// NEVER push a run whose assertions failed (belt-and-braces with the t.Fatalf
	// above). Any failed capture assertion means the payload may misrepresent the app —
	// most dangerously as CLEAN — and a pushed run becomes auditloop's regression
	// BASELINE for this target, so a bad push poisons every future comparison, not just
	// this report. A local failure is cheap; a corrupt baseline is not.
	if t.Failed() {
		t.Fatalf("assertions failed — refusing to push a possibly-misrepresentative run (%d artifacts left in %s for inspection)", len(res.Files), outDir)
	}

	// Opt-in push (non-fatal): only when configured; a failure is LOGGED, never fatal.
	if url, attempted, perr := MaybePush(ctx, res); attempted {
		if perr != nil {
			t.Logf("PUSH attempted but failed (non-fatal): %v", perr)
		} else {
			t.Logf("PUSH ok: %s", url)
		}
	} else {
		t.Logf("push skipped (AUDITLOOP_PUSH_URL / token not set) — %d artifacts in %s", len(res.Files), outDir)
	}
}

// openedContentCase is one view that must show content the HERO does not, because its
// prep opened something (a <dialog>, a <details>) that is closed everywhere else.
type openedContentCase struct {
	view string
	want []string
}

// heroBaselineView is the view every openedContentCase is contrasted against: the same
// terminal run panel, with nothing opened. It is what makes each `want` string a
// DISCRIMINATOR rather than an assertion that the app renders some copy somewhere.
const heroBaselineView = "run-missing-models"

// assertOpenedContent checks, per case, that the named view's rendered text contains
// copy the hero's does not.
//
// It uses Capture.BodyText (document.body.innerText), and the choice is load-bearing:
// innerText reflects what is RENDERED, so a closed <dialog> and the body of a closed
// <details> are both excluded. The same string read out of the HTML source would be
// present either way and could not tell the two states apart.
func assertOpenedContent(t *testing.T, res *WalkResult, cases []openedContentCase) {
	t.Helper()

	byView := map[string]string{}
	for i := range res.Captures {
		cv := &res.Captures[i]
		if cv.Viewport.Name == "desktop" {
			byView[cv.View.Name] = cv.Capture.BodyText
		}
	}

	hero, ok := byView[heroBaselineView]
	if !ok {
		t.Fatalf("no %q desktop capture — the discriminator below has nothing to contrast "+
			"against and would degrade into 'the app renders this somewhere'", heroBaselineView)
	}
	// Fixture reached the interesting case: the baseline really is the run panel.
	if !strings.Contains(hero, HeroMarker) {
		t.Fatalf("the %q baseline does not contain %q — it is not the panel these views open "+
			"things on top of", heroBaselineView, HeroMarker)
	}

	for _, tc := range cases {
		body, ok := byView[tc.view]
		if !ok {
			t.Errorf("no %q desktop capture — the view was not walked, so nothing proves its prep "+
				"opens anything", tc.view)
			continue
		}
		for _, want := range tc.want {
			if !strings.Contains(body, want) {
				t.Errorf("%s: rendered text is missing %q.\nThat copy lives inside something the "+
					"prep is supposed to OPEN (a <dialog> or a <details>); innerText excludes both "+
					"when closed. So the prep clicked/expanded nothing and this capture is the same "+
					"state as %q — axe scanned no new surface.", tc.view, want, heroBaselineView)
			}
			// The other half: it must be ABSENT from the hero, or it is not a discriminator
			// and would still pass with the prep's opening step deleted.
			if strings.Contains(hero, want) {
				t.Errorf("%s: %q is ALSO in the %q capture, so it does not discriminate — this row "+
					"would pass with the view's opening step removed. Pick copy that only renders "+
					"once the dialog/disclosure is open.", tc.view, want, heroBaselineView)
			}
		}
	}
}

func hasPNGMagic(b []byte) bool {
	const sig = "\x89PNG\r\n\x1a\n"
	return len(b) >= len(sig) && string(b[:len(sig)]) == sig
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
