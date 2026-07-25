package web

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ZacxDev/civitai-manager/internal/store"
	g "maragu.dev/gomponents"
	h "maragu.dev/gomponents/html"
)

// workflowResolver resolves the display data a workflow list item enriches its
// civitai linkage with, entirely from LOCAL state (offline-first): a model's
// cached display name + raw detail (from model_cache) and whether a referenced
// resource file is present locally. Its funcs may be nil (a zero resolver renders
// the plain fallbacks), so every access is nil-guarded through the methods below.
type workflowResolver struct {
	// cachedModel returns a model's cached name + raw GetModel body, ok=false when
	// there is no cached entry (the caller then lazy-loads the name via /title).
	cachedModel func(id int) (name string, raw []byte, ok bool)
	// haveFile reports whether a file with the given basename exists locally.
	haveFile func(basename string) bool
}

// modelName returns the cached, non-blank model name, ok=false when uncached (so
// the caller lazy-loads it via the existing /models/{id}/title endpoint).
func (r workflowResolver) modelName(id int) (string, bool) {
	if r.cachedModel == nil {
		return "", false
	}
	name, _, ok := r.cachedModel(id)
	if !ok || strings.TrimSpace(name) == "" {
		return "", false
	}
	return name, true
}

// versionName parses the version's name out of the cached model's raw detail,
// ok=false when the model is uncached or the version id is not present.
func (r workflowResolver) versionName(modelID, versionID int) (string, bool) {
	if r.cachedModel == nil {
		return "", false
	}
	_, raw, ok := r.cachedModel(modelID)
	if !ok {
		return "", false
	}
	return versionNameFromRaw(raw, versionID)
}

// have reports whether the resource basename is present locally (false for a nil
// resolver — no local-file knowledge, so nothing is claimed present).
func (r workflowResolver) have(basename string) bool {
	if r.haveFile == nil {
		return false
	}
	return r.haveFile(basename)
}

// libraryWorkflowsView bundles what the Workflows library tab renders: the stored
// workflows, an optional import/action flash, and the bootstrapped initial content
// of the stable #workflow-scan-results container (the live scanning fragment on a
// reload during a scan, or nil to fall back to the idle terminal view).
type libraryWorkflowsView struct {
	Workflows   []store.Workflow
	Flash       string
	FlashLevel  string
	ScanInitial g.Node
	// Resolver resolves list-item model/version names + local-file presence from
	// local state (built by the handler, which has store access).
	Resolver workflowResolver
}

// workflowsPanel is the "Workflows" Library tab: an import panel (paste JSON /
// upload PNG) above the workflow-scan control + stored-workflow list. It mirrors
// filesPanel: the scan control and list live inside the STABLE
// #workflow-scan-results container, so a scan status swap hides the control while
// scanning and restores it (with the refreshed list) when settled. extraAllowed
// reflects the loopback gate — import + scanning are disabled off-loopback,
// matching the egress posture for endpoints that ingest/scan arbitrary content.
func workflowsPanel(wfs []store.Workflow, csrf string, extraAllowed bool, flashLevel, flashMsg string, scanInitial g.Node, resolver workflowResolver) g.Node {
	if scanInitial == nil {
		// Idle: the scan form card above the current list (rebuilt on each status swap).
		scanInitial = workflowScanTerminal(wfs, workflowScanSnapshot{}, csrf, extraAllowed, resolver)
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

// workflowImportDialogID is the native <dialog> opened by the "Import a workflow"
// trigger button. The open call is a tiny INLINE script (allowed — the offline
// invariant forbids EXTERNAL scripts/styles only); the dialog closes via its own
// <form method="dialog"> controls.
const workflowImportDialogID = "workflow-import-dialog"

// workflowImportPanel renders the "Import a workflow" trigger button plus the
// native <dialog> holding the paste-JSON and upload-PNG import forms. The forms,
// their endpoints, CSRF, and loopback gate are unchanged — only relocated behind
// the modal. The <dialog> itself is a transparent positioning shell; the visible
// surface is a theme-aware card so it renders correctly under both data-themes.
func workflowImportPanel(csrf string, extraAllowed bool) g.Node {
	if !extraAllowed {
		return card(
			sectionTitle("Import a workflow"),
			alert("warning", "",
				g.Text("Workflow import is disabled when the server is bound to a non-loopback address.")),
		)
	}
	trigger := civButton("filled", "md", []g.Node{
		h.Type("button"),
		// Inline open — no external script. showModal() gives the native top-layer
		// modal + backdrop; the dialog id is a constant, not user input.
		g.Attr("onclick", "document.getElementById('"+workflowImportDialogID+"').showModal()"),
	}, g.Text("Import a workflow"))

	dialog := h.Dialog(
		h.ID(workflowImportDialogID),
		// Transparent, chrome-less shell — the inner card provides the visible,
		// theme-aware surface. Constrain width; the UA centers a showModal() dialog.
		h.Class("bg-transparent p-0 border-0 w-full max-w-2xl"),
		card(
			h.Div(h.Class("flex items-center justify-between gap-4 mb-3"),
				h.H2(h.Class("text-lg font-semibold text-slate-100"), g.Text("Import a workflow")),
				// A dialog-method form closes the modal without submitting anything.
				h.Form(h.Method("dialog"), h.Class("inline"),
					civButton("subtle", "sm", []g.Node{h.Type("submit"),
						g.Attr("aria-label", "Close")}, g.Text("✕"))),
			),
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
			h.Div(h.Class("flex justify-end mt-4"),
				h.Form(h.Method("dialog"), h.Class("inline"),
					btnSecondary(g.Text("Cancel")))),
		),
	)

	return card(
		sectionTitle("Import a workflow"),
		h.P(h.Class("text-sm text-slate-400 mb-3"),
			g.Text("Paste an API/UI graph or extract one from a ComfyUI PNG.")),
		trigger,
		dialog,
	)
}

// workflowList renders the stored workflows as cards, or an empty state.
func workflowList(wfs []store.Workflow, csrf string, resolver workflowResolver) g.Node {
	if len(wfs) == 0 {
		return card(h.P(h.Class("text-slate-400 text-center py-6"),
			g.Text("No workflows yet. Import one above to get started.")))
	}
	cards := make([]g.Node, 0, len(wfs))
	for _, wf := range wfs {
		cards = append(cards, workflowCard(wf, csrf, resolver))
	}
	return h.Div(h.Class("space-y-4"), g.Group(cards))
}

// workflowCard renders one stored workflow.
func workflowCard(wf store.Workflow, csrf string, resolver workflowResolver) g.Node {
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
	// Model linkage: a link to the model page whose text is the RESOLVED name —
	// instantly from model_cache when present, else lazy-loaded via the existing
	// /models/{id}/title endpoint (cache-first, one civitai fetch when uncached).
	if wf.ModelID != nil {
		meta = append(meta, workflowModelLink(*wf.ModelID, resolver))
	}
	// Version: the resolved version name (parsed from the cached model's raw
	// detail) when available, else the bare "version {id}" fallback.
	if wf.VersionID != nil {
		label := "version " + strconv.Itoa(*wf.VersionID)
		if wf.ModelID != nil {
			if vn, ok := resolver.versionName(*wf.ModelID, *wf.VersionID); ok {
				label = vn
			}
		}
		meta = append(meta, badge(label, "slate"))
	}
	if len(wf.Resources) > 0 {
		meta = append(meta, workflowResourcesDisclosure(wf.Resources, resolver))
	}

	// Actions row. Run links to the detail page, which hosts the live run panel
	// (submit → progress → result gallery) against the local ComfyUI. It is an
	// anchor styled as a button (not a <button> inside an <a>, which is invalid).
	var actions []g.Node
	actions = append(actions, h.A(
		h.Href("/workflows/"+id+"#"+runStatusContainerID),
		dataAttr("civitai-ui", "button"), dataAttr("variant", "outline"), dataAttr("size", "sm"),
		g.Attr("title", "Run on your local ComfyUI"),
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

// workflowModelLink renders the "→ model page" chip: an <a> to /models/{id} whose
// text is the resolved model name. When the name is cached it renders inline;
// otherwise the span lazy-loads it (hx-get=/models/{id}/title, hx-trigger=load —
// the endpoint is cache-first and fetches civitai only on a cache miss), showing a
// "model #id" placeholder until the swap. All text is escaped via g.Text.
func workflowModelLink(modelID int, resolver workflowResolver) g.Node {
	ids := strconv.Itoa(modelID)
	var text g.Node
	if nm, ok := resolver.modelName(modelID); ok {
		text = g.Text(nm)
	} else {
		text = h.Span(
			hx("get", "/models/"+ids+"/title"),
			hx("trigger", "load"),
			g.Text("model #"+ids),
		)
	}
	return h.A(
		h.Href("/models/"+ids),
		h.Class("text-sm text-indigo-400 hover:text-indigo-300"),
		text,
	)
}

// workflowResourcesDisclosure renders the referenced-resource list as a compact,
// native <details> disclosure (zero-JS): the summary shows the count; expanding
// lists each filename with a have ✓ / missing ✗ badge from a local-file check.
// Filenames are UNTRUSTED (from arbitrary graphs) — escaped via g.Text.
func workflowResourcesDisclosure(resources []string, resolver workflowResolver) g.Node {
	items := make([]g.Node, 0, len(resources))
	for _, res := range resources {
		var b g.Node
		if resolver.have(filepath.Base(res)) {
			b = badge("have ✓", "green")
		} else {
			b = badge("missing ✗", "red")
		}
		items = append(items, h.Li(
			h.Class("flex items-center gap-2"),
			b,
			h.Span(h.Class("font-mono text-xs text-slate-300 break-all"), g.Text(res)),
		))
	}
	n := len(resources)
	return h.Details(
		h.Class("text-sm"),
		h.Summary(
			h.Class("cursor-pointer text-slate-400 select-none"),
			g.Text(fmt.Sprintf("%d resource%s", n, plural(n))),
		),
		h.Ul(h.Class("mt-2 space-y-1"), g.Group(items)),
	)
}

// workflowDetailPage renders a single workflow: its pretty-printed graph (escaped
// — untrusted), resources, attachment controls, and metadata. runSection is the
// live Run panel (nil renders no run controls).
func workflowDetailPage(wf *store.Workflow, prettyGraph, csrf, theme, nsfwMode string, runSection g.Node) g.Node {
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

	// Run panel (local ComfyUI execution).
	if runSection != nil {
		body = append(body, runSection)
	}

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

	// Graph card — a server-rendered SVG for UI-format graphs (litegraph
	// coordinates), a structured node listing for API-format / unrenderable graphs,
	// plus a collapsible raw-JSON view. All content escaped (untrusted graph).
	body = append(body, card(
		sectionTitle("Graph"),
		workflowGraphSection([]byte(wf.Graph), wf.Format),
		h.Details(h.Class("mt-3 text-sm"),
			h.Summary(h.Class("cursor-pointer text-slate-400 select-none"), g.Text("View raw JSON")),
			h.Pre(
				h.Class("overflow-x-auto text-xs text-slate-300 bg-slate-900 rounded p-3 max-h-96 mt-2"),
				h.Code(g.Text(prettyGraph)),
			),
		),
	))

	return page(name+" · Workflow", theme, csrf, nsfwMode, body...)
}

// workflowGraphSection picks the best graph rendering: an SVG for a UI-format
// (litegraph) graph that carries coordinates, else a structured node listing (also
// the fallback when a UI graph cannot be laid out).
func workflowGraphSection(graph []byte, format string) g.Node {
	if format == store.WorkflowFormatUI {
		if svg, ok := workflowGraphSVG(graph); ok {
			return svg
		}
	}
	return workflowGraphStructured(graph, format)
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
