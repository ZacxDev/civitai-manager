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

// maxBatchInstallFiles caps ONE batch install. It bounds three things at once: the
// sequential CivitAI round-trips a single click can issue, the number of files a
// single click can write, and — together with MaxFileSizeBytes — the aggregate bytes,
// since the batch can write at most maxBatchInstallFiles × MaxFileSizeBytes. No
// separate aggregate byte cap is imposed because any tighter number would be
// arbitrary; instead the resolved advertised total is REPORTED before the first byte
// is fetched (see downloadBatchOpeningMessage) so the user can Stop.
//
// 12 is comfortably above any real workflow's missing-model count (a big SDXL
// pipeline references a checkpoint, a VAE, an upscaler and a handful of LoRAs) and
// far below "someone posted a graph referencing 400 files".
const maxBatchInstallFiles = 12

// installMissingUnavailable is the reason shown when this server cannot install
// model files at all. It is the SAME precondition as resolveReasonNotEligible
// (run_download.go), deliberately kept as its own string because the registers
// differ: that one is a sentence rendered above resolve cards, this one is a button
// tooltip/hint. Keep the two in sync if the precondition itself changes.
const installMissingUnavailable = "Installing automatically needs comfy_model_path set and comfy_url pointing at a ComfyUI on this machine."

// installMissingNoRoute is why a particular missing file can never be part of a batch
// install: nothing in the graph said which KIND of model it is, so there is no
// ComfyUI subfolder to write it into.
const installMissingNoRoute = "civitai-manager cannot tell which ComfyUI folder they belong in, so it cannot install them automatically."

// installMissingBusyReason explains a click the one-run-at-a-time guard dropped.
const installMissingBusyReason = "Nothing was downloaded: another run or download is already in progress. Wait for it to finish (or Stop it), then try again."

// batchInstallPlan is the RENDER-TIME triage of a run's missing model files: which
// ones a one-click batch install could actually deliver, and which ones it provably
// could not.
//
// This exists because the CTA must not be offered ENABLED in a state where it can
// only fail. A missing reference whose CivitAI type could not be inferred (an
// unrecognised loader input → comfy.InferCivitaiType returns "") has no destination
// subfolder, so resolveInstallPlan skips the CivitAI branch entirely and the batch is
// doomed before the first request. Offering "Install 3 missing model files and run"
// and then declining after three round-trips is exactly the promise-more-than-you-
// deliver failure this panel was rewritten to remove.
type batchInstallPlan struct {
	// Installable are the files whose type routes to a known ComfyUI subfolder, so a
	// resolve+install attempt is meaningful. ONLY these ride in the form.
	Installable []comfy.MissingModel
	// Unroutable are the files a batch install can never place. They keep their
	// per-file "Choose a model…" path, which can still reach a HuggingFace match or a
	// library substitute — neither of those needs an inferred CivitAI type.
	Unroutable []comfy.MissingModel
	// Overflow is how many installable files maxBatchInstallFiles left for a second
	// click.
	Overflow int
	// Available reports whether the batch CTA can be offered ENABLED at all.
	Available bool
}

// planBatchInstall triages the missing set for the render layer. dlEligible is the
// server-level precondition (comfy_model_path + a local ComfyUI); routability is the
// per-file one. Duplicate references (the same file used by two loaders) collapse to
// one entry — the batch would otherwise resolve, count and report it twice.
func planBatchInstall(models []comfy.MissingModel, dlEligible bool) batchInstallPlan {
	var p batchInstallPlan
	seen := map[string]bool{}
	for _, mm := range models {
		key := strings.ToLower(path.Base(strings.ReplaceAll(mm.Filename, "\\", "/")))
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		if comfyTypeRoutable(mm.CivitaiType) {
			p.Installable = append(p.Installable, mm)
			continue
		}
		p.Unroutable = append(p.Unroutable, mm)
	}
	if n := len(p.Installable); n > maxBatchInstallFiles {
		p.Overflow = n - maxBatchInstallFiles
		p.Installable = p.Installable[:maxBatchInstallFiles]
	}
	p.Available = dlEligible && len(p.Installable) > 0
	return p
}

// installAllMissingAction is THE primary recovery action for a run blocked by
// missing model files: one control that installs every installable missing file and
// then runs the workflow, instead of leaving the user to press N identically-
// labelled per-file buttons followed by a separate "Run again".
//
// It is a real <form> (not an hx-vals button) because the missing set is a LIST: the
// filenames + their inferred CivitAI types ride as parallel missing_filename /
// missing_type fields, exactly like the incompatible-options section's
// opt_input/opt_old arrays. runModesInclude comes along so a multi-mode template
// still runs the pipeline the user picked.
//
// Every state that CANNOT deliver renders a DISABLED control plus the reason (never a
// hidden control — the rule installAndRunButton follows), and a batch that covers only
// SOME of the missing files says so in both the label and a note.
//
// total is the number of DISTINCT missing model files, so a partial batch can say
// "Install 2 of 3".
func installAllMissingAction(p batchInstallPlan, total int, wfID int64, csrf string) g.Node {
	if total == 0 {
		return nil
	}
	n := len(p.Installable)
	if !p.Available {
		reason := installMissingUnavailable
		if n == 0 && len(p.Unroutable) > 0 {
			reason = installMissingNoRoute
		}
		return h.Div(h.Class("mt-3 space-y-1"),
			civButton("filled", "md", []g.Node{
				h.Type("button"), h.Disabled(),
				g.Attr("title", reason),
			}, g.Text(fmt.Sprintf("Install %d missing model file%s and run", total, plural(total)))),
			h.P(h.Class("text-xs text-slate-500"), g.Text(reason)),
		)
	}

	label := fmt.Sprintf("Install %d missing model file%s and run", n, plural(n))
	if n < total {
		label = fmt.Sprintf("Install %d of %d missing model files and run", n, total)
	}
	fields := make([]g.Node, 0, 2*n)
	for _, mm := range p.Installable {
		fields = append(fields,
			h.Input(h.Type("hidden"), h.Name("missing_filename"), h.Value(mm.Filename)),
			h.Input(h.Type("hidden"), h.Name("missing_type"), h.Value(mm.CivitaiType)),
		)
	}
	var notes []g.Node
	if len(p.Unroutable) > 0 {
		notes = append(notes, h.P(h.Class("text-xs text-slate-400"),
			g.Text(fmt.Sprintf("%d of them cannot be installed in one click — %s Use the per-file option for %s.",
				len(p.Unroutable), installMissingNoRoute, unroutableNames(p.Unroutable)))))
	}
	if p.Overflow > 0 {
		notes = append(notes, h.P(h.Class("text-xs text-slate-400"),
			g.Text(fmt.Sprintf("At most %d files are installed per click, so %d are left for a second click.",
				maxBatchInstallFiles, p.Overflow))))
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
		h.P(h.Class("text-xs text-slate-400"), g.Text(batchInstallHint)),
		g.Group(notes),
	)
}

// batchInstallHint states what the click does, with the guarantee SCOPED to the stage
// that actually provides it.
//
// Matching is genuinely all-or-nothing: every file is resolved before anything is
// written. Downloading is not — a 404, a timeout, or a gated file part-way through
// leaves the already-finished files on disk (which is why downloadBatchError reports
// the count). An unqualified "nothing is downloaded if any of them cannot be
// matched" reads as a promise about the whole operation, and this panel exists
// because copy that promises more than the code delivers is the defect.
const batchInstallHint = "Finds each file on CivitAI and downloads it into your ComfyUI models folder, then starts the run. " +
	"If any of them cannot be matched, nothing is downloaded at all. If a download fails part-way, the files " +
	"that already finished stay on disk, the run does not start, and you are told how many landed."

// unroutableNames lists the un-installable files for the note above. They are
// graph-derived (untrusted) and escaped by the renderer.
func unroutableNames(models []comfy.MissingModel) string {
	names := make([]string, 0, len(models))
	for _, mm := range models {
		names = append(names, path.Base(strings.ReplaceAll(mm.Filename, "\\", "/")))
	}
	return strings.Join(names, ", ")
}

// handleWorkflowInstallMissingAndRun is the primary recovery action's endpoint: it
// resolves EVERY missing model file the form listed, installs them all, then runs the
// ORIGINAL workflow (the files now exist on disk, so the stored graph resolves
// unchanged — no substitution is persisted or applied). CSRF-protected +
// loopback-gated, same prologue order as handleWorkflowDownloadAndRun.
//
// RESOLUTION is all-or-nothing: every file is resolved BEFORE anything is written, and
// if even one cannot be matched to an exact CivitAI/HuggingFace file then NOTHING is
// downloaded and the response says which ones failed. A half-install would leave the
// user with a still-failing run, some new gigabytes on disk, and no idea which of the
// two happened — and silently installing a differently-named file is a hazard the
// single-file flow already refuses (resolveInstallPlan's resolveInstallSubstitute
// note); this endpoint has no card to confirm against, so it declines the same way.
//
// DOWNLOADING is NOT all-or-nothing, and is not claimed to be: a failure part-way
// through keeps the completed files (they are the right bytes at the right
// destinations, so a retry only fetches what is left) and reports the count — see
// downloadBatchError.
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
	// The two arrays are POSITIONAL. A short or offset missing_type array would
	// silently pair a file with ANOTHER file's type and route real bytes into the
	// wrong folder, so a mismatched length is REFUSED rather than tolerated.
	if len(types) != len(names) {
		http.Error(w, "missing_type must be parallel to missing_filename", http.StatusBadRequest)
		return
	}
	// Bound the work one click can request (see maxBatchInstallFiles). The form never
	// sends more; a hand-rolled POST is refused rather than served.
	if len(names) > maxBatchInstallFiles {
		http.Error(w, "too many files in one batch install", http.StatusBadRequest)
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
		s.renderInstallMissingDeclined(w, id, installMissingUnavailable)
		return
	}

	var plans []pendingDownload
	var unresolved []string
	present := 0
	seen := map[string]bool{}
	for i, raw := range names {
		name := strings.TrimSpace(raw)
		// De-duplicate: the same file referenced by two loaders must be fetched once.
		key := strings.ToLower(path.Base(strings.ReplaceAll(name, "\\", "/")))
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		typ := civitaiTypeParam(types[i])
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
		s.renderInstallMissingDeclined(w, id, installMissingUnresolvedReason(len(seen), unresolved))
		return
	}
	if len(plans) == 0 {
		// Every file was already on disk: run, and SAY that nothing was downloaded (the
		// same honesty rule as alreadyInstalledNote — this branch must not look like a
		// fresh install).
		if !s.startRunWithMessage(wf, opts, fmt.Sprintf(
			"All %d model file%s %s already installed — nothing was downloaded. Starting run…",
			present, plural(present), isAre(present))) {
			s.renderInstallMissingDeclined(w, id, installMissingBusyReason)
			return
		}
		s.render(w, http.StatusOK, runStatusFragment(s.runJobState(), id, s.csrf, s.comfyDownloadEligible(), s.nsfwMode()))
		return
	}
	// A click that lands while another run/download is in flight is DISCARDED by the
	// one-run-at-a-time guard. Say so — otherwise this response renders the OTHER
	// job's panel and the click looks either dead or, worse, like it started this
	// install.
	if !s.startDownloadsAndRun(wf, plans, opts, present) {
		s.renderInstallMissingDeclined(w, id, installMissingBusyReason)
		return
	}
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
func (s *Server) renderInstallMissingDeclined(w http.ResponseWriter, wfID int64, reason string) {
	s.render(w, http.StatusOK, g.Group([]g.Node{
		alertIcon("warning", "⚠", "Nothing was downloaded", h.P(h.Class("text-sm"), g.Text(reason))),
		runStatusFragment(s.runJobState(), wfID, s.csrf, s.comfyDownloadEligible(), s.nsfwMode()),
	}))
}
