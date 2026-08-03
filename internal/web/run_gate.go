package web

import "github.com/ZacxDev/civitai-manager/internal/comfy"

// ── THE NEVER-SUBMIT GATE, IN ONE PLACE ──────────────────────────────────────
//
// TWO surfaces answer the same question about the same graph:
//
//   - realRun (run_handlers.go) — "may I submit this to ComfyUI?"
//   - workflowReadiness (run_readiness.go) — "may I tell the user it is ready?"
//
// 🔴 THEY MUST NEVER DISAGREE, AND THEY DID. The readiness line was written as a
// second, independent copy of the decision, and its copy was strictly WEAKER: it
// keyed on the three preflight COUNTS plus conv.Warnings and never read
// report.OK. comfy.Preflight answers `OK:false` with all three lists EMPTY for a
// graph it could not parse at all, so an unparseable graph — a UI graph stored as
// api, a JSON array, a bare string, garbage bytes — fell through every count and
// rendered "Ready — every node type and model file this workflow references is
// installed" about a graph realRun refuses to submit. Measured by an adversarial
// audit: 6 of 10 probed unusable inputs rendered `ready`, all reachable end to end
// through POST /workflows/import-png.
//
// So the decision lives HERE, once, and both callers ask it. This is the repo's
// "one rule, one place" rule applied to the exact predicate whose duplication the
// original gate's own comment warned about ("a future edit to the failure branch
// must not be able to make a holed graph submittable") — a second copy defeats
// that warning by construction, because the future edit lands in the copy.

// runGate is the resolved never-submit decision for one prepared graph. Each field
// vetoes on its OWN, independently of the others — that independence IS the
// invariant, and it is why blocked() is a written-out disjunction rather than
// something derived from report.OK.
type runGate struct {
	// ConvWarned — the UI→API conversion emitted any warning (an unresolved link, an
	// ambiguous bypass, an unmapped widget). Blunt, but fail-closed and correct: a
	// graph whose links did not resolve is not the graph the user asked to run.
	ConvWarned bool
	// GraphIncomplete — the converter had to CUT one or more ACTIVE nodes because
	// their class is not installed. The class is then no longer IN the document
	// comfy.Preflight validates, so preflight sees a perfectly well-formed graph and
	// can legitimately answer OK. Running it runs a workflow with a hole in it, and
	// it also makes any model-file count a LOWER bound (the cut node's own model
	// references went with it).
	GraphIncomplete bool
	// NoNodes — comfy.Preflight validated ZERO nodes. That is "this is not a graph",
	// and it is invisible to every other signal here: the three preflight lists are
	// empty for a perfect graph too, and OK is `true` for `{}`. See
	// comfy.PreflightReport.Nodes.
	NoNodes bool
	// ReportOK is comfy.Preflight's own verdict, read AFTER evalRunGate's merge (a
	// cut class is folded into MissingNodes and OK is forced false, so both callers
	// see the same union).
	ReportOK bool
}

// blocked reports whether this graph must not be submitted — and, equivalently,
// must never be described to the user as ready.
//
// 🔴 THE DISJUNCTION IS WRITTEN OUT ON PURPOSE. Do not collapse it into
// `!ReportOK`: three of the four conditions are true of graphs preflight itself
// calls OK, so any collapse silently re-opens exactly one of the holes above.
//
// ⚠ MEASURED, so nobody re-derives it: deleting `GraphIncomplete` from this
// disjunction leaves the WHOLE suite green — BOTH surfaces. That is not a coverage
// hole, it is the SECOND cover: evalRunGate below already forces `report.OK = false`
// for an incomplete graph, so `!ReportOK` catches it too, and the merged
// MissingNodes makes the readiness answer "needs" rather than "ready". Both covers
// are deliberate; do not delete either on the grounds that the other exists.
//
// Deleting any of the OTHER three turns BOTH surfaces red. Named, not counted —
// this repo has twice shipped a comment-embedded count that went stale and was then
// trusted, and a test name at least fails loudly (a grep for it finds nothing) where
// a number just quietly lies:
//
//   - ConvWarned → TestReadinessConvertWarnedIsUnknownNotReady (readiness) and
//     TestRunNeverSubmitsAWarnOnlyGraph (run).
//   - NoNodes → every case of the shared `unusableGraphs` table, through
//     TestReadinessNeverCallsAnUnusableGraphReady and
//     TestRunNeverSubmitsAnUnusableGraph. One table, both surfaces — that is the
//     consolidation evidence.
//   - ReportOK → 🔴 DO NOT EXPECT A FAILURE COUNT HERE, AND DO NOT WRITE ONE DOWN.
//     Measured: `go test ./internal/web/` does not survive the deletion. It ends in
//     `panic: test timed out`, having printed only four top-level failures — none of
//     them a gate test — before the truncation ate the rest. An un-blocked bad graph
//     is SUBMITTED, and a run poller then waits on a run that never settles. So the
//     honest statement is the mechanism: without ReportOK the gate stops reading
//     preflight's own verdict at all, and the suite hangs rather than reporting.
//     Any number you see attached to this condition was truncated, not measured.
func (g runGate) blocked() bool {
	return g.ConvWarned || g.GraphIncomplete || g.NoNodes || !g.ReportOK
}

// evalRunGate resolves the gate, MUTATING report so both callers read one merged
// answer: a class the converter cut is unioned into report.MissingNodes (preflight
// cannot see it — the node is gone from the document it validated) and OK is forced
// false, because a holed graph is not OK however clean the remains look.
//
// conv's fields are both EMPTY for an api-format workflow — nothing was converted,
// so nothing was cut and there is nothing to warn about — which is what keeps the
// api-format path behaving exactly as it always has.
func evalRunGate(conv comfy.ConversionResult, report *comfy.PreflightReport) runGate {
	g := runGate{
		ConvWarned:      len(conv.Warnings) > 0,
		GraphIncomplete: len(conv.MissingNodeTypes) > 0,
		NoNodes:         report.Nodes == 0,
	}
	if g.GraphIncomplete {
		report.MissingNodes = mergeMissingNodes(report.MissingNodes, conv.MissingNodeTypes)
		report.OK = false
	}
	g.ReportOK = report.OK
	return g
}

// emptyGraphWarning is what a run reports when the gate stops it on NoNodes and
// there is nothing structured to show. It is phrased as the engine's own finding
// because it takes the same terminal panel as a conversion warning.
//
// This is the ONE case realRun did not already refuse: it used to hand `{}` (or a
// graph stored in the wrong format) straight to ComfyUI and let ComfyUI's
// submit-time validation reject it. Refusing locally is the same fail-closed
// direction as the rest of this gate, and it is what lets the readiness line and
// the run agree about a graph that is not a graph.
const emptyGraphWarning = "this workflow's saved graph contains no nodes this app can read, " +
	"so there is nothing to submit. Re-import the workflow from ComfyUI."
