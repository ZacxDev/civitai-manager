package web

import (
	"fmt"
	"strconv"

	"github.com/ZacxDev/civitai-manager/internal/library"
	"github.com/ZacxDev/civitai-manager/internal/store"
	g "maragu.dev/gomponents"
	h "maragu.dev/gomponents/html"
)

// workflowScanResultsID is the STABLE container the workflow-scan poller swaps
// into (never outerHTML-replacing the poller node itself — the repo's streaming
// invariant). The scan form + result list live inside it, so a status swap hides
// the form while scanning and restores it (with the refreshed list) when settled.
const workflowScanResultsID = "workflow-scan-results"

// workflowScanFormCard is the idle/terminal control: a "Scan workflows" button
// that POSTs /library/workflow-scan and swaps the running fragment into the stable
// container. Disabled off-loopback (the walk-triggering endpoint is gated).
func workflowScanFormCard(csrf string, extraAllowed bool) g.Node {
	if !extraAllowed {
		return card(
			sectionTitle("Scan for workflows"),
			h.P(h.Class("text-sm text-slate-400"),
				g.Text("Workflow scanning is disabled when the server is bound to a non-loopback address.")),
		)
	}
	return card(
		sectionTitle("Scan for workflows"),
		h.P(h.Class("mb-3 text-sm text-slate-400"),
			g.Text("Find ComfyUI installs and index their saved workflows, auto-linking each to a matching model version in your library.")),
		h.Form(
			hx("post", "/library/workflow-scan"),
			hx("target", "#"+workflowScanResultsID),
			hx("swap", "innerHTML"),
			hx("disabled-elt", "find button"),
			csrfInput(csrf),
			btnPrimary(g.Text("Scan workflows")),
		),
	)
}

// workflowScanPoller is the one-shot re-arming poll element that drives the
// scanning view to its terminal state (the workflow twin of scanPoller). It never
// targets itself: it fires once, GETs the status, and swaps the innerHTML of the
// STABLE container. Each running response carries a fresh poller; the terminal
// fragment carries none, so polling stops.
func workflowScanPoller() g.Node {
	return h.Div(
		h.ID("workflow-scan-poll"),
		hx("get", "/library/workflow-scan/status"),
		hx("trigger", "load delay:1s"),
		hx("target", "#"+workflowScanResultsID),
		hx("swap", "innerHTML"),
	)
}

// workflowScanStopButton POSTs /library/workflow-scan/stop and swaps the status
// fragment into the stable container.
func workflowScanStopButton(csrf string) g.Node {
	return civButton("filled", "lg", []g.Node{
		h.Type("button"),
		hx("post", "/library/workflow-scan/stop"),
		hx("target", "#"+workflowScanResultsID),
		hx("swap", "innerHTML"),
		csrfInline(csrf),
		h.Class("w-full sm:w-auto"),
	}, g.Text("Stop scanning"))
}

// workflowScanProgressText renders the muted running/terminal count line.
func workflowScanProgressText(snap workflowScanSnapshot) string {
	s := fmt.Sprintf("%d found · %d linked", snap.Found, snap.Linked)
	if snap.Unchanged > 0 {
		s += fmt.Sprintf(" · %d unchanged", snap.Unchanged)
	}
	if snap.Skip > 0 {
		s += fmt.Sprintf(" · %d skipped", snap.Skip)
	}
	return s
}

// workflowScanScanning is the in-progress fragment swapped into the stable
// container: a Stop CTA, a spinner + live progress, the workflows found so far,
// and the one-shot re-arming poller.
func workflowScanScanning(snap workflowScanSnapshot, csrf string) g.Node {
	header := h.Div(
		h.Class("flex items-center gap-2 text-sm text-slate-300"),
		spinnerGlyph(),
		g.Text("Scanning for ComfyUI workflows… "+workflowScanProgressText(snap)+" (Stop any time)"),
	)
	children := []g.Node{
		h.Class("space-y-3 border-indigo-500/50"),
		header,
		h.Div(h.Class("flex"), workflowScanStopButton(csrf)),
	}
	if len(snap.Results) > 0 {
		var cards []g.Node
		for _, wr := range snap.Results {
			cards = append(cards, workflowScanResultCard(wr))
		}
		children = append(children, h.Div(h.Class("space-y-2"), g.Group(cards)))
	}
	return h.Div(
		h.Class("mt-2 space-y-2"),
		card(children...),
		workflowScanPoller(),
	)
}

// workflowScanTerminal is the settled fragment (no poller): a status line, the
// scan form card (restored), and the authoritative workflow list rebuilt from the
// store. snap.Started=false renders just the form + list so a stray poller halts.
func workflowScanTerminal(wfs []store.Workflow, snap workflowScanSnapshot, csrf string, extraAllowed bool, resolver workflowResolver) g.Node {
	var status g.Node
	switch {
	case !snap.Started:
		status = nil
	case snap.Err != nil && !snap.Stopped:
		status = h.P(h.Class("text-xs text-amber-400"), g.Text("Workflow scan ended early: "+snap.Err.Error()))
	case snap.Stopped || snap.Err != nil:
		status = h.P(h.Class("text-xs text-amber-400"),
			g.Text("Scan stopped — "+workflowScanProgressText(snap)))
	default:
		status = h.P(h.Class("text-xs text-emerald-400"),
			g.Text("Scan complete — "+workflowScanProgressText(snap)))
	}
	return h.Div(
		h.Class("space-y-4"),
		workflowScanFormCard(csrf, extraAllowed),
		g.If(status != nil, card(h.Class("space-y-1"), sectionTitle("Workflow scan"), status)),
		workflowList(wfs, csrf, extraAllowed, resolver),
	)
}

// workflowScanResultCard renders one streamed scanned workflow: its name, format
// badge, linked-version indicator, and resource count. Untrusted on-disk names go
// through g.Text (auto-escaped).
func workflowScanResultCard(wr library.WorkflowResult) g.Node {
	fmtVariant := "slate"
	if wr.Format == store.WorkflowFormatAPI {
		fmtVariant = "green"
	}
	meta := []g.Node{badge(wr.Format, fmtVariant)}
	if wr.Linked && wr.VersionID != nil {
		label := "→ v" + strconv.Itoa(*wr.VersionID)
		if wr.ModelID != nil {
			meta = append(meta, h.A(h.Href("/models/"+strconv.Itoa(*wr.ModelID)),
				h.Class("text-xs text-indigo-300 hover:text-indigo-200"),
				g.Text("model "+strconv.Itoa(*wr.ModelID)+" "+label)))
		} else {
			meta = append(meta, h.Span(h.Class("text-xs text-slate-400"), g.Text(label)))
		}
	} else if wr.Unchanged {
		meta = append(meta, badge("unchanged", "slate"))
	} else {
		meta = append(meta, badge("unlinked", "slate"))
	}
	if n := len(wr.Resources); n > 0 {
		meta = append(meta, h.Span(h.Class("text-xs text-slate-400"),
			g.Text(fmt.Sprintf("%d resource%s", n, plural(n)))))
	}
	return h.Div(
		h.Class("rounded-md border border-slate-800 bg-slate-900 p-2"),
		h.Div(h.Class("truncate text-sm text-slate-200"), h.Title(wr.Name), g.Text(wr.Name)),
		h.Div(h.Class("flex flex-wrap items-center gap-1 mt-1"), g.Group(meta)),
	)
}
