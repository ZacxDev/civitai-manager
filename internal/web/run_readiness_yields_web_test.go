package web

import (
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/store"
)

// ─────────────────────────────────────────────────────────────────────────────
// THE READINESS LINE YIELDS TO A RUN (runStatusHoldsARunFor, run_readiness.go).
//
// v0.1.104 shipped the pre-click readiness line and v0.1.105 reworked the
// run-failure panel. Each was verified alone. Measured together in the operator's
// live browser on workflow 590:
//
//	readiness line   y 1802–1838 (h=36)  "! A run needs at least 1 node type and
//	                                      3 model files that are not installed.
//	                                      The count may be low…"
//	failure panel    y 1838              "⚠ Run failed — 3 model files and 1 custom
//	                                      node are missing. This is a lower bound…"
//	                 GAP: 0px
//
// 🔴 ASSERTIONS HERE ARE ON ATTRIBUTES, NEVER PROSE. The two surfaces state the
// SAME counts and the SAME lower-bound caveat, so any substring a duplication guard
// could look for is present on both by construction — that is the bug. This package
// has already shipped a `Contains(body, "disabled")` that matched `hx-disabled-elt`
// on a live button, and a non-vacuity marker that was a substring of the failure
// headline it was meant to exclude. So:
//
//	the readiness surface is present  ⇔  data-readiness="…"   (runReadinessFragment)
//	the run-panel surface is present  ⇔  data-run-seq="…"     (dataRunSeq)
//
// Neither attribute name occurs in the other's markup, and neither is prose.
// ─────────────────────────────────────────────────────────────────────────────

// runShape is one of the five states #run-status can be in for a given workflow.
// The names are the ones the PR's terminal-state matrix uses.
type runShape string

const (
	shapeNoRun   runShape = "no run"
	shapeRunning runShape = "running"
	shapeFailed  runShape = "failed"
	shapeDone    runShape = "succeeded"
	shapeStopped runShape = "stopped"
)

// installRunShape parks the server's run job in the given shape for wfID.
//
// It writes runJob directly, under runMu, rather than driving a real run — four of
// the five shapes are TRANSIENT under a real run (running settles; stopped needs a
// Stop to land inside the window) and a test that races the settle would be timing
// -dependent for no gain. The real-run path is exercised end to end by
// TestAFailedRunLeavesExactlyOneAnswerOnThePage below, which is where the fixture
// has to be genuine; here the point is coverage of every state, not of the runner.
//
// 🔴 seq is non-zero on purpose: dataRunSeq OMITS the attribute for seq<=0, so a
// zero-seq job would make every "the run panel is showing" assertion below silently
// unobservable — the marker would be absent for the wrong reason.
func installRunShape(t *testing.T, srv *Server, wfID int64, shape runShape) {
	t.Helper()
	srv.runMu.Lock()
	defer srv.runMu.Unlock()
	if shape == shapeNoRun {
		srv.runJob = nil
		return
	}
	j := &runJob{workflowID: wfID, seq: 7, promptID: "p1"}
	switch shape {
	case shapeRunning:
		j.running, j.phase, j.message = true, runPhaseRunning, "Generating…"
	case shapeFailed:
		j.phase, j.message = runPhaseFailed, "Run failed."
	case shapeDone:
		j.phase, j.message = runPhaseDone, "Run complete."
	case shapeStopped:
		j.stopped, j.phase, j.message = true, runPhaseFailed, "Run stopped."
	default:
		t.Fatalf("unknown run shape %q", shape)
	}
	srv.runJob = j
}

// answeringSurfaces reports which of the two surfaces currently answer "can this
// workflow run?" — the readiness line and the run panel — by driving the REAL
// routes: the lazy fragment's own endpoint and the run poller's.
//
// It deliberately reads the readiness endpoint rather than the page, because that is
// what the browser holds: the line reaches the DOM through that GET, and the page
// only decides whether the GET is issued at all. Both halves are asserted; see
// TestReadinessRequestIsNotEvenIssuedWhileARunIsShowing for the page half.
func answeringSurfaces(t *testing.T, srv *Server, id string) (readinessShown, panelShown bool) {
	t.Helper()
	rd := get(t, srv, "/workflows/"+id+"/run/readiness")
	if rd.Code != 200 {
		t.Fatalf("readiness = %d, want 200", rd.Code)
	}
	st := get(t, srv, "/workflows/"+id+"/run/status")
	if st.Code != 200 {
		t.Fatalf("run status = %d, want 200", st.Code)
	}
	return strings.Contains(rd.Body.String(), `data-readiness="`),
		strings.Contains(st.Body.String(), `data-run-seq="`)
}

// TestReadinessAndTheRunPanelNeverBothAnswer is THE guard: across every run state,
// and with the readiness question both ANSWERABLE and UNANSWERABLE, at most one
// surface answers it.
//
// The answerability axis is not decoration. "Unanswerable" renders
// data-readiness="unknown" — a DIFFERENT branch of runReadinessFragment with
// different copy — and a suppression keyed on the answer rather than on the run
// state would pass the answerable half and leak the unknown line under a failure
// panel. Both axes are driven on the same server, so the fixture cannot differ
// between them by accident.
func TestReadinessAndTheRunPanelNeverBothAnswer(t *testing.T) {
	for _, ans := range []struct {
		name string
		warm bool
	}{
		{"readiness answerable (warm node-list cache)", true},
		{"readiness unanswerable (cold node-list cache)", false},
	} {
		t.Run(ans.name, func(t *testing.T) {
			srv := newReadinessServer(t)
			if ans.warm {
				seedObjectInfo(t, srv, readinessInfo)
			}
			id := seedWorkflow(t, srv, store.WorkflowFormatAPI, readinessAPIGraph)
			wfID, err := strconv.ParseInt(id, 10, 64)
			if err != nil {
				t.Fatalf("workflow id: %v", err)
			}

			// PRECONDITION, asserted rather than assumed. With no run, the readiness
			// line MUST answer — otherwise every "not shown" result below is green for
			// the wrong reason and the whole table proves nothing. It also pins WHICH
			// answer, so the two axes are really distinct fixtures.
			installRunShape(t, srv, wfID, shapeNoRun)
			base := readiness(t, srv, id)
			if ans.warm {
				wantState(t, base, "needs", "")
			} else {
				wantState(t, base, "unknown", "cold")
			}

			for _, shape := range []runShape{shapeNoRun, shapeRunning, shapeFailed, shapeDone, shapeStopped} {
				t.Run(string(shape), func(t *testing.T) {
					installRunShape(t, srv, wfID, shape)
					readinessShown, panelShown := answeringSurfaces(t, srv, id)

					// The run panel must be showing in exactly the states that have a run,
					// and not in the one that does not. Without this the "never both" test
					// is satisfiable by a run panel that renders nothing at all.
					wantPanel := shape != shapeNoRun
					if panelShown != wantPanel {
						t.Fatalf("run panel shown = %v, want %v — the fixture does not reach "+
							"the %s state, so nothing below is measuring it", panelShown, wantPanel, shape)
					}
					if readinessShown && panelShown {
						t.Errorf("BOTH surfaces answer 'can this run?' at once in the %s state: "+
							"the readiness line (data-readiness) and the run panel (data-run-seq) "+
							"are on screen together — this is the 0px stack", shape)
					}
					// And the other direction: with no run, the line MUST still be there.
					// A suppression that simply deleted the feature would pass "never both".
					if !readinessShown && shape == shapeNoRun {
						t.Errorf("the readiness line is gone with NO run showing — it is the " +
							"pre-click answer and that is the one state it exists for")
					}
				})
			}
		})
	}
}

// TestReadinessRequestIsNotEvenIssuedWhileARunIsShowing is the OTHER half of the
// fix: the pixels are not the cost, the fetch is.
//
// A "hide it" that still renders hx-trigger="load" keeps paying the UI→API
// conversion and the ~4.66 MB /object_info decode on a page whose answer is never
// shown. So the page must emit the container WITHOUT hx-get, and the endpoint must
// refuse before it reads anything.
func TestReadinessRequestIsNotEvenIssuedWhileARunIsShowing(t *testing.T) {
	srv := newReadinessServer(t)
	srv.comfyClientFn = func() comfyClient { return &fakeComfy{} }
	seedObjectInfo(t, srv, readinessInfo)
	id := seedWorkflow(t, srv, store.WorkflowFormatAPI, readinessAPIGraph)
	wfID, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		t.Fatalf("workflow id: %v", err)
	}
	route := `hx-get="/workflows/` + id + `/run/readiness"`

	// POSITIVE CONTROL: idle, the loader IS emitted. Without this the assertion
	// below passes against a page that never had a readiness container at all.
	installRunShape(t, srv, wfID, shapeNoRun)
	idle := get(t, srv, "/workflows/"+id).Body.String()
	start, end := divExtent(t, idle, `id="`+runReadinessID+`"`)
	if !strings.Contains(idle[start:end], route) {
		t.Fatalf("idle page does not lazily fetch the readiness line — the control is "+
			"broken, so the suppression below measures nothing:\n%s", idle[start:end])
	}

	for _, shape := range []runShape{shapeRunning, shapeFailed, shapeDone, shapeStopped} {
		t.Run(string(shape), func(t *testing.T) {
			installRunShape(t, srv, wfID, shape)
			page := get(t, srv, "/workflows/"+id).Body.String()
			// The container itself must SURVIVE — it is the target the out-of-band
			// clear needs when a run starts after the page rendered.
			s, e := divExtent(t, page, `id="`+runReadinessID+`"`)
			ext := page[s:e]
			if strings.Contains(ext, route) || strings.Contains(ext, `hx-trigger="load"`) {
				t.Errorf("the readiness fetch is still armed in the %s state — the ~4.66 MB "+
					"decode is paid for an answer nothing will show:\n%s", shape, ext)
			}
			// And the endpoint refuses independently, which is what closes the race
			// where the GET was already in flight when the run started.
			if body := readiness(t, srv, id); strings.Contains(body, `data-readiness="`) {
				t.Errorf("the readiness endpoint still answers in the %s state:\n%s", shape, body)
			}
		})
	}
}

// TestRunStatusResponseCarriesTheOutOfBandClear pins the mechanism that actually
// removes the line from a page that is ALREADY rendered — the case a server-side
// check structurally cannot reach, because at Generate-time the line is on screen
// and nothing re-requests it.
//
// It asserts the OOB marker on the READINESS container specifically, by brace-
// balanced extent. A bare Contains(body, "hx-swap-oob") would be satisfied by any
// other out-of-band element the response happens to carry (#run-modes already does
// this on the preset path) — the same defect as the ` popover` assertion that was
// satisfied by a trigger's ` popovertarget`.
func TestRunStatusResponseCarriesTheOutOfBandClear(t *testing.T) {
	srv := newReadinessServer(t)
	seedObjectInfo(t, srv, readinessInfo)
	id := seedWorkflow(t, srv, store.WorkflowFormatAPI, readinessAPIGraph)
	wfID, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		t.Fatalf("workflow id: %v", err)
	}

	// With NO run the response must carry NO oob element at all: re-arming the
	// loader from a status response is what would re-pay the decode per poll tick,
	// and handleWorkflowRunStop renders with the id absent (0), which would have
	// pointed a re-armed container at /workflows/0/run/readiness.
	installRunShape(t, srv, wfID, shapeNoRun)
	if body := get(t, srv, "/workflows/"+id+"/run/status").Body.String(); strings.Contains(body, "hx-swap-oob") {
		t.Errorf("an idle run-status response must be byte-identical to before:\n%s", body)
	}

	for _, shape := range []runShape{shapeRunning, shapeFailed, shapeDone, shapeStopped} {
		t.Run(string(shape), func(t *testing.T) {
			installRunShape(t, srv, wfID, shape)
			body := get(t, srv, "/workflows/"+id+"/run/status").Body.String()
			// 🔴 STATE THE CASE BEFORE MEASURING IT. divExtent t.Fatal's on an absent
			// element, and "no element carrying id=…" is the HELPER's precondition
			// message, not this guard's — the deletion this test exists to catch
			// removes the element outright, so without this branch the whole guard
			// reports through someone else's error and names nothing about the bug.
			if !strings.Contains(body, `id="`+runReadinessID+`"`) {
				t.Fatalf("the %s run-status response carries no #%s element at all, so it "+
					"cannot clear a readiness line already on screen — the poller swaps "+
					"#run-status and nothing else ever revisits that container:\n%s",
					shape, runReadinessID, body)
			}
			s, e := divExtent(t, body, `id="`+runReadinessID+`"`)
			ext := body[s:e]
			if !strings.Contains(ext, `hx-swap-oob="true"`) {
				t.Errorf("the %s run-status response does not clear #%s out of band, so a "+
					"line already on screen survives the swap:\n%s", shape, runReadinessID, ext)
			}
			// It must clear, never REFILL: an oob element carrying the loader would
			// re-fetch on every poll tick.
			if strings.Contains(ext, "hx-get=") || strings.Contains(ext, "data-readiness=") {
				t.Errorf("the out-of-band swap must EMPTY #%s, not repopulate it:\n%s",
					runReadinessID, ext)
			}
			// The in-band half must still be there — the oob element rides ALONGSIDE
			// the fragment, and a response that lost the fragment would delete the
			// poller and the Stop control with it.
			if !strings.Contains(body, `data-run-seq="`) {
				t.Errorf("the %s response lost its run-status fragment:\n%s", shape, body)
			}
		})
	}
}

// TestAFailedRunLeavesExactlyOneAnswerOnThePage is the end-to-end reproduction of
// the reported bug, over the REAL run path: a UI-format workflow (the format of all
// 71 workflows on the operator's real database) that genuinely cannot run, driven
// through the real Generate endpoint to a real preflight failure.
//
// The four preceding tests park the job by hand; this one earns the state.
func TestAFailedRunLeavesExactlyOneAnswerOnThePage(t *testing.T) {
	srv := newReadinessServer(t)
	fake := &fakeComfy{info: mustObjectInfo(t, readinessInfo)}
	srv.comfyClientFn = func() comfyClient { return fake }
	seedObjectInfo(t, srv, readinessInfo)
	id := seedWorkflow(t, srv, store.WorkflowFormatUI, readinessUIGraph)

	// BEFORE THE CLICK the line is the whole point, and it must be the graph-
	// incomplete "needs" answer — the exact shape whose lower-bound caveat the
	// failure panel repeats verbatim. Anything else and the reproduction is not
	// reproducing the reported pair.
	before := readiness(t, srv, id)
	wantState(t, before, "needs", "")

	if rec := post(t, srv, "/workflows/"+id+"/run", url.Values{}, true); rec.Code != 200 {
		t.Fatalf("run start = %d", rec.Code)
	}
	terminal := pollRunUntilDone(t, srv, id)
	if fake.submitCalled {
		t.Fatal("the fixture SUBMITTED — this workflow was supposed to be unrunnable, " +
			"so there is no failure panel and nothing below is measuring the collision")
	}
	if !strings.Contains(terminal, `data-run-seq="`) {
		t.Fatalf("no run panel in the terminal response:\n%s", terminal)
	}

	// THE COLLISION. One answer on the page, not two.
	if strings.Contains(terminal, `data-readiness="`) {
		t.Errorf("the terminal run response carries a readiness answer beside the failure "+
			"panel:\n%s", terminal)
	}
	if body := readiness(t, srv, id); strings.Contains(body, `data-readiness="`) {
		t.Errorf("the readiness endpoint still answers after the run failed — the line the "+
			"user is looking at is re-servable and the panel below it says the same "+
			"thing:\n%s", body)
	}
	// Same reason as above: name the outright-absent case in this guard's own words
	// rather than letting divExtent's precondition fatal speak for it.
	if !strings.Contains(terminal, `id="`+runReadinessID+`"`) {
		t.Fatalf("the terminal run response carries no #%s element at all — the readiness "+
			"line the user has been looking at since first paint is never removed, which "+
			"is the reported 0px stack:\n%s", runReadinessID, terminal)
	}
	s, e := divExtent(t, terminal, `id="`+runReadinessID+`"`)
	if !strings.Contains(terminal[s:e], `hx-swap-oob="true"`) {
		t.Errorf("the terminal response does not clear the line that is already on "+
			"screen:\n%s", terminal[s:e])
	}
	// A reload settles on the same answer through the other code path.
	page := get(t, srv, "/workflows/"+id).Body.String()
	ps, pe := divExtent(t, page, `id="`+runReadinessID+`"`)
	if strings.Contains(page[ps:pe], "hx-get=") {
		t.Errorf("a reload while the failure panel is showing re-arms the readiness "+
			"fetch:\n%s", page[ps:pe])
	}
}
