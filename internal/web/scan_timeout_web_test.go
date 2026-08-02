package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/config"
	"github.com/ZacxDev/civitai-manager/internal/library"
)

// errScanContextUnbounded is the seam's SENTINEL for "nobody bounded me". A scan
// seam that sits waiting on ctx.Done() and instead trips its own fallback timer
// proves the job context carried no deadline the test could reach — which is
// exactly the shape of the bug these guards exist for. Returning a distinct
// error (rather than just hanging) makes the failure land on a named assertion
// with a readable message instead of on a poll timeout.
var errScanContextUnbounded = errors.New("scan seam: the job context was NOT bounded by the configured web scan timeout")

// seamFallback is how long a ctx-waiting seam waits before declaring the context
// unbounded. It is only ever paid on a FAILING (mutated) run — a passing run's
// deadline fires in tens of milliseconds — and it must stay comfortably inside
// pollScanUntilDone's 5s budget so a regression fails with THIS test's message
// rather than a generic "scan did not finish".
const seamFallback = 2 * time.Second

// TestWebScanTimeoutBoundsTheScanJob is THE regression guard for the defect this
// change fixes: Server.webScanTimeout() existed, was documented as
// `web_scan_timeout` / `--web-scan-timeout`, was plumbed YAML -> flag ->
// config.Config -> web.Config -> Server... and had ZERO callers from v0.1.13
// (29d48f8) through v0.1.101, because startScan bounded the job with a
// hard-coded 6h const instead. Setting the documented knob changed nothing.
//
// It fails if startScan stops deriving the job context from s.webScanTimeout():
// the seam then never sees a 40ms deadline, trips its own fallback, and settles
// with errScanContextUnbounded instead of context.DeadlineExceeded.
func TestWebScanTimeoutBoundsTheScanJob(t *testing.T) {
	srv := newLibraryTestServer(t, t.TempDir())
	// Injected, not slept: the whole point is that the CONFIGURED value governs.
	srv.cfg.WebScanTimeout = 40 * time.Millisecond

	entered := make(chan struct{}, 1)
	srv.scanFn = func(ctx context.Context, onFile func(library.FileResult), onDiscovered func(int), onHashed func(int)) error {
		entered <- struct{}{}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(seamFallback):
			return errScanContextUnbounded
		}
	}

	if rec := post(t, srv, "/library/scan", url.Values{}, true); rec.Code != http.StatusOK {
		t.Fatalf("scan POST = %d, want 200", rec.Code)
	}
	// Confirm the fixture REACHES the code path under guard. Without this, a
	// startScan that silently refused to run at all would satisfy every
	// assertion below by never producing a sentinel error.
	select {
	case <-entered:
	case <-time.After(seamFallback):
		t.Fatal("the scan seam never ran, so nothing below observes the job context: this guard would be vacuous")
	}

	body := pollScanUntilDone(t, srv)
	snap := srv.scanJobState()

	if errors.Is(snap.Err, errScanContextUnbounded) {
		t.Fatalf("cfg.WebScanTimeout (40ms) did NOT bound the scan job: the seam waited %v on ctx.Done() and no deadline ever fired. "+
			"startScan must derive its context from s.webScanTimeout(), not a hard-coded budget", seamFallback)
	}
	if !errors.Is(snap.Err, context.DeadlineExceeded) {
		t.Fatalf("a timed-out scan should settle with context.DeadlineExceeded, got %v", snap.Err)
	}
	// The user must be told it TIMED OUT (and how to raise the limit) — not left
	// with a silent success, a bare crash, or a "you stopped it" they never did.
	if want := "Scan timed out; narrow the path or raise --web-scan-timeout."; !strings.Contains(body, want) {
		t.Errorf("terminal fragment should carry %q:\n%s", want, body)
	}
	for _, unwanted := range []string{"Scan stopped", "Scan complete", "Scan failed"} {
		if strings.Contains(body, unwanted) {
			t.Errorf("a timed-out scan must not read as %q:\n%s", unwanted, body)
		}
	}
	// And it is genuinely terminal: the job settled and the client stops polling.
	if snap.Running {
		t.Error("job should have settled (running=false) after the deadline fired")
	}
	if hasScanPoller(body) {
		t.Error("terminal fragment must not re-arm the poller")
	}
}

// TestUnconfiguredScanKeepsTheHistoricalSixHourBudget pins the OTHER half of the
// fix: wiring the knob up must not change behaviour for anyone who never set it.
// Honouring the previously-documented 2m default would have started killing
// scans that succeed today (hashing a multi-GB library on a slow drive runs for
// hours), so the default moved to the value the hard-coded scanJobBudget already
// enforced. This fails if the default regresses to minutes AND if the deadline
// is dropped entirely — "unset" means the default, never "no bound".
func TestUnconfiguredScanKeepsTheHistoricalSixHourBudget(t *testing.T) {
	// cfg.WebScanTimeout is left at its zero value: nothing configured.
	srv := newLibraryTestServer(t, t.TempDir())
	if srv.cfg.WebScanTimeout != 0 {
		t.Fatalf("fixture must leave the timeout unset to exercise the default, got %v", srv.cfg.WebScanTimeout)
	}

	var (
		budget   time.Duration
		hasDL    bool
		observed = make(chan struct{})
	)
	srv.scanFn = func(ctx context.Context, onFile func(library.FileResult), onDiscovered func(int), onHashed func(int)) error {
		dl, ok := ctx.Deadline()
		hasDL = ok
		if ok {
			budget = time.Until(dl)
		}
		close(observed) // publishes the two writes above to the reader below
		return nil
	}

	if rec := post(t, srv, "/library/scan", url.Values{}, true); rec.Code != http.StatusOK {
		t.Fatalf("scan POST = %d, want 200", rec.Code)
	}
	select {
	case <-observed:
	case <-time.After(seamFallback):
		t.Fatal("the scan seam never ran; the budget below was never measured")
	}
	pollScanUntilDone(t, srv)

	if !hasDL {
		t.Fatal("an unset web_scan_timeout must still bound the job (it means the DEFAULT, not 'no bound'): the job context carried no deadline")
	}
	// A window, not an equality: the deadline is set a few microseconds before we
	// read it. The lower bound is far above any plausible minutes-scale default.
	if budget < 5*time.Hour+55*time.Minute || budget > 6*time.Hour {
		t.Fatalf("unconfigured scan budget = %v, want ~6h (the value the hard-coded scanJobBudget enforced before the knob was wired up); "+
			"an unset web_scan_timeout must not change today's behaviour", budget)
	}
}

// TestWebScanTimeoutFallsBackToTheDefault pins the resolution table directly:
// the literal default, and that a zero/negative configured value means that
// default rather than "no bound" (would leak a stuck job forever) or "instant"
// (would kill every scan at once).
func TestWebScanTimeoutFallsBackToTheDefault(t *testing.T) {
	// A literal, not config.DefaultWebScanTimeout restated — this is the contract
	// the docs advertise, and it must move only deliberately.
	if config.DefaultWebScanTimeout != 6*time.Hour {
		t.Fatalf("config.DefaultWebScanTimeout = %v, want 6h (docs/configuration.md and docs/cli.md both state 6h)", config.DefaultWebScanTimeout)
	}

	srv := newLibraryTestServer(t, t.TempDir())
	for _, tc := range []struct {
		name string
		set  time.Duration
		want time.Duration
	}{
		{"unset", 0, 6 * time.Hour},
		{"negative", -time.Second, 6 * time.Hour},
		{"configured", 90 * time.Second, 90 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv.cfg.WebScanTimeout = tc.set
			if got := srv.webScanTimeout(); got != tc.want {
				t.Fatalf("webScanTimeout() with cfg=%v = %v, want %v", tc.set, got, tc.want)
			}
		})
	}
}

// TestScanStopStillWorksWithAConfiguredTimeout guards the interaction: the
// job's cancel func is now the one returned by context.WithTimeout(…,
// s.webScanTimeout()), and stopScan calls it. A user Stop must still end the
// scan promptly and read as "Scan stopped" — not as a timeout, and not as a
// hang until the (here, one-hour) deadline.
func TestScanStopStillWorksWithAConfiguredTimeout(t *testing.T) {
	srv := newLibraryTestServer(t, t.TempDir())
	// Long enough that ONLY the Stop can end this scan: if the terminal fragment
	// arrives, cancellation did it.
	srv.cfg.WebScanTimeout = time.Hour

	entered := make(chan struct{}, 1)
	srv.scanFn = func(ctx context.Context, onFile func(library.FileResult), onDiscovered func(int), onHashed func(int)) error {
		entered <- struct{}{}
		<-ctx.Done()
		return ctx.Err()
	}

	if rec := post(t, srv, "/library/scan", url.Values{}, true); rec.Code != http.StatusOK {
		t.Fatalf("scan POST = %d, want 200", rec.Code)
	}
	select {
	case <-entered:
	case <-time.After(seamFallback):
		t.Fatal("the scan seam never ran; there was nothing for Stop to cancel")
	}
	if !srv.scanJobState().Running {
		t.Fatal("scan should be running before Stop")
	}

	if rec := post(t, srv, "/library/scan/stop", url.Values{}, true); rec.Code != http.StatusOK {
		t.Fatalf("stop POST = %d, want 200", rec.Code)
	}
	body := pollScanUntilDone(t, srv)
	snap := srv.scanJobState()

	if !snap.Stopped || snap.Running {
		t.Fatalf("after Stop the job should be stopped and settled, got stopped=%v running=%v", snap.Stopped, snap.Running)
	}
	if !errors.Is(snap.Err, context.Canceled) {
		t.Fatalf("a stopped scan should settle with context.Canceled, got %v", snap.Err)
	}
	if !strings.Contains(body, "Scan stopped") {
		t.Errorf("terminal fragment should read 'Scan stopped':\n%s", body)
	}
	if strings.Contains(body, "Scan timed out") {
		t.Errorf("a user Stop must not be reported as a timeout:\n%s", body)
	}
	if hasScanPoller(body) {
		t.Errorf("terminal fragment must not re-arm the poller:\n%s", body)
	}
}
