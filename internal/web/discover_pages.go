package web

import (
	"fmt"
	"path/filepath"
	"strconv"

	"github.com/ZacxDev/civitai-manager/internal/library"
	g "maragu.dev/gomponents"
	h "maragu.dev/gomponents/html"
)

// parentDir returns path's parent, or "" when path is already the filesystem
// root or a blocked system directory (so the browser never offers a ".." up into
// a refused location).
func parentDir(path string) string {
	parent := filepath.Dir(path)
	if parent == path || parent == "" {
		return ""
	}
	if library.BlockedForBrowse(parent) {
		return ""
	}
	return parent
}

// jsString renders s as a safe JavaScript string literal for an inline handler.
func jsString(s string) string { return strconv.Quote(s) }

// csrfInline attaches the CSRF token to an htmx-driven button via hx-vals, so a
// control that issues its own POST (outside a submitted form, or alongside one)
// always carries a valid token.
func csrfInline(csrf string) g.Node {
	return hx("vals", fmt.Sprintf(`{"csrf_token":"%s"}`, csrf))
}

// selectedDirsList renders the persisted extra scan directories as pre-checked
// checkboxes (name "scan_dir"), each with a remove control. An empty selection
// shows a hint. This fragment is swapped in place after add/remove.
func selectedDirsList(dirs []string, csrf string) g.Node {
	if len(dirs) == 0 {
		return h.P(h.Class("text-xs text-slate-500"),
			g.Text("No extra directories selected. Discover installs or browse to add one."))
	}
	var rows []g.Node
	for _, d := range dirs {
		rows = append(rows, h.Label(
			h.Class("flex items-center justify-between gap-2 rounded border border-slate-800 bg-slate-900 px-2 py-1"),
			h.Div(
				h.Class("flex items-center gap-2 min-w-0"),
				h.Input(h.Type("checkbox"), h.Name("scan_dir"), h.Value(d), g.Attr("checked"),
					h.Class("rounded border-slate-600 bg-slate-800 text-indigo-500")),
				h.Span(h.Class("truncate text-xs text-slate-300"), g.Text(d)),
			),
			h.Button(
				h.Type("button"),
				hx("post", "/library/scan-dirs/remove"),
				hx("vals", fmt.Sprintf(`{"path":%q,"csrf_token":%q}`, d, csrf)),
				hx("target", "#selected-dirs"),
				hx("swap", "innerHTML"),
				h.Class("shrink-0 text-xs text-rose-400 hover:text-rose-300"),
				g.Text("remove"),
			),
		))
	}
	return h.Div(h.Class("space-y-1"), g.Group(rows))
}

// discoverResults renders the auto-discovery candidates: each install as a card
// with a type badge, model-dir count, git branch + dirty indicator, confidence,
// and an "Add" button that persists it into the selection. selected marks
// installs already in the persisted set as added.
func discoverResults(installs []library.Install, selected []string, truncated bool, csrf string) g.Node {
	selSet := map[string]bool{}
	for _, s := range selected {
		selSet[s] = true
	}
	if len(installs) == 0 {
		return h.P(h.Class("text-xs text-slate-500 mt-2"),
			g.Text("No ComfyUI or Automatic1111/Forge installs found in the usual locations."))
	}
	var cards []g.Node
	for _, in := range installs {
		cards = append(cards, discoverCard(in, selSet[in.Path], csrf))
	}
	nodes := []g.Node{h.Class("mt-2 space-y-2"), g.Group(cards)}
	if truncated {
		nodes = append(nodes, h.P(h.Class("text-xs text-amber-400"),
			g.Text("Search stopped at the time budget; results may be incomplete.")))
	}
	return h.Div(nodes...)
}

func discoverCard(in library.Install, added bool, csrf string) g.Node {
	kindLabel := "ComfyUI"
	kindVariant := "indigo"
	if in.Kind == library.KindA1111 {
		kindLabel, kindVariant = "A1111 / Forge", "blue"
	}
	confVariant := "amber"
	if in.Confidence == library.ConfidenceHigh {
		confVariant = "green"
	}

	meta := []g.Node{
		badge(kindLabel, kindVariant),
		badge(strconv.Itoa(len(in.ModelDirs))+" model dirs", "slate"),
		badge(in.Confidence+" confidence", confVariant),
	}
	if in.Git != nil {
		branch := in.Git.Branch
		if branch == "" {
			branch = "git"
		}
		meta = append(meta, badge("⎇ "+branch, "slate"))
		if in.Git.Dirty {
			meta = append(meta, badge("dirty", "red"))
		}
	}

	var action g.Node
	if added {
		action = h.Span(h.Class("text-xs text-emerald-400"), g.Text("added ✓"))
	} else {
		action = h.Button(
			h.Type("button"),
			hx("post", "/library/scan-dirs/add"),
			hx("vals", fmt.Sprintf(`{"path":%q,"csrf_token":%q}`, in.Path, csrf)),
			hx("target", "#selected-dirs"),
			hx("swap", "innerHTML"),
			h.Class("shrink-0 rounded-md border border-indigo-700 bg-indigo-900/40 px-2 py-1 text-xs text-indigo-200 hover:bg-indigo-900"),
			g.Text("Add"),
		)
	}

	return h.Div(
		h.Class("flex items-center justify-between gap-3 rounded-md border border-slate-800 bg-slate-900 p-2"),
		h.Div(
			h.Class("min-w-0 space-y-1"),
			h.Div(h.Class("truncate text-sm text-slate-200"), g.Text(in.Path)),
			h.Div(h.Class("flex flex-wrap items-center gap-1"), g.Group(meta)),
		),
		action,
	)
}

// directoryBrowser renders the server-side directory-browser control: a path
// input, a "Browse" button, and the results target.
func directoryBrowser(csrf string) g.Node {
	return h.Div(
		h.Class("space-y-2 border-t border-slate-800 pt-2"),
		h.Div(h.Class("text-xs font-medium text-slate-400"), g.Text("Browse server directories")),
		h.Div(
			h.Class("flex items-center gap-2"),
			h.Input(
				h.Type("text"), h.Name("browse_path"), h.ID("browse-path"),
				h.Placeholder("Leave blank for your home directory"),
				h.Class("flex-1 rounded-md border border-slate-700 bg-slate-900 px-2 py-1 text-xs text-slate-200 placeholder:text-slate-600"),
			),
			h.Button(
				h.Type("button"),
				hx("post", "/library/browse"),
				hx("include", "#browse-path"),
				hx("vals", fmt.Sprintf(`{"csrf_token":%q}`, csrf)),
				hx("target", "#browse-results"),
				hx("swap", "innerHTML"),
				h.Class("rounded-md border border-slate-700 bg-slate-800 px-3 py-1 text-xs text-slate-200 hover:bg-slate-700"),
				g.Text("Browse"),
			),
		),
		h.Div(h.ID("browse-results")),
	)
}

// browseResults renders one directory listing: an "add this directory" control
// (when it is an addable scan root) plus each immediate subdirectory as a
// drill-in button. The path input is updated so navigation is one click.
func browseResults(path string, dirs []browseEntry, canAdd bool, csrf string) g.Node {
	header := []g.Node{
		h.Class("flex items-center justify-between gap-2"),
		h.Span(h.Class("truncate text-xs text-slate-300"), g.Text(path)),
	}
	if canAdd {
		header = append(header, h.Button(
			h.Type("button"),
			hx("post", "/library/scan-dirs/add"),
			hx("vals", fmt.Sprintf(`{"path":%q,"csrf_token":%q}`, path, csrf)),
			hx("target", "#selected-dirs"),
			hx("swap", "innerHTML"),
			h.Class("shrink-0 rounded-md border border-indigo-700 bg-indigo-900/40 px-2 py-1 text-xs text-indigo-200 hover:bg-indigo-900"),
			g.Text("Add this directory"),
		))
	}

	var items []g.Node
	if parent := parentDir(path); parent != "" {
		items = append(items, browseDirButton("..", parent, csrf))
	}
	for _, d := range dirs {
		items = append(items, browseDirButton(d.Name, d.Path, csrf))
	}
	if len(dirs) == 0 {
		items = append(items, h.Li(h.Class("text-xs text-slate-500"), g.Text("(no subdirectories)")))
	}

	return h.Div(
		h.Class("mt-2 space-y-2 rounded-md border border-slate-800 bg-slate-900 p-2"),
		h.Div(header...),
		h.Ul(h.Class("max-h-56 space-y-1 overflow-y-auto"), g.Group(items)),
	)
}

// browseDirButton is one navigable subdirectory row: clicking it re-browses into
// that directory. It also writes the path back into the browse input via a small
// hx-on handler so the current location stays visible.
func browseDirButton(label, path, csrf string) g.Node {
	return h.Li(
		h.Button(
			h.Type("button"),
			hx("post", "/library/browse"),
			hx("vals", fmt.Sprintf(`{"path":%q,"csrf_token":%q}`, path, csrf)),
			hx("target", "#browse-results"),
			hx("swap", "innerHTML"),
			g.Attr("hx-on:click", "document.getElementById('browse-path').value="+jsString(path)),
			h.Class("w-full truncate rounded px-2 py-1 text-left text-xs text-slate-300 hover:bg-slate-800"),
			g.Text("📁 "+label),
		),
	)
}
