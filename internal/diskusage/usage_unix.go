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
	return fromStatfs(&st)
}

// fromStatfs is the ARGUMENT WIRING, split out so a test can pin it.
//
// 🔴 THIS IS A SECOND MAPPING DECISION, DISTINCT FROM fromBlocks'. The package
// doc argues the mapping lives in fromBlocks "where a table test CAN pin it" —
// but that only pins what fromBlocks does with the four numbers it is handed,
// not WHICH Statfs_t field is handed to each parameter. Wiring Bfree into the
// bavail slot — the "forgot the root reserve" bug, which over-reports free space
// by the reserve on every ext4 install — left the entire suite green.
//
// The gap was invisible because the only thing exercising this line was
// TestStatRealDirectoryIsPlausible, a probe of the HOST's filesystem: it catches
// a Bavail/Bfree swap only where the mounted filesystem happens to have a
// non-zero root reserve. On tmpfs, APFS, exfat and many btrfs configs
// Bfree == Bavail and the swap is undetectable. A guard whose redness depends on
// which filesystem /tmp is on is not a guard, so TestFromStatfsWiresEveryField
// (usage_unix_test.go) feeds a SYNTHETIC Statfs_t with all four counters
// distinct and asserts the exact Usage.
//
// It takes a pointer only to avoid copying a struct that is ~120 bytes on linux
// and carries a fixed-size array; nothing here mutates it.
func fromStatfs(st *syscall.Statfs_t) (Usage, error) {
	return fromBlocks(uint64(st.Blocks), uint64(st.Bfree), uint64(st.Bavail), uint64(st.Bsize))
}
