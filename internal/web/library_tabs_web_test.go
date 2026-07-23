package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/library"
)

// countPoller reports how many #discover-poll elements a fragment contains. The
// re-discover fix guarantees exactly one while scanning and zero when terminal —
// never a duplicate/orphan.
func countPoller(body string) int { return strings.Count(body, `id="discover-poll"`) }

// TestDefaultTabIsInstallDirectories proves the default (no ?tab) tab is
// "Install directories" (Tab A): the discovery UI renders and the model-scan
// control does not.
func TestDefaultTabIsInstallDirectories(t *testing.T) {
	out := renderString(t, libraryPage(buildLibraryView(nil), "csrf", true, nil, "dark", "", nil))
	if !strings.Contains(out, `href="/library?tab=sources"`) || !strings.Contains(out, `href="/library?tab=files"`) {
		t.Errorf("tab strip missing both tab links:\n%s", out)
	}
	// The active (sources) tab is the filled one.
	if !strings.Contains(out, `data-variant="filled" data-size="md" href="/library?tab=sources"`) {
		t.Errorf("default active tab should be Install directories (filled):\n%s", out)
	}
	if !strings.Contains(out, "Discover installs") {
		t.Error("default tab must render the discovery UI")
	}
	if strings.Contains(out, "Scan for model files") {
		t.Error("default tab must not render the model-scan control")
	}
}

// TestFilesTabEmptyStateWhenNoDirs proves Tab B shows a clear empty state (not a
// bare scan button) when no install directories have been selected yet.
func TestFilesTabEmptyStateWhenNoDirs(t *testing.T) {
	out := renderString(t, libraryPage(buildLibraryView(nil), "csrf", true, nil, "dark", "files", nil))
	if !strings.Contains(out, "Add install directories first") {
		t.Errorf("files tab with no dirs should show the empty state:\n%s", out)
	}
	if strings.Contains(out, "Scan for model files") {
		t.Error("files-tab empty state must not render a bare scan button")
	}
	// And it never renders discovery UI (that is Tab A's job).
	if strings.Contains(out, "Discover installs") {
		t.Error("files tab must not render discovery UI")
	}
}

// TestScanningViewHidesControlsAndStreamsInCard proves finding #3's running
// view: discoverScanning renders a large PRIMARY Stop CTA, the "found N" copy,
// and the installs streaming INSIDE the card, while OMITTING the idle controls
// (discover button / manual input / browser). The terminal view restores them.
func TestScanningViewHidesControlsAndStreamsInCard(t *testing.T) {
	install := library.Install{
		Path: "/opt/ComfyUI", Kind: library.KindComfyUI,
		Confidence: library.ConfidenceHigh, ModelDirs: []string{"checkpoints"},
	}

	scan := renderString(t, discoverScanning([]library.Install{install}, nil, "csrf"))
	for _, want := range []string{
		"Stop scanning", `data-variant="filled"`, `data-size="lg"`, // big primary CTA
		"found 1 so far",
		install.Path, "/library/scan-dirs/add", // the install streamed in the card with an Add
	} {
		if !strings.Contains(scan, want) {
			t.Errorf("scanning view missing %q:\n%s", want, scan)
		}
	}
	for _, gone := range []string{"Discover installs", "Add a directory by path", "Browse server directories"} {
		if strings.Contains(scan, gone) {
			t.Errorf("scanning view must hide idle control %q", gone)
		}
	}
	if n := countPoller(scan); n != 1 {
		t.Errorf("scanning view must have exactly one poller, got %d:\n%s", n, scan)
	}

	// Terminal view (user-stopped): restores the controls, states the outcome, no poller.
	term := renderString(t, discoverResults([]library.Install{install}, nil, true, nil, "csrf"))
	for _, want := range []string{
		"Discover installs", "Add a directory by path", "Browse server directories",
		"Scan stopped — found 1",
	} {
		if !strings.Contains(term, want) {
			t.Errorf("terminal view missing restored control/message %q:\n%s", want, term)
		}
	}
	if n := countPoller(term); n != 0 {
		t.Errorf("terminal view must have zero pollers, got %d:\n%s", n, term)
	}
}

// TestRediscoverAfterStopRestartsPolling is the markup-level guard for the
// re-discover-after-stop bug (finding #4). It drives the exact failing sequence
// TWICE at the handler level: Discover → (running, exactly one stable-target
// poller) → Stop → (terminal, zero orphan #discover-poll). The SECOND pass —
// re-discovering after a stop — must restart polling exactly like the first,
// which is what the old outerHTML-self-replace poller failed to do. The browser
// harness provides the definitive end-to-end proof.
func TestRediscoverAfterStopRestartsPolling(t *testing.T) {
	srv := newLibraryTestServer(t, t.TempDir())
	srv.discoverRoots = []string{t.TempDir()}
	install := library.Install{Path: "/home/u/ComfyUI", Kind: library.KindComfyUI, Confidence: library.ConfidenceHigh}

	entered := make(chan struct{}, 4)
	srv.crawlFn = func(ctx context.Context, _ []string, opts library.DiscoverOptions) ([]library.Install, error) {
		if opts.OnInstall != nil {
			opts.OnInstall(install)
		}
		entered <- struct{}{}
		<-ctx.Done() // stay running until Stop cancels the context
		return []library.Install{install}, ctx.Err()
	}

	runOnce := func(pass int) {
		rec := post(t, srv, "/library/discover", url.Values{}, true)
		body := rec.Body.String()
		if !hasPoller(body) {
			t.Fatalf("pass %d: discover POST must return a scanning fragment with the poller:\n%s", pass, body)
		}
		if n := countPoller(body); n != 1 {
			t.Fatalf("pass %d: scanning fragment must have exactly ONE poller, got %d:\n%s", pass, n, body)
		}
		if strings.Contains(body, `hx-swap="outerHTML"`) {
			t.Fatalf("pass %d: poller must target the stable container, never self-replace via outerHTML:\n%s", pass, body)
		}
		if !strings.Contains(body, `hx-target="#discover-results"`) {
			t.Fatalf("pass %d: poller must target the STABLE #discover-results container:\n%s", pass, body)
		}
		<-entered // the crawl is running

		if !hasPoller(get(t, srv, "/library/discover/status").Body.String()) {
			t.Fatalf("pass %d: a status poll while running must keep polling", pass)
		}

		if rec := post(t, srv, "/library/discover/stop", url.Values{}, true); rec.Code != http.StatusOK {
			t.Fatalf("pass %d: stop = %d", pass, rec.Code)
		}
		term := pollDiscoverUntilDone(t, srv)
		if n := countPoller(term); n != 0 {
			t.Fatalf("pass %d: terminal fragment must leave NO orphan #discover-poll, got %d:\n%s", pass, n, term)
		}
	}

	runOnce(1)
	runOnce(2) // the bug: the second Discover must restart polling just like the first
}
