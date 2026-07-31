//go:build unix

package diskusage

import (
	"syscall"
	"testing"
)

// TestFromStatfsWiresEveryField pins the unix shim's ARGUMENT WIRING — which
// Statfs_t field reaches which fromBlocks parameter.
//
// 🔴 WHY THIS EXISTS SEPARATELY FROM TestFromBlocksMapsTheStatfsCounters. That
// test pins what fromBlocks does with four numbers; it says nothing about which
// number each one is. The shim is a second, independent mapping decision, and
// with it unguarded the classic "forgot the root reserve" bug — passing Bfree
// where bavail belongs, over-reporting free space by the reserve on every ext4
// install — kept the whole suite GREEN.
//
// 🔴 AND WHY IT IS SYNTHETIC RATHER THAN A REAL PROBE. The only other thing
// exercising this line is TestStatRealDirectoryIsPlausible, which stats the
// HOST's filesystem. That catches a Bavail/Bfree swap ONLY when the mounted
// filesystem has a non-zero root reserve; on tmpfs, APFS, exfat and many btrfs
// configs Bfree == Bavail and the swap is invisible. A guard whose redness
// depends on what /tmp is mounted as is not a guard. Every counter below is a
// distinct value, so each possible mis-wiring lands on a different wrong answer.
func TestFromStatfsWiresEveryField(t *testing.T) {
	// Deliberately asymmetric AND pairwise-distinct: no two counters share a
	// value, so no swap can coincidentally produce the right number.
	const (
		blocks = 1000 // -> Total  = 4,096,000
		bfree  = 400  // -> Used   = (1000-400)*4096 = 2,457,600
		bavail = 350  // -> Free   = 1,433,600   (the 50-block gap is the reserve)
		bsize  = 4096
	)
	for _, pair := range [][2]uint64{{blocks, bfree}, {blocks, bavail}, {bfree, bavail}} {
		if pair[0] == pair[1] {
			t.Fatalf("fixture is degenerate: two counters share the value %d, so a swap between "+
				"them would be undetectable", pair[0])
		}
	}

	// Statfs_t's field WIDTHS differ by GOOS (Bsize is int64 on linux/amd64 and
	// int32 on darwin) — untyped constants convert to whichever this target uses,
	// which is exactly the portability the shim exists to provide.
	st := syscall.Statfs_t{Blocks: blocks, Bfree: bfree, Bavail: bavail, Bsize: bsize}

	u, err := fromStatfs(&st)
	if err != nil {
		t.Fatalf("fromStatfs: %v", err)
	}
	if want := uint64(blocks * bsize); u.Total != want {
		t.Errorf("Total = %d, want %d — Blocks must reach the `blocks` parameter", u.Total, want)
	}
	if want := uint64(bavail * bsize); u.Free != want {
		t.Errorf("Free = %d, want %d — Bavail (NOT Bfree) must reach the `bavail` parameter; "+
			"wiring Bfree here over-reports free space by the root reserve", u.Free, want)
	}
	if want := uint64((blocks - bfree) * bsize); u.Used != want {
		t.Errorf("Used = %d, want %d — Bfree (NOT Bavail) must reach the `bfree` parameter, or the "+
			"root reserve is counted as used", u.Used, want)
	}
	// The reserve is neither used nor free: the two must fall SHORT of Total by
	// exactly it. This is the assertion a Bfree/Bavail swap cannot survive in
	// EITHER direction.
	if want := uint64((bfree - bavail) * bsize); u.Total-u.Used-u.Free != want {
		t.Errorf("Total-Used-Free = %d, want the root reserve %d", u.Total-u.Used-u.Free, want)
	}
	// A dropped or swapped Bsize would scale everything; pin it independently of
	// the ratios above, which a uniform scale error would survive.
	if u.Total/blocks != bsize {
		t.Errorf("Total/blocks = %d, want the block size %d — Bsize must reach the `bsize` parameter",
			u.Total/blocks, bsize)
	}
}

// TestFromStatfsPropagatesTheZeroBlockSizeFailure proves the shim does not
// swallow fromBlocks' error: a filesystem reporting a zero block size must reach
// the caller as a failure, not as a fabricated 0-byte disk.
func TestFromStatfsPropagatesTheZeroBlockSizeFailure(t *testing.T) {
	st := syscall.Statfs_t{Blocks: 1000, Bfree: 400, Bavail: 350, Bsize: 0}
	u, err := fromStatfs(&st)
	if err == nil {
		t.Fatal("fromStatfs with Bsize 0 must return the error fromBlocks produced")
	}
	if u.Known() {
		t.Errorf("a failed fromStatfs must leave a zero Usage, got %+v", u)
	}
}
