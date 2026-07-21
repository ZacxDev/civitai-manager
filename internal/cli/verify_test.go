package cli

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/poller"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// previewlessServer serves payload for every request. The CLI fixture's version
// JSON carries no images, so no preview is fetched — a single-response server is
// enough for these download/verify tests.
func previewlessServer(t *testing.T, payload []byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(payload)
	}))
}

// downloadOnce seeds a subscription and backfills its latest version to disk (a
// `done` queue row + the file), using a counting downloader so tests can assert
// on re-download behavior. It returns the app, the counter, and the on-disk path.
func seedDownloadedModel(t *testing.T, payload []byte, srvURL string) (*app, *atomic.Int64, string) {
	t.Helper()
	var calls atomic.Int64
	counting := func(ctx context.Context, u string) (*http.Response, error) {
		calls.Add(1)
		return loopbackDownloader(ctx, u)
	}
	a := newTestApp(t, fixtureClient(srvURL+"/file", sha256Hex(payload), counting))
	opts := poller.SubscribeOptions{AutoDownload: true, BackfillLatest: true, PollInterval: time.Hour}
	var out bytes.Buffer
	if err := subscribeRun(context.Background(), a, &out, "", []string{"1"}, opts); err != nil {
		t.Fatalf("seed backfill: %v (out=%q)", err, out.String())
	}
	if calls.Load() != 1 {
		t.Fatalf("seed should download exactly once, got %d", calls.Load())
	}
	path := findFileExt(t, a.cfg.ModelRoot, ".safetensors")
	if path == "" {
		t.Fatalf("seed did not put a file on disk")
	}
	return a, &calls, path
}

// TestVerifyHealthyReportsOK proves plain `verify` on a healthy library reports
// all OK and (with --repair) repairs nothing / does not re-download.
func TestVerifyHealthyReportsOK(t *testing.T) {
	payload := []byte("healthy model payload")
	srv := previewlessServer(t, payload)
	defer srv.Close()

	a, calls, _ := seedDownloadedModel(t, payload, srv.URL)

	var out bytes.Buffer
	if err := verifyRun(context.Background(), a, &out, false, false); err != nil {
		t.Fatalf("verify: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "1 OK") || !strings.Contains(got, "0 missing") {
		t.Errorf("healthy verify should report 1 OK / 0 missing, got %q", got)
	}
	if strings.Contains(got, "MISSING") {
		t.Errorf("healthy verify must not list any MISSING file, got %q", got)
	}

	// --repair on a healthy library repairs nothing and does not re-download.
	var out2 bytes.Buffer
	if err := verifyRun(context.Background(), a, &out2, true, false); err != nil {
		t.Fatalf("verify --repair: %v", err)
	}
	if !strings.Contains(out2.String(), "Nothing to repair.") {
		t.Errorf("healthy --repair should say nothing to repair, got %q", out2.String())
	}
	if calls.Load() != 1 {
		t.Errorf("healthy --repair must not re-download, download calls=%d", calls.Load())
	}
}

// TestVerifyDetectsAndRepairsMissingFile proves finding #1's core repro: a file
// the tool downloaded but the user has since DELETED is (a) reported MISSING by
// plain verify and (b) re-downloaded by `verify --repair`, restoring the file
// and returning the row to `done`. A normal poll can never do this (the version
// is already in seen_versions).
func TestVerifyDetectsAndRepairsMissingFile(t *testing.T) {
	payload := []byte("the model bytes that will be deleted then restored")
	srv := previewlessServer(t, payload)
	defer srv.Close()

	a, calls, path := seedDownloadedModel(t, payload, srv.URL)

	// User deletes the downloaded file.
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove file: %v", err)
	}

	// Plain verify: reports MISSING and lists the path, exit 0.
	var report bytes.Buffer
	if err := verifyRun(context.Background(), a, &report, false, false); err != nil {
		t.Fatalf("verify (report): %v", err)
	}
	if !strings.Contains(report.String(), "1 missing") {
		t.Errorf("verify should report 1 missing, got %q", report.String())
	}
	if !strings.Contains(report.String(), "MISSING") || !strings.Contains(report.String(), path) {
		t.Errorf("verify should list the missing path %q, got %q", path, report.String())
	}

	// verify --repair: re-downloads the deleted file.
	var repair bytes.Buffer
	if err := verifyRun(context.Background(), a, &repair, true, false); err != nil {
		t.Fatalf("verify --repair: %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("repair must re-download the missing file (want 2 total calls), got %d; out=%q",
			calls.Load(), repair.String())
	}
	if !strings.Contains(repair.String(), "Repaired 1 of 1 file(s).") {
		t.Errorf("repair should report 1 repaired, got %q", repair.String())
	}
	// File is back on disk with the right content.
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(payload) {
		t.Fatalf("repaired file wrong: err=%v", err)
	}
	// The row is `done` again — and there is still exactly one row (the done→queued
	// re-enqueue reused it; it did not trip the ux_dlq_active unique index).
	all, _ := a.store.ListQueue()
	if len(all) != 1 {
		t.Fatalf("want exactly one queue row after repair, got %d", len(all))
	}
	if all[0].Status != store.StatusDone {
		t.Errorf("row status after repair = %s, want done", all[0].Status)
	}
}

// TestVerifyCheckHashDetectsAndRepairsCorruptFile proves the --check-hash path: a
// PRESENT file whose bytes no longer match the expected SHA256 is reported CORRUPT
// and repaired (re-downloaded from source) by `verify --repair --check-hash`.
func TestVerifyCheckHashDetectsAndRepairsCorruptFile(t *testing.T) {
	payload := []byte("the correct model bytes")
	srv := previewlessServer(t, payload)
	defer srv.Close()

	a, calls, path := seedDownloadedModel(t, payload, srv.URL)

	// Corrupt the on-disk file in place (present, but wrong bytes).
	if err := os.WriteFile(path, []byte("corrupted contents that do not match the hash"), 0o644); err != nil {
		t.Fatalf("corrupt file: %v", err)
	}

	// Without --check-hash the file is present, so it reads as OK (cheap check).
	var cheap bytes.Buffer
	if err := verifyRun(context.Background(), a, &cheap, false, false); err != nil {
		t.Fatalf("verify (cheap): %v", err)
	}
	if !strings.Contains(cheap.String(), "1 OK") {
		t.Errorf("cheap verify should not detect corruption, got %q", cheap.String())
	}

	// With --check-hash it is CORRUPT; --repair re-downloads the good bytes.
	var repair bytes.Buffer
	if err := verifyRun(context.Background(), a, &repair, true, true); err != nil {
		t.Fatalf("verify --repair --check-hash: %v", err)
	}
	if !strings.Contains(repair.String(), "1 corrupt") || !strings.Contains(repair.String(), "CORRUPT") {
		t.Errorf("check-hash verify should report the corrupt file, got %q", repair.String())
	}
	if calls.Load() != 2 {
		t.Fatalf("repair must re-download the corrupt file (want 2 calls), got %d", calls.Load())
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(payload) {
		t.Fatalf("repaired file should hold the correct bytes: err=%v", err)
	}
	if !strings.Contains(repair.String(), "Repaired 1 of 1 file(s).") {
		t.Errorf("repair should report 1 repaired, got %q", repair.String())
	}
}

// TestBackfillAlreadyPresentMessage proves finding #3: re-running
// `subscribe --backfill-latest` on a subscription whose latest is already on disk
// prints the precise "Already have the latest version" line, not the old generic
// "No file downloaded (…)".
func TestBackfillAlreadyPresentMessage(t *testing.T) {
	payload := []byte("already-present backfill bytes")
	srv := previewlessServer(t, payload)
	defer srv.Close()

	a, _, _ := seedDownloadedModel(t, payload, srv.URL)

	// Second backfill on the same (healthy) subscription: already present.
	opts := poller.SubscribeOptions{AutoDownload: true, BackfillLatest: true, PollInterval: time.Hour}
	var out bytes.Buffer
	if err := subscribeRun(context.Background(), a, &out, "", []string{"1"}, opts); err != nil {
		t.Fatalf("second backfill: %v (out=%q)", err, out.String())
	}
	got := out.String()
	if !strings.Contains(got, "Already have the latest version (v1) on disk.") {
		t.Errorf("expected the already-present message, got %q", got)
	}
	if strings.Contains(got, "No file downloaded") {
		t.Errorf("must not print the old generic message, got %q", got)
	}
}

// TestBackfillFilterMismatchMessage proves finding #3: a backfill whose latest
// version is filtered out by the base-model filter reports the filter reason
// (naming the filter), not the generic line.
func TestBackfillFilterMismatchMessage(t *testing.T) {
	// The download path must never be reached (the filter short-circuits before
	// resolving the file); a call would be a bug.
	client := fixtureClient("http://unused", "", func(ctx context.Context, u string) (*http.Response, error) {
		t.Errorf("download must not be attempted for a filtered-out version")
		return nil, context.Canceled
	})
	a := newTestApp(t, client)

	// The fixture's only version is base model "SD 1.5"; filter on "SDXL".
	opts := poller.SubscribeOptions{
		AutoDownload: true, BackfillLatest: true, BaseModelFilter: "SDXL", PollInterval: time.Hour,
	}
	var out bytes.Buffer
	if err := subscribeRun(context.Background(), a, &out, "", []string{"1"}, opts); err != nil {
		t.Fatalf("subscribeRun: %v (out=%q)", err, out.String())
	}
	got := out.String()
	if !strings.Contains(got, `base model does not match filter "SDXL"`) {
		t.Errorf("expected the base-model filter reason, got %q", got)
	}
	if strings.Contains(got, "No file downloaded") {
		t.Errorf("must not print the old generic message, got %q", got)
	}
	// Nothing on disk, nothing queued.
	if p := findFileExt(t, a.cfg.ModelRoot, ".safetensors"); p != "" {
		t.Errorf("a filtered-out backfill must not write a file, found %q", p)
	}
}

// TestPrintDownloadVerificationUsesOnDiskName proves finding #2: the friendly
// verified line prints the ACTUAL on-disk file name (derived from dest_path),
// not the API's file.Name, so grepping the printed name finds the real file.
func TestPrintDownloadVerificationUsesOnDiskName(t *testing.T) {
	var buf bytes.Buffer
	items := []store.QueueItem{{
		FileName:       "easynegative.safetensors", // API name (lower-case)
		DestPath:       "/models/embed/EasyNegative.safetensors",
		SHA256Expected: "abc123def456abc123def456",
		SHA256Actual:   "abc123def456abc123def456",
	}}
	printDownloadVerification(&buf, items)
	got := buf.String()
	if !strings.Contains(got, "EasyNegative.safetensors") {
		t.Errorf("verified line must print the on-disk name, got %q", got)
	}
	if strings.Contains(got, "easynegative.safetensors") {
		t.Errorf("verified line must NOT print the API file name, got %q", got)
	}
}
