package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/store"
)

// TestCanQueueAgreesAcrossPickerHintButtonAndHandler is the agreement guard for
// canQueueWorkflow.
//
// WHY IT EXISTS. "This workflow can be queued as a batch of N" drives four
// decisions rendered by THREE handlers that cannot see each other:
//
//	generateSection (page)          → the ×1/×2/… count picker, and the hint sentence
//	handleWorkflowRunComfyStatus    → which endpoint the ONE primary button posts to
//	handleWorkflowRunQueue          → whether the batch is accepted at all
//
// The button is delivered by a SEPARATE request from the page that renders the
// picker, so the two can disagree while each looks correct on its own. Measured on
// the parent commit: replacing the comfy-status handler's derivation with a
// constant `false` left **all 1762 tests in this package passing**. That drift is
// not a refusal — it is a silent wrong result. The picker still renders, the user
// selects ×8, the button posts to the single-run endpoint, and exactly ONE run
// happens with no error and no warning. The count is discarded while the control
// that collected it stays fully interactive.
//
// The server-side authority fails CLOSED and is not what this guards; nothing
// unsafe happens either way. What is lost is the user's instruction.
//
// HOW IT AVOIDS BEING VACUOUS (see CLAUDE.md's taxonomy):
//   - Each decision is pinned to a LITERAL expectation in the table, not to the
//     other decisions. An "do the three sites agree?" test is satisfied by all
//     three being wrong together — exactly the drift that ships.
//   - Both directions are covered, and each decision is asserted positively AND
//     negatively (the picker's absence, and the OTHER endpoint / OTHER hint), so
//     a page that renders nothing at all cannot satisfy the !canQueue half.
//   - The preconditions below prove the fixture reached the interesting case
//     before any decision is read off it.
func TestCanQueueAgreesAcrossPickerHintButtonAndHandler(t *testing.T) {
	for _, tc := range []struct {
		name     string
		format   string
		graph    string
		canQueue bool
	}{
		// queueSeedGraph carries a KSampler seed, so the no-seed offer does not
		// intercept the POST and the batch really runs.
		{"ui format queues a batch", store.WorkflowFormatUI, queueSeedGraph, true},
		{"api format runs once", store.WorkflowFormatAPI, `{"1":{"class_type":"KSampler","inputs":{}}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := newTestServer(t)
			// A reachable ComfyUI, so the status fragment renders the ENABLED
			// primary control — the thing that carries the endpoint.
			srv.comfyClientFn = func() comfyClient { return &fakeComfy{} }
			var calls int32
			srv.runFn = func(context.Context, *store.Workflow, runUpdater, runOptions) (*runResult, error) {
				atomic.AddInt32(&calls, 1)
				return &runResult{PromptID: "p"}, nil
			}
			id := seedWorkflow(t, srv, tc.format, tc.graph)

			page := get(t, srv, "/workflows/"+id).Body.String()
			frag := get(t, srv, "/workflows/"+id+"/run/comfy-status").Body.String()

			// ── preconditions: the fixture REACHED the case ──────────────────
			if !strings.Contains(page, `id="`+runZoneID+`"`) {
				t.Fatalf("precondition failed: the run zone did not render, so the "+
					"picker assertions below would be vacuous:\n%s", page)
			}
			// 🔴 data-state="ok", NOT the words "ComfyUI reachable". The
			// unreachable headline is "No ComfyUI reachable at <url>", so that
			// prose is true on BOTH branches — CLAUDE.md records it as a
			// non-vacuity check that was itself vacuous.
			if !strings.Contains(frag, `data-state="ok"`) {
				t.Fatalf("precondition failed: the comfy probe did not take the reachable "+
					"branch, so no primary control was rendered:\n%s", frag)
			}
			if !strings.Contains(frag, "cm-generate-cta") {
				t.Fatalf("precondition failed: the fragment carries no primary Generate "+
					"control, so the endpoint assertions below would be vacuous:\n%s", frag)
			}

			// ── the four decisions, each against the table's literal ─────────
			for _, d := range []struct {
				what string
				got  bool
			}{
				{"the count picker renders", strings.Contains(page, `id="`+runCountGroupID+`"`)},
				{"the hint names a batch", strings.Contains(page, runZoneHint(true))},
				{"the primary button posts to /run/queue",
					strings.Contains(frag, `hx-post="/workflows/`+id+`/run/queue"`)},
			} {
				if d.got != tc.canQueue {
					t.Errorf("canQueue DRIFT for a %s-format workflow: %s = %v, want %v",
						tc.format, d.what, d.got, tc.canQueue)
				}
			}
			// The complements. Asserted separately because "the batch hint is
			// absent" does not prove the single-run hint is present.
			for _, d := range []struct {
				what string
				got  bool
			}{
				{"the hint says it runs once", strings.Contains(page, runZoneHint(false))},
				{"the primary button posts to /run-with-params",
					strings.Contains(frag, `hx-post="/workflows/`+id+`/run-with-params"`)},
			} {
				if d.got == tc.canQueue {
					t.Errorf("canQueue DRIFT for a %s-format workflow: %s = %v, want %v",
						tc.format, d.what, d.got, !tc.canQueue)
				}
			}

			// ── the handler, and whether the count actually survives ─────────
			// Posting count=2 to the batch endpoint pins the decision AND the
			// damage: a count that is accepted but discarded is the failure this
			// whole guard is about, so assert the number of RUNS, not the status.
			rec := postQueue(t, srv, id, url.Values{
				"csrf_token": {srv.csrf}, batchCountField: {"2"},
			})
			if accepted := rec.Code != http.StatusNotFound; accepted != tc.canQueue {
				t.Fatalf("canQueue DRIFT for a %s-format workflow: POST /run/queue accepted = %v "+
					"(status %d), want accepted = %v", tc.format, accepted, rec.Code, tc.canQueue)
			}
			if !tc.canQueue {
				if n := atomic.LoadInt32(&calls); n != 0 {
					t.Errorf("the refused batch still ran %d items", n)
				}
				return
			}
			// The picker's field must be the one the handler reads, or the count
			// is discarded between the two even when every decision above agrees.
			if !strings.Contains(page, `name="`+batchCountField+`"`) {
				t.Errorf("the count picker does not emit %q, so its value cannot reach the "+
					"handler that reads it:\n%s", batchCountField, page)
			}
			waitBatchDone(t, srv)
			if n := atomic.LoadInt32(&calls); n != 2 {
				t.Errorf("the endpoint the primary button names ran %d items for count=2 — "+
					"the user's count was silently discarded", n)
			}
		})
	}
}
