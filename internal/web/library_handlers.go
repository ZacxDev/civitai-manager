package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
	"github.com/ZacxDev/civitai-manager/internal/library"
	"github.com/ZacxDev/civitai-manager/internal/store"
	g "maragu.dev/gomponents"
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
		// The web scan is a bounded surface: cap the walked model-file count so an
		// over-broad path aborts with a friendly error instead of running away.
		MaxFiles: s.webScanMaxFiles(),
	}
	var reader civitai.Reader
	if !noRemote {
		reader = s.reader
	}
	return library.NewScanner(s.store, reader, opts, s.log)
}

// checkScanRoot delegates to library.CheckScanRoot, the single source of truth
// for the dangerous-scan-root blocklist (shared so the scan form and the
// discovery crawl enforce one identical guard: "/", the system dirs, and HOME
// itself are refused).
func checkScanRoot(p string) error { return library.CheckScanRoot(p) }

// validateScanDir validates one submitted directory: absolute, an existing
// directory, and not a dangerous root. It returns the cleaned path.
func validateScanDir(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", fmt.Errorf("empty path")
	}
	if !filepath.IsAbs(p) {
		return "", fmt.Errorf("path must be absolute: %s", p)
	}
	p = filepath.Clean(p)
	fi, err := os.Stat(p)
	if err != nil || !fi.IsDir() {
		return "", fmt.Errorf("not an existing directory: %s", p)
	}
	if err := checkScanRoot(p); err != nil {
		return "", err
	}
	return p, nil
}

// parseScanPaths parses the Library page's "scan_paths" field: a set of extra
// scan directories separated by newlines OR commas. Each path is validated to be
// absolute, an existing directory, and NOT an obviously-dangerous root (see
// checkScanRoot); a bad entry yields a friendly error (never a 500). It returns
// the cleaned, de-duplicated list (possibly empty).
func parseScanPaths(raw string) ([]string, error) {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == '\n' || r == '\r' || r == ','
	})
	seen := map[string]bool{}
	var out []string
	for _, f := range fields {
		if strings.TrimSpace(f) == "" {
			continue
		}
		p, err := validateScanDir(f)
		if err != nil {
			return nil, err
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
	var selected []string
	if s.extraPathsAllowed() {
		if sel, err := s.store.ListScanDirs(); err == nil {
			selected = sel
		}
	}
	// Bootstrap the stable #discover-results container from the live job state so a
	// reload / tab-switch during a crawl resumes the scanning view (and its
	// re-arming poller) instead of dropping back to the idle controls.
	var discoverInitial g.Node
	if s.extraPathsAllowed() {
		if started, running, installs, stopped, derr := s.discoverJobState(); started {
			if running {
				discoverInitial = discoverScanning(installs, selected, s.csrf)
			} else {
				discoverInitial = discoverResults(installs, selected, stopped, derr, s.csrf)
			}
		}
	}
	// Bootstrap the stable #scan-results container from the live scan job so a
	// reload / tab-switch (including the CTA's HX-Redirect landing) during a scan
	// resumes the scanning view + re-arming poller instead of the idle content.
	var scanInitial g.Node
	if snap := s.scanJobState(); snap.Started {
		if snap.Running {
			scanInitial = scanScanning(snap, s.csrf)
		} else {
			scanInitial = scanResults(buildLibraryView(files), snap, s.csrf)
		}
	}
	tab := r.URL.Query().Get("tab")
	s.render(w, http.StatusOK, libraryPage(buildLibraryView(files), s.csrf, s.extraPathsAllowed(), selected, s.currentTheme(), tab, discoverInitial, s.matchRemoteEnabled(), scanInitial))
}

func (s *Server) handleLibraryScan(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if !s.verifyCSRF(w, r) {
		return
	}
	// Extra scan dirs come from two inputs: the new "scan_dir" checkboxes (the
	// selected-installs UI) and, for backward compatibility, the legacy
	// "scan_paths" textarea field. Both are validated identically.
	extra, err := parseScanPaths(r.FormValue("scan_paths"))
	if err != nil {
		s.render(w, http.StatusOK, errorNote("Invalid scan path: "+err.Error()))
		return
	}
	selectedDirs, err := s.collectScanDirs(r)
	if err != nil {
		s.render(w, http.StatusOK, errorNote("Invalid scan path: "+err.Error()))
		return
	}
	// The new Tab B model-scan form submits NO scan_dir checkboxes: the dirs to
	// scan are the persisted selection managed in Tab A. When no checkbox field is
	// present, fall back to that persisted selection (and do NOT re-persist below,
	// so an empty submit can never wipe it). The legacy checkbox/textarea UIs still
	// submit scan_dir and take the persist-what-is-checked path.
	_, hasCheckboxField := r.Form["scan_dir"]
	if !hasCheckboxField && s.extraPathsAllowed() {
		if sel, err := s.store.ListScanDirs(); err == nil {
			selectedDirs = sel
		}
	}
	extra = unionPaths(extra, selectedDirs)

	// Non-loopback gating: the arbitrary extra-scan-path capability is a local,
	// single-user convenience. When the server is exposed on a non-loopback
	// interface it becomes an unauthenticated remote arbitrary-path walk, so any
	// submitted extra path is refused outright (the input is also hidden — see
	// scanForm). A plain model_root scan still works.
	if len(extra) > 0 && !s.extraPathsAllowed() {
		s.render(w, http.StatusOK, errorNote(
			"Extra scan paths are disabled when the server is bound to a non-loopback address. "+
				"Only model_root and configured library_paths are scanned."))
		return
	}

	// Persist the checkbox selection so it survives across scans and pre-fills the
	// form on the next load (legacy checkbox UI only). A checkbox-less Tab B scan is
	// skipped here so it can never overwrite the Tab-A-managed persisted selection.
	if hasCheckboxField && s.extraPathsAllowed() {
		if err := s.store.SetScanDirs(selectedDirs); err != nil {
			s.log.Warn("persist scan dirs", "err", err)
		}
	}

	// The "Match against CivitAI" opt-in is PERSISTED (the Tab B toggle) and is the
	// single source of truth: the Tab-A "Scan for models" CTA carries no per-form
	// checkbox, so it must read the setting. A legacy form that still submits
	// match_remote persists that value first, then we read the persisted setting.
	// A web scan stays offline (local duplicate/broken analysis only) unless the
	// operator explicitly enabled remote matching.
	if _, ok := r.Form["match_remote"]; ok {
		val := "false"
		if r.FormValue("match_remote") == "true" {
			val = "true"
		}
		if err := s.store.SetSetting(matchRemoteSettingKey, val); err != nil {
			s.log.Warn("persist match_remote", "err", err)
		}
	}
	noRemote := !s.matchRemoteEnabled()

	// Moving to the model-scan phase: STOP any running install-discovery crawl (the
	// user is done finding install dirs), then start the STREAMING scan on a
	// background context (NOT r.Context()) so it outlives the request and a long
	// hash of a multi-GB library completes. Land the user on the Model files tab,
	// whose #scan-results container bootstraps the live scanning view + re-arming
	// poller from the job just started (mirrors how discovery resumes on reload).
	s.stopDiscovery()
	s.startScan(extra, noRemote)
	w.Header().Set("HX-Redirect", "/library?tab=files")
	w.WriteHeader(http.StatusOK)
}

// collectScanDirs validates every "scan_dir" checkbox value on the request,
// returning the cleaned, de-duplicated list. A bad entry yields a friendly error.
func (s *Server) collectScanDirs(r *http.Request) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, raw := range r.Form["scan_dir"] {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		p, err := validateScanDir(raw)
		if err != nil {
			return nil, err
		}
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out, nil
}

// unionPaths merges two path lists, de-duplicating (order-preserving).
func unionPaths(a, b []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, list := range [][]string{a, b} {
		for _, p := range list {
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	return out
}

// scanErrorMessage maps a scan failure to a friendly, actionable message. The
// budget/deadline aborts get a "narrow the path" hint; other errors pass through.
func scanErrorMessage(err error) string {
	switch {
	case errors.Is(err, library.ErrScanTooLarge):
		return "Scan too large; narrow the path (too many files under the scanned directories)."
	case errors.Is(err, context.DeadlineExceeded):
		return "Scan timed out; narrow the path or raise --web-scan-timeout."
	case errors.Is(err, context.Canceled):
		return "Scan cancelled."
	default:
		return "Scan failed: " + err.Error()
	}
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
	s.render(w, http.StatusOK, trashPage(batches, s.csrf, s.currentTheme()))
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
