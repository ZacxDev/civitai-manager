package library

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/store"
)

// analyzerScan sets up a scanner whose hashFn maps each path to a caller-chosen
// hash, then runs a full scan (walk+match+analyze) so the candidate flags are
// exercised end-to-end.
func analyzerScan(t *testing.T, root string, hashes map[string]string, fr *fakeReader) *ScanReport {
	t.Helper()
	sc := NewScanner(newTestStore(t), fr, Options{ModelRoot: root}, nil)
	sc.hashFn = func(p string) (string, error) { return hashes[p], nil }
	report, err := sc.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return report
}

func reasonByPath(cands []store.LocalFile) map[string]string {
	m := map[string]string{}
	for _, c := range cands {
		m[filepath.Base(c.Path)] = c.CandidateReason
	}
	return m
}

func TestAnalyzerSuperseded(t *testing.T) {
	root := t.TempDir()
	old := filepath.Join(root, "old.safetensors")
	new := filepath.Join(root, "new.safetensors")
	solo := filepath.Join(root, "solo.safetensors") // single-version model: never flagged
	writeFile(t, old, "old")
	writeFile(t, new, "new")
	writeFile(t, solo, "solo")

	hashes := map[string]string{old: "h-old", new: "h-new", solo: "h-solo"}
	fr := &fakeReader{byHash: versionMap(
		"h-old", version(10, 100, "h-old"), // model 100, version 10
		"h-new", version(20, 100, "h-new"), // model 100, version 20 (newer)
		"h-solo", version(5, 200, "h-solo"), // model 200, sole version
	)}

	report := analyzerScan(t, root, hashes, fr)
	got := reasonByPath(report.Candidates)
	if got["old.safetensors"] != store.CandidateSuperseded {
		t.Errorf("old should be superseded, got %q", got["old.safetensors"])
	}
	if _, flagged := got["new.safetensors"]; flagged {
		t.Errorf("newest local version must not be flagged")
	}
	if _, flagged := got["solo.safetensors"]; flagged {
		t.Errorf("single-version model must not be flagged")
	}
}

func TestAnalyzerDuplicateKeepsExactlyOne(t *testing.T) {
	root := t.TempDir()
	// Same bytes (same hash) at two paths; the shorter path is the keeper.
	keep := filepath.Join(root, "a.safetensors")
	dupe := filepath.Join(root, "sub", "aa.safetensors")
	writeFile(t, keep, "dup")
	writeFile(t, dupe, "dup")

	hashes := map[string]string{keep: "same", dupe: "same"}
	fr := &fakeReader{byHash: versionMap("same", version(30, 300, "same"))}

	report := analyzerScan(t, root, hashes, fr)
	if len(report.Candidates) != 1 {
		t.Fatalf("expected exactly one duplicate flagged, got %d", len(report.Candidates))
	}
	if got := reasonByPath(report.Candidates); got["aa.safetensors"] != store.CandidateDuplicate {
		t.Fatalf("the longer-path copy should be flagged duplicate, got %v", got)
	}
}

// TestAnalyzerDuplicateOfflineUnmatched asserts that duplicate detection is a
// pure local-hash signal: two byte-identical files are flagged even offline,
// when neither is matched to CivitAI.
func TestAnalyzerDuplicateOfflineUnmatched(t *testing.T) {
	root := t.TempDir()
	a := filepath.Join(root, "a.safetensors")
	b := filepath.Join(root, "longer-name-b.safetensors")
	writeFile(t, a, "identical")
	writeFile(t, b, "identical")

	// NoRemote: no API, both files end up unmatched — but still duplicates.
	sc := NewScanner(newTestStore(t), nil, Options{ModelRoot: root, NoRemote: true}, nil)
	hashes := map[string]string{a: "dup", b: "dup"}
	sc.hashFn = func(p string) (string, error) { return hashes[p], nil }
	report, err := sc.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.Duplicate != 1 {
		t.Fatalf("offline duplicate count = %d, want 1", report.Duplicate)
	}
	if got := reasonByPath(report.Candidates); got["longer-name-b.safetensors"] != store.CandidateDuplicate {
		t.Fatalf("the longer-path unmatched copy should be flagged duplicate, got %v", got)
	}
}

func TestAnalyzerBroken(t *testing.T) {
	root := t.TempDir()

	// Abandoned .part (no active download row) -> broken.
	writeFile(t, filepath.Join(root, "abandoned.safetensors.part"), "partial")
	// Empty .civitai.info -> broken.
	writeFile(t, filepath.Join(root, "empty.civitai.info"), "  \n ")
	// Orphan preview (no sibling model file) -> broken.
	writeFile(t, filepath.Join(root, "ghost.preview.png"), "img")
	// Healthy preview WITH a sibling model file -> NOT broken.
	writeFile(t, filepath.Join(root, "real.safetensors"), "weights")
	writeFile(t, filepath.Join(root, "real.preview.png"), "img")

	fr := &fakeReader{} // real.safetensors -> unmatched (fine; not a candidate)
	sc := NewScanner(newTestStore(t), fr, Options{ModelRoot: root}, nil)
	sc.hashFn = func(string) (string, error) { return "x", nil }
	report, err := sc.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	got := reasonByPath(report.Candidates)
	for _, want := range []string{"abandoned.safetensors.part", "empty.civitai.info", "ghost.preview.png"} {
		if got[want] != store.CandidateBroken {
			t.Errorf("%s should be broken, got %q", want, got[want])
		}
	}
	if _, flagged := got["real.preview.png"]; flagged {
		t.Errorf("a preview with a sibling model file must not be flagged")
	}
	if report.Broken != 3 {
		t.Errorf("broken count = %d, want 3", report.Broken)
	}
}

func TestAnalyzerActivePartNotBroken(t *testing.T) {
	root := t.TempDir()
	dest := filepath.Join(root, "downloading.safetensors")
	writeFile(t, dest+".part", "in progress")

	st := newTestStore(t)
	// An in-flight download row targeting dest keeps the .part from being broken.
	if _, err := st.Enqueue(store.QueueItem{
		ModelID: 1, VersionID: 1, FileID: 1, FileName: "downloading.safetensors",
		DownloadURL: "http://x", DestPath: dest, Status: store.StatusDownloading,
	}); err != nil {
		t.Fatal(err)
	}

	sc := NewScanner(st, &fakeReader{}, Options{ModelRoot: root}, nil)
	sc.hashFn = func(string) (string, error) { return "x", nil }
	report, err := sc.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.Broken != 0 {
		t.Fatalf("a .part with an active download must not be broken, got %d", report.Broken)
	}
}

// TestAnalyzerReclaimableSumsCandidates asserts the reclaimable total is the sum
// of flagged candidate sizes.
func TestAnalyzerReclaimableSumsCandidates(t *testing.T) {
	root := t.TempDir()
	keep := filepath.Join(root, "a.safetensors")
	dupe := filepath.Join(root, "duplicate-bb.safetensors")
	writeFile(t, keep, "0123456789") // 10 bytes
	writeFile(t, dupe, "0123456789") // 10 bytes, same hash -> duplicate flagged

	hashes := map[string]string{keep: "same", dupe: "same"}
	fr := &fakeReader{byHash: versionMap("same", version(1, 1, "same"))}
	report := analyzerScan(t, root, hashes, fr)

	var want int64
	for _, c := range report.Candidates {
		want += c.SizeBytes
	}
	if report.Reclaimable != want || report.Reclaimable != 10 {
		t.Fatalf("reclaimable = %d, want %d (10)", report.Reclaimable, want)
	}
}
