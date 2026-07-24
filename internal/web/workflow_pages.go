package web

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/ZacxDev/civitai-manager/internal/store"
	g "maragu.dev/gomponents"
	h "maragu.dev/gomponents/html"
)

// libraryWorkflowsView bundles what the Workflows library tab renders: the stored
// workflows, an optional import/action flash, and the bootstrapped initial content
// of the stable #workflow-scan-results container (the live scanning fragment on a
// reload during a scan, or nil to fall back to the idle terminal view).
type libraryWorkflowsView struct {
	Workflows   []store.Workflow
	Flash       string
	FlashLevel  string
	ScanInitial g.Node
}

// workflowsPanel is the "Workflows" Library tab: an import panel (paste JSON /
// upload PNG) above the workflow-scan control + stored-workflow list. It mirrors
// filesPanel: the scan control and list live inside the STABLE
// #workflow-scan-results container, so a scan status swap hides the control while
// scanning and restores it (with the refreshed list) when settled. extraAllowed
// reflects the loopback gate — import + scanning are disabled off-loopback,
// matching the egress posture for endpoints that ingest/scan arbitrary content.
func workflowsPanel(wfs []store.Workflow, csrf string, extraAllowed bool, flashLevel, flashMsg string, scanInitial g.Node) g.Node {
	if scanInitial == nil {
		// Idle: the scan form card above the current list (rebuilt on each status swap).
		scanInitial = workflowScanTerminal(wfs, workflowScanSnapshot{}, csrf, extraAllowed)
	}
	var body []g.Node
	body = append(body, h.P(h.Class("text-sm text-slate-400"),
		g.Text("Locally-curated ComfyUI workflows. Import an API/UI graph, extract one from a ComfyUI PNG, "+
			"or scan your ComfyUI installs to index and auto-link saved workflows. Local run is coming in a later release.")))
	if flashMsg != "" {
		if flashLevel == "" {
			flashLevel = "info"
		}
		body = append(body, alert(flashLevel, "", g.Text(flashMsg)))
	}
	body = append(body, workflowImportPanel(csrf, extraAllowed))
	// The STABLE poll/results container: only its innerHTML is ever swapped, so the
	// re-arming workflow-scan poller can never orphan its #workflow-scan-poll.
	body = append(body, h.Div(h.ID(workflowScanResultsID), scanInitial))
	return h.Div(h.Class("space-y-6"), g.Group(body))
}

// workflowImportPanel renders the paste-JSON and upload-PNG import affordances.
func workflowImportPanel(csrf string, extraAllowed bool) g.Node {
	if !extraAllowed {
		return card(
			sectionTitle("Import a workflow"),
			alert("warning", "",
				g.Text("Workflow import is disabled when the server is bound to a non-loopback address.")),
		)
	}
	return card(
		sectionTitle("Import a workflow"),
		h.Div(h.Class("grid gap-6 md:grid-cols-2"),
			// Paste API/UI JSON.
			h.Form(
				h.Method("post"), h.Action("/workflows/import"),
				h.Class("space-y-3"),
				csrfInput(csrf),
				textInput("text-input", "wf-name", "Name (optional)",
					h.Type("text"), h.Name("name"), h.Placeholder("e.g. SDXL portrait")),
				textInput("textarea", "wf-graph", "Workflow JSON (API or UI format)",
					h.Name("graph"), h.Rows("8"),
					h.Placeholder(`{"3":{"class_type":"CheckpointLoaderSimple", ...}}`),
					g.Attr("required")),
				btnPrimary(g.Text("Import JSON")),
			),
			// Upload a ComfyUI PNG.
			h.Form(
				h.Method("post"), h.Action("/workflows/import-png"),
				g.Attr("enctype", "multipart/form-data"),
				h.Class("space-y-3"),
				csrfInput(csrf),
				h.Div(
					h.Label(dataFlag("civitai-ui-label"), h.For("wf-png"),
						g.Text("ComfyUI PNG (extracts the embedded workflow)")),
					h.Input(h.Type("file"), h.ID("wf-png"), h.Name("png"),
						h.Accept("image/png"), g.Attr("required"),
						h.Class("block w-full text-sm text-slate-300 mt-1")),
				),
				btnPrimary(g.Text("Upload PNG")),
			),
		),
	)
}

// workflowList renders the stored workflows as cards, or an empty state.
func workflowList(wfs []store.Workflow, csrf string) g.Node {
	if len(wfs) == 0 {
		return card(h.P(h.Class("text-slate-400 text-center py-6"),
			g.Text("No workflows yet. Import one above to get started.")))
	}
	cards := make([]g.Node, 0, len(wfs))
	for _, wf := range wfs {
		cards = append(cards, workflowCard(wf, csrf))
	}
	return h.Div(h.Class("space-y-4"), g.Group(cards))
}

// workflowCard renders one stored workflow.
func workflowCard(wf store.Workflow, csrf string) g.Node {
	id := strconv.FormatInt(wf.ID, 10)

	name := wf.Name
	if strings.TrimSpace(name) == "" {
		name = "workflow #" + id
	}

	// Format badge: api (runnable) is highlighted, ui is neutral.
	fmtVariant := "slate"
	if wf.Format == store.WorkflowFormatAPI {
		fmtVariant = "green"
	}

	var meta []g.Node
	meta = append(meta, badge(wf.Format, fmtVariant))
	meta = append(meta, badge(wf.Source, "slate"))
	if wf.IsGolden {
		meta = append(meta, badge("golden ✓", "amber"))
	}
	if wf.VersionID != nil {
		var link g.Node
		label := "version " + strconv.Itoa(*wf.VersionID)
		if wf.ModelID != nil {
			link = h.A(h.Href("/models/"+strconv.Itoa(*wf.ModelID)),
				h.Class("text-indigo-400 hover:text-indigo-300"),
				g.Text("model "+strconv.Itoa(*wf.ModelID)+" · "+label))
		} else {
			link = g.Text(label)
		}
		meta = append(meta, h.Span(h.Class("text-sm text-slate-400"), link))
	}
	if n := len(wf.Resources); n > 0 {
		meta = append(meta, h.Span(h.Class("text-sm text-slate-400"),
			g.Text(fmt.Sprintf("%d resource%s", n, plural(n)))))
	}

	// Actions row.
	var actions []g.Node
	// Run is intentionally disabled this slice (no execution engine yet).
	actions = append(actions, civButton("outline", "sm",
		[]g.Node{h.Type("button"), g.Attr("disabled"),
			g.Attr("title", "local run coming in the next release")},
		g.Text("Run")))
	actions = append(actions, h.A(h.Href("/workflows/"+id),
		h.Class("text-sm text-indigo-400 hover:text-indigo-300 self-center"),
		g.Text("View")))

	// Golden toggle: only meaningful when attached to a version.
	if wf.VersionID != nil {
		if wf.IsGolden {
			actions = append(actions, postButton("/workflows/"+id+"/golden", csrf,
				map[string]string{"action": "unset"}, "outline", "Unset golden"))
		} else {
			actions = append(actions, postButton("/workflows/"+id+"/golden", csrf,
				map[string]string{"action": "set"}, "outline", "Set golden"))
		}
	}
	actions = append(actions, postButton("/workflows/"+id+"/delete", csrf, nil, "subtle", "Delete"))

	return card(
		h.Div(h.Class("flex items-start justify-between gap-4"),
			h.Div(
				h.H3(h.Class("text-lg font-semibold text-slate-100"), g.Text(name)),
				h.Div(h.Class("flex flex-wrap items-center gap-2 mt-2"), g.Group(meta)),
			),
		),
		h.Div(h.Class("flex flex-wrap items-center gap-2 mt-4"), g.Group(actions)),
	)
}

// workflowDetailPage renders a single workflow: its pretty-printed graph (escaped
// — untrusted), resources, attachment controls, and metadata.
func workflowDetailPage(wf *store.Workflow, prettyGraph, csrf, theme, nsfwMode string) g.Node {
	id := strconv.FormatInt(wf.ID, 10)
	name := wf.Name
	if strings.TrimSpace(name) == "" {
		name = "workflow #" + id
	}

	var body []g.Node
	body = append(body, h.Div(h.Class("flex items-center justify-between"),
		h.H1(h.Class("text-2xl font-semibold text-slate-100"), g.Text(name)),
		h.A(h.Href("/library?tab=workflows"), h.Class("text-sm text-indigo-400 hover:text-indigo-300"),
			g.Text("← Back to Workflows")),
	))

	// Metadata card.
	meta := []g.Node{
		metaRow("Format", wf.Format),
		metaRow("Source", wf.Source),
	}
	if wf.BaseModel != "" {
		meta = append(meta, metaRow("Base model", wf.BaseModel))
	}
	if wf.VersionID != nil {
		meta = append(meta, metaRow("Attached version", strconv.Itoa(*wf.VersionID)))
	}
	if wf.ModelID != nil {
		meta = append(meta, metaRow("Attached model", strconv.Itoa(*wf.ModelID)))
	}
	meta = append(meta, metaRow("Golden", boolText(wf.IsGolden)))
	body = append(body, card(sectionTitle("Details"), h.Dl(h.Class("space-y-1"), g.Group(meta))))

	// Resources card.
	if len(wf.Resources) > 0 {
		items := make([]g.Node, 0, len(wf.Resources))
		for _, r := range wf.Resources {
			items = append(items, h.Li(h.Class("text-sm text-slate-300 font-mono"), g.Text(r)))
		}
		body = append(body, card(sectionTitle("Referenced resources"),
			h.Ul(h.Class("list-disc pl-5 space-y-1"), g.Group(items))))
	}

	// Attachment controls.
	body = append(body, card(
		sectionTitle("Attach to a civitai version"),
		h.Form(
			h.Method("post"), h.Action("/workflows/"+id+"/attach"),
			h.Class("flex flex-wrap items-end gap-3"),
			csrfInput(csrf),
			h.Div(h.Class("w-40"), textInput("number-input", "wf-model", "Model id",
				h.Type("number"), h.Name("model_id"), h.Min("0"),
				attachValue(wf.ModelID))),
			h.Div(h.Class("w-40"), textInput("number-input", "wf-version", "Version id",
				h.Type("number"), h.Name("version_id"), h.Min("0"),
				attachValue(wf.VersionID))),
			btnPrimary(g.Text("Attach")),
		),
		h.P(h.Class("text-xs text-slate-500 mt-2"),
			g.Text("Leave both blank and submit to detach (also clears golden).")),
	))

	// Graph card — escaped, scrollable.
	body = append(body, card(
		sectionTitle("Graph JSON"),
		h.Pre(
			h.Class("overflow-x-auto text-xs text-slate-300 bg-slate-900 rounded p-3 max-h-96"),
			h.Code(g.Text(prettyGraph)),
		),
	))

	return page(name+" · Workflow", theme, csrf, nsfwMode, body...)
}

// postButton renders a small inline form that POSTs (with CSRF + optional extra
// fields) to path, styled as a civitai button. Using a real form (not hx-vals)
// keeps the action verifiable with a plain HTTP POST.
func postButton(path, csrf string, fields map[string]string, variant, label string) g.Node {
	inner := []g.Node{
		h.Method("post"), h.Action(path),
		h.Class("inline"),
		csrfInput(csrf),
	}
	for k, v := range fields {
		inner = append(inner, h.Input(h.Type("hidden"), h.Name(k), h.Value(v)))
	}
	inner = append(inner, civButton(variant, "sm", []g.Node{h.Type("submit")}, g.Text(label)))
	return h.Form(inner...)
}

func metaRow(label, value string) g.Node {
	return h.Div(h.Class("flex gap-2 text-sm"),
		h.Dt(h.Class("text-slate-500 w-40 shrink-0"), g.Text(label)),
		h.Dd(h.Class("text-slate-200"), g.Text(value)),
	)
}

func attachValue(p *int) g.Node {
	if p == nil {
		return g.Text("")
	}
	return h.Value(strconv.Itoa(*p))
}

func boolText(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
