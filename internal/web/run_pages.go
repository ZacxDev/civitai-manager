package web

import (
	"fmt"
	"net/url"
	"strconv"

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
func runPanel(wf *store.Workflow, snap runSnapshot, csrf string, extraAllowed, dlEligible bool, mode string) g.Node {
	id := strconv.FormatInt(wf.ID, 10)
	if !extraAllowed {
		return card(
			sectionTitle("Run"),
			alert("warning", "",
				g.Text("Local run is disabled when the server is bound to a non-loopback address.")),
		)
	}
	return card(
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
		// Run job status container (unchanged): poller drives running → terminal.
		h.Div(h.ID(runStatusContainerID), runStatusFragment(snap, wf.ID, csrf, dlEligible, mode)),
	)
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
		return runStopped(wfID, csrf)
	}
	if snap.Running {
		return runRunning(snap, wfID, csrf)
	}
	return runTerminal(snap, wfID, csrf, dlEligible, mode)
}

// runStopped is the terminal fragment after an explicit Stop (no poller).
func runStopped(wfID int64, csrf string) g.Node {
	body := []g.Node{h.P(h.Class("text-sm text-amber-400"), g.Text("Run stopped."))}
	if wfID > 0 {
		body = append(body, h.Div(h.Class("pt-1"), runAgainButton(wfID, csrf)))
	}
	return h.Div(h.Class("space-y-3"), g.Group(body))
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
		body = append(body, h.Div(h.Class("pt-1"), runAgainButton(wfID, csrf)))
	}
	return h.Div(h.Class("space-y-3"), g.Group(body))
}

// runAgainButton re-triggers a run into the same stable container.
func runAgainButton(wfID int64, csrf string) g.Node {
	return civButton("outline", "sm", []g.Node{
		h.Type("button"),
		hx("post", fmt.Sprintf("/workflows/%d/run", wfID)),
		hx("target", "#"+runStatusContainerID),
		hx("swap", "innerHTML"),
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

// runFailure renders the failure report: the (escaped, untrusted) message plus any
// preflight detail (missing nodes/models) or conversion warnings.
func runFailure(snap runSnapshot, wfID int64, csrf string, dlEligible bool, mode string) g.Node {
	// An empty-conversion abort is not a failure in the run sense — nothing was
	// submitted. Render it as its own actionable report (the message is the
	// escaped, actionable guidance from *comfy.ConversionEmptyError).
	if snap.Aborted {
		return alert("warning", "Run aborted — nothing to run", g.Text(snap.Message))
	}

	var detail []g.Node
	detail = append(detail, g.Text(snap.Message))

	if snap.Preflight != nil {
		if len(snap.Preflight.MissingNodes) > 0 {
			detail = append(detail, missingList("Missing custom nodes", snap.Preflight.MissingNodes))
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
			detail = append(detail, incompatibleOptionsSection(snap.Preflight.BadOptions, wfID, csrf))
		}
	}
	if len(snap.Warnings) > 0 {
		detail = append(detail, missingList("Conversion warnings", snap.Warnings))
	}
	return alert("error", "Run failed", detail...)
}

// incompatibleOptionsSection renders the "Incompatible options" section: a single
// <form> (hx-post to /run-with-options, CSRF in a hidden input) with one group per
// BadOption — the node class + input name + the current invalid value + a <select>
// of the input's valid choices — and ONE "Run with selected options" submit that
// applies every pick and runs. It lives inside the terminal (poller-free) fragment,
// so it is never swapped away mid-interaction. Every untrusted string (class, input,
// current value, choice) is escaped via g.Text / attribute escaping.
func incompatibleOptionsSection(bad []comfy.BadOption, wfID int64, csrf string) g.Node {
	groups := make([]g.Node, 0, len(bad))
	for i, bo := range bad {
		groups = append(groups, badOptionGroup(i, bo))
	}
	return h.Form(
		hx("post", "/workflows/"+strconv.FormatInt(wfID, 10)+"/run-with-options"),
		hx("target", "#"+runStatusContainerID),
		hx("swap", "innerHTML"),
		hx("disabled-elt", "find button[type='submit']"),
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
// opt_new <select>. A single-choice group pre-selects its only valid option (still
// surfaced, never silently rewritten); a multi-choice group leads with a "Choose…"
// placeholder and marks the select required so a pick is forced.
func badOptionGroup(idx int, bo comfy.BadOption) g.Node {
	single := len(bo.Choices) == 1
	opts := make([]selectOption, 0, len(bo.Choices)+1)
	selected := ""
	if single {
		selected = bo.Choices[0]
	} else {
		opts = append(opts, selectOption{Value: "", Label: "Choose a valid option…"})
	}
	for _, c := range bo.Choices {
		opts = append(opts, selectOption{Value: c, Label: c})
	}
	return h.Div(
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
		optionSelect("opt-new-"+strconv.Itoa(idx), opts, selected, !single),
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
		lis = append(lis, h.Li(h.Class("font-mono text-xs text-slate-300"), g.Text(it)))
	}
	return h.Div(h.Class("mt-2"),
		h.Div(h.Class("text-xs font-semibold text-slate-200"), g.Text(title)),
		h.Ul(h.Class("list-disc pl-5 space-y-0.5 mt-1"), g.Group(lis)),
	)
}
