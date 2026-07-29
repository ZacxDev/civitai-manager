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
		// Landing / dashboard — also hosts the Subscriptions section (this app renders
		// subscriptions on the dashboard, not a dedicated page; seeded non-empty).
		{Name: "dashboard", Path: "/"},
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
			return []chromedp.Action{
				// The Run button appears once the comfy-status htmx fragment reports the
				// (fake) ComfyUI reachable.
				chromedp.WaitVisible(runSel, chromedp.ByQuery),
				chromedp.Click(runSel, chromedp.ByQuery),
				// The run is async (POST → running fragment → poller → terminal). Because
				// the run-job is a server-global singleton, a fresh navigation can bootstrap
				// #run-status with the PREVIOUS run's terminal panel — so we can't just wait
				// for the missing-models text (it may be stale). Instead: (1) confirm THIS
				// run went in-flight (the running fragment's Stop button appears), (2) wait
				// for it to settle (Stop gone), then (3) assert the terminal panel rendered.
				waitForText("#run-status", "Stop", 15*time.Second),
				waitForTextGone("#run-status", "Stop", 45*time.Second),
				waitForText("#run-status", HeroMarker, 10*time.Second),
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

	payload, files := BuildPayload(label, caps)
	metaJSON, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := payload.Validate(setOf(files)); err != nil {
		return nil, fmt.Errorf("built payload is invalid: %w", err)
	}
	if err := payload.ValidateFiles(files); err != nil {
		return nil, fmt.Errorf("built payload exceeds push size caps: %w", err)
	}

	// Persist artifacts + metadata so `make ux-audit` leaves a browsable output dir.
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(outDir, name), data, 0o644); err != nil {
			return nil, fmt.Errorf("write artifact %s: %w", name, err)
		}
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
func BuildPayload(label string, caps []CapturedView) (PushPayload, map[string][]byte) {
	files := map[string][]byte{}
	pages := make([]PushPage, 0, len(caps))
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
		pages = append(pages, pg)
	}
	return PushPayload{Label: label, Environment: EnvLab, Pages: pages}, files
}

// setOf returns the key set of a file map (the "provided" set Validate expects).
func setOf(files map[string][]byte) map[string]bool {
	out := make(map[string]bool, len(files))
	for k := range files {
		out[k] = true
	}
	return out
}

// waitForText blocks until the element at sel has innerText containing text, or the
// timeout elapses (a loud error — the walk fails rather than capturing a wrong
// state). Implemented as a polling Evaluate so it needs no chromedp version-specific
// Poll helper.
func waitForText(sel, text string, timeout time.Duration) chromedp.Action {
	return waitForCond(sel, text, true, timeout)
}

// waitForTextGone blocks until the element at sel no longer has innerText containing
// text (or the timeout elapses). Used to observe a transient state clearing (e.g. the
// run's Stop button disappearing as it settles to its terminal panel).
func waitForTextGone(sel, text string, timeout time.Duration) chromedp.Action {
	return waitForCond(sel, text, false, timeout)
}

// waitForCond polls until (sel's innerText contains text) == want, or timeout.
func waitForCond(sel, text string, want bool, timeout time.Duration) chromedp.Action {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		deadline := time.Now().Add(timeout)
		expr := fmt.Sprintf(
			"(()=>{const el=document.querySelector(%q);return !!(el&&el.innerText&&el.innerText.indexOf(%q)>=0);})()",
			sel, text)
		for {
			var present bool
			if err := chromedp.Evaluate(expr, &present).Do(ctx); err != nil {
				return err
			}
			if present == want {
				return nil
			}
			if time.Now().After(deadline) {
				verb := "present"
				if !want {
					verb = "gone"
				}
				return fmt.Errorf("timeout after %s waiting for %q to be %s in %s", timeout, text, verb, sel)
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
	if err := res.Payload.ValidateFiles(res.Files); err != nil {
		return "", true, fmt.Errorf("refusing to push: %w", err)
	}
	out, err := Upload(ctx, nil, baseURL, token, res.Metadata, res.Files)
	if err != nil {
		return "", true, err
	}
	return out.URL, true, nil
}
