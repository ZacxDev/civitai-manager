package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
	"github.com/ZacxDev/civitai-manager/internal/config"
	"github.com/ZacxDev/civitai-manager/internal/poller"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// cliFakeClient is a configurable in-memory civitai.Client (Reader+Downloader)
// for driving CLI command logic without a network. Downloads are served by an
// optional httptest server the test wires via dlURL.
type cliFakeClient struct {
	model     *civitai.ModelDetail
	version   *civitai.ModelVersionDetail
	search    *civitai.ModelSearchResult
	searchErr error

	// lastSearch captures the url.Values passed to SearchModels.
	lastSearch url.Values

	// dl implements DownloadFile (wired to a loopback httptest server in tests
	// that exercise the download path).
	dl func(ctx context.Context, fileURL string) (*http.Response, error)
}

func (c *cliFakeClient) GetModel(_ context.Context, _ string) (*civitai.ModelDetail, []byte, error) {
	if c.model == nil {
		return nil, nil, civitai.ErrNotFound
	}
	raw, _ := json.Marshal(c.model)
	return c.model, raw, nil
}
func (c *cliFakeClient) GetModelVersion(_ context.Context, _ string) (*civitai.ModelVersionDetail, []byte, error) {
	if c.version == nil {
		return nil, nil, civitai.ErrNotFound
	}
	raw, _ := json.Marshal(c.version)
	return c.version, raw, nil
}
func (c *cliFakeClient) GetModelVersionByHash(_ context.Context, _ string) (*civitai.ModelVersionDetail, []byte, error) {
	return nil, nil, civitai.ErrNotFound
}
func (c *cliFakeClient) GetModelVersionsByHashes(_ context.Context, _ []string) ([]civitai.HashMatch, error) {
	return nil, nil
}
func (c *cliFakeClient) SearchModels(_ context.Context, q url.Values) (*civitai.ModelSearchResult, error) {
	c.lastSearch = q
	if c.searchErr != nil {
		return nil, c.searchErr
	}
	if c.search != nil {
		return c.search, nil
	}
	return &civitai.ModelSearchResult{}, nil
}
func (c *cliFakeClient) SearchCreators(_ context.Context, _ url.Values) (*civitai.CreatorSearchResult, error) {
	return &civitai.CreatorSearchResult{}, nil
}
func (c *cliFakeClient) SearchImages(_ context.Context, _ url.Values) (*civitai.ImageSearchResult, error) {
	return &civitai.ImageSearchResult{}, nil
}
func (c *cliFakeClient) DownloadFile(ctx context.Context, fileURL string) (*http.Response, error) {
	return c.dl(ctx, fileURL)
}

func newTestApp(t *testing.T, client civitai.Client) *app {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	cfg := &config.Config{
		BaseURL:             "https://civitai.com",
		ModelRoot:           t.TempDir(),
		DefaultPollInterval: config.Duration(time.Hour),
		DownloadJitter:      0,
	}
	return &app{
		cfg:       cfg,
		store:     st,
		client:    client,
		log:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		logWriter: io.Discard,
	}
}

// loopbackDownloader fetches over plain http (the httptest server is loopback).
func loopbackDownloader(ctx context.Context, fileURL string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fileURL, nil)
	if err != nil {
		return nil, err
	}
	return http.DefaultClient.Do(req)
}

// fixtureClient builds a fake client whose single model/version points its
// primary file at the given download URL with the given expected sha256.
func fixtureClient(dlURL, sha string, dl func(context.Context, string) (*http.Response, error)) *cliFakeClient {
	return &cliFakeClient{
		model: &civitai.ModelDetail{
			ID: 1, Name: "TestModel", Type: "LORA",
			Creator:       &civitai.Creator{Username: "alice"},
			ModelVersions: []civitai.ModelVersionSummary{{ID: 100, Name: "v1", BaseModel: "SD 1.5"}},
		},
		version: &civitai.ModelVersionDetail{
			ID: 100, ModelID: 1, Name: "v1", BaseModel: "SD 1.5",
			Files: []civitai.ModelVersionFile{{
				ID: 500, Name: "test.safetensors", Type: "Model", Primary: true,
				DownloadURL: dlURL, SizeKB: 1, Hashes: civitai.FileHashes{SHA256: sha},
			}},
		},
		dl: dl,
	}
}

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// findFileExt walks root and returns the first regular file whose name ends
// with ext. (The on-disk base name is the version name, not the API file name.)
func findFileExt(t *testing.T, root, ext string) string {
	t.Helper()
	var found string
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err == nil && info != nil && !info.IsDir() && strings.HasSuffix(info.Name(), ext) {
			found = p
		}
		return nil
	})
	return found
}

// TestSubscribeBackfillDownloadsToDisk proves finding #1: subscribe
// --backfill-latest leaves the latest version's file ON DISK when the command
// returns — not merely a queued row.
func TestSubscribeBackfillDownloadsToDisk(t *testing.T) {
	payload := []byte("the latest model version bytes")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	client := fixtureClient(srv.URL+"/file", sha256Hex(payload), loopbackDownloader)
	a := newTestApp(t, client)

	var out bytes.Buffer
	opts := poller.SubscribeOptions{AutoDownload: true, BackfillLatest: true, PollInterval: time.Hour}
	if err := subscribeRun(context.Background(), a, &out, "", []string{"1"}, opts); err != nil {
		t.Fatalf("subscribeRun: %v", err)
	}

	// The file must be on disk NOW (the command returned only after draining it).
	path := findFileExt(t, a.cfg.ModelRoot, ".safetensors")
	if path == "" {
		t.Fatalf("backfill file not on disk after subscribe returned; output=%q", out.String())
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(payload) {
		t.Fatalf("downloaded file content wrong: err=%v", err)
	}
	// The queue row must be done, not left queued.
	q, _ := a.store.ListQueue(store.StatusQueued)
	if len(q) != 0 {
		t.Errorf("no items should remain queued after backfill, got %d", len(q))
	}
}

// TestSubscribePlainLeavesQueuedNotDownloaded proves the contrast: plain
// subscribe (no --backfill-latest) does NOT download and does NOT enqueue the
// back-catalogue — it only seeds the ledger.
func TestSubscribePlainLeavesQueuedNotDownloaded(t *testing.T) {
	called := false
	client := fixtureClient("http://unused", "", func(ctx context.Context, u string) (*http.Response, error) {
		called = true
		return nil, context.Canceled
	})
	a := newTestApp(t, client)

	var out bytes.Buffer
	opts := poller.SubscribeOptions{AutoDownload: true, BackfillLatest: false, PollInterval: time.Hour}
	if err := subscribeRun(context.Background(), a, &out, "", []string{"1"}, opts); err != nil {
		t.Fatalf("subscribeRun: %v", err)
	}
	if called {
		t.Errorf("plain subscribe must not download anything")
	}
	if p := findFileExt(t, a.cfg.ModelRoot, ".safetensors"); p != "" {
		t.Errorf("plain subscribe must not write a file, found %q", p)
	}
	// First poll seeds the ledger without enqueuing the back-catalogue.
	q, _ := a.store.ListQueue(store.StatusQueued)
	if len(q) != 0 {
		t.Errorf("plain subscribe should not enqueue on first poll, got %d queued", len(q))
	}
}

// TestSubscribeBackfillDownloadErrorFails proves finding #1's failure path: a
// failed backfill download makes the command return non-nil. It uses a checksum
// mismatch (a terminal failure — no retry backoff) so the test is fast.
func TestSubscribeBackfillDownloadErrorFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("corrupt bytes that will not match the expected hash"))
	}))
	defer srv.Close()

	// Expected hash is for different content → the stream verifies as a mismatch
	// and the row is marked failed (terminal, no retries).
	client := fixtureClient(srv.URL+"/file", sha256Hex([]byte("the real content")), loopbackDownloader)
	a := newTestApp(t, client)

	var out bytes.Buffer
	opts := poller.SubscribeOptions{AutoDownload: true, BackfillLatest: true, PollInterval: time.Hour}
	err := subscribeRun(context.Background(), a, &out, "", []string{"1"}, opts)
	if err == nil {
		t.Fatalf("expected a non-nil error when the backfill download fails; output=%q", out.String())
	}
	if !strings.Contains(err.Error(), "backfill") {
		t.Errorf("error should mention backfill, got %v", err)
	}
}

// TestSubscribeBackfillScopedToNewSubscription proves findings #1/#2: the
// --backfill-latest synchronous drain is confined to the subscription just
// created. A DUE queued row belonging to a DIFFERENT subscription (mimicking a
// backlog left by a prior `check` without --download) must be left untouched —
// still queued, never claimed or downloaded — and the reported count must be 1
// (only the backfill), not 2.
func TestSubscribeBackfillScopedToNewSubscription(t *testing.T) {
	payload := []byte("new subscription backfill bytes")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	client := fixtureClient(srv.URL+"/file", sha256Hex(payload), loopbackDownloader)
	a := newTestApp(t, client)

	// Pre-seed a DIFFERENT subscription with a DUE queued row (not_before nil, so
	// it is immediately claimable by an unscoped drain). Its download URL points
	// at a closed port so that, if the scoped drain ever touched it, the test
	// would fail loudly rather than silently succeeding.
	otherMid := 42
	otherSubID, err := a.store.CreateSubscription(store.Subscription{
		Kind: store.KindModel, ModelID: &otherMid, AutoDownload: true, PollIntervalSecs: 3600,
	})
	if err != nil {
		t.Fatalf("create other subscription: %v", err)
	}
	otherRowID, _, err := a.store.Enqueue(store.QueueItem{
		SubscriptionID: &otherSubID,
		ModelID:        42, VersionID: 200, FileID: 600,
		FileName:    "other.safetensors",
		DownloadURL: "http://127.0.0.1:1/never-fetched",
		DestPath:    filepath.Join(a.cfg.ModelRoot, "other", "other.safetensors"),
		Status:      store.StatusQueued,
	})
	if err != nil {
		t.Fatalf("enqueue other row: %v", err)
	}

	var out bytes.Buffer
	opts := poller.SubscribeOptions{AutoDownload: true, BackfillLatest: true, PollInterval: time.Hour}
	if err := subscribeRun(context.Background(), a, &out, "", []string{"1"}, opts); err != nil {
		t.Fatalf("subscribeRun: %v", err)
	}

	// The NEW subscription's file IS on disk.
	path := findFileExt(t, a.cfg.ModelRoot, ".safetensors")
	if path == "" {
		t.Fatalf("backfill file not on disk after subscribe returned; output=%q", out.String())
	}
	got, rerr := os.ReadFile(path)
	if rerr != nil || string(got) != string(payload) {
		t.Fatalf("downloaded file content wrong: err=%v", rerr)
	}

	// The UNRELATED subscription's row is STILL queued and was never claimed.
	other, gerr := a.store.GetQueueItem(otherRowID)
	if gerr != nil {
		t.Fatalf("get other row: %v", gerr)
	}
	if other.Status != store.StatusQueued {
		t.Errorf("unrelated subscription's row must remain queued, got %q", other.Status)
	}
	if other.Attempts != 0 {
		t.Errorf("unrelated subscription's row must not have been claimed, attempts=%d", other.Attempts)
	}

	// The reported count is ONLY the backfill (1), not the unrelated row too.
	if !strings.Contains(out.String(), "Downloaded 1 file(s).") {
		t.Errorf("expected \"Downloaded 1 file(s).\", got %q", out.String())
	}
}

// TestSubscribeBackfillRecoversAfterFailure proves the backfill retry story: a
// `--backfill-latest` whose FIRST download fails (leaving the version marked seen
// and the queue row terminally failed) is recoverable by simply re-running
// `subscribe --backfill-latest` — the existing-subscription recovery path
// re-attempts the current latest even though a normal poll never would.
func TestSubscribeBackfillRecoversAfterFailure(t *testing.T) {
	good := []byte("the real latest version bytes")
	var serveGood atomic.Bool // false first: serve corrupt bytes, then good bytes
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if serveGood.Load() {
			_, _ = w.Write(good)
		} else {
			_, _ = w.Write([]byte("corrupt bytes that will not match the hash"))
		}
	}))
	defer srv.Close()

	// Expected hash is for the GOOD bytes, so phase 1 (corrupt) mismatches and
	// fails terminally (no retry backoff), and phase 2 (good) verifies.
	client := fixtureClient(srv.URL+"/file", sha256Hex(good), loopbackDownloader)
	a := newTestApp(t, client)
	opts := poller.SubscribeOptions{AutoDownload: true, BackfillLatest: true, PollInterval: time.Hour}

	// Phase 1: first backfill fails.
	var out1 bytes.Buffer
	if err := subscribeRun(context.Background(), a, &out1, "", []string{"1"}, opts); err == nil {
		t.Fatalf("phase 1 backfill should fail on a checksum mismatch; out=%q", out1.String())
	}
	if p := findFileExt(t, a.cfg.ModelRoot, ".safetensors"); p != "" {
		t.Fatalf("no file should be on disk after a failed backfill, found %q", p)
	}

	// Phase 2: re-run backfill; the server now serves the good bytes. The recovery
	// path must re-attempt despite the version already being seen and the prior
	// row being terminally failed.
	serveGood.Store(true)
	var out2 bytes.Buffer
	if err := subscribeRun(context.Background(), a, &out2, "", []string{"1"}, opts); err != nil {
		t.Fatalf("phase 2 recovery backfill should succeed, got %v (out=%q)", err, out2.String())
	}
	if !strings.Contains(out2.String(), "re-attempting latest download") {
		t.Errorf("recovery run should announce it is re-attempting, got %q", out2.String())
	}
	path := findFileExt(t, a.cfg.ModelRoot, ".safetensors")
	if path == "" {
		t.Fatalf("recovery backfill did not put the file on disk; out=%q", out2.String())
	}
	got, rerr := os.ReadFile(path)
	if rerr != nil || string(got) != string(good) {
		t.Fatalf("recovered file content wrong: err=%v", rerr)
	}
}

// TestSubscribeBackfillIdempotentOnHealthySub proves the item-#3 invariant:
// re-running `subscribe --backfill-latest` on a HEALTHY subscription (its latest
// version already downloaded to `done`) is a no-op — it must NOT re-fetch the
// file and must NOT create a second queue row. The first backfill leaves exactly
// one `done` row; the second sees that active `done` row (via the dedup guard /
// partial-unique index) and enqueues nothing, so the fake downloader's call
// count does not increase.
func TestSubscribeBackfillIdempotentOnHealthySub(t *testing.T) {
	payload := []byte("healthy-sub backfill bytes")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	// Count how many times the download path is actually invoked.
	var downloadCalls atomic.Int64
	counting := func(ctx context.Context, u string) (*http.Response, error) {
		downloadCalls.Add(1)
		return loopbackDownloader(ctx, u)
	}
	client := fixtureClient(srv.URL+"/file", sha256Hex(payload), counting)
	a := newTestApp(t, client)

	opts := poller.SubscribeOptions{AutoDownload: true, BackfillLatest: true, PollInterval: time.Hour}

	// First backfill: downloads the latest to done.
	var out1 bytes.Buffer
	if err := subscribeRun(context.Background(), a, &out1, "", []string{"1"}, opts); err != nil {
		t.Fatalf("first backfill: %v (out=%q)", err, out1.String())
	}
	if got := downloadCalls.Load(); got != 1 {
		t.Fatalf("first backfill should download exactly once, got %d calls", got)
	}
	doneRows, _ := a.store.ListQueue(store.StatusDone)
	if len(doneRows) != 1 {
		t.Fatalf("after first backfill want exactly 1 done row, got %d", len(doneRows))
	}
	firstRowID := doneRows[0].ID

	// Second backfill on the SAME (now healthy) subscription: must be a no-op.
	var out2 bytes.Buffer
	if err := subscribeRun(context.Background(), a, &out2, "", []string{"1"}, opts); err != nil {
		t.Fatalf("second backfill: %v (out=%q)", err, out2.String())
	}

	// The downloader was NOT called a second time.
	if got := downloadCalls.Load(); got != 1 {
		t.Errorf("re-running backfill on a healthy sub must NOT re-download, download calls = %d, want 1", got)
	}
	// No new queue row was created — still exactly the one done row, same id.
	allRows, _ := a.store.ListQueue()
	if len(allRows) != 1 {
		t.Fatalf("re-running backfill must not create a new queue row, total rows = %d, want 1", len(allRows))
	}
	if allRows[0].ID != firstRowID || allRows[0].Status != store.StatusDone {
		t.Errorf("the single row must remain the original done row (id=%d status=%s), got id=%d status=%s",
			firstRowID, store.StatusDone, allRows[0].ID, allRows[0].Status)
	}
	// Nothing is left queued.
	if q, _ := a.store.ListQueue(store.StatusQueued); len(q) != 0 {
		t.Errorf("no rows should be queued after an idempotent re-backfill, got %d", len(q))
	}
}

// TestBackfillDefaultOutputHasNoStructuredLogs proves two things about the
// subscribe --backfill-latest output at DEFAULT verbosity: (1) it is clean — the
// friendly progress/summary lines WITHOUT the raw `level=INFO msg=downloading …`
// structured dumps — while -v still yields the detailed structured logs; and (2)
// it surfaces the hash-VERIFICATION status of each download in that friendly
// output (the tool's headline safety feature), sourced from the completed queue
// rows' sha256_expected/actual — NOT from the suppressed slog INFO line. A
// hash-matched download reads "verified"; a download the API gave no hash for
// reads "unverified". The friendly prints and the worker/poller logger are
// pointed at ONE buffer so the assertion inspects exactly the stream a user sees.
func TestBackfillDefaultOutputHasNoStructuredLogs(t *testing.T) {
	payload := []byte("the latest model version bytes")
	newServer := func() *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write(payload)
		}))
	}
	opts := poller.SubscribeOptions{AutoDownload: true, BackfillLatest: true, PollInterval: time.Hour}

	// Default verbosity: clean output that still conveys hash verification.
	t.Run("default", func(t *testing.T) {
		srv := newServer()
		defer srv.Close()
		a := newTestApp(t, fixtureClient(srv.URL+"/file", sha256Hex(payload), loopbackDownloader))
		var buf bytes.Buffer
		a.logWriter = &buf // worker/poller logs land in the same buffer as friendly output

		if err := subscribeRun(context.Background(), a, &buf, "", []string{"1"}, opts); err != nil {
			t.Fatalf("subscribeRun: %v", err)
		}
		got := buf.String()
		if strings.Contains(got, "level=INFO") {
			t.Errorf("default output must NOT contain raw structured INFO lines, got:\n%s", got)
		}
		if !strings.Contains(got, "Downloaded 1 file(s).") {
			t.Errorf("default output must contain the friendly summary, got:\n%s", got)
		}
		// The verification signal must be present in the friendly output (the API
		// provided a matching hash → "verified").
		if !strings.Contains(got, "verified") {
			t.Errorf("default output must convey hash verification, got:\n%s", got)
		}
	})

	// Default verbosity, NO hash from the API: the file is still downloaded, but
	// the friendly output must explicitly flag it UNVERIFIED (never "verified").
	t.Run("default-unverified", func(t *testing.T) {
		srv := newServer()
		defer srv.Close()
		// An empty expected hash means the API gave no sha256 for the file.
		a := newTestApp(t, fixtureClient(srv.URL+"/file", "", loopbackDownloader))
		var buf bytes.Buffer
		a.logWriter = &buf

		if err := subscribeRun(context.Background(), a, &buf, "", []string{"1"}, opts); err != nil {
			t.Fatalf("subscribeRun: %v", err)
		}
		got := buf.String()
		if strings.Contains(got, "level=INFO") {
			t.Errorf("default output must NOT contain raw structured INFO lines, got:\n%s", got)
		}
		if !strings.Contains(got, "Downloaded 1 file(s).") {
			t.Errorf("default output must contain the friendly summary, got:\n%s", got)
		}
		if !strings.Contains(got, "unverified") {
			t.Errorf("a no-hash download must be flagged unverified, got:\n%s", got)
		}
	})

	// -v: detailed structured logs are present.
	t.Run("verbose", func(t *testing.T) {
		srv := newServer()
		defer srv.Close()
		a := newTestApp(t, fixtureClient(srv.URL+"/file", sha256Hex(payload), loopbackDownloader))
		a.verbose = true
		var buf bytes.Buffer
		a.logWriter = &buf

		if err := subscribeRun(context.Background(), a, &buf, "", []string{"1"}, opts); err != nil {
			t.Fatalf("subscribeRun -v: %v", err)
		}
		got := buf.String()
		if !strings.Contains(got, "level=INFO") {
			t.Errorf("-v output must contain the detailed structured logs, got:\n%s", got)
		}
		if !strings.Contains(got, "Downloaded 1 file(s).") {
			t.Errorf("-v output must still contain the friendly summary, got:\n%s", got)
		}
	})
}

// TestCheckSummaryReflectsDownloads proves finding #4: after a run that
// downloads an item, the summary reports the real counts, not "0 queued".
func TestCheckSummaryReflectsDownloads(t *testing.T) {
	payload := []byte("check-download payload bytes")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	client := fixtureClient(srv.URL+"/file", sha256Hex(payload), loopbackDownloader)
	a := newTestApp(t, client)

	// Pre-seed a subscription with the version UNSEEN so PollAll finds it "new"
	// and enqueues it, then the drain downloads it.
	mid := 1
	if _, err := a.store.CreateSubscription(store.Subscription{
		Kind: store.KindModel, ModelID: &mid, AutoDownload: true, PollIntervalSecs: 3600,
	}); err != nil {
		t.Fatal(err)
	}
	// Mark a bogus version seen so the real one (100) is diffed as new rather
	// than seeding the whole ledger silently on the first poll.
	subs, _ := a.store.ListSubscriptions()
	if err := a.store.MarkSeen(subs[0].ID, 999, time.Time{}); err != nil {
		t.Fatal(err)
	}

	pol := configuredPoller(a.store, a.client, a.cfg, a.log)
	newCount, err := pol.PollAll(context.Background())
	if err != nil {
		t.Fatalf("PollAll: %v", err)
	}
	if newCount < 1 {
		t.Fatalf("expected >=1 new version found, got %d", newCount)
	}
	completed, err := drainDownloads(context.Background(), a, a.cmdLogger())
	if err != nil {
		t.Fatalf("drainDownloads: %v", err)
	}
	if len(completed) < 1 {
		t.Fatalf("expected >=1 downloaded, got %d", len(completed))
	}
	remaining, _ := a.store.ListQueue(store.StatusQueued)

	summary := formatCheckSummary(newCount, len(completed), len(remaining))
	if !strings.Contains(summary, "1 downloaded") {
		t.Errorf("summary should report the download count, got %q", summary)
	}
	if strings.Contains(summary, "0 item(s) queued") {
		t.Errorf("summary must not read as if nothing happened, got %q", summary)
	}
}

// TestUnsubscribeClearsStateForCleanReSubscribe proves finding #3: unsubscribing
// must delete the subscription's seen_versions AND its download_queue rows so a
// later re-subscribe to the same target is a clean slate. Without the cleanup, a
// terminal `done` queue row survives (its FK is ON DELETE SET NULL, not CASCADE)
// and, via the ux_dlq_active partial-unique index, dedup-blocks the re-enqueue —
// so after the user deletes the file from disk, re-subscribing prints "No file
// downloaded" and never re-fetches. Here the re-subscribe must download AGAIN.
func TestUnsubscribeClearsStateForCleanReSubscribe(t *testing.T) {
	payload := []byte("re-subscribe backfill bytes")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	var downloadCalls atomic.Int64
	counting := func(ctx context.Context, u string) (*http.Response, error) {
		downloadCalls.Add(1)
		return loopbackDownloader(ctx, u)
	}
	client := fixtureClient(srv.URL+"/file", sha256Hex(payload), counting)
	a := newTestApp(t, client)
	opts := poller.SubscribeOptions{AutoDownload: true, BackfillLatest: true, PollInterval: time.Hour}

	// First subscribe + backfill: downloads once, leaves one done row.
	var out1 bytes.Buffer
	if err := subscribeRun(context.Background(), a, &out1, "", []string{"1"}, opts); err != nil {
		t.Fatalf("first subscribe: %v (out=%q)", err, out1.String())
	}
	if got := downloadCalls.Load(); got != 1 {
		t.Fatalf("first backfill should download once, got %d", got)
	}
	subs, _ := a.store.ListSubscriptions()
	if len(subs) != 1 {
		t.Fatalf("want 1 subscription, got %d", len(subs))
	}
	oldSubID := subs[0].ID
	if n, _ := a.store.CountSeen(oldSubID); n == 0 {
		t.Fatalf("expected seen_versions recorded for the subscription")
	}
	if done, _ := a.store.ListQueue(store.StatusDone); len(done) != 1 {
		t.Fatalf("want 1 done queue row after first backfill, got %d", len(done))
	}

	// Simulate the user deleting the downloaded file from disk.
	path := findFileExt(t, a.cfg.ModelRoot, ".safetensors")
	if path == "" {
		t.Fatalf("expected a downloaded file on disk")
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove file: %v", err)
	}

	// Unsubscribe: must wipe the subscription's seen_versions AND queue rows.
	if err := a.store.DeleteSubscription(oldSubID); err != nil {
		t.Fatalf("unsubscribe: %v", err)
	}
	if n, _ := a.store.CountSeen(oldSubID); n != 0 {
		t.Errorf("seen_versions must be cleared on unsubscribe, got %d", n)
	}
	if done, _ := a.store.ListQueue(store.StatusDone); len(done) != 0 {
		t.Errorf("download_queue rows must be cleared on unsubscribe, got %d done rows", len(done))
	}

	// Re-subscribe + backfill: a clean slate, so it must download AGAIN.
	var out2 bytes.Buffer
	if err := subscribeRun(context.Background(), a, &out2, "", []string{"1"}, opts); err != nil {
		t.Fatalf("re-subscribe: %v (out=%q)", err, out2.String())
	}
	if got := downloadCalls.Load(); got != 2 {
		t.Fatalf("re-subscribe must re-download (want 2 total calls), got %d; out=%q", got, out2.String())
	}
	if !strings.Contains(out2.String(), "Downloaded 1 file(s).") {
		t.Errorf("re-subscribe should report a fresh download, got %q", out2.String())
	}
	if p := findFileExt(t, a.cfg.ModelRoot, ".safetensors"); p == "" {
		t.Errorf("re-subscribe should have put the file back on disk")
	}
}
