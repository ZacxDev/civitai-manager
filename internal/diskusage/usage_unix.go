//go:build unix

package diskusage

import "syscall"

// stat answers from statfs(2).
//
// FIELD WIDTHS DIFFER BY GOOS — that is why every field is converted through
// uint64 rather than used directly. On linux/amd64 Statfs_t.Bsize is an int64;
// on darwin it is a uint32. Writing `st.Bsize * st.Blocks` compiles on exactly
// one of the six release targets.
//
// Bavail vs Bfree: Bavail is what an UNPRIVILEGED process may still allocate,
// Bfree includes the root reserve. Free reports Bavail (the honest "how much can
// I actually write"), and Used is derived from Bfree so the root reserve is
// counted as neither used nor free — see the Usage doc.
func stat(path string) (Usage, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return Usage{}, err
	}
	return fromBlocks(uint64(st.Blocks), uint64(st.Bfree), uint64(st.Bavail), uint64(st.Bsize))
}
