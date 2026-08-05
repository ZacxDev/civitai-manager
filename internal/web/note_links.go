package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/comfy"
	"github.com/ZacxDev/civitai-manager/internal/hf"
	"github.com/ZacxDev/civitai-manager/internal/store"
	g "maragu.dev/gomponents"
	h "maragu.dev/gomponents/html"
)

// The workflow's OWN notes as a resolution source for a missing model file.
//
// A ComfyUI workflow routinely carries a "## Model links" MarkdownNote naming
// every file it needs and where to get it. Neither of the two existing sources can
// find those files: CivitAI has no filename search (we do a fuzzy title search plus
// an exact-basename filter over the results) and HuggingFace's `?search=` indexes
// repo NAMES, so a file living in a repo whose name shares nothing with the
// filename is unreachable from a query derived from that filename. Measured on the
// operator's workflow 590, the note names both files preflight reports missing.
//
// 🔴 THE SECURITY POSTURE, in one place, because it is the whole reason this file
// is more than a link renderer:
//
//  1. NOTE TEXT IS UNTRUSTED. A workflow arrives from CivitAI; its note is a
//     string a stranger wrote. A URL out of it gets no more authority than that.
//  2. AUTO-FETCH ONLY WHERE A HARDENED CLIENT ALREADY COVERS THE HOST.
//     modelDownloader is a TWO-WAY switch on pendingDownload.SourceHF — the
//     SSRF-hardened HuggingFace client or the civitai SDK downloader, and there is
//     no third. The civitai downloader blocks private dial targets but carries NO
//     host allowlist, so handing it a github.com URL would fetch it. Therefore only
//     a `huggingface.co/{repo}/resolve/...` URL is ever fetched, always through
//     hf.Client (whose dialer enforces the host allowlist on every redirect hop),
//     and EVERYTHING ELSE IS A LINK the user clicks themselves. Adding a third
//     egress client is deliberately out of scope.
//  3. THE URL IS BOUND TO THE GRAPH AT THE POINT OF ACTION. The install handler
//     re-extracts the note links from the STORED workflow and refuses any url that
//     is not among them — so the endpoint cannot be driven to fetch an arbitrary
//     URL even by a caller who already holds a CSRF token.
//  4. OFFER, DON'T PERFORM. An exact-basename match installs on one click; a
//     DIFFERENT remote basename is offered and needs a second click carrying both
//     confirm_substitute=1 and confirm_file=<remote basename> — the same rule, and
//     the same reasons, as pickFileFromModel's primary-version fallback.

// noteLinkOffer is one note URL offered against ONE missing model file. It is
// computed at run settle and rendered later, so it carries everything the render
// needs and no server handle.
type noteLinkOffer struct {
	// URL is the https URL exactly as the author wrote it.
	URL string
	// Basename is the URL's final path segment — equal (case-insensitively) to the
	// missing filename, since that is how the offer was selected.
	Basename string
	// Host is the URL's hostname, shown so the user can see WHO they would be
	// downloading from before they click.
	Host string
	// AutoFetchable reports that this app can fetch the URL itself: it is a
	// HuggingFace /resolve/ URL AND the hardened HuggingFace client is available.
	// False means link-only — see the posture note above.
	AutoFetchable bool
}

// noteLinkOffersFor selects the note links whose basename EXACTLY matches this
// missing file, and classifies each as auto-fetchable or link-only.
//
// hfAvailable is passed in rather than read from a server so this stays a pure
// function: whether the HuggingFace client exists is a config fact, and folding it
// in here means the render can never offer a CTA the handler would refuse.
func noteLinkOffersFor(links []comfy.NoteLink, filename string, hfAvailable bool) []noteLinkOffer {
	matches := comfy.NoteLinksMatching(links, filename)
	if len(matches) == 0 {
		return nil
	}
	out := make([]noteLinkOffer, 0, len(matches))
	for _, l := range matches {
		u, err := url.Parse(l.URL)
		if err != nil {
			continue
		}
		_, _, _, isHF := hf.ParseResolveURL(l.URL)
		out = append(out, noteLinkOffer{
			URL:           l.URL,
			Basename:      l.Basename,
			Host:          u.Hostname(),
			AutoFetchable: isHF && hfAvailable,
		})
	}
	return out
}

// attachNoteLinks fills each missing model's NoteLinks from the workflow's OWN
// stored graph. It is called at run settle, beside the CivitAI/HuggingFace
// resolution, and does NO network work at all — extraction and matching are pure.
//
// 🔴 It reads wf.Graph, the UI graph, NOT the converted api graph the rest of the
// failure analysis runs on. Note and MarkdownNote are virtual node types and
// conversion DROPS them, so the notes do not exist any more by the time preflight
// has produced a report.
func (s *Server) attachNoteLinks(wf *store.Workflow, models []comfy.MissingModel, resolved map[string]missingResolution) {
	if wf == nil || len(models) == 0 || resolved == nil {
		return
	}
	links := comfy.ExtractNoteLinks(wf.Format, json.RawMessage(wf.Graph))
	if len(links) == 0 {
		return
	}
	hfAvailable := s.hfClientOrNil() != nil
	for _, mm := range models {
		offers := noteLinkOffersFor(links, mm.Filename, hfAvailable)
		if len(offers) == 0 {
			continue
		}
		r := resolved[mm.Filename]
		r.NoteLinks = offers
		resolved[mm.Filename] = r
	}
}

// noteSectionID is the id of the note-links block inside ONE missing model's Fix
// dialog. It is derived from that dialog's own id so the two can never disagree
// about which missing file they describe. Guards assert this id plus the block's
// state attribute rather than any wording, so a copy change cannot satisfy them.
func noteSectionID(dlgID string) string { return dlgID + "-note-links" }

// noteLinkSection renders the "Linked in this workflow's notes" section of the Fix
// dialog. It renders NOTHING when the workflow's notes named no matching file, so
// an ordinary workflow's dialog is unchanged.
//
// Each offer is either an Install-and-run CTA (HuggingFace only — see the posture
// note) or a scheme-validated external link with the reason it is link-only. Every
// untrusted string (the URL, its host) is escaped by g.Text / gomponents attribute
// escaping, and the href is emitted only after the extractor has already proven the
// scheme is https.
func noteLinkSection(dlgID string, mm comfy.MissingModel, offers []noteLinkOffer, wfID int64, csrf string, dlEligible bool) g.Node {
	if len(offers) == 0 {
		return nil
	}
	installable := dlEligible && comfyTypeRoutable(mm.CivitaiType)
	rows := make([]g.Node, 0, len(offers))
	for _, o := range offers {
		rows = append(rows, noteLinkRow(mm, o, wfID, csrf, installable))
	}
	return h.Div(
		h.ID(noteSectionID(dlgID)),
		// The spacing lives here, not in a wrapper at the call site: a wrapper would
		// emit an empty <div> on every ordinary dialog, where this whole section
		// renders nothing at all.
		h.Class("mt-6"),
		// A STATE attribute, not prose: a guard can assert what this section IS
		// without depending on a sentence any other feature is free to spell.
		g.Attr("data-note-links", strconv.Itoa(len(offers))),
		h.H3(h.Class("text-sm font-semibold text-slate-200"),
			g.Text("Linked in this workflow's notes")),
		h.P(h.Class("text-xs text-slate-400 mb-2"),
			g.Text("The workflow author wrote this download link into a note in the graph. "+
				"It is their claim, not a verified one — check the source before installing.")),
		g.Group(rows),
	)
}

// noteLinkOnlyReason is the one-line explanation under a link-only note URL. It
// names what this app can fetch, rather than accusing the host of anything: a
// github release or an openmodeldb page is a perfectly good download, it is simply
// not a host either of this app's two hardened clients covers.
const noteLinkOnlyReason = "civitai-manager only downloads automatically from CivitAI and HuggingFace, " +
	"so open this link and save the file into ComfyUI yourself."

// noteInstallBlockedReason is shown when the URL IS fetchable but this install
// cannot proceed — comfy_model_path unset, a non-local ComfyUI, or a model type
// with no known ComfyUI folder. It points at no control, for the same structural
// reason as cardInstallBlockedText: this renders inside a showModal() <dialog>, so
// anything "above" is behind the modal and invisible to the reader.
const noteInstallBlockedReason = "civitai-manager is not set up to install this file for you, " +
	"so open the link and save it into ComfyUI yourself."

// noteLinkRow renders ONE offered note URL: the host + URL, then either the CTA or
// the external link and its reason.
func noteLinkRow(mm comfy.MissingModel, o noteLinkOffer, wfID int64, csrf string, installable bool) g.Node {
	body := []g.Node{
		h.Div(h.Class("text-xs text-slate-500"), g.Text("from "+o.Host)),
		h.Div(h.Class("font-mono text-xs text-slate-300 break-all"), g.Text(o.URL)),
	}
	switch {
	case o.AutoFetchable && installable:
		body = append(body, h.Div(h.Class("mt-2"),
			civButton("filled", "sm", []g.Node{
				h.Type("button"),
				// The dialog already carries an "Install and run" per CivitAI card, so
				// the accessible name has to say WHICH file from WHERE — otherwise a
				// screen reader hears the same label several times over.
				g.Attr("aria-label", "Install "+o.Basename+" from "+o.Host+" and run"),
				hx("post", "/workflows/"+strconv.FormatInt(wfID, 10)+"/install-from-note"),
				hx("target", "#"+runStatusContainerID),
				hx("swap", "innerHTML"),
				hx("disabled-elt", "this"),
				noteInstallVals(csrf, mm, o.URL, "", ""),
			}, g.Text("Install and run")),
		))
	default:
		reason := noteLinkOnlyReason
		if o.AutoFetchable {
			reason = noteInstallBlockedReason
		}
		body = append(body,
			h.Div(h.Class("mt-2 space-y-1"),
				noteOpenLink(o.URL, o.Basename, o.Host),
				h.P(h.Class("text-xs text-slate-500"), g.Text(reason)),
			))
	}
	return h.Div(h.Class("rounded border border-slate-800 p-2 mb-2"), g.Group(body))
}

// noteOpenLink is the external link for a link-only note URL. The href is emitted
// only for an https URL — the extractor's pattern already guarantees that, and this
// asserts it a second time so a change there cannot quietly put another scheme into
// an href (mirrors hfOpenLink's defensive posture).
func noteOpenLink(rawURL, basename, host string) g.Node {
	if !strings.HasPrefix(rawURL, "https://") {
		return h.P(h.Class("text-xs text-slate-500"), g.Text("This link could not be shown safely."))
	}
	return h.A(
		h.Href(rawURL),
		h.Target("_blank"),
		h.Rel("noopener noreferrer"),
		g.Attr("aria-label", "Open "+basename+" on "+host+" (opens in a new tab)"),
		h.Class("text-sm text-indigo-400 hover:underline"),
		g.Text("Open the download link ↗"),
	)
}

// noteInstallVals builds the hx-vals JSON for a note install POST. json.Marshal
// escapes any quote/backslash in the untrusted URL and filename; gomponents then
// HTML-escapes the attribute value (matching the repo's csrfInline posture).
//
// confirmFile is non-empty ONLY on the confirming click of a substitution, and it
// carries WHICH remote basename was approved — an approval bound to a specific
// file, not a blanket one.
func noteInstallVals(csrf string, mm comfy.MissingModel, noteURL, confirmSub, confirmFile string) g.Node {
	vals := map[string]string{
		"csrf_token": csrf,
		"filename":   mm.Filename,
		"type":       mm.CivitaiType,
		"note_url":   noteURL,
	}
	if confirmSub != "" {
		vals["confirm_substitute"] = confirmSub
		vals["confirm_file"] = confirmFile
	}
	b, _ := json.Marshal(vals)
	return hx("vals", string(b))
}

// noteResolveReason* are the explanations rendered above the resolve cards when a
// note install could not proceed. Like the resolveReason* constants in
// run_download.go, every one opens with "Nothing was downloaded" — this fragment
// REPLACES the panel the user was looking at, so without a reason the response is
// byte-identical to the pre-click panel and the button reads as dead.
const (
	// The posted URL is not one this app can fetch (not a HuggingFace /resolve/
	// URL). The button for such a link is never rendered, so reaching this means a
	// stale page or a hand-made request.
	noteReasonNotFetchable = "Nothing was downloaded: that link is not one civitai-manager can " +
		"download from. Open it yourself and save the file into ComfyUI."
	// The HuggingFace fallback is switched off in config, so there is no hardened
	// client to fetch with. The civitai downloader must NEVER be used here.
	noteReasonHFDisabled = "Nothing was downloaded: HuggingFace downloads are turned off in this " +
		"install's configuration. Open the link and save the file into ComfyUI yourself."
	// The type has no ComfyUI folder, so there is no defined destination.
	noteReasonNoDestination = "Nothing was downloaded: civitai-manager does not know which ComfyUI " +
		"folder this kind of file belongs in, so it cannot install it for you."
	// The repo no longer contains the file, or HuggingFace could not be reached.
	noteReasonRepoMiss = "Nothing was downloaded: that HuggingFace repository no longer contains " +
		"this file, or it could not be reached. Open the link to check."
	// The repo is gated: an anonymous download would fail, so refuse before egress.
	noteReasonGated = "Nothing was downloaded: that HuggingFace repository is gated — accept the " +
		"model's terms on HuggingFace, then download it there."
	// 🔴 No sha256 to pin the bytes against. Every other HuggingFace install in this
	// app verifies the stream before the atomic rename, and a link out of untrusted
	// note text is the LAST place to relax that.
	noteReasonNoHash = "Nothing was downloaded: HuggingFace publishes no content hash for that " +
		"file, so the download could not be verified. Open the link and save it yourself."
)

// noteInstallBudget bounds the ONE HuggingFace metadata round-trip a note install
// makes (repo info + file tree) before any bytes are fetched.
const noteInstallBudget = 20 * time.Second

// handleWorkflowInstallFromNote installs a model file from a download URL THE
// WORKFLOW ITSELF documents in a Note/MarkdownNote node, then runs the workflow.
// CSRF-protected + loopback-gated (same prologue order as
// handleWorkflowDownloadAndRun) and reaching huggingface.co + the local filesystem.
//
// Its refusals are the interesting part, in the order they run:
//
//   - the filename must be one THIS workflow references (workflowReferencesFile);
//   - 🔴 the url must be one THIS workflow's notes contain, re-derived from the
//     STORED graph on every request. That is what stops the endpoint from being an
//     arbitrary-URL fetcher: holding a CSRF token buys you the workflow's own
//     links and nothing else;
//   - the scheme must be https (assertHTTPSDownloadURL);
//   - 🔴 the host must be one the hardened HuggingFace client covers, and that
//     client must exist. Everything else is link-only — see this file's posture
//     note for why there is no third client to route to;
//   - a remote basename that DIFFERS from the workflow's reference is OFFERED, not
//     installed, and the approval must name the exact file;
//   - the bytes must be pinnable: gated repos and files with no LFS oid are
//     refused rather than downloaded unverified.
func (s *Server) handleWorkflowInstallFromNote(w http.ResponseWriter, r *http.Request) {
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
	noteURL := strings.TrimSpace(r.FormValue("note_url"))
	if noteURL == "" {
		http.Error(w, "missing note_url", http.StatusBadRequest)
		return
	}
	typ := civitaiTypeParam(r.FormValue("type"))
	confirmed := r.FormValue("confirm_substitute") == "1"
	approvedFile := strings.TrimSpace(r.FormValue("confirm_file"))

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
	// 🔴 THE BINDING CHECK. Re-derive the workflow's own links and require the
	// posted url to be one of them, byte-for-byte. A 400 (not a rendered reason) is
	// deliberate: this is never something a click can produce, so it is a bad
	// request rather than a state to explain.
	if !workflowNoteLinkURL(wf, noteURL) {
		http.Error(w, "note_url is not linked by this workflow", http.StatusBadRequest)
		return
	}

	if !s.comfyDownloadEligible() {
		s.renderResolveFallback(w, r, filename, typ, resolveReasonNotEligible)
		return
	}
	if err := assertHTTPSDownloadURL(noteURL); err != nil {
		s.renderResolveFallback(w, r, filename, typ, noteReasonNotFetchable)
		return
	}
	repo, _, repoPath, isHF := hf.ParseResolveURL(noteURL)
	if !isHF {
		// 🔴 The link-only branch. There is no downloader for this host, and the
		// civitai one must not be pressed into service — it has no host allowlist.
		s.renderResolveFallback(w, r, filename, typ, noteReasonNotFetchable)
		return
	}
	client := s.hfClientOrNil()
	if client == nil {
		s.renderResolveFallback(w, r, filename, typ, noteReasonHFDisabled)
		return
	}
	subdir, ok := comfy.TypeSubdir(typ)
	if !ok {
		s.renderResolveFallback(w, r, filename, typ, noteReasonNoDestination)
		return
	}
	dest, err := comfy.SafeModelDest(s.comfyModelPath(), subdir, filename)
	if err != nil {
		s.renderResolveFallback(w, r, filename, typ, noteReasonNoDestination)
		return
	}

	// OFFER-DON'T-PERFORM, decided BEFORE any egress: the remote name is already
	// known from the URL, so a substitution is offered without contacting anyone.
	want := comfy.PathBase(filename)
	remote := path.Base(repoPath)
	if !strings.EqualFold(remote, want) {
		if !confirmed || !strings.EqualFold(approvedFile, remote) {
			s.render(w, http.StatusOK, noteSubstituteOfferFragment(id, s.csrf, want, remote, noteURL, typ))
			return
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), noteInstallBudget)
	defer cancel()
	m, found, rerr := client.ResolveInRepo(ctx, repo, remote)
	if rerr != nil {
		s.log.Warn("install-from-note: huggingface lookup failed", "repo", repo, "err", rerr)
	}
	if rerr != nil || !found || m == nil {
		s.renderResolveFallback(w, r, filename, typ, noteReasonRepoMiss)
		return
	}
	if m.Gated {
		s.renderResolveFallback(w, r, filename, typ, noteReasonGated)
		return
	}
	if strings.TrimSpace(m.SHA256) == "" {
		s.renderResolveFallback(w, r, filename, typ, noteReasonNoHash)
		return
	}

	plan := installPlan{
		FileName:       want,
		RemoteFileName: m.FileName,
		// m.URL, not the note's URL: ResolveInRepo pins it to the repo's current
		// commit sha, where the author's link says /main/ and main moves.
		URL:            m.URL,
		DestPath:       dest,
		ExpectedSHA256: m.SHA256,
		SourceHF:       true,
		HFRepo:         m.Repo,
		HFPath:         m.Path,
		HFRevision:     m.Revision,
	}

	if fileExists(plan.DestPath) {
		if !s.startRunWithMessage(wf, runOptions{}, alreadyInstalledNote(filename)) {
			s.renderRunActionDeclined(w, id, installMissingBusyReason)
			return
		}
		s.render(w, http.StatusOK, runStatusBody(s.runJobState(), id, s.csrf, s.comfyDownloadEligible(), s.maturity()))
		return
	}
	if !s.startDownloadAndRun(wf, planToPending(plan, s.cfg.MaxFileSizeBytes), runOptions{}) {
		s.renderRunActionDeclined(w, id, installMissingBusyReason)
		return
	}
	s.render(w, http.StatusOK, runStatusBody(s.runJobState(), id, s.csrf, true, s.maturity()))
}

// workflowNoteLinkURL reports whether rawURL is one of the https URLs this
// workflow's OWN Note/MarkdownNote nodes contain.
//
// It re-extracts from the stored graph on every request rather than trusting
// anything the client echoed back, which is the property that keeps the install
// endpoint from being a general-purpose fetcher. Comparison is exact: the render
// emits the URL verbatim, so a client that changes so much as a character is not
// clicking a link this workflow offers.
func workflowNoteLinkURL(wf *store.Workflow, rawURL string) bool {
	if wf == nil || rawURL == "" {
		return false
	}
	for _, l := range comfy.ExtractNoteLinks(wf.Format, json.RawMessage(wf.Graph)) {
		if l.URL == rawURL {
			return true
		}
	}
	return false
}

// noteSubstituteOfferFragment answers a note install whose remote basename is NOT
// the file the workflow references: it downloads NOTHING, names both files, and
// offers a second click carrying confirm_substitute=1 AND confirm_file=<remote>.
//
// It reuses substituteOfferText, so the two substitution paths say the same thing
// about the same hazard — the bytes and the on-disk name would disagree.
//
// ⚠ DELIBERATE BYPASS of runStatusBody, same shape and same reasoning as
// renderSubstituteOffer in run_download.go: it writes into #run-status without an
// out-of-band readiness clear, which is safe ONLY because it is reachable solely
// from an already-rendered failure panel and STARTS NO RUN. The confirming click
// does start one, and goes through runStatusBody as it must.
func noteSubstituteOfferFragment(wfID int64, csrf, requested, remote, noteURL, typ string) g.Node {
	mm := comfy.MissingModel{Filename: requested, CivitaiType: typ}
	return h.Div(
		g.Attr("data-note-substitute-offer", remote),
		h.P(g.Attr("role", "status"), h.Class("text-xs font-semibold text-amber-400 mb-2"),
			g.Text(substituteOfferText(requested, remote))),
		h.Div(h.Class("mb-3"),
			civButton("filled", "sm", []g.Node{
				h.Type("button"),
				hx("post", "/workflows/"+strconv.FormatInt(wfID, 10)+"/install-from-note"),
				hx("target", "#"+runStatusContainerID),
				hx("swap", "innerHTML"),
				hx("disabled-elt", "this"),
				noteInstallVals(csrf, mm, noteURL, "1", remote),
			}, g.Text("Install "+remote+" as "+requested)),
		),
	)
}
