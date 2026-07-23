package library

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/store"
)

// TestScanStreamsOnFile proves the streaming seam: Options.OnFile is invoked once
// per scanned model file, AFTER its index row is written, carrying the file's
// path/name/size/hash/status and a preview flag. It also proves the streamed
// count matches the report's FilesScanned and that OnFile being nil is a no-op
// (the default scan path).
func TestScanStreamsOnFile(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.safetensors"), "aaa")
	writeFile(t, filepath.Join(root, "sub", "b.ckpt"), "bbb")
	writeFile(t, filepath.Join(root, "c.safetensors"), "ccc")
	// A sibling preview for c only, so exactly one streamed result flags HasPreview.
	writeFile(t, filepath.Join(root, "c.preview.png"), "img")

	// OnFile now fires from MULTIPLE worker goroutines concurrently (the scan is a
	// bounded worker pool), so the appender MUST guard itself — mirroring the web
	// layer's scanMu. (Previously this append was unsynchronized, relying on the old
	// single-goroutine sequential scan; that reliance is what changed.)
	var mu sync.Mutex
	var got []FileResult
	sc := NewScanner(newTestStore(t), nil, Options{
		ModelRoot: root, NoRemote: true,
		OnFile: func(fr FileResult) {
			mu.Lock()
			got = append(got, fr)
			mu.Unlock()
		},
	}, nil)
	// Deterministic per-path hash so the streamed SHA256 is assertable.
	sc.hashFn = func(p string) (string, error) { return "hash-" + filepath.Base(p), nil }

	report, err := sc.Scan(context.Background())
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("OnFile called %d times, want 3 (one per model file): %+v", len(got), got)
	}
	if report.FilesScanned != len(got) {
		t.Errorf("FilesScanned=%d but streamed %d; the stream must match the report", report.FilesScanned, len(got))
	}

	byName := map[string]FileResult{}
	for _, fr := range got {
		byName[fr.Name] = fr
		if fr.Path == "" || fr.Name != filepath.Base(fr.Path) {
			t.Errorf("streamed result has inconsistent path/name: %+v", fr)
		}
		if fr.SHA256 != "hash-"+fr.Name {
			t.Errorf("streamed SHA256=%q, want hash-%s", fr.SHA256, fr.Name)
		}
		// NoRemote + no sidecar → every file is unmatched (never a false match).
		if fr.Status != store.LocalStatusUnmatched {
			t.Errorf("%s status=%q, want unmatched", fr.Name, fr.Status)
		}
	}
	for _, name := range []string{"a.safetensors", "b.ckpt", "c.safetensors"} {
		if _, ok := byName[name]; !ok {
			t.Errorf("expected a streamed result for %s", name)
		}
	}
	if !byName["c.safetensors"].HasPreview {
		t.Errorf("c.safetensors has a sibling .preview.png; HasPreview must be true")
	}
	if byName["a.safetensors"].HasPreview {
		t.Errorf("a.safetensors has no preview; HasPreview must be false")
	}

	// The streamed size must equal the on-disk size.
	fi, _ := os.Stat(filepath.Join(root, "a.safetensors"))
	if byName["a.safetensors"].SizeBytes != fi.Size() {
		t.Errorf("streamed size=%d, want %d", byName["a.safetensors"].SizeBytes, fi.Size())
	}
}

// TestScanReportsDiscoveredTotal proves the OnDiscovered seam: it fires EXACTLY
// ONCE with the total number of model files the walk found (the progress
// denominator), that the total equals the report's FilesScanned, and that it
// fires BEFORE any OnFile — the ordering the web progress line relies on ("N /
// total discovered"). Sidecars/previews are NOT counted; only model-weight files.
func TestScanReportsDiscoveredTotal(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.safetensors"), "aaa")
	writeFile(t, filepath.Join(root, "sub", "b.ckpt"), "bbb")
	writeFile(t, filepath.Join(root, "c.safetensors"), "ccc")
	// Sidecars/partials the walk classifies separately — they must NOT inflate the
	// discovered model-file total.
	writeFile(t, filepath.Join(root, "c.preview.png"), "img")
	writeFile(t, filepath.Join(root, "a.civitai.info"), "{}")

	var (
		mu             sync.Mutex
		discoveredN    = -1
		discoveredHits int
		onFileCount    int
		onFileBefore   int // OnFile calls that happened before OnDiscovered fired
	)
	sc := NewScanner(newTestStore(t), nil, Options{
		ModelRoot: root, NoRemote: true,
		OnDiscovered: func(total int) {
			mu.Lock()
			discoveredN = total
			discoveredHits++
			mu.Unlock()
		},
		OnFile: func(fr FileResult) {
			mu.Lock()
			onFileCount++
			if discoveredHits == 0 {
				onFileBefore++
			}
			mu.Unlock()
		},
	}, nil)
	sc.hashFn = func(p string) (string, error) { return "hash-" + filepath.Base(p), nil }

	report, err := sc.Scan(context.Background())
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if discoveredHits != 1 {
		t.Fatalf("OnDiscovered fired %d times, want exactly 1", discoveredHits)
	}
	if discoveredN != 3 {
		t.Errorf("discovered total=%d, want 3 (a.safetensors, sub/b.ckpt, c.safetensors — sidecars excluded)", discoveredN)
	}
	if discoveredN != report.FilesScanned {
		t.Errorf("discovered total=%d must equal report.FilesScanned=%d", discoveredN, report.FilesScanned)
	}
	if onFileCount != 3 {
		t.Errorf("OnFile fired %d times, want 3", onFileCount)
	}
	if onFileBefore != 0 {
		t.Errorf("OnDiscovered must fire BEFORE any OnFile; saw %d OnFile calls first", onFileBefore)
	}

	// A nil OnDiscovered is a harmless no-op (the default scan path).
	sc2 := NewScanner(newTestStore(t), nil, Options{ModelRoot: root, NoRemote: true}, nil)
	sc2.hashFn = func(p string) (string, error) { return "h", nil }
	if _, err := sc2.Scan(context.Background()); err != nil {
		t.Fatalf("scan with nil OnDiscovered: %v", err)
	}
}
