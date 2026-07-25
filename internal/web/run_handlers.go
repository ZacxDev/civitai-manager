package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/comfy"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// runJobBudget is a RUNAWAY BACKSTOP for a workflow run (not the normal
// termination path): a generation can legitimately take minutes, so the run keeps
// polling until ComfyUI reports the prompt done, the USER stops it, or the server
// shuts down. The budget only bounds a genuinely stuck job so it cannot leak a
// goroutine forever.
const runJobBudget = 30 * time.Minute

// runPollInterval is the cadence at which the run goroutine polls /history + /queue
// for completion. It is well above ComfyUI's own tick and keeps the loop cheap.
const runPollInterval = 1 * time.Second

// comfyClient is the ComfyUI surface the run job and the /view proxy need. It is an
// interface so tests can inject a fake; *comfy.Client satisfies it.
type comfyClient interface {
	SystemStats(ctx context.Context) (*comfy.SystemStats, error)
	ObjectInfo(ctx context.Context) (comfy.ObjectInfo, error)
	Submit(ctx context.Context, apiGraph json.RawMessage, clientID, promptID string) (*comfy.SubmitResult, error)
	QueueState(ctx context.Context, promptID string) (running bool, queuedPos int, found bool, err error)
	History(ctx context.Context, promptID string) (*comfy.HistoryEntry, error)
	View(ctx context.Context, ref comfy.ImageRef) ([]byte, string, error)
	Interrupt(ctx context.Context) error
}

// Run phases (job.phase).
const (
	runPhasePreparing = "preparing"
	runPhaseQueued    = "queued"
	runPhaseRunning   = "running"
	runPhaseDone      = "done"
	runPhaseFailed    = "failed"
)

// runJob is the in-memory state of a single background workflow run. All fields are
// read/written only under Server.runMu.
type runJob struct {
	running    bool
	workflowID int64
	promptID   string
	phase      string
	queuePos   int
	// message is a human status/error line. It may embed UNTRUSTED ComfyUI error
	// text, so every render routes it through g.Text (auto-escaped).
	message string
	images  []comfy.ImageRef
	// preflight is set when the run aborted on a failed preflight (missing nodes/
	// models); warnings is set when a UI→API conversion produced unrunnable nodes.
	preflight  *comfy.PreflightReport
	warnings   []string
	stopped    bool
	err        error
	startedAt  time.Time
	finishedAt time.Time
	cancel     context.CancelFunc
}

// runResult is what runFn returns: images on success, or a preflight report /
// conversion warnings when the run was aborted BEFORE submitting.
type runResult struct {
	Images    []comfy.ImageRef
	Preflight *comfy.PreflightReport
	Warnings  []string
	PromptID  string
}

// runUpdater lets runFn stream phase transitions into the job under the mutex.
type runUpdater struct {
	setPhase    func(phase, msg string, queuePos int)
	setPromptID func(id string)
}

// comfy builds the ComfyUI client (via the test seam, or from config). Returns nil
// when no comfy_url is configured (local run disabled).
func (s *Server) comfy() comfyClient {
	if s.comfyClientFn != nil {
		return s.comfyClientFn()
	}
	if strings.TrimSpace(s.cfg.ComfyURL) == "" {
		return nil
	}
	return comfy.NewClient(s.cfg.ComfyURL, s.cfg.ComfyToken)
}

// startRun launches a background run for wf unless one is already running
// (idempotent — a re-click while a run is in flight starts no second goroutine).
// The run derives its context from the server base context (so shutdown cancels it)
// with the runaway-backstop budget.
func (s *Server) startRun(wf *store.Workflow) {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	if s.runJob != nil && s.runJob.running {
		return // one run at a time
	}

	base := s.baseCtx
	if base == nil {
		base = context.Background()
	}
	ctx, cancel := context.WithTimeout(base, runJobBudget)
	job := &runJob{
		running: true, workflowID: wf.ID, phase: runPhasePreparing,
		message: "Starting run…", startedAt: time.Now(), cancel: cancel,
	}
	s.runJob = job

	up := runUpdater{
		setPhase: func(phase, msg string, pos int) {
			s.runMu.Lock()
			job.phase, job.message, job.queuePos = phase, msg, pos
			s.runMu.Unlock()
		},
		setPromptID: func(id string) {
			s.runMu.Lock()
			job.promptID = id
			s.runMu.Unlock()
		},
	}

	run := s.runFn
	if run == nil {
		run = s.realRun
	}

	go func() {
		defer cancel()
		var res *runResult
		var err error
		// The run path parses two large untrusted surfaces (the imported UI graph in
		// ConvertUIToAPI and untrusted comfy JSON). A panic here must fail THIS job,
		// not crash the server. (audit 🟡)
		func() {
			defer func() {
				if r := recover(); r != nil {
					err = fmt.Errorf("run panicked: %v", r)
				}
			}()
			res, err = run(ctx, wf, up)
		}()
		s.runMu.Lock()
		defer s.runMu.Unlock()
		job.running = false
		job.finishedAt = time.Now()
		switch {
		case job.stopped:
			job.phase, job.message = runPhaseFailed, "Run stopped."
		case err != nil:
			job.phase, job.err, job.message = runPhaseFailed, err, runErrorMessage(err)
		case res != nil && res.Preflight != nil:
			job.phase, job.preflight = runPhaseFailed, res.Preflight
			job.message = "Preflight failed — this workflow references nodes or models that are not installed."
		case res != nil && len(res.Warnings) > 0:
			job.phase, job.warnings = runPhaseFailed, res.Warnings
			job.message = "This workflow could not be converted into a runnable graph."
		case res != nil:
			job.phase, job.images = runPhaseDone, res.Images
			if res.PromptID != "" {
				job.promptID = res.PromptID
			}
			if len(res.Images) == 0 {
				job.message = "Run complete (no images returned)."
			} else {
				job.message = "Run complete."
			}
		default:
			job.phase, job.message = runPhaseFailed, "Run produced no result."
		}
	}()
}

// realRun is the production run seam: load → (convert UI→API) → preflight → submit
// → poll. It aborts (without an error) on a failed preflight or on conversion
// warnings so a broken graph is never submitted.
func (s *Server) realRun(ctx context.Context, wf *store.Workflow, up runUpdater) (*runResult, error) {
	client := s.comfy()
	if client == nil {
		return nil, errors.New("local run is not configured (set comfy_url)")
	}

	up.setPhase(runPhasePreparing, "Contacting ComfyUI…", 0)
	if _, err := client.SystemStats(ctx); err != nil {
		return nil, fmt.Errorf("no ComfyUI reachable at %s", s.cfg.ComfyURL)
	}

	// Load the node schema once; it is needed for a UI conversion and for preflight.
	up.setPhase(runPhasePreparing, "Loading node schema…", 0)
	info, err := client.ObjectInfo(ctx)
	if err != nil {
		return nil, fmt.Errorf("load object_info: %w", err)
	}

	var apiGraph json.RawMessage
	if wf.Format == store.WorkflowFormatUI {
		up.setPhase(runPhasePreparing, "Converting workflow to API format…", 0)
		g, warnings, cerr := comfy.ConvertUIToAPI(json.RawMessage(wf.Graph), info)
		if cerr != nil {
			return nil, fmt.Errorf("convert workflow: %w", cerr)
		}
		if len(warnings) > 0 {
			// Unrunnable nodes / unresolved links — abort rather than submit a broken graph.
			return &runResult{Warnings: warnings}, nil
		}
		apiGraph = g
	} else {
		apiGraph = json.RawMessage(wf.Graph)
	}

	up.setPhase(runPhasePreparing, "Checking installed nodes & models…", 0)
	report := comfy.Preflight(apiGraph, info, func(name string) bool {
		ok, _ := s.store.HasLocalFileNamed(name)
		return ok
	})
	if !report.OK {
		return &runResult{Preflight: &report}, nil
	}

	clientID := comfy.NewID()
	promptID := comfy.NewID()
	up.setPromptID(promptID)
	up.setPhase(runPhasePreparing, "Submitting to ComfyUI…", 0)
	if _, err := client.Submit(ctx, apiGraph, clientID, promptID); err != nil {
		return nil, err // *comfy.PromptValidationError or a transport error
	}

	// Poll history (done) + queue (running/queued position) until settled.
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		hist, err := client.History(ctx, promptID)
		if err != nil {
			return nil, fmt.Errorf("poll history: %w", err)
		}
		if hist != nil {
			st := strings.ToLower(strings.TrimSpace(hist.Status.StatusStr))
			if !hist.Status.Completed && st != "" && st != "success" {
				return nil, fmt.Errorf("ComfyUI reported the run as %q", hist.Status.StatusStr)
			}
			return &runResult{Images: hist.AllImages(), PromptID: promptID}, nil
		}
		running, pos, found, err := client.QueueState(ctx, promptID)
		if err != nil {
			return nil, fmt.Errorf("poll queue: %w", err)
		}
		switch {
		case running:
			up.setPhase(runPhaseRunning, "Generating…", 0)
		case found:
			up.setPhase(runPhaseQueued, "Queued", pos)
		default:
			// Brief gap right after submit (not yet in queue, not yet in history);
			// keep polling.
			up.setPhase(runPhaseRunning, "Working…", 0)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(runPollInterval):
		}
	}
}

// stopRun cancels the running run and best-effort interrupts ComfyUI. Idempotent.
func (s *Server) stopRun() {
	s.runMu.Lock()
	j := s.runJob
	if j == nil || !j.running {
		s.runMu.Unlock()
		return
	}
	j.stopped = true
	cancel := j.cancel
	s.runMu.Unlock()
	if cancel != nil {
		cancel()
	}
	if c := s.comfy(); c != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = c.Interrupt(ctx)
	}
}

// runSnapshot is a locked, self-consistent view of the run job.
type runSnapshot struct {
	Started, Running bool
	WorkflowID       int64
	PromptID         string
	Phase            string
	Message          string
	QueuePos         int
	Images           []comfy.ImageRef
	Preflight        *comfy.PreflightReport
	Warnings         []string
	Stopped          bool
}

// runJobState returns a locked snapshot of the current run job.
func (s *Server) runJobState() runSnapshot {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	j := s.runJob
	if j == nil {
		return runSnapshot{}
	}
	imgs := make([]comfy.ImageRef, len(j.images))
	copy(imgs, j.images)
	warns := make([]string, len(j.warnings))
	copy(warns, j.warnings)
	return runSnapshot{
		Started: true, Running: j.running, WorkflowID: j.workflowID,
		PromptID: j.promptID, Phase: j.phase, Message: j.message, QueuePos: j.queuePos,
		Images: imgs, Preflight: j.preflight, Warnings: warns, Stopped: j.stopped,
	}
}

// handleWorkflowRun starts a run for the workflow and returns the live status
// fragment (which htmx-polls). CSRF-protected + loopback-gated (it reaches the
// comfy server).
func (s *Server) handleWorkflowRun(w http.ResponseWriter, r *http.Request) {
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
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad workflow id", http.StatusBadRequest)
		return
	}
	wf, err := s.store.GetWorkflow(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.renderError(w, "load workflow", err)
		return
	}
	s.startRun(wf)
	s.render(w, http.StatusOK, runStatusFragment(s.runJobState(), id, s.csrf))
}

// handleWorkflowRunStatus is polled by the running fragment. GET (no state change,
// no CSRF); loopback-gated like the other comfy-reaching endpoints.
func (s *Server) handleWorkflowRunStatus(w http.ResponseWriter, r *http.Request) {
	if !s.gate(w) {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad workflow id", http.StatusBadRequest)
		return
	}
	s.render(w, http.StatusOK, runStatusFragment(s.runJobState(), id, s.csrf))
}

// handleWorkflowRunStop cancels the running run and interrupts ComfyUI.
// CSRF-protected + loopback-gated; idempotent.
func (s *Server) handleWorkflowRunStop(w http.ResponseWriter, r *http.Request) {
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
	s.stopRun()
	// The stop button carries the workflow id so the terminal fragment can offer a
	// "Run again" button; 0 (absent) simply omits it.
	id, _ := strconv.ParseInt(r.FormValue("workflow_id"), 10, 64)
	s.render(w, http.StatusOK, runStatusFragment(s.runJobState(), id, s.csrf))
}

// handleWorkflowRunView proxies a result image from ComfyUI's /view to the browser
// (so the browser never needs to reach ComfyUI directly). Loopback-gated. It is NOT
// an open proxy: it serves ONLY an image that belongs to the active/last run's
// prompt (the requested prompt id AND the exact filename/subfolder/type tuple must
// match one of that run's outputs).
func (s *Server) handleWorkflowRunView(w http.ResponseWriter, r *http.Request) {
	if !s.gate(w) {
		return
	}
	snap := s.runJobState()
	q := r.URL.Query()
	if !snap.Started || snap.PromptID == "" || q.Get("prompt") != snap.PromptID {
		http.Error(w, "unknown or expired run image", http.StatusForbidden)
		return
	}
	ref := comfy.ImageRef{
		Filename:  q.Get("filename"),
		Subfolder: q.Get("subfolder"),
		Type:      q.Get("type"),
	}
	if !imageAllowed(snap.Images, ref) {
		http.Error(w, "image is not an output of the active run", http.StatusForbidden)
		return
	}
	client := s.comfy()
	if client == nil {
		http.Error(w, "local run is not configured", http.StatusServiceUnavailable)
		return
	}
	data, ct, err := client.View(r.Context(), ref)
	if err != nil {
		http.Error(w, "could not fetch image from ComfyUI", http.StatusBadGateway)
		return
	}
	// The comfy server is untrusted: constrain the proxied response to images so a
	// hostile comfy can't return text/html+JS that renders in our origin on direct
	// navigation, and forbid content-type sniffing. (audit 🟡)
	if !strings.HasPrefix(ct, "image/") {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}

// imageAllowed reports whether ref exactly matches one of the run's output images.
func imageAllowed(images []comfy.ImageRef, ref comfy.ImageRef) bool {
	for _, img := range images {
		if img == ref {
			return true
		}
	}
	return false
}

// runErrorMessage renders a run error for display. A *comfy.PromptValidationError
// yields its message plus a compact per-node summary; other errors yield their
// text. Both are UNTRUSTED (comfy-authored) and are escaped at render time.
func runErrorMessage(err error) string {
	var ve *comfy.PromptValidationError
	if errors.As(err, &ve) {
		msg := "ComfyUI rejected the workflow"
		if ve.Message != "" {
			msg += ": " + ve.Message
		}
		if n := len(ve.NodeErrors); n > 0 {
			ids := make([]string, 0, n)
			for id := range ve.NodeErrors {
				ids = append(ids, id)
			}
			msg += fmt.Sprintf(" (node(s) %s)", strings.Join(ids, ", "))
		}
		return msg
	}
	return err.Error()
}
