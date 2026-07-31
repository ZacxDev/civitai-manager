package web

import (
	"strings"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
)

// TestCompactCount covers the boundary table for the compact count formatter.
func TestCompactCount(t *testing.T) {
	cases := []struct {
		in   int
		want string
	}{
		{0, "0"},
		{999, "999"},
		{1000, "1K"},
		{1234, "1.2K"},
		{12340, "12.3K"},
		{999_999, "1000K"}, // rounds up to the top of the K band
		{1_000_000, "1M"},
		{3_400_000, "3.4M"},
		{1_000_000_000, "1B"},
		{1_500_000_000, "1.5B"},
		{-5, "-5"}, // negatives (shouldn't occur) render as-is
	}
	for _, c := range cases {
		if got := compactCount(c.in); got != c.want {
			t.Errorf("compactCount(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestSearchCardRendersCompactCounts proves a rendered search card humanizes the
// download/like totals via compactCount (e.g. 12_345 -> "12.3K") and renders them
// as inline icon + count: the compact counts and the download/thumbs-up icons are
// present, the a11y labels are kept, but the old "N downloads"/"N likes" word
// phrases are gone (the words survive only inside aria-label/title).
func TestSearchCardRendersCompactCounts(t *testing.T) {
	res := &civitai.ModelSearchResult{Items: []civitai.ModelListItem{
		{ID: 1, Name: "Cool LoRA", Type: "LORA",
			Stats: civitai.ModelStats{DownloadCount: 12_345, ThumbsUpCount: 6_789}},
	}}
	out := renderString(t, searchResults(res, nil, fullMaturityRange(), "test-csrf", ""))

	// Compact counts still present.
	if !strings.Contains(out, "12.3K") {
		t.Errorf("search card should show compact downloads (12.3K), got:\n%s", out)
	}
	if !strings.Contains(out, "6.8K") {
		t.Errorf("search card should show compact likes (6.8K)")
	}
	// The old word-phrase form is gone (counts and words are no longer adjacent).
	if strings.Contains(out, "12.3K downloads") || strings.Contains(out, "6.8K likes") {
		t.Errorf("stat words should be replaced by icons, but a count+word phrase remains:\n%s", out)
	}
	// Icons rendered inline.
	if !strings.Contains(out, "cm-stat-ico") {
		t.Errorf("stat icons (cm-stat-ico) not rendered:\n%s", out)
	}
	// a11y labels retained (via aria-label + title, not visible text).
	for _, want := range []string{`aria-label="downloads"`, `title="downloads"`, `aria-label="likes"`, `title="likes"`} {
		if !strings.Contains(out, want) {
			t.Errorf("missing a11y attribute %q:\n%s", want, out)
		}
	}
}
