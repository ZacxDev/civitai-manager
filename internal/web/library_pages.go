package web

import (
	"fmt"
	"sort"
	"strconv"

	"github.com/ZacxDev/civitai-manager/internal/library"
	"github.com/ZacxDev/civitai-manager/internal/store"
	g "maragu.dev/gomponents"
	h "maragu.dev/gomponents/html"
)

// spinnerGlyph is a small CSS-animated spinner used inside an htmx-indicator so
// a running request reads as active progress, not a hang.
func spinnerGlyph() g.Node {
	return h.Span(h.Class(
		"inline-block h-3 w-3 shrink-0 animate-spin rounded-full border-2 border-slate-500 border-t-transparent"))
}

// libraryView bundles the data the library page renders.
type libraryView struct {
	Files       []store.LocalFile
	Candidates  []store.LocalFile
	TotalBytes  int64
	Reclaimable int64
}

func buildLibraryView(files []store.LocalFile) libraryView {
	v := libraryView{}
	for _, f := range files {
		if f.Kind == store.LocalKindModel {
			v.Files = append(v.Files, f)
			v.TotalBytes += f.SizeBytes
		}
		if f.IsCandidate() {
			v.Candidates = append(v.Candidates, f)
			v.Reclaimable += f.SizeBytes
		}
	}
	return v
}

// libraryPage is the full Library page, split into two tabs (finding/selecting
// install dirs vs. scanning them for model files). allowExtra gates the arbitrary
// extra-scan-path capability (discovery, the directory browser, manual add, and
// the persisted selection): it is available only on a loopback bind (see
// Server.extraPathsAllowed), so a network-exposed server never offers the remote
// arbitrary-path walk control. selectedDirs pre-fills the persisted selection.
//
// activeTab ("sources"|"files"; default "sources") is server-rendered from ?tab=
// so the active tab is robust across every htmx swap within a panel. Only the
// active panel is rendered, so its htmx targets exist only while it is shown.
// discoverInitial is the initial content of the stable #discover-results
// container (idle controls, or the live scanning/terminal fragment when a crawl
// is in flight); nil falls back to the idle controls.
func libraryPage(v libraryView, csrf string, allowExtra bool, selectedDirs []string, theme, activeTab string, discoverInitial g.Node, matchRemote bool, scanInitial g.Node) g.Node {
	if activeTab != "files" {
		activeTab = "sources"
	}
	var panel g.Node
	if activeTab == "files" {
		panel = filesPanel(v, csrf, allowExtra, selectedDirs, matchRemote, scanInitial)
	} else {
		panel = sourcesPanel(csrf, allowExtra, selectedDirs, discoverInitial)
	}
	return page("Library", theme, csrf,
		h.Div(
			sectionTitle("Library"),
			libraryTabStrip(activeTab),
		),
		h.Div(h.ID("tab-panel"), panel),
	)
}

// libraryTabStrip renders the two-tab navigation as an UNDERLINE tab strip (not
// buttons): a horizontal row of plain text links where the active tab carries an
// accent-colored underline and inactive tabs are muted. The tabs stay real
// full-page navigation links (?tab=…) — only the styling changed — so the strip
// survives every in-panel htmx interaction (those never re-render it). The tab
// styling lives in the vendored app.css (.lib-tab* classes), themed via the
// --civitai-* tokens so it works in both light and dark with no CDN.
func libraryTabStrip(active string) g.Node {
	return h.Div(
		g.Attr("role", "tablist"),
		h.Class("lib-tabs mt-1 mb-4 flex gap-6"),
		libraryTab("sources", "Install directories", active),
		libraryTab("files", "Model files", active),
	)
}

func libraryTab(id, label, active string) g.Node {
	attrs := []g.Node{
		h.Href("/library?tab=" + id),
		g.Attr("role", "tab"),
	}
	if id == active {
		attrs = append(attrs,
			h.Class("lib-tab lib-tab-active"),
			g.Attr("aria-selected", "true"),
			// aria-current="page" marks the active tab as the current page for AT,
			// on top of the visual accent-underline distinction.
			g.Attr("aria-current", "page"),
		)
	} else {
		attrs = append(attrs,
			h.Class("lib-tab"),
			g.Attr("aria-selected", "false"),
		)
	}
	attrs = append(attrs, g.Text(label))
	return h.A(attrs...)
}

// sourcesPanel is Tab A ("Install directories"): FINDING/SELECTING scan dirs
// only — the stable #discover-results container (discovery button + manual add +
// browser when idle; the live scanning card while a crawl runs) and the persisted
// #selected-dirs list (add/remove). It renders NO model-file scan UI. On a
// non-loopback bind the whole capability is disabled, so it shows only the gating
// note.
func sourcesPanel(csrf string, allowExtra bool, selectedDirs []string, discoverInitial g.Node) g.Node {
	if !allowExtra {
		return card(
			sectionTitle("Install directories"),
			h.P(h.Class("text-sm text-slate-400"),
				g.Text("Directory discovery and selection are disabled when the server is bound to a non-loopback address.")),
		)
	}
	if discoverInitial == nil {
		discoverInitial = discoverControls(csrf)
	}
	return card(
		sectionTitle("Install directories"),
		h.P(h.Class("mb-3 text-sm text-slate-400"),
			g.Text("Find and select the ComfyUI / Automatic1111 install directories to scan. Switch to “Model files” to scan them.")),
		// The STABLE poll/results container: only its innerHTML is ever swapped, so
		// the re-arming poller can never orphan a #discover-poll (the re-discover fix).
		h.Div(h.ID("discover-results"), discoverInitial),
		h.Div(
			h.Class("mt-4 space-y-2 border-t border-slate-800 pt-4"),
			h.Div(h.Class("text-xs font-medium text-slate-300"), g.Text("Selected scan directories")),
			h.Div(h.ID("selected-dirs"), selectedDirsList(selectedDirs, csrf)),
		),
	)
}

// filesPanel is Tab B ("Model files"): SCANNING the selected dirs for model
// files — an explicit "Scan for model files" button, the "Match against CivitAI"
// opt-in, and (after a scan) the Summary / Files-by-model / Deletion-candidate /
// quarantine results. It renders NO discovery UI. When no install directories
// have been selected yet (loopback bind), it shows an empty state pointing at Tab
// A rather than a bare scan button.
// scanInitial is the initial content of the STABLE #scan-results container (the
// idle library content, or the live scanning/terminal fragment when a scan is in
// flight); nil falls back to the idle library content. matchRemote pre-checks the
// persisted "Match against CivitAI" toggle.
func filesPanel(v libraryView, csrf string, allowExtra bool, selectedDirs []string, matchRemote bool, scanInitial g.Node) g.Node {
	// Gate EVERY scan affordance on ≥1 ADDED install directory: until the persisted
	// selection has at least one entry, Tab B shows an empty state pointing at Tab A
	// and renders NO scan button/form — regardless of the loopback/non-loopback bind
	// AND regardless of model_root contents. Trade-off (intended): a model_root that
	// already holds auto-downloaded files is not scannable until the user adds a scan
	// directory in Tab A. This mirrors Tab A's own CTA gating (scanForModelsCTA).
	if len(selectedDirs) == 0 {
		return card(
			sectionTitle("Model files"),
			alert("info", "No install directories selected yet",
				h.P(h.Class("mt-1 text-sm"),
					g.Text("Add install directories first (see the “Install directories” tab), then scan them for model files.")),
			),
		)
	}
	if scanInitial == nil {
		scanInitial = libraryContent(v, csrf)
	}
	return h.Div(
		h.Class("space-y-6"),
		card(
			sectionTitle("Model files"),
			modelScanForm(csrf, matchRemote),
		),
		// The STABLE poll/results container: only its innerHTML is ever swapped, so
		// the re-arming scan poller can never orphan a #scan-poll (mirrors
		// #discover-results). It bootstraps from the live scan job on reload.
		h.Div(h.ID("scan-results"), scanInitial),
	)
}

// modelScanForm renders Tab B's model-file scan form: the explicit "Scan for
// model files" submit and the opt-in "Match against CivitAI" checkbox. It carries
// NO scan_dir checkboxes — the dirs to scan are the persisted selection managed in
// Tab A (handleLibraryScan falls back to them when no checkboxes are submitted).
//
// The remote-match checkbox defaults ON (matchRemoteEnabled defaults true when
// unset): by default a web scan matches against CivitAI by hash so the library is
// identified. Matching sends each file's SHA256 to civitai.com's by-hash lookup —
// stated inline beneath the toggle. Unchecking it makes THIS and future scans run
// offline (local duplicate/broken analysis only); that choice persists.
func modelScanForm(csrf string, matchRemote bool) g.Node {
	// The toggle PERSISTS on change (POST /settings/match-remote, no swap) so it is
	// the single source of truth the Tab-A CTA also reads. A single checkbox posts
	// its value only when checked, so presence == enabled.
	cb := []g.Node{
		h.Type("checkbox"), h.Name("match_remote"), h.Value("true"),
		hx("post", "/settings/match-remote"),
		hx("trigger", "change"),
		hx("swap", "none"),
		csrfInline(csrf),
		h.Class("rounded border-slate-600 bg-slate-800 text-indigo-500"),
	}
	if matchRemote {
		cb = append(cb, g.Attr("checked"))
	}
	return h.Form(
		// Submitting starts the async streaming scan; the handler HX-Redirects to the
		// Model files tab. hx-target is only the fallback for a synchronous validation
		// error (rendered into #scan-results); on success the redirect supersedes it.
		hx("post", "/library/scan"),
		hx("target", "#scan-results"),
		hx("swap", "innerHTML"),
		h.Class("space-y-3"),
		csrfInput(csrf),
		h.Label(
			h.Class("flex items-center gap-2 text-xs text-slate-400"),
			h.Input(cb...),
			g.Text("Match against CivitAI (sends file hashes to civitai.com)"),
		),
		h.P(h.Class("text-xs text-slate-500"),
			g.Text("Matches your files against CivitAI by hash (sends file hashes to civitai.com). Uncheck to scan offline.")),
		btnPrimary(g.Text("Scan for model files")),
	)
}

// libraryContent is the fragment swapped after a scan: totals, per-model
// grouping, and the deletion-candidate table.
func libraryContent(v libraryView, csrf string) g.Node {
	matched, unmatched := splitMatchedUnmatched(v.Files)
	return h.Div(
		h.Class("space-y-6"),
		summaryBanner(v),
		card(
			sectionTitle("Summary"),
			h.Div(
				h.Class("grid grid-cols-2 gap-4 sm:grid-cols-4 text-sm"),
				stat("Files", strconv.Itoa(len(v.Files))),
				stat("Total size", humanBytes(v.TotalBytes)),
				stat("Candidates", strconv.Itoa(len(v.Candidates))),
				stat("Reclaimable", humanBytes(v.Reclaimable)),
			),
		),
		// MATCHED MODELS FIRST — enriched, lazy-loaded cards.
		matchedModelsSection(matched),
		// Unmatched / other files in a clearly-separated secondary section.
		otherFilesSection(unmatched),
		card(
			h.ID("deletion-candidates"),
			sectionTitle("Deletion candidates"),
			candidatesTable(v.Candidates, csrf),
			h.Div(h.ID("quarantine-preview"), h.Class("mt-3")),
		),
		// The shared lightbox + interaction scripts the model-card carousels reuse.
		// Included once here (the results fragment) so a lazy-loaded card's tiles can
		// open the lightbox and the prev/next buttons work. Offline/vendored only.
		lightboxOverlay(),
		modelPageScript(),
		libraryCarouselScript(),
	)
}

// matchedModelsSection renders the identified models at the TOP of the results
// as enriched, lazy-loaded cards (one per model), ordered by total local size
// descending so the biggest reclaimable footprints lead. Each card renders
// immediately as a placeholder and lazy-loads its name + carousel + details.
func matchedModelsSection(groups []fileGroup) g.Node {
	if len(groups) == 0 {
		return card(
			sectionTitle("Matched models"),
			h.P(h.Class("text-sm text-slate-500"),
				g.Text("No models identified yet. Enable “Match against CivitAI” and scan to identify your library.")),
		)
	}
	var cards []g.Node
	for _, gr := range groups {
		cards = append(cards, modelCardLazy(gr))
	}
	return card(
		sectionTitle(fmt.Sprintf("Matched models (%d)", len(groups))),
		h.Div(h.Class("grid gap-4 md:grid-cols-2"), g.Group(cards)),
	)
}

// otherFilesSection renders the unmatched (unidentified) files as a secondary,
// sortable table below the matched model cards. When everything matched it shows
// a reassuring note instead.
func otherFilesSection(unmatched []store.LocalFile) g.Node {
	if len(unmatched) == 0 {
		return card(
			sectionTitle("Other files"),
			h.P(h.Class("text-sm text-slate-500"), g.Text("Every scanned file was identified on CivitAI.")),
		)
	}
	return card(
		sectionTitle(fmt.Sprintf("Other files (%d unmatched)", len(unmatched))),
		libraryModelTable(unmatched),
	)
}

// splitMatchedUnmatched partitions scanned model files into matched-model groups
// (files carrying a CivitAI model id, grouped by model, ordered by total size
// desc) and the flat list of unmatched files.
func splitMatchedUnmatched(files []store.LocalFile) (matched []fileGroup, unmatched []store.LocalFile) {
	byID := map[int]*fileGroup{}
	var order []int
	for _, f := range files {
		if f.ModelID == nil {
			unmatched = append(unmatched, f)
			continue
		}
		id := *f.ModelID
		gr, ok := byID[id]
		if !ok {
			gr = &fileGroup{modelID: id}
			byID[id] = gr
			order = append(order, id)
		}
		gr.files = append(gr.files, f)
	}
	matched = make([]fileGroup, 0, len(order))
	for _, id := range order {
		matched = append(matched, *byID[id])
	}
	// Biggest total footprint first; ties broken by model id for determinism.
	sort.Slice(matched, func(a, b int) bool {
		sa, sb := groupBytes(matched[a]), groupBytes(matched[b])
		if sa != sb {
			return sa > sb
		}
		return matched[a].modelID < matched[b].modelID
	})
	return matched, unmatched
}

// groupBytes sums a group's file sizes.
func groupBytes(gr fileGroup) int64 {
	var total int64
	for _, f := range gr.files {
		total += f.SizeBytes
	}
	return total
}

// librarySummary is the post-scan roll-up the next-steps banner renders.
type librarySummary struct {
	ModelsIdentified int   // distinct matched model ids
	Unmatched        int   // model files not identified on CivitAI
	Duplicates       int   // redundant copies (duplicate + superseded candidates)
	DuplicateBytes   int64 // reclaimable bytes from those redundant copies
	Broken           int   // broken sidecars/partials
}

// summarizeLibrary derives the banner roll-up from a libraryView: distinct
// identified models, unidentified files, redundant (duplicate/superseded)
// copies + their reclaimable bytes, and broken files.
func summarizeLibrary(v libraryView) librarySummary {
	var s librarySummary
	models := map[int]bool{}
	for _, f := range v.Files {
		if f.ModelID != nil {
			models[*f.ModelID] = true
		} else {
			s.Unmatched++
		}
	}
	s.ModelsIdentified = len(models)
	for _, c := range v.Candidates {
		switch c.CandidateReason {
		case store.CandidateBroken:
			s.Broken++
		case store.CandidateDuplicate, store.CandidateSuperseded:
			s.Duplicates++
			s.DuplicateBytes += c.SizeBytes
		}
	}
	return s
}

// summaryBanner is the "what to do next" banner at the top of the Model-files
// results: an at-a-glance roll-up plus a clear primary action. When there are
// duplicates/broken files it offers a primary "Review & quarantine…" CTA (which
// scrolls to the candidates section) and a secondary link; when the library is
// clean it reassures instead. Theme-aware, civitai-styled.
func summaryBanner(v libraryView) g.Node {
	s := summarizeLibrary(v)

	// Clean state: nothing to act on — reassure rather than nag.
	if s.Duplicates == 0 && s.Broken == 0 {
		return alert("success", "Your library is clean",
			h.P(h.Class("mt-1 text-sm"),
				g.Text(fmt.Sprintf("No duplicates or broken files found — %d models identified · %d unmatched.",
					s.ModelsIdentified, s.Unmatched))),
		)
	}

	// Actionable roll-up: dot-separated counts.
	pieces := []g.Node{
		bannerStat(strconv.Itoa(s.ModelsIdentified), "models identified"),
	}
	if s.Duplicates > 0 {
		pieces = append(pieces, bannerSep(),
			bannerStat(strconv.Itoa(s.Duplicates),
				fmt.Sprintf("duplicates (reclaim %s)", humanBytes(s.DuplicateBytes))))
	}
	pieces = append(pieces, bannerSep(), bannerStat(strconv.Itoa(s.Unmatched), "unmatched"))
	if s.Broken > 0 {
		pieces = append(pieces, bannerSep(), bannerStat(strconv.Itoa(s.Broken), "broken"))
	}

	primaryLabel := "Review & quarantine duplicates"
	if s.Duplicates == 0 {
		primaryLabel = "Review broken files"
	}

	return card(
		h.Class("border-indigo-500/40"),
		h.Div(
			h.Class("flex flex-wrap items-center justify-between gap-4"),
			h.Div(
				h.Class("flex flex-wrap items-center gap-x-2 gap-y-1 text-sm text-slate-200"),
				g.Group(pieces),
			),
			h.Div(
				h.Class("flex items-center gap-3"),
				civButton("filled", "md", []g.Node{
					h.Type("button"),
					g.Attr("onclick",
						"var e=document.getElementById('deletion-candidates');if(e){e.scrollIntoView({behavior:'smooth'});}"),
				}, g.Text(primaryLabel)),
				h.A(h.Href("#deletion-candidates"),
					h.Class("text-sm text-indigo-300 hover:text-indigo-200 underline"),
					g.Text("See deletion candidates")),
			),
		),
	)
}

// bannerStat renders one "<value> <label>" chunk of the summary count line.
func bannerStat(value, label string) g.Node {
	return h.Span(
		h.Span(h.Class("font-semibold text-slate-100"), g.Text(value+" ")),
		h.Span(h.Class("text-slate-400"), g.Text(label)),
	)
}

// bannerSep is the muted dot separator between banner count chunks.
func bannerSep() g.Node {
	return h.Span(h.Class("text-slate-600"), g.Text("·"))
}

// File-size magnitude thresholds for the color-coded Size cell (see sizeClass).
// Documented tiers: <500MB muted, 500MB–2GB yellow, 2–6GB orange, >6GB red.
const (
	sizeTierMedium int64 = 500 * 1024 * 1024      // 500 MB
	sizeTierLarge  int64 = 2 * 1024 * 1024 * 1024 // 2 GB
	sizeTierHuge   int64 = 6 * 1024 * 1024 * 1024 // 6 GB
)

// sizeClass maps a byte size to its magnitude tier CSS class (defined in
// app.css, theme-aware via --civitai-* tokens): so a multi-GB checkpoint reads
// red at a glance and a small LoRA reads muted.
func sizeClass(b int64) string {
	switch {
	case b >= sizeTierHuge:
		return "cm-size-huge"
	case b >= sizeTierLarge:
		return "cm-size-large"
	case b >= sizeTierMedium:
		return "cm-size-medium"
	default:
		return "cm-size-small"
	}
}

// sizeCell renders a table Size cell: the humanized size colored by magnitude
// (sizeClass) and carrying the RAW byte count in data-sort-value so the
// client-side column sorter orders by bytes, not the humanized string.
func sizeCell(b int64) g.Node {
	return h.Td(
		h.Class("px-3 py-2 "+sizeClass(b)),
		dataAttr("sort-value", strconv.FormatInt(b, 10)),
		g.Text(humanBytes(b)),
	)
}

// sizeText renders a non-table size label colored by magnitude (used on the
// streamed scan-result cards, which are not tables).
func sizeText(b int64) g.Node {
	return h.Span(h.Class("shrink-0 text-xs "+sizeClass(b)), g.Text(humanBytes(b)))
}

func stat(label, value string) g.Node {
	return h.Div(
		h.Class("rounded-md border border-slate-800 bg-slate-900 p-3"),
		h.Div(h.Class("text-xs text-slate-400"), g.Text(label)),
		h.Div(h.Class("text-lg font-semibold text-slate-100"), g.Text(value)),
	)
}

func libraryModelTable(files []store.LocalFile) g.Node {
	if len(files) == 0 {
		return h.P(h.Class("text-sm text-slate-500"), g.Text("No files scanned yet. Click “Scan for model files”."))
	}
	groups := groupFilesByModel(files)
	var rows []g.Node
	for _, gr := range groups {
		for _, f := range gr.files {
			rows = append(rows, h.Tr(
				h.Class("border-b border-slate-800/60"),
				h.Td(h.Class("px-3 py-2 text-slate-400"), g.Text(modelLabel(f.ModelID))),
				h.Td(h.Class("px-3 py-2 text-slate-400"), g.Text(versionLabel(f.VersionID))),
				h.Td(h.Class("px-3 py-2"), statusBadge(f)),
				sizeCell(f.SizeBytes),
				h.Td(h.Class("px-3 py-2 text-slate-300 truncate max-w-lg"), g.Text(f.Path)),
			))
		}
	}
	return h.Div(
		h.Class("overflow-x-auto"),
		h.Table(
			h.Class("cm-sortable-table min-w-full text-sm"),
			h.THead(h.Tr(
				h.Class("text-left text-slate-400 border-b border-slate-800"),
				sortableTh("Model"), sortableTh("Version"), sortableTh("Status"),
				sortableTh("Size"), sortableTh("Path"),
			)),
			h.TBody(g.Group(rows)),
		),
		librarySortScript(),
	)
}

// sortableTh renders a click-to-sort table header: keyboard-operable (Enter or
// Space), announced to AT via aria-sort (the single source of truth the CSS
// indicator glyph also reads), and marked data-sortable so the inline sort
// script can find it. The size column carries data-sort-value on its cells, so
// that column sorts numerically by bytes (see librarySortScript).
func sortableTh(label string) g.Node {
	return h.Th(
		h.Class("px-3 py-2 font-medium"),
		dataFlag("sortable"),
		g.Attr("role", "columnheader"),
		g.Attr("aria-sort", "none"),
		g.Attr("tabindex", "0"),
		g.Attr("onclick", "cmSortTable(this)"),
		g.Attr("onkeydown", "if(event.key==='Enter'||event.key===' '){event.preventDefault();cmSortTable(this);}"),
		h.Span(g.Text(label)),
		h.Span(dataFlag("sort-ind-cell"), h.Class("cm-sort-ind")),
	)
}

// librarySortScript is the small, self-contained (vendored, no CDN) client-side
// column sorter. Clicking (or Enter/Space on) a data-sortable header sorts the
// loaded tbody rows in-browser and toggles asc/desc, updating aria-sort on the
// headers (which also drives the CSS direction glyph). A cell carrying
// data-sort-value is compared NUMERICALLY (so Size sorts by raw bytes, not the
// humanized string); otherwise a case-insensitive text compare is used. The
// function is (re)defined idempotently so it survives every htmx swap of the
// results fragment; it attaches no duplicate listeners (headers use inline
// onclick).
func librarySortScript() g.Node {
	const js = `
function cmSortTable(th){
  var table = th.closest('table');
  if(!table){ return; }
  var headers = Array.prototype.slice.call(table.querySelectorAll('th[data-sortable]'));
  var idx = headers.indexOf(th);
  if(idx < 0){ return; }
  var dir = th.getAttribute('aria-sort') === 'ascending' ? 'descending' : 'ascending';
  headers.forEach(function(h){ h.setAttribute('aria-sort', 'none'); });
  th.setAttribute('aria-sort', dir);
  var tbody = table.tBodies[0];
  if(!tbody){ return; }
  var mult = dir === 'ascending' ? 1 : -1;
  var rows = Array.prototype.slice.call(tbody.rows);
  rows.sort(function(a, b){
    var ca = a.cells[idx], cb = b.cells[idx];
    if(ca && cb && ca.hasAttribute('data-sort-value') && cb.hasAttribute('data-sort-value')){
      var na = parseFloat(ca.getAttribute('data-sort-value')) || 0;
      var nb = parseFloat(cb.getAttribute('data-sort-value')) || 0;
      return (na - nb) * mult;
    }
    var ta = ca ? ca.textContent.trim().toLowerCase() : '';
    var tb = cb ? cb.textContent.trim().toLowerCase() : '';
    if(ta < tb){ return -1 * mult; }
    if(ta > tb){ return 1 * mult; }
    return 0;
  });
  rows.forEach(function(r){ tbody.appendChild(r); });
}
`
	return h.Script(g.Raw(js))
}

// candidatesTable renders flagged candidates with per-row + bulk quarantine
// (dry-run preview). Every action POSTs with the CSRF token.
func candidatesTable(cands []store.LocalFile, csrf string) g.Node {
	if len(cands) == 0 {
		return h.P(h.Class("text-sm text-slate-500"), g.Text("No deletion candidates."))
	}
	var rows []g.Node
	for _, c := range cands {
		id := strconv.FormatInt(c.ID, 10)
		rows = append(rows, h.Tr(
			h.Class("border-b border-slate-800/60"),
			h.Td(h.Class("px-3 py-2"),
				h.Input(h.Type("checkbox"), h.Name("id"), h.Value(id),
					h.Class("rounded border-slate-600 bg-slate-800 text-indigo-500")),
			),
			h.Td(h.Class("px-3 py-2"), candidateBadge(c.CandidateReason)),
			sizeCell(c.SizeBytes),
			h.Td(h.Class("px-3 py-2 text-slate-300 truncate max-w-md"), g.Text(c.Path)),
			h.Td(h.Class("px-3 py-2 text-right"),
				civButton("subtle", "sm", []g.Node{
					h.Type("button"),
					hx("post", "/library/quarantine"),
					hx("vals", fmt.Sprintf(`{"id":"%s","apply":"false","csrf_token":"%s"}`, id, csrf)),
					hx("target", "#quarantine-preview"),
					hx("swap", "innerHTML"),
					h.StyleAttr("--civitai-color-primary:var(--civitai-color-warning)"),
				}, g.Text("Quarantine")),
			),
		))
	}
	return h.Form(
		hx("post", "/library/quarantine"),
		hx("vals", fmt.Sprintf(`{"apply":"false","csrf_token":"%s"}`, csrf)),
		hx("target", "#quarantine-preview"),
		hx("swap", "innerHTML"),
		csrfInput(csrf),
		h.Div(
			h.Class("overflow-x-auto"),
			h.Table(
				h.Class("min-w-full text-sm"),
				h.THead(h.Tr(
					h.Class("text-left text-slate-400 border-b border-slate-800"),
					th(""), th("Reason"), th("Size"), th("Path"), th(""),
				)),
				h.TBody(g.Group(rows)),
			),
		),
		h.Div(
			h.Class("mt-3"),
			civButton("light", "md", []g.Node{
				h.Type("submit"),
				h.StyleAttr("--civitai-color-primary:var(--civitai-color-warning)"),
			}, g.Text("Preview quarantine (selected)")),
		),
	)
}

// quarantinePreview renders the dry-run plan with a confirm-apply button, or the
// applied result.
func quarantinePreview(plan *library.QuarantinePlan, ids []int64, csrf string) g.Node {
	if len(plan.Moves) == 0 && len(plan.Skipped) == 0 {
		return h.P(h.Class("text-sm text-slate-500"), g.Text("Nothing to quarantine."))
	}
	var moveRows []g.Node
	for _, m := range plan.Moves {
		tag := m.Reason
		if m.IsSidecar {
			tag = "sidecar"
		}
		moveRows = append(moveRows, h.Li(
			h.Class("text-xs text-slate-300"),
			g.Text(tag+": "+m.OriginalPath),
		))
	}
	var skipRows []g.Node
	for _, sk := range plan.Skipped {
		skipRows = append(skipRows, h.Li(
			h.Class("text-xs text-rose-300"),
			g.Text("skipped "+sk.Path+": "+sk.Reason),
		))
	}

	if plan.Applied {
		return alert("success",
			fmt.Sprintf("Quarantined %d file(s) (%s) as batch #%d. Restore from the Trash page.",
				len(plan.Moves), humanBytes(plan.TotalBytes), plan.BatchID),
			h.Ul(h.Class("mt-2 space-y-1"), g.Group(moveRows)),
			h.Ul(h.Class("mt-2 space-y-1"), g.Group(skipRows)),
			h.Div(h.Class("mt-3"),
				civButton("outline", "sm", []g.Node{
					h.Type("button"),
					hx("get", "/library"),
					hx("target", "body"),
					hx("swap", "outerHTML"),
				}, g.Text("Refresh library")),
			),
		)
	}

	// Dry-run: show plan + confirm-apply button carrying the same ids.
	var idVals string
	for i, id := range ids {
		if i > 0 {
			idVals += ","
		}
		idVals += strconv.FormatInt(id, 10)
	}
	return alert("warning",
		fmt.Sprintf("Dry-run: would move %d file(s) (%s). Confirm to move them to the trash dir (reversible).",
			len(plan.Moves), humanBytes(plan.TotalBytes)),
		h.Ul(h.Class("mt-2 space-y-1"), g.Group(moveRows)),
		h.Ul(h.Class("mt-2 space-y-1"), g.Group(skipRows)),
		g.If(len(plan.Moves) > 0,
			h.Div(h.Class("mt-3"),
				civButton("filled", "md", []g.Node{
					h.Type("button"),
					hx("post", "/library/quarantine"),
					hx("vals", fmt.Sprintf(`{"ids":"%s","apply":"true","csrf_token":"%s"}`, idVals, csrf)),
					hx("target", "#quarantine-preview"),
					hx("swap", "innerHTML"),
					hx("confirm", "Move these files to the trash dir?"),
					h.StyleAttr("--civitai-color-primary:var(--civitai-color-warning)"),
				}, g.Text("Confirm quarantine")),
			),
		),
	)
}

// trashPage lists quarantine batches with restore controls.
func trashPage(batches []batchView, csrf, theme string) g.Node {
	return page("Trash", theme, csrf,
		card(
			sectionTitle("Quarantine trash"),
			h.Div(h.ID("trash-content"), trashTable(batches, csrf)),
		),
	)
}

type batchView struct {
	Batch store.QuarantineBatch
	Files int
}

func trashTable(batches []batchView, csrf string) g.Node {
	if len(batches) == 0 {
		return h.P(h.Class("text-sm text-slate-500"), g.Text("Trash is empty."))
	}
	var rows []g.Node
	for _, bv := range batches {
		b := bv.Batch
		id := strconv.FormatInt(b.ID, 10)
		var action g.Node
		if b.Restored() {
			action = badge("restored", "green")
		} else {
			action = civButton("subtle", "sm", []g.Node{
				h.Type("button"),
				hx("post", "/trash/"+id+"/restore"),
				hx("vals", fmt.Sprintf(`{"csrf_token":"%s"}`, csrf)),
				hx("target", "#trash-content"),
				hx("swap", "innerHTML"),
				hx("confirm", "Restore batch #"+id+" to its original locations?"),
				h.StyleAttr("--civitai-color-primary:var(--civitai-color-success)"),
			}, g.Text("Restore"))
		}
		rows = append(rows, h.Tr(
			h.ID("batch-"+id),
			h.Class("border-b border-slate-800/60"),
			h.Td(h.Class("px-3 py-2 text-slate-300"), g.Text("#"+id)),
			h.Td(h.Class("px-3 py-2 text-slate-400"), g.Text(humanTime(b.CreatedAt))),
			h.Td(h.Class("px-3 py-2"), g.If(b.Reason != "", badge(b.Reason, "amber"))),
			h.Td(h.Class("px-3 py-2 text-slate-400"), g.Text(strconv.Itoa(bv.Files))),
			h.Td(h.Class("px-3 py-2 text-right"), action),
		))
	}
	return h.Div(
		h.Class("overflow-x-auto"),
		h.Table(
			h.Class("min-w-full text-sm"),
			h.THead(h.Tr(
				h.Class("text-left text-slate-400 border-b border-slate-800"),
				th("Batch"), th("Created"), th("Reason"), th("Files"), th(""),
			)),
			h.TBody(g.Group(rows)),
		),
	)
}

// --- badges & grouping ---

func statusBadge(f store.LocalFile) g.Node {
	if f.IsCandidate() {
		return candidateBadge(f.CandidateReason)
	}
	switch f.Status {
	case store.LocalStatusMatched:
		return badge("matched", "green")
	case store.LocalStatusUnmatchedPending:
		return badge("pending", "blue")
	case store.LocalStatusBroken:
		return badge("broken", "red")
	default:
		return badge("unmatched", "slate")
	}
}

func candidateBadge(reason string) g.Node {
	switch reason {
	case store.CandidateDuplicate:
		return badge("duplicate", "blue")
	case store.CandidateBroken:
		return badge("broken", "amber")
	default:
		return badge("superseded", "amber")
	}
}

type fileGroup struct {
	modelID int
	files   []store.LocalFile
}

func groupFilesByModel(files []store.LocalFile) []fileGroup {
	byID := map[int]*fileGroup{}
	var order []int
	for _, f := range files {
		id := 0
		if f.ModelID != nil {
			id = *f.ModelID
		}
		gr, ok := byID[id]
		if !ok {
			gr = &fileGroup{modelID: id}
			byID[id] = gr
			order = append(order, id)
		}
		gr.files = append(gr.files, f)
	}
	sort.Ints(order)
	out := make([]fileGroup, 0, len(order))
	for _, id := range order {
		out = append(out, *byID[id])
	}
	return out
}

func modelLabel(p *int) string {
	if p == nil {
		return "—"
	}
	return strconv.Itoa(*p)
}

func versionLabel(p *int) string {
	if p == nil {
		return "—"
	}
	return "v" + strconv.Itoa(*p)
}
