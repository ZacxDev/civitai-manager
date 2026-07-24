package web

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/library"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// fileResult builds a matched/unmatched streamed result for the seam.
func fileResult(name string, modelID *int) library.FileResult {
	status := store.LocalStatusUnmatched
	if modelID != nil {
		status = store.LocalStatusMatched
	}
	return library.FileResult{
		Path: "/models/" + name, Name: name, SizeBytes: 1234,
		SHA256: "sha-" + name, Status: status, ModelID: modelID,
	}
}

// countScanCards counts rendered scan result cards by their status-badge-bearing
// row marker (each scanResultCard is a flex row with this class).
func countScanCards(body string) int {
	return strings.Count(body, `class="flex items-center justify-between gap-3 rounded-md border border-slate-800 bg-slate-900 p-2"`)
}

func intp(i int) *int { return &i }

// --- CTA (finding #2) --------------------------------------------------------

// TestScanCTAAppearsAfterFirstAdd proves finding #2: the persisted-selection
// fragment renders NO CTA at zero dirs and the "Scan for models" CTA once >=1
// dir, and the Add response (POST /library/scan-dirs/add) carries BOTH the
// updated #selected-dirs list AND that CTA — so it appears immediately after the
// first Add with no page reload.
func TestScanCTAAppearsAfterFirstAdd(t *testing.T) {
	// Zero dirs → no CTA, just the empty hint.
	empty := renderString(t, selectedDirsList(nil, "csrf"))
	if strings.Contains(empty, "Scan for models") {
		t.Errorf("no CTA should render at 0 selected dirs:\n%s", empty)
	}

	// One dir → the CTA is present and POSTs to /library/scan.
	one := renderString(t, selectedDirsList([]string{"/data/loras"}, "csrf"))
	if !strings.Contains(one, "Scan for models") || !strings.Contains(one, `hx-post="/library/scan"`) {
		t.Errorf(">=1 selected dir should render the 'Scan for models' CTA posting /library/scan:\n%s", one)
	}

	// The Add handler's response fragment includes the CTA too.
	root := t.TempDir()
	extra := t.TempDir()
	srv := newLibraryTestServer(t, root)
	rec := post(t, srv, "/library/scan-dirs/add", url.Values{"path": {extra}}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("add = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, extra) || !strings.Contains(body, "Scan for models") {
		t.Errorf("add response must include the selected dir AND the CTA:\n%s", body)
	}
}

// --- Poller structure (the orphan-poller bug-class guard) --------------------

// countScanPoll reports how many #scan-poll elements a fragment contains: exactly
// one while scanning, zero when terminal — never an orphan.
func countScanPoll(body string) int { return strings.Count(body, `id="scan-poll"`) }

// TestScanPollerStructure proves the scanning fragment has exactly one #scan-poll
// targeting the STABLE #scan-results (never self-replacing via outerHTML), and the
// terminal fragment has none (so htmx stops and no orphan poller remains).
func TestScanPollerStructure(t *testing.T) {
	scan := renderString(t, scanScanning(scanSnapshot{
		Started: true, Running: true,
		Results: []library.FileResult{fileResult("a.safetensors", nil)},
		Scanned: 1, Unmatched: 1,
	}, "csrf"))
	if n := countScanPoll(scan); n != 1 {
		t.Errorf("scanning fragment must have exactly one #scan-poll, got %d:\n%s", n, scan)
	}
	for _, want := range []string{
		`hx-get="/library/scan/status"`,
		`hx-trigger="load delay:1s"`,
		`hx-target="#scan-results"`,
		"Stop scanning",
	} {
		if !strings.Contains(scan, want) {
			t.Errorf("scanning fragment missing %q:\n%s", want, scan)
		}
	}
	if strings.Contains(scan, `hx-swap="outerHTML"`) {
		t.Errorf("scan poller must target the stable container, never self-replace via outerHTML:\n%s", scan)
	}

	term := renderString(t, scanResults(buildLibraryView(nil), scanSnapshot{
		Started: true, Scanned: 3, Matched: 1, Unmatched: 2,
	}, "csrf"))
	if n := countScanPoll(term); n != 0 {
		t.Errorf("terminal fragment must have zero #scan-poll, got %d:\n%s", n, term)
	}
}

// TestScanResultsMessaging proves the terminal render distinguishes complete vs.
// stopped vs. errored, and the never-started case renders plain content.
func TestScanResultsMessaging(t *testing.T) {
	complete := renderString(t, scanResults(buildLibraryView(nil), scanSnapshot{
		Started: true, Scanned: 5, Discovered: 5, Matched: 2, Unmatched: 3,
	}, "csrf"))
	// The terminal shows the discovered total + matched/unmatched breakdown so a low
	// match count reads as normal.
	if !strings.Contains(complete, "Scan complete — 5 discovered · 2 matched · 3 unmatched") {
		t.Errorf("exhausted scan should say 'Scan complete — 5 discovered · 2 matched · 3 unmatched':\n%s", complete)
	}
	stopped := renderString(t, scanResults(buildLibraryView(nil), scanSnapshot{
		Started: true, Scanned: 3, Discovered: 5, Matched: 1, Unmatched: 2, Stopped: true,
	}, "csrf"))
	// A stop mid-scan shows progress against the total it had discovered so far.
	if !strings.Contains(stopped, "Scan stopped — 3 / 5 discovered · matched 1 · unmatched 2") {
		t.Errorf("user-stopped scan should say 'Scan stopped …' with N / total discovered:\n%s", stopped)
	}
	if strings.Contains(stopped, "Scan complete") {
		t.Errorf("stopped scan must not claim complete:\n%s", stopped)
	}
	// Errored (too large) surfaces the friendly message.
	errored := renderString(t, scanResults(buildLibraryView(nil), scanSnapshot{
		Started: true, Err: library.ErrScanTooLarge,
	}, "csrf"))
	if !strings.Contains(errored, "Scan too large") {
		t.Errorf("errored scan should surface the friendly message:\n%s", errored)
	}
	// Never started → plain library content, no status card.
	none := renderString(t, scanResults(buildLibraryView(nil), scanSnapshot{}, "csrf"))
	if strings.Contains(none, "Scan result") {
		t.Errorf("never-started terminal must render plain content, no 'Scan result' card:\n%s", none)
	}
}

// --- Async / streaming (finding #3) ------------------------------------------

// blockingScan returns a scanFn seam that signals when entered (started) and
// blocks until release is closed, then emits emit and returns err.
func blockingScan(started chan<- struct{}, release <-chan struct{}, emit []library.FileResult, err error) func(context.Context, func(library.FileResult), func(int), func(int)) error {
	return func(ctx context.Context, onFile func(library.FileResult), onDiscovered func(int), onHashed func(int)) error {
		select {
		case started <- struct{}{}:
		default:
		}
		<-release
		onDiscovered(len(emit))
		for _, fr := range emit {
			onFile(fr)
		}
		return err
	}
}

// TestScanPostRedirectsAndRunsInBackground proves the POST returns immediately
// (HX-Redirect to the Model files tab) while the scan runs on a BACKGROUND
// context, not the request context: the initiating POST returns with the job
// still running, then the job settles and a poll observes the streamed cards.
func TestScanPostRedirectsAndRunsInBackground(t *testing.T) {
	srv := newLibraryTestServer(t, t.TempDir())
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	srv.scanFn = blockingScan(started, release, []library.FileResult{
		fileResult("a.safetensors", intp(7)),
	}, nil)

	rec := post(t, srv, "/library/scan", url.Values{}, true)
	if rec.Code != http.StatusOK || rec.Header().Get("HX-Redirect") != "/library?tab=files" {
		t.Fatalf("scan POST should redirect to the Model files tab, got %d redirect=%q", rec.Code, rec.Header().Get("HX-Redirect"))
	}
	<-started // the scan goroutine is running while the request has already returned

	if !srv.scanJobState().Running {
		t.Fatalf("scan job should still be running after the POST returned")
	}
	// A status poll during this window keeps polling (scanning fragment).
	if body := get(t, srv, "/library/scan/status").Body.String(); !hasScanPoller(body) {
		t.Fatalf("status while running must return the poller:\n%s", body)
	}

	close(release)
	// The terminal view is the AUTHORITATIVE local_files Summary (the seam does not
	// persist, so it is empty here) plus the completion message. The streamed cards
	// live in the scanning view, exercised by TestScanStatusStreamsGrowingCards.
	term := pollScanUntilDone(t, srv)
	for _, want := range []string{"Scan result", "Scan complete — 1 discovered · 1 matched · 0 unmatched"} {
		if !strings.Contains(term, want) {
			t.Errorf("completed scan terminal missing %q:\n%s", want, term)
		}
	}
}

// TestScanStopsDiscovery proves finding #3(a): starting a model scan STOPS any
// running install-discovery crawl (the user is moving to the next phase).
func TestScanStopsDiscovery(t *testing.T) {
	srv := newLibraryTestServer(t, t.TempDir())
	srv.discoverRoots = []string{t.TempDir()}

	entered := make(chan struct{})
	srv.crawlFn = func(ctx context.Context, _ []string, _ library.DiscoverOptions) ([]library.Install, error) {
		close(entered)
		<-ctx.Done() // stay running until cancelled
		return nil, ctx.Err()
	}
	// A trivially-completing scan seam so the scan itself does not block.
	srv.scanFn = func(ctx context.Context, onFile func(library.FileResult), onDiscovered func(int), onHashed func(int)) error {
		return nil
	}

	if rec := post(t, srv, "/library/discover", url.Values{}, true); rec.Code != http.StatusOK {
		t.Fatalf("discover = %d", rec.Code)
	}
	<-entered // discovery crawl is running

	if rec := post(t, srv, "/library/scan", url.Values{}, true); rec.Code != http.StatusOK {
		t.Fatalf("scan = %d", rec.Code)
	}

	// The discovery job must be cancelled (stopped=true) and settle (running=false).
	deadline := time.Now().Add(2 * time.Second)
	for {
		_, running, _, stopped, _ := srv.discoverJobState()
		if !running {
			if !stopped {
				t.Fatalf("starting a scan should have STOPPED the discovery crawl (stopped=false)")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("discovery crawl was not cancelled when the scan started (still running)")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestScanStatusStreamsGrowingCards proves the streaming view GROWS: with a seam
// that emits results one at a time (gated deterministically), successive
// /library/scan/status polls show 1, then 2, then 3 cards, then a terminal view.
func TestScanStatusStreamsGrowingCards(t *testing.T) {
	srv := newLibraryTestServer(t, t.TempDir())
	frs := []library.FileResult{
		fileResult("a.safetensors", intp(1)),
		fileResult("b.safetensors", nil),
		fileResult("c.safetensors", intp(3)),
	}
	progress := make(chan int)
	step := make(chan struct{})
	srv.scanFn = func(ctx context.Context, onFile func(library.FileResult), onDiscovered func(int), onHashed func(int)) error {
		onDiscovered(len(frs)) // walk found all 3 up front
		for i, fr := range frs {
			onFile(fr) // appends under scanMu (happens-before the progress send)
			progress <- i
			<-step
		}
		return nil
	}

	if rec := post(t, srv, "/library/scan", url.Values{}, true); rec.Code != http.StatusOK {
		t.Fatalf("scan = %d", rec.Code)
	}
	for i := range frs {
		<-progress // result i has been appended
		body := get(t, srv, "/library/scan/status").Body.String()
		if !hasScanPoller(body) {
			t.Fatalf("status should still be scanning after %d results:\n%s", i+1, body)
		}
		if got := countScanCards(body); got != i+1 {
			t.Fatalf("after %d streamed results, status should show %d cards, got %d:\n%s", i+1, i+1, got, body)
		}
		// The streaming progress line shows progress against the discovered total.
		if want := fmt.Sprintf("%d / 3 discovered", i+1); !strings.Contains(body, want) {
			t.Errorf("streaming progress after %d results should read %q:\n%s", i+1, want, body)
		}
		step <- struct{}{} // let the seam emit the next
	}
	// After the last step the seam returns → terminal (no poller).
	term := pollScanUntilDone(t, srv)
	if countScanPoll(term) != 0 {
		t.Errorf("terminal must have no poller:\n%s", term)
	}
	if !strings.Contains(term, "Scan complete — 3 discovered · 2 matched · 1 unmatched") {
		t.Errorf("terminal should report 3 discovered / 2 matched / 1 unmatched:\n%s", term)
	}
}

// TestScanStatusRaceSafe drives concurrent OnFile appends and status snapshots to
// prove the snapshot-copy-under-lock guard (run under -race): a poller reading
// /library/scan/status while the scan streams must never see a torn/duplicated
// slice nor trip the race detector.
func TestScanStatusRaceSafe(t *testing.T) {
	srv := newLibraryTestServer(t, t.TempDir())
	const n = 200
	done := make(chan struct{})
	srv.scanFn = func(ctx context.Context, onFile func(library.FileResult), onDiscovered func(int), onHashed func(int)) error {
		defer close(done)
		for i := 0; i < n; i++ {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
			onFile(fileResult("f"+strconv.Itoa(i), nil))
		}
		return nil
	}

	if rec := post(t, srv, "/library/scan", url.Values{}, true); rec.Code != http.StatusOK {
		t.Fatalf("scan = %d", rec.Code)
	}

	// Hammer the status endpoint concurrently with the streaming appends.
	var wg sync.WaitGroup
	var polls int32
	stop := make(chan struct{})
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				_ = get(t, srv, "/library/scan/status").Body.String()
				atomic.AddInt32(&polls, 1)
			}
		}()
	}
	<-done
	close(stop)
	wg.Wait()

	if atomic.LoadInt32(&polls) == 0 {
		t.Fatal("expected concurrent status polls")
	}
	term := pollScanUntilDone(t, srv)
	if got := srv.mustScanned(t); got != n {
		t.Fatalf("all %d streamed results should be recorded, got %d", n, got)
	}
	_ = term
}

// mustScanned returns the job's scanned counter under the lock.
func (s *Server) mustScanned(t *testing.T) int {
	t.Helper()
	return s.scanJobState().Scanned
}

// TestScanCapturesDiscoveredFromRealScanner proves the end-to-end discovered-count
// wiring through the PRODUCTION path (scanFn=nil): the real scanner walks a temp
// tree with K model files and reports the total via OnDiscovered, the scan job
// records discovered==K, and the terminal renders "K discovered". Offline
// (match_remote=false) so the scan makes no CivitAI API calls.
func TestScanCapturesDiscoveredFromRealScanner(t *testing.T) {
	root := t.TempDir()
	const k = 3
	for i := 0; i < k; i++ {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("m%d.safetensors", i)), []byte("weights"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// A sidecar/preview must NOT inflate the discovered model-file total.
	if err := os.WriteFile(filepath.Join(root, "m0.preview.png"), []byte("img"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := newLibraryTestServer(t, root)
	if err := srv.store.SetSetting(matchRemoteSettingKey, "false"); err != nil {
		t.Fatal(err)
	}
	// scanFn stays nil → startScan builds and runs the real scanner over model_root.
	if rec := post(t, srv, "/library/scan", url.Values{}, true); rec.Code != http.StatusOK {
		t.Fatalf("scan = %d", rec.Code)
	}
	term := pollScanUntilDone(t, srv)
	if got := srv.scanJobState().Discovered; got != k {
		t.Fatalf("discovered total from the real scanner = %d, want %d", got, k)
	}
	if want := fmt.Sprintf("Scan complete — %d discovered", k); !strings.Contains(term, want) {
		t.Errorf("terminal should show %q:\n%s", want, term)
	}
}

// --- Stop (finding #3) -------------------------------------------------------

// TestScanStopCancelsAndIsTerminal proves POST /library/scan/stop cancels the
// running scan → the status shows "Scan stopped" in a terminal (poller-less)
// view; it is idempotent and CSRF-protected.
func TestScanStopCancelsAndIsTerminal(t *testing.T) {
	srv := newLibraryTestServer(t, t.TempDir())
	entered := make(chan struct{})
	srv.scanFn = func(ctx context.Context, onFile func(library.FileResult), onDiscovered func(int), onHashed func(int)) error {
		onFile(fileResult("a.safetensors", nil))
		close(entered)
		<-ctx.Done() // block until Stop cancels the context
		return ctx.Err()
	}

	if rec := post(t, srv, "/library/scan", url.Values{}, true); rec.Code != http.StatusOK {
		t.Fatalf("scan = %d", rec.Code)
	}
	<-entered

	// CSRF required.
	if rec := post(t, srv, "/library/scan/stop", url.Values{}, false); rec.Code != http.StatusForbidden {
		t.Fatalf("stop without CSRF = %d, want 403", rec.Code)
	}

	if rec := post(t, srv, "/library/scan/stop", url.Values{}, true); rec.Code != http.StatusOK {
		t.Fatalf("stop = %d", rec.Code)
	}
	term := pollScanUntilDone(t, srv)
	if !strings.Contains(term, "Scan stopped") {
		t.Errorf("stopped scan should read 'Scan stopped':\n%s", term)
	}
	if countScanPoll(term) != 0 {
		t.Errorf("stopped terminal must have no poller:\n%s", term)
	}

	// Idempotent: a second stop with nothing running is a harmless no-op (200).
	if rec := post(t, srv, "/library/scan/stop", url.Values{}, true); rec.Code != http.StatusOK {
		t.Fatalf("idempotent stop = %d", rec.Code)
	}
}

// TestScanStopIsTerminalWhileGoroutineStillRunning is the client-side stop-bug
// regression. stopScan cancels the scan's context but the worker goroutine only
// settles (running=false) once cancellation propagates — for a scan mid-way
// through hashing a multi-GB file that can be many seconds. In that window the OLD
// render (keyed off Running alone) re-emitted the scanning fragment WITH a fresh
// #scan-poll, so the poller kept the scanning view alive and Stop looked dead.
//
// A seam that blocks on a channel it never releases on ctx-cancel keeps
// running=true past Stop, reproducing that window. The fix keys the terminal
// branch off the synchronously-set Stopped flag, so BOTH the Stop response and any
// status poll in the window are the terminal, poller-less "Scan stopped" fragment.
func TestScanStopIsTerminalWhileGoroutineStillRunning(t *testing.T) {
	srv := newLibraryTestServer(t, t.TempDir())
	entered := make(chan struct{})
	release := make(chan struct{})
	srv.scanFn = func(ctx context.Context, onFile func(library.FileResult), onDiscovered func(int), onHashed func(int)) error {
		onFile(fileResult("a.safetensors", nil))
		close(entered)
		<-release // deliberately ignores ctx: the goroutine stays running past Stop
		return nil
	}

	if rec := post(t, srv, "/library/scan", url.Values{}, true); rec.Code != http.StatusOK {
		t.Fatalf("scan = %d", rec.Code)
	}
	<-entered

	// The Stop response itself must be the terminal (poller-less) fragment even
	// though the job goroutine is still running.
	rec := post(t, srv, "/library/scan/stop", url.Values{}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("stop = %d", rec.Code)
	}
	if !srv.scanJobState().Running {
		t.Fatal("test precondition: the scan goroutine should still be running (blocked on release)")
	}
	stopBody := rec.Body.String()
	if n := countScanPoll(stopBody); n != 0 {
		t.Errorf("stop response must carry NO #scan-poll while still running, got %d:\n%s", n, stopBody)
	}
	if !strings.Contains(stopBody, "Scan stopped") {
		t.Errorf("stop response must show 'Scan stopped':\n%s", stopBody)
	}
	if strings.Contains(stopBody, `hx-trigger="load delay:1s"`) {
		t.Errorf("stop response must not re-arm a load-delay poll trigger:\n%s", stopBody)
	}

	// A status poll in the SAME window (goroutine still running) must ALSO be
	// terminal — no race where a stale in-flight poll re-shows the scanning view.
	statusBody := get(t, srv, "/library/scan/status").Body.String()
	if n := countScanPoll(statusBody); n != 0 {
		t.Errorf("status poll after stop must carry NO #scan-poll while running, got %d:\n%s", n, statusBody)
	}
	if !strings.Contains(statusBody, "Scan stopped") {
		t.Errorf("status poll after stop must show 'Scan stopped':\n%s", statusBody)
	}

	close(release)
	pollScanUntilDone(t, srv)
}

// TestScanIdempotentSingleJob proves a second POST while a scan is running starts
// NO second goroutine: the scan seam is entered exactly once.
func TestScanIdempotentSingleJob(t *testing.T) {
	srv := newLibraryTestServer(t, t.TempDir())
	var calls int32
	release := make(chan struct{})
	srv.scanFn = func(ctx context.Context, onFile func(library.FileResult), onDiscovered func(int), onHashed func(int)) error {
		atomic.AddInt32(&calls, 1)
		<-release
		return nil
	}

	post(t, srv, "/library/scan", url.Values{}, true)
	post(t, srv, "/library/scan", url.Values{}, true) // idempotent re-click
	// Give the (single) goroutine a moment to enter the seam.
	deadline := time.Now().Add(time.Second)
	for atomic.LoadInt32(&calls) == 0 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Fatalf("expected exactly one scan goroutine, got %d", n)
	}
	close(release)
	pollScanUntilDone(t, srv)
}

// TestScanTabLandingBootstrapsScanningView proves finding #3's tab landing: while
// a scan runs, GET /library?tab=files renders the Model files tab in the scanning
// view (Stop CTA + streamed card + poller), bootstrapped from the live job.
func TestScanTabLandingBootstrapsScanningView(t *testing.T) {
	srv := newLibraryTestServer(t, t.TempDir())
	// Seed a selected dir so Tab B is not the empty state.
	if err := srv.store.AddScanDir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	// Emit the card, then keep running so the tab landing observes the scanning view.
	srv.scanFn = func(ctx context.Context, onFile func(library.FileResult), onDiscovered func(int), onHashed func(int)) error {
		onFile(fileResult("a.safetensors", intp(9)))
		<-release
		return nil
	}

	if rec := post(t, srv, "/library/scan", url.Values{}, true); rec.Code != http.StatusOK {
		t.Fatalf("scan = %d", rec.Code)
	}
	// Wait until the streamed card is visible in the job.
	deadline := time.Now().Add(time.Second)
	for srv.mustScanned(t) < 1 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}

	rec := get(t, srv, "/library?tab=files")
	body := rec.Body.String()
	for _, want := range []string{
		`href="/library?tab=files"`, // the Model files tab
		"Stop scanning",             // scanning view
		`id="scan-poll"`,            // the re-arming poller
		"Model #9", "a.safetensors", // the streamed card
	} {
		if !strings.Contains(body, want) {
			t.Errorf("tab landing during a scan should show the scanning view element %q:\n%s", want, body)
		}
	}
	close(release)
	pollScanUntilDone(t, srv)
}
