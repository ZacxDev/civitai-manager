package web

import (
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

// TestSummaryBannerCounts asserts summarizeLibrary rolls up the right counts and
// the pills row renders the models/duplicates/out-of-date/broken/unmatched pills
// plus the quarantine CTA given a populated libraryView.
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
		OutOfDate: 1, // one matched model has a newer remote version (cache-derived)
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
	if s.OutOfDate != 1 {
		t.Errorf("OutOfDate = %d, want 1", s.OutOfDate)
	}

	out := renderString(t, summaryBanner(v))
	for _, want := range []string{
		"cm-pill", "models", // the models pill
		"duplicates · 3.0 GB",           // duplicates pill with reclaimable bytes
		"cm-pill-update", "out of date", // the out-of-date (remote update) pill
		"broken",                             // broken pill
		"unmatched",                          // unmatched pill
		"Review &amp; quarantine duplicates", // primary CTA (ampersand escaped)
		"See deletion candidates",            // secondary link
		"#deletion-candidates",               // CTA target anchor
	} {
		if !strings.Contains(out, want) {
			t.Errorf("summary banner missing %q\n%s", want, out)
		}
	}
}

// TestSummaryBannerOutOfDatePillConditional asserts the out-of-date pill is only
// rendered when OutOfDate > 0.
func TestSummaryBannerOutOfDatePillConditional(t *testing.T) {
	v := libraryView{
		Files:      []store.LocalFile{matchedFile(1, 1<<20)},
		Candidates: []store.LocalFile{candidate(store.CandidateBroken, 10)}, // actionable → non-clean card
	}
	if out := renderString(t, summaryBanner(v)); strings.Contains(out, "out of date") {
		t.Errorf("no out-of-date pill expected when OutOfDate==0:\n%s", out)
	}
	v.OutOfDate = 3
	if out := renderString(t, summaryBanner(v)); !strings.Contains(out, "out of date") || !strings.Contains(out, "cm-pill-update") {
		t.Errorf("out-of-date pill expected when OutOfDate>0")
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
