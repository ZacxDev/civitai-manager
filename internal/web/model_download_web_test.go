package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
)

// downloadReader is a model reader whose selected version's file carries a
// download URL + hash, so the enqueue path has a real file to queue.
func downloadReader(t *testing.T) fakeReader {
	r := newModelReader(t)
	r.version.Name = "v2"
	r.version.Files = []civitai.ModelVersionFile{{
		ID: 1, Name: "great-model.safetensors", Type: "Model", SizeKB: 2 * 1024 * 1024,
		DownloadURL: "https://civitai.com/api/download/models/11",
		Hashes:      civitai.FileHashes{SHA256: "ABC123"},
	}}
	return r
}

func downloadForm(versionID, fileID string) url.Values {
	return url.Values{"versionId": {versionID}, "fileId": {fileID}}
}

// TestModelDownloadEnqueues proves a valid POST enqueues a QueueItem with the
// resolved fields and returns the "Queued" feedback fragment.
func TestModelDownloadEnqueues(t *testing.T) {
	srv := newModelServer(t, downloadReader(t))
	srv.cfg.ModelRoot = "/models"

	rec := post(t, srv, "/models/7/download", downloadForm("11", "1"), true)
	if rec.Code != http.StatusOK {
		t.Fatalf("download POST = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Queued") {
		t.Errorf("expected a Queued fragment:\n%s", rec.Body.String())
	}

	items, err := srv.store.ListQueue()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("expected exactly one queued item, got %d", len(items))
	}
	it := items[0]
	if it.ModelID != 7 || it.VersionID != 11 || it.FileID != 1 {
		t.Errorf("queue item ids wrong: %+v", it)
	}
	if it.FileName != "great-model.safetensors" {
		t.Errorf("file name = %q", it.FileName)
	}
	if it.DownloadURL != "https://civitai.com/api/download/models/11" {
		t.Errorf("download url = %q", it.DownloadURL)
	}
	if it.SHA256Expected != "ABC123" {
		t.Errorf("sha256 = %q", it.SHA256Expected)
	}
	if it.SizeKB != 2*1024*1024 {
		t.Errorf("sizeKB = %v", it.SizeKB)
	}
	// Destination is derived server-side under ModelRoot (never a client path):
	// <root>/<type>/<creator>/<model>/<versionName><ext>.
	if !strings.HasPrefix(it.DestPath, "/models/Checkpoint/carol/Great Model/") ||
		!strings.HasSuffix(it.DestPath, ".safetensors") {
		t.Errorf("dest path not derived under ModelRoot: %q", it.DestPath)
	}
}

// TestModelDownloadDuplicate proves a second enqueue of the same active file is
// deduped (store.Enqueue inserted=false) and reports "Already queued".
func TestModelDownloadDuplicate(t *testing.T) {
	srv := newModelServer(t, downloadReader(t))

	first := post(t, srv, "/models/7/download", downloadForm("11", "1"), true)
	if !strings.Contains(first.Body.String(), "Queued") {
		t.Fatalf("first enqueue should succeed:\n%s", first.Body.String())
	}
	second := post(t, srv, "/models/7/download", downloadForm("11", "1"), true)
	if !strings.Contains(second.Body.String(), "Already queued") {
		t.Errorf("duplicate enqueue should report 'Already queued':\n%s", second.Body.String())
	}
	items, _ := srv.store.ListQueue()
	if len(items) != 1 {
		t.Errorf("duplicate must not create a second row, got %d", len(items))
	}
}

// TestModelDownloadRejectsMissingCSRF proves a POST without a CSRF token is
// rejected (403) and enqueues nothing.
func TestModelDownloadRejectsMissingCSRF(t *testing.T) {
	srv := newModelServer(t, downloadReader(t))

	rec := post(t, srv, "/models/7/download", downloadForm("11", "1"), false)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("missing CSRF = %d, want 403", rec.Code)
	}
	items, _ := srv.store.ListQueue()
	if len(items) != 0 {
		t.Errorf("no enqueue should happen without CSRF, got %d", len(items))
	}
}

// TestModelDownloadBogusFile proves an unknown file id fails gracefully with a
// note and enqueues nothing.
func TestModelDownloadBogusFile(t *testing.T) {
	srv := newModelServer(t, downloadReader(t))

	rec := post(t, srv, "/models/7/download", downloadForm("11", "999"), true)
	if rec.Code != http.StatusOK {
		t.Fatalf("bogus file = %d, want 200 (graceful)", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "File not found") {
		t.Errorf("bogus file should report 'File not found':\n%s", rec.Body.String())
	}
	items, _ := srv.store.ListQueue()
	if len(items) != 0 {
		t.Errorf("a bogus file must not enqueue, got %d", len(items))
	}
}

// TestModelPageFileRowHasDownloadButton proves the file row on the model page
// renders the Download control wired to the CSRF-protected endpoint.
func TestModelPageFileRowHasDownloadButton(t *testing.T) {
	srv := newModelServer(t, downloadReader(t))
	body := getModelPage(t, srv, "/models/7")

	if !strings.Contains(body, `hx-post="/models/7/download"`) {
		t.Errorf("file row should carry the download POST:\n%s", body)
	}
	if !strings.Contains(body, srv.csrf) {
		t.Error("the download control must carry the CSRF token")
	}
	if !strings.Contains(body, ">Download<") {
		t.Error("the file row should render a Download button")
	}
}
