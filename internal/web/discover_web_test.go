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

func TestDiscoverEndpointRendersCandidates(t *testing.T) {
	root, install := buildComfyFixture(t)
	srv := newLibraryTestServer(t, t.TempDir())
	srv.discoverRoots = []string{root}

	// Without a CSRF token → 403.
	rec := post(t, srv, "/library/discover", url.Values{}, false)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("discover without CSRF = %d, want 403", rec.Code)
	}

	// With the token → renders the candidate with an Add control.
	rec = post(t, srv, "/library/discover", url.Values{}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("discover = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{install, "ComfyUI", "/library/scan-dirs/add", "Add", "confidence"} {
		if !strings.Contains(body, want) {
			t.Errorf("discover results missing %q in:\n%s", want, body)
		}
	}
}

func TestDiscoverDedupesAgainstModelRoot(t *testing.T) {
	root, install := buildComfyFixture(t)
	// Point model_root AT the discovered install so it is de-duped away.
	srv := newLibraryTestServer(t, install)
	srv.discoverRoots = []string{root}

	rec := post(t, srv, "/library/discover", url.Values{}, true)
	if strings.Contains(rec.Body.String(), "/library/scan-dirs/add") {
		t.Errorf("install equal to model_root should be de-duped, got:\n%s", rec.Body.String())
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
}
