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

// cloudEntryCard is the detail-page card that lazy-loads the cloud-run panel into
// a stable container. It sits next to the local Run panel. The loaded fragment
// itself handles the enabled/disabled (comfy_cloud) and API-format cases.
func cloudEntryCard(wfID int64) g.Node {
	id := strconv.FormatInt(wfID, 10)
	return card(
		sectionTitle("Run on CivitAI Cloud"),
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
	wfID      int64
	enabled   bool // comfy_cloud
	apiFormat bool // the workflow is API-format (cloud-runnable)
	rows      []comfy.ResolvedResource
	snap      cloudSnapshot
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
		return h.Div(
			alert("info", "Cloud run is off",
				g.Text("Enable comfy_cloud in your config to run workflows on CivitAI cloud.")),
		)
	}
	if !v.apiFormat {
		return h.Div(
			alert("warning", "API-format required",
				g.Text("Cloud run currently requires an API-format workflow. This workflow is "+
					"stored in UI format; export it to API format (or import the API graph) to run it on cloud.")),
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
	err              string // untrusted remote error text (escaped)
}

// cloudEstimateFragment renders the cost estimate + the "Run for real" button
// (enabled only when the estimate says the account can afford it).
func cloudEstimateFragment(v cloudEstimateView, csrf string) g.Node {
	if v.err != "" {
		return alert("error", "Estimate failed", g.Text(v.err))
	}
	if !v.estimated {
		return h.Div()
	}

	costStr := strconv.FormatFloat(v.cost, 'f', -1, 64)
	body := []g.Node{
		h.P(h.Class("text-sm text-slate-200"),
			g.Text("Estimated cost: "), h.Span(h.Class("font-semibold"), g.Text(costStr+" Buzz"))),
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
	return cloudTerminal(snap)
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

// cloudTerminal renders the settled cloud run: a result gallery on success, or the
// failure message.
func cloudTerminal(snap cloudSnapshot) g.Node {
	if snap.Phase == cloudPhaseDone {
		body := []g.Node{h.P(h.Class("text-sm text-emerald-400"), g.Text(snap.Message))}
		if gal := cloudGallery(snap.BlobURLs); gal != nil {
			body = append(body, gal)
		}
		return h.Div(h.Class("space-y-3"), g.Group(body))
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
