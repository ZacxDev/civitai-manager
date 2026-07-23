package library

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// stubBatchReader is a civitai.Reader whose batch by-hash returns a configured
// response and records exactly which hashes were sent in which call — the seam
// for asserting one-batch-not-N, sidecar short-circuiting (no hash sent), and
// duplicate-hash / failure handling. It embeds fakeReader for the other methods.
type stubBatchReader struct {
	fakeReader
	mu    sync.Mutex
	sent  [][]string                     // hashes per GetModelVersionsByHashes call
	resp  map[string][]civitai.HashMatch // UPPER-cased hash -> matches to return
	err   error                          // when set, the whole batch fails
	calls int
}

func (r *stubBatchReader) GetModelVersionsByHashes(_ context.Context, hashes []string) ([]civitai.HashMatch, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	r.sent = append(r.sent, append([]string(nil), hashes...))
	if r.err != nil {
		return nil, r.err
	}
	var out []civitai.HashMatch
	for _, h := range hashes {
		out = append(out, r.resp[strings.ToUpper(h)]...)
	}
	return out, nil
}

func (r *stubBatchReader) snapshot() ([][]string, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sent, r.calls
}

// nameHashFn hashes a file to "hash-<basename>" deterministically (uppercased on
// the wire by the batch endpoint; our side keeps lowercase hex-shaped strings).
func nameHashFn(p string) (string, error) { return "hash-" + filepath.Base(p), nil }

// TestBatchScanParityWithPerFileOutcome proves the batch path yields the SAME
// local_files matched/unmatched rows + ScanReport counters the old per-file path
// would: a fake batch that matches a SUBSET, the rest are definitive misses.
func TestBatchScanParityWithPerFileOutcome(t *testing.T) {
	root := t.TempDir()
	const nMatched, nUnmatched = 15, 9
	byHash := map[string]*civitai.ModelVersionDetail{}
	for i := 0; i < nMatched; i++ {
		name := fmt.Sprintf("m%02d.safetensors", i)
		writeFile(t, filepath.Join(root, "d", name), name)
		byHash[strings.ToLower("hash-"+name)] = version(1000+i, 5000+i, "hash-"+name)
	}
	for i := 0; i < nUnmatched; i++ {
		writeFile(t, filepath.Join(root, fmt.Sprintf("u%02d.ckpt", i)), fmt.Sprintf("u%d", i))
	}

	st := newTestStore(t)
	fr := &fakeReader{byHash: byHash}
	sc := NewScanner(st, fr, Options{ModelRoot: root}, nil)
	sc.hashFn = nameHashFn

	report, err := sc.Scan(context.Background())
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if report.Matched != nMatched || report.Unmatched != nUnmatched || report.Pending != 0 {
		t.Fatalf("counts matched=%d unmatched=%d pending=%d, want %d/%d/0",
			report.Matched, report.Unmatched, report.Pending, nMatched, nUnmatched)
	}
	// Per-row parity: matched files carry model+version ids; unmatched carry none.
	files, err := st.ListLocalFiles()
	if err != nil {
		t.Fatal(err)
	}
	matched := 0
	for _, f := range files {
		if f.Kind != store.LocalKindModel {
			continue
		}
		switch f.Status {
		case store.LocalStatusMatched:
			matched++
			if f.ModelID == nil || f.VersionID == nil {
				t.Errorf("matched %s missing ids: model=%v version=%v", filepath.Base(f.Path), f.ModelID, f.VersionID)
			}
		case store.LocalStatusUnmatched:
			if f.ModelID != nil || f.VersionID != nil {
				t.Errorf("unmatched %s must carry no ids", filepath.Base(f.Path))
			}
		default:
			t.Errorf("%s unexpected status %q", filepath.Base(f.Path), f.Status)
		}
	}
	if matched != nMatched {
		t.Errorf("persisted matched rows=%d, want %d", matched, nMatched)
	}
}

// TestBatchScanOneCallNotN is the core seam: N files hashed → EXACTLY ONE batch
// call (N < 10k), never one call per file, and the retired per-file GET is never
// touched.
func TestBatchScanOneCallNotN(t *testing.T) {
	root := t.TempDir()
	const n = 50
	byHash := map[string]*civitai.ModelVersionDetail{}
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("m%02d.safetensors", i)
		writeFile(t, filepath.Join(root, name), name)
		byHash[strings.ToLower("hash-"+name)] = version(1000+i, 5000+i, "hash-"+name)
	}
	fr := &fakeReader{byHash: byHash}
	sc := NewScanner(newTestStore(t), fr, Options{ModelRoot: root}, nil)
	sc.hashFn = nameHashFn

	report, err := sc.Scan(context.Background())
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if report.Matched != n {
		t.Fatalf("Matched=%d, want %d", report.Matched, n)
	}
	if fr.batchCalls != 1 {
		t.Fatalf("batch calls=%d, want exactly 1 (one batch, not N=%d)", fr.batchCalls, n)
	}
	if fr.calls != 0 {
		t.Fatalf("per-file GetModelVersionByHash calls=%d, want 0", fr.calls)
	}
	if fr.batchHashes != n {
		t.Fatalf("hashes sent=%d, want %d (every file's SHA in the one batch)", fr.batchHashes, n)
	}
}

// TestBatchScanSidecarShortCircuitSendsNoHash proves a valid .civitai.info sidecar
// still resolves a file locally: its SHA is NOT sent in the batch (only the
// no-sidecar file's is), and the sidecar file is matched from the sidecar's ids.
func TestBatchScanSidecarShortCircuitSendsNoHash(t *testing.T) {
	root := t.TempDir()
	// File A: valid sidecar (declares this file's hash) → resolved locally.
	fileA := filepath.Join(root, "a.safetensors")
	writeFile(t, fileA, "a-weights")
	writeFile(t, filepath.Join(root, "a.civitai.info"),
		`{"id": 777, "modelId": 66, "files": [{"hashes": {"SHA256": "hash-a.safetensors"}}]}`)
	// File B: no sidecar → must go through the batch.
	fileB := filepath.Join(root, "b.safetensors")
	writeFile(t, fileB, "b-weights")

	rd := &stubBatchReader{resp: map[string][]civitai.HashMatch{
		strings.ToUpper("hash-b.safetensors"): {{ModelVersionID: 11, ModelID: 22, Hash: strings.ToUpper("hash-b.safetensors")}},
	}}
	sc := NewScanner(newTestStore(t), rd, Options{ModelRoot: root}, nil)
	sc.hashFn = nameHashFn

	report, err := sc.Scan(context.Background())
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if report.Matched != 2 {
		t.Fatalf("Matched=%d, want 2 (sidecar A + batch B)", report.Matched)
	}
	sent, calls := rd.snapshot()
	if calls != 1 {
		t.Fatalf("batch calls=%d, want 1", calls)
	}
	// Only B's hash was sent; A short-circuited on its sidecar.
	all := strings.Join(flatten(sent), ",")
	if strings.Contains(strings.ToLower(all), "hash-a.safetensors") {
		t.Errorf("sidecar-resolved file A's hash must not be sent in the batch; sent=%v", sent)
	}
	if !strings.Contains(strings.ToLower(all), "hash-b.safetensors") {
		t.Errorf("no-sidecar file B's hash must be sent in the batch; sent=%v", sent)
	}
}

// TestBatchScanOfflineSendsNoBatch proves --no-remote makes no batch call at all
// (a panicking reader would blow up), and every file is recorded unmatched.
func TestBatchScanOfflineSendsNoBatch(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 20; i++ {
		writeFile(t, filepath.Join(root, fmt.Sprintf("f%02d.safetensors", i)), fmt.Sprintf("b%d", i))
	}
	sc := NewScanner(newTestStore(t), &panicReader{}, Options{ModelRoot: root, NoRemote: true}, nil)
	sc.hashFn = nameHashFn

	report, err := sc.Scan(context.Background())
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if report.Matched != 0 || report.Unmatched != 20 {
		t.Fatalf("offline matched=%d unmatched=%d, want 0/20", report.Matched, report.Unmatched)
	}
}

// TestBatchScanFailedBatchLeavesPending proves a failed batch (after the SDK's own
// retries) leaves the affected files UnmatchedPending — never falsely unmatched or
// flagged: no candidate is derived from a pending file.
func TestBatchScanFailedBatchLeavesPending(t *testing.T) {
	root := t.TempDir()
	const n = 12
	for i := 0; i < n; i++ {
		writeFile(t, filepath.Join(root, fmt.Sprintf("m%02d.safetensors", i)), fmt.Sprintf("m%d", i))
	}
	rd := &stubBatchReader{err: civitai.ErrRateLimited}
	sc := NewScanner(newTestStore(t), rd, Options{ModelRoot: root}, nil)
	sc.hashFn = nameHashFn

	report, err := sc.Scan(context.Background())
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if report.Pending != n {
		t.Fatalf("Pending=%d, want %d (failed batch → pending)", report.Pending, n)
	}
	if report.Unmatched != 0 || report.Matched != 0 {
		t.Fatalf("failed batch must not resolve anything: matched=%d unmatched=%d", report.Matched, report.Unmatched)
	}
	if len(report.Candidates) != 0 {
		t.Fatalf("pending files must never be flagged, got %d candidates", len(report.Candidates))
	}
}

// TestBatchScanDuplicateHashNoPanic proves the map handles a hash the endpoint
// returns MULTIPLE times: two byte-identical files share one hash that resolves to
// two versions; the deterministic pick is the LOWEST version id and nothing panics.
func TestBatchScanDuplicateHashNoPanic(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "one.safetensors"), "identical")
	writeFile(t, filepath.Join(root, "two.safetensors"), "identical")

	const shared = "sharedhash"
	rd := &stubBatchReader{resp: map[string][]civitai.HashMatch{
		strings.ToUpper(shared): {
			{ModelVersionID: 20, ModelID: 2, Hash: strings.ToUpper(shared)},
			{ModelVersionID: 10, ModelID: 2, Hash: strings.ToUpper(shared)},
		},
	}}
	sc := NewScanner(newTestStore(t), rd, Options{ModelRoot: root}, nil)
	sc.hashFn = func(string) (string, error) { return shared, nil }

	report, err := sc.Scan(context.Background())
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if report.Matched != 2 {
		t.Fatalf("Matched=%d, want 2", report.Matched)
	}
	files, err := sc.store.ListLocalFiles()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if f.Kind != store.LocalKindModel {
			continue
		}
		if f.VersionID == nil || *f.VersionID != 10 {
			t.Errorf("%s version=%v, want the deterministic lowest (10)", filepath.Base(f.Path), f.VersionID)
		}
	}
}

// TestBatchScanCtxCancelAborts proves a ctx cancel during the batch aborts the
// scan promptly with context.Canceled and flags nothing (pending/partial is fine).
func TestBatchScanCtxCancelAborts(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 10; i++ {
		writeFile(t, filepath.Join(root, fmt.Sprintf("m%02d.safetensors", i)), fmt.Sprintf("m%d", i))
	}
	ctx, cancel := context.WithCancel(context.Background())
	// The batch reader cancels the scan and returns the ctx error, exactly as the
	// SDK does when ctx is cancelled mid-request.
	rd := &cancelBatchReader{cancel: cancel, ctx: ctx}
	sc := NewScanner(newTestStore(t), rd, Options{ModelRoot: root}, nil)
	sc.hashFn = nameHashFn

	_, err := sc.Scan(ctx)
	if err != context.Canceled {
		t.Fatalf("Scan err=%v, want context.Canceled", err)
	}
}

type cancelBatchReader struct {
	fakeReader
	cancel context.CancelFunc
	ctx    context.Context
}

func (r *cancelBatchReader) GetModelVersionsByHashes(context.Context, []string) ([]civitai.HashMatch, error) {
	r.cancel()
	return nil, r.ctx.Err()
}

func flatten(ss [][]string) []string {
	var out []string
	for _, s := range ss {
		out = append(out, s...)
	}
	return out
}
