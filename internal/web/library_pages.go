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

// libraryPage is the full Library page. allowExtra gates the arbitrary
// extra-scan-path controls (discovery, the directory browser, and the selected
// -dirs list): they are rendered only on a loopback bind (see
// Server.extraPathsAllowed), so a network-exposed server never even offers the
// remote arbitrary-path walk control. selectedDirs pre-fills the persisted
// selection.
func libraryPage(v libraryView, csrf string, allowExtra bool, selectedDirs []string) g.Node {
	return page("Library",
		card(
			sectionTitle("Library"),
			scanForm(csrf, allowExtra, selectedDirs),
			h.Div(h.ID("scan-spinner"), h.Class("htmx-indicator text-xs text-slate-400 mt-1"), g.Text("Scanning…")),
		),
		h.Div(h.ID("library-content"), libraryContent(v, csrf)),
	)
}

// scanForm renders the scan controls. When allowExtra is true (loopback bind
// only) it renders the rich extra-directory selector: the persisted selection as
// pre-checked checkboxes, an auto-discovery button, a server-side directory
// browser, and the remote-match opt-in. The selected dirs are unioned with
// model_root so cross-directory duplicates outside model_root become visible —
// the same reach as the CLI `scan --path`. The form carries the CSRF token.
//
// The remote-match checkbox is OPT-IN: by default a web scan runs offline (local
// duplicate/broken analysis only) and does NOT send file SHA256 hashes to
// CivitAI's by-hash lookup. Ticking it enables CivitAI matching for this scan.
func scanForm(csrf string, allowExtra bool, selectedDirs []string) g.Node {
	children := []g.Node{
		hx("post", "/library/scan"),
		hx("target", "#library-content"),
		hx("swap", "innerHTML"),
		hx("indicator", "#scan-spinner"),
		h.Class("mt-3 space-y-3"),
		csrfInput(csrf),
	}
	if allowExtra {
		children = append(children,
			h.Div(
				h.Class("space-y-2 rounded-md border border-slate-800 bg-slate-900/60 p-3"),
				h.Div(h.Class("text-xs font-medium text-slate-300"), g.Text("Extra scan directories")),
				h.Div(h.ID("selected-dirs"), selectedDirsList(selectedDirs, csrf)),
				h.Div(
					h.Class("flex flex-wrap items-center gap-2"),
					h.Button(
						h.Type("button"),
						hx("post", "/library/discover"),
						hx("target", "#discover-results"),
						hx("swap", "innerHTML"),
						hx("indicator", "#discover-spinner"),
						csrfInline(csrf),
						h.Class("rounded-md border border-slate-700 bg-slate-800 px-3 py-1.5 text-sm text-slate-200 hover:bg-slate-700"),
						g.Text("Discover installs"),
					),
					h.Span(h.ID("discover-spinner"), h.Class("htmx-indicator text-xs text-slate-400"), g.Text("Searching…")),
				),
				h.Div(h.ID("discover-results")),
				directoryBrowser(csrf),
			),
			h.Label(
				h.Class("flex items-center gap-2 text-xs text-slate-400"),
				h.Input(h.Type("checkbox"), h.Name("match_remote"), h.Value("true"),
					h.Class("rounded border-slate-600 bg-slate-800 text-indigo-500")),
				g.Text("Match against CivitAI (sends file hashes to civitai.com)"),
			),
		)
	}
	children = append(children,
		h.Button(
			h.Type("submit"),
			h.Class("rounded-md bg-indigo-600 px-3 py-1.5 text-sm font-medium text-white hover:bg-indigo-500"),
			g.Text("Scan selected"),
		),
	)
	return h.Form(children...)
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
		return h.P(h.Class("text-sm text-slate-500"), g.Text("No files scanned yet. Click “Scan now”."))
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
				h.Button(
					h.Type("button"),
					hx("post", "/library/quarantine"),
					hx("vals", fmt.Sprintf(`{"id":"%s","apply":"false","csrf_token":"%s"}`, id, csrf)),
					hx("target", "#quarantine-preview"),
					hx("swap", "innerHTML"),
					h.Class("text-xs text-amber-400 hover:text-amber-300"),
					g.Text("Quarantine"),
				),
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
			h.Button(
				h.Type("submit"),
				h.Class("rounded-md border border-amber-700 bg-amber-900 px-3 py-1.5 text-sm text-amber-100 hover:bg-amber-800"),
				g.Text("Preview quarantine (selected)"),
			),
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
		return h.Div(
			h.Class("rounded-md border border-emerald-800 bg-emerald-950 p-3"),
			h.P(h.Class("text-sm text-emerald-200"),
				g.Text(fmt.Sprintf("Quarantined %d file(s) (%s) as batch #%d. Restore from the Trash page.",
					len(plan.Moves), humanBytes(plan.TotalBytes), plan.BatchID))),
			h.Ul(h.Class("mt-2 space-y-1"), g.Group(moveRows)),
			h.Ul(h.Class("mt-2 space-y-1"), g.Group(skipRows)),
			h.Button(
				hx("get", "/library"),
				hx("target", "body"),
				hx("swap", "outerHTML"),
				h.Class("mt-3 rounded-md border border-slate-700 bg-slate-800 px-3 py-1.5 text-sm text-slate-200 hover:bg-slate-700"),
				g.Text("Refresh library"),
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
	return h.Div(
		h.Class("rounded-md border border-amber-800 bg-amber-950 p-3"),
		h.P(h.Class("text-sm text-amber-100"),
			g.Text(fmt.Sprintf("Dry-run: would move %d file(s) (%s). Confirm to move them to the trash dir (reversible).",
				len(plan.Moves), humanBytes(plan.TotalBytes)))),
		h.Ul(h.Class("mt-2 space-y-1"), g.Group(moveRows)),
		h.Ul(h.Class("mt-2 space-y-1"), g.Group(skipRows)),
		g.If(len(plan.Moves) > 0,
			h.Button(
				h.Type("button"),
				hx("post", "/library/quarantine"),
				hx("vals", fmt.Sprintf(`{"ids":"%s","apply":"true","csrf_token":"%s"}`, idVals, csrf)),
				hx("target", "#quarantine-preview"),
				hx("swap", "innerHTML"),
				hx("confirm", "Move these files to the trash dir?"),
				h.Class("mt-3 rounded-md bg-amber-600 px-3 py-1.5 text-sm font-medium text-white hover:bg-amber-500"),
				g.Text("Confirm quarantine"),
			),
		),
	)
}

// trashPage lists quarantine batches with restore controls.
func trashPage(batches []batchView, csrf string) g.Node {
	return page("Trash",
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
			action = h.Button(
				hx("post", "/trash/"+id+"/restore"),
				hx("vals", fmt.Sprintf(`{"csrf_token":"%s"}`, csrf)),
				hx("target", "#trash-content"),
				hx("swap", "innerHTML"),
				hx("confirm", "Restore batch #"+id+" to its original locations?"),
				h.Class("text-xs text-emerald-400 hover:text-emerald-300"),
				g.Text("Restore"),
			)
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
		return badge("unmatched", "amber")
	}
}

func candidateBadge(reason string) g.Node {
	switch reason {
	case store.CandidateDuplicate:
		return badge("duplicate", "amber")
	case store.CandidateBroken:
		return badge("broken", "red")
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
