// Package diskusage reports the capacity of the filesystem holding a given path.
//
// WHY IT IS ITS OWN PACKAGE, AND STDLIB-ONLY. Capacity is the one thing on the
// /disks page that cannot be answered from the database, and the obvious way to
// answer it — golang.org/x/sys/unix.Statfs — would promote x/sys from an
// INDIRECT to a DIRECT dependency. That edits go.mod, and this repo's flake pins
// a `vendorHash` that is not derived from anything the Go build reads: any
// go.mod/go.sum movement silently invalidates it and `nix build
// github:ZacxDev/civitai-manager` starts failing for everyone with a hash
// mismatch (it has shipped broken on the public path once already). So this
// package uses only `syscall`, in two build-tagged files.
//
// BEST-EFFORT IS THE CONTRACT. Releases build six targets
// ({linux,darwin,windows} × {amd64,arm64}) and the paths handed to Stat come
// from user config — they may not exist, may be on an unmounted volume, or may
// live on a filesystem the syscall refuses. Every one of those is a normal
// answer, not an error page: callers render "unknown" for a path whose Usage is
// not Known. Stat therefore returns a zero Usage plus an error and never panics.
package diskusage

import "errors"

// ErrUnsupported is returned by Stat on a GOOS with no implementation. It is
// deliberately a sentinel rather than a build failure: a new port should render
// "unknown" capacity, not fail to compile the whole web package.
var ErrUnsupported = errors.New("diskusage: not supported on this platform")

// Usage is the capacity of ONE filesystem, in bytes.
//
// Used is deliberately NOT Total-Free. On a POSIX filesystem some blocks are
// reserved for root, so the blocks free to an unprivileged process (Free) are
// fewer than the blocks that are actually unallocated. Deriving Used from Free
// would silently count that reserve as "used" and make the three numbers fail to
// add up against `df`. Used is computed from the unallocated count instead, so
// Used + reserve + Free == Total and each field means exactly what it says.
type Usage struct {
	// Total is the size of the filesystem.
	Total uint64
	// Free is the space available TO THIS USER (excludes any root reserve).
	Free uint64
	// Used is the space actually allocated (excludes the root reserve).
	Used uint64
}

// Known reports whether this Usage carries a real measurement. A filesystem with
// a zero Total is not a filesystem this UI can describe (it is what a failed or
// unimplemented probe leaves behind), so callers treat it as "unknown" rather
// than rendering a 0-byte disk that reads as a real, completely full volume.
func (u Usage) Known() bool { return u.Total > 0 }

// UsedFraction is Used/Total clamped to [0,1], for a meter width. It returns 0
// for an unknown Usage — a bar of zero length, never a divide by zero.
func (u Usage) UsedFraction() float64 {
	if !u.Known() {
		return 0
	}
	f := float64(u.Used) / float64(u.Total)
	if f < 0 {
		return 0
	}
	if f > 1 {
		return 1
	}
	return f
}

// Stat returns the capacity of the filesystem containing path.
//
// It is implemented per-GOOS in usage_unix.go / usage_windows.go / usage_other.go.
// A non-nil error always comes with a zero Usage, so a caller that ignores the
// error still renders "unknown" rather than a fabricated zero-byte disk.
func Stat(path string) (Usage, error) { return stat(path) }

// fromBlocks converts raw statfs(2) block counters into a Usage.
//
// IT IS PLATFORM-INDEPENDENT ON PURPOSE. Which counter feeds which field is the
// only interesting decision in the unix implementation, and a syscall result
// cannot be pinned by a test — nothing knows what the machine's real disk holds,
// and probing it by writing a file is flaky on compressing/CoW filesystems. So
// the arithmetic lives here where a table test CAN pin it (a Free/Used swap or a
// Bavail/Bfree mix-up fails immediately), and usage_unix.go stays a thin shim
// whose only job is widening the OS's differently-sized fields.
//
//   - blocks: total blocks in the filesystem
//   - bfree:  blocks unallocated (INCLUDES any root reserve)
//   - bavail: blocks allocatable by an unprivileged process (EXCLUDES it)
//   - bsize:  bytes per block
//
// bfree >= bavail always; the difference is the reserve, which this reports as
// neither used nor free.
func fromBlocks(blocks, bfree, bavail, bsize uint64) (Usage, error) {
	if bsize == 0 {
		// Every product would be zero, which Known() reports as "unknown" anyway —
		// return it as an explicit failure so a caller that logs sees the reason.
		return Usage{}, ErrUnsupported
	}
	total := blocks * bsize
	unalloc := bfree * bsize
	var used uint64
	if total > unalloc {
		used = total - unalloc
	}
	return Usage{Total: total, Free: bavail * bsize, Used: used}, nil
}

// fromByteCounts converts GetDiskFreeSpaceExW's three byte counts into a Usage,
// for the same reason fromBlocks exists: it is the mapping decision, and it is
// testable on every platform while the DLL call is not.
//
//   - total:         TotalNumberOfBytes
//   - totalFree:     TotalNumberOfFreeBytes (ignores the caller's quota)
//   - availToCaller: FreeBytesAvailableToCaller (honours it)
//
// availToCaller is the Windows analogue of statfs' Bavail, and totalFree of
// Bfree, so the three fields mean exactly what they mean on unix.
func fromByteCounts(total, totalFree, availToCaller uint64) Usage {
	var used uint64
	if total > totalFree {
		used = total - totalFree
	}
	return Usage{Total: total, Free: availToCaller, Used: used}
}
