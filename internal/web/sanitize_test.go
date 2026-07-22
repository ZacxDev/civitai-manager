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

func TestNSFWLevelAndMode(t *testing.T) {
	// The inline nsfwLevel is NUMERIC (1=None/PG .. 32=XXX). Only None/PG (<= 1)
	// is safe; Soft (2) and above are NSFW.
	if isNSFWLevel(1) {
		t.Error("level 1 (None/PG) must be treated as safe")
	}
	for _, nsfw := range []int{2, 4, 8, 16, 32} {
		if !isNSFWLevel(nsfw) {
			t.Errorf("level %d must be treated as NSFW", nsfw)
		}
	}
	// Fail closed: the nsfwLevelUnknown sentinel (assigned to absent/garbage
	// levels) is NSFW, so an unmapped image is blurred/omitted, not shown clear.
	if !isNSFWLevel(nsfwLevelUnknown) {
		t.Error("unknown-level sentinel must fail closed (NSFW)")
	}

	// parseNSFWLevel: integers pass through; absent/null/non-integer → sentinel.
	for raw, want := range map[string]int{
		"1": 1, "4": 4, "32": 32, "0": 0,
		"":              nsfwLevelUnknown,
		"null":          nsfwLevelUnknown,
		`"garbage"`:     nsfwLevelUnknown,
		"1.5":           nsfwLevelUnknown,
		`"SuperSpicy9"`: nsfwLevelUnknown,
	} {
		if got := parseNSFWLevel([]byte(raw)); got != want {
			t.Errorf("parseNSFWLevel(%q) = %d, want %d", raw, got, want)
		}
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
