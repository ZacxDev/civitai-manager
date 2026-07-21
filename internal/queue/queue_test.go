package queue

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// fakeDownloader fetches via a plain client (the httptest server is loopback
// http, which the real SDK's SSRF guard would block).
type fakeDownloader struct{}

func (fakeDownloader) DownloadFile(ctx context.Context, fileURL string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fileURL, nil)
	if err != nil {
		return nil, err
	}
	return http.DefaultClient.Do(req)
}

// fakeReader supplies version detail + raw JSON (with an image URL) for sidecars.
type fakeReader struct {
	raw []byte
}

func (f fakeReader) GetModel(context.Context, string) (*civitai.ModelDetail, []byte, error) {
	return &civitai.ModelDetail{}, nil, nil
}
func (f fakeReader) GetModelVersion(context.Context, string) (*civitai.ModelVersionDetail, []byte, error) {
	return &civitai.ModelVersionDetail{}, f.raw, nil
}
func (f fakeReader) GetModelVersionByHash(context.Context, string) (*civitai.ModelVersionDetail, []byte, error) {
	return nil, nil, civitai.ErrNotFound
}
func (f fakeReader) SearchModels(context.Context, url.Values) (*civitai.ModelSearchResult, error) {
	return &civitai.ModelSearchResult{}, nil
}
func (f fakeReader) SearchCreators(context.Context, url.Values) (*civitai.CreatorSearchResult, error) {
	return &civitai.CreatorSearchResult{}, nil
}
func (f fakeReader) SearchImages(context.Context, url.Values) (*civitai.ImageSearchResult, error) {
	return &civitai.ImageSearchResult{}, nil
}

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func TestDownloadHappyPathVerifiesAndFinalizes(t *testing.T) {
	payload := []byte("this is a model file payload")
	preview := []byte("PNGDATA")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/file":
			_, _ = w.Write(payload)
		case "/img":
			_, _ = w.Write(preview)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	st := newTestStore(t)
	dest := filepath.Join(t.TempDir(), "LORA", "alice", "Model", "v1.safetensors")

	// Uppercase expected hash to prove case-insensitive verification.
	expected := strings.ToUpper(sha256Hex(payload))
	id, _ := st.Enqueue(store.QueueItem{
		ModelID: 1, VersionID: 5, FileID: 50, FileName: "v1.safetensors",
		DownloadURL: srv.URL + "/file", DestPath: dest, SHA256Expected: expected,
		SizeKB: float64(len(payload)) / 1024,
	})

	raw, _ := json.Marshal(map[string]any{
		"id": 5, "images": []map[string]string{{"url": srv.URL + "/img"}},
	})
	w := New(st, fakeDownloader{}, fakeReader{raw: raw}, nil)

	ok, err := w.ProcessOne(context.Background())
	if err != nil || !ok {
		t.Fatalf("ProcessOne: ok=%v err=%v", ok, err)
	}

	item, _ := st.GetQueueItem(id)
	if item.Status != store.StatusDone {
		t.Fatalf("status = %s, want done (err=%q)", item.Status, item.LastError)
	}
	// File finalized at dest with correct content.
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read dest: %v", err)
	}
	if string(got) != string(payload) {
		t.Errorf("dest content mismatch")
	}
	// .part temp removed after atomic rename.
	if _, err := os.Stat(dest + ".part"); !os.IsNotExist(err) {
		t.Errorf(".part temp should be gone after rename")
	}
	// Verified hash recorded (lowercase).
	if !strings.EqualFold(item.SHA256Actual, expected) {
		t.Errorf("recorded hash %q != expected %q", item.SHA256Actual, expected)
	}
	// Sidecars written.
	base := civitai.SidecarBase(dest)
	if _, err := os.Stat(base + ".civitai.info"); err != nil {
		t.Errorf(".civitai.info sidecar missing: %v", err)
	}
	if _, err := os.Stat(base + ".preview.png"); err != nil {
		t.Errorf(".preview.png sidecar missing: %v", err)
	}
	// local_files indexed.
	if n, _ := st.CountLocalFiles(); n != 1 {
		t.Errorf("local_files count = %d, want 1", n)
	}
}

func TestDownloadHashMismatchFailsRow(t *testing.T) {
	payload := []byte("real bytes")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	st := newTestStore(t)
	dest := filepath.Join(t.TempDir(), "out.safetensors")
	id, _ := st.Enqueue(store.QueueItem{
		ModelID: 1, VersionID: 5, FileID: 50, FileName: "out.safetensors",
		DownloadURL: srv.URL, DestPath: dest,
		SHA256Expected: "deadbeefdeadbeef", // wrong
		SizeKB:         1,
	})

	w := New(st, fakeDownloader{}, nil, nil)
	if _, err := w.ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}

	item, _ := st.GetQueueItem(id)
	if item.Status != store.StatusFailed {
		t.Fatalf("status = %s, want failed", item.Status)
	}
	if !strings.Contains(item.LastError, "mismatch") {
		t.Errorf("expected mismatch error, got %q", item.LastError)
	}
	// Corrupt file must NOT be kept: neither dest nor .part remains.
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Errorf("corrupt file must not be finalized at dest")
	}
	if _, err := os.Stat(dest + ".part"); !os.IsNotExist(err) {
		t.Errorf(".part must be removed on mismatch")
	}
}

func TestDownloadNoExpectedHashStillCompletes(t *testing.T) {
	payload := []byte("no hash provided by API")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	st := newTestStore(t)
	dest := filepath.Join(t.TempDir(), "nohash.bin")
	id, _ := st.Enqueue(store.QueueItem{
		ModelID: 1, VersionID: 5, FileID: 50, FileName: "nohash.bin",
		DownloadURL: srv.URL, DestPath: dest, // no SHA256Expected
		SizeKB: 1,
	})

	w := New(st, fakeDownloader{}, nil, nil)
	if _, err := w.ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	item, _ := st.GetQueueItem(id)
	if item.Status != store.StatusDone {
		t.Fatalf("status = %s, want done", item.Status)
	}
	if _, err := os.Stat(dest); err != nil {
		t.Errorf("file should be finalized even without an expected hash: %v", err)
	}
	if item.SHA256Actual == "" {
		t.Errorf("computed hash should still be recorded")
	}
}

// eventKinds returns the kind of every recorded event (newest first).
func eventKinds(t *testing.T, st *store.Store) []store.Event {
	t.Helper()
	evs, err := st.RecentEvents(50)
	if err != nil {
		t.Fatalf("recent events: %v", err)
	}
	return evs
}

// TestGracefulShutdownRequeuesInterruptedDownload proves finding #1: a download
// interrupted by a graceful shutdown (ctx cancel) must NOT be marked failed —
// it must return to the queue and complete on restart, never stranded in
// 'failed' with its version already seen.
func TestGracefulShutdownRequeuesInterruptedDownload(t *testing.T) {
	payload := []byte("the complete model file payload for the restart path")
	var reqs int32
	started := make(chan struct{})
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&reqs, 1) == 1 {
			// First attempt: stream a partial chunk, then block so the client is
			// mid-transfer when the context is cancelled.
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("partial-"))
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			close(started)
			<-release
			return
		}
		// Restart attempt: serve the whole file.
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	st := newTestStore(t)
	dest := filepath.Join(t.TempDir(), "interrupted.safetensors")
	expected := sha256Hex(payload)
	id, _ := st.Enqueue(store.QueueItem{
		ModelID: 1, VersionID: 5, FileID: 50, FileName: "interrupted.safetensors",
		DownloadURL: srv.URL, DestPath: dest, SHA256Expected: expected,
		SizeKB: float64(len(payload)) / 1024,
	})

	w := New(st, fakeDownloader{}, nil, nil)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-started
		cancel() // simulate SIGINT mid-download
	}()
	_, _ = w.ProcessOne(ctx)

	item, _ := st.GetQueueItem(id)
	if item.Status == store.StatusFailed {
		t.Fatalf("interrupted download must NOT be marked failed (would strand it); err=%q", item.LastError)
	}
	if item.Status != store.StatusQueued {
		t.Fatalf("interrupted download should be requeued, got status=%s", item.Status)
	}
	if item.Attempts != 0 {
		t.Errorf("a cancelled attempt must not count; attempts=%d want 0", item.Attempts)
	}
	if _, err := os.Stat(dest + ".part"); !os.IsNotExist(err) {
		t.Errorf(".part temp should be removed on shutdown, not leaked")
	}

	// Restart: unblock the first (now-abandoned) handler, then re-process.
	close(release)
	ok, err := w.ProcessOne(context.Background())
	if err != nil || !ok {
		t.Fatalf("restart ProcessOne: ok=%v err=%v", ok, err)
	}
	item, _ = st.GetQueueItem(id)
	if item.Status != store.StatusDone {
		t.Fatalf("after restart the download should complete, got %s (err=%q)", item.Status, item.LastError)
	}
	got, err := os.ReadFile(dest)
	if err != nil || string(got) != string(payload) {
		t.Fatalf("final file wrong after restart: err=%v", err)
	}
}

// TestDownloadEmptyHashRecordedUnverified proves finding #3: a file the API gave
// no hash for is finalized but reported as UNVERIFIED, never "verified".
func TestDownloadEmptyHashRecordedUnverified(t *testing.T) {
	payload := []byte("legit file, but the API supplied no hash")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	st := newTestStore(t)
	dest := filepath.Join(t.TempDir(), "nohash.bin")
	id, _ := st.Enqueue(store.QueueItem{
		ModelID: 1, VersionID: 5, FileID: 50, FileName: "nohash.bin",
		DownloadURL: srv.URL, DestPath: dest, // no SHA256Expected
		SizeKB: 1,
	})

	w := New(st, fakeDownloader{}, nil, nil)
	if _, err := w.ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}

	item, _ := st.GetQueueItem(id)
	if item.Status != store.StatusDone {
		t.Fatalf("status = %s, want done", item.Status)
	}

	var sawUnverified bool
	for _, e := range eventKinds(t, st) {
		if e.Kind == "download_unverified" {
			sawUnverified = true
			if !strings.Contains(e.Message, "UNVERIFIED") {
				t.Errorf("unverified event message should say UNVERIFIED, got %q", e.Message)
			}
		}
		if e.Kind == "download_done" && strings.Contains(e.Message, "verified") {
			t.Errorf("empty-hash download must not emit a 'verified' event: %q", e.Message)
		}
	}
	if !sawUnverified {
		t.Error("expected a download_unverified event for a no-hash download")
	}
}

// TestVerifiedDownloadEmitsVerifiedEvent guards the happy-path event text so the
// unverified change in #3 does not accidentally relabel real verifications.
func TestVerifiedDownloadEmitsVerifiedEvent(t *testing.T) {
	payload := []byte("hashed content")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	st := newTestStore(t)
	dest := filepath.Join(t.TempDir(), "hashed.bin")
	_, _ = st.Enqueue(store.QueueItem{
		ModelID: 1, VersionID: 5, FileID: 50, FileName: "hashed.bin",
		DownloadURL: srv.URL, DestPath: dest, SHA256Expected: sha256Hex(payload), SizeKB: 1,
	})
	w := New(st, fakeDownloader{}, nil, nil)
	if _, err := w.ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	var sawVerified bool
	for _, e := range eventKinds(t, st) {
		if e.Kind == "download_done" && strings.Contains(e.Message, "verified") {
			sawVerified = true
		}
		if e.Kind == "download_unverified" {
			t.Errorf("a hash-verified download must not be reported unverified: %q", e.Message)
		}
	}
	if !sawVerified {
		t.Error("expected a 'verified' download_done event on the happy path")
	}
}

// TestDownloadCLIOutputSaysVerified proves finding #4: the user-facing worker log
// (what a CLI user running `check --download` / `subscribe --backfill-latest`
// sees on their terminal) says "verified" on a hash-matched download and
// "unverified" when the API supplied no hash — never "verified" for an
// unverified file.
func TestDownloadCLIOutputSaysVerified(t *testing.T) {
	run := func(t *testing.T, sha string, payload []byte) string {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write(payload)
		}))
		defer srv.Close()

		st := newTestStore(t)
		dest := filepath.Join(t.TempDir(), "out.bin")
		_, _ = st.Enqueue(store.QueueItem{
			ModelID: 1, VersionID: 5, FileID: 50, FileName: "out.bin",
			DownloadURL: srv.URL, DestPath: dest, SHA256Expected: sha, SizeKB: 1,
		})

		var buf bytes.Buffer
		log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
		w := New(st, fakeDownloader{}, nil, log)
		if _, err := w.ProcessOne(context.Background()); err != nil {
			t.Fatal(err)
		}
		return buf.String()
	}

	t.Run("hash-matched says verified", func(t *testing.T) {
		payload := []byte("verified content")
		out := run(t, sha256Hex(payload), payload)
		if !strings.Contains(out, "verified") {
			t.Errorf("hash-matched CLI output must say 'verified', got %q", out)
		}
		if strings.Contains(out, "unverified") {
			t.Errorf("a hash-matched download must not read as 'unverified', got %q", out)
		}
	})

	t.Run("no-hash says unverified", func(t *testing.T) {
		payload := []byte("no hash supplied by the API")
		out := run(t, "", payload) // no expected hash
		if !strings.Contains(out, "unverified") {
			t.Errorf("a no-hash download's CLI output must say 'unverified', got %q", out)
		}
	})
}

func TestDownloadHTTPErrorRetriesThenFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer srv.Close()

	st := newTestStore(t)
	dest := filepath.Join(t.TempDir(), "err.bin")
	id, _ := st.Enqueue(store.QueueItem{
		ModelID: 1, VersionID: 5, FileID: 50, FileName: "err.bin",
		DownloadURL: srv.URL, DestPath: dest, SizeKB: 1,
	})

	w := New(st, fakeDownloader{}, nil, nil)
	w.maxAttempts = 1 // fail fast without the retry backoff sleep

	if _, err := w.ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	item, _ := st.GetQueueItem(id)
	if item.Status != store.StatusFailed {
		t.Fatalf("status = %s, want failed", item.Status)
	}
	if !strings.Contains(item.LastError, "500") {
		t.Errorf("expected HTTP 500 in error, got %q", item.LastError)
	}
}
