package web

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/diskusage"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// hungMount is the deterministic gate these tests are built on.
//
// 🔴 IT IS A CHANNEL, NOT A SLEEP, AND THAT IS THE WHOLE DESIGN. The failure it
// models — statfs on a wedged NFS/SMB mount — is UNINTERRUPTIBLE: the call never
// returns. A stub that slept for "longer than the timeout" would be racing the
// scheduler, and a green result would mean nothing more than "this machine was
// fast enough today". Blocking on a channel the test controls reproduces the
// real thing exactly: the probe CANNOT complete until the test says so, so the
// watchdog is the only way the request can ever finish.
type hungMount struct {
	// release is closed by Cleanup. Until then every probe of a hung path parks.
	release chan struct{}
	// hung is the set of paths that block; anything else answers immediately.
	hung map[string]bool
	// started counts probes that ENTERED the stub, finished counts those that
	// LEFT it. finished is what proves the handler did not wait: a request that
	// returns while finished == 0 cannot have been blocked on the probe.
	mu       sync.Mutex
	started  map[string]int
	finished map[string]int
}

func newHungMount(t *testing.T, hung ...string) *hungMount {
	t.Helper()
	m := &hungMount{
		release:  make(chan struct{}),
		hung:     map[string]bool{},
		started:  map[string]int{},
		finished: map[string]int{},
	}
	for _, p := range hung {
		m.hung[p] = true
	}
	// Unwedge everything at the end of the test, so no goroutine outlives it.
	t.Cleanup(func() { close(m.release) })
	return m
}

func (m *hungMount) stat(path string) (diskusage.Usage, error) {
	m.mu.Lock()
	m.started[path]++
	blocked := m.hung[path]
	m.mu.Unlock()

	if blocked {
		<-m.release
		m.mu.Lock()
		m.finished[path]++
		m.mu.Unlock()
		// After the release the mount "answers" — with an error, so a late
		// completion can never be mistaken for the healthy branch.
		return diskusage.Usage{}, errors.New("mount released by the test")
	}
	m.mu.Lock()
	m.finished[path]++
	m.mu.Unlock()
	return diskusage.Usage{Total: 4000, Free: 1000, Used: 3000}, nil
}

func (m *hungMount) count(which map[string]int, path string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return which[path]
}

func (m *hungMount) startedFor(path string) int  { return m.count(m.started, path) }
func (m *hungMount) finishedFor(path string) int { return m.count(m.finished, path) }

// newHungDisksServer builds a loopback /disks server over a REAL model root plus
// the given extra library paths, with a short probe timeout so the watchdog is
// exercised in test time. The timeout is short but the gate above means it is
// still deterministic: the probe can never win the race, whatever the machine.
func newHungDisksServer(t *testing.T, extra ...string) *Server {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	srv := NewServer(st, stubReader{}, stubSubscriber{}, Config{
		Addr: "127.0.0.1:8787", ModelRoot: t.TempDir(), LibraryPaths: extra,
		DefaultPollInterval: time.Hour,
	}, nil)
	srv.diskProbeTimeout = 50 * time.Millisecond
	return srv
}

// rawGet issues a GET without touching *testing.T, so it is safe to call from a
// goroutine (t.Fatalf from a non-test goroutine is undefined behaviour).
func rawGet(srv *Server, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

// TestDisksSurvivesAHungMount is the headline guard: one wedged directory must
// not cost the page.
//
// THE BUG IT GUARDS. diskRows() used to call statFn SERIALLY on the request
// goroutine with no timeout. A hung NFS/CIFS mount blocks statfs in
// uninterruptible sleep, so /disks never responded at all and every reload
// stacked another wedged request goroutine on top.
//
// 🔴 THE PROOF IS `finished == 0`, NOT A STOPWATCH. Timing how long the request
// took would be a measurement of this machine. Asserting that the response came
// back while the probe was still INSIDE the stub is a statement about causality:
// the handler cannot have waited for a call that has not returned.
func TestDisksSurvivesAHungMount(t *testing.T) {
	hung := t.TempDir()
	srv := newHungDisksServer(t, hung)
	m := newHungMount(t, hung)
	srv.diskStatFn = m.stat

	rec := rawGet(srv, "/disks")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /disks = %d, want 200 — a hung mount must not error the page", rec.Code)
	}
	body := rec.Body.String()

	// FIXTURE REACH: the hung path really was probed, and the probe really is
	// still stuck. Without this the assertions below would be satisfied by a
	// server that never probed anything.
	if got := m.startedFor(hung); got != 1 {
		t.Fatalf("the hung path was probed %d times, want exactly 1 — the timeout branch was never reached", got)
	}
	if got := m.finishedFor(hung); got != 0 {
		t.Fatalf("the hung probe RETURNED (%d completions) before the response — the fixture is not "+
			"reproducing an unresponsive mount, so this test proves nothing about the watchdog", got)
	}

	// The hung row renders as unknown, with the reason that names the MOUNT
	// rather than the path (a hung mount is neither missing nor unsupported).
	if !strings.Contains(body, diskProbeTimeoutText) {
		t.Errorf("the hung row must say %q:\n%s", diskProbeTimeoutText, firstN(body, 2500))
	}
	if !strings.Contains(body, hung) {
		t.Errorf("the hung directory %q must still get a row:\n%s", hung, firstN(body, 2500))
	}
	if !strings.Contains(body, "Capacity unknown") {
		t.Errorf("the hung row must render the unknown-capacity shape:\n%s", firstN(body, 2500))
	}

	// MIXED RENDER: the healthy directories in the SAME request still report real
	// figures. A watchdog that abandoned the whole collection would pass every
	// assertion above and fail here.
	if !strings.Contains(body, "free of") {
		t.Errorf("the healthy rows must still report real figures alongside the hung one:\n%s", firstN(body, 2500))
	}
	if n := strings.Count(body, `class="cm-meter"`); n != 2 {
		t.Errorf("expected 2 meters (model root + the derived trash dir, both healthy), got %d — "+
			"the hung row must not render one and the healthy ones must not be lost", n)
	}
}

// TestDisksMemoizesAHungMount is the guard on the part that actually bounds the
// damage.
//
// 🔴 A TIMEOUT ALONE DOES NOT FIX THIS BUG. The abandoned probe goroutine is
// unkillable — it is parked in an uninterruptible syscall — so without a memo
// every reload of /disks spawns another one and a user refreshing a page they
// cannot read leaks a goroutine per refresh. The memo turns "one per request"
// into "one per TTL window".
//
// It counts at the diskStatFn seam, one level BELOW the memo, so the assertion
// is about syscalls issued rather than about markup: a version that consulted
// the memo only to pick the reason string, while still firing the probe, is red
// here.
func TestDisksMemoizesAHungMount(t *testing.T) {
	hung := t.TempDir()
	srv := newHungDisksServer(t, hung)
	m := newHungMount(t, hung)
	srv.diskStatFn = m.stat
	root := srv.cfg.ModelRoot

	if rec := rawGet(srv, "/disks"); rec.Code != http.StatusOK {
		t.Fatalf("first GET /disks = %d", rec.Code)
	}
	if got := m.startedFor(hung); got != 1 {
		t.Fatalf("fixture is wrong: the first request probed the hung path %d times, want 1", got)
	}

	// --- the memo: a second load must not issue a second probe ----------------
	rec := rawGet(srv, "/disks")
	if rec.Code != http.StatusOK {
		t.Fatalf("second GET /disks = %d", rec.Code)
	}
	if got := m.startedFor(hung); got != 1 {
		t.Errorf("the second load probed the hung path again (%d probes total) — each reload would "+
			"leak another goroutine parked in an uncancellable statfs", got)
	}
	// CONTROL: the second request was a real render that DID probe. Without this
	// the assertion above would also pass for a request that probed nothing at
	// all (a broken gate, a cached page, an early return).
	if got := m.startedFor(root); got != 2 {
		t.Fatalf("fixture is wrong: the healthy model root was probed %d times across two loads, want 2 — "+
			"the second request did not really re-probe, so the memo assertion is vacuous", got)
	}
	// And the row still explains itself from the memo.
	if !strings.Contains(rec.Body.String(), diskProbeTimeoutText) {
		t.Errorf("a memoised row must still carry the timeout reason:\n%s", firstN(rec.Body.String(), 2500))
	}

	// --- the TTL: the memo is a window, not a tombstone -----------------------
	// A mount that comes back must be measured again. The stored timestamp is
	// moved into the past directly rather than sleeping out the TTL, so this is a
	// statement about the expiry logic with no clock race in it.
	srv.diskProbeMu.Lock()
	srv.diskHung[hung] = time.Now().Add(-2 * srv.diskProbeMemoTTL)
	srv.diskProbeMu.Unlock()

	if rec := rawGet(srv, "/disks"); rec.Code != http.StatusOK {
		t.Fatalf("third GET /disks = %d", rec.Code)
	}
	if got := m.startedFor(hung); got != 2 {
		t.Errorf("after the memo TTL expired the path was probed %d times total, want 2 — an entry "+
			"that never expires means a mount that recovers is never measured again", got)
	}
}

// liveProbeGoroutines counts the goroutines currently running probeDisk's inner
// probe function.
//
// It reads the goroutine dump and matches THAT ONE FRAME rather than calling
// runtime.NumGoroutine(), which is a process-wide number every other test in the
// package also moves — a bare count would make this guard's colour depend on
// what ran before it. Matching the frame makes the measurement about exactly the
// goroutines under test.
func liveProbeGoroutines() int {
	buf := make([]byte, 1<<20)
	n := runtime.Stack(buf, true)
	return strings.Count(string(buf[:n]), "web.(*Server).probeDisk.func")
}

// TestAbandonedProbeGoroutineExitsWhenTheMountRecovers guards the ONE line in
// probeDisk that no rendering assertion can see: the result channel's capacity.
//
// 🔴 THE CLASSIC TIMEOUT-GOROUTINE LEAK. After the watchdog fires, probeDisk has
// returned and nobody is left to receive. With an UNBUFFERED channel the probe
// goroutine parks forever on its send — even after the mount recovers and the
// syscall finally returns — so the leak outlives the failure that caused it and
// pins the channel alive with it. One slot of buffer lets the late sender
// complete and exit.
//
// Every rendered byte is IDENTICAL either way, which is exactly why this needs
// its own instrument.
func TestAbandonedProbeGoroutineExitsWhenTheMountRecovers(t *testing.T) {
	// Settle first: a probe goroutine left mid-exit by an earlier test would
	// otherwise be counted as this test's leak.
	waitForProbeGoroutines(t, 0, "before the test starts")

	srv := &Server{
		diskProbeTimeout: 20 * time.Millisecond,
		diskProbeMemoTTL: time.Minute,
		diskHung:         map[string]time.Time{},
	}
	gate := make(chan struct{})
	var releaseOnce sync.Once
	unwedge := func() { releaseOnce.Do(func() { close(gate) }) }
	defer unwedge()

	statFn := func(string) (diskusage.Usage, error) {
		<-gate
		return diskusage.Usage{Total: 10, Free: 5, Used: 5}, nil
	}

	if _, timedOut, _ := srv.probeDisk(statFn, "/mnt/wedged"); !timedOut {
		t.Fatal("fixture is wrong: the probe did not time out, so no goroutine was ever abandoned")
	}
	// FIXTURE REACH: the abandoned goroutine really is still there, still parked
	// in statFn. Without this the "it exited" assertion below would pass for a
	// probe that never spawned one.
	if got := liveProbeGoroutines(); got != 1 {
		t.Fatalf("%d abandoned probe goroutines, want 1 — the fixture is not reproducing the leak", got)
	}

	// The mount answers at last. The abandoned goroutine must now be able to
	// finish its send and exit.
	unwedge()
	waitForProbeGoroutines(t, 0, "after the mount recovered")
}

// waitForProbeGoroutines polls until the live probe-goroutine count reaches want,
// failing with a diagnosis if it never does. Polling (rather than one sample) is
// what makes it robust: a goroutine that IS exiting only needs to be scheduled,
// whereas one parked on an unbuffered send never will be.
func waitForProbeGoroutines(t *testing.T, want int, when string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		got := liveProbeGoroutines()
		if got == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d probe goroutines are still live %s, want %d — an abandoned probe must be "+
				"able to complete its send and exit once the mount answers, which it cannot do on "+
				"an unbuffered channel", got, when, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestDisksProbesAreConcurrentAndBounded pins the two halves of the fan-out
// policy at once, because they trade against each other: unbounded parallelism
// would satisfy the first half and blow the second.
//
// 🔴 IT MEASURES SIMULTANEITY, NOT DURATION. Every abandoned probe stays parked
// forever, so "how many probes are in flight at the end" is 8 whether they were
// issued together or one watchdog-window apart — counting at the end proves
// nothing. The distinguishing observation has to be made BEFORE the first
// watchdog fires, which is why the probe timeout here is long (5s) and the
// barrier wait is short (2s): a serial implementation cannot reach the barrier
// inside that window, because each probe waits out the previous one's watchdog.
func TestDisksProbesAreConcurrentAndBounded(t *testing.T) {
	// Two more directories than the cap, so the cap has something to hold back.
	// ModelRoot + 8 library paths + the derived trash dir = 10 entries.
	extra := make([]string, 8)
	for i := range extra {
		extra[i] = t.TempDir()
	}
	srv := newHungDisksServer(t, extra...)
	srv.diskProbeTimeout = 5 * time.Second

	release := make(chan struct{})
	var releaseOnce sync.Once
	unwedge := func() { releaseOnce.Do(func() { close(release) }) }
	defer unwedge() // also covers the t.Errorf paths below, so nothing outlives the test
	var entered atomic.Int64
	full := make(chan struct{})
	var once sync.Once
	srv.diskStatFn = func(string) (diskusage.Usage, error) {
		if entered.Add(1) == int64(diskProbeParallelism) {
			once.Do(func() { close(full) })
		}
		<-release
		return diskusage.Usage{}, errors.New("released")
	}

	done := make(chan struct{})
	go func() { defer close(done); rawGet(srv, "/disks") }()

	// --- concurrency: all diskProbeParallelism probes are in flight at once ---
	select {
	case <-full:
	case <-time.After(2 * time.Second):
		t.Errorf("only %d of %d probes had been issued after 2s while every one of them was blocked — "+
			"the probes are running SERIALLY, so each hung directory costs the page another full "+
			"watchdog window", entered.Load(), diskProbeParallelism)
	}

	// --- the cap: and not one more, while those workers are still blocked -----
	// Every worker is parked in its watchdog select for the full 5s, so a correct
	// implementation CANNOT dispatch a 9th probe during this settle. An unbounded
	// one dispatches all 10 immediately.
	time.Sleep(200 * time.Millisecond)
	if got := entered.Load(); got != int64(diskProbeParallelism) {
		t.Errorf("%d probes were in flight, want exactly %d — the fan-out is unbounded, and the "+
			"directory count is user-controlled (library_paths + the scan-dir table)",
			got, diskProbeParallelism)
	}

	unwedge()
	<-done
}
