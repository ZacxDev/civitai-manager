package uxaudit

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chromedp/chromedp"
)

// View is one logical funnel view the walk captures at both viewports. Name is the
// STABLE page identity: it is both the artifact-filename slug AND the auditloop
// PushPage.URL that P2 diffing matches on across pushes — so it must stay constant
// run to run. Path is the app-relative URL to navigate to. Prep, when non-nil,
// returns interactive chromedp actions (built against the booted App) that run after
// navigation + settle and before the axe scan + screenshot, driving the page into
// the end-state we want to audit (open a dialog, trigger a run, …).
type View struct {
	Name string
	Path string
	Prep func(app *App) []chromedp.Action
	// Hero marks the missing-models resolution panel — the product's centrepiece.
	// The walk fails loudly if a Hero view's Prep can't reach its end-state.
	Hero bool
}

// Views is the ordered funnel the walk captures. workflow-detail is deliberately
// captured BEFORE run-missing-models: triggering the run mutates the server's global
// run-job state, so the clean detail view must be captured first.
func Views(app *App) []View {
	wfPath := fmt.Sprintf("/workflows/%d", app.WorkflowID)
	runSel := fmt.Sprintf(`button[hx-post="/workflows/%d/run"]`, app.WorkflowID)
	return []View{
		// The subscriptions page — add-a-subscription, the subscription list (seeded
		// non-empty), the download queue and the activity feed. It used to be "/";
		// "/" is now a 302 into /search (handleHome), and a redirect is not a view.
		{Name: "dashboard", Path: "/subscriptions"},
		// Workflows list (the library's Workflows tab).
		{Name: "workflows-list", Path: "/library?tab=workflows"},
		// Paste-JSON import — the native <dialog> opened from the Workflows tab.
		{Name: "workflow-import", Path: "/library?tab=workflows", Prep: func(*App) []chromedp.Action {
			trigger := `button[aria-label^="Add a workflow"]`
			return []chromedp.Action{
				chromedp.WaitVisible(trigger, chromedp.ByQuery),
				chromedp.Click(trigger, chromedp.ByQuery),
				chromedp.WaitVisible(`#workflow-import-dialog[open]`, chromedp.ByQuery),
				chromedp.Sleep(300 * time.Millisecond),
			}
		}},
		// The seeded imported workflow's detail page.
		{Name: "workflow-detail", Path: wfPath},
		// HERO: trigger a run; the fake ComfyUI + seeded un-installed models make
		// preflight report both models missing, rendering the resolution panel.
		{Name: "run-missing-models", Path: wfPath, Hero: true, Prep: func(*App) []chromedp.Action {
			// preSeq holds the run-status container's data-run-seq BEFORE we trigger a new
			// run — captured at action time (readRunSeq), then read by waitForNewRunPanel.
			var preSeq int64
			return []chromedp.Action{
				// The Run button appears once the comfy-status htmx fragment reports the
				// (fake) ComfyUI reachable.
				chromedp.WaitVisible(runSel, chromedp.ByQuery),
				// The run job is a server-global SINGLETON, so a fresh navigation can
				// bootstrap #run-status with a PREVIOUS run's terminal panel. Each run is
				// stamped server-side with a strictly-increasing data-run-seq; capture the
				// currently-displayed seq (0 when idle / no prior run) so we can re-pin to
				// the run THIS click starts — which will carry a strictly-greater seq.
				readRunSeq("#run-status", &preSeq),
				chromedp.Click(runSel, chromedp.ByQuery),
				// Condition-based (not window-based) re-pin: wait until #run-status shows a
				// run whose data-run-seq > preSeq (proves it is the run this click started,
				// NEVER a stale prior panel) AND the terminal missing-models panel has
				// rendered. This tolerates the run settling to its preflight-failure terminal
				// BEFORE the transient in-flight "Stop" fragment is ever observed — which is
				// exactly where the old Stop-appears → Stop-gone chain flaked (a fast
				// preflight failure + the +1s self-poll could miss the ephemeral window and
				// time out step 1). The seq gate keeps the re-pin guarantee the old chain
				// gave: the asserted panel provably belongs to this run, not a leftover.
				waitForNewRunPanel("#run-status", HeroMarker, &preSeq, 60*time.Second),
				chromedp.Sleep(300 * time.Millisecond),
			}
		}},
		// Discover browse (workflows), served offline from the fake CivitAI reader.
		{Name: "discover-workflows", Path: "/workflows/discover"},
		// Library "Scan for model files" entry (the Sources tab lists the seeded dir).
		{Name: "library-sources", Path: "/library?tab=sources"},
	}
}

// HeroMarker is the text the missing-models resolution panel renders; the hero
// capture waits for it and the walk test asserts on it (non-vacuous — a regression
// that stops the panel rendering fails the walk).
const HeroMarker = "Missing model files"

// CapturedView pairs a View + Viewport with its capture.
type CapturedView struct {
	View     View
	Viewport Viewport
	Capture  ViewCapture
}

// WalkResult is the outcome of a full walk: the per-(view,viewport) captures, the
// built auditloop push metadata + the multipart file set it references, and the
// output directory the artifacts were written to.
type WalkResult struct {
	Captures []CapturedView
	Payload  PushPayload
	Metadata []byte            // json.Marshal(Payload) — the `metadata` multipart part
	Files    map[string][]byte // filename -> bytes (screenshots + axe + network json)
	OutDir   string
}

// Walk boots a hermetic civitai-manager, drives Chromium through every funnel View
// at both viewports, writes the artifacts under outDir, and builds the auditloop
// push payload. execPath is a resolved Chromium binary; outDir must exist or be
// creatable. It does NOT push — see MaybePush.
func Walk(ctx context.Context, execPath, outDir, label string) (*WalkResult, error) {
	if execPath == "" {
		return nil, fmt.Errorf("no chromium executable provided")
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return nil, fmt.Errorf("create out dir: %w", err)
	}

	workDir, err := os.MkdirTemp("", "uxaudit-walk-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(workDir)

	app, err := Boot(workDir)
	if err != nil {
		return nil, fmt.Errorf("boot app: %w", err)
	}
	defer app.Close()

	br, err := NewBrowser(ctx, execPath)
	if err != nil {
		return nil, fmt.Errorf("launch chromium: %w", err)
	}
	defer br.Close()

	var caps []CapturedView
	for _, v := range Views(app) {
		pageURL := app.URL + v.Path
		var prep []chromedp.Action
		if v.Prep != nil {
			prep = v.Prep(app)
		}
		for _, vp := range Viewports {
			vc, err := br.CaptureWith(pageURL, vp, prep)
			if err != nil {
				// A Hero failure is fatal (the centrepiece must render); other views are
				// best-effort but we still surface the error rather than silently gap.
				return nil, fmt.Errorf("capture %s (%s): %w", v.Name, vp.Name, err)
			}
			caps = append(caps, CapturedView{View: v, Viewport: vp, Capture: vc})
		}
	}

	payload, files, buildErr := BuildPayload(label, caps)

	// Persist the artifacts BEFORE acting on buildErr. BuildPayload returns a complete
	// file map even when it fails, so a fail-loud walk (e.g. one errored axe scan out
	// of 14 views) still leaves every screenshot/axe/network artifact — including the
	// errored page's axe.json — browsable in outDir for diagnosis. Writing them is not
	// a push and cannot corrupt anything downstream.
	if err := writeArtifacts(outDir, files); err != nil {
		if buildErr != nil {
			// Report the root cause, not the write that failed after it.
			return nil, fmt.Errorf("build payload: %w", buildErr)
		}
		return nil, err
	}
	if buildErr != nil {
		// An axe scan failure is FATAL: a page that was never scanned must not be
		// pushed as clean (it would corrupt the a11y regression baseline).
		return nil, fmt.Errorf("build payload: %w", buildErr)
	}

	metaJSON, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := payload.Validate(setOf(files)); err != nil {
		return nil, fmt.Errorf("built payload is invalid: %w", err)
	}
	if err := payload.ValidateFiles(metaJSON, files); err != nil {
		return nil, fmt.Errorf("built payload exceeds push size caps: %w", err)
	}

	if err := os.WriteFile(filepath.Join(outDir, "metadata.json"), metaJSON, 0o644); err != nil {
		return nil, fmt.Errorf("write metadata.json: %w", err)
	}

	return &WalkResult{
		Captures: caps,
		Payload:  payload,
		Metadata: metaJSON,
		Files:    files,
		OutDir:   outDir,
	}, nil
}

// BuildPayload maps the captured views to the auditloop plugin PushPayload + the
// multipart file set it references. Each (view,viewport) becomes one PushPage whose
// stable url is the view name suffixed with the viewport (so mobile/desktop are
// distinct P2-diff identities), carrying a screenshot + optional axe/network parts
// and the origin-classified rollup counts. Environment is always "lab" (hermetic
// localhost — auditloop keeps the numbers but suppresses the misleading perf
// findings). The file form-name IS the filename, per the push contract.
//
// Each page also carries its normalized FINDINGS (see findings.go / CaptureFindings):
// one a11y finding per violated axe rule with a structured detail whose top-level
// "id" drives auditloop's P2 `new_a11y_rules` regression delta, plus one finding per
// FIRST-PARTY console/network error carrying the actual message text. Without them
// the pushed run reports "no accessibility issues" and the CI a11y gate is a no-op.
// Third-party events stay counts-only, and no perf/layout findings are emitted
// (auditloop derives those server-side).
// It returns an error when a capture's axe scan FAILED (axe threw, so the page was
// never scanned): pushing that page as "clean" would empty auditloop's a11y rule set
// for the run and flip the regression delta on the next good run.
// On error the returned file map is still COMPLETE (every capture's artifacts),
// because the artifact pass runs BEFORE any finding is computed. That lets Walk
// persist the artifacts of the good views — including the ERRORED page's axe.json,
// the one file you need to diagnose the failure — and only then fail. Building
// artifacts first is also why the payload is returned zero-valued on error: it is
// deliberately unusable, so no caller can push a partially-mapped run.
func BuildPayload(label string, caps []CapturedView) (PushPayload, map[string][]byte, error) {
	files := map[string][]byte{}
	pages := make([]PushPage, 0, len(caps))

	// Pass 1 — artifacts + page refs. Independent of findings, so it always completes.
	for _, cv := range caps {
		base := cv.View.Name + "." + cv.Viewport.Name
		shot := base + ".png"
		files[shot] = cv.Capture.ScreenshotPNG

		pg := PushPage{
			URL:               cv.View.Name + "@" + cv.Viewport.Name,
			Viewport:          cv.Viewport.Name,
			Screenshot:        shot,
			AxeViolations:     cv.Capture.AxeViolations,
			ConsoleFirstParty: cv.Capture.ConsoleFirstParty,
			ConsoleThirdParty: cv.Capture.ConsoleThirdParty,
			NetworkFirstParty: cv.Capture.NetworkFirstParty,
			NetworkThirdParty: cv.Capture.NetworkThirdParty,
		}
		if len(cv.Capture.AxeJSON) > 0 {
			axe := base + ".axe.json"
			files[axe] = cv.Capture.AxeJSON
			pg.Axe = axe
		}
		if len(cv.Capture.NetworkJSON) > 0 {
			net := base + ".network.json"
			files[net] = cv.Capture.NetworkJSON
			pg.Network = net
		}
		// The DOM/a11y digest is attached ONLY when it carries at least one element.
		// auditloop rejects an all-empty digest, and that rejection 400s the ENTIRE
		// push — so the guard is not a nicety, it is what keeps one bare view (or one
		// page where the digest script threw and hit its empty catch-all) from
		// discarding the whole run. No ref and no file part is the correct, backward-
		// compatible output for such a page.
		if nonEmptyA11yDigest(cv.Capture.A11yDigestJSON) {
			dig := base + ".a11y.json"
			files[dig] = cv.Capture.A11yDigestJSON
			pg.A11yDigest = dig
		}
		pages = append(pages, pg)
	}

	// Pass 2 — findings. An axe scan failure aborts here, with `files` intact.
	for i, cv := range caps {
		findings, err := CaptureFindings(cv.Capture)
		if err != nil {
			return PushPayload{}, files, fmt.Errorf("%s (%s): %w", cv.View.Name, cv.Viewport.Name, err)
		}
		pages[i].Findings = findings
	}

	return PushPayload{Label: label, Environment: EnvLab, Pages: pages}, files, nil
}

// writeArtifacts persists every built artifact into outDir so `make ux-audit` leaves a
// browsable output dir. It is called even on a BuildPayload failure (see Walk).
func writeArtifacts(outDir string, files map[string][]byte) error {
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(outDir, name), data, 0o644); err != nil {
			return fmt.Errorf("write artifact %s: %w", name, err)
		}
	}
	return nil
}

// setOf returns the key set of a file map (the "provided" set Validate expects).
func setOf(files map[string][]byte) map[string]bool {
	out := make(map[string]bool, len(files))
	for k := range files {
		out[k] = true
	}
	return out
}

// readRunSeq reads the data-run-seq attribute of the run-status fragment inside
// containerSel into *out (0 when the container is idle / has no run-scoped child). It
// is a snapshot of the CURRENTLY-displayed run's server-assigned monotonic identity,
// taken before triggering a new run so a later wait can re-pin to a strictly-newer run.
func readRunSeq(containerSel string, out *int64) chromedp.Action {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		expr := fmt.Sprintf(
			"(()=>{const el=document.querySelector(%q);if(!el)return 0;const v=parseInt(el.getAttribute('data-run-seq'),10);return isNaN(v)?0:v;})()",
			containerSel+" [data-run-seq]")
		var seq int64
		if err := chromedp.Evaluate(expr, &seq).Do(ctx); err != nil {
			return err
		}
		*out = seq
		return nil
	})
}

// waitForNewRunPanel blocks until the run-status fragment inside containerSel both (a)
// carries a data-run-seq strictly greater than *preSeq — proving it is the run just
// triggered, not a stale panel left by a prior run of the same workflow — AND (b) its
// text contains marker (the terminal panel has rendered). *preSeq is read at poll time
// (it is filled by an earlier readRunSeq action). This is the condition-based re-pin
// that replaces the fragile "observe the transient Stop button" chain: it does not
// depend on catching any ephemeral in-flight window, so a run that settles immediately
// still satisfies it, while a stale prior panel (seq <= preSeq) never does.
func waitForNewRunPanel(containerSel, marker string, preSeq *int64, timeout time.Duration) chromedp.Action {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		deadline := time.Now().Add(timeout)
		for {
			prev := *preSeq
			expr := fmt.Sprintf(
				"(()=>{const c=document.querySelector(%q);if(!c)return false;"+
					"const el=c.querySelector('[data-run-seq]');if(!el)return false;"+
					"const v=parseInt(el.getAttribute('data-run-seq'),10);"+
					"if(isNaN(v)||v<=%d)return false;"+
					"return !!(c.innerText&&c.innerText.indexOf(%q)>=0);})()",
				containerSel, prev, marker)
			var ok bool
			if err := chromedp.Evaluate(expr, &ok).Do(ctx); err != nil {
				return err
			}
			if ok {
				return nil
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("timeout after %s waiting for a new run (data-run-seq > %d) showing %q in %s",
					timeout, prev, marker, containerSel)
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(300 * time.Millisecond):
			}
		}
	})
}

// PushConfigFromEnv reads the opt-in push configuration. A push is attempted ONLY
// when AUDITLOOP_PUSH_URL is set AND a token is resolvable, from either:
//   - AUDITLOOP_PUSH_TOKEN (a bare token), or
//   - AUDITLOOP_PUSH_TOKENS, a JSON object keyed by target name — this harness uses
//     the "civitai-manager-funnel" key (mirrors the naida/vetr push convention).
//
// It returns ok=false when unconfigured (the walk still runs + writes local
// artifacts; the push is simply skipped). A token is NEVER logged.
func PushConfigFromEnv() (baseURL, token string, ok bool) {
	baseURL = strings.TrimSpace(os.Getenv("AUDITLOOP_PUSH_URL"))
	if baseURL == "" {
		return "", "", false
	}
	if t := strings.TrimSpace(os.Getenv("AUDITLOOP_PUSH_TOKEN")); t != "" {
		return baseURL, t, true
	}
	if raw := strings.TrimSpace(os.Getenv("AUDITLOOP_PUSH_TOKENS")); raw != "" {
		var m map[string]string
		if err := json.Unmarshal([]byte(raw), &m); err == nil {
			if t := strings.TrimSpace(m[PushTargetName]); t != "" {
				return baseURL, t, true
			}
		}
	}
	return "", "", false
}

// PushTargetName is the auditloop plugin-target key this harness pushes under (the
// AUDITLOOP_PUSH_TOKENS map key).
const PushTargetName = "civitai-manager-funnel"

// MaybePush pushes the walk result to auditloop IFF the opt-in env is configured. It
// is NON-FATAL: an unconfigured env is a no-op (returns ""); a push failure is
// returned as an error for the caller to LOG, never to fail the walk on. On success
// it returns the absolute run URL.
func MaybePush(ctx context.Context, res *WalkResult) (runURL string, attempted bool, err error) {
	baseURL, token, ok := PushConfigFromEnv()
	if !ok {
		return "", false, nil
	}
	if err := res.Payload.ValidateFiles(res.Metadata, res.Files); err != nil {
		return "", true, fmt.Errorf("refusing to push: %w", err)
	}
	out, err := Upload(ctx, nil, baseURL, token, res.Metadata, res.Files)
	if err != nil {
		return "", true, err
	}
	return out.URL, true, nil
}
