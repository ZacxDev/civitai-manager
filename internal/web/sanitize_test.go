package web

import (
	"strings"
	"testing"
)

func TestSanitizeDescriptionStripsDangerousMarkup(t *testing.T) {
	in := `<p>Hello <b>bold</b> <em>x</em></p>` +
		`<script>alert(1)</script>` +
		`<a href="javascript:alert(2)">evil</a>` +
		`<img src="x" onerror="alert(3)">` +
		`<a href="https://example.com">good</a>` +
		`<div onclick="steal()">click</div>`

	out := sanitizeDescription(in)

	// Safe formatting + links are preserved.
	for _, want := range []string{"Hello", "<b>bold</b>", "<em>", "https://example.com", "good"} {
		if !strings.Contains(out, want) {
			t.Errorf("sanitizer dropped safe content %q; got: %s", want, out)
		}
	}
	// Dangerous vectors are removed.
	for _, bad := range []string{"<script", "alert(1)", "alert(2)", "alert(3)", "onerror", "onclick", "javascript:", "steal()"} {
		if strings.Contains(out, bad) {
			t.Errorf("sanitizer left dangerous content %q; got: %s", bad, out)
		}
	}
}

// TestInlineLevelParsing covers the INLINE modelVersions[].images[] level, which
// is the opposite shape to /api/v1/images: here `nsfwLevel` is already the NUMBER
// (measured 2026-07-31: 1|2|4|8|16, and no `browsingLevel` key at all), whereas
// there it is a string label and the number lives under `browsingLevel`.
func TestInlineLevelParsing(t *testing.T) {
	// Integers pass through; absent/null/non-integer → the fail-closed sentinel.
	for raw, want := range map[string]int{
		"1": 1, "4": 4, "16": 16, "32": 32, "0": 0,
		"":              browsingLevelUnknown,
		"null":          browsingLevelUnknown,
		`"garbage"`:     browsingLevelUnknown,
		"1.5":           browsingLevelUnknown,
		`"SuperSpicy9"`: browsingLevelUnknown,
	} {
		if got := parseNSFWLevel([]byte(raw)); got != want {
			t.Errorf("parseNSFWLevel(%q) = %d, want %d", raw, got, want)
		}
	}

	// The parsed value feeds the maturity scale directly. The scale's five steps
	// are recognised; the sentinel, 0 and 32 (Blocked) are not, so no range
	// contains them.
	full := fullMaturityRange()
	for _, n := range []int{1, 2, 4, 8, 16} {
		if !full.containsBrowsingLevel(n) {
			t.Errorf("the full range should contain level %d", n)
		}
	}
	for _, n := range []int{0, 32, browsingLevelUnknown} {
		if full.containsBrowsingLevel(n) {
			t.Errorf("level %d is not a scale step — it must fail closed even at the full range", n)
		}
	}
}
