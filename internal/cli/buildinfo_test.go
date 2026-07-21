package cli

import (
	"runtime/debug"
	"testing"
)

// TestResolveBuildInfoPrefersLdflags proves the release path: when the
// ldflag-injected version is present, those values win untouched and the build
// info is never consulted.
func TestResolveBuildInfoPrefersLdflags(t *testing.T) {
	in := BuildInfo{Version: "v1.2.3", Commit: "abc123", Date: "2026-01-02"}
	read := func() (*debug.BuildInfo, bool) {
		t.Fatal("ReadBuildInfo must not be called when ldflags are present")
		return nil, false
	}
	got := resolveBuildInfo(in, read)
	if got != in {
		t.Errorf("resolveBuildInfo = %+v, want unchanged %+v", got, in)
	}
}

// TestResolveBuildInfoFallsBackToBuildInfo proves the `go install` path: with
// the package-main defaults ("dev"/"none"/"unknown"), resolution reads the
// module version and vcs.revision/vcs.time stamps from build info.
func TestResolveBuildInfoFallsBackToBuildInfo(t *testing.T) {
	in := BuildInfo{Version: "dev", Commit: "none", Date: "unknown"}
	read := func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{
			Main: debug.Module{Version: "v0.1.0"},
			Settings: []debug.BuildSetting{
				{Key: "vcs.revision", Value: "deadbeefcafe"},
				{Key: "vcs.time", Value: "2026-07-20T10:00:00Z"},
				{Key: "GOARCH", Value: "amd64"},
			},
		}, true
	}
	got := resolveBuildInfo(in, read)
	if got.Version != "v0.1.0" {
		t.Errorf("Version = %q, want v0.1.0", got.Version)
	}
	if got.Commit != "deadbeefcafe" {
		t.Errorf("Commit = %q, want deadbeefcafe", got.Commit)
	}
	if got.Date != "2026-07-20T10:00:00Z" {
		t.Errorf("Date = %q, want the vcs.time stamp", got.Date)
	}
}

// TestResolveBuildInfoDevelModuleVersionIgnored ensures a working-tree build
// ("(devel)") does not clobber the version, while VCS stamps still fill in.
func TestResolveBuildInfoDevelModuleVersionIgnored(t *testing.T) {
	in := BuildInfo{Version: "dev", Commit: "none", Date: "unknown"}
	read := func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{
			Main:     debug.Module{Version: "(devel)"},
			Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "feed01"}},
		}, true
	}
	got := resolveBuildInfo(in, read)
	if got.Version != "dev" {
		t.Errorf("Version = %q, want unchanged dev for a (devel) module", got.Version)
	}
	if got.Commit != "feed01" {
		t.Errorf("Commit = %q, want feed01", got.Commit)
	}
}

// TestResolveBuildInfoNoBuildInfo leaves the defaults in place when build info
// is unavailable.
func TestResolveBuildInfoNoBuildInfo(t *testing.T) {
	in := BuildInfo{Version: "dev", Commit: "none", Date: "unknown"}
	got := resolveBuildInfo(in, func() (*debug.BuildInfo, bool) { return nil, false })
	if got != in {
		t.Errorf("resolveBuildInfo = %+v, want unchanged %+v", got, in)
	}
}
