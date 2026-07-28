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
	g "maragu.dev/gomponents"
)

// workflowOutputsPreviewLimit caps how many recent generations the per-workflow
// section shows before the "View all →" link.
const workflowOutputsPreviewLimit = 12

// renderWorkflowOutputs builds the per-workflow "Recent outputs" card for the
// workflow detail page, or nil when the workflow has no generations (a query
// failure also yields nil — the section is a non-essential add-on and must never
// break the detail page).
func (s *Server) renderWorkflowOutputs(ctx context.Context, workflowID int64) g.Node {
	total, err := s.store.CountGenerations(ctx, &workflowID)
	if err != nil || total == 0 {
		return nil
	}
	gens, err := s.store.ListGenerations(ctx, store.ListGenerationsOpts{
		WorkflowID: &workflowID,
		Limit:      workflowOutputsPreviewLimit,
	})
	if err != nil || len(gens) == 0 {
		return nil
	}
	return workflowOutputsSection(workflowID, gens, total)
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
		page, total, s.csrf, s.currentTheme(), s.nsfwMode()))
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

	// Restrict the served content-type to images (the stored bytes come from an
	// untrusted comfy server); forbid sniffing. The file is immutable once written,
	// so a real long cache is safe (unlike the live /view proxy).
	ct := img.ContentType
	if !strings.HasPrefix(ct, "image/") {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
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
	s.render(w, http.StatusOK, generationDetailPage(gen, images, s.csrf, s.currentTheme(), s.nsfwMode()))
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

	// Reconstruct the applied overrides and re-run the CURRENT workflow. startRun is
	// idempotent (no-op while a run is already in flight).
	s.startRun(wf, runOptionsFromParams(gen.Params))

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
