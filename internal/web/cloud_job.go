package web

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/comfy"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// Cloud run phases (cloudJob.phase). These are the UI-facing coarse states; the
// finer orchestration status enum is carried separately in the message.
const (
	cloudPhasePreparing = "preparing"
	cloudPhaseRunning   = "running"
	cloudPhaseDone      = "done"
	cloudPhaseFailed    = "failed"
)

// cloudJob is the in-memory state of a single background cloud run. All fields are
// read/written only under Server.cloudMu.
//
// It mirrors runJob's race-safe streaming pattern (append/mutate + snapshot both
// under the mutex). It is deliberately a PARALLEL job type rather than a merge of
// the local run machinery: the cloud path polls a remote Workflow object (status
// enum + blob URLs) instead of ComfyUI /history+/queue, so a shared struct would
// carry two disjoint field sets. The snapshot/poll/stop SHAPE is shared (same
// stable-container poll contract, same one-global-run guard, same gallery idea),
// which is what keeps the streaming rules consistent.
type cloudJob struct {
	running    bool
	workflowID int64 // the local workflow id (for fragment targeting)
	// cloudID is the orchestration workflow id (untrusted remote string; escaped).
	cloudID string
	phase   string
	// status is the raw orchestration status enum (untrusted; escaped).
	status string
	// message is a human status/error line (may embed untrusted remote text →
	// escaped at render time).
	message string
	// affordability is true when the run failed the submit-time minimumDurationSeconds
	// gate (a 400 on a GATED submit): the terminal state then offers a "run anyway"
	// (gate-skipped) retry instead of a plain failure.
	affordability bool
	// urns is the resource URN list this run used, carried so the affordability
	// terminal state can re-POST the identical set with the gate skipped.
	urns       []string
	blobURLs   []string
	stopped    bool
	err        error
	startedAt  time.Time
	finishedAt time.Time
	cancel     context.CancelFunc
}

// cloudUpdater lets runCloud stream status transitions into the job under the
// mutex (mirrors runUpdater for the local run).
type cloudUpdater struct {
	setStatus  func(phase, status, msg string)
	setCloudID func(id string)
}

// startCloudRun launches a background cloud run for wf with the given resource
// URNs, unless one is already running (idempotent — a re-click starts no second
// goroutine). Same one-global-run invariant as the local run.
//
// apiGraph is the API-format graph to submit (the CONVERTED graph for a UI-format
// workflow) — resolved by the caller via cloudAPIGraph so this goroutine never
// re-reads wf.Graph. It is passed as a value into the goroutine (no shared mutable
// state).
//
// skipGate omits the submit-time minimumDurationSeconds affordability gate (the
// "run anyway" path); the default (false) applies the gate.
func (s *Server) startCloudRun(wf *store.Workflow, apiGraph json.RawMessage, urns []string, skipGate bool) {
	s.cloudMu.Lock()
	defer s.cloudMu.Unlock()
	if s.cloudJob != nil && s.cloudJob.running {
		return
	}

	base := s.baseCtx
	if base == nil {
		base = context.Background()
	}
	ctx, cancel := context.WithTimeout(base, cloudRunBudget)
	job := &cloudJob{
		running: true, workflowID: wf.ID, phase: cloudPhasePreparing,
		message: "Submitting to CivitAI cloud…", startedAt: time.Now(), cancel: cancel,
		urns: urns,
	}
	s.cloudJob = job

	up := cloudUpdater{
		setStatus: func(phase, status, msg string) {
			s.cloudMu.Lock()
			job.phase, job.status, job.message = phase, status, msg
			s.cloudMu.Unlock()
		},
		setCloudID: func(id string) {
			s.cloudMu.Lock()
			job.cloudID = id
			s.cloudMu.Unlock()
		},
	}

	graph := []byte(apiGraph)
	go func() {
		defer cancel()
		var urls []string
		var err error
		// The poll loop decodes untrusted remote JSON; a panic must fail THIS job,
		// not crash the server.
		func() {
			defer func() {
				if r := recover(); r != nil {
					err = fmt.Errorf("cloud run panicked: %v", r)
				}
			}()
			urls, err = s.runCloud(ctx, graph, urns, skipGate, up)
		}()
		s.cloudMu.Lock()
		defer s.cloudMu.Unlock()
		job.running = false
		job.finishedAt = time.Now()
		switch {
		case job.stopped:
			job.phase, job.message = cloudPhaseFailed, "Cloud run canceled."
		case err != nil && !skipGate && isCloudAffordabilityReject(err):
			// A 400 on a GATED submit is (very likely) the minimumDurationSeconds
			// affordability gate rejecting the run. We can't perfectly discriminate an
			// affordability-400 from a bad-graph-400, but offering "run anyway" is safe:
			// if it was a graph error it simply fails again with the same detail. Only
			// do this for a gated submit — a skip-gate run that still 400s is a real
			// failure (run-anyway already skipped the gate, so retrying cannot help).
			job.phase, job.err, job.affordability, job.message =
				cloudPhaseFailed, err, true, cloudErrorMessage(err)
		case err != nil:
			job.phase, job.err, job.message = cloudPhaseFailed, err, cloudErrorMessage(err)
		default:
			job.phase, job.blobURLs = cloudPhaseDone, urls
			if len(urls) == 0 {
				job.message = "Cloud run complete (no images returned)."
			} else {
				job.message = "Cloud run complete."
			}
		}
	}()
}

// runCloud submits the template for real and polls the orchestration workflow to a
// terminal state, returning the result blob URLs on success or an error otherwise.
//
// Unless skipGate is set, the submit carries the minimumDurationSeconds affordability
// gate (the real runaway-spend protection — whatif's cost preview is inert for the
// per-second-metered customComfy step). skipGate omits it (the "run anyway" path).
// The gate is enforced at THIS real submit; the whatif preview is best-effort.
func (s *Server) runCloud(ctx context.Context, graph []byte, urns []string, skipGate bool, up cloudUpdater) ([]string, error) {
	client := s.cloud()
	if client == nil {
		return nil, fmt.Errorf("CivitAI cloud is not configured (set a CivitAI token)")
	}

	tmpl := comfy.NewCustomComfyTemplate(graph, urns)
	if !skipGate {
		tmpl = tmpl.WithMinimumDuration(defaultCloudMinDurationSeconds)
	}
	wf, err := client.SubmitCloud(ctx, tmpl, false)
	if err != nil {
		return nil, err
	}
	if wf.ID == "" {
		return nil, fmt.Errorf("orchestration API returned no workflow id")
	}
	up.setCloudID(wf.ID)

	// The submit response may already be terminal (200 done) or still running (202).
	if comfy.CloudStatusTerminal(wf.Status) {
		return cloudTerminalResult(wf)
	}
	up.setStatus(cloudPhaseRunning, wf.Status, cloudStatusLine(wf.Status))

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(s.cloudPollInterval):
		}
		cur, err := client.GetCloudWorkflow(ctx, wf.ID)
		if err != nil {
			return nil, fmt.Errorf("poll cloud workflow: %w", err)
		}
		if comfy.CloudStatusTerminal(cur.Status) {
			return cloudTerminalResult(cur)
		}
		up.setStatus(cloudPhaseRunning, cur.Status, cloudStatusLine(cur.Status))
	}
}

// cloudTerminalResult maps a terminal Workflow to (blobURLs, err): succeeded →
// the blob URLs; failed/expired/canceled → an error naming the status.
func cloudTerminalResult(wf *comfy.CloudWorkflow) ([]string, error) {
	if strings.EqualFold(strings.TrimSpace(wf.Status), comfy.CloudStatusSucceeded) {
		return wf.BlobURLs(), nil
	}
	return nil, fmt.Errorf("cloud run %s", strings.TrimSpace(wf.Status))
}

// cloudStatusLine renders a human line for an in-flight orchestration status.
func cloudStatusLine(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case comfy.CloudStatusUnassigned, comfy.CloudStatusScheduled:
		return "Scheduled — waiting for a worker…"
	case comfy.CloudStatusPreparing:
		return "Preparing…"
	case comfy.CloudStatusProcessing:
		return "Generating on CivitAI cloud…"
	default:
		return "Working…"
	}
}

// stopCloudRun requests cancellation of the active cloud run and cancels its poll
// context. Idempotent.
func (s *Server) stopCloudRun() {
	s.cloudMu.Lock()
	j := s.cloudJob
	if j == nil || !j.running {
		s.cloudMu.Unlock()
		return
	}
	j.stopped = true
	cancel := j.cancel
	cloudID := j.cloudID
	s.cloudMu.Unlock()
	if cancel != nil {
		cancel()
	}
	// Best-effort remote cancel (PUT status=canceled) on its own short deadline.
	if cloudID != "" {
		if c := s.cloud(); c != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = c.CancelCloudWorkflow(ctx, cloudID)
		}
	}
}

// cloudSnapshot is a locked, self-consistent view of the cloud run job.
type cloudSnapshot struct {
	Started, Running bool
	WorkflowID       int64
	CloudID          string
	Phase            string
	Status           string
	Message          string
	BlobURLs         []string
	Stopped          bool
	// Affordability marks a gated-submit 400 (the affordability terminal state); URNs
	// carries the run's resource list so the "run anyway" button can re-POST it.
	Affordability bool
	URNs          []string
}

// cloudJobState returns a locked snapshot of the current cloud run job.
func (s *Server) cloudJobState() cloudSnapshot {
	s.cloudMu.Lock()
	defer s.cloudMu.Unlock()
	j := s.cloudJob
	if j == nil {
		return cloudSnapshot{}
	}
	urls := make([]string, len(j.blobURLs))
	copy(urls, j.blobURLs)
	jurns := make([]string, len(j.urns))
	copy(jurns, j.urns)
	return cloudSnapshot{
		Started: true, Running: j.running, WorkflowID: j.workflowID,
		CloudID: j.cloudID, Phase: j.phase, Status: j.status, Message: j.message,
		BlobURLs: urls, Stopped: j.stopped,
		Affordability: j.affordability, URNs: jurns,
	}
}
