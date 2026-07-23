package web

import (
	"context"
	"net/http"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/library"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// scanJobBudget is a RUNAWAY BACKSTOP for a streaming model-file scan, not the
// normal termination path (mirrors discoveryJobBudget). Hashing a multi-GB
// library on a slow/spun-down drive can take a long time, so the scan is meant
// to keep going until it exhausts the tree, the USER stops it, or the server
// shuts down. The budget only bounds a genuinely stuck/forgotten job so it
// cannot leak a goroutine forever; it is deliberately huge.
const scanJobBudget = 6 * time.Hour

// matchRemoteSettingKey persists the "Match against CivitAI" opt-in (Tab B
// toggle). It is the single source of truth for whether a web-triggered scan
// sends file hashes to civitai.com: the Tab-A "Scan for models" CTA (which
// carries no per-form checkbox) reads it, and the Tab-B toggle writes it.
const matchRemoteSettingKey = "match_remote"

// matchRemoteEnabled reports the persisted opt-in state. It defaults ON when the
// setting is UNSET: a fresh web scan matches against CivitAI by hash so a user's
// library is actually identified out of the box (the prior default-off silently
// left almost everything "unmatched"). An operator who explicitly turned it OFF
// stays off — a persisted "false" is respected. Matching sends file SHA256 hashes
// to civitai.com; that is surfaced at the scan CTA / Match toggle (see
// modelScanForm / scanForModelsCTA).
func (s *Server) matchRemoteEnabled() bool {
	v, _ := s.store.GetSettingDefault(matchRemoteSettingKey, "true")
	return v == "true"
}

// handleSetMatchRemote persists the Tab-B "Match against CivitAI" toggle. A
// single checkbox posting itself sends its value only when checked, so presence
// == enabled. CSRF-protected; returns 204 (htmx swaps nothing).
func (s *Server) handleSetMatchRemote(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if !s.verifyCSRF(w, r) {
		return
	}
	val := "false"
	if r.FormValue("match_remote") == "true" {
		val = "true"
	}
	if err := s.store.SetSetting(matchRemoteSettingKey, val); err != nil {
		s.log.Warn("persist match_remote", "err", err)
	}
	w.WriteHeader(http.StatusNoContent)
}

// startScan launches a background streaming model-file scan unless one is already
// running (idempotent — a re-click while a scan is in flight starts no second
// goroutine). The scan derives its context from the server base context (so
// shutdown cancels it) with the runaway-backstop scanJobBudget timeout, and
// STREAMS each scanned file into the job (appended under scanMu) so a
// /library/scan/status poll shows the growing list. On settle it records the
// final state. extra are the resolved scan dirs (model_root is added by the
// scanner); noRemote gates CivitAI by-hash matching.
func (s *Server) startScan(extra []string, noRemote bool) {
	s.scanMu.Lock()
	defer s.scanMu.Unlock()
	if s.scanJob != nil && s.scanJob.running {
		return // one job at a time
	}

	base := s.baseCtx
	if base == nil {
		base = context.Background()
	}
	ctx, cancel := context.WithTimeout(base, scanJobBudget)
	job := &scanJob{running: true, startedAt: time.Now(), cancel: cancel, noRemote: noRemote}
	s.scanJob = job

	// onFile streams each scanned file into the job. The scanner runs a concurrent
	// worker pool, so in production this is called from MULTIPLE scanning goroutines
	// at once (a test seam may also call it from its own goroutine). Either way it
	// takes the mutex: job.results is appended by concurrent callers and read
	// concurrently by /status, so scanMu is what makes both safe.
	onFile := func(fr library.FileResult) {
		s.scanMu.Lock()
		job.results = append(job.results, fr)
		job.scanned++
		// Partition each streamed file into matched / pending / unmatched so the
		// progress reads scanned = matched + unmatched + pending. Anything that is
		// not a confirmed match or a retryable pending (including broken) counts as
		// unmatched — a normal, non-error outcome.
		switch fr.Status {
		case store.LocalStatusMatched:
			job.matched++
		case store.LocalStatusUnmatchedPending:
			job.pending++
		default:
			job.unmatched++
		}
		s.scanMu.Unlock()
	}

	scan := s.scanFn
	if scan == nil {
		// Production path: build the real scanner over the resolved dirs and run its
		// streaming Scan. local_files persistence + candidate analysis still happen
		// inside Scan; OnFile only ADDS the incremental view.
		sc := s.newScanner(extra, noRemote)
		scan = func(ctx context.Context, of func(library.FileResult)) error {
			sc.SetOnFile(of)
			_, err := sc.Scan(ctx)
			return err
		}
	}

	go func() {
		defer cancel()
		err := scan(ctx, onFile)
		s.scanMu.Lock()
		job.err = err
		job.running = false
		job.finishedAt = time.Now()
		s.scanMu.Unlock()
	}()
}

// stopScan marks the running scan stopped and cancels its context. Idempotent: a
// stop with no running job is a harmless no-op. The scan goroutine settles on its
// own (running=false) once cancellation propagates; job.stopped stays set so the
// terminal fragment reads "Scan stopped".
func (s *Server) stopScan() {
	s.scanMu.Lock()
	defer s.scanMu.Unlock()
	j := s.scanJob
	if j == nil || !j.running {
		return
	}
	j.stopped = true
	if j.cancel != nil {
		j.cancel()
	}
}

// scanSnapshot is a locked, self-consistent view of the scan job returned by
// scanJobState. Started is false when no scan has ever been triggered. Results is
// a COPY of the job's slice taken under the lock (never the live, still-appended
// header) — the same torn-slice guard the discovery job uses. Arrival order is
// preserved (the streaming order files were scanned in).
type scanSnapshot struct {
	Started, Running                     bool
	Results                              []library.FileResult
	Scanned, Matched, Unmatched, Pending int
	Stopped, NoRemote                    bool
	Err                                  error
}

// scanJobState returns a locked snapshot of the current scan job.
func (s *Server) scanJobState() scanSnapshot {
	s.scanMu.Lock()
	defer s.scanMu.Unlock()
	j := s.scanJob
	if j == nil {
		return scanSnapshot{}
	}
	snap := make([]library.FileResult, len(j.results))
	copy(snap, j.results)
	return scanSnapshot{
		Started:   true,
		Running:   j.running,
		Results:   snap,
		Scanned:   j.scanned,
		Matched:   j.matched,
		Unmatched: j.unmatched,
		Pending:   j.pending,
		Stopped:   j.stopped,
		NoRemote:  j.noRemote,
		Err:       j.err,
	}
}

// renderScanStatus renders the current scan-job state into the STABLE
// #scan-results container: while running, the scanning fragment (WITH the
// poller, Stop button, progress, and the result cards streamed so far); once
// settled, the terminal Model-files view (Summary / Files / Candidates built
// from the completed local_files, WITHOUT the poller) so htmx stops polling.
// Shared by the status poll and the Stop handler.
func (s *Server) renderScanStatus(w http.ResponseWriter) {
	snap := s.scanJobState()
	if snap.Started && snap.Running {
		s.render(w, http.StatusOK, scanScanning(snap, s.csrf))
		return
	}
	// Terminal (or never-started): rebuild the authoritative Model-files view from
	// local_files. A never-started job renders the plain library content, which
	// also halts any stray poller.
	files, ferr := s.store.ListLocalFiles()
	if ferr != nil {
		s.renderError(w, "reload library", ferr)
		return
	}
	s.render(w, http.StatusOK, scanResults(buildLibraryView(files), snap, s.csrf))
}

// handleScanStatus is polled by the scanning fragment. GET (no state change, so
// no CSRF). Unlike discovery's status it is NOT loopback-gated: a model scan of
// model_root is a supported capability on any bind (only the arbitrary
// extra-scan-path SELECTION is loopback-gated, enforced in handleLibraryScan),
// and the status exposes only local_files paths the Library page already shows.
func (s *Server) handleScanStatus(w http.ResponseWriter, r *http.Request) {
	s.renderScanStatus(w)
}

// handleScanStop cancels the running scan (the user is done). CSRF-protected;
// idempotent — stopping when nothing runs is a no-op. It returns the current
// status fragment (which the #scan-poll element swaps in): still scanning if the
// scan has not yet settled — the poller then drives it to the terminal "Scan
// stopped" fragment — or the terminal fragment directly.
func (s *Server) handleScanStop(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if !s.verifyCSRF(w, r) {
		return
	}
	s.stopScan()
	s.renderScanStatus(w)
}
