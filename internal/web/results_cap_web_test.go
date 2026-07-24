package web

import (
	"fmt"
	"strings"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/store"
)

// The F1 render caps are a crash-prevention limit on the post-scan results view:
// a pathological library (tens of thousands of files) must not build a DOM large
// enough to crash the browser tab. These tests assert each capped section renders
// exactly its cap, shows a "Showing first N of M …" note carrying the TRUE total
// M, keeps its heading counting M, and does NOT leak the capped-away rows.

// makeMatchedGroups builds n matched-model fileGroups with distinct model ids
// (1..n), each with one model file, so matchedModelsSection renders n cards.
func makeMatchedGroups(n int) []fileGroup {
	groups := make([]fileGroup, 0, n)
	for i := 1; i <= n; i++ {
		groups = append(groups, fileGroup{
			modelID: i,
			files: []store.LocalFile{
				{Path: fmt.Sprintf("/lib/m%d.safetensors", i), ModelID: intPtr(i),
					SizeBytes: 1024 * 1024, Status: store.LocalStatusMatched, Kind: store.LocalKindModel},
			},
		})
	}
	return groups
}

// makeUnmatchedFiles builds n unmatched (ModelID == nil) model files.
func makeUnmatchedFiles(n int) []store.LocalFile {
	files := make([]store.LocalFile, 0, n)
	for i := 0; i < n; i++ {
		files = append(files, store.LocalFile{
			ID: int64(i + 1), Path: fmt.Sprintf("/lib/u%d.bin", i),
			SizeBytes: 1024, Status: store.LocalStatusUnmatched, Kind: store.LocalKindModel,
		})
	}
	return files
}

// makeCandidates builds n deletion-candidate files (broken).
func makeCandidates(n int) []store.LocalFile {
	cands := make([]store.LocalFile, 0, n)
	for i := 0; i < n; i++ {
		cands = append(cands, store.LocalFile{
			ID: int64(i + 1), Path: fmt.Sprintf("/lib/c%d.bin", i),
			SizeBytes: 1024, Status: store.LocalStatusBroken,
			CandidateReason: store.CandidateBroken, Kind: store.LocalKindModel,
		})
	}
	return cands
}

// TestMatchedModelsSectionCaps: with > cap groups, exactly maxRenderedMatchedCards
// cards render and the truncation note carries the TRUE total; the heading counts M.
func TestMatchedModelsSectionCaps(t *testing.T) {
	total := maxRenderedMatchedCards + 50
	out := renderString(t, matchedModelsSection(makeMatchedGroups(total)))

	// One `id="model-card-` per rendered card.
	if got := strings.Count(out, `id="model-card-`); got != maxRenderedMatchedCards {
		t.Fatalf("expected exactly %d rendered cards, got %d", maxRenderedMatchedCards, got)
	}
	wantNote := fmt.Sprintf("Showing first %d of %d — capped to keep the page responsive.",
		maxRenderedMatchedCards, total)
	if !strings.Contains(out, wantNote) {
		t.Errorf("missing truncation note %q", wantNote)
	}
	// The heading must show the TRUE total M, not the capped N.
	if !strings.Contains(out, fmt.Sprintf("Matched models (%d)", total)) {
		t.Errorf("heading should show true total %d", total)
	}
	// The capped-away card (e.g. the last model id) must NOT be rendered.
	if strings.Contains(out, fmt.Sprintf(`id="model-card-%d"`, total)) {
		t.Errorf("capped-away card model-card-%d leaked into the render", total)
	}
}

// TestMatchedModelsSectionNoCapWhenUnderLimit: <= cap renders all cards, no note.
func TestMatchedModelsSectionNoCapWhenUnderLimit(t *testing.T) {
	total := 3
	out := renderString(t, matchedModelsSection(makeMatchedGroups(total)))

	if got := strings.Count(out, `id="model-card-`); got != total {
		t.Fatalf("expected all %d cards rendered, got %d", total, got)
	}
	if strings.Contains(out, "Showing first") || strings.Contains(out, "capped to keep the page responsive") {
		t.Error("no truncation note expected when under the cap")
	}
}

// TestOtherFilesSectionCaps: with > cap unmatched files, exactly
// maxRenderedUnmatchedRows rows render, the note carries the humanized true total,
// and the section heading still shows the true total M.
func TestOtherFilesSectionCaps(t *testing.T) {
	total := 1200 // > cap and large enough to exercise the thousands separator
	out := renderString(t, otherFilesSection(makeUnmatchedFiles(total)))

	// One row Tr class per rendered unmatched row.
	if got := strings.Count(out, `border-b border-slate-800/60`); got != maxRenderedUnmatchedRows {
		t.Fatalf("expected exactly %d rendered rows, got %d", maxRenderedUnmatchedRows, got)
	}
	// The note humanizes M with a thousands separator (humanCount).
	wantNote := fmt.Sprintf("Showing first %s of %s — capped to keep the page responsive.",
		humanCount(maxRenderedUnmatchedRows), humanCount(total))
	if !strings.Contains(out, wantNote) {
		t.Errorf("missing truncation note %q", wantNote)
	}
	// The heading keeps the TRUE total M.
	if !strings.Contains(out, fmt.Sprintf("Other files (%d unmatched)", total)) {
		t.Errorf("heading should show true total %d unmatched", total)
	}
	// The capped-away files must not appear as rendered rows.
	if strings.Contains(out, "/lib/u1199.bin") {
		t.Error("capped-away unmatched row leaked into the render")
	}
}

// TestOtherFilesSectionNoCapWhenUnderLimit: <= cap renders all rows, no note.
func TestOtherFilesSectionNoCapWhenUnderLimit(t *testing.T) {
	total := 3
	out := renderString(t, otherFilesSection(makeUnmatchedFiles(total)))

	if got := strings.Count(out, `border-b border-slate-800/60`); got != total {
		t.Fatalf("expected all %d rows rendered, got %d", total, got)
	}
	if strings.Contains(out, "Showing first") || strings.Contains(out, "capped to keep the page responsive") {
		t.Error("no truncation note expected when under the cap")
	}
}

// TestCandidatesTableCaps: with > cap candidates, exactly maxRenderedCandidateRows
// rows render and the note carries the true total.
func TestCandidatesTableCaps(t *testing.T) {
	total := maxRenderedCandidateRows + 200
	out := renderString(t, candidatesTable(makeCandidates(total), "csrf-tok"))

	// One checkbox `name="id"` per rendered candidate row.
	if got := strings.Count(out, `name="id"`); got != maxRenderedCandidateRows {
		t.Fatalf("expected exactly %d rendered candidate rows, got %d", maxRenderedCandidateRows, got)
	}
	wantNote := fmt.Sprintf("Showing first %s of %s — capped to keep the page responsive.",
		humanCount(maxRenderedCandidateRows), humanCount(total))
	if !strings.Contains(out, wantNote) {
		t.Errorf("missing truncation note %q", wantNote)
	}
	// The capped-away candidate must not be rendered.
	if strings.Contains(out, fmt.Sprintf("/lib/c%d.bin", total-1)) {
		t.Error("capped-away candidate row leaked into the render")
	}
}

// TestCandidatesTableNoCapWhenUnderLimit: <= cap renders all rows, no note.
func TestCandidatesTableNoCapWhenUnderLimit(t *testing.T) {
	total := 4
	out := renderString(t, candidatesTable(makeCandidates(total), "csrf-tok"))

	if got := strings.Count(out, `name="id"`); got != total {
		t.Fatalf("expected all %d candidate rows rendered, got %d", total, got)
	}
	if strings.Contains(out, "Showing first") || strings.Contains(out, "capped to keep the page responsive") {
		t.Error("no truncation note expected when under the cap")
	}
}

// TestHumanCountThousandsSeparator guards the small formatting helper used in the
// truncation notes.
func TestHumanCountThousandsSeparator(t *testing.T) {
	cases := map[int]string{
		0:       "0",
		12:      "12",
		200:     "200",
		999:     "999",
		1000:    "1,000",
		4312:    "4,312",
		45000:   "45,000",
		1234567: "1,234,567",
	}
	for in, want := range cases {
		if got := humanCount(in); got != want {
			t.Errorf("humanCount(%d) = %q, want %q", in, got, want)
		}
	}
}
