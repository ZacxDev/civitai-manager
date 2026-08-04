package uxaudit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// TestCaptureIsRepeatable is a MEASUREMENT instrument, not a guard, and it is committed
// because the thing it measures will recur.
//
// A full walk is ~50s, so an intermittent per-capture difference costs an afternoon to
// characterise by re-running walks — and "I ran it three times and saw no difference"
// is worth very little against a 1-in-6 flake. This captures ONE view N times against a
// single booted app and browser and reports the DISTINCT screenshot hashes, turning
// that into a two-minute measurement.
//
// It is opt-in (UXAUDIT_FLAKE_VIEW) and browser-gated, so it never runs in
// `go test ./...` or CI. Run it:
//
//	AUDITLOOP_CHROMIUM=/run/current-system/sw/bin/brave UXAUDIT_WALK=1 \
//	  UXAUDIT_FLAKE_VIEW=run-missing-models-expanded UXAUDIT_FLAKE_N=30 \
//	  go test -run TestCaptureIsRepeatable -count=1 -timeout 25m -v .
//
// Worked example — the CSS-transition flake this instrument found and then confirmed
// fixed (see freezeAnimationScript in capture.go):
//
//	before any fix       24 captures → 3 distinct (20 / 3 / 1)
//	waiting for quiescence  30 captures → 2 distinct (29 / 1)   ← better, NOT fixed
//	freezing animation    30 captures → 1 distinct (30)
//
// The middle row is the reason the instrument is worth keeping: a wait-based fix looks
// convincing over three runs and is still broken.
func TestCaptureIsRepeatable(t *testing.T) {
	view := os.Getenv("UXAUDIT_FLAKE_VIEW")
	if view == "" {
		t.Skip("set UXAUDIT_FLAKE_VIEW=<view name> to measure one view's capture repeatability")
	}
	execPath := ResolveChromium()
	if execPath == "" {
		t.Skip("no chromium found: set AUDITLOOP_CHROMIUM")
	}
	n := 10
	if v, err := strconv.Atoi(os.Getenv("UXAUDIT_FLAKE_N")); err == nil && v > 0 {
		n = v
	}

	dir, release, err := acquireWorkDir()
	if err != nil {
		t.Fatalf("acquire work dir: %v", err)
	}
	defer release()
	app, err := Boot(dir)
	if err != nil {
		t.Fatalf("boot app: %v", err)
	}
	defer app.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	br, err := NewBrowser(ctx, execPath)
	if err != nil {
		t.Fatalf("launch chromium: %v", err)
	}
	defer br.Close()

	var target *View
	for _, v := range Views(app) {
		v := v
		if v.Name == view {
			target = &v
			break
		}
	}
	if target == nil {
		t.Fatalf("no view named %q in Views()", view)
	}
	base := app.URL
	if target.BaseURL != "" {
		base = target.BaseURL
	}
	// Desktop only: one viewport is enough to characterise a flake, and halving the
	// captures halves the wall time.
	vp := Viewports[1]

	counts := map[string]int{}
	var order []string
	outDir := t.TempDir()
	for i := 0; i < n; i++ {
		prep := prepFor(target, app)
		vc, err := br.CaptureWith(base+target.Path, vp, prep)
		if err != nil {
			t.Fatalf("capture %d/%d: %v", i+1, n, err)
		}
		sum := sha256.Sum256(vc.ScreenshotPNG)
		h := hex.EncodeToString(sum[:])[:12]
		if counts[h] == 0 {
			order = append(order, h)
			// One copy of each DISTINCT screenshot, so a difference can be diffed after.
			path := fmt.Sprintf("%s/%s.%s.png", outDir, view, h)
			if werr := os.WriteFile(path, vc.ScreenshotPNG, 0o644); werr == nil {
				t.Logf("  new distinct screenshot %s → %s", h, path)
			}
		}
		counts[h]++
	}

	t.Logf("=== %s: %d captures, %d DISTINCT screenshot(s)", view, n, len(order))
	for _, h := range order {
		t.Logf("      %s ×%d", h, counts[h])
	}
	if len(order) != 1 {
		t.Errorf("%s produced %d distinct screenshots over %d captures — this view is not a "+
			"stable visual baseline, and a diff against it will report changes nobody made",
			view, len(order), n)
	}
}

// prepFor builds a view's prep actions, tolerating a view that declares none.
func prepFor(v *View, app *App) []chromedp.Action {
	if v.Prep == nil {
		return nil
	}
	return v.Prep(app)
}
