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
	"github.com/ZacxDev/civitai-manager/internal/hf"
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

// installMissingUnavailable is the reason returned when a batch-install POST reaches
// a server that cannot install model files at all. It is the SAME precondition as
// resolveReasonNotEligible (run_download.go), deliberately kept as its own string
// because the registers differ. Keep the two in sync if the precondition changes.
//
// ⚠ It is a HANDLER refusal, not render copy: the render layer no longer offers a
// control in this state at all (see blockedInstallAction), so reaching this string
// means a hand-rolled POST or a page that went stale under a config change. Naming
// the config keys is right for that audience; do not copy this register back into
// the panel, which is the dead end this rework removed.
const installMissingUnavailable = "Installing automatically needs comfy_model_path set and comfy_url pointing at a ComfyUI on this machine."

// installMissingUncertain is the ADVISORY for files whose model type could not be
// inferred from the graph AND whose filename does not match a curated HuggingFace
// family. Those two together are the only render-time signals available, and neither
// is decisive: the HuggingFace search fallback can still auto-install a
// recognized-org match, so such a file is UNCERTAIN, never "impossible".
//
// An earlier revision of this file wrongly treated an uninferrable CivitAI type as
// proof the batch was doomed and EXCLUDED those files. That was false — the
// resolveInstallPlan path the batch handler calls falls back to HuggingFace whenever
// no model was explicitly chosen, and that fallback needs no CivitAI type at all
// (a `bbox/face_yolov9c.pt` detector installs cleanly through the curated map). The
// exclusion silently removed real capability and told the user something untrue, so
// these files stay IN the batch and this string only warns.
const installMissingUncertain = "civitai-manager could not tell from the workflow which kind of model these are, so they may not be found automatically."

// installMissingBusyReason explains a click the one-run-at-a-time guard dropped.
const installMissingBusyReason = "Nothing was downloaded: another run or download is already in progress. Wait for it to finish (or Stop it), then try again."

// batchInstallPlan is the RENDER-TIME triage of a run's missing model files: the set a
// one-click batch install will attempt, plus the subset whose success is genuinely
// uncertain from what is knowable without a network call.
//
// The ONLY render-time facts available are (a) whether the file's inferred CivitAI type
// routes to a ComfyUI subfolder, and (b) whether its basename matches a curated
// HuggingFace family (a pure local table). NEITHER IS DECISIVE, so nothing is EXCLUDED
// on the strength of them: resolveInstallPlan falls back to HuggingFace whenever no
// model was explicitly chosen — the exact call the batch handler makes — and that path
// needs no CivitAI type at all, so the search fallback can still auto-install a
// recognized-org match. The handler's honest all-or-nothing decline, which names every
// file it could not match, is what keeps the CTA truthful.
type batchInstallPlan struct {
	// Installable is every DISTINCT missing file the batch will attempt (capped). All
	// of them ride in the form.
	Installable []comfy.MissingModel
	// Uncertain is the subset with neither a routable CivitAI type nor a curated
	// HuggingFace family match: worth attempting, but flagged so the CTA does not imply
	// confidence it does not have.
	Uncertain []comfy.MissingModel
	// Overflow is how many distinct files maxBatchInstallFiles left for a second click.
	Overflow int
	// Available reports whether the batch CTA can be offered ENABLED at all.
	Available bool
	// SetupCanHelp reports that the batch is blocked ONLY for a reason the user can
	// fix from this page: civitai-manager does not have a usable ComfyUI models
	// folder, and the ComfyUI it talks to is on this machine, so choosing that
	// folder makes the batch live.
	//
	// 🔴 It is what decides whether the blocked panel gets a PRIMARY ACTION at all.
	// With a remote comfy_url no folder choice can unblock anything, and a
	// prominent "Set up automatic install" leading to a panel that says "this
	// server is not pointed at a ComfyUI on this machine" is the same dead end,
	// one click further in.
	SetupCanHelp bool
}

// planBatchInstall triages the missing set for the render layer. dlEligible is the ONE
// decisive precondition (comfy_model_path + a local ComfyUI); everything else here is
// advisory.
//
// comfyRemote splits the NOT-eligible case in two, which is the only thing the render
// layer needs beyond dlEligible: a blocked install whose ComfyUI is local is one folder
// choice away from working, and a blocked install whose ComfyUI is somewhere else
// cannot be fixed from this page at all.
//
// Duplicate references collapse — but only when they are the SAME INSTALL. The key is
// (basename, destination subfolder), NOT the basename alone: `SDXL/model.safetensors`
// as a Checkpoint and `flux/model.safetensors` as a LORA share a basename, yet
// SafeModelDest routes them to checkpoints/ and loras/ respectively. They are two
// files, and collapsing them would under-count the batch ("Install 1 …" for two
// missing files) and quietly leave the run still broken.
func planBatchInstall(models []comfy.MissingModel, dlEligible, comfyRemote bool) batchInstallPlan {
	var p batchInstallPlan
	seen := map[string]bool{}
	for _, mm := range models {
		key, ok := installDedupeKey(mm.Filename, mm.CivitaiType)
		if !ok || seen[key] {
			continue
		}
		seen[key] = true
		p.Installable = append(p.Installable, mm)
		// Normalize the type exactly as the handler does (civitaiTypeParam), so the two
		// layers never disagree about routability — see installDedupeKey.
		if !comfyTypeRoutable(civitaiTypeParam(mm.CivitaiType)) && !hf.CuratedFamilyMatch(mm.Filename) {
			p.Uncertain = append(p.Uncertain, mm)
		}
	}
	if n := len(p.Installable); n > maxBatchInstallFiles {
		p.Overflow = n - maxBatchInstallFiles
		p.Installable = p.Installable[:maxBatchInstallFiles]
	}
	p.Available = dlEligible && len(p.Installable) > 0
	p.SetupCanHelp = !dlEligible && !comfyRemote && len(p.Installable) > 0
	return p
}

// installDedupeKey identifies ONE install: the reference's basename plus the ComfyUI
// subfolder it would be written to. Two references with the same basename but different
// destinations are different files (see planBatchInstall). An empty/dot basename is not
// installable at all (ok=false).
//
// It is deliberately built from the SAME two functions that decide the real destination —
// civitaiTypeParam (which the handler applies to every submitted type) then
// comfy.TypeSubdir — so the render and handler layers can never key differently. Reading
// mm.CivitaiType raw here would diverge: `LyCORIS` is in comfy.typeSubdirs (→ loras/) but
// NOT in resolveTypeWhitelist, so the handler normalizes it to "" while an un-normalized
// render key would say "loras". Unreachable today (InferCivitaiType never emits it), but
// a divergence between two copies of one rule is exactly what this key exists to avoid.
//
// CASE: the basename is compared CASE-SENSITIVELY, matching comfy.SafeModelDest, which
// preserves case. On a case-sensitive filesystem that is simply correct —
// `Model.safetensors` and `model.safetensors` are two files, and folding them would make
// the CTA offer "Install 1" for two missing models, install one, and leave the run
// failing with no explanation.
//
// On a case-INSENSITIVE filesystem (macOS/Windows) the two references name ONE file, so
// this key over-counts by one. That direction degrades safely and visibly: both are
// resolved, the first is written, and the second's write is refused by
// comfy.ErrDestExists BEFORE any body is streamed (the existence check precedes the temp
// file), which downloadModelFile treats as "already present, proceed". The cost is one
// redundant HTTP request, not a redundant download. The asymmetry is why folding is not
// the safer default: over-counting wastes a request, under-counting silently drops a file
// the run needs.
func installDedupeKey(filename, civitaiType string) (string, bool) {
	base := path.Base(strings.ReplaceAll(strings.TrimSpace(filename), "\\", "/"))
	if base == "" || base == "." || base == ".." {
		return "", false
	}
	// An un-routable type has no destination, so every same-basename reference with one
	// resolves through the SAME filename-keyed HuggingFace path — one install.
	dest, _ := comfy.TypeSubdir(civitaiTypeParam(civitaiType))
	return base + "\x1f" + dest, true
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
// A state that cannot deliver renders a DISABLED control plus the reason (never a
// hidden control — the rule installAndRunButton follows), and a batch capped short of
// the full set says so in both the label and a note.
//
// total is the number of DISTINCT missing model files, so a capped batch can say
// "Install 12 of 15".
func installAllMissingAction(p batchInstallPlan, total int, wfID int64, csrf string) g.Node {
	if total == 0 {
		return nil
	}
	n := len(p.Installable)
	if !p.Available {
		return blockedInstallAction(p, total, wfID)
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
	if len(p.Uncertain) > 0 {
		// ADVISORY, not exclusion: these files are still attempted (the HuggingFace
		// fallback may well place them). If they cannot be matched, the handler declines
		// the whole batch and names them, so this note prepares the user for that outcome
		// without claiming it.
		notes = append(notes, h.P(h.Class("text-xs text-slate-400"),
			g.Text(fmt.Sprintf("%s If %s cannot be found, nothing is downloaded and you are told which: %s.",
				installMissingUncertain, itThem(len(p.Uncertain)), missingBasenames(p.Uncertain)))))
	}
	if p.Overflow > 0 {
		notes = append(notes, h.P(h.Class("text-xs text-slate-400"),
			g.Text(fmt.Sprintf("At most %d files are installed per click, so %d are left for a second click.",
				maxBatchInstallFiles, p.Overflow))))
	}
	// 🔴 The disclosure renders in the WORKING state too, not only when the CTA is
	// blocked. comfy_model_path is the single value deciding where gigabytes land, it
	// is writable from exactly one control in the whole app, and a wrong-but-valid
	// choice (a path that exists and is writable, so it passes validation) is
	// otherwise uncorrectable in-app — there is no settings page. It sits AFTER the
	// action, collapsed, so it never competes with the primary CTA; it costs nothing
	// until opened, since the body is fetched on first toggle.
	return h.Div(
		h.Form(
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
		),
		comfySetupDisclosure(wfID),
	)
}

// blockedInstallAction is what the failure panel renders INSTEAD of the batch CTA when
// this server cannot install model files.
//
// 🔴 IT RENDERS NO DISABLED BUTTON. The previous version did — a `filled`/`md` control
// carrying the same `rgb(25,113,194)` fill as a working one, distinguished only by
// `opacity: 0.6` — and measured live on the operator's install it was the LARGEST,
// TOPMOST, primary-coloured control on the whole failure panel while doing nothing at
// all. The thing that would have made it work (the setup step) rendered underneath it
// as a 16px, 12px-font disclosure summary, and the panel's one genuinely working
// button (Install <nodepack>) was smaller and 233px further down. A dead control
// outranking both its own fix and every live action is the defect; restyling it
// quieter would not have fixed it, because a quieter dead button is still a dead
// button.
//
// The information the dead button carried — that N model files could be installed
// automatically once this is set up — is NOT lost: it moved into the setup CTA's own
// label, which is where a reader deciding whether to bother needs it.
//
// Two blocked states, and they get different treatment because only one of them has
// an answer on this page:
//
//   - SetupCanHelp: the ComfyUI is on this machine and only the models folder is
//     missing, so choosing that folder is the recovery action — rendered as a real
//     primary control, the ONE live primary action in this section.
//   - otherwise (a remote comfy_url): nothing here can fix it, so there is no action
//     to offer. Saying so plainly, once, and pointing at the per-file paths that DO
//     work beats a prominent button that opens a panel explaining it cannot help.
func blockedInstallAction(p batchInstallPlan, total int, wfID int64) g.Node {
	if !p.SetupCanHelp {
		return remoteComfyExplanation()
	}
	return h.Div(h.Class("mt-3"),
		comfySetupCTA(wfID, fmt.Sprintf("Set up automatic install for %d missing model file%s",
			total, plural(total))),
	)
}

// comfySetupCTA is THE setup control: the primary button that loads the
// models-folder form, plus the sentence explaining what supplying it buys.
//
// 🔴 IT RENDERS THE APP'S ONLY id="comfy-setup" CONTAINER, so at most ONE call site
// may run per rendered page. That is not a convention each caller has to remember —
// failureSetupOwner (run_pages.go) takes the decision once for the whole failure
// panel, and TestFailurePanelHasAtMostOneComfySetupContainer asserts the COUNT over
// every state combination rather than mere presence.
//
// It was extracted from blockedInstallAction so the incompatible-options section can
// reach the same single instance: a run can fail with BadOptions and ZERO
// MissingModels, and that surface used to render no setup affordance at all (measured
// on 52cb872: 0 containers, 0 links) while its per-option Install buttons sat
// disabled. Copying the markup instead would have put two elements of this id on any
// page where both sections render, making htmx's target ambiguous.
//
// The CTA lives INSIDE the container and targets it with innerHTML, so the first
// click REPLACES the button and its explanation with the setup form. That shape is
// deliberate: it leaves no stale control that has already been used, it needs no
// `once` trigger (which would render the button permanently inert-looking), and a
// rejected save — which HX-Retargets #comfy-setup — lands in the same container it
// came from. The body is still fetched lazily, so the ComfyUI probe stays off the
// panel's render path.
//
// label is the caller's, because the COUNT it carries differs by section (missing
// model files vs. blocked bad-option files) and a reader deciding whether to bother
// needs the number for the section they are looking at. The why-line does not vary:
// it states the same precondition either way.
func comfySetupCTA(wfID int64, label string) g.Node {
	return h.Div(h.ID(comfySetupContainerID), h.Class("space-y-1"),
		civButton("filled", "md", []g.Node{
			h.Type("button"),
			hx("get", "/workflows/"+strconv.FormatInt(wfID, 10)+"/comfy-setup"),
			hx("target", "#"+comfySetupContainerID),
			hx("swap", "innerHTML"),
			hx("disabled-elt", "this"),
			g.Attr("aria-describedby", comfySetupWhyID),
		}, g.Text(label)),
		h.P(h.ID(comfySetupWhyID), h.Class("text-xs text-slate-400"), g.Text(comfySetupWhyText)),
	)
}

// remoteComfyExplanation is the blocked state with NO answer on this page: comfy_url
// points at a ComfyUI somewhere else, so no folder choice can unblock anything.
//
// Extracted alongside comfySetupCTA and gated by the same owner decision. It is the
// OTHER thing a section can carry in the setup slot, and rendering it twice would
// state the same dead end twice on one panel — noise rather than a duplicate-id bug,
// but noise directly above the per-file paths that do work.
func remoteComfyExplanation() g.Node {
	return h.Div(h.Class("mt-3 space-y-1"),
		h.P(g.Attr("role", "status"), h.Class("text-xs text-amber-400"), g.Text(installRemoteComfyReason)),
		h.P(h.Class("text-xs text-slate-500"), g.Text(installRemoteComfyNextStep)),
	)
}

// comfySetupWhyID names the sentence explaining the setup CTA, so the button can point
// at it with aria-describedby — the count is in the label, the reason is here, and a
// screen reader gets both from the control itself.
const comfySetupWhyID = "comfy-setup-why"

// comfySetupWhyText is the one line under the setup CTA. It says what civitai-manager
// needs and what the reader gets for supplying it, and names no config key: the whole
// point of the control is that the value is suppliable from this page.
const comfySetupWhyText = "Choose where ComfyUI keeps its models and installing them becomes one click."

// installRemoteComfyReason / installRemoteComfyNextStep are the blocked state that has
// NO answer on this page: comfy_url points at a ComfyUI somewhere else, so
// civitai-manager cannot write files into it whatever folder is configured.
//
// 🔴 The first string is shared with comfySetupFragment's own comfy_url branch, which
// is the panel a user would have reached by clicking a setup CTA here. Keeping ONE
// copy is the point — the two are the same claim about the same precondition, and they
// used to be reachable only in that order.
const installRemoteComfyReason = "This server is not pointed at a ComfyUI on this machine, so it cannot install " +
	"model files for you. Set comfy_url to your local ComfyUI (for example " +
	"http://127.0.0.1:8188) and restart civitai-manager."

const installRemoteComfyNextStep = "Until then, use the per-file options below to download each model yourself."

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

// missingBasenames lists the files for the advisory note above. They are graph-derived
// (untrusted) and escaped by the renderer.
func missingBasenames(models []comfy.MissingModel) string {
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
		s.renderRunActionDeclined(w, id, installMissingUnavailable)
		return
	}

	var plans []pendingDownload
	var unresolved []string
	present := 0
	seen := map[string]bool{}
	for i, raw := range names {
		name := strings.TrimSpace(raw)
		typ := civitaiTypeParam(types[i])
		// De-duplicate the same file referenced by two loaders — keyed on (basename,
		// destination) so two DIFFERENT files that merely share a basename are not merged
		// into one install (see installDedupeKey / planBatchInstall).
		key, keyOK := installDedupeKey(name, typ)
		if !keyOK || seen[key] {
			continue
		}
		seen[key] = true
		// Fast path: the destination already holds this file → nothing to fetch.
		if subdir, ok := comfy.TypeSubdir(typ); ok {
			if dest, derr := comfy.SafeModelDest(s.comfyModelPath(), subdir, name); derr == nil && fileExists(dest) {
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
		s.renderRunActionDeclined(w, id, installMissingUnresolvedReason(len(seen), unresolved))
		return
	}
	if len(plans) == 0 {
		// Every file was already on disk: run, and SAY that nothing was downloaded (the
		// same honesty rule as alreadyInstalledNote — this branch must not look like a
		// fresh install).
		if !s.startRunWithMessage(wf, opts, fmt.Sprintf(
			"All %d model file%s %s already installed — nothing was downloaded. Starting run…",
			present, plural(present), isAre(present))) {
			s.renderRunActionDeclined(w, id, installMissingBusyReason)
			return
		}
		s.render(w, http.StatusOK, runStatusBody(s.runJobState(), id, s.csrf, s.comfyDownloadEligible(), s.maturity()))
		return
	}
	// A click that lands while another run/download is in flight is DISCARDED by the
	// one-run-at-a-time guard. Say so — otherwise this response renders the OTHER
	// job's panel and the click looks either dead or, worse, like it started this
	// install.
	if !s.startDownloadsAndRun(wf, plans, opts, present) {
		s.renderRunActionDeclined(w, id, installMissingBusyReason)
		return
	}
	s.render(w, http.StatusOK, runStatusBody(s.runJobState(), id, s.csrf, true, s.maturity()))
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

// renderRunActionDeclined answers a run-starting action that did NOTHING: the reason,
// followed by the run panel the user was already looking at. Shared by the batch
// install and by the single-file install buttons whose click the one-run-at-a-time
// guard discarded.
//
// Re-rendering the panel underneath matters — this response replaces the whole
// #run-status container, so a bare alert would delete the very per-file controls the
// reason tells the user to use. The snapshot is the SAME settled run (this path never
// starts one), so its data-run-seq is preserved too.
func (s *Server) renderRunActionDeclined(w http.ResponseWriter, wfID int64, reason string) {
	s.render(w, http.StatusOK, g.Group([]g.Node{
		alertIcon("warning", "⚠", "Nothing was downloaded", h.P(h.Class("text-sm"), g.Text(reason))),
		runStatusBody(s.runJobState(), wfID, s.csrf, s.comfyDownloadEligible(), s.maturity()),
	}))
}
