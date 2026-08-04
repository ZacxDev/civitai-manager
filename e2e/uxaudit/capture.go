package uxaudit

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

// Viewport is one capture viewport. Widths mirror auditloop (mobile 390px,
// desktop 1440px) so the pushed pages line up with a native auditloop crawl.
type Viewport struct {
	Name   string
	Width  int64
	Height int64
	Mobile bool
}

// Viewports are the two viewports every view is captured at.
var Viewports = []Viewport{
	{Name: "mobile", Width: 390, Height: 844, Mobile: true},
	{Name: "desktop", Width: 1440, Height: 900, Mobile: false},
}

// consoleEvent / networkEvent are the classified per-view events.
type consoleEvent struct {
	Text      string `json:"text"`
	URL       string `json:"url"`
	FirstPart bool   `json:"first_party"`
}

type networkEvent struct {
	URL       string `json:"url"`
	Status    int    `json:"status,omitempty"`
	Reason    string `json:"reason,omitempty"`
	FirstPart bool   `json:"first_party"`
}

// ViewCapture is everything captured for one (view, viewport).
type ViewCapture struct {
	ScreenshotPNG []byte
	AxeJSON       []byte // raw axe-core result JSON
	NetworkJSON   []byte // marshaled []networkEvent
	BodyText      string // document.body.innerText at capture time (for content assertions)

	// A11yDigestJSON is the bounded DOM/accessibility digest (a11y-digest.js output,
	// see a11y.go) captured in the SAME pass as axe, off the settled post-prep DOM.
	// auditloop feeds it to a deterministic gate that drops persona findings the DOM
	// refutes. It is BEST-EFFORT: empty when the script failed or the page is bare,
	// and BuildPayload attaches it only when non-empty (an empty digest 400s the push).
	A11yDigestJSON []byte

	AxeViolations     int
	ConsoleFirstParty int
	ConsoleThirdParty int
	NetworkFirstParty int
	NetworkThirdParty int

	// Console / Network are the ORIGIN-CLASSIFIED event lists behind the roll-up
	// counts above (each event's FirstPart is set). They are retained — not just
	// counted — so BuildPayload can emit per-event push FINDINGS carrying the actual
	// message text/status: a bare count tells you a first-party console error exists
	// but not what it says, which is unactionable in the pushed report.
	Console []consoleEvent
	Network []networkEvent
}

// Browser is a headless Chromium driving a SINGLE shared tab (the auditloop
// gotcha: a captureBeyondViewport screenshot on a second tab intermittently hangs
// under recent Chromium). One CDP listener routes events to the current view's
// collector.
type Browser struct {
	allocCtx    context.Context
	allocCancel context.CancelFunc
	ctx         context.Context
	ctxCancel   context.CancelFunc

	sess *session
}

// NewBrowser launches headless Chromium at execPath (host-agnostic; never
// downloads a browser). The caller owns Close.
func NewBrowser(parent context.Context, execPath string) (*Browser, error) {
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(execPath),
		chromedp.Headless,
		chromedp.DisableGPU,
		chromedp.NoSandbox, // nix chromium in CI/dev sandboxes has no user namespaces
		chromedp.Flag("hide-scrollbars", true),
		chromedp.Flag("mute-audio", true),
	)
	allocCtx, allocCancel := chromedp.NewExecAllocator(parent, opts...)
	ctx, ctxCancel := chromedp.NewContext(allocCtx)

	b := &Browser{
		allocCtx: allocCtx, allocCancel: allocCancel,
		ctx: ctx, ctxCancel: ctxCancel,
		sess: &session{},
	}

	// Start the browser + register ONE listener before any navigation.
	if err := chromedp.Run(ctx, network.Enable()); err != nil {
		b.Close()
		return nil, err
	}
	chromedp.ListenTarget(ctx, b.sess.handle)
	return b, nil
}

// Close shuts the browser down.
func (b *Browser) Close() {
	if b.ctxCancel != nil {
		b.ctxCancel()
	}
	if b.allocCancel != nil {
		b.allocCancel()
	}
}

// Capture navigates to pageURL at the given viewport and captures a full-page
// screenshot, the axe-core violations, and the classified console/network events.
func (b *Browser) Capture(pageURL string, vp Viewport) (ViewCapture, error) {
	return b.CaptureWith(pageURL, vp, nil)
}

// CaptureWith is Capture with an optional list of interactive `prep` actions that
// run AFTER the page has navigated + settled but BEFORE the axe scan + screenshot.
// It is how the walk drives a real interaction into a stable end-state before
// capturing it — e.g. opening the import <dialog>, or clicking Run and waiting for
// the missing-models hero panel to render — so the captured PNG/axe/DOM reflect the
// post-interaction view, not the initial load. prep is nil for a plain page load.
func (b *Browser) CaptureWith(pageURL string, vp Viewport, prep []chromedp.Action) (ViewCapture, error) {
	col := &collector{reqURLs: map[network.RequestID]string{}}
	b.sess.set(col)
	defer b.sess.set(nil)

	pageCtx, cancel := context.WithTimeout(b.ctx, 90*time.Second)
	defer cancel()

	var axeJSON string
	tasks := chromedp.Tasks{
		emulation.SetDeviceMetricsOverride(vp.Width, vp.Height, 1, vp.Mobile),
		chromedp.Navigate(pageURL),
		chromedp.WaitReady("body", chromedp.ByQuery),
		chromedp.Sleep(600 * time.Millisecond), // settle render/network
	}
	// Interactive prep (open a dialog, trigger a run, …) runs against the settled
	// page, mutating the DOM into the state we want to audit.
	tasks = append(tasks, prep...)
	tasks = append(tasks,
		chromedp.Evaluate(axeSource, nil),
		chromedp.Evaluate(axeRunScript, &axeJSON, func(p *runtime.EvaluateParams) *runtime.EvaluateParams {
			return p.WithAwaitPromise(true)
		}),
	)

	var out ViewCapture

	// Bounded DOM/accessibility digest for auditloop's persona-evaluator grounding.
	// Captured on the SAME settled, post-prep DOM axe just scanned, so the digest and
	// the axe result describe the same tree. NON-FATAL by design (mirrors auditloop's
	// own crawler): a digest failure must never fail the capture — the page simply
	// carries no digest and auditloop degrades to screenshot-only for it.
	tasks = append(tasks, chromedp.ActionFunc(func(ctx context.Context) error {
		var raw string
		if err := chromedp.Evaluate(a11yDigestSource, &raw).Do(ctx); err != nil {
			return nil
		}
		// Over-cap ⇒ treat as absent. auditloop REJECTS an oversized digest, and a
		// rejection fails the whole multi-page push, so dropping it here is the safe
		// direction (lost grounding on one page beats a lost run).
		if raw != "" && len(raw) <= MaxA11yDigestBytes {
			out.A11yDigestJSON = []byte(raw)
		}
		return nil
	}))

	// Freeze animation, then confirm quiescence, then shoot. Both steps run AFTER axe and
	// the digest so those two scan the page exactly as it renders for a user.
	tasks = append(tasks,
		chromedp.Evaluate(freezeAnimationScript, nil),
		waitForTransitionsToSettle(transitionSettleTimeout),
	)

	tasks = append(tasks, chromedp.ActionFunc(func(ctx context.Context) error {
		b, err := page.CaptureScreenshot().
			WithFormat(page.CaptureScreenshotFormatPng).
			WithCaptureBeyondViewport(true).
			Do(ctx)
		if err != nil {
			return err
		}
		out.ScreenshotPNG = b
		return nil
	}))
	// Grab the rendered body text so callers/tests can assert on CONTENT (e.g. the
	// hero "Missing model files" panel), not just a non-empty screenshot.
	tasks = append(tasks, chromedp.Evaluate(`document.body.innerText`, &out.BodyText))

	if err := chromedp.Run(pageCtx, tasks); err != nil {
		return out, err
	}

	// Parse axe violations.
	if axeJSON != "" {
		out.AxeJSON = []byte(axeJSON)
		var ar struct {
			Violations []json.RawMessage `json:"violations"`
		}
		if json.Unmarshal([]byte(axeJSON), &ar) == nil {
			out.AxeViolations = len(ar.Violations)
		}
	}

	// Classify + roll up console/network events by origin.
	col.mu.Lock()
	var netEvents []networkEvent
	for _, ce := range col.console {
		// Stamp the classification ONTO the retained event (ce is a copy, so this
		// assignment is what makes FirstPart meaningful downstream in BuildPayload —
		// previously the verdict was computed for the count and then thrown away).
		ce.FirstPart = sameOrigin(pageURL, orDefault(ce.URL, pageURL))
		if ce.FirstPart {
			out.ConsoleFirstParty++
		} else {
			out.ConsoleThirdParty++
		}
		out.Console = append(out.Console, ce)
	}
	for _, ne := range col.network {
		ne.FirstPart = sameOrigin(pageURL, ne.URL)
		if ne.FirstPart {
			out.NetworkFirstParty++
		} else {
			out.NetworkThirdParty++
		}
		netEvents = append(netEvents, ne)
	}
	out.Network = netEvents
	col.mu.Unlock()
	out.NetworkJSON, _ = json.Marshal(netEvents)

	return out, nil
}

// freezeAnimationScript collapses every CSS transition and animation to zero duration
// immediately before the screenshot, so any in-flight transition jumps to its FINAL
// value and no new one can start.
//
// 🔴 This is the fix; waitForTransitionsToSettle below is only a confirmation. Waiting
// alone was measured and is NOT sufficient: it cut the observed instability from 4-in-24
// to 1-in-30, but could not remove it, because a transition can begin AFTER the last
// poll — including during CaptureScreenshot itself, which with captureBeyondViewport is
// not instantaneous. No pre-shot check can close that window; removing the source can.
//
// It does not doctor the artifact. The settled value is what a user sees once the
// 120ms elapses, and it is exactly what a prefers-reduced-motion user sees at all
// times; only the intermediate frames are eliminated. It runs AFTER axe and the a11y
// digest so neither is measured against a modified page.
//
// `animation-play-state: paused` is deliberately NOT used: it freezes an infinite
// animation at whatever frame it reached, which is nondeterministic. Zero duration
// pins it at its final/first frame instead.
const freezeAnimationScript = `(() => {
  const id = 'uxaudit-freeze-animation';
  if (document.getElementById(id)) return true;
  const st = document.createElement('style');
  st.id = id;
  st.textContent = '*, *::before, *::after {' +
    'transition-duration: 0s !important;' +
    'transition-delay: 0s !important;' +
    'animation-duration: 0s !important;' +
    'animation-delay: 0s !important;' +
    'animation-iteration-count: 1 !important;' +
  '}';
  (document.head || document.documentElement).appendChild(st);
  return true;
})()`

// transitionSettleTimeout bounds the wait below. It is a BACKSTOP, not a patience
// setting: the transitions in question are 120ms, so anything still running after this
// is not going to settle and the capture proceeds rather than failing the walk.
const transitionSettleTimeout = 2 * time.Second

// runningTransitionsScript counts CSS TRANSITIONS currently running in the page.
//
// 🔴 Transitions ONLY — deliberately not every animation. A CSS animation can be
// `infinite` (a spinner), so waiting on getAnimations() as a whole would burn the
// timeout on every capture of any page with one. A transition always terminates, which
// is what makes waiting on it bounded by construction rather than by the deadline.
const runningTransitionsScript = `(() => {
  if (typeof document.getAnimations !== 'function') return 0;
  if (typeof CSSTransition === 'undefined') return 0;
  return document.getAnimations().filter(
    a => a instanceof CSSTransition && a.playState === 'running').length;
})()`

// waitForTransitionsToSettle blocks until no CSS transition is running, then confirms
// it once more after a short gap. With freezeAnimationScript applied first this is
// normally satisfied on the first poll; it stays as the confirmation that the freeze
// actually took.
//
// 🔴 WHY ANY OF THIS EXISTS, measured rather than assumed. Without it the
// run-missing-models-expanded capture was intermittently nondeterministic: 24 captures
// of the SAME view on the same tree produced 3 DISTINCT screenshots (20/3/1), differing
// only in a 99x36px box at (64,337) — the "Generate" button, whose fill read
// rgb(24,100,171) settled and rgb(24,103,176) mid-transition. The rule is
// `transition: background-color 120ms ease` on .civitai-button
// (internal/web/assets/civitai-components.css:43).
//
// ⚠ `prefers-reduced-motion: reduce` would NOT have fixed it, which is worth recording
// because it is the obvious first move: the app's reduced-motion blocks all live in
// app.css, while that transition is in the VENDORED civitai-components.css and is not
// covered by any of them. Emulating reduced motion would also change what every capture
// renders; waiting changes only WHEN the shot is taken.
//
// The second confirmation after a gap is the cheap defence against sampling the one
// instant between two chained transitions, where a single poll reads zero.
func waitForTransitionsToSettle(timeout time.Duration) chromedp.Action {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		deadline := time.Now().Add(timeout)
		quiet := 0
		for {
			var running int
			if err := chromedp.Evaluate(runningTransitionsScript, &running).Do(ctx); err != nil {
				return err // a broken probe must not pass as "settled"
			}
			if running == 0 {
				quiet++
				if quiet >= 2 {
					return nil
				}
			} else {
				quiet = 0
			}
			if time.Now().After(deadline) {
				// Deliberately NOT an error: a stuck transition is not worth failing a walk
				// over, and the capture is still useful. It just may not be byte-stable.
				return nil
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(40 * time.Millisecond):
			}
		}
	})
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// session routes CDP events from the single shared tab to the current view's
// collector. The listener runs on its own goroutine, so access is mutex-guarded.
type session struct {
	mu  sync.Mutex
	cur *collector
}

func (s *session) set(c *collector) {
	s.mu.Lock()
	s.cur = c
	s.mu.Unlock()
}

func (s *session) handle(ev any) {
	s.mu.Lock()
	c := s.cur
	s.mu.Unlock()
	if c != nil {
		c.handle(ev)
	}
}

// collector accumulates console + network errors from CDP events (thread-safe).
type collector struct {
	mu      sync.Mutex
	console []consoleEvent
	network []networkEvent
	reqURLs map[network.RequestID]string
}

func (c *collector) handle(ev any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch e := ev.(type) {
	case *network.EventRequestWillBeSent:
		c.reqURLs[e.RequestID] = e.Request.URL
	case *network.EventResponseReceived:
		if e.Response.Status >= 400 {
			c.network = append(c.network, networkEvent{URL: e.Response.URL, Status: int(e.Response.Status), Reason: "http_status"})
		}
	case *network.EventLoadingFailed:
		u := c.reqURLs[e.RequestID]
		if u == "" || e.ErrorText == "net::ERR_ABORTED" {
			return
		}
		c.network = append(c.network, networkEvent{URL: u, Reason: e.ErrorText})
	case *runtime.EventConsoleAPICalled:
		if e.Type != "error" {
			return
		}
		c.console = append(c.console, consoleEvent{Text: consoleText(e), URL: stackURL(e.StackTrace)})
	case *runtime.EventExceptionThrown:
		det := e.ExceptionDetails
		u := det.URL
		if u == "" {
			u = stackURL(det.StackTrace)
		}
		c.console = append(c.console, consoleEvent{Text: det.Text, URL: u})
	}
}

// consoleText renders a console.error(...) call's arguments as human-readable text.
//
// A CDP arg's Value is RAW JSON, so a string argument arrives QUOTED and
// backslash-escaped (`"the selector \"#x\" matched nothing"`). Stripping only the
// outer quotes leaves the inner `\"` escapes in the text — which used to be
// invisible (the text was counted and discarded) but is now shipped verbatim as a
// console FINDING detail, where the stray backslashes are user-visible noise. So
// decode a JSON string properly and fall back to the raw JSON for non-string args
// (numbers, objects), which are already their own readable representation.
func consoleText(e *runtime.EventConsoleAPICalled) string {
	parts := make([]string, 0, len(e.Args))
	for _, a := range e.Args {
		if a.Value != nil {
			parts = append(parts, decodeConsoleArg(a.Value))
		} else if a.Description != "" {
			parts = append(parts, a.Description)
		}
	}
	return strings.Join(parts, " ")
}

// decodeConsoleArg turns one raw-JSON console argument into display text: a JSON
// string is unquoted/unescaped; anything else (number, object, null) is returned as
// its JSON form.
//
// It decodes into `any` rather than straight into a string on purpose: JSON `null`
// unmarshals into a string WITHOUT error and leaves it "", which would silently drop
// a console.error(null) argument instead of showing it.
func decodeConsoleArg(raw []byte) string {
	var v any
	if json.Unmarshal(raw, &v) == nil {
		if s, ok := v.(string); ok {
			return s
		}
	}
	// Non-string (or undecodable) args are already readable as their JSON text, and
	// echoing it avoids re-formatting numbers.
	return string(raw)
}

func stackURL(st *runtime.StackTrace) string {
	if st == nil || len(st.CallFrames) == 0 {
		return ""
	}
	return st.CallFrames[0].URL
}
