//go:build !unix

package comfy

// freeDiskBytes has no portable implementation off unix (Windows release target);
// it reports "unknown" so the caller skips the free-space pre-check. The size cap
// and the bounded copy still guard the download.
func freeDiskBytes(_ string) (int64, bool) { return 0, false }
