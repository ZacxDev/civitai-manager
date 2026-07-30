package web

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/ZacxDev/civitai-manager/internal/comfy"
	g "maragu.dev/gomponents"
	h "maragu.dev/gomponents/html"
)

// Stable container ids the cloud poller/swaps target (never outerHTML-replacing
// the poller node itself — the repo's streaming invariant).
const (
	cloudPanelContainerID    = "cloud-panel"
	cloudEstimateContainerID = "cloud-estimate"
	cloudStatusContainerID   = "cloud-status"
)

// cloudGenerateBlock is the "Run on CivitAI Cloud" sub-block of the detail page's
// ONE Generate section (generateSection in run_pages.go). It used to be a card of
// its own sitting beside the local Run card, which made the two read as competing
// features rather than as "run it here" vs "run it there".
//
// It lazy-loads the cloud-run panel into the SAME stable container as before
// (#cloud-panel, same endpoint, same swap) — only the surrounding chrome moved. The
// loaded fragment itself handles the enabled/disabled (comfy_cloud) and API-format
// cases.
func cloudGenerateBlock(wfID int64) g.Node {
	id := strconv.FormatInt(wfID, 10)
	return h.Div(
		h.Class("cm-gen-sep"),
		h.H3(h.Class("text-sm font-semibold text-slate-200 mb-2"), g.Text("Run on CivitAI Cloud")),
		// --- PR C2 (filled the C2 SEAM) ----------------------------------------
		// The CivitAI-cloud CONNECT block. C1 reserved this spot for a "credential
		// entry" form; the real code says there is NO credential to enter. Cloud
		// auth reuses the already-configured CivitAI Token (Server.cloud →
		// comfy.NewCloudClient(_, cfg.Token) → `Authorization: Bearer …`), so the
		// only thing to "connect" is the comfy_cloud on/off decision plus an honest
		// statement of where that token comes from. No secret is written over HTTP
		// and none is stored in the DB — see cloud_connect.go.
		//
		// It lazy-loads into its OWN stable container so a toggle re-renders it and,
		// via a one-shot loader, the panel below.
		// -----------------------------------------------------------------------
		h.Div(h.ID(cloudConnectContainerID),
			h.Class("mb-2"),
			hx("get", "/workflows/"+id+"/cloud/connect"),
			hx("trigger", "load"),
			hx("swap", "innerHTML"),
		),
		h.Div(h.ID(cloudPanelContainerID),
			hx("get", "/workflows/"+id+"/cloud"),
			hx("trigger", "load"),
			hx("swap", "innerHTML"),
			h.Span(h.Class("text-sm text-slate-400"), g.Text("Loading…")),
		),
	)
}

// cloudPanelView is the resolved state the cloud panel renders.
type cloudPanelView struct {
	wfID    int64
	enabled bool // comfy_cloud
	// runnable is true when a submittable API graph was resolved (either the
	// workflow is already API-format, or a UI-format graph converted cleanly via the
	// local ComfyUI). When false, note/warnings explain why.
	runnable bool
	// willConvert is true when a UI-format workflow WILL be converted to API format
	// via the local ComfyUI before submission (surfaced so the user understands it).
	willConvert bool
	// note is a human explanation shown when the workflow is not runnable (e.g. the
	// local ComfyUI is unreachable, or a conversion error). Escaped at render.
	note string
	// warnings is the UI→API conversion warning list (unrunnable/bypass/unknown
	// nodes) shown when a UI-format workflow could not be converted cleanly.
	warnings []string
	rows     []comfy.ResolvedResource
	snap     cloudSnapshot
}

// cloudEgressWarning is the prominent egress + Buzz-spend affordance, mirroring the
// match_remote egress tone. Rendered near the estimate/run controls so the data
// egress and Buzz spend are obvious before the user acts.
func cloudEgressWarning() g.Node {
	return alert("warning", "Sends data to civitai.com + spends Buzz",
		g.Text("Estimating or running this workflow submits your workflow graph and the "+
			"resource list to civitai.com. Running for real spends Buzz from your account."))
}

// cloudPanelFragment renders the full cloud-run panel into #cloud-panel: the
// egress warning, the resolved-resources table, the editable URN textarea, an
// Estimate button, and the estimate + status containers.
func cloudPanelFragment(v cloudPanelView, csrf string) g.Node {
	id := strconv.FormatInt(v.wfID, 10)

	if !v.enabled {
		// A bare "enable comfy_cloud in your config" is a dead end for anyone who does
		// not already know where that config file is or what the key does, so this state
		// carries a real next step.
		//
		// The nearest next step is now the TOGGLE above, not the config file: cloud is
		// switchable from the UI, and it needs no restart. The config route is kept as
		// the secondary answer because an explicit `comfy_cloud:` value still WINS over
		// the stored preference in both directions — so a reader who has one set needs
		// to know that is why the toggle is read-only for them. The docs link survives
		// from the config-only era: it is the thing that made this state actionable for
		// someone who does not know where the file lives, and it is still the right
		// answer for that reader.
		return h.Div(
			alert("info", "Cloud run is off",
				h.P(h.Class("text-sm"),
					g.Text("Running on CivitAI's cloud is opt-in. Turn it on with the "+
						"toggle above — no restart needed. You can also set "),
					h.Span(h.Class("font-mono"), g.Text("comfy_cloud: true")),
					g.Text(" in your config file, which then takes precedence over the toggle."),
				),
				h.P(h.Class("mt-1"), configDocsLink("Where the config file lives")),
			),
		)
	}
	if !v.runnable {
		// A UI-format workflow that could not be converted cleanly: show the specific
		// conversion warnings if we have them, else the note (unreachable ComfyUI or a
		// conversion error).
		if len(v.warnings) > 0 {
			return h.Div(cloudConversionWarnings(v.warnings))
		}
		return h.Div(
			alert("warning", "Cloud run needs a runnable graph", g.Text(v.note)),
		)
	}

	// Prefill the textarea: one line per resolved-resource row (its URN, or blank
	// for unresolved/custom so the user completes it) — line order matches the table.
	var lines []string
	for _, r := range v.rows {
		lines = append(lines, r.URN)
	}
	prefill := strings.Join(lines, "\n")

	return h.Div(
		h.Class("space-y-4"),
		cloudEgressWarning(),
		g.If(v.willConvert, cloudWillConvertNote()),
		cloudResourceTable(v.rows),
		h.Form(
			hx("post", "/workflows/"+id+"/cloud/whatif"),
			hx("target", "#"+cloudEstimateContainerID),
			hx("swap", "innerHTML"),
			h.Class("space-y-2"),
			csrfInput(csrf),
			h.Div(
				h.Label(dataFlag("civitai-ui-label"), h.For("cloud-urns"),
					g.Text("Resource URNs (one per line — edit unresolved/incorrect entries)")),
				h.Textarea(
					h.ID("cloud-urns"), h.Name("resources"),
					h.Class("w-full h-40 font-mono text-xs bg-slate-900 text-slate-200 rounded p-2 border border-slate-700"),
					g.Text(prefill),
				),
			),
			civButton("filled", "md", []g.Node{h.Type("submit"), hx("disabled-elt", "this")},
				g.Text("Estimate cost")),
		),
		h.Div(h.ID(cloudEstimateContainerID)),
		h.Div(h.ID(cloudStatusContainerID), cloudStatusFragment(v.snap, v.wfID, csrf)),
	)
}

// cloudWillConvertNote tells the user a UI-format workflow will be converted to API
// format via the local ComfyUI before it is submitted to CivitAI cloud — so the
// resolved-resources table below reflects the CONVERTED graph, not the raw UI graph.
func cloudWillConvertNote() g.Node {
	return alert("info", "UI-format workflow — converts via local ComfyUI",
		g.Text("This workflow is stored in UI format. Cloud run converts it to API format "+
			"using your local ComfyUI before submitting. The resources below are resolved from "+
			"the converted graph."))
}

// cloudConversionWarnings renders the UI→API conversion warning list (unrunnable/
// bypass/unknown nodes) as an error alert, mirroring the local-run abort: a workflow
// that can't be converted cleanly is NOT submitted (mis-submitting wastes Buzz). Each
// warning is untrusted (graph-derived) and escaped via g.Text by missingList.
func cloudConversionWarnings(warnings []string) g.Node {
	return alert("error", "This workflow could not be converted into a runnable graph",
		g.Text("Cloud run converts UI-format workflows to API format via your local ComfyUI, "+
			"but this workflow has nodes that could not be converted. It was not submitted."),
		missingList("Conversion warnings", warnings),
	)
}

// cloudResourceTable renders the resolved-resources table: Filename · Status badge
// · URN. Every untrusted string (filename, URN) is escaped via g.Text.
func cloudResourceTable(rows []comfy.ResolvedResource) g.Node {
	if len(rows) == 0 {
		return h.P(h.Class("text-sm text-slate-400"),
			g.Text("No resources were auto-detected in this workflow. Add any required URNs below."))
	}
	trs := make([]g.Node, 0, len(rows))
	for _, r := range rows {
		urnCell := g.Text(r.URN)
		if strings.TrimSpace(r.URN) == "" {
			urnCell = h.Span(h.Class("text-slate-500 italic"), g.Text("(fill in below)"))
		}
		trs = append(trs, h.Tr(
			h.Td(h.Class("py-1 pr-3 font-mono text-xs text-slate-300 align-top"), g.Text(r.Filename)),
			h.Td(h.Class("py-1 pr-3 align-top"), cloudStatusBadge(r.Status)),
			h.Td(h.Class("py-1 font-mono text-xs text-slate-400 break-all align-top"), urnCell),
		))
	}
	return h.Div(h.Class("overflow-x-auto"),
		h.Table(h.Class("w-full text-left text-sm"),
			h.THead(h.Tr(
				th("Resource"), th("Status"), th("AIR URN"),
			)),
			h.TBody(g.Group(trs)),
		),
	)
}

// cloudStatusBadge maps a resolution status to a labeled, colored badge.
func cloudStatusBadge(status string) g.Node {
	switch status {
	case comfy.ResolveResolved:
		return badge("have ✓", "green")
	case comfy.ResolveGuessed:
		return badge("guessed ⚠", "amber")
	case comfy.ResolveCustomNode:
		return badge("custom node", "blue")
	default: // unresolved
		return badge("unresolved ✗", "red")
	}
}

// cloudEstimateView is the resolved state the estimate fragment renders.
type cloudEstimateView struct {
	wfID             int64
	estimated        bool
	cost             float64
	insufficientBuzz bool
	urns             []string
	// affordability marks a gated-whatif 400 (affordability gate rejection): render
	// the escaped detail + a "run anyway" button rather than a generic estimate error.
	affordability bool
	err           string // untrusted remote error text (escaped)
}

// cloudEstimateFragment renders the whatif result + the "Run for real" button.
//
// Honesty note: a CustomComfy run is billed PER-SECOND of GPU time and its total
// is computed only AFTER it completes (Max(1, runtimeSeconds × buzzPerSecond)), so
// the whatif estimate returns ~0 upfront — it is NOT a meaningful fixed price.
// Rendering "Estimated cost: 0 Buzz" would read as "free", so instead we confirm
// the request was accepted, show the per-second billing reality plainly, and only
// surface a fixed number if the API ever actually returns one (>0).
func cloudEstimateFragment(v cloudEstimateView, csrf string) g.Node {
	if v.affordability {
		// The gated whatif was rejected (400) — treat it as an affordability warning
		// (escaped detail) + "run anyway", NOT a generic "Estimate failed".
		return cloudAffordabilityFragment(v.err, v.wfID, v.urns, csrf)
	}
	if v.err != "" {
		return alert("error", "Estimate failed", g.Text(v.err))
	}
	if !v.estimated {
		return h.Div()
	}

	body := []g.Node{
		h.P(h.Class("text-sm text-slate-200"),
			g.Text("CivitAI accepted this workflow. It is billed "),
			h.Span(h.Class("font-semibold"), g.Text("per second of GPU time while running")),
			g.Text(" — the total isn't known until it finishes, so no fixed upfront price is shown. Buzz will be charged for the actual runtime.")),
	}
	if v.cost > 0 {
		costStr := strconv.FormatFloat(v.cost, 'f', -1, 64)
		body = append(body, h.P(h.Class("text-sm text-slate-200"),
			g.Text("Estimated cost: "), h.Span(h.Class("font-semibold"), g.Text(costStr+" Buzz"))))
	}
	if v.insufficientBuzz {
		body = append(body,
			alert("error", "Insufficient Buzz",
				g.Text("Your account does not have enough Buzz to run this workflow.")))
	} else {
		body = append(body, cloudRunForRealButton(v.wfID, v.urns, csrf))
	}
	return h.Div(h.Class("space-y-2"), g.Group(body))
}

// cloudRunForRealButton posts the (edited) URNs to the real-run endpoint, driving
// the #cloud-status container. It carries the URNs forward via a hidden field so
// the run uses exactly the estimated set.
func cloudRunForRealButton(wfID int64, urns []string, csrf string) g.Node {
	id := strconv.FormatInt(wfID, 10)
	return h.Form(
		hx("post", "/workflows/"+id+"/cloud/run"),
		hx("target", "#"+cloudStatusContainerID),
		hx("swap", "innerHTML"),
		csrfInput(csrf),
		h.Input(h.Type("hidden"), h.Name("resources"), h.Value(strings.Join(urns, "\n"))),
		civButton("filled", "md", []g.Node{h.Type("submit"), hx("disabled-elt", "this")},
			g.Text("Run for real (spends Buzz)")),
	)
}

// cloudAffordabilityFragment renders the submit-time affordability-gate rejection:
// an amber alert with the API's (untrusted, escaped) ProblemDetails detail — which
// explains how much generation the user can afford — plus a "run anyway" button that
// resubmits with the 5-minute gate skipped. Shared by the whatif preview and the
// real-run terminal state.
func cloudAffordabilityFragment(detail string, wfID int64, urns []string, csrf string) g.Node {
	body := []g.Node{}
	if strings.TrimSpace(detail) != "" {
		body = append(body, g.Text(detail))
	} else {
		body = append(body, g.Text("CivitAI rejected this run because you cannot afford "+
			"the 5-minute minimum generation time."))
	}
	return h.Div(h.Class("space-y-2"),
		alert("warning", "Not enough Buzz for the 5-minute minimum", body...),
		cloudRunAnywayButton(wfID, urns, csrf),
	)
}

// cloudRunAnywayButton posts the (edited) URNs to the real-run endpoint with
// run_anyway=1 so the submit-time affordability gate is SKIPPED, driving the
// #cloud-status container. Mirrors cloudRunForRealButton but adds the skip flag.
func cloudRunAnywayButton(wfID int64, urns []string, csrf string) g.Node {
	id := strconv.FormatInt(wfID, 10)
	return h.Form(
		hx("post", "/workflows/"+id+"/cloud/run"),
		hx("target", "#"+cloudStatusContainerID),
		hx("swap", "innerHTML"),
		csrfInput(csrf),
		h.Input(h.Type("hidden"), h.Name("resources"), h.Value(strings.Join(urns, "\n"))),
		h.Input(h.Type("hidden"), h.Name("run_anyway"), h.Value("1")),
		civButton("filled", "md", []g.Node{h.Type("submit"), hx("disabled-elt", "this")},
			g.Text("Run anyway (skip the 5-min affordability check)")),
	)
}

// cloudStatusFragment dispatches the cloud run job's state into #cloud-status: the
// running fragment (poller + Stop) while in flight, else the terminal result. A run
// belonging to a DIFFERENT workflow is not shown here.
func cloudStatusFragment(snap cloudSnapshot, wfID int64, csrf string) g.Node {
	if snap.Started && snap.Running && snap.WorkflowID != wfID {
		return h.Div(h.Class("text-sm text-amber-400"),
			g.Text("A cloud run is already in progress for another workflow. Try again when it finishes."))
	}
	if !snap.Started || snap.WorkflowID != wfID {
		return h.Div()
	}
	if snap.Stopped {
		// Cancel is best-effort: if the run reached the orchestrator before Stop, a
		// cancel was requested but Buzz may already have been charged. Say so plainly
		// rather than imply a stopped run is always free.
		return h.Div(h.Class("space-y-2"),
			h.P(h.Class("text-sm text-amber-400"), g.Text("Cloud run canceled.")),
			h.P(h.Class("text-xs text-slate-400"),
				g.Text("If the run had already been submitted to CivitAI, Buzz may still have been charged.")))
	}
	if snap.Running {
		return cloudRunning(snap, wfID, csrf)
	}
	return cloudTerminal(snap, wfID, csrf)
}

// cloudPoller is the one-shot re-arming poll element driving the running view to
// terminal. It never targets itself: it fires once and swaps the innerHTML of the
// STABLE #cloud-status container.
func cloudPoller(wfID int64) g.Node {
	return h.Div(
		h.ID("cloud-poll"),
		hx("get", fmt.Sprintf("/workflows/cloud/status?workflow_id=%d", wfID)),
		hx("trigger", "load delay:2s"),
		hx("target", "#"+cloudStatusContainerID),
		hx("swap", "innerHTML"),
	)
}

// cloudRunning is the in-flight fragment: spinner + status line, a Stop button, and
// the re-arming poller.
func cloudRunning(snap cloudSnapshot, wfID int64, csrf string) g.Node {
	stop := civButton("filled", "sm", []g.Node{
		h.Type("button"),
		hx("post", "/workflows/cloud/stop"),
		hx("target", "#"+cloudStatusContainerID),
		hx("swap", "innerHTML"),
		hx("vals", fmt.Sprintf(`{"csrf_token":"%s","workflow_id":"%d"}`, csrf, wfID)),
	}, g.Text("Stop"))

	return h.Div(
		h.Class("space-y-3"),
		h.Div(h.Class("flex items-center gap-2 text-sm text-slate-300"),
			spinnerGlyph(),
			g.Text(snap.Message),
		),
		h.Div(h.Class("flex"), stop),
		cloudPoller(wfID),
	)
}

// cloudTerminal renders the settled cloud run: a result gallery on success, the
// affordability-rejection state (escaped detail + "run anyway") when the gated
// submit was rejected, or the plain failure message otherwise.
func cloudTerminal(snap cloudSnapshot, wfID int64, csrf string) g.Node {
	if snap.Phase == cloudPhaseDone {
		body := []g.Node{h.P(h.Class("text-sm text-emerald-400"), g.Text(snap.Message))}
		if gal := cloudGallery(snap.BlobURLs); gal != nil {
			body = append(body, gal)
		}
		return h.Div(h.Class("space-y-3"), g.Group(body))
	}
	if snap.Affordability {
		return cloudAffordabilityFragment(snap.Message, wfID, snap.URNs, csrf)
	}
	return alert("error", "Cloud run failed", g.Text(snap.Message))
}

// cloudGallery renders result blob URLs as a responsive grid of <img>s. The blob
// URLs are civitai CDN image URLs (same origin class as the showcase images the app
// already renders directly) — this does NOT violate the vendored-asset invariant,
// which concerns app JS/CSS/fonts, not user/result content. URLs are attribute-
// escaped by gomponents.
func cloudGallery(urls []string) g.Node {
	if len(urls) == 0 {
		return nil
	}
	imgs := make([]g.Node, 0, len(urls))
	for _, u := range urls {
		imgs = append(imgs, h.Img(
			h.Src(u),
			g.Attr("loading", "lazy"),
			h.Class("w-full h-auto rounded border border-slate-800 bg-slate-900"),
		))
	}
	return h.Div(h.Class("grid grid-cols-2 sm:grid-cols-3 gap-3"), g.Group(imgs))
}
