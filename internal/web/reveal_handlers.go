package web

import (
	"context"
	"errors"
	"net/http"
	"os"
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
//   - The opener NAME is a fixed per-GOOS allowlist ("xdg-open"/"open"/"explorer").
//     That name is never read from config, from the environment, or from the
//     request. The BINARY it resolves to is a different question: exec.Command
//     resolves a bare name through exec.LookPath, i.e. through $PATH, so which
//     executable actually runs depends on the environment `serve` was started
//     with. That is normal desktop-launcher behaviour and is deliberately not
//     changed — but "never from the environment" is true of the NAME only, and
//     anyone reading this for a security property needs to know the difference.
//   - The child inherits the server's FULL environment: cmd.Env is left unset, so
//     exec passes os.Environ() through — including CIVITAI_TOKEN and HF_TOKEN if
//     `serve` was started with them. Again normal for a desktop launcher (the file
//     manager needs DISPLAY/WAYLAND_DISPLAY/DBUS_SESSION_BUS_ADDRESS/XDG_* to do
//     anything at all), and again documented rather than changed. A scrubbed
//     allowlist env is the alternative if that ever stops being acceptable.
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

// openerCommand builds the *exec.Cmd for an argv. It is the single place a
// process is constructed, and it is deliberately trivial so it can be asserted in
// a test WITHOUT being run:
//
//	exec.CommandContext(ctx, argv[0], argv[1:]...)
//
// execs argv[0] directly with argv[1:] as its arguments. There is NO shell — no
// "sh", no "-c", no string that gets parsed — so no character in the directory
// name can be interpreted as a command separator, a redirect, or a glob,
// regardless of what the filesystem contains.
func openerCommand(ctx context.Context, argv []string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	// No stdio is wired up: the child must not be able to write into our streams,
	// and it has nothing to read.
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	return cmd
}

// startFileManager is the production opener. It STARTS the child and returns —
// the HTTP handler never blocks on a desktop process — while a background
// goroutine reaps it and releases the timeout context. exec.CommandContext kills
// the child if it outlives revealTimeout.
func startFileManager(argv []string) error {
	if len(argv) == 0 {
		return errNoOpener
	}
	ctx, cancel := context.WithTimeout(context.Background(), revealTimeout)
	cmd := openerCommand(ctx, argv)
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

// hasGraphicalSession reports whether the machine running `serve` has a session a
// file-manager window could appear in.
//
// 🔴 THIS EXISTS BECAUSE THE OPENER LIES BY OMISSION. On the free-desktop
// platforms `xdg-open` is a shell script that falls back through a list of
// handlers and **exits 0 even when every one of them is missing**. Measured on
// this host with the server's own environment:
//
//	$ env -u DISPLAY -u XAUTHORITY xdg-open /home/zach
//	xdg-open: line 1032: www-browser: command not found
//	xdg-open: line 1032: links2: command not found
//	xdg-open: line 1032: elinks: command not found
//	  exit=0
//
// startFileManager does not even consult that exit code — it reports success from
// cmd.Start(), i.e. from "a process was created". So there is no signal AFTER the
// launch that distinguishes "a window opened" from "nothing happened", and the
// endpoint reported a confident "Opened…" for a window that was never created.
// That is worse than an error: the user goes looking for it.
//
// The one thing that CAN be established beforehand is whether a window could
// appear at all. On X11/Wayland that is exactly DISPLAY / WAYLAND_DISPLAY: with
// neither set, no client can connect to a display server and nothing graphical
// can be shown, no matter which opener runs. A headless box, a systemd service
// with a clean environment, and a plain SSH session all land here — and so does
// this repo's own dogfood instance when it is started from a non-graphical shell.
//
// It is deliberately NOT a general "is a desktop running" test:
//
//   - darwin/windows report true unconditionally. Their openers talk to a session
//     window server rather than to a display-server socket named by an env var,
//     so there is no equivalent variable to read, and inventing one would refuse
//     the control on machines where it works.
//   - A set DISPLAY does not PROVE a reachable server (it can name a dead socket).
//     This is a necessary condition, not a sufficient one — which is why the
//     success wording is still hedged rather than promoted back to "Opened".
//
// env is injected (os.Getenv in production) so the behaviour can be tested
// without depending on the environment the test process happens to inherit.
func hasGraphicalSession(goos string, env func(string) string) bool {
	if env == nil {
		return false
	}
	switch goos {
	case "darwin", "windows":
		return true
	}
	for _, key := range []string{"DISPLAY", "WAYLAND_DISPLAY"} {
		if strings.TrimSpace(env(key)) != "" {
			return true
		}
	}
	return false
}

// graphical returns the configured graphical-session seam (tests) or the real
// probe against this process's own environment.
func (s *Server) graphical() func() bool {
	if s.graphicalFn != nil {
		return s.graphicalFn
	}
	return func() bool { return hasGraphicalSession(runtime.GOOS, os.Getenv) }
}

// revealRoots is the set of directories a revealed file is allowed to live under:
// this app's own model root, the ComfyUI models dir it installs into, the extra
// library paths from config, and the scan directories the user explicitly
// selected.
//
// 🔴 The root set IS request-extensible, and pretending otherwise would be the
// kind of comment that outlives the code. The first three legs come from config
// (or flags) and cannot be reached over HTTP; the fourth, store.ListScanDirs,
// is populated by `POST /library/scan-dirs/add`, which takes the directory
// straight from r.FormValue("path") (handleScanDirAdd in discover_handlers.go).
// So a request CAN add a directory to this allowlist. That is not a widening we
// consider exploitable, and it is deliberately left alone, because the add path
// is:
//
//   - CSRF-verified and loopback-gated, exactly like this endpoint;
//   - validated by validateScanDir (library_handlers.go): non-empty, ABSOLUTE,
//     Clean'd, and os.Stat'd as an EXISTING DIRECTORY at add time;
//   - passed through library.CheckScanRoot, which refuses "/", the system
//     directories, and the bare home directory.
//
// In other words a local user can extend their own library allowlist from their
// own browser, which is the feature — not an escalation, since the same user
// already controls the config file.
//
// Second-order fact worth stating, because it is where the two checks disagree:
// validateScanDir proves the path is a real DIRECTORY at ADD time and nothing
// re-proves it afterwards. resolveRoots re-runs EvalSymlinks at CLICK time, so a
// stored root that has since become a SYMLINK is followed to wherever it now
// points, and that resolved target becomes the containment root. The persisted
// string is the only thing that is stable; what it names is re-read on every
// click. (resolveRoots does re-check IsDir, so a root that became a regular file
// is dropped — but a root that became a symlink TO a directory is honoured.)
//
// A blank entry is dropped rather than treated as "/" (an empty root would make
// the containment check pass for every path on the filesystem). A LITERAL "/"
// is, however, accepted: resolveRoots([]string{"/"}) returns ["/"], and every
// absolute path is then contained. The UI cannot produce that — CheckScanRoot
// refuses it on the add path — but `library_paths: ["/"]` in the config file
// reaches here unfiltered. Known and accepted: config is the same trust level as
// model_root, and a user who writes that has disabled their own containment
// check on purpose.
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
// a folder we cannot prove is where we think it is. Nor will we open something
// that is not a folder at all — see the IsDir test in containedDirIn.
func containedDir(path string, roots []string) (string, bool) {
	return containedDirIn(path, resolveRoots(roots))
}

// resolveRoots resolves each configured root through symlinks ONCE. Rendering a
// page of chips would otherwise re-resolve every root for every chip; the handler
// still resolves at action time, so a stale rendered button can never authorise
// anything the fresh check would refuse.
//
// A root that does not resolve (missing) or does not stat as a DIRECTORY is
// dropped: it cannot contain anything.
func resolveRoots(roots []string) []string {
	out := make([]string, 0, len(roots))
	for _, root := range roots {
		if strings.TrimSpace(root) == "" {
			continue
		}
		real, err := filepath.EvalSymlinks(root)
		if err != nil || !isRealDir(real) {
			continue
		}
		out = append(out, real)
	}
	return out
}

// isRealDir reports whether path exists and is a DIRECTORY. os.Stat follows
// symlinks, so a link to a regular file is correctly not a directory.
func isRealDir(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

// containedDirIn is containedDir against ALREADY-RESOLVED roots.
func containedDirIn(path string, realRoots []string) (string, bool) {
	path = strings.TrimSpace(path)
	if path == "" || !filepath.IsAbs(path) {
		return "", false
	}
	dir, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return "", false
	}
	// 🔴 The resolved leaf must actually BE a directory. EvalSymlinks succeeds on
	// ANY existing path, so without this a local_files row of
	// <root>/notadir/model.safetensors — where <root>/notadir is a REGULAR FILE —
	// resolved to that file, passed containment, and was handed to the opener.
	// xdg-open/open on a FILE launches that file's REGISTERED APPLICATION instead
	// of a file browser, which is the only path in this design where the impact
	// rises above "a file-manager window opened". Refusing here means the chip's
	// folder button does not render AND a forged click is refused — the same
	// behaviour an uncontained path already gets.
	if !isRealDir(dir) {
		return "", false
	}
	for _, realRoot := range realRoots {
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

	// TOCTOU narrowing, not elimination. containedDir already stat'd this path, but
	// this is the LAST filesystem sample taken before the exec: everything between
	// here and cmd.Start() (fileManagerArgv, openerCommand) is pure string work
	// that touches no filesystem, so re-sampling here is the closest a portable Go
	// program can get to "check at exec time". It still cannot be atomic — an
	// fd-based open (openat/O_DIRECTORY + fexecve-style handoff) is what would make
	// it so, and the stdlib exposes no portable equivalent. What remains is the
	// window between this stat and the kernel resolving the path inside the opener
	// process, during which a local attacker with write access to a library root
	// could swap the leaf. That requires local write access to a directory the user
	// configured as their own model library.
	if !isRealDir(dir) {
		s.render(w, http.StatusOK, s.revealResult(id, "",
			"That folder is no longer a folder on disk, so it will not be opened.", "error"))
		return
	}

	argv, ok := fileManagerArgv(dir)
	if !ok {
		s.render(w, http.StatusOK, s.revealResult(id, "",
			"Opening a folder is not supported on this platform.", "error"))
		return
	}

	// 🔴 REFUSE BEFORE LAUNCHING WHEN NO WINDOW COULD APPEAR. This check is the
	// whole reason hasGraphicalSession exists: without it the handler shells out
	// into a headless session, xdg-open exits 0 having done nothing, and the
	// response claims a window opened. Say what is actually true instead, and name
	// the folder anyway — it is the fact the user can still act on (copy the path,
	// open a terminal there).
	if !s.graphical()() {
		s.render(w, http.StatusOK, s.revealResult(id, dir,
			"The computer running civitai-manager has no graphical session "+
				"(no DISPLAY or WAYLAND_DISPLAY), so no file-manager window can open there. "+
				"The folder is:", "error"))
		return
	}

	if err := s.opener()(argv); err != nil {
		s.log.Warn("open containing folder failed", "file_id", id, "opener", argv[0], "err", err)
		s.render(w, http.StatusOK, s.revealResult(id, "",
			"Could not start "+argv[0]+" on the computer running civitai-manager.", "error"))
		return
	}
	// 🔴 THE WORDING IS HEDGED ON PURPOSE — DO NOT PROMOTE IT BACK TO "Opened".
	// All that has happened here is that the opener PROCESS STARTED (startFileManager
	// reports from cmd.Start() and never waits), and even its eventual exit code
	// would not settle the question: xdg-open exits 0 with no handler installed at
	// all. Claiming "Opened" from that is a false success, and a false success is
	// worse than an error because the user goes hunting for a window that does not
	// exist. What we can honestly assert is that we ASKED the desktop to open it —
	// so that is what it says, and it still names the folder so the claim is
	// checkable.
	s.render(w, http.StatusOK, s.revealResult(id, dir,
		"Asked the desktop on the computer running civitai-manager to open this folder.", "ok"))
}

// revealResult re-renders the control with an outcome message. It names the
// folder whenever one was resolved, so every statement it makes is checkable
// against something the user can see.
func (s *Server) revealResult(id int64, dir, msg, state string) g.Node {
	if dir != "" {
		msg = msg + " (" + dir + ")"
	}
	return resourceOpenControl(id, s.csrf, msg, state)
}
