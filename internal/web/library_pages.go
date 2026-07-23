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

// libraryTabStrip renders the two-tab navigation. The active tab is a plain
// full-page navigation (?tab=…) styled with the civitai button component, so it
// survives every in-panel htmx interaction (those never re-render the strip).
func libraryTabStrip(active string) g.Node {
	return h.Div(
		g.Attr("role", "tablist"),
		h.Class("mt-1 mb-4 flex gap-2 border-b border-slate-800 pb-3"),
		libraryTab("sources", "Install directories", active),
		libraryTab("files", "Model files", active),
	)
}

func libraryTab(id, label, active string) g.Node {
	variant, selected := "subtle", "false"
	if id == active {
		variant, selected = "filled", "true"
	}
	return civLinkButton(variant, "md", "/library?tab="+id, []g.Node{
		g.Attr("role", "tab"),
		g.Attr("aria-selected", selected),
	}, g.Text(label))
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
	if allowExtra && len(selectedDirs) == 0 {
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
// The remote-match checkbox is OPT-IN: by default a web scan runs offline (local
// duplicate/broken analysis only) and does NOT send file SHA256 hashes to
// CivitAI's by-hash lookup. Ticking it enables CivitAI matching for this scan.
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
		btnPrimary(g.Text("Scan for model files")),
	)
}

// libraryContent is the fragment swapped after a scan: totals, per-model
// grouping, and the deletion-candidate table.
func libraryContent(v libraryView, csrf string) g.Node {
	return h.Div(
		h.Class("space-y-6"),
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
		card(
			sectionTitle("Files by model"),
			libraryModelTable(v.Files),
		),
		card(
			sectionTitle("Deletion candidates"),
			candidatesTable(v.Candidates, csrf),
			h.Div(h.ID("quarantine-preview"), h.Class("mt-3")),
		),
	)
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
				h.Td(h.Class("px-3 py-2 text-slate-400"), g.Text(humanBytes(f.SizeBytes))),
				h.Td(h.Class("px-3 py-2 text-slate-300 truncate max-w-lg"), g.Text(f.Path)),
			))
		}
	}
	return h.Div(
		h.Class("overflow-x-auto"),
		h.Table(
			h.Class("min-w-full text-sm"),
			h.THead(h.Tr(
				h.Class("text-left text-slate-400 border-b border-slate-800"),
				th("Model"), th("Version"), th("Status"), th("Size"), th("Path"),
			)),
			h.TBody(g.Group(rows)),
		),
	)
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
			h.Td(h.Class("px-3 py-2 text-slate-400"), g.Text(humanBytes(c.SizeBytes))),
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
