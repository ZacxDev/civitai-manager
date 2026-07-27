//go:build unix

package comfy

import "syscall"

// freeDiskBytes returns the bytes available to an unprivileged process in the
// filesystem holding dir. The bool is false when the query fails (the caller then
// skips the free-space pre-check rather than blocking a download).
func freeDiskBytes(dir string) (int64, bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, false
	}
	return int64(st.Bavail) * int64(st.Bsize), true
}
