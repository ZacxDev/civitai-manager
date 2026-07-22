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

func TestNSFWRankAndMode(t *testing.T) {
	if nsfwRank("None") != 0 || nsfwRank("") != 0 {
		t.Error("None/empty should rank 0 (safe)")
	}
	if nsfwRank("X") <= nsfwRank("Mature") || nsfwRank("Mature") <= nsfwRank("Soft") {
		t.Error("nsfw ranks should be ordered Soft < Mature < X")
	}
	for in, want := range map[string]string{
		"hide": NSFWHide, "HIDE": NSFWHide, "show": NSFWShow,
		"blur": NSFWBlur, "": NSFWBlur, "garbage": NSFWBlur,
	} {
		if got := normalizeNSFWMode(in); got != want {
			t.Errorf("normalizeNSFWMode(%q) = %q, want %q", in, got, want)
		}
	}
}
