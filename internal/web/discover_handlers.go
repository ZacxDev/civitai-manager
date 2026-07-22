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

// discoveryJobBudget is a RUNAWAY BACKSTOP, not the normal termination path. The
// discovery crawl now STREAMS installs as it finds them and is meant to keep
// crawling ALL disks — including slow/spun-down drives where reading a couple of
// directories can take tens of seconds — until the USER stops it (they Stop the
// moment they see the install they want) or the crawl exhausts the tree. The
// budget only bounds a genuinely stuck/forgotten job so it cannot leak a
// goroutine forever; it is deliberately huge (hours), so in practice termination
// is user-Stop, crawl exhaustion, or server shutdown. It is HARD-enforced by
// library.DiscoverInstalls (which returns at the deadline even if a worker is
// blocked in a ReadDir syscall).
const discoveryJobBudget = 6 * time.Hour

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

// handleLibraryDiscover starts a background crawl for ComfyUI/A1111 installs (if
// one is not already running) and returns IMMEDIATELY with a "Scanning…"
// fragment that htmx-polls /library/discover/status for the result. The crawl is
// bounded, CSRF-protected, and loopback-gated. It runs on a background context
// (tied to the server, not the request) so it survives the request returning and
// a full ~26s crawl of a large $HOME completes instead of being truncated by the
// HTTP request's lifetime.
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

	s.startDiscovery()
	// Return the scanning fragment immediately (always WITH the poller, even if the
	// crawl already finished) so the first poll deterministically drives it to the
	// terminal results. The fragment shows whatever has streamed in so far.
	_, _, installs, _, _ := s.discoverJobState()
	selected, _ := s.store.ListScanDirs()
	s.render(w, http.StatusOK, discoverScanning(installs, selected, s.csrf))
}

// startDiscovery launches a background discovery crawl unless one is already
// running (idempotent — a re-click while a crawl is in flight starts no second
// goroutine). The crawl derives its context from the server base context (so
// shutdown cancels it) with the runaway-backstop discoveryJobBudget timeout, and
// STREAMS each discovered install into the job (appended under the mutex) so a
// /status poll shows the growing list. On settle it records the final state.
func (s *Server) startDiscovery() {
	s.discoverMu.Lock()
	defer s.discoverMu.Unlock()
	if s.discoverJob != nil && s.discoverJob.running {
		return // one job at a time
	}

	// Snapshot the de-dupe set (tool roots + persisted selection) up front so the
	// background goroutine does not touch the store concurrently.
	skip := append([]string{s.cfg.ModelRoot}, s.cfg.LibraryPaths...)
	if sel, err := s.store.ListScanDirs(); err == nil {
		skip = append(skip, sel...)
	}

	base := s.baseCtx
	if base == nil {
		base = context.Background()
	}
	crawl := s.crawlFn
	if crawl == nil {
		crawl = library.DiscoverInstalls
	}
	roots := s.discoverRoots

	ctx, cancel := context.WithTimeout(base, discoveryJobBudget)
	job := &discoveryJob{running: true, startedAt: time.Now(), cancel: cancel}
	s.discoverJob = job

	// onInstall streams each newly-found install into the job. It is called from
	// walker goroutines and MUST take the mutex: job.installs is read concurrently
	// by /status. First-writer-wins dedup already happened in the collector, so a
	// path never streams twice.
	onInstall := func(in library.Install) {
		s.discoverMu.Lock()
		job.installs = append(job.installs, in)
		s.discoverMu.Unlock()
	}

	go func() {
		defer cancel()
		installs, err := crawl(ctx, roots, library.DiscoverOptions{Skip: skip, OnInstall: onInstall})
		s.discoverMu.Lock()
		// Merge any installs the crawl RETURNED but did not stream (a crawlFn seam
		// may return a final slice without calling OnInstall; the real crawl streams
		// AND returns the same set, so this de-dups by path and adds nothing).
		seen := make(map[string]bool, len(job.installs))
		for _, in := range job.installs {
			seen[in.Path] = true
		}
		for _, in := range installs {
			if !seen[in.Path] {
				seen[in.Path] = true
				job.installs = append(job.installs, in)
			}
		}
		job.err = err
		job.running = false
		job.finishedAt = time.Now()
		s.discoverMu.Unlock()
	}()
}

// stopDiscovery marks the running job stopped and cancels its context. Idempotent:
// a stop with no running job is a harmless no-op. The crawl goroutine settles on
// its own (running=false) once cancellation propagates; job.stopped stays set so
// the terminal fragment reads "Scan stopped".
func (s *Server) stopDiscovery() {
	s.discoverMu.Lock()
	defer s.discoverMu.Unlock()
	j := s.discoverJob
	if j == nil || !j.running {
		return
	}
	j.stopped = true
	if j.cancel != nil {
		j.cancel()
	}
}

// discoverJobState returns a locked snapshot of the current job. started is false
// when no crawl has ever been triggered. installs is a COPY of the job's slice
// taken under the lock (never the live, still-appended header) and sorted by path
// for stable rendering.
func (s *Server) discoverJobState() (started, running bool, installs []library.Install, stopped bool, err error) {
	s.discoverMu.Lock()
	defer s.discoverMu.Unlock()
	j := s.discoverJob
	if j == nil {
		return false, false, nil, false, nil
	}
	snap := make([]library.Install, len(j.installs))
	copy(snap, j.installs)
	sort.Slice(snap, func(a, b int) bool { return snap[a].Path < snap[b].Path })
	return true, j.running, snap, j.stopped, j.err
}

// renderDiscoverStatus renders the current job state: while running, the scanning
// fragment (WITH the poller, Stop button, and the installs streamed so far);
// once settled, the terminal results fragment (WITHOUT the poller) so htmx stops
// polling. Shared by the status poll and the Stop handler.
func (s *Server) renderDiscoverStatus(w http.ResponseWriter) {
	started, running, installs, stopped, err := s.discoverJobState()
	selected, _ := s.store.ListScanDirs()
	if started && running {
		s.render(w, http.StatusOK, discoverScanning(installs, selected, s.csrf))
		return
	}
	// Not started, or finished: terminal results (no poller). An unstarted job
	// renders the plain "no installs" copy, which also halts any stray poller.
	s.render(w, http.StatusOK, discoverResults(installs, selected, stopped, err, s.csrf))
}

// handleDiscoverStatus is polled by the scanning fragment. GET (no state change,
// so no CSRF) but still loopback-gated.
func (s *Server) handleDiscoverStatus(w http.ResponseWriter, r *http.Request) {
	if !s.gate(w) {
		return
	}
	s.renderDiscoverStatus(w)
}

// handleDiscoverStop cancels the running discovery crawl (the user found the
// install they wanted). CSRF-protected and loopback-gated like the other POSTs.
// Idempotent — stopping when nothing runs is a no-op. It returns the current
// status fragment (which the #discover-poll element swaps in): still scanning if
// the crawl has not yet settled — the poller then drives it to the terminal
// "Scan stopped" fragment — or the terminal fragment directly.
func (s *Server) handleDiscoverStop(w http.ResponseWriter, r *http.Request) {
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
	s.stopDiscovery()
	s.renderDiscoverStatus(w)
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
