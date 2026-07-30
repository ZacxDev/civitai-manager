package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
	"github.com/ZacxDev/civitai-manager/internal/comfy"
	"github.com/ZacxDev/civitai-manager/internal/config"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// comfyURLIsLocal reports whether the configured ComfyUI base URL points at the
// loopback interface — the precondition for writing into its local models dir.
// A non-loopback ComfyUI is on another host, so we cannot install files for it.
func comfyURLIsLocal(comfyURL string) bool {
	u, err := url.Parse(strings.TrimSpace(comfyURL))
	if err != nil || u.Host == "" {
		return false
	}
	return config.IsLoopbackAddr(u.Host)
}

// comfyDownloadEligible reports whether the "Download & run" action is available:
// comfy_model_path is configured, the ComfyUI is local (loopback), and the model
// root is (still) an existing directory.
//
// This runs on EVERY run-status poll (~1-2s during a run), so it must stay cheap:
// it does an os.Stat (exists + is-dir) re-check only — NOT a CreateTemp
// write-probe, which would churn a temp file in the ComfyUI models dir on every
// poll. Writability was validated at config load; if the dir became read-only
// since, the actual download (comfy.WriteModelStream) fails cleanly and the user
// sees the error — button visibility does not need the stronger probe.
func (s *Server) comfyDownloadEligible() bool {
	root := strings.TrimSpace(s.cfg.ComfyModelPath)
	if root == "" {
		return false
	}
	if !comfyURLIsLocal(s.cfg.ComfyURL) {
		return false
	}
	fi, err := os.Stat(root)
	if err != nil || !fi.IsDir() {
		if err != nil {
			s.log.Warn("comfy_model_path no longer available", "path", root, "err", err)
		}
		return false
	}
	return true
}

// comfyTypeRoutable reports whether a CivitAI type maps to a known ComfyUI
// subfolder (so a download for it has a well-defined destination).
func comfyTypeRoutable(civitaiType string) bool {
	_, ok := comfy.TypeSubdir(civitaiType)
	return ok
}

// pendingDownload is the resolved plan for a Download & run: the file's civitai
// download URL, the destination path under comfy_model_path, a size cap, and the
// advertised size hint (for the free-disk pre-check and progress). It carries the
// bare basename for display.
type pendingDownload struct {
	FileName string
	// RemoteFileName is the name the file has ON CivitAI/HuggingFace. It differs from
	// FileName only for a user-CONFIRMED substitution, and exists so every progress
	// line names the bytes actually being fetched — never only the expected name.
	RemoteFileName    string
	URL               string
	DestPath          string
	MaxBytes          int64
	ContentLengthHint int64
	// ExpectedSHA256, when set, is verified against the streamed bytes before the
	// atomic rename (the HuggingFace path pins on the tree's LFS oid). Empty for the
	// civitai path (no hash to pin).
	ExpectedSHA256 string
	// SourceHF routes the fetch through the SSRF-hardened HuggingFace client instead
	// of the civitai downloader.
	SourceHF bool
	// HFRepo/HFPath/HFRevision are the PROVENANCE triple for an HF source: the repo
	// id, the path within it, and the concrete commit sha the URL is pinned to. They
	// are recorded (against ExpectedSHA256) only AFTER the verified atomic rename
	// succeeds — see recordHFProvenance. Empty for the civitai path.
	HFRepo     string
	HFPath     string
	HFRevision string
}

// progressName is the name shown in every progress/status line for this download.
// For a confirmed substitution it names BOTH files ("<remote> as <expected>") so the
// user is never told they are fetching bytes they are not: the destination keeps the
// workflow's reference name, but the bytes are a different file and the UI must say
// so while they stream.
func (pd pendingDownload) progressName() string {
	remote := strings.TrimSpace(pd.RemoteFileName)
	if remote == "" || strings.EqualFold(remote, pd.FileName) {
		return pd.FileName
	}
	return remote + " as " + pd.FileName
}

// resolvedDownload is one file candidate parsed from a model-search raw body: a
// model version's file, with the parent model id (for the deep link) and its
// download URL / size. Only files with a non-empty download URL are usable.
type resolvedDownload struct {
	ModelID     int
	FileName    string
	DownloadURL string
	SizeBytes   int64
}

// handleWorkflowDownloadAndRun resolves a missing model FILENAME to a CivitAI
// file, downloads it into the local ComfyUI models dir under the exact reference
// name, then auto-runs the ORIGINAL workflow (which now finds the file). It is
// CSRF-protected + loopback-gated (same prologue order as
// handleWorkflowRunSubstitute: ParseForm → verifyCSRF → gate) and reaches both
// civitai.com (egress) and the local filesystem.
//
// It degrades safely: when the flow is not eligible (comfy_model_path unset /
// non-writable, or a non-local ComfyUI), or the type is unroutable, or resolution
// is ambiguous/zero, it writes NOTHING and returns the CivitAI-link/resolve
// fragment so the user can pick a model manually.
func (s *Server) handleWorkflowDownloadAndRun(w http.ResponseWriter, r *http.Request) {
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
	filename := strings.TrimSpace(r.FormValue("filename"))
	if filename == "" {
		http.Error(w, "missing filename", http.StatusBadRequest)
		return
	}
	typ := civitaiTypeParam(r.FormValue("type"))
	// An optionally chosen model (from a card click) narrows resolution to exactly
	// that model. A malformed value is REFUSED rather than silently read as "absent" —
	// those two mean very different things now that a card always supplies one.
	chosenModel, ok := parseModelIDParam(r.FormValue("model_id"))
	if !ok {
		http.Error(w, "bad model_id", http.StatusBadRequest)
		return
	}
	// A user-confirmed substitution: install a DIFFERENTLY-NAMED remote file under the
	// workflow's reference name. Only ever set by the explicit confirm button, which
	// also echoes WHICH remote file was approved.
	confirmed := r.FormValue("confirm_substitute") == "1"
	approvedFile := strings.TrimSpace(r.FormValue("confirm_file"))

	// Load + bind the request to THIS workflow FIRST: `filename` is free-form and now
	// steers a real download, so it must be a file this workflow actually references.
	// This precedes the eligibility check so the endpoint's contract is uniform — an
	// unreferenced target is refused whether or not this server can install anything.
	wf, err := s.store.GetWorkflow(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.renderError(w, "load workflow", err)
		return
	}
	if !workflowReferencesFile(wf, filename) {
		http.Error(w, "filename is not referenced by this workflow", http.StatusBadRequest)
		return
	}

	// Not eligible → link-only fallback (never attempt a write/download).
	if !s.comfyDownloadEligible() {
		s.renderResolveFallback(w, r, filename, typ, resolveReasonNotEligible)
		return
	}

	// Fast path: for a routable CivitAI type whose destination already exists, skip
	// the network entirely and just run the original workflow. Say so — this branch is
	// what makes any earlier install (right or wrong) permanent, so it must not look
	// like a fresh download.
	if subdir, ok := comfy.TypeSubdir(typ); ok {
		if dest, derr := comfy.SafeModelDest(s.cfg.ComfyModelPath, subdir, filename); derr == nil && fileExists(dest) {
			s.startRunWithMessage(wf, runOptions{}, alreadyInstalledNote(filename))
			s.render(w, http.StatusOK, runStatusFragment(s.runJobState(), id, s.csrf, s.comfyDownloadEligible(), s.nsfwMode()))
			return
		}
	}

	// Resolve the install source: CivitAI first, then the HuggingFace fallback (only
	// an auto-eligible HF match — curated/recognized-org + non-gated + exact + sha).
	// Nothing installable → the resolve cards WITH the reason (no download): a silent
	// re-render of the same panel is a dead button.
	plan, outcome := s.resolveInstallPlan(r.Context(), filename, typ, chosenModel)
	switch outcome {
	case resolveInstallOK:
		// exact filename match — one click, straight to the download below.
	case resolveInstallSubstitute:
		// The resolved file is NOT the one the workflow asked for. OFFER it; never
		// perform it on this click. A confirmation counts ONLY for the exact file it
		// approved: if re-resolution now yields something else (CivitAI promoted a new
		// primary version between the two clicks), re-offer instead of installing a
		// file the user never saw.
		if !confirmed || !strings.EqualFold(approvedFile, plan.RemoteFileName) {
			s.renderSubstituteOffer(w, r, id, typ, chosenModel, plan)
			return
		}
	default:
		s.renderResolveFallback(w, r, filename, typ, resolveFailureReason(outcome, chosenModel))
		return
	}

	// Already installed at the resolved destination → skip the download, run.
	if fileExists(plan.DestPath) {
		// This branch sits AFTER resolveInstallPlan, i.e. after a live CivitAI round-trip
		// this click already paid for. A drop by the one-run-at-a-time guard must therefore
		// be REPORTED, not answered with the other job's panel (finding 9). The pre-resolution
		// fast path above is left alone: it does no network work, so a dropped click there is
		// a plain no-op.
		if !s.startRunWithMessage(wf, runOptions{}, alreadyInstalledNote(filename)) {
			s.renderRunActionDeclined(w, id, installMissingBusyReason)
			return
		}
		s.render(w, http.StatusOK, runStatusFragment(s.runJobState(), id, s.csrf, s.comfyDownloadEligible(), s.nsfwMode()))
		return
	}

	// A click the one-run-at-a-time guard discards must SAY so: this handler already
	// paid a CivitAI round-trip, and answering with the other job's panel makes the
	// click look either dead or as if this install had started.
	if !s.startDownloadAndRun(wf, planToPending(plan, s.cfg.MaxFileSizeBytes), runOptions{}) {
		s.renderRunActionDeclined(w, id, installMissingBusyReason)
		return
	}
	s.render(w, http.StatusOK, runStatusFragment(s.runJobState(), id, s.csrf, true, s.nsfwMode()))
}

// handleWorkflowInstallOptionAndRun installs the model FILE behind ONE model-file
// incompatible-option (resolve → download into the local ComfyUI models dir) AND
// applies the user's picked fixes for the OTHER options in the section, then runs the
// ORIGINAL workflow ephemerally — the stored workflow is never mutated. It is
// CSRF-protected + loopback-gated (same prologue as handleWorkflowDownloadAndRun) and
// reaches civitai.com/HuggingFace (egress) + the local filesystem + ComfyUI.
//
// The section form hx-includes carry the whole group's opt_input/opt_old/opt_new
// arrays; parseOptionFixes turns the picked ones into fixes. The install target's own
// fix is DROPPED (we install the exact file, not substitute it). It degrades safely
// exactly like download-and-run: not eligible / unroutable / ambiguous → writes
// NOTHING and returns the resolve/link fragment.
func (s *Server) handleWorkflowInstallOptionAndRun(w http.ResponseWriter, r *http.Request) {
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
	filename := strings.TrimSpace(r.FormValue("install_filename"))
	if filename == "" {
		http.Error(w, "missing install_filename", http.StatusBadRequest)
		return
	}
	typ := civitaiTypeParam(r.FormValue("install_type"))

	// Fixes for the OTHER groups (the picks). Drop any fix keyed on the install
	// target's own value — installing the exact file makes the original value valid,
	// so it must NOT also be rewritten to a substitute.
	fixes := parseOptionFixes(r.Form)
	for k := range fixes {
		if k.OldValue == filename {
			delete(fixes, k)
		}
	}
	opts := runOptions{OptionFixes: fixes}

	// Load + bind BEFORE the eligibility check, so an unreferenced target is refused
	// identically whether or not this server can install (see
	// handleWorkflowDownloadAndRun).
	wf, err := s.store.GetWorkflow(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.renderError(w, "load workflow", err)
		return
	}
	if !workflowReferencesFile(wf, filename) {
		http.Error(w, "install_filename is not referenced by this workflow", http.StatusBadRequest)
		return
	}

	// Not eligible → link-only fallback (never attempt a write/download).
	if !s.comfyDownloadEligible() {
		s.renderResolveFallback(w, r, filename, typ, resolveReasonNotEligible)
		return
	}

	// Fast path: a routable CivitAI type whose destination already exists → skip the
	// network and run with the picked option-fixes (the original value is valid).
	if subdir, ok := comfy.TypeSubdir(typ); ok {
		if dest, derr := comfy.SafeModelDest(s.cfg.ComfyModelPath, subdir, filename); derr == nil && fileExists(dest) {
			s.startRunWithMessage(wf, opts, alreadyInstalledNote(filename))
			s.render(w, http.StatusOK, runStatusFragment(s.runJobState(), id, s.csrf, s.comfyDownloadEligible(), s.nsfwMode()))
			return
		}
	}

	// Resolve the install source (CivitAI first, then the auto-eligible HF fallback).
	// A bad-option install has NO model card behind it — the target comes from the
	// graph's own (invalid) value — so chosenModel = 0 is correct here and the
	// detector's HF curated path stays reachable. Nothing installable → resolve cards
	// WITH the reason, so this button can never look dead either.
	plan, outcome := s.resolveInstallPlan(r.Context(), filename, typ, 0)
	if outcome != resolveInstallOK {
		// resolveInstallSubstitute is unreachable at chosenModel=0 (both the search and
		// HF paths are exact-basename only); if that ever changes, DECLINE rather than
		// install a differently-named file — there is no card here to confirm against.
		s.renderResolveFallback(w, r, filename, typ, resolveFailureReason(outcome, 0))
		return
	}

	// Already installed at the resolved destination → skip the download, run with fixes.
	if fileExists(plan.DestPath) {
		// This branch sits AFTER resolveInstallPlan, i.e. after a live CivitAI round-trip
		// this click already paid for. A drop by the one-run-at-a-time guard must therefore
		// be REPORTED, not answered with the other job's panel (finding 9). The pre-resolution
		// fast path above is left alone: it does no network work, so a dropped click there is
		// a plain no-op.
		if !s.startRunWithMessage(wf, opts, alreadyInstalledNote(filename)) {
			s.renderRunActionDeclined(w, id, installMissingBusyReason)
			return
		}
		s.render(w, http.StatusOK, runStatusFragment(s.runJobState(), id, s.csrf, s.comfyDownloadEligible(), s.nsfwMode()))
		return
	}

	// Same dropped-click honesty as handleWorkflowDownloadAndRun above.
	if !s.startDownloadAndRun(wf, planToPending(plan, s.cfg.MaxFileSizeBytes), opts) {
		s.renderRunActionDeclined(w, id, installMissingBusyReason)
		return
	}
	s.render(w, http.StatusOK, runStatusFragment(s.runJobState(), id, s.csrf, true, s.nsfwMode()))
}

// installPlan is a resolved, ready-to-execute install: the source download URL, the
// containment-checked destination under comfy_model_path, and — for a HuggingFace
// source — the expected sha256 the bytes are verified against before the rename.
type installPlan struct {
	// FileName is the DESTINATION basename — the name the workflow references, which
	// is what must land on disk for the graph to resolve.
	FileName string
	// RemoteFileName is what the file is actually called on CivitAI/HuggingFace. When
	// it differs from FileName the install is a SUBSTITUTION and must be confirmed.
	RemoteFileName    string
	URL               string
	DestPath          string
	ContentLengthHint int64
	// ExpectedSHA256 is the HF file's LFS oid; the download is refused unless the
	// streamed bytes hash to it. Empty for the civitai source (no hash to pin).
	ExpectedSHA256 string
	// SourceHF routes the download through the hardened HuggingFace client.
	SourceHF bool
	// HFRepo/HFPath/HFRevision carry the HuggingFace provenance triple straight
	// off the *hf.Match, so the download can record where the bytes came from
	// without re-resolving (and therefore without any extra egress).
	HFRepo     string
	HFPath     string
	HFRevision string
}

// substituted reports whether executing this plan would write bytes that are NOT the
// file the workflow asked for.
func (p installPlan) substituted() bool {
	return strings.TrimSpace(p.RemoteFileName) != "" && !strings.EqualFold(p.RemoteFileName, p.FileName)
}

// installResolveOutcome is why resolution did (or did not) yield a one-click install.
// It exists so the handler can answer with an ACCURATE reason instead of a single
// undifferentiated "couldn't resolve", and so a substitution can be OFFERED rather
// than performed.
type installResolveOutcome int

const (
	// resolveInstallOK — an exact-filename match; download immediately.
	resolveInstallOK installResolveOutcome = iota
	// resolveInstallNone — nothing installable resolved at all.
	resolveInstallNone
	// resolveInstallNoFile — the CHOSEN model exists but has no downloadable file.
	resolveInstallNoFile
	// resolveInstallWrongType — the CHOSEN model is not the type this destination
	// subfolder is for (e.g. a LORA id posted with type=Checkpoint).
	resolveInstallWrongType
	// resolveInstallSubstitute — resolved, but to a DIFFERENT file than the workflow
	// references. NEVER auto-downloaded: the user must confirm.
	resolveInstallSubstitute
)

// resolveInstallPlan resolves a missing FILENAME to an install plan. It tries CivitAI
// first (only for a routable type, whose subdir gives the destination) and, on a
// CivitAI miss, the HuggingFace fallback — but ONLY an auto-download-eligible HF match
// (curated-map/recognized-org + non-gated + exact-basename + a captured sha256 + a
// determinable ComfyUI subdir). An explicitly chosen CivitAI model (model_id) is
// CivitAI-only and never falls back to HF.
//
// The returned outcome distinguishes the failure modes so the caller can explain
// itself, and — critically — flags resolveInstallSubstitute when the resolved remote
// file is NOT the file the workflow references. That case must never download on the
// first click: a model's primary version routinely carries a differently-named file
// (Juggernaut XL has no juggernautXL_v9Rundiffusion.safetensors; its primary version
// is Ragnarok), so auto-installing it would stream 6.6 GB of a DIFFERENT checkpoint to
// disk under the expected name, invisibly and permanently (the fileExists fast path
// then makes every later click a no-op).
func (s *Server) resolveInstallPlan(ctx context.Context, filename, typ string, chosenModel int) (installPlan, installResolveOutcome) {
	want := path.Base(strings.ReplaceAll(filename, "\\", "/"))

	// CivitAI branch — needs a routable type so the destination subdir is defined.
	if subdir, ok := comfy.TypeSubdir(typ); ok {
		if dest, err := comfy.SafeModelDest(s.cfg.ComfyModelPath, subdir, filename); err == nil {
			src, out := s.resolveDownloadSource(ctx, filename, typ, chosenModel)
			switch out {
			case resolveInstallOK:
				plan := installPlan{
					FileName:          want,
					RemoteFileName:    path.Base(strings.ReplaceAll(src.FileName, "\\", "/")),
					URL:               src.DownloadURL,
					DestPath:          dest,
					ContentLengthHint: src.SizeBytes,
				}
				if plan.substituted() {
					return plan, resolveInstallSubstitute
				}
				return plan, resolveInstallOK
			case resolveInstallNoFile, resolveInstallWrongType:
				// An EXPLICIT model choice is CivitAI-only — never silently reinterpreted
				// as a HuggingFace install.
				return installPlan{}, out
			}
		}
	}

	// HuggingFace fallback — never for an explicitly chosen CivitAI model. An eligible
	// HF match is exact-basename by construction (hfInstallEligible), so it can never
	// be a substitution.
	if chosenModel == 0 {
		if m := s.resolveHF(ctx, filename); m != nil && s.hfInstallEligible(m) {
			if dest, err := comfy.SafeModelDest(s.cfg.ComfyModelPath, m.Subdir, m.FileName); err == nil {
				return installPlan{
					FileName:       m.FileName,
					RemoteFileName: m.FileName,
					URL:            m.URL,
					DestPath:       dest,
					ExpectedSHA256: m.SHA256,
					SourceHF:       true,
					HFRepo:         m.Repo,
					HFPath:         m.Path,
					HFRevision:     m.Revision,
				}, resolveInstallOK
			}
		}
	}
	return installPlan{}, resolveInstallNone
}

// resolveReason* are the explanations rendered above the resolve cards when an
// install ACTION could not proceed. Every one of them opens with "Nothing was
// downloaded" because the whole point is to distinguish "your click did something
// and it declined" from "your click did nothing at all": this fragment replaces the
// panel the user was already looking at, so WITHOUT a reason the response is
// byte-identical to the pre-click panel and the button reads as dead. Never call
// renderResolveFallback from an action path with an empty reason.
const (
	// The flow itself is unavailable (comfy_model_path unset / non-writable, or a
	// non-loopback ComfyUI we cannot install files for). installMissingUnavailable
	// (run_install_all.go) is the button-hint register of this SAME precondition — keep
	// the two in sync if the precondition changes.
	resolveReasonNotEligible = "Nothing was downloaded: installing automatically is not available here. Set comfy_model_path and point comfy_url at a local ComfyUI, or install this file yourself."
	// Filename-only resolution found no single CivitAI file to install. Reached from
	// the CTAs that legitimately carry no model id (the HuggingFace-fallback install
	// and the bad-option install), never from a model card.
	resolveReasonNoMatch = "Nothing was downloaded: this filename did not identify a single CivitAI file. Pick the exact model below and use its Install and run button."
	// A specific model WAS chosen (model_id) but it yielded no downloadable file.
	resolveReasonChosenModel = "Nothing was downloaded: that CivitAI model has no downloadable file for this reference. Try one of the other matches below."
	// The chosen model is not the kind of model this destination holds.
	resolveReasonWrongType = "Nothing was downloaded: that CivitAI model is not the type this file slot expects, so installing it would put the wrong kind of model in the wrong folder."
	// Defensive: a substitution reached a path that cannot offer a confirmation.
	resolveReasonNeedsChoice = "Nothing was downloaded: the only file found has a different name than this reference, so it cannot be installed automatically. Pick the exact model you want."
)

// resolveFailureReason maps a non-installable outcome to its user-facing explanation.
func resolveFailureReason(outcome installResolveOutcome, chosenModel int) string {
	switch outcome {
	case resolveInstallWrongType:
		return resolveReasonWrongType
	case resolveInstallNoFile:
		return resolveReasonChosenModel
	case resolveInstallSubstitute:
		return resolveReasonNeedsChoice
	default:
		if chosenModel > 0 {
			return resolveReasonChosenModel
		}
		return resolveReasonNoMatch
	}
}

// substituteOfferText is the offer shown when the resolved file is NOT the file the
// workflow references. It names BOTH concretely, because the whole hazard is that the
// bytes and the on-disk name disagree.
func substituteOfferText(requested, remote string) string {
	return "Nothing was downloaded. This CivitAI model has no file named " + requested +
		". Its current primary version provides " + remote +
		" instead — a different file. Installing it would save " + remote +
		" to disk under the name " + requested + ", and your workflow would run with that model."
}

// planToPending turns a resolved plan into the download job's input, carrying the
// remote name through so progress lines can name the real bytes.
func planToPending(plan installPlan, maxBytes int64) pendingDownload {
	return pendingDownload{
		FileName:          plan.FileName,
		RemoteFileName:    plan.RemoteFileName,
		URL:               plan.URL,
		DestPath:          plan.DestPath,
		MaxBytes:          maxBytes,
		ContentLengthHint: plan.ContentLengthHint,
		ExpectedSHA256:    plan.ExpectedSHA256,
		SourceHF:          plan.SourceHF,
		HFRepo:            plan.HFRepo,
		HFPath:            plan.HFPath,
		HFRevision:        plan.HFRevision,
	}
}

// typeDestinationMismatch reports whether a CivitAI model's own type would route to a
// DIFFERENT ComfyUI folder than the requested type does — the only thing that actually
// matters, since the requested type is what picks the destination subdir.
//
// It compares mapped destinations rather than type strings so the several CivitAI
// types that share one folder (LORA / LoCon / LyCORIS → loras/) are treated as
// equivalent. It concedes rather than refuses whenever it cannot tell: an empty type
// on either side (the API omits it on some shapes) or a type comfy cannot map. Those
// concessions are safe — an unmappable REQUESTED type never reaches here anyway,
// because resolveInstallPlan only enters the CivitAI branch for a routable type.
func typeDestinationMismatch(modelType, requestedType string) (bool, string, string) {
	got, wantTyp := strings.TrimSpace(modelType), strings.TrimSpace(requestedType)
	if got == "" || wantTyp == "" {
		return false, got, wantTyp
	}
	gotSub, gotOK := comfy.TypeSubdir(got)
	wantSub, wantOK := comfy.TypeSubdir(wantTyp)
	if !gotOK || !wantOK {
		return false, got, wantTyp
	}
	return gotSub != wantSub, got, wantTyp
}

// parseModelIDParam reads the optional model_id form value. Absent/empty is a valid
// "no chosen model" (0, true); a non-numeric or negative value is REFUSED (0, false)
// rather than silently collapsing to "absent" — those two now take different code
// paths (filename resolution vs a specific model), so conflating them hides a bug.
func parseModelIDParam(v string) (int, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, true
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// alreadyInstalledNote is the run-status line for the skip-the-download fast path. It
// exists because that branch is what makes ANY earlier install permanent: without it,
// clicking install on the right model after a wrong file already occupies the
// destination looks identical to a successful fresh install.
func alreadyInstalledNote(filename string) string {
	return path.Base(strings.ReplaceAll(filename, "\\", "/")) +
		" is already installed — nothing was downloaded. Starting run…"
}

// workflowReferencesFile reports whether a workflow's stored graph actually references
// this model filename. Both `filename` and `model_id` arrive as free-form form fields
// and now drive a real network fetch plus a filesystem write, so the target must be
// bound to the workflow the request names — otherwise the endpoint installs arbitrary
// (filename, model) pairs for any workflow id. Matching is on the raw reference OR its
// basename, case-insensitively, since a reference may carry a subfolder prefix.
//
// It delegates to comfy.ReferencesModelFile — a pure function of the STORED graph, so
// the check cannot break when a run job is replaced or the server restarts (a
// run-state-based check would).
func workflowReferencesFile(wf *store.Workflow, filename string) bool {
	if wf == nil {
		return false
	}
	return comfy.ReferencesModelFile(wf.Format, json.RawMessage(wf.Graph), filename)
}

// renderResolveFallback renders the existing resolve fragment (heuristic model
// cards + "Search CivitAI" link) for a filename — the degrade path when a
// download cannot/should not proceed automatically — prefixed with reason, the
// one-line explanation of why this click installed nothing.
func (s *Server) renderResolveFallback(w http.ResponseWriter, r *http.Request, filename, typ, reason string) {
	query := comfy.CleanModelQuery(filename)
	var res *civitai.ModelSearchResult
	if query != "" {
		res = s.resolveModels(r.Context(), query, typ)
	}
	s.render(w, http.StatusOK, resolveModelFragmentWithReason(query, res, s.nsfwMode(), reason))
}

// renderSubstituteOffer answers a click that resolved to a DIFFERENT file than the
// workflow references: it downloads NOTHING and instead names both files and offers a
// second, explicit click (confirm_substitute=1) that carries the same target. The
// resolve cards are kept below so picking a different model stays one click away.
func (s *Server) renderSubstituteOffer(w http.ResponseWriter, r *http.Request, wfID int64, typ string, chosenModel int, plan installPlan) {
	query := comfy.CleanModelQuery(plan.FileName)
	var res *civitai.ModelSearchResult
	if query != "" {
		res = s.resolveModels(r.Context(), query, typ)
	}
	s.render(w, http.StatusOK, substituteOfferFragment(
		wfID, s.csrf, plan.FileName, plan.RemoteFileName, typ, chosenModel, query, res, s.nsfwMode()))
}

// fileExists reports whether path exists (any file type). Used to skip a download
// when the referenced model is already installed.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// resolveDownloadSource resolves a missing FILENAME to a downloadable CivitAI
// file. When chosenModel>0 it fetches that model directly and picks the file
// whose basename equals filename (else the primary file of the primary version).
// Otherwise it searches (types PLURAL, TTL-cached) and looks for the UNIQUE file
// across results whose basename equals filename — the strong, unambiguous signal.
// It returns ok=false when nothing matches (the caller then shows the resolve
// cards instead of auto-downloading).
//
// Supply-chain note: a workflow's model reference carries only a filename, not a
// hash, so the resolved file cannot be pinned/verified against an expected digest.
// "Download & run" therefore TRUSTS CivitAI's search ranking + the basename match
// to identify the intended model. The download itself is still constrained by the
// SDK's HTTPS-only, private-IP-blocking, civitai-host-scoped-token dialer, and by
// the size cap and path-containment guards — but the CHOICE of file is a trust in
// CivitAI, same as every other download in the app.
func (s *Server) resolveDownloadSource(parent context.Context, filename, typ string, chosenModel int) (resolvedDownload, installResolveOutcome) {
	want := path.Base(strings.ReplaceAll(strings.TrimSpace(filename), "\\", "/"))

	if chosenModel > 0 {
		ctx, cancel := context.WithTimeout(parent, 20*time.Second)
		defer cancel()
		_, raw, err := s.reader.GetModel(ctx, strconv.Itoa(chosenModel))
		if err != nil {
			s.log.Warn("download-and-run: GetModel failed", "model", chosenModel, "err", err)
			return resolvedDownload{}, resolveInstallNoFile
		}
		var body modelDetailEnvelope
		if err := json.Unmarshal(raw, &body); err != nil {
			return resolvedDownload{}, resolveInstallNoFile
		}
		// Cross-check the model's TYPE against the destination we are about to write
		// into: without it, a POST pairing type=Checkpoint with a LORA's model_id
		// writes a LoRA into checkpoints/.
		//
		// The invariant is the DESTINATION, not the type string. comfy.TypeSubdir
		// deliberately maps LORA, LoCon and LyCORIS all to loras/, and the resolver
		// routinely pairs them: a workflow's lora_name input makes InferCivitaiType
		// return "LORA", so a LoCon model's card posts type=LORA against a LoCon
		// model — a raw string compare hard-refuses a card that works, while nothing
		// could have landed in the wrong folder.
		if mismatch, got, wantTyp := typeDestinationMismatch(body.Type, typ); mismatch {
			s.log.Warn("download-and-run: model type routes to a different folder",
				"model", chosenModel, "model_type", got, "requested_type", wantTyp)
			return resolvedDownload{}, resolveInstallWrongType
		}
		rd, ok := pickFileFromModel(body, want)
		if !ok {
			return resolvedDownload{}, resolveInstallNoFile
		}
		return rd, resolveInstallOK
	}

	query := comfy.CleanModelQuery(filename)
	if query == "" {
		return resolvedDownload{}, resolveInstallNone
	}
	res := s.resolveModels(parent, query, typ)
	if res == nil || len(res.Raw) == 0 {
		return resolvedDownload{}, resolveInstallNone
	}
	// Filename-only resolution is EXACT-match across the search results — it never
	// substitutes, because with no chosen model there is no specific model to offer.
	rd, ok := pickFileFromSearchRaw(res.Raw, want)
	if !ok {
		return resolvedDownload{}, resolveInstallNone
	}
	return rd, resolveInstallOK
}

// searchFileEnvelope decodes the file-bearing subset of a model-search /
// model-detail raw body. Both the models-list and models/{id} responses embed
// modelVersions[].files[] with name/downloadUrl/sizeKB.
type searchFileEnvelope struct {
	Items []modelWithVersions `json:"items"`
}

type modelDetailEnvelope struct {
	modelWithVersions
}

type modelWithVersions struct {
	ID int `json:"id"`
	// Type is the CivitAI model type ("Checkpoint", "LORA", …). Decoded so a chosen
	// model_id can be cross-checked against the destination subfolder the caller's
	// `type` field selects — see resolveDownloadSource.
	Type          string `json:"type"`
	ModelVersions []struct {
		Files []struct {
			Name        string  `json:"name"`
			DownloadURL string  `json:"downloadUrl"`
			SizeKB      float64 `json:"sizeKB"`
			Primary     bool    `json:"primary"`
		} `json:"files"`
	} `json:"modelVersions"`
}

// pickFileFromSearchRaw finds, across all models/versions in a search body, the
// file whose basename equals want (case-insensitive). It returns the FIRST such
// match with a non-empty download URL — an exact-filename hit is the unambiguous
// "this is the file the workflow references" signal.
func pickFileFromSearchRaw(raw []byte, want string) (resolvedDownload, bool) {
	var body searchFileEnvelope
	if err := json.Unmarshal(raw, &body); err != nil {
		return resolvedDownload{}, false
	}
	for _, m := range body.Items {
		if rd, ok := m.pickFile(want); ok {
			return rd, true
		}
	}
	return resolvedDownload{}, false
}

// pickFileFromModelRaw decodes a single model's detail body and delegates to
// pickFileFromModel. Kept as the raw-bytes entry point for tests.
func pickFileFromModelRaw(raw []byte, want string) (resolvedDownload, bool) {
	var body modelDetailEnvelope
	if err := json.Unmarshal(raw, &body); err != nil {
		return resolvedDownload{}, false
	}
	return pickFileFromModel(body, want)
}

// pickFileFromModel finds, in a single model's detail body, the file whose basename
// equals want; failing that it falls back to the primary file of the primary
// (positional [0]) version — the version the detail page defaults to.
//
// That fallback is a DIFFERENT FILE from the one asked for. Callers must treat a
// returned FileName that differs from want as a substitution needing confirmation,
// never as a match — CLAUDE.md already records that modelVersions[0] is the creator's
// primary, not the newest or the intended one.
func pickFileFromModel(body modelDetailEnvelope, want string) (resolvedDownload, bool) {
	if rd, ok := body.pickFile(want); ok {
		return rd, true
	}
	// Fallback: primary file of the primary version.
	for _, v := range body.ModelVersions {
		var first *resolvedDownload
		for _, f := range v.Files {
			if strings.TrimSpace(f.DownloadURL) == "" {
				continue
			}
			rd := resolvedDownload{ModelID: body.ID, FileName: f.Name, DownloadURL: f.DownloadURL, SizeBytes: int64(f.SizeKB * 1024)}
			if f.Primary {
				return rd, true
			}
			if first == nil {
				cp := rd
				first = &cp
			}
		}
		if first != nil {
			return *first, true
		}
	}
	return resolvedDownload{}, false
}

// pickFile returns this model's file whose basename equals want (case-insensitive)
// and has a non-empty download URL.
func (m modelWithVersions) pickFile(want string) (resolvedDownload, bool) {
	lowWant := strings.ToLower(want)
	for _, v := range m.ModelVersions {
		for _, f := range v.Files {
			if strings.TrimSpace(f.DownloadURL) == "" {
				continue
			}
			base := path.Base(strings.ReplaceAll(f.Name, "\\", "/"))
			if strings.ToLower(base) == lowWant {
				return resolvedDownload{
					ModelID:     m.ID,
					FileName:    f.Name,
					DownloadURL: f.DownloadURL,
					SizeBytes:   int64(f.SizeKB * 1024),
				}, true
			}
		}
	}
	return resolvedDownload{}, false
}

// startDownloadAndRun launches a background job that FIRST downloads pd into the
// ComfyUI models dir (streaming progress into the run-status container), then runs
// the workflow with opts. It respects the one-run-at-a-time invariant (a click while
// any run/download is in flight is a no-op).
//
// opts is EMPTY for the plain download-and-run (the referenced file now exists on
// disk, so the original graph resolves unchanged). For install-option-and-run it
// carries the OTHER incompatible-options' picked fixes, applied to the ephemeral graph
// after the file lands — so the whole section resolves in one action. The stored
// workflow is never touched.
func (s *Server) startDownloadAndRun(wf *store.Workflow, pd pendingDownload, opts runOptions) bool {
	return s.startDownloadsAndRun(wf, []pendingDownload{pd}, opts, 0)
}

// startDownloadsAndRun is startDownloadAndRun for a BATCH: it installs every pd in
// order, then runs the workflow once. It is the same job/settle machinery — the only
// additions are the per-file progress prefix and the honest partial-failure error, so
// the single-file path (len(pds) == 1) produces byte-identical messages to before.
//
// A mid-batch download failure ABORTS before the run: the error names how many files
// did land, because those bytes are permanently on disk and a report that only said
// "download failed" would hide that. The files that succeeded are kept (they are the
// right files at the right destinations), so a retry only fetches what is left.
//
// alreadyPresent is how many of the caller's requested files were ALREADY on disk and
// so are not in pds. It is threaded through purely so the status lines can say so: a
// batch that silently drops the files it did not need to fetch reads as if the user
// had asked for fewer files than they did.
//
// It reports whether the job actually STARTED. false means the one-run-at-a-time guard
// discarded this call, and the caller must say so rather than render the other job's
// panel as if this click had worked.
func (s *Server) startDownloadsAndRun(wf *store.Workflow, pds []pendingDownload, opts runOptions, alreadyPresent int) bool {
	if len(pds) == 0 {
		return false
	}
	s.runMu.Lock()
	defer s.runMu.Unlock()
	if s.runJob != nil && s.runJob.running {
		return false // one run at a time
	}

	base := s.baseCtx
	if base == nil {
		base = context.Background()
	}
	// The runaway backstop has to cover N downloads AND the run. Reusing the
	// single-file budget for a batch would cancel a legitimate multi-checkpoint
	// install part-way through on a slow link — manufacturing exactly the
	// partial-install state this flow works to avoid.
	ctx, cancel := context.WithTimeout(base, batchJobBudget(len(pds)))
	s.runSeq++
	job := &runJob{
		running: true, workflowID: wf.ID, seq: s.runSeq, phase: runPhaseDownloading,
		message: downloadBatchOpeningMessage(pds, alreadyPresent), startedAt: time.Now(), cancel: cancel,
		uiFormat: wf.Format == store.WorkflowFormatUI,
	}
	s.runJob = job

	up := s.newRunUpdater(job)
	run := s.runFn
	if run == nil {
		run = s.realRun
	}
	download := s.downloadFn
	if download == nil {
		download = s.downloadModelFile
	}

	go func() {
		defer cancel()
		var res *runResult
		var err error
		func() {
			defer func() {
				if r := recover(); r != nil {
					err = fmt.Errorf("download-and-run panicked: %v", r)
				}
			}()
			for i, pd := range pds {
				idx := i
				if derr := download(ctx, pd, func(msg string) {
					up.setPhase(runPhaseDownloading, downloadStepMessage(idx, len(pds), alreadyPresent, msg), 0)
				}); derr != nil {
					err = downloadBatchError(idx, len(pds), alreadyPresent, derr)
					return
				}
			}
			res, err = run(ctx, wf, up, opts)
		}()
		// Settle + capture through the SHARED tail so a successful download-and-run
		// lands in the output gallery exactly like a plain run. This goroutine used to
		// take runMu itself (with a deferred unlock) around applyRunOutcomeLocked;
		// settleAndCapture now owns that lock/unlock, because it must RELEASE runMu
		// before the capture, which does network + disk work off the run mutex. (The
		// enclosing startDownloadAndRun still holds runMu across its own body — that
		// is the one-run-at-a-time guard and is unrelated to this goroutine.)
		s.settleAndCapture(job, wf, opts, res, err)
	}()
	return true
}

// downloadFileBudget is the runaway-backstop allowance added PER EXTRA FILE in a
// batch install. Like runJobBudget it is not the normal termination path — a model
// file is routinely multiple GB and a slow link legitimately takes a long time; this
// only bounds a genuinely stuck batch so it cannot leak a goroutine forever.
const downloadFileBudget = 30 * time.Minute

// batchJobBudget sizes the runaway backstop for n downloads plus the run. n == 1
// returns runJobBudget exactly, so the single-file path is unchanged. n is bounded by
// maxBatchInstallFiles, so the budget is bounded too.
func batchJobBudget(n int) time.Duration {
	if n <= 1 {
		return runJobBudget
	}
	return runJobBudget + time.Duration(n-1)*downloadFileBudget
}

// downloadBatchOpeningMessage is the job's first status line. A lone download with
// nothing already present keeps its exact original wording; otherwise it names the
// counts, the already-installed files (never silently dropped — the user asked about
// those too) and the advertised total size, which is real data from the resolved
// plans and is the last moment Stop is free.
func downloadBatchOpeningMessage(pds []pendingDownload, alreadyPresent int) string {
	if len(pds) == 1 && alreadyPresent == 0 {
		return "Preparing download of " + pds[0].progressName() + "…"
	}
	var total int64
	for _, pd := range pds {
		if pd.ContentLengthHint > 0 {
			total += pd.ContentLengthHint
		}
	}
	// The total is the sum of the ADVERTISED sizes CivitAI reported for the resolved
	// files (sizeKB), not bytes anyone has counted, so it is labelled as such — the
	// whole point of showing a real number here is not to imply a measured one.
	size := ""
	if total > 0 {
		size = " (about " + humanBytes(total) + " as listed on CivitAI)"
	}
	if alreadyPresent > 0 {
		return fmt.Sprintf("%d model file%s %s already installed — preparing to install the remaining %d%s…",
			alreadyPresent, plural(alreadyPresent), isAre(alreadyPresent), len(pds), size)
	}
	return fmt.Sprintf("Preparing to install %d model file%s%s…", len(pds), plural(len(pds)), size)
}

// downloadStepMessage prefixes a batch progress line with its position IN THE WHOLE
// REQUESTED SET, so a multi-file install is legible ("(2/3) Downloading x… 41%") and
// so files that were already on disk are still counted — with 1 of 2 already present
// the single remaining download reads "(2/2)", not "(1/1)", which would quietly
// misreport what the user asked for. Unprefixed only for a lone download with nothing
// already present (unchanged behaviour).
func downloadStepMessage(i, n, alreadyPresent int, msg string) string {
	if n == 1 && alreadyPresent == 0 {
		return msg
	}
	return fmt.Sprintf("(%d/%d) %s", alreadyPresent+i+1, alreadyPresent+n, msg)
}

// downloadBatchError reports a mid-batch failure HONESTLY: it names how many files are
// installed (the ones this batch completed PLUS the ones that were already on disk —
// all of them are present now, and those bytes are permanent) rather than presenting a
// partial install as a plain download error.
func downloadBatchError(i, n, alreadyPresent int, err error) error {
	if n == 1 && alreadyPresent == 0 {
		return err
	}
	return fmt.Errorf("installed %d of %d model files, then failed: %w", alreadyPresent+i, alreadyPresent+n, err)
}

// downloadModelFile fetches pd.URL and writes it atomically to pd.DestPath under
// the ComfyUI models dir, enforcing the size cap and streaming throttled progress
// via cb. An already-present destination (ErrDestExists) is NOT an error — the
// file is there, so the run can proceed.
func (s *Server) downloadModelFile(ctx context.Context, pd pendingDownload, cb func(string)) error {
	cb("Downloading " + pd.progressName() + "…")
	if err := assertHTTPSDownloadURL(pd.URL); err != nil {
		return err
	}
	resp, err := s.modelDownloader(pd).DownloadFile(ctx, pd.URL)
	if err != nil {
		return fmt.Errorf("download %s: %w", pd.FileName, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		snippet := make([]byte, 256)
		n, _ := io.ReadFull(io.LimitReader(resp.Body, 256), snippet)
		return fmt.Errorf("download %s returned HTTP %d: %s", pd.FileName, resp.StatusCode, string(snippet[:n]))
	}

	contentLen := resp.ContentLength
	if contentLen <= 0 && pd.ContentLengthHint > 0 {
		contentLen = pd.ContentLengthHint
	}
	pr := &downloadProgressReader{
		r: resp.Body, total: contentLen, name: pd.progressName(), cb: cb, last: time.Now(),
	}
	// The HuggingFace path pins on a known sha256 (the tree's LFS oid); the civitai
	// path has no hash to pin (empty → verification skipped, unchanged behavior).
	_, err = comfy.WriteModelStreamVerified(pd.DestPath, pr, contentLen, pd.MaxBytes, pd.ExpectedSHA256)
	if errors.Is(err, comfy.ErrDestExists) {
		// Installed concurrently / already present — proceed to run, but record
		// NOTHING: we did not write these bytes, so we cannot claim they are ours.
		return nil
	}
	if err != nil {
		return err
	}
	// The rename succeeded, which for an HF source means the streamed bytes already
	// hashed to pd.ExpectedSHA256 (WriteModelStreamVerified verifies BEFORE the
	// rename and removes the temp file on a mismatch). Only here is the provenance
	// claim actually true.
	s.recordHFProvenance(pd)
	cb("Download complete — starting run…")
	return nil
}

// recordHFProvenance persists "these bytes came from {repo}@{revision}/{path}"
// for a completed HuggingFace install.
//
// It is called ONLY after WriteModelStreamVerified returns nil, i.e. after the
// stream hashed to pd.ExpectedSHA256 and the atomic rename landed. hfInstallEligible
// → hf.Match.AutoDownloadEligible requires a non-empty oid, so an HF plan ALWAYS
// carries an expected sha256 and that verification is never skipped — the recorded
// hash is proven against the bytes on disk, not asserted.
//
// It writes nothing for a civitai download, nothing when the triple is incomplete,
// and nothing when the destination already existed (handled by the caller). It adds
// ZERO egress: every value is already in hand from the resolve that produced the
// download.
//
// Failure is logged and never propagated. The file is on disk and works; a missing
// source link is cosmetic and must not turn a successful install into a failed one.
func (s *Server) recordHFProvenance(pd pendingDownload) {
	if !pd.SourceHF || s.store == nil {
		return
	}
	if pd.ExpectedSHA256 == "" || pd.HFRepo == "" || pd.HFPath == "" || pd.HFRevision == "" {
		return
	}
	if err := s.store.UpsertHFProvenance(store.HFProvenance{
		SHA256:     pd.ExpectedSHA256,
		Repo:       pd.HFRepo,
		Path:       pd.HFPath,
		Revision:   pd.HFRevision,
		RecordedAt: time.Now().UTC(),
	}); err != nil {
		s.log.Warn("record huggingface provenance failed",
			"file", pd.FileName, "repo", pd.HFRepo, "err", err)
	}
}

// assertHTTPSDownloadURL is a cheap, app-level belt on every API-supplied download
// URL (CivitAI `downloadUrl`, HF resolve URL) before it is handed to a downloader:
// the scheme MUST be https, else we refuse WITHOUT egressing. This catches an
// http:/file:/other-scheme URL from a malicious or compromised API response at the
// app layer. It is intentionally NOT a host allowlist — that risks breaking legit
// downloads for marginal gain.
//
// The private-IP / SSRF dial-time block (and token host-scoping) remains the SDK's
// job: the `github.com/civitai/cli` downloader's dialer blocks private/link-local
// dial targets and scopes the bearer token to civitai hosts (covered by its
// download_ssrf_test.go — IsBlockedDownloadIP / RequireHTTPSDownload / loopback
// refusal; at the pinned v0.1.82 that lives in pkg/civitai/). RE-VERIFY that guard
// on any civitai/cli bump — this app-level assertion does not replace it.
func assertHTTPSDownloadURL(rawURL string) error {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return fmt.Errorf("refusing download: unparseable URL")
	}
	if !strings.EqualFold(u.Scheme, "https") {
		return fmt.Errorf("refusing download: URL scheme %q is not https", u.Scheme)
	}
	return nil
}

// modelDownloader picks the fetcher for a pending download: the SSRF-hardened
// HuggingFace client for an HF source, otherwise the civitai downloader. Both
// satisfy the same (ctx,url)→(*http.Response,error) shape.
func (s *Server) modelDownloader(pd pendingDownload) interface {
	DownloadFile(ctx context.Context, fileURL string) (*http.Response, error)
} {
	if pd.SourceHF {
		if c := s.hfClientOrNil(); c != nil {
			return c
		}
	}
	return s.downloader()
}

// downloadProgressReader wraps a download body and reports throttled progress
// (at most every progressInterval) through cb, so the run-status poller shows a
// moving "Downloading… N%" line during an otherwise-silent multi-second fetch.
type downloadProgressReader struct {
	r     io.Reader
	total int64
	name  string
	read  int64
	last  time.Time
	cb    func(string)
}

const progressInterval = 500 * time.Millisecond

func (d *downloadProgressReader) Read(p []byte) (int, error) {
	n, err := d.r.Read(p)
	d.read += int64(n)
	if time.Since(d.last) >= progressInterval {
		d.last = time.Now()
		d.cb(d.message())
	}
	return n, err
}

func (d *downloadProgressReader) message() string {
	if d.total > 0 {
		pct := d.read * 100 / d.total
		if pct > 100 {
			pct = 100
		}
		return fmt.Sprintf("Downloading %s… %d%% (%s / %s)", d.name, pct, humanBytes(d.read), humanBytes(d.total))
	}
	return fmt.Sprintf("Downloading %s… %s", d.name, humanBytes(d.read))
}
