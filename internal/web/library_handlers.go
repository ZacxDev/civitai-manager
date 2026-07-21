package web

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
	"github.com/ZacxDev/civitai-manager/internal/library"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// newScanner builds a library.Scanner from the server's configuration.
func (s *Server) newScanner(noRemote bool) *library.Scanner {
	opts := library.Options{
		Paths:      s.cfg.LibraryPaths,
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
	sc := s.newScanner(false)
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

	sc := s.newScanner(false)
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
	sc := s.newScanner(false)
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
