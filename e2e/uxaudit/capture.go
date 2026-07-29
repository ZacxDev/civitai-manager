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

	AxeViolations     int
	ConsoleFirstParty int
	ConsoleThirdParty int
	NetworkFirstParty int
	NetworkThirdParty int
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
		first := sameOrigin(pageURL, orDefault(ce.URL, pageURL))
		if first {
			out.ConsoleFirstParty++
		} else {
			out.ConsoleThirdParty++
		}
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
	col.mu.Unlock()
	out.NetworkJSON, _ = json.Marshal(netEvents)

	return out, nil
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

func consoleText(e *runtime.EventConsoleAPICalled) string {
	parts := make([]string, 0, len(e.Args))
	for _, a := range e.Args {
		if a.Value != nil {
			parts = append(parts, strings.Trim(string(a.Value), `"`))
		} else if a.Description != "" {
			parts = append(parts, a.Description)
		}
	}
	return strings.Join(parts, " ")
}

func stackURL(st *runtime.StackTrace) string {
	if st == nil || len(st.CallFrames) == 0 {
		return ""
	}
	return st.CallFrames[0].URL
}
