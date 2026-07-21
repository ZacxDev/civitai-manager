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
	out := renderString(t, libraryPage(buildLibraryView(files), "csrf-tok"))
	for _, want := range []string{"Library", "Scan now", "Summary", "Deletion candidates", "superseded", "Reclaimable"} {
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
	if !strings.Contains(rec.Body.String(), "Scan now") {
		t.Error("library page missing Scan now")
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

// TestScanFormRendersPathsInput proves finding #2's UI: the Library page renders
// the extra-scan-paths input so the user can scan beyond model_root.
func TestScanFormRendersPathsInput(t *testing.T) {
	out := renderString(t, libraryPage(buildLibraryView(nil), "csrf-tok"))
	for _, want := range []string{"Scan now", "scan_paths", "Extra scan paths"} {
		if !strings.Contains(out, want) {
			t.Errorf("scan form missing %q", want)
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
