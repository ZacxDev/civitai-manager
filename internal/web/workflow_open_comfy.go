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

// extProbeTimeout bounds a single probe leg (ping, then asset). A ComfyUI that is
// down or slow must not stall the click that triggered it.
const extProbeTimeout = 1500 * time.Millisecond

// extOpenTimeout bounds the jump-an-open-tab broadcast. It is deliberately tight
// and SEPARATE from the enclosing handler budget: the broadcast is a nice-to-have
// (the new tab opens the workflow regardless), so a wedged helper must not hold
// the user's click for the full save timeout.
const extOpenTimeout = 2 * time.Second

// comfyExtContainerID is the STABLE element the helper install/uninstall actions
// swap.
//
// It is a CONSTANT, not a per-workflow id. Helper management is a server-wide
// setting that lives in its own disclosure (see comfyHelperDisclosure), so the
// endpoints must be usable from any surface that renders it — they no longer take
// a workflow_id, and therefore nothing from the request reaches the markup at all.
const comfyExtContainerID = "cm-comfy-ext"

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

// comfyExtensionProbe reports whether this ComfyUI is running a USABLE
// civitai-manager helper, using a short-lived cache of BOTH outcomes.
//
// Detection has TWO legs and needs BOTH:
//
//  1. /civitai-manager/ping answers as our helper, and
//  2. the helper's FRONTEND script is actually being served.
//
// Leg 2 is not belt-and-braces, it is the fix for a live-caught bug. ComfyUI
// registers a custom node's python routes ONCE, at startup, and keeps the
// handlers in memory: after the helper directory is deleted, the ping keeps
// answering 200 with our exact body until ComfyUI restarts, while the static
// asset route (served from disk) 404s immediately. The frontend script is the
// half that does the work — ?cm_open= handling and the websocket listener — so
// with leg 1 alone the app cheerfully reported "asked the tab to jump to it"
// while literally nothing could happen. That is the exact failure a user hit.
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
		pingOK := false
		switch {
		case err == nil && info != nil:
			pingOK = true
			res.version = info.Version
		case errors.Is(err, comfy.ErrExtensionAbsent):
			// Expected: a stock ComfyUI. Cache the negative too.
		case err != nil:
			s.log.Debug("comfy helper probe failed", "err", err)
		}
		if pingOK {
			actx, acancel := context.WithTimeout(ctx, extProbeTimeout)
			aerr := client.ExtensionAsset(actx)
			acancel()
			if aerr == nil {
				res.usable = true
			} else {
				// Routes live, script gone: the zombie. Report it as NOT usable and
				// let the UI ask for the restart that actually fixes it.
				res.zombie = true
				s.log.Debug("comfy helper frontend script is not being served", "err", aerr)
			}
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

// comfyHelperView is the helper's ON-DISK state, as rendered by the management
// disclosure. It is a couple of stat() calls with no network and no writes, so it
// is safe on a render path (unlike comfyExtensionProbe, which must never run
// there). Its zero value means "no comfy_root configured".
type comfyHelperView struct {
	// disk is what Inspect found under comfy_root.
	disk comfyext.Status
	// rootSet reports whether a comfy_root is configured at all.
	rootSet bool
	// csrf powers the install/uninstall buttons.
	csrf string
}

// comfyHelperState builds the on-disk helper view for a render path.
func (s *Server) comfyHelperState() comfyHelperView {
	root := strings.TrimSpace(s.cfg.ComfyRoot)
	return comfyHelperView{disk: comfyext.Inspect(root), rootSet: root != "", csrf: s.csrf}
}

// handleWorkflowOpenInComfyUI is the "Open in ComfyUI" action. It is submitted by
// a real <form target="_blank">, so the click ALREADY opened a new tab by the time
// this runs — which is the whole point: the browser opened it synchronously from
// the user gesture, so no popup blocker is involved and no JS is needed.
//
// With a USABLE helper it therefore does the thing the user asked for and nothing
// else: save the workflow, broadcast the jump so an already-open editor tab
// follows along, and REDIRECT the new tab straight into
// <comfy_url>/?cm_open=<path>, where the helper opens the workflow. No
// intermediate "here is what happened, now click this other link" page.
//
// Without a usable helper it does NOT dump the user on a blank ComfyUI (there is
// no supported deep link — see openComfyParam). It renders the honest result
// instead: the exact Workflows-menu path, a copy button, and the one-click helper
// install.
//
// It reaches (and writes to) the ComfyUI server, so it is CSRF-protected +
// loopback-gated exactly like handleWorkflowRun. API-format graphs are refused
// (they do not load into the editor) — but the control is only shown for UI-format
// anyway.
func (s *Server) handleWorkflowOpenInComfyUI(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if !s.verifyCSRF(w, r) {
		return
	}
	// Same loopback gate as every other path-taking / local-ComfyUI-reaching
	// endpoint, worded identically — but rendered as a full page, because this
	// response lands in a freshly opened tab rather than an htmx swap target.
	if !s.extraPathsAllowed() {
		s.renderOpenComfyPage(w, openComfyNote("amber",
			"This control is disabled when the server is bound to a non-loopback address."), nil)
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

	backLink := openComfyBackLink(id)
	if wf.Format != store.WorkflowFormatUI {
		s.renderOpenComfyPage(w, openComfyNote("amber",
			"Only UI-format workflows can be opened in the ComfyUI editor (API graphs don’t load into it)."), backLink)
		return
	}
	client := s.comfy()
	if client == nil {
		s.renderOpenComfyPage(w, openComfyNote("amber",
			"ComfyUI is not configured (set comfy_url)."), backLink)
		return
	}

	filename := sanitizeWorkflowFilename(wf.Name, wf.ID)
	relPath := openComfyDir + "/" + filename
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	if err := client.SaveUserWorkflow(ctx, relPath, json.RawMessage(wf.Graph)); err != nil {
		s.renderOpenComfyPage(w, openComfyNote("amber",
			"Could not reach ComfyUI to save the workflow — is it running at "+s.cfg.ComfyURL+"?"), backLink)
		return
	}

	out := openComfyView{
		workflowID: id,
		relPath:    relPath,
		helper:     s.comfyHelperState(),
		comfyURL:   strings.TrimRight(s.cfg.ComfyURL, "/"),
	}

	probe := s.comfyExtensionProbe(ctx, client)
	out.version = probe.version
	out.zombie = probe.zombie
	if probe.usable {
		out.usable = true
		// Jump any ALREADY-OPEN editor tab as well, so the user does not end up
		// with the workflow only in the new tab. A helper that vanished between the
		// probe and this call invalidates the cache and falls through to the honest
		// fallback instead of redirecting into a ComfyUI that cannot open anything.
		octx, ocancel := context.WithTimeout(ctx, extOpenTimeout)
		err := client.ExtensionOpen(octx, relPath)
		ocancel()
		switch {
		case err == nil:
		case errors.Is(err, comfy.ErrExtensionAbsent):
			s.invalidateComfyExtensionProbe()
			out.usable = false
		default:
			// The helper is there but the broadcast failed; ?cm_open= still works.
			s.log.Debug("comfy helper open broadcast failed", "err", err)
		}
	}

	if out.usable {
		if openURL := openComfyDeepLinkURL(out); openURL != "" {
			// THE point of the whole feature: the tab the click opened becomes the
			// ComfyUI tab, with the workflow loading in it.
			http.Redirect(w, r, openURL, http.StatusSeeOther)
			return
		}
		// A helper we cannot build a valid URL for is not usable.
		out.usable = false
		out.badURL = true
	}
	s.renderOpenComfyPage(w, openComfyResult(out), backLink)
}

// openComfyView is everything the result page needs. It is built entirely
// server-side; nothing in it is echoed back from the request.
type openComfyView struct {
	workflowID int64
	// relPath is the saved workflow path relative to ComfyUI's workflows dir.
	relPath string
	// comfyURL is the (slash-trimmed) configured ComfyUI base URL.
	comfyURL string
	// usable is true when the helper answered BOTH detection legs.
	usable bool
	// zombie is true for the ping-answers-but-script-is-gone state, which is fixed
	// by restarting ComfyUI (and by nothing else).
	zombie bool
	// badURL is true when comfy_url is not a usable http(s) address to redirect to.
	badURL bool
	// version is the helper version the ping reported.
	version string
	// helper is the on-disk install state, for the install offer.
	helper comfyHelperView
}

// renderOpenComfyPage renders a standalone page. The "Open in ComfyUI" click is a
// form submit with target="_blank", so whatever this handler returns is a WHOLE
// new tab — a bare htmx fragment would land there unstyled and contextless.
func (s *Server) renderOpenComfyPage(w http.ResponseWriter, body, backLink g.Node) {
	nodes := []g.Node{pageTitle("Open in ComfyUI"), card(body)}
	if backLink != nil {
		nodes = append(nodes, backLink)
	}
	s.render(w, http.StatusOK, page("Open in ComfyUI", s.currentTheme(), s.csrf, s.nsfwMode(), railData{}, nodes...))
}

func openComfyBackLink(id int64) g.Node {
	return h.A(
		h.Href("/workflows/"+strconv.FormatInt(id, 10)),
		h.Class("text-sm text-indigo-400 hover:text-indigo-300"),
		g.Text("← Back to the workflow"),
	)
}

// openComfyNote renders a single-line outcome. tone is "amber" (nothing happened)
// or "ok".
func openComfyNote(tone, msg string) g.Node {
	cls := "text-xs cm-ok"
	if tone == "amber" {
		cls = "text-xs text-amber-400"
	}
	return h.P(h.Class(cls), g.Text(msg))
}

// openComfyResult renders the NOT-usable outcome. The workflow was saved, so the
// job is to say exactly where it landed and how to make the one-click path work —
// never to fabricate a deep link and never to dump the user in a blank ComfyUI.
//
// It deliberately carries NO uninstall control. A destructive "Remove helper"
// button sat here before, inline with the success text, and a user clicked it
// without knowing what it did — which disabled one-click open entirely. Helper
// management now lives in its own labelled disclosure (comfyHelperDisclosure).
func openComfyResult(v openComfyView) g.Node {
	nodes := []g.Node{
		openComfyNote("ok", "Saved into ComfyUI. Open it from the Workflows menu:"),
		openComfyPathLine(v.relPath),
	}
	if v.badURL {
		nodes = append(nodes, openComfyNote("amber",
			"The configured ComfyUI URL is not a valid http(s) address to open."))
	}
	if v.zombie {
		// The live-caught state: routes still registered from startup, script gone.
		nodes = append(nodes, openComfyNote("amber",
			"The ComfyUI helper’s routes are still answering, but its frontend script is no longer being served — so it cannot actually open anything. This is what ComfyUI looks like after the helper is removed or updated: restart ComfyUI once to settle it."))
	}
	nodes = append(nodes, openComfyPlainLink(v))
	nodes = append(nodes, h.Div(h.ID(comfyExtContainerID), openComfyInstallOffer(v)))
	return h.Div(h.Class("flex flex-col gap-2"), g.Group(nodes))
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

// openComfyDeepLinkURL builds the ?cm_open= URL the new tab is redirected to, or
// "" when the configured ComfyUI URL is not a safe http(s) address. It is reached
// ONLY when the helper answered both detection legs — the param works because our
// helper implements it, and for no other reason.
func openComfyDeepLinkURL(v openComfyView) string {
	openURL := v.comfyURL + "/?" + openComfyParam + "=" + url.QueryEscape(v.relPath)
	if !isSafeHTTPURL(openURL) {
		return ""
	}
	return openURL
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
// helper is, that it needs one restart, and offer the one-click install — or
// explain why the install is unavailable. It never offers an UNINSTALL.
func openComfyInstallOffer(v openComfyView) g.Node {
	hv := v.helper
	switch {
	case hv.disk.Installed && !v.zombie:
		// On disk but not answering at all: ComfyUI has not been restarted since.
		return openComfyNote("amber",
			"The ComfyUI helper is installed but not active yet — restart ComfyUI once, then this button will open the workflow directly.")
	case hv.disk.Installed:
		return nil // the zombie note above already says "restart ComfyUI".
	case hv.disk.Foreign:
		return openComfyNote("amber",
			"A directory named “"+comfyext.DirName+"” already exists in your ComfyUI custom_nodes but was not created by civitai-manager, so it will not be touched. Move or delete it to install the helper.")
	case !hv.rootSet:
		return h.P(h.Class("text-xs text-slate-500"),
			g.Text("Tip: set comfy_root (or comfy_model_path) to your ComfyUI install to enable a one-click helper that opens workflows directly."))
	default:
		return h.Div(h.Class("flex flex-col gap-1"),
			h.P(h.Class("text-xs text-slate-500"),
				g.Text("ComfyUI has no built-in “open this workflow” link. civitai-manager can install a small helper into your ComfyUI ("+hv.disk.Dir+") so this button opens the workflow directly. It adds no nodes, and ComfyUI must be restarted once.")),
			comfyExtInstallButton(hv, "Install ComfyUI helper"),
		)
	}
}

func openComfyVersionLabel(version string) string {
	if strings.TrimSpace(version) == "" {
		return "(unknown version)"
	}
	return "v" + version
}

// --- helper management (its own, deliberate surface) ---

// comfyHelperDisclosure is where install/remove live: a COLLAPSED <details>
// labelled "ComfyUI helper (advanced)", well away from the per-click result.
//
// It exists because the uninstall button used to sit inline in the success
// message, one pixel from "it worked", and a user clicked it without knowing it
// disabled one-click open. A destructive action that turns a feature off must be
// somewhere you go on purpose, and must say what it costs BEFORE you click it —
// this mirrors how the library page presents quarantine (explain the consequence
// next to the control, not after it).
//
// The status shown here is on-disk only (stat calls). It never probes the network:
// there is a test asserting zero probes on a render path.
func comfyHelperDisclosure(hv comfyHelperView) g.Node {
	return h.Details(
		h.Class("mt-4"),
		h.Summary(
			h.Class("cursor-pointer select-none text-sm text-slate-400 hover:text-slate-200"),
			g.Text("ComfyUI helper (advanced)"),
		),
		h.Div(h.Class("mt-3"),
			h.Div(h.ID(comfyExtContainerID), comfyHelperControls(hv)),
		),
	)
}

// comfyHelperControls renders the helper's current on-disk status plus the
// install/update and uninstall actions appropriate to it. It is also what the
// install/uninstall endpoints swap back in, so the surface always re-states the
// status after an action.
func comfyHelperControls(hv comfyHelperView) g.Node {
	nodes := []g.Node{comfyHelperStatusLine(hv)}
	switch {
	case hv.disk.Foreign:
		nodes = append(nodes, h.P(h.Class("text-xs text-amber-400"),
			g.Text("A directory named “"+comfyext.DirName+"” already exists in your ComfyUI custom_nodes but was not created by civitai-manager, so civitai-manager will neither overwrite nor remove it. Move or delete it yourself first.")))
	case !hv.rootSet:
		nodes = append(nodes, h.P(h.Class("text-xs text-slate-500"),
			g.Text("Set comfy_root (or comfy_model_path) to your ComfyUI install directory to manage the helper from here.")))
	case hv.disk.Installed:
		if hv.disk.Outdated {
			nodes = append(nodes,
				h.P(h.Class("text-xs text-amber-400"),
					g.Text("A newer helper ("+comfyext.ExtensionVersion+") ships with this build. Updating replaces the files in place; ComfyUI must be restarted once afterwards.")),
				comfyExtInstallButton(hv, "Update ComfyUI helper"))
		}
		nodes = append(nodes, comfyHelperUninstallBlock(hv))
	default:
		nodes = append(nodes,
			h.P(h.Class("text-xs text-slate-500"),
				g.Text("The helper adds no nodes. It adds a feature-detection route, a route that tells an already-open ComfyUI tab to jump to a workflow, and a small script that honours the ?"+openComfyParam+"= link. ComfyUI must be restarted once after installing.")),
			comfyExtInstallButton(hv, "Install ComfyUI helper"))
	}
	return h.Div(h.Class("flex flex-col gap-2"), g.Group(nodes))
}

func comfyHelperStatusLine(hv comfyHelperView) g.Node {
	var msg string
	switch {
	case !hv.rootSet:
		msg = "Status: no ComfyUI install directory configured."
	case hv.disk.Foreign:
		msg = "Status: not installed by civitai-manager (" + hv.disk.Dir + " exists but is not ours)."
	case hv.disk.Installed:
		msg = "Status: installed (" + openComfyVersionLabel(hv.disk.Version) + ") at " + hv.disk.Dir + "."
	default:
		msg = "Status: not installed. One-click “Open in ComfyUI” will save the workflow and tell you where it went, but cannot open it for you."
	}
	return h.P(h.Class("text-xs text-slate-300"), g.Text(msg))
}

// comfyHelperUninstallBlock states the CONSEQUENCE above the button, not after it.
func comfyHelperUninstallBlock(hv comfyHelperView) g.Node {
	return h.Div(h.Class("flex flex-col gap-1 border-t border-slate-800 pt-3"),
		h.P(h.Class("text-xs text-amber-400"),
			g.Text("Uninstall the ComfyUI helper — one-click open will stop working. “Open in ComfyUI” will still save the workflow into ComfyUI, but you will have to open it yourself from the Workflows menu.")),
		h.P(h.Class("text-xs text-slate-500"),
			g.Text("Removing deletes the whole "+hv.disk.Dir+" directory, including files civitai-manager did not write (e.g. ComfyUI’s __pycache__). ComfyUI must be restarted once afterwards to unregister its routes.")),
		h.Div(comfyExtUninstallButton(hv, "Uninstall ComfyUI helper")),
	)
}

func comfyExtInstallButton(hv comfyHelperView, label string) g.Node {
	return civButton("outline", "sm", []g.Node{
		h.Type("button"),
		hx("post", "/comfy/extension/install"),
		hx("vals", comfyExtActionVals(hv)),
		hx("target", "#"+comfyExtContainerID),
		hx("swap", "innerHTML"),
		hx("disabled-elt", "this"),
		g.Attr("aria-label", "Install the civitai-manager helper into ComfyUI"),
	}, g.Text(label))
}

func comfyExtUninstallButton(hv comfyHelperView, label string) g.Node {
	return civButton("subtle", "sm", []g.Node{
		h.Type("button"),
		hx("post", "/comfy/extension/uninstall"),
		hx("vals", comfyExtActionVals(hv)),
		hx("target", "#"+comfyExtContainerID),
		hx("swap", "innerHTML"),
		hx("disabled-elt", "this"),
		hx("confirm", "Uninstall the ComfyUI helper? One-click “Open in ComfyUI” will stop working until you install it again."),
		g.Attr("aria-label", "Uninstall the civitai-manager helper from ComfyUI"),
	}, g.Text(label))
}

// comfyExtActionVals builds the hx-vals JSON for an install/uninstall click. It
// carries the CSRF token and NOTHING else — the container these actions swap is a
// constant, so no request value is ever reflected into the response markup.
func comfyExtActionVals(hv comfyHelperView) string {
	return fmt.Sprintf(`{"csrf_token":%q}`, hv.csrf)
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
	// These endpoints take NO request-supplied values beyond the CSRF token: the
	// helper is a single server-wide install and the container they swap is a
	// constant, so there is nothing to validate and nothing to reflect.
	root := strings.TrimSpace(s.cfg.ComfyRoot)
	if root == "" {
		s.renderComfyExtResult(w, false,
			"No ComfyUI install directory is configured. Set comfy_root (or comfy_model_path, whose parent is used when it looks like a ComfyUI install).")
		return
	}

	if !install {
		err := comfyext.Uninstall(root)
		s.invalidateComfyExtensionProbe()
		switch {
		case err == nil:
			// Be honest about the zombie: ComfyUI registered the helper's routes at
			// startup and still holds them, so /civitai-manager/ping keeps answering
			// until a restart. civitai-manager already reports the helper as NOT
			// installed (detection also requires the frontend script, which is gone
			// with the directory), but ComfyUI itself is not fully clean yet.
			s.renderComfyExtResult(w, true,
				"Removed the ComfyUI helper — the whole "+comfyext.Dir(root)+" directory is gone, including files civitai-manager did not write (e.g. ComfyUI’s __pycache__). One-click open is off from now on. RESTART ComfyUI once to finish removing it: its helper routes were registered at startup and stay live in memory until then.")
		case errors.Is(err, comfyext.ErrNotInstalled):
			s.renderComfyExtResult(w, true, "The ComfyUI helper is not installed — nothing to remove.")
		default:
			s.log.Warn("comfy helper uninstall failed", "err", err)
			s.renderComfyExtResult(w, false, err.Error())
		}
		return
	}

	st, err := comfyext.Install(root)
	s.invalidateComfyExtensionProbe()
	if err != nil {
		s.log.Warn("comfy helper install failed", "root", root, "err", err)
		s.renderComfyExtResult(w, false, err.Error())
		return
	}
	s.renderComfyExtResult(w, true,
		"Installed the ComfyUI helper (v"+comfyext.ExtensionVersion+") into "+st.Dir+
			". RESTART ComfyUI once, then “Open in ComfyUI” will open the workflow directly.")
}

// renderComfyExtResult renders an install/uninstall outcome INSIDE the helper
// container, followed by the freshly re-read management controls — so the surface
// always shows the new status without a page reload. The message may embed a
// filesystem path or an OS error string, so it is always emitted through g.Text
// (escaped).
func (s *Server) renderComfyExtResult(w http.ResponseWriter, ok bool, msg string) {
	cls := "text-xs text-amber-400"
	if ok {
		cls = "text-xs cm-ok"
	}
	s.render(w, http.StatusOK, h.Div(h.ID(comfyExtContainerID), h.Class("flex flex-col gap-2"),
		h.P(h.Class(cls), g.Text(msg)),
		comfyHelperControls(s.comfyHelperState()),
	))
}

// (workflowOpenComfyCard lived here — the standalone "Open in ComfyUI" CARD on the
// workflow detail page. PR C1 merged it into the ONE "Generate" section
// (generateSection in run_pages.go), where the same openInComfyForm control is the
// SECONDARY action beside the primary "Generate" CTA, and comfyHelperDisclosure is
// rendered directly by that section. The card is gone; the control and every rule
// governing it are unchanged.)

// openInComfyForm is THE "Open in ComfyUI" control, shared by the Generate section
// and by the run-failure report.
//
// 🔴 It is a real <form method="post" target="_blank">, NOT an htmx button. A form
// submit opens the new tab SYNCHRONOUSLY from the click itself, so the browser
// never treats it as a popup and the handler can 303 that tab straight into
// <comfy_url>/?cm_open=<path>. An htmx POST could only respond with markup — which
// is how this once shipped as "we saved it, now click this OTHER link" instead of
// just opening the workflow. Callers must NOT re-implement it as an htmx button.
//
// It does NO helper probing: detection is a network round-trip and must not run on
// every page render. It happens once per click (cached for extProbeTTL).
//
// It carries a CSRF token like every other POST here, and it deliberately does no
// probing: whether the helper is usable is decided once per click, inside the
// handler.
func openInComfyForm(ids, csrf, variant, size string) g.Node {
	return h.Form(
		h.Method("post"),
		h.Action("/workflows/"+ids+"/open-in-comfyui"),
		// The new tab IS the ComfyUI tab: the handler redirects it to
		// ?cm_open=<path> when the helper is usable.
		h.Target("_blank"),
		g.Attr("rel", "noopener"),
		csrfInput(csrf),
		civButton(variant, size, []g.Node{
			h.Type("submit"),
			g.Attr("aria-label", "Open this workflow in the ComfyUI editor"),
		},
			g.Text("Open in ComfyUI "),
			h.Span(h.Class("cm-cta-icon"), g.Attr("aria-hidden", "true"), g.Text("↗")),
		),
	)
}
