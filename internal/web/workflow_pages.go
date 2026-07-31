package web

import (
	"fmt"
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
	// localResource resolves a referenced resource's basename to the matched local
	// file's absolute path + civitai linkage (see resourceInfo). ok=false when the
	// basename is unknown OR ambiguous — the chip then renders without a path and
	// without a source link rather than guessing.
	localResource func(basename string) (resourceInfo, bool)
	// nsfwMode is the persisted NSFW display mode (hide|blur|show) threaded to the
	// list-item showcase carousels so they honor it (carried on the resolver to
	// avoid threading it through workflowList/Item/Card + the scan-terminal path).
	nsfwMode string
	// csrf is the server's CSRF token, needed by the resource chip's "open folder"
	// POST. Carried here for the same reason nsfwMode is: the chip renderer is
	// reached from four call sites (detail card, list popover, scan terminal,
	// showcase) and threading a token through all of them would be noise.
	csrf string
	// openFolder reports whether the "open containing folder" control may be
	// offered at all. It mirrors the endpoint's own loopback gate — the server
	// execs a process on the machine running `serve`, so the affordance must not
	// even appear on a non-loopback bind.
	openFolder bool
}

// showcase returns the linked model's showcase gallery (from the LOCAL model_cache
// raw — never a civitai fetch), capped like a search card. Nil when the model is
// uncached or carries no inline images (the card then renders a placeholder).
func (r workflowResolver) showcase(modelID int) []galleryImage {
	if r.cachedModel == nil {
		return nil
	}
	_, raw, ok := r.cachedModel(modelID)
	if !ok || len(raw) == 0 {
		return nil
	}
	return cardCarouselImages(raw)
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
	// Workflows is the list to RENDER — already narrowed by Facets. Filtering
	// happens in the handler (which owns the classification) so every render path
	// shows the same thing.
	Workflows   []store.Workflow
	Flash       string
	FlashLevel  string
	ScanInitial g.Node
	// Resolver resolves list-item model/version names + local-file presence from
	// local state (built by the handler, which has store access).
	Resolver workflowResolver
	// Facets is the normalized browse-by selection (?eco=/?use=/?model=), and Counts
	// is the per-bucket population of the SCOPE (the whole library, or — when a
	// source-post filter is active — that post's workflows) rather than of the
	// filtered list, so a chip's count stays meaningful while a filter is applied.
	Facets libraryWorkflowFacets
	Counts workflowFacetCounts
	// SourceModelName is the cached CivitAI name of the ?model= post ("" when the
	// model was never cached — the header then names the id only, and never invents
	// a title).
	SourceModelName string
}

// sourcePostHeader names the source post a ?model= filter is scoped to, links back
// to it on civitai.com, and offers a one-click escape to the full library. It is
// rendered ONLY while the filter is active.
//
// It exists because importing a CivitAI Workflows model routinely yields MANY
// workflows (22 for one post in a real library); landing on an unfiltered list
// after such an import makes the import look like it did nothing in particular.
//
// The model NAME is untrusted CivitAI text (escaped via g.Text); the href is built
// from the numeric id only, so nothing from the request reaches the URL.
func sourcePostHeader(modelID int, name string, shown int) g.Node {
	ids := strconv.Itoa(modelID)
	title := "Workflows from CivitAI model " + ids
	if strings.TrimSpace(name) != "" {
		title = name
	}
	count := strconv.Itoa(shown) + " workflow"
	if shown != 1 {
		count += "s"
	}
	return h.Div(
		h.Class("rounded border border-slate-800 p-3 flex flex-wrap items-center justify-between gap-3"),
		h.Div(
			h.Div(h.Class("text-sm font-semibold text-slate-100"), g.Text(title)),
			h.Div(h.Class("text-xs text-slate-400"),
				g.Text(count+" imported from this CivitAI post")),
		),
		h.Div(h.Class("flex flex-wrap items-center gap-3"),
			h.A(
				h.Href("https://civitai.com/models/"+ids),
				h.Target("_blank"), g.Attr("rel", "noopener noreferrer"),
				h.Class("text-xs text-indigo-400 hover:underline"),
				g.Text("View on CivitAI ↗"),
			),
			h.A(
				h.Href("/library?tab=workflows"),
				h.Class("text-xs text-indigo-400 hover:underline"),
				g.Text("Show all workflows"),
			),
		),
	)
}

// workflowsPanel is the "Workflows" Library tab: an import panel (paste JSON /
// upload PNG) above the workflow-scan control + stored-workflow list. It mirrors
// filesPanel: the scan control and list live inside the STABLE
// #workflow-scan-results container, so a scan status swap hides the control while
// scanning and restores it (with the refreshed list) when settled. extraAllowed
// reflects the loopback gate — import + scanning are disabled off-loopback,
// matching the egress posture for endpoints that ingest/scan arbitrary content.
func workflowsPanel(lw libraryWorkflowsView, csrf string, extraAllowed bool) g.Node {
	scanInitial := lw.ScanInitial
	if scanInitial == nil {
		// Idle: the scan form card above the current list (rebuilt on each status swap).
		scanInitial = workflowScanTerminal(lw.Workflows, workflowScanSnapshot{}, csrf, extraAllowed, lw.Resolver)
	}
	// CONTROLS (head): the source-post scope header, then the browse-by chips. Both
	// sit ABOVE the stable scan container so a scan-status innerHTML swap never
	// re-renders (or orphans) them — unchanged; they simply live in the surface's
	// head now instead of floating between two cards.
	var controls []g.Node
	if lw.Facets.Model > 0 {
		controls = append(controls, sourcePostHeader(lw.Facets.Model, lw.SourceModelName, lw.Counts.Total))
	}
	if lw.Counts.Total > 0 {
		controls = append(controls, workflowFacetBar(lw.Counts, lw.Facets))
	}

	// RESULTS: the scan control + workflow list, or — for a filter that matched
	// nothing — the guided empty state instead (without it the panel would render the
	// generic "No workflows yet" and look like data loss).
	//
	// The surface's results container IS the existing stable #workflow-scan-results
	// element, so the re-arming workflow-scan poller keeps its exact target and can
	// never orphan its #workflow-scan-poll. Nothing about the swap contract moved.
	results := scanInitial
	noneMatched := (lw.Facets.Model > 0 && lw.Counts.Total == 0) ||
		(lw.Facets.any() && len(lw.Workflows) == 0 && lw.Counts.Total > 0)
	if noneMatched {
		results = workflowFacetEmptyLocal(lw.Facets)
	}

	var notice g.Node
	if lw.Flash != "" {
		level := lw.FlashLevel
		if level == "" {
			level = "info"
		}
		notice = alert(level, "", g.Text(lw.Flash))
	}

	return browseSurface(browseSurfaceSpec{
		// No Title: the Library page already owns the single <h1> above the tab strip.
		Blurb: "Locally-curated ComfyUI workflows. Import an API/UI graph, extract one from a ComfyUI PNG, " +
			"or scan your ComfyUI installs to index and auto-link saved workflows.",
		Aside:     workflowImportTrigger(extraAllowed),
		Controls:  g.Group(controls),
		Notice:    notice,
		ResultsID: workflowScanResultsID,
		Results:   results,
		Foot:      []g.Node{workflowImportDialog(csrf, extraAllowed)},
	})
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
// workflowImportTrigger is the "Add a workflow" action alone — it rides on the
// browse surface's head row rather than in a card of its own, so the tab reads as
// one surface (title/blurb + actions + filters, then the workflows). The <dialog>
// it opens is emitted separately by workflowImportDialog.
func workflowImportTrigger(extraAllowed bool) g.Node {
	if !extraAllowed {
		return alert("warning", "",
			g.Text("Workflow import is disabled when the server is bound to a non-loopback address."))
	}
	return civButton("filled", "md", []g.Node{
		h.Type("button"),
		// Inline open — no external script. showModal() gives the native top-layer
		// modal + backdrop; the dialog id is a constant, not user input.
		g.Attr("onclick", "document.getElementById('"+workflowImportDialogID+"').showModal()"),
		g.Attr("aria-label", "Add a workflow — paste JSON or upload a ComfyUI PNG"),
	},
		h.Span(h.Class("cm-cta-icon"), g.Attr("aria-hidden", "true"), g.Text("＋ ")),
		g.Text("Add a workflow"),
	)
}

// workflowImportDialog is the native <dialog> holding the paste-JSON and
// upload-PNG import forms. The forms, their endpoints, CSRF and loopback gate are
// unchanged — only the surrounding chrome moved. It renders nothing when import is
// gated off (the trigger already says so).
func workflowImportDialog(csrf string, extraAllowed bool) g.Node {
	if !extraAllowed {
		return nil
	}
	return h.Dialog(
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
}

// workflowListID is the STABLE container the client-side sort/filter/group script
// reorders (and inserts group headers into). It is nested INSIDE the swapped
// #workflow-scan-results container, so the whole controls-bar + list + script
// fragment is re-emitted (and the script re-runs, re-binding idempotently) after
// every workflow-scan status swap — see workflowControlsScript.
const workflowListID = "cm-wf-list"

// workflowSortOptions drives the client-side sort <select>. "Imported" uses each
// workflow's CreatedAt (data-created epoch); the default (first option) reproduces
// the current server order (newest imported first).
var workflowSortOptions = []selectOption{
	{Value: "created_desc", Label: "Imported (newest first)"},
	{Value: "created_asc", Label: "Imported (oldest first)"},
	{Value: "name_asc", Label: "Name A→Z"},
	{Value: "name_desc", Label: "Name Z→A"},
}

// workflowSourceFilterOptions narrows the list by store.Workflow.Source. Values are
// the raw source constants (matched against each card's data-source); labels are the
// user-facing wording ("Discovered" == the civitai-import source).
var workflowSourceFilterOptions = []selectOption{
	{Value: "", Label: "All sources"},
	{Value: store.WorkflowSourceImported, Label: "Imported"},
	{Value: store.WorkflowSourceCivitai, Label: "Discovered"},
	{Value: store.WorkflowSourceScanned, Label: "Scanned"},
	{Value: store.WorkflowSourceExtractedPNG, Label: "PNG"},
	{Value: store.WorkflowSourceAuthored, Label: "Authored"},
}

// workflowFormatFilterOptions narrows by store.Workflow.Format (raw values matched
// against data-format).
var workflowFormatFilterOptions = []selectOption{
	{Value: "", Label: "All formats"},
	{Value: store.WorkflowFormatAPI, Label: "Runnable API"},
	{Value: store.WorkflowFormatUI, Label: "UI"},
}

// workflowList renders the stored workflows as cards, or an empty state. When there
// are workflows it prepends a read-only controls bar (sort / text filter / source +
// format filter / group-by-base toggle) that a small INLINE script (no external
// asset — offline-safe) drives entirely client-side over per-card data-* attributes.
func workflowList(wfs []store.Workflow, csrf string, extraAllowed bool, resolver workflowResolver) g.Node {
	if len(wfs) == 0 {
		return workflowEmptyState(extraAllowed)
	}
	items := make([]g.Node, 0, len(wfs))
	for _, wf := range wfs {
		items = append(items, workflowListItem(wf, csrf, resolver))
	}
	return h.Div(
		h.Class("space-y-4"),
		workflowControlsBar(),
		h.Div(h.ID(workflowListID), h.Class("space-y-4"), g.Group(items)),
		workflowControlsScript(),
		workflowDeeplinkScript(),
		// The shared lightbox + carousel scripts the card showcase strips reuse
		// (the same ones the model-search / discover cards rely on). Included once
		// here in the swapped fragment (idempotent re-definition), so a card tile can
		// open the lightbox and the prev/next buttons scroll — offline/vendored only.
		lightboxOverlay(),
		modelPageScript(),
		libraryCarouselScript(),
	)
}

// workflowDeeplinkScript is the self-contained (no-CDN, offline-safe) "View in
// library" deep-link handler. On load — and after every htmx swap settles — it
// checks location.hash for a #wf-<id> anchor; when it matches a rendered item it
// scrolls it into view and briefly applies the .cm-wf-highlight pulse (a subtle
// accent ring that fades ~1.5s; reduced-motion → a static ring, honored entirely
// in CSS). It binds its htmx:afterSettle listener exactly ONCE (a window guard),
// so re-emitting this script after a scan-poller innerHTML swap never stacks
// duplicate listeners and never orphans the scan poller. NON-DOM caveat: the
// scroll/highlight behavior is markup-verified only (no browser here).
func workflowDeeplinkScript() g.Node {
	const js = `
function cmWfDeeplink(){
  var m = (window.location.hash || '').match(/^#wf-(\d+)$/);
  if(!m){ return; }
  var el = document.getElementById('wf-' + m[1]);
  if(!el){ return; }
  try { el.scrollIntoView({ behavior: 'smooth', block: 'center' }); }
  catch(e) { el.scrollIntoView(); }
  el.classList.remove('cm-wf-highlight');
  void el.offsetWidth; // reflow so re-adding the class restarts the animation
  el.classList.add('cm-wf-highlight');
  window.setTimeout(function(){ el.classList.remove('cm-wf-highlight'); }, 2000);
}
if(!window.__cmWfDeeplinkBound){
  window.__cmWfDeeplinkBound = true;
  document.body.addEventListener('htmx:afterSettle', cmWfDeeplink);
}
cmWfDeeplink();
`
	return h.Script(g.Raw(js))
}

// workflowEmptyState is the friendly, guided empty state shown when the library
// holds no workflows: a short heading + explainer and (on a loopback bind, where
// the import dialog exists) a single primary CTA that opens the SAME "Add a
// workflow" dialog. When import is gated off (non-loopback) the CTA is omitted —
// the panel's gating note already explains why — so the button can never open a
// dialog that was not rendered.
func workflowEmptyState(extraAllowed bool) g.Node {
	children := []g.Node{
		h.H3(h.Class("text-base font-semibold text-slate-200"), g.Text("No workflows yet")),
		h.P(h.Class("mx-auto mt-1 mb-3 max-w-md text-sm text-slate-400"),
			g.Text("Add a ComfyUI workflow to run it locally, organize it, and link it to your models — paste an API/UI graph, extract one from a PNG, or scan your installs.")),
	}
	if extraAllowed {
		children = append(children, civButton("filled", "md", []g.Node{
			h.Type("button"),
			g.Attr("onclick", "document.getElementById('"+workflowImportDialogID+"').showModal()"),
			g.Attr("aria-label", "Add your first workflow"),
		},
			h.Span(h.Class("cm-cta-icon"), g.Attr("aria-hidden", "true"), g.Text("＋ ")),
			g.Text("Add a workflow"),
		))
	}
	return card(h.Class("cm-lift py-6 text-center"), g.Group(children))
}

// workflowListItem wraps one workflowCard in a data-attributed container the
// client-side controls script filters, sorts, and groups. workflowCard itself is
// unchanged. data-name carries the (unescaped-by-g.Attr) display name; data-created
// is the CreatedAt epoch (sortable); data-base is the base model (empty → grouped
// under "Unspecified").
func workflowListItem(wf store.Workflow, csrf string, resolver workflowResolver) g.Node {
	name := wf.Name
	if strings.TrimSpace(name) == "" {
		name = "workflow #" + strconv.FormatInt(wf.ID, 10)
	}
	return h.Div(
		// Stable anchor for the "View in library" deep-link (#wf-<id>): the
		// workflows-tab deeplink script scrolls to + briefly highlights this node.
		h.ID("wf-"+strconv.FormatInt(wf.ID, 10)),
		h.Class("cm-wf-item"),
		dataAttr("name", name),
		dataAttr("source", wf.Source),
		dataAttr("format", wf.Format),
		dataAttr("base", strings.TrimSpace(wf.BaseModel)),
		dataAttr("created", strconv.FormatInt(wf.CreatedAt.Unix(), 10)),
		workflowCard(wf, csrf, resolver),
	)
}

// wfFilterSelect renders a theme-aware civitai-ui labeled <select> that calls the
// client-side controls script on change. No form name — the selects are read-only UI
// state, never submitted.
func wfFilterSelect(id, label string, opts []selectOption) g.Node {
	optNodes := make([]g.Node, 0, len(opts))
	for _, o := range opts {
		optNodes = append(optNodes, h.Option(h.Value(o.Value), g.Text(o.Label)))
	}
	return h.Div(
		dataAttr("civitai-ui", "text-input"),
		h.Label(dataFlag("civitai-ui-label"), h.For(id), g.Text(label)),
		h.Select(append([]g.Node{
			dataFlag("civitai-ui-control"), h.ID(id),
			g.Attr("onchange", "cmWfApply()"),
		}, optNodes...)...),
	)
}

// workflowControlsBar is the sort/filter/group controls card above the workflow
// list. Every control fires cmWfApply() (inline handler → globally (re)defined fn),
// so it survives htmx swaps with no duplicate listeners (mirrors librarySortScript).
func workflowControlsBar() g.Node {
	return card(
		h.Div(
			h.Class("flex flex-wrap items-end gap-3"),
			h.Div(
				h.Class("min-w-[12rem] flex-1"),
				textInput("text-input", "cm-wf-q", "Filter",
					h.Type("text"), h.Placeholder("Filter by name or base model…"),
					g.Attr("oninput", "cmWfApply()")),
			),
			wfFilterSelect("cm-wf-sort", "Sort", workflowSortOptions),
			wfFilterSelect("cm-wf-source", "Source", workflowSourceFilterOptions),
			wfFilterSelect("cm-wf-format", "Format", workflowFormatFilterOptions),
			h.Label(
				h.Class("flex items-center gap-2 text-sm text-slate-300"),
				h.Input(h.Type("checkbox"), h.ID("cm-wf-group"),
					g.Attr("onchange", "cmWfApply()")),
				g.Text("Group by base model"),
			),
		),
		h.P(h.Class("mt-2 text-xs text-slate-500"), h.Span(h.ID("cm-wf-count"))),
	)
}

// workflowControlsScript is the self-contained, vendored (no-CDN) client-side
// sort/filter/group engine for the workflow list. It (re)defines cmWfApply()
// idempotently so it survives every htmx swap of the #workflow-scan-results
// container, then runs it once so the default sort/order applies on (re)render. It
// attaches NO listeners (the controls use inline oninput/onchange), so re-running
// after a swap can never duplicate handlers or orphan the scan poller.
func workflowControlsScript() g.Node {
	const js = `
function cmWfApply(){
  var list = document.getElementById('cm-wf-list');
  if(!list){ return; }
  function val(id){ var el = document.getElementById(id); return el ? el.value : ''; }
  var q = (val('cm-wf-q') || '').trim().toLowerCase();
  var srcSel = val('cm-wf-source');
  var fmtSel = val('cm-wf-format');
  var sortSel = val('cm-wf-sort') || 'created_desc';
  var groupEl = document.getElementById('cm-wf-group');
  var group = !!(groupEl && groupEl.checked);

  Array.prototype.slice.call(list.querySelectorAll('.cm-wf-group-header')).forEach(function(h){ h.remove(); });

  var items = Array.prototype.slice.call(list.querySelectorAll('.cm-wf-item'));
  var shown = 0;
  items.forEach(function(it){
    var name = it.getAttribute('data-name') || '';
    var base = it.getAttribute('data-base') || '';
    var hay = (name + ' ' + base).toLowerCase();
    var ok = true;
    if(q && hay.indexOf(q) < 0){ ok = false; }
    if(ok && srcSel && it.getAttribute('data-source') !== srcSel){ ok = false; }
    if(ok && fmtSel && it.getAttribute('data-format') !== fmtSel){ ok = false; }
    it.style.display = ok ? '' : 'none';
    if(ok){ shown++; }
  });

  function nameOf(it){ return (it.getAttribute('data-name') || '').toLowerCase(); }
  function createdOf(it){ return parseInt(it.getAttribute('data-created') || '0', 10) || 0; }
  function baseOf(it){ return (it.getAttribute('data-base') || '').trim(); }
  function cmp(a, b){
    switch(sortSel){
      case 'created_asc': return createdOf(a) - createdOf(b);
      case 'name_asc': { var x=nameOf(a), y=nameOf(b); return x<y?-1:(x>y?1:0); }
      case 'name_desc': { var x=nameOf(a), y=nameOf(b); return x<y?1:(x>y?-1:0); }
      default: return createdOf(b) - createdOf(a);
    }
  }

  var visible = items.filter(function(it){ return it.style.display !== 'none'; });
  if(group){
    visible.sort(function(a, b){
      var ba = baseOf(a), bb = baseOf(b);
      var ea = ba === '' ? 1 : 0, eb = bb === '' ? 1 : 0;
      if(ea !== eb){ return ea - eb; }
      var la = ba.toLowerCase(), lb = bb.toLowerCase();
      if(la < lb){ return -1; }
      if(la > lb){ return 1; }
      return cmp(a, b);
    });
    var lastBase = null;
    visible.forEach(function(it){
      var b = baseOf(it) || 'Unspecified';
      if(b !== lastBase){
        var hdr = document.createElement('div');
        hdr.className = 'cm-wf-group-header';
        hdr.textContent = b;
        list.appendChild(hdr);
        lastBase = b;
      }
      list.appendChild(it);
    });
  } else {
    visible.sort(cmp);
    visible.forEach(function(it){ list.appendChild(it); });
  }

  var countEl = document.getElementById('cm-wf-count');
  if(countEl){ countEl.textContent = shown + ' of ' + items.length + ' shown'; }
}
cmWfApply();
`
	return h.Script(g.Raw(js))
}

// workflowFormatBadge renders the format badge with a friendly label: an API
// graph is "Runnable API" (green — it can be submitted to ComfyUI), a UI graph is
// "UI" (neutral). Shared by the list card and elsewhere.
func workflowFormatBadge(format string) g.Node {
	if format == store.WorkflowFormatAPI {
		return badge("Runnable API", "green")
	}
	return badge("UI", "slate")
}

// workflowCardShowcase renders the list card's CivitAI showcase strip, REUSING the
// same NSFW-aware carousel the model-search / Discover-workflows cards use
// (modelCardCarousel). The images come from the workflow's linked model's LOCAL
// model_cache raw (never a fetch). When there is no linked model or no cached
// showcase it degrades to a tasteful placeholder — never a broken image.
func workflowCardShowcase(wf store.Workflow, resolver workflowResolver) g.Node {
	if wf.ModelID != nil {
		if imgs := resolver.showcase(*wf.ModelID); len(imgs) > 0 {
			return modelCardCarousel(*wf.ModelID, imgs, resolver.nsfwMode)
		}
	}
	return h.Div(h.Class("cm-wf-noimg"), g.Text("No preview"))
}

// workflowRunDeepLink is the library list item's primary CTA target: the detail
// page's combined Generate section. The fragment drives a pure-CSS `:target`
// highlight on the section's primary button (.cm-generate / .cm-generate-cta in
// app.css) — no JS, and <html>'s scroll-padding-top keeps the sticky nav from
// covering it.
func workflowRunDeepLink(id string) string {
	return "/workflows/" + id + "#" + runGenerateSectionID
}

// workflowCard renders one stored workflow as a richer, scannable card.
//
// LAYOUT (reorganized in PR C1): the showcase strip, then a header row holding the
// name + its badges on the left and the PRIMARY "Run" CTA on the right, then a
// footer row of secondary actions. The old card gave Run, View, the golden toggle
// and Delete the same visual weight in one undifferentiated row, so the thing a
// user comes to a workflow to do was indistinguishable from destroying it.
//
// The sort/filter data-* attributes and the #wf-<id> anchor live on the wrapping
// .cm-wf-item (workflowListItem), so this leaves the client-side controls +
// deep-link untouched.
func workflowCard(wf store.Workflow, csrf string, resolver workflowResolver) g.Node {
	return workflowCardWith(wf, csrf, resolver, false)
}

// workflowCardCompact is the SAME card renderer in its compact variant — a
// VARIANT of the shared component, deliberately not a second card. Every
// surface that shows a workflow as a card goes through workflowCardWith, so the
// two can never drift in naming, badge vocabulary or the Run deep link.
//
// It exists for the imported-workflows carousel on a Workflows-type model detail
// page (see importedWorkflowsCarousel), where three of the full card's parts are
// actively WRONG rather than merely large:
//
//   - The showcase strip is dropped. Every workflow in that carousel was
//     imported from the model whose page this is, so the strip would repeat, once
//     per card, the exact images already shown full-size in the showcase card
//     above — and it would nest a scroll-snap image carousel (with its own
//     prev/next buttons, NSFW reveal overlays and video badges, i.e. the whole
//     z-4/5/10/20 escaping-decoration family) inside another horizontal strip.
//     Dropping it removes that stacking hazard by construction, not by z-index.
//   - The model-linkage / version meta and the resources popover are dropped.
//     "from <model>" is tautological on that model's own page, and both need a
//     workflowResolver — which is exactly the per-card cached-model lookup this
//     surface must not pay for. The popover is also the documented .cm-lift
//     stacking trap, so it stays out of a horizontally scrolling strip.
//   - The secondary action row is dropped, Delete above all: a browse strip on
//     a model page is not where a destructive, CSRF-bearing library mutation
//     belongs. That is why this variant takes NO csrf token — it cannot render a
//     state-changing control even by accident.
//
// What remains is what the carousel is for: the name (linking to the workflow),
// the identity badges, and the primary Run CTA.
func workflowCardCompact(wf store.Workflow) g.Node {
	return workflowCardWith(wf, "", workflowResolver{}, true)
}

// workflowCardWith is the one renderer behind workflowCard and
// workflowCardCompact. See workflowCardCompact for what `compact` drops and why.
func workflowCardWith(wf store.Workflow, csrf string, resolver workflowResolver, compact bool) g.Node {
	id := strconv.FormatInt(wf.ID, 10)

	name := wf.Name
	if strings.TrimSpace(name) == "" {
		name = "workflow #" + id
	}

	var meta []g.Node
	meta = append(meta, workflowFormatBadge(wf.Format))
	if b := strings.TrimSpace(wf.BaseModel); b != "" {
		meta = append(meta, badge(b, "blue"))
	}
	meta = append(meta, badge(optionLabel(workflowSourceFilterOptions, wf.Source), "slate"))
	if wf.IsGolden {
		meta = append(meta, badge("golden ✓", "amber"))
	}
	// Model linkage: the RESOLVED name as text — instantly from model_cache when
	// present, else lazy-loaded via the existing /models/{id}/title endpoint
	// (cache-first, one civitai fetch when uncached). Navigating to the post is the
	// "View post" button's job now, so this is no longer a bare link.
	if wf.ModelID != nil && !compact {
		meta = append(meta, h.Span(h.Class("text-xs text-slate-400"),
			g.Text("from "), workflowModelNameText(*wf.ModelID, resolver)))
	}
	// Version: the resolved version name (parsed from the cached model's raw
	// detail) when available, else the bare "version {id}" fallback.
	if wf.VersionID != nil && !compact {
		label := "version " + strconv.Itoa(*wf.VersionID)
		if wf.ModelID != nil {
			if vn, ok := resolver.versionName(*wf.ModelID, *wf.VersionID); ok {
				label = vn
			}
		}
		meta = append(meta, badge(label, "slate"))
	}
	// Referenced resources: a hover/click popover of chips, reusing the page's ONE
	// popover mechanism (see workflowResourcesPopover) instead of the old inline
	// <details>, which pushed every following card down when opened.
	if len(wf.Resources) > 0 && !compact {
		meta = append(meta, workflowResourcesPopover(wf.Resources, resolver))
		// The reverse link-back, in its compact list form. It reads "uses <model>" —
		// never "from <model>", which two entries above means the workflow was
		// IMPORTED from that model. Nil when no resource resolved to a local model.
		if uses := workflowUsesChips(wf, resolver); uses != nil {
			meta = append(meta, uses)
		}
	}

	// PRIMARY CTA — Run. It is an anchor styled as a button (not a <button> inside
	// an <a>, which is invalid) deep-linking to the detail page's Generate section,
	// which hosts the live run panel against the local ComfyUI.
	ctaSize := "md"
	if compact {
		ctaSize = "sm"
	}
	runCTA := h.A(
		h.Href(workflowRunDeepLink(id)),
		dataAttr("civitai-ui", "button"), dataAttr("variant", "filled"), dataAttr("size", ctaSize),
		g.Attr("title", "Run on your local ComfyUI"),
		g.Attr("aria-label", "Run "+name+" on your local ComfyUI"),
		h.Span(h.Class("cm-cta-icon"), g.Attr("aria-hidden", "true"), g.Text("▶ ")),
		g.Text("Run"),
	)

	if compact {
		// Compact layout: name, badges, then the Run CTA on its own row. The full
		// card's side-by-side header would leave the name ~8rem inside a
		// fixed-width carousel card.
		//
		// The name is `truncate min-w-0` (the pairing TestLongUntrustedStringsCanBreak
		// requires) with the full name in title=: a card in a fixed-width horizontal
		// strip has no room to wrap an arbitrary 90-char workflow name, and letting it
		// wrap would grow the strip's row height for every card.
		return card(
			h.Class("cm-lift"),
			h.A(h.Href("/workflows/"+id),
				h.Class("block truncate min-w-0 text-base font-semibold text-slate-100 hover:text-indigo-300"),
				g.Attr("title", name),
				g.Text(name)),
			h.Div(h.Class("flex flex-wrap items-center gap-2 mt-2"), g.Group(meta)),
			h.Div(h.Class("mt-3"), runCTA),
		)
	}

	// Secondary actions, lowest emphasis last.
	var actions []g.Node
	actions = append(actions, h.A(h.Href("/workflows/"+id),
		dataAttr("civitai-ui", "button"), dataAttr("variant", "outline"), dataAttr("size", "sm"),
		g.Text("View")))
	if wf.ModelID != nil {
		actions = append(actions, viewPostButton(*wf.ModelID))
	}
	// Golden toggle: only meaningful when attached to a version.
	if wf.VersionID != nil {
		if wf.IsGolden {
			actions = append(actions, postButton("/workflows/"+id+"/golden", csrf,
				map[string]string{"action": "unset"}, "subtle", "Unset golden"))
		} else {
			actions = append(actions, postButton("/workflows/"+id+"/golden", csrf,
				map[string]string{"action": "set"}, "subtle", "Set golden"))
		}
	}
	actions = append(actions, postButton("/workflows/"+id+"/delete", csrf, nil, "subtle", "Delete"))

	return card(
		h.Class("cm-lift"),
		// CivitAI showcase strip (NSFW-aware) — reused from the discover cards; a
		// placeholder renders when the workflow has no linked model/showcase.
		h.Div(h.Class("mb-3"), workflowCardShowcase(wf, resolver)),
		h.Div(h.Class("flex items-start justify-between gap-4"),
			h.Div(h.Class("min-w-0"),
				h.A(h.Href("/workflows/"+id),
					h.Class("text-lg font-semibold text-slate-100 hover:text-indigo-300"),
					g.Text(name)),
				h.Div(h.Class("flex flex-wrap items-center gap-2 mt-2"), g.Group(meta)),
			),
			h.Div(h.Class("shrink-0"), runCTA),
		),
		h.Div(h.Class("flex flex-wrap items-center gap-2 mt-4"), g.Group(actions)),
	)
}

// viewPostButton is THE control for "go to the CivitAI post this came from",
// replacing the raw /models/<id> text link the list item and the detail page each
// used to render. The href is built from the numeric model id only — nothing from
// the request reaches the URL — and it points at the IN-APP model page (not
// civitai.com), which is where the version tabs, downloads and showcase live.
func viewPostButton(modelID int) g.Node {
	return h.A(
		h.Href("/models/"+strconv.Itoa(modelID)),
		dataAttr("civitai-ui", "button"), dataAttr("variant", "outline"), dataAttr("size", "sm"),
		g.Attr("title", "Open the CivitAI post this workflow came from"),
		g.Text("View post"),
	)
}

// workflowSourceLinks renders the workflow's PROVENANCE row at the top of the
// detail "Details" card: the source chip (Imported / Discovered / Scanned / PNG /
// Authored), the resolved model name, and — when the workflow is model-linked — the
// "View post" button plus an EXTERNAL "View on CivitAI ↗" to
// civitai.com/models/<id>[?modelVersionId=<vid>] (scheme/host-validated, escaped,
// rel=noopener, new tab). The raw in-app /models/<id> text link it used to render
// is now the same "View post" button the library list item uses.
//
// Untrusted values (model name) route through g.Text.
func workflowSourceLinks(wf *store.Workflow, resolver workflowResolver) g.Node {
	row := []g.Node{
		h.Span(h.Class("text-xs text-slate-500"), g.Text("Source")),
		badge(optionLabel(workflowSourceFilterOptions, wf.Source), "slate"),
	}

	if wf.ModelID != nil {
		row = append(row, h.Span(h.Class("text-xs text-slate-400"),
			g.Text("from "), workflowModelNameText(*wf.ModelID, resolver)))
		row = append(row, viewPostButton(*wf.ModelID))

		modelURL := fmt.Sprintf("https://civitai.com/models/%d", *wf.ModelID)
		if wf.VersionID != nil {
			modelURL += fmt.Sprintf("?modelVersionId=%d", *wf.VersionID)
		}
		// The URL is built from integers (never user text), but validate the scheme/
		// host anyway before it becomes an href (defense in depth, mirrors apps).
		if isSafeHTTPURL(modelURL) {
			row = append(row, h.A(
				h.Href(modelURL), h.Target("_blank"), g.Attr("rel", "noopener"),
				h.Class("text-sm text-indigo-400 hover:text-indigo-300"),
				g.Text("View on CivitAI ↗"),
			))
		}
	}

	return h.Div(h.Class("flex flex-wrap items-center gap-3"), g.Group(row))
}

// workflowDetailsReveal is the collapsed-by-default disclosure holding everything
// about a workflow that is NOT needed to run it: the raw format/source strings, the
// attached model + version ids, the golden flag, and a scanned workflow's on-disk
// path.
//
// The card used to render all of it as an always-visible <dl> above the run
// controls, so the page opened on six rows of ids before showing the thing the user
// came for. Rendered as a native <details> (no JS, keyboard-operable, announced as
// a disclosure by AT); the open motion is CSS in .cm-meta-reveal and is disabled
// under prefers-reduced-motion. The path is an arbitrary filesystem string —
// escaped via metaRow's g.Text.
func workflowDetailsReveal(wf *store.Workflow) g.Node {
	rows := []g.Node{
		metaRow("Format", wf.Format),
		metaRow("Source", wf.Source),
	}
	if wf.BaseModel != "" {
		rows = append(rows, metaRow("Base model", wf.BaseModel))
	}
	if wf.ModelID != nil {
		rows = append(rows, metaRow("Attached model", strconv.Itoa(*wf.ModelID)))
	}
	if wf.VersionID != nil {
		rows = append(rows, metaRow("Attached version", strconv.Itoa(*wf.VersionID)))
	}
	rows = append(rows, metaRow("Golden", boolText(wf.IsGolden)))
	if p := strings.TrimSpace(wf.SourcePath); p != "" {
		rows = append(rows, metaRow("On disk", p))
	}
	return h.Details(
		h.Class("cm-meta-reveal mt-3"),
		h.Summary(
			h.Class("cm-meta-summary"),
			h.Span(h.Class("cm-meta-chevron"), g.Attr("aria-hidden", "true"), g.Text("›")),
			g.Text("Workflow metadata"),
		),
		h.Div(h.Class("cm-meta-body"), h.Dl(h.Class("space-y-1"), g.Group(rows))),
	)
}

// workflowAttachReveal folds the "Attach to a civitai version" form into the same
// collapsed idiom. The endpoint, its CSRF token and its detach semantics are
// UNCHANGED — only the chrome moved, because re-attaching a workflow is a rare,
// deliberate act that does not deserve a permanent card of its own.
func workflowAttachReveal(wf *store.Workflow, csrf string) g.Node {
	id := strconv.FormatInt(wf.ID, 10)
	return h.Details(
		h.Class("cm-meta-reveal mt-3"),
		h.Summary(
			h.Class("cm-meta-summary"),
			h.Span(h.Class("cm-meta-chevron"), g.Attr("aria-hidden", "true"), g.Text("›")),
			g.Text("Attach to a civitai version"),
		),
		h.Div(h.Class("cm-meta-body"),
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
		),
	)
}

// workflowModelNameText renders the linked model's resolved NAME as escaped text.
// When the name is cached it renders inline; otherwise the span lazy-loads it
// (hx-get=/models/{id}/title, hx-trigger=load — the endpoint is cache-first and
// fetches civitai only on a cache miss), showing a "model #id" placeholder until
// the swap.
//
// It deliberately returns TEXT, not a link: navigating to the post is the "View
// post" button's job (viewPostButton), so the same affordance is not duplicated as
// an easy-to-miss inline link.
func workflowModelNameText(modelID int, resolver workflowResolver) g.Node {
	ids := strconv.Itoa(modelID)
	if nm, ok := resolver.modelName(modelID); ok {
		return h.Span(h.Class("text-slate-300"), g.Text(nm))
	}
	return h.Span(
		h.Class("text-slate-300"),
		hx("get", "/models/"+ids+"/title"),
		hx("trigger", "load"),
		g.Text("model #"+ids),
	)
}

// workflowDetailPage renders a single workflow.
//
// LAYOUT (reworked in PR C1) — top to bottom, in the order a user actually needs
// them: the title, the linked model's showcase, the ONE combined "Generate" section
// (generate), the referenced-resource chips, the graph preview, and last a
// "Details" card whose non-key facts and the attach form both live behind a
// collapsed disclosure.
//
// What is deliberately GONE: the "Open in ComfyUI" card (merged into Generate), the
// "Run on CivitAI Cloud" card (ditto), the always-visible metadata <dl>, the
// standalone attach card, and the raw-JSON dump — a pretty-printed copy of the
// graph the user already has as a file, which was the single largest thing on the
// page and told nobody anything the graph preview does not.
//
// `generate` is the combined run section (nil renders no run controls at all).
// `recent` is this workflow's most recent captured outputs (bounded by the handler);
// empty renders no strip at all.
func workflowDetailPage(wf *store.Workflow, csrf, theme, nsfwMode string, generate g.Node,
	recent []store.Generation, resolver workflowResolver, rail ...railData) g.Node {
	id := strconv.FormatInt(wf.ID, 10)
	name := wf.Name
	if strings.TrimSpace(name) == "" {
		name = "workflow #" + id
	}

	var body []g.Node
	body = append(body, h.Div(h.Class("flex items-center justify-between"),
		h.H1(h.Class("text-2xl font-semibold text-slate-100"), g.Text(name)),
		h.A(h.Href("/library?tab=workflows#wf-"+id), h.Class("text-sm text-indigo-400 hover:text-indigo-300"),
			g.Text("← Back to Workflows")),
	))

	// CivitAI showcase carousel for the linked model — REUSES the exact carousel the
	// model detail page uses (modelCardCarouselW + the shared lightbox), fed entirely
	// from the LOCAL model_cache (never a fetch). NSFW-aware via the reused component:
	// `show` reveals, `blur`/`hide` obscure behind .cm-blur (the card carousel
	// migrates `hide`→`blur` at this layer, matching the model detail showcase + the
	// workflow list cards). Rendered only when the workflow is model-linked AND that
	// model has cached images; otherwise nothing is emitted (no broken markup).
	//
	// The "Showcase images" caption is deliberately omitted here (showcaseCardUntitled):
	// on a workflow page the images are the only pictures present and sit directly
	// under the workflow's own <h1>, so the label was pure chrome.
	showcaseShown := false
	if wf.ModelID != nil {
		if imgs := resolver.showcase(*wf.ModelID); len(imgs) > 0 {
			body = append(body, showcaseCardUntitled(*wf.ModelID, imgs, nsfwMode))
			showcaseShown = true
		}
	}

	// The ONE Generate section (local ComfyUI + editor hand-off + cloud).
	if generate != nil {
		body = append(body, generate)
	}

	// What this workflow has actually MADE, directly under the controls that make
	// more of it. Nil (no card at all) for a workflow that has never produced output.
	body = append(body, workflowOutputsStrip(wf.ID, recent))

	// Referenced resources, as chips: have/missing at a glance, the absolute on-disk
	// path on hover, and a source link for anything matched to a CivitAI model or
	// downloaded from HuggingFace by this app.
	if len(wf.Resources) > 0 {
		kids := []g.Node{
			sectionTitle("Referenced resources"),
			workflowResourceChips(wf.Resources, resolver),
		}
		// The folder control execs a file manager on the SERVER. Say so once, here,
		// rather than only in a tooltip — someone driving this UI from a phone or
		// another desktop would otherwise click it and see nothing happen.
		if resolver.openFolder {
			kids = append(kids, h.P(h.Class("text-xs text-slate-400 mt-2"),
				g.Text("The folder button opens a file-manager window on the computer running civitai-manager.")))
		}
		body = append(body, card(kids...))
	}

	// Graph card — a server-rendered SVG for UI-format graphs (litegraph
	// coordinates), a structured node listing for API-format / unrenderable graphs.
	// All content escaped (untrusted graph). No raw-JSON dump.
	body = append(body, card(
		sectionTitle("Graph"),
		workflowGraphSection([]byte(wf.Graph), wf.Format),
	))

	// Details: the provenance row up front, everything else behind a disclosure.
	body = append(body, card(
		sectionTitle("Details"),
		workflowSourceLinks(wf, resolver),
		// The OTHER direction, deliberately adjacent to (and never merged with) the
		// provenance row above: "Source … from <model>" is where the workflow CAME
		// FROM, "Uses" is which models its files belong to. Nil when nothing resolved.
		workflowUsesRow(wf, resolver),
		workflowDetailsReveal(wf),
		workflowAttachReveal(wf, csrf),
	))

	// The shared full-size lightbox + carousel scripts the showcase tiles reuse
	// (the same ones the model/library pages rely on). Appended ONCE, only when the
	// showcase is present, so a tile can open the lightbox and prev/next can scroll —
	// offline/vendored only, no external asset. They stay CONDITIONAL because
	// modelPageScript's Escape handler dereferences #cm-lightbox without a nil guard,
	// so the script must never ship without the overlay (and a page with no images
	// should not carry a dangling overlay either).
	//
	// Consequence, stated rather than papered over: on a showcase-less detail page
	// the shared popover hover CONTROLLER is absent, so this page's popovers (the
	// ComfyUI reachability icon) open through the CSS :hover / :focus-within rules
	// alone — same open/close behaviour, minus the 200 ms close grace.
	if showcaseShown {
		body = append(body, lightboxOverlay(), modelPageScript(), libraryCarouselScript())
	}

	return page(name+" · Workflow", theme, csrf, nsfwMode, railOf(rail), body...)
}

// workflowGraphSection picks the best graph rendering: an SVG for a UI-format
// (litegraph) graph that carries coordinates, else a structured node listing (also
// the fallback when a UI graph cannot be laid out).
func workflowGraphSection(graph []byte, format string) g.Node {
	if format == store.WorkflowFormatUI {
		if svg, ok := workflowGraphSVG(graph); ok {
			// The caption states plainly what this static render does and does not
			// show, so the (expected) difference from the ComfyUI canvas does not read
			// as "the preview is showing a different workflow".
			//
			// The click-to-drag pan script rides WITH the SVG, so it is emitted only
			// where there IS a pannable canvas — the structured listing below is an
			// ordinary document and must not advertise a gesture it does not support.
			return h.Div(svg, graphPreviewCaption(), workflowGraphPanScript())
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

// metaRow is the shared label/value row of every detail <dl> (workflow details,
// generation run parameters, …).
//
// MOBILE: the label was a flat w-40 — 160px, ~49% of the ~326px content box a
// 390px phone leaves inside a card — and the value had neither min-w-0 nor a
// break opportunity. A flex item's default min-width:auto refuses to shrink below
// its content, so an unbreakable value (the worst caller is "Graph hash": a
// 64-char sha256, ~490px of unbreakable mono text) forced the row ~660px wide
// inside 326px and pushed the whole page into a horizontal scroll. w-28 until sm
// gives the value 48 more px, and break-all + min-w-0 let it actually wrap.
func metaRow(label, value string) g.Node {
	return h.Div(h.Class("flex gap-2 text-sm"),
		h.Dt(h.Class("text-slate-500 w-28 sm:w-40 shrink-0"), g.Text(label)),
		h.Dd(h.Class("text-slate-200 min-w-0 break-all"), g.Text(value)),
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
