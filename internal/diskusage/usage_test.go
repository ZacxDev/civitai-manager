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
