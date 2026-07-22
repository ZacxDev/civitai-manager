package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/library"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// buildComfyFixture creates a ComfyUI-shaped install under a fresh temp dir and
// returns the crawl root plus the install path.
func buildComfyFixture(t *testing.T) (root, install string) {
	t.Helper()
	root = t.TempDir()
	install = filepath.Join(root, "ComfyUI")
	for _, d := range []string{
		filepath.Join(install, "models", "checkpoints"),
		filepath.Join(install, "models", "loras"),
	} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(install, "main.py"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root, install
}

func post(t *testing.T, srv *Server, path string, form url.Values, withCSRF bool) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if withCSRF {
		req.Header.Set("X-CSRF-Token", srv.csrf)
	}
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

// get issues a GET (no CSRF) against the server handler.
func get(t *testing.T, srv *Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

// discoverPollerMarkup is the set of htmx attributes that MUST be present in a
// scanning fragment for the client to keep polling; their ABSENCE marks a
// terminal (done) fragment.
var discoverPollerMarkup = []string{
	`hx-get="/library/discover/status"`,
	`hx-trigger="every 1s"`,
	`hx-swap="outerHTML"`,
}

// hasPoller reports whether body is a scanning fragment (still polling).
func hasPoller(body string) bool {
	for _, want := range discoverPollerMarkup {
		if !strings.Contains(body, want) {
			return false
		}
	}
	return true
}

// pollDiscoverUntilDone polls the status endpoint until it returns a terminal
// (poller-less) fragment, then returns that body. It fails the test if the job
// does not finish within a generous deadline.
func pollDiscoverUntilDone(t *testing.T, srv *Server) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		rec := get(t, srv, "/library/discover/status")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
		body := rec.Body.String()
		if !hasPoller(body) {
			return body // terminal fragment
		}
		if time.Now().After(deadline) {
			t.Fatalf("discovery did not finish before deadline; last body:\n%s", body)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestDiscoverEndpointRendersCandidates proves the async flow end to end: the
// POST returns immediately with the scanning fragment (WITH the poller), and
// polling the status endpoint eventually yields the candidate with its Add
// control (WITHOUT the poller).
//
// (Adapted from the pre-async test, which asserted the POST returned the
// candidates directly on the request thread — discovery is now a background job.)
func TestDiscoverEndpointRendersCandidates(t *testing.T) {
	root, install := buildComfyFixture(t)
	srv := newLibraryTestServer(t, t.TempDir())
	srv.discoverRoots = []string{root}

	// Without a CSRF token → 403.
	rec := post(t, srv, "/library/discover", url.Values{}, false)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("discover without CSRF = %d, want 403", rec.Code)
	}

	// With the token → returns the scanning fragment immediately (with the poller).
	rec = post(t, srv, "/library/discover", url.Values{}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("discover = %d", rec.Code)
	}
	if !hasPoller(rec.Body.String()) {
		t.Fatalf("POST should return the scanning fragment with the poller, got:\n%s", rec.Body.String())
	}

	// Poll to completion → the candidate is rendered with an Add control.
	body := pollDiscoverUntilDone(t, srv)
	for _, want := range []string{install, "ComfyUI", "/library/scan-dirs/add", "Add", "confidence"} {
		if !strings.Contains(body, want) {
			t.Errorf("discover results missing %q in:\n%s", want, body)
		}
	}
}

// TestDiscoverDedupesAgainstModelRoot proves an install equal to model_root is
// de-duped from the results. (Adapted for the async flow: poll to done.)
func TestDiscoverDedupesAgainstModelRoot(t *testing.T) {
	root, install := buildComfyFixture(t)
	// Point model_root AT the discovered install so it is de-duped away.
	srv := newLibraryTestServer(t, install)
	srv.discoverRoots = []string{root}

	if rec := post(t, srv, "/library/discover", url.Values{}, true); rec.Code != http.StatusOK {
		t.Fatalf("discover = %d", rec.Code)
	}
	body := pollDiscoverUntilDone(t, srv)
	if strings.Contains(body, "/library/scan-dirs/add") {
		t.Errorf("install equal to model_root should be de-duped, got:\n%s", body)
	}
}

func TestBrowseEndpointListsSubdirsAndRefusesSystemDir(t *testing.T) {
	base := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, "childdir"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Point model_root at base so it is within the browse allowlist (the browser
	// is constrained to $HOME ∪ model_root ∪ library_paths — see
	// TestBrowseConstrainedToAllowedRoots).
	srv := newLibraryTestServer(t, base)

	// Lists immediate subdirs.
	rec := post(t, srv, "/library/browse", url.Values{"path": {base}}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("browse = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "childdir") {
		t.Errorf("browse should list childdir, got:\n%s", rec.Body.String())
	}

	// Refuses a system directory.
	rec = post(t, srv, "/library/browse", url.Values{"path": {"/etc"}}, true)
	if !strings.Contains(rec.Body.String(), "Refusing to browse a system directory") {
		t.Errorf("browse should refuse /etc, got:\n%s", rec.Body.String())
	}

	// CSRF required.
	rec = post(t, srv, "/library/browse", url.Values{"path": {base}}, false)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("browse without CSRF = %d, want 403", rec.Code)
	}
}

// TestBrowseConstrainedToAllowedRoots proves the interactive directory browser
// is bounded to $HOME ∪ model_root ∪ library_paths: a subdir of model_root is
// browsable, an unrelated top-level dir is refused, and a symlink escaping an
// allowed dir to an outside dir is refused on the resolved real path.
func TestBrowseConstrainedToAllowedRoots(t *testing.T) {
	root := t.TempDir() // model_root
	child := filepath.Join(root, "sub")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	srv := newLibraryTestServer(t, root)

	// In-scope: a subdir of model_root is browsable.
	rec := post(t, srv, "/library/browse", url.Values{"path": {child}}, true)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "Refusing to browse outside") {
		t.Fatalf("subdir of model_root should be browsable, got %d:\n%s", rec.Code, rec.Body.String())
	}

	// Out-of-scope: an unrelated top-level dir (exists, not under HOME/model_root).
	outside := t.TempDir()
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		resolved := outside
		if r, err := filepath.EvalSymlinks(outside); err == nil {
			resolved = r
		}
		if hr, err := filepath.EvalSymlinks(home); err == nil {
			home = hr
		}
		if strings.HasPrefix(resolved+string(filepath.Separator), home+string(filepath.Separator)) {
			t.Skip("TMPDIR is under $HOME; cannot construct an out-of-scope dir")
		}
	}
	rec = post(t, srv, "/library/browse", url.Values{"path": {outside}}, true)
	if !strings.Contains(rec.Body.String(), "Refusing to browse outside") {
		t.Errorf("out-of-scope dir %s should be refused, got:\n%s", outside, rec.Body.String())
	}

	// Symlink escape: a symlink under model_root pointing at an outside dir is
	// refused on the resolved real path.
	if runtime.GOOS != "windows" {
		link := filepath.Join(root, "escape")
		if err := os.Symlink(outside, link); err != nil {
			t.Fatal(err)
		}
		rec = post(t, srv, "/library/browse", url.Values{"path": {link}}, true)
		if !strings.Contains(rec.Body.String(), "Refusing to browse outside") {
			t.Errorf("symlink escaping to %s should be refused, got:\n%s", outside, rec.Body.String())
		}
	}
}

func TestScanDirAddRemovePersistAndScan(t *testing.T) {
	root := t.TempDir()
	extra := t.TempDir()
	srv := newLibraryTestServer(t, root)

	// Add → persisted + rendered as a pre-checked checkbox.
	rec := post(t, srv, "/library/scan-dirs/add", url.Values{"path": {extra}}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("add = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, extra) || !strings.Contains(body, `name="scan_dir"`) || !strings.Contains(body, "checked") {
		t.Errorf("add did not render a pre-checked selection:\n%s", body)
	}
	dirs, _ := srv.store.ListScanDirs()
	if len(dirs) != 1 || dirs[0] != extra {
		t.Fatalf("add did not persist: %v", dirs)
	}

	// A "Scan selected" over the persisted dir surfaces a cross-dir duplicate and
	// re-persists exactly the checked selection.
	if err := os.WriteFile(filepath.Join(root, "a.safetensors"), []byte("dup"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(extra, "b.safetensors"), []byte("dup"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec = post(t, srv, "/library/scan", url.Values{"scan_dir": {extra}}, true)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "duplicate") {
		t.Fatalf("scan selected should surface the duplicate, got %d:\n%s", rec.Code, rec.Body.String())
	}
	dirs, _ = srv.store.ListScanDirs()
	if len(dirs) != 1 || dirs[0] != extra {
		t.Fatalf("scan should persist the checked selection, got %v", dirs)
	}

	// Remove → selection cleared.
	rec = post(t, srv, "/library/scan-dirs/remove", url.Values{"path": {extra}}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("remove = %d", rec.Code)
	}
	if dirs, _ = srv.store.ListScanDirs(); len(dirs) != 0 {
		t.Fatalf("remove should clear the selection, got %v", dirs)
	}
}

// TestDiscoverLoadingIndicatorMarkup asserts the non-hanging loading affordance.
// Since the POST now returns instantly and swaps in the polling scanning
// fragment, the progress spinner + copy live on that fragment (discoverScanning),
// NOT as a button-level hx-indicator. The button keeps a brief click-guard.
func TestDiscoverLoadingIndicatorMarkup(t *testing.T) {
	// allowExtra=true so the discover control is rendered.
	out := renderString(t, libraryPage(buildLibraryView(nil), "csrf-tok", true, nil, "dark"))
	for _, want := range []string{
		`hx-post="/library/discover"`,
		`hx-disabled-elt="this"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("library page missing discover control attr %q", want)
		}
	}
	// The stale button-level indicator must be gone (it caused a double spinner).
	for _, gone := range []string{`hx-indicator="#discover-spinner"`, `id="discover-spinner"`} {
		if strings.Contains(out, gone) {
			t.Errorf("library page still has removed button indicator %q", gone)
		}
	}
	// The real progress affordance lives on the scanning fragment: a self-polling
	// element with the spinner + scanning copy.
	scan := renderString(t, discoverScanning())
	for _, want := range []string{
		`hx-get="/library/discover/status"`,
		`hx-trigger="every 1s"`,
		"Scanning your system for ComfyUI / Automatic1111 installs",
	} {
		if !strings.Contains(scan, want) {
			t.Errorf("scanning fragment missing progress affordance %q", want)
		}
	}
}

// TestDiscoverResultsMessaging proves the three distinct render states:
// completed-empty, truncated-with-partial, and truncated-empty.
func TestDiscoverResultsMessaging(t *testing.T) {
	install := library.Install{
		Path: "/home/u/ComfyUI", Kind: library.KindComfyUI,
		Confidence: library.ConfidenceHigh, ModelDirs: []string{"checkpoints"},
	}
	const budget = 6 * time.Second

	// Completed, nothing found → plain "no installs" copy, no timeout note.
	completed := renderString(t, discoverResults(nil, nil, false, budget, "csrf"))
	if !strings.Contains(completed, "No ComfyUI or Automatic1111/Forge installs found") {
		t.Errorf("completed-empty should render the plain no-installs copy:\n%s", completed)
	}
	if strings.Contains(completed, "Stopped after") {
		t.Errorf("completed-empty must NOT claim it was stopped:\n%s", completed)
	}

	// Truncated WITH partial installs → renders the install AND the stopped note.
	partial := renderString(t, discoverResults([]library.Install{install}, nil, true, budget, "csrf"))
	if !strings.Contains(partial, install.Path) {
		t.Errorf("truncated result should still render the partial install:\n%s", partial)
	}
	for _, want := range []string{"Stopped after 6s", "some installs may be missing", "add a path manually"} {
		if !strings.Contains(partial, want) {
			t.Errorf("truncated result missing %q:\n%s", want, partial)
		}
	}

	// Truncated with NOTHING found → still the stopped note, NOT the plain
	// "completed, none found" copy (the two states must be distinguishable).
	truncatedEmpty := renderString(t, discoverResults(nil, nil, true, budget, "csrf"))
	if !strings.Contains(truncatedEmpty, "Stopped after 6s") {
		t.Errorf("truncated-empty should render the stopped note:\n%s", truncatedEmpty)
	}
	if strings.Contains(truncatedEmpty, "No ComfyUI or Automatic1111/Forge installs found") {
		t.Errorf("truncated-empty must not claim a completed empty search:\n%s", truncatedEmpty)
	}
}

// TestDiscoverBrowseDisabledOnNonLoopback proves the discovery/browser controls
// are refused when the server is bound to a non-loopback interface.
func TestDiscoverBrowseDisabledOnNonLoopback(t *testing.T) {
	root := t.TempDir()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	srv := NewServer(st, stubReader{}, stubSubscriber{}, Config{
		BaseURL: "https://civitai.com", DefaultPollInterval: time.Hour,
		Addr:      "0.0.0.0:8787", // non-loopback
		ModelRoot: root, TrashDir: filepath.Join(root, ".trash"),
	}, nil)

	for _, path := range []string{"/library/discover", "/library/browse", "/library/scan-dirs/add"} {
		rec := post(t, srv, path, url.Values{"path": {t.TempDir()}}, true)
		if !strings.Contains(rec.Body.String(), "disabled when the server is bound to a non-loopback") {
			t.Errorf("%s should be gated on a non-loopback bind, got:\n%s", path, rec.Body.String())
		}
	}

	// The status GET endpoint is loopback-gated too (it exposes discovered host
	// paths), even though it needs no CSRF.
	rec := get(t, srv, "/library/discover/status")
	if !strings.Contains(rec.Body.String(), "disabled when the server is bound to a non-loopback") {
		t.Errorf("/library/discover/status should be gated on a non-loopback bind, got:\n%s", rec.Body.String())
	}
}
