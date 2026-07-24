package web

import (
	"strings"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/store"
)

func matchedFile(id int, size int64) store.LocalFile {
	m := id
	return store.LocalFile{ModelID: &m, SizeBytes: size,
		Status: store.LocalStatusMatched, Kind: store.LocalKindModel}
}

func candidate(reason string, size int64) store.LocalFile {
	return store.LocalFile{CandidateReason: reason, SizeBytes: size,
		Kind: store.LocalKindModel, Status: store.LocalStatusMatched}
}

// TestSummaryBannerCounts asserts the banner renders the right roll-up and the
// actionable CTAs given a populated libraryView.
func TestSummaryBannerCounts(t *testing.T) {
	v := libraryView{
		Files: []store.LocalFile{
			matchedFile(1, 1<<20), matchedFile(1, 1<<20), // model 1 (two files, one model)
			matchedFile(2, 1<<20), // model 2
			{Kind: store.LocalKindModel, Status: store.LocalStatusUnmatched}, // unmatched (no ModelID)
			{Kind: store.LocalKindModel, Status: store.LocalStatusUnmatched},
		},
		Candidates: []store.LocalFile{
			candidate(store.CandidateDuplicate, 2*1024*1024*1024),  // 2 GB
			candidate(store.CandidateSuperseded, 1*1024*1024*1024), // 1 GB (counts as duplicate/redundant)
			candidate(store.CandidateBroken, 10),
		},
	}

	s := summarizeLibrary(v)
	if s.ModelsIdentified != 2 {
		t.Errorf("ModelsIdentified = %d, want 2", s.ModelsIdentified)
	}
	if s.Unmatched != 2 {
		t.Errorf("Unmatched = %d, want 2", s.Unmatched)
	}
	if s.Duplicates != 2 {
		t.Errorf("Duplicates = %d, want 2 (duplicate+superseded)", s.Duplicates)
	}
	if s.Broken != 1 {
		t.Errorf("Broken = %d, want 1", s.Broken)
	}
	if s.DuplicateBytes != 3*1024*1024*1024 {
		t.Errorf("DuplicateBytes = %d, want 3GB", s.DuplicateBytes)
	}

	out := renderString(t, summaryBanner(v))
	for _, want := range []string{
		"2", "models identified",
		"duplicates (reclaim 3.0 GB)",
		"unmatched",
		"broken",
		"Review &amp; quarantine duplicates", // primary CTA (ampersand escaped)
		"See deletion candidates",            // secondary link
		"#deletion-candidates",               // CTA target anchor
	} {
		if !strings.Contains(out, want) {
			t.Errorf("summary banner missing %q\n%s", want, out)
		}
	}
}

// TestSummaryBannerCleanState asserts the reassuring copy renders when there are
// no duplicates or broken files.
func TestSummaryBannerCleanState(t *testing.T) {
	v := libraryView{
		Files: []store.LocalFile{matchedFile(1, 1<<20), matchedFile(2, 1<<20)},
	}
	out := renderString(t, summaryBanner(v))
	if !strings.Contains(out, "Your library is clean") {
		t.Errorf("clean state should reassure: %s", out)
	}
	if !strings.Contains(out, "No duplicates or broken files found") {
		t.Errorf("clean state missing reassuring copy: %s", out)
	}
	// No quarantine CTA in the clean state.
	if strings.Contains(out, "Review &amp; quarantine") {
		t.Errorf("clean state must not show a quarantine CTA")
	}
}
