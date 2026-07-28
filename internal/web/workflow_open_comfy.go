package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/comfy"
	"github.com/ZacxDev/civitai-manager/internal/comfyext"
	"github.com/ZacxDev/civitai-manager/internal/store"
	g "maragu.dev/gomponents"
	h "maragu.dev/gomponents/html"
)

// openComfyDir is the namespace all "Open in ComfyUI" workflows are written under
// in ComfyUI's user workflow store, so the tool's files stay grouped (and are
// never confused with the user's own saved workflows).
const openComfyDir = "civitai-manager"

// openComfyParam is the URL parameter the civitai-manager ComfyUI helper reads to
// open a saved workflow.
//
// It is deliberately NOT `workflow`: ComfyUI has NEVER had a `?workflow=` param in
// any frontend version (verified by sourcemap extraction across 1.45.20, 1.47.10
// and ~1.49 — the only workflow-opening params are template/source/mode plus a
// cloud-only share). The old link silently landed the user on whatever graph
// ComfyUI had open last, which read as "civitai-manager saved the wrong
// workflow". This param works only because OUR helper implements it, so it is
// emitted ONLY when the helper is actually detected.
const openComfyParam = "cm_open"

// extProbeTTL bounds how long a helper feature-detection result (present OR
// absent) is reused before re-probing.
const extProbeTTL = 30 * time.Second

// extProbeTimeout bounds a single probe. A ComfyUI that is down or slow must not
// stall the click that triggered it.
const extProbeTimeout = 1500 * time.Millisecond

// extOpenTimeout bounds the jump-an-open-tab broadcast. It is deliberately tight
// and SEPARATE from the enclosing handler budget: the broadcast is a nice-to-have
// (the deep link works without it), so a wedged helper must not hold the user's
// click for the full save timeout.
const extOpenTimeout = 2 * time.Second

// openComfyContainerID is the stable element the "Open in ComfyUI" button and its
// POST result share, so the action swaps its own container inline (mirrors
// workflowImportContainerID).
func openComfyContainerID(id int64) string {
	return fmt.Sprintf("wf-open-comfy-%d", id)
}

// extContainerID is the nested container the helper install/uninstall actions
// swap, so installing does not blow away the surrounding result fragment.
func extContainerID(id int64) string {
	return fmt.Sprintf("cm-comfy-ext-%d", id)
}

// sanitizeWorkflowFilename builds a TRAVERSAL-SAFE "<name>-<id>.json" filename from
// an untrusted workflow name. It keeps only [A-Za-z0-9-_], collapses every other
// character (including path separators and "..") to a single '-', trims dashes, and
// bounds the length. The result therefore can never contain '/', '\\', or '..', so
// the namespaced civitai-manager/<file> path cannot escape its directory.
func sanitizeWorkflowFilename(name string, id int64) string {
	var b strings.Builder
	lastDash := false
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	base := strings.Trim(b.String(), "-")
	if len(base) > 60 {
		base = strings.Trim(base[:60], "-")
	}
	if base == "" {
		base = "workflow"
	}
	return fmt.Sprintf("%s-%d.json", base, id)
}

// comfyExtensionProbe reports whether this ComfyUI is running the civitai-manager
// helper, using a short-lived cache of BOTH outcomes.
//
// It is only ever called from a user action (never a page render): the probe is a
// network round-trip, and a detection result that is stale by up to extProbeTTL is
// harmless — the worst case is one wasted fallback render right after the user
// restarts ComfyUI.
func (s *Server) comfyExtensionProbe(ctx context.Context, client comfyClient) extProbe {
	s.extProbeMu.Lock()
	if s.extProbeVal != nil && time.Now().Before(s.extProbeExp) {
		cached := *s.extProbeVal
		s.extProbeMu.Unlock()
		return cached
	}
	s.extProbeMu.Unlock()

	res := extProbe{}
	if client != nil {
		pctx, cancel := context.WithTimeout(ctx, extProbeTimeout)
		info, err := client.ExtensionPing(pctx)
		cancel()
		switch {
		case err == nil && info != nil:
			res.present = true
			res.version = info.Version
		case errors.Is(err, comfy.ErrExtensionAbsent):
			// Expected: a stock ComfyUI. Cache the negative too.
		case err != nil:
			s.log.Debug("comfy helper probe failed", "err", err)
		}
	}

	s.extProbeMu.Lock()
	cached := res
	s.extProbeVal = &cached
	s.extProbeExp = time.Now().Add(extProbeTTL)
	s.extProbeMu.Unlock()
	return res
}

// invalidateComfyExtensionProbe drops the cached detection result. It is called
// after an install/uninstall (and after a request proves the cache wrong) so the
// UI does not keep serving a stale verdict for up to extProbeTTL.
func (s *Server) invalidateComfyExtensionProbe() {
	s.extProbeMu.Lock()
	s.extProbeVal = nil
	s.extProbeExp = time.Time{}
	s.extProbeMu.Unlock()
}

// comfyExtStatus inspects the helper's on-disk install state under the configured
// comfy_root. It is a couple of stat() calls with no network and no writes, so it
// is safe on a render path. An unset comfy_root yields a zero Status.
func (s *Server) comfyExtStatus() comfyext.Status {
	return comfyext.Inspect(strings.TrimSpace(s.cfg.ComfyRoot))
}

// handleWorkflowOpenInComfyUI writes a UI-format workflow into ComfyUI's user
// workflow store (namespaced under civitai-manager/) and then does the best HONEST
// thing available:
//
//   - helper detected → ask it to jump any already-open editor tab to the saved
//     workflow, and offer a ?cm_open= deep link for when no tab is open;
//   - helper absent → say exactly where the workflow now lives (Workflows →
//     civitai-manager → <file>) with a copy button, and offer the one-click
//     helper install (which needs ONE ComfyUI restart).
//
// It never emits a ?workflow= link: no ComfyUI frontend has ever supported one.
//
// It reaches (and writes to) the ComfyUI server, so it is CSRF-protected +
// loopback-gated exactly like handleWorkflowRun. API-format graphs are refused
// (they do not load into the editor) — but the button is only shown for UI-format
// anyway.
func (s *Server) handleWorkflowOpenInComfyUI(w http.ResponseWriter, r *http.Request) {
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

	cid := openComfyContainerID(id)
	if wf.Format != store.WorkflowFormatUI {
		s.render(w, http.StatusOK, openComfyFailed(cid,
			"Only UI-format workflows can be opened in the ComfyUI editor (API graphs don’t load into it)."))
		return
	}
	client := s.comfy()
	if client == nil {
		s.render(w, http.StatusOK, openComfyFailed(cid, "ComfyUI is not configured (set comfy_url)."))
		return
	}

	filename := sanitizeWorkflowFilename(wf.Name, wf.ID)
	relPath := openComfyDir + "/" + filename
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	if err := client.SaveUserWorkflow(ctx, relPath, json.RawMessage(wf.Graph)); err != nil {
		s.render(w, http.StatusOK, openComfyFailed(cid,
			"Could not reach ComfyUI to save the workflow — is it running at "+s.cfg.ComfyURL+"?"))
		return
	}

	out := openComfyView{
		containerID: cid,
		extID:       extContainerID(id),
		workflowID:  id,
		relPath:     relPath,
		csrf:        s.csrf,
		comfyURL:    strings.TrimRight(s.cfg.ComfyURL, "/"),
		disk:        s.comfyExtStatus(),
		rootSet:     strings.TrimSpace(s.cfg.ComfyRoot) != "",
	}

	probe := s.comfyExtensionProbe(ctx, client)
	if probe.present {
		// Jump any already-open editor tab. A helper that vanished between the probe
		// and this call (a restart, an uninstall) invalidates the cache and falls
		// through to the honest fallback instead of pretending it worked.
		octx, ocancel := context.WithTimeout(ctx, extOpenTimeout)
		err := client.ExtensionOpen(octx, relPath)
		ocancel()
		switch {
		case err == nil:
			out.detected = true
			out.version = probe.version
			out.jumped = true
		case errors.Is(err, comfy.ErrExtensionAbsent):
			s.invalidateComfyExtensionProbe()
		default:
			// The helper is there but the broadcast failed; the deep link still works.
			s.log.Debug("comfy helper open broadcast failed", "err", err)
			out.detected = true
			out.version = probe.version
		}
	}
	s.render(w, http.StatusOK, openComfyResult(out))
}

// openComfyView is everything the result fragment needs. It is built entirely
// server-side; nothing in it is echoed back from the request.
type openComfyView struct {
	containerID string
	extID       string
	workflowID  int64
	// relPath is the saved workflow path relative to ComfyUI's workflows dir.
	relPath string
	csrf    string
	// comfyURL is the (slash-trimmed) configured ComfyUI base URL.
	comfyURL string
	// detected is true when the civitai-manager helper answered its ping route;
	// version is what it reported. jumped is true when the open broadcast landed.
	detected bool
	version  string
	jumped   bool
	// disk is the helper's on-disk install state under comfy_root; rootSet reports
	// whether a comfy_root is configured at all.
	disk    comfyext.Status
	rootSet bool
}

// openComfyFailed renders the amber, no-crash outcome (nothing was saved).
func openComfyFailed(containerID, msg string) g.Node {
	return h.Div(h.ID(containerID),
		h.P(h.Class("text-xs text-amber-400"), g.Text(msg)),
	)
}

// openComfyResult renders the outcome of a successful save. There are exactly two
// shapes and both are honest:
//
//   - detected: the helper opened (or can open) the REAL saved workflow, so a
//     ?cm_open= deep link is offered — it works because our helper implements it.
//   - absent: NO deep link at all. The exact Workflows-menu path with a copy
//     button, plus the one-click helper install and its restart caveat.
func openComfyResult(v openComfyView) g.Node {
	if v.detected {
		return h.Div(h.ID(v.containerID), h.Class("flex flex-col gap-2"),
			h.P(h.Class("text-xs cm-ok"), g.Text(openComfyJumpMessage(v.jumped))),
			openComfyPathLine(v.relPath),
			openComfyDeepLink(v),
			h.Div(h.ID(v.extID), openComfyInstalledNote(v)),
		)
	}
	return h.Div(h.ID(v.containerID), h.Class("flex flex-col gap-2"),
		h.P(h.Class("text-xs cm-ok"),
			g.Text("Saved into ComfyUI. Open it from the Workflows menu:")),
		openComfyPathLine(v.relPath),
		openComfyPlainLink(v),
		h.Div(h.ID(v.extID), openComfyInstallOffer(v)),
	)
}

func openComfyJumpMessage(jumped bool) string {
	if jumped {
		return "Saved into ComfyUI and asked any open ComfyUI tab to jump to it."
	}
	return "Saved into ComfyUI. The helper is installed but did not confirm the jump — use the link below."
}

// openComfyPathLine shows the EXACT menu path the workflow now lives at, with a
// self-contained copy button. This is the piece the old dead ?workflow= link was
// hiding: even with no helper at all, the user knows precisely where to click.
func openComfyPathLine(relPath string) g.Node {
	label := "Workflows → " + strings.ReplaceAll(relPath, "/", " → ")
	return h.Div(h.Class("flex flex-wrap items-center gap-2"),
		h.Code(h.Class("text-xs text-slate-300 break-all"), g.Text(label)),
		h.Button(
			h.Type("button"),
			dataAttr("copy", relPath),
			// Self-contained: no shared script needed inside an htmx-swapped fragment.
			g.Attr("onclick", "if(navigator.clipboard){navigator.clipboard.writeText(this.getAttribute('data-copy'));}this.textContent='Copied ✓';"),
			g.Attr("aria-label", "Copy the workflow path"),
			h.Class("text-xs text-indigo-400 hover:underline"),
			g.Text("Copy path"),
		),
	)
}

// openComfyDeepLink renders the ?cm_open= link — ONLY reachable when the helper
// was detected. The URL is scheme-validated before it becomes an href.
func openComfyDeepLink(v openComfyView) g.Node {
	openURL := v.comfyURL + "/?" + openComfyParam + "=" + url.QueryEscape(v.relPath)
	if !isSafeHTTPURL(openURL) {
		return h.P(h.Class("text-xs text-amber-400"),
			g.Text("Saved, but the configured ComfyUI URL is not a valid http(s) address to open."))
	}
	return h.A(
		h.Href(openURL),
		h.Target("_blank"),
		g.Attr("rel", "noopener"),
		h.Class("text-xs text-indigo-400 hover:underline"),
		g.Text("No ComfyUI tab open? Open it here ↗"),
	)
}

// openComfyPlainLink links the ComfyUI root with NO parameters. Without the helper
// there is no supported way to deep-link a workflow, so we do not pretend there is.
func openComfyPlainLink(v openComfyView) g.Node {
	if !isSafeHTTPURL(v.comfyURL) {
		return nil
	}
	return h.A(
		h.Href(v.comfyURL+"/"),
		h.Target("_blank"),
		g.Attr("rel", "noopener"),
		h.Class("text-xs text-indigo-400 hover:underline"),
		g.Text("Open ComfyUI ↗"),
	)
}

// openComfyInstallOffer is the honest fallback's call to action: explain what the
// helper is, that it needs one restart, and that it can be removed — or explain
// why the install is unavailable.
func openComfyInstallOffer(v openComfyView) g.Node {
	switch {
	case v.disk.Installed:
		// On disk but not answering: ComfyUI has not been restarted since.
		return h.Div(h.Class("flex flex-col gap-1"),
			h.P(h.Class("text-xs text-amber-400"),
				g.Text("The ComfyUI helper is installed but not active yet — restart ComfyUI once, then this button will open the workflow directly.")),
			openComfyRemovalNote(v),
		)
	case v.disk.Foreign:
		return h.P(h.Class("text-xs text-amber-400"),
			g.Text("A directory named “"+comfyext.DirName+"” already exists in your ComfyUI custom_nodes but was not created by civitai-manager, so it will not be touched. Move or delete it to install the helper."))
	case !v.rootSet:
		return h.P(h.Class("text-xs text-slate-500"),
			g.Text("Tip: set comfy_root (or comfy_model_path) to your ComfyUI install to enable a one-click helper that opens workflows directly."))
	default:
		return h.Div(h.Class("flex flex-col gap-1"),
			h.P(h.Class("text-xs text-slate-500"),
				g.Text("ComfyUI has no built-in “open this workflow” link. civitai-manager can install a small helper into your ComfyUI ("+v.disk.Dir+") so this button opens the workflow directly. It adds no nodes, and ComfyUI must be restarted once.")),
			openComfyInstallButton(v, "Install ComfyUI helper"),
		)
	}
}

// openComfyInstalledNote is the detected path's small footer: which helper version
// is running, an update offer when it is out of date, and a way to back it out.
func openComfyInstalledNote(v openComfyView) g.Node {
	nodes := []g.Node{
		h.P(h.Class("text-xs text-slate-500"),
			g.Text("ComfyUI helper "+openComfyVersionLabel(v.version)+" is active.")),
	}
	if v.disk.Installed && v.disk.Outdated && v.rootSet {
		nodes = append(nodes,
			h.P(h.Class("text-xs text-amber-400"),
				g.Text("A newer helper ("+comfyext.ExtensionVersion+") ships with this build — update it, then restart ComfyUI once.")),
			openComfyInstallButton(v, "Update ComfyUI helper"))
	}
	if v.disk.Installed {
		nodes = append(nodes, openComfyRemovalNote(v))
	}
	return h.Div(h.Class("flex flex-col gap-1"), g.Group(nodes))
}

func openComfyVersionLabel(version string) string {
	if strings.TrimSpace(version) == "" {
		return "(unknown version)"
	}
	return "v" + version
}

// openComfyRemovalNote pairs the uninstall button with what it ACTUALLY does:
// removing the helper deletes its whole directory, including anything that ended
// up inside it that civitai-manager did not write (ComfyUI's own __pycache__ is
// always there). That is the correct behaviour — a partially deleted custom node
// is worse than none — but the user should not have to guess it.
func openComfyRemovalNote(v openComfyView) g.Node {
	return h.Div(h.Class("flex flex-col gap-1"),
		openComfyUninstallButton(v, "Remove helper"),
		h.P(h.Class("text-xs text-slate-500"),
			g.Text("Removing deletes the whole "+v.disk.Dir+" directory, including files civitai-manager did not write (e.g. ComfyUI's __pycache__). Restart ComfyUI once afterwards.")),
	)
}

func openComfyInstallButton(v openComfyView, label string) g.Node {
	return civButton("outline", "sm", []g.Node{
		h.Type("button"),
		hx("post", "/comfy/extension/install"),
		hx("vals", extActionVals(v)),
		hx("target", "#"+v.extID),
		hx("swap", "innerHTML"),
		hx("disabled-elt", "this"),
		g.Attr("aria-label", "Install the civitai-manager helper into ComfyUI"),
	}, g.Text(label))
}

func openComfyUninstallButton(v openComfyView, label string) g.Node {
	return civButton("subtle", "sm", []g.Node{
		h.Type("button"),
		hx("post", "/comfy/extension/uninstall"),
		hx("vals", extActionVals(v)),
		hx("target", "#"+v.extID),
		hx("swap", "innerHTML"),
		hx("disabled-elt", "this"),
		g.Attr("aria-label", "Remove the civitai-manager helper from ComfyUI"),
	}, g.Text(label))
}

// extActionVals builds the hx-vals JSON for an install/uninstall click. workflow_id
// only ever names the container to swap; it is re-parsed as an int64 server-side,
// so it can never inject markup.
func extActionVals(v openComfyView) string {
	return fmt.Sprintf(`{"csrf_token":%q,"workflow_id":%q}`, v.csrf, strconv.FormatInt(v.workflowID, 10))
}

// handleComfyExtensionInstall writes the embedded ComfyUI helper into the user's
// ComfyUI install. It is an EXPLICIT user action (never automatic), it takes a
// filesystem path from configuration, and it writes outside the app's own data —
// so it is CSRF-protected AND loopback-gated, exactly like the scan/discover
// endpoints. comfyext refuses a non-ComfyUI root and never clobbers a directory it
// did not write.
func (s *Server) handleComfyExtensionInstall(w http.ResponseWriter, r *http.Request) {
	s.handleComfyExtensionAction(w, r, true)
}

// handleComfyExtensionUninstall removes the helper directory — but only one
// carrying our install marker. Same CSRF + loopback posture as the install.
func (s *Server) handleComfyExtensionUninstall(w http.ResponseWriter, r *http.Request) {
	s.handleComfyExtensionAction(w, r, false)
}

func (s *Server) handleComfyExtensionAction(w http.ResponseWriter, r *http.Request, install bool) {
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
	// workflow_id only names the container to swap. Parse it strictly and re-render
	// it from the parsed int64 so nothing from the request reaches the markup.
	wfID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("workflow_id")), 10, 64)
	if err != nil || wfID < 0 {
		http.Error(w, "bad workflow id", http.StatusBadRequest)
		return
	}
	cid := extContainerID(wfID)

	root := strings.TrimSpace(s.cfg.ComfyRoot)
	if root == "" {
		s.render(w, http.StatusOK, extActionResult(cid, false,
			"No ComfyUI install directory is configured. Set comfy_root (or comfy_model_path, whose parent is used when it looks like a ComfyUI install)."))
		return
	}

	if !install {
		err := comfyext.Uninstall(root)
		s.invalidateComfyExtensionProbe()
		switch {
		case err == nil:
			s.render(w, http.StatusOK, extActionResult(cid, true,
				"Removed the ComfyUI helper — the whole "+comfyext.Dir(root)+" directory is gone, including files civitai-manager did not write (e.g. ComfyUI's __pycache__). Restart ComfyUI once to finish removing it."))
		case errors.Is(err, comfyext.ErrNotInstalled):
			s.render(w, http.StatusOK, extActionResult(cid, true,
				"The ComfyUI helper is not installed — nothing to remove."))
		default:
			s.log.Warn("comfy helper uninstall failed", "err", err)
			s.render(w, http.StatusOK, extActionResult(cid, false, err.Error()))
		}
		return
	}

	st, err := comfyext.Install(root)
	s.invalidateComfyExtensionProbe()
	if err != nil {
		s.log.Warn("comfy helper install failed", "root", root, "err", err)
		s.render(w, http.StatusOK, extActionResult(cid, false, err.Error()))
		return
	}
	s.render(w, http.StatusOK, extActionResult(cid, true,
		"Installed the ComfyUI helper (v"+comfyext.ExtensionVersion+") into "+st.Dir+
			". RESTART ComfyUI once, then click “Open in ComfyUI” again — it will open the workflow directly."))
}

// extActionResult renders an install/uninstall outcome inside the nested helper
// container. The message may embed a filesystem path or an OS error string, so it
// is always emitted through g.Text (escaped).
func extActionResult(containerID string, ok bool, msg string) g.Node {
	cls := "text-xs text-amber-400"
	if ok {
		cls = "text-xs cm-ok"
	}
	return h.Div(h.ID(containerID), h.P(h.Class(cls), g.Text(msg)))
}

// workflowOpenComfyCard is the "Open in ComfyUI" affordance on the workflow detail
// page — SEPARATE from the "Run on ComfyUI" panel. It writes the UI graph into the
// editor's workflow store and then opens (or tells the user exactly how to open)
// it. Rendered ONLY for UI-format workflows with a configured comfy_url (the caller
// gates on both); the button POSTs the loopback-gated endpoint (CSRF via hx-vals)
// and swaps its own container.
//
// The card itself does NO helper probing: detection is a network round-trip and
// must not run on every page render, so it happens once per click (cached for
// extProbeTTL) and the outcome is rendered into the container below.
func workflowOpenComfyCard(id int64, csrf string) g.Node {
	cid := openComfyContainerID(id)
	ids := strconv.FormatInt(id, 10)
	return card(
		sectionTitle("Open in ComfyUI"),
		h.P(h.Class("text-sm text-slate-400 mb-3"),
			g.Text("Save this workflow into your ComfyUI editor (under the “"+openComfyDir+"” folder) and open it there.")),
		h.Div(
			h.ID(cid),
			civButton("outline", "md", []g.Node{
				h.Type("button"),
				hx("post", "/workflows/"+ids+"/open-in-comfyui"),
				hx("vals", fmt.Sprintf(`{"csrf_token":%q}`, csrf)),
				hx("target", "#"+cid),
				hx("swap", "innerHTML"),
				hx("disabled-elt", "this"),
				g.Attr("aria-label", "Open this workflow in the ComfyUI editor"),
			},
				g.Text("Open in ComfyUI "),
				h.Span(h.Class("cm-cta-icon"), g.Attr("aria-hidden", "true"), g.Text("↗")),
			),
		),
	)
}
