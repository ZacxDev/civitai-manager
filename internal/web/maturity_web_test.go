package web

import "testing"

// TestMaturityLevelFromBrowsingLevel pins the measured string↔numeric mapping.
//
// The `Label` column is the string CivitAI puts on the item; it is here ONLY to
// document the collapse, never to drive the mapping.
func TestMaturityLevelFromBrowsingLevel(t *testing.T) {
	cases := []struct {
		Label string // CivitAI's string nsfwLevel — informational
		N     int    // CivitAI's numeric browsingLevel — authoritative
		Want  maturityLevel
	}{
		{"None", 1, maturityPG},
		{"Soft", 2, maturityPG13},
		{"Mature", 4, maturityR},
		{"X", 8, maturityX},
		{"X", 16, maturityXXX}, // SAME LABEL, different level
	}
	for _, c := range cases {
		if got := maturityFromBrowsingLevel(c.N); got != c.Want {
			t.Errorf("maturityFromBrowsingLevel(%d) [label %q] = %d, want %d",
				c.N, c.Label, got, c.Want)
		}
	}
}

// TestMaturityXAndXXXAreDistinguished is the 🔴 guard for the whole feature.
//
// THE FIXTURE IS THE POINT: both items carry the IDENTICAL string label "X" —
// exactly as measured live (41 items at browsingLevel 8 and 40 at 16 on one
// `nsfw=X&limit=100` response, all labelled "X"). Any implementation that reads
// the string cannot tell them apart, so it must answer the same for both and
// FAIL here. Only a numeric implementation can pass.
func TestMaturityXAndXXXAreDistinguished(t *testing.T) {
	type item struct {
		NSFWLabel     string
		BrowsingLevel int
	}
	// Deliberately indistinguishable by label.
	hardX := item{NSFWLabel: "X", BrowsingLevel: 8}
	hardXXX := item{NSFWLabel: "X", BrowsingLevel: 16}
	if hardX.NSFWLabel != hardXXX.NSFWLabel {
		t.Fatalf("fixture is not exercising the collapse: labels differ (%q vs %q)",
			hardX.NSFWLabel, hardXXX.NSFWLabel)
	}

	xOnly := maturityRange{Min: maturityX, Max: maturityX}
	xxxOnly := maturityRange{Min: maturityXXX, Max: maturityXXX}

	if !xOnly.containsBrowsingLevel(hardX.BrowsingLevel) {
		t.Errorf("X-only range must contain browsingLevel 8")
	}
	if xOnly.containsBrowsingLevel(hardXXX.BrowsingLevel) {
		t.Errorf("X-only range must NOT contain browsingLevel 16 (XXX) — the label %q "+
			"is the same, so this can only pass on the number", hardXXX.NSFWLabel)
	}
	if !xxxOnly.containsBrowsingLevel(hardXXX.BrowsingLevel) {
		t.Errorf("XXX-only range must contain browsingLevel 16")
	}
	if xxxOnly.containsBrowsingLevel(hardX.BrowsingLevel) {
		t.Errorf("XXX-only range must NOT contain browsingLevel 8 (X)")
	}

	// And a range ending at X must not leak XXX — the failure mode a string scale
	// would ship.
	upToX := maturityRange{Min: maturityPG, Max: maturityX}
	if upToX.containsBrowsingLevel(hardXXX.BrowsingLevel) {
		t.Errorf("PG..X leaked XXX (browsingLevel 16)")
	}
}

// TestMaturityUnknownFailsClosed: an absent / garbage / out-of-scale level (0,
// 3, 32=Blocked, a future value) is never inside ANY range, including the full
// one.
func TestMaturityUnknownFailsClosed(t *testing.T) {
	full := fullMaturityRange()
	for _, n := range []int{0, -1, 3, 5, 32, 64, 99} {
		if got := maturityFromBrowsingLevel(n); got != maturityUnknown {
			t.Errorf("maturityFromBrowsingLevel(%d) = %d, want maturityUnknown", n, got)
		}
		if full.containsBrowsingLevel(n) {
			t.Errorf("full range contains out-of-scale level %d — must fail closed", n)
		}
	}
}

// TestMaturityRangeFiltering walks every level against several bands.
func TestMaturityRangeFiltering(t *testing.T) {
	all := []int{1, 2, 4, 8, 16}
	cases := []struct {
		name string
		r    maturityRange
		in   []int
	}{
		{"full PG..XXX", fullMaturityRange(), []int{1, 2, 4, 8, 16}},
		{"PG only", maturityRange{maturityPG, maturityPG}, []int{1}},
		{"PG13 only", maturityRange{maturityPG13, maturityPG13}, []int{2}},
		{"R only", maturityRange{maturityR, maturityR}, []int{4}},
		{"X only", maturityRange{maturityX, maturityX}, []int{8}},
		{"XXX only", maturityRange{maturityXXX, maturityXXX}, []int{16}},
		{"PG..R", maturityRange{maturityPG, maturityR}, []int{1, 2, 4}},
		{"R..XXX", maturityRange{maturityR, maturityXXX}, []int{4, 8, 16}},
		{"PG13..X", maturityRange{maturityPG13, maturityX}, []int{2, 4, 8}},
	}
	for _, c := range cases {
		want := map[int]bool{}
		for _, n := range c.in {
			want[n] = true
		}
		for _, n := range all {
			if got := c.r.containsBrowsingLevel(n); got != want[n] {
				t.Errorf("%s: contains(browsingLevel %d) = %v, want %v", c.name, n, got, want[n])
			}
		}
	}
}

// TestMaturityRangeRejectsInverted: min > max is refused, not swapped, not
// clamped to empty.
func TestMaturityRangeRejectsInverted(t *testing.T) {
	inverted := []string{"xxx:pg", "r:pg13", "x:r", "pg13:pg"}
	for _, s := range inverted {
		if r, ok := parseMaturityRange(s); ok {
			t.Errorf("parseMaturityRange(%q) accepted an inverted range as %+v", s, r)
		}
	}
	if (maturityRange{Min: maturityXXX, Max: maturityPG}).valid() {
		t.Errorf("XXX..PG reported valid")
	}
}

// TestMaturityRangeParseRoundTrip covers the persisted form and the junk paths.
func TestMaturityRangeParseRoundTrip(t *testing.T) {
	for _, lo := range maturityScale {
		for _, hi := range maturityScale {
			if lo > hi {
				continue
			}
			want := maturityRange{Min: lo, Max: hi}
			got, ok := parseMaturityRange(want.String())
			if !ok || got != want {
				t.Errorf("round trip %q = (%+v,%v), want (%+v,true)", want.String(), got, ok, want)
			}
		}
	}
	for _, junk := range []string{"", "pg", "pg:", ":xxx", "pg:pg14", "nope:xxx", "pg;xxx", "1:16"} {
		if _, ok := parseMaturityRange(junk); ok {
			t.Errorf("parseMaturityRange(%q) accepted junk", junk)
		}
	}
	// The default is the whole scale.
	if got := fullMaturityRange(); got.Min != maturityPG || got.Max != maturityXXX {
		t.Errorf("fullMaturityRange() = %+v, want PG..XXX", got)
	}
}

// TestImagesNSFWCeiling pins the request ceiling to the range MAX, and pins it
// to values the API actually accepts.
//
// The accepted set was read out of the API's own 400 body on 2026-07-31:
// "expected one of \"None\"|\"Soft\"|\"Mature\"|\"X\"|\"Blocked\"". `nsfw=XXX`
// is a 400 — so a ceiling of XXX must map to "X", not to its own name.
func TestImagesNSFWCeiling(t *testing.T) {
	apiAccepts := map[string]bool{"None": true, "Soft": true, "Mature": true, "X": true, "Blocked": true}
	cases := []struct {
		r    maturityRange
		want string
	}{
		{maturityRange{maturityPG, maturityPG}, "None"},
		{maturityRange{maturityPG, maturityPG13}, "Soft"},
		{maturityRange{maturityPG13, maturityPG13}, "Soft"},
		{maturityRange{maturityPG, maturityR}, "Mature"},
		{maturityRange{maturityR, maturityR}, "Mature"},
		{maturityRange{maturityPG, maturityX}, "X"},
		{maturityRange{maturityX, maturityX}, "X"},
		{maturityRange{maturityXXX, maturityXXX}, "X"}, // NOT "XXX" — that is a 400
		{fullMaturityRange(), "X"},
	}
	for _, c := range cases {
		got := c.r.imagesNSFWCeiling()
		if got != c.want {
			t.Errorf("%s.imagesNSFWCeiling() = %q, want %q", c.r.String(), got, c.want)
		}
		if !apiAccepts[got] {
			t.Errorf("%s.imagesNSFWCeiling() = %q, which the API rejects with HTTP 400", c.r.String(), got)
		}
		if got == "Blocked" {
			t.Errorf("%s emitted the Blocked moderation bucket as a ceiling", c.r.String())
		}
	}
	// Exhaustive: no valid range may ever produce an unaccepted ceiling.
	for _, lo := range maturityScale {
		for _, hi := range maturityScale {
			if lo > hi {
				continue
			}
			r := maturityRange{Min: lo, Max: hi}
			if c := r.imagesNSFWCeiling(); !apiAccepts[c] || c == "Blocked" {
				t.Errorf("range %s produced ceiling %q", r.String(), c)
			}
		}
	}
}

// TestModelsNSFWFlag: /api/v1/models takes only a boolean (level names are a
// 400), so the range degrades to "does this band need anything above PG".
func TestModelsNSFWFlag(t *testing.T) {
	cases := []struct {
		r    maturityRange
		want bool
	}{
		{maturityRange{maturityPG, maturityPG}, false},
		{maturityRange{maturityPG, maturityPG13}, true},
		{maturityRange{maturityR, maturityR}, true},
		{maturityRange{maturityXXX, maturityXXX}, true},
		{fullMaturityRange(), true},
	}
	for _, c := range cases {
		if got := c.r.modelsNSFWFlag(); got != c.want {
			t.Errorf("%s.modelsNSFWFlag() = %v, want %v", c.r.String(), got, c.want)
		}
	}
}

// TestMaturityLevelSlugsAreStable guards the persisted tokens (a drift here
// silently resets everyone's stored range on upgrade).
func TestMaturityLevelSlugsAreStable(t *testing.T) {
	want := map[maturityLevel]string{
		maturityPG: "pg", maturityPG13: "pg13", maturityR: "r",
		maturityX: "x", maturityXXX: "xxx",
	}
	for l, s := range want {
		if got := l.slug(); got != s {
			t.Errorf("level %d slug = %q, want %q", l, got, s)
		}
		if back, ok := maturityFromSlug(s); !ok || back != l {
			t.Errorf("maturityFromSlug(%q) = (%d,%v), want (%d,true)", s, back, ok, l)
		}
	}
	if _, ok := maturityFromSlug("hide"); ok {
		t.Errorf("the dead NSFW mode %q resolved as a level", "hide")
	}
	if _, ok := maturityFromSlug(""); ok {
		t.Errorf("empty slug resolved as a level")
	}
}
