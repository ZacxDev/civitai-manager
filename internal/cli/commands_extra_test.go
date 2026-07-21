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
		cfg:    cfg,
		store:  st,
		client: client,
		log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
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
	otherRowID, err := a.store.Enqueue(store.QueueItem{
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
	downloaded, err := drainDownloads(context.Background(), a)
	if err != nil {
		t.Fatalf("drainDownloads: %v", err)
	}
	if downloaded < 1 {
		t.Fatalf("expected >=1 downloaded, got %d", downloaded)
	}
	remaining, _ := a.store.ListQueue(store.StatusQueued)

	summary := formatCheckSummary(newCount, downloaded, len(remaining))
	if !strings.Contains(summary, "1 downloaded") {
		t.Errorf("summary should report the download count, got %q", summary)
	}
	if strings.Contains(summary, "0 item(s) queued") {
		t.Errorf("summary must not read as if nothing happened, got %q", summary)
	}
}
