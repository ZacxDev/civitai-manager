package web

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/library"
)

// installsInBody reports how many of paths appear in the rendered fragment.
func installsInBody(body string, paths []string) int {
	n := 0
	for _, p := range paths {
		if strings.Contains(body, p) {
			n++
		}
	}
	return n
}

// TestDiscoverStreamsIncrementally proves installs surface as they are found, not
// only at the end: the crawl emits three installs one at a time (gated by the
// test), and a /status poll after each emission shows the list GROWING (1, then
// 2, then 3), then the terminal fragment shows all three with "Scan complete".
func TestDiscoverStreamsIncrementally(t *testing.T) {
	srv := newLibraryTestServer(t, t.TempDir())
	srv.discoverRoots = []string{t.TempDir()}

	installs := []library.Install{
		{Path: "/disk1/ComfyUI", Kind: library.KindComfyUI, Confidence: library.ConfidenceLow},
		{Path: "/disk2/ComfyUI", Kind: library.KindComfyUI, Confidence: library.ConfidenceLow},
		{Path: "/disk3/webui", Kind: library.KindA1111, Confidence: library.ConfidenceLow},
	}
	paths := []string{installs[0].Path, installs[1].Path, installs[2].Path}

	gate := make(chan struct{}) // test → crawl: "emit the next install"
	emitted := make(chan int)   // crawl → test: "install N appended"
	hold := make(chan struct{}) // test → crawl: "you may now finish"
	srv.crawlFn = func(ctx context.Context, _ []string, opts library.DiscoverOptions) ([]library.Install, error) {
		for i, in := range installs {
			select {
			case <-gate:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			if opts.OnInstall != nil {
				opts.OnInstall(in) // appends under discoverMu; happens-before the send
			}
			emitted <- i + 1
		}
		<-hold // stay running so the test can observe the full streamed list
		return installs, nil
	}

	if rec := post(t, srv, "/library/discover", url.Values{}, true); rec.Code != http.StatusOK {
		t.Fatalf("discover = %d", rec.Code)
	}

	// Before any emission: scanning, zero installs.
	body := get(t, srv, "/library/discover/status").Body.String()
	if !hasPoller(body) {
		t.Fatalf("should still be scanning before any emission:\n%s", body)
	}
	if got := installsInBody(body, paths); got != 0 {
		t.Fatalf("expected 0 installs before any emission, got %d:\n%s", got, body)
	}

	// Emit one at a time; after each, the poll shows exactly that many installs.
	for want := 1; want <= len(installs); want++ {
		gate <- struct{}{}
		if n := <-emitted; n != want {
			t.Fatalf("crawl reported %d emitted, want %d", n, want)
		}
		body = get(t, srv, "/library/discover/status").Body.String()
		if !hasPoller(body) {
			t.Fatalf("still scanning while emitting (want %d): fragment lost its poller:\n%s", want, body)
		}
		if got := installsInBody(body, paths); got != want {
			t.Fatalf("after %d emissions the poll should show %d installs, got %d:\n%s", want, want, got, body)
		}
	}

	// Let the crawl finish → terminal fragment lists all three, "Scan complete".
	close(hold)
	body = pollDiscoverUntilDone(t, srv)
	if got := installsInBody(body, paths); got != len(installs) {
		t.Fatalf("terminal fragment should list all %d installs, got %d:\n%s", len(installs), got, body)
	}
	if !strings.Contains(body, "Scan complete — found 3") {
		t.Errorf("exhausted crawl should say 'Scan complete — found 3':\n%s", body)
	}
}

// TestDiscoverStatusRaceUnderConcurrentAppendAndPoll drives the crawl to append
// installs while several goroutines poll /status concurrently. Under `go test
// -race` it proves the snapshot-under-lock guard: no data race on job.installs
// and no torn/duplicated result (the final list is exactly the appended set).
func TestDiscoverStatusRaceUnderConcurrentAppendAndPoll(t *testing.T) {
	srv := newLibraryTestServer(t, t.TempDir())
	srv.discoverRoots = []string{t.TempDir()}

	const n = 200
	srv.crawlFn = func(_ context.Context, _ []string, opts library.DiscoverOptions) ([]library.Install, error) {
		for i := 0; i < n; i++ {
			if opts.OnInstall != nil {
				opts.OnInstall(library.Install{
					Path: fmt.Sprintf("/race/i%04d/ComfyUI", i), Kind: library.KindComfyUI,
					Confidence: library.ConfidenceLow,
				})
			}
			time.Sleep(50 * time.Microsecond) // keep appends in flight while pollers hammer
		}
		return nil, nil
	}

	if rec := post(t, srv, "/library/discover", url.Values{}, true); rec.Code != http.StatusOK {
		t.Fatalf("discover = %d", rec.Code)
	}

	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			deadline := time.Now().Add(5 * time.Second)
			for {
				if !hasPoller(get(t, srv, "/library/discover/status").Body.String()) {
					return // job settled: stop polling
				}
				if time.Now().After(deadline) {
					return
				}
			}
		}()
	}
	wg.Wait()

	// The final snapshot holds exactly the appended set — no torn/duplicated slice.
	_, running, installs, _, _ := srv.discoverJobState()
	if running {
		t.Fatalf("crawl should have settled")
	}
	if len(installs) != n {
		t.Fatalf("want %d streamed installs, got %d", n, len(installs))
	}
	seen := map[string]bool{}
	for _, in := range installs {
		if seen[in.Path] {
			t.Fatalf("duplicated install in streamed result: %s", in.Path)
		}
		seen[in.Path] = true
	}
}

// TestDiscoverStopCancelsRunningJob proves POST /library/discover/stop cancels a
// running crawl: the crawl settles, /status shows "Scan stopped — found N"
// WITHOUT the poller, and the job records stopped=true.
func TestDiscoverStopCancelsRunningJob(t *testing.T) {
	srv := newLibraryTestServer(t, t.TempDir())
	srv.discoverRoots = []string{t.TempDir()}

	install := library.Install{Path: "/home/u/ComfyUI", Kind: library.KindComfyUI, Confidence: library.ConfidenceHigh}
	entered := make(chan struct{})
	srv.crawlFn = func(ctx context.Context, _ []string, opts library.DiscoverOptions) ([]library.Install, error) {
		if opts.OnInstall != nil {
			opts.OnInstall(install)
		}
		close(entered)
		<-ctx.Done() // block until Stop cancels the context
		return []library.Install{install}, ctx.Err()
	}

	if rec := post(t, srv, "/library/discover", url.Values{}, true); rec.Code != http.StatusOK {
		t.Fatalf("discover = %d", rec.Code)
	}
	<-entered // crawl running with one streamed install, blocked on ctx.Done

	if rec := post(t, srv, "/library/discover/stop", url.Values{}, true); rec.Code != http.StatusOK {
		t.Fatalf("stop = %d:\n%s", rec.Code, rec.Body.String())
	}

	body := pollDiscoverUntilDone(t, srv)
	if !strings.Contains(body, "Scan stopped — found 1") {
		t.Errorf("stopped crawl should say 'Scan stopped — found 1':\n%s", body)
	}
	if !strings.Contains(body, install.Path) {
		t.Errorf("stopped fragment should still show the found install:\n%s", body)
	}
	if hasPoller(body) {
		t.Errorf("terminal (stopped) fragment must not include the poller:\n%s", body)
	}
	if _, running, _, stopped, _ := srv.discoverJobState(); running || !stopped {
		t.Errorf("after stop, job should be settled+stopped (running=%v stopped=%v)", running, stopped)
	}
}

// TestDiscoverStopNoJobIsNoOp proves stopping when nothing runs is a harmless
// no-op: it creates no job and returns the terminal (poller-less) fragment.
func TestDiscoverStopNoJobIsNoOp(t *testing.T) {
	srv := newLibraryTestServer(t, t.TempDir())
	srv.discoverRoots = []string{t.TempDir()}

	rec := post(t, srv, "/library/discover/stop", url.Values{}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("stop with no job = %d", rec.Code)
	}
	if hasPoller(rec.Body.String()) {
		t.Errorf("stop with no job must not start a poller:\n%s", rec.Body.String())
	}
	if started, _, _, _, _ := srv.discoverJobState(); started {
		t.Errorf("stop with no job must not create a job")
	}
}

// TestDiscoverStopRequiresCSRF proves the stop endpoint rejects a missing token.
func TestDiscoverStopRequiresCSRF(t *testing.T) {
	srv := newLibraryTestServer(t, t.TempDir())
	srv.discoverRoots = []string{t.TempDir()}
	if rec := post(t, srv, "/library/discover/stop", url.Values{}, false); rec.Code != http.StatusForbidden {
		t.Fatalf("stop without CSRF = %d, want 403", rec.Code)
	}
}

// TestDiscoverAddPromptsToStopWhileRunning proves an install's Add control carries
// the "stop the scan?" prompt WHILE a scan is running (the user likely found what
// they came for), and carries NO prompt in the terminal fragment (Add is silent).
func TestDiscoverAddPromptsToStopWhileRunning(t *testing.T) {
	install := library.Install{Path: "/home/u/ComfyUI", Kind: library.KindComfyUI, Confidence: library.ConfidenceHigh}

	// While scanning: the Add control prompts to stop the still-running scan.
	running := renderString(t, discoverScanning([]library.Install{install}, nil, "csrf"))
	if !strings.Contains(running, "/library/scan-dirs/add") {
		t.Fatalf("scanning fragment should render an Add control:\n%s", running)
	}
	if !strings.Contains(running, `hx-confirm=`) {
		t.Errorf("scanning-time Add should carry an hx-confirm prompt:\n%s", running)
	}
	if !strings.Contains(running, "the background scan is still running") {
		t.Errorf("scanning-time Add prompt should mention the running scan:\n%s", running)
	}

	// Terminal (not running): Add has no stop prompt.
	terminal := renderString(t, discoverResults([]library.Install{install}, nil, false, nil, "csrf"))
	if !strings.Contains(terminal, "/library/scan-dirs/add") {
		t.Fatalf("terminal fragment should render an Add control:\n%s", terminal)
	}
	if strings.Contains(terminal, "hx-confirm") {
		t.Errorf("terminal Add must NOT carry a stop prompt (no scan runs):\n%s", terminal)
	}
}
