package library

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// fakeReader is an in-memory civitai.Reader for driving the matcher without a
// network. Only GetModelVersionByHash is exercised; the rest satisfy the
// interface.
type fakeReader struct {
	byHash map[string]*civitai.ModelVersionDetail
	// failN returns ErrRateLimited this many times before serving a real answer
	// (simulates a transient rate limit that eventually clears).
	failN int
	// alwaysErr, when set, is returned for every by-hash call.
	alwaysErr error
	calls     int
}

func (f *fakeReader) GetModelVersionByHash(_ context.Context, hash string) (*civitai.ModelVersionDetail, []byte, error) {
	f.calls++
	if f.failN > 0 {
		f.failN--
		return nil, nil, civitai.ErrRateLimited
	}
	if f.alwaysErr != nil {
		return nil, nil, f.alwaysErr
	}
	if v, ok := f.byHash[strings.ToLower(hash)]; ok {
		return v, nil, nil
	}
	return nil, nil, civitai.ErrNotFound
}

func (f *fakeReader) GetModel(context.Context, string) (*civitai.ModelDetail, []byte, error) {
	return nil, nil, civitai.ErrNotFound
}
func (f *fakeReader) GetModelVersion(context.Context, string) (*civitai.ModelVersionDetail, []byte, error) {
	return nil, nil, civitai.ErrNotFound
}
func (f *fakeReader) SearchModels(context.Context, url.Values) (*civitai.ModelSearchResult, error) {
	return &civitai.ModelSearchResult{}, nil
}
func (f *fakeReader) SearchCreators(context.Context, url.Values) (*civitai.CreatorSearchResult, error) {
	return &civitai.CreatorSearchResult{}, nil
}
func (f *fakeReader) SearchImages(context.Context, url.Values) (*civitai.ImageSearchResult, error) {
	return &civitai.ImageSearchResult{}, nil
}

// panicReader fails the test if any by-hash call is made (used to assert the
// offline/no-remote path never touches the API).
type panicReader struct{ fakeReader }

func (p *panicReader) GetModelVersionByHash(context.Context, string) (*civitai.ModelVersionDetail, []byte, error) {
	panic("reader must not be called")
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

// writeFile creates a file (and parent dirs) with the given content.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func version(id, modelID int, sha string) *civitai.ModelVersionDetail {
	return &civitai.ModelVersionDetail{
		ID: id, ModelID: modelID,
		Files: []civitai.ModelVersionFile{{Hashes: civitai.FileHashes{SHA256: sha, AutoV2: "av2-" + sha}}},
	}
}

// versionMap builds a by-hash lookup (keys are lower-cased) from sha/version
// pairs.
func versionMap(pairs ...any) map[string]*civitai.ModelVersionDetail {
	m := map[string]*civitai.ModelVersionDetail{}
	for i := 0; i+1 < len(pairs); i += 2 {
		sha := strings.ToLower(pairs[i].(string))
		m[sha] = pairs[i+1].(*civitai.ModelVersionDetail)
	}
	return m
}

func TestWalkFindsModelFilesRecursivelyAndSkipsTrashAndHidden(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.safetensors"), "a")
	writeFile(t, filepath.Join(root, "sub", "b.ckpt"), "b")
	writeFile(t, filepath.Join(root, "sub", "notes.txt"), "ignore me")
	writeFile(t, filepath.Join(root, "c.png"), "img")                       // preview bucket, not a model
	writeFile(t, filepath.Join(root, "d.safetensors.part"), "p")            // partial
	writeFile(t, filepath.Join(root, "e.civitai.info"), "{}")               // sidecar
	writeFile(t, filepath.Join(root, ".trash", "old.safetensors"), "trash") // must be skipped
	writeFile(t, filepath.Join(root, ".hidden", "h.safetensors"), "hidden") // hidden dir skipped

	sc := NewScanner(newTestStore(t), nil, Options{ModelRoot: root, NoRemote: true}, nil)
	wr, err := sc.walk()
	if err != nil {
		t.Fatal(err)
	}

	if len(wr.modelFiles) != 2 {
		t.Fatalf("model files = %d (%v), want 2", len(wr.modelFiles), wr.modelFiles)
	}
	for _, m := range wr.modelFiles {
		if strings.Contains(m, ".trash") || strings.Contains(m, ".hidden") {
			t.Errorf("walk should skip trash/hidden dirs, got %s", m)
		}
	}
	if len(wr.parts) != 1 || len(wr.infos) != 1 || len(wr.previews) != 1 {
		t.Errorf("sidecar collection: parts=%d infos=%d previews=%d, want 1/1/1",
			len(wr.parts), len(wr.infos), len(wr.previews))
	}
}

// TestHashCacheSkipsUnchangedFiles asserts the incremental cache: an unchanged
// file (size+mtime) is NOT re-hashed on a re-scan, while a changed file is.
func TestHashCacheSkipsUnchangedFiles(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "model.safetensors")
	writeFile(t, path, "original-bytes")

	st := newTestStore(t)
	sc := NewScanner(st, nil, Options{ModelRoot: root, NoRemote: true}, nil)

	var hashCalls int
	sc.hashFn = func(p string) (string, error) {
		hashCalls++
		return "hash-of-" + filepath.Base(p), nil
	}

	// First scan: file is hashed once.
	if _, err := sc.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if hashCalls != 1 {
		t.Fatalf("first scan hashCalls = %d, want 1", hashCalls)
	}

	// Second scan, file unchanged: cache hit, NOT re-hashed.
	if _, err := sc.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if hashCalls != 1 {
		t.Fatalf("re-scan of unchanged file hashCalls = %d, want 1 (cache hit)", hashCalls)
	}

	// Change the size: cache miss, re-hashed.
	writeFile(t, path, "original-bytes-now-longer")
	if _, err := sc.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if hashCalls != 2 {
		t.Fatalf("re-scan of size-changed file hashCalls = %d, want 2", hashCalls)
	}

	// Change only the mtime (content and size untouched): cache miss, re-hashed.
	future := time.Now().Add(2 * time.Hour)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatal(err)
	}
	if _, err := sc.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if hashCalls != 3 {
		t.Fatalf("re-scan of mtime-changed file hashCalls = %d, want 3", hashCalls)
	}
}

// TestScanRecordsUnmatchedNeverAsCandidate confirms an unmatched file is
// recorded and surfaced but never flagged for deletion.
func TestScanRecordsUnmatchedNeverAsCandidate(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "orphan.safetensors"), "no match")

	st := newTestStore(t)
	fr := &fakeReader{} // every hash -> ErrNotFound
	sc := NewScanner(st, fr, Options{ModelRoot: root}, nil)
	sc.hashFn = func(p string) (string, error) { return "orphanhash", nil }

	report, err := sc.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.Unmatched != 1 || report.Matched != 0 {
		t.Fatalf("unmatched=%d matched=%d, want 1/0", report.Unmatched, report.Matched)
	}
	if len(report.Candidates) != 0 {
		t.Fatalf("unmatched file must never be a candidate, got %d", len(report.Candidates))
	}
}
