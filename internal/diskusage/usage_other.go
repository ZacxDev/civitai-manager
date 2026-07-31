//go:build !unix && !windows

package diskusage

// stat is the fallback for a GOOS with neither statfs(2) nor kernel32 — js/wasm
// and plan9 today. Capacity is BEST-EFFORT by contract, so an unsupported port
// must still compile and render "unknown"; a build failure here would take the
// entire web package down with it on a platform where nothing else is broken.
func stat(string) (Usage, error) { return Usage{}, ErrUnsupported }
