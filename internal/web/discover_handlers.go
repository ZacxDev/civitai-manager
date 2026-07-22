package web

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/library"
)

// discoverBudget bounds a web-triggered discovery crawl. It is independent of
// the scan timeout: discovery only stats/marker-checks (no hashing), so it is
// cheap, but it still needs a hard ceiling on a huge $HOME.
const discoverBudget = 15 * time.Second

// browseEntry is one immediate subdirectory listed by the directory browser.
type browseEntry struct {
	Name string
	Path string
}

// gate reports whether the arbitrary-path capability is available; when it is
// not it renders the standard gating note and returns false so the handler stops.
// Discovery, browsing, and scan-dir selection are all local-single-user
// conveniences disabled on a non-loopback bind (an unauthenticated remote
// arbitrary-read primitive otherwise).
func (s *Server) gate(w http.ResponseWriter) bool {
	if s.extraPathsAllowed() {
		return true
	}
	s.render(w, http.StatusOK, errorNote(
		"This control is disabled when the server is bound to a non-loopback address."))
	return false
}

// handleLibraryDiscover crawls for ComfyUI/A1111 installs and renders each as a
// selectable candidate. Bounded, CSRF-protected, loopback-gated.
func (s *Server) handleLibraryDiscover(w http.ResponseWriter, r *http.Request) {
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

	// De-dupe discovered installs against the tool's own roots + the current
	// persisted selection.
	skip := append([]string{s.cfg.ModelRoot}, s.cfg.LibraryPaths...)
	if sel, err := s.store.ListScanDirs(); err == nil {
		skip = append(skip, sel...)
	}

	ctx, cancel := context.WithTimeout(r.Context(), discoverBudget)
	defer cancel()

	installs, err := library.DiscoverInstalls(ctx, s.discoverRoots, library.DiscoverOptions{Skip: skip})
	// A budget/cancel abort still returns the partial results found so far; render
	// them and note the truncation rather than failing.
	truncated := err != nil
	selected, _ := s.store.ListScanDirs()
	s.render(w, http.StatusOK, discoverResults(installs, selected, truncated, s.csrf))
}

// handleLibraryBrowse lists the immediate subdirectories of a server path,
// letting the user navigate and add a directory to the selection. It never
// descends, follows symlinks, or leaks file contents; system dirs are refused.
func (s *Server) handleLibraryBrowse(w http.ResponseWriter, r *http.Request) {
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

	path := strings.TrimSpace(r.FormValue("path"))
	if path == "" {
		if home, err := os.UserHomeDir(); err == nil {
			path = home
		} else {
			path = string(filepath.Separator)
		}
	}
	if !filepath.IsAbs(path) {
		s.render(w, http.StatusOK, errorNote("Path must be absolute: "+path))
		return
	}
	path = filepath.Clean(path)
	fi, err := os.Stat(path)
	if err != nil || !fi.IsDir() {
		s.render(w, http.StatusOK, errorNote("Not a directory: "+path))
		return
	}
	if library.BlockedForBrowse(path) {
		s.render(w, http.StatusOK, errorNote("Refusing to browse a system directory: "+path))
		return
	}
	// Constrain the interactive browser to the dirs a user could plausibly scan:
	// $HOME plus the tool's own model_root and configured library_paths. This is
	// checked on the symlink-resolved real path, so a symlink out of an allowed
	// dir cannot escape it. (Defense-in-depth atop the loopback+CSRF gate.)
	allowedRoots := append([]string{s.cfg.ModelRoot}, s.cfg.LibraryPaths...)
	if !library.BrowseAllowed(path, allowedRoots) {
		s.render(w, http.StatusOK, errorNote(
			"Refusing to browse outside your home directory, model_root, or library_paths: "+path))
		return
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		s.render(w, http.StatusOK, errorNote("Could not read directory: "+err.Error()))
		return
	}
	var dirs []browseEntry
	for _, e := range entries {
		// Only directories; a symlink is reported by ReadDir with its own type —
		// list it only when it points at a directory, but never follow it further.
		if !e.IsDir() {
			if e.Type()&os.ModeSymlink == 0 {
				continue
			}
			target, err := os.Stat(filepath.Join(path, e.Name()))
			if err != nil || !target.IsDir() {
				continue
			}
		}
		if strings.HasPrefix(e.Name(), ".") {
			continue // hide dotdirs to keep the browser tidy
		}
		dirs = append(dirs, browseEntry{Name: e.Name(), Path: filepath.Join(path, e.Name())})
	}
	sort.Slice(dirs, func(i, j int) bool { return dirs[i].Name < dirs[j].Name })

	// canAdd: HOME itself and "/" cannot be scan roots, but their subdirs can.
	canAdd := checkScanRoot(path) == nil
	s.render(w, http.StatusOK, browseResults(path, dirs, canAdd, s.csrf))
}

// handleScanDirAdd validates and persists one selected scan directory, returning
// the refreshed selection fragment.
func (s *Server) handleScanDirAdd(w http.ResponseWriter, r *http.Request) {
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
	p, err := validateScanDir(r.FormValue("path"))
	if err != nil {
		s.render(w, http.StatusOK, errorNote("Cannot add: "+err.Error()))
		return
	}
	if err := s.store.AddScanDir(p); err != nil {
		s.renderError(w, "add scan dir", err)
		return
	}
	s.renderSelectedDirs(w)
}

// handleScanDirRemove drops one persisted scan directory.
func (s *Server) handleScanDirRemove(w http.ResponseWriter, r *http.Request) {
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
	if err := s.store.RemoveScanDir(strings.TrimSpace(r.FormValue("path"))); err != nil {
		s.renderError(w, "remove scan dir", err)
		return
	}
	s.renderSelectedDirs(w)
}

func (s *Server) renderSelectedDirs(w http.ResponseWriter) {
	sel, err := s.store.ListScanDirs()
	if err != nil {
		s.renderError(w, "load scan dirs", err)
		return
	}
	s.render(w, http.StatusOK, selectedDirsList(sel, s.csrf))
}
