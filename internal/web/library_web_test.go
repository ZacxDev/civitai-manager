package web

import (
	"context"
	"net/http"
	"net/http/httptest"
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

	sc := srv.newScanner(false)
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
