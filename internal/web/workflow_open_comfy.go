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

	"github.com/ZacxDev/civitai-manager/internal/store"
	g "maragu.dev/gomponents"
	h "maragu.dev/gomponents/html"
)

// openComfyDir is the namespace all "Open in ComfyUI" workflows are written under
// in ComfyUI's user workflow store, so the tool's files stay grouped (and are
// never confused with the user's own saved workflows).
const openComfyDir = "civitai-manager"

// openComfyContainerID is the stable element the "Open in ComfyUI" button and its
// POST result share, so the action swaps its own container inline (mirrors
// workflowImportContainerID).
func openComfyContainerID(id int64) string {
	return fmt.Sprintf("wf-open-comfy-%d", id)
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

// handleWorkflowOpenInComfyUI writes a UI-format workflow into ComfyUI's user
// workflow store (namespaced under civitai-manager/) and returns a fragment that
// opens the ComfyUI editor at that workflow in a new tab. It reaches (and writes
// to) the ComfyUI server, so it is CSRF-protected + loopback-gated exactly like
// handleWorkflowRun. API-format graphs are refused (they do not load into the
// editor) — but the button is only shown for UI-format anyway.
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
		s.render(w, http.StatusOK, openComfyResult(cid, "",
			"Only UI-format workflows can be opened in the ComfyUI editor (API graphs don’t load into it).", false))
		return
	}
	client := s.comfy()
	if client == nil {
		s.render(w, http.StatusOK, openComfyResult(cid, "",
			"ComfyUI is not configured (set comfy_url).", false))
		return
	}

	filename := sanitizeWorkflowFilename(wf.Name, wf.ID)
	relPath := openComfyDir + "/" + filename
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	if err := client.SaveUserWorkflow(ctx, relPath, json.RawMessage(wf.Graph)); err != nil {
		s.render(w, http.StatusOK, openComfyResult(cid, "",
			"Could not reach ComfyUI to save the workflow — is it running at "+s.cfg.ComfyURL+"?", false))
		return
	}

	// Best-effort deep link into the editor at the saved workflow. The workflow is
	// ALSO now selectable from ComfyUI's Workflows menu (civitai-manager folder),
	// which is the reliable path if a given ComfyUI frontend does not honor the
	// ?workflow= param. The URL is scheme-validated before it becomes an href.
	openName := openComfyDir + "/" + strings.TrimSuffix(filename, ".json")
	openURL := strings.TrimRight(s.cfg.ComfyURL, "/") + "/?workflow=" + url.QueryEscape(openName)
	if !isSafeHTTPURL(openURL) {
		s.render(w, http.StatusOK, openComfyResult(cid, "",
			"Saved to ComfyUI, but its URL is not a valid http(s) address to open.", false))
		return
	}
	s.render(w, http.StatusOK, openComfyResult(cid, openURL,
		"Saved to ComfyUI under “"+openComfyDir+"/” — open it there from the Workflows menu.", true))
}

// openComfyResult renders the inline outcome that replaces the "Open in ComfyUI"
// button after the POST. On success it (a) attempts window.open within the click's
// transient user-activation window and (b) always renders a visible, scheme-
// validated fallback anchor (new tab, rel=noopener) so the user can open it even
// if the browser blocks the programmatic popup. On failure it shows an amber note
// (no crash). The open URL is JSON-encoded into the inline script so it cannot
// break out of the string / a <script> context.
func openComfyResult(containerID, openURL, msg string, ok bool) g.Node {
	if !ok {
		return h.Div(h.ID(containerID),
			h.P(h.Class("text-xs text-amber-400"), g.Text(msg)),
		)
	}
	// json.Marshal HTML-escapes <, >, & → safe to embed in a <script>.
	enc, _ := json.Marshal(openURL)
	openJS := "window.open(" + string(enc) + ",'_blank','noopener');"
	return h.Div(h.ID(containerID),
		h.Class("flex flex-col gap-1"),
		h.Script(g.Raw(openJS)),
		h.P(h.Class("text-xs cm-ok"), g.Text(msg)),
		h.A(
			h.Href(openURL),
			h.Target("_blank"),
			g.Attr("rel", "noopener"),
			h.Class("text-xs text-indigo-400 hover:underline"),
			g.Text("Open ComfyUI ↗"),
		),
	)
}

// workflowOpenComfyCard is the "Open in ComfyUI" affordance on the workflow detail
// page — SEPARATE from the "Run on ComfyUI" panel. It writes the UI graph into the
// editor's workflow store and opens it. Rendered ONLY for UI-format workflows with
// a configured comfy_url (the caller gates on both); the button POSTs the
// loopback-gated endpoint (CSRF via hx-vals) and swaps its own container.
func workflowOpenComfyCard(id int64, csrf string) g.Node {
	cid := openComfyContainerID(id)
	ids := strconv.FormatInt(id, 10)
	return card(
		sectionTitle("Open in ComfyUI"),
		h.P(h.Class("text-sm text-slate-400 mb-3"),
			g.Text("Save this workflow into your ComfyUI editor (under the “"+openComfyDir+"” folder) and open it in a new tab.")),
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
