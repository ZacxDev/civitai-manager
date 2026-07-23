package library

import (
	"context"
	"sync"
	"time"
)

// matchConcurrencyDefault caps the number of IN-FLIGHT CivitAI by-hash lookups
// the scan may have outstanding at once, SHARED across the whole worker pool.
// v0.1.14 made hashing concurrent (min(NumCPU,8) workers); without this the
// remote match phase would burst up to 8 simultaneous by-hash requests at
// CivitAI. 3 keeps enough parallelism to overlap network latency while staying
// well under the hash pool and gentle on the (undocumented) rate limits.
// Hashing concurrency (scanWorkerCap) is unaffected.
const matchConcurrencyDefault = 3

// matchLimiter is the Scanner-owned coordinator that paces the remote by-hash
// lookups as ONE client instead of N independent workers. It combines:
//
//   - a shared semaphore capping in-flight by-hash calls (the burst guard), and
//   - a shared 429 cooldown every worker respects before its next by-hash call,
//     escalating on repeated rate-limits and resetting on the first success (the
//     coordinated-backoff guard), replacing the old per-worker 2m sleeps.
//
// All fields are safe for concurrent use: the semaphore is a channel and the
// cooldown state is mutex-guarded. The limiter changes only request PACING, never
// which files match.
type matchLimiter struct {
	// sem has one slot per permitted in-flight by-hash call; its capacity is the
	// concurrency cap. acquire/release bracket exactly the network call.
	sem chan struct{}

	// mu guards the shared cooldown state below, touched by every worker.
	mu sync.Mutex
	// backoff is the current shared escalation level (0 == no active cooldown).
	backoff time.Duration
	// cooldownUntil is the wall-clock instant until which workers hold off issuing
	// a by-hash call. The zero value means "no cooldown".
	cooldownUntil time.Time
}

// newMatchLimiter builds a limiter with the given in-flight cap; a non-positive
// value falls back to matchConcurrencyDefault so Options can leave it unset.
func newMatchLimiter(concurrency int) *matchLimiter {
	if concurrency <= 0 {
		concurrency = matchConcurrencyDefault
	}
	return &matchLimiter{sem: make(chan struct{}, concurrency)}
}

// cap reports the configured in-flight concurrency cap.
func (l *matchLimiter) cap() int { return cap(l.sem) }

// acquire blocks until an in-flight by-hash permit is free or ctx is cancelled.
// It reports whether a permit was taken; on false (ctx cancelled) there is
// nothing to release.
func (l *matchLimiter) acquire(ctx context.Context) bool {
	select {
	case l.sem <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

// release returns a permit taken by acquire. It must be paired with every
// successful acquire (even on error) so the pool never deadlocks.
func (l *matchLimiter) release() { <-l.sem }

// waitCooldown blocks until the shared rate-limit cooldown elapses (or ctx is
// cancelled). It uses the injected clock (nowFn) and sleeper (waitFn) so tests
// need no real time, and it aborts PROMPTLY on ctx cancel because waitFn
// (sleepCtx) selects on both the timer AND ctx.Done. It reports false only when
// ctx was cancelled, so a cooling worker returns instead of finishing the sleep.
func (l *matchLimiter) waitCooldown(ctx context.Context, waitFn func(context.Context, time.Duration), nowFn func() time.Time) bool {
	l.mu.Lock()
	until := l.cooldownUntil
	l.mu.Unlock()
	if !until.IsZero() {
		if d := until.Sub(nowFn()); d > 0 {
			waitFn(ctx, d)
		}
	}
	return ctx.Err() == nil
}

// onRateLimited records a 429 (or transient error) from a by-hash call and
// escalates the SHARED cooldown so the WHOLE pool slows, not just this worker:
// 0 -> 2m, then doubling to a 30m ceiling. A 429 that lands inside a cooldown
// window another worker already opened does NOT re-escalate — the burst is
// treated as one rate-limit signal — so many simultaneous 429s cool the pool
// once rather than compounding into a runaway backoff.
func (l *matchLimiter) onRateLimited(nowFn func() time.Time) {
	now := nowFn()
	l.mu.Lock()
	defer l.mu.Unlock()
	if now.Before(l.cooldownUntil) {
		return
	}
	l.backoff = nextBackoff(l.backoff)
	l.cooldownUntil = now.Add(l.backoff)
}

// onSuccess clears the shared cooldown after a by-hash call gets through: the
// pool resumes full pace and the escalation resets, so a later burst starts over
// at the 2m floor rather than the last ceiling.
func (l *matchLimiter) onSuccess() {
	l.mu.Lock()
	l.backoff = 0
	l.cooldownUntil = time.Time{}
	l.mu.Unlock()
}
