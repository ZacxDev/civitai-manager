package web

import (
	"strings"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/store"
)

// TestLibraryTableSortableMarkup asserts the files table carries the sortable
// affordances: aria-sort headers, data-sortable, the numeric data-sort-value on
// the size cell (raw bytes), and the inline sort script.
func TestLibraryTableSortableMarkup(t *testing.T) {
	mid, vid := 42, 7
	files := []store.LocalFile{
		{ID: 1, Path: "/lib/a.safetensors", ModelID: &mid, VersionID: &vid,
			SizeBytes: 3 * 1024 * 1024 * 1024, Status: store.LocalStatusMatched, Kind: store.LocalKindModel},
	}
	out := renderString(t, libraryModelTable(files))

	for _, want := range []string{
		`data-sortable`,         // headers marked sortable
		`aria-sort="none"`,      // initial sort state (accessible)
		`cm-sortable-table`,     // table marker the script scopes to
		`onclick="cmSortTable(`, // click-to-sort wiring
		`function cmSortTable(`, // the sort script itself is present
		`cm-sort-ind`,           // direction indicator
	} {
		if !strings.Contains(out, want) {
			t.Errorf("sortable table markup missing %q", want)
		}
	}
	// Size must sort NUMERICALLY: the size cell carries data-sort-value = raw bytes.
	if !strings.Contains(out, `data-sort-value="`+itoa64(3*1024*1024*1024)+`"`) {
		t.Errorf("size cell must carry raw-byte data-sort-value for numeric sort: %s", out)
	}
}

// TestSortScriptComparesBytesNumerically is a light guard that the script sorts
// data-sort-value cells with a numeric compare (subtraction), not lexically.
func TestSortScriptComparesBytesNumerically(t *testing.T) {
	out := renderString(t, librarySortScript())
	if !strings.Contains(out, "parseFloat(ca.getAttribute('data-sort-value'))") {
		t.Error("sort script should parse data-sort-value as a number")
	}
	if !strings.Contains(out, "(na - nb) * mult") {
		t.Error("sort script should compare numeric sort values by subtraction")
	}
}
