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
	g "maragu.dev/gomponents"
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
	// SaveUserWorkflow writes a UI-format graph into ComfyUI's user workflow store
	// (the "Open in ComfyUI" path). relPath is sanitized by the caller.
	SaveUserWorkflow(ctx context.Context, relPath string, graph json.RawMessage) error
	// ExtensionPing feature-detects the civitai-manager ComfyUI helper. A missing
	// helper is comfy.ErrExtensionAbsent — an expected outcome, not a failure.
	ExtensionPing(ctx context.Context) (*comfy.ExtensionInfo, error)
	// ExtensionAsset verifies the helper's FRONTEND script is actually served. It
	// is the second, NON-OPTIONAL half of feature detection: the ping route can
	// outlive a deleted helper (startup-registered, in-memory), the asset cannot.
	ExtensionAsset(ctx context.Context) error
	// ExtensionOpen asks the helper to broadcast an "open this workflow" event to
	// already-open editor tabs.
	ExtensionOpen(ctx context.Context, relPath string) error
}

// Run phases (job.phase).
const (
	runPhasePreparing   = "preparing"
	runPhaseDownloading = "downloading"
	runPhaseQueued      = "queued"
	runPhaseRunning     = "running"
	runPhaseDone        = "done"
	runPhaseFailed      = "failed"
)

// runJob is the in-memory state of a single background workflow run. All fields are
// read/written only under Server.runMu.
type runJob struct {
	running    bool
	workflowID int64
	// seq is this run's monotonic identity (Server.runSeq at start time). It is
	// surfaced as data-run-seq on the run-status fragment so a run's terminal panel is
	// distinguishable from a stale prior run's panel in the shared #run-status.
	seq      int64
	promptID string
	phase    string
	queuePos int
	// message is a human status/error line. It may embed UNTRUSTED ComfyUI error
	// text, so every render routes it through g.Text (auto-escaped).
	message string
	images  []comfy.ImageRef
	// preflight is set when the run aborted on a failed preflight (missing nodes/
	// models); warnings is set when a UI→API conversion produced unrunnable nodes.
	preflight *comfy.PreflightReport
	// missingModels is the enriched analysis for a failed preflight (nil otherwise),
	// carried into the snapshot so the failure panel can render resolve/substitute.
	missingModels []comfy.MissingModel
	// missingResolved is the eager, at-settle CivitAI resolution per missing filename
	// (computed ONCE while /object_info is in hand — never per poll). libMeta is the
	// batch local-model enrichment for the substitute candidates, keyed by lowercased
	// basename. Both are read-only after settle.
	missingResolved map[string]missingResolution
	libMeta         map[string]store.LocalModelMeta
	// nodeAttr is the at-settle custom-node attribution for preflight.MissingNodes
	// (which pack provides each missing class_type, and whether it can be installed).
	// Computed ONCE at settle — never per poll — and read-only afterwards.
	nodeAttr nodeAttribution
	warnings []string
	// aborted marks a run that never submitted because the UI→API conversion yielded
	// zero runnable nodes (an all-disabled template / no installed node types). It
	// gets a distinct "nothing to run" report rather than the generic failure alert.
	aborted bool
	// uiFormat records whether the run's workflow is a UI-format ("Save") graph. It is
	// carried on the JOB (not re-read per poll) so the failure report can offer the
	// "Open in ComfyUI" escape hatch, which only applies to an editable UI graph.
	uiFormat   bool
	stopped    bool
	err        error
	startedAt  time.Time
	finishedAt time.Time
	cancel     context.CancelFunc

	// ── Batch fields (a single run is simply a batch of 1) ───────────────────
	//
	// ONE job owns N prompts and submits them SEQUENTIALLY. There is no second job
	// type: runSeq / data-run-seq / #run-status / runStatusFragment are all keyed to
	// one job, and a parallel batch-job type would have to duplicate every one of
	// them. batchTotal == 1 && batchID == "" is exactly today's behaviour.

	// batchID groups this batch's captured generations (generations.batch_id). It is
	// "" for a single run, so an ordinary run's row stays NULL in all three batch
	// columns, indistinguishable from every pre-0016 row.
	batchID string
	// batchTotal is N AS REQUESTED, batchIndex the 1-based item currently in flight,
	// batchDone the number that reached phase==done. Keeping total as requested is
	// what lets a halt say "3 of 8 — 5 not started" instead of silently reporting a
	// batch of 3.
	batchTotal int
	batchIndex int
	batchDone  int
	// batchStop is the Stop request. It is READ at the top of every item, under
	// runMu, BEFORE any work — so the un-submitted remainder is never submitted.
	// Nothing is queued ahead, which is the whole payoff of sequential submission.
	batchStop bool
	// itemCancel cancels the CURRENT item only (the batch-level cancel is `cancel`).
	// Cancelling the item is what unblocks realRun's poll loop.
	itemCancel context.CancelFunc
	// batchSummary is the one extra terminal line a MULTI-item batch renders above
	// the existing (unchanged) terminal fragment. It is kept OFF `message` on
	// purpose: message may carry untrusted ComfyUI error text and drives the failure
	// panel, and the batch accounting must not be entangled with it.
	batchSummary string
}

// runResult is what runFn returns: images on success, or a preflight report /
// conversion warnings when the run was aborted BEFORE submitting.
type runResult struct {
	Images    []comfy.ImageRef
	Preflight *comfy.PreflightReport
	// MissingModels is the enriched per-missing-model analysis (search query +
	// installed substitute candidates) computed WHILE the live /object_info schema
	// is in hand, so the failure panel can offer resolve + substitute actions
	// without re-fetching. Set only alongside a failed Preflight.
	MissingModels []comfy.MissingModel
	// MissingResolved is the eager per-filename CivitAI resolution and LibMeta the
	// batch local-model enrichment of substitute candidates — both computed ONCE at
	// settle so the terminal popover renders inline without any per-poll API call.
	MissingResolved map[string]missingResolution
	LibMeta         map[string]store.LocalModelMeta
	// NodeAttr is the custom-node attribution for Preflight.MissingNodes, resolved
	// at settle alongside MissingResolved so the failure panel is actionable
	// without any per-poll network call.
	NodeAttr nodeAttribution
	Warnings []string
	PromptID string
}

// runOptions carries per-run overrides that must NOT be persisted to the stored
// workflow. Substitute maps a missing model filename → an installed substitute:
// the run applies it to the CONVERTED api graph (an ephemeral copy) before
// preflight/submit, leaving the stored workflow untouched. OptionFixes maps a
// combo-input (name, old value) → a chosen valid value: applied the same ephemeral
// way to rewrite `value_not_in_list` combos (validated on-list against the live
// object_info before injection).
type runOptions struct {
	Substitute  map[string]string
	OptionFixes map[comfy.OptionFixKey]string
	// WidgetOverrides carries per-(node,input) edits from the "Parameters" panel: an
	// ephemeral rewrite of a targeted node's targeted scalar input on the CONVERTED
	// api graph (via comfy.ApplyWidgetOverrides), keyed by node+input since a prompt
	// is node-specific. Applied like the other overrides — on a copy, never persisted.
	WidgetOverrides map[comfy.WidgetOverrideKey]string
	// UIWidgetOverrides carries the "Parameters" panel's per-run edits for a
	// UI-format workflow, keyed by (node id, widgets_values index) — the coordinates
	// DetectRunInputs emits. They are applied to a COPY of the UI graph BEFORE
	// conversion, so ConvertUIToAPI maps each edited slot onto the correct api input
	// name with the live schema. This is what makes an edit to a parameter that lives
	// on an UPSTREAM node (a widget converted to an input) actually reach ComfyUI.
	UIWidgetOverrides map[comfy.UIWidgetKey]string
	// ModeSelection picks ONE pipeline out of a multi-mode template workflow, keyed by
	// comfy.ModeSelector.Key → comfy.ModeGroup.Key. It is applied to a COPY of the UI
	// graph BEFORE conversion (comfy.ApplyModeSelection), un-bypassing the chosen
	// group's nodes so there is something to convert at all. Ephemeral like every other
	// field here — the stored workflow is never rewritten.
	ModeSelection map[string]string
	// PresetID/PresetName attribute the run to the saved run preset ("tab") it was
	// started from. They are pure ATTRIBUTION — nothing about the run behaves
	// differently — and are snapshotted onto the captured generation so a deleted
	// preset's outputs stay labeled (the same idiom as workflow_name). Zero/"" for a
	// run that did not come from a preset.
	PresetID   int64
	PresetName string
	// BatchID/BatchIndex/BatchTotal attribute the run to the "Queue ×N" batch it is
	// an item of. Like PresetID/PresetName they are pure ATTRIBUTION — nothing about
	// the run behaves differently — and are snapshotted onto the captured generation
	// so N runs group into one batch in the gallery instead of flooding it with N
	// undifferentiated tiles. Zero/"" for an ordinary single run.
	BatchID    string
	BatchIndex int
	BatchTotal int
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
func (s *Server) startRun(wf *store.Workflow, opts runOptions) bool {
	return s.startRunWithMessage(wf, opts, "Starting run…")
}

// startRunNotice starts a single run and returns the line to render ABOVE the status
// fragment: "" when it started, and the refusal otherwise.
//
// Every fragment-returning run handler goes through it. They all used to discard the
// refusal and re-render the OTHER run's status, so a click while something was in
// flight looked like nothing happened at all — tolerable for a re-click, actively
// confusing for "I clicked Queue ×8".
func (s *Server) startRunNotice(wf *store.Workflow, opts runOptions) string {
	started, ref := s.startBatch(wf, opts, batchSpec{Count: 1, Message: "Starting run…"})
	if started {
		return ""
	}
	return ref.notice()
}

// renderRunStatus writes the refusal line (if any) plus the run-status fragment.
// The line is a SIBLING above #run-status's content, never a wrapper: the poller
// still swaps this whole body into the stable #run-status container.
func (s *Server) renderRunStatus(w http.ResponseWriter, wfID int64, notice string) {
	s.render(w, http.StatusOK, g.Group([]g.Node{
		runNoticeLine(notice, false),
		runStatusFragment(s.runJobState(), wfID, s.csrf, s.comfyDownloadEligible(), s.nsfwMode()),
	}))
}

// startRunWithMessage is startRun with the job's OPENING status line supplied, so a
// caller that reached the run by a notable route (e.g. "the file was already installed,
// nothing was downloaded") can say so instead of showing an indistinguishable
// "Starting run…". msg is server-authored text, never reflected request input.
//
// It reports whether the job actually STARTED. false means the one-run-at-a-time
// guard discarded this call — a caller that already did visible work for this click
// (e.g. the batch install's N resolutions) must say so rather than answer with the
// other job's panel.
func (s *Server) startRunWithMessage(wf *store.Workflow, opts runOptions, msg string) bool {
	started, _ := s.startBatch(wf, opts, batchSpec{Count: 1, Message: msg})
	return started
}

// settleAndCapture is the settle-then-capture tail of the DOWNLOAD-THEN-RUN job
// (run_download.go), which builds and drives its own runJob: it settles the finished
// job under runMu, then runs the best-effort output capture.
//
// The ordinary run path no longer comes through here — it is a batch of one and
// runBatch inlines this same sequence per item (see run_batch.go), because a batch
// must settle the ITEM without finishing the JOB. Both paths still share
// applyRunOutcomeLocked / applyItemOutcomeLocked, so the classification cannot
// diverge; only the running=false transition differs, and that is the whole point.
//
// Two properties are load-bearing and must not be reordered:
//   - applyRunOutcomeLocked runs under runMu, and the phase is snapshotted under
//     that SAME lock (a later read would race a new job).
//   - the capture runs OUTSIDE runMu — it does network /view fetches + disk writes
//     and must never block a status poll or hold the run mutex.
//
// Capture happens on the SUCCESS path only and is fully panic-guarded, so a
// capture failure can NEVER change or crash the settled run. captureGeneration
// swallows its own errors; this recover is the last-resort guard around a
// seam/panic.
func (s *Server) settleAndCapture(job *runJob, wf *store.Workflow, opts runOptions, res *runResult, err error) {
	s.runMu.Lock()
	s.applyRunOutcomeLocked(job, res, err)
	phase := job.phase
	s.runMu.Unlock()

	if phase != runPhaseDone || res == nil || len(res.Images) == 0 {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			s.log.Error("output capture panicked", "err", r)
		}
	}()
	capture := s.captureFn
	if capture == nil {
		capture = s.captureGeneration
	}
	capture(wf, opts, res)
}

// newRunUpdater builds the runUpdater that streams a run's phase transitions into
// job under runMu. Shared by startRun and startDownloadAndRun.
func (s *Server) newRunUpdater(job *runJob) runUpdater {
	return runUpdater{
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
}

// applyRunOutcomeLocked settles job from a run's (res, err) AND finishes it. The
// caller MUST hold runMu. It is the WHOLE-JOB settle — classification plus the
// running=false transition — and stays the shared tail of the download-then-run
// path (settleAndCapture).
//
// It is now the composition of the two halves the batch runner needs separately. A
// single run is a batch of one, so "settle the item, then finish the batch" is
// exactly what it always did.
func (s *Server) applyRunOutcomeLocked(job *runJob, res *runResult, err error) {
	s.applyItemOutcomeLocked(job, res, err)
	s.applyBatchOutcomeLocked(job)
}

// applyItemOutcomeLocked classifies ONE run's outcome into job: stopped, error,
// failed preflight, conversion warnings, success. The caller MUST hold runMu. The
// classification switch below is the ORIGINAL one, moved verbatim — nothing about
// how a run is judged changed, only when the job stops being "running".
//
// 🔴 IT DELIBERATELY DOES NOT TOUCH job.running.
// Between the items of a batch the run singleton MUST stay held. If this cleared
// `running`, a concurrent startRun would slip in through the gap between item i's
// settle and item i+1's submit, and two runs would be submitting into ComfyUI at
// once — a nondeterministic, load-dependent failure, the hardest kind to catch
// after the fact. Clearing it is applyBatchOutcomeLocked's job and happens exactly
// ONCE per batch. TestBatchSingletonHeldAcrossItems and
// TestBatchConcurrentStartRunRace exist for this four-line split alone.
//
// It reports whether the item ended in a NON-done state, which HALTS the batch (see
// runBatch): preflight failures are deterministic across items — only the seed
// differs — and the failure panel's fix actions each start a NEW run, so they
// cannot coherently target "item 4 of 8".
func (s *Server) applyItemOutcomeLocked(job *runJob, res *runResult, err error) bool {
	switch {
	case job.stopped:
		job.phase, job.message = runPhaseFailed, "Run stopped."
	case err != nil:
		// An empty-conversion abort (all nodes disabled / no installed types) gets a
		// dedicated "nothing to run" report carrying the actionable guidance, rather
		// than the generic failure message.
		var ece *comfy.ConversionEmptyError
		if errors.As(err, &ece) {
			job.phase, job.err, job.aborted = runPhaseFailed, err, true
			job.message = ece.Error()
		} else {
			job.phase, job.err, job.message = runPhaseFailed, err, runErrorMessage(err)
		}
	case res != nil && res.Preflight != nil:
		job.phase, job.preflight, job.missingModels = runPhaseFailed, res.Preflight, res.MissingModels
		job.missingResolved, job.libMeta = res.MissingResolved, res.LibMeta
		job.nodeAttr = res.NodeAttr
		job.message = preflightMessage(res.Preflight)
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
	return job.phase != runPhaseDone
}

// applyBatchOutcomeLocked FINISHES the job: it is the only place `running` is
// cleared, and it runs exactly once per batch. The caller MUST hold runMu.
//
// It also composes the batch summary line while every counter is still consistent
// under this lock — computing it later, from a snapshot, is how "3 of 8" drifts.
func (s *Server) applyBatchOutcomeLocked(job *runJob) {
	job.running = false
	job.finishedAt = time.Now()
	job.itemCancel = nil
	job.batchSummary = batchSummaryLine(job)
}

// batchSummaryLine is the extra terminal line a MULTI-item batch renders. It
// returns "" for a single run (batchTotal <= 1), so every existing terminal
// fragment is byte-identical to before.
//
// The three shapes match the three terminal states. `stopped` is checked FIRST
// because a Stop during item i settles that item as failed (the classification
// switch's first case), and reporting it as "halted" would blame the workflow for
// the user's own click.
func batchSummaryLine(job *runJob) string {
	if job.batchTotal <= 1 {
		return ""
	}
	done, idx, total := job.batchDone, job.batchIndex, job.batchTotal
	switch {
	case job.stopped:
		return fmt.Sprintf("Batch stopped — %d of %d completed, %d cancelled.",
			done, total, total-done)
	case job.phase != runPhaseDone:
		// batchTotal is N AS REQUESTED, so "not started" is honest about the runs that
		// were never submitted rather than silently reporting a smaller batch.
		return fmt.Sprintf("Batch halted at item %d of %d — %d completed, %d not started.",
			idx, total, done, total-idx)
	default:
		return fmt.Sprintf("%d of %d complete.", done, total)
	}
}

// realRun is the production run seam: load → (convert UI→API) → preflight → submit
// → poll. It aborts (without an error) on a failed preflight or on conversion
// warnings so a broken graph is never submitted.
func (s *Server) realRun(ctx context.Context, wf *store.Workflow, up runUpdater, opts runOptions) (*runResult, error) {
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
		// Apply the "Parameters" panel edits to a COPY of the UI graph FIRST: the value
		// a parameter shows may live on an upstream node whose widget was converted to
		// an input, and only the pre-conversion widgets_values slot is authoritative —
		// the converter then maps it onto the right api input. The stored workflow is
		// never touched (ApplyUIWidgetOverrides returns a new document).
		uiGraph := json.RawMessage(wf.Graph)
		// Multi-mode template: un-bypass the ONE pipeline the user picked before doing
		// anything else, so the converter has a runnable graph to work with. Applied to a
		// copy; a workflow with no mutually-exclusive selector (the overwhelming majority)
		// gets its bytes back unchanged.
		if len(opts.ModeSelection) > 0 {
			uiGraph = comfy.ApplyModeSelection(uiGraph, opts.ModeSelection)
		}
		if len(opts.UIWidgetOverrides) > 0 {
			uiGraph = comfy.ApplyUIWidgetOverrides(uiGraph, opts.UIWidgetOverrides)
		}
		g, warnings, cerr := comfy.ConvertUIToAPI(uiGraph, info)
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

	// Apply any one-off model substitution to the CONVERTED (ephemeral) graph. This
	// swaps every loader-input value equal to a missing filename for the chosen
	// installed substitute — the stored workflow is never touched (apiGraph is a
	// converted/parsed copy, never persisted).
	if len(opts.Substitute) > 0 {
		apiGraph = comfy.ApplySubstitutions(apiGraph, opts.Substitute)
	}

	// Apply any per-run "Parameters" edits to the CONVERTED (ephemeral) graph — before
	// preflight/submit so preflight sees the actual values that will run, and on a
	// copy so the stored workflow is never touched. ApplyWidgetOverrides rewrites only
	// existing scalar inputs on the targeted nodes (never links, never new keys).
	if len(opts.WidgetOverrides) > 0 {
		apiGraph = comfy.ApplyWidgetOverrides(apiGraph, opts.WidgetOverrides)
	}

	up.setPhase(runPhasePreparing, "Checking installed nodes & models…", 0)
	localHave := func(name string) bool {
		ok, _ := s.store.HasLocalFileNamed(name)
		return ok
	}
	report := comfy.Preflight(apiGraph, info, localHave)

	// Apply any user-selected incompatible-option fixes to the CONVERTED (ephemeral)
	// graph: validate each chosen value against the detected BadOptions (on-list ONLY —
	// an off-list value is refused, never injected), rewrite the matching combo inputs
	// across all nodes, and re-run preflight so OK reflects the fixed graph. The stored
	// workflow is never touched (apiGraph is a converted/parsed copy, never persisted).
	if len(opts.OptionFixes) > 0 && len(report.BadOptions) > 0 {
		valid := comfy.ValidateOptionFixes(opts.OptionFixes, report.BadOptions)
		if len(valid) > 0 {
			apiGraph = comfy.ApplyOptionFixes(apiGraph, valid)
			report = comfy.Preflight(apiGraph, info, localHave)
		}
	}

	if !report.OK {
		// Enrich the missing-models list with resolve queries + installed substitute
		// candidates while /object_info is in hand, so the failure panel is actionable.
		var missing []comfy.MissingModel
		var resolved map[string]missingResolution
		var libMeta map[string]store.LocalModelMeta
		// Attribute the missing custom-node classes to the packs that provide them,
		// HERE at settle (bounded), so the terminal panel can offer a gated install
		// and the ~1s run-status poll never reaches ComfyUI-Manager or the Registry.
		// Attribution runs on the CONVERTED api graph's missing classes (the
		// preflight report), never on the raw UI graph — subgraph UUIDs and rgthree
		// UI-only nodes have already been dropped by then.
		var nodeAttr nodeAttribution
		if len(report.MissingNodes) > 0 {
			nodeAttr = s.attributeMissingNodes(ctx, report.MissingNodes)
			nodeAttr.ComfyRoot = s.cfg.ComfyRoot
			nodeAttr.RemoteLookup = s.cfg.ResolveNodePacks
		}
		if len(report.MissingModels) > 0 {
			missing = comfy.AnalyzeMissingModels(apiGraph, info, report.MissingModels, wf.BaseModel)
			// Resolve each missing model to CivitAI + enrich substitute candidates ONCE,
			// HERE at settle (bounded), so the terminal Fix popover renders inline and the
			// ~1-2s run-status poll never triggers a CivitAI search.
			resolved, libMeta = s.resolveMissingModels(ctx, missing)
		}
		return &runResult{
			Preflight: &report, MissingModels: missing,
			MissingResolved: resolved, LibMeta: libMeta, NodeAttr: nodeAttr,
		}, nil
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

// stopRun cancels the running run — the WHOLE remaining batch — and best-effort
// interrupts ComfyUI. Idempotent.
//
// Its ordering is load-bearing and unchanged: `stopped` is set SYNCHRONOUSLY, under
// runMu, before the run goroutine settles, which is why runStatusFragment can key
// the terminal render off it (the Stop response and any in-flight poll immediately
// render the poller-free view, so the poll loop halts instead of re-arming).
//
// What happens to each part of a batch:
//
//   - THE CURRENTLY EXECUTING PROMPT: `Interrupt` is sent, once. It takes no prompt
//     id, so it interrupts whatever ComfyUI is executing right now — which is our
//     item, because we submit strictly one at a time and never enqueue ahead. ⚠ It
//     can also kill a prompt the user submitted from their own ComfyUI tab; that
//     hazard exists today for a single run and a batch does NOT widen it. Do not
//     add a queue-clear.
//   - THE UN-SUBMITTED REMAINDER: never submitted at all. batchStop is read at the
//     top of each item under runMu, before any work. There is nothing to cancel
//     because nothing was queued — the direct payoff of sequential submission.
//   - ALREADY-CAPTURED GENERATIONS: kept. Stop is not undo; the images are on disk
//     and the rows exist, and deleting them would destroy the user's output.
//
// The `stopped` early return makes a double-click a genuine no-op: a second
// Interrupt could otherwise land on whatever ComfyUI picked up next.
func (s *Server) stopRun() {
	s.runMu.Lock()
	j := s.runJob
	if j == nil || !j.running || j.stopped {
		s.runMu.Unlock()
		return
	}
	j.stopped = true
	j.batchStop = true
	cancel, itemCancel := j.cancel, j.itemCancel
	s.runMu.Unlock()
	// The ITEM context first — that is the one realRun's poll loop selects on — then
	// the batch context, which also stops the loop from starting anything further.
	if itemCancel != nil {
		itemCancel()
	}
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
	Seq              int64
	PromptID         string
	Phase            string
	Message          string
	QueuePos         int
	Images           []comfy.ImageRef
	Preflight        *comfy.PreflightReport
	MissingModels    []comfy.MissingModel
	MissingResolved  map[string]missingResolution
	LibMeta          map[string]store.LocalModelMeta
	NodeAttr         nodeAttribution
	Warnings         []string
	Aborted          bool
	UIFormat         bool
	Stopped          bool
	// Batch progress. BatchTotal <= 1 is a single run and every batch-aware render
	// falls back to today's markup exactly. All four are snapshotted under the SAME
	// runMu hold as the rest of the view, so a poller can never observe a torn
	// "item 4 of 3".
	BatchID      string
	BatchIndex   int
	BatchTotal   int
	BatchDone    int
	BatchSummary string
}

// IsBatch reports whether this snapshot describes a multi-item batch.
func (s runSnapshot) IsBatch() bool { return s.BatchTotal > 1 }

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
		Started: true, Running: j.running, WorkflowID: j.workflowID, Seq: j.seq,
		PromptID: j.promptID, Phase: j.phase, Message: j.message, QueuePos: j.queuePos,
		Images: imgs, Preflight: j.preflight, MissingModels: j.missingModels,
		MissingResolved: j.missingResolved, LibMeta: j.libMeta, NodeAttr: j.nodeAttr,
		Warnings: warns, Aborted: j.aborted, UIFormat: j.uiFormat, Stopped: j.stopped,
		BatchID: j.batchID, BatchIndex: j.batchIndex, BatchTotal: j.batchTotal,
		BatchDone: j.batchDone, BatchSummary: j.batchSummary,
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
	s.renderRunStatus(w, id, s.startRunNotice(wf, runOptions{ModeSelection: parseModeChoices(r.Form, wf)}))
}

// comfyStatusTimeout bounds the reachability probe so a dead/hung ComfyUI can
// never block the page render. It has its OWN short deadline (independent of the
// request) — a run itself may take minutes, but a health ping must be quick.
const comfyStatusTimeout = 3 * time.Second

// handleWorkflowRunComfyStatus pings ComfyUI and returns the reachability fragment
// (pill + enabled/disabled Run + Recheck) for #run-comfy-status. GET (no state
// change, no CSRF); loopback-gated like the other comfy-reaching endpoints. The
// probe uses a short, independent timeout so an unreachable/hung server degrades to
// the red pill instead of hanging the request.
func (s *Server) handleWorkflowRunComfyStatus(w http.ResponseWriter, r *http.Request) {
	if !s.gate(w) {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad workflow id", http.StatusBadRequest)
		return
	}
	client := s.comfy()
	if client == nil {
		s.render(w, http.StatusOK, runComfyStatusFragment(id, s.csrf, comfyStatusView{configured: false}))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), comfyStatusTimeout)
	defer cancel()
	view := comfyStatusView{configured: true, comfyURL: s.cfg.ComfyURL}
	if stats, serr := client.SystemStats(ctx); serr == nil && stats != nil {
		view.reachable = true
		view.version = stats.ComfyUIVersion
	}
	s.render(w, http.StatusOK, runComfyStatusFragment(id, s.csrf, view))
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
	s.render(w, http.StatusOK, runStatusFragment(s.runJobState(), id, s.csrf, s.comfyDownloadEligible(), s.nsfwMode()))
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
	s.render(w, http.StatusOK, runStatusFragment(s.runJobState(), id, s.csrf, s.comfyDownloadEligible(), s.nsfwMode()))
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
// preflightMessage builds the failure headline from WHICH preflight categories
// actually failed. A run blocked only by drifted combo values (BadOptions) must
// not claim "nodes or models are not installed" — those nodes ARE installed; it's
// the saved option values that are stale.
func preflightMessage(r *comfy.PreflightReport) string {
	if r == nil {
		return "Preflight failed."
	}
	missing := len(r.MissingNodes) > 0 || len(r.MissingModels) > 0
	opts := len(r.BadOptions) > 0
	switch {
	case missing && opts:
		return "Preflight failed — this workflow references nodes or models that are not installed, and some saved option values are no longer valid. Fix the items below, then run."
	case missing:
		return "Preflight failed — this workflow references nodes or models that are not installed."
	case opts:
		return "Preflight failed — some saved option values are no longer valid on your installed nodes. Pick a valid option for each below, then run."
	default:
		return "Preflight failed."
	}
}

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
