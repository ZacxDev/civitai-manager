package web

import (
	"errors"
	"fmt"
	"net/http"
	"path"
	"strconv"
	"strings"

	g "maragu.dev/gomponents"
	h "maragu.dev/gomponents/html"

	"github.com/ZacxDev/civitai-manager/internal/comfy"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// installMissingUnavailable is the reason shown on the DISABLED primary action when
// this server cannot install model files at all. It mirrors resolveReasonNotEligible
// (the same precondition) in the shorter register a button hint needs.
const installMissingUnavailable = "Installing automatically needs comfy_model_path set and comfy_url pointing at a ComfyUI on this machine."

// installAllMissingAction is THE primary recovery action for a run blocked by
// missing model files: one control that installs every missing file and then runs
// the workflow, instead of leaving the user to press N identically-labelled per-file
// buttons followed by a separate "Run again".
//
// It is a real <form> (not an hx-vals button) because the missing set is a LIST: the
// filenames + their inferred CivitAI types ride as parallel missing_filename /
// missing_type fields, exactly like the incompatible-options section's
// opt_input/opt_old arrays. runModesInclude comes along so a multi-mode template
// still runs the pipeline the user picked.
//
// When installing is not available it renders a DISABLED control plus the reason
// (never a hidden control — the same rule installAndRunButton follows), so the
// action's absence is explained rather than mysterious.
func installAllMissingAction(models []comfy.MissingModel, wfID int64, csrf string, dlEligible bool) g.Node {
	n := len(models)
	if n == 0 {
		return nil
	}
	label := fmt.Sprintf("Install %d missing model file%s and run", n, plural(n))
	if !dlEligible {
		return h.Div(h.Class("mt-3 space-y-1"),
			civButton("filled", "md", []g.Node{
				h.Type("button"), h.Disabled(),
				g.Attr("title", installMissingUnavailable),
			}, g.Text(label)),
			h.P(h.Class("text-xs text-slate-500"), g.Text(installMissingUnavailable)),
		)
	}
	fields := make([]g.Node, 0, 2*n)
	for _, mm := range models {
		fields = append(fields,
			h.Input(h.Type("hidden"), h.Name("missing_filename"), h.Value(mm.Filename)),
			h.Input(h.Type("hidden"), h.Name("missing_type"), h.Value(mm.CivitaiType)),
		)
	}
	return h.Form(
		hx("post", "/workflows/"+strconv.FormatInt(wfID, 10)+"/install-missing-and-run"),
		hx("target", "#"+runStatusContainerID),
		hx("swap", "innerHTML"),
		hx("disabled-elt", "find button[type='submit']"),
		hx("include", runModesInclude),
		h.Class("mt-3 space-y-1"),
		csrfInput(csrf),
		g.Group(fields),
		civButton("filled", "md", []g.Node{h.Type("submit")}, g.Text(label)),
		h.P(h.Class("text-xs text-slate-400"),
			g.Text("Finds each file on CivitAI, downloads it into your ComfyUI models folder, "+
				"then starts the run. Nothing is downloaded if any of them cannot be matched.")),
	)
}

// handleWorkflowInstallMissingAndRun is the primary recovery action's endpoint: it
// resolves EVERY missing model file the panel listed, installs them all, then runs
// the ORIGINAL workflow (the files now exist on disk, so the stored graph resolves
// unchanged — no substitution is persisted or applied). CSRF-protected +
// loopback-gated, same prologue order as handleWorkflowDownloadAndRun.
//
// It is ALL-OR-NOTHING on resolution: every file is resolved BEFORE anything is
// written, and if even one cannot be matched to an exact CivitAI/HuggingFace file
// then NOTHING is downloaded and the response says which ones failed. A half-install
// would leave the user with a still-failing run, some new gigabytes on disk, and no
// idea which of the two happened — and silently installing a differently-named file
// is a hazard the single-file flow already refuses (see resolveInstallPlan's
// resolveInstallSubstitute note); this endpoint has no card to confirm against, so it
// declines the same way.
func (s *Server) handleWorkflowInstallMissingAndRun(w http.ResponseWriter, r *http.Request) {
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
	names := r.Form["missing_filename"]
	types := r.Form["missing_type"]
	if len(names) == 0 {
		http.Error(w, "missing missing_filename", http.StatusBadRequest)
		return
	}

	// Load + bind to THIS workflow BEFORE any eligibility check, so an unreferenced
	// target is refused identically whether or not this server can install anything
	// (the contract handleWorkflowDownloadAndRun documents).
	wf, err := s.store.GetWorkflow(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.renderError(w, "load workflow", err)
		return
	}
	for _, name := range names {
		if !workflowReferencesFile(wf, strings.TrimSpace(name)) {
			http.Error(w, "missing_filename is not referenced by this workflow", http.StatusBadRequest)
			return
		}
	}

	opts := runOptions{ModeSelection: parseModeChoices(r.Form, wf)}

	// Not eligible → write nothing, and say so where the user is looking.
	if !s.comfyDownloadEligible() {
		s.renderInstallMissingDeclined(w, id, installMissingUnavailable, nil)
		return
	}

	var plans []pendingDownload
	var unresolved []string
	present := 0
	for i, raw := range names {
		name := strings.TrimSpace(raw)
		typ := civitaiTypeParam(formIndex(types, i))
		// Fast path: the destination already holds this file → nothing to fetch.
		if subdir, ok := comfy.TypeSubdir(typ); ok {
			if dest, derr := comfy.SafeModelDest(s.cfg.ComfyModelPath, subdir, name); derr == nil && fileExists(dest) {
				present++
				continue
			}
		}
		plan, outcome := s.resolveInstallPlan(r.Context(), name, typ, 0)
		if outcome != resolveInstallOK {
			unresolved = append(unresolved, path.Base(strings.ReplaceAll(name, "\\", "/")))
			continue
		}
		if fileExists(plan.DestPath) {
			present++
			continue
		}
		plans = append(plans, planToPending(plan, s.cfg.MaxFileSizeBytes))
	}

	if len(unresolved) > 0 {
		s.renderInstallMissingDeclined(w, id, installMissingUnresolvedReason(len(names), unresolved), unresolved)
		return
	}
	if len(plans) == 0 {
		// Every file was already on disk: run, and SAY that nothing was downloaded (the
		// same honesty rule as alreadyInstalledNote — this branch must not look like a
		// fresh install).
		s.startRunWithMessage(wf, opts, fmt.Sprintf(
			"All %d model file%s %s already installed — nothing was downloaded. Starting run…",
			present, plural(present), isAre(present)))
		s.render(w, http.StatusOK, runStatusFragment(s.runJobState(), id, s.csrf, s.comfyDownloadEligible(), s.nsfwMode()))
		return
	}
	s.startDownloadsAndRun(wf, plans, opts)
	s.render(w, http.StatusOK, runStatusFragment(s.runJobState(), id, s.csrf, true, s.nsfwMode()))
}

// installMissingUnresolvedReason is the honest report for a declined batch: how many
// of the requested files could not be matched, and their names. The names are
// graph-derived (untrusted) and are escaped by the renderer.
func installMissingUnresolvedReason(total int, unresolved []string) string {
	return fmt.Sprintf("Nothing was downloaded: %d of %d file%s could not be matched to a single file on CivitAI (%s). "+
		"Installing the rest would leave the run failing anyway, so nothing was written. "+
		"Pick each one yourself below.",
		len(unresolved), total, plural(total), strings.Join(unresolved, ", "))
}

// renderInstallMissingDeclined answers a batch install that wrote NOTHING: the
// reason, followed by the run panel the user was already looking at.
//
// Re-rendering the panel underneath matters — this response replaces the whole
// #run-status container, so a bare alert would delete the very per-file controls the
// reason tells the user to use. The snapshot is the SAME settled run (this path never
// starts one), so its data-run-seq is preserved too.
func (s *Server) renderInstallMissingDeclined(w http.ResponseWriter, wfID int64, reason string, _ []string) {
	s.render(w, http.StatusOK, g.Group([]g.Node{
		alertIcon("warning", "⚠", "Nothing was downloaded", h.P(h.Class("text-sm"), g.Text(reason))),
		runStatusFragment(s.runJobState(), wfID, s.csrf, s.comfyDownloadEligible(), s.nsfwMode()),
	}))
}

// formIndex reads the i-th value of a parallel form array, "" when short. The
// missing_filename / missing_type arrays are browser-supplied and must not be
// assumed equal-length.
func formIndex(vals []string, i int) string {
	if i < 0 || i >= len(vals) {
		return ""
	}
	return vals[i]
}
