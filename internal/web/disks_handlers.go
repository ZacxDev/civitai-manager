package web

import (
	"errors"
	"io/fs"
	"net/http"
	"path/filepath"
	"syscall"

	"github.com/ZacxDev/civitai-manager/internal/diskusage"
	"github.com/ZacxDev/civitai-manager/internal/library"
)

// handleDisks renders /disks: filesystem capacity for every configured model
// directory plus the quarantine batches /trash used to show.
//
// 🔴 LOOPBACK-GATED, THROUGH THE SAME PREDICATE AS /library/browse AND
// /library/scan — s.extraPathsAllowed(), the app's ONE answer to "is this a
// local single-user surface". It is not a second mechanism: the browse/discover
// endpoints call it via s.gate(w), which additionally renders a fragment, and
// this handler needs a full PAGE instead, so it consults the predicate directly
// and hands the result to disksPage. The wording is the same string, so the
// shared gate-message assertion covers both.
//
// The gate covers the CAPACITY half only, and the page still renders. That is a
// deliberate scoping call with a real trade-off, recorded here because it is not
// obvious: the paths are the sensitive part (an unauthenticated remote caller
// learning the operator's directory layout), while the quarantine table exposes
// batch ids, timestamps and file COUNTS — no paths — and POST /trash/{id}/restore
// was never gated. Gating the whole page would therefore have REMOVED a working
// capability from a non-loopback bind (restore) to protect data that half of the
// page does not carry. Off-loopback: no path, no capacity, no probe — the
// syscalls are not even issued, because the rows are never collected. That last
// claim is pinned by TestDisksRowsAreNotEvenProbedOffLoopback, which counts at
// Server.diskStatFn rather than inspecting the markup.
//
// CAVEAT, PRE-EXISTING AND SHARED WITH /library/browse: extraPathsAllowed keys
// on the server's own BIND ADDRESS, not on the peer. A loopback-bound instance
// behind a reverse proxy therefore looks local to every remote caller the proxy
// forwards, and this page's paths are exposed. Deploying that way is outside the
// app's single-user-local design; do not "fix" it here in isolation, since the
// predicate governs several endpoints at once.
//
// GET, read-only, no state change -> no CSRF.
func (s *Server) handleDisks(w http.ResponseWriter, r *http.Request) {
	batches, err := s.loadBatchViews()
	if err != nil {
		s.renderError(w, "load quarantine batches", err)
		return
	}
	gated := !s.extraPathsAllowed()
	var rows []diskRow
	if !gated {
		rows = s.diskRows()
	}
	s.render(w, http.StatusOK, disksPage(rows, batches, gated, s.csrf, s.currentTheme(), s.maturity(), s.rail(r.Context())))
}

// handleTrashRedirect keeps the old /trash bookmark working after the nav rework
// folded that page into /disks.
//
// 302 FOUND, NOT 301 MOVED PERMANENTLY — chosen deliberately. A 301 is cached by
// browsers INDEFINITELY and is notoriously hard for a user to clear; if /trash
// ever needs to mean something again (a real delete surface, say), every user who
// visited it once would keep being bounced to /disks by their own cache with no
// request reaching the server. A 302 costs one redirect per visit and stays
// reversible, which is the right trade for a URL inside a single-user local app
// where there is no SEO or link-equity argument for a 301.
//
// Only the GET moves. POST /trash/{id}/restore is UNCHANGED and still handled by
// handleRestore: it is the action the quarantine table's buttons issue, a
// redirect would turn an htmx POST into a fragment-less GET, and relocating a
// working CSRF-protected endpoint buys nothing.
func (s *Server) handleTrashRedirect(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/disks", http.StatusFound)
}

// diskRows collects the directories this app manages and probes each one's
// filesystem. Callers MUST have checked the loopback gate first — the returned
// rows carry absolute paths.
//
// The set is exactly the directories the app itself reads or writes: the model
// root, the configured extra library paths, the user's selected scan
// directories, and the quarantine (trash) directory. Nothing here walks the
// filesystem or accepts a path from the request: every entry comes from config
// or from the store, so this cannot become an arbitrary-path primitive.
//
// FAILURE IS PER-ROW. One unreadable path must not cost the page, so a failed
// probe becomes a row with an unknown Usage and a reason string. A store read
// that fails contributes no rows at all rather than failing the request — the
// selected-scan-dirs list is an enhancement over the configured ones.
func (s *Server) diskRows() []diskRow {
	// lazy marks a directory this app CREATES ON DEMAND rather than one the user is
	// expected to already have. It changes nothing but the not-exist wording — see
	// diskErrText.
	type entry struct {
		label, path string
		lazy        bool
	}
	var entries []entry
	add := func(label, path string) {
		if path != "" {
			entries = append(entries, entry{label: label, path: path})
		}
	}
	addLazy := func(label, path string) {
		if path != "" {
			entries = append(entries, entry{label: label, path: path, lazy: true})
		}
	}
	add("Model root", s.cfg.ModelRoot)
	for _, p := range s.cfg.LibraryPaths {
		add("Library path", p)
	}
	if sel, err := s.store.ListScanDirs(); err == nil {
		for _, p := range sel {
			add("Scan directory", p)
		}
	}
	// 🔴 THE TRASH DIR MUST BE RESOLVED, NOT READ RAW. `trash_dir` is unset on
	// essentially every install, and the real quarantine target is derived
	// (<ModelRoot>/.trash) inside library.NewScanner — so `add("Trash",
	// s.cfg.TrashDir)` silently contributed NO row on a default config while this
	// doc claimed the set included "the quarantine (trash) directory".
	// library.ResolveTrashDir is that derivation, called rather than restated, so
	// the row can never name a directory quarantine would not actually use.
	//
	// The DERIVED dir is created LAZILY — quarantine.go makes it on the first move —
	// so on a fresh install it does not exist and probing it rendered a warning-shaped
	// "⚠ Capacity unknown — the directory does not exist or is not mounted". That is
	// wrong twice: nothing is broken, and since it sits UNDER ModelRoot it could never
	// report a capacity the "Model root" row above does not already show. So the
	// derived case is marked lazy and reads "not created yet" instead. An EXPLICITLY
	// configured trash_dir stays a real warning when it is missing — the user named
	// that directory, it may be on another filesystem, and its absence is worth
	// surfacing.
	if trash := library.ResolveTrashDir(s.cfg.TrashDir, s.cfg.ModelRoot); s.cfg.TrashDir != "" {
		add("Trash", trash)
	} else {
		addLazy("Trash", trash)
	}

	statFn := s.diskStatFn
	if statFn == nil {
		statFn = diskusage.Stat
	}

	rows := make([]diskRow, 0, len(entries))
	// Dedupe on the CLEANED path so "/models" and "/models/" are one row. This is
	// deliberately NOT a same-filesystem dedupe: two distinct directories that
	// happen to share a disk stay two rows (the page says so in its footnote),
	// because merging them would need device ids that only exist on unix and would
	// wrongly merge two identically-sized empty volumes.
	seen := make(map[string]bool, len(entries))
	for _, e := range entries {
		clean := filepath.Clean(e.path)
		if seen[clean] {
			continue
		}
		seen[clean] = true
		row := diskRow{Label: e.label, Path: e.path}
		u, err := statFn(clean)
		if err != nil {
			row.Err = diskErrText(err, e.lazy)
		} else {
			row.Usage = u
		}
		rows = append(rows, row)
	}
	return rows
}

// diskErrText turns a probe failure into short human text for the row.
//
// It deliberately does NOT surface the raw syscall error: on unix that is
// "statfs /some/path: no such file or directory", which repeats the path already
// shown beside it and leaks the errno wording into the UI. The two cases a user
// can act on are "the directory is not there" and "this build cannot measure it";
// anything else falls through to the error's own text, since inventing a
// friendly message for an unknown failure would hide the only clue.
//
// lazy flips ONLY the not-exist case. A directory this app creates on demand is
// not a fault when it is absent, and dressing it as one trains the user to ignore
// the warning that does matter.
func diskErrText(err error, lazy bool) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, fs.ErrNotExist), errors.Is(err, syscall.ENOENT):
		if lazy {
			return "not created yet — it appears the first time something is quarantined"
		}
		return "the directory does not exist or is not mounted"
	case errors.Is(err, diskusage.ErrUnsupported):
		return "capacity reporting is not available on this platform"
	default:
		return err.Error()
	}
}
