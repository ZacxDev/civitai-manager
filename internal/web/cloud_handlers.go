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

// cloudRunBudget is a runaway backstop for a cloud run: the orchestrator does the
// work, we only poll, so this bounds a genuinely stuck poll loop (a job whose
// remote never settles) from leaking a goroutine forever.
const cloudRunBudget = 30 * time.Minute

// defaultCloudPollInterval is how often the cloud run goroutine polls
// GetCloudWorkflow. It is above the local run cadence — remote generation is
// slower and the API is edge-cached, so polling faster wastes requests. It seeds
// the per-Server Server.cloudPollInterval field; tests shrink that field on their
// own Server instance (never a shared global) for a fast, deterministic
// running→done transition — the poll goroutine reads it without any concurrent
// write, so there is no data race.
const defaultCloudPollInterval = 3 * time.Second

// defaultCloudMinDurationSeconds is the submit-time affordability floor applied to
// a cloud run: the orchestrator rejects the submit (HTTP 400) unless the user can
// afford at least this many seconds of generation. 300s (5 min) mirrors the CivitAI
// consumer (comfy-cloud) default. This is the REAL runaway-spend protection for the
// per-second-metered customComfy step (whatif's upfront cost is inert). The user can
// override it per-run via "run anyway", which resubmits with the gate omitted.
const defaultCloudMinDurationSeconds = 300

// cloudClient is the CivitAI orchestration surface the cloud handlers need. It is
// an interface so tests can inject a fake; *comfy.CloudClient satisfies it.
type cloudClient interface {
	SubmitCloud(ctx context.Context, template comfy.CloudTemplate, whatif bool) (*comfy.CloudWorkflow, error)
	GetCloudWorkflow(ctx context.Context, id string) (*comfy.CloudWorkflow, error)
	CancelCloudWorkflow(ctx context.Context, id string) error
}

// cloud builds the orchestration client (via the test seam, or from config).
// Returns nil when the CivitAI token is unset (cloud auth impossible).
func (s *Server) cloud() cloudClient {
	if s.cloudClientFn != nil {
		return s.cloudClientFn()
	}
	if strings.TrimSpace(s.cfg.Token) == "" {
		return nil
	}
	return comfy.NewCloudClient("", s.cfg.Token)
}

// storeResourceLookup adapts *store.Store to comfy.ResourceLookup so the resolver
// stays store-agnostic (and unit-testable with a fake).
type storeResourceLookup struct{ st *store.Store }

func (l storeResourceLookup) LocalFileByBasename(basename string) (*comfy.LocalMatch, error) {
	lf, err := l.st.LocalFileByBasename(basename)
	if err != nil || lf == nil {
		return nil, err
	}
	m := &comfy.LocalMatch{}
	if lf.ModelID != nil {
		m.ModelID = *lf.ModelID
	}
	if lf.VersionID != nil {
		m.VersionID = *lf.VersionID
	}
	return m, nil
}

// ModelTypeBaseModel parses the cached model-detail JSON for the model's type and
// the specific version's baseModel. ok is false when the model is not cached or
// the version is not present in its modelVersions[].
func (l storeResourceLookup) ModelTypeBaseModel(modelID, versionID int) (string, string, bool) {
	entry, err := l.st.GetModelCache(modelID)
	if err != nil || entry == nil {
		return "", "", false
	}
	var parsed struct {
		Type          string `json:"type"`
		ModelVersions []struct {
			ID        int    `json:"id"`
			BaseModel string `json:"baseModel"`
		} `json:"modelVersions"`
	}
	if err := json.Unmarshal(entry.Raw, &parsed); err != nil {
		return "", "", false
	}
	for _, v := range parsed.ModelVersions {
		if v.ID == versionID {
			return parsed.Type, v.BaseModel, true
		}
	}
	return "", "", false
}

// resolveWorkflowResources runs the resolution chain for a workflow, returning the
// resolved-resource rows and whether the graph is usable as-is (API format). A
// UI-format graph cannot be resolved into a runnable API graph without a live
// ComfyUI /object_info, so cloud run currently requires API format.
func (s *Server) resolveWorkflowResources(wf *store.Workflow) (rows []comfy.ResolvedResource, apiFormat bool) {
	if wf.Format != store.WorkflowFormatAPI {
		return nil, false
	}
	rows, _ = comfy.ResolveResources([]byte(wf.Graph), storeResourceLookup{st: s.store})
	return rows, true
}

// handleWorkflowCloud renders the cloud-run panel: the resolved-resources table,
// the editable URN textarea, the egress+Buzz warning, and the Estimate button.
// GET (no state change, no CSRF); loopback-gated (it reaches civitai.com on the
// subsequent POSTs and exposes resolution of an arbitrary workflow's resources).
func (s *Server) handleWorkflowCloud(w http.ResponseWriter, r *http.Request) {
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
	rows, apiFormat := s.resolveWorkflowResources(wf)
	view := cloudPanelView{
		wfID:      wf.ID,
		enabled:   s.cfg.ComfyCloud,
		apiFormat: apiFormat,
		rows:      rows,
		snap:      s.cloudJobState(),
	}
	s.render(w, http.StatusOK, cloudPanelFragment(view, s.csrf))
}

// handleWorkflowCloudWhatif builds the template from the user-edited URNs and asks
// the orchestrator for a cost estimate (whatif=true — no run, no spend). CSRF is
// validated BEFORE any egress call; loopback-gated.
func (s *Server) handleWorkflowCloudWhatif(w http.ResponseWriter, r *http.Request) {
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
	wf, urns, ok := s.cloudPrepare(w, r)
	if !ok {
		return
	}
	client := s.cloud()
	if client == nil {
		s.render(w, http.StatusOK, cloudEstimateFragment(cloudEstimateView{
			wfID: wf.ID, err: "CivitAI cloud is not configured (set a CivitAI token).",
		}, s.csrf))
		return
	}
	// Submit the whatif WITH the affordability gate so the user gets a FREE
	// affordability preview before spending. It is UNKNOWN whether whatif=true
	// actually enforces the gate (it may just return the normal cost-0 estimate);
	// both outcomes are handled — the gate is verified-at-real-submit, this preview
	// is best-effort. If whatif ignores the gate, the flow still works (Run for real
	// applies it, and the real-run affordability terminal state offers "run anyway").
	tmpl := comfy.NewCustomComfyTemplate([]byte(wf.Graph), urns).
		WithMinimumDuration(defaultCloudMinDurationSeconds)
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	res, err := client.SubmitCloud(ctx, tmpl, true)
	view := cloudEstimateView{wfID: wf.ID, urns: urns}
	if err != nil {
		// A 400 on the gated whatif is (very likely) the affordability gate: surface it
		// as a distinct affordability warning + "run anyway", NOT a generic estimate
		// failure. A non-400 stays a normal estimate error.
		if isCloudAffordabilityReject(err) {
			view.affordability = true
		}
		view.err = cloudErrorMessage(err)
	} else {
		view.cost = res.BaseCost()
		view.insufficientBuzz = res.InsufficientBuzz()
		view.estimated = true
	}
	s.render(w, http.StatusOK, cloudEstimateFragment(view, s.csrf))
}

// handleWorkflowCloudRun submits the workflow for real (whatif=false) and starts a
// race-safe background poll job. CSRF validated BEFORE the egress/spend call;
// loopback-gated.
func (s *Server) handleWorkflowCloudRun(w http.ResponseWriter, r *http.Request) {
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
	wf, urns, ok := s.cloudPrepare(w, r)
	if !ok {
		return
	}
	if client := s.cloud(); client == nil {
		s.render(w, http.StatusOK, cloudStatusFragment(cloudSnapshot{
			Started: true, WorkflowID: wf.ID, Phase: cloudPhaseFailed,
			Message: "CivitAI cloud is not configured (set a CivitAI token).",
		}, wf.ID, s.csrf))
		return
	}
	// run_anyway (present/non-empty) skips the submit-time affordability gate — the
	// "run anyway" retry offered after the gate rejected an earlier submit. Absent ⇒
	// apply the gate (the default, affordability floor honored).
	skipGate := strings.TrimSpace(r.FormValue("run_anyway")) != ""
	s.startCloudRun(wf, urns, skipGate)
	s.render(w, http.StatusOK, cloudStatusFragment(s.cloudJobState(), wf.ID, s.csrf))
}

// cloudPrepare loads the workflow, enforces the comfy_cloud gate + API-format
// requirement, and parses the edited URN textarea. It writes the appropriate
// fragment and returns ok=false when the request cannot proceed.
func (s *Server) cloudPrepare(w http.ResponseWriter, r *http.Request) (*store.Workflow, []string, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad workflow id", http.StatusBadRequest)
		return nil, nil, false
	}
	wf, err := s.store.GetWorkflow(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return nil, nil, false
	}
	if err != nil {
		s.renderError(w, "load workflow", err)
		return nil, nil, false
	}
	if !s.cfg.ComfyCloud {
		s.render(w, http.StatusOK, errorNote("CivitAI cloud run is disabled. Enable comfy_cloud in your config."))
		return nil, nil, false
	}
	if wf.Format != store.WorkflowFormatAPI {
		s.render(w, http.StatusOK, errorNote("Cloud run currently requires an API-format workflow."))
		return nil, nil, false
	}
	return wf, parseURNs(r.FormValue("resources")), true
}

// parseURNs splits the textarea into one URN per line, trimming blanks.
func parseURNs(raw string) []string {
	var out []string
	for _, line := range strings.Split(raw, "\n") {
		if u := strings.TrimSpace(line); u != "" {
			out = append(out, u)
		}
	}
	return out
}

// handleWorkflowCloudStatus is polled by the running cloud fragment. GET (no state
// change, no CSRF); loopback-gated.
func (s *Server) handleWorkflowCloudStatus(w http.ResponseWriter, r *http.Request) {
	if !s.gate(w) {
		return
	}
	id, _ := strconv.ParseInt(r.URL.Query().Get("workflow_id"), 10, 64)
	s.render(w, http.StatusOK, cloudStatusFragment(s.cloudJobState(), id, s.csrf))
}

// handleWorkflowCloudStop requests cancellation of the active cloud run. CSRF +
// loopback-gated; idempotent.
func (s *Server) handleWorkflowCloudStop(w http.ResponseWriter, r *http.Request) {
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
	s.stopCloudRun()
	id, _ := strconv.ParseInt(r.FormValue("workflow_id"), 10, 64)
	s.render(w, http.StatusOK, cloudStatusFragment(s.cloudJobState(), id, s.csrf))
}

// isCloudAffordabilityReject reports whether err is an orchestration 400 — the
// status the submit-time minimumDurationSeconds affordability gate rejects with. It
// cannot perfectly distinguish an affordability-400 from a bad-graph-400, but the
// caller only uses it to OFFER a gate-skipped "run anyway" retry, which is safe: a
// genuine graph error simply fails again with the same detail.
func isCloudAffordabilityReject(err error) bool {
	var p *comfy.CloudProblem
	return errors.As(err, &p) && p.IsBadRequest()
}

// cloudErrorMessage renders a cloud error for display. A *comfy.CloudProblem
// yields its title/detail; other errors yield their text. Both are UNTRUSTED
// (remote) and escaped at render time.
func cloudErrorMessage(err error) string {
	var p *comfy.CloudProblem
	if errors.As(err, &p) {
		detail := strings.TrimSpace(p.Detail)
		if detail == "" {
			detail = strings.TrimSpace(p.Title)
		}
		if detail == "" {
			detail = fmt.Sprintf("orchestration API returned status %d", p.StatusCode)
		}
		return detail
	}
	return err.Error()
}
