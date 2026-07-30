package web

import (
	"bytes"
	"context"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/hf"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// countProvenance is the total number of hf_provenance rows in a server's store —
// the "exactly once, and nothing for the negative cases" assertion.
func countProvenance(t *testing.T, srv *Server) int {
	t.Helper()
	var n int
	if err := srv.store.DB().QueryRow(`SELECT COUNT(*) FROM hf_provenance`).Scan(&n); err != nil {
		t.Fatalf("count hf_provenance: %v", err)
	}
	return n
}

// TestHFInstallRecordsProvenanceOnce: a successful HuggingFace install writes
// EXACTLY ONE provenance row, and the recorded sha256 is the digest of the bytes
// that actually landed on disk — i.e. the row is written after verification, not
// before it.
func TestHFInstallRecordsProvenanceOnce(t *testing.T) {
	body := []byte("YOLO-DETECTOR-WEIGHTS")
	fake := &fakeHFClient{match: curatedMatch(body), ok: true, body: body}
	srv, comfyModels := newHFServer(t, fake)
	srv.runFn = (&runRecorder{}).fn()
	wfID := seedWorkflow(t, srv, store.WorkflowFormatAPI,
		`{"42":{"class_type":"UltralyticsDetectorProvider","inputs":{"model_name":"bbox/face_yolov9c.pt"}}}`)

	rec := post(t, srv, "/workflows/"+wfID+"/download-and-run", url.Values{
		"filename": {"bbox/face_yolov9c.pt"},
	}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("download-and-run = %d", rec.Code)
	}
	pollRunUntilDone(t, srv, wfID)

	dest := filepath.Join(comfyModels, "ultralytics", "bbox", "face_yolov9c.pt")
	onDisk, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("model not written: %v", err)
	}

	if n := countProvenance(t, srv); n != 1 {
		t.Fatalf("hf_provenance rows = %d, want exactly 1", n)
	}
	got, err := srv.store.HFProvenanceForFile(sha256Hex(onDisk))
	if err != nil {
		t.Fatalf("read provenance: %v", err)
	}
	if got == nil {
		t.Fatal("no provenance recorded for the digest of the bytes on disk")
	}
	if got.Repo != "Bingsu/adetailer" || got.Path != "face_yolov9c.pt" || got.Revision != "53cc19de" {
		t.Fatalf("provenance = %+v, want the match's repo/path/pinned revision", got)
	}
	if got.RecordedAt.IsZero() {
		t.Error("recorded_at must be stamped")
	}
	// The link points at the PINNED revision, not at a moving branch.
	want := "https://huggingface.co/Bingsu/adetailer/blob/53cc19de/face_yolov9c.pt"
	if got.FileURL() != want {
		t.Errorf("FileURL() = %q, want %q", got.FileURL(), want)
	}
}

// TestHFProvenanceNotRecordedOnFailure covers every path that must record NOTHING.
// These are the negative tests that keep a source link from becoming a claim we
// cannot support.
func TestHFProvenanceNotRecordedOnFailure(t *testing.T) {
	body := []byte("REAL-BYTES")

	tests := []struct {
		name string
		// mutate adjusts the canned HF match (nil = the eligible curated match).
		mutate func(*hf.Match)
		// disableHF turns off the fallback entirely (the CivitAI-only path).
		disableHF bool
		wantFile  bool
	}{
		{
			name: "sha mismatch: bytes never verified, so nothing is claimed",
			mutate: func(m *hf.Match) {
				m.SHA256 = sha256Hex([]byte("SOMETHING ELSE"))
			},
		},
		{
			name:   "gated match is link-only: no bytes transferred, nothing recorded",
			mutate: func(m *hf.Match) { m.Gated = true },
		},
		{
			name:   "no determinable subdir: link-only, nothing recorded",
			mutate: func(m *hf.Match) { m.Subdir = "" },
		},
		{
			name:   "no LFS oid: not auto-eligible, nothing recorded",
			mutate: func(m *hf.Match) { m.SHA256 = "" },
		},
		{
			name:      "HuggingFace fallback disabled: nothing resolved, nothing recorded",
			disableHF: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := curatedMatch(body)
			if tc.mutate != nil {
				tc.mutate(m)
			}
			fake := &fakeHFClient{match: m, ok: true, body: body}
			srv, comfyModels := newHFServer(t, fake)
			if tc.disableHF {
				srv.cfg.HFFallback = false
				srv.hfClientFn = nil
			}
			srv.runFn = (&runRecorder{}).fn()
			wfID := seedWorkflow(t, srv, store.WorkflowFormatAPI,
				`{"42":{"class_type":"UltralyticsDetectorProvider","inputs":{"model_name":"bbox/face_yolov9c.pt"}}}`)

			rec := post(t, srv, "/workflows/"+wfID+"/download-and-run", url.Values{
				"filename": {"bbox/face_yolov9c.pt"},
			}, true)
			if rec.Code != http.StatusOK {
				t.Fatalf("download-and-run = %d", rec.Code)
			}
			pollRunUntilDone(t, srv, wfID)

			if n := countProvenance(t, srv); n != 0 {
				t.Fatalf("hf_provenance rows = %d, want 0", n)
			}
			dest := filepath.Join(comfyModels, "ultralytics", "bbox", "face_yolov9c.pt")
			if _, err := os.Stat(dest); !os.IsNotExist(err) {
				t.Errorf("no file should have been written to %s", dest)
			}
		})
	}
}

// TestCivitAIDownloadRecordsNoProvenance: the CivitAI path has no HuggingFace
// provenance to record and must not invent one. recordHFProvenance is exercised
// directly with a civitai-shaped pendingDownload (SourceHF false, no triple).
func TestCivitAIDownloadRecordsNoProvenance(t *testing.T) {
	srv, _ := newHFServer(t, &fakeHFClient{})

	tests := []struct {
		name string
		pd   pendingDownload
	}{
		{
			name: "civitai source",
			pd: pendingDownload{
				FileName: "x.safetensors", ExpectedSHA256: sha256Hex([]byte("b")),
				SourceHF: false, HFRepo: "a/b", HFPath: "x.safetensors", HFRevision: "rev",
			},
		},
		{"hf source with no repo", pendingDownload{FileName: "x", ExpectedSHA256: "aa", SourceHF: true, HFPath: "x", HFRevision: "r"}},
		{"hf source with no path", pendingDownload{FileName: "x", ExpectedSHA256: "aa", SourceHF: true, HFRepo: "a/b", HFRevision: "r"}},
		{"hf source with no revision", pendingDownload{FileName: "x", ExpectedSHA256: "aa", SourceHF: true, HFRepo: "a/b", HFPath: "x"}},
		{"hf source with no verified digest", pendingDownload{FileName: "x", SourceHF: true, HFRepo: "a/b", HFPath: "x", HFRevision: "r"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv.recordHFProvenance(tc.pd)
			if n := countProvenance(t, srv); n != 0 {
				t.Fatalf("hf_provenance rows = %d, want 0", n)
			}
		})
	}
}

// TestHFProvenanceStoreErrorIsNonFatal: the file is on disk and works, so a
// failure to record the (cosmetic) source link must never turn a successful
// install into a failed one.
func TestHFProvenanceStoreErrorIsNonFatal(t *testing.T) {
	body := []byte("BYTES-THAT-LAND")
	m := curatedMatch(body)
	fake := &fakeHFClient{match: m, ok: true, body: body}
	srv, comfyModels := newHFServer(t, fake)

	// Break the store AFTER migrations, so the provenance write errors while the
	// download itself is untouched.
	if err := srv.store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	dest := filepath.Join(comfyModels, "ultralytics", "bbox", "face_yolov9c.pt")
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatal(err)
	}
	pd := pendingDownload{
		FileName: "face_yolov9c.pt", URL: m.URL, DestPath: dest,
		ExpectedSHA256: m.SHA256, SourceHF: true,
		HFRepo: m.Repo, HFPath: m.Path, HFRevision: m.Revision,
	}
	if err := srv.downloadModelFile(context.Background(), pd, func(string) {}); err != nil {
		t.Fatalf("download must succeed despite an unrecordable provenance: %v", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("file not written: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("dest content = %q, want %q", got, body)
	}
}

// TestHFProvenanceNotRecordedWhenDestExists: an already-present destination is a
// success for the run, but we did NOT write those bytes — so we cannot claim they
// came from HuggingFace.
func TestHFProvenanceNotRecordedWhenDestExists(t *testing.T) {
	body := []byte("BYTES")
	m := curatedMatch(body)
	fake := &fakeHFClient{match: m, ok: true, body: body}
	srv, comfyModels := newHFServer(t, fake)

	dest := filepath.Join(comfyModels, "ultralytics", "bbox", "face_yolov9c.pt")
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatal(err)
	}
	// Somebody else's bytes are already there.
	if err := os.WriteFile(dest, []byte("PRE-EXISTING-DIFFERENT-BYTES"), 0o644); err != nil {
		t.Fatal(err)
	}

	pd := pendingDownload{
		FileName: "face_yolov9c.pt", URL: m.URL, DestPath: dest,
		ExpectedSHA256: m.SHA256, SourceHF: true,
		HFRepo: m.Repo, HFPath: m.Path, HFRevision: m.Revision,
	}
	if err := srv.downloadModelFile(context.Background(), pd, func(string) {}); err != nil {
		t.Fatalf("an already-present destination is not an error: %v", err)
	}
	if n := countProvenance(t, srv); n != 0 {
		t.Fatalf("hf_provenance rows = %d, want 0 for a file we did not write", n)
	}
}
