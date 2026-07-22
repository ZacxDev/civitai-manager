package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/library"
)

// blockingCrawl returns a crawlFn seam that signals when the crawl is entered
// (via started) and blocks until release is closed, then returns the given
// result. It lets a test observe the "job running while the request already
// returned" window deterministically, without touching the real filesystem.
func blockingCrawl(started chan<- struct{}, release <-chan struct{}, out []library.Install, err error) func(context.Context, []string, library.DiscoverOptions) ([]library.Install, error) {
	return func(ctx context.Context, roots []string, opts library.DiscoverOptions) ([]library.Install, error) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-release
		return out, err
	}
}

// TestDiscoverPostReturnsScanningFragment proves the POST returns immediately
// with the scanning fragment: the poller markup and the scanning copy, without
// waiting on the crawl.
func TestDiscoverPostReturnsScanningFragment(t *testing.T) {
	srv := newLibraryTestServer(t, t.TempDir())
	srv.discoverRoots = []string{t.TempDir()} // never the real $HOME
	release := make(chan struct{})
	defer close(release)
	srv.crawlFn = blockingCrawl(make(chan struct{}, 1), release, nil, nil)

	rec := post(t, srv, "/library/discover", url.Values{}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("discover = %d", rec.Code)
	}
	body := rec.Body.String()
	if !hasPoller(body) {
		t.Fatalf("POST must return the scanning fragment with the poller, got:\n%s", body)
	}
	for _, want := range []string{
		`id="discover-poll"`,
		"Scanning your system for ComfyUI / Automatic1111 installs",
		"large home dirs can take ~30s",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("scanning fragment missing %q in:\n%s", want, body)
		}
	}
}

// TestDiscoverJobIndependentOfRequestContext proves the crawl runs on a
// background context, not the request context: the initiating POST returns
// (with the job STILL running), then the job completes and populates results,
// which a subsequent poll observes. This is the core of the async fix.
func TestDiscoverJobIndependentOfRequestContext(t *testing.T) {
	srv := newLibraryTestServer(t, t.TempDir())
	srv.discoverRoots = []string{t.TempDir()}
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	install := library.Install{
		Path: "/home/u/ComfyUI", Kind: library.KindComfyUI, Confidence: library.ConfidenceHigh,
	}
	srv.crawlFn = blockingCrawl(started, release, []library.Install{install}, nil)

	// The POST returns while the crawl is still blocked → the request finished
	// before the job did.
	rec := post(t, srv, "/library/discover", url.Values{}, true)
	if rec.Code != http.StatusOK || !hasPoller(rec.Body.String()) {
		t.Fatalf("POST should return the scanning fragment, got %d:\n%s", rec.Code, rec.Body.String())
	}
	<-started // the crawl goroutine has entered crawlFn and is blocked on release

	// With the request already returned and the crawl still blocked, the job must
	// report running — proving the crawl outlives the request.
	if _, running, _, _, _ := srv.discoverJobState(); !running {
		t.Fatalf("job should still be running after the POST returned")
	}
	// A status poll during this window keeps polling (scanning fragment).
	if body := get(t, srv, "/library/discover/status").Body.String(); !hasPoller(body) {
		t.Fatalf("status while running must return the poller, got:\n%s", body)
	}

	// Let the background job finish; the result must land even though the request
	// that triggered it is long gone.
	close(release)
	body := pollDiscoverUntilDone(t, srv)
	if !strings.Contains(body, install.Path) || !strings.Contains(body, "/library/scan-dirs/add") {
		t.Fatalf("completed job should render the install with an Add control, got:\n%s", body)
	}
}

// TestDiscoverStatusEmptyTree proves a completed crawl that found nothing renders
// the plain "no installs" copy and stops polling (no poller).
func TestDiscoverStatusEmptyTree(t *testing.T) {
	srv := newLibraryTestServer(t, t.TempDir())
	srv.discoverRoots = []string{t.TempDir()} // real crawl over an empty tree

	if rec := post(t, srv, "/library/discover", url.Values{}, true); rec.Code != http.StatusOK {
		t.Fatalf("discover = %d", rec.Code)
	}
	body := pollDiscoverUntilDone(t, srv)
	if !strings.Contains(body, "No ComfyUI or Automatic1111/Forge installs found") {
		t.Errorf("empty tree should render the no-installs copy, got:\n%s", body)
	}
	if hasPoller(body) {
		t.Errorf("terminal fragment must not include the poller, got:\n%s", body)
	}
}

// TestDiscoverStatusTruncated proves a budget-truncated crawl (crawlFn returns
// partial installs + a deadline error) renders the partial install AND the
// "stopped after Ns" note keyed to the job budget.
func TestDiscoverStatusTruncated(t *testing.T) {
	srv := newLibraryTestServer(t, t.TempDir())
	srv.discoverRoots = []string{t.TempDir()}
	install := library.Install{
		Path: "/home/u/ComfyUI", Kind: library.KindComfyUI, Confidence: library.ConfidenceHigh,
	}
	srv.crawlFn = func(context.Context, []string, library.DiscoverOptions) ([]library.Install, error) {
		return []library.Install{install}, context.DeadlineExceeded
	}

	if rec := post(t, srv, "/library/discover", url.Values{}, true); rec.Code != http.StatusOK {
		t.Fatalf("discover = %d", rec.Code)
	}
	body := pollDiscoverUntilDone(t, srv)
	for _, want := range []string{install.Path, "Stopped after 30s", "add a path manually"} {
		if !strings.Contains(body, want) {
			t.Errorf("truncated result missing %q in:\n%s", want, body)
		}
	}
	if hasPoller(body) {
		t.Errorf("terminal (truncated) fragment must not include the poller, got:\n%s", body)
	}
}

// TestDiscoverIdempotentSingleJob proves a second POST while a crawl is running
// starts NO second goroutine: the crawl seam is entered exactly once, and both
// responses are scanning fragments.
func TestDiscoverIdempotentSingleJob(t *testing.T) {
	srv := newLibraryTestServer(t, t.TempDir())
	srv.discoverRoots = []string{t.TempDir()}
	var calls int32
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	srv.crawlFn = func(ctx context.Context, roots []string, opts library.DiscoverOptions) ([]library.Install, error) {
		atomic.AddInt32(&calls, 1)
		select {
		case started <- struct{}{}:
		default:
		}
		<-release
		return nil, nil
	}

	rec1 := post(t, srv, "/library/discover", url.Values{}, true)
	rec2 := post(t, srv, "/library/discover", url.Values{}, true) // idempotent re-click
	for i, rec := range []*httptest.ResponseRecorder{rec1, rec2} {
		if rec.Code != http.StatusOK || !hasPoller(rec.Body.String()) {
			t.Fatalf("POST #%d should return a scanning fragment, got %d:\n%s", i+1, rec.Code, rec.Body.String())
		}
	}
	<-started // the single crawl actually began
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Fatalf("expected exactly one crawl goroutine, got %d", n)
	}
	close(release)
	pollDiscoverUntilDone(t, srv)
}

// TestDiscoverShutdownCancelsCrawl proves cancelling the server base context
// cancels an in-flight crawl: the crawl goroutine returns (no leak) and the job
// transitions to a finished, truncated state.
func TestDiscoverShutdownCancelsCrawl(t *testing.T) {
	srv := newLibraryTestServer(t, t.TempDir())
	srv.discoverRoots = []string{t.TempDir()}
	base, cancelBase := context.WithCancel(context.Background())
	srv.SetBaseContext(base)

	entered := make(chan struct{})
	srv.crawlFn = func(ctx context.Context, roots []string, opts library.DiscoverOptions) ([]library.Install, error) {
		close(entered)
		<-ctx.Done() // block until the server base ctx cancels the crawl
		return nil, ctx.Err()
	}

	if rec := post(t, srv, "/library/discover", url.Values{}, true); rec.Code != http.StatusOK {
		t.Fatalf("discover = %d", rec.Code)
	}
	<-entered // crawl is running, blocked on ctx.Done

	cancelBase() // simulate server shutdown

	// The crawl goroutine must return; the job settles to finished + truncated.
	deadline := time.Now().Add(2 * time.Second)
	for {
		_, running, _, truncated, err := srv.discoverJobState()
		if !running {
			if err == nil {
				t.Fatalf("cancelled crawl should record a context error")
			}
			if !truncated {
				t.Fatalf("cancelled crawl should mark the job truncated")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("crawl goroutine did not return after base ctx cancel (leak)")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
