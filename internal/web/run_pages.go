package web

import (
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"strconv"
	"strings"

	"github.com/ZacxDev/civitai-manager/internal/comfy"
	"github.com/ZacxDev/civitai-manager/internal/store"
	g "maragu.dev/gomponents"
	h "maragu.dev/gomponents/html"
)

// runStatusContainerID is the STABLE container the run poller swaps into (never
// outerHTML-replacing the poller node itself — the repo's streaming invariant).
const runStatusContainerID = "run-status"

// runComfyStatusID is the STABLE container the reachability fragment loads into. It
// lazy-loads once on page load and is re-fetched by the Recheck button — its
// innerHTML is swapped, never the node itself (same streaming invariant).
const runComfyStatusID = "run-comfy-status"

// runGenerateSectionID is the id of the ONE "Generate" section, and the fragment
// the library list item's primary Run CTA deep-links to
// (/workflows/<id>#cm-generate). The scroll + the CTA pulse are both native CSS
// `:target` behaviour — see .cm-generate / .cm-generate-cta in app.css.
const runGenerateSectionID = "cm-generate"

// generateSection is the detail page's ONE place a workflow is executed.
//
// It replaced three sibling cards — "Open in ComfyUI", "Run", and "Run on CivitAI
// Cloud" — that each looked like an independent feature, so "how do I run this?"
// had three plausible answers stacked vertically. Now there is a single section
// with an explicit hierarchy: the PRIMARY CTA is "Generate" (submit to the local
// ComfyUI), "Open in ComfyUI" is the secondary action beside it, and the cloud run
// is a separated sub-block below.
//
// 🔴 "Open in ComfyUI" is a real <form method="post" target="_blank">, NOT an htmx
// button, and it is rendered HERE (outside the lazily-loaded #run-comfy-status
// fragment) so it exists from the first paint. The form submit opens the new tab
// synchronously from the click, which is what lets the handler 303 that tab into
// <comfy_url>/?cm_open=<path>. An htmx POST can only answer with markup — which is
// how this once shipped as "we saved it, now click this OTHER link". See
// openInComfyForm / workflow_open_comfy.go before touching it.
//
// STRUCTURAL INVARIANT: #run-params and #run-status stay SIBLINGS at the same
// level, exactly as before. The 1 s run poller swaps #run-status's innerHTML, so it
// structurally cannot clobber a half-typed prompt in the preset tabs that live in
// #run-params. Do not collapse or reparent them.
func generateSection(wf *store.Workflow, snap runSnapshot, csrf string, extraAllowed, dlEligible bool,
	mr maturityRange, presets presetTabView, comfyConfigured bool, helper comfyHelperView) g.Node {
	sectionAttrs := []g.Node{h.ID(runGenerateSectionID), h.Class("cm-generate")}
	if !extraAllowed {
		return card(append(sectionAttrs,
			sectionTitle("Generate"),
			alert("warning", "",
				g.Text("Local run is disabled when the server is bound to a non-loopback address.")),
		)...)
	}
	// The editor hand-off applies only to a UI-format graph with somewhere to reach:
	// an API graph does not load into the ComfyUI editor.
	editable := comfyConfigured && wf.Format == store.WorkflowFormatUI

	body := append(sectionAttrs,
		sectionTitle("Generate"),
		h.P(h.Class("text-xs text-slate-500 mb-3"),
			g.Text("Runs this workflow on your local ComfyUI ("+comfyDisplayURL(wf)+"). "+
				"UI-format graphs are converted to API format first; missing nodes or models are reported before submitting.")),
	)
	// Multi-mode template picker (empty container for an ordinary workflow). It sits
	// ABOVE Parameters because the choice determines which parameters exist.
	// The mode picker is PRE-SELECTED from the active preset's stored mode (rule 2
	// in run_presets.go): the panel below is reconciled against that same mode, so
	// what the page shows and what a run would convert never diverge. Once rendered
	// the picker is authoritative — changing it re-fetches the panel.
	body = append(body, runModesPanelSelected(wf, csrf, presets.ModesOOB))
	// Editable "Parameters" panel (prompts / KSampler settings / latent dimensions),
	// detected from the graph and applied as per-run overrides. Empty when the graph
	// exposes none of the curated inputs (e.g. an api-format graph with no widgets, or
	// a multi-mode template before a mode is picked). The container is STABLE so the
	// mode picker can swap its innerHTML.
	body = append(body, h.Div(h.ID(runParamsContainerID), runPresetPanel(wf, csrf, presets)))
	// THE ONE RUN ZONE — the count segment, the single primary control (inside the
	// stable, lazily-probed #run-comfy-status) and the secondary "Open in ComfyUI".
	// It sits BETWEEN #run-params and #run-status and is nested in NEITHER, so a
	// preset-tab swap and the 1 s run poller both leave it alone. See run_zone.go for
	// why this replaced three competing controls.
	//
	// WHERE it runs is ONE decision: the zone and the cloud block are the two panels
	// of a single destination control (runDestination), not two stacked apparatuses.
	body = append(body, runDestination(
		runZone(wf.ID, csrf, canQueueWorkflow(wf), editable, snap),
		cloudGenerateBlock(wf.ID),
	))
	// Run job status container (unchanged): poller drives running → terminal.
	//
	// 🔴 It stays OUTSIDE both destination panels. Inside the local one, switching to
	// Cloud would hide a local run that is still in flight — and the poller would go
	// on updating an invisible element, which is the shape of "my run vanished".
	body = append(body, h.Div(h.ID(runStatusContainerID), runStatusFragment(snap, wf.ID, csrf, dlEligible, mr)))
	// Helper install/remove stays a collapsed "advanced" disclosure and is never
	// surfaced in a per-click result (an inline "Remove helper" button beside the
	// success text got clicked by a user who did not know it disabled the feature).
	if editable {
		body = append(body, comfyHelperDisclosure(helper))
	}
	return card(body...)
}

// comfyStatusView is the resolved reachability state the fragment renders.
type comfyStatusView struct {
	configured bool   // s.comfy() != nil
	reachable  bool   // SystemStats succeeded
	version    string // ComfyUIVersion (untrusted — escaped)
	comfyURL   string // configured comfy_url (escaped)
	// canQueue is true for a UI-format workflow, i.e. one the batch endpoint
	// accepts. It decides which endpoint the ONE primary control posts to, and it
	// must agree with runZone's own canQueue — the count segment only exists when
	// this is true, so a disagreement renders a picker whose value the button then
	// discards. Both are now derived from canQueueWorkflow (run_queue_handlers.go),
	// which is also what the endpoint itself enforces;
	// TestCanQueueAgreesAcrossPickerHintButtonAndHandler pins the agreement.
	canQueue bool
}

// runComfyStatusFragment renders the reachability INDICATOR + the primary Generate
// control into #run-comfy-status. Reachable → a green ✓ icon + enabled Generate;
// unreachable → a red ✕ icon + disabled Generate (with a tooltip) + an icon-only
// Recheck; unconfigured → a neutral icon + a short note. Every untrusted string
// (version, comfy_url) goes through g.Text.
//
// The wordy green/red PILL became a small icon in PR C1: the sentence "No ComfyUI
// reachable at http://127.0.0.1:8188" is the widest element in the row and pushed
// the actual CTA off to the side. The words did not disappear — they moved into the
// icon's popover, its title=, and its aria-label, so nothing is colour-only.
func runComfyStatusFragment(wfID int64, csrf string, v comfyStatusView) g.Node {
	id := strconv.FormatInt(wfID, 10)
	if !v.configured {
		return g.Group([]g.Node{
			comfyStatusIcon(v),
			h.Span(h.Class("text-sm text-slate-400"),
				g.Text("Local ComfyUI is not configured. Set comfy_url to enable running workflows.")),
		})
	}
	if v.reachable {
		return g.Group([]g.Node{
			comfyStatusIcon(v),
			runButtonEnabled(id, csrf, v.canQueue),
		})
	}
	return g.Group([]g.Node{
		comfyStatusIcon(v),
		runButtonDisabled("ComfyUI is not reachable — start it (or fix comfy_url), then Recheck."),
		recheckButton(id),
	})
}

// comfyStatusLines returns the icon glyph, the state key (.cm-status-ico
// [data-state]) and the popover's headline + detail for a reachability state. It is
// one function so the icon, its title=, its aria-label and its popover can never
// disagree about what the connection is doing.
func comfyStatusLines(v comfyStatusView) (glyph, state, headline, detail string) {
	switch {
	case !v.configured:
		return "○", "off", "Local ComfyUI is not configured",
			"Set comfy_url to enable running workflows."
	case v.reachable:
		headline = "ComfyUI reachable"
		if v.version != "" {
			headline += " · v" + v.version
		}
		return "✓", "ok", headline, "Runs are submitted to your local ComfyUI."
	default:
		return "✕", "down", "No ComfyUI reachable at " + v.comfyURL,
			"Start ComfyUI (or fix comfy_url), then use Recheck."
	}
}

// comfyStatusIcon renders the compact reachability indicator: a coloured glyph plus
// a hover/click popover carrying the full state.
//
// It reuses the page's EXISTING popover mechanism rather than adding a second one —
// `.cm-updated` wrapper + `.cm-updated-pop` child is what the shared hover
// controller in modelPageScript delegates on — and carries tabindex=0 so a click or
// a Tab opens it through :focus-within. The glyph colours come from the `-text`
// half of the intent tokens (never the bare fill), and the state is ALSO in the
// aria-label, so it is never conveyed by colour alone.
func comfyStatusIcon(v comfyStatusView) g.Node {
	glyph, state, headline, detail := comfyStatusLines(v)
	return h.Span(
		h.Class("cm-updated"),
		g.Attr("tabindex", "0"),
		g.Attr("role", "img"),
		// No title= beside the popover — the native tooltip would render on top of
		// it (see updatedPopBody's "one hover affordance per element"). role=img
		// means the aria-label above IS the accessible name.
		g.Attr("aria-label", headline+". "+detail),
		h.Span(h.Class("cm-status-ico"), dataAttr("state", state),
			g.Attr("aria-hidden", "true"), g.Text(glyph)),
		h.Span(h.Class("cm-updated-pop"), g.Attr("role", "tooltip"),
			h.Div(h.Class("cm-updated-title"), g.Text(headline)),
			h.Div(h.Class("cm-updated-date"), g.Text(detail)),
		),
	)
}

// runButtonEnabled is THE live primary control — the only button on the page that
// starts a local run.
//
// 🔴 Two things changed here and both were bugs, not polish:
//
//   - hx-include was runModesInclude (#run-modes ONLY). This button sat directly
//     above the Parameters panel and SILENTLY DISCARDED everything typed into it,
//     while a second, smaller "Run with these parameters" submit inside the panel
//     did honour it. It now includes runZoneInclude — the parameters form, the mode
//     picker AND the count segment — so the prominent button runs what is on screen.
//   - the endpoint was /run, which does not read parameters at all. It is now the
//     batch endpoint for a UI-format workflow (count==1 is an ordinary single run —
//     see handleWorkflowRunQueue) and /run-with-params for an API-format one, which
//     the batch endpoint deliberately refuses.
//
// .cm-generate-cta is the hook the section's `:target` deep-link highlight pulses;
// .cm-run-go is what the count segment's CSS suffixes with " ×4".
func runButtonEnabled(id, csrf string, canQueue bool) g.Node {
	post := "/workflows/" + id + "/run-with-params"
	if canQueue {
		post = "/workflows/" + id + "/run/queue"
	}
	return civButton("filled", "md", []g.Node{
		h.Class("cm-generate-cta cm-run-go"),
		h.Type("button"),
		hx("post", post),
		hx("target", "#"+runStatusContainerID),
		hx("swap", "innerHTML"),
		hx("disabled-elt", "this"),
		hx("include", runZoneInclude),
		csrfInline(csrf),
	}, g.Text("Generate"))
}

// runButtonDisabled is the inert primary control shown when ComfyUI is unreachable;
// the tooltip explains why.
func runButtonDisabled(reason string) g.Node {
	return civButton("filled", "md", []g.Node{
		h.Class("cm-generate-cta"),
		h.Type("button"),
		h.Disabled(),
		g.Attr("title", reason),
	}, g.Text("Generate"))
}

// recheckButton re-fetches the reachability fragment into #run-comfy-status. It is
// an ICON button (↻): it only ever appears next to the "not reachable" icon, which
// already carries the explanation, so a word-wide button beside it was pure noise.
// The word "Recheck" stays in the title + aria-label for AT and for hover.
func recheckButton(id string) g.Node {
	return civButton("outline", "sm", []g.Node{
		h.Type("button"),
		g.Attr("title", "Recheck the ComfyUI connection"),
		g.Attr("aria-label", "Recheck the ComfyUI connection"),
		hx("get", "/workflows/"+id+"/run/comfy-status"),
		hx("target", "#"+runComfyStatusID),
		hx("swap", "innerHTML"),
	}, h.Span(g.Attr("aria-hidden", "true"), g.Text("↻")))
}

// comfyDisplayURL is a tiny indirection so the panel copy can name the server; the
// URL itself is not workflow-specific but reading it from config keeps the string
// truthful. (wf is unused today but keeps the signature stable for future per-wf
// targeting.)
func comfyDisplayURL(_ *store.Workflow) string { return "local ComfyUI" }

// runStatusBody is what a HANDLER answers with when it writes into #run-status: the
// fragment itself, PLUS the out-of-band clear of the readiness container computed
// from the SAME snapshot.
//
// 🔴 ONE RESPONSE UPDATES BOTH CONTAINERS, and that is the entire fix. #run-status
// and #cm-run-readiness are swapped by DIFFERENT requests — the run poller owns one,
// a lazy `hx-trigger="load"` owns the other — so a rule enforced in only one of them
// drifts by construction. That drift is exactly what shipped: the readiness line was
// fetched legitimately at first paint and then simply never revisited, so a run that
// failed afterwards stacked its panel flush underneath it.
//
// A server-side check alone could not have fixed this. By the time Generate is
// clicked the line is already on screen; nothing re-requests it. Something has to
// reach out and remove it, and riding it out-of-band on the response that puts the
// run there is the only way to keep the two in one place.
//
// runReadinessCleared is a no-op group when no run for this workflow is showing, so
// an idle response is byte-identical to before. generateSection deliberately does
// NOT use this — hx-swap-oob is only processed on an AJAX response, so on a full
// page load the marker would sit inert in the markup; the page-render path decides
// the same question by simply not emitting the lazy loader (see runZone).
func runStatusBody(snap runSnapshot, wfID int64, csrf string, dlEligible bool, mr maturityRange) g.Node {
	return g.Group([]g.Node{
		runStatusFragment(snap, wfID, csrf, dlEligible, mr),
		runReadinessCleared(snap, wfID),
	})
}

// runStatusFragment dispatches the run job's current state into #run-status: the
// running fragment (with poller + Stop) while in flight, else the terminal result.
// A run belonging to a DIFFERENT workflow is not shown here (the poller would
// otherwise attach this page to another workflow's run).
func runStatusFragment(snap runSnapshot, wfID int64, csrf string, dlEligible bool, mr maturityRange) g.Node {
	if snap.Started && snap.Running && snap.WorkflowID != wfID {
		return h.Div(h.Class("text-sm text-amber-400"),
			g.Text("A run is already in progress for another workflow. Try again when it finishes."))
	}
	// 🔴 THE SAME predicate the readiness line's suppression reads — shared, not
	// mirrored. "This container is idle" and "the readiness line may render" are one
	// decision; see runStatusHoldsARunFor in run_readiness.go.
	if !runStatusHoldsARunFor(snap, wfID) {
		return h.Div() // idle: nothing to show for this workflow yet
	}
	// Stopped is set synchronously by stopRun (before the run goroutine settles), so
	// keying the terminal off it makes Stop deterministic: the response — and any
	// in-flight poll — renders the poller-less "Run stopped" view at once, halting
	// the poll loop instead of re-arming it (the same guard the scan job uses).
	if snap.Stopped {
		return runStopped(snap, wfID, csrf)
	}
	if snap.Running {
		return runRunning(snap, wfID, csrf)
	}
	return runTerminal(snap, wfID, csrf, dlEligible, mr)
}

// dataRunSeq stamps the run's monotonic identity onto a run-status fragment root as
// data-run-seq. It is present on EVERY fragment that represents an actual run (running,
// stopped, terminal) so a caller can pin to a specific run regardless of which phase it
// observes. Omitted (empty node) for seq<=0 (idle / no run yet). This is the single
// DOM hook the ux-audit harness keys on to distinguish THIS run from a stale prior one.
func dataRunSeq(seq int64) g.Node {
	if seq <= 0 {
		return g.Group(nil)
	}
	return g.Attr("data-run-seq", strconv.FormatInt(seq, 10))
}

// runStopped is the terminal fragment after an explicit Stop (no poller).
func runStopped(snap runSnapshot, wfID int64, csrf string) g.Node {
	body := []g.Node{h.P(h.Class("text-sm text-amber-400"), g.Text("Run stopped."))}
	if line := batchSummaryNode(snap); line != nil {
		body = append(body, line)
	}
	if wfID > 0 {
		body = append(body, h.Div(h.Class("pt-1"), runAgainButton(wfID, csrf)))
	}
	return h.Div(dataRunSeq(snap.Seq), dataBatchProgress(snap), h.Class("space-y-3"), g.Group(body))
}

// dataBatchProgress stamps data-batch-index / data-batch-total on a run-status
// fragment root, beside data-run-seq. Same rationale as that attribute: the harness
// (and any test) can pin batch progress without scraping prose, and the prose is
// then free to change.
//
// Omitted entirely for a single run, so every existing fragment's markup is
// byte-identical to before.
func dataBatchProgress(snap runSnapshot) g.Node {
	if !snap.IsBatch() {
		return g.Group(nil)
	}
	return g.Group([]g.Node{
		g.Attr("data-batch-index", strconv.Itoa(snap.BatchIndex)),
		g.Attr("data-batch-total", strconv.Itoa(snap.BatchTotal)),
	})
}

// batchSummaryNode renders the one extra terminal line a MULTI-item batch adds
// ("8 of 8 complete.", "Batch halted at item 3 of 8 — 2 completed, 5 not started.").
// It is server-authored text composed under runMu while the counters were still
// consistent, and nil for a single run.
func batchSummaryNode(snap runSnapshot) g.Node {
	if snap.BatchSummary == "" {
		return nil
	}
	return h.P(h.Class("text-sm text-slate-300"), g.Text(snap.BatchSummary))
}

// runPoller is the one-shot re-arming poll element driving the running view to its
// terminal state. It never targets itself: it fires once and swaps the innerHTML of
// the STABLE #run-status container. Each running response carries a fresh poller;
// the terminal fragment carries none, so polling stops.
func runPoller(wfID int64) g.Node {
	return h.Div(
		h.ID("run-poll"),
		hx("get", fmt.Sprintf("/workflows/%d/run/status", wfID)),
		hx("trigger", "load delay:1s"),
		hx("target", "#"+runStatusContainerID),
		hx("swap", "innerHTML"),
	)
}

// runRunning is the in-flight fragment: a spinner + phase line, an optional queue
// position, a Stop button, and the re-arming poller.
func runRunning(snap runSnapshot, wfID int64, csrf string) g.Node {
	line := snap.Message
	if snap.Phase == runPhaseQueued {
		line = fmt.Sprintf("Queued — %d ahead", snap.QueuePos)
	}
	stopLabel := "Stop"
	if snap.IsBatch() {
		// The batch's progress leads the phase line: "which of my eight is this?" is
		// the first thing the user asks, and it is the only thing the phase line
		// cannot answer.
		line = fmt.Sprintf("Item %d of %d · %s", snap.BatchIndex, snap.BatchTotal, line)
		// Naming what Stop actually does: it cancels the whole remaining batch, not
		// just the item on screen.
		stopLabel = "Stop batch"
	}
	stop := civButton("filled", "sm", []g.Node{
		h.Type("button"),
		hx("post", "/workflows/run/stop"),
		hx("target", "#"+runStatusContainerID),
		hx("swap", "innerHTML"),
		runStopVals(csrf, wfID),
	}, g.Text(stopLabel))

	return h.Div(
		dataRunSeq(snap.Seq),
		dataBatchProgress(snap),
		h.Class("space-y-3"),
		h.Div(h.Class("flex items-center gap-2 text-sm text-slate-300"),
			spinnerGlyph(),
			g.Text(line),
		),
		h.Div(h.Class("flex"), stop),
		runPoller(wfID),
	)
}

// runStopVals attaches the CSRF token AND the workflow id to the Stop button's
// self-issued POST (so the terminal response can offer a "Run again" button).
func runStopVals(csrf string, wfID int64) g.Node {
	return hx("vals", fmt.Sprintf(`{"csrf_token":"%s","workflow_id":"%d"}`, csrf, wfID))
}

// runTerminal renders the settled run: a result gallery on success, or the failure
// report (message + preflight/warnings detail) — plus a "Run again" button.
func runTerminal(snap runSnapshot, wfID int64, csrf string, dlEligible bool, mr maturityRange) g.Node {
	var body []g.Node
	// The batch line goes ABOVE the (byte-for-byte unchanged) per-run report. On a
	// halt the failing item's runFailure panel keeps every missing-model /
	// node-attribution / option-fix affordance working — the batch wrapper must not
	// swallow them, because those are the actions that let the user fix it and
	// re-queue.
	if line := batchSummaryNode(snap); line != nil {
		body = append(body, line)
	}
	switch snap.Phase {
	case runPhaseDone:
		body = append(body, h.P(h.Class("text-sm text-emerald-400"), g.Text(snap.Message)))
		if gal := runGallery(snap); gal != nil {
			body = append(body, gal)
		}
	default: // failed / stopped
		body = append(body, runFailure(snap, wfID, csrf, dlEligible, mr))
	}
	if wfID > 0 {
		actions := []g.Node{runAgainButton(wfID, csrf)}
		// A FAILED run is exactly when the user needs the graph itself: preflight
		// blocked it, the conversion warned, models are missing, or saved option values
		// no longer resolve. Put the existing "Open in ComfyUI" flow right next to "Run
		// again" so fixing it is one click — and keep it OFF the success path, where it
		// would be clutter. It is REUSED, never re-implemented (openInComfyForm posts to
		// /workflows/{id}/open-in-comfyui), and it applies only to an editable UI graph.
		if snap.Phase != runPhaseDone && !snap.Stopped && snap.UIFormat {
			actions = append(actions, openInComfyForm(strconv.FormatInt(wfID, 10), csrf, "subtle", "sm"))
		}
		body = append(body, h.Div(h.Class("pt-1 flex flex-wrap items-center gap-2"), g.Group(actions)))
	}
	return h.Div(dataRunSeq(snap.Seq), dataBatchProgress(snap), h.Class("space-y-3"), g.Group(body))
}

// runAgainButton re-triggers a run into the same stable container.
//
// It posts to /run-with-params and hx-includes runPresetInclude for the same reason
// runButtonEnabled does: /run reads no parameters, so "Run again" used to re-run the
// workflow's SAVED values and quietly throw away the edits still sitting in the panel
// above — the one moment a user is most likely to have just changed something.
// It stays a SINGLE run (runPresetInclude, not runZoneInclude): "again" after a
// halted 8-item batch must not silently re-queue eight more.
func runAgainButton(wfID int64, csrf string) g.Node {
	return civButton("outline", "sm", []g.Node{
		h.Type("button"),
		hx("post", fmt.Sprintf("/workflows/%d/run-with-params", wfID)),
		hx("target", "#"+runStatusContainerID),
		hx("swap", "innerHTML"),
		hx("include", runPresetInclude),
		csrfInline(csrf),
	}, g.Text("Run again"))
}

// runGallery renders the result images as a responsive grid of proxied <img>s.
// Each src points at the /workflows/run/view proxy (so the browser never reaches
// ComfyUI directly); filenames are URL-encoded in the query and escaped in alt.
func runGallery(snap runSnapshot) g.Node {
	if len(snap.Images) == 0 {
		return nil
	}
	var imgs []g.Node
	for _, ref := range snap.Images {
		src := runViewURL(snap.PromptID, ref)
		imgs = append(imgs, h.Img(
			h.Src(src),
			h.Alt(ref.Filename),
			g.Attr("loading", "lazy"),
			h.Class("w-full h-auto rounded border border-slate-800 bg-slate-900"),
		))
	}
	return h.Div(h.Class("grid grid-cols-2 sm:grid-cols-3 gap-3"), g.Group(imgs))
}

// runViewURL builds the run-image proxy URL for one output image ref.
func runViewURL(promptID string, ref comfy.ImageRef) string {
	q := url.Values{}
	q.Set("prompt", promptID)
	q.Set("filename", ref.Filename)
	q.Set("subfolder", ref.Subfolder)
	q.Set("type", ref.Type)
	return "/workflows/run/view?" + q.Encode()
}

// runFailure renders the failure report.
//
// INFORMATION HIERARCHY (this is the whole point of the ordering below, and four
// independent persona walkthroughs flagged the previous one at both viewports):
// a failed preflight is a RECOVERABLE state, so the report leads with a
// plain-language summary of what is wrong, then ONE primary recovery action for the
// whole failure, then the per-item secondary paths — and the raw engine text
// (the technical preflight sentence, the conversion warnings) is SUBORDINATED into
// a <details>. Previously the first thing rendered was the technical sentence
// ("Preflight failed — this workflow references nodes or models that are not
// installed"), immediately followed by raw filenames and two identically-labelled
// "Fix" buttons with no indication of which to press.
//
// The missing-model FILENAMES stay visible (they are the concrete thing the user has
// to recognise, they name the model to go and find, and hiding the per-item rows in
// a collapsed <details> would also break each row's native <dialog>.showModal() —
// a modal inside a closed <details> has no rendered ancestor box).
func runFailure(snap runSnapshot, wfID int64, csrf string, dlEligible bool, mr maturityRange) g.Node {
	// An empty-conversion abort is not a failure in the run sense — nothing was
	// submitted. Render it as its own actionable report (the message is the
	// escaped, actionable guidance from *comfy.ConversionEmptyError).
	if snap.Aborted {
		return alert("warning", "Run aborted — nothing to run", g.Text(snap.Message))
	}

	// Triage the missing set ONCE: the lead, the headline and the CTA must agree about
	// what this server can actually deliver (see batchInstallPlan).
	batch := planBatchInstall(snap.MissingModels, dlEligible, snap.ComfyRemote)
	nm, nn := failureMissingCounts(snap, batch)

	var detail []g.Node
	// Lead: what went wrong, in the user's terms. The raw engine message moves into
	// the technical <details> at the bottom.
	if lead := failureLead(snap, nm, nn, batch.Available); lead != "" {
		detail = append(detail, h.P(h.Class("text-sm text-slate-300"), g.Text(lead)))
	}
	// The honesty caveat goes ABOVE the panels and above the install-all CTA on
	// purpose: the CTA is a promise ("install these and it should run") and the
	// reader has to know it is bounded before acting on it.
	if notice := lowerBoundNotice(snap); notice != nil {
		detail = append(detail, notice)
	}

	if snap.Preflight != nil {
		// THE primary recovery action for the whole failure: install every missing
		// model file, then re-run. It leads the actions because "which of these
		// buttons do I press?" was the top reported confusion.
		if len(snap.MissingModels) > 0 {
			detail = append(detail, installAllMissingAction(batch,
				len(batch.Installable)+batch.Overflow, wfID, csrf))
		}
		if len(snap.Preflight.MissingNodes) > 0 {
			// Attributed, actionable panel: which pack provides each missing class, a
			// gated Install where ComfyUI-Manager can do it, and always a manual
			// command. The attribution was computed once at settle (never per poll).
			detail = append(detail, missingNodesPanel(snap.NodeAttr, snap.Preflight.MissingNodes,
				wfID, csrf, snap.NodeAttr.ComfyRoot))
		}
		if len(snap.Preflight.MissingModels) > 0 {
			// The actionable panel (resolve-to-CivitAI + run-with-substitute) needs the
			// enriched analysis; fall back to a plain bullet list if it is absent (e.g.
			// an older snapshot with only filenames).
			if len(snap.MissingModels) > 0 {
				detail = append(detail, missingModelsPanel(snap.MissingModels, snap.MissingResolved, snap.LibMeta, wfID, csrf, dlEligible, mr))
			} else {
				detail = append(detail, missingList("Missing model files", snap.Preflight.MissingModels))
			}
		}
		// Incompatible combo (enum) options: a dedicated pick-a-valid-choice-and-run
		// form. Independent of the missing nodes/models sections above.
		if len(snap.Preflight.BadOptions) > 0 {
			detail = append(detail, incompatibleOptionsSection(snap.Preflight.BadOptions, wfID, csrf, dlEligible))
		}
	}
	if tech := failureTechnicalDetail(snap, nm, nn); tech != nil {
		detail = append(detail, tech)
	}
	// The glyph makes the failure state distinguishable by shape, not by tint alone.
	return alertIcon("error", "⚠", failureTitle(snap, nm, nn), detail...)
}

// lowerBoundNoticeText is the honesty caveat rendered when the UI→API conversion
// had to REMOVE nodes from the graph.
//
// 🔴 It must never read as "these are all the files you need", because it is
// provably not: a removed node took its own model references out of the document
// with it, and nothing downstream can enumerate what a node that is not installed
// would have loaded. So the wording names the bound explicitly ("a lower bound,
// not a complete list") rather than hedging with "may". A second round WILL be
// needed after the node packs are installed, and saying so up front is cheaper
// than the user discovering it.
//
// It deliberately states NO count. The number of dropped classes is recoverable
// from Preflight.MissingNodes today, but only because the api-format path
// contributes none of them — quoting a number here would encode that coupling
// into user-facing copy, and it would be wrong the moment both sources
// contribute.
//
// ⚠ SHORTENED, NOT WEAKENED (it was 246 characters). Every load-bearing element is
// still here and was checked off individually: it names the bound in the same words
// ("a lower bound, not a complete list"), it states NO count, it gives the CAUSE
// (the dropped nodes took their references with them) so the reader can tell this
// is not a hedge, and it names the SECOND ROUND. What went was the restatement of
// the mechanism in a second clause. Do not shorten it further by dropping the cause
// or the next step — a caveat the reader cannot act on is one they learn to skip.
const lowerBoundNoticeText = "This is a lower bound, not a complete list: the missing custom nodes below " +
	"took their own model files out of the graph with them. Install the nodes, then run again to see the rest."

// lowerBoundNotice renders lowerBoundNoticeText, or nil when the graph is whole
// (an api-format workflow, or a UI conversion that removed nothing — then the
// lists really are complete and the caveat would be noise).
func lowerBoundNotice(snap runSnapshot) g.Node {
	if !snap.GraphIncomplete {
		return nil
	}
	return h.P(h.Class("text-xs text-amber-400"), g.Text(lowerBoundNoticeText))
}

// failureTitle is the failure alert's headline. It keeps the "Run failed" stem
// (that is the state, and it is what every failure surface is identified by) and
// appends the plain-language count of what is actually wrong, so the headline alone
// answers "what do I have to do?" for the overwhelmingly common case.
func failureTitle(snap runSnapshot, nm, nn int) string {
	switch {
	case nm > 0 && nn > 0:
		return fmt.Sprintf("Run failed — %d model file%s and %d custom node%s are missing",
			nm, plural(nm), nn, plural(nn))
	case nm > 0:
		return fmt.Sprintf("Run failed — %d model file%s missing", nm, plural(nm))
	case nn > 0:
		return fmt.Sprintf("Run failed — %d custom node%s missing", nn, plural(nn))
	case snap.Preflight != nil && len(snap.Preflight.BadOptions) > 0:
		return "Run failed — some saved settings no longer exist"
	default:
		return "Run failed"
	}
}

// failureLead is the plain-language summary shown FIRST: what is wrong and what
// fixes it, with no engine vocabulary ("preflight", "references", "graph"). It
// returns "" when there is nothing to add beyond the raw message — which then
// renders as the lead instead (see failureTechnicalDetail).
//
// canInstall is whether the batch-install CTA can actually deliver on this server
// (batchInstallPlan.Available = comfy_model_path + a local ComfyUI + a non-empty set).
// It is LOAD-BEARING, not cosmetic: "Install them and it should run" is a promise, and
// on a server that cannot install anything it is false and sends the reader looking for
// a button that is greyed out. The un-installable wording names the real next step.
//
// SCOPE, stated precisely because an earlier version of this comment overstated it: a
// reference whose model type could not be inferred does NOT suppress the promise. Since
// those files are attempted (the HuggingFace fallback often places them — see
// batchInstallPlan) they are only FLAGGED, and if they cannot be matched the handler
// declines the whole batch and names them. Suppressing the lead for a file that will
// probably install would be its own false signal; the honest decline is what covers the
// case where it does not.
func failureLead(snap runSnapshot, nm, nn int, canInstall bool) string {
	switch {
	case nm > 0 && !canInstall:
		// Deliberately SILENT. The old copy here said "this copy of civitai-manager
		// cannot fetch the model files for you", which is no longer true — the blocked
		// state now leads with a setup step that makes it able to. Repeating a dead end
		// directly above the control that offers the way out is worse than saying
		// nothing. The other blocked state (a remote comfy_url) renders its own reason
		// in blockedInstallAction, one line below where this lead would have sat — so
		// putting one here too would state the same thing twice.
		//
		// 🔴 The guard is `nm > 0 && !canInstall`, NOT `!canInstall`. canInstall is
		// about MODEL files only (comfy_model_path + a local ComfyUI); a nodes-only
		// failure is installed through ComfyUI-Manager and needs none of that, so
		// keying the whole lead off canInstall silently blanked a state that was never
		// blocked. Caught by TestRunFailureNodeAndOptionCopy.
		return ""
	case nm > 0 || nn > 0:
		// 🔴 The COUNTS are the headline's job, and repeating them here was pure
		// restatement — measured at 194 characters directly under a headline that had
		// just said the same thing. The lead now adds only what the headline cannot:
		// that this is a setup gap rather than a broken workflow, plus the promise the
		// primary action is about to deliver.
		return "Nothing is broken — these are just not installed in ComfyUI yet. Add them and it should run."
	case snap.Preflight != nil && len(snap.Preflight.BadOptions) > 0:
		return "Some settings saved with this workflow no longer exist on your installed nodes. " +
			"Pick a valid choice for each one below, then run."
	default:
		// No structured detail to summarise: the raw message IS the summary.
		return snap.Message
	}
}

// failureTechnicalDetail is the SUBORDINATED engine detail: the exact preflight
// sentence and any conversion warnings, behind the app's progressive-disclosure
// idiom (a native <details>, as used for the collapsed substitute tail and the
// model description). It returns nil when the raw message was already promoted to
// the lead, so nothing is ever shown twice.
// nm/nn are the same counts the headline used; only whether they are NON-ZERO matters
// here (it decides whether the raw message was already promoted to the lead).
func failureTechnicalDetail(snap runSnapshot, nm, nn int) g.Node {
	hasSummary := nm > 0 || nn > 0 || (snap.Preflight != nil && len(snap.Preflight.BadOptions) > 0)
	var body []g.Node
	if hasSummary && strings.TrimSpace(snap.Message) != "" {
		body = append(body, h.P(h.Class("text-xs text-slate-400 break-all"), g.Text(snap.Message)))
	}
	// The custom-node methodology lives here rather than above the recovery actions —
	// it is provenance, not instruction. See nodePackProvenanceNotes.
	if snap.Preflight != nil && len(snap.Preflight.MissingNodes) > 0 {
		body = append(body, nodePackProvenanceNotes(snap.NodeAttr)...)
	}
	if len(snap.Warnings) > 0 {
		body = append(body, missingList("Conversion warnings", snap.Warnings))
	}
	if len(body) == 0 {
		return nil
	}
	return h.Details(h.Class("mt-3"),
		h.Summary(h.Class("cursor-pointer text-xs text-slate-400"), g.Text("Technical details")),
		h.Div(h.Class("mt-1"), g.Group(body)),
	)
}

// failureMissingCounts is the (missing models, missing nodes) pair the headline and lead
// are built from.
//
// The model count comes from the batch triage's DISTINCT set whenever there is one, so
// the headline and the CTA can never disagree: two references sharing a basename and a
// destination are ONE install, and counting raw preflight strings rendered
// "Run failed — 2 model files missing" directly above "Install 1 missing model file and
// run". The CTA is the honest number, so the headline follows it.
//
// It falls back to the PREFLIGHT count when the enriched per-model analysis is absent
// (an older snapshot carrying only filenames), where no triage is possible.
func failureMissingCounts(snap runSnapshot, batch batchInstallPlan) (models, nodes int) {
	if snap.Preflight == nil {
		return 0, 0
	}
	models = len(snap.Preflight.MissingModels)
	if n := len(batch.Installable) + batch.Overflow; n > 0 {
		models = n
	}
	return models, len(snap.Preflight.MissingNodes)
}

// itThem keeps the generated copy grammatical for n == 1. Its sibling `isAre` is the
// shared one in model_pages.go — do NOT add a second copy here.
func itThem(n int) string {
	if n == 1 {
		return "it"
	}
	return "them"
}

// incompatibleOptionsSection renders the "Incompatible options" section: a single
// <form> (hx-post to /run-with-options, CSRF in a hidden input) with one group per
// BadOption — the node class + input name + the current invalid value + a <select>
// of the input's valid choices — and ONE "Run with selected options" submit that
// applies every pick and runs. It lives inside the terminal (poller-free) fragment,
// so it is never swapped away mid-interaction. Every untrusted string (class, input,
// current value, choice) is escaped via g.Text / attribute escaping.
func incompatibleOptionsSection(bad []comfy.BadOption, wfID int64, csrf string, dlEligible bool) g.Node {
	groups := make([]g.Node, 0, len(bad))
	for i, bo := range bad {
		groups = append(groups, badOptionGroup(i, bo, wfID, csrf, dlEligible))
	}
	return h.Form(
		hx("post", "/workflows/"+strconv.FormatInt(wfID, 10)+"/run-with-options"),
		hx("target", "#"+runStatusContainerID),
		hx("swap", "innerHTML"),
		hx("disabled-elt", "find button[type='submit']"),
		hx("include", runModesInclude),
		h.Class("mt-3 space-y-3"),
		h.Input(h.Type("hidden"), h.Name("csrf_token"), h.Value(csrf)),
		h.Div(h.Class("text-xs font-semibold text-slate-200"), g.Text("Incompatible options")),
		h.P(h.Class("text-xs text-slate-400"),
			g.Text("These saved values are no longer valid choices on your installed nodes. "+
				"Pick a valid option for each, then run.")),
		g.Group(groups),
		h.Div(h.Class("pt-1"),
			civButton("filled", "sm", []g.Node{h.Type("submit")}, g.Text("Run with selected options"))),
	)
}

// badOptionGroup renders one incompatible-option group: the (escaped) node class +
// input name + current value, the parallel hidden opt_input/opt_old fields, and the
// opt_new <select>.
//
// Two shapes:
//   - A model-FILE bad option (Current has a model extension, e.g. a detector
//     "bbox/face_yolov9c.pt") gets an "Install <basename>" action ALONGSIDE the
//     dropdown: install downloads the EXACT referenced file so the original saved
//     value becomes valid. Its dropdown is an OPTIONAL substitute (never required —
//     Install is the primary fix), so clicking Install is never blocked by a forced
//     pick on this group.
//   - An inert enum drift (no model extension, e.g. a wildcard-picker label) is
//     pick-only: a single-choice group pre-selects its only valid option; a
//     multi-choice group leads with a "Choose…" placeholder and is required.
func badOptionGroup(idx int, bo comfy.BadOption, wfID int64, csrf string, dlEligible bool) g.Node {
	isModelFile := comfy.IsModelFileValue(bo.Current)
	single := len(bo.Choices) == 1
	opts := make([]selectOption, 0, len(bo.Choices)+1)
	selected := ""
	required := false
	switch {
	case isModelFile:
		// The exact file is installable; the dropdown is only an optional substitute.
		opts = append(opts, selectOption{Value: "", Label: "Or substitute an installed file…"})
	case single:
		selected = bo.Choices[0]
	default:
		opts = append(opts, selectOption{Value: "", Label: "Choose a valid option…"})
		required = true
	}
	for _, c := range bo.Choices {
		opts = append(opts, selectOption{Value: c, Label: c})
	}
	children := []g.Node{
		h.Class("rounded border border-slate-800 p-2 space-y-1"),
		h.Div(h.Class("text-xs text-slate-300"),
			h.Span(h.Class("font-semibold"), g.Text(bo.ClassType)),
			g.Text(" · "),
			h.Span(h.Class("font-mono"), g.Text(bo.InputName)),
		),
		h.Div(h.Class("text-xs text-slate-400"),
			g.Text("Current: "),
			h.Span(h.Class("font-mono break-all text-amber-400"), g.Text(bo.Current)),
		),
		h.Input(h.Type("hidden"), h.Name("opt_input"), h.Value(bo.InputName)),
		h.Input(h.Type("hidden"), h.Name("opt_old"), h.Value(bo.Current)),
		optionSelect("opt-new-"+strconv.Itoa(idx), opts, selected, required),
	}
	if isModelFile {
		children = append(children, badOptionInstallAction(bo, wfID, csrf, dlEligible))
	}
	return h.Div(children...)
}

// badOptionInstallBlockedText is the reason under a blocked bad-option "Install <file>".
//
// 🔴 Unlike cardInstallBlockedText it must NOT point at the setup step, because an
// incompatible-options failure is independent of missing models: a run can fail with
// BadOptions and zero MissingModels, in which case runFailure renders no batch action
// and no setup CTA at all, and "use the setup step above" would name a control that
// is not on the page. So this says only what is true in every case it can render in,
// and leans on the search link beside it, which always works.
//
// Honest residue: this surface still has no in-app way to supply the models folder.
// Giving it one means a second id="comfy-setup" on the page whenever both sections
// render, so it needs a shared single-instance setup control rather than a second
// copy — deliberately not attempted here.
const badOptionInstallBlockedText = "civitai-manager is not set up to install model files here, so fetch this one yourself."

// badOptionInstallAction renders the "Install <basename>" control for a model-file
// bad option. When installing is available (comfyDownloadEligible AND the value routes
// to a known ComfyUI subdir) it POSTs to /install-option-and-run, hx-including the
// enclosing section form so the OTHER groups' picks (e.g. the wildcard) ride along and
// the whole section resolves in ONE action. When NOT installable here it renders a
// DISABLED Install + a reason + a "Search CivitAI ↗" link (never hidden — the user is
// told the file is fetchable, just not automatically). Every untrusted string (the
// basename, the current value) is escaped via g.Text / json.Marshal + attribute-escaping.
func badOptionInstallAction(bo comfy.BadOption, wfID int64, csrf string, dlEligible bool) g.Node {
	base := path.Base(strings.ReplaceAll(bo.Current, "\\", "/"))
	civitaiType, _, routable := comfy.InferBadOptionInstall(bo.ClassType, bo.InputName, bo.Current)
	label := "Install " + base
	if !(dlEligible && routable) {
		return h.Div(h.Class("mt-1 space-y-1"),
			h.Div(h.Class("flex flex-wrap items-center gap-2"),
				civButton("filled", "sm", []g.Node{
					h.Type("button"), h.Disabled(),
				}, g.Text(label)),
				resolveFallbackLink(comfy.CleanModelQuery(bo.Current)),
			),
			h.P(h.Class("text-xs text-slate-500"), g.Text(badOptionInstallBlockedText)),
		)
	}
	// hx-include pulls the section form's csrf_token + every opt_input/opt_old/opt_new
	// so the picks for the OTHER groups are applied together with this install. The
	// install target itself rides in hx-vals (json.Marshal escapes the untrusted value).
	b, _ := json.Marshal(map[string]string{
		"install_filename": bo.Current,
		"install_type":     civitaiType,
	})
	return h.Div(h.Class("mt-1"),
		civButton("filled", "sm", []g.Node{
			h.Type("button"),
			hx("post", "/workflows/"+strconv.FormatInt(wfID, 10)+"/install-option-and-run"),
			hx("target", "#"+runStatusContainerID),
			hx("swap", "innerHTML"),
			hx("include", "closest form, "+runModesInclude),
			hx("disabled-elt", "this"),
			hx("vals", string(b)),
		}, g.Text(label)),
	)
}

// optionSelect renders the opt_new <select> for one incompatible-option group,
// styled with the civitai text-input control role (theme-aware in both data-theme
// paths). Option values are the real choice strings (attribute-escaped); required is
// set for multi-choice groups so the user must make a pick.
func optionSelect(id string, opts []selectOption, selected string, required bool) g.Node {
	optNodes := make([]g.Node, 0, len(opts))
	for _, o := range opts {
		attrs := []g.Node{h.Value(o.Value)}
		if o.Value == selected {
			attrs = append(attrs, g.Attr("selected"))
		}
		attrs = append(attrs, g.Text(o.Label))
		optNodes = append(optNodes, h.Option(attrs...))
	}
	selAttrs := []g.Node{dataFlag("civitai-ui-control"), h.ID(id), h.Name("opt_new")}
	if required {
		selAttrs = append(selAttrs, g.Attr("required"))
	}
	selAttrs = append(selAttrs, optNodes...)
	return h.Div(dataAttr("civitai-ui", "text-input"), h.Select(selAttrs...))
}

// missingList renders a titled bullet list of untrusted strings (node names, model
// filenames, warnings), each escaped via g.Text.
func missingList(title string, items []string) g.Node {
	lis := make([]g.Node, 0, len(items))
	for _, it := range items {
		// break-all: these are untrusted node names / model filenames / warnings with
		// no guaranteed break opportunity.
		lis = append(lis, h.Li(h.Class("font-mono text-xs text-slate-300 break-all"), g.Text(it)))
	}
	return h.Div(h.Class("mt-2"),
		h.Div(h.Class("text-xs font-semibold text-slate-200"), g.Text(title)),
		h.Ul(h.Class("list-disc pl-5 space-y-0.5 mt-1"), g.Group(lis)),
	)
}
