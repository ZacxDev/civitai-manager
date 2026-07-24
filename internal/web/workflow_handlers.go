package web

import (
	"bytes"
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
	s.render(w, http.StatusOK, workflowDetailPage(wf, prettyJSON(wf.Graph),
		s.csrf, s.currentTheme(), s.nsfwMode()))
}

// handleWorkflowImport ingests a pasted API/UI graph. CSRF-protected and
// loopback-gated (it accepts arbitrary user-supplied content). It detects the
// format, extracts referenced resources for api graphs, stores the workflow, and
// redirects back to the library with a flash.
func (s *Server) handleWorkflowImport(w http.ResponseWriter, r *http.Request) {
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
	var (
		graph  string
		format string
		res    []string
	)
	if ex.APIGraph != nil {
		graph, format = string(ex.APIGraph), comfy.FormatAPI
		if r, rerr := comfy.ExtractResources(ex.APIGraph); rerr == nil {
			res = r
		}
	} else {
		graph, format = string(ex.UIGraph), comfy.FormatUI
	}

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

// --- helpers ---

// redirectWorkflows POST-redirect-GETs back to the Workflows Library tab with a
// flash.
func (s *Server) redirectWorkflows(w http.ResponseWriter, r *http.Request, msg, level string) {
	q := url.Values{"tab": {"workflows"}, "flash": {msg}, "level": {level}}
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

// prettyJSON indents a stored graph for display, falling back to the raw string
// when it does not re-parse (it is stored untrusted; never assume it re-indents).
func prettyJSON(raw string) string {
	var buf bytes.Buffer
	if err := json.Indent(&buf, []byte(raw), "", "  "); err != nil {
		return raw
	}
	return buf.String()
}

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
