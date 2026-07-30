package web

import (
	"context"
	"errors"
	"net/http"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	g "maragu.dev/gomponents"
)

// 🔴 This file is the ONLY place internal/web starts a process. Everything below
// exists to make that safe, and none of it is optional:
//
//   - The request carries an INTEGER local_files rowid and a CSRF token. It never
//     carries a filesystem path, so there is no attacker-controlled string that
//     could become an argv element. The path is re-derived server-side from the
//     database row.
//   - That path is resolved through symlinks FIRST and only then checked for
//     containment in a configured library root (resolve-then-check). Checking
//     before resolving is the classic bypass: a symlink whose name sits inside a
//     root but whose target does not would pass it.
//   - The opener is a fixed per-GOOS allowlist. It is never read from config, from
//     the environment, or from the request.
//   - There is no shell anywhere: exec.CommandContext receives an argv, so nothing
//     in the directory name can be interpreted as a command, a redirect, or a
//     separator regardless of what characters it contains.
//   - The endpoint is loopback-gated for the same reason the scan/browse endpoints
//     are: it acts on the machine running `serve`, and CSRF is not an auth boundary.

// revealTimeout bounds the child process. A file manager launcher returns almost
// immediately (it hands off to an already-running desktop session), so anything
// still alive after this is stuck and gets killed rather than accumulating.
const revealTimeout = 20 * time.Second

// errNoOpener is returned when the platform has no allowlisted opener.
var errNoOpener = errors.New("no file manager opener for this platform")

// fileManagerOpener returns the FIXED opener command for the running platform.
//
// This allowlist is the whole point: it is a compile-time constant per GOOS,
// never a config value and never anything from the request. A user-supplied
// opener would turn this endpoint into arbitrary command execution.
func fileManagerOpener() (string, bool) {
	switch runtime.GOOS {
	case "darwin":
		return "open", true
	case "windows":
		return "explorer", true
	case "linux", "freebsd", "openbsd", "netbsd", "dragonfly":
		// xdg-open is the freedesktop.org standard indirection; it is what every
		// desktop environment on these platforms installs.
		return "xdg-open", true
	}
	return "", false
}

// fileManagerArgv builds the exact argv to execute for a RESOLVED, CONTAINED
// directory: the allowlisted opener plus that one directory, and nothing else.
// No flags, no shell, no user-supplied token.
func fileManagerArgv(dir string) ([]string, bool) {
	opener, ok := fileManagerOpener()
	if !ok || dir == "" {
		return nil, false
	}
	return []string{opener, dir}, true
}

// startFileManager is the production opener. It STARTS the child and returns —
// the HTTP handler never blocks on a desktop process — while a background
// goroutine reaps it and releases the timeout context. exec.CommandContext kills
// the child if it outlives revealTimeout.
//
// exec.CommandContext(name, args...) execs `name` directly with `args` as argv;
// there is no shell, so no character in dir can be interpreted as syntax.
func startFileManager(argv []string) error {
	if len(argv) == 0 {
		return errNoOpener
	}
	ctx, cancel := context.WithTimeout(context.Background(), revealTimeout)
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	// No stdio is wired up: the child must not be able to write into our streams,
	// and it has nothing to read.
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	if err := cmd.Start(); err != nil {
		cancel()
		return err
	}
	go func() {
		defer cancel()
		_ = cmd.Wait()
	}()
	return nil
}

// opener returns the configured opener seam (tests) or the real one.
func (s *Server) opener() func(argv []string) error {
	if s.openerFn != nil {
		return s.openerFn
	}
	return startFileManager
}

// revealRoots is the set of directories a revealed file is allowed to live under:
// this app's own model root, the ComfyUI models dir it installs into, the extra
// library paths from config, and the scan directories the user explicitly
// selected. Every one of them is a location the USER configured as part of their
// library — nothing here comes from a request.
//
// A blank entry is dropped rather than treated as "/" (an empty root would make
// the containment check pass for every path on the filesystem).
func (s *Server) revealRoots() []string {
	candidates := []string{s.cfg.ModelRoot, s.cfg.ComfyModelPath}
	candidates = append(candidates, s.cfg.LibraryPaths...)
	if s.store != nil {
		if sel, err := s.store.ListScanDirs(); err == nil {
			candidates = append(candidates, sel...)
		}
	}
	roots := make([]string, 0, len(candidates))
	seen := map[string]bool{}
	for _, c := range candidates {
		c = strings.TrimSpace(c)
		if c == "" || !filepath.IsAbs(c) || seen[c] {
			continue
		}
		seen[c] = true
		roots = append(roots, c)
	}
	return roots
}

// containedDir resolves the directory holding path and reports whether it lies
// inside one of roots. It returns the RESOLVED directory — the exact string that
// will be exec'd — so the caller can never accidentally pass the unresolved one.
//
// Order matters and is the security property: EvalSymlinks runs BEFORE the
// containment test, on both the candidate and each root. A symlink at
// <root>/link → /etc therefore fails, because what is compared is /etc, not
// <root>/link. A "../" traversal is likewise normalised away by EvalSymlinks
// (which calls Clean), so it cannot smuggle a path out of a root either.
//
// A path that does not exist cannot be resolved and is refused: we will not open
// a folder we cannot prove is where we think it is.
func containedDir(path string, roots []string) (string, bool) {
	path = strings.TrimSpace(path)
	if path == "" || !filepath.IsAbs(path) {
		return "", false
	}
	dir, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return "", false
	}
	for _, root := range roots {
		realRoot, err := filepath.EvalSymlinks(root)
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(realRoot, dir)
		if err != nil {
			continue
		}
		// Rel yields "." for the root itself and a path starting with ".." for
		// anything outside it. Both the exact ".." and the "../" prefix must be
		// rejected; a sibling directory named "..foo" must not be.
		if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		return dir, true
	}
	return "", false
}

// handleLibraryFileReveal opens the containing folder of ONE indexed local file in
// the platform file manager.
//
// CSRF-protected + loopback-gated. The request supplies only the file's rowid; the
// path is re-derived from the database and must resolve (through symlinks) into a
// configured library root, or the request is refused without executing anything.
//
// The response replaces the control in place with the same button plus a short
// outcome line — including the fact that the window opened on the SERVER's
// machine, which is the one thing a user driving this from another device needs
// to be told.
func (s *Server) handleLibraryFileReveal(w http.ResponseWriter, r *http.Request) {
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
	if err != nil || id <= 0 {
		http.Error(w, "bad file id", http.StatusBadRequest)
		return
	}

	lf, err := s.store.GetLocalFile(id)
	if err != nil || lf == nil {
		// Includes store.ErrNotFound: the id names no indexed file, so there is no
		// path to derive and nothing to open.
		s.render(w, http.StatusOK, s.revealResult(id, "", "That file is no longer in your library index.", "error"))
		return
	}

	dir, ok := containedDir(lf.Path, s.revealRoots())
	if !ok {
		s.render(w, http.StatusOK, s.revealResult(id, "",
			"That file is not inside one of your configured library folders, so it will not be opened.", "error"))
		return
	}

	argv, ok := fileManagerArgv(dir)
	if !ok {
		s.render(w, http.StatusOK, s.revealResult(id, "",
			"Opening a folder is not supported on this platform.", "error"))
		return
	}
	if err := s.opener()(argv); err != nil {
		s.log.Warn("open containing folder failed", "file_id", id, "opener", argv[0], "err", err)
		s.render(w, http.StatusOK, s.revealResult(id, "",
			"Could not start "+argv[0]+" on the computer running civitai-manager.", "error"))
		return
	}
	s.render(w, http.StatusOK, s.revealResult(id, dir,
		"Opened on the computer running civitai-manager.", "ok"))
}

// revealResult re-renders the control with an outcome message. On success it
// names the folder that was opened, so the statement is checkable.
func (s *Server) revealResult(id int64, dir, msg, state string) g.Node {
	if dir != "" {
		msg = msg + " (" + dir + ")"
	}
	return resourceOpenControl(id, s.csrf, msg, state)
}
