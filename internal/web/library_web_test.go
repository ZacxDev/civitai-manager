package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/store"
)

func newLibraryTestServer(t *testing.T, root string) *Server {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return NewServer(st, stubReader{}, stubSubscriber{}, Config{
		BaseURL: "https://civitai.com", DefaultPollInterval: time.Hour,
		// A loopback Addr so the extra-scan-path capability is enabled for the
		// tests that exercise it; the non-loopback gating is tested separately.
		Addr:      "127.0.0.1:8787",
		ModelRoot: root, TrashDir: filepath.Join(root, ".trash"),
	}, nil)
}

func intPtr(i int) *int { return &i }

// seedDuplicateCandidate creates a keeper + a flagged duplicate on disk and in
// the index, returning the duplicate's id.
func seedDuplicateCandidate(t *testing.T, srv *Server, root string) (int64, string) {
	t.Helper()
	keeper := filepath.Join(root, "a.safetensors")
	dupe := filepath.Join(root, "b.safetensors")
	for _, p := range []string{keeper, dupe} {
		if err := os.WriteFile(p, []byte("same-bytes"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must := func(lf store.LocalFile) {
		if err := srv.store.UpsertLocalFile(lf); err != nil {
			t.Fatal(err)
		}
	}
	must(store.LocalFile{Path: keeper, SHA256: "same", ModelID: intPtr(1), VersionID: intPtr(1),
		SizeBytes: 10, Status: store.LocalStatusMatched, Kind: store.LocalKindModel})
	must(store.LocalFile{Path: dupe, SHA256: "same", ModelID: intPtr(1), VersionID: intPtr(1),
		SizeBytes: 10, Status: store.LocalStatusMatched, CandidateReason: store.CandidateDuplicate, Kind: store.LocalKindModel})
	got, _ := srv.store.GetLocalFileByPath(dupe)
	return got.ID, dupe
}

func TestLibraryAndTrashPagesRender(t *testing.T) {
	files := []store.LocalFile{
		{ID: 1, Path: "/m/a.safetensors", ModelID: intPtr(10), VersionID: intPtr(1), SizeBytes: 1024,
			Status: store.LocalStatusMatched, Kind: store.LocalKindModel},
		{ID: 2, Path: "/m/b.safetensors", ModelID: intPtr(10), VersionID: intPtr(2), SizeBytes: 2048,
			Status: store.LocalStatusMatched, CandidateReason: store.CandidateSuperseded, Kind: store.LocalKindModel},
	}
	out := renderString(t, libraryPage(buildLibraryView(files), "csrf-tok", true, nil))
	for _, want := range []string{"Library", "Scan selected", "Summary", "Deletion candidates", "superseded", "Reclaimable"} {
		if !strings.Contains(out, want) {
			t.Errorf("library page missing %q", want)
		}
	}

	created := time.Now()
	batches := []batchView{{Batch: store.QuarantineBatch{ID: 7, CreatedAt: created, Reason: "duplicate"}, Files: 3}}
	tout := renderString(t, trashPage(batches, "csrf-tok"))
	for _, want := range []string{"Trash", "Restore", "#7", "duplicate"} {
		if !strings.Contains(tout, want) {
			t.Errorf("trash page missing %q", want)
		}
	}
}

func TestLibraryHandlerRenders(t *testing.T) {
	srv := newLibraryTestServer(t, t.TempDir())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/library", nil)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Scan selected") {
		t.Error("library page missing Scan selected")
	}
}

// TestLibraryPostsAreCSRFProtected asserts the new state-changing endpoints
// reject a POST without a valid token and accept one with it.
func TestLibraryPostsAreCSRFProtected(t *testing.T) {
	paths := []struct{ name, path, body string }{
		{"scan", "/library/scan", ""},
		{"quarantine", "/library/quarantine", "id=1"},
		{"restore", "/trash/1/restore", ""},
	}
	for _, tc := range paths {
		t.Run(tc.name+" without token → 403", func(t *testing.T) {
			srv := newLibraryTestServer(t, t.TempDir())
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			srv.Handler().ServeHTTP(rec, req)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("expected 403 without CSRF token, got %d", rec.Code)
			}
		})
		t.Run(tc.name+" with token → not 403", func(t *testing.T) {
			srv := newLibraryTestServer(t, t.TempDir())
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Set("X-CSRF-Token", srv.csrf)
			srv.Handler().ServeHTTP(rec, req)
			if rec.Code == http.StatusForbidden {
				t.Fatalf("valid CSRF token should not be rejected (got 403)")
			}
		})
	}
}

// TestScanFormRendersPathsInput proves finding #2's UI: on a loopback bind the
// Library page renders the rich extra-directory selector (discovery + browser +
// selection) so the user can scan beyond model_root, plus the opt-in
// remote-match checkbox.
func TestScanFormRendersPathsInput(t *testing.T) {
	out := renderString(t, libraryPage(buildLibraryView(nil), "csrf-tok", true, nil))
	for _, want := range []string{
		"Scan selected", "Extra scan directories", "Discover installs",
		"/library/discover", "Browse server directories", "/library/browse", "match_remote",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("scan form missing %q", want)
		}
	}
}

// TestScanFormRendersPersistedSelection proves the persisted selection pre-fills
// the form as pre-checked checkboxes.
func TestScanFormRendersPersistedSelection(t *testing.T) {
	out := renderString(t, libraryPage(buildLibraryView(nil), "csrf-tok", true, []string{"/data/loras"}))
	for _, want := range []string{`name="scan_dir"`, "/data/loras", "checked"} {
		if !strings.Contains(out, want) {
			t.Errorf("persisted selection missing %q in:\n%s", want, out)
		}
	}
}

// TestScanFormOmitsPathsInputWhenNotAllowed proves finding #1(b)'s non-loopback
// gating at the UI layer: when extra paths are disabled the discovery/browser
// controls are not rendered, so a network-exposed server never offers the
// arbitrary-path control.
func TestScanFormOmitsPathsInputWhenNotAllowed(t *testing.T) {
	out := renderString(t, libraryPage(buildLibraryView(nil), "csrf-tok", false, nil))
	if !strings.Contains(out, "Scan selected") {
		t.Error("scan form should still offer 'Scan selected'")
	}
	for _, unwanted := range []string{
		"scan_paths", "Extra scan directories", "Discover installs",
		"/library/discover", "Browse server directories", "match_remote",
	} {
		if strings.Contains(out, unwanted) {
			t.Errorf("scan form must omit %q when extra paths are disabled", unwanted)
		}
	}
}

// TestLibraryScanWithExtraPathFindsCrossDirDuplicate proves finding #2's core: a
// scan driven from the web form over an EXTRA path (outside model_root) surfaces
// a cross-directory duplicate as a candidate — the flagship feature previously
// reachable only via the CLI `scan --path`.
func TestLibraryScanWithExtraPathFindsCrossDirDuplicate(t *testing.T) {
	root := t.TempDir()
	extra := t.TempDir()
	srv := newLibraryTestServer(t, root)

	// Two byte-identical model files, one in model_root and one in the extra dir.
	if err := os.WriteFile(filepath.Join(root, "a.safetensors"), []byte("identical-weights"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(extra, "b.safetensors"), []byte("identical-weights"), 0o644); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	form := url.Values{"scan_paths": {extra}}
	req := httptest.NewRequest(http.MethodPost, "/library/scan", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-CSRF-Token", srv.csrf)
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("scan status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "duplicate") {
		t.Fatalf("cross-dir duplicate not surfaced as a candidate:\n%s", rec.Body.String())
	}

	// The candidate is persisted and visible on the Library page too.
	cands, err := srv.store.ListCandidates(store.CandidateDuplicate)
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 1 {
		t.Fatalf("want 1 duplicate candidate persisted, got %d", len(cands))
	}
}

// TestLibraryScanRejectsBadPath proves finding #2's validation: a nonexistent
// extra path yields a friendly error (200 with a message), never a 500/panic.
func TestLibraryScanRejectsBadPath(t *testing.T) {
	srv := newLibraryTestServer(t, t.TempDir())
	rec := httptest.NewRecorder()
	form := url.Values{"scan_paths": {"/no/such/directory/here"}}
	req := httptest.NewRequest(http.MethodPost, "/library/scan", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-CSRF-Token", srv.csrf)
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("bad-path scan status = %d, want 200 with a friendly error", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Invalid scan path") {
		t.Errorf("expected a friendly 'Invalid scan path' error, got:\n%s", rec.Body.String())
	}
}

// postScan drives POST /library/scan against srv with the given form and a valid
// CSRF token, returning the recorder.
func postScan(t *testing.T, srv *Server, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/library/scan", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-CSRF-Token", srv.csrf)
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

// TestLibraryScanRejectsDangerousRoots proves finding #1(b): the web scan
// refuses "/", a system directory, and the user's HOME itself — with a friendly
// message and no walk — while permitting a normal subdirectory.
func TestLibraryScanRejectsDangerousRoots(t *testing.T) {
	srv := newLibraryTestServer(t, t.TempDir())

	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	for _, tc := range []struct{ name, path string }{
		{"root", "/"},
		{"etc", "/etc"},
		{"under-etc", "/etc/ssl"},
		{"home-itself", home},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := postScan(t, srv, url.Values{"scan_paths": {tc.path}})
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 with a friendly rejection", rec.Code)
			}
			if !strings.Contains(rec.Body.String(), "Invalid scan path") {
				t.Errorf("expected a friendly rejection naming the path, got:\n%s", rec.Body.String())
			}
		})
	}
}

// TestLibraryScanNonLoopbackDisablesExtraPaths proves finding #1(b)'s core: when
// the server is bound to a non-loopback address the extra-scan-path capability is
// disabled — a submitted scan_paths is rejected, the input is not rendered, yet a
// plain model_root scan still works.
func TestLibraryScanNonLoopbackDisablesExtraPaths(t *testing.T) {
	root := t.TempDir()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	srv := NewServer(st, stubReader{}, stubSubscriber{}, Config{
		BaseURL: "https://civitai.com", DefaultPollInterval: time.Hour,
		Addr:      "0.0.0.0:8787", // non-loopback: LAN-exposed
		ModelRoot: root, TrashDir: filepath.Join(root, ".trash"),
	}, nil)

	// The Library page must NOT render the extra-scan-path input.
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/library", nil))
	if strings.Contains(rec.Body.String(), "scan_paths") {
		t.Error("non-loopback server must not render the scan_paths input")
	}

	// A submitted extra path is rejected with the gating message.
	extra := t.TempDir()
	if err := os.WriteFile(filepath.Join(extra, "x.safetensors"), []byte("bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec = postScan(t, srv, url.Values{"scan_paths": {extra}})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "disabled when the server is bound to a non-loopback") {
		t.Fatalf("expected the non-loopback gating message, got status=%d:\n%s", rec.Code, rec.Body.String())
	}

	// A plain model_root scan (no extra paths) still succeeds.
	if err := os.WriteFile(filepath.Join(root, "m.safetensors"), []byte("bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec = postScan(t, srv, url.Values{})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Summary") {
		t.Fatalf("model_root-only scan should still work, got status=%d:\n%s", rec.Code, rec.Body.String())
	}
}

// TestLibraryScanBudgetDoesNotWipeCandidates proves finding #1(b) budget + #1(c)
// state safety together: a scan that exceeds the model-file budget aborts with a
// friendly "scan too large" error AND leaves any prior candidate flags intact
// (the failed scan must not wipe candidate state).
func TestLibraryScanBudgetDoesNotWipeCandidates(t *testing.T) {
	root := t.TempDir()
	srv := newLibraryTestServer(t, root)
	// Force a tiny budget so any real directory trips it.
	srv.cfg.WebScanMaxFiles = 1

	// Seed a pre-existing duplicate candidate (keeper + flagged dupe on disk).
	_, _ = seedDuplicateCandidate(t, srv, root)
	before, err := srv.store.ListCandidates(store.CandidateDuplicate)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != 1 {
		t.Fatalf("precondition: want 1 seeded candidate, got %d", len(before))
	}

	// Scan a directory holding more model files than the budget allows.
	big := t.TempDir()
	for _, n := range []string{"a.safetensors", "b.safetensors", "c.safetensors"} {
		if err := os.WriteFile(filepath.Join(big, n), []byte(n), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	rec := postScan(t, srv, url.Values{"scan_paths": {big}})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Scan too large") {
		t.Fatalf("expected a friendly 'Scan too large' abort, got status=%d:\n%s", rec.Code, rec.Body.String())
	}

	// The prior candidate flags must survive the aborted scan.
	after, err := srv.store.ListCandidates(store.CandidateDuplicate)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 1 {
		t.Fatalf("aborted scan must not wipe candidates: want 1, got %d", len(after))
	}
}

func TestQuarantineHandlerDryRunThenApply(t *testing.T) {
	root := t.TempDir()
	srv := newLibraryTestServer(t, root)
	id, dupe := seedDuplicateCandidate(t, srv, root)

	// Dry-run: reports the plan, moves nothing.
	rec := httptest.NewRecorder()
	body := "id=" + strconv.FormatInt(id, 10) + "&apply=false"
	req := httptest.NewRequest(http.MethodPost, "/library/quarantine", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-CSRF-Token", srv.csrf)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Dry-run") {
		t.Fatalf("dry-run: status=%d body missing 'Dry-run':\n%s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(dupe); err != nil {
		t.Fatal("dry-run must not move the file")
	}

	// Apply: actually moves the file.
	rec = httptest.NewRecorder()
	body = "id=" + strconv.FormatInt(id, 10) + "&apply=true"
	req = httptest.NewRequest(http.MethodPost, "/library/quarantine", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-CSRF-Token", srv.csrf)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Quarantined") {
		t.Fatalf("apply: status=%d body missing 'Quarantined':\n%s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(dupe); err == nil {
		t.Fatal("apply should have moved the file out of its original path")
	}
	batches, _ := srv.store.ListQuarantineBatches()
	if len(batches) != 1 {
		t.Fatalf("apply should record one batch, got %d", len(batches))
	}
}

func TestRestoreHandlerRoundTrips(t *testing.T) {
	root := t.TempDir()
	srv := newLibraryTestServer(t, root)
	id, dupe := seedDuplicateCandidate(t, srv, root)

	sc := srv.newScanner(nil, false)
	plan, err := sc.Quarantine(context.Background(), []int64{id}, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dupe); err == nil {
		t.Fatal("precondition: file should be quarantined")
	}

	rec := httptest.NewRecorder()
	path := "/trash/" + strconv.FormatInt(plan.BatchID, 10) + "/restore"
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(""))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-CSRF-Token", srv.csrf)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("restore status = %d", rec.Code)
	}
	if _, err := os.Stat(dupe); err != nil {
		t.Fatal("restore should have returned the file to its original path")
	}
}
