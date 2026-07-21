//go:build integration

package integration

import (
	"bytes"
	"os"
	"strconv"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
	"github.com/ZacxDev/civitai-manager/internal/hashutil"
	"github.com/ZacxDev/civitai-manager/internal/queue"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// Test 6: Full authenticated download end-to-end (guarded by
// CIVITAI_INTEGRATION_DOWNLOAD=1 because it transfers real bytes).
//
// This runs the REAL download-worker path (internal/queue) for the small
// CIVITAI_TEST_DOWNLOAD_VERSION_ID into a t.TempDir(), and validates the single
// most-important unverified thing in the MVP: that CivitAI's signed-redirect +
// auth flow yields bytes whose computed SHA256 equals the API-reported hash. It
// also asserts the sidecars are written and the token never appears in logs.
func TestIntegration_FullDownload(t *testing.T) {
	requireIntegration(t)
	if os.Getenv("CIVITAI_INTEGRATION_DOWNLOAD") == "" {
		t.Skip("set CIVITAI_INTEGRATION_DOWNLOAD=1 to run the real-bytes download test")
	}
	tok := requireToken(t)
	c := newClient()
	ctx := testContext(t)

	// Resolve the small version and its primary file.
	vid := downloadVersionID()
	vd, _, err := c.GetModelVersion(ctx, vid)
	if err != nil {
		t.Fatalf("GetModelVersion(%s): %v", vid, err)
	}
	file := civitai.SelectFile(vd.Files, "")
	if file == nil {
		t.Fatalf("version %s has no downloadable file", vid)
	}
	if file.Hashes.SHA256 == "" {
		t.Fatalf("version %s primary file %q has no SHA256 to verify against", vid, file.Name)
	}
	if file.SizeKB > 500_000 {
		// ~500 MB guard: refuse to accidentally pull a multi-GB checkpoint if the
		// configured version id points at something big.
		t.Fatalf("primary file %q is %.0f KB; override CIVITAI_TEST_DOWNLOAD_VERSION_ID with a SMALL file", file.Name, file.SizeKB)
	}
	t.Logf("downloading version %s file %q (%.1f KB, sha256=%s)", vid, file.Name, file.SizeKB, file.Hashes.SHA256)

	// Resolve model metadata for the on-disk layout.
	m, _, err := c.GetModel(ctx, strconv.Itoa(vd.ModelID))
	if err != nil {
		t.Fatalf("GetModel(%d): %v", vd.ModelID, err)
	}
	creator := ""
	if m.Creator != nil {
		creator = m.Creator.Username
	}

	root := t.TempDir()
	dest := civitai.DestPath(root, m.Type, creator, m.Name, vd.Name, file.Name)

	downloadURL := file.DownloadURL
	if downloadURL == "" {
		downloadURL = vd.DownloadURL
	}

	// Persist a queue row and drain it through the REAL worker.
	st, err := store.Open(t.TempDir() + "/download.db")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	if _, err := st.Enqueue(store.QueueItem{
		ModelID:        vd.ModelID,
		VersionID:      vd.ID,
		FileID:         file.ID,
		FileName:       file.Name,
		DownloadURL:    downloadURL,
		DestPath:       dest,
		Status:         store.StatusQueued,
		SizeKB:         file.SizeKB,
		SHA256Expected: file.Hashes.SHA256,
	}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	var logs bytes.Buffer
	// Pass the client as BOTH Downloader and Reader so sidecars are generated.
	w := queue.New(st, c, c, bufLogger(&logs))

	ok, err := w.ProcessOne(ctx)
	if err != nil {
		t.Fatalf("ProcessOne: %v", err)
	}
	if !ok {
		t.Fatal("ProcessOne reported an empty queue; expected one item")
	}

	// (a) The file landed at the expected layout path.
	if _, err := os.Stat(dest); err != nil {
		t.Fatalf("expected downloaded file at %s: %v", dest, err)
	}

	// (b) Its computed SHA256 equals the API-reported hash — the #1 thing to prove:
	// signed-redirect + auth + real bytes end-to-end.
	sum, err := hashutil.SumFile(dest)
	if err != nil {
		t.Fatalf("hash downloaded file: %v", err)
	}
	if !hashutil.Equal(sum, file.Hashes.SHA256) {
		t.Fatalf("SHA256 mismatch: downloaded=%s api=%s", sum, file.Hashes.SHA256)
	}
	t.Logf("downloaded %s: sha256=%s matches API hash", dest, sum)

	// (c) Sidecars were written. .civitai.info is always expected (raw JSON is
	// non-empty); the preview is expected only when the version carries an image.
	base := civitai.SidecarBase(dest)
	if _, err := os.Stat(base + ".civitai.info"); err != nil {
		t.Errorf("expected sidecar %s.civitai.info: %v", base, err)
	}
	if info, err := os.ReadFile(base + ".civitai.info"); err == nil {
		if civitai.FirstImageURL(info) != "" {
			if _, err := os.Stat(base + ".preview.png"); err != nil {
				t.Errorf("version has an image but preview sidecar missing: %v", err)
			} else {
				t.Logf("preview sidecar written: %s.preview.png", base)
			}
		} else {
			t.Logf("version has no image URL in .civitai.info; skipping preview assertion")
		}
	}

	// The token must never appear in emitted logs.
	assertNoTokenLeak(t, logs.String(), tok)
}
