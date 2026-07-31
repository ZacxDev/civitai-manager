package comfy

import (
	"sort"
	"testing"
)

// TestLessNodeKeyIsAStrictWeakOrdering pins the property that makes AllImages'
// node ordering deterministic — and that the obvious comparator does NOT have.
//
// The failing witness is {"9","10","5x"}: numerically 9 < 10; lexically "5x" < "9"
// and "10" < "5x" — a cycle. `sort.Slice` on an intransitive comparator returns an
// ARBITRARY permutation, and since the input order comes from a randomized Go map
// range the result differs between runs of the same prompt. That position becomes
// the persisted generation_images.idx, which picks the gallery thumbnail — so the
// thumbnail flips. It is the exact bug the sort was added to fix, reintroduced by
// the fix itself.
//
// Mixed keys are reachable, not theoretical: convert_subgraph.go mints ids as
// "<instance>:<interior>", so a VHS output inside a subgraph alongside a top-level
// output node produces precisely this key set.
func TestLessNodeKeyIsAStrictWeakOrdering(t *testing.T) {
	keys := []string{"9", "10", "5x", "07", "7", "2", "93:12", "a", "100"}

	// Brute-force the three properties sort.Slice actually requires. The set is
	// tiny and these are exactly the guarantees an intransitive comparator breaks.
	for _, a := range keys {
		if lessNodeKey(a, a) {
			t.Errorf("lessNodeKey(%q,%q) = true; must be irreflexive", a, a)
		}
		for _, b := range keys {
			if lessNodeKey(a, b) && lessNodeKey(b, a) {
				t.Errorf("lessNodeKey(%q,%q) and lessNodeKey(%q,%q) are BOTH true — not asymmetric",
					a, b, b, a)
			}
			for _, c := range keys {
				if lessNodeKey(a, b) && lessNodeKey(b, c) && !lessNodeKey(a, c) {
					t.Errorf("INTRANSITIVE: %q<%q and %q<%q but NOT %q<%q — sort.Slice then "+
						"returns an arbitrary order and the persisted thumbnail idx flips "+
						"between identical runs", a, b, b, c, a, c)
				}
			}
		}
	}

	// And the intended shape: numeric keys first by value, everything else lexically.
	got := append([]string(nil), keys...)
	sort.Slice(got, func(i, j int) bool { return lessNodeKey(got[i], got[j]) })
	want := []string{"2", "07", "7", "9", "10", "100", "5x", "93:12", "a"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sorted = %v, want %v (numerics by value first, then lexical)", got, want)
		}
	}
}
