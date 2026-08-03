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
// mergeMissingNodes and Server.localHaveFile — the same four decisions realRun makes.
// The one thing it deliberately does NOT share is realRun's per-run mutation of the
// graph (mode selection, parameter overrides, substitutions): this answers about the
// STORED workflow, which is what the page is showing.

// runReadinessID is the STABLE container the readiness fragment lazily loads into.
// Its innerHTML is swapped, never the node itself — the repo's streaming-fragment
// invariant (a self-replacing node cannot be re-targeted).
const runReadinessID = "cm-run-readiness"

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
		return unknown(reasonColdCache)
	}

	ent, err := s.store.GetComfyObjectInfo()
	if err != nil {
		s.log.Warn("readiness: read cached comfy object_info", "err", err)
		return unknown(reasonUnreadableSchema)
	}
	if ent == nil || len(ent.ObjectInfoJSON) == 0 {
		return unknown(reasonColdCache) // cold cache: expected, not a fault
	}
	var info comfy.ObjectInfo
	if uerr := json.Unmarshal(ent.ObjectInfoJSON, &info); uerr != nil {
		s.log.Warn("readiness: decode cached comfy object_info",
			"err", uerr, "bytes", len(ent.ObjectInfoJSON))
		return unknown(reasonUnreadableSchema)
	}
	if len(info) == 0 {
		// A well-formed but EMPTY schema is not "nothing is installed" — it is a row
		// we cannot trust. Every class in the graph would read as missing.
		s.log.Warn("readiness: cached comfy object_info decoded to zero node types",
			"bytes", len(ent.ObjectInfoJSON))
		return unknown(reasonUnreadableSchema)
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
	// The SAME union realRun's never-submit gate builds: a UI graph has the unknown
	// class cut out before preflight sees it, an api graph keeps it verbatim.
	nodes := mergeMissingNodes(report.MissingNodes, conv.MissingNodeTypes)

	v := readinessView{
		missingNodes:    len(nodes),
		missingModels:   len(report.MissingModels),
		badOptions:      len(report.BadOptions),
		graphIncomplete: len(conv.MissingNodeTypes) > 0,
	}
	if v.missingNodes+v.missingModels+v.badOptions > 0 {
		v.state = readinessNeeds
		return v
	}
	// Nothing structured is missing — but realRun still refuses a graph that only
	// WARNED, so this is not ready either. Report it as unknown rather than inventing
	// a count for something that has none.
	if len(conv.Warnings) > 0 {
		return unknown(reasonConvertWarned)
	}
	v.state = readinessReady
	return v
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

// readinessSentence is the whole of the copy. Three rules it must keep:
//
//   - "ready" claims only what was checked: node types and model files against the
//     cached schema and the local library. It does not promise the run succeeds.
//   - "needs" states COUNTS, and says "at least" whenever the graph was incomplete.
//   - "unknown" always names WHY, and never implies the workflow is fine.
func readinessSentence(v readinessView) string {
	switch v.state {
	case readinessReady:
		return "Ready — every node type and model file this workflow references is installed."
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
	if parts := readinessCountParts(v); len(parts) > 0 {
		lead := "Needs "
		if v.graphIncomplete {
			lead = "Needs at least "
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
		verb := " is "
		if v.badOptions != 1 {
			verb = " are "
		}
		out = append(out, readinessCount(v.badOptions, "saved option value", "saved option values")+
			verb+"no longer valid on your installed nodes.")
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
	case reasonColdCache:
		return "this app has not seen your ComfyUI's node list yet. Run any workflow once and it will be checked from then on."
	case reasonUnreadableSchema:
		return "the stored copy of your ComfyUI's node list could not be read. Running any workflow replaces it."
	case reasonConvertFailed:
		return "this workflow's graph could not be converted to API format."
	case reasonConvertWarned:
		return "this workflow's graph did not convert cleanly, so it was not checked. Generate will report the details."
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
