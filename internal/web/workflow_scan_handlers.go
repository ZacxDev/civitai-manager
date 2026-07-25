package web

import (
	"context"
	"net/http"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/library"
)

// workflowScanJobBudget is a RUNAWAY BACKSTOP for a streaming workflow scan (not
// the normal termination path), mirroring scanJobBudget. Discovering ComfyUI
// installs across all disks plus parsing every workflow graph can take a while on
// a large/slow tree; the budget only bounds a genuinely stuck job so it cannot
// leak a goroutine forever.
const workflowScanJobBudget = 1 * time.Hour

// workflowScanJob is the in-memory state of a single background streaming
// workflow scan. All fields are read/written only under Server.workflowScanMu.
//
// The scan STREAMS results: it appends to results incrementally (under the mutex)
// as each workflow is processed, so a /library/workflow-scan/status poll shows the
// growing list. A reader MUST snapshot-copy the slice under the lock before
// rendering (the torn-slice guard the model-scan and discovery jobs use).
type workflowScanJob struct {
	running bool
	results []library.WorkflowResult
	// found/linked/unchanged partition the streamed results; skipped comes from the
	// terminal report (non-workflow .json files are not streamed).
	found     int
	linked    int
	unchanged int
	skipped   int
	stopped   bool
	err       error
	startedAt time.Time
	finished  time.Time
	// cancel cancels the scan's context; invoked when the scan finishes (to release
	// the timeout context), on server shutdown, and on an explicit Stop.
	cancel context.CancelFunc
}

// handleWorkflowScan starts a background streaming workflow scan (if none is
// running) and returns the live scanning fragment that htmx-polls for the result.
// CSRF-protected and loopback-gated: it triggers a filesystem walk (discovery +
// workflow-dir walk), the same posture as the model scan's arbitrary-path surface.
func (s *Server) handleWorkflowScan(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if !s.verifyCSRF(w, r) {
		return
	}
	if !s.gate(w) {
		return
	}
	s.startWorkflowScan()
	s.render(w, http.StatusOK, workflowScanScanning(s.workflowScanJobState(), s.csrf))
}

// startWorkflowScan launches a background streaming workflow scan unless one is
// already running (idempotent). The scan derives its context from the server base
// context (so shutdown cancels it) with the runaway-backstop budget, and STREAMS
// each processed workflow into the job (appended under workflowScanMu). In
// production it resolves the ComfyUI workflow dirs (the marked install dirs'
// user-workflow dirs, else auto-discovery) and runs a library.WorkflowScanner.
func (s *Server) startWorkflowScan() {
	s.workflowScanMu.Lock()
	defer s.workflowScanMu.Unlock()
	if s.workflowScanJob != nil && s.workflowScanJob.running {
		return // one job at a time
	}

	base := s.baseCtx
	if base == nil {
		base = context.Background()
	}
	ctx, cancel := context.WithTimeout(base, workflowScanJobBudget)
	job := &workflowScanJob{running: true, startedAt: time.Now(), cancel: cancel}
	s.workflowScanJob = job

	// onWorkflow streams each processed workflow into the job. The production
	// scanner processes files sequentially, but a test seam may stream from its own
	// goroutine — either way the mutex makes the append + counters race-safe against
	// a concurrent /status poll.
	onWorkflow := func(wr library.WorkflowResult) {
		s.workflowScanMu.Lock()
		job.results = append(job.results, wr)
		switch {
		case wr.Unchanged:
			job.unchanged++
		default:
			job.found++
			if wr.Linked {
				job.linked++
			}
		}
		s.workflowScanMu.Unlock()
	}

	scan := s.workflowScanFn
	if scan == nil {
		scan = s.realWorkflowScan
	}

	go func() {
		defer cancel()
		report, err := scan(ctx, onWorkflow)
		s.workflowScanMu.Lock()
		job.err = err
		if report != nil {
			job.skipped = report.Skipped
		}
		job.running = false
		job.finished = time.Now()
		s.workflowScanMu.Unlock()
	}()
}

// realWorkflowScan is the production workflow-scan seam: it resolves the ComfyUI
// workflow directories (the user's marked install dirs, derived to their
// user-workflow dirs; or a bounded $HOME auto-discovery crawl when nothing is
// marked) and runs a library.WorkflowScanner over them, streaming each result.
func (s *Server) realWorkflowScan(ctx context.Context, onWorkflow func(library.WorkflowResult)) (*library.WorkflowScanReport, error) {
	dirs := s.resolveWorkflowScanDirs(ctx)
	if len(dirs) == 0 {
		return &library.WorkflowScanReport{}, nil
	}
	ws := library.NewWorkflowScanner(s.store, s.log)
	return ws.ScanWorkflows(ctx, dirs, library.WorkflowScanOptions{OnWorkflow: onWorkflow})
}

// resolveWorkflowScanDirs finds the directories to scan for workflows: a bounded
// discovery crawl surfaces each ComfyUI install's existing WorkflowDirs, unioned
// with any persisted scan_dirs the user selected (in case a workflow dir was added
// directly). Discovery errors are tolerated — a partial/empty result just narrows
// the scan.
func (s *Server) resolveWorkflowScanDirs(ctx context.Context) []string {
	seen := map[string]bool{}
	var out []string
	add := func(d string) {
		if d == "" || seen[d] {
			return
		}
		seen[d] = true
		out = append(out, d)
	}

	// Preferred, deterministic path: the user has already marked their install
	// dirs (the same ones the model scan uses). Derive each one's user-workflow
	// directory and scan THAT specifically — fast, and no $HOME crawl. This also
	// avoids sweeping in bundled custom-node examples / ComfyUI templates that a
	// full-tree walk of an install root would pick up.
	if sel, err := s.store.ListScanDirs(); err == nil && len(sel) > 0 {
		for _, d := range sel {
			for _, wd := range library.WorkflowDirsForMarked(d) {
				add(wd)
			}
		}
		return out
	}

	// Fallback only when nothing is marked: auto-discover ComfyUI installs under
	// $HOME/opt (bounded, best-effort) and scan their workflow dirs.
	crawl := s.crawlFn
	if crawl == nil {
		crawl = library.DiscoverInstalls
	}
	installs, _ := crawl(ctx, s.discoverRoots, library.DiscoverOptions{})
	for _, d := range library.WorkflowScanDirs(installs) {
		add(d)
	}
	return out
}

// stopWorkflowScan marks the running workflow scan stopped and cancels its
// context. Idempotent: a stop with no running job is a harmless no-op.
func (s *Server) stopWorkflowScan() {
	s.workflowScanMu.Lock()
	defer s.workflowScanMu.Unlock()
	j := s.workflowScanJob
	if j == nil || !j.running {
		return
	}
	j.stopped = true
	if j.cancel != nil {
		j.cancel()
	}
}

// workflowScanSnapshot is a locked, self-consistent view of the workflow-scan job.
// Started is false when no scan has ever been triggered. Results is a COPY taken
// under the lock (never the live, still-appended header).
type workflowScanSnapshot struct {
	Started, Running               bool
	Results                        []library.WorkflowResult
	Found, Linked, Unchanged, Skip int
	Stopped                        bool
	Err                            error
}

// workflowScanJobState returns a locked snapshot of the current workflow-scan job.
func (s *Server) workflowScanJobState() workflowScanSnapshot {
	s.workflowScanMu.Lock()
	defer s.workflowScanMu.Unlock()
	j := s.workflowScanJob
	if j == nil {
		return workflowScanSnapshot{}
	}
	snap := make([]library.WorkflowResult, len(j.results))
	copy(snap, j.results)
	return workflowScanSnapshot{
		Started:   true,
		Running:   j.running,
		Results:   snap,
		Found:     j.found,
		Linked:    j.linked,
		Unchanged: j.unchanged,
		Skip:      j.skipped,
		Stopped:   j.stopped,
		Err:       j.err,
	}
}

// renderWorkflowScanStatus renders the current workflow-scan state into the STABLE
// #workflow-scan-results container: while running (and not stopped), the scanning
// fragment WITH the re-arming poller; once settled or stopped, the terminal
// workflow list rebuilt from the store WITHOUT the poller so htmx stops polling.
func (s *Server) renderWorkflowScanStatus(w http.ResponseWriter) {
	snap := s.workflowScanJobState()
	if snap.Started && snap.Running && !snap.Stopped {
		s.render(w, http.StatusOK, workflowScanScanning(snap, s.csrf))
		return
	}
	wfs, err := s.store.ListWorkflows(context.Background())
	if err != nil {
		s.renderError(w, "reload workflows", err)
		return
	}
	s.render(w, http.StatusOK, workflowScanTerminal(wfs, snap, s.csrf, s.extraPathsAllowed(), s.workflowResolver()))
}

// handleWorkflowScanStatus is polled by the scanning fragment. GET (no state
// change, so no CSRF). Loopback-gated like the model-scan discovery status.
func (s *Server) handleWorkflowScanStatus(w http.ResponseWriter, r *http.Request) {
	if !s.gate(w) {
		return
	}
	s.renderWorkflowScanStatus(w)
}

// handleWorkflowScanStop cancels the running workflow scan. CSRF-protected and
// loopback-gated; idempotent — stopping when nothing runs is a no-op.
func (s *Server) handleWorkflowScanStop(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if !s.verifyCSRF(w, r) {
		return
	}
	if !s.gate(w) {
		return
	}
	s.stopWorkflowScan()
	s.renderWorkflowScanStatus(w)
}
