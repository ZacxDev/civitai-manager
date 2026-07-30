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

// runPanel is the detail-page "Run" section. The Run control is gated behind a
// ComfyUI reachability check: a lazy-loaded (#run-comfy-status) fragment pings the
// server and renders a green/red pill plus an enabled/disabled Run button + a
// Recheck. The actual run job drives the separate stable #run-status container
// (unchanged). When bound off-loopback the run endpoints are gated → a note.
func runPanel(wf *store.Workflow, snap runSnapshot, csrf string, extraAllowed, dlEligible bool, mode string, presets presetTabView) g.Node {
	id := strconv.FormatInt(wf.ID, 10)
	if !extraAllowed {
		return card(
			sectionTitle("Run"),
			alert("warning", "",
				g.Text("Local run is disabled when the server is bound to a non-loopback address.")),
		)
	}
	body := []g.Node{
		sectionTitle("Run"),
		h.P(h.Class("text-xs text-slate-500 mb-3"),
			g.Text("Submits this workflow to your local ComfyUI ("+comfyDisplayURL(wf)+"). "+
				"UI-format graphs are converted to API format first; missing nodes or models are reported before submitting.")),
		// Reachability region: lazy-loads the pill + (enabled/disabled) Run button.
		h.Div(h.ID(runComfyStatusID),
			hx("get", "/workflows/"+id+"/run/comfy-status"),
			hx("trigger", "load"),
			hx("swap", "innerHTML"),
			h.Span(h.Class("text-sm text-slate-400"), g.Text("Checking ComfyUI…")),
		),
	}
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
	// Run job status container (unchanged): poller drives running → terminal.
	body = append(body, h.Div(h.ID(runStatusContainerID), runStatusFragment(snap, wf.ID, csrf, dlEligible, mode)))
	return card(body...)
}

// comfyStatusView is the resolved reachability state the fragment renders.
type comfyStatusView struct {
	configured bool   // s.comfy() != nil
	reachable  bool   // SystemStats succeeded
	version    string // ComfyUIVersion (untrusted — escaped)
	comfyURL   string // configured comfy_url (escaped)
}

// runComfyStatusFragment renders the reachability pill + Run/Recheck controls into
// #run-comfy-status. Reachable → green pill + enabled Run; unreachable → red pill +
// disabled Run (with a tooltip) + Recheck; unconfigured → a neutral note. Every
// untrusted string (version, comfy_url) goes through g.Text via badge/text.
func runComfyStatusFragment(wfID int64, csrf string, v comfyStatusView) g.Node {
	id := strconv.FormatInt(wfID, 10)
	if !v.configured {
		return h.Div(h.Class("text-sm text-slate-400"),
			g.Text("Local ComfyUI is not configured. Set comfy_url to enable running workflows."))
	}
	if v.reachable {
		label := "ComfyUI reachable"
		if v.version != "" {
			label += " · v" + v.version
		}
		return h.Div(h.Class("flex flex-wrap items-center gap-3"),
			badge(label, "green"),
			runButtonEnabled(id, csrf),
			recheckButton(id),
		)
	}
	return h.Div(h.Class("flex flex-wrap items-center gap-3"),
		badge("No ComfyUI reachable at "+v.comfyURL, "red"),
		runButtonDisabled("ComfyUI is not reachable — start it (or fix comfy_url), then Recheck."),
		recheckButton(id),
	)
}

// runButtonEnabled is the live Run control: posts to /run and swaps #run-status.
func runButtonEnabled(id, csrf string) g.Node {
	return civButton("filled", "md", []g.Node{
		h.Type("button"),
		hx("post", "/workflows/"+id+"/run"),
		hx("target", "#"+runStatusContainerID),
		hx("swap", "innerHTML"),
		hx("disabled-elt", "this"),
		hx("include", runModesInclude),
		csrfInline(csrf),
	}, g.Text("Run on ComfyUI"))
}

// runButtonDisabled is the inert Run control shown when ComfyUI is unreachable; the
// tooltip explains why.
func runButtonDisabled(reason string) g.Node {
	return civButton("filled", "md", []g.Node{
		h.Type("button"),
		h.Disabled(),
		g.Attr("title", reason),
	}, g.Text("Run on ComfyUI"))
}

// recheckButton re-fetches the reachability fragment into #run-comfy-status.
func recheckButton(id string) g.Node {
	return civButton("outline", "sm", []g.Node{
		h.Type("button"),
		hx("get", "/workflows/"+id+"/run/comfy-status"),
		hx("target", "#"+runComfyStatusID),
		hx("swap", "innerHTML"),
	}, g.Text("Recheck"))
}

// comfyDisplayURL is a tiny indirection so the panel copy can name the server; the
// URL itself is not workflow-specific but reading it from config keeps the string
// truthful. (wf is unused today but keeps the signature stable for future per-wf
// targeting.)
func comfyDisplayURL(_ *store.Workflow) string { return "local ComfyUI" }

// runStatusFragment dispatches the run job's current state into #run-status: the
// running fragment (with poller + Stop) while in flight, else the terminal result.
// A run belonging to a DIFFERENT workflow is not shown here (the poller would
// otherwise attach this page to another workflow's run).
func runStatusFragment(snap runSnapshot, wfID int64, csrf string, dlEligible bool, mode string) g.Node {
	if snap.Started && snap.Running && snap.WorkflowID != wfID {
		return h.Div(h.Class("text-sm text-amber-400"),
			g.Text("A run is already in progress for another workflow. Try again when it finishes."))
	}
	if !snap.Started || snap.WorkflowID != wfID {
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
	return runTerminal(snap, wfID, csrf, dlEligible, mode)
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
	if wfID > 0 {
		body = append(body, h.Div(h.Class("pt-1"), runAgainButton(wfID, csrf)))
	}
	return h.Div(dataRunSeq(snap.Seq), h.Class("space-y-3"), g.Group(body))
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
	stop := civButton("filled", "sm", []g.Node{
		h.Type("button"),
		hx("post", "/workflows/run/stop"),
		hx("target", "#"+runStatusContainerID),
		hx("swap", "innerHTML"),
		runStopVals(csrf, wfID),
	}, g.Text("Stop"))

	return h.Div(
		dataRunSeq(snap.Seq),
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
func runTerminal(snap runSnapshot, wfID int64, csrf string, dlEligible bool, mode string) g.Node {
	var body []g.Node
	switch snap.Phase {
	case runPhaseDone:
		body = append(body, h.P(h.Class("text-sm text-emerald-400"), g.Text(snap.Message)))
		if gal := runGallery(snap); gal != nil {
			body = append(body, gal)
		}
	default: // failed / stopped
		body = append(body, runFailure(snap, wfID, csrf, dlEligible, mode))
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
	return h.Div(dataRunSeq(snap.Seq), h.Class("space-y-3"), g.Group(body))
}

// runAgainButton re-triggers a run into the same stable container.
func runAgainButton(wfID int64, csrf string) g.Node {
	return civButton("outline", "sm", []g.Node{
		h.Type("button"),
		hx("post", fmt.Sprintf("/workflows/%d/run", wfID)),
		hx("target", "#"+runStatusContainerID),
		hx("swap", "innerHTML"),
		hx("include", runModesInclude),
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
func runFailure(snap runSnapshot, wfID int64, csrf string, dlEligible bool, mode string) g.Node {
	// An empty-conversion abort is not a failure in the run sense — nothing was
	// submitted. Render it as its own actionable report (the message is the
	// escaped, actionable guidance from *comfy.ConversionEmptyError).
	if snap.Aborted {
		return alert("warning", "Run aborted — nothing to run", g.Text(snap.Message))
	}

	// Triage the missing set ONCE: the lead, the headline and the CTA must agree about
	// what this server can actually deliver (see batchInstallPlan).
	batch := planBatchInstall(snap.MissingModels, dlEligible)

	var detail []g.Node
	// Lead: what went wrong, in the user's terms. The raw engine message moves into
	// the technical <details> at the bottom.
	if lead := failureLead(snap, batch.Available); lead != "" {
		detail = append(detail, h.P(h.Class("text-sm text-slate-300"), g.Text(lead)))
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
				detail = append(detail, missingModelsPanel(snap.MissingModels, snap.MissingResolved, snap.LibMeta, wfID, csrf, dlEligible, mode))
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
	if tech := failureTechnicalDetail(snap); tech != nil {
		detail = append(detail, tech)
	}
	// The glyph makes the failure state distinguishable by shape, not by tint alone.
	return alertIcon("error", "⚠", failureTitle(snap), detail...)
}

// failureTitle is the failure alert's headline. It keeps the "Run failed" stem
// (that is the state, and it is what every failure surface is identified by) and
// appends the plain-language count of what is actually wrong, so the headline alone
// answers "what do I have to do?" for the overwhelmingly common case.
func failureTitle(snap runSnapshot) string {
	nm, nn := failureMissingCounts(snap)
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
// (see batchInstallPlan). It is LOAD-BEARING, not cosmetic: "Install them and it
// should run" is a promise, and on a server that cannot install anything — no
// comfy_model_path, a remote ComfyUI, or references whose type could not be inferred
// — that sentence is false and sends the reader looking for a button that is greyed
// out. The un-installable wording names the real next step instead.
func failureLead(snap runSnapshot, canInstall bool) string {
	nm, nn := failureMissingCounts(snap)
	switch {
	case nm > 0 && nn > 0:
		if !canInstall {
			return fmt.Sprintf("This workflow needs %d model file%s and %d custom node%s that are not installed in "+
				"ComfyUI yet. This copy of civitai-manager cannot fetch the model files for you — see the options "+
				"for each one below.", nm, plural(nm), nn, plural(nn))
		}
		return fmt.Sprintf("Nothing is broken — this workflow just needs %d model file%s and %d custom node%s "+
			"that are not installed in ComfyUI yet. Add them and it should run.", nm, plural(nm), nn, plural(nn))
	case nm > 0:
		if !canInstall {
			return fmt.Sprintf("This workflow needs %d model file%s that %s not installed in ComfyUI yet. "+
				"This copy of civitai-manager cannot fetch %s for you — see the options for each one below.",
				nm, plural(nm), isAre(nm), itThem(nm))
		}
		return fmt.Sprintf("Nothing is broken — this workflow just needs %d model file%s that %s not installed "+
			"in ComfyUI yet. Install %s and it should run.",
			nm, plural(nm), isAre(nm), itThem(nm))
	case nn > 0:
		return fmt.Sprintf("This workflow uses %d custom node%s that %s not installed in ComfyUI yet. "+
			"Install %s and it should run.", nn, plural(nn), isAre(nn), itThem(nn))
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
func failureTechnicalDetail(snap runSnapshot) g.Node {
	nm, nn := failureMissingCounts(snap)
	hasSummary := nm > 0 || nn > 0 || (snap.Preflight != nil && len(snap.Preflight.BadOptions) > 0)
	var body []g.Node
	if hasSummary && strings.TrimSpace(snap.Message) != "" {
		body = append(body, h.P(h.Class("text-xs text-slate-400 break-all"), g.Text(snap.Message)))
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

// failureMissingCounts is the (missing models, missing nodes) pair the copy above
// is built from. It counts the PREFLIGHT report (the authority) so the wording is
// right even when the enriched per-model analysis is absent.
func failureMissingCounts(snap runSnapshot) (models, nodes int) {
	if snap.Preflight == nil {
		return 0, 0
	}
	return len(snap.Preflight.MissingModels), len(snap.Preflight.MissingNodes)
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
					g.Attr("title", "Set comfy_model_path (with a local ComfyUI) to install this file here."),
				}, g.Text(label)),
				resolveFallbackLink(comfy.CleanModelQuery(bo.Current)),
			),
			h.P(h.Class("text-xs text-slate-500"),
				g.Text("Set comfy_model_path to install this file here, or fetch it manually.")),
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
