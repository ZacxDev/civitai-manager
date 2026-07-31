package cli

import (
	"strings"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/poller"
)

// TestBackfillReasonMessageNamesTheWorkflowPostSkip pins the CLI half of the
// workflow-post guard: `subscribe --backfill-latest` against a Workflows post
// downloads nothing, and the line it prints must say WHY and point at Import —
// not fall through to the default "nothing new to back-fill", which would read
// as "this is up to date" about a model that was never downloadable.
func TestBackfillReasonMessageNamesTheWorkflowPostSkip(t *testing.T) {
	got := backfillReasonMessage(poller.BackfillOutcome{
		Reason: poller.BackfillFilteredWorkflowPost, VersionID: 2179254, VersionName: "v1.0",
	})
	for _, want := range []string{"workflow post", "imported, not downloaded", "Import"} {
		if !strings.Contains(got, want) {
			t.Errorf("workflow-post backfill line missing %q, got: %s", want, got)
		}
	}
	// It must NOT be the generic default — that is the exact failure mode a new
	// BackfillReason with no switch arm produces, and it is silent.
	fallback := backfillReasonMessage(poller.BackfillOutcome{Reason: poller.BackfillReasonUnknown})
	if got == fallback {
		t.Errorf("the workflow-post reason fell through to the default line %q", fallback)
	}
}

// TestBackfillReasonMessagesStayDistinct guards the renumbering risk of inserting
// a value into the BackfillReason iota block: every reason must still map to its
// OWN line. A shifted constant would silently make one reason print another's
// message, which no single-reason assertion can see.
func TestBackfillReasonMessagesStayDistinct(t *testing.T) {
	reasons := []poller.BackfillReason{
		poller.BackfillAlreadyPresent,
		poller.BackfillCompletedMissingOnDisk,
		poller.BackfillFilteredBaseModel,
		poller.BackfillFilteredSize,
		poller.BackfillFilteredWorkflowPost,
		poller.BackfillNoDownloadableFile,
		poller.BackfillNoCandidates,
		poller.BackfillTransientError,
		poller.BackfillReasonUnknown,
	}
	seen := map[string]poller.BackfillReason{}
	for _, r := range reasons {
		msg := backfillReasonMessage(poller.BackfillOutcome{
			Reason: r, VersionID: 1, VersionName: "v1", Detail: "DETAIL",
		})
		if prev, dup := seen[msg]; dup {
			t.Errorf("reasons %d and %d render the SAME line %q — the iota block has shifted "+
				"or a switch arm is missing", prev, r, msg)
		}
		seen[msg] = r
	}
}
