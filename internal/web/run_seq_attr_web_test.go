package web

import (
	"strings"
	"testing"
)

// TestRunStatusFragmentRunSeqAttr locks the data-run-seq contract the uxaudit
// harness's re-pin wait depends on: a real run (Seq>0) renders
// data-run-seq="<seq>" on every run-status fragment, and an idle snapshot
// (Seq==0) omits the attribute entirely (so no zero-seq leaks a phantom run).
// Non-vacuous: the present-arm fails if dataRunSeq is dropped from a fragment;
// the absent-arm fails if the idle-omit guard (seq<=0) regresses.
func TestRunStatusFragmentRunSeqAttr(t *testing.T) {
	const csrf = "csrf-tok"
	frags := map[string]func(snap runSnapshot) string{
		"runRunning": func(s runSnapshot) string { return renderString(t, runRunning(s, 1, csrf)) },
		"runStopped": func(s runSnapshot) string { return renderString(t, runStopped(s, 1, csrf)) },
		"runTerminal": func(s runSnapshot) string {
			return renderString(t, runTerminal(s, 1, csrf, false, ""))
		},
	}

	for name, render := range frags {
		t.Run(name+"/present-when-seq-positive", func(t *testing.T) {
			html := render(runSnapshot{Started: true, WorkflowID: 1, Seq: 7})
			if !strings.Contains(html, `data-run-seq="7"`) {
				t.Fatalf("%s: expected data-run-seq=\"7\" on the fragment, not found in:\n%s", name, html)
			}
		})
		t.Run(name+"/omitted-when-idle", func(t *testing.T) {
			html := render(runSnapshot{Started: true, WorkflowID: 1, Seq: 0})
			if strings.Contains(html, "data-run-seq") {
				t.Fatalf("%s: idle snapshot (Seq=0) must omit data-run-seq, but it rendered:\n%s", name, html)
			}
		})
	}
}
