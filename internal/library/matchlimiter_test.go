package library

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fixedClock returns a controllable clock+sleeper pair for limiter tests. wait
// ADVANCES the clock by the slept duration (so a cooldown deadline is actually
// reached without real time) and records every slept duration. It is safe under
// concurrency.
type fixedClock struct {
	mu     sync.Mutex
	now    time.Time
	waited []time.Duration
}

func newFixedClock() *fixedClock {
	return &fixedClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
}

func (c *fixedClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fixedClock) wait(ctx context.Context, d time.Duration) {
	if ctx.Err() != nil {
		return
	}
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.waited = append(c.waited, d)
	c.mu.Unlock()
}

func (c *fixedClock) totalWaited() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	var sum time.Duration
	for _, d := range c.waited {
		sum += d
	}
	return sum
}

func (c *fixedClock) waitCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.waited)
}

// TestMatchLimiterDefaultCap proves a non-positive concurrency falls back to the
// default so Options can leave MatchConcurrency unset.
func TestMatchLimiterDefaultCap(t *testing.T) {
	for _, in := range []int{0, -1, -5} {
		if got := newMatchLimiter(in).cap(); got != matchConcurrencyDefault {
			t.Errorf("newMatchLimiter(%d).cap() = %d, want default %d", in, got, matchConcurrencyDefault)
		}
	}
	if got := newMatchLimiter(5).cap(); got != 5 {
		t.Errorf("cap() = %d, want 5", got)
	}
}

// TestMatchLimiterConcurrencyCap is the core burst guard: many goroutines
// acquire/release around a "call" that records the peak simultaneous holders.
// The peak must never exceed the semaphore capacity, no matter how many
// goroutines pile in. Run under -race, it also proves the semaphore is race-free.
func TestMatchLimiterConcurrencyCap(t *testing.T) {
	const capN = 3
	const workers = 32
	const perWorker = 50
	lim := newMatchLimiter(capN)

	var inFlight, peak int64
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < perWorker; j++ {
				if !lim.acquire(context.Background()) {
					t.Errorf("acquire failed unexpectedly")
					return
				}
				cur := atomic.AddInt64(&inFlight, 1)
				for {
					p := atomic.LoadInt64(&peak)
					if cur <= p || atomic.CompareAndSwapInt64(&peak, p, cur) {
						break
					}
				}
				atomic.AddInt64(&inFlight, -1)
				lim.release()
			}
		}()
	}
	wg.Wait()
	if got := atomic.LoadInt64(&peak); got > capN {
		t.Fatalf("peak in-flight = %d, want <= cap %d", got, capN)
	}
}

// TestMatchLimiterAcquireCtxCancel proves acquire returns false (never blocks
// forever) when ctx is cancelled while the pool is saturated.
func TestMatchLimiterAcquireCtxCancel(t *testing.T) {
	lim := newMatchLimiter(1)
	if !lim.acquire(context.Background()) {
		t.Fatal("first acquire should succeed")
	}
	// Pool full; a cancelled ctx must make acquire give up rather than deadlock.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if lim.acquire(ctx) {
		t.Fatal("acquire should return false on a cancelled ctx when saturated")
	}
	lim.release()
}

// TestMatchLimiterCoordinatedBackoffEscalatesAndResets proves the SHARED
// cooldown escalates on repeated 429s (2m -> 4m -> 8m) and resets on success —
// the state that makes the pool back off cooperatively instead of per worker.
func TestMatchLimiterCoordinatedBackoffEscalatesAndResets(t *testing.T) {
	clk := newFixedClock()
	lim := newMatchLimiter(3)

	// No cooldown yet: waitCooldown is a no-op.
	if !lim.waitCooldown(context.Background(), clk.wait, clk.Now) {
		t.Fatal("waitCooldown with no cooldown should return true")
	}
	if clk.waitCount() != 0 {
		t.Fatalf("no cooldown should sleep 0 times, slept %d", clk.waitCount())
	}

	want := []time.Duration{2 * time.Minute, 4 * time.Minute, 8 * time.Minute}
	for i, w := range want {
		lim.onRateLimited(clk.Now) // a 429 at the current (advanced) instant escalates
		if !lim.waitCooldown(context.Background(), clk.wait, clk.Now) {
			t.Fatalf("attempt %d: waitCooldown returned false", i)
		}
		got := clk.waited[len(clk.waited)-1]
		if got != w {
			t.Fatalf("attempt %d: slept %s, want %s (escalating shared backoff)", i, got, w)
		}
	}

	// A success resets the shared cooldown: the next waitCooldown sleeps nothing.
	lim.onSuccess()
	before := clk.waitCount()
	if !lim.waitCooldown(context.Background(), clk.wait, clk.Now) {
		t.Fatal("waitCooldown after reset returned false")
	}
	if clk.waitCount() != before {
		t.Fatalf("success must reset cooldown; extra sleep occurred (%d -> %d)", before, clk.waitCount())
	}
	// And a fresh 429 starts back at the 2m floor, proving the reset.
	lim.onRateLimited(clk.Now)
	lim.waitCooldown(context.Background(), clk.wait, clk.Now)
	if got := clk.waited[len(clk.waited)-1]; got != 2*time.Minute {
		t.Fatalf("post-reset backoff = %s, want 2m floor", got)
	}
}

// TestMatchLimiterBurstDoesNotCompound proves a burst of 429s that all land
// inside one already-open cooldown window escalates the shared backoff exactly
// ONCE — the coordination that stops 8 workers from compounding a single
// rate-limit signal into a runaway 2m->4m->...->30m climb.
func TestMatchLimiterBurstDoesNotCompound(t *testing.T) {
	clk := newFixedClock()
	lim := newMatchLimiter(8)

	// 8 simultaneous 429s at the SAME instant (clock not advanced between them).
	for i := 0; i < 8; i++ {
		lim.onRateLimited(clk.Now)
	}
	lim.waitCooldown(context.Background(), clk.wait, clk.Now)
	if got := clk.waited[len(clk.waited)-1]; got != 2*time.Minute {
		t.Fatalf("burst of 429s cooled the pool by %s, want a single 2m window", got)
	}

	// A later 429 AFTER that window elapsed (clock advanced by the wait above)
	// opens a new window and escalates once more.
	lim.onRateLimited(clk.Now)
	lim.waitCooldown(context.Background(), clk.wait, clk.Now)
	if got := clk.waited[len(clk.waited)-1]; got != 4*time.Minute {
		t.Fatalf("second window = %s, want 4m (one escalation per distinct burst)", got)
	}
}

// TestMatchLimiterWaitCooldownCtxCancel proves a worker cooling down aborts
// promptly on ctx cancel: waitCooldown returns false and does not consume the
// full cooldown. Uses a waitFn that honours ctx (like production sleepCtx) but
// records real elapsed time to prove no long sleep occurred.
func TestMatchLimiterWaitCooldownCtxCancel(t *testing.T) {
	lim := newMatchLimiter(3)
	// Open a long cooldown via a real-time clock so the deadline is genuinely far.
	lim.onRateLimited(func() time.Time { return time.Now() })

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled: the ctx-honouring wait must return at once

	start := time.Now()
	ok := lim.waitCooldown(ctx, sleepCtx, time.Now)
	if ok {
		t.Fatal("waitCooldown must report false when ctx is cancelled")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("cooling worker slept %s; must abort promptly on ctx cancel", elapsed)
	}
}

// TestMatchLimiterSharedAcrossWorkers proves the cooldown is SHARED state, not
// per worker: a 429 recorded by "worker A" makes a DIFFERENT worker B (which
// never saw a 429 itself) wait the same cooldown before its next call. This is
// exactly why the pool throttles as a whole.
func TestMatchLimiterSharedAcrossWorkers(t *testing.T) {
	clk := newFixedClock()
	lim := newMatchLimiter(3)

	// Worker A hits a 429 and opens the shared cooldown.
	lim.onRateLimited(clk.Now)

	// Worker B, which had no error of its own, still observes the shared cooldown.
	if !lim.waitCooldown(context.Background(), clk.wait, clk.Now) {
		t.Fatal("waitCooldown returned false")
	}
	if got := clk.totalWaited(); got != 2*time.Minute {
		t.Fatalf("worker B waited %s, want the shared 2m cooldown opened by worker A", got)
	}
}
