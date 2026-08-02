package civitai

// TEMPORARY — reverted in the very next commit. This file exists only to make
// the new deadcode CI job go RED once, on the record. A gate nobody has watched
// fail is not a gate.
//
// Two shapes, because the two tiers catch different things:

// redproofNoCallerAnywhere has no caller at all, not even a test.
// Expected: reported by BOTH tiers.
func redproofNoCallerAnywhere() int { return 41 }

// redproofTestOnlyCaller is called only from its own test, so it is LIVE under
// -test and DEAD without. Expected: reported by TIER B ONLY — the discriminating
// case, and the whole reason tier B exists.
func redproofTestOnlyCaller() int { return 42 }
