package web

import (
	"regexp"
	"strings"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

func matchedFile(id int, size int64) store.LocalFile {
	m := id
	return store.LocalFile{ModelID: &m, SizeBytes: size,
		Status: store.LocalStatusMatched, Kind: store.LocalKindModel}
}

func matchedFileVer(id, ver int, size int64) store.LocalFile {
	m, v := id, ver
	return store.LocalFile{ModelID: &m, VersionID: &v, SizeBytes: size,
		Status: store.LocalStatusMatched, Kind: store.LocalKindModel}
}

func candidate(reason string, size int64) store.LocalFile {
	return store.LocalFile{CandidateReason: reason, SizeBytes: size,
		Kind: store.LocalKindModel, Status: store.LocalStatusMatched}
}

// populatedLibrary is the fixture the status-card tests share: two identified
// models (three files), two unidentified files, a duplicate + a superseded + a
// broken candidate, and one cache-derived out-of-date model.
//
// Every chip therefore has a NON-ZERO count and a populated popover, which is what
// makes "a chip lost its popover" observable — a fixture that could only reach the
// zero branch would assert nothing about the populated one.
func populatedLibrary() libraryView {
	return libraryView{
		Files: []store.LocalFile{
			matchedFile(1, 1<<20), matchedFile(1, 1<<20), // model 1 (two files, one model)
			matchedFile(2, 1<<20), // model 2
			{Kind: store.LocalKindModel, Status: store.LocalStatusUnmatched}, // unmatched (no ModelID)
			{Kind: store.LocalKindModel, Status: store.LocalStatusUnmatched},
		},
		Candidates: []store.LocalFile{
			candidate(store.CandidateDuplicate, 2*1024*1024*1024),  // 2 GB
			candidate(store.CandidateSuperseded, 1*1024*1024*1024), // 1 GB (redundant → "duplicate")
			candidate(store.CandidateBroken, 10),
		},
		// TotalBytes is what buildLibraryView sums over the model-kind files above;
		// set explicitly here because the fixture is built directly.
		TotalBytes: 3 << 20,
		OutOfDate:  1, // one matched model has a newer remote version (cache-derived)
	}
}

// TestSummarizeLibraryCounts asserts the roll-up arithmetic the card renders.
func TestSummarizeLibraryCounts(t *testing.T) {
	s := summarizeLibrary(populatedLibrary())
	for _, c := range []struct {
		what      string
		got, want int
	}{
		{"ModelsIdentified", s.ModelsIdentified, 2},
		{"Unmatched", s.Unmatched, 2},
		{"Duplicates (duplicate+superseded)", s.Duplicates, 2},
		{"Broken", s.Broken, 1},
		{"OutOfDate", s.OutOfDate, 1},
	} {
		if c.got != c.want {
			t.Errorf("%s = %d, want %d", c.what, c.got, c.want)
		}
	}
	if s.DuplicateBytes != 3*1024*1024*1024 {
		t.Errorf("DuplicateBytes = %d, want 3GB", s.DuplicateBytes)
	}
}

// TestStatusCardIsOneCardWithFourChips is the core item-2 gate: ONE card whose
// content is the four-chip row, each chip carrying its count AND a popover holding
// the detail the two old cards printed inline.
//
// MUTATION-VERIFIED: deleting the popover child from statChip fails this with
// `chip "models" renders no .cm-updated-pop popover`.
func TestStatusCardIsOneCardWithFourChips(t *testing.T) {
	out := renderString(t, libraryStatusCard(populatedLibrary()))

	// Exactly ONE card, holding exactly FOUR chips.
	if n := strings.Count(out, `data-civitai-ui="card"`); n != 1 {
		t.Errorf("the status card must be ONE card, got %d:\n%s", n, out)
	}
	if n := strings.Count(out, "cm-chip-stat"); n != 4 {
		t.Errorf("expected exactly 4 status chips, got %d:\n%s", n, out)
	}

	// Each chip: the right count, the right label, an icon, and a POPOVER.
	for _, c := range []struct{ label, count string }{
		{"models", "2"},
		{"duplicates", "2"},
		{"out of date", "1"},
		{"unmatched", "2"},
	} {
		chip := chipFragment(t, out, c.label)
		if !strings.Contains(chip, `<span class="font-semibold">`+c.count+`</span> `+c.label) {
			t.Errorf("chip %q should read %s %s:\n%s", c.label, c.count, c.label, chip)
		}
		if !strings.Contains(chip, "cm-updated-pop") {
			t.Errorf("chip %q renders no .cm-updated-pop popover — the detail the old "+
				"Summary/duplicates cards printed has nowhere to live:\n%s", c.label, chip)
		}
		// A real popover TRIGGER on the SHARED mechanism, reachable by keyboard.
		if !strings.Contains(chip, "cm-updated ") || !strings.Contains(chip, `tabindex="0"`) {
			t.Errorf("chip %q must reuse the .cm-updated trigger and be focusable:\n%s", c.label, chip)
		}
		if !strings.Contains(chip, "<svg") || !strings.Contains(chip, "cm-stat-ico") {
			t.Errorf("chip %q must carry an inline .cm-stat-ico SVG glyph:\n%s", c.label, chip)
		}
		// ONE hover affordance per element: a custom popover AND a native title=
		// stack two tooltips on the same hover.
		if strings.Contains(chip, "title=") {
			t.Errorf("chip %q must not carry title= beside its popover:\n%s", c.label, chip)
		}
	}

	// The DETAIL each retired card used to show, now inside a popover.
	for _, want := range []string{
		"3.0 GB reclaimable", // was the "Reclaimable" summary cell
		"3.0 MB on disk",     // was the "Total size" summary cell
		"5 model file(s)",    // was the "Files" summary cell
		"Plus 1 broken",      // was the standalone broken pill
		"#deletion-candidates",
		"Review deletion candidates", // was the banner's quarantine CTA
	} {
		if !strings.Contains(out, want) {
			t.Errorf("status card popovers missing detail %q:\n%s", want, out)
		}
	}
}

// TestOldSummaryAndDuplicatesCardsAreGone pins the de-duplication: the two cards
// the status card replaced must not come back beside it.
func TestOldSummaryAndDuplicatesCardsAreGone(t *testing.T) {
	out := renderString(t, libraryContent(populatedLibrary(), "csrf"))
	for _, gone := range []string{
		`>Summary</h2>`,                      // the old 4-cell Summary card heading
		"Review &amp; quarantine duplicates", // the old banner's primary CTA
		"Your library is clean",              // the old banner's success alert
		"See deletion candidates",            // the old banner's secondary link
	} {
		if strings.Contains(out, gone) {
			t.Errorf("the results view still renders the retired %q — the summary and "+
				"duplicates cards were folded INTO the status card, not duplicated beside "+
				"it:\n%s", gone, out)
		}
	}
	// …and the one card that DID survive is still there (the quarantine table).
	if !strings.Contains(out, `>Deletion candidates</h2>`) {
		t.Errorf("the deletion-candidates card must survive — it is the action surface:\n%s", out)
	}
}

// TestZeroCountChipRendersDimmedAndIsNeverOmitted pins the deliberate decision for
// a zero count: the chip STAYS, dimmed. The old pills row hid every zero, so the
// row's shape depended on the data and "0 duplicates" — real reassurance — could
// only be said by a whole extra success alert.
func TestZeroCountChipRendersDimmedAndIsNeverOmitted(t *testing.T) {
	out := renderString(t, libraryStatusCard(libraryView{
		Files: []store.LocalFile{matchedFile(1, 1<<20)},
	}))

	if n := strings.Count(out, "cm-chip-stat"); n != 4 {
		t.Fatalf("all four chips must render even at zero, got %d:\n%s", n, out)
	}
	for _, label := range []string{"duplicates", "out of date", "unmatched"} {
		chip := chipFragment(t, out, label)
		if !strings.Contains(chip, `<span class="font-semibold">0</span> `+label) {
			t.Errorf("zero chip %q must still print its 0:\n%s", label, chip)
		}
		if !strings.Contains(chip, "cm-chip-zero") {
			t.Errorf("zero chip %q must carry the dimmed .cm-chip-zero class:\n%s", label, chip)
		}
		if !strings.Contains(chip, "cm-updated-pop") {
			t.Errorf("zero chip %q must keep its popover (it explains the zero):\n%s", label, chip)
		}
	}
	// The non-zero chip keeps its intent colour rather than the dimmed one.
	if models := chipFragment(t, out, "models"); strings.Contains(models, "cm-chip-zero") {
		t.Errorf("a non-zero chip must not be dimmed:\n%s", models)
	}
}

// TestZeroDimmingIsNotOpacity reads the SHIPPED rule: .cm-chip-zero must dim with
// COLOUR only. `opacity` (and filter / will-change / contain) creates a stacking
// context, which would trap the chip's own z-index:50 popover inside the chip.
func TestZeroDimmingIsNotOpacity(t *testing.T) {
	body := cssRuleBody(t, ".cm-chip-zero")
	for _, banned := range []string{"opacity", "filter", "will-change", "contain"} {
		if strings.Contains(body, banned) {
			t.Errorf(".cm-chip-zero uses %q to dim. That creates a STACKING CONTEXT, which "+
				"traps the chip's own z-index:50 popover inside the chip — the exact trap the "+
				".cm-lift POPOVER ESCAPE block exists for. Dim with a colour token instead.\n%s",
				banned, body)
		}
	}
	if !strings.Contains(body, "--civitai-color-text-dimmed") {
		t.Errorf(".cm-chip-zero must dim via the text-dimmed token:\n%s", body)
	}
}

// cssRuleBody returns the declaration block of the FIRST rule in the shipped
// app.css whose selector list is exactly `sel`, comments stripped — so an assertion
// about a rule cannot be satisfied by a COMMENT that merely mentions the selector
// (that exact false pass has happened in this package before).
func cssRuleBody(t *testing.T, sel string) string {
	t.Helper()
	css := cssCommentRE.ReplaceAllString(readAppCSS(t), "")
	re := regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(sel) + `\s*\{([^{}]*)\}`)
	m := re.FindStringSubmatch(css)
	if m == nil {
		t.Fatalf("no %s rule in app.css", sel)
	}
	return m[1]
}

// TestComputeOutOfDate proves the cache-only out-of-date count: a model whose
// resolved detail has a newer remote version counts; an uncached model (resolver
// returns nil) does not; and a nil resolver yields 0 without panicking.
func TestComputeOutOfDate(t *testing.T) {
	v := libraryView{
		Files: []store.LocalFile{
			matchedFileVer(1, 10, 1<<20), // model 1, local version 10
			matchedFileVer(2, 20, 1<<20), // model 2, local version 20 (uncached)
		},
	}
	resolve := func(id int) *civitai.ModelDetail {
		if id == 1 {
			// Latest remote version (99) is NOT in the library → update available.
			return &civitai.ModelDetail{ModelVersions: []civitai.ModelVersionSummary{
				{ID: 99, Name: "v3"}, {ID: 10, Name: "v1"},
			}}
		}
		return nil // model 2 is uncached → skipped, never fetched
	}
	if n := computeOutOfDate(v, resolve); n != 1 {
		t.Errorf("computeOutOfDate = %d, want 1 (only the cached, updatable model)", n)
	}
	if n := computeOutOfDate(v, nil); n != 0 {
		t.Errorf("computeOutOfDate with nil resolver = %d, want 0", n)
	}

	// A cached model whose latest version IS local must not count.
	resolveUpToDate := func(id int) *civitai.ModelDetail {
		return &civitai.ModelDetail{ModelVersions: []civitai.ModelVersionSummary{
			{ID: 10, Name: "v1"},
		}}
	}
	if n := computeOutOfDate(libraryView{Files: []store.LocalFile{matchedFileVer(1, 10, 1<<20)}}, resolveUpToDate); n != 0 {
		t.Errorf("an up-to-date cached model must not count, got %d", n)
	}
}

// chipFragment slices out the markup of ONE status chip by its label, so an
// assertion about "the duplicates chip" cannot be satisfied by another chip's
// markup.
func chipFragment(t *testing.T, out, label string) string {
	t.Helper()
	end := strings.Index(out, "</span> "+label)
	if end < 0 {
		t.Fatalf("no chip labelled %q in:\n%s", label, out)
	}
	start := strings.LastIndex(out[:end], `<span class="cm-updated cm-chip-stat`)
	if start < 0 {
		t.Fatalf("chip %q has no .cm-chip-stat wrapper in:\n%s", label, out)
	}
	// Extend past the label to the end of this chip's popover.
	tail := out[end:]
	popEnd := strings.Index(tail, "</span></span>")
	if popEnd < 0 {
		return out[start:]
	}
	return out[start : end+popEnd+len("</span></span>")]
}
