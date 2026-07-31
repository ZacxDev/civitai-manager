package diskusage

import (
	"os"
	"path/filepath"
	"testing"
)

// TestStatRealDirectoryIsPlausible probes the REAL filesystem under the test's
// own temp dir. It asserts the relationships between the three fields rather
// than any absolute number (which no test can know), which is what would catch
// the classic port bugs: a wrong block-size field width making Total absurd, or
// Free/Used being swapped.
func TestStatRealDirectoryIsPlausible(t *testing.T) {
	dir := t.TempDir()
	u, err := Stat(dir)
	if err != nil {
		t.Fatalf("Stat(%q) = %v; the test's own temp dir must be stat-able", dir, err)
	}
	if !u.Known() {
		t.Fatalf("Stat(%q) reported an unknown Usage %+v on a live filesystem", dir, u)
	}
	// A real filesystem is at least a megabyte and (as of 2026) below an exabyte.
	// A bad Bsize conversion lands far outside this band in one direction or the
	// other — that is the bug this bound exists to catch, not a capacity policy.
	const oneMB, oneEB = 1 << 20, uint64(1) << 60
	if u.Total < oneMB || u.Total > oneEB {
		t.Errorf("Total = %d bytes, implausible for a real filesystem (block-size conversion bug?)", u.Total)
	}
	if u.Used > u.Total {
		t.Errorf("Used (%d) > Total (%d)", u.Used, u.Total)
	}
	if u.Free > u.Total {
		t.Errorf("Free (%d) > Total (%d)", u.Free, u.Total)
	}
	// Used excludes the root reserve and Free excludes it too, so their sum is
	// Total MINUS the reserve — never more than Total.
	if u.Used+u.Free > u.Total {
		t.Errorf("Used (%d) + Free (%d) exceeds Total (%d)", u.Used, u.Free, u.Total)
	}
	if f := u.UsedFraction(); f < 0 || f > 1 {
		t.Errorf("UsedFraction() = %v, want [0,1]", f)
	}
}

// TestStatMissingPathFailsSoft pins the best-effort contract: a path that does
// not exist returns an error AND a zero Usage, so a caller that renders
// Known()==false shows "unknown" instead of a fabricated 0-byte disk.
func TestStatMissingPathFailsSoft(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-dir", "nested")
	if _, err := os.Stat(missing); err == nil {
		t.Fatalf("fixture is wrong: %q exists", missing)
	}
	u, err := Stat(missing)
	if err == nil {
		t.Fatalf("Stat(%q) succeeded; a missing path must report an error", missing)
	}
	if u.Known() {
		t.Errorf("a failed Stat must leave a zero Usage, got %+v", u)
	}
	if u.Total != 0 || u.Free != 0 || u.Used != 0 {
		t.Errorf("a failed Stat must zero every field, got %+v", u)
	}
	if f := u.UsedFraction(); f != 0 {
		t.Errorf("UsedFraction() on an unknown Usage = %v, want 0", f)
	}
}

// TestFromBlocksMapsTheStatfsCounters is the real guard on the unix semantics:
// which statfs(2) counter becomes which Usage field. A live syscall cannot pin
// that (no test knows what the machine's disk holds, and probing by writing a
// file is flaky on compressing/CoW filesystems), so usage_unix.go delegates the
// decision here where a table CAN pin it.
//
// The fixture is deliberately asymmetric — bfree != bavail, and every counter a
// different number — so a Free/Used swap, a Bavail/Bfree mix-up or a dropped
// block-size multiply each land on a different wrong answer.
func TestFromBlocksMapsTheStatfsCounters(t *testing.T) {
	const (
		bsize  = 4096
		blocks = 1000 // 4,096,000 total
		bfree  = 400  // 1,638,400 unallocated -> used is the other 600 blocks
		bavail = 350  // 1,433,600 usable; the 50-block difference is the root reserve
	)
	u, err := fromBlocks(blocks, bfree, bavail, bsize)
	if err != nil {
		t.Fatalf("fromBlocks: %v", err)
	}
	// The fixture must actually reach the interesting case: a NON-EMPTY root
	// reserve. With bfree == bavail the reserve is zero and Used/Free would add up
	// to Total, which is exactly the state in which a Bfree/Bavail mix-up is
	// invisible.
	if bfree == bavail {
		t.Fatal("fixture is degenerate: bfree == bavail leaves no root reserve to distinguish them")
	}
	if want := uint64(blocks * bsize); u.Total != want {
		t.Errorf("Total = %d, want %d (blocks*bsize)", u.Total, want)
	}
	if want := uint64(bavail * bsize); u.Free != want {
		t.Errorf("Free = %d, want %d (bavail*bsize — the UNPRIVILEGED figure, not bfree)", u.Free, want)
	}
	if want := uint64((blocks - bfree) * bsize); u.Used != want {
		t.Errorf("Used = %d, want %d ((blocks-bfree)*bsize — derived from bfree, NOT from bavail)", u.Used, want)
	}
	// The root reserve is neither used nor free, so the two must fall SHORT of
	// Total by exactly it. This is what breaks if Used is derived from bavail.
	reserve := uint64((bfree - bavail) * bsize)
	if got := u.Total - u.Used - u.Free; got != reserve {
		t.Errorf("Total-Used-Free = %d, want the root reserve %d", got, reserve)
	}
	if u.UsedFraction() != 0.6 {
		t.Errorf("UsedFraction() = %v, want 0.6", u.UsedFraction())
	}
}

// TestFromBlocksZeroBlockSizeFailsSoft pins the guard against a filesystem that
// reports a zero block size: every product would be zero, which renders as a
// real 0-byte disk unless it is reported as a failure.
func TestFromBlocksZeroBlockSizeFailsSoft(t *testing.T) {
	u, err := fromBlocks(1000, 400, 350, 0)
	if err == nil {
		t.Fatal("fromBlocks with bsize 0 must fail rather than report a 0-byte filesystem")
	}
	if u.Known() {
		t.Errorf("a failed fromBlocks must leave a zero Usage, got %+v", u)
	}
}

// TestFromByteCountsMapsTheWindowsCounts is the fromBlocks guard's Windows twin,
// and it runs on EVERY platform — the mapping is what a port gets wrong, and it
// must not go unguarded just because CI is not Windows.
//
// FreeBytesAvailableToCaller (quota-aware) and TotalNumberOfFreeBytes (not) are
// deliberately different here, so swapping them fails.
func TestFromByteCountsMapsTheWindowsCounts(t *testing.T) {
	const (
		total         = 4096000
		totalFree     = 1638400 // ignores the caller's quota
		availToCaller = 1433600 // honours it -> smaller
	)
	if totalFree == availToCaller {
		t.Fatal("fixture is degenerate: the two free counts must differ to tell them apart")
	}
	u := fromByteCounts(total, totalFree, availToCaller)
	if u.Total != total {
		t.Errorf("Total = %d, want %d", u.Total, total)
	}
	if u.Free != availToCaller {
		t.Errorf("Free = %d, want FreeBytesAvailableToCaller %d (the quota-aware count)", u.Free, availToCaller)
	}
	if want := uint64(total - totalFree); u.Used != want {
		t.Errorf("Used = %d, want %d (total - TotalNumberOfFreeBytes)", u.Used, want)
	}
}

// TestUsedFractionClamps covers the meter-width helper's edges directly: an
// over-large Used (which a racing filesystem can briefly report) must clamp to a
// full bar rather than overflow the meter.
func TestUsedFractionClamps(t *testing.T) {
	cases := []struct {
		name string
		u    Usage
		want float64
	}{
		{"unknown", Usage{}, 0},
		{"empty", Usage{Total: 100, Free: 100, Used: 0}, 0},
		{"half", Usage{Total: 100, Free: 50, Used: 50}, 0.5},
		{"full", Usage{Total: 100, Free: 0, Used: 100}, 1},
		{"over-full clamps", Usage{Total: 100, Free: 0, Used: 250}, 1},
	}
	for _, c := range cases {
		if got := c.u.UsedFraction(); got != c.want {
			t.Errorf("%s: UsedFraction() = %v, want %v", c.name, got, c.want)
		}
	}
}
