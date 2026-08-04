package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ZacxDev/civitai-manager/internal/comfy"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// maxWorkflowUpload bounds a PNG upload's total request body. ComfyUI metadata
// lives in early tEXt chunks but the carrier image can be sizeable; 64 MiB is a
// generous ceiling that still refuses a resource-exhaustion payload. It mirrors
// comfy.maxPNGBytes.
const maxWorkflowUpload = 64 << 20

// handleWorkflows redirects the legacy standalone /workflows page to the Workflows
// Library tab (the workflow UI moved into /library). Any ?flash=/&level= query is
// carried through so a POST-redirect-GET that still targets /workflows lands on the
// tab with its flash intact.
func (s *Server) handleWorkflows(w http.ResponseWriter, r *http.Request) {
	target := "/library?tab=workflows"
	if q := r.URL.RawQuery; q != "" {
		target += "&" + q
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// handleWorkflowDetail renders one workflow with its pretty-printed (escaped)
// graph and attachment controls.
func (s *Server) handleWorkflowDetail(w http.ResponseWriter, r *http.Request) {
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
	// This workflow's own recent outputs (per-workflow provenance — the global rail is
	// CROSS-workflow and does not answer "what has THIS one made"). ONE bounded read
	// through the existing per-workflow filter; a store error degrades to no strip
	// rather than failing the page, exactly like the rail.
	recent, err := s.store.ListGenerations(r.Context(), store.ListGenerationsOpts{
		WorkflowID: &id, Limit: workflowOutputsStripLimit,
	})
	if err != nil {
		s.log.Warn("workflow detail: list recent outputs failed", "workflow_id", id, "err", err)
		recent = nil
	}
	//
	// generateSection is the ONE run surface: the local-ComfyUI CTA, the "Open in
	// ComfyUI" editor hand-off, the parameters/preset panel, the run status, and the
	// CivitAI-cloud sub-block. comfyHelperState is stat-only — no network probe ever
	// runs on this render path.
	comfyConfigured := strings.TrimSpace(s.cfg.ComfyURL) != ""
	generate := generateSection(wf, s.runJobState(), s.csrf, s.extraPathsAllowed(),
		s.comfyDownloadEligible(), s.maturity(),
		s.buildPresetView(r.Context(), wf, 0, nil, true),
		comfyConfigured, s.comfyHelperState())
	s.render(w, http.StatusOK, workflowDetailPage(wf,
		s.csrf, s.maturity(), generate, recent, s.workflowResolver(),
		s.rail(r.Context())))
}

// handleWorkflowImport ingests a pasted API/UI graph. CSRF-protected and
// loopback-gated (it accepts arbitrary user-supplied content). It detects the
// format, extracts referenced resources for api graphs, stores the workflow, and
// redirects back to the library with a flash.
func (s *Server) handleWorkflowImport(w http.ResponseWriter, r *http.Request) {
	// Bound the pasted-graph body (net/http caps urlencoded forms at 10 MiB, but be
	// explicit) so a hostile paste can't buffer an oversized graph before parsing.
	r.Body = http.MaxBytesReader(w, r.Body, maxWorkflowUpload)
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

	raw := strings.TrimSpace(r.FormValue("graph"))
	if raw == "" {
		s.redirectWorkflows(w, r, "No workflow JSON provided.", "error")
		return
	}
	if !json.Valid([]byte(raw)) {
		s.redirectWorkflows(w, r, "That does not parse as JSON.", "error")
		return
	}
	format, err := comfy.DetectFormat([]byte(raw))
	if err != nil {
		s.redirectWorkflows(w, r,
			"Unrecognized workflow format (expected an API or UI ComfyUI graph).", "error")
		return
	}

	wf := &store.Workflow{
		Name:   strings.TrimSpace(r.FormValue("name")),
		Format: format,
		Graph:  raw,
		Source: store.WorkflowSourceImported,
	}
	if format == comfy.FormatAPI {
		if res, rerr := comfy.ExtractResources([]byte(raw)); rerr == nil {
			wf.Resources = res
		}
	}
	if _, err := s.store.InsertWorkflow(r.Context(), wf); err != nil {
		s.renderError(w, "store workflow", err)
		return
	}
	s.redirectWorkflows(w, r, "Imported "+format+" workflow.", "success")
}

// handleWorkflowImportPNG extracts a workflow from an uploaded ComfyUI PNG.
// CSRF-protected and loopback-gated. It prefers the api-format `prompt` graph;
// falling back to the ui-format `workflow` graph when only that is present.
func (s *Server) handleWorkflowImportPNG(w http.ResponseWriter, r *http.Request) {
	// Bound the total request body before parsing so an oversized upload cannot
	// exhaust memory/disk.
	r.Body = http.MaxBytesReader(w, r.Body, maxWorkflowUpload+(1<<20))
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		http.Error(w, "bad or oversized upload", http.StatusBadRequest)
		return
	}
	// ParseMultipartForm spills parts over its memory limit to temp files that
	// Go's http server never cleans up on its own; remove them when we're done
	// (also covers the early CSRF/gate/error returns below).
	defer func() {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
	}()
	if !s.verifyCSRF(w, r) {
		return
	}
	if !s.gate(w) {
		return
	}

	file, header, err := r.FormFile("png")
	if err != nil {
		s.redirectWorkflows(w, r, "No PNG file uploaded.", "error")
		return
	}
	defer file.Close()

	ex, err := comfy.ExtractFromPNG(file)
	switch {
	case errors.Is(err, comfy.ErrA1111Only):
		s.redirectWorkflows(w, r,
			"That PNG carries A1111 parameters, not a ComfyUI workflow.", "error")
		return
	case errors.Is(err, comfy.ErrNoWorkflow):
		s.redirectWorkflows(w, r,
			"No ComfyUI workflow metadata found in that PNG.", "error")
		return
	case errors.Is(err, comfy.ErrInvalidPNG):
		s.redirectWorkflows(w, r, "That file is not a valid PNG.", "error")
		return
	case err != nil:
		s.redirectWorkflows(w, r, "Could not read the PNG: "+err.Error(), "error")
		return
	}

	name := defaultWorkflowName(header.Filename)

	// 🔴 THE CHUNK KEYWORD IS A HINT, NEVER THE ANSWER. ComfyUI writes the api graph
	// under `prompt` and the editor graph under `workflow`, but the only check
	// comfy.ExtractFromPNG applies is looksLikeJSON — a first-byte pre-filter, not a
	// parse — so a truncated, wrapped or UI-shaped `prompt` chunk used to be stored
	// VERBATIM as format=api. This was the ONE import path that skipped
	// classification: handleWorkflowImport (above), importOneArchive
	// (discover_workflow_import.go) and the library workflow scan all run
	// comfy.DetectFormat and act on what it says.
	//
	// Both chunks now go through DetectFormat, in the same api-then-ui preference
	// order as before, and the DETECTED format is what gets stored. Trying the second
	// chunk when the first does not classify is deliberately MORE permissive than the
	// old code: a PNG whose `prompt` chunk is truncated but whose `workflow` chunk is
	// intact used to yield an unusable api-labelled row and silently drop the good
	// graph; it now imports the ui graph.
	var (
		graph  string
		format string
	)
	for _, cand := range []json.RawMessage{ex.APIGraph, ex.UIGraph} {
		if cand == nil {
			continue
		}
		f, derr := comfy.DetectFormat(cand)
		if derr != nil {
			continue // not a graph we can classify — fall through to the other chunk.
		}
		graph, format = string(cand), f
		break
	}
	// Nothing in the PNG classified. REFUSING is the fail-CLOSED answer and it costs
	// the user no capability: DetectFormat's reject set is exactly the set this app
	// cannot do anything with (the run gate refuses it, readiness reports "unknown",
	// ExtractResourcesAny/PrimaryCheckpoint answer nothing). Storing it under an
	// invented third format string would only move the mislabelling one name over —
	// `format` is a free TEXT column and every downstream switch treats an
	// unrecognised value as the non-ui branch, i.e. as api. A refusal the user reads
	// beats a row they believe imported.
	if format == "" {
		s.redirectWorkflows(w, r,
			"That PNG's ComfyUI metadata is not a workflow graph this app can read "+
				"(it may be truncated or from an unsupported version).", "error")
		return
	}
	// Keyed off the DETECTED format, so a ui graph found under the `prompt` keyword is
	// scanned as a ui graph instead of yielding nothing — the same call the archive
	// import and the library scan make.
	res, _ := comfy.ExtractResourcesAny(format, json.RawMessage(graph))

	wf := &store.Workflow{
		Name:      name,
		Format:    format,
		Graph:     graph,
		Source:    store.WorkflowSourceExtractedPNG,
		Resources: res,
	}
	if _, err := s.store.InsertWorkflow(r.Context(), wf); err != nil {
		s.renderError(w, "store workflow", err)
		return
	}
	s.redirectWorkflows(w, r, "Extracted "+format+" workflow from "+name+".", "success")
}

// handleWorkflowDelete removes a workflow. CSRF-protected.
func (s *Server) handleWorkflowDelete(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if !s.verifyCSRF(w, r) {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad workflow id", http.StatusBadRequest)
		return
	}
	if err := s.store.DeleteWorkflow(r.Context(), id); err != nil && !errors.Is(err, store.ErrNotFound) {
		s.renderError(w, "delete workflow", err)
		return
	}
	s.redirectWorkflows(w, r, "Workflow deleted.", "success")
}

// handleWorkflowAttach sets or clears a workflow's civitai model/version linkage.
// CSRF-protected. Blank ids detach (which also clears golden).
func (s *Server) handleWorkflowAttach(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if !s.verifyCSRF(w, r) {
		return
	}
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		http.Error(w, "bad workflow id", http.StatusBadRequest)
		return
	}
	modelID, merr := parseOptionalInt(r.FormValue("model_id"))
	versionID, verr := parseOptionalInt(r.FormValue("version_id"))
	if merr != nil || verr != nil {
		s.redirectWorkflowDetail(w, r, id, "Model and version ids must be numbers.", "error")
		return
	}
	if err := s.store.AttachWorkflow(r.Context(), id, modelID, versionID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		s.renderError(w, "attach workflow", err)
		return
	}
	s.redirectWorkflowDetail(w, r, id, "Attachment updated.", "success")
}

// handleWorkflowGolden toggles a workflow's golden flag. CSRF-protected. The
// hidden `action` field is "set" or "unset".
func (s *Server) handleWorkflowGolden(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if !s.verifyCSRF(w, r) {
		return
	}
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		http.Error(w, "bad workflow id", http.StatusBadRequest)
		return
	}
	if r.FormValue("action") == "unset" {
		if err := s.store.UnsetGolden(r.Context(), id); err != nil {
			s.renderError(w, "unset golden", err)
			return
		}
		s.redirectWorkflows(w, r, "Golden cleared.", "success")
		return
	}
	if err := s.store.SetGolden(r.Context(), id); err != nil {
		switch {
		case errors.Is(err, store.ErrGoldenNeedsVersion):
			s.redirectWorkflows(w, r, "Attach the workflow to a version before marking it golden.", "error")
		case errors.Is(err, store.ErrNotFound):
			http.NotFound(w, r)
		default:
			s.renderError(w, "set golden", err)
		}
		return
	}
	s.redirectWorkflows(w, r, "Golden workflow set.", "success")
}

// workflowResolver builds the offline-first display resolver for workflow list
// items: model name/raw from model_cache and local-file presence from the store.
// Both funcs read the store lazily (per card), and the workflow list is small, so
// this stays cheap. Never fetches civitai — the model name lazy-loads via
// /models/{id}/title only for cards whose model is uncached.
func (s *Server) workflowResolver() workflowResolver {
	// The reveal capability and its library roots are resolved ONCE per resolver,
	// not per chip: revealRoots reads the persisted scan-dir selection and
	// resolveRoots stats every root. The endpoint re-derives both on each click, so
	// this snapshot only decides whether the control is OFFERED.
	openFolder := s.extraPathsAllowed()
	var realRoots []string
	if openFolder {
		realRoots = resolveRoots(s.revealRoots())
	}
	// ONE index per resolver — i.e. one per request — decoded LAZILY on the first
	// chip that actually needs it. See comfyModelIndex for the measurement that
	// makes this mandatory rather than an optimisation.
	comfyIdx := s.newComfyModelIndex()
	return workflowResolver{
		cachedModel: func(id int) (string, []byte, bool) {
			ent, err := s.store.GetModelCache(id)
			if err != nil || ent == nil {
				return "", nil, false
			}
			return ent.Name, ent.Raw, true
		},
		haveFile: func(basename string) bool {
			ok, _ := s.store.HasLocalFileNamed(basename)
			return ok
		},
		// LocalFileByBasename refuses an AMBIGUOUS basename (several indexed files
		// disagreeing on their civitai linkage) by returning no match — so a chip for
		// such a file shows "present" without a path or a source link, which is the
		// honest rendering. Never fetches civitai.
		localResource: func(basename string) (resourceInfo, bool) {
			lf, err := s.store.LocalFileByBasename(basename)
			if err != nil || lf == nil {
				return resourceInfo{}, false
			}
			info := resourceInfo{Path: lf.Path, FileID: lf.ID}
			if openFolder {
				_, info.Contained = containedDirIn(lf.Path, realRoots)
			}
			if lf.ModelID != nil {
				info.ModelID = *lf.ModelID
			}
			if lf.VersionID != nil {
				info.VersionID = *lf.VersionID
			}
			// Recorded HuggingFace provenance, if any. This is a primary-key lookup
			// in the LOCAL database keyed by the file's content hash — never a
			// fetch, so the chip stays offline-renderable. A file with no recorded
			// provenance simply gets none: there is no lookup, no guess, and no
			// implication that a source is known.
			if p, perr := s.store.HFProvenanceForFile(lf.SHA256); perr == nil && p != nil {
				info.HF = p
			}
			return info, true
		},
		// The cached-ComfyUI lookup. The closure carries the shared index, so every
		// chip on the page reuses one decode.
		comfyResource: comfyIdx.has,
		mr:            s.maturity(),
		csrf:          s.csrf,
		openFolder:    openFolder,
	}
}

// versionNameFromRaw parses a version's display name out of a cached GetModel raw
// body (modelVersions[].name where id == versionID). Defensive: any parse failure
// or absent id yields ok=false so the caller falls back to "version {id}".
func versionNameFromRaw(raw []byte, versionID int) (string, bool) {
	if len(raw) == 0 {
		return "", false
	}
	var m struct {
		ModelVersions []struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
		} `json:"modelVersions"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return "", false
	}
	for _, v := range m.ModelVersions {
		if v.ID == versionID {
			if strings.TrimSpace(v.Name) == "" {
				return "", false
			}
			return v.Name, true
		}
	}
	return "", false
}

// --- helpers ---

// redirectWorkflows POST-redirect-GETs back to the Workflows Library tab with a
// flash.
func (s *Server) redirectWorkflows(w http.ResponseWriter, r *http.Request, msg, level string) {
	s.redirectWorkflowsForModel(w, r, msg, level, 0)
}

// redirectWorkflowsForModel is redirectWorkflows scoped to a source post, so a
// plain (non-htmx) Workflows-model import lands on the SAME filtered view its
// inline result links to. modelID <= 0 omits the filter.
func (s *Server) redirectWorkflowsForModel(w http.ResponseWriter, r *http.Request, msg, level string, modelID int) {
	q := url.Values{"tab": {"workflows"}, "flash": {msg}, "level": {level}}
	if modelID > 0 {
		q.Set("model", strconv.Itoa(modelID))
	}
	http.Redirect(w, r, "/library?"+q.Encode(), http.StatusSeeOther)
}

// redirectWorkflowDetail POST-redirect-GETs back to a workflow detail page. (The
// detail page does not currently render flashes, but the redirect keeps refresh
// semantics clean; the message is carried for parity/future use.)
func (s *Server) redirectWorkflowDetail(w http.ResponseWriter, r *http.Request, id int, msg, level string) {
	_ = msg
	_ = level
	http.Redirect(w, r, "/workflows/"+strconv.Itoa(id), http.StatusSeeOther)
}

// (prettyJSON lived here. It indented the stored graph for the detail page's "View
// raw JSON" disclosure, which PR C1 removed: it dumped a pretty-printed copy of a
// file the user already has, was the largest element on the page, and told nobody
// anything the graph preview does not. Deleted rather than kept as an unreachable
// helper.)

// parseOptionalInt parses a trimmed integer, returning (nil, nil) for a blank
// value so a blank attach field detaches rather than erroring.
func parseOptionalInt(s string) (*int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return nil, err
	}
	return &v, nil
}

// defaultWorkflowName derives a workflow name from an uploaded filename (its base
// without extension), falling back to a generic label.
func defaultWorkflowName(filename string) string {
	base := filepath.Base(filename)
	base = strings.TrimSuffix(base, filepath.Ext(base))
	base = strings.TrimSpace(base)
	if base == "" || base == "." {
		return "extracted workflow"
	}
	return base
}
