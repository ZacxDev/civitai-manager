package web

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
	"github.com/ZacxDev/civitai-manager/internal/library"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// newScanner builds a library.Scanner from the server's configuration, adding
// any extra scan-path directories (from the Library page's scan form) unioned
// with the configured library paths and model_root. This reuses the exact same
// scanner the CLI `scan` command drives — no forked scan logic.
func (s *Server) newScanner(extraPaths []string, noRemote bool) *library.Scanner {
	paths := append([]string{}, s.cfg.LibraryPaths...)
	paths = append(paths, extraPaths...)
	opts := library.Options{
		Paths:      paths,
		ModelRoot:  s.cfg.ModelRoot,
		TrashDir:   s.cfg.TrashDir,
		Extensions: library.ExtensionSet(s.cfg.Extensions),
		NoRemote:   noRemote,
	}
	var reader civitai.Reader
	if !noRemote {
		reader = s.reader
	}
	return library.NewScanner(s.store, reader, opts, s.log)
}

// parseScanPaths parses the Library page's "scan_paths" field: a set of extra
// scan directories separated by newlines OR commas. Each path is validated to be
// absolute and an existing directory; a bad entry yields a friendly error (never
// a 500). It returns the cleaned, de-duplicated list (possibly empty).
func parseScanPaths(raw string) ([]string, error) {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == '\n' || r == '\r' || r == ','
	})
	seen := map[string]bool{}
	var out []string
	for _, f := range fields {
		p := strings.TrimSpace(f)
		if p == "" {
			continue
		}
		if !filepath.IsAbs(p) {
			return nil, fmt.Errorf("path must be absolute: %s", p)
		}
		p = filepath.Clean(p)
		fi, err := os.Stat(p)
		if err != nil || !fi.IsDir() {
			return nil, fmt.Errorf("not an existing directory: %s", p)
		}
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out, nil
}

func (s *Server) handleLibrary(w http.ResponseWriter, r *http.Request) {
	files, err := s.store.ListLocalFiles()
	if err != nil {
		s.renderError(w, "load library", err)
		return
	}
	s.render(w, http.StatusOK, libraryPage(buildLibraryView(files), s.csrf))
}

func (s *Server) handleLibraryScan(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if !s.verifyCSRF(w, r) {
		return
	}
	extra, err := parseScanPaths(r.FormValue("scan_paths"))
	if err != nil {
		s.render(w, http.StatusOK, errorNote("Invalid scan path: "+err.Error()))
		return
	}
	sc := s.newScanner(extra, false)
	if _, err := sc.Scan(r.Context()); err != nil {
		s.render(w, http.StatusOK, errorNote("Scan failed: "+err.Error()))
		return
	}
	files, err := s.store.ListLocalFiles()
	if err != nil {
		s.renderError(w, "reload library", err)
		return
	}
	s.render(w, http.StatusOK, libraryContent(buildLibraryView(files), s.csrf))
}

func (s *Server) handleQuarantine(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if !s.verifyCSRF(w, r) {
		return
	}
	ids := s.quarantineIDs(r)
	if len(ids) == 0 {
		s.render(w, http.StatusOK, errorNote("Select at least one candidate to quarantine."))
		return
	}
	apply := r.FormValue("apply") == "true"

	sc := s.newScanner(nil, false)
	plan, err := sc.Quarantine(r.Context(), ids, apply)
	if err != nil {
		s.render(w, http.StatusOK, errorNote("Quarantine failed: "+err.Error()))
		return
	}
	s.render(w, http.StatusOK, quarantinePreview(plan, ids, s.csrf))
}

// quarantineIDs collects the target ids from the request: repeated "id" checkbox
// fields, a comma-separated "ids" field (the confirm button), or, failing both,
// a "reason"/"all" selector resolved against the store.
func (s *Server) quarantineIDs(r *http.Request) []int64 {
	var out []int64
	seen := map[int64]bool{}
	add := func(v string) {
		v = strings.TrimSpace(v)
		if v == "" {
			return
		}
		if id, err := strconv.ParseInt(v, 10, 64); err == nil && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	for _, v := range r.Form["id"] {
		add(v)
	}
	for _, v := range strings.Split(r.FormValue("ids"), ",") {
		add(v)
	}
	if len(out) > 0 {
		return out
	}

	// Fall back to a reason/all selector.
	reason := r.FormValue("reason")
	switch reason {
	case store.CandidateSuperseded, store.CandidateDuplicate, store.CandidateBroken, "":
	default:
		return nil
	}
	if reason == "" && r.FormValue("all") != "true" {
		return nil
	}
	cands, err := s.store.ListCandidates(reason)
	if err != nil {
		return nil
	}
	for _, c := range cands {
		out = append(out, c.ID)
	}
	return out
}

func (s *Server) handleTrash(w http.ResponseWriter, r *http.Request) {
	batches, err := s.loadBatchViews()
	if err != nil {
		s.renderError(w, "load trash", err)
		return
	}
	s.render(w, http.StatusOK, trashPage(batches, s.csrf))
}

func (s *Server) handleRestore(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if !s.verifyCSRF(w, r) {
		return
	}
	sc := s.newScanner(nil, false)
	res, err := sc.Restore(r.Context(), id)
	if err != nil {
		s.render(w, http.StatusOK, errorNote("Restore failed: "+err.Error()))
		return
	}
	// Re-render the whole trash table so the batch row reflects its new state.
	batches, err := s.loadBatchViews()
	if err != nil {
		s.renderError(w, "reload trash", err)
		return
	}
	_ = res
	s.render(w, http.StatusOK, trashTable(batches, s.csrf))
}

func (s *Server) loadBatchViews() ([]batchView, error) {
	batches, err := s.store.ListQuarantineBatches()
	if err != nil {
		return nil, err
	}
	out := make([]batchView, 0, len(batches))
	for _, b := range batches {
		files, err := s.store.ListQuarantinedFiles(b.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, batchView{Batch: b, Files: len(files)})
	}
	return out, nil
}
