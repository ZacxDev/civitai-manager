package web

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/ZacxDev/civitai-manager/internal/comfy"
	"github.com/ZacxDev/civitai-manager/internal/store"
	g "maragu.dev/gomponents"
	h "maragu.dev/gomponents/html"
)

// ── RUN READINESS ────────────────────────────────────────────────────────────
//
// Today a user learns a workflow cannot run only AFTER clicking Generate: the run
// aborts at preflight and the terminal panel explains what is missing. This is the
// same question answered BEFORE the click — "needs 1 node type and 3 model files".
//
// 🔴 IT MAKES NO NETWORK REQUEST. NONE. Not /object_info, not ComfyUI-Manager, not
// the Comfy Registry, not CivitAI. This runs on a PAGE VIEW; every one of those
// lookups belongs at settle-time, where preflightFailureResult does them ONCE per
// failed run. Deliberate consequences:
//
//   - It counts MISSING NODE TYPES, never node PACKS. Attributing a class to a pack
//     is `attributeMissingNodes` — ComfyUI-Manager on loopback, then the static
//     extension-node-map, then the Registry. A "1 pack" figure here would be a count
//     this code did not compute.
//   - The node schema comes from the 0019 comfy_model_cache row (written by a run,
//     dropped by a library scan — see comfy_model_cache.go), never from a fetch. A
//     cold cache is therefore the normal state on a fresh install and is reported as
//     such, not guessed at.
//
// 🔴 FAIL DIRECTION: it must NEVER answer "ready" when it could not check. Every
// unreadable input — cold cache, undecodable blob, a conversion that errored, a
// conversion that only warned — resolves to readinessUnknown WITH A NAMED REASON.
// Claiming ready on a cold cache would be the maturity-control fail-OPEN bug in a
// new place: a control that tells the user everything is fine because nothing ran.
//
// 🔴 THE MISSING COUNTS ARE A LOWER BOUND WHEN THE GRAPH IS INCOMPLETE.
// ConvertUIToAPI CUTS a node whose class this ComfyUI does not have, and the cut
// node's own model references vanish with it — the same fact runResult.GraphIncomplete
// carries on the failure card. The copy says "at least" and names why; it must not
// claim completeness the bound does not have.
//
// WHAT IT REUSES rather than re-deciding: comfy.ConvertUIToAPIResult, comfy.Preflight,
// Server.localHaveFile and — the load-bearing one — evalRunGate, realRun's own
// never-submit predicate, which also performs the mergeMissingNodes union.
//
// 🔴 THAT GATE IS SHARED, NOT MIRRORED, AND IT MUST STAY THAT WAY. This function
// originally re-implemented the decision from the three preflight counts plus
// conv.Warnings, and the copy was strictly weaker than the original: it never read
// report.OK, so an api graph comfy.Preflight could not parse — which reports OK:false
// with all three lists EMPTY — fell through every count and rendered "Ready". See
// run_gate.go for the measured blast radius. Mutating the gate must break tests on
// BOTH surfaces; that is the evidence there is only one copy.
//
// The one thing it deliberately does NOT share is realRun's per-run mutation of the
// graph (mode selection, parameter overrides, substitutions): this answers about the
// STORED workflow, which is what the page is showing.

// runReadinessID is the STABLE container the readiness fragment lazily loads into.
// Its innerHTML is swapped, never the node itself — the repo's streaming-fragment
// invariant (a self-replacing node cannot be re-targeted).
const runReadinessID = "cm-run-readiness"

// ── READINESS YIELDS TO A RUN ────────────────────────────────────────────────
//
// v0.1.104 shipped this line and v0.1.105 reworked the run-failure panel. Each was
// verified alone; together, on a workflow that cannot run, they stacked FLUSH — a
// 36px-tall "! A run needs at least 1 node type and 3 model files that are not
// installed. The count may be low…" sitting at y=1802–1838 directly on top of
// "⚠ Run failed — 3 model files and 1 custom node are missing. This is a lower
// bound…" at y=1838. Gap: 0px. Same counts, same lower-bound caveat, twice.
//
// 🔴 THE RULE IS GENERAL, NOT A PATCH FOR THAT ONE PAIRING: once a run for this
// workflow exists, the RUN is the fresher and more authoritative answer to the
// question the readiness line asks, so the line yields. That covers all four
// terminal shapes, not just the failure one:
//
//	failed   — the panel states the same counts AND carries the install actions.
//	           Pure duplication; this is the reported bug.
//	done     — the run SUCCEEDED, which is proof by execution that it could run.
//	           A surviving "needs 3 model files" is not redundant, it is FALSE.
//	stopped  — the panel says only "Run stopped." So the line is not duplicated
//	           here — but it is just as stale, and for the same reason (below).
//	running  — the decision the line exists to inform has already been made. An
//	           amber "!" beside a spinner is noise, and letting it survive into the
//	           settle is precisely how the 0px stack happened.
//
// WHY "STALE" IS THE LOAD-BEARING WORD, AND WHY stopped/running ARE IN. The line is
// fetched ONCE, on load, and is never re-fetched within a page (see runZone's
// KNOWN AND DEFERRED note). A run does not merely pass time — the failure panel's
// own Install CTAs DOWNLOAD MODEL FILES, and download-then-run installs before it
// submits. So after any run, the first-paint snapshot may be describing files that
// now exist. Keeping it for the two states where it is not duplicated would keep it
// exactly where it is most likely to be wrong.
//
// CONSEQUENCE, ACCEPTED: "Run again" does NOT bring the line back. It cannot be a
// return to the idle state — the click starts a run — and after a run the panel is
// strictly the better instrument (it is recomputed at every settle; this is not).
// A reload restores it, and the reload is what makes it true again.
//
// FAIL DIRECTION: suppression REMOVES an advisory, it never adds a claim. The worst
// case is a user who must reload to see a line they could already act on from the
// panel — versus the alternative failure, a stale line contradicting a fresh result.

// runStatusHoldsARunFor reports whether #run-status is showing a report about a run
// OF THIS WORKFLOW — running, stopped, failed or done alike.
//
// 🔴 IT IS THE ONE RULE, AND runStatusFragment READS IT TOO. That function's idle
// early-return is the exact negation of this predicate, so "the readiness line is
// hidden" and "#run-status is occupied" are decided by ONE expression rather than by
// two that agree today. Two containers swapped by DIFFERENT requests is how the
// original bug happened; a rule enforced in only one of them would re-create it.
//
// A run belonging to a different workflow is deliberately NOT held: the page shows a
// one-line "a run is already in progress for another workflow" note there, which
// duplicates nothing, and the readiness answer for THIS workflow is still a live,
// un-stale, pre-click answer.
func runStatusHoldsARunFor(snap runSnapshot, wfID int64) bool {
	return snap.Started && snap.WorkflowID == wfID
}

// runReadinessCleared is the OUT-OF-BAND emptying of #cm-run-readiness that rides
// along with a #run-status response.
//
// 🔴 IT ONLY EVER CLEARS — it never re-arms the lazy loader, and that asymmetry is
// deliberate in three ways:
//
//   - Monotonic. The line is emitted at page render if and only if the page was
//     idle, and from the first response that carries a run it is only ever emptied.
//     ⚠ NOT "cleared exactly once" — this line used to say that and it is FALSE.
//     The OOB element rides EVERY run-status response, so for the whole duration of
//     a run htmx outerHTML-swaps an already-empty #cm-run-readiness once per ~1 s
//     poll tick. That is idempotent and costs one empty <div> per tick; the property
//     that actually matters is that it NEVER RE-ARMS, so the ~4.66 MB /object_info
//     decode behind the line cannot be re-paid per tick.
//   - A re-arming OOB would have to name a workflow, and not every response that
//     writes into #run-status has a usable id to name. handleWorkflowRunStop takes
//     its id from the POST body and falls back to 0, which would have re-armed the
//     container against /workflows/0/run/readiness and replaced a correctly-hidden
//     line with "Not checked — this workflow is no longer in your library".
//     ⚠ THAT ZERO IS A HAND-CRAFTED POST, NOT THE SHIPPED UI, and this line used to
//     present it as the live shape. runStopVals (run_pages.go) ALWAYS writes
//     workflow_id, and the only Stop button is rendered by runRunning with the
//     caller's real id — so no click in the app can produce it. The conclusion is
//     unaffected: it rests on the monotonicity above, not on this reachability.
//   - It closes the load-race for free: a readiness GET still in flight when the
//     user clicks Generate lands in a node this swap has already detached. (The
//     handler refuses independently — see handleWorkflowRunReadiness — because a
//     detached-target swap is htmx behaviour, not a guarantee we should rest on.)
//
// hx-swap-oob="true" is an outerHTML replace, which is safe here and NOT a breach of
// the streaming invariant: #cm-run-readiness is a one-shot lazy target, not a
// re-arming poller node, and the replacement drops its hx-get so nothing can refill
// it. Returns an empty group when nothing needs clearing, so an idle response is
// byte-identical to before.
func runReadinessCleared(snap runSnapshot, wfID int64) g.Node {
	if !runStatusHoldsARunFor(snap, wfID) {
		return g.Group(nil)
	}
	return h.Div(h.ID(runReadinessID), hx("swap-oob", "true"))
}

// readinessState is the three-way answer. It is rendered as a data-readiness
// attribute so a test can assert the STATE rather than the prose (prose that a
// failure message can contain has produced vacuous guards here before).
type readinessState string

const (
	readinessReady   readinessState = "ready"
	readinessNeeds   readinessState = "needs"
	readinessUnknown readinessState = "unknown"
)

// readinessReason names WHY an unknown answer is unknown. Empty for ready/needs.
type readinessReason string

const (
	// reasonColdCache — no 0019 row at all. The normal fresh-install state.
	reasonColdCache readinessReason = "cold"
	// reasonUnreadableSchema — the row exists but could not be read or decoded, or
	// decoded to ZERO node types. The last case matters on its own: an empty schema
	// makes EVERY class in the graph look missing, which would render a confidently
	// enormous and completely wrong count.
	reasonUnreadableSchema readinessReason = "schema"
	// reasonConvertFailed — ConvertUIToAPIResult returned an error, so there is no
	// api graph to check.
	reasonConvertFailed readinessReason = "convert"
	// reasonConvertWarned — the conversion produced warnings but nothing structured
	// is missing. realRun's never-submit gate REFUSES this graph, so "ready" would be
	// a straight lie; but the warnings are not a count of anything, so "needs N" would
	// be one too.
	reasonConvertWarned readinessReason = "warnings"
	// reasonMultiPipeline — the workflow is a multi-mode template: it ships several
	// mutually-exclusive pipelines in ONE graph and the user picks which one runs.
	// WHAT IT NEEDS DEPENDS ON THE PICK, and this fragment cannot know the pick.
	//
	// 🔴 THE SUPPRESSION IS THE POINT — see workflowReadiness for the measurement.
	reasonMultiPipeline readinessReason = "modes"
	// reasonNoWorkflow — there is no workflow to check: the row is gone (deleted or
	// re-scanned away since the page rendered), or the read itself failed.
	//
	// 🔴 IT IS NOT reasonColdCache, which is what this used to answer. The cold-cache
	// text tells the user "Run any workflow once and it will be checked from then on"
	// — advice that is false here (the cache may be perfectly warm) and useless here
	// (running a workflow does not bring back a deleted row). Every reason in this
	// list must name a DIFFERENT cause, or "could not check" is indistinguishable
	// from a bug in this code.
	reasonNoWorkflow readinessReason = "gone"
	// reasonUnusableGraph — the stored graph is not an api graph this app can
	// validate: comfy.Preflight either could not parse it (a UI graph stored as api, a
	// JSON array, a bare string, garbage bytes) or parsed it to ZERO nodes.
	//
	// 🔴 THIS IS THE STATE THAT USED TO RENDER "Ready". Preflight answers `OK:false`
	// with all three lists EMPTY for a graph it could not parse, and `OK:true` with
	// all three lists empty for `{}` — so a count-driven decision sees a spotless
	// report either way. It is separated from reasonConvertFailed because that one is
	// the UI→API CONVERTER erroring; this one is the api graph itself being unusable,
	// which is reachable with no conversion in sight.
	reasonUnusableGraph readinessReason = "graph"
)

// readinessView is the resolved answer the fragment renders.
type readinessView struct {
	state  readinessState
	reason readinessReason
	// missingNodes / missingModels / badOptions are counts ONLY — no names, no
	// attribution. See the file header.
	missingNodes  int
	missingModels int
	badOptions    int
	// graphIncomplete is set when the converter had to REMOVE an active node whose
	// class is not installed. It makes missingModels a LOWER bound.
	graphIncomplete bool
}

// localHaveFile reports whether the local library indexes a file with this
// basename. It is the `localHave` predicate comfy.Preflight takes, and it lives here
// as ONE function because two callers now need it (realRun and the readiness
// fragment) — a second inline copy is how the same decision drifts apart.
//
// A store error reads as "not present", matching realRun's long-standing behaviour:
// preflight over-reporting a missing file is recoverable (the user is told to install
// something they already have), under-reporting it is not.
func (s *Server) localHaveFile(name string) bool {
	ok, _ := s.store.HasLocalFileNamed(name)
	return ok
}

// workflowReadiness answers "could this workflow run right now?" from local state
// only. See the file header for the fail direction and the lower-bound caveat.
func (s *Server) workflowReadiness(wf *store.Workflow) readinessView {
	unknown := func(r readinessReason) readinessView {
		return readinessView{state: readinessUnknown, reason: r}
	}
	if wf == nil {
		return unknown(reasonNoWorkflow)
	}
	// 🔴 A MULTI-MODE TEMPLATE HAS NO SINGLE ANSWER, so this must not invent one.
	//
	// realRun applies comfy.ApplyModeSelection BEFORE converting, un-bypassing the
	// pipeline the user picked; this fragment converts the STORED graph verbatim. On a
	// template those are DIFFERENT GRAPHS. Reproduced end to end on a two-pipeline
	// fixture (TestReadinessSuppressesTheClaimForAMultiPipelineTemplate): the stored
	// graph preflights clean — the line said "Ready" — while the run with pipeline B
	// picked reports a missing model file. And the picker is pre-selected from the
	// ACTIVE PRESET'S stored mode (runModesPanelSelected), so the divergence exists at
	// FIRST PAINT, before any click.
	//
	// WHY SUPPRESS RATHER THAN FOLLOW THE PICK. Three shapes were on the table:
	//   (a) apply the mode selection here. The container is `hx-trigger="load"` with
	//       no params and sits OUTSIDE #run-params (what the picker swaps), so it can
	//       only ever answer about the mode that was current at first paint — the next
	//       pick silently makes it wrong again. (a) MOVES the lie; it does not remove it.
	//   (b) (a) plus re-triggering the fragment on every mode change. That is the
	//       honest full answer and it is real feature work — mode params on a GET
	//       fragment, a second hx-target on the picker, and the same staleness problem
	//       the run poller has. Deferred deliberately, not forgotten.
	//   (c) this: say the line cannot answer, and name why.
	// (c) is the only one of the three that is correct at first paint AND after every
	// pick, and it costs almost nothing: MEASURED on a copy of the operator's real
	// database, 3 of 71 workflows carry a mode selector (ids 589, 588, 581).
	//
	// Asked through detectWorkflowModes (run_modes.go), which is the single place
	// every other run surface asks it. This site open-coded the same
	// `Format == UI && len(DetectModeSelectors(graph)) > 0` test — a third copy of a
	// predicate in the PR whose whole thesis is one rule, one place. A drift here
	// would be silent in the worst direction: the readiness line would answer for a
	// template the run surfaces treat as multi-mode.
	if len(detectWorkflowModes(wf)) > 0 {
		return unknown(reasonMultiPipeline)
	}

	// The 0019 row, decoded ONCE per distinct payload rather than once per page view
	// — a ~73-113 ms decode on the operator's real 4.66 MB row. See
	// run_readiness_schema.go for the memo, its key, and what it does NOT save.
	info, sreason := s.readinessSchema()
	if sreason != "" {
		return unknown(sreason)
	}

	apiGraph := json.RawMessage(wf.Graph)
	var conv comfy.ConversionResult
	if wf.Format == store.WorkflowFormatUI {
		c, cerr := comfy.ConvertUIToAPIResult(apiGraph, info)
		if cerr != nil {
			return unknown(reasonConvertFailed)
		}
		conv = c
		apiGraph = conv.APIGraph
	}

	report := comfy.Preflight(apiGraph, info, s.localHaveFile)
	// 🔴 THE SAME never-submit decision realRun makes, from the SAME function — not a
	// second copy of it. evalRunGate also performs the MissingNodes union (a UI graph
	// has the unknown class cut out before preflight sees it, an api graph keeps it
	// verbatim), so report.MissingNodes below is already merged. See run_gate.go.
	gate := evalRunGate(conv, &report)
	if !gate.blocked() {
		return readinessView{state: readinessReady}
	}

	v := readinessView{
		missingNodes:    len(report.MissingNodes),
		missingModels:   len(report.MissingModels),
		badOptions:      len(report.BadOptions),
		graphIncomplete: gate.GraphIncomplete,
	}
	if v.missingNodes+v.missingModels+v.badOptions > 0 {
		v.state = readinessNeeds
		return v
	}
	// Blocked with nothing to count. Name WHICH of the gate's countless conditions
	// fired — "could not check" with no cause is indistinguishable from a bug here.
	if gate.NoNodes || !gate.ReportOK {
		// !ReportOK with every list empty is comfy.Preflight's parse-failure return,
		// which also leaves Nodes at 0; the second test is belt-and-braces so a future
		// not-OK-with-no-specifics answer cannot fall through to a warning it did not
		// produce.
		return unknown(reasonUnusableGraph)
	}
	return unknown(reasonConvertWarned)
}

// runReadinessFragment renders the ONE quiet line: a state glyph plus a sentence.
//
// It is deliberately QUIET. Most workflows are fine, and this repo has already
// removed one flat "this needs custom nodes" assertion for crying wolf — a banner
// that fires on a healthy workflow gets ignored, taking the unhealthy case with it.
// The glyph reuses .cm-status-ico, the indicator already in this row, so a reader
// does not learn a second visual vocabulary and no new colour pair enters the
// contrast table.
func runReadinessFragment(v readinessView) g.Node {
	glyph, state := readinessGlyph(v.state)
	sentence := readinessSentence(v)
	return h.Div(h.Class("cm-readiness"),
		dataAttr("readiness", string(v.state)),
		readinessReasonAttr(v.reason),
		h.Span(h.Class("cm-status-ico"), dataAttr("state", state),
			g.Attr("aria-hidden", "true"), g.Text(glyph)),
		h.Span(g.Text(sentence)),
	)
}

// readinessReasonAttr emits data-reason only for an unknown answer.
func readinessReasonAttr(r readinessReason) g.Node {
	if r == "" {
		return g.Group(nil)
	}
	return dataAttr("reason", string(r))
}

// readinessGlyph maps a state to the glyph and the .cm-status-ico[data-state] key.
// "warn" is the one state that icon did not already have; ok/off are its existing
// success/dimmed colours.
func readinessGlyph(s readinessState) (glyph, state string) {
	switch s {
	case readinessReady:
		return "✓", "ok"
	case readinessNeeds:
		return "!", "warn"
	default:
		return "○", "off"
	}
}

// readinessSentence is the whole of the copy. Four rules it must keep:
//
//   - "ready" claims only what was checked: node types and model files against the
//     cached schema and the local library. It does not promise the run succeeds.
//   - "needs" states COUNTS, and says "at least" whenever the graph was incomplete.
//   - "unknown" always names WHY, and never implies the workflow is fine.
//   - 🔴 EVERY STATE SAYS WHAT IT COUNTS, and the subject is "a run".
//
// The fourth rule is the fix for a reported confusion: this line said "3 model
// files" on a page whose "Referenced resources" card showed 6 chips, and the
// original explanation — the converter cut a node — was only ONE of THREE reasons
// the two numbers differ:
//
//  1. a cut node's model references vanish with it (the "at least" hedge, and the
//     only source that hedge covers);
//  2. wf.Resources comes from ExtractResourcesAny → extractResourcesUI with
//     activeOnly=false, so it DELIBERATELY includes bypassed pipelines;
//  3. the UI extractor scans every node's widgets_values while ExtractResources on
//     the api graph looks only at loader classes.
//
// ⚠ A FOURTH SOURCE WAS LISTED HERE AND IT IS NOT REAL: "wf.Resources is a snapshot
// written at INSERT time and never recomputed". Verified in internal/store/
// workflows.go — UpsertWorkflowByPath's ON CONFLICT sets `resources =
// excluded.resources` in the SAME statement as `graph = excluded.graph`, and no
// other `UPDATE workflows` in the package touches either column (they move
// name/model_id/version_id/is_golden/updated_at only). So a re-scan refreshes both
// together and the plain InsertWorkflow path writes both exactly once. resources
// cannot go stale RELATIVE TO THE GRAPH, which is the only staleness that could
// make these two figures disagree. Do not re-add it.
//
// On a template pack (2) is likely the dominant source — the repo documents 15-31
// optional groups on pack 1386234 — and the hedge does not fire for it at all. So
// the label cannot enumerate causes and stay true; it states the SCOPE instead,
// which is true for all three. The chips card states its own scope (see
// workflow_pages.go). Neither figure is changed: they count different things and
// both are correct about the thing they count.
func readinessSentence(v readinessView) string {
	switch v.state {
	case readinessReady:
		return "Ready — every node type and model file a run would load is installed."
	case readinessNeeds:
		return readinessNeedsSentence(v)
	default:
		return "Not checked — " + readinessReasonText(v.reason)
	}
}

// readinessNeedsSentence composes the counts. The clauses are separate sentences
// because they are separate problems: something is not installed, versus a saved
// option value that no longer exists on a node that IS installed.
func readinessNeedsSentence(v readinessView) string {
	var out []string
	parts := readinessCountParts(v)
	if len(parts) > 0 {
		lead := "A run needs "
		if v.graphIncomplete {
			lead = "A run needs at least "
		}
		// The relative clause has to agree with the TOTAL, not with the last part.
		// The live sweep over the operator's 71 real workflows caught this rendering
		// "Needs 1 model file that are not installed" — the one shape no fixture with
		// distinct multi-item counts can produce.
		tail := " that are not installed."
		if v.missingNodes+v.missingModels == 1 {
			tail = " that is not installed."
		}
		out = append(out, lead+joinAnd(parts)+tail)
	}
	if v.badOptions > 0 {
		count := readinessCount(v.badOptions, "saved option value", "saved option values")
		if len(parts) == 0 {
			// 🔴 THE ONLY FINDING, so it has to carry the lead itself. With zero missing
			// nodes and zero missing models `parts` is empty, the "A run needs …" sentence
			// above never renders, and the line was an amber ! beside a bare statement
			// with nothing naming it as the thing to act on. Reachable (an installed node
			// whose saved combo value drifted) and previously untested.
			out = append(out, "A run needs "+count+" updated — no longer valid on your installed nodes.")
		} else {
			verb := " is "
			if v.badOptions != 1 {
				verb = " are "
			}
			out = append(out, count+verb+"no longer valid on your installed nodes.")
		}
	}
	if v.graphIncomplete {
		// The lower bound, named. A cut node's model references went with it, so the
		// FILE count in particular can only be low, never high.
		out = append(out, "The count may be low: a node type this ComfyUI does not have "+
			"was dropped from the graph, and any files it referenced went with it.")
	}
	return strings.Join(out, " ")
}

// readinessCountParts renders the two installable categories, omitting a zero.
func readinessCountParts(v readinessView) []string {
	var parts []string
	if v.missingNodes > 0 {
		parts = append(parts, readinessCount(v.missingNodes, "node type", "node types"))
	}
	if v.missingModels > 0 {
		parts = append(parts, readinessCount(v.missingModels, "model file", "model files"))
	}
	return parts
}

// readinessReasonText is the human half of a readinessReason. Each one names a
// DIFFERENT cause, because "could not check" with no cause is indistinguishable from
// a bug in this code.
func readinessReasonText(r readinessReason) string {
	switch r {
	case reasonNoWorkflow:
		return "this workflow is no longer in your library, so there is nothing to check. Reload the page."
	case reasonMultiPipeline:
		return "this workflow holds several pipelines, so what it needs depends on which one you pick above. Generate checks the one you picked."
	case reasonColdCache:
		return "this app has not seen your ComfyUI's node list yet. Run any workflow once and it will be checked from then on."
	case reasonUnreadableSchema:
		return "the stored copy of your ComfyUI's node list could not be read. Running any workflow replaces it."
	case reasonConvertFailed:
		return "this workflow's graph could not be converted to API format."
	case reasonConvertWarned:
		return "this workflow's graph did not convert cleanly, so it was not checked. Generate will report the details."
	case reasonUnusableGraph:
		return "this workflow's saved graph has no nodes this app can read, so there is nothing to check. Generate will refuse it too."
	default:
		return "this workflow could not be checked."
	}
}

// readinessCount renders "1 thing" / "N things". A count is never rendered without
// its noun, so the two can never disagree. (The package's existing `plural` returns
// only the suffix and cannot carry the number; joinAnd in nodepack_pages.go is
// reused as-is.)
func readinessCount(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}
