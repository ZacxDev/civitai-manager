package library

import (
	"context"
	"os"
	"path/filepath"
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

	var got []FileResult
	sc := NewScanner(newTestStore(t), nil, Options{
		ModelRoot: root, NoRemote: true,
		OnFile: func(fr FileResult) { got = append(got, fr) },
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
