package library

import (
	"context"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// syncReader is a THREAD-SAFE civitai.Reader for the concurrent-scan tests: the
// scan now calls GetModelVersionByHash from multiple worker goroutines at once, so
// the reader's own bookkeeping (call counter) must be race-free under `go test
// -race`. The by-hash map is written once at construction and only read
// afterwards, so concurrent reads of it are safe.
type syncReader struct {
	byHash map[string]*civitai.ModelVersionDetail
	calls  int64 // atomic
}

func (r *syncReader) GetModelVersionByHash(_ context.Context, hash string) (*civitai.ModelVersionDetail, []byte, error) {
	atomic.AddInt64(&r.calls, 1)
	if v, ok := r.byHash[strings.ToLower(hash)]; ok {
		return v, nil, nil
	}
	return nil, nil, civitai.ErrNotFound
}

func (r *syncReader) GetModel(context.Context, string) (*civitai.ModelDetail, []byte, error) {
	return nil, nil, civitai.ErrNotFound
}
func (r *syncReader) GetModelVersion(context.Context, string) (*civitai.ModelVersionDetail, []byte, error) {
	return nil, nil, civitai.ErrNotFound
}
func (r *syncReader) SearchModels(context.Context, url.Values) (*civitai.ModelSearchResult, error) {
	return &civitai.ModelSearchResult{}, nil
}
func (r *syncReader) SearchCreators(context.Context, url.Values) (*civitai.CreatorSearchResult, error) {
	return &civitai.CreatorSearchResult{}, nil
}
func (r *syncReader) SearchImages(context.Context, url.Values) (*civitai.ImageSearchResult, error) {
	return &civitai.ImageSearchResult{}, nil
}

// contentHashFn is a deterministic injected hash: files whose base name starts
// with "dup" all collapse to one shared hash (so they are byte-identical
// duplicates for the analyzer), every other file gets a unique per-name hash.
func contentHashFn(p string) (string, error) {
	base := filepath.Base(p)
	if strings.HasPrefix(base, "dup") {
		return "dup-shared-hash", nil
	}
	return "hash-" + base, nil
}

// TestConcurrentScanParity proves the concurrent worker pool produces the SAME
// authoritative result as the intended sequential reference: over a mixed tree of
// matched, unmatched, and duplicate files, the report counters, the persisted
// local_files rows, and the candidate set are exactly what a correct scan must
// yield — regardless of the nondeterministic worker arrival order. This guards
// that concurrency did not drop, duplicate, or corrupt any result.
func TestConcurrentScanParity(t *testing.T) {
	root := t.TempDir()

	const nMatched = 30
	const nUnmatched = 12

	byHash := map[string]*civitai.ModelVersionDetail{}
	for i := 0; i < nMatched; i++ {
		name := fmt.Sprintf("m%02d.safetensors", i)
		writeFile(t, filepath.Join(root, "sub", name), name)
		// The injected hash of this file is "hash-<name>"; map it to a distinct version.
		byHash[strings.ToLower("hash-"+name)] = version(1000+i, 5000+i, "hash-"+name)
	}
	for i := 0; i < nUnmatched; i++ {
		name := fmt.Sprintf("u%02d.ckpt", i)
		writeFile(t, filepath.Join(root, name), name)
	}
	// A byte-identical duplicate PAIR (same shared hash), unmatched — offline
	// duplicate analysis still flags one of them.
	writeFile(t, filepath.Join(root, "dupA.safetensors"), "identical")
	writeFile(t, filepath.Join(root, "nested", "dupB.safetensors"), "identical")

	total := nMatched + nUnmatched + 2

	st := newTestStore(t)
	rd := &syncReader{byHash: byHash}
	sc := NewScanner(st, rd, Options{ModelRoot: root}, nil)
	sc.hashFn = contentHashFn

	report, err := sc.Scan(context.Background())
	if err != nil {
		t.Fatalf("scan: %v", err)
	}

	if report.FilesScanned != total {
		t.Errorf("FilesScanned=%d, want %d", report.FilesScanned, total)
	}
	if report.Matched != nMatched {
		t.Errorf("Matched=%d, want %d", report.Matched, nMatched)
	}
	// The two dup files hash to a value not in byHash → unmatched, plus the u* files.
	if wantUnmatched := nUnmatched + 2; report.Unmatched != wantUnmatched {
		t.Errorf("Unmatched=%d, want %d", report.Unmatched, wantUnmatched)
	}
	if report.Pending != 0 {
		t.Errorf("Pending=%d, want 0", report.Pending)
	}
	if report.Hashed != total || report.Reused != 0 {
		t.Errorf("first scan Hashed=%d Reused=%d, want %d/0", report.Hashed, report.Reused, total)
	}
	if len(report.Files) != total {
		t.Errorf("local_files model rows=%d, want %d", len(report.Files), total)
	}
	// Exactly one of the duplicate pair is flagged.
	if report.Duplicate != 1 || len(report.Candidates) != 1 {
		t.Fatalf("Duplicate=%d Candidates=%d, want 1/1", report.Duplicate, len(report.Candidates))
	}
	if got := report.Candidates[0].CandidateReason; got != store.CandidateDuplicate {
		t.Errorf("candidate reason=%q, want %q", got, store.CandidateDuplicate)
	}

	// The persisted rows must be a complete, correct set (order-independent).
	files, err := st.ListLocalFiles()
	if err != nil {
		t.Fatal(err)
	}
	matched := 0
	for _, f := range files {
		if f.Kind != store.LocalKindModel {
			continue
		}
		if f.Status == store.LocalStatusMatched {
			matched++
		}
	}
	if matched != nMatched {
		t.Errorf("persisted matched rows=%d, want %d", matched, nMatched)
	}
	// Every matched file resolved to the reader (its by-hash lookup ran).
	if got := atomic.LoadInt64(&rd.calls); got < int64(nMatched) {
		t.Errorf("by-hash calls=%d, want ≥%d (every matched file looked up)", got, nMatched)
	}
}

// TestConcurrentScanRaceWithStreamingOnFile is the key -race guard: a scan over a
// multi-file tree fires OnFile from multiple worker goroutines while a concurrent
// appender (modeling the web layer's scanMu-guarded onFile) collects them. Run
// under `go test -race`, this proves the shared report/seen-map and the concurrent
// OnFile fan-in are free of data races, and that every file is streamed exactly once.
func TestConcurrentScanRaceWithStreamingOnFile(t *testing.T) {
	root := t.TempDir()
	const n = 64
	for i := 0; i < n; i++ {
		writeFile(t, filepath.Join(root, "d", fmt.Sprintf("f%03d.safetensors", i)), fmt.Sprintf("bytes-%d", i))
	}

	var mu sync.Mutex
	seen := map[string]bool{}
	count := 0
	onFile := func(fr FileResult) {
		// A mutex-guarded appender, exactly like the web job's onFile.
		mu.Lock()
		seen[fr.Path] = true
		count++
		mu.Unlock()
	}

	sc := NewScanner(newTestStore(t), nil, Options{ModelRoot: root, NoRemote: true, OnFile: onFile}, nil)
	sc.hashFn = contentHashFn

	report, err := sc.Scan(context.Background())
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if count != n {
		t.Errorf("OnFile fired %d times, want %d", count, n)
	}
	if len(seen) != n {
		t.Errorf("distinct streamed files=%d, want %d (no drop/dup under concurrency)", len(seen), n)
	}
	if report.FilesScanned != n {
		t.Errorf("FilesScanned=%d, want %d", report.FilesScanned, n)
	}
}

// TestConcurrentScanContextCancelDuringProcessing proves prompt cancellation of
// the concurrent pool: cancelling mid-scan aborts with the ctx error, does NOT
// process every file (partial results are fine), and returns promptly (the workers
// drain rather than finishing the whole tree).
func TestConcurrentScanContextCancelDuringProcessing(t *testing.T) {
	root := t.TempDir()
	const n = 200
	for i := 0; i < n; i++ {
		writeFile(t, filepath.Join(root, fmt.Sprintf("f%03d.safetensors", i)), fmt.Sprintf("b%d", i))
	}

	ctx, cancel := context.WithCancel(context.Background())
	var hashCalls int64
	sc := NewScanner(newTestStore(t), nil, Options{ModelRoot: root, NoRemote: true}, nil)
	sc.hashFn = func(p string) (string, error) {
		// Cancel after the first file is hashed; remaining workers must observe the
		// cancellation and stop pulling new work.
		if atomic.AddInt64(&hashCalls, 1) == 1 {
			cancel()
		}
		return "h-" + filepath.Base(p), nil
	}

	_, err := sc.Scan(ctx)
	if err != context.Canceled {
		t.Fatalf("Scan err=%v, want context.Canceled", err)
	}
	// Partial: with a bounded pool only a handful of files can be in-flight when the
	// cancel lands, so the scan must NOT have hashed the whole tree.
	if got := atomic.LoadInt64(&hashCalls); got >= int64(n) {
		t.Errorf("cancelled scan hashed %d/%d files; expected an early, partial abort", got, n)
	}
}

// TestConcurrentScanOfflineSkipsAPI proves the offline/noRemote guard holds under
// concurrency: with NoRemote set, no worker ever calls the reader — even across
// many files hashed in parallel. A panicking reader makes any stray call fail loudly.
func TestConcurrentScanOfflineSkipsAPI(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 40; i++ {
		writeFile(t, filepath.Join(root, fmt.Sprintf("f%02d.safetensors", i)), fmt.Sprintf("b%d", i))
	}
	// NoRemote=true with a reader that panics if touched: proves the API is skipped.
	sc := NewScanner(newTestStore(t), &panicReader{}, Options{ModelRoot: root, NoRemote: true}, nil)
	sc.hashFn = contentHashFn

	report, err := sc.Scan(context.Background())
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if report.Matched != 0 || report.Unmatched != 40 {
		t.Errorf("offline scan matched=%d unmatched=%d, want 0/40", report.Matched, report.Unmatched)
	}
}
