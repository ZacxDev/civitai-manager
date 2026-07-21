package queue

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
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
