package cli

import "runtime/debug"

// readBuildInfo is the injectable seam for runtime/debug.ReadBuildInfo so the
// resolver can be unit-tested with a synthesized *debug.BuildInfo.
var readBuildInfo = debug.ReadBuildInfo

// ResolveBuildInfo fills in build metadata for the running binary. Release
// binaries carry the values injected via -ldflags (see package main); a
// `go install module@version` build has no ldflags, so this falls back to the
// module version and VCS stamps embedded by the Go toolchain in
// runtime/debug.ReadBuildInfo.
func ResolveBuildInfo(bi BuildInfo) BuildInfo {
	return resolveBuildInfo(bi, readBuildInfo)
}

// resolveBuildInfo is the testable core: when the ldflag-injected version is
// present (a real release build), those values win untouched; otherwise it
// reads the module version and vcs.revision/vcs.time settings from the supplied
// build info. A missing/empty build info leaves the defaults in place.
func resolveBuildInfo(bi BuildInfo, read func() (*debug.BuildInfo, bool)) BuildInfo {
	// Prefer ldflags when they were actually injected. The package-main default
	// is "dev"; treat that (and empty) as "not injected".
	if bi.Version != "" && bi.Version != "dev" {
		return bi
	}

	info, ok := read()
	if !ok || info == nil {
		return bi
	}

	out := bi
	// info.Main.Version is the tagged version (e.g. "v0.1.0"), a pseudo-version
	// for an untagged commit, or "(devel)" for a working-tree build.
	if v := info.Main.Version; v != "" && v != "(devel)" {
		out.Version = v
	}
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			if s.Value != "" {
				out.Commit = s.Value
			}
		case "vcs.time":
			if s.Value != "" {
				out.Date = s.Value
			}
		}
	}
	return out
}
