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

// libraryPage is the full Library page. allowExtra gates the arbitrary
// extra-scan-path controls (discovery, the directory browser, and the selected
// -dirs list): they are rendered only on a loopback bind (see
// Server.extraPathsAllowed), so a network-exposed server never even offers the
// remote arbitrary-path walk control. selectedDirs pre-fills the persisted
// selection.
func libraryPage(v libraryView, csrf string, allowExtra bool, selectedDirs []string, theme string) g.Node {
	return page("Library", theme, csrf,
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
					civButton("outline", "sm", []g.Node{
						h.Type("button"),
						hx("post", "/library/discover"),
						hx("target", "#discover-results"),
						hx("swap", "innerHTML"),
						hx("indicator", "#discover-spinner"),
						// Disable the button for the duration of the request so it
						// cannot be re-clicked mid-crawl and the user sees it is busy.
						hx("disabled-elt", "this"),
						csrfInline(csrf),
					}, g.Text("Discover installs")),
					h.Span(h.ID("discover-spinner"),
						h.Class("htmx-indicator inline-flex items-center gap-1 text-xs text-slate-400"),
						spinnerGlyph(),
						g.Text("Scanning your system for ComfyUI / Automatic1111 installs… (up to a few seconds)")),
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
		btnPrimary(g.Text("Scan selected")),
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
