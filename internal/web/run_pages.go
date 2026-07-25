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

// runPanel is the detail-page "Run" section: a Run button above the stable
// #run-status container whose innerHTML the poller drives through running →
// terminal. The button is enabled for both api and ui workflows (ui graphs are
// converted to api at run time). When the server is bound off-loopback the run
// endpoints are gated, so the panel shows a disabled note instead.
func runPanel(wf *store.Workflow, snap runSnapshot, csrf string, extraAllowed bool) g.Node {
	id := strconv.FormatInt(wf.ID, 10)
	if !extraAllowed {
		return card(
			sectionTitle("Run"),
			alert("warning", "",
				g.Text("Local run is disabled when the server is bound to a non-loopback address.")),
		)
	}
	runBtn := civButton("filled", "md", []g.Node{
		h.Type("button"),
		hx("post", "/workflows/"+id+"/run"),
		hx("target", "#"+runStatusContainerID),
		hx("swap", "innerHTML"),
		hx("disabled-elt", "this"),
		csrfInline(csrf),
	}, g.Text("Run on ComfyUI"))

	return card(
		h.Div(h.Class("flex items-center justify-between gap-4"),
			sectionTitle("Run"),
			runBtn,
		),
		h.P(h.Class("text-xs text-slate-500 mb-3"),
			g.Text("Submits this workflow to your local ComfyUI ("+comfyDisplayURL(wf)+"). "+
				"UI-format graphs are converted to API format first; missing nodes or models are reported before submitting.")),
		h.Div(h.ID(runStatusContainerID), runStatusFragment(snap, wf.ID, csrf)),
	)
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
func runStatusFragment(snap runSnapshot, wfID int64, csrf string) g.Node {
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
	return runTerminal(snap, wfID, csrf)
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
func runTerminal(snap runSnapshot, wfID int64, csrf string) g.Node {
	var body []g.Node
	switch snap.Phase {
	case runPhaseDone:
		body = append(body, h.P(h.Class("text-sm text-emerald-400"), g.Text(snap.Message)))
		if gal := runGallery(snap); gal != nil {
			body = append(body, gal)
		}
	default: // failed / stopped
		body = append(body, runFailure(snap))
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
func runFailure(snap runSnapshot) g.Node {
	var detail []g.Node
	detail = append(detail, g.Text(snap.Message))

	if snap.Preflight != nil {
		if len(snap.Preflight.MissingNodes) > 0 {
			detail = append(detail, missingList("Missing custom nodes", snap.Preflight.MissingNodes))
		}
		if len(snap.Preflight.MissingModels) > 0 {
			detail = append(detail, missingList("Missing model files", snap.Preflight.MissingModels))
		}
	}
	if len(snap.Warnings) > 0 {
		detail = append(detail, missingList("Conversion warnings", snap.Warnings))
	}
	return alert("error", "Run failed", detail...)
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
