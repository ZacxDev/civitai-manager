//go:build integration

package integration

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
	"github.com/ZacxDev/civitai-manager/internal/library"
	"github.com/ZacxDev/civitai-manager/internal/queue"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// Test 7: Library scan positive-match, live. Fakes can't reach a real by-hash
// response, so this downloads the small CIVITAI_TEST_DOWNLOAD_VERSION_ID, then
// runs the REAL library.Scanner over that directory and asserts the scanner's
// matcher glue resolves the file to the correct model/version via the live
// /model-versions/by-hash endpoint — the one library path the unit tests stub.
//
// Guarded by CIVITAI_INTEGRATION_DOWNLOAD=1 because it transfers real bytes.
func TestIntegration_LibraryScanMatchesLive(t *testing.T) {
	requireIntegration(t)
	if os.Getenv("CIVITAI_INTEGRATION_DOWNLOAD") == "" {
		t.Skip("set CIVITAI_INTEGRATION_DOWNLOAD=1 to run the real-bytes library scan test")
	}
	c := newClient()
	ctx := testContext(t)

	vid := downloadVersionID()
	vd, _, err := c.GetModelVersion(ctx, vid)
	if err != nil {
		t.Fatalf("GetModelVersion(%s): %v", vid, err)
	}
	file := civitai.SelectFile(vd.Files, "")
	if file == nil || file.Hashes.SHA256 == "" {
		t.Skipf("version %s has no primary file with a SHA256", vid)
	}
	if file.SizeKB > 500_000 {
		t.Fatalf("primary file %q is %.0f KB; override CIVITAI_TEST_DOWNLOAD_VERSION_ID with a SMALL file", file.Name, file.SizeKB)
	}

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

	st, err := store.Open(filepath.Join(t.TempDir(), "lib.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	if _, _, err := st.Enqueue(store.QueueItem{
		ModelID: vd.ModelID, VersionID: vd.ID, FileID: file.ID, FileName: file.Name,
		DownloadURL: downloadURL, DestPath: dest, Status: store.StatusQueued,
		SizeKB: file.SizeKB, SHA256Expected: file.Hashes.SHA256,
	}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	// Download the real bytes through the worker.
	w := queue.New(st, c, c, nil)
	if ok, err := w.ProcessOne(ctx); err != nil || !ok {
		t.Fatalf("ProcessOne: ok=%v err=%v", ok, err)
	}

	// The worker wrote a .civitai.info sidecar; remove it so the scan is forced
	// down the LIVE by-hash path rather than the sidecar short-circuit.
	_ = os.Remove(civitai.SidecarBase(dest) + ".civitai.info")

	// Fresh store so the scanner cannot reuse the worker's index row: it must
	// hash and match from scratch, hitting the live API.
	scanStore, err := store.Open(filepath.Join(t.TempDir(), "scan.db"))
	if err != nil {
		t.Fatalf("open scan store: %v", err)
	}
	t.Cleanup(func() { _ = scanStore.Close() })

	sc := library.NewScanner(scanStore, c, library.Options{ModelRoot: root}, nil)
	report, err := sc.Scan(ctx)
	if err != nil {
		t.Fatalf("library scan: %v", err)
	}
	if report.Matched < 1 {
		t.Fatalf("expected the downloaded file to match live by hash; report=%+v", report)
	}

	files, err := scanStore.ListLocalFiles()
	if err != nil {
		t.Fatal(err)
	}
	var matched *store.LocalFile
	for i := range files {
		if files[i].Path == dest {
			matched = &files[i]
		}
	}
	if matched == nil {
		t.Fatalf("scanned file %s not indexed", dest)
	}
	if matched.Status != store.LocalStatusMatched || matched.ModelID == nil || *matched.ModelID != vd.ModelID ||
		matched.VersionID == nil || *matched.VersionID != vd.ID {
		t.Fatalf("live match mismatch: status=%q model=%v version=%v want matched %d/%d",
			matched.Status, matched.ModelID, matched.VersionID, vd.ModelID, vd.ID)
	}
	t.Logf("library scan matched %s live by hash -> model %d version %d", file.Name, vd.ModelID, vd.ID)
}
