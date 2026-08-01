package web

import (
	"errors"
	"io/fs"
	"net/http"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/diskusage"
	"github.com/ZacxDev/civitai-manager/internal/library"
)

// The /disks capacity probe's bounds. All three are defaults written into the
// Server in NewServer; tests override them on their own instance.
const (
	// defaultDiskProbeTimeout is how long ONE directory's probe may take before
	// its row renders as unknown. Generous enough that a cold spinning disk or a
	// healthy-but-slow network share still reports real figures, short enough that
	// a page full of hung mounts is still a page.
	defaultDiskProbeTimeout = 2 * time.Second
	// defaultDiskProbeMemoTTL is how long a timed-out path is answered from the
	// memo instead of being probed again. See Server.diskHung for why the memo,
	// not the timeout, is what actually bounds the damage.
	defaultDiskProbeMemoTTL = 30 * time.Second
	// diskProbeParallelism caps concurrent probes. The directory count is
	// USER-CONTROLLED (library_paths + the scan-dir table), so an unbounded
	// fan-out is a goroutine/syscall multiplier the user chooses; 8 keeps healthy
	// directories from serializing behind each other without becoming one.
	diskProbeParallelism = 8
)

// diskProbeTimeoutText is the reason a timed-out row carries. It is DISTINCT
// from the not-exist and unsupported wordings on purpose: "did not respond" is
// the one failure the user can neither fix by creating a directory nor blame on
// their platform, and it is the one that says something about the mount rather
// than about the path.
const diskProbeTimeoutText = "the mount did not respond — an unresponsive network share can block indefinitely"

// handleDisks renders /disks: filesystem capacity for every configured model
// directory plus the quarantine batches /trash used to show.
//
// 🔴 LOOPBACK-GATED, THROUGH THE SAME PREDICATE AS /library/browse AND
// /library/scan — s.extraPathsAllowed(), the app's ONE answer to "is this a
// local single-user surface". It is not a second mechanism: the browse/discover
// endpoints call it via s.gate(w), which additionally renders a fragment, and
// this handler needs a full PAGE instead, so it consults the predicate directly
// and hands the result to disksPage. The wording is the same string, so the
// shared gate-message assertion covers both.
//
// The gate covers the CAPACITY half only, and the page still renders. That is a
// deliberate scoping call with a real trade-off, recorded here because it is not
// obvious: the paths are the sensitive part (an unauthenticated remote caller
// learning the operator's directory layout), while the quarantine table exposes
// batch ids, timestamps and file COUNTS — no paths — and POST /trash/{id}/restore
// was never gated. Gating the whole page would therefore have REMOVED a working
// capability from a non-loopback bind (restore) to protect data that half of the
// page does not carry. Off-loopback: no path, no capacity, no probe — the
// syscalls are not even issued, because the rows are never collected. That last
// claim is pinned by TestDisksRowsAreNotEvenProbedOffLoopback, which counts at
// Server.diskStatFn rather than inspecting the markup.
//
// CAVEAT, PRE-EXISTING AND SHARED WITH /library/browse: extraPathsAllowed keys
// on the server's own BIND ADDRESS, not on the peer. A loopback-bound instance
// behind a reverse proxy therefore looks local to every remote caller the proxy
// forwards, and this page's paths are exposed. Deploying that way is outside the
// app's single-user-local design; do not "fix" it here in isolation, since the
// predicate governs several endpoints at once.
//
// GET, read-only, no state change -> no CSRF.
func (s *Server) handleDisks(w http.ResponseWriter, r *http.Request) {
	batches, err := s.loadBatchViews()
	if err != nil {
		s.renderError(w, "load quarantine batches", err)
		return
	}
	gated := !s.extraPathsAllowed()
	var rows []diskRow
	if !gated {
		rows = s.diskRows()
	}
	s.render(w, http.StatusOK, disksPage(rows, batches, gated, s.csrf, s.maturity(), s.rail(r.Context())))
}

// handleTrashRedirect keeps the old /trash bookmark working after the nav rework
// folded that page into /disks.
//
// 302 FOUND, NOT 301 MOVED PERMANENTLY — chosen deliberately. A 301 is cached by
// browsers INDEFINITELY and is notoriously hard for a user to clear; if /trash
// ever needs to mean something again (a real delete surface, say), every user who
// visited it once would keep being bounced to /disks by their own cache with no
// request reaching the server. A 302 costs one redirect per visit and stays
// reversible, which is the right trade for a URL inside a single-user local app
// where there is no SEO or link-equity argument for a 301.
//
// Only the GET moves. POST /trash/{id}/restore is UNCHANGED and still handled by
// handleRestore: it is the action the quarantine table's buttons issue, a
// redirect would turn an htmx POST into a fragment-less GET, and relocating a
// working CSRF-protected endpoint buys nothing.
func (s *Server) handleTrashRedirect(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/disks", http.StatusFound)
}

// diskRows collects the directories this app manages and probes each one's
// filesystem. Callers MUST have checked the loopback gate first — the returned
// rows carry absolute paths.
//
// The set is exactly the directories the app itself reads or writes: the model
// root, the configured extra library paths, the user's selected scan
// directories, and the quarantine (trash) directory. Nothing here walks the
// filesystem or accepts a path from the request: every entry comes from config
// or from the store, so this cannot become an arbitrary-path primitive.
//
// FAILURE IS PER-ROW. One unreadable path must not cost the page, so a failed
// probe becomes a row with an unknown Usage and a reason string. A store read
// that fails contributes no rows at all rather than failing the request — the
// selected-scan-dirs list is an enhancement over the configured ones.
func (s *Server) diskRows() []diskRow {
	// lazy marks a directory this app CREATES ON DEMAND rather than one the user is
	// expected to already have. It changes nothing but the not-exist wording — see
	// diskErrText.
	type entry struct {
		label, path string
		lazy        bool
	}
	var entries []entry
	add := func(label, path string) {
		if path != "" {
			entries = append(entries, entry{label: label, path: path})
		}
	}
	addLazy := func(label, path string) {
		if path != "" {
			entries = append(entries, entry{label: label, path: path, lazy: true})
		}
	}
	add("Model root", s.cfg.ModelRoot)
	for _, p := range s.cfg.LibraryPaths {
		add("Library path", p)
	}
	if sel, err := s.store.ListScanDirs(); err == nil {
		for _, p := range sel {
			add("Scan directory", p)
		}
	}
	// 🔴 THE TRASH DIR MUST BE RESOLVED, NOT READ RAW. `trash_dir` is unset on
	// essentially every install, and the real quarantine target is derived
	// (<ModelRoot>/.trash) inside library.NewScanner — so `add("Trash",
	// s.cfg.TrashDir)` silently contributed NO row on a default config while this
	// doc claimed the set included "the quarantine (trash) directory".
	// library.ResolveTrashDir is that derivation, called rather than restated, so
	// the row can never name a directory quarantine would not actually use.
	//
	// The DERIVED dir is created LAZILY — quarantine.go makes it on the first move —
	// so on a fresh install it does not exist and probing it rendered a warning-shaped
	// "⚠ Capacity unknown — the directory does not exist or is not mounted". That is
	// wrong twice: nothing is broken, and since it sits UNDER ModelRoot it could never
	// report a capacity the "Model root" row above does not already show. So the
	// derived case is marked lazy and reads "not created yet" instead. An EXPLICITLY
	// configured trash_dir stays a real warning when it is missing — the user named
	// that directory, it may be on another filesystem, and its absence is worth
	// surfacing.
	if trash := library.ResolveTrashDir(s.cfg.TrashDir, s.cfg.ModelRoot); s.cfg.TrashDir != "" {
		add("Trash", trash)
	} else {
		addLazy("Trash", trash)
	}

	statFn := s.diskStatFn
	if statFn == nil {
		statFn = diskusage.Stat
	}

	// Dedupe on the CLEANED path so "/models" and "/models/" are one row. This is
	// deliberately NOT a same-filesystem dedupe: two distinct directories that
	// happen to share a disk stay two rows (the page says so in its footnote),
	// because merging them would need device ids that only exist on unix and would
	// wrongly merge two identically-sized empty volumes.
	rows := make([]diskRow, 0, len(entries))
	clean := make([]string, 0, len(entries))
	lazy := make([]bool, 0, len(entries))
	seen := make(map[string]bool, len(entries))
	for _, e := range entries {
		c := filepath.Clean(e.path)
		if seen[c] {
			continue
		}
		seen[c] = true
		rows = append(rows, diskRow{Label: e.label, Path: e.path})
		clean = append(clean, c)
		lazy = append(lazy, e.lazy)
	}

	// 🔴 THE PROBES RUN OFF THE REQUEST GOROUTINE, BOUNDED AND WATCHDOGGED.
	// syscall.Statfs on a hung NFS/SMB mount blocks in UNINTERRUPTIBLE SLEEP —
	// it cannot be cancelled, so a serial loop here wedged /disks forever and a
	// context would have bought nothing (which is why one is not plumbed through:
	// the goroutine survives cancellation either way). Each probe is therefore
	// given its own goroutine and abandoned after s.diskProbeTimeout, and the
	// abandonment is REMEMBERED — see s.diskHung.
	//
	// Each worker writes rows[i] for an i no other worker touches, so the slice
	// needs no lock; only the memo does.
	n := diskProbeParallelism
	if n > len(rows) {
		n = len(rows)
	}
	if n > 0 {
		idx := make(chan int)
		var wg sync.WaitGroup
		for w := 0; w < n; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := range idx {
					u, timedOut, err := s.probeDisk(statFn, clean[i])
					switch {
					case timedOut:
						rows[i].Err = diskProbeTimeoutText
					case err != nil:
						rows[i].Err = diskErrText(err, lazy[i])
					default:
						rows[i].Usage = u
					}
				}
			}()
		}
		for i := range rows {
			idx <- i
		}
		close(idx)
		wg.Wait()
	}
	return rows
}

// probeDisk measures one path, giving up after s.diskProbeTimeout and never
// probing a path already known to be hanging.
//
// The middle return is "the watchdog fired" — deliberately separate from err,
// because a timeout is not something statFn reported and must not be routed
// through diskErrText's not-exist/unsupported classification.
//
// 🔴 THE RESULT CHANNEL IS BUFFERED (cap 1) AND THAT IS LOAD-BEARING. On the
// timeout path nobody is left to receive: this function has already returned. An
// UNBUFFERED channel would park the orphaned probe goroutine forever on its send
// — even after the mount finally answers and the syscall returns — so the leak
// would be permanent rather than lasting only as long as the mount is wedged,
// and the channel would be pinned alive with it. This is the classic
// timeout-goroutine leak; the one-slot buffer lets the late sender complete and
// exit, and the buffer is then garbage.
func (s *Server) probeDisk(statFn func(string) (diskusage.Usage, error), path string) (diskusage.Usage, bool, error) {
	if s.diskIsHung(path) {
		return diskusage.Usage{}, true, nil
	}

	type probeResult struct {
		u   diskusage.Usage
		err error
	}
	ch := make(chan probeResult, 1) // cap 1: see the doc comment above.
	go func() { u, err := statFn(path); ch <- probeResult{u, err} }()

	// A zero/negative timeout would fire the timer INSTANTLY and report every
	// directory as hung, so a Server assembled without NewServer falls back to the
	// package default rather than to "nothing works".
	d := s.diskProbeTimeout
	if d <= 0 {
		d = defaultDiskProbeTimeout
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case r := <-ch:
		return r.u, false, r.err
	case <-timer.C:
		s.markDiskHung(path)
		return diskusage.Usage{}, true, nil
	}
}

// diskIsHung reports whether path timed out recently enough that probing it
// again would only leak another goroutine. An expired entry is dropped on the
// way past, so the map cannot accumulate paths that have since been fixed (or
// removed from the config).
func (s *Server) diskIsHung(path string) bool {
	s.diskProbeMu.Lock()
	defer s.diskProbeMu.Unlock()
	at, ok := s.diskHung[path]
	if !ok {
		return false
	}
	ttl := s.diskProbeMemoTTL
	if ttl <= 0 {
		ttl = defaultDiskProbeMemoTTL
	}
	if time.Since(at) >= ttl {
		delete(s.diskHung, path)
		return false
	}
	return true
}

// markDiskHung records that path's probe was abandoned, starting its TTL window.
func (s *Server) markDiskHung(path string) {
	s.diskProbeMu.Lock()
	defer s.diskProbeMu.Unlock()
	if s.diskHung == nil {
		// A Server built as a bare literal (some tests) has no map. Nil-map WRITES
		// panic, so this is not optional.
		s.diskHung = map[string]time.Time{}
	}
	s.diskHung[path] = time.Now()
}

// diskErrText turns a probe failure into short human text for the row.
//
// It deliberately does NOT surface the raw syscall error: on unix that is
// "statfs /some/path: no such file or directory", which repeats the path already
// shown beside it and leaks the errno wording into the UI. The two cases a user
// can act on are "the directory is not there" and "this build cannot measure it";
// anything else falls through to the error's own text, since inventing a
// friendly message for an unknown failure would hide the only clue.
//
// lazy flips ONLY the not-exist case. A directory this app creates on demand is
// not a fault when it is absent, and dressing it as one trains the user to ignore
// the warning that does matter.
func diskErrText(err error, lazy bool) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, fs.ErrNotExist), errors.Is(err, syscall.ENOENT):
		if lazy {
			return "not created yet — it appears the first time something is quarantined"
		}
		return "the directory does not exist or is not mounted"
	case errors.Is(err, diskusage.ErrUnsupported):
		return "capacity reporting is not available on this platform"
	default:
		return err.Error()
	}
}
