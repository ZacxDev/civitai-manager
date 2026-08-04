package uxaudit

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The walk's work dir is DETERMINISTIC, and that is a correctness property of the
// artifacts, not a tidiness preference.
//
// 🔴 It used to be os.MkdirTemp("", "uxaudit-walk-"). boot.go derives scanDir as
// <workDir>/models and persists it with AddScanDir, and the library Sources tab
// renders every selected scan directory through selectedDirsList — which emits the
// raw path FOUR times per entry (h.Value, h.Title, the visible label and the hx-vals
// JSON) and lands it in the a11y digest as the checkbox's accessible_name. So the
// `library-sources` capture embedded a fresh random path on every run and two walks
// of the SAME TREE differed by construction.
//
// Measured on this repo at 52cb872, two full walks, 104 artifacts each: exactly FOUR
// files differed — library-sources.{mobile,desktop}.{png,a11y.json} — and the digest
// diff was literally
//
//	"accessible_name": "/tmp/uxaudit-walk-1579615661/modelsremove"
//	"accessible_name": "/tmp/uxaudit-walk-2482708266/modelsremove"
//
// Nothing else moved, so the whole instability is this one path.
//
// 🔴 THE FIX BELONGS TO THE HARNESS, NOT TO THE APP. Masking or truncating the path
// in selectedDirsList would hide a real user's real directory to make a test stable —
// the wrong trade in the wrong place. The harness owns the path, so the harness makes
// it stable.

// workDirName is the fixed basename of the walk's work dir under os.TempDir().
const workDirName = "uxaudit-walk"

// WorkDirEnv overrides the work dir location. It exists for the ONE hazard a fixed
// path creates: two walks running at once would otherwise share a SQLite file, a
// seeded models tree and a deferred RemoveAll. The lock below refuses that case
// outright; this env var is how the second run gets its own home instead.
const WorkDirEnv = "UXAUDIT_WORKDIR"

// workDirPath is the deterministic work-dir path for this run. It reads the
// environment but is otherwise PURE — no filesystem side effects, no randomness — so
// calling it twice in a row must yield the same string. That is the property
// TestWalkWorkDirPathIsDeterministic pins, and it is exactly what MkdirTemp violated.
func workDirPath() string {
	if p := strings.TrimSpace(os.Getenv(WorkDirEnv)); p != "" {
		return p
	}
	return filepath.Join(os.TempDir(), workDirName)
}

// acquireWorkDir takes exclusive ownership of the deterministic work dir and returns
// it plus a release func the caller MUST defer.
//
// The exclusivity is a real lock file created with O_EXCL, not an advisory
// convention: a fixed path means a second concurrent walk would boot a second store
// over the same SQLite file, re-seed the same models tree underneath the first, and
// then RemoveAll it out from under a live run. Failing loudly with a message naming
// both the lock and the override is the only honest option — a silent fallback to a
// random dir would quietly restore the very instability this file removes.
//
// A crashed run leaves a stale lock. That is deliberate: an automatic staleness
// heuristic (age, pid liveness) is exactly the kind of guess that deletes a running
// walk's data, and the recovery is one `rm` the error message spells out.
func acquireWorkDir() (dir string, release func(), err error) {
	dir = workDirPath()
	lockPath := dir + ".lock"

	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return "", nil, fmt.Errorf(
				"another ux-audit walk holds %s (lock: %s).\n"+
					"The walk's work dir is deterministic so the library-sources capture is a stable "+
					"visual baseline, which means two concurrent walks cannot share it.\n"+
					"Run the second walk with %s=<some other dir>, or if no walk is running the lock is "+
					"stale from a crashed run: rm %s",
				dir, lockPath, WorkDirEnv, lockPath)
		}
		return "", nil, fmt.Errorf("create work-dir lock %s: %w", lockPath, err)
	}
	fmt.Fprintf(f, "pid %d\nstarted %s\n", os.Getpid(), time.Now().UTC().Format(time.RFC3339))
	_ = f.Close()

	unlock := func() { _ = os.Remove(lockPath) }

	// Start from a clean tree. A previous run normally removes it, but a crashed one
	// does not, and reusing its SQLite file would seed a second copy of every fixture.
	if err := os.RemoveAll(dir); err != nil {
		unlock()
		return "", nil, fmt.Errorf("clear work dir %s: %w", dir, err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		unlock()
		return "", nil, fmt.Errorf("create work dir %s: %w", dir, err)
	}

	return dir, func() {
		_ = os.RemoveAll(dir)
		unlock()
	}, nil
}
