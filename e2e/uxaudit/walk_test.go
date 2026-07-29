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
	var heroCap *CapturedView
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
			t.Errorf("%s/%s: no axe JSON", cv.View.Name, cv.Viewport.Name)
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
			heroCap = cv
		}
	}
	for name := range wantViews {
		if gotView[name] != len(Viewports) {
			t.Errorf("view %q: captured %d times, want %d (one per viewport)", name, gotView[name], len(Viewports))
		}
	}

	// The HERO: the missing-models resolution panel must actually have rendered —
	// assert on its CONTENT (the panel heading AND a specific missing filename the
	// fixture guarantees), not merely a 200 / non-empty shot.
	if heroCap == nil {
		t.Fatal("no hero (run-missing-models) capture found")
	}
	body := heroCap.Capture.BodyText
	if !strings.Contains(body, HeroMarker) {
		t.Errorf("hero body missing %q panel; got:\n%s", HeroMarker, truncate(body, 800))
	}
	if !strings.Contains(body, "MISSING") {
		t.Errorf("hero body does not name a missing model file; got:\n%s", truncate(body, 800))
	}

	// The generated metadata must be schema-valid against the file set.
	if err := res.Payload.Validate(setOf(res.Files)); err != nil {
		t.Errorf("generated metadata invalid: %v", err)
	}
	// metadata.json must have been written to the output dir.
	if _, err := os.Stat(outDir + "/metadata.json"); err != nil {
		t.Errorf("metadata.json not written: %v", err)
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
