package web

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/library"
)

// The "Model files" tab focuses the live scan progress: while a model-file scan
// runs, the scan FORM is hidden and the progress fragment IS the main content of
// the stable #scan-results container; when idle (never scanned) or terminal (scan
// finished) the form is visible again ABOVE the results. Because the form now lives
// INSIDE the swapped #scan-results body, each /library/scan/status poll swap hides
// the form while running and restores it when terminal — automatically.
//
// scanFormMarker is the form's distinctive submit button; its ABSENCE proves the
// form is hidden (the Tab-A CTA reads "Scan for models", so this exact string is
// unique to the Tab-B form).
const scanFormMarker = "Scan for model files"

// waitScanScanned blocks until the scan job has recorded >= n scanned files (so the
// running snapshot is observable), failing on timeout.
func waitScanScanned(t *testing.T, srv *Server, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for srv.scanJobState().Scanned < n {
		if time.Now().After(deadline) {
			t.Fatalf("scan did not reach %d scanned in time", n)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// runningScanSrv starts a scan that emits one card then blocks until the returned
// release func is called, leaving the job in the running state with a selected dir
// so Tab B is not the empty state.
func runningScanSrv(t *testing.T) (srv *Server, release func()) {
	t.Helper()
	srv = newLibraryTestServer(t, t.TempDir())
	if err := srv.store.AddScanDir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	rel := make(chan struct{})
	srv.scanFn = func(ctx context.Context, onFile func(library.FileResult), onDiscovered func(int), onHashed func(int)) error {
		onDiscovered(1)
		onFile(fileResult("a.safetensors", intp(9)))
		<-rel
		return nil
	}
	if rec := post(t, srv, "/library/scan", url.Values{}, true); rec.Code != http.StatusOK {
		t.Fatalf("scan = %d", rec.Code)
	}
	waitScanScanned(t, srv, 1)
	return srv, func() { close(rel) }
}

// TestFilesTabRunningHidesForm proves that WHILE a scan runs, the Model files tab
// (full page) and the status poll show the progress fragment as the main content —
// the stable #scan-results container with the re-arming poller and the Stop button
// — and do NOT render the scan form.
func TestFilesTabRunningHidesForm(t *testing.T) {
	srv, release := runningScanSrv(t)
	defer func() { release(); pollScanUntilDone(t, srv) }()

	// Full page during the scan: progress is the main content, no form.
	page := get(t, srv, "/library?tab=files").Body.String()
	if !strings.Contains(page, `id="scan-results"`) {
		t.Errorf("running page must keep the stable #scan-results container:\n%s", page)
	}
	for _, want := range []string{`id="scan-poll"`, "Stop scanning"} {
		if !strings.Contains(page, want) {
			t.Errorf("running page missing progress marker %q:\n%s", want, page)
		}
	}
	if strings.Contains(page, scanFormMarker) {
		t.Errorf("running page must HIDE the scan form (%q present):\n%s", scanFormMarker, page)
	}

	// The status-poll swap body (the #scan-results innerHTML) is likewise form-less.
	status := get(t, srv, "/library/scan/status").Body.String()
	if !hasScanPoller(status) || !strings.Contains(status, "Stop scanning") {
		t.Errorf("running status poll must be the scanning fragment:\n%s", status)
	}
	if strings.Contains(status, scanFormMarker) {
		t.Errorf("running status poll must NOT contain the scan form (%q):\n%s", scanFormMarker, status)
	}
}

// TestFilesTabIdleShowsForm proves the never-scanned (idle) Model files tab renders
// the scan form inside the stable #scan-results container, with NO poller.
func TestFilesTabIdleShowsForm(t *testing.T) {
	srv := newLibraryTestServer(t, t.TempDir())
	if err := srv.store.AddScanDir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	page := get(t, srv, "/library?tab=files").Body.String()
	if !strings.Contains(page, `id="scan-results"`) {
		t.Errorf("idle page must have the stable #scan-results container:\n%s", page)
	}
	if !strings.Contains(page, scanFormMarker) {
		t.Errorf("idle page must SHOW the scan form (%q):\n%s", scanFormMarker, page)
	}
	if strings.Contains(page, `id="scan-poll"`) {
		t.Errorf("idle page must NOT carry a poller:\n%s", page)
	}
}

// TestFilesTabTerminalShowsFormAndResults proves that once a scan finishes, the
// Model files tab restores the scan form ABOVE the terminal results (Summary +
// "Scan result" status), inside the stable #scan-results container, with NO poller.
func TestFilesTabTerminalShowsFormAndResults(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "m0.safetensors"), []byte("weights"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := newLibraryTestServer(t, root)
	if err := srv.store.AddScanDir(root); err != nil {
		t.Fatal(err)
	}
	// Offline so the real scanner makes no CivitAI calls.
	if err := srv.store.SetSetting(matchRemoteSettingKey, "false"); err != nil {
		t.Fatal(err)
	}
	if rec := post(t, srv, "/library/scan", url.Values{}, true); rec.Code != http.StatusOK {
		t.Fatalf("scan = %d", rec.Code)
	}

	// The terminal status-poll body restores the form ABOVE the results.
	term := pollScanUntilDone(t, srv)
	for _, want := range []string{scanFormMarker, "Scan result", "Summary"} {
		if !strings.Contains(term, want) {
			t.Errorf("terminal status body missing %q:\n%s", want, term)
		}
	}
	if strings.Contains(term, `id="scan-poll"`) {
		t.Errorf("terminal status body must have NO poller:\n%s", term)
	}

	// The terminal full-page bootstrap agrees: form + results in the stable container.
	page := get(t, srv, "/library?tab=files").Body.String()
	for _, want := range []string{`id="scan-results"`, scanFormMarker, "Scan result", "Summary"} {
		if !strings.Contains(page, want) {
			t.Errorf("terminal page missing %q:\n%s", want, page)
		}
	}
	if strings.Contains(page, `id="scan-poll"`) {
		t.Errorf("terminal page must have NO poller:\n%s", page)
	}
}

// TestFilesTabStableContainerAcrossStates proves the #scan-results container is the
// stable element in ALL THREE states (idle / running / terminal) and the poller
// node appears ONLY while running — so no state self-replaces the polling
// container, preserving the race-safe streaming invariant.
func TestFilesTabStableContainerAcrossStates(t *testing.T) {
	// Idle.
	idleSrv := newLibraryTestServer(t, t.TempDir())
	if err := idleSrv.store.AddScanDir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	idle := get(t, idleSrv, "/library?tab=files").Body.String()

	// Running.
	runSrv, release := runningScanSrv(t)
	running := get(t, runSrv, "/library?tab=files").Body.String()
	release()
	pollScanUntilDone(t, runSrv)

	// Terminal.
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "m0.safetensors"), []byte("weights"), 0o644); err != nil {
		t.Fatal(err)
	}
	termSrv := newLibraryTestServer(t, root)
	if err := termSrv.store.AddScanDir(root); err != nil {
		t.Fatal(err)
	}
	if err := termSrv.store.SetSetting(matchRemoteSettingKey, "false"); err != nil {
		t.Fatal(err)
	}
	if rec := post(t, termSrv, "/library/scan", url.Values{}, true); rec.Code != http.StatusOK {
		t.Fatalf("scan = %d", rec.Code)
	}
	pollScanUntilDone(t, termSrv)
	terminal := get(t, termSrv, "/library?tab=files").Body.String()

	states := map[string]struct {
		body       string
		wantPoller bool
	}{
		"idle":     {idle, false},
		"running":  {running, true},
		"terminal": {terminal, false},
	}
	for name, st := range states {
		if c := strings.Count(st.body, `id="scan-results"`); c != 1 {
			t.Errorf("%s state must have exactly one stable #scan-results container, got %d:\n%s", name, c, st.body)
		}
		poller := strings.Contains(st.body, `id="scan-poll"`)
		if poller != st.wantPoller {
			t.Errorf("%s state poller presence = %v, want %v", name, poller, st.wantPoller)
		}
	}
	// Sanity: the running state must never self-replace #scan-results via outerHTML.
	if strings.Contains(running, `hx-swap="outerHTML"`) && strings.Contains(running, `hx-target="#scan-results"`) {
		t.Errorf("running fragment must swap #scan-results innerHTML, never outerHTML:\n%s", running)
	}
}
