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

// dangerousRoots are the filesystem roots a web-triggered scan must refuse to
// walk: "/" and the top-level system directories. Scanning one of these would
// hash a huge, sensitive, irrelevant subtree — a local footgun and, on a
// non-loopback bind, a remote arbitrary-read/DoS primitive. A submitted path is
// rejected if, after symlink resolution, it IS one of these or lives UNDER one.
var dangerousRoots = []string{
	"/proc", "/sys", "/dev", "/etc", "/boot", "/run",
	"/var", "/usr", "/bin", "/sbin", "/lib",
}

// checkScanRoot rejects an obviously-dangerous scan root. It judges the CLEANED,
// SYMLINK-RESOLVED absolute path (resolved first so a symlink pointing at "/" or
// a system dir is caught, not the innocent-looking link name). It rejects:
//   - "/" itself,
//   - any dangerousRoots entry or a path nested under one,
//   - the user's HOME directory ITSELF (too broad to hash wholesale); a
//     subdirectory of HOME is allowed.
func checkScanRoot(p string) error {
	resolved := p
	if r, err := filepath.EvalSymlinks(p); err == nil {
		resolved = r
	}
	resolved = filepath.Clean(resolved)

	if resolved == string(filepath.Separator) {
		return fmt.Errorf("refusing to scan the filesystem root: %s", p)
	}
	for _, d := range dangerousRoots {
		if resolved == d || isUnderDir(resolved, d) {
			return fmt.Errorf("refusing to scan a system directory (%s): %s", d, p)
		}
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if hr, err := filepath.EvalSymlinks(home); err == nil {
			home = hr
		}
		if resolved == filepath.Clean(home) {
			return fmt.Errorf("refusing to scan your entire home directory (pick a subdirectory): %s", p)
		}
	}
	return nil
}

// isUnderDir reports whether path is nested under dir (not equal to it).
func isUnderDir(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
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
		if err := checkScanRoot(p); err != nil {
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
	s.render(w, http.StatusOK, libraryPage(buildLibraryView(files), s.csrf, s.extraPathsAllowed()))
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

	// noRemote defaults to true for web scans: a web-triggered scan should not
	// send SHA256 hashes of arbitrary host files to CivitAI's by-hash lookup
	// without the operator explicitly opting in. Local duplicate/broken analysis
	// still runs offline. The "match_remote" checkbox opts into remote matching.
	noRemote := r.FormValue("match_remote") != "true"

	// Budget the scan: a deadline plus the model-file cap (threaded into the
	// scanner via MaxFiles) bound the walk so an over-broad path cannot tie up the
	// server. Derive from the request context so a client disconnect also aborts.
	ctx, cancel := context.WithTimeout(r.Context(), s.webScanTimeout())
	defer cancel()

	sc := s.newScanner(extra, noRemote)
	if _, err := sc.Scan(ctx); err != nil {
		s.render(w, http.StatusOK, errorNote(scanErrorMessage(err)))
		return
	}
	files, err := s.store.ListLocalFiles()
	if err != nil {
		s.renderError(w, "reload library", err)
		return
	}
	s.render(w, http.StatusOK, libraryContent(buildLibraryView(files), s.csrf))
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
