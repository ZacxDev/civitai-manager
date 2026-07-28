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
	// nsfwMode is the persisted NSFW display mode (hide|blur|show) threaded to the
	// list-item showcase carousels so they honor it (carried on the resolver to
	// avoid threading it through workflowList/Item/Card + the scan-terminal path).
	nsfwMode string
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
		g.Attr("aria-label", "Add a workflow — paste JSON or upload a ComfyUI PNG"),
	},
		h.Span(h.Class("cm-cta-icon"), g.Attr("aria-hidden", "true"), g.Text("＋ ")),
		g.Text("Add a workflow"),
	)

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
		h.Class("cm-lift"),
		sectionTitle("Add a workflow"),
		h.P(h.Class("text-sm text-slate-400 mb-3"),
			g.Text("Paste an API/UI graph or extract one from a ComfyUI PNG. To index workflows already saved in your ComfyUI installs, use “Scan for workflows” below.")),
		trigger,
		dialog,
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

// workflowCard renders one stored workflow as a richer, scannable card: a CivitAI
// showcase strip (NSFW-aware, reused from the discover cards), the name, base-model
// + format badges, model/version linkage, and the action row. A subtle hover-lift
// (.cm-lift, reduced-motion-safe) is applied. The sort/filter data-* attributes
// and the #wf-<id> anchor live on the wrapping .cm-wf-item (workflowListItem), so
// this redesign leaves the client-side controls + deep-link untouched.
func workflowCard(wf store.Workflow, csrf string, resolver workflowResolver) g.Node {
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
		h.Class("cm-lift"),
		// CivitAI showcase strip (NSFW-aware) — reused from the discover cards; a
		// placeholder renders when the workflow has no linked model/showcase.
		h.Div(h.Class("mb-3"), workflowCardShowcase(wf, resolver)),
		h.Div(h.Class("flex items-start justify-between gap-4"),
			h.Div(
				h.A(h.Href("/workflows/"+id),
					h.Class("text-lg font-semibold text-slate-100 hover:text-indigo-300"),
					g.Text(name)),
				h.Div(h.Class("flex flex-wrap items-center gap-2 mt-2"), g.Group(meta)),
			),
		),
		h.Div(h.Class("flex flex-wrap items-center gap-2 mt-4"), g.Group(actions)),
	)
}

// workflowSourceLinks renders the workflow's PROVENANCE block at the top of the
// detail "Details" card: a source chip (Imported / Discovered / Scanned / PNG /
// Authored), the CivitAI links when the workflow is model-linked (an EXTERNAL
// "View on CivitAI ↗" to civitai.com/models/<id>[?modelVersionId=<vid>] — scheme/
// host-validated, escaped, rel=noopener, new tab — plus the in-app model link via
// workflowModelLink), and, for a SCANNED workflow, the on-disk source path
// (escaped). Untrusted values (model name, path) all route through g.Text.
func workflowSourceLinks(wf *store.Workflow, resolver workflowResolver) g.Node {
	var rows []g.Node

	// Provenance chip.
	rows = append(rows, h.Div(
		h.Class("flex items-center gap-2"),
		h.Span(h.Class("text-xs text-slate-500"), g.Text("Source")),
		badge(optionLabel(workflowSourceFilterOptions, wf.Source), "slate"),
	))

	// CivitAI provenance: external civitai.com link + the in-app model page link.
	if wf.ModelID != nil {
		modelURL := fmt.Sprintf("https://civitai.com/models/%d", *wf.ModelID)
		if wf.VersionID != nil {
			modelURL += fmt.Sprintf("?modelVersionId=%d", *wf.VersionID)
		}
		var links []g.Node
		// The URL is built from integers (never user text), but validate the scheme/
		// host anyway before it becomes an href (defense in depth, mirrors apps).
		if isSafeHTTPURL(modelURL) {
			links = append(links, h.A(
				h.Href(modelURL), h.Target("_blank"), g.Attr("rel", "noopener"),
				h.Class("text-sm text-indigo-400 hover:text-indigo-300"),
				g.Text("View on CivitAI ↗"),
			))
		}
		links = append(links, workflowModelLink(*wf.ModelID, resolver))
		rows = append(rows, h.Div(h.Class("flex flex-wrap items-center gap-3"), g.Group(links)))
	}

	// Scanned on-disk source path (escaped — arbitrary filesystem path).
	if strings.TrimSpace(wf.SourcePath) != "" {
		rows = append(rows, h.Div(
			h.Class("flex items-start gap-2"),
			h.Span(h.Class("shrink-0 text-xs text-slate-500"), g.Text("On disk")),
			h.Span(h.Class("font-mono text-xs text-slate-300 break-all"), g.Text(wf.SourcePath)),
		))
	}

	return h.Div(h.Class("mb-3 space-y-2"), g.Group(rows))
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
// helper is the ComfyUI helper's ON-DISK state, rendered into the "Open in
// ComfyUI" card's management disclosure. It is stat-only — no network probe ever
// runs on this render path.
func workflowDetailPage(wf *store.Workflow, prettyGraph, csrf, theme, nsfwMode string, runSection g.Node, comfyConfigured bool, helper comfyHelperView, resolver workflowResolver, rail ...railData) g.Node {
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

	// CivitAI showcase carousel for the linked model — REUSES the exact card the
	// model detail page uses (showcaseCard → modelCardCarouselW + the shared
	// lightbox), fed entirely from the LOCAL model_cache (never a fetch). NSFW-aware
	// via the reused component: `show` reveals, `blur`/`hide` obscure behind .cm-blur
	// (the card carousel migrates `hide`→`blur` at this layer, matching the model
	// detail showcase + the workflow list cards). Rendered only when the workflow is
	// model-linked AND that model has cached images; otherwise nothing is emitted (no
	// broken markup). The shared lightbox + carousel scripts are appended once, at the
	// end of the body, when it is shown.
	showcaseShown := false
	if wf.ModelID != nil {
		if imgs := resolver.showcase(*wf.ModelID); len(imgs) > 0 {
			body = append(body, showcaseCard(*wf.ModelID, imgs, nsfwMode))
			showcaseShown = true
		}
	}

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
	body = append(body, card(sectionTitle("Details"), workflowSourceLinks(wf, resolver), h.Dl(h.Class("space-y-1"), g.Group(meta))))

	// "Open in ComfyUI" (load into the editor) — SEPARATE from the run panel.
	// Shown only for UI-format graphs with a configured comfy_url: API graphs do
	// not load into the editor, and there is nothing to reach without comfy_url.
	if comfyConfigured && wf.Format == store.WorkflowFormatUI {
		body = append(body, workflowOpenComfyCard(wf.ID, csrf, helper))
	}

	// Run panel (local ComfyUI execution).
	if runSection != nil {
		body = append(body, runSection)
	}

	// Resources card.
	if len(wf.Resources) > 0 {
		items := make([]g.Node, 0, len(wf.Resources))
		for _, r := range wf.Resources {
			// break-all: a resource is an arbitrary, often long unbroken filename/path.
			items = append(items, h.Li(h.Class("text-sm text-slate-300 font-mono break-all"), g.Text(r)))
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

	// The shared full-size lightbox + carousel scripts the showcase tiles reuse
	// (the same ones the model/library pages rely on). Appended ONCE, only when the
	// showcase is present, so a tile can open the lightbox and prev/next can scroll —
	// offline/vendored only, no external asset.
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
			return h.Div(svg, graphPreviewCaption())
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
