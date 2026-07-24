package web

import (
	"strings"
	"testing"
)

// TestSizeClassTiers asserts each magnitude tier maps to its expected class,
// exactly at and around the documented thresholds.
func TestSizeClassTiers(t *testing.T) {
	cases := []struct {
		bytes int64
		want  string
	}{
		{0, "cm-size-small"},
		{499 * 1024 * 1024, "cm-size-small"},      // just under 500MB
		{500 * 1024 * 1024, "cm-size-medium"},     // exactly 500MB
		{1500 * 1024 * 1024, "cm-size-medium"},    // 1.5GB
		{2 * 1024 * 1024 * 1024, "cm-size-large"}, // exactly 2GB
		{5 * 1024 * 1024 * 1024, "cm-size-large"}, // 5GB
		{6 * 1024 * 1024 * 1024, "cm-size-huge"},  // exactly 6GB
		{20 * 1024 * 1024 * 1024, "cm-size-huge"}, // 20GB
	}
	for _, c := range cases {
		if got := sizeClass(c.bytes); got != c.want {
			t.Errorf("sizeClass(%d) = %q, want %q", c.bytes, got, c.want)
		}
	}
}

// TestSizeCellRendersTierClassAndSortValue proves a size cell at each tier
// renders the expected color class AND the raw byte count in data-sort-value.
func TestSizeCellRendersTierClassAndSortValue(t *testing.T) {
	cases := []struct {
		bytes int64
		class string
	}{
		{100 * 1024 * 1024, "cm-size-small"},
		{1 * 1024 * 1024 * 1024, "cm-size-medium"},
		{3 * 1024 * 1024 * 1024, "cm-size-large"},
		{8 * 1024 * 1024 * 1024, "cm-size-huge"},
	}
	for _, c := range cases {
		out := renderString(t, sizeCell(c.bytes))
		if !strings.Contains(out, c.class) {
			t.Errorf("sizeCell(%d) missing class %q: %s", c.bytes, c.class, out)
		}
		if !strings.Contains(out, `data-sort-value="`) {
			t.Errorf("sizeCell(%d) missing data-sort-value: %s", c.bytes, out)
		}
		// data-sort-value must be the raw bytes so numeric sorting is correct.
		if !strings.Contains(out, `data-sort-value="`+itoa64(c.bytes)+`"`) {
			t.Errorf("sizeCell(%d) data-sort-value should be raw bytes: %s", c.bytes, out)
		}
	}
}

func itoa64(v int64) string {
	// small local helper to avoid importing strconv in the test
	if v == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	return string(b[i:])
}
