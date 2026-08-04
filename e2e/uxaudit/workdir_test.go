package uxaudit

import (
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// This file is the browserless rot-guard for the walk's DETERMINISTIC work dir —
// the same shape, and for the same reason, as walk_selectors_test.go: `make ux-audit`
// is double-gated out of `go test ./...`, so anything only the browser walk can
// observe rots silently. These need no browser and no network.
//
// What is being protected: <workDir>/models is persisted as a scan directory and
// rendered verbatim on /library?tab=sources, so a random work dir makes the
// library-sources capture differ between two runs of the same tree. See workdir.go.

// TestWalkWorkDirPathIsDeterministic is the guard proper. workDirPath is pure, so two
// calls in one process MUST agree — which is precisely what os.MkdirTemp cannot do.
//
// It asserts the value as well as the agreement, because "two calls agree" alone is
// satisfied by a path memoised at init from a random seed: that would be stable within
// one process and different in the NEXT run, which is the run pair that matters.
func TestWalkWorkDirPathIsDeterministic(t *testing.T) {
	// Empty means "not set" to workDirPath, so this exercises the DEFAULT branch — the
	// one the walk actually takes — without touching the ambient environment.
	t.Setenv(WorkDirEnv, "")

	first, err := workDirPath()
	if err != nil {
		t.Fatalf("workDirPath: %v", err)
	}
	second, err := workDirPath()
	if err != nil {
		t.Fatalf("workDirPath (second call): %v", err)
	}
	if first != second {
		t.Fatalf("workDirPath() returned %q then %q — the walk's work dir is not deterministic, "+
			"so the library-sources capture embeds a different path on every run and cannot be "+
			"diffed as a visual baseline", first, second)
	}
	if want := filepath.Join(os.TempDir(), workDirName); first != want {
		t.Errorf("workDirPath() = %q, want %q — a per-process value is stable WITHIN a run and "+
			"still different in the next one, which is the pair the baseline is diffed across",
			first, want)
	}
	// Fixture reached the interesting case: a path with no directory component would
	// make the comparison above trivially true for the wrong reason.
	if !filepath.IsAbs(first) {
		t.Errorf("workDirPath() = %q, want an absolute path", first)
	}
}

func TestWalkWorkDirEnvOverrideWins(t *testing.T) {
	custom := filepath.Join(t.TempDir(), "elsewhere")
	t.Setenv(WorkDirEnv, custom)
	got, err := workDirPath()
	if err != nil {
		t.Fatalf("workDirPath: %v", err)
	}
	if got != custom {
		t.Fatalf("workDirPath() = %q, want the %s override %q — without a working override there "+
			"is no way to run two walks at once, and the lock below would be a hard block rather "+
			"than a redirect", got, WorkDirEnv, custom)
	}
}

// TestWalkWorkDirRefusesAConcurrentRun pins the hazard a FIXED path creates. Two
// concurrent walks would boot two stores over one SQLite file, re-seed one models
// tree, and RemoveAll it out from under each other. The lock must refuse the second
// one loudly rather than silently falling back to a random dir (which would restore
// the instability) or silently sharing (which would corrupt the run).
//
// It also asserts release() FREES the lock. A one-shot lock would make the second
// walk on a machine fail forever, which is a worse bug than the one being fixed.
func TestWalkWorkDirRefusesAConcurrentRun(t *testing.T) {
	// Never the real default path: `go test ./...` must not be able to collide with a
	// `make ux-audit` running beside it.
	t.Setenv(WorkDirEnv, filepath.Join(t.TempDir(), "wd"))

	dir, release, err := acquireWorkDir()
	if err != nil {
		t.Fatalf("first acquireWorkDir: %v", err)
	}
	// Fixture reached the interesting case: the dir really exists and really is locked,
	// so the refusal below is about contention and not about a failed setup.
	if st, serr := os.Stat(dir); serr != nil || !st.IsDir() {
		t.Fatalf("acquireWorkDir returned %q which is not a directory (%v)", dir, serr)
	}
	if _, lerr := os.Stat(dir + ".lock"); lerr != nil {
		t.Fatalf("acquireWorkDir left no lock file at %s: %v", dir+".lock", lerr)
	}

	if _, _, err2 := acquireWorkDir(); err2 == nil {
		t.Error("a SECOND acquireWorkDir succeeded while the first still held the dir — two " +
			"concurrent walks would share one SQLite file and RemoveAll each other's fixtures")
	} else if !strings.Contains(err2.Error(), WorkDirEnv) {
		t.Errorf("the refusal does not name %s, so the operator is told to stop rather than how "+
			"to run two walks: %v", WorkDirEnv, err2)
	}

	release()
	if _, serr := os.Stat(dir); !os.IsNotExist(serr) {
		t.Errorf("release() left %q behind: %v", dir, serr)
	}

	// The lock is reusable, not one-shot.
	_, release2, err3 := acquireWorkDir()
	if err3 != nil {
		t.Fatalf("acquireWorkDir after release: %v — release() did not free the lock", err3)
	}
	release2()
}

// TestWalkWorkDirRefusesToWipeADirectoryItDoesNotOwn is the guard for the deletion
// hazard a FIXED path buys and MkdirTemp structurally could not have: this file wipes
// its work dir twice per run, and the path now comes from an operator-supplied env var
// that the lock's own refusal message tells them to set.
//
// Verified as a real hazard, not a hypothetical: before requireOwnedWorkDir existed,
// pointing UXAUDIT_WORKDIR at a directory containing a file DELETED that file at
// acquire time.
func TestWalkWorkDirRefusesToWipeADirectoryItDoesNotOwn(t *testing.T) {
	victim := filepath.Join(t.TempDir(), "not-ours")
	if err := os.MkdirAll(victim, 0o755); err != nil {
		t.Fatal(err)
	}
	precious := filepath.Join(victim, "thesis.txt")
	if err := os.WriteFile(precious, []byte("years of work"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(WorkDirEnv, victim)

	_, release, err := acquireWorkDir()
	if err == nil {
		release()
		t.Fatal("acquireWorkDir ACCEPTED a directory it did not create — it wipes that directory " +
			"twice per run, so this is operator data loss")
	}
	// The refusal must name the marker, or the operator cannot tell what would make the
	// directory acceptable.
	if !strings.Contains(err.Error(), workDirMarker) {
		t.Errorf("the refusal does not name the %s marker: %v", workDirMarker, err)
	}

	// 🔴 The assertion that matters: the file is STILL THERE. "It returned an error" is
	// not the property under test — "it deleted nothing" is.
	if b, rerr := os.ReadFile(precious); rerr != nil || string(b) != "years of work" {
		t.Fatalf("acquireWorkDir destroyed operator data it refused to accept: %v / %q", rerr, b)
	}
	// And it must not have left a lock file behind for a run it refused to start.
	if _, lerr := os.Stat(victim + ".lock"); !os.IsNotExist(lerr) {
		t.Errorf("a refused acquire left its lock file at %s — the next run would report a "+
			"phantom concurrent walk", victim+".lock")
	}
}

// TestWalkWorkDirAcceptsAnEmptyOrOwnedDirectory is the other half: the refusal above
// must not be so broad that the harness cannot run. Without this, "refuses everything"
// would satisfy the guard above and the walk would never start.
func TestWalkWorkDirAcceptsAnEmptyOrOwnedDirectory(t *testing.T) {
	base := t.TempDir()

	t.Run("does not exist", func(t *testing.T) {
		t.Setenv(WorkDirEnv, filepath.Join(base, "fresh"))
		_, release, err := acquireWorkDir()
		if err != nil {
			t.Fatalf("a non-existent work dir was refused: %v", err)
		}
		release()
	})

	t.Run("exists and is empty", func(t *testing.T) {
		empty := filepath.Join(base, "empty")
		if err := os.MkdirAll(empty, 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv(WorkDirEnv, empty)
		_, release, err := acquireWorkDir()
		if err != nil {
			t.Fatalf("an empty work dir was refused — that is what an operator improvising a "+
				"second concurrent walk actually creates: %v", err)
		}
		release()
	})

	t.Run("carries our marker and real content", func(t *testing.T) {
		owned := filepath.Join(base, "owned")
		if err := os.MkdirAll(owned, 0o755); err != nil {
			t.Fatal(err)
		}
		// A crashed run's leftovers: our marker plus a stale DB.
		if err := os.WriteFile(filepath.Join(owned, workDirMarker), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		stale := filepath.Join(owned, "uxaudit.db")
		if err := os.WriteFile(stale, []byte("stale"), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Setenv(WorkDirEnv, owned)
		dir, release, err := acquireWorkDir()
		if err != nil {
			t.Fatalf("a marked work dir left by a crashed run was refused: %v", err)
		}
		defer release()
		// Fixture reached the interesting case: the stale DB really was cleared, which is
		// the whole reason acquire wipes rather than reuses.
		if _, serr := os.Stat(stale); !os.IsNotExist(serr) {
			t.Errorf("the stale %s survived acquire — a second copy of every fixture would be "+
				"seeded on top of it", stale)
		}
		// And the fresh dir carries a marker, or the NEXT run would refuse it.
		if _, merr := os.Stat(filepath.Join(dir, workDirMarker)); merr != nil {
			t.Errorf("acquire did not write the %s marker, so the next run would refuse this "+
				"directory as foreign: %v", workDirMarker, merr)
		}
	})
}

// TestWalkWorkDirPathRejectsUnsafeShapes pins the second half of the deletion guard:
// the path itself. A relative path means a different directory depending on where the
// walk was started (`make ux-audit` cds into e2e/uxaudit), and a filesystem root is
// never a work dir.
func TestWalkWorkDirPathRejectsUnsafeShapes(t *testing.T) {
	for _, tc := range []struct {
		name string
		val  string
	}{
		{"relative", "scratch"},
		{"relative dot-slash", "./scratch"},
		{"parent-relative", "../scratch"},
		{"bare dot", "."},
		{"filesystem root", "/"},
		{"root via traversal", "/tmp/.."},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(WorkDirEnv, tc.val)
			got, err := workDirPath()
			if err == nil {
				t.Fatalf("%s=%q was ACCEPTED, resolving to %q — this harness wipes that path twice "+
					"per run", WorkDirEnv, tc.val, got)
			}
		})
	}
}

// TestWalkWorkDirLockIsASiblingNotAChild is the guard for the trailing-slash escape.
//
// The lock path is derived by appending ".lock" to the work dir. If the work dir were
// not cleaned first, UXAUDIT_WORKDIR=/path/wd/ would put the lock at /path/wd/.lock —
// INSIDE the tree acquireWorkDir wipes three lines later — so the lock would delete
// itself and a second concurrent walk would be admitted. That is exactly the
// two-walks-share-one-SQLite-file hazard the lock exists to prevent, reachable through
// the one escape hatch the lock's own error message advertises.
//
// The fixture carries the trailing slash deliberately: without it this test passes
// against the broken implementation.
//
// 🔴 It also PRE-CREATES the directory, and that is load-bearing. Against the broken
// implementation an absent directory makes the lock creation fail with ENOENT — a red,
// but for the wrong reason, and one that would disappear the moment the directory
// happened to exist. Pre-creating it lets the lock be created INSIDE the work dir,
// which is the real hazard: the wipe then deletes the lock and the second acquire is
// admitted. Measured: with the directory absent the mutant fails on "no such file or
// directory"; with it present the mutant reaches the assertion this test is about.
func TestWalkWorkDirLockIsASiblingNotAChild(t *testing.T) {
	base := t.TempDir()
	dirNoSlash := filepath.Join(base, "wd")
	if err := os.MkdirAll(dirNoSlash, 0o755); err != nil {
		t.Fatal(err)
	}
	// 🔴 The marker is load-bearing for the same reason as pre-creating the directory.
	// Without it, a broken (un-cleaned) implementation puts the lock inside the dir, and
	// requireOwnedWorkDir then refuses the whole acquire — so this test goes red with a
	// DIFFERENT guard's error and would pass with the sibling derivation still broken.
	// Marking the directory as ours lets the acquire proceed, so the assertions below
	// are the ones that fail.
	if err := os.WriteFile(filepath.Join(dirNoSlash, workDirMarker), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	withSlash := dirNoSlash + string(os.PathSeparator)
	t.Setenv(WorkDirEnv, withSlash)

	_, release, err := acquireWorkDir()
	if err != nil {
		t.Fatalf("acquireWorkDir with a trailing slash: %v", err)
	}
	defer release()

	// Both paths are derived from the KNOWN-clean fixture, never from acquireWorkDir's
	// return value — a broken implementation returns the un-cleaned path, and deriving
	// the expectation from it would make expectation and subject move together.
	if _, serr := os.Stat(filepath.Join(dirNoSlash, ".lock")); serr == nil {
		t.Errorf("the lock landed INSIDE the work dir (%s) — acquire's RemoveAll deletes it, so "+
			"the lock cannot hold", filepath.Join(dirNoSlash, ".lock"))
	}
	if _, serr := os.Stat(dirNoSlash + ".lock"); serr != nil {
		t.Errorf("no sibling lock at %s: %v", dirNoSlash+".lock", serr)
	}

	// 🔴 The behavioural half, and the one that actually fails on the broken version: a
	// second acquire must still be refused.
	if _, release2, err2 := acquireWorkDir(); err2 == nil {
		release2()
		t.Error("a SECOND acquireWorkDir succeeded for a work dir given with a trailing slash — " +
			"the lock was wiped by its own work-dir cleanup, so two walks would share one " +
			"SQLite file")
	}
}

// TestWalkAcquiresTheDeterministicWorkDir closes the SEAM the two tests above cannot
// see: they prove workDirPath/acquireWorkDir are deterministic, not that Walk USES
// them. Walk is browser-only, so no ordinary test can observe its choice at runtime —
// this reads the source instead.
//
// It is deliberately structural rather than spelled: the assertion is "no
// os.MkdirTemp call exists in this package's non-test source, and Walk calls
// acquireWorkDir", not a substring hunt for a particular line.
func TestWalkAcquiresTheDeterministicWorkDir(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", nonTestGoFile, 0)
	if err != nil {
		t.Fatalf("parse package source: %v", err)
	}
	pkg, ok := pkgs["uxaudit"]
	if !ok {
		t.Fatalf("no uxaudit package parsed (got %v)", keysOf(pkgs))
	}
	// Scanner precondition / negative control: a parse that silently found almost
	// nothing would report "no offenders" and read as a pass. This package has ~10
	// non-test files; 5 is a floor that cannot be met by a broken glob.
	const minScannedFiles = 5
	if n := len(pkg.Files); n < minScannedFiles {
		t.Fatalf("scanned only %d non-test files, want >= %d — the scan is broken, so its "+
			"'no offenders' verdict means nothing", n, minScannedFiles)
	}

	var mkdirTempSites []string
	var walkFound, walkAcquires bool
	for name, f := range pkg.Files {
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if selectorIs(call.Fun, "os", "MkdirTemp") {
				mkdirTempSites = append(mkdirTempSites,
					filepath.Base(name)+":"+
						fsetLine(fset, call.Pos()))
			}
			return true
		})
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || fn.Name.Name != "Walk" {
				continue
			}
			walkFound = true
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "acquireWorkDir" {
					walkAcquires = true
				}
				return true
			})
		}
	}

	if !walkFound {
		t.Fatal("no func Walk found in the package source — the scan cannot see what it is guarding")
	}
	if !walkAcquires {
		t.Error("Walk does not call acquireWorkDir — whatever work dir it builds is not the " +
			"deterministic one the tests above pin, so the library-sources capture can drift again")
	}
	if len(mkdirTempSites) > 0 {
		t.Errorf("os.MkdirTemp is called from non-test source at %v — a random work dir is what "+
			"made the library-sources capture differ between two runs of the same tree; use "+
			"acquireWorkDir()", mkdirTempSites)
	}
}

// TestExpandStaticDetailsIsScopedToTheRunStatusContainer is the SEAM guard for
// expandStaticDetails's scope argument — the one thing in this harness whose 20-line
// comment says widening it re-imports a known flake, and which nothing checked.
//
// Verified as a real hole: mutating expandedRunPrep to pass "body" instead of
// RunStatusContainerSelector left the entire nested-module suite GREEN, while
// re-importing the comfy.ExtractResources map-ordering nondeterminism into the
// run-missing-models-expanded capture.
//
// It is structural (an AST scan of the actual argument) rather than a string search,
// for the same reason as TestWalkAcquiresTheDeterministicWorkDir: the property is "the
// call passes THIS identifier", not "this word appears in the file".
func TestExpandStaticDetailsIsScopedToTheRunStatusContainer(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", nonTestGoFile, 0)
	if err != nil {
		t.Fatalf("parse package source: %v", err)
	}
	pkg, ok := pkgs["uxaudit"]
	if !ok {
		t.Fatalf("no uxaudit package parsed (got %v)", keysOf(pkgs))
	}
	const minScannedFiles = 5
	if n := len(pkg.Files); n < minScannedFiles {
		t.Fatalf("scanned only %d non-test files, want >= %d — the scan is broken, so its "+
			"verdict means nothing", n, minScannedFiles)
	}

	// Every argument every caller passes to expandStaticDetails, as source text.
	var args []string
	for _, f := range pkg.Files {
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			id, ok := call.Fun.(*ast.Ident)
			if !ok || id.Name != "expandStaticDetails" {
				return true
			}
			for _, a := range call.Args {
				args = append(args, exprText(fset, a))
			}
			return true
		})
	}

	// Scanner precondition: if the scan found no call at all it would report "no
	// offending scope" and read as a pass.
	if len(args) == 0 {
		t.Fatal("no call to expandStaticDetails found in non-test source — either it was " +
			"deleted (delete this guard too) or the scan is broken; either way its verdict " +
			"means nothing")
	}
	for _, got := range args {
		if got != "RunStatusContainerSelector" {
			t.Errorf("expandStaticDetails is called with scope %s, want RunStatusContainerSelector.\n"+
				"The scope is what keeps run-missing-models-expanded a STABLE baseline: widening it "+
				"reaches the workflow detail page's Referenced-resources card, whose chip order is "+
				"randomised per process by comfy.ExtractResources ranging a map. Two walks of the "+
				"same tree then differ by construction — measured, 316x23px.", got)
		}
	}
}

// exprText renders an AST expression back to its source text.
func exprText(fset *token.FileSet, e ast.Expr) string {
	var sb strings.Builder
	if err := printer.Fprint(&sb, fset, e); err != nil {
		return "<unprintable>"
	}
	return sb.String()
}

// nonTestGoFile selects the package's production source: .go, not _test.go.
func nonTestGoFile(fi os.FileInfo) bool {
	name := fi.Name()
	return strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go")
}

// selectorIs reports whether expr is the selector `pkg.name` (e.g. os.MkdirTemp).
func selectorIs(expr ast.Expr, pkg, name string) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != name {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && id.Name == pkg
}

func fsetLine(fset *token.FileSet, pos token.Pos) string {
	return strings.TrimPrefix(fset.Position(pos).String(), fset.Position(pos).Filename)
}

func keysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
