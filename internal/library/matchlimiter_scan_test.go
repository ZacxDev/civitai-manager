package library

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// peakReader is a thread-safe civitai.Reader that records the MAXIMUM number of
// by-hash calls simultaneously in flight. To make overlap deterministic (no real
// sleeps), it gathers arriving calls with a cond var and only releases a group
// once `gate` calls are simultaneously inside — or once every expected call has
// arrived (so the final partial group never hangs). The peak it records is thus
// the true concurrency the limiter permitted.
type peakReader struct {
	fakeReader // satisfies the non-by-hash methods

	mu       sync.Mutex
	cond     *sync.Cond
	inFlight int
	peak     int
	arrived  int
	calls    int64
	gate     int // release a group once this many calls are gathered
	expected int // total by-hash calls this scan will make
	hits     map[string]*civitai.ModelVersionDetail
}

func newPeakReader(gate, expected int, hits map[string]*civitai.ModelVersionDetail) *peakReader {
	r := &peakReader{gate: gate, expected: expected, hits: hits}
	r.cond = sync.NewCond(&r.mu)
	return r
}

func (r *peakReader) GetModelVersionByHash(_ context.Context, hash string) (*civitai.ModelVersionDetail, []byte, error) {
	r.mu.Lock()
	r.arrived++
	r.inFlight++
	r.calls++
	if r.inFlight > r.peak {
		r.peak = r.inFlight
	}
	for r.inFlight < r.gate && r.arrived < r.expected {
		r.cond.Wait()
	}
	r.cond.Broadcast()
	r.inFlight--
	v := r.hits[strings.ToLower(hash)]
	r.mu.Unlock()
	if v == nil {
		return nil, nil, civitai.ErrNotFound
	}
	return v, nil, nil
}

func (r *peakReader) peakAndCalls() (int, int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.peak, r.calls
}

// TestScanMatchConcurrencyCapsInFlightByHash is the core burst guard: with 8 hash
// workers processing many files, the SHARED semaphore holds simultaneous by-hash
// calls at MatchConcurrency (3), never the 8-wide burst the pool could otherwise
// produce. Hashing stays 8-wide; only the remote match calls are capped.
func TestScanMatchConcurrencyCapsInFlightByHash(t *testing.T) {
	root := t.TempDir()
	const nFiles = 60
	byHash := map[string]*civitai.ModelVersionDetail{}
	for i := 0; i < nFiles; i++ {
		name := fmt.Sprintf("m%02d.safetensors", i)
		writeFile(t, filepath.Join(root, "d", name), name)
		byHash[strings.ToLower("hash-"+name)] = version(1000+i, 5000+i, "hash-"+name)
	}

	const matchCap = 3
	// The hash pool is min(NumCPU,8); the true overlap the limiter should permit is
	// min(matchCap, hashWorkers). Gate the reader on that so the test is robust on
	// low-core CI while still proving the cap holds.
	hashWorkers := runtime.NumCPU()
	if hashWorkers > scanWorkerCap {
		hashWorkers = scanWorkerCap
	}
	wantPeak := matchCap
	if hashWorkers < wantPeak {
		wantPeak = hashWorkers
	}

	rd := newPeakReader(wantPeak, nFiles, byHash)
	sc := NewScanner(newTestStore(t), rd, Options{ModelRoot: root, MatchConcurrency: matchCap}, nil)
	sc.hashFn = contentHashFn

	report, err := sc.Scan(context.Background())
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if report.Matched != nFiles {
		t.Fatalf("Matched=%d, want %d", report.Matched, nFiles)
	}

	peak, calls := rd.peakAndCalls()
	if calls != nFiles {
		t.Fatalf("by-hash calls=%d, want %d", calls, nFiles)
	}
	if peak > matchCap {
		t.Fatalf("peak in-flight by-hash=%d, EXCEEDS MatchConcurrency cap %d (burst not capped)", peak, matchCap)
	}
	if peak != wantPeak {
		t.Fatalf("peak in-flight by-hash=%d, want %d (limiter should overlap up to the cap)", peak, wantPeak)
	}
}

// TestScanCoordinated429BackoffEscalates drives matchFile for a single file with
// an injected advancing clock and a reader that is rate-limited K times then
// succeeds. The recorded sleeps prove the SHARED cooldown escalates 2m->4m->8m
// (one coordinated sequence), the file eventually matches, and the success resets
// the shared backoff so the next lookup starts clean.
func TestScanCoordinated429BackoffEscalates(t *testing.T) {
	root := t.TempDir()
	model := filepath.Join(root, "m.safetensors")
	writeFile(t, model, "weights")

	clk := newFixedClock()
	fr := &fakeReader{byHash: versionMap("abc", version(10, 100, "abc")), failN: 3}
	sc := NewScanner(newTestStore(t), fr, Options{ModelRoot: root}, nil)
	sc.hashFn = func(string) (string, error) { return "abc", nil }
	sc.nowFn = clk.Now
	sc.waitFn = clk.wait
	sc.maxHashRetries = 3 // 4 attempts: 3 rate-limited + 1 success

	res := sc.matchFile(context.Background(), model, "abc")
	if res.status != store.LocalStatusMatched {
		t.Fatalf("status=%q, want matched after coordinated backoff", res.status)
	}
	want := []time.Duration{2 * time.Minute, 4 * time.Minute, 8 * time.Minute}
	if len(clk.waited) != len(want) {
		t.Fatalf("slept %v, want the escalating sequence %v", clk.waited, want)
	}
	for i, w := range want {
		if clk.waited[i] != w {
			t.Fatalf("sleep[%d]=%s, want %s (escalating shared cooldown)", i, clk.waited[i], w)
		}
	}
	// Success reset the shared cooldown.
	if sc.matchLimiter.backoff != 0 || !sc.matchLimiter.cooldownUntil.IsZero() {
		t.Fatalf("shared cooldown not reset after success: backoff=%s until=%v",
			sc.matchLimiter.backoff, sc.matchLimiter.cooldownUntil)
	}
}

// TestScanCoordinated429PoolCompletes proves the WHOLE pool recovers from a burst
// of 429s: a reader rate-limited for the first K calls (globally, across all
// workers) then serving matches. Every file still matches, none is left pending,
// and the shared cooldown is reset once calls get through — i.e. the pool
// throttles cooperatively and drains, rather than each worker stalling.
func TestScanCoordinated429PoolCompletes(t *testing.T) {
	root := t.TempDir()
	const nFiles = 24
	byHash := map[string]*civitai.ModelVersionDetail{}
	for i := 0; i < nFiles; i++ {
		name := fmt.Sprintf("m%02d.safetensors", i)
		writeFile(t, filepath.Join(root, name), name)
		byHash[strings.ToLower("hash-"+name)] = version(1000+i, 5000+i, "hash-"+name)
	}

	// Rate-limited for the first several calls, then everything succeeds.
	rd := &syncRateLimitedReader{byHash: byHash, failFirst: 5}
	sc := NewScanner(newTestStore(t), rd, Options{ModelRoot: root, MatchConcurrency: 3}, nil)
	sc.hashFn = contentHashFn
	sc.waitFn = func(context.Context, time.Duration) {} // no real sleep; cooldown is instant
	sc.maxHashRetries = 6

	report, err := sc.Scan(context.Background())
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if report.Matched != nFiles {
		t.Fatalf("Matched=%d, want %d (pool must recover from the 429 burst)", report.Matched, nFiles)
	}
	if report.Pending != 0 {
		t.Fatalf("Pending=%d, want 0 (no file left stuck after the burst cleared)", report.Pending)
	}
	// The last calls succeeded, so the shared cooldown is reset.
	if sc.matchLimiter.backoff != 0 || !sc.matchLimiter.cooldownUntil.IsZero() {
		t.Fatalf("shared cooldown not reset after the pool recovered: backoff=%s", sc.matchLimiter.backoff)
	}
}

// syncRateLimitedReader is thread-safe and returns ErrRateLimited for the first
// failFirst by-hash calls (globally, across all workers) before serving matches —
// used to model a transient burst the whole pool must ride out.
type syncRateLimitedReader struct {
	fakeReader
	byHash    map[string]*civitai.ModelVersionDetail
	failFirst int64
	seen      int64
}

func (r *syncRateLimitedReader) GetModelVersionByHash(_ context.Context, hash string) (*civitai.ModelVersionDetail, []byte, error) {
	if atomic.AddInt64(&r.seen, 1) <= r.failFirst {
		return nil, nil, civitai.ErrRateLimited
	}
	if v, ok := r.byHash[strings.ToLower(hash)]; ok {
		return v, nil, nil
	}
	return nil, nil, civitai.ErrNotFound
}

// TestScanCtxCancelDuringCooldown proves a worker parked in the SHARED cooldown
// aborts promptly on ctx cancel rather than sleeping out the (minutes-long)
// backoff: an always-rate-limited reader opens a real 2m cooldown; cancelling the
// scan while a worker is cooling makes it return context.Canceled quickly with the
// file left pending — no leaked goroutine, no long block.
func TestScanCtxCancelDuringCooldown(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 8; i++ {
		writeFile(t, filepath.Join(root, fmt.Sprintf("m%02d.safetensors", i)), fmt.Sprintf("b%d", i))
	}

	rd := &fakeReaderAlways{err: civitai.ErrRateLimited}
	sc := NewScanner(newTestStore(t), rd, Options{ModelRoot: root, MatchConcurrency: 3}, nil)
	sc.hashFn = contentHashFn // nowFn stays real, so the 2m cooldown deadline is genuine

	entered := make(chan struct{}, 64)
	sc.waitFn = func(ctx context.Context, d time.Duration) {
		select {
		case entered <- struct{}{}:
		default:
		}
		sleepCtx(ctx, d) // honours ctx: a cancel wakes it immediately
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := sc.Scan(ctx); done <- err }()

	select {
	case <-entered: // a worker is now parked in the shared cooldown
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("no worker entered the cooldown wait")
	}
	cancel()

	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatalf("Scan err=%v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Scan did not abort promptly on ctx cancel during cooldown (still sleeping the backoff?)")
	}
}

// fakeReaderAlways is a thread-safe reader that always fails with a fixed error.
type fakeReaderAlways struct {
	fakeReader
	err error
}

func (r *fakeReaderAlways) GetModelVersionByHash(context.Context, string) (*civitai.ModelVersionDetail, []byte, error) {
	return nil, nil, r.err
}

// TestScanNoRemoteBypassesLimiter proves the offline path is entirely inert w.r.t.
// the limiter: no by-hash call is made (a panicking reader would blow up), and the
// shared cooldown is never touched — the limiter costs nothing offline.
func TestScanNoRemoteBypassesLimiter(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 30; i++ {
		writeFile(t, filepath.Join(root, fmt.Sprintf("f%02d.safetensors", i)), fmt.Sprintf("b%d", i))
	}
	sc := NewScanner(newTestStore(t), &panicReader{}, Options{ModelRoot: root, NoRemote: true}, nil)
	sc.hashFn = contentHashFn

	report, err := sc.Scan(context.Background())
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if report.Unmatched != 30 || report.Matched != 0 {
		t.Fatalf("offline scan matched=%d unmatched=%d, want 0/30", report.Matched, report.Unmatched)
	}
	// The limiter was never engaged: no cooldown opened, escalation untouched.
	if sc.matchLimiter.backoff != 0 || !sc.matchLimiter.cooldownUntil.IsZero() {
		t.Fatalf("NoRemote scan touched the limiter cooldown: backoff=%s", sc.matchLimiter.backoff)
	}
}

// TestScanMatchParityAcrossConcurrency proves the limiter changes only PACING,
// not WHAT matches: the same tree scanned at MatchConcurrency 1 and 8 yields
// byte-identical match results (status + ids per path).
func TestScanMatchParityAcrossConcurrency(t *testing.T) {
	build := func() (string, map[string]*civitai.ModelVersionDetail) {
		byHash := map[string]*civitai.ModelVersionDetail{}
		root := t.TempDir()
		for i := 0; i < 20; i++ {
			name := fmt.Sprintf("m%02d.safetensors", i)
			writeFile(t, filepath.Join(root, "s", name), name)
			byHash[strings.ToLower("hash-"+name)] = version(1000+i, 5000+i, "hash-"+name)
		}
		for i := 0; i < 8; i++ { // unmatched files (ErrNotFound)
			writeFile(t, filepath.Join(root, fmt.Sprintf("u%02d.ckpt", i)), fmt.Sprintf("u%d", i))
		}
		return root, byHash
	}

	run := func(concurrency int) map[string][3]string {
		root, byHash := build()
		rd := &syncReader{byHash: byHash}
		sc := NewScanner(newTestStore(t), rd, Options{ModelRoot: root, MatchConcurrency: concurrency}, nil)
		sc.hashFn = contentHashFn
		if _, err := sc.Scan(context.Background()); err != nil {
			t.Fatalf("scan(concurrency=%d): %v", concurrency, err)
		}
		files, err := sc.store.ListLocalFiles()
		if err != nil {
			t.Fatal(err)
		}
		out := map[string][3]string{}
		for _, f := range files {
			if f.Kind != store.LocalKindModel {
				continue
			}
			key := filepath.Base(f.Path)
			out[key] = [3]string{f.Status, idStr(f.ModelID), idStr(f.VersionID)}
		}
		return out
	}

	one := run(1)
	many := run(8)
	if len(one) != len(many) {
		t.Fatalf("row counts differ: %d vs %d", len(one), len(many))
	}
	for k, v := range one {
		if many[k] != v {
			t.Fatalf("match result for %s differs across concurrency: %v vs %v", k, v, many[k])
		}
	}
}

func idStr(p *int) string {
	if p == nil {
		return "-"
	}
	return fmt.Sprintf("%d", *p)
}
