package web

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ZacxDev/civitai-manager/internal/store"
)

// rail loads the per-request state of the global "Recent outputs" sidebar: ONE
// bounded newest-first query (railFetchLimit rows) plus the persisted collapse
// flag. It runs on every full-page render, so it must stay cheap and must NEVER
// fail a page: a store error degrades to the zero value, which renders no rail.
//
// The rows are then collapsed into at most outputsRailLimit GROUPS (one per batch)
// in memory — no second query, no per-group lookup. Over-fetching is what keeps
// the rail full: see railFetchLimit for why grouping over exactly outputsRailLimit
// rows would under-fill it.
func (s *Server) rail(ctx context.Context) railData {
	gens, err := s.store.ListRecentGenerations(ctx, railFetchLimit)
	if err != nil {
		return railData{}
	}
	groups := groupRailGenerations(gens, outputsRailLimit)
	// The activity widget reuses the generation rows already in hand and adds ONE
	// bounded read. store.ListQueue is deliberately not consulted — it takes no
	// limit and would put a whole-table scan on every page render (see
	// rail_activity.go); download activity reaches the feed through the events the
	// queue worker already writes.
	evs, err := s.store.RecentEvents(railActivityFetchLimit)
	if err != nil {
		// A failed event read degrades to an outputs-only rail rather than dropping
		// the whole sidebar — the outputs widget's heading is the app's only in-app
		// link to /outputs.
		evs = nil
	}
	activity := buildRailActivity(groups, evs, railActivityLimit)
	if len(groups) == 0 && len(activity) == 0 {
		return railData{}
	}
	return railData{Groups: groups, Activity: activity, Collapsed: s.railCollapsed()}
}

// handleRailActivityFragment serves the activity widget's poll target.
//
// 🔴 IT RETURNS THE WIDGET'S INNER CONTENT ONLY — the same railActivityList the
// first paint renders — because the client swaps it with hx-swap="innerHTML" into
// a STABLE container. Returning the container itself would make the poller
// replace the very node carrying its own hx-trigger, and the loop would stop after
// one tick, silently. See railActivityBodyID.
func (s *Server) handleRailActivityFragment(w http.ResponseWriter, r *http.Request) {
	s.render(w, http.StatusOK, railActivityList(s.rail(r.Context()).Activity))
}

// railCollapsed reads the persisted rail collapse state (default expanded).
func (s *Server) railCollapsed() bool {
	v, err := s.store.GetSettingDefault(outputsRailSettingKey, "false")
	if err != nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(v), "true")
}

// handleSetOutputsRail persists the outputs rail's collapsed state and asks htmx
// to refresh so the shell re-renders at the new width. This mirrors the theme and
// NSFW toggles exactly (CSRF-protected POST → settings store → HX-Refresh); the
// state is server-side, never localStorage.
func (s *Server) handleSetOutputsRail(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if !s.verifyCSRF(w, r) {
		return
	}
	collapsed := "false"
	if strings.EqualFold(strings.TrimSpace(r.FormValue("collapsed")), "true") {
		collapsed = "true"
	}
	if err := s.store.SetSetting(outputsRailSettingKey, collapsed); err != nil {
		s.renderError(w, "save outputs rail setting", err)
		return
	}
	w.Header().Set("HX-Refresh", "true")
	w.WriteHeader(http.StatusNoContent)
}

// handleOutputs renders the global /outputs gallery: a paginated masonry grid,
// newest-first, with an optional ?workflow=<id> filter. GET, read-only (no CSRF).
// Outputs render PLAIN (the user's own local generations, no rating signal).
func (s *Server) handleOutputs(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Optional workflow filter.
	var workflowID *int64
	selectedWorkflow := ""
	if raw := strings.TrimSpace(r.URL.Query().Get("workflow")); raw != "" {
		if id, err := strconv.ParseInt(raw, 10, 64); err == nil {
			workflowID = &id
			selectedWorkflow = raw
		}
	}

	// Page (0-based internally; ?page= is also 0-based).
	page := 0
	if raw := strings.TrimSpace(r.URL.Query().Get("page")); raw != "" {
		if p, err := strconv.Atoi(raw); err == nil && p > 0 {
			page = p
		}
	}

	total, err := s.store.CountGenerations(ctx, workflowID)
	if err != nil {
		s.renderError(w, "count generations", err)
		return
	}
	gens, err := s.store.ListGenerations(ctx, store.ListGenerationsOpts{
		WorkflowID: workflowID,
		Limit:      outputsPageSize,
		Offset:     page * outputsPageSize,
	})
	if err != nil {
		s.renderError(w, "list generations", err)
		return
	}
	refs, err := s.store.ListGenerationWorkflowRefs(ctx)
	if err != nil {
		s.renderError(w, "list workflow refs", err)
		return
	}

	s.render(w, http.StatusOK, outputsGalleryPage(gens, refs, selectedWorkflow,
		page, total, s.csrf, s.maturity(), s.rail(ctx)))
}

// handleOutputsBatch renders ONE batch's generations (GET /outputs/batch/{id}).
//
// {id} is UNTRUSTED URL PATH INPUT, so it is validated against store.ValidBatchID
// (a bare [A-Za-z0-9_-]{1,64}) before it can reach a query — a hostile or stale id
// must be a missing page, never a 500 and never a bound parameter of unbounded
// shape. A well-formed but UNKNOWN id selects zero rows, which is likewise a 404:
// an empty batch page would tell the user a batch exists when it does not.
//
// GET + read-only → no CSRF. Not loopback-gated, for handleOutputsImage's reason:
// it reads app-owned local data (its own generations table), not an arbitrary
// filesystem path and not a comfy-reaching proxy, so it works on a LAN bind like
// every other browse page.
func (s *Server) handleOutputsBatch(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	if !store.ValidBatchID(id) {
		http.NotFound(w, r)
		return
	}
	gens, err := s.store.ListGenerationsByBatch(ctx, id)
	if err != nil {
		s.renderError(w, "list batch generations", err)
		return
	}
	if len(gens) == 0 {
		http.NotFound(w, r)
		return
	}
	s.render(w, http.StatusOK, batchGalleryPage(gens, s.csrf, s.maturity(), s.rail(ctx)))
}

// handleOutputsImage serves one app-owned output image by id, from disk,
// path-contained. GET, read-only app data → no CSRF. NOT loopback-gated (it serves
// local app files, not a comfy-reaching proxy — works on a LAN bind like every
// other page), but strictly id-indexed + path-contained + content-type-restricted.
func (s *Server) handleOutputsImage(w http.ResponseWriter, r *http.Request) {
	imageID, err := strconv.ParseInt(r.PathValue("imageID"), 10, 64)
	if err != nil {
		http.Error(w, "bad image id", http.StatusBadRequest)
		return
	}
	img, err := s.store.GetGenerationImage(r.Context(), imageID)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.renderError(w, "load image", err)
		return
	}
	root := strings.TrimSpace(s.cfg.OutputsDir)
	if root == "" {
		http.NotFound(w, r)
		return
	}
	// Defense in depth: even though rel_path comes from the DB, resolve it through
	// the containment check so a corrupted row can never read outside the root.
	dest, err := safeOutputPath(root, img.RelPath)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(dest)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || fi.IsDir() {
		http.NotFound(w, r)
		return
	}

	// Restrict the served content-type to the WHITELIST (images + the video types a
	// browser can actually play); forbid sniffing. The stored bytes came from an
	// untrusted comfy server and the stored content_type is re-derived here rather
	// than trusted, so a corrupted or older row can never widen what this origin
	// serves. Anything outside the whitelist is served as application/octet-stream —
	// still downloadable, just not renderable. The file is immutable once written,
	// so a real long cache is safe (unlike the live /view proxy).
	w.Header().Set("Content-Type", servableOutputContentType(img.ContentType))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	// ServeContent (not io.Copy) is load-bearing for VIDEO: it implements HTTP Range.
	// A ComfyUI/VHS mp4 is NOT written faststart — the moov atom sits at the END of
	// the file (measured on a real 471 KB capture: mdat at offset 40, moov at
	// 464497) — so a <video preload="metadata"> must issue a tail Range request to
	// find the metadata at all. Without Range support the browser would download the
	// whole file just to render a poster frame, and seeking would not work.
	http.ServeContent(w, r, "", fi.ModTime(), f)
}

// handleGenerationDetail renders one generation's detail page (full images +
// params + actions). GET.
func (s *Server) handleGenerationDetail(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad generation id", http.StatusBadRequest)
		return
	}
	gen, images, err := s.store.GetGeneration(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.renderError(w, "load generation", err)
		return
	}
	// The SAME resolver the workflow detail page builds — the provenance card renders
	// the run's resources through the shared .cm-res-chip component, which needs it to
	// resolve a basename to a local file and its CivitAI/HuggingFace source link.
	s.render(w, http.StatusOK, generationDetailPage(gen, images, s.csrf,
		s.maturity(), s.workflowResolver(), s.rail(r.Context())))
}

// handleGenerationRerun re-runs the CURRENT stored workflow with the generation's
// snapshotted params. CSRF-protected + loopback-gated (it reaches ComfyUI). It is
// disabled (404) when the source workflow was deleted (workflow_id NULL).
func (s *Server) handleGenerationRerun(w http.ResponseWriter, r *http.Request) {
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
		http.Error(w, "bad generation id", http.StatusBadRequest)
		return
	}
	gen, _, err := s.store.GetGeneration(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.renderError(w, "load generation", err)
		return
	}
	if gen.WorkflowID == nil {
		// The source workflow was deleted — re-run is not available.
		http.Error(w, "the source workflow was deleted; this generation can no longer be re-run", http.StatusNotFound)
		return
	}
	wf, err := s.store.GetWorkflow(r.Context(), *gen.WorkflowID)
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "the source workflow no longer exists", http.StatusNotFound)
		return
	}
	if err != nil {
		s.renderError(w, "load workflow", err)
		return
	}

	// Reconstruct the applied overrides and re-run the CURRENT workflow. startRunWithMessage is
	// idempotent (no-op while a run is already in flight). The positional (widget
	// index) overrides are only reconstructed when the workflow's graph still hashes
	// the same as when this generation was recorded — otherwise they would land on
	// whatever widget now occupies that position, so refuse rather than re-run with
	// silently different parameters.
	opts, stale := runOptionsFromParams(gen.Params, gen.GraphHash, wf.GraphHash)
	if stale != "" {
		http.Error(w, stale, http.StatusConflict)
		return
	}
	// A refusal must not redirect as if it worked. This path answers with markup
	// nowhere — it 303s — so the only honest answer is the same 409 shape the stale
	// check above already uses, rather than sending the user to a panel showing a
	// DIFFERENT run and letting them conclude their re-run started.
	if started, ref := s.startBatch(wf, opts, batchSpec{Count: 1, Message: "Starting run…"}); !started {
		http.Error(w, ref.notice(), http.StatusConflict)
		return
	}

	// Send the user to the workflow's run panel (which polls the live run status).
	http.Redirect(w, r, "/workflows/"+strconv.FormatInt(*gen.WorkflowID, 10), http.StatusSeeOther)
}

// handleGenerationDelete removes a generation's DB rows and its on-disk files.
// CSRF-protected + loopback-gated. Rows are deleted first (returning the file
// paths), then the files are unlinked — so a failed unlink leaves a benign orphan
// FILE, never an orphan row that 404s on serve.
func (s *Server) handleGenerationDelete(w http.ResponseWriter, r *http.Request) {
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
		http.Error(w, "bad generation id", http.StatusBadRequest)
		return
	}
	relPaths, err := s.store.DeleteGeneration(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.renderError(w, "delete generation", err)
		return
	}
	s.removeOutputFiles(relPaths)
	http.Redirect(w, r, "/outputs", http.StatusSeeOther)
}

// removeOutputFiles best-effort unlinks the given root-relative output files and
// prunes their now-empty prompt-id directories. Every step is path-contained and
// error-swallowing (a failed unlink is a benign leak, never a request failure).
//
// It is the SHARED file-removal helper for every path that drops generations: the
// delete handler above and the disk-cap eviction in outputs_capture.go. Every
// unlink routes through safeOutputPath — path containment is a hard invariant, so
// do not open-code an os.Remove on a rel_path anywhere else.
func (s *Server) removeOutputFiles(relPaths []string) {
	root := strings.TrimSpace(s.cfg.OutputsDir)
	if root == "" {
		return
	}
	dirs := map[string]struct{}{}
	for _, rel := range relPaths {
		dest, err := safeOutputPath(root, rel)
		if err != nil {
			s.log.Warn("output delete: unsafe rel_path skipped", "rel", rel, "err", err)
			continue
		}
		if err := os.Remove(dest); err != nil && !os.IsNotExist(err) {
			s.log.Warn("output delete: unlink failed", "path", dest, "err", err)
		}
		dirs[filepath.Dir(dest)] = struct{}{}
	}
	// Prune now-empty prompt-id dirs (only when empty; ignore errors).
	for dir := range dirs {
		if dir == root {
			continue
		}
		_ = os.Remove(dir) // removes only if empty
	}
}
