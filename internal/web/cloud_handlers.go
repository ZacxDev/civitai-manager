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

// cloudObjectInfoTimeout bounds the local-ComfyUI /object_info probe on the cloud
// path so a hung ComfyUI cannot stall the handler. A run itself may take minutes,
// but fetching the node schema for a UI→API conversion must be quick.
const cloudObjectInfoTimeout = 8 * time.Second

// cloudAPIGraph returns the API-format graph to submit to CivitAI cloud for wf.
//
//   - API-format workflows are returned unchanged.
//   - UI-format workflows are converted to API format using the LOCAL ComfyUI's
//     /object_info (mirroring the local-run path exactly), so cloud run is usable on
//     the UI-format workflows that make up the real library.
//
// ok is false — with a rendered explanation — when the workflow cannot be cloud-run:
//   - the local ComfyUI is unreachable (note explains how to fix; an EXPECTED
//     fallback, not a hard error);
//   - the conversion errored (note carries the escaped error);
//   - the conversion produced warnings (warnings carries the unrunnable/bypass/
//     unknown-node list — same abort-rather-than-mis-submit rule as local run, since
//     mis-submitting a broken graph wastes Buzz).
//
// 🔴 rawInfo IS RETURNED, NOT JUST CACHED. It is the /object_info body this call
// just fetched, and it is the FRESHEST view of the local ComfyUI this app ever
// holds. It was previously written to the display cache and dropped on the floor,
// after which the caller read the same ~4.66 MB back out of SQLite and re-parsed
// it — a needless read plus a full parse on the DOMINANT path (all 71 workflows on
// the operator's real database are UI-format), and, worse, a silent downgrade to
// STALE data whenever the cache write failed: PutComfyObjectInfo's error is
// swallowed by design (cacheComfyObjectInfo is best-effort so a run cannot fail
// over a display cache), and a read-only DB never writes at all. The classifier
// would then answer from an old row — or from no row, i.e. the pre-fix
// coreNodeClasses table — while the authoritative payload sat in the same call
// frame.
//
// It is nil on the API-format path, which never contacts ComfyUI at all. That is
// not a degenerate case to be tidied away: the cache is the ONLY origin source
// those workflows have, so the caller MUST keep the cache-read fallback.
func (s *Server) cloudAPIGraph(ctx context.Context, wf *store.Workflow) (apiGraph json.RawMessage, rawInfo []byte, warnings []string, note string, ok bool) {
	if wf.Format == store.WorkflowFormatAPI {
		return json.RawMessage(wf.Graph), nil, nil, "", true
	}
	client := s.comfy()
	if client == nil {
		return nil, nil, nil, s.cloudUnreachableNote(), false
	}
	octx, cancel := context.WithTimeout(ctx, cloudObjectInfoTimeout)
	defer cancel()
	info, rawInfo, err := client.ObjectInfoRaw(octx)
	if err != nil {
		return nil, nil, nil, s.cloudUnreachableNote(), false
	}
	// Same as the local run path: a fetched /object_info feeds the display cache.
	s.cacheComfyObjectInfo(rawInfo)
	g, warns, cerr := comfy.ConvertUIToAPI(json.RawMessage(wf.Graph), info)
	if cerr != nil {
		// An empty-conversion abort already carries a full, actionable sentence
		// (all-disabled template / no installed node types) — surface it directly
		// instead of wrapping it in the generic "could not convert" prefix.
		var ece *comfy.ConversionEmptyError
		if errors.As(cerr, &ece) {
			return nil, nil, nil, ece.Error(), false
		}
		return nil, nil, nil, "Could not convert this UI-format workflow to API format: " + cerr.Error(), false
	}
	if len(warns) > 0 {
		return nil, nil, warns, "", false
	}
	return g, rawInfo, nil, "", true
}

// cloudUnreachableNote explains the UI-format fallback: cloud converts via the local
// ComfyUI, which isn't reachable. It is an expected state, not an error.
func (s *Server) cloudUnreachableNote() string {
	if strings.TrimSpace(s.cfg.ComfyURL) == "" {
		return "This workflow is in UI format. Cloud run converts it to API format using your local ComfyUI, " +
			"but ComfyUI isn't configured (set comfy_url). Start ComfyUI (or import an API-format workflow) to run it on cloud."
	}
	return "This workflow is in UI format. Cloud run converts it to API format using your local ComfyUI, but " +
		"ComfyUI isn't reachable at " + s.cfg.ComfyURL + ". Start ComfyUI (or import an API-format workflow) to run it on cloud."
}

// resolveResources runs the resolution chain over an API-format graph, returning the
// resolved-resource rows for the cloud panel's table.
//
// rawInfo is the /object_info body the CALLER already holds (see cloudAPIGraph); it
// wins over the cache whenever it can classify anything, because it is strictly
// fresher and needs no SQLite read and no second parse.
//
// 🔴 THE CACHE FALLBACK IS NOT DEAD CODE — DO NOT DELETE IT. An API-format workflow
// never contacts ComfyUI (cloudAPIGraph returns before the fetch), so rawInfo is nil
// and the cached row is the only origin source those workflows have. The fallback
// also covers a rawInfo that exists but yields nothing (truncated/corrupt body), for
// which a stale row still beats no answer at all.
func (s *Server) resolveResources(apiGraph json.RawMessage, rawInfo []byte) []comfy.ResolvedResource {
	origins := comfy.NodeOrigins(rawInfo)
	if len(origins) == 0 {
		origins = s.comfyNodeOrigins()
	}
	rows, _ := comfy.ResolveResources(apiGraph, storeResourceLookup{st: s.store}, origins)
	return rows
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
	// The EFFECTIVE comfy_cloud state: an explicit config-file value wins, else the
	// DB toggle set from the connect block above this panel (see cloud_connect.go).
	enabled := s.cloudEnabled()
	view := cloudPanelView{
		wfID:    wf.ID,
		enabled: enabled,
		snap:    s.cloudJobState(),
	}
	if enabled {
		apiGraph, rawInfo, warnings, note, ok := s.cloudAPIGraph(r.Context(), wf)
		if ok {
			view.runnable = true
			view.willConvert = wf.Format != store.WorkflowFormatAPI
			view.rows = s.resolveResources(apiGraph, rawInfo)
		} else {
			view.note = note
			view.warnings = warnings
		}
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
	wf, apiGraph, urns, ok := s.cloudPrepare(w, r)
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
	tmpl := comfy.NewCustomComfyTemplate(apiGraph, urns).
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
	wf, apiGraph, urns, ok := s.cloudPrepare(w, r)
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
	s.startCloudRun(wf, apiGraph, urns, skipGate)
	s.render(w, http.StatusOK, cloudStatusFragment(s.cloudJobState(), wf.ID, s.csrf))
}

// cloudPrepare loads the workflow, enforces the comfy_cloud gate, resolves the
// API-format graph to submit (converting a UI-format workflow via the local ComfyUI),
// and parses the edited URN textarea. It writes the appropriate fragment and returns
// ok=false when the request cannot proceed (feature off, conversion warnings, or an
// unreachable-ComfyUI note). The returned apiGraph is what the caller must submit —
// the CONVERTED graph for a UI-format workflow, not the raw UI graph.
func (s *Server) cloudPrepare(w http.ResponseWriter, r *http.Request) (*store.Workflow, json.RawMessage, []string, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad workflow id", http.StatusBadRequest)
		return nil, nil, nil, false
	}
	wf, err := s.store.GetWorkflow(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return nil, nil, nil, false
	}
	if err != nil {
		s.renderError(w, "load workflow", err)
		return nil, nil, nil, false
	}
	if !s.cloudEnabled() {
		s.render(w, http.StatusOK, errorNote("CivitAI cloud run is disabled. Turn it on above, or set comfy_cloud in your config."))
		return nil, nil, nil, false
	}
	// rawInfo is discarded here on purpose: cloudPrepare serves the whatif/run
	// POSTs, which submit the graph and never resolve resource origins.
	apiGraph, _, warnings, note, ok := s.cloudAPIGraph(r.Context(), wf)
	if !ok {
		if len(warnings) > 0 {
			s.render(w, http.StatusOK, cloudConversionWarnings(warnings))
		} else {
			s.render(w, http.StatusOK, errorNote(note))
		}
		return nil, nil, nil, false
	}
	return wf, apiGraph, parseURNs(r.FormValue("resources")), true
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
//
// A 401/403 gets a SERVER-AUTHORED prefix naming the real cause. It is the one
// remote failure whose fix is entirely local and entirely unguessable from the
// remote text: CivitAI answers a bad, expired or revoked token with bodies like
// "Unauthorized" or a bare string, which tells a user nothing about the fact that
// this app resolves a token from four different layers and one of them is stale.
// The remote detail is still shown after it, never replaced — the API is the
// authority on what went wrong, we only add what it cannot know.
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
		if p.StatusCode == http.StatusUnauthorized || p.StatusCode == http.StatusForbidden {
			return "CivitAI rejected your API token (HTTP " + strconv.Itoa(p.StatusCode) + "). " +
				"It is missing, expired, revoked, or lacks access — check the token shown above, " +
				"replace it (CIVITAI_TOKEN, --token, or `token:` in your config file) and restart. " +
				"CivitAI said: " + detail
		}
		return detail
	}
	return err.Error()
}
