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
//
// 🔴 A FIXED PATH BUYS A DELETION HAZARD THAT MkdirTemp STRUCTURALLY COULD NOT HAVE.
// This file wipes its work dir twice per run (once to start clean, once on release),
// and with the path now coming from an env var an operator can aim those wipes at
// anything. Two guards below are load-bearing, not defensive padding:
//
//   - requireOwnedWorkDir — an EXISTING directory is wiped only when it carries this
//     harness's own marker file, or is empty. Anything else is refused untouched.
//   - validateWorkDirPath — the path must be absolute and must not be a filesystem
//     root, so a typo cannot resolve to something catastrophic.
//
// Both exist because the refusal message deliberately TELLS the operator to set
// UXAUDIT_WORKDIR, and it does so at the exact moment they are blocked and improvising
// — which is the worst moment to hand someone an unguarded rm -rf.

// workDirName is the fixed basename of the walk's work dir under os.TempDir().
const workDirName = "uxaudit-walk"

// workDirMarker is written into the work dir at creation and is the ONLY proof that
// an existing directory belongs to this harness and may be wiped. Its name is
// deliberately specific enough that no ordinary directory carries one by accident.
const workDirMarker = ".uxaudit-workdir"

// WorkDirEnv overrides the work dir location. It exists for the ONE hazard a fixed
// path creates: two walks running at once would otherwise share a SQLite file, a
// seeded models tree and a deferred RemoveAll. The lock below refuses that case
// outright; this env var is how the second run gets its own home instead.
//
// ⚠ KNOWN CONSEQUENCE, accepted rather than hidden: a walk run with this set produces
// a `library-sources` capture naming THAT path, so it cannot be diffed against a
// default-path run. The workaround un-does the determinism it works around. Use it for
// a second concurrent walk, not for the run you intend to diff.
const WorkDirEnv = "UXAUDIT_WORKDIR"

// workDirPath is the deterministic work-dir path for this run. It reads the
// environment but is otherwise PURE — no filesystem side effects, no randomness — so
// calling it twice in a row must yield the same string. That is the property
// TestWalkWorkDirPathIsDeterministic pins, and it is exactly what MkdirTemp violated.
//
// The result is always filepath.Clean'd. That is not cosmetic: acquireWorkDir derives
// the lock path as a SIBLING by appending ".lock", and an un-cleaned trailing slash
// would put the lock INSIDE the work dir, where the very next RemoveAll deletes it —
// silently defeating the lock. See TestWalkWorkDirLockIsASiblingNotAChild.
func workDirPath() (string, error) {
	raw := strings.TrimSpace(os.Getenv(WorkDirEnv))
	if raw == "" {
		return filepath.Clean(filepath.Join(os.TempDir(), workDirName)), nil
	}
	return validateWorkDirPath(raw)
}

// validateWorkDirPath cleans an operator-supplied work dir and refuses the shapes that
// would turn this file's two RemoveAll calls into something destructive.
//
// It requires an ABSOLUTE path. A relative one is refused rather than resolved,
// because what it resolves to depends on the caller's cwd — and `make ux-audit` cds
// into e2e/uxaudit, so the same string means different directories depending on how
// the walk was started. A wipe target must not be ambiguous.
func validateWorkDirPath(raw string) (string, error) {
	clean := filepath.Clean(raw)
	if !filepath.IsAbs(clean) {
		return "", fmt.Errorf("%s=%q is not an absolute path. It names a directory this harness "+
			"WIPES at the start of every run, so it must not depend on the working directory "+
			"(`make ux-audit` runs from e2e/uxaudit, not the repo root)", WorkDirEnv, raw)
	}
	// A path whose parent is itself is a filesystem root ("/", or "C:\" on Windows).
	if parent := filepath.Dir(clean); parent == clean {
		return "", fmt.Errorf("%s=%q resolves to the filesystem root %q, which this harness would "+
			"wipe. Point it at a dedicated directory", WorkDirEnv, raw, clean)
	}
	return clean, nil
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
// A crashed run leaves a stale lock, and `defer` does not run on SIGINT, so a
// Ctrl-C'd walk leaves one too. That is deliberate: an automatic staleness heuristic
// (age, pid liveness) is exactly the kind of guess that deletes a running walk's data.
// The refusal instead PRINTS the lock's recorded pid and start time and states both
// possibilities, so the operator decides.
func acquireWorkDir() (dir string, release func(), err error) {
	dir, err = workDirPath()
	if err != nil {
		return "", nil, err
	}
	// 🔴 A SIBLING, via the already-cleaned dir — never filepath.Join(dir, ...). With a
	// trailing slash in the env var, string concatenation would yield "<dir>/.lock",
	// i.e. a lock INSIDE the tree the next RemoveAll deletes, so a second acquire would
	// succeed and two walks would share one SQLite file. workDirPath cleans; this
	// depends on that.
	lockPath := dir + ".lock"

	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return "", nil, fmt.Errorf(
				"the ux-audit work dir %s is locked (%s).\n%s"+
					"Either another walk is running, or a previous one was interrupted "+
					"(Ctrl-C and a crash both leave the lock behind — nothing reaps it "+
					"automatically, because guessing wrong deletes a live run's data).\n"+
					"  • to run a SECOND walk concurrently: %s=<some other absolute dir>\n"+
					"  • if no walk is running: rm %s",
				dir, lockPath, describeLock(lockPath), WorkDirEnv, lockPath)
		}
		return "", nil, fmt.Errorf("create work-dir lock %s: %w", lockPath, err)
	}
	fmt.Fprintf(f, "pid %d\nstarted %s\n", os.Getpid(), time.Now().UTC().Format(time.RFC3339))
	_ = f.Close()

	unlock := func() { _ = os.Remove(lockPath) }

	// 🔴 Refuse to wipe a directory that is not ours. Checked BEFORE the RemoveAll, and
	// re-checked before the one in release().
	if err := requireOwnedWorkDir(dir); err != nil {
		unlock()
		return "", nil, err
	}

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
	if err := os.WriteFile(filepath.Join(dir, workDirMarker), []byte(workDirMarkerBody()), 0o644); err != nil {
		unlock()
		return "", nil, fmt.Errorf("write work-dir marker in %s: %w", dir, err)
	}

	return dir, func() {
		// 🔴 RE-CHECK immediately before the destructive step, not once at acquire time.
		// The ownership fact was measured ~60s ago; between then and here the operator
		// may have replaced the directory. A stale observation is a hypothesis about
		// now, and the check is cheap.
		if err := requireOwnedWorkDir(dir); err == nil {
			_ = os.RemoveAll(dir)
		}
		unlock()
	}, nil
}

// requireOwnedWorkDir reports whether dir may be wiped: it must not exist, or it must
// be a directory that either carries workDirMarker or is empty.
//
// An empty directory is allowed because that is what an operator improvising a second
// walk actually does (`mkdir /tmp/wd2`), and removing an empty directory destroys
// nothing. Anything else — a home directory, a source tree, a downloads folder — is
// refused with its own contents named, untouched.
func requireOwnedWorkDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil // nothing there to destroy
	}
	if err != nil {
		// A non-directory (ENOTDIR) lands here too, and must never be wiped.
		return fmt.Errorf("cannot inspect the ux-audit work dir %s before wiping it: %w", dir, err)
	}
	if len(entries) == 0 {
		return nil
	}
	for _, e := range entries {
		if e.Name() == workDirMarker {
			return nil
		}
	}
	names := make([]string, 0, 4)
	for _, e := range entries {
		if len(names) == 4 {
			names = append(names, "…")
			break
		}
		names = append(names, e.Name())
	}
	return fmt.Errorf(
		"refusing to wipe %s: it is not empty and carries no %s marker, so it was not created "+
			"by this harness (contains: %s).\n"+
			"The ux-audit walk DELETES its work dir at the start of every run and again at the "+
			"end. Point %s at a dedicated or non-existent directory",
		dir, workDirMarker, strings.Join(names, ", "), WorkDirEnv)
}

func workDirMarkerBody() string {
	return fmt.Sprintf("civitai-manager ux-audit walk work dir.\n"+
		"Created by pid %d at %s.\n"+
		"This directory is DELETED and recreated on every walk.\n",
		os.Getpid(), time.Now().UTC().Format(time.RFC3339))
}

// describeLock renders the holder recorded inside a lock file, so the refusal tells the
// operator WHO holds it instead of only that someone does. Unreadable or empty content
// yields "" rather than a lie about the holder.
func describeLock(lockPath string) string {
	b, err := os.ReadFile(lockPath)
	if err != nil {
		return ""
	}
	body := strings.TrimSpace(string(b))
	if body == "" {
		return ""
	}
	return "The lock records: " + strings.ReplaceAll(body, "\n", "; ") + ".\n"
}
