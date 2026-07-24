package web

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/store"
)

// The F1 render caps are a crash-prevention limit on the post-scan results view:
// a pathological library (tens of thousands of files) must not build a DOM large
// enough to crash the browser tab. These tests assert each capped section renders
// exactly its cap, shows a "Showing largest N of M …" note carrying the TRUE total
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
	wantNote := fmt.Sprintf("Showing largest %d of %d — capped to keep the page responsive.",
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
	if strings.Contains(out, "Showing largest") || strings.Contains(out, "capped to keep the page responsive") {
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
	wantNote := fmt.Sprintf("Showing largest %s of %s — capped to keep the page responsive.",
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
	if strings.Contains(out, "Showing largest") || strings.Contains(out, "capped to keep the page responsive") {
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
	wantNote := fmt.Sprintf("Showing largest %s of %s — capped to keep the page responsive.",
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
	if strings.Contains(out, "Showing largest") || strings.Contains(out, "capped to keep the page responsive") {
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

// --- F1 audit fixes: sort-before-cap + cap-immune "Quarantine all" buttons ---

// TestSplitUnmatchedSortedBiggestFirst proves fix #2 for unmatched files: the
// render cap keeps the LARGEST files, not an arbitrary path-alphabetical slice.
// A known-largest file placed LAST in input (so it would be capped away without a
// size sort) must render; a known-smallest placed FIRST must be capped away.
func TestSplitUnmatchedSortedBiggestFirst(t *testing.T) {
	n := maxRenderedUnmatchedRows + 100
	files := make([]store.LocalFile, 0, n)
	// Smallest first — would be path-early and rendered under the old order.
	files = append(files, store.LocalFile{ID: 1, Path: "/lib/aaa_smallest.bin",
		SizeBytes: 1, Status: store.LocalStatusUnmatched, Kind: store.LocalKindModel})
	// Uniform mid-size filler.
	for i := 0; i < n-2; i++ {
		files = append(files, store.LocalFile{ID: int64(i + 2),
			Path: fmt.Sprintf("/lib/f%05d.bin", i), SizeBytes: 1000,
			Status: store.LocalStatusUnmatched, Kind: store.LocalKindModel})
	}
	// Largest last — would be capped away under the old order.
	files = append(files, store.LocalFile{ID: int64(n), Path: "/lib/zzz_largest.bin",
		SizeBytes: 1 << 40, Status: store.LocalStatusUnmatched, Kind: store.LocalKindModel})

	_, unmatched := splitMatchedUnmatched(files)
	out := renderString(t, otherFilesSection(unmatched))
	if !strings.Contains(out, "/lib/zzz_largest.bin") {
		t.Error("largest unmatched file must be rendered after sort-before-cap")
	}
	if strings.Contains(out, "/lib/aaa_smallest.bin") {
		t.Error("smallest unmatched file must be capped away after sort-before-cap")
	}
}

// TestCandidatesSortedBeforeCap proves fix #2 for candidates: candidatesTable
// sorts a COPY biggest-first before capping, so the largest reclaimable candidate
// renders and the smallest is capped away — and it does NOT mutate the caller's
// slice.
func TestCandidatesSortedBeforeCap(t *testing.T) {
	n := maxRenderedCandidateRows + 100
	cands := make([]store.LocalFile, 0, n)
	cands = append(cands, store.LocalFile{ID: 1, Path: "/lib/aaa_smallest.bin",
		SizeBytes: 1, CandidateReason: store.CandidateBroken, Kind: store.LocalKindModel})
	for i := 0; i < n-2; i++ {
		cands = append(cands, store.LocalFile{ID: int64(i + 2),
			Path: fmt.Sprintf("/lib/c%05d.bin", i), SizeBytes: 1000,
			CandidateReason: store.CandidateBroken, Kind: store.LocalKindModel})
	}
	cands = append(cands, store.LocalFile{ID: int64(n), Path: "/lib/zzz_largest.bin",
		SizeBytes: 1 << 40, CandidateReason: store.CandidateBroken, Kind: store.LocalKindModel})

	// Snapshot the caller's slice order to prove no in-place mutation.
	firstPathBefore := cands[0].Path
	out := renderString(t, candidatesTable(cands, "csrf-tok"))
	if cands[0].Path != firstPathBefore {
		t.Errorf("candidatesTable must not mutate the caller's slice (cands[0] changed to %q)", cands[0].Path)
	}
	if !strings.Contains(out, "/lib/zzz_largest.bin") {
		t.Error("largest reclaimable candidate must be rendered after sort-before-cap")
	}
	if strings.Contains(out, "/lib/aaa_smallest.bin") {
		t.Error("smallest candidate must be capped away after sort-before-cap")
	}
}

// escVals is the HTML-escaped form of an hx-vals JSON fragment (gomponents encodes
// the double-quotes in an attribute value as &#34;).
func escVals(reason, csrf string) string {
	return fmt.Sprintf("&#34;reason&#34;:&#34;%s&#34;,&#34;apply&#34;:&#34;false&#34;,&#34;csrf_token&#34;:&#34;%s&#34;",
		reason, csrf)
}

// TestQuarantineAllButtonsPerReason proves fix #1: candidatesTable emits one
// "Quarantine all" button per reason present in the FULL set, each carrying the
// correct reason + csrf in its hx-vals and the correct per-reason count in the
// label. Absent reasons get no button.
func TestQuarantineAllButtonsPerReason(t *testing.T) {
	// 2 duplicates + 3 superseded + 1 broken.
	var cands []store.LocalFile
	add := func(reason string, count int) {
		for i := 0; i < count; i++ {
			cands = append(cands, store.LocalFile{
				ID: int64(len(cands) + 1), Path: fmt.Sprintf("/lib/%s-%d.bin", reason, i),
				SizeBytes: int64(1000 * (i + 1)), CandidateReason: reason, Kind: store.LocalKindModel,
			})
		}
	}
	add(store.CandidateDuplicate, 2)
	add(store.CandidateSuperseded, 3)
	add(store.CandidateBroken, 1)

	out := renderString(t, candidatesTable(cands, "csrf-tok"))
	for _, want := range []string{
		"Quarantine all 2 duplicates",
		"Quarantine all 3 superseded",
		"Quarantine all 1 broken",
		escVals(store.CandidateDuplicate, "csrf-tok"),
		escVals(store.CandidateSuperseded, "csrf-tok"),
		escVals(store.CandidateBroken, "csrf-tok"),
	} {
		if !strings.Contains(out, want) {
			t.Errorf("quarantine-all buttons missing %q", want)
		}
	}
	// Exactly three "Quarantine all" buttons for three present reasons.
	if got := strings.Count(out, "Quarantine all "); got != 3 {
		t.Errorf("expected 3 quarantine-all buttons, got %d", got)
	}
}

// TestQuarantineAllButtonsSingleReason proves only present reasons render a
// button: with only broken candidates, exactly one "Quarantine all" button
// appears and no button for the absent duplicate/superseded reasons.
func TestQuarantineAllButtonsSingleReason(t *testing.T) {
	out := renderString(t, candidatesTable(makeCandidates(5), "csrf-tok"))
	if !strings.Contains(out, "Quarantine all 5 broken") {
		t.Error("expected a 'Quarantine all 5 broken' button")
	}
	if got := strings.Count(out, "Quarantine all "); got != 1 {
		t.Errorf("expected exactly 1 quarantine-all button, got %d", got)
	}
	for _, absent := range []string{"duplicates", "superseded"} {
		if strings.Contains(out, "Quarantine all "+absent) || strings.Contains(out, "Quarantine all 0 "+absent) {
			t.Errorf("must not render a quarantine-all button for absent reason %q", absent)
		}
	}
}

// TestCandidatesCapHint proves the capped candidates table appends the "Quarantine
// all" actionability hint, and that the hint is ABSENT when under the cap.
func TestCandidatesCapHint(t *testing.T) {
	// g.Text HTML-escapes the quotes, so match the stable unquoted tail.
	const hint = `to act on every candidate.`

	capped := renderString(t, candidatesTable(makeCandidates(maxRenderedCandidateRows+10), "csrf-tok"))
	if !strings.Contains(capped, hint) {
		t.Errorf("capped candidates table must include the actionability hint %q", hint)
	}

	under := renderString(t, candidatesTable(makeCandidates(5), "csrf-tok"))
	if strings.Contains(under, hint) {
		t.Error("under-cap candidates table must NOT include the actionability hint")
	}
}

// TestQuarantineIDsReasonPathBypassesRenderCap proves fix #1's handler contract:
// the cap-immune reason path resolves EVERY candidate of a reason (via
// ListCandidates), not just the rendered 500 — so a "Quarantine all broken" button
// acts on candidates past the render cap.
func TestQuarantineIDsReasonPathBypassesRenderCap(t *testing.T) {
	srv := newLibraryTestServer(t, t.TempDir())
	const n = maxRenderedCandidateRows + 25 // > the 500-row render cap
	for i := 0; i < n; i++ {
		if err := srv.store.UpsertLocalFile(store.LocalFile{
			Path:            fmt.Sprintf("/lib/broken/%05d.bin", i),
			SizeBytes:       1024,
			Status:          store.LocalStatusBroken,
			CandidateReason: store.CandidateBroken,
			Kind:            store.LocalKindModel,
		}); err != nil {
			t.Fatalf("seed candidate %d: %v", i, err)
		}
	}
	cands, err := srv.store.ListCandidates(store.CandidateBroken)
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) <= maxRenderedCandidateRows {
		t.Fatalf("precondition: want > %d broken candidates, got %d", maxRenderedCandidateRows, len(cands))
	}

	req := httptest.NewRequest(http.MethodPost, "/library/quarantine",
		strings.NewReader("reason=broken&apply=false"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err := req.ParseForm(); err != nil {
		t.Fatal(err)
	}
	ids := srv.quarantineIDs(req)
	if len(ids) != len(cands) {
		t.Fatalf("reason path should resolve all %d candidates, got %d", len(cands), len(ids))
	}
	if len(ids) <= maxRenderedCandidateRows {
		t.Fatalf("reason path must exceed the render cap (%d), resolved only %d", maxRenderedCandidateRows, len(ids))
	}
}
