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

// TestWindowsImplementationIsReachable proves the windows file is not
// accidentally excluded from every build by a malformed constraint.
//
// THIS IS THE OTHER HALF OF THE CROSS-BUILD CHECK, and it exists because the
// build above CANNOT catch it: a file whose `//go:build windows` line is
// mistyped (or separated from `package` by a missing blank line) is silently
// treated as a plain comment or dropped from the build entirely — and a package
// with NO windows implementation still compiles for windows, because
// usage_other.go's `!unix && !windows` would... not match either, leaving `stat`
// undefined. The failure mode worth guarding is the reverse: usage_other.go
// quietly satisfying windows too, which would ship a Windows binary that reports
// every disk as "unknown" while every build stays green.
//
// So this asserts the constraint LINES themselves, which is the thing a typo
// breaks.
func TestWindowsImplementationIsReachable(t *testing.T) {
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
