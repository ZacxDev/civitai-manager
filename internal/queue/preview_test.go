package queue

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// enqueueForPreview enqueues a verified-download row whose model file and preview
// image are served by srv (paths /file and /img). It returns the row id and the
// destination path. Shared by the preview-policy tests.
func enqueueForPreview(t *testing.T, st *store.Store, srvURL string, payload []byte) (int64, string) {
	t.Helper()
	dest := filepath.Join(t.TempDir(), "LORA", "alice", "Model", "v1.safetensors")
	id, _, err := st.Enqueue(store.QueueItem{
		ModelID: 1, VersionID: 5, FileID: 50, FileName: "v1.safetensors",
		DownloadURL: srvURL + "/file", DestPath: dest,
		SHA256Expected: sha256Hex(payload), SizeKB: float64(len(payload)) / 1024,
	})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	return id, dest
}

func previewServer(t *testing.T, payload, preview []byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/file":
			_, _ = w.Write(payload)
		case "/img":
			_, _ = w.Write(preview)
		default:
			http.NotFound(w, r)
		}
	}))
}

func previewRaw(srvURL string) []byte {
	raw, _ := json.Marshal(map[string]any{
		"id": 5, "images": []map[string]string{{"url": srvURL + "/img"}},
	})
	return raw
}

// TestNoPreviewSkipsPreviewSidecar proves finding #4: --no-preview
// (SetPreviewPolicy noPreview=true) writes NO .preview.png, while the model file
// and the .civitai.info sidecar still land and the row still verifies as done.
func TestNoPreviewSkipsPreviewSidecar(t *testing.T) {
	payload := []byte("model file payload for no-preview test")
	preview := bytes.Repeat([]byte("P"), 4096)
	srv := previewServer(t, payload, preview)
	defer srv.Close()

	st := newTestStore(t)
	id, dest := enqueueForPreview(t, st, srv.URL, payload)

	w := New(st, fakeDownloader{}, fakeReader{raw: previewRaw(srv.URL)}, nil)
	w.SetPreviewPolicy(true, 0) // --no-preview
	if ok, err := w.ProcessOne(context.Background()); err != nil || !ok {
		t.Fatalf("ProcessOne: ok=%v err=%v", ok, err)
	}

	item, _ := st.GetQueueItem(id)
	if item.Status != store.StatusDone {
		t.Fatalf("status = %s, want done (err=%q)", item.Status, item.LastError)
	}
	if _, err := os.Stat(dest); err != nil {
		t.Errorf("model file must still be written: %v", err)
	}
	base := civitai.SidecarBase(dest)
	if _, err := os.Stat(base + ".civitai.info"); err != nil {
		t.Errorf(".civitai.info must still be written with --no-preview: %v", err)
	}
	if _, err := os.Stat(base + ".preview.png"); !os.IsNotExist(err) {
		t.Errorf(".preview.png must NOT be written with --no-preview (stat err=%v)", err)
	}
}

// TestMaxPreviewSizeSkipsOversizePreview proves finding #4: --max-preview-size
// smaller than the fetched preview skips ONLY the preview — the model file is
// still downloaded and verified done, and .civitai.info still lands.
func TestMaxPreviewSizeSkipsOversizePreview(t *testing.T) {
	payload := []byte("model file payload for max-preview-size test")
	preview := bytes.Repeat([]byte("P"), 8192) // 8 KiB preview
	srv := previewServer(t, payload, preview)
	defer srv.Close()

	st := newTestStore(t)
	id, dest := enqueueForPreview(t, st, srv.URL, payload)

	w := New(st, fakeDownloader{}, fakeReader{raw: previewRaw(srv.URL)}, nil)
	w.SetPreviewPolicy(false, 1024) // cap 1 KiB < 8 KiB preview
	if ok, err := w.ProcessOne(context.Background()); err != nil || !ok {
		t.Fatalf("ProcessOne: ok=%v err=%v", ok, err)
	}

	item, _ := st.GetQueueItem(id)
	if item.Status != store.StatusDone {
		t.Fatalf("status = %s, want done (err=%q)", item.Status, item.LastError)
	}
	if _, err := os.Stat(dest); err != nil {
		t.Errorf("model file must still be downloaded: %v", err)
	}
	if item.SHA256Actual == "" {
		t.Errorf("model file must still be hash-verified")
	}
	base := civitai.SidecarBase(dest)
	if _, err := os.Stat(base + ".preview.png"); !os.IsNotExist(err) {
		t.Errorf("oversize .preview.png must be skipped (stat err=%v)", err)
	}
	// No stray .part left behind.
	if _, err := os.Stat(base + ".preview.png.part"); !os.IsNotExist(err) {
		t.Errorf("preview .part temp must be cleaned up (stat err=%v)", err)
	}
}

// TestMaxPreviewSizeAllowsSmallPreview proves the cap is a ceiling, not an
// off-switch: a preview UNDER the cap is still written.
func TestMaxPreviewSizeAllowsSmallPreview(t *testing.T) {
	payload := []byte("model file payload for small-preview test")
	preview := []byte("tiny-png")
	srv := previewServer(t, payload, preview)
	defer srv.Close()

	st := newTestStore(t)
	_, dest := enqueueForPreview(t, st, srv.URL, payload)

	w := New(st, fakeDownloader{}, fakeReader{raw: previewRaw(srv.URL)}, nil)
	w.SetPreviewPolicy(false, 1<<20) // 1 MiB cap, preview is a few bytes
	if ok, err := w.ProcessOne(context.Background()); err != nil || !ok {
		t.Fatalf("ProcessOne: ok=%v err=%v", ok, err)
	}
	base := civitai.SidecarBase(dest)
	got, err := os.ReadFile(base + ".preview.png")
	if err != nil {
		t.Fatalf("under-cap preview must be written: %v", err)
	}
	if string(got) != string(preview) {
		t.Errorf("preview content mismatch: got %q", got)
	}
}
