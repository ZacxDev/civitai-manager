package diskusage_test

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// readSource reads one of this package's own source files.
func readSource(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}

// TestEveryReleaseTargetCompiles is the cross-platform guard the /disks capacity
// feature needs.
//
// WHY IT SHELLS OUT TO `go build`. This package is the ONLY place in the repo
// with GOOS-specific files, and the Windows half — a syscall.NewLazyDLL call
// into kernel32 — is compiled on no developer machine and by no ordinary `go
// test ./...`. Releases build SIX targets ({linux,darwin,windows} ×
// {amd64,arm64}); a typo in usage_windows.go would sail through every local
// check and break the release job instead. Type-checking each target is the only
// way to catch that from a linux workstation.
//
// CGO_ENABLED=0 mirrors the release exactly (see .github/workflows/release.yml —
// the pure-Go SQLite driver is what makes it possible). Without it, a
// cross-target build shells out to the host gcc and fails on the C runtime
// rather than on anything in this package, which reads as a false failure.
//
// It is skipped in -short mode: six compiles is a few seconds, which is fine for
// a normal run but not for a tight edit loop.
func TestEveryReleaseTargetCompiles(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the 6-target cross-compile in -short mode")
	}
	targets := []struct{ goos, goarch string }{
		{"linux", "amd64"}, {"linux", "arm64"},
		{"darwin", "amd64"}, {"darwin", "arm64"},
		{"windows", "amd64"}, {"windows", "arm64"},
	}
	for _, tc := range targets {
		t.Run(tc.goos+"/"+tc.goarch, func(t *testing.T) {
			// `go build` with no -o discards the object, so nothing is written to
			// the repo. It still type-checks the whole package for that target,
			// which is the point.
			cmd := exec.Command("go", "build", "github.com/ZacxDev/civitai-manager/internal/diskusage")
			cmd.Env = append(cmd.Environ(),
				"GOOS="+tc.goos, "GOARCH="+tc.goarch, "CGO_ENABLED=0")
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Errorf("GOOS=%s GOARCH=%s build failed: %v\n%s",
					tc.goos, tc.goarch, err, out)
			}
		})
	}
}

// TestBuildConstraintsAreIntact asserts each per-GOOS file OPENS with its build
// constraint, which is the thing a typo breaks.
//
// 🔴 THE RATIONALE THIS TEST USED TO CARRY WAS WRONG ON TWO COUNTS, AND BOTH WERE
// MEASURED RATHER THAN REASONED ABOUT (go1.25.x, this host):
//
//   - It claimed a constraint "separated from `package` by a missing blank line"
//     is silently dropped. It is NOT. A file whose first two lines are
//     `//go:build unix` and `package p`, with no blank line between them, is still
//     correctly EXCLUDED from a windows build. The blank line is a gofmt/vet
//     convention, not a condition for the constraint to apply — so that failure
//     mode does not exist and this test never guarded it.
//   - It framed the risk as being about usage_windows.go. That file is the one
//     that does NOT need the constraint: the `_windows.go` FILENAME SUFFIX carries
//     an implicit GOOS constraint of its own. Measured with constraint lines
//     removed entirely — GOOS=linux and GOOS=darwin both take only the `_unix.go`
//     file, GOOS=windows takes both. `_unix` is not a GOOS, so `usage_unix.go`
//     gets NO implicit constraint and depends entirely on its `//go:build unix`
//     line; `usage_other.go` likewise.
//
// So the test is kept, with its aim corrected: the file that a typo can actually
// break is usage_unix.go (and usage_other.go). A mistyped `//go:buildunix` makes
// the file unconditional, which was verified to produce `F redeclared in this
// block` on a windows build — loud, but only on a target no one builds by hand,
// which is why asserting the line here is worth the four lines it costs.
func TestBuildConstraintsAreIntact(t *testing.T) {
	for _, tc := range []struct{ file, want string }{
		{"usage_unix.go", "//go:build unix"},
		{"usage_windows.go", "//go:build windows"},
		{"usage_other.go", "//go:build !unix && !windows"},
	} {
		src := readSource(t, tc.file)
		if !strings.HasPrefix(src, tc.want+"\n") {
			t.Errorf("%s must OPEN with %q (a constraint anywhere else is just a comment); got:\n%s",
				tc.file, tc.want, firstLine(src))
		}
	}
}

func firstLine(s string) string {
	if i := strings.Index(s, "\n"); i >= 0 {
		return s[:i]
	}
	return s
}

// TestWindowsShimWiresItsOutParams pins the ONE thing about usage_windows.go that
// no test on this host can execute: which out-pointer sits in which argument
// position of the GetDiskFreeSpaceExW call.
//
// 🔴 IT ASSERTS SOURCE TEXT, AND THAT IS A DELIBERATE, NARROW CONCESSION. The
// prototype is
//
//	BOOL GetDiskFreeSpaceExW(LPCWSTR, PULARGE_INTEGER lpFreeBytesAvailableToCaller,
//	                         PULARGE_INTEGER lpTotalNumberOfBytes,
//	                         PULARGE_INTEGER lpTotalNumberOfFreeBytes)
//
// so arguments 2-4 are fixed by the API, and swapping two of them yields
// plausible-looking WRONG NUMBERS on a real Windows box while every build here
// stays green. A behavioural check would need a Windows machine; a source check
// needs none, and this ordering changes about once a decade.
//
// Everything AFTER the call — which out-param becomes which Usage field — is
// covered EXECUTABLY by TestDiskFreeSpaceExOutMapsToUsage, which is why
// diskFreeSpaceExOut lives in usage.go. This guard is deliberately limited to the
// argument order, the only part that cannot be run.
func TestWindowsShimWiresItsOutParams(t *testing.T) {
	src := readSource(t, "usage_windows.go")
	call := sliceFrom(t, src, "getDiskFreeSpaceEx.Call(", "\t)")
	// The exact argument sequence, in API order. Checked for PRESENCE and for
	// RELATIVE POSITION, so a swap fails even though both strings still occur.
	want := []string{
		"uintptr(unsafe.Pointer(p)),",
		"uintptr(unsafe.Pointer(&out.AvailToCaller)),",
		"uintptr(unsafe.Pointer(&out.Total)),",
		"uintptr(unsafe.Pointer(&out.TotalFree)),",
	}
	prev := -1
	for i, w := range want {
		at := strings.Index(call, w)
		if at < 0 {
			t.Fatalf("argument %d of GetDiskFreeSpaceExW is not %s; the call is:\n%s", i+1, w, call)
		}
		if at <= prev {
			t.Errorf("argument %d (%s) is out of API order — the fixed prototype order is "+
				"lpFreeBytesAvailableToCaller, lpTotalNumberOfBytes, lpTotalNumberOfFreeBytes. Call:\n%s",
				i+1, w, call)
		}
		prev = at
	}
}

// sliceFrom returns the text of s from start through the first end after it,
// failing the test when either is missing — an empty slice would make every
// assertion below it fail confusingly rather than reporting the real problem.
func sliceFrom(t *testing.T, s, start, end string) string {
	t.Helper()
	i := strings.Index(s, start)
	if i < 0 {
		t.Fatalf("source does not contain %q", start)
	}
	j := strings.Index(s[i:], end)
	if j < 0 {
		t.Fatalf("source has %q with no closing %q", start, end)
	}
	return s[i : i+j+len(end)]
}
