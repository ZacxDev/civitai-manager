package civitai

import "testing"

// Gives redproofTestOnlyCaller a TEST caller and nothing else, which is what
// makes it invisible to tier A and visible to tier B.
func TestRedproofTestOnlyCaller(t *testing.T) {
	if redproofTestOnlyCaller() != 42 {
		t.Fatal("redproof")
	}
}
